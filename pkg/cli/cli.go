package cli

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"github.com/m-mizutani/goerr/v2"
	"github.com/m-mizutani/octify/pkg/domain/model"
	"github.com/m-mizutani/octify/pkg/infra/gh"
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
	all            bool
	maxPages       int
	archiveGap     time.Duration
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

func (o *options) resolvePaths() (credentialPath, statePath string, err error) {
	dir, err := configDir()
	if err != nil {
		return "", "", err
	}
	credentialPath = o.credentialFile
	if credentialPath == "" {
		credentialPath = filepath.Join(dir, "credential.json")
	}
	statePath = o.stateFile
	if statePath == "" {
		statePath = filepath.Join(dir, "read-state.json")
	}
	return credentialPath, statePath, nil
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

	credentialPath, statePath, err := o.resolvePaths()
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

	uc := usecase.New(tokens, reads, o.usecaseConfig())
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

	return tui.Run(ctx, uc, tui.Config{
		WebBase:  opt.webBase,
		OpenURL:  browser.Open,
		ShowRead: opt.all,
	})
}
