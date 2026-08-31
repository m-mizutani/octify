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

func TestClientShowSendsTheRequest(t *testing.T) {
	got := make(chan []byte, 1)
	path := startServer(t, func(conn net.Conn, line []byte) {
		got <- append([]byte(nil), line...)
		reply(conn, `{"id":"octify-1","result":{"type":"notification_show","shown":true,"reason":"shown"}}`)
	})

	c := herdr.New(path, herdr.WithSound(herdr.SoundDone))
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

	gt.NoError(t, herdr.New(path).Show(context.Background(), "octify", "body"))
}

func TestClientShowFailures(t *testing.T) {
	t.Run("the server answers with an error body", func(t *testing.T) {
		path := startServer(t, func(conn net.Conn, _ []byte) {
			reply(conn, `{"id":"octify-1","error":{"code":"busy","message":"a modal is open"}}`)
		})

		err := herdr.New(path).Show(context.Background(), "octify", "body")
		gt.Error(t, err).Is(herdr.ErrRequestFailed)
		gt.S(t, err.Error()).Contains("herdr refused notification.show")
	})

	t.Run("the server answers with a line that is not JSON", func(t *testing.T) {
		path := startServer(t, func(conn net.Conn, _ []byte) {
			reply(conn, `not json at all`)
		})

		err := herdr.New(path).Show(context.Background(), "octify", "body")
		gt.Error(t, err)
		gt.S(t, err.Error()).Contains("failed to decode the herdr response")
	})

	t.Run("the server closes without answering", func(t *testing.T) {
		path := startServer(t, func(net.Conn, []byte) {})

		gt.Error(t, herdr.New(path).Show(context.Background(), "octify", "body")).Is(herdr.ErrNoResponse)
	})

	t.Run("the server answers with a line past the size limit", func(t *testing.T) {
		path := startServer(t, func(conn net.Conn, _ []byte) {
			reply(conn, strings.Repeat("a", 70*1024))
		})

		err := herdr.New(path).Show(context.Background(), "octify", "body")
		gt.Error(t, err)
		// A line too long to read is a read failure, not a silent server.
		gt.False(t, errors.Is(err, herdr.ErrNoResponse))
		gt.S(t, err.Error()).Contains("failed to read the herdr response")
	})

	t.Run("there is no socket to dial", func(t *testing.T) {
		err := herdr.New(filepath.Join(t.TempDir(), "absent.sock")).Show(context.Background(), "octify", "body")
		gt.Error(t, err)
		gt.S(t, err.Error()).Contains("failed to dial the herdr socket")
	})

	t.Run("the server never answers", func(t *testing.T) {
		release := make(chan struct{})
		t.Cleanup(func() { close(release) })

		path := startServer(t, func(net.Conn, []byte) { <-release })

		start := time.Now()
		err := herdr.New(path, herdr.WithTimeout(200*time.Millisecond)).
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

		gt.Error(t, herdr.New(path).Show(ctx, "octify", "body"))
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
			done <- herdr.New("unused",
				herdr.WithTimeout(time.Hour),
				herdr.WithDialer(func(context.Context, string) (net.Conn, error) { return client, nil }),
			).Show(ctx, "octify", "body")
		}()

		cancel()
		gt.Error(t, <-done)
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
