package pollcache_test

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/m-mizutani/gt"
	"github.com/m-mizutani/octify/pkg/domain/model"
	"github.com/m-mizutani/octify/pkg/infra/pollcache"
)

func TestStorePath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "poll-cache.json")
	gt.Equal(t, pollcache.New(path, "github.com").Path(), path)
}

func TestDelete(t *testing.T) {
	path := filepath.Join(t.TempDir(), "poll-cache.json")
	store := pollcache.New(path, "github.com")
	gt.NoError(t, store.Save(&model.PollSnapshot{
		SavedAt:       base,
		Notifications: []model.Notification{notification("1", 1)},
	}))

	gt.NoError(t, store.Delete())

	_, err := os.Stat(path)
	gt.True(t, errors.Is(err, fs.ErrNotExist))

	got := gt.R1(store.Load()).NoError(t)
	gt.Nil(t, got)
}

func TestDeleteMissingFile(t *testing.T) {
	store := pollcache.New(filepath.Join(t.TempDir(), "absent.json"), "github.com")

	// The caller only asks for the file to be gone, and it is.
	gt.NoError(t, store.Delete())
}

func TestConcurrentSaveAndDelete(t *testing.T) {
	path := filepath.Join(t.TempDir(), "poll-cache.json")
	store := pollcache.New(path, "github.com")
	snap := &model.PollSnapshot{
		SavedAt:       base,
		Notifications: []model.Notification{notification("1", 1), notification("2", 2)},
	}

	// A poll cycle saving while the archive goroutine signs out on a rejected
	// token is the real pairing this guards.
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(2)
		go func() {
			defer wg.Done()
			gt.NoError(t, store.Save(snap))
		}()
		go func() {
			defer wg.Done()
			gt.NoError(t, store.Delete())
		}()
	}
	wg.Wait()

	// Whichever call landed last, the file is either absent or complete; a
	// half-written one would fail to load.
	got, err := store.Load()
	gt.NoError(t, err)
	if got != nil {
		gt.A(t, got.Notifications).Length(2)
	}
}
