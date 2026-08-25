package async

import (
	"context"
	"log/slog"

	"github.com/m-mizutani/goerr/v2"
	"github.com/m-mizutani/octify/pkg/utils/logging"
)

// Go runs fn in a new goroutine. A panic in fn is recovered and logged instead
// of taking the whole TUI down.
//
// The returned channel is closed once fn has finished, panic or not. Callers
// that do not care may ignore it; tests use it to wait without sleeping.
func Go(ctx context.Context, fn func(ctx context.Context)) <-chan struct{} {
	logger := logging.From(ctx)
	done := make(chan struct{})

	go func() {
		defer close(done)
		defer func() {
			if r := recover(); r != nil {
				err := goerr.New("panic in background task", goerr.V("recovered", r))
				logger.Error("background task panicked", slog.Any("error", err))
			}
		}()
		fn(ctx)
	}()

	return done
}
