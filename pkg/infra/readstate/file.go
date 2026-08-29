package readstate

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"

	"github.com/m-mizutani/goerr/v2"
	"github.com/m-mizutani/octify/pkg/domain/model"
	"github.com/m-mizutani/octify/pkg/domain/types"
	"github.com/m-mizutani/octify/pkg/utils/atomicfile"
)

const (
	stateFileMode fs.FileMode = 0o600
	stateDirMode  fs.FileMode = 0o700
)

type stateFile struct {
	Version int `json:"version"`
	// Host records which GitHub the thread IDs belong to. It is informational:
	// pointing octify at another host normally means another state file too.
	Host      string                                `json:"host"`
	Overrides map[types.ThreadID]model.ReadOverride `json:"overrides"`
}

// Load reads the file into memory. A missing file is an empty set, not an error,
// because that is the state of a first run.
func (s *Store) Load() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	raw, err := os.ReadFile(s.path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			s.overrides = make(map[types.ThreadID]model.ReadOverride)
			return nil
		}
		return model.WithUserMessage(
			goerr.Wrap(err, "failed to read read-state file", goerr.V("path", s.path)),
			model.UserMessage{
				Summary: "could not read the read-state file at " + s.path,
				Action:  "check that the file is readable, or delete it to start over",
			},
		)
	}

	var body stateFile
	if err := json.Unmarshal(raw, &body); err != nil {
		return model.WithUserMessage(
			goerr.Wrap(ErrInvalidState, "read-state file is not valid json",
				goerr.V("path", s.path), goerr.V("cause", err.Error())),
			model.UserMessage{
				Summary: "the read-state file at " + s.path + " is broken",
				Action:  "delete it to start over; all notifications return to GitHub's unread state",
			},
		)
	}
	if body.Version != StateVersion {
		// A higher version means the file came from a build that knows more than
		// this one; a lower one means it is either older or missing the field
		// entirely. Only the first is fixed by updating octify.
		msg := model.UserMessage{
			Summary: "the read-state file at " + s.path + " is not in a format this octify understands",
			Action:  "delete it to start over; all notifications return to GitHub's unread state",
		}
		if body.Version > StateVersion {
			msg = model.UserMessage{
				Summary: "the read-state file was written by a newer octify",
				Action:  "update octify, or delete " + s.path,
			}
		}
		return model.WithUserMessage(
			goerr.Wrap(ErrUnsupportedStateVersion, "read-state file version mismatch",
				goerr.V("path", s.path), goerr.V("version", body.Version), goerr.V("supported", StateVersion)),
			msg,
		)
	}

	s.overrides = make(map[types.ThreadID]model.ReadOverride, len(body.Overrides))
	for id, ov := range body.Overrides {
		// Drop entries the current build cannot interpret rather than failing the
		// whole file: the rest of the records are still usable.
		if ov.State.Validate() != nil || id == "" {
			continue
		}
		s.overrides[id] = ov
	}
	return nil
}

// write persists the whole map. Every mutation goes through here so that a
// crash cannot lose an operation the user already saw applied.
//
// The caller must already hold s.mu for writing.
func (s *Store) write() error {
	body := stateFile{
		Version:   StateVersion,
		Host:      s.host,
		Overrides: s.overrides,
	}
	raw, err := json.MarshalIndent(body, "", "  ")
	if err != nil {
		return goerr.Wrap(err, "failed to encode read state")
	}

	if err := atomicfile.Write(s.path, raw, stateFileMode, stateDirMode); err != nil {
		return model.WithUserMessage(err, model.UserMessage{
			Summary: "could not save read state to " + s.path,
			Action:  "changes apply to this session only",
		})
	}
	s.writes++
	return nil
}
