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
	slept  []time.Duration
}

type harnessOption func(*usecase.Config)

func withConfig(fn func(*usecase.Config)) harnessOption { return fn }

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
		WebBase:     srv.URL,
		MinInterval: 60 * time.Second,
		MaxPages:    10,
		ArchiveGap:  time.Second,
		StateTTL:    30 * 24 * time.Hour,
	}
	for _, opt := range opts {
		opt(&cfg)
	}

	h := &harness{tokens: &fakeTokenStore{}, reads: reads}
	h.uc = usecase.New(h.tokens, reads, cfg,
		usecase.WithHTTPClient(srv.Client()),
		usecase.WithClock(nowFunc),
		usecase.WithSleep(func(ctx context.Context, d time.Duration) {
			h.slept = append(h.slept, d)
		}),
	)
	return h
}

// authenticate gives the use case a working GitHub client.
func (h *harness) authenticate(t *testing.T) {
	t.Helper()
	h.tokens.cred = savedCredential()
	func() { _, _, err := h.uc.Restore(t.Context()); gt.NoError(t, err) }()
	gt.True(t, h.uc.Authenticated())
}
