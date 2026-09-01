package model

import "github.com/m-mizutani/octify/pkg/domain/types"

// Arrivals reports the notifications in next that prev did not hold, and those
// prev held with an older UpdatedAt.
//
// A later UpdatedAt counts because that is how GitHub reports a new comment on
// a thread that is already in the list: the thread does not reappear, it moves.
// A timestamp that stayed the same or moved backwards is not an arrival.
//
// It says nothing about read state. The caller decides whether an arrival is
// worth announcing, because only the caller knows the local read records.
//
// The order of next is preserved, so the first element is the one GitHub put
// first.
func Arrivals(prev, next []Notification) []Notification {
	if len(next) == 0 {
		return nil
	}

	seen := make(map[types.ThreadID]Notification, len(prev))
	for _, n := range prev {
		seen[n.ID] = n
	}

	out := make([]Notification, 0, len(next))
	for _, n := range next {
		before, ok := seen[n.ID]
		if ok && !n.UpdatedAt.After(before.UpdatedAt) {
			continue
		}
		out = append(out, n)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
