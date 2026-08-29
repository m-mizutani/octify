package readstate

import (
	"maps"
	"time"

	"github.com/m-mizutani/goerr/v2"
	"github.com/m-mizutani/octify/pkg/domain/model"
	"github.com/m-mizutani/octify/pkg/domain/types"
)

// StateVersion is the format version written by this build.
const StateVersion = 1

var (
	ErrInvalidState            = goerr.New("readstate: invalid state file")
	ErrUnsupportedStateVersion = goerr.New("readstate: unsupported state file version")
)

// Store keeps every read/unread record in memory and writes the whole file back
// whenever something changes. The records are small enough that this stays
// cheaper than maintaining an index.
//
// Methods are called from the Bubble Tea update loop and from the archive
// goroutine's completion handler only, so there is no internal locking.
type Store struct {
	path      string
	host      string
	overrides map[types.ThreadID]model.ReadOverride
	writes    int
}

// New returns an empty store. Call Load before using it so that the records
// from a previous run are visible.
func New(path, host string) *Store {
	return &Store{
		path:      path,
		host:      host,
		overrides: make(map[types.ThreadID]model.ReadOverride),
	}
}

func (s *Store) Path() string { return s.path }

func (s *Store) Len() int { return len(s.overrides) }

// Lookup returns the record for one notification, if any.
func (s *Store) Lookup(id types.ThreadID) (model.ReadOverride, bool) {
	ov, ok := s.overrides[id]
	return ov, ok
}

// Put overwrites several records and writes the file once.
func (s *Store) Put(entries map[types.ThreadID]model.ReadOverride) error {
	if len(entries) == 0 {
		return nil
	}
	maps.Copy(s.overrides, entries)
	return s.write()
}

// Remove drops records and writes the file only when something was actually
// removed.
func (s *Store) Remove(ids ...types.ThreadID) error {
	removed := false
	for _, id := range ids {
		if _, ok := s.overrides[id]; ok {
			delete(s.overrides, id)
			removed = true
		}
	}
	if !removed {
		return nil
	}
	return s.write()
}

type ReconcileOption struct {
	// PruneMissing enables dropping records for notifications that were not in
	// the list. It must be false when the list was cut short by the page limit,
	// otherwise records for notifications that simply were not fetched are lost.
	PruneMissing bool
	TTL          time.Duration
	Now          time.Time
}

// Reconcile drops records that no longer mean anything: those overtaken by a
// newer update, and those whose notification has been gone for longer than TTL.
func (s *Store) Reconcile(notifications []model.Notification, opt ReconcileOption) (int, error) {
	alive := make(map[types.ThreadID]struct{}, len(notifications))
	removed := 0

	for _, n := range notifications {
		alive[n.ID] = struct{}{}
		if ov, ok := s.overrides[n.ID]; ok && n.OverrideStale(ov) {
			delete(s.overrides, n.ID)
			removed++
		}
	}

	if opt.PruneMissing {
		cutoff := opt.Now.Add(-opt.TTL)
		for id, ov := range s.overrides {
			if _, ok := alive[id]; ok {
				continue
			}
			if ov.At.Before(cutoff) {
				delete(s.overrides, id)
				removed++
			}
		}
	}

	if removed == 0 {
		return 0, nil
	}
	if err := s.write(); err != nil {
		return removed, err
	}
	return removed, nil
}
