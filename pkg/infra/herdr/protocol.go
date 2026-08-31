package herdr

import (
	"encoding/json"
	"time"
)

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

	methodNotificationShow   = "notification.show"
	methodPaneReportAgent    = "pane.report_agent"
	methodPaneReportMetadata = "pane.report_metadata"
	methodPaneReleaseAgent   = "pane.release_agent"

	socketFileName = "herdr.sock"

	// agentLabel is the name herdr lists octify's pane under.
	agentLabel = "octify"
	// reportSource identifies octify among the things that may report on a
	// pane. herdr documents the "custom:" prefix for anything that is not one
	// of its own integrations.
	reportSource = "custom:octify"
)

// The wire format is newline-delimited JSON over the local socket: one object
// per line, no length prefix and no handshake.

type request struct {
	ID     string `json:"id"`
	Method string `json:"method"`
	// Params is one of the parameter types below, chosen by Method.
	Params any `json:"params"`
}

// showParams deliberately carries no position field. Where a toast appears is
// part of the herdr configuration the user already made, not octify's decision.
type showParams struct {
	Title string `json:"title"`
	Body  string `json:"body,omitempty"`
	Sound Sound  `json:"sound"`
}

type reportAgentParams struct {
	PaneID string `json:"pane_id"`
	Source string `json:"source"`
	Agent  string `json:"agent"`
	State  State  `json:"state"`
	Seq    uint64 `json:"seq"`
}

// reportMetadataParams carries no tokens and no ttl_ms. The count belongs in
// the title, and an expiry would drop the title while leaving the state behind,
// which reads as "blocked, count unknown".
type reportMetadataParams struct {
	PaneID string `json:"pane_id"`
	Source string `json:"source"`
	Agent  string `json:"agent"`
	Title  string `json:"title"`
	Seq    uint64 `json:"seq"`
}

type releaseAgentParams struct {
	PaneID string `json:"pane_id"`
	Source string `json:"source"`
	Agent  string `json:"agent"`
	Seq    uint64 `json:"seq"`
}

// response keeps the result undecoded because each method answers with a
// different shape and only notification.show has anything octify reads.
type response struct {
	ID     string          `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  *errorBody      `json:"error"`
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
