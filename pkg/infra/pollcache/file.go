package pollcache

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"time"

	"github.com/m-mizutani/goerr/v2"
	"github.com/m-mizutani/octify/pkg/domain/model"
	"github.com/m-mizutani/octify/pkg/domain/types"
	"github.com/m-mizutani/octify/pkg/utils/atomicfile"
)

const (
	cacheFileMode fs.FileMode = 0o600
	cacheDirMode  fs.FileMode = 0o700
)

// cacheFile is the on-disk shape. It is defined here rather than by tagging the
// domain types because the markers cannot be encoded as they are held in
// memory: both are maps keyed by model.SubjectRef, and encoding/json rejects a
// struct key. Converting only those two would leave the format described in two
// places, so the whole file is described here.
type cacheFile struct {
	Version int `json:"version"`
	// Host records which GitHub the entries belong to. Unlike the read-state
	// file, where it is informational, a mismatch here is refused: another
	// host's titles and thread IDs must not be drawn as this one's.
	Host          string              `json:"host"`
	SavedAt       time.Time           `json:"saved_at"`
	Notifications []notificationEntry `json:"notifications"`
	Reviews       []refEntry          `json:"review_requests"`
	States        []stateEntry        `json:"subject_states"`
}

type notificationEntry struct {
	ID            types.ThreadID     `json:"id"`
	RepoFullName  types.RepoFullName `json:"repo_full_name"`
	RepoHTMLURL   string             `json:"repo_html_url"`
	RepoPrivate   bool               `json:"repo_private"`
	SubjectTitle  string             `json:"subject_title"`
	SubjectType   types.SubjectType  `json:"subject_type"`
	SubjectURL    string             `json:"subject_url"`
	SubjectNumber int                `json:"subject_number"`
	Reason        model.Reason       `json:"reason"`
	ServerUnread  bool               `json:"server_unread"`
	UpdatedAt     time.Time          `json:"updated_at"`
}

type refEntry struct {
	Repo   types.RepoFullName `json:"repo"`
	Number int                `json:"number"`
}

type stateEntry struct {
	Repo     types.RepoFullName `json:"repo"`
	Number   int                `json:"number"`
	Authored bool               `json:"authored"`
	Merged   bool               `json:"merged"`
	Closed   bool               `json:"closed"`
}

// Load reads the saved list. A missing file returns no snapshot and no error,
// because that is the state of a first run.
//
// A file this build cannot use — broken, written by another version, or
// belonging to another host — returns the reason as an error and no snapshot.
// None of those errors carry a user message: the caller logs them and carries
// on, since the next successful poll replaces the file anyway.
func (s *Store) Load() (*model.PollSnapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	raw, err := os.ReadFile(s.path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, goerr.Wrap(err, "failed to read the poll cache file", goerr.V("path", s.path))
	}

	var body cacheFile
	if err := json.Unmarshal(raw, &body); err != nil {
		return nil, goerr.Wrap(ErrInvalidCache, "poll cache file is not valid json",
			goerr.V("path", s.path), goerr.V("cause", err.Error()))
	}
	if body.Version != CacheVersion {
		return nil, goerr.Wrap(ErrUnsupportedCacheVersion, "poll cache file version mismatch",
			goerr.V("path", s.path), goerr.V("version", body.Version), goerr.V("supported", CacheVersion))
	}
	if body.Host != s.host {
		return nil, goerr.Wrap(ErrHostMismatch, "poll cache file was written for another host",
			goerr.V("path", s.path), goerr.V("host", body.Host), goerr.V("expected", s.host))
	}

	return decode(body), nil
}

// Save replaces the file with what the cycle put on screen.
func (s *Store) Save(snap *model.PollSnapshot) error {
	if snap == nil {
		return goerr.Wrap(ErrNilSnapshot, "refusing to save nothing", goerr.V("path", s.path))
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	// Not indented: the file holds up to five hundred notifications and is only
	// ever read back by octify itself.
	raw, err := json.Marshal(encode(snap, s.host))
	if err != nil {
		return goerr.Wrap(err, "failed to encode the poll cache")
	}

	if err := atomicfile.Write(s.path, raw, cacheFileMode, cacheDirMode); err != nil {
		// The rename never ran, so whatever the file held before is still there:
		// the next start gets an older list rather than none.
		return model.WithUserMessage(err, model.UserMessage{
			Summary: "could not save the notification list to " + s.path,
			Action:  "the next start may show an older list",
		})
	}
	return nil
}

func encode(snap *model.PollSnapshot, host string) cacheFile {
	body := cacheFile{
		Version:       CacheVersion,
		Host:          host,
		SavedAt:       snap.SavedAt,
		Notifications: make([]notificationEntry, 0, len(snap.Notifications)),
		Reviews:       make([]refEntry, 0, len(snap.ReviewRequests)),
		States:        make([]stateEntry, 0, len(snap.SubjectStates)),
	}

	for _, n := range snap.Notifications {
		body.Notifications = append(body.Notifications, notificationEntry{
			ID:            n.ID,
			RepoFullName:  n.Repo.FullName,
			RepoHTMLURL:   n.Repo.HTMLURL,
			RepoPrivate:   n.Repo.Private,
			SubjectTitle:  n.Subject.Title,
			SubjectType:   n.Subject.Type,
			SubjectURL:    n.Subject.URL,
			SubjectNumber: n.Subject.Number,
			Reason:        n.Reason,
			ServerUnread:  n.ServerUnread,
			UpdatedAt:     n.UpdatedAt,
		})
	}
	for ref := range snap.ReviewRequests {
		body.Reviews = append(body.Reviews, refEntry{Repo: ref.Repo, Number: ref.Number})
	}
	for ref, st := range snap.SubjectStates {
		body.States = append(body.States, stateEntry{
			Repo:     ref.Repo,
			Number:   ref.Number,
			Authored: st.Authored,
			Merged:   st.Merged,
			Closed:   st.Closed,
		})
	}
	return body
}

// decode drops entries this build cannot interpret rather than refusing the
// whole file: the rest of the list is still worth drawing.
func decode(body cacheFile) *model.PollSnapshot {
	snap := &model.PollSnapshot{
		SavedAt:        body.SavedAt,
		Notifications:  make([]model.Notification, 0, len(body.Notifications)),
		ReviewRequests: make(model.ReviewRequests, len(body.Reviews)),
		SubjectStates:  make(model.SubjectStates, len(body.States)),
	}

	for _, e := range body.Notifications {
		if e.ID == "" {
			continue
		}
		snap.Notifications = append(snap.Notifications, model.Notification{
			ID: e.ID,
			Repo: model.Repository{
				FullName: e.RepoFullName,
				HTMLURL:  e.RepoHTMLURL,
				Private:  e.RepoPrivate,
			},
			Subject: model.Subject{
				Title:  e.SubjectTitle,
				Type:   e.SubjectType,
				URL:    e.SubjectURL,
				Number: e.SubjectNumber,
			},
			Reason:       e.Reason,
			ServerUnread: e.ServerUnread,
			UpdatedAt:    e.UpdatedAt,
		})
	}
	for _, e := range body.Reviews {
		ref, ok := subjectRef(e.Repo, e.Number)
		if !ok {
			continue
		}
		snap.ReviewRequests[ref] = struct{}{}
	}
	for _, e := range body.States {
		ref, ok := subjectRef(e.Repo, e.Number)
		if !ok {
			continue
		}
		snap.SubjectStates[ref] = model.SubjectState{
			Authored: e.Authored,
			Merged:   e.Merged,
			Closed:   e.Closed,
		}
	}
	return snap
}

// subjectRef rejects what model.SubjectRef never holds, so a damaged entry
// cannot become a marker that matches nothing.
func subjectRef(repo types.RepoFullName, number int) (model.SubjectRef, bool) {
	if repo == "" || number <= 0 {
		return model.SubjectRef{}, false
	}
	return model.SubjectRef{Repo: repo, Number: number}, true
}
