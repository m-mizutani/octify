package main

import (
	"context"
	"errors"
	"os"
	"os/signal"
	"syscall"

	"github.com/m-mizutani/octify/pkg/cli"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := cli.Run(ctx, os.Args); err != nil {
		// Ctrl+C and SIGTERM are how a person leaves this program, not failures
		// to report. Anything else is.
		if errors.Is(err, context.Canceled) {
			return
		}
		cli.Report(os.Stderr, err)
		os.Exit(1)
	}
}
