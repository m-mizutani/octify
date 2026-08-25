package cli_test

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/m-mizutani/gt"
	"github.com/m-mizutani/octify/pkg/cli"
	"github.com/m-mizutani/octify/pkg/domain/model"
	"github.com/m-mizutani/octify/pkg/domain/types"
	"github.com/m-mizutani/octify/pkg/infra/gh"
	"github.com/m-mizutani/octify/pkg/infra/readstate"
	"github.com/m-mizutani/octify/pkg/infra/tokenstore"
)

// env points every path and endpoint at test-owned locations so no command
// touches the machine's real configuration or GitHub.
type env struct {
	dir       string
	serverURL string
}

func newEnv(t *testing.T, handler http.HandlerFunc) *env {
	t.Helper()

	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	return &env{dir: dir, serverURL: srv.URL}
}

func (e *env) args(extra ...string) []string {
	base := []string{
		"octify",
		"--client-id", "test-client",
		"--api-base", e.serverURL,
		"--web-base", e.serverURL,
		"--no-keyring",
	}
	return append(base, extra...)
}

func (e *env) statePath() string {
	return filepath.Join(e.dir, "octify", "read-state.json")
}

func (e *env) credentialPath() string {
	return filepath.Join(e.dir, "octify", "credential.json")
}

// run executes one command and captures what the user would see.
func run(t *testing.T, argv []string) (stderr string, err error) {
	t.Helper()

	err = cli.Run(t.Context(), argv)
	if err == nil {
		return "", nil
	}

	var buf bytes.Buffer
	cli.Report(&buf, err)
	return buf.String(), err
}

const notificationBody = `[
  {"id":"1","unread":true,"reason":"review_requested","updated_at":"2026-08-25T08:00:00Z",
   "subject":{"title":"Fix the parser","url":"https://api.github.com/repos/acme/tools/pulls/1","type":"PullRequest"},
   "repository":{"full_name":"acme/tools","html_url":"https://github.com/acme/tools"}}
]`

func githubHandler(t *testing.T, pending *atomic.Int64) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/login/device/code":
			_, _ = w.Write([]byte(`{"device_code":"dc","user_code":"ABCD-EFGH",
			  "verification_uri":"https://github.com/login/device","expires_in":900,"interval":1}`))
		case "/login/oauth/access_token":
			if pending != nil && pending.Add(-1) >= 0 {
				_, _ = w.Write([]byte(`{"error":"authorization_pending"}`))
				return
			}
			_, _ = w.Write([]byte(`{"access_token":"gho_test","token_type":"bearer","scope":"repo,notifications"}`))
		case "/notifications":
			_, _ = w.Write([]byte(notificationBody))
		case "/search/issues":
			_, _ = w.Write([]byte(`{"total_count":0,"incomplete_results":false,"items":[]}`))
		default:
			w.WriteHeader(http.StatusNoContent)
		}
	}
}

func TestAuthLoginThenStatus(t *testing.T) {
	var pending atomic.Int64
	pending.Store(1) // one "not yet" answer before success

	e := newEnv(t, githubHandler(t, &pending))

	_, err := run(t, e.args("auth", "login"))
	gt.NoError(t, err)

	// The credential must land in the file, since the keychain is disabled.
	info := gt.R1(os.Stat(e.credentialPath())).NoError(t)
	gt.Equal(t, info.Mode().Perm(), os.FileMode(0o600))

	_, err = run(t, e.args("auth", "status"))
	gt.NoError(t, err)

	saved := gt.R1(os.ReadFile(e.credentialPath())).NoError(t)
	gt.S(t, string(saved)).Contains("repo,notifications")
}

func TestAuthStatusWithoutCredential(t *testing.T) {
	e := newEnv(t, githubHandler(t, nil))

	_, err := run(t, e.args("auth", "status"))
	// Not being signed in is a state, not a failure.
	gt.NoError(t, err)
}

func TestAuthLogoutKeepsReadState(t *testing.T) {
	e := newEnv(t, githubHandler(t, nil))
	gt.NoError(t, run2(t, e.args("auth", "login")))

	// A read record from an earlier session.
	store := readstate.New(e.statePath(), "github.com")
	gt.NoError(t, store.Load())
	gt.NoError(t, store.Put(map[types.ThreadID]model.ReadOverride{"1": {State: model.ReadStateRead}}))

	gt.NoError(t, run2(t, e.args("auth", "logout")))

	_, err := os.Stat(e.credentialPath())
	gt.True(t, os.IsNotExist(err))

	// Signing out is about the token; what the user has already triaged stays.
	reloaded := readstate.New(e.statePath(), "github.com")
	gt.NoError(t, reloaded.Load())
	gt.Equal(t, reloaded.Len(), 1)
}

// A read-state file this build cannot parse must not lock the user out of the
// credential commands: the file has nothing to do with the token they may be
// trying to remove.
func TestBrokenReadStateFileDoesNotBlockCredentialCommands(t *testing.T) {
	e := newEnv(t, githubHandler(t, nil))
	gt.NoError(t, run2(t, e.args("auth", "login")))

	const content = `{"version": 99, "overrides": {}}`
	gt.NoError(t, os.WriteFile(e.statePath(), []byte(content), 0o600))

	gt.NoError(t, run2(t, e.args("auth", "status")))
	gt.NoError(t, run2(t, e.args("auth", "logout")))

	_, err := os.Stat(e.credentialPath())
	gt.True(t, os.IsNotExist(err))

	// A file octify cannot read must survive untouched.
	after := gt.R1(os.ReadFile(e.statePath())).NoError(t)
	gt.Equal(t, string(after), content)
}

func TestFailureIsReportedAsTwoLines(t *testing.T) {
	e := newEnv(t, githubHandler(t, nil))

	stderr, err := run(t, e.args("--log-format", "yaml", "auth", "status"))
	gt.Error(t, err)

	lines := strings.Split(strings.TrimRight(stderr, "\n"), "\n")
	gt.A(t, lines).Length(2)
	gt.S(t, lines[0]).HasPrefix("octify: ")
	gt.S(t, lines[0]).Contains("unknown log format")
	gt.S(t, lines[1]).HasPrefix("  → ")

	// Neither the error chain nor a stack may reach the user.
	gt.S(t, stderr).ContainsNone("logging:")
	gt.S(t, stderr).ContainsNone(".go:")
}

func TestFlagValidation(t *testing.T) {
	testCases := map[string]struct {
		args []string
		want string
	}{
		"interval too small":   {[]string{"--interval", "500ms"}, "--interval must be at least 1s"},
		"state ttl too small":  {[]string{"--state-ttl", "1m"}, "--state-ttl must be at least 1h"},
		"max pages too small":  {[]string{"--max-pages", "0"}, "--max-pages must be at least 1"},
		"negative archive gap": {[]string{"--archive-gap", "-1s"}, "--archive-gap must not be negative"},
		"unknown log format":   {[]string{"--log-format", "yaml"}, "unknown log format"},
		"unknown log level":    {[]string{"--log-level", "chatty"}, "unknown log level"},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t, githubHandler(t, nil))
			stderr, err := run(t, e.args(append(tc.args, "auth", "status")...))
			gt.Error(t, err)
			gt.S(t, stderr).Contains(tc.want)
		})
	}
}

func TestFlagOverridesEnvironment(t *testing.T) {
	e := newEnv(t, githubHandler(t, nil))
	t.Setenv("OCTIFY_INTERVAL", "500ms")

	// The environment value alone would be rejected.
	stderr, err := run(t, e.args("auth", "status"))
	gt.Error(t, err)
	gt.S(t, stderr).Contains("--interval must be at least 1s")

	// The flag wins over it.
	_, err = run(t, e.args("--interval", "90s", "auth", "status"))
	gt.NoError(t, err)
}

func TestMissingClientID(t *testing.T) {
	e := newEnv(t, githubHandler(t, nil))

	stderr, err := run(t, []string{
		"octify", "--client-id", "", "--api-base", e.serverURL,
		"--web-base", e.serverURL, "--no-keyring", "auth", "login",
	})
	gt.Error(t, err)
	gt.S(t, stderr).Contains("no OAuth client ID is configured")
}

func TestLogFileCapturesDetailWithoutTheToken(t *testing.T) {
	e := newEnv(t, githubHandler(t, nil))
	logPath := filepath.Join(e.dir, "octify.log")

	gt.NoError(t, run2(t, e.args("--log-file", logPath, "--log-level", "debug", "auth", "login")))

	written := gt.R1(os.ReadFile(logPath)).NoError(t)
	gt.S(t, string(written)).ContainsNone("gho_test")
}

// run2 discards the captured output for cases that only care about the error.
func run2(t *testing.T, argv []string) error {
	t.Helper()
	_, err := run(t, argv)
	return err
}

// The spec lists the exact wording for every error the user can meet, and
// cli.Report is what puts it on screen. Reaching each one through the code that
// raises it is what keeps that table honest: a new failure path without display
// text falls through to the generic message, which is precisely the case where
// an actionable one matters.
func TestEveryUserFacingErrorCarriesAMessage(t *testing.T) {
	testCases := map[string]func(t *testing.T) error{
		"github rejected the token": func(t *testing.T) error {
			return callGitHub(t, http.StatusUnauthorized, nil)
		},
		"github denied the request": func(t *testing.T) error {
			return callGitHub(t, http.StatusForbidden, nil)
		},
		"rate limited": func(t *testing.T) error {
			return callGitHub(t, http.StatusForbidden, map[string]string{"Retry-After": "60"})
		},
		"unexpected status": func(t *testing.T) error {
			return callGitHub(t, http.StatusServiceUnavailable, nil)
		},
		"broken response body": func(t *testing.T) error {
			return callGitHubBody(t, `{"not":"an array"}`)
		},
		"notification no longer exists": func(t *testing.T) error {
			return callGitHub(t, http.StatusNotFound, nil)
		},
		"unreachable host": func(t *testing.T) error {
			client := gh.New("t", gh.WithAPIBase("http://127.0.0.1:1"))
			_, err := client.ListNotifications(t.Context(), gh.ListNotificationsInput{})
			return err
		},
		"device code expired": func(t *testing.T) error {
			return exchangeDeviceCode(t, "expired_token")
		},
		"authorization declined": func(t *testing.T) error {
			return exchangeDeviceCode(t, "access_denied")
		},
		"device flow disabled": func(t *testing.T) error {
			return exchangeDeviceCode(t, "device_flow_disabled")
		},
		"credential file readable by others": func(t *testing.T) error {
			path := filepath.Join(t.TempDir(), "credential.json")
			gt.NoError(t, os.WriteFile(path, []byte(`{"version":1,"access_token":"t"}`), 0o644))
			_, _, err := tokenstore.NewFile(path).Load(t.Context())
			return err
		},
		"credential in an unreadable format": func(t *testing.T) error {
			path := filepath.Join(t.TempDir(), "credential.json")
			gt.NoError(t, os.WriteFile(path, []byte(`not json`), 0o600))
			_, _, err := tokenstore.NewFile(path).Load(t.Context())
			return err
		},
		"credential from a newer octify": func(t *testing.T) error {
			path := filepath.Join(t.TempDir(), "credential.json")
			gt.NoError(t, os.WriteFile(path, []byte(`{"version":99,"access_token":"t"}`), 0o600))
			_, _, err := tokenstore.NewFile(path).Load(t.Context())
			return err
		},
		"read-state file is broken": func(t *testing.T) error {
			path := filepath.Join(t.TempDir(), "read-state.json")
			gt.NoError(t, os.WriteFile(path, []byte(`{`), 0o600))
			return readstate.New(path, "github.com").Load()
		},
		"read-state from a newer octify": func(t *testing.T) error {
			path := filepath.Join(t.TempDir(), "read-state.json")
			gt.NoError(t, os.WriteFile(path, []byte(`{"version":99,"overrides":{}}`), 0o600))
			return readstate.New(path, "github.com").Load()
		},
		"read state could not be saved": func(t *testing.T) error {
			if os.Geteuid() == 0 {
				t.Skip("a read-only directory does not stop writes when running as root")
			}
			dir := filepath.Join(t.TempDir(), "read-only")
			gt.NoError(t, os.Mkdir(dir, 0o700))
			store := readstate.New(filepath.Join(dir, "read-state.json"), "github.com")
			gt.NoError(t, store.Load())
			gt.NoError(t, os.Chmod(dir, 0o500))
			t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
			return store.Put(map[types.ThreadID]model.ReadOverride{"1": {State: model.ReadStateRead}})
		},
		// Two failures are covered where they are raised rather than here: the
		// browser launch, because only pkg/utils/browser can replace the launcher,
		// and the flag validation errors, which TestFlagValidation drives through
		// the real command line.
	}

	for name, raise := range testCases {
		t.Run(name, func(t *testing.T) {
			err := raise(t)
			gt.Error(t, err)

			msg, ok := model.UserMessageOf(err)
			gt.True(t, ok)

			// The status line joins these with a separator and truncates from the
			// right, so a newline or a trailing period would show up as a defect on
			// screen. Length is not asserted: several of these embed a file path,
			// and the renderer is what bounds the line (see the status line tests
			// in pkg/tui).
			gt.S(t, msg.Summary).IsNotEmpty()
			for _, part := range []string{msg.Summary, msg.Action} {
				gt.S(t, part).ContainsNone("\n")
				gt.False(t, strings.HasSuffix(part, "."))
			}

			// And it has to reach the user in the documented two-line shape.
			var buf bytes.Buffer
			cli.Report(&buf, err)
			gt.S(t, buf.String()).HasPrefix("octify: " + msg.Summary)
		})
	}
}

func callGitHub(t *testing.T, status int, headers map[string]string) error {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for k, v := range headers {
			w.Header().Set(k, v)
		}
		w.WriteHeader(status)
	}))
	t.Cleanup(srv.Close)

	client := gh.New("t", gh.WithAPIBase(srv.URL), gh.WithHTTPClient(srv.Client()))
	_, err := client.ListNotifications(t.Context(), gh.ListNotificationsInput{})
	return err
}

func callGitHubBody(t *testing.T, body string) error {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)

	client := gh.New("t", gh.WithAPIBase(srv.URL), gh.WithHTTPClient(srv.Client()))
	_, err := client.ListNotifications(t.Context(), gh.ListNotificationsInput{})
	return err
}

func exchangeDeviceCode(t *testing.T, code string) error {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"error":"` + code + `"}`))
	}))
	t.Cleanup(srv.Close)

	_, err := gh.ExchangeDeviceCode(t.Context(), srv.Client(), srv.URL, "id", "dc", time.Now)
	return err
}
