package browser_test

import (
	"context"
	"runtime"
	"testing"

	"github.com/m-mizutani/goerr/v2"
	"github.com/m-mizutani/gt"
	"github.com/m-mizutani/octify/pkg/domain/model"
	"github.com/m-mizutani/octify/pkg/utils/browser"
)

const targetURL = "https://github.com/m-mizutani/octify/pull/1"

func TestCommandForCurrentPlatform(t *testing.T) {
	name, args := browser.Command(targetURL)

	switch runtime.GOOS {
	case "darwin":
		gt.Equal(t, name, "open")
		gt.A(t, args).Equal([]string{targetURL})
	case "windows":
		gt.Equal(t, name, "rundll32")
		gt.A(t, args).Equal([]string{"url.dll,FileProtocolHandler", targetURL})
	case "linux", "freebsd", "openbsd", "netbsd":
		gt.Equal(t, name, "xdg-open")
		gt.A(t, args).Equal([]string{targetURL})
	default:
		gt.Equal(t, name, "")
	}
}

func TestOpenInvokesTheLauncher(t *testing.T) {
	var gotName string
	var gotArgs []string

	restore := browser.SetRunner(func(ctx context.Context, name string, args ...string) error {
		gotName = name
		gotArgs = args
		return nil
	})
	t.Cleanup(restore)

	gt.NoError(t, browser.Open(t.Context(), targetURL))

	wantName, wantArgs := browser.Command(targetURL)
	gt.Equal(t, gotName, wantName)
	gt.A(t, gotArgs).Equal(wantArgs)
}

func TestOpenReportsLauncherFailure(t *testing.T) {
	restore := browser.SetRunner(func(ctx context.Context, name string, args ...string) error {
		return goerr.New("executable file not found")
	})
	t.Cleanup(restore)

	err := browser.Open(t.Context(), targetURL)
	gt.Error(t, err)

	// The user needs the URL itself so they can copy it by hand.
	msg, ok := model.UserMessageOf(err)
	gt.True(t, ok)
	gt.Equal(t, msg.Summary, "could not open the browser")
	gt.Equal(t, msg.Action, targetURL)
}
