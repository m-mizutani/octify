package async_test

import (
	"bytes"
	"context"
	"log/slog"
	"testing"

	"github.com/m-mizutani/gt"
	"github.com/m-mizutani/octify/pkg/utils/async"
	"github.com/m-mizutani/octify/pkg/utils/logging"
)

func TestGoRunsTheFunction(t *testing.T) {
	var got string
	done := async.Go(t.Context(), func(ctx context.Context) {
		got = "ran"
	})

	<-done
	gt.Equal(t, got, "ran")
}

func TestGoPassesTheContext(t *testing.T) {
	type key struct{}
	ctx := context.WithValue(t.Context(), key{}, "value")

	var got any
	<-async.Go(ctx, func(ctx context.Context) {
		got = ctx.Value(key{})
	})

	gt.Equal(t, got, "value")
}

func TestGoRecoversPanic(t *testing.T) {
	var buf bytes.Buffer
	logger := logging.New(logging.Config{Writer: &buf, Level: slog.LevelDebug, Format: logging.FormatJSON})
	ctx := logging.With(t.Context(), logger)

	// A panic here must not reach the test runner.
	<-async.Go(ctx, func(ctx context.Context) {
		panic("boom")
	})

	gt.S(t, buf.String()).Contains("background task panicked")
	gt.S(t, buf.String()).Contains("boom")
}

func TestGoWithoutLoggerDoesNotPanic(t *testing.T) {
	<-async.Go(t.Context(), func(ctx context.Context) {
		panic("boom")
	})
}
