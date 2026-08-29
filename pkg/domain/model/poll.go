package model

// PollState carries what one polling cycle needs to know about the previous one.
type PollState struct {
	// LastModified is the raw Last-Modified header value of the previous response.
	LastModified string
	// Failures counts consecutive failures and drives the backoff.
	Failures int
}
