package herdr_test

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/m-mizutani/gt"
	"github.com/m-mizutani/octify/pkg/infra/herdr"
)

// startServer runs a stand-in for the herdr server on a real unix socket. It
// reads one line per connection and hands it to handle, which decides what the
// caller sees.
func startServer(t *testing.T, handle func(conn net.Conn, line []byte)) string {
	t.Helper()

	path := socketPath(t)
	ln, err := net.Listen("unix", path)
	gt.NoError(t, err).Required()

	// Cleanups run last registered first, so waiting for the accept loop is
	// registered before the close that lets it end.
	done := make(chan struct{})
	t.Cleanup(func() { <-done })
	t.Cleanup(func() { _ = ln.Close() })

	go func() {
		defer close(done)
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			// Each connection is served on its own goroutine so that a handler
			// which deliberately never answers cannot stall the accept loop.
			go func() {
				defer func() { _ = conn.Close() }()
				sc := bufio.NewScanner(conn)
				if !sc.Scan() {
					return
				}
				handle(conn, sc.Bytes())
			}()
		}
	}()

	return path
}

// socketPath keeps the socket well inside the length a sockaddr_un can hold,
// which the test's own name would otherwise push it past.
func socketPath(t *testing.T) string {
	t.Helper()

	dir, err := os.MkdirTemp("", "oct")
	gt.NoError(t, err).Required()
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	return filepath.Join(dir, "h.sock")
}

func reply(conn net.Conn, line string) {
	_, _ = io.WriteString(conn, line+"\n")
}

// session is a herdr session complete enough to report with. Tests that need a
// session without a pane build one themselves.
func session(socket string) herdr.Session {
	return herdr.Session{Socket: socket, PaneID: "w1:p1"}
}

const okReply = `{"id":"octify-1","result":{"type":"ok"}}`

// TestLive exercises the herdr server this process is running inside. Every
// other test here talks to a stand-in written against the same assumptions
// octify's client makes, so this is the only check that the requests are ones
// the real server accepts.
//
// It draws a toast and briefly lists this pane as octify, so it is opt-in.
func TestLive(t *testing.T) {
	if os.Getenv("TEST_HERDR_LIVE") == "" {
		t.Skip("set TEST_HERDR_LIVE to reach the running herdr server")
	}

	sess, ok := herdr.Detect()
	if !ok {
		t.Fatal("TEST_HERDR_LIVE is set, but this process is not inside a herdr session")
	}
	t.Logf("socket: %s, pane: %s", sess.Socket, sess.PaneID)

	c := herdr.New(sess)
	gt.NoError(t, c.Show(t.Context(),
		"octify · m-mizutani/octify", "live check of the herdr bridge"))

	if sess.PaneID == "" {
		t.Skip("this session carries no pane id, so reporting cannot be checked")
	}

	// Report and then withdraw, so the pane is left as this test found it.
	gt.NoError(t, c.Report(t.Context(), 1, herdr.StateBlocked, "12 unread"))
	gt.NoError(t, c.Release(t.Context(), 2))
}

func TestClientShowSendsTheRequest(t *testing.T) {
	got := make(chan []byte, 1)
	path := startServer(t, func(conn net.Conn, line []byte) {
		got <- append([]byte(nil), line...)
		reply(conn, `{"id":"octify-1","result":{"type":"notification_show","shown":true,"reason":"shown"}}`)
	})

	c := herdr.New(session(path), herdr.WithSound(herdr.SoundDone))
	gt.NoError(t, c.Show(context.Background(), "octify · m-mizutani/octify", "Add a herdr bridge"))

	var sent map[string]any
	gt.NoError(t, json.Unmarshal(<-got, &sent)).Required()

	gt.Equal(t, "notification.show", sent["method"])
	gt.Equal(t, "octify-1", sent["id"])

	params := gt.Cast[map[string]any](t, sent["params"])
	gt.Equal(t, "octify · m-mizutani/octify", params["title"])
	gt.Equal(t, "Add a herdr bridge", params["body"])
	gt.Equal(t, "done", params["sound"])

	// The toast's placement belongs to the herdr config, so octify must not
	// send one.
	_, hasPosition := params["position"]
	gt.False(t, hasPosition)
}

func TestClientShowAcceptsAToastTheServerDidNotDraw(t *testing.T) {
	path := startServer(t, func(conn net.Conn, _ []byte) {
		reply(conn, `{"id":"octify-1","result":{"type":"notification_show","shown":false,"reason":"disabled"}}`)
	})

	gt.NoError(t, herdr.New(session(path)).Show(context.Background(), "octify", "body"))
}

func TestClientShowFailures(t *testing.T) {
	t.Run("the server answers with an error body", func(t *testing.T) {
		path := startServer(t, func(conn net.Conn, _ []byte) {
			reply(conn, `{"id":"octify-1","error":{"code":"busy","message":"a modal is open"}}`)
		})

		err := herdr.New(session(path)).Show(context.Background(), "octify", "body")
		gt.Error(t, err).Is(herdr.ErrRequestFailed)
		gt.S(t, err.Error()).Contains("herdr refused the request")
	})

	t.Run("the server answers with a line that is not JSON", func(t *testing.T) {
		path := startServer(t, func(conn net.Conn, _ []byte) {
			reply(conn, `not json at all`)
		})

		err := herdr.New(session(path)).Show(context.Background(), "octify", "body")
		gt.Error(t, err)
		gt.S(t, err.Error()).Contains("failed to decode the herdr response")
	})

	t.Run("the server answers with a result that is not a notification result", func(t *testing.T) {
		path := startServer(t, func(conn net.Conn, _ []byte) {
			reply(conn, `{"id":"octify-1","result":"broken"}`)
		})

		// Nothing in an unreadable result says the toast was accepted, so this
		// is a failure to ask rather than a silent success.
		err := herdr.New(session(path)).Show(context.Background(), "octify", "body")
		gt.Error(t, err)
		gt.S(t, err.Error()).Contains("failed to decode the notification.show result")
	})

	t.Run("the server closes without answering", func(t *testing.T) {
		path := startServer(t, func(net.Conn, []byte) {})

		gt.Error(t, herdr.New(session(path)).Show(context.Background(), "octify", "body")).Is(herdr.ErrNoResponse)
	})

	t.Run("the server answers with a line past the size limit", func(t *testing.T) {
		path := startServer(t, func(conn net.Conn, _ []byte) {
			reply(conn, strings.Repeat("a", 70*1024))
		})

		err := herdr.New(session(path)).Show(context.Background(), "octify", "body")
		gt.Error(t, err)
		// A line too long to read is a read failure, not a silent server.
		gt.False(t, errors.Is(err, herdr.ErrNoResponse))
		gt.S(t, err.Error()).Contains("failed to read the herdr response")
	})

	t.Run("there is no socket to dial", func(t *testing.T) {
		err := herdr.New(session(filepath.Join(t.TempDir(), "absent.sock"))).Show(context.Background(), "octify", "body")
		gt.Error(t, err)
		gt.S(t, err.Error()).Contains("failed to dial the herdr socket")
	})

	t.Run("the server never answers", func(t *testing.T) {
		release := make(chan struct{})
		t.Cleanup(func() { close(release) })

		path := startServer(t, func(net.Conn, []byte) { <-release })

		start := time.Now()
		err := herdr.New(session(path), herdr.WithTimeout(200*time.Millisecond)).
			Show(context.Background(), "octify", "body")
		gt.Error(t, err)
		gt.True(t, time.Since(start) < 5*time.Second)
	})

	t.Run("the context is already cancelled", func(t *testing.T) {
		path := startServer(t, func(conn net.Conn, _ []byte) {
			reply(conn, `{"id":"octify-1","result":{"type":"notification_show","shown":true,"reason":"shown"}}`)
		})

		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		gt.Error(t, herdr.New(session(path)).Show(ctx, "octify", "body"))
	})

	t.Run("the context is cancelled once the connection is open", func(t *testing.T) {
		// net.Pipe carries no buffer, so the request blocks until something
		// releases it. With an hour-long timeout the only thing that can is the
		// cancellation, which is exactly what this pins down: a caller that
		// gives up must not leave a goroutine on the connection.
		client, server := net.Pipe()
		t.Cleanup(func() { _ = server.Close() })

		ctx, cancel := context.WithCancel(context.Background())

		done := make(chan error, 1)
		go func() {
			done <- herdr.New(session("unused"),
				herdr.WithTimeout(time.Hour),
				herdr.WithDialer(func(context.Context, string) (net.Conn, error) { return client, nil }),
			).Show(ctx, "octify", "body")
		}()

		cancel()
		gt.Error(t, <-done)
	})
}

// collectServer answers every request with a bare success and keeps the lines
// it was sent, so a test can look at all of them once the call returns.
func collectServer(t *testing.T, lines chan<- []byte) string {
	t.Helper()
	return startServer(t, func(conn net.Conn, line []byte) {
		lines <- append([]byte(nil), line...)
		reply(conn, okReply)
	})
}

func decode(t *testing.T, line []byte) (method string, params map[string]any) {
	t.Helper()

	var sent map[string]any
	gt.NoError(t, json.Unmarshal(line, &sent)).Required()
	return gt.Cast[string](t, sent["method"]), gt.Cast[map[string]any](t, sent["params"])
}

func TestClientReportSendsTheStateThenTheTitle(t *testing.T) {
	lines := make(chan []byte, 2)
	path := collectServer(t, lines)

	gt.NoError(t, herdr.New(session(path)).Report(context.Background(), 7, herdr.StateBlocked, "12 unread"))

	method, params := decode(t, <-lines)
	gt.Equal(t, "pane.report_agent", method)
	gt.Equal(t, "w1:p1", params["pane_id"])
	gt.Equal(t, "custom:octify", params["source"])
	gt.Equal(t, "octify", params["agent"])
	gt.Equal(t, "blocked", params["state"])
	gt.Equal(t, float64(7), gt.Cast[float64](t, params["seq"]))

	method, params = decode(t, <-lines)
	gt.Equal(t, "pane.report_metadata", method)
	gt.Equal(t, "w1:p1", params["pane_id"])
	gt.Equal(t, "custom:octify", params["source"])
	gt.Equal(t, "octify", params["agent"])
	gt.Equal(t, "12 unread", params["title"])
	gt.Equal(t, float64(7), gt.Cast[float64](t, params["seq"]))

	// An expiry would drop the title and leave the state, and the count belongs
	// in the title rather than in a token the sidebar has to be told about.
	_, hasTTL := params["ttl_ms"]
	gt.False(t, hasTTL)
	_, hasTokens := params["tokens"]
	gt.False(t, hasTokens)
}

func TestClientRelease(t *testing.T) {
	lines := make(chan []byte, 1)
	path := collectServer(t, lines)

	gt.NoError(t, herdr.New(session(path)).Release(context.Background(), 1))

	method, params := decode(t, <-lines)
	gt.Equal(t, "pane.release_agent", method)
	gt.Equal(t, "w1:p1", params["pane_id"])
	gt.Equal(t, "custom:octify", params["source"])
	gt.Equal(t, "octify", params["agent"])
	gt.Equal(t, float64(1), gt.Cast[float64](t, params["seq"]))
}

func TestClientReportFailures(t *testing.T) {
	t.Run("the state is refused, so no title follows it", func(t *testing.T) {
		lines := make(chan []byte, 2)
		path := startServer(t, func(conn net.Conn, line []byte) {
			lines <- append([]byte(nil), line...)
			reply(conn, `{"id":"octify-1","error":{"code":"not_found","message":"no such pane"}}`)
		})

		err := herdr.New(session(path)).Report(context.Background(), 1, herdr.StateIdle, "0 unread")
		gt.Error(t, err).Is(herdr.ErrRequestFailed)

		method, _ := decode(t, <-lines)
		gt.Equal(t, "pane.report_agent", method)
		gt.Equal(t, 0, len(lines))
	})

	t.Run("the title is refused after the state was accepted", func(t *testing.T) {
		var seen int
		path := startServer(t, func(conn net.Conn, _ []byte) {
			seen++
			if seen == 1 {
				reply(conn, okReply)
				return
			}
			reply(conn, `{"id":"octify-2","error":{"code":"busy","message":"try again"}}`)
		})

		err := herdr.New(session(path)).Report(context.Background(), 1, herdr.StateIdle, "0 unread")
		gt.Error(t, err).Is(herdr.ErrRequestFailed)
	})

	t.Run("a session without a pane cannot report or release", func(t *testing.T) {
		// The socket does not exist, which proves the guard runs before the dial.
		c := herdr.New(herdr.Session{Socket: filepath.Join(t.TempDir(), "absent.sock")})

		gt.Error(t, c.Report(context.Background(), 1, herdr.StateIdle, "0 unread")).Is(herdr.ErrNoPane)
		gt.Error(t, c.Release(context.Background(), 1)).Is(herdr.ErrNoPane)
	})

	t.Run("a state herdr does not accept is refused before the dial", func(t *testing.T) {
		c := herdr.New(herdr.Session{
			Socket: filepath.Join(t.TempDir(), "absent.sock"),
			PaneID: "w1:p1",
		})

		gt.Error(t, c.Report(context.Background(), 1, herdr.State("done"), "0 unread")).Is(herdr.ErrInvalidState)
	})

	t.Run("there is no socket to dial", func(t *testing.T) {
		c := herdr.New(session(filepath.Join(t.TempDir(), "absent.sock")))

		err := c.Report(context.Background(), 1, herdr.StateIdle, "0 unread")
		gt.Error(t, err)
		gt.S(t, err.Error()).Contains("failed to dial the herdr socket")
	})
}

func TestSanitize(t *testing.T) {
	t.Run("control characters become spaces", func(t *testing.T) {
		gt.Equal(t, "a b c", herdr.Sanitize("a\nb\tc", herdr.MaxTitleRunes))
	})

	t.Run("a string within the limit is untouched", func(t *testing.T) {
		gt.Equal(t, "Add a herdr bridge", herdr.Sanitize("Add a herdr bridge", herdr.MaxTitleRunes))
	})

	t.Run("surrounding spaces within the limit are left alone", func(t *testing.T) {
		gt.Equal(t, "  padded  ", herdr.Sanitize("  padded  ", herdr.MaxTitleRunes))
	})

	t.Run("a string of exactly the limit is untouched", func(t *testing.T) {
		exact := strings.Repeat("a", herdr.MaxBodyRunes)
		gt.Equal(t, exact, herdr.Sanitize(exact, herdr.MaxBodyRunes))
	})

	t.Run("one rune past the limit is cut", func(t *testing.T) {
		got := herdr.Sanitize(strings.Repeat("a", herdr.MaxBodyRunes+1), herdr.MaxBodyRunes)
		gt.Equal(t, herdr.MaxBodyRunes, len([]rune(got)))
		gt.S(t, got).HasSuffix("…")
	})

	t.Run("a string past the limit ends in an ellipsis", func(t *testing.T) {
		got := herdr.Sanitize(strings.Repeat("a", herdr.MaxBodyRunes+50), herdr.MaxBodyRunes)
		gt.Equal(t, herdr.MaxBodyRunes, len([]rune(got)))
		gt.S(t, got).HasSuffix("…")
	})

	t.Run("a limit of one leaves room for the ellipsis alone", func(t *testing.T) {
		gt.Equal(t, "…", herdr.Sanitize("abc", 1))
	})

	t.Run("multibyte characters are counted and cut by rune", func(t *testing.T) {
		got := herdr.Sanitize(strings.Repeat("通知", 200), 10)
		gt.Equal(t, "通知通知通知通知通…", got)
		gt.Equal(t, 10, len([]rune(got)))
	})
}
