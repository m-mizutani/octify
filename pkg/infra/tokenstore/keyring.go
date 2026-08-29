package tokenstore

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/m-mizutani/goerr/v2"
	"github.com/m-mizutani/octify/pkg/domain/model"
	"github.com/zalando/go-keyring"
)

// DefaultKeyringService is the service name octify registers under.
const DefaultKeyringService = "octify"

type keyringStore struct {
	service string
	user    string
}

// NewKeyring stores the credential in the OS keychain. The user is the GitHub
// host so that separate hosts do not overwrite each other.
func NewKeyring(service, user string) Store {
	return &keyringStore{service: service, user: user}
}

func (s *keyringStore) Backend() Backend { return BackendKeyring }

func (s *keyringStore) Load(ctx context.Context) (*model.Credential, error) {
	raw, err := keyring.Get(s.service, s.user)
	if err != nil {
		return nil, s.translate(err, "failed to read from keyring")
	}

	var cred model.Credential
	if err := json.Unmarshal([]byte(raw), &cred); err != nil {
		return nil, model.WithUserMessage(
			goerr.Wrap(model.ErrInvalidCredential, "keyring entry is not valid json",
				goerr.V("service", s.service), goerr.V("user", s.user)),
			model.UserMessage{
				Summary: "the saved credential is not usable",
				Action:  "press o to sign in again",
			},
		)
	}
	if err := cred.Validate(); err != nil {
		// The advice has to name the keychain, not a file path: there is no file
		// for the user to delete on this path.
		return nil, decorateCredentialError(err, model.UserMessage{
			Summary: "the credential in the OS keychain was written by a newer octify",
			Action:  "update octify, or run: octify auth logout",
		})
	}
	return &cred, nil
}

func (s *keyringStore) Save(ctx context.Context, cred *model.Credential) (Backend, error) {
	raw, err := json.Marshal(cred)
	if err != nil {
		return "", goerr.Wrap(err, "failed to encode credential")
	}
	if err := keyring.Set(s.service, s.user, string(raw)); err != nil {
		return "", s.translate(err, "failed to write to keyring")
	}
	return BackendKeyring, nil
}

func (s *keyringStore) Delete(ctx context.Context) error {
	if err := keyring.Delete(s.service, s.user); err != nil {
		return s.translate(err, "failed to delete from keyring")
	}
	return nil
}

// translate maps keyring failures onto the two conditions the fallback store
// reacts to. Anything else is reported as-is.
func (s *keyringStore) translate(err error, msg string) error {
	switch {
	case errors.Is(err, keyring.ErrNotFound):
		return goerr.Wrap(ErrNotFound, msg, goerr.V("service", s.service), goerr.V("user", s.user))
	case errors.Is(err, keyring.ErrUnsupportedPlatform):
		return goerr.Wrap(ErrBackendUnavailable, msg, goerr.V("cause", err.Error()))
	default:
		// A keychain that is present but refuses to answer (locked, no session bus)
		// is treated as unavailable so the file path can take over.
		return goerr.Wrap(ErrBackendUnavailable, msg, goerr.V("cause", err.Error()))
	}
}

// decorateCredentialError attaches display text to the validation errors raised
// by model.Credential. Only the version mismatch needs wording specific to
// where the credential is stored; everything else is fixed by signing in again.
func decorateCredentialError(err error, versionMismatch model.UserMessage) error {
	if errors.Is(err, model.ErrUnsupportedCredentialVersion) {
		return model.WithUserMessage(err, versionMismatch)
	}
	return model.WithUserMessage(err, model.UserMessage{
		Summary: "the saved credential is not usable",
		Action:  "press o to sign in again",
	})
}
