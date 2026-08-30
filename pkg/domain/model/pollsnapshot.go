package model

import "time"

// PollSnapshot is what one polling cycle put on screen: the list and the
// markers drawn beside it.
//
// It is saved so that the next start has something to draw before its own first
// poll answers. It deliberately carries no conditional-request state: the first
// poll of a session asks unconditionally, so a snapshot can never outlive the
// list it describes by more than one cycle.
type PollSnapshot struct {
	// SavedAt is when the snapshot was written. It is informational; nothing
	// reads it back to decide anything.
	SavedAt        time.Time
	Notifications  []Notification
	ReviewRequests ReviewRequests
	SubjectStates  SubjectStates
}
