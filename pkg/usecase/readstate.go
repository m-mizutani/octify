package usecase

import (
	"github.com/m-mizutani/octify/pkg/domain/model"
	"github.com/m-mizutani/octify/pkg/domain/types"
)

// SetReadState records the user's read or unread decision for the given
// notifications. It touches no network: GitHub's own read flag is left alone,
// so this completes as fast as a single file write.
//
// The notifications themselves are needed, not just their IDs, because each
// record stores the UpdatedAt it was written against. That is what lets a later
// update supersede the record.
func (u *UseCase) SetReadState(st model.ReadState, ns []model.Notification) error {
	if len(ns) == 0 {
		return nil
	}
	if err := st.Validate(); err != nil {
		return err
	}

	now := u.now()
	entries := make(map[types.ThreadID]model.ReadOverride, len(ns))
	for _, n := range ns {
		entries[n.ID] = model.ReadOverride{
			State:            st,
			At:               now,
			SubjectUpdatedAt: n.UpdatedAt,
		}
	}
	return u.reads.Put(entries)
}

// ReadOverride returns the stored decision for one notification, if any.
func (u *UseCase) ReadOverride(id types.ThreadID) (model.ReadOverride, bool) {
	return u.reads.Lookup(id)
}

// Unread reports the displayed unread state for a notification, combining
// GitHub's flag with the local record.
func (u *UseCase) Unread(n model.Notification) bool {
	ov, ok := u.reads.Lookup(n.ID)
	return n.Unread(ov, ok)
}
