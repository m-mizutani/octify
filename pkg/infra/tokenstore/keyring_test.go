package tokenstore_test

import (
	"testing"

	"github.com/m-mizutani/goerr/v2"
	"github.com/m-mizutani/gt"
	"github.com/m-mizutani/octify/pkg/domain/model"
	"github.com/m-mizutani/octify/pkg/infra/tokenstore"
	"github.com/zalando/go-keyring"
)

func TestKeyringStoreRoundTrip(t *testing.T) {
	keyring.MockInit()

	store := tokenstore.NewKeyring(tokenstore.DefaultKeyringService, "github.com")
	gt.Equal(t, store.Backend(), tokenstore.BackendKeyring)

	backend := gt.R1(store.Save(t.Context(), sampleCredential())).NoError(t)
	gt.Equal(t, backend, tokenstore.BackendKeyring)

	loaded := gt.R1(store.Load(t.Context())).NoError(t)
	gt.Equal(t, loaded.AccessToken, sampleCredential().AccessToken)
	gt.Equal(t, loaded.Scope, sampleCredential().Scope)

	gt.NoError(t, store.Delete(t.Context()))
	_, err := store.Load(t.Context())
	gt.Error(t, err).Is(tokenstore.ErrNotFound)
}

func TestKeyringStoreUnavailableBackend(t *testing.T) {
	keyring.MockInitWithError(goerr.New("no session bus"))
	t.Cleanup(keyring.MockInit)

	store := tokenstore.NewKeyring(tokenstore.DefaultKeyringService, "github.com")

	_, loadErr := store.Load(t.Context())
	gt.Error(t, loadErr).Is(tokenstore.ErrBackendUnavailable)

	_, saveErr := store.Save(t.Context(), sampleCredential())
	gt.Error(t, saveErr).Is(tokenstore.ErrBackendUnavailable)

	gt.Error(t, store.Delete(t.Context())).Is(tokenstore.ErrBackendUnavailable)
}

func TestKeyringStoreBrokenContent(t *testing.T) {
	keyring.MockInit()

	const service, user = "octify-test", "github.com"
	gt.NoError(t, keyring.Set(service, user, "not json"))

	_, err := tokenstore.NewKeyring(service, user).Load(t.Context())
	gt.Error(t, err).Is(model.ErrInvalidCredential)

	msg, ok := model.UserMessageOf(err)
	gt.True(t, ok)
	gt.NotEqual(t, msg.Summary, "")
}

func TestKeyringStoreUnsupportedVersion(t *testing.T) {
	keyring.MockInit()

	const service, user = "octify-test-version", "github.com"
	gt.NoError(t, keyring.Set(service, user, `{"version": 99, "access_token": "t"}`))

	_, err := tokenstore.NewKeyring(service, user).Load(t.Context())
	gt.Error(t, err).Is(model.ErrUnsupportedCredentialVersion)
}
