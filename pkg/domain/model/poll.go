package model

// PollState carries what one polling cycle needs to know about the previous one.
type PollState struct {
	// LastModified is the raw Last-Modified header value of the previous response.
	LastModified string
	// Failures counts consecutive failures and drives the backoff.
	Failures int
	// NotModifiedStreak counts how many cycles in a row GitHub answered with
	// 304. A conditional request that keeps succeeding never reaches the state
	// lookup, so the streak is what eventually forces an unconditional one.
	NotModifiedStreak int
}
