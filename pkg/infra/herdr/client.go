package herdr

import (
	"bufio"
	"context"
	"encoding/json"
	"log/slog"
	"net"
	"strconv"
	"sync/atomic"
	"time"
	"unicode"

	"github.com/m-mizutani/goerr/v2"
	"github.com/m-mizutani/octify/pkg/utils/logging"
	"github.com/m-mizutani/octify/pkg/utils/safe"
)

var (
	// ErrRequestFailed is the server answering with an error body instead of a
	// result.
	ErrRequestFailed = goerr.New("herdr: the server refused the request")
	// ErrNoResponse is the server closing the connection without a line.
	ErrNoResponse = goerr.New("herdr: the server closed without answering")
	// ErrNoPane is a report attempted for a session that named no pane.
	ErrNoPane = goerr.New("herdr: no pane id in this session")
)

// Client sends toasts to one herdr session.
//
// Each call opens its own connection and closes it again. A toast goes out at
// most once per polling cycle, so holding a connection open between them would
// only add reconnection and liveness state for no gain; the protocol needs no
// handshake, so a fresh connection is ready to use immediately.
//
// The zero value is not usable; call New.
type Client struct {
	sess    Session
	sound   Sound
	timeout time.Duration
	dial    func(ctx context.Context, path string) (net.Conn, error)

	// seq numbers requests so a log line can be matched with an answer. Ids do
	// not have to be unique across connections, but they cost nothing here.
	seq atomic.Uint64
}

type Option func(*Client)

// WithSound picks the audio herdr plays. A value outside the three the server
// accepts is ignored, leaving the client silent.
func WithSound(s Sound) Option {
	return func(c *Client) {
		if s.Validate() == nil {
			c.sound = s
		}
	}
}

// WithTimeout replaces the deadline covering the dial, the write and the read.
func WithTimeout(d time.Duration) Option {
	return func(c *Client) {
		if d > 0 {
			c.timeout = d
		}
	}
}

// WithDialer replaces the socket dialer so tests do not need a real herdr.
func WithDialer(fn func(ctx context.Context, path string) (net.Conn, error)) Option {
	return func(c *Client) {
		if fn != nil {
			c.dial = fn
		}
	}
}

func New(sess Session, opts ...Option) *Client {
	c := &Client{
		sess:    sess,
		sound:   SoundNone,
		timeout: defaultTimeout,
		dial:    dialUnix,
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// CanReport reports whether this session named a pane to report for. Toasts
// work without one; reports do not.
func (c *Client) CanReport() bool { return c.sess.PaneID != "" }

func dialUnix(ctx context.Context, path string) (net.Conn, error) {
	var d net.Dialer
	return d.DialContext(ctx, "unix", path)
}

// Show sends one toast and waits for the server's answer.
//
// A toast the server declined to draw — because toasts are switched off, or the
// rate limit was hit, or no client is in the foreground — is not an error. Only
// a failure to ask is.
func (c *Client) Show(ctx context.Context, title, body string) error {
	resp, err := c.call(ctx, methodNotificationShow, showParams{
		Title: sanitize(title, maxTitleRunes),
		Body:  sanitize(body, maxBodyRunes),
		Sound: c.sound,
	})
	if err != nil {
		return err
	}

	// The reason is the only result octify reads, and only to record in the log
	// why a toast the server accepted never reached the screen. A result that
	// cannot be read at all is still a failure to ask, because nothing then says
	// the toast was accepted.
	var result showResult
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		return goerr.Wrap(err, "failed to decode the notification.show result")
	}
	if !result.Shown {
		logging.From(ctx).Debug("herdr did not draw the notification",
			slog.String("reason", result.Reason))
	}
	return nil
}

// Report tells herdr that this pane holds octify, in what state, and what to
// show beside it.
//
// seq orders reports against each other. They are sent concurrently and may
// arrive out of order, and the order that matters is the one the caller
// produced them in rather than the one they reach the socket in.
//
// The state is sent first and the title second, so a state the server refuses
// stops the call before a title describes a state that was never accepted.
func (c *Client) Report(ctx context.Context, seq uint64, state State, title string) error {
	if err := state.Validate(); err != nil {
		return err
	}
	if c.sess.PaneID == "" {
		return goerr.Wrap(ErrNoPane, "cannot report an agent")
	}

	if _, err := c.call(ctx, methodPaneReportAgent, reportAgentParams{
		PaneID: c.sess.PaneID,
		Source: reportSource,
		Agent:  agentLabel,
		State:  state,
		Seq:    seq,
	}); err != nil {
		return err
	}

	_, err := c.call(ctx, methodPaneReportMetadata, reportMetadataParams{
		PaneID: c.sess.PaneID,
		Source: reportSource,
		Agent:  agentLabel,
		Title:  sanitize(title, maxTitleRunes),
		Seq:    seq,
	})
	return err
}

// Release withdraws the report so the pane stops being listed as octify.
//
// herdr cannot notice on its own that the process which reported an agent has
// gone: the report arrives over a connection that closes immediately after it,
// and nothing in it identifies a process. Withdrawing is the only way the entry
// leaves the list short of the pane itself closing.
//
// seq must be higher than every seq already reported, so that a report the
// server has not applied yet cannot outrank the withdrawal and put a finished
// octify back in the list.
func (c *Client) Release(ctx context.Context, seq uint64) error {
	if c.sess.PaneID == "" {
		return goerr.Wrap(ErrNoPane, "cannot release an agent")
	}

	_, err := c.call(ctx, methodPaneReleaseAgent, releaseAgentParams{
		PaneID: c.sess.PaneID,
		Source: reportSource,
		Agent:  agentLabel,
		Seq:    seq,
	})
	return err
}

// call sends one request and reads the single line that answers it.
func (c *Client) call(ctx context.Context, method string, params any) (*response, error) {
	req := request{
		ID:     "octify-" + strconv.FormatUint(c.seq.Add(1), 10),
		Method: method,
		Params: params,
	}

	// The effective deadline is the earlier of this client's own and any the
	// caller already carried.
	reqCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	conn, err := c.dial(reqCtx, c.sess.Socket)
	if err != nil {
		return nil, goerr.Wrap(err, "failed to dial the herdr socket", goerr.V("path", c.sess.Socket))
	}
	defer safe.Close(ctx, conn)

	if deadline, ok := reqCtx.Deadline(); ok {
		if err := conn.SetDeadline(deadline); err != nil {
			return nil, goerr.Wrap(err, "failed to set the herdr socket deadline")
		}
	}

	// A cancellation after the dial has to reach the connection as well: the
	// deadline alone would hold this goroutine on the read for the rest of the
	// timeout after octify has already quit. Registered after the deadline
	// above so that an already-cancelled context is not overwritten, and
	// released before it on the way out.
	stop := context.AfterFunc(reqCtx, func() { _ = conn.SetDeadline(time.Now()) })
	defer stop()

	// Encode writes a trailing newline, which is exactly the framing the server
	// expects.
	if err := json.NewEncoder(conn).Encode(req); err != nil {
		return nil, goerr.Wrap(err, "failed to send the herdr request",
			goerr.V("method", method), goerr.V("request_id", req.ID))
	}

	resp, err := readResponse(conn)
	if err != nil {
		return nil, err
	}
	if resp.Error != nil {
		return nil, goerr.Wrap(ErrRequestFailed, "herdr refused the request",
			goerr.V("method", method),
			goerr.V("request_id", req.ID),
			goerr.V("code", resp.Error.Code),
			goerr.V("message", resp.Error.Message))
	}
	return resp, nil
}

func readResponse(conn net.Conn) (*response, error) {
	sc := bufio.NewScanner(conn)
	sc.Buffer(make([]byte, 0, 4096), maxResponseBytes)

	if !sc.Scan() {
		if err := sc.Err(); err != nil {
			return nil, goerr.Wrap(err, "failed to read the herdr response")
		}
		return nil, ErrNoResponse
	}

	var resp response
	if err := json.Unmarshal(sc.Bytes(), &resp); err != nil {
		return nil, goerr.Wrap(err, "failed to decode the herdr response")
	}
	return &resp, nil
}

// sanitize makes one field safe to put on a single JSON line and short enough
// to read in a toast: control characters become spaces, and anything past max
// runes is replaced by an ellipsis. A field already within the limit and free
// of control characters comes back untouched, surrounding spaces included:
// trimming them would be an edit no caller asked for.
//
// Counting is by rune rather than by byte so that a title in Japanese is cut
// between characters instead of through one.
func sanitize(s string, max int) string {
	if s == "" || max < 1 {
		return ""
	}

	out := make([]rune, 0, len(s))
	for _, r := range s {
		if unicode.IsControl(r) {
			out = append(out, ' ')
			continue
		}
		out = append(out, r)
	}

	if len(out) > max {
		out = append(out[:max-1], '…')
	}
	return string(out)
}
