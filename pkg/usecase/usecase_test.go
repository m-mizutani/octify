package usecase_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/m-mizutani/goerr/v2"
	"github.com/m-mizutani/gt"
	"github.com/m-mizutani/octify/pkg/domain/model"
	"github.com/m-mizutani/octify/pkg/infra/pollcache"
	"github.com/m-mizutani/octify/pkg/infra/readstate"
	"github.com/m-mizutani/octify/pkg/infra/tokenstore"
	"github.com/m-mizutani/octify/pkg/usecase"
)

var fixedNow = time.Date(2026, 8, 25, 9, 0, 0, 0, time.UTC)

func nowFunc() time.Time { return fixedNow }

// fakeTokenStore stands in for the keychain so tests never touch the machine's
// real credential storage.
type fakeTokenStore struct {
	cred    *model.Credential
	loadErr error
	saveErr error
	deletes int
	saves   int
}

func (s *fakeTokenStore) Load(ctx context.Context) (*model.Credential, tokenstore.Backend, error) {
	if s.loadErr != nil {
		return nil, "", s.loadErr
	}
	if s.cred == nil {
		return nil, "", goerr.Wrap(tokenstore.ErrNotFound, "nothing saved")
	}
	return s.cred, tokenstore.BackendFile, nil
}

func (s *fakeTokenStore) Save(ctx context.Context, cred *model.Credential) (tokenstore.Backend, error) {
	s.saves++
	if s.saveErr != nil {
		return "", s.saveErr
	}
	s.cred = cred
	return tokenstore.BackendFile, nil
}

func (s *fakeTokenStore) Delete(ctx context.Context) error {
	s.deletes++
	s.cred = nil
	return nil
}

func savedCredential() *model.Credential {
	return &model.Credential{
		Version:     model.CredentialVersion,
		Host:        "github.com",
		AccessToken: "gho_saved",
		TokenType:   "bearer",
		Scope:       "repo,notifications",
		StoredAt:    fixedNow,
	}
}

// harness bundles a use case with the fakes it was built from.
type harness struct {
	uc     *usecase.UseCase
	tokens *fakeTokenStore
	reads  *readstate.Store
	// cache is nil when the harness was built with withoutPollCache.
	cache *pollcache.Store
	slept []time.Duration
}

type harnessOption func(*harnessOptions)

type harnessOptions struct {
	cfg usecase.Config
	// cachePath is empty when the use case gets no poll cache at all.
	cachePath string
}

func withConfig(fn func(*usecase.Config)) harnessOption {
	return func(o *harnessOptions) { fn(&o.cfg) }
}

// withPollCachePath points the saved list at a path of the test's choosing,
// which is how a save is made to fail.
func withPollCachePath(path string) harnessOption {
	return func(o *harnessOptions) { o.cachePath = path }
}

// withoutPollCache builds the use case the way --no-cache does.
func withoutPollCache() harnessOption {
	return func(o *harnessOptions) { o.cachePath = "" }
}

func newHarness(t *testing.T, handler http.HandlerFunc, opts ...harnessOption) *harness {
	t.Helper()

	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	reads := readstate.New(filepath.Join(t.TempDir(), "read-state.json"), "github.com")
	gt.NoError(t, reads.Load())

	cfg := usecase.Config{
		ClientID:    "client-id",
		Scopes:      []string{"repo", "notifications"},
		APIBase:     srv.URL,
		GraphQLBase: srv.URL + "/graphql",
		WebBase:     srv.URL,
		MinInterval: 60 * time.Second,
		MaxPages:    10,
		ArchiveGap:  time.Second,
		StateTTL:    30 * 24 * time.Hour,
	}
	settings := harnessOptions{
		cfg:       cfg,
		cachePath: filepath.Join(t.TempDir(), "poll-cache.json"),
	}
	for _, opt := range opts {
		opt(&settings)
	}

	h := &harness{tokens: &fakeTokenStore{}, reads: reads}
	ucOpts := []usecase.Option{
		usecase.WithHTTPClient(srv.Client()),
		usecase.WithClock(nowFunc),
		usecase.WithSleep(func(ctx context.Context, d time.Duration) {
			h.slept = append(h.slept, d)
		}),
	}
	if settings.cachePath != "" {
		h.cache = pollcache.New(settings.cachePath, "github.com")
		ucOpts = append(ucOpts, usecase.WithPollCache(h.cache))
	}

	h.uc = usecase.New(h.tokens, reads, settings.cfg, ucOpts...)
	return h
}

// savedList reports what the poll cache holds, and whether it holds anything.
func (h *harness) savedList(t *testing.T) (*model.PollSnapshot, bool) {
	t.Helper()
	snap := gt.R1(h.cache.Load()).NoError(t)
	return snap, snap != nil
}

// authenticate gives the use case a working GitHub client.
func (h *harness) authenticate(t *testing.T) {
	t.Helper()
	h.tokens.cred = savedCredential()
	func() { _, _, err := h.uc.Restore(t.Context()); gt.NoError(t, err) }()
	gt.True(t, h.uc.Authenticated())
}
