package browser

import "context"

// Command exposes the platform lookup so tests can pin the launcher.
var Command = command

// SetRunner replaces the process launcher for the duration of a test.
func SetRunner(fn func(ctx context.Context, name string, args ...string) error) func() {
	prev := runner
	runner = fn
	return func() { runner = prev }
}
