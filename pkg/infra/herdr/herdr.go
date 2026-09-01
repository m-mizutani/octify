// Package herdr shows desktop toasts through the socket API of herdr, the
// terminal workspace manager octify is often run inside.
//
// Everything here is optional by construction: a process that is not running in
// a herdr pane never gets a client, and a client that cannot reach the server
// reports it and nothing else happens. octify's notification list does not
// depend on any of it.
package herdr

import (
	"os"
	"path/filepath"

	"github.com/m-mizutani/goerr/v2"
)

// Sound is the audio herdr plays with the toast. The three values are the ones
// the server accepts; anything else is refused.
type Sound string

const (
	SoundNone    Sound = "none"
	SoundDone    Sound = "done"
	SoundRequest Sound = "request"
)

var ErrInvalidSound = goerr.New("herdr: invalid sound")

func (s Sound) Validate() error {
	switch s {
	case SoundNone, SoundDone, SoundRequest:
		return nil
	default:
		return goerr.Wrap(ErrInvalidSound, "unknown herdr sound", goerr.V("sound", string(s)))
	}
}

// State is what herdr shows beside the agent in its list.
//
// These four are what a process may report. herdr derives a fifth, done, from
// an idle agent whose tab has not been looked at, so it is not reportable.
type State string

const (
	StateIdle    State = "idle"
	StateWorking State = "working"
	StateBlocked State = "blocked"
	StateUnknown State = "unknown"
)

var ErrInvalidState = goerr.New("herdr: invalid agent state")

func (s State) Validate() error {
	switch s {
	case StateIdle, StateWorking, StateBlocked, StateUnknown:
		return nil
	default:
		return goerr.Wrap(ErrInvalidState, "unknown herdr agent state", goerr.V("state", string(s)))
	}
}

// Session is the herdr session this process is running in.
type Session struct {
	// Socket is the path of the server's unix socket.
	Socket string
	// PaneID is the pane this process occupies. It is empty when herdr did not
	// inject one, which leaves toasts working and reporting unavailable: a
	// toast is addressed to the session, a report to a particular pane.
	PaneID string
}

// Detect reports the herdr session this process is running in.
//
// HERDR_ENV is the gate rather than the presence of a socket file, because
// herdr's own guidance is that a process outside a pane must not reach into a
// running session. A person running octify in a plain terminal must not get
// toasts drawn on someone else's herdr screen.
//
// The second return value is false when this process is not inside a session.
// There is nothing for a user to fix in that case, so it is not an error.
func Detect() (Session, bool) {
	if os.Getenv("HERDR_ENV") != "1" {
		return Session{}, false
	}

	sess := Session{PaneID: os.Getenv("HERDR_PANE_ID")}

	// Set in every managed pane. The rest of this function covers the
	// documented resolution order for the cases where it is not.
	if path := os.Getenv("HERDR_SOCKET_PATH"); path != "" {
		sess.Socket = path
		return sess, true
	}

	dir, ok := configDir()
	if !ok {
		return Session{}, false
	}
	if session := os.Getenv("HERDR_SESSION"); session != "" {
		sess.Socket = filepath.Join(dir, "sessions", session, socketFileName)
		return sess, true
	}
	sess.Socket = filepath.Join(dir, socketFileName)
	return sess, true
}

// configDir locates herdr's configuration directory.
//
// os.UserConfigDir is deliberately not used: on macOS it answers
// ~/Library/Application Support, while herdr puts its config and socket under
// ~/.config/herdr on that same platform.
func configDir() (string, bool) {
	if base := os.Getenv("XDG_CONFIG_HOME"); base != "" {
		return filepath.Join(base, "herdr"), true
	}
	if home := os.Getenv("HOME"); home != "" {
		return filepath.Join(home, ".config", "herdr"), true
	}
	return "", false
}
