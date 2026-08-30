package usecase

import (
	"context"
	"net/http"
	"sync"
	"time"

	"github.com/m-mizutani/goerr/v2"
	"github.com/m-mizutani/octify/pkg/domain/model"
	"github.com/m-mizutani/octify/pkg/infra/gh"
	"github.com/m-mizutani/octify/pkg/infra/readstate"
	"github.com/m-mizutani/octify/pkg/infra/tokenstore"
)

// Config carries the values the caller decided; nothing here has a default of
// its own so that every knob stays visible at the CLI boundary.
type Config struct {
	ClientID string
	Scopes   []string
	APIBase  string
	// GraphQLBase is the GraphQL endpoint. GitHub Enterprise Server serves it
	// on a different path than the REST root, so it is configured separately.
	GraphQLBase string
	WebBase     string
	// MinInterval is the floor for polling. The effective interval is the larger
	// of this and the x-poll-interval GitHub returns.
	MinInterval time.Duration
	MaxPages    int
	ArchiveGap  time.Duration
	// StateTTL is how long a read record survives after its notification stops
	// appearing in the list.
	StateTTL time.Duration
}

var ErrNotAuthenticated = goerr.New("usecase: not authenticated")

// notAuthenticated wraps the sentinel with the text the user needs. It is built
// per call rather than stored, because attaching the message once and reusing
// the value would share one stack trace across unrelated failures.
func notAuthenticated() error {
	return model.WithUserMessage(ErrNotAuthenticated, model.UserMessage{
		Summary: "not signed in",
		Action:  "press o to sign in with GitHub",
	})
}

// UseCase assembles one polling cycle, one archive job and one authentication
// attempt out of the infrastructure pieces.
//
// Polling, archiving and the terminal loop each run on their own goroutine and
// all three reach the GitHub client, so the credential is guarded.
type UseCase struct {
	tokens tokenstore.Store
	reads  *readstate.Store
	cfg    Config

	hc    *http.Client
	now   func() time.Time
	sleep func(ctx context.Context, d time.Duration)

	mu     sync.RWMutex
	client *gh.Client
}

// currentClient returns the client to use for one operation. Taking a copy of
// the pointer means a concurrent sign-out cannot turn it into nil midway.
func (u *UseCase) currentClient() *gh.Client {
	u.mu.RLock()
	defer u.mu.RUnlock()
	return u.client
}

func (u *UseCase) setClient(c *gh.Client) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.client = c
}

type Option func(*UseCase)

func WithHTTPClient(hc *http.Client) Option {
	return func(u *UseCase) {
		if hc != nil {
			u.hc = hc
		}
	}
}

func WithClock(now func() time.Time) Option {
	return func(u *UseCase) {
		if now != nil {
			u.now = now
		}
	}
}

// WithSleep replaces the wait between archive requests so tests do not spend
// real time on it.
func WithSleep(fn func(ctx context.Context, d time.Duration)) Option {
	return func(u *UseCase) {
		if fn != nil {
			u.sleep = fn
		}
	}
}

func New(tokens tokenstore.Store, reads *readstate.Store, cfg Config, opts ...Option) *UseCase {
	u := &UseCase{
		tokens: tokens,
		reads:  reads,
		cfg:    cfg,
		hc:     &http.Client{Timeout: 30 * time.Second},
		now:    time.Now,
		sleep:  sleepUntil,
	}
	for _, opt := range opts {
		opt(u)
	}
	return u
}

// sleepUntil waits for d, or returns early when the context ends.
func sleepUntil(ctx context.Context, d time.Duration) {
	if d <= 0 {
		return
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
	case <-timer.C:
	}
}
