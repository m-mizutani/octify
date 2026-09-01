package cli

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/m-mizutani/goerr/v2"
	"github.com/m-mizutani/octify/pkg/domain/model"
	"github.com/m-mizutani/octify/pkg/infra/gh"
	"github.com/m-mizutani/octify/pkg/infra/herdr"
	"github.com/m-mizutani/octify/pkg/infra/pollcache"
	"github.com/m-mizutani/octify/pkg/infra/readstate"
	"github.com/m-mizutani/octify/pkg/infra/tokenstore"
	"github.com/m-mizutani/octify/pkg/tui"
	"github.com/m-mizutani/octify/pkg/usecase"
	"github.com/m-mizutani/octify/pkg/utils/browser"
	"github.com/m-mizutani/octify/pkg/utils/logging"
	"github.com/m-mizutani/octify/pkg/utils/safe"
	ucli "github.com/urfave/cli/v3"
)

var (
	// Version is filled in at build time with -ldflags.
	Version = "dev"

	// DefaultClientID is octify's own OAuth app, so that a plain `go install`
	// produces a binary that can sign in without any further setup.
	//
	// A client ID is public information: the device flow uses no client secret,
	// and the ID alone grants nothing — every token still comes from a person
	// approving the request on github.com. It can be overridden with
	// --client-id or OCTIFY_CLIENT_ID to point octify at another app.
	DefaultClientID = "Ov23liscIpYyCx1ELGuo"
)

// requiredScopes covers private repositories, which the notifications scope
// alone does not reliably reach.
var requiredScopes = []string{"repo", "notifications"}

var ErrMissingClientID = goerr.New("no OAuth client ID configured")

// options holds every value the flags produce. Defaults live here and nowhere
// deeper, so a reader can see the whole configuration in one place.
type options struct {
	interval       time.Duration
	clientID       string
	apiBase        string
	graphqlBase    string
	webBase        string
	credentialFile string
	noKeyring      bool
	stateFile      string
	stateTTL       time.Duration
	cacheFile      string
	noCache        bool
	all            bool
	maxPages       int
	archiveGap     time.Duration
	noHerdr        bool
	herdrSound     string
	logLevel       string
	logFile        string
	logFormat      string
}

// Report writes a failure the way the user needs to read it: the cause, then
// what to do about it. The error chain and the stack belong in the log, so
// neither appears here.
func Report(w io.Writer, err error) {
	msg, ok := model.UserMessageOf(err)
	if !ok {
		fmt.Fprintln(w, "octify: something went wrong")
		fmt.Fprintln(w, "  → rerun with --log-file to capture details")
		return
	}

	fmt.Fprintf(w, "octify: %s\n", msg.Summary)
	if msg.Action != "" {
		fmt.Fprintf(w, "  → %s\n", msg.Action)
	}
}

func Run(ctx context.Context, argv []string) error {
	var opt options

	cmd := &ucli.Command{
		Name:    "octify",
		Usage:   "triage GitHub notifications in the terminal",
		Version: Version,
		Flags:   opt.flags(),
		Action: func(ctx context.Context, _ *ucli.Command) error {
			return runTUI(ctx, &opt)
		},
		Commands: []*ucli.Command{authCommand(&opt)},
	}

	return cmd.Run(ctx, argv)
}

func (o *options) flags() []ucli.Flag {
	return []ucli.Flag{
		&ucli.DurationFlag{
			Name:        "interval",
			Usage:       "lower bound for the polling interval; GitHub's x-poll-interval wins when it is larger",
			Value:       60 * time.Second,
			Sources:     ucli.EnvVars("OCTIFY_INTERVAL"),
			Destination: &o.interval,
		},
		&ucli.StringFlag{
			Name:        "client-id",
			Usage:       "OAuth app client ID used for the device flow",
			Value:       DefaultClientID,
			Sources:     ucli.EnvVars("OCTIFY_CLIENT_ID"),
			Destination: &o.clientID,
		},
		&ucli.StringFlag{
			Name:        "api-base",
			Usage:       "GitHub REST API root",
			Value:       gh.DefaultAPIBase,
			Sources:     ucli.EnvVars("OCTIFY_API_BASE"),
			Destination: &o.apiBase,
		},
		&ucli.StringFlag{
			Name:        "graphql-base",
			Usage:       "GitHub GraphQL endpoint; GitHub Enterprise Server serves it at https://HOST/api/graphql",
			Value:       gh.DefaultGraphQLBase,
			Sources:     ucli.EnvVars("OCTIFY_GRAPHQL_BASE"),
			Destination: &o.graphqlBase,
		},
		&ucli.StringFlag{
			Name:        "web-base",
			Usage:       "GitHub web root, used for the device flow and for opening pages",
			Value:       gh.DefaultWebBase,
			Sources:     ucli.EnvVars("OCTIFY_WEB_BASE"),
			Destination: &o.webBase,
		},
		&ucli.StringFlag{
			Name:        "credential-file",
			Usage:       "where the token is stored when no OS keychain is available",
			Sources:     ucli.EnvVars("OCTIFY_CREDENTIAL_FILE"),
			Destination: &o.credentialFile,
		},
		&ucli.BoolFlag{
			Name:        "no-keyring",
			Usage:       "skip the OS keychain and always use the credential file",
			Sources:     ucli.EnvVars("OCTIFY_NO_KEYRING"),
			Destination: &o.noKeyring,
		},
		&ucli.StringFlag{
			Name:        "state-file",
			Usage:       "where octify's own read/unread records are stored",
			Sources:     ucli.EnvVars("OCTIFY_STATE_FILE"),
			Destination: &o.stateFile,
		},
		&ucli.DurationFlag{
			Name:        "state-ttl",
			Usage:       "how long a read record survives after its notification leaves the list",
			Value:       720 * time.Hour,
			Sources:     ucli.EnvVars("OCTIFY_STATE_TTL"),
			Destination: &o.stateTTL,
		},
		&ucli.StringFlag{
			Name:        "cache-file",
			Usage:       "where the last poll's notification list is saved, so the next start has something to show",
			Sources:     ucli.EnvVars("OCTIFY_CACHE_FILE"),
			Destination: &o.cacheFile,
		},
		&ucli.BoolFlag{
			Name:        "no-cache",
			Usage:       "never save the notification list; every start begins with an empty list",
			Sources:     ucli.EnvVars("OCTIFY_NO_CACHE"),
			Destination: &o.noCache,
		},
		&ucli.BoolFlag{
			Name:        "all",
			Usage:       "start with read notifications shown as well",
			Sources:     ucli.EnvVars("OCTIFY_ALL"),
			Destination: &o.all,
		},
		&ucli.IntFlag{
			Name:        "max-pages",
			Usage:       "how many pages of 50 notifications one poll may fetch",
			Value:       10,
			Sources:     ucli.EnvVars("OCTIFY_MAX_PAGES"),
			Destination: &o.maxPages,
		},
		&ucli.DurationFlag{
			Name:        "archive-gap",
			Usage:       "wait between the requests of a bulk archive",
			Value:       time.Second,
			Sources:     ucli.EnvVars("OCTIFY_ARCHIVE_GAP"),
			Destination: &o.archiveGap,
		},
		&ucli.BoolFlag{
			Name:        "no-herdr",
			Usage:       "never reach herdr, even when running inside one of its panes: no desktop notification and no entry in its agent list",
			Sources:     ucli.EnvVars("OCTIFY_NO_HERDR"),
			Destination: &o.noHerdr,
		},
		&ucli.StringFlag{
			Name:        "herdr-sound",
			Usage:       "sound herdr plays with the notification: none, done or request",
			Value:       string(herdr.SoundNone),
			Sources:     ucli.EnvVars("OCTIFY_HERDR_SOUND"),
			Destination: &o.herdrSound,
		},
		&ucli.StringFlag{
			Name:        "log-level",
			Usage:       "debug, info, warn or error",
			Value:       "warn",
			Sources:     ucli.EnvVars("OCTIFY_LOG_LEVEL"),
			Destination: &o.logLevel,
		},
		&ucli.StringFlag{
			Name:        "log-file",
			Usage:       "file to write logs to; without it logs are discarded",
			Sources:     ucli.EnvVars("OCTIFY_LOG_FILE"),
			Destination: &o.logFile,
		},
		&ucli.StringFlag{
			Name:        "log-format",
			Usage:       "text or json",
			Value:       string(logging.FormatText),
			Sources:     ucli.EnvVars("OCTIFY_LOG_FORMAT"),
			Destination: &o.logFormat,
		},
	}
}

func (o *options) validate() error {
	// Refuse the one combination that would send the token to the wrong host.
	// The two endpoints are configured separately because GitHub Enterprise
	// Server puts them on paths that share no prefix, but that also lets a user
	// move --api-base to their own server and leave GraphQL on github.com. Every
	// poll would then POST their enterprise token to api.github.com, and the 401
	// that comes back is indistinguishable from an expired token, so octify
	// would delete the perfectly good credential it just saved.
	if o.apiBase != gh.DefaultAPIBase && o.graphqlBase == gh.DefaultGraphQLBase {
		return model.WithUserMessage(
			goerr.New("graphql base still points at github.com",
				goerr.V("api_base", o.apiBase), goerr.V("graphql_base", o.graphqlBase)),
			model.UserMessage{
				Summary: "--api-base points at another GitHub, but --graphql-base still points at github.com",
				Action:  "set --graphql-base as well; GitHub Enterprise Server serves it at https://HOST/api/graphql",
			},
		)
	}
	if o.interval < time.Second {
		return model.WithUserMessage(
			goerr.New("interval is too small", goerr.V("interval", o.interval.String())),
			model.UserMessage{Summary: "--interval must be at least 1s"},
		)
	}
	if o.stateTTL < time.Hour {
		return model.WithUserMessage(
			goerr.New("state ttl is too small", goerr.V("state_ttl", o.stateTTL.String())),
			model.UserMessage{Summary: "--state-ttl must be at least 1h"},
		)
	}
	if o.maxPages < 1 {
		return model.WithUserMessage(
			goerr.New("max pages is too small", goerr.V("max_pages", o.maxPages)),
			model.UserMessage{Summary: "--max-pages must be at least 1"},
		)
	}
	if o.archiveGap < 0 {
		return model.WithUserMessage(
			goerr.New("archive gap is negative", goerr.V("archive_gap", o.archiveGap.String())),
			model.UserMessage{Summary: "--archive-gap must not be negative"},
		)
	}
	if err := herdr.Sound(o.herdrSound).Validate(); err != nil {
		return model.WithUserMessage(err, model.UserMessage{
			Summary: "unknown herdr sound",
			Action:  "use none, done or request",
		})
	}
	if err := logging.Format(o.logFormat).Validate(); err != nil {
		return model.WithUserMessage(err, model.UserMessage{
			Summary: "unknown log format",
			Action:  "use text or json",
		})
	}
	if _, err := parseLevel(o.logLevel); err != nil {
		return err
	}
	return nil
}

func parseLevel(name string) (slog.Level, error) {
	switch name {
	case "debug":
		return slog.LevelDebug, nil
	case "info":
		return slog.LevelInfo, nil
	case "warn":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return 0, model.WithUserMessage(
			goerr.New("unknown log level", goerr.V("level", name)),
			model.UserMessage{Summary: "unknown log level", Action: "use debug, info, warn or error"},
		)
	}
}

// hostOf names the GitHub instance, which keys both the keychain entry and the
// read-state file.
func hostOf(webBase string) string {
	if u, err := url.Parse(webBase); err == nil && u.Host != "" {
		return u.Host
	}
	return "github.com"
}

func configDir() (string, error) {
	if dir, ok := os.LookupEnv("XDG_CONFIG_HOME"); ok && dir != "" {
		return filepath.Join(dir, "octify"), nil
	}
	base, err := os.UserConfigDir()
	if err != nil {
		return "", goerr.Wrap(err, "failed to locate the user config directory")
	}
	return filepath.Join(base, "octify"), nil
}

func (o *options) resolvePaths() (credentialPath, statePath, cachePath string, err error) {
	dir, err := configDir()
	if err != nil {
		return "", "", "", err
	}
	credentialPath = o.credentialFile
	if credentialPath == "" {
		credentialPath = filepath.Join(dir, "credential.json")
	}
	statePath = o.stateFile
	if statePath == "" {
		statePath = filepath.Join(dir, "read-state.json")
	}
	cachePath = o.cacheFile
	if cachePath == "" {
		cachePath = filepath.Join(dir, "poll-cache.json")
	}
	return credentialPath, statePath, cachePath, nil
}

// setupLogger opens the log destination. The returned closer is nil when logs
// are discarded.
func (o *options) setupLogger() (*slog.Logger, io.Closer, error) {
	level, err := parseLevel(o.logLevel)
	if err != nil {
		return nil, nil, err
	}

	// The terminal belongs to the TUI, so without an explicit file there is
	// nowhere safe to write.
	if o.logFile == "" {
		return logging.New(logging.Config{Level: level, Format: logging.Format(o.logFormat)}), nil, nil
	}

	f, err := os.OpenFile(o.logFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, nil, model.WithUserMessage(
			goerr.Wrap(err, "failed to open the log file", goerr.V("path", o.logFile)),
			model.UserMessage{Summary: "could not open the log file at " + o.logFile},
		)
	}

	logger := logging.New(logging.Config{
		Writer: f,
		Level:  level,
		Format: logging.Format(o.logFormat),
		Source: level == slog.LevelDebug,
	})
	return logger, f, nil
}

// build assembles the stores and the use case shared by every command.
//
// requireReadState is false for the credential commands. Refusing to run
// `auth logout` because the read-state file cannot be parsed would trap the user:
// the file has nothing to do with the token they are trying to remove.
func (o *options) build(ctx context.Context, requireReadState bool) (context.Context, *usecase.UseCase, io.Closer, error) {
	if err := o.validate(); err != nil {
		return ctx, nil, nil, err
	}

	logger, closer, err := o.setupLogger()
	if err != nil {
		return ctx, nil, nil, err
	}
	ctx = logging.With(ctx, logger)

	credentialPath, statePath, cachePath, err := o.resolvePaths()
	if err != nil {
		return ctx, nil, closer, err
	}

	host := hostOf(o.webBase)

	var tokens tokenstore.Store = tokenstore.NewFile(credentialPath)
	if !o.noKeyring {
		tokens = tokenstore.NewFallback(
			tokenstore.NewKeyring(tokenstore.DefaultKeyringService, host),
			tokenstore.NewFile(credentialPath),
		)
	}

	reads := readstate.New(statePath, host)
	if err := reads.Load(); err != nil {
		if requireReadState {
			return ctx, nil, closer, err
		}
		// The command does not touch read state, so carry on with an empty set
		// and leave the file alone.
		logging.From(ctx).Warn("ignoring an unreadable read-state file",
			slog.Any("error", err), slog.String("path", statePath))
	}

	cache := pollcache.New(cachePath, host)

	var ucOpts []usecase.Option
	if o.noCache {
		// --no-cache says octify keeps no list on this machine, which has to
		// cover one an earlier run without the flag left behind. Failing to
		// remove it is not worth refusing the command over.
		if err := cache.Delete(); err != nil {
			logging.From(ctx).Warn("could not remove the saved notification list",
				slog.Any("error", err), slog.String("path", cachePath))
		}
	} else {
		ucOpts = append(ucOpts, usecase.WithPollCache(cache))
	}

	uc := usecase.New(tokens, reads, o.usecaseConfig(), ucOpts...)
	return ctx, uc, closer, nil
}

// usecaseConfig is where every flag turns into the configuration the deeper
// layers see. It is separate from build so that a test can check the mapping
// without standing up a token store and a state file.
func (o *options) usecaseConfig() usecase.Config {
	return usecase.Config{
		ClientID:    o.clientID,
		Scopes:      requiredScopes,
		APIBase:     o.apiBase,
		GraphQLBase: o.graphqlBase,
		WebBase:     o.webBase,
		MinInterval: o.interval,
		MaxPages:    o.maxPages,
		ArchiveGap:  o.archiveGap,
		StateTTL:    o.stateTTL,
	}
}

func runTUI(ctx context.Context, opt *options) error {
	ctx, uc, closer, err := opt.build(ctx, true)
	if err != nil {
		return err
	}
	defer safe.Close(ctx, closer)

	if opt.clientID == "" {
		return model.WithUserMessage(ErrMissingClientID, model.UserMessage{
			Summary: "no OAuth client ID is configured",
			Action:  "set OCTIFY_CLIENT_ID, or build octify with one embedded",
		})
	}

	link := opt.herdrLink()

	err = tui.Run(ctx, uc, tui.Config{
		WebBase:  opt.webBase,
		OpenURL:  browser.Open,
		ShowRead: opt.all,
		Announce: link.announceFunc(),
		Report:   link.reportFunc(),
	})

	// The pane stops being octify's the moment the program ends.
	link.release(ctx)
	return err
}

// herdrLink is one run's connection to the workspace.
//
// It is built once and shared, because the withdrawal at the end has to know
// what the reports before it did: Bubble Tea abandons command goroutines when
// the program ends rather than waiting for them, so a report can still be on
// the socket when octify is already leaving.
//
// A nil link is a run with no workspace to talk to, and every method here
// accepts one.
type herdrLink struct {
	client *herdr.Client
	// inFlight counts reports that have begun and not yet finished. The
	// withdrawal waits for them, which is bounded because every report carries
	// the client's own deadline.
	inFlight sync.WaitGroup
	// seq is the highest report number handed to the server, so the withdrawal
	// can be numbered above anything the server has not applied yet.
	seq atomic.Uint64
}

// herdrLink returns this run's link to the workspace, or nil when there is none
// to talk to.
//
// Whether octify has a workspace to reach for is decided here and nowhere else,
// so nothing below this boundary has to know that herdr exists.
func (o *options) herdrLink() *herdrLink {
	if o.noHerdr {
		return nil
	}
	sess, ok := herdr.Detect()
	if !ok {
		return nil
	}
	return &herdrLink{client: herdr.New(sess, herdr.WithSound(herdr.Sound(o.herdrSound)))}
}

// announceFunc returns the toast sender for this run, or nil when there is
// nowhere to send one.
func (l *herdrLink) announceFunc() func(ctx context.Context, title, body string) error {
	if l == nil {
		return nil
	}
	return l.client.Show
}

// reportFunc returns the reporter for this run, or nil when there is nowhere to
// report to.
//
// A session with no pane can still show toasts, so the two are decided
// separately: a toast is addressed to the session, a report to a pane.
func (l *herdrLink) reportFunc() func(ctx context.Context, seq uint64, activity tui.Activity, unread int) error {
	if l == nil || !l.client.CanReport() {
		return nil
	}

	return func(ctx context.Context, seq uint64, activity tui.Activity, unread int) error {
		l.inFlight.Add(1)
		defer l.inFlight.Done()

		// Reports run concurrently, so the highest number is taken rather than
		// the latest one to arrive here.
		for high := l.seq.Load(); seq > high; high = l.seq.Load() {
			if l.seq.CompareAndSwap(high, seq) {
				break
			}
		}

		state, title := herdrStatus(activity, unread)
		return l.client.Report(ctx, seq, state, title)
	}
}

// release withdraws the report after the terminal loop has ended.
//
// It waits for reports already under way first, so the withdrawal is the last
// thing the server hears from this run. The context is usually already done by
// this point — ctrl+c is how most people leave octify — so the withdrawal is
// sent on one that does not inherit that.
func (l *herdrLink) release(ctx context.Context) {
	if l == nil || !l.client.CanReport() {
		return
	}
	l.inFlight.Wait()

	releaseCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), releaseTimeout)
	defer cancel()

	if err := l.client.Release(releaseCtx, l.seq.Add(1)); err != nil {
		logging.From(ctx).Warn("could not withdraw the herdr report", slog.Any("error", err))
	}
}

// herdrStatus translates what octify is doing into what herdr shows.
//
// Waiting for the device code is blocked rather than working because it is
// exactly what herdr means by blocked: nothing moves until a person answers.
// Unread notifications are blocked for the same reason at one remove — they are
// the program asking for attention.
func herdrStatus(activity tui.Activity, unread int) (herdr.State, string) {
	switch activity {
	case tui.ActivitySignedOut:
		return herdr.StateIdle, "not signed in"
	case tui.ActivityAuthenticating:
		return herdr.StateBlocked, "waiting for the device code"
	case tui.ActivityLoading:
		return herdr.StateWorking, "loading"
	case tui.ActivityReady:
		title := strconv.Itoa(unread) + " unread"
		if unread == 0 {
			return herdr.StateIdle, title
		}
		return herdr.StateBlocked, title
	default:
		return herdr.StateUnknown, "octify"
	}
}

// releaseTimeout bounds the withdrawal at the end of a run, which happens after
// the screen is already gone.
const releaseTimeout = 2 * time.Second
