package usecase

import (
	"context"
	"net/http"
	"sync"
	"time"

	"github.com/m-mizutani/goerr/v2"
	"github.com/m-mizutani/octify/pkg/domain/model"
	"github.com/m-mizutani/octify/pkg/infra/gh"
	"github.com/m-mizutani/octify/pkg/infra/pollcache"
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
	// cache is nil when the user turned the saved list off, which is why every
	// use of it is guarded rather than assumed.
	cache *pollcache.Store
	cfg   Config

	hc    *http.Client
	now   func() time.Time
	sleep func(ctx context.Context, d time.Duration)

	mu     sync.RWMutex
	client *gh.Client
	// pollSeq numbers polling cycles in the order they start; savedSeq is the
	// newest cycle whose result reached the cache. A manual refresh can leave
	// two cycles in flight, and without these the slower one would write its
	// older list over the newer one that is already on screen.
	pollSeq  uint64
	savedSeq uint64
}

// beginPoll hands one cycle the client to use and the number that orders it
// against the others. Taking the pointer here means a concurrent sign-out
// cannot turn it into nil midway through the cycle.
func (u *UseCase) beginPoll() (*gh.Client, uint64) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.pollSeq++
	return u.client, u.pollSeq
}

// dropClientAndSnapshot discards the credential in memory and the saved list
// together.
//
// They go in one critical section because saveSnapshot checks the client under
// the same lock: without that, a cycle already past its own check could write
// the list back after this deleted it, leaving one account's notification
// titles on disk for whoever signs in next.
func (u *UseCase) dropClientAndSnapshot() error {
	u.mu.Lock()
	defer u.mu.Unlock()

	u.client = nil
	if u.cache == nil {
		return nil
	}
	return u.cache.Delete()
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

// WithPollCache enables the saved notification list. Without it octify keeps
// nothing between runs, which is what --no-cache asks for.
func WithPollCache(store *pollcache.Store) Option {
	return func(u *UseCase) {
		u.cache = store
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
