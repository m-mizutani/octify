package cli

import (
	"context"
	"io"

	"github.com/m-mizutani/octify/pkg/tui"
	"github.com/m-mizutani/octify/pkg/usecase"
	ucli "github.com/urfave/cli/v3"
)

// ConfigForTest parses argv through the same flag definitions Run uses and
// returns the configuration the command would hand to the use case layer.
func ConfigForTest(ctx context.Context, argv []string) (usecase.Config, error) {
	var opt options
	var got usecase.Config

	cmd := &ucli.Command{
		Name:  "octify",
		Flags: opt.flags(),
		Action: func(context.Context, *ucli.Command) error {
			got = opt.usecaseConfig()
			return nil
		},
	}
	if err := cmd.Run(ctx, argv); err != nil {
		return usecase.Config{}, err
	}
	return got, nil
}

// AnnounceForTest reports whether the given argv, in the environment the test
// has set up, leaves the TUI somewhere to send desktop notifications.
func AnnounceForTest(ctx context.Context, argv []string) (bool, error) {
	var opt options
	var got bool

	cmd := &ucli.Command{
		Name:  "octify",
		Flags: opt.flags(),
		Action: func(context.Context, *ucli.Command) error {
			got = opt.herdrLink().announceFunc() != nil
			return nil
		},
	}
	if err := cmd.Run(ctx, argv); err != nil {
		return false, err
	}
	return got, nil
}

// Link is one run's connection to the workspace, as runTUI builds it. The
// reporter and the withdrawal share it, which is what makes them ordered
// against each other.
type Link struct {
	Report  func(ctx context.Context, seq uint64, activity tui.Activity, unread int) error
	Release func(ctx context.Context)
	Toast   func(ctx context.Context, title, body string) error
}

// LinkForTest builds the workspace link for the given argv in the environment
// the test has set up.
func LinkForTest(ctx context.Context, argv []string) (Link, error) {
	var opt options
	var got Link

	cmd := &ucli.Command{
		Name:  "octify",
		Flags: opt.flags(),
		Action: func(context.Context, *ucli.Command) error {
			link := opt.herdrLink()
			got = Link{
				Report:  link.reportFunc(),
				Release: link.release,
				Toast:   link.announceFunc(),
			}
			return nil
		},
	}
	if err := cmd.Run(ctx, argv); err != nil {
		return Link{}, err
	}
	return got, nil
}

// HerdrStatus exposes the translation from what octify is doing into what herdr
// shows beside the pane.
var HerdrStatus = herdrStatus

// Paths reports where the three files land for the given argv, which is the
// other half of what the flags decide.
type Paths struct {
	Credential string
	State      string
	Cache      string
}

func PathsForTest(ctx context.Context, argv []string) (Paths, error) {
	var opt options
	var got Paths
	var pathErr error

	cmd := &ucli.Command{
		Name:  "octify",
		Flags: opt.flags(),
		Action: func(context.Context, *ucli.Command) error {
			got.Credential, got.State, got.Cache, pathErr = opt.resolvePaths()
			return nil
		},
	}
	if err := cmd.Run(ctx, argv); err != nil {
		return Paths{}, err
	}
	return got, pathErr
}

// BuildForTest assembles the use case the way a real command does, so a test
// can observe which stores the flags wired into it.
func BuildForTest(ctx context.Context, argv []string) (*usecase.UseCase, error) {
	var opt options
	var got *usecase.UseCase
	var buildErr error

	cmd := &ucli.Command{
		Name:  "octify",
		Flags: opt.flags(),
		Action: func(ctx context.Context, _ *ucli.Command) error {
			var closer io.Closer
			_, got, closer, buildErr = opt.build(ctx, true)
			if closer != nil {
				_ = closer.Close()
			}
			return nil
		},
	}
	if err := cmd.Run(ctx, argv); err != nil {
		return nil, err
	}
	return got, buildErr
}
