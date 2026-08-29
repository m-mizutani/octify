package logging

import (
	"context"
	"io"
	"log/slog"

	"github.com/m-mizutani/clog"
	"github.com/m-mizutani/clog/hooks"
	"github.com/m-mizutani/goerr/v2"
	"github.com/m-mizutani/masq"
	"github.com/m-mizutani/octify/pkg/domain/types"
)

// Format selects how log records are written.
type Format string

const (
	// FormatText is the human readable form produced by clog.
	FormatText Format = "text"
	FormatJSON Format = "json"
)

var ErrInvalidLogFormat = goerr.New("invalid log format")

func (f Format) Validate() error {
	switch f {
	case FormatText, FormatJSON:
		return nil
	default:
		return goerr.Wrap(ErrInvalidLogFormat, "unknown log format", goerr.V("format", string(f)))
	}
}

type Config struct {
	// Writer is where records go. A nil Writer discards everything.
	Writer io.Writer
	Level  slog.Level
	Format Format
	Source bool
}

// New builds a logger with token redaction wired into both formats.
func New(cfg Config) *slog.Logger {
	w := cfg.Writer
	if w == nil {
		w = io.Discard
	}

	// Redact by type first so a token leaks through no attribute, then by field
	// name so a raw string assigned to those fields is caught as well.
	// DeviceCode is included because it can be exchanged for a token while valid.
	// UserCode is not: it is meant to be shown to the user.
	redact := masq.New(
		masq.WithType[types.AccessToken](),
		masq.WithFieldName("AccessToken"),
		masq.WithFieldName("DeviceCode"),
	)

	var handler slog.Handler
	switch cfg.Format {
	case FormatJSON:
		handler = slog.NewJSONHandler(w, &slog.HandlerOptions{
			Level:       cfg.Level,
			AddSource:   cfg.Source,
			ReplaceAttr: redact,
		})
	default:
		handler = clog.New(
			clog.WithWriter(w),
			clog.WithLevel(cfg.Level),
			// The terminal belongs to the TUI, so output always goes to a file.
			clog.WithColor(false),
			clog.WithSource(cfg.Source),
			clog.WithReplaceAttr(redact),
			clog.WithAttrHook(hooks.GoErr(hooks.WithStackTrace(cfg.Source))),
		)
	}

	return slog.New(handler)
}

type ctxKey struct{}

// With stores the logger so that any layer can reach it without threading it
// through every signature.
func With(ctx context.Context, logger *slog.Logger) context.Context {
	return context.WithValue(ctx, ctxKey{}, logger)
}

// From returns the logger stored in ctx, or a discarding one.
func From(ctx context.Context) *slog.Logger {
	if logger, ok := ctx.Value(ctxKey{}).(*slog.Logger); ok && logger != nil {
		return logger
	}
	return discard
}

var discard = slog.New(slog.NewTextHandler(io.Discard, nil))
