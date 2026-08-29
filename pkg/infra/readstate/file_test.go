package readstate_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/m-mizutani/gt"
	"github.com/m-mizutani/octify/pkg/domain/model"
	"github.com/m-mizutani/octify/pkg/domain/types"
	"github.com/m-mizutani/octify/pkg/infra/readstate"
)

func TestStoreRoundTripAcrossInstances(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "read-state.json")

	first := readstate.New(path, "github.com")
	gt.NoError(t, first.Load())
	gt.NoError(t, first.Put(map[types.ThreadID]model.ReadOverride{
		"1": override(model.ReadStateRead, base, base.Add(-time.Minute)),
		"2": override(model.ReadStateUnread, base.Add(time.Second), base),
	}))

	second := readstate.New(path, "github.com")
	gt.NoError(t, second.Load())
	gt.Equal(t, second.Len(), 2)

	got1, ok := second.Lookup("1")
	gt.True(t, ok)
	gt.Equal(t, got1.State, model.ReadStateRead)
	gt.True(t, got1.At.Equal(base))
	gt.True(t, got1.SubjectUpdatedAt.Equal(base.Add(-time.Minute)))

	got2, ok := second.Lookup("2")
	gt.True(t, ok)
	gt.Equal(t, got2.State, model.ReadStateUnread)
	gt.True(t, got2.At.Equal(base.Add(time.Second)))
}

func TestStoreLoadMissingFile(t *testing.T) {
	store := readstate.New(filepath.Join(t.TempDir(), "absent.json"), "github.com")

	// A first run has no file; that is an empty set, not a failure.
	gt.NoError(t, store.Load())
	gt.Equal(t, store.Len(), 0)
}

func TestStoreFilePermission(t *testing.T) {
	// Use a directory octify has to create itself, so its mode can be checked.
	dir := filepath.Join(t.TempDir(), "created-by-octify")
	path := filepath.Join(dir, "read-state.json")
	store := readstate.New(path, "github.com")
	gt.NoError(t, store.Load())
	gt.NoError(t, store.Put(map[types.ThreadID]model.ReadOverride{
		"1": override(model.ReadStateRead, base, base),
	}))

	info := gt.R1(os.Stat(path)).NoError(t)
	gt.Equal(t, info.Mode().Perm(), os.FileMode(0o600))

	dirInfo := gt.R1(os.Stat(dir)).NoError(t)
	gt.Equal(t, dirInfo.Mode().Perm(), os.FileMode(0o700))

	// No temporary file may survive the rename.
	entries := gt.R1(os.ReadDir(dir)).NoError(t)
	gt.A(t, entries).Length(1)
	gt.Equal(t, entries[0].Name(), "read-state.json")
}

func TestStoreLoadRejectsBrokenFile(t *testing.T) {
	testCases := map[string]struct {
		content     string
		wantErr     error
		wantSummary string
	}{
		"not json": {
			content:     `{"version": 1,`,
			wantErr:     readstate.ErrInvalidState,
			wantSummary: "is broken",
		},
		"newer version": {
			content:     `{"version": 99, "overrides": {}}`,
			wantErr:     readstate.ErrUnsupportedStateVersion,
			wantSummary: "written by a newer octify",
		},
		"missing version field": {
			// Version 0 is not "newer": telling the user to update octify would
			// not resolve anything.
			content:     `{"overrides": {}}`,
			wantErr:     readstate.ErrUnsupportedStateVersion,
			wantSummary: "not in a format this octify understands",
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "read-state.json")
			gt.NoError(t, os.WriteFile(path, []byte(tc.content), 0o600))

			store := readstate.New(path, "github.com")
			err := store.Load()
			gt.Error(t, err).Is(tc.wantErr)

			msg, ok := model.UserMessageOf(err)
			gt.True(t, ok)
			gt.S(t, msg.Summary).Contains(tc.wantSummary)

			// A file octify cannot read must survive untouched.
			after := gt.R1(os.ReadFile(path)).NoError(t)
			gt.Equal(t, string(after), tc.content)
		})
	}
}

func TestStoreLoadSkipsUnusableEntries(t *testing.T) {
	path := filepath.Join(t.TempDir(), "read-state.json")
	content := `{
	  "version": 1,
	  "host": "github.com",
	  "overrides": {
	    "1": {"state": "read", "at": "2026-08-25T09:00:00Z", "subject_updated_at": "2026-08-25T09:00:00Z"},
	    "2": {"state": "archived", "at": "2026-08-25T09:00:00Z", "subject_updated_at": "2026-08-25T09:00:00Z"},
	    "": {"state": "read", "at": "2026-08-25T09:00:00Z", "subject_updated_at": "2026-08-25T09:00:00Z"}
	  }
	}`
	gt.NoError(t, os.WriteFile(path, []byte(content), 0o600))

	store := readstate.New(path, "github.com")
	gt.NoError(t, store.Load())

	// Entries this build cannot interpret are dropped; the rest stay usable.
	gt.Equal(t, store.Len(), 1)
	_, ok := store.Lookup("1")
	gt.True(t, ok)
}

func TestStoreWriteFailureIsReported(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("a read-only directory does not stop writes when running as root")
	}

	dir := filepath.Join(t.TempDir(), "read-only")
	gt.NoError(t, os.Mkdir(dir, 0o700))

	store := readstate.New(filepath.Join(dir, "read-state.json"), "github.com")
	gt.NoError(t, store.Load())

	// Make the directory unwritable after loading so only the write can fail.
	gt.NoError(t, os.Chmod(dir, 0o500))
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	err := store.Put(map[types.ThreadID]model.ReadOverride{
		"1": override(model.ReadStateRead, base, base),
	})
	gt.Error(t, err)

	msg, ok := model.UserMessageOf(err)
	gt.True(t, ok)
	gt.S(t, msg.Summary).Contains("could not save read state")

	// The in-memory value is kept so the current session still behaves correctly.
	_, found := store.Lookup("1")
	gt.True(t, found)
}
