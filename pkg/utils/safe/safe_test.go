package safe_test

import (
	"bytes"
	"io"
	"log/slog"
	"testing"

	"github.com/m-mizutani/goerr/v2"
	"github.com/m-mizutani/gt"
	"github.com/m-mizutani/octify/pkg/utils/logging"
	"github.com/m-mizutani/octify/pkg/utils/safe"
)

type stubCloser struct {
	err    error
	closed int
}

func (c *stubCloser) Close() error {
	c.closed++
	return c.err
}

func TestCloseIgnoresNil(t *testing.T) {
	var closer io.Closer
	safe.Close(t.Context(), closer)
}

func TestCloseClosesOnce(t *testing.T) {
	c := &stubCloser{}
	safe.Close(t.Context(), c)
	gt.Equal(t, c.closed, 1)
}

func TestCloseLogsFailure(t *testing.T) {
	var buf bytes.Buffer
	logger := logging.New(logging.Config{Writer: &buf, Level: slog.LevelDebug, Format: logging.FormatJSON})
	ctx := logging.With(t.Context(), logger)

	safe.Close(ctx, &stubCloser{err: goerr.New("device is busy")})

	gt.S(t, buf.String()).Contains("failed to close resource")
	gt.S(t, buf.String()).Contains("device is busy")
}

func TestCloseSuccessIsSilent(t *testing.T) {
	var buf bytes.Buffer
	logger := logging.New(logging.Config{Writer: &buf, Level: slog.LevelDebug, Format: logging.FormatJSON})

	safe.Close(logging.With(t.Context(), logger), &stubCloser{})
	gt.Equal(t, buf.Len(), 0)
}
