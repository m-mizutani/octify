package tokenstore_test

import (
	"context"
	"testing"

	"github.com/m-mizutani/goerr/v2"
	"github.com/m-mizutani/gt"
	"github.com/m-mizutani/octify/pkg/domain/model"
	"github.com/m-mizutani/octify/pkg/infra/tokenstore"
)

// fakeStore records how it was used so the fallback rules can be observed.
type fakeStore struct {
	backend tokenstore.Backend
	cred    *model.Credential
	loadErr error
	saveErr error
	delErr  error
	loads   int
	saves   int
	deletes int
}

func (s *fakeStore) Backend() tokenstore.Backend { return s.backend }

func (s *fakeStore) Load(ctx context.Context) (*model.Credential, error) {
	s.loads++
	if s.loadErr != nil {
		return nil, s.loadErr
	}
	return s.cred, nil
}

func (s *fakeStore) Save(ctx context.Context, cred *model.Credential) (tokenstore.Backend, error) {
	s.saves++
	if s.saveErr != nil {
		return "", s.saveErr
	}
	s.cred = cred
	return s.backend, nil
}

func (s *fakeStore) Delete(ctx context.Context) error {
	s.deletes++
	return s.delErr
}

func TestFallbackUsesPrimaryWhenItWorks(t *testing.T) {
	primary := &fakeStore{backend: tokenstore.BackendKeyring, cred: sampleCredential()}
	secondary := &fakeStore{backend: tokenstore.BackendFile}
	store := tokenstore.NewFallback(primary, secondary)

	gt.Equal(t, store.Backend(), tokenstore.BackendKeyring)

	loaded := gt.R1(store.Load(t.Context())).NoError(t)
	gt.Equal(t, loaded.AccessToken, sampleCredential().AccessToken)

	backend := gt.R1(store.Save(t.Context(), sampleCredential())).NoError(t)
	gt.Equal(t, backend, tokenstore.BackendKeyring)

	// The secondary must not be touched while the primary is healthy.
	gt.Equal(t, secondary.loads, 0)
	gt.Equal(t, secondary.saves, 0)
}

func TestFallbackSwitchesWhenBackendUnavailable(t *testing.T) {
	unavailable := goerr.Wrap(tokenstore.ErrBackendUnavailable, "no keychain here")
	primary := &fakeStore{backend: tokenstore.BackendKeyring, loadErr: unavailable, saveErr: unavailable}
	secondary := &fakeStore{backend: tokenstore.BackendFile, cred: sampleCredential()}
	store := tokenstore.NewFallback(primary, secondary)

	loaded := gt.R1(store.Load(t.Context())).NoError(t)
	gt.Equal(t, loaded.AccessToken, sampleCredential().AccessToken)
	gt.Equal(t, secondary.loads, 1)

	backend := gt.R1(store.Save(t.Context(), sampleCredential())).NoError(t)
	gt.Equal(t, backend, tokenstore.BackendFile)
	gt.Equal(t, secondary.saves, 1)
}

func TestFallbackLooksInSecondaryWhenPrimaryHasNothing(t *testing.T) {
	primary := &fakeStore{
		backend: tokenstore.BackendKeyring,
		loadErr: goerr.Wrap(tokenstore.ErrNotFound, "nothing in the keychain"),
	}
	secondary := &fakeStore{backend: tokenstore.BackendFile, cred: sampleCredential()}
	store := tokenstore.NewFallback(primary, secondary)

	loaded := gt.R1(store.Load(t.Context())).NoError(t)
	gt.Equal(t, loaded.AccessToken, sampleCredential().AccessToken)
	gt.Equal(t, secondary.loads, 1)
}

func TestFallbackPropagatesOtherErrors(t *testing.T) {
	primary := &fakeStore{
		backend: tokenstore.BackendKeyring,
		loadErr: goerr.Wrap(model.ErrInvalidCredential, "corrupt entry"),
	}
	secondary := &fakeStore{backend: tokenstore.BackendFile, cred: sampleCredential()}
	store := tokenstore.NewFallback(primary, secondary)

	_, err := store.Load(t.Context())
	gt.Error(t, err).Is(model.ErrInvalidCredential)
	// A broken entry must be surfaced, not quietly replaced by the fallback.
	gt.Equal(t, secondary.loads, 0)
}

func TestFallbackDropsTheSupersededCopyOnSave(t *testing.T) {
	t.Run("falling back removes the primary's stale copy", func(t *testing.T) {
		unavailable := goerr.Wrap(tokenstore.ErrBackendUnavailable, "keychain locked")
		primary := &fakeStore{backend: tokenstore.BackendKeyring, cred: sampleCredential(), saveErr: unavailable}
		secondary := &fakeStore{backend: tokenstore.BackendFile}
		store := tokenstore.NewFallback(primary, secondary)

		backend := gt.R1(store.Save(t.Context(), sampleCredential())).NoError(t)
		gt.Equal(t, backend, tokenstore.BackendFile)

		// Load always prefers the primary, so an older credential left there would
		// shadow the one just saved and fail with 401 on the next run.
		gt.Equal(t, primary.deletes, 1)
	})

	t.Run("saving to the primary removes the secondary's stale copy", func(t *testing.T) {
		primary := &fakeStore{backend: tokenstore.BackendKeyring}
		secondary := &fakeStore{backend: tokenstore.BackendFile, cred: sampleCredential()}
		store := tokenstore.NewFallback(primary, secondary)

		gt.R1(store.Save(t.Context(), sampleCredential())).NoError(t)
		gt.Equal(t, secondary.deletes, 1)
	})

	t.Run("a delete that fails does not fail the save", func(t *testing.T) {
		unavailable := goerr.Wrap(tokenstore.ErrBackendUnavailable, "keychain locked")
		primary := &fakeStore{
			backend: tokenstore.BackendKeyring,
			saveErr: unavailable,
			delErr:  goerr.New("keychain still locked"),
		}
		secondary := &fakeStore{backend: tokenstore.BackendFile}
		store := tokenstore.NewFallback(primary, secondary)

		backend := gt.R1(store.Save(t.Context(), sampleCredential())).NoError(t)
		gt.Equal(t, backend, tokenstore.BackendFile)
	})
}

func TestFallbackDeletesFromBothBackends(t *testing.T) {
	primary := &fakeStore{
		backend: tokenstore.BackendKeyring,
		delErr:  goerr.Wrap(tokenstore.ErrNotFound, "nothing to delete"),
	}
	secondary := &fakeStore{backend: tokenstore.BackendFile}
	store := tokenstore.NewFallback(primary, secondary)

	// A missing entry on either side is not a failure: the goal is that no
	// credential is left behind anywhere.
	gt.NoError(t, store.Delete(t.Context()))
	gt.Equal(t, primary.deletes, 1)
	gt.Equal(t, secondary.deletes, 1)
}

func TestFallbackReportsRealDeleteFailure(t *testing.T) {
	primary := &fakeStore{backend: tokenstore.BackendKeyring}
	secondary := &fakeStore{backend: tokenstore.BackendFile, delErr: goerr.New("disk is read-only")}
	store := tokenstore.NewFallback(primary, secondary)

	gt.Error(t, store.Delete(t.Context()))
	gt.Equal(t, primary.deletes, 1)
	gt.Equal(t, secondary.deletes, 1)
}
