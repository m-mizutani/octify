package readstate_test

import (
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/m-mizutani/gt"
	"github.com/m-mizutani/octify/pkg/domain/model"
	"github.com/m-mizutani/octify/pkg/domain/types"
	"github.com/m-mizutani/octify/pkg/infra/readstate"
)

var base = time.Date(2026, 8, 25, 9, 0, 0, 0, time.UTC)

func newStore(t *testing.T) *readstate.Store {
	t.Helper()
	return readstate.New(filepath.Join(t.TempDir(), "read-state.json"), "github.com")
}

func override(state model.ReadState, at, subjectUpdatedAt time.Time) model.ReadOverride {
	return model.ReadOverride{State: state, At: at, SubjectUpdatedAt: subjectUpdatedAt}
}

func notification(id types.ThreadID, updatedAt time.Time) model.Notification {
	return model.Notification{ID: id, UpdatedAt: updatedAt}
}

func TestStorePutAndLookup(t *testing.T) {
	store := newStore(t)
	gt.NoError(t, store.Load())

	ov := override(model.ReadStateRead, base, base)
	gt.NoError(t, store.Put(map[types.ThreadID]model.ReadOverride{"1": ov}))

	got, ok := store.Lookup("1")
	gt.True(t, ok)
	gt.Equal(t, got, ov)

	_, ok = store.Lookup("missing")
	gt.False(t, ok)
	gt.Equal(t, store.Len(), 1)
}

func TestStorePutWritesOnce(t *testing.T) {
	store := newStore(t)
	gt.NoError(t, store.Load())

	entries := make(map[types.ThreadID]model.ReadOverride, 200)
	for i := range 200 {
		entries[types.ThreadID(string(rune('a'+i%26))+string(rune('0'+i/26)))] =
			override(model.ReadStateRead, base, base)
	}

	gt.NoError(t, store.Put(entries))
	gt.Equal(t, store.WriteCount(), 1)
	gt.Equal(t, store.Len(), len(entries))
}

func TestStorePutEmptyDoesNotWrite(t *testing.T) {
	store := newStore(t)
	gt.NoError(t, store.Load())

	gt.NoError(t, store.Put(nil))
	gt.NoError(t, store.Put(map[types.ThreadID]model.ReadOverride{}))
	gt.Equal(t, store.WriteCount(), 0)
}

func TestStoreRemove(t *testing.T) {
	store := newStore(t)
	gt.NoError(t, store.Load())
	gt.NoError(t, store.Put(map[types.ThreadID]model.ReadOverride{
		"1": override(model.ReadStateRead, base, base),
		"2": override(model.ReadStateRead, base, base),
	}))
	writesAfterPut := store.WriteCount()

	gt.NoError(t, store.Remove("1"))
	_, ok := store.Lookup("1")
	gt.False(t, ok)
	gt.Equal(t, store.WriteCount(), writesAfterPut+1)

	// Removing something that is not there must not cost a write.
	gt.NoError(t, store.Remove("absent"))
	gt.Equal(t, store.WriteCount(), writesAfterPut+1)
}

func TestReconcileDropsStaleRecords(t *testing.T) {
	store := newStore(t)
	gt.NoError(t, store.Load())
	gt.NoError(t, store.Put(map[types.ThreadID]model.ReadOverride{
		"stale":     override(model.ReadStateRead, base, base),
		"unchanged": override(model.ReadStateRead, base, base),
	}))

	removed := gt.R1(store.Reconcile([]model.Notification{
		notification("stale", base.Add(time.Minute)),
		notification("unchanged", base),
	}, readstate.ReconcileOption{Now: base})).NoError(t)

	gt.Equal(t, removed, 1)
	_, ok := store.Lookup("stale")
	gt.False(t, ok)
	_, ok = store.Lookup("unchanged")
	gt.True(t, ok)
}

func TestReconcileDropsExpiredRecords(t *testing.T) {
	store := newStore(t)
	gt.NoError(t, store.Load())
	gt.NoError(t, store.Put(map[types.ThreadID]model.ReadOverride{
		"gone-long-ago": override(model.ReadStateRead, base.Add(-40*24*time.Hour), base),
		"gone-recently": override(model.ReadStateRead, base.Add(-1*time.Hour), base),
		"still-listed":  override(model.ReadStateRead, base.Add(-40*24*time.Hour), base),
	}))

	removed := gt.R1(store.Reconcile(
		[]model.Notification{notification("still-listed", base)},
		readstate.ReconcileOption{PruneMissing: true, TTL: 30 * 24 * time.Hour, Now: base},
	)).NoError(t)

	gt.Equal(t, removed, 1)
	_, ok := store.Lookup("gone-long-ago")
	gt.False(t, ok)
	_, ok = store.Lookup("gone-recently")
	gt.True(t, ok)
	// A record whose notification is still listed survives regardless of age.
	_, ok = store.Lookup("still-listed")
	gt.True(t, ok)
}

func TestReconcileSkipsPruningWhenTruncated(t *testing.T) {
	store := newStore(t)
	gt.NoError(t, store.Load())
	gt.NoError(t, store.Put(map[types.ThreadID]model.ReadOverride{
		"missing": override(model.ReadStateRead, base.Add(-40*24*time.Hour), base),
		"stale":   override(model.ReadStateRead, base, base),
	}))

	removed := gt.R1(store.Reconcile(
		[]model.Notification{notification("stale", base.Add(time.Minute))},
		readstate.ReconcileOption{PruneMissing: false, TTL: 30 * 24 * time.Hour, Now: base},
	)).NoError(t)

	// The stale check still runs; only the "not in the list" check is suppressed.
	gt.Equal(t, removed, 1)
	_, ok := store.Lookup("missing")
	gt.True(t, ok)
	_, ok = store.Lookup("stale")
	gt.False(t, ok)
}

// The list is drawn from the terminal loop while polling and archiving mutate
// the records from their own goroutines. An unguarded map would be a fatal
// runtime error there, so this pins the guarantee under -race.
func TestStoreIsSafeForConcurrentUse(t *testing.T) {
	store := newStore(t)
	gt.NoError(t, store.Load())

	const workers = 8
	var wg sync.WaitGroup

	for w := range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			id := types.ThreadID("thread-" + string(rune('a'+w)))
			for range 50 {
				_ = store.Put(map[types.ThreadID]model.ReadOverride{
					id: override(model.ReadStateRead, base, base),
				})
				_, _ = store.Lookup(id)
				_, _ = store.Reconcile(
					[]model.Notification{notification(id, base)},
					readstate.ReconcileOption{PruneMissing: true, TTL: time.Hour, Now: base},
				)
				_ = store.Remove(id)
				_ = store.Len()
			}
		}()
	}

	wg.Wait()
}

func TestReconcileWithoutChangesDoesNotWrite(t *testing.T) {
	store := newStore(t)
	gt.NoError(t, store.Load())
	gt.NoError(t, store.Put(map[types.ThreadID]model.ReadOverride{
		"1": override(model.ReadStateRead, base, base),
	}))
	writesAfterPut := store.WriteCount()

	removed := gt.R1(store.Reconcile(
		[]model.Notification{notification("1", base)},
		readstate.ReconcileOption{PruneMissing: true, TTL: time.Hour, Now: base},
	)).NoError(t)

	gt.Equal(t, removed, 0)
	gt.Equal(t, store.WriteCount(), writesAfterPut)
}
