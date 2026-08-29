package browser

import (
	"context"
	"os/exec"
	"runtime"

	"github.com/m-mizutani/goerr/v2"
	"github.com/m-mizutani/octify/pkg/domain/model"
)

// runner executes the launch command. It is a variable so that tests can drive
// Open without starting a real browser.
var runner = func(ctx context.Context, name string, args ...string) error {
	return exec.CommandContext(ctx, name, args...).Run()
}

// Open launches the platform's default browser for url.
func Open(ctx context.Context, url string) error {
	name, args := command(url)
	if name == "" {
		return model.WithUserMessage(
			goerr.New("unsupported platform for opening a browser", goerr.V("goos", runtime.GOOS)),
			model.UserMessage{Summary: "could not open the browser", Action: url},
		)
	}

	if err := runner(ctx, name, args...); err != nil {
		return model.WithUserMessage(
			goerr.Wrap(err, "browser command failed", goerr.V("command", name), goerr.V("url", url)),
			model.UserMessage{Summary: "could not open the browser", Action: url},
		)
	}
	return nil
}

// command returns the launcher for the current platform, or an empty name when
// there is none.
func command(url string) (string, []string) {
	switch runtime.GOOS {
	case "darwin":
		return "open", []string{url}
	case "windows":
		return "rundll32", []string{"url.dll,FileProtocolHandler", url}
	case "linux", "freebsd", "openbsd", "netbsd":
		return "xdg-open", []string{url}
	default:
		return "", nil
	}
}
