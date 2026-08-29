package model

import (
	"time"

	"github.com/m-mizutani/goerr/v2"
)

// ReadState is the read or unread decision the user made inside octify.
type ReadState string

const (
	ReadStateRead   ReadState = "read"
	ReadStateUnread ReadState = "unread"
)

var ErrInvalidReadState = goerr.New("invalid read state")

func (s ReadState) Validate() error {
	switch s {
	case ReadStateRead, ReadStateUnread:
		return nil
	default:
		return goerr.Wrap(ErrInvalidReadState, "unknown read state", goerr.V("state", string(s)))
	}
}

// ReadOverride records the user's decision for one notification. It overrides
// GitHub's own unread flag until the notification is updated again.
type ReadOverride struct {
	State ReadState `json:"state"`
	// At is when the record was written. Only the retention rule uses it.
	At time.Time `json:"at"`
	// SubjectUpdatedAt is the notification's UpdatedAt at the time the record was
	// written. A later update makes the record stale.
	SubjectUpdatedAt time.Time `json:"subject_updated_at"`
}

// OverrideStale reports whether the notification has been updated since the
// record was written. Equal timestamps are not stale.
func (n Notification) OverrideStale(ov ReadOverride) bool {
	return n.UpdatedAt.After(ov.SubjectUpdatedAt)
}

// Unread decides the displayed unread state. Without a usable record it falls
// back to GitHub's flag, which is what makes the first run agree with the web UI.
func (n Notification) Unread(ov ReadOverride, ok bool) bool {
	if !ok || n.OverrideStale(ov) {
		return n.ServerUnread
	}
	return ov.State == ReadStateUnread
}
