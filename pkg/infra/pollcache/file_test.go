package pollcache_test

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/m-mizutani/gt"
	"github.com/m-mizutani/octify/pkg/domain/model"
	"github.com/m-mizutani/octify/pkg/domain/types"
	"github.com/m-mizutani/octify/pkg/infra/pollcache"
)

var base = time.Date(2026, 8, 25, 9, 0, 0, 0, time.UTC)

func notification(id types.ThreadID, number int) model.Notification {
	return model.Notification{
		ID: id,
		Repo: model.Repository{
			FullName: "acme/tools",
			HTMLURL:  "https://github.com/acme/tools",
			Private:  true,
		},
		Subject: model.Subject{
			Title:  "Fix the flaky poller test",
			Type:   types.SubjectPullRequest,
			URL:    "https://api.github.com/repos/acme/tools/pulls/482",
			Number: number,
		},
		Reason:       model.ReasonReviewRequested,
		ServerUnread: true,
		UpdatedAt:    base.Add(-2 * time.Hour),
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "poll-cache.json")
	saved := &model.PollSnapshot{
		SavedAt:       base,
		Notifications: []model.Notification{notification("1", 482), notification("2", 91), notification("3", 7)},
		ReviewRequests: model.ReviewRequests{
			{Repo: "acme/tools", Number: 482}: {},
			{Repo: "acme/web", Number: 91}:    {},
		},
		SubjectStates: model.SubjectStates{
			{Repo: "acme/tools", Number: 482}: {Authored: true, Merged: true},
			{Repo: "acme/web", Number: 91}:    {Closed: true},
		},
	}

	gt.NoError(t, pollcache.New(path, "github.com").Save(saved))

	got := gt.R1(pollcache.New(path, "github.com").Load()).NoError(t)
	gt.NotNil(t, got)
	gt.True(t, got.SavedAt.Equal(base))

	gt.A(t, got.Notifications).Length(3)
	first := got.Notifications[0]
	gt.Equal(t, first.ID, types.ThreadID("1"))
	gt.Equal(t, first.Repo.FullName, types.RepoFullName("acme/tools"))
	gt.Equal(t, first.Repo.HTMLURL, "https://github.com/acme/tools")
	gt.True(t, first.Repo.Private)
	gt.Equal(t, first.Subject.Title, "Fix the flaky poller test")
	gt.Equal(t, first.Subject.Type, types.SubjectPullRequest)
	gt.Equal(t, first.Subject.URL, "https://api.github.com/repos/acme/tools/pulls/482")
	gt.Equal(t, first.Subject.Number, 482)
	gt.Equal(t, first.Reason, model.ReasonReviewRequested)
	gt.True(t, first.ServerUnread)
	gt.True(t, first.UpdatedAt.Equal(base.Add(-2*time.Hour)))

	// The markers are maps keyed by a struct, which is the part of the format
	// that cannot survive a naive encoding.
	gt.Equal(t, len(got.ReviewRequests), 2)
	gt.True(t, got.ReviewRequests.Has(model.SubjectRef{Repo: "acme/tools", Number: 482}))
	gt.True(t, got.ReviewRequests.Has(model.SubjectRef{Repo: "acme/web", Number: 91}))

	merged, ok := got.SubjectStates.Lookup(model.SubjectRef{Repo: "acme/tools", Number: 482})
	gt.True(t, ok)
	gt.True(t, merged.Authored)
	gt.True(t, merged.Merged)
	gt.False(t, merged.Closed)

	closed, ok := got.SubjectStates.Lookup(model.SubjectRef{Repo: "acme/web", Number: 91})
	gt.True(t, ok)
	gt.True(t, closed.Closed)
	gt.False(t, closed.Merged)
}

func TestSaveLoadEmptySnapshot(t *testing.T) {
	path := filepath.Join(t.TempDir(), "poll-cache.json")

	gt.NoError(t, pollcache.New(path, "github.com").Save(&model.PollSnapshot{SavedAt: base}))

	got := gt.R1(pollcache.New(path, "github.com").Load()).NoError(t)
	// An inbox that is genuinely empty is a snapshot, not the absence of one.
	gt.NotNil(t, got)
	gt.A(t, got.Notifications).Length(0)
	gt.Equal(t, len(got.ReviewRequests), 0)
	gt.Equal(t, len(got.SubjectStates), 0)
}

func TestSaveReplacesWholeFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "poll-cache.json")
	store := pollcache.New(path, "github.com")

	gt.NoError(t, store.Save(&model.PollSnapshot{
		SavedAt:       base,
		Notifications: []model.Notification{notification("1", 1), notification("2", 2), notification("3", 3)},
	}))
	gt.NoError(t, store.Save(&model.PollSnapshot{
		SavedAt:       base.Add(time.Minute),
		Notifications: []model.Notification{notification("9", 9)},
	}))

	got := gt.R1(store.Load()).NoError(t)
	gt.A(t, got.Notifications).Length(1)
	gt.Equal(t, got.Notifications[0].ID, types.ThreadID("9"))
}

func TestSaveNilSnapshot(t *testing.T) {
	path := filepath.Join(t.TempDir(), "poll-cache.json")

	gt.Error(t, pollcache.New(path, "github.com").Save(nil)).
		Is(pollcache.ErrNilSnapshot)

	// Nothing was written, so a previously saved list would still be there.
	_, err := os.Stat(path)
	gt.True(t, errors.Is(err, fs.ErrNotExist))
}

func TestLoadMissingFile(t *testing.T) {
	store := pollcache.New(filepath.Join(t.TempDir(), "absent.json"), "github.com")

	// A first run has no file; that is not a failure.
	got := gt.R1(store.Load()).NoError(t)
	gt.Nil(t, got)
}

func TestLoadBrokenJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "poll-cache.json")
	gt.NoError(t, os.WriteFile(path, []byte("{"), 0o600))

	got, err := pollcache.New(path, "github.com").Load()
	gt.Nil(t, got)
	gt.Error(t, err).Is(pollcache.ErrInvalidCache)
}

func TestLoadVersionMismatch(t *testing.T) {
	path := filepath.Join(t.TempDir(), "poll-cache.json")
	gt.NoError(t, os.WriteFile(path,
		[]byte(`{"version":99,"host":"github.com","notifications":[]}`), 0o600))

	got, err := pollcache.New(path, "github.com").Load()
	gt.Nil(t, got)
	gt.Error(t, err).Is(pollcache.ErrUnsupportedCacheVersion)
}

func TestLoadHostMismatch(t *testing.T) {
	path := filepath.Join(t.TempDir(), "poll-cache.json")
	gt.NoError(t, pollcache.New(path, "ghe.example.com").Save(&model.PollSnapshot{
		SavedAt:       base,
		Notifications: []model.Notification{notification("1", 1)},
	}))

	got, err := pollcache.New(path, "github.com").Load()
	gt.Nil(t, got)
	gt.Error(t, err).Is(pollcache.ErrHostMismatch)
}

func TestLoadDropsUnusableEntries(t *testing.T) {
	path := filepath.Join(t.TempDir(), "poll-cache.json")
	gt.NoError(t, os.WriteFile(path, []byte(`{
	  "version": 1,
	  "host": "github.com",
	  "saved_at": "2026-08-25T09:00:00Z",
	  "notifications": [
	    {"id": "", "subject_title": "no id"},
	    {"id": "2", "subject_title": "kept", "repo_full_name": "acme/tools"}
	  ],
	  "review_requests": [
	    {"repo": "acme/tools", "number": 0},
	    {"repo": "", "number": 5},
	    {"repo": "acme/web", "number": 91}
	  ],
	  "subject_states": [
	    {"repo": "acme/tools", "number": -1, "merged": true},
	    {"repo": "acme/web", "number": 91, "closed": true}
	  ]
	}`), 0o600))

	got := gt.R1(pollcache.New(path, "github.com").Load()).NoError(t)
	gt.A(t, got.Notifications).Length(1)
	gt.Equal(t, got.Notifications[0].ID, types.ThreadID("2"))

	gt.Equal(t, len(got.ReviewRequests), 1)
	gt.True(t, got.ReviewRequests.Has(model.SubjectRef{Repo: "acme/web", Number: 91}))

	gt.Equal(t, len(got.SubjectStates), 1)
	_, ok := got.SubjectStates.Lookup(model.SubjectRef{Repo: "acme/web", Number: 91})
	gt.True(t, ok)
}

func TestLoadUnreadableFile(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads a 0000 file regardless of its mode")
	}

	path := filepath.Join(t.TempDir(), "poll-cache.json")
	store := pollcache.New(path, "github.com")
	gt.NoError(t, store.Save(&model.PollSnapshot{SavedAt: base}))
	gt.NoError(t, os.Chmod(path, 0o000))

	got, err := store.Load()
	gt.Nil(t, got)
	gt.Error(t, err)
}

func TestFilePermission(t *testing.T) {
	// Use a directory octify has to create itself, so its mode can be checked.
	dir := filepath.Join(t.TempDir(), "created-by-octify")
	path := filepath.Join(dir, "poll-cache.json")

	gt.NoError(t, pollcache.New(path, "github.com").Save(&model.PollSnapshot{
		SavedAt:       base,
		Notifications: []model.Notification{notification("1", 1)},
	}))

	// The file carries the titles of private repositories, so nobody else may
	// read it.
	info := gt.R1(os.Stat(path)).NoError(t)
	gt.Equal(t, info.Mode().Perm(), os.FileMode(0o600))

	dirInfo := gt.R1(os.Stat(dir)).NoError(t)
	gt.Equal(t, dirInfo.Mode().Perm(), os.FileMode(0o700))
}
