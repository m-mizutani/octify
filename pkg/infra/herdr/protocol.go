package herdr

import "time"

const (
	// defaultTimeout covers the dial, the write and the read together. The
	// caller is a terminal program that polls GitHub every minute, so waiting
	// longer than this for a toast buys nothing.
	defaultTimeout = 2 * time.Second

	maxTitleRunes = 120
	maxBodyRunes  = 200
	// maxResponseBytes bounds the one line the server is expected to answer
	// with, so a server that streams instead cannot grow the buffer unchecked.
	maxResponseBytes = 64 * 1024

	methodNotificationShow = "notification.show"
	socketFileName         = "herdr.sock"
)

// The wire format is newline-delimited JSON over the local socket: one object
// per line, no length prefix and no handshake.

type request struct {
	ID     string     `json:"id"`
	Method string     `json:"method"`
	Params showParams `json:"params"`
}

// showParams deliberately carries no position field. Where a toast appears is
// part of the herdr configuration the user already made, not octify's decision.
type showParams struct {
	Title string `json:"title"`
	Body  string `json:"body,omitempty"`
	Sound Sound  `json:"sound"`
}

type response struct {
	ID     string      `json:"id"`
	Result *showResult `json:"result"`
	Error  *errorBody  `json:"error"`
}

type showResult struct {
	Type  string `json:"type"`
	Shown bool   `json:"shown"`
	// Reason is one of shown, disabled, rate_limited, no_foreground_client or
	// busy. Only the first means the toast reached the screen.
	Reason string `json:"reason"`
}

type errorBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}
