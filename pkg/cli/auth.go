package cli

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/m-mizutani/goerr/v2"
	"github.com/m-mizutani/octify/pkg/domain/model"
	"github.com/m-mizutani/octify/pkg/infra/gh"
	"github.com/m-mizutani/octify/pkg/infra/tokenstore"
	"github.com/m-mizutani/octify/pkg/usecase"
	"github.com/m-mizutani/octify/pkg/utils/safe"
	ucli "github.com/urfave/cli/v3"
)

// slowDownPenalty matches what GitHub documents for the device flow.
const slowDownPenalty = 5 * time.Second

func authCommand(opt *options) *ucli.Command {
	return &ucli.Command{
		Name:  "auth",
		Usage: "manage the GitHub credential",
		Commands: []*ucli.Command{
			{
				Name:   "login",
				Usage:  "sign in with the OAuth device flow",
				Action: func(ctx context.Context, cmd *ucli.Command) error { return authLogin(ctx, cmd, opt) },
			},
			{
				Name:   "logout",
				Usage:  "delete the saved credential",
				Action: func(ctx context.Context, cmd *ucli.Command) error { return authLogout(ctx, cmd, opt) },
			},
			{
				Name:   "status",
				Usage:  "show whether a credential is saved, and where",
				Action: func(ctx context.Context, cmd *ucli.Command) error { return authStatus(ctx, cmd, opt) },
			},
		},
	}
}

func authLogin(ctx context.Context, cmd *ucli.Command, opt *options) error {
	ctx, uc, closer, err := opt.build(ctx, false)
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

	dc, err := uc.StartDeviceFlow(ctx)
	if err != nil {
		return err
	}

	out := cmd.Root().Writer
	fmt.Fprintf(out, "Open %s and enter the code: %s\n", dc.VerificationURI, dc.UserCode)
	fmt.Fprintln(out, "Waiting for authorization...")

	cred, backend, err := pollForToken(ctx, uc, dc)
	if err != nil {
		return err
	}

	fmt.Fprintf(out, "Signed in to %s with scopes: %s\n", cred.Host, cred.Scope)
	// Saying where it went matters: when the keychain is unavailable the token
	// ends up in a plain file, and the user should know that happened.
	fmt.Fprintf(out, "Credential stored in the %s.\n", backendLabel(backend))
	return nil
}

func backendLabel(backend tokenstore.Backend) string {
	if backend == tokenstore.BackendKeyring {
		return "OS keychain"
	}
	return "credential file"
}

// pollForToken retries at the interval GitHub asked for until it succeeds, the
// code expires, or the user declines.
func pollForToken(ctx context.Context, uc *usecase.UseCase, dc *gh.DeviceCode) (*model.Credential, tokenstore.Backend, error) {
	interval := dc.Interval

	for {
		select {
		case <-ctx.Done():
			// Interrupting the wait is a normal way out, so it stays a bare
			// context.Canceled for main to recognise instead of a reportable error.
			return nil, "", ctx.Err()
		case <-time.After(interval):
		}

		if time.Now().After(dc.ExpiresAt) {
			return nil, "", model.WithUserMessage(
				goerr.Wrap(gh.ErrExpiredToken, "device code expired before authorization"),
				model.UserMessage{Summary: "the device code expired", Action: "run octify auth login again"},
			)
		}

		cred, backend, err := uc.TryCompleteDeviceFlow(ctx, dc)
		switch {
		case err == nil:
			return cred, backend, nil
		case errors.Is(err, gh.ErrAuthorizationPending):
			continue
		case errors.Is(err, gh.ErrSlowDown):
			interval += slowDownPenalty
		default:
			return nil, "", err
		}
	}
}

func authLogout(ctx context.Context, cmd *ucli.Command, opt *options) error {
	ctx, uc, closer, err := opt.build(ctx, false)
	if err != nil {
		return err
	}
	defer safe.Close(ctx, closer)

	if err := uc.Logout(ctx); err != nil {
		return err
	}

	// The read records are a separate concern and are deliberately kept, so
	// signing back in does not reset what the user has already triaged.
	fmt.Fprintln(cmd.Root().Writer, "Signed out. Read/unread records were kept.")
	return nil
}

func authStatus(ctx context.Context, cmd *ucli.Command, opt *options) error {
	ctx, uc, closer, err := opt.build(ctx, false)
	if err != nil {
		return err
	}
	defer safe.Close(ctx, closer)

	out := cmd.Root().Writer

	cred, backend, err := uc.Restore(ctx)
	if err != nil {
		if errors.Is(err, tokenstore.ErrNotFound) {
			fmt.Fprintln(out, "Not signed in.")
			return nil
		}
		return err
	}

	// The token itself is never printed.
	fmt.Fprintf(out, "Signed in to %s\n", cred.Host)
	fmt.Fprintf(out, "  stored in: %s\n", backendLabel(backend))
	fmt.Fprintf(out, "  scopes:    %s\n", cred.Scope)
	fmt.Fprintf(out, "  saved at:  %s\n", cred.StoredAt.Format(time.RFC3339))
	return nil
}
