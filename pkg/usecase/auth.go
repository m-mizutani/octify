package usecase

import (
	"context"
	"errors"

	"github.com/m-mizutani/octify/pkg/domain/model"
	"github.com/m-mizutani/octify/pkg/infra/gh"
	"github.com/m-mizutani/octify/pkg/infra/tokenstore"
)

// Authenticated reports whether a GitHub client has been built.
func (u *UseCase) Authenticated() bool { return u.currentClient() != nil }

// Restore loads the saved credential and builds the GitHub client from it. The
// backend it came from is reported so the caller can tell the user where the
// token actually lives.
//
// It returns tokenstore.ErrNotFound when there is nothing saved yet.
func (u *UseCase) Restore(ctx context.Context) (*model.Credential, tokenstore.Backend, error) {
	cred, backend, err := u.tokens.Load(ctx)
	if err != nil {
		if errors.Is(err, tokenstore.ErrNotFound) {
			return nil, "", model.WithUserMessage(err, model.UserMessage{
				Summary: "not signed in",
				Action:  "press o to sign in with GitHub",
			})
		}
		return nil, "", err
	}

	u.useCredential(cred)
	return cred, backend, nil
}

// StartDeviceFlow asks GitHub for a device code to show to the user.
func (u *UseCase) StartDeviceFlow(ctx context.Context) (*gh.DeviceCode, error) {
	return gh.RequestDeviceCode(ctx, u.hc, u.cfg.WebBase, u.cfg.ClientID, u.cfg.Scopes, u.now)
}

// TryCompleteDeviceFlow makes a single exchange attempt. The caller schedules
// the retries so that the UI keeps redrawing between them.
//
// gh.ErrAuthorizationPending and gh.ErrSlowDown are the normal waiting states
// and are returned unchanged.
func (u *UseCase) TryCompleteDeviceFlow(ctx context.Context, dc *gh.DeviceCode) (*model.Credential, tokenstore.Backend, error) {
	cred, err := gh.ExchangeDeviceCode(ctx, u.hc, u.cfg.WebBase, u.cfg.ClientID, dc.DeviceCode, u.now)
	if err != nil {
		return nil, "", err
	}

	backend, err := u.tokens.Save(ctx, cred)
	if err != nil {
		return nil, "", err
	}

	u.useCredential(cred)
	return cred, backend, nil
}

// Logout drops the credential. The read records are deliberately left in place:
// they are a separate concern and should survive signing back in.
func (u *UseCase) Logout(ctx context.Context) error {
	u.setClient(nil)
	if err := u.tokens.Delete(ctx); err != nil && !errors.Is(err, tokenstore.ErrNotFound) {
		return err
	}
	return nil
}

func (u *UseCase) useCredential(cred *model.Credential) {
	u.setClient(gh.New(cred.AccessToken,
		gh.WithHTTPClient(u.hc),
		gh.WithAPIBase(u.cfg.APIBase),
	))
}

// forgetCredential is called when GitHub rejects the token, so that the next
// start goes through the device flow instead of failing again. It runs from the
// archive goroutine as well as the polling one.
func (u *UseCase) forgetCredential(ctx context.Context) {
	u.setClient(nil)
	_ = u.tokens.Delete(ctx)
}
