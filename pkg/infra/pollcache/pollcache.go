package pollcache

import (
	"errors"
	"io/fs"
	"os"
	"sync"

	"github.com/m-mizutani/goerr/v2"
)

// CacheVersion is the format version written by this build.
const CacheVersion = 1

var (
	ErrInvalidCache            = goerr.New("pollcache: invalid cache file")
	ErrUnsupportedCacheVersion = goerr.New("pollcache: unsupported cache file version")
	ErrHostMismatch            = goerr.New("pollcache: cache file belongs to another host")
	ErrNilSnapshot             = goerr.New("pollcache: nil snapshot")
)

// Store keeps the last polling cycle's list on disk so the next start has
// something to draw before its own first poll answers.
//
// Everything it holds is derived from GitHub and is replaced by the next cycle,
// so a file this build cannot use is refused rather than repaired: the caller
// carries on without it.
//
// The methods are safe for concurrent use. Saving runs on the polling
// goroutine, deleting can come from the archive goroutine when GitHub rejects
// the token, and loading happens once at start-up.
type Store struct {
	mu   sync.Mutex
	path string
	host string
}

// New returns a store over path. host is the GitHub the entries belong to; a
// file written for another host is refused by Load.
func New(path, host string) *Store {
	return &Store{path: path, host: host}
}

func (s *Store) Path() string { return s.path }

// Delete removes the saved list. A missing file is a success: the caller only
// asks for it to be gone.
func (s *Store) Delete() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := os.Remove(s.path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return goerr.Wrap(err, "failed to remove the poll cache file", goerr.V("path", s.path))
	}
	return nil
}
