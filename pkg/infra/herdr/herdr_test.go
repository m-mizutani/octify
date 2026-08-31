package herdr_test

import (
	"testing"

	"github.com/m-mizutani/gt"
	"github.com/m-mizutani/octify/pkg/infra/herdr"
)

func TestSoundValidate(t *testing.T) {
	gt.NoError(t, herdr.SoundNone.Validate())
	gt.NoError(t, herdr.SoundDone.Validate())
	gt.NoError(t, herdr.SoundRequest.Validate())
	gt.Error(t, herdr.Sound("chime").Validate()).Is(herdr.ErrInvalidSound)
	gt.Error(t, herdr.Sound("").Validate()).Is(herdr.ErrInvalidSound)
}

func TestDetect(t *testing.T) {
	testCases := map[string]struct {
		env      map[string]string
		wantPath string
		wantOK   bool
	}{
		"outside herdr entirely": {
			env:    map[string]string{},
			wantOK: false,
		},
		"HERDR_ENV set to something other than 1": {
			env:    map[string]string{"HERDR_ENV": "0", "HERDR_SOCKET_PATH": "/run/herdr.sock"},
			wantOK: false,
		},
		"the socket path a managed pane carries": {
			env: map[string]string{
				"HERDR_ENV":         "1",
				"HERDR_SOCKET_PATH": "/run/user/501/herdr.sock",
			},
			wantPath: "/run/user/501/herdr.sock",
			wantOK:   true,
		},
		"a named session without an explicit socket path": {
			env: map[string]string{
				"HERDR_ENV":       "1",
				"HERDR_SESSION":   "review",
				"XDG_CONFIG_HOME": "/cfg",
				"HOME":            "/home/octo",
			},
			wantPath: "/cfg/herdr/sessions/review/herdr.sock",
			wantOK:   true,
		},
		"the default session without an explicit socket path": {
			env: map[string]string{
				"HERDR_ENV":       "1",
				"XDG_CONFIG_HOME": "/cfg",
				"HOME":            "/home/octo",
			},
			wantPath: "/cfg/herdr/herdr.sock",
			wantOK:   true,
		},
		"HOME is the base when XDG_CONFIG_HOME is unset": {
			env: map[string]string{
				"HERDR_ENV": "1",
				"HOME":      "/home/octo",
			},
			wantPath: "/home/octo/.config/herdr/herdr.sock",
			wantOK:   true,
		},
		"inside herdr but with no way to locate the config directory": {
			env:    map[string]string{"HERDR_ENV": "1"},
			wantOK: false,
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			// Every variable Detect reads is pinned, so the developer's own
			// environment cannot decide the outcome.
			for _, key := range []string{"HERDR_ENV", "HERDR_SOCKET_PATH", "HERDR_SESSION", "XDG_CONFIG_HOME", "HOME"} {
				t.Setenv(key, tc.env[key])
			}

			path, ok := herdr.Detect()
			gt.Equal(t, tc.wantOK, ok)
			gt.Equal(t, tc.wantPath, path)
		})
	}
}
