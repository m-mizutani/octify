package tokenstore

import (
	"context"
	"errors"

	"github.com/m-mizutani/goerr/v2"
	"github.com/m-mizutani/octify/pkg/domain/model"
)

// Backend names where a credential ended up, so the user can be told when the
// keychain was not available.
type Backend string

const (
	BackendKeyring Backend = "keyring"
	BackendFile    Backend = "file"
)

var (
	ErrNotFound           = goerr.New("tokenstore: credential not found")
	ErrBackendUnavailable = goerr.New("tokenstore: backend unavailable")
	ErrInsecurePermission = goerr.New("tokenstore: credential file permission is too open")
)

// Store persists the GitHub credential.
//
// Load and Save both report which backend actually served the call. A single
// Backend() accessor could not answer truthfully once a fallback is involved,
// because the answer depends on which backend happened to work at the time.
type Store interface {
	Load(ctx context.Context) (*model.Credential, Backend, error)
	Save(ctx context.Context, cred *model.Credential) (Backend, error)
	Delete(ctx context.Context) error
}

// fallbackStore prefers primary and drops to secondary only when the primary
// backend is unavailable on this machine.
type fallbackStore struct {
	primary   Store
	secondary Store
}

// NewFallback returns a store that uses primary where it works and secondary
// where it does not.
func NewFallback(primary, secondary Store) Store {
	return &fallbackStore{primary: primary, secondary: secondary}
}

func (s *fallbackStore) Load(ctx context.Context) (*model.Credential, Backend, error) {
	cred, backend, err := s.primary.Load(ctx)
	if err == nil {
		return cred, backend, nil
	}
	// Both "this machine has no keychain" and "the keychain has nothing" mean the
	// file is the next place to look.
	if errors.Is(err, ErrBackendUnavailable) || errors.Is(err, ErrNotFound) {
		return s.secondary.Load(ctx)
	}
	return nil, "", err
}

func (s *fallbackStore) Save(ctx context.Context, cred *model.Credential) (Backend, error) {
	backend, err := s.primary.Save(ctx, cred)
	if err == nil {
		// Drop any older copy from the secondary so a later fallback read cannot
		// resurrect a credential that has since been replaced.
		s.discard(ctx, s.secondary)
		return backend, nil
	}
	if errors.Is(err, ErrBackendUnavailable) {
		backend, err := s.secondary.Save(ctx, cred)
		if err != nil {
			return "", err
		}
		// Load always prefers the primary. Leaving the superseded credential in
		// the keychain would mean the next run picks it up and fails with 401
		// even though the user has just signed in.
		s.discard(ctx, s.primary)
		return backend, nil
	}
	return "", err
}

// discard removes a superseded credential on a best-effort basis. A backend
// that cannot be reached right now is left alone: the alternative is refusing a
// sign-in that otherwise succeeded.
func (s *fallbackStore) discard(ctx context.Context, store Store) {
	_ = store.Delete(ctx)
}

// Delete removes the credential from both backends so that a stale copy cannot
// be picked up later by the fallback path.
func (s *fallbackStore) Delete(ctx context.Context) error {
	primaryErr := s.primary.Delete(ctx)
	if errors.Is(primaryErr, ErrNotFound) || errors.Is(primaryErr, ErrBackendUnavailable) {
		primaryErr = nil
	}

	secondaryErr := s.secondary.Delete(ctx)
	if errors.Is(secondaryErr, ErrNotFound) {
		secondaryErr = nil
	}

	return errors.Join(primaryErr, secondaryErr)
}
