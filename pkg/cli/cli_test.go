package cli_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
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
	"github.com/m-mizutani/octify/pkg/infra/herdr"
	"github.com/m-mizutani/octify/pkg/infra/pollcache"
	"github.com/m-mizutani/octify/pkg/infra/readstate"
	"github.com/m-mizutani/octify/pkg/infra/tokenstore"
	"github.com/m-mizutani/octify/pkg/tui"
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
		"--graphql-base", e.serverURL + "/graphql",
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
		"unknown herdr sound":  {[]string{"--herdr-sound", "chime"}, "unknown herdr sound"},
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

// GitHub Enterprise Server puts GraphQL on a different path than REST, so the
// endpoint has to be configurable on its own rather than derived from
// --api-base.
func TestGraphQLBaseFlag(t *testing.T) {
	t.Run("default", func(t *testing.T) {
		cfg := gt.R1(cli.ConfigForTest(t.Context(), []string{"octify"})).NoError(t)
		gt.Equal(t, cfg.GraphQLBase, gh.DefaultGraphQLBase)
	})

	t.Run("flag", func(t *testing.T) {
		cfg := gt.R1(cli.ConfigForTest(t.Context(), []string{
			"octify", "--graphql-base", "https://ghe.example.com/api/graphql",
		})).NoError(t)
		gt.Equal(t, cfg.GraphQLBase, "https://ghe.example.com/api/graphql")
		// The REST root is untouched by it.
		gt.Equal(t, cfg.APIBase, gh.DefaultAPIBase)
	})

	t.Run("environment", func(t *testing.T) {
		t.Setenv("OCTIFY_GRAPHQL_BASE", "https://from-env.example.com/api/graphql")
		cfg := gt.R1(cli.ConfigForTest(t.Context(), []string{"octify"})).NoError(t)
		gt.Equal(t, cfg.GraphQLBase, "https://from-env.example.com/api/graphql")
	})

	t.Run("flag wins over environment", func(t *testing.T) {
		t.Setenv("OCTIFY_GRAPHQL_BASE", "https://from-env.example.com/api/graphql")
		cfg := gt.R1(cli.ConfigForTest(t.Context(), []string{
			"octify", "--graphql-base", "https://from-flag.example.com/api/graphql",
		})).NoError(t)
		gt.Equal(t, cfg.GraphQLBase, "https://from-flag.example.com/api/graphql")
	})
}

// A blank default would mean `go install` produces a binary that cannot sign
// in — the failure the compiled-in ID exists to prevent.
func TestDefaultClientIDIsCompiledIn(t *testing.T) {
	gt.S(t, cli.DefaultClientID).IsNotEmpty()
	gt.S(t, cli.DefaultClientID).HasPrefix("Ov23li")
}

func TestMissingClientID(t *testing.T) {
	e := newEnv(t, githubHandler(t, nil))

	stderr, err := run(t, []string{
		"octify", "--client-id", "", "--api-base", e.serverURL,
		"--graphql-base", e.serverURL + "/graphql",
		"--web-base", e.serverURL, "--no-keyring", "auth", "login",
	})
	gt.Error(t, err)
	gt.S(t, stderr).Contains("no OAuth client ID is configured")
}

// Leaving --graphql-base on github.com while --api-base points elsewhere would
// POST the enterprise token to api.github.com every poll, and the 401 that came
// back would make octify delete the credential it had just saved.
func TestApiBaseWithoutGraphQLBaseIsRejected(t *testing.T) {
	e := newEnv(t, githubHandler(t, nil))

	stderr, err := run(t, []string{
		"octify", "--client-id", "test-client",
		"--api-base", "https://ghe.example.com/api/v3",
		"--web-base", "https://ghe.example.com",
		"--no-keyring", "auth", "status",
	})
	gt.Error(t, err)
	gt.S(t, stderr).Contains("--graphql-base still points at github.com")

	// Naming both endpoints is accepted.
	_, err = run(t, e.args("auth", "status"))
	gt.NoError(t, err)

	// So is leaving both at their defaults.
	_, err = run(t, []string{"octify", "--client-id", "test-client", "--no-keyring", "auth", "status"})
	gt.NoError(t, err)
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

func TestCachePathDefaults(t *testing.T) {
	e := newEnv(t, githubHandler(t, nil))

	paths := gt.R1(cli.PathsForTest(t.Context(), e.args())).NoError(t)
	gt.Equal(t, paths.Cache, filepath.Join(e.dir, "octify", "poll-cache.json"))
	// The other two are unchanged by the new one.
	gt.Equal(t, paths.State, e.statePath())
	gt.Equal(t, paths.Credential, e.credentialPath())
}

func TestCacheFileFlagBeatsEnvironment(t *testing.T) {
	e := newEnv(t, githubHandler(t, nil))
	t.Setenv("OCTIFY_CACHE_FILE", filepath.Join(e.dir, "from-env.json"))

	fromEnv := gt.R1(cli.PathsForTest(t.Context(), e.args())).NoError(t)
	gt.Equal(t, fromEnv.Cache, filepath.Join(e.dir, "from-env.json"))

	fromFlag := gt.R1(cli.PathsForTest(t.Context(),
		e.args("--cache-file", filepath.Join(e.dir, "from-flag.json")))).NoError(t)
	gt.Equal(t, fromFlag.Cache, filepath.Join(e.dir, "from-flag.json"))
}

func TestNoCacheLeavesTheSavedListUnread(t *testing.T) {
	e := newEnv(t, githubHandler(t, nil))
	path := filepath.Join(e.dir, "octify", "poll-cache.json")

	// A list saved by an earlier run, at the path the default would use.
	saved := pollcache.New(path, hostOfTestServer(t, e.serverURL))
	gt.NoError(t, saved.Save(&model.PollSnapshot{
		SavedAt:       time.Now(),
		Notifications: []model.Notification{{ID: "1", UpdatedAt: time.Now()}},
	}))

	withCache := gt.R1(cli.BuildForTest(t.Context(), e.args())).NoError(t)
	restored := gt.R1(withCache.Snapshot()).NoError(t)
	gt.NotNil(t, restored)

	withoutCache := gt.R1(cli.BuildForTest(t.Context(), e.args("--no-cache"))).NoError(t)
	// No store was wired in, so the file on disk is never even looked at.
	gt.Nil(t, gt.R1(withoutCache.Snapshot()).NoError(t))
}

func TestNoCacheRemovesAListAnEarlierRunLeftBehind(t *testing.T) {
	e := newEnv(t, githubHandler(t, nil))
	path := filepath.Join(e.dir, "octify", "poll-cache.json")

	saved := pollcache.New(path, hostOfTestServer(t, e.serverURL))
	gt.NoError(t, saved.Save(&model.PollSnapshot{
		SavedAt:       time.Now(),
		Notifications: []model.Notification{{ID: "1", UpdatedAt: time.Now()}},
	}))

	gt.R1(cli.BuildForTest(t.Context(), e.args("--no-cache"))).NoError(t)

	// The flag promises octify keeps no list on this machine, so a file an
	// earlier run wrote has to go too — including one `auth logout` would
	// otherwise leave for the next account that signs in.
	_, err := os.Stat(path)
	gt.True(t, os.IsNotExist(err))
}

// hostOfTestServer names the GitHub the test server stands in for, which is what
// keys the saved list.
func hostOfTestServer(t *testing.T, serverURL string) string {
	t.Helper()
	u := gt.R1(url.Parse(serverURL)).NoError(t)
	return u.Host
}

func TestDesktopNotificationsFollowTheHerdrEnvironment(t *testing.T) {
	testCases := map[string]struct {
		env  map[string]string
		args []string
		want bool
	}{
		"outside a herdr pane": {
			env:  map[string]string{},
			want: false,
		},
		"inside a herdr pane": {
			env:  map[string]string{"HERDR_ENV": "1", "HERDR_SOCKET_PATH": "/run/herdr.sock"},
			want: true,
		},
		"inside a herdr pane with --no-herdr": {
			env:  map[string]string{"HERDR_ENV": "1", "HERDR_SOCKET_PATH": "/run/herdr.sock"},
			args: []string{"--no-herdr"},
			want: false,
		},
		"inside a herdr pane with OCTIFY_NO_HERDR": {
			env: map[string]string{
				"HERDR_ENV":         "1",
				"HERDR_SOCKET_PATH": "/run/herdr.sock",
				"OCTIFY_NO_HERDR":   "true",
			},
			want: false,
		},
		"inside a herdr pane with a sound chosen through the environment": {
			env: map[string]string{
				"HERDR_ENV":          "1",
				"HERDR_SOCKET_PATH":  "/run/herdr.sock",
				"OCTIFY_HERDR_SOUND": "done",
			},
			want: true,
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			for _, key := range []string{"HERDR_ENV", "HERDR_SOCKET_PATH", "OCTIFY_NO_HERDR", "OCTIFY_HERDR_SOUND"} {
				t.Setenv(key, tc.env[key])
			}

			got := gt.R1(cli.AnnounceForTest(t.Context(), append([]string{"octify"}, tc.args...))).NoError(t)
			gt.Equal(t, tc.want, got)
		})
	}
}

func TestReportingFollowsTheHerdrEnvironment(t *testing.T) {
	testCases := map[string]struct {
		env        map[string]string
		args       []string
		wantReport bool
		wantToast  bool
	}{
		"outside a herdr pane": {
			env: map[string]string{},
		},
		"inside a herdr pane with a pane id": {
			env: map[string]string{
				"HERDR_ENV":         "1",
				"HERDR_SOCKET_PATH": "/run/herdr.sock",
				"HERDR_PANE_ID":     "w1:p1",
			},
			wantReport: true,
			wantToast:  true,
		},
		"inside a herdr session that named no pane": {
			env: map[string]string{
				"HERDR_ENV":         "1",
				"HERDR_SOCKET_PATH": "/run/herdr.sock",
			},
			// A toast is addressed to the session, a report to a pane.
			wantReport: false,
			wantToast:  true,
		},
		"inside a herdr pane with --no-herdr": {
			env: map[string]string{
				"HERDR_ENV":         "1",
				"HERDR_SOCKET_PATH": "/run/herdr.sock",
				"HERDR_PANE_ID":     "w1:p1",
			},
			args: []string{"--no-herdr"},
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			for _, key := range []string{"HERDR_ENV", "HERDR_SOCKET_PATH", "HERDR_PANE_ID", "OCTIFY_NO_HERDR"} {
				t.Setenv(key, tc.env[key])
			}
			argv := append([]string{"octify"}, tc.args...)

			link := gt.R1(cli.LinkForTest(t.Context(), argv)).NoError(t)
			gt.Equal(t, tc.wantReport, link.Report != nil)
			gt.Equal(t, tc.wantToast, link.Toast != nil)
		})
	}
}

func TestHerdrStatus(t *testing.T) {
	testCases := map[string]struct {
		activity  tui.Activity
		unread    int
		wantState herdr.State
		wantTitle string
	}{
		"signed out": {
			activity:  tui.ActivitySignedOut,
			wantState: herdr.StateIdle,
			wantTitle: "not signed in",
		},
		"waiting for the user to enter the device code": {
			activity:  tui.ActivityAuthenticating,
			wantState: herdr.StateBlocked,
			wantTitle: "waiting for the device code",
		},
		"waiting for the first list": {
			activity:  tui.ActivityLoading,
			wantState: herdr.StateWorking,
			wantTitle: "loading",
		},
		"nothing unread": {
			activity:  tui.ActivityReady,
			unread:    0,
			wantState: herdr.StateIdle,
			wantTitle: "0 unread",
		},
		"one unread keeps the plural": {
			activity:  tui.ActivityReady,
			unread:    1,
			wantState: herdr.StateBlocked,
			wantTitle: "1 unread",
		},
		"several unread": {
			activity:  tui.ActivityReady,
			unread:    12,
			wantState: herdr.StateBlocked,
			wantTitle: "12 unread",
		},
		"an activity this build does not know": {
			activity:  tui.Activity("napping"),
			wantState: herdr.StateUnknown,
			wantTitle: "octify",
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			state, title := cli.HerdrStatus(tc.activity, tc.unread)
			gt.Equal(t, tc.wantState, state)
			gt.Equal(t, tc.wantTitle, title)

			// Whatever the translation produces has to be something herdr will
			// take.
			gt.NoError(t, state.Validate())
		})
	}
}

func TestReleaseIsSentEvenWhenTheContextIsDone(t *testing.T) {
	lines := make(chan []byte, 4)
	socket := startHerdrStub(t, lines, nil)

	t.Setenv("HERDR_ENV", "1")
	t.Setenv("HERDR_SOCKET_PATH", socket)
	t.Setenv("HERDR_PANE_ID", "w1:p1")

	link := gt.R1(cli.LinkForTest(t.Context(), []string{"octify"})).NoError(t)

	// ctrl+c is how most people leave octify, so the context is already done by
	// the time the withdrawal is sent.
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	link.Release(ctx)

	method, _ := decodeLine(t, <-lines)
	gt.Equal(t, "pane.release_agent", method)
}

func TestTheWithdrawalOutranksEveryReport(t *testing.T) {
	lines := make(chan []byte, 8)
	socket := startHerdrStub(t, lines, nil)

	t.Setenv("HERDR_ENV", "1")
	t.Setenv("HERDR_SOCKET_PATH", socket)
	t.Setenv("HERDR_PANE_ID", "w1:p1")

	link := gt.R1(cli.LinkForTest(t.Context(), []string{"octify"})).NoError(t)
	gt.NoError(t, link.Report(t.Context(), 1, tui.ActivityReady, 3))
	gt.NoError(t, link.Report(t.Context(), 2, tui.ActivityReady, 0))
	link.Release(t.Context())

	// Two reports of two requests each, then the withdrawal.
	var last map[string]any
	for range 5 {
		_, last = decodeLine(t, <-lines)
	}

	// A report the server has not applied yet must not be able to outrank the
	// withdrawal and put a finished octify back in the list.
	gt.True(t, gt.Cast[float64](t, last["seq"]) > 2)
}

func TestTheWithdrawalWaitsForAReportAlreadyUnderWay(t *testing.T) {
	lines := make(chan []byte, 8)
	gate := make(chan struct{})
	socket := startHerdrStub(t, lines, gate)

	t.Setenv("HERDR_ENV", "1")
	t.Setenv("HERDR_SOCKET_PATH", socket)
	t.Setenv("HERDR_PANE_ID", "w1:p1")

	link := gt.R1(cli.LinkForTest(t.Context(), []string{"octify"})).NoError(t)

	reported := make(chan error, 1)
	go func() { reported <- link.Report(t.Context(), 1, tui.ActivityReady, 3) }()

	// The state has reached the server and is being held there, so the report is
	// unambiguously under way before the withdrawal is asked for.
	method, _ := decodeLine(t, <-lines)
	gt.Equal(t, "pane.report_agent", method)

	released := make(chan struct{})
	go func() { defer close(released); link.Release(t.Context()) }()

	close(gate)
	gt.NoError(t, <-reported)
	<-released

	// Bubble Tea abandons command goroutines when the program ends, so without
	// the wait the withdrawal could reach the server first and be undone by the
	// rest of the report.
	method, _ = decodeLine(t, <-lines)
	gt.Equal(t, "pane.report_metadata", method)
	method, _ = decodeLine(t, <-lines)
	gt.Equal(t, "pane.release_agent", method)
}

func TestNothingIsReleasedOutsideHerdr(t *testing.T) {
	lines := make(chan []byte, 1)
	socket := startHerdrStub(t, lines, nil)

	t.Setenv("HERDR_ENV", "")
	t.Setenv("HERDR_SOCKET_PATH", socket)
	t.Setenv("HERDR_PANE_ID", "")

	link := gt.R1(cli.LinkForTest(t.Context(), []string{"octify"})).NoError(t)
	gt.True(t, link.Report == nil)
	link.Release(t.Context())

	gt.Equal(t, 0, len(lines))
}

func decodeLine(t *testing.T, line []byte) (method string, params map[string]any) {
	t.Helper()

	var sent map[string]any
	gt.NoError(t, json.Unmarshal(line, &sent)).Required()
	return gt.Cast[string](t, sent["method"]), gt.Cast[map[string]any](t, sent["params"])
}

// startHerdrStub answers every request with a bare success and keeps the lines
// it was sent. When hold is non-nil, the first pane.report_agent is answered
// only once that channel is closed, which lets a test pin a report as under way.
func startHerdrStub(t *testing.T, lines chan<- []byte, hold <-chan struct{}) string {
	t.Helper()

	// Kept short so the socket path stays inside what a sockaddr_un can hold.
	dir := gt.R1(os.MkdirTemp("", "oct")).NoError(t)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	path := filepath.Join(dir, "h.sock")
	ln := gt.R1(net.Listen("unix", path)).NoError(t)

	done := make(chan struct{})
	t.Cleanup(func() { <-done })
	t.Cleanup(func() { _ = ln.Close() })

	go func() {
		defer close(done)
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer func() { _ = conn.Close() }()
				sc := bufio.NewScanner(conn)
				if !sc.Scan() {
					return
				}
				line := append([]byte(nil), sc.Bytes()...)
				lines <- line

				if hold != nil && strings.Contains(string(line), "pane.report_agent") {
					<-hold
				}
				_, _ = io.WriteString(conn, `{"id":"octify-1","result":{"type":"ok"}}`+"\n")
			}()
		}
	}()

	return path
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
