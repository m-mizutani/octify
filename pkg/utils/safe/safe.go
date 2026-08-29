package safe

import (
	"context"
	"io"
	"log/slog"

	"github.com/m-mizutani/goerr/v2"
	"github.com/m-mizutani/octify/pkg/utils/logging"
)

// Close closes c when it is not nil and reports a failure to the log. It is for
// deferred cleanup where there is nothing left to do about the error.
func Close(ctx context.Context, c io.Closer) {
	if c == nil {
		return
	}
	if err := c.Close(); err != nil {
		logging.From(ctx).Warn("failed to close resource",
			slog.Any("error", goerr.Wrap(err, "close failed")))
	}
}
