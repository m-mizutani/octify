package usecase_test

import (
	"net/http"
	"testing"
	"time"

	"github.com/m-mizutani/goerr/v2"
	"github.com/m-mizutani/gt"
	"github.com/m-mizutani/octify/pkg/domain/model"
	"github.com/m-mizutani/octify/pkg/infra/gh"
	"github.com/m-mizutani/octify/pkg/infra/tokenstore"
)

func TestRestoreWithSavedCredential(t *testing.T) {
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {})
	h.tokens.cred = savedCredential()

	gt.False(t, h.uc.Authenticated())

	cred, backend, restoreErr := h.uc.Restore(t.Context())
	gt.NoError(t, restoreErr)
	gt.Equal(t, backend, tokenstore.BackendFile)
	gt.Equal(t, cred.AccessToken, savedCredential().AccessToken)
	gt.True(t, h.uc.Authenticated())
}

func TestRestoreWithoutCredential(t *testing.T) {
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {})

	_, _, err := h.uc.Restore(t.Context())
	gt.Error(t, err).Is(tokenstore.ErrNotFound)
	gt.False(t, h.uc.Authenticated())

	msg, ok := model.UserMessageOf(err)
	gt.True(t, ok)
	gt.Equal(t, msg.Summary, "not signed in")
}

func TestRestorePropagatesOtherErrors(t *testing.T) {
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {})
	h.tokens.loadErr = goerr.Wrap(model.ErrInvalidCredential, "broken")

	_, _, err := h.uc.Restore(t.Context())
	gt.Error(t, err).Is(model.ErrInvalidCredential)
	gt.False(t, h.uc.Authenticated())
}

func TestDeviceFlowStartAndComplete(t *testing.T) {
	step := 0
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/login/device/code":
			_, _ = w.Write([]byte(`{"device_code":"dc","user_code":"UC","verification_uri":"https://github.com/login/device","expires_in":900,"interval":5}`))
		case "/login/oauth/access_token":
			step++
			if step == 1 {
				_, _ = w.Write([]byte(`{"error":"authorization_pending"}`))
				return
			}
			_, _ = w.Write([]byte(`{"access_token":"gho_new","token_type":"bearer","scope":"repo,notifications"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})

	dc := gt.R1(h.uc.StartDeviceFlow(t.Context())).NoError(t)
	gt.Equal(t, dc.UserCode, "UC")
	gt.Equal(t, dc.Interval, 5*time.Second)
	gt.Equal(t, dc.ExpiresAt, fixedNow.Add(900*time.Second))

	// The first attempt is the normal waiting state and must not save anything.
	_, _, err := h.uc.TryCompleteDeviceFlow(t.Context(), dc)
	gt.Error(t, err).Is(gh.ErrAuthorizationPending)
	gt.Equal(t, h.tokens.saves, 0)
	gt.False(t, h.uc.Authenticated())

	cred, _, completeErr := h.uc.TryCompleteDeviceFlow(t.Context(), dc)
	gt.NoError(t, completeErr)
	gt.Equal(t, cred.AccessToken, model.Credential{AccessToken: "gho_new"}.AccessToken)
	gt.Equal(t, h.tokens.saves, 1)
	gt.True(t, h.uc.Authenticated())
}

func TestDeviceFlowDeniedDoesNotSave(t *testing.T) {
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"error":"access_denied"}`))
	})

	_, _, err := h.uc.TryCompleteDeviceFlow(t.Context(), &gh.DeviceCode{DeviceCode: "dc"})
	gt.Error(t, err).Is(gh.ErrAccessDenied)
	gt.Equal(t, h.tokens.saves, 0)
	gt.False(t, h.uc.Authenticated())
}

func TestDeviceFlowSaveFailurePropagates(t *testing.T) {
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"access_token":"gho_new","token_type":"bearer"}`))
	})
	h.tokens.saveErr = goerr.New("keychain refused")

	_, _, err := h.uc.TryCompleteDeviceFlow(t.Context(), &gh.DeviceCode{DeviceCode: "dc"})
	gt.Error(t, err)
	// A token that could not be saved must not be treated as usable.
	gt.False(t, h.uc.Authenticated())
}

func TestLogout(t *testing.T) {
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {})
	h.authenticate(t)

	gt.NoError(t, h.uc.Logout(t.Context()))
	gt.False(t, h.uc.Authenticated())
	gt.Equal(t, h.tokens.deletes, 1)
}

func TestUnauthorizedPollDropsCredential(t *testing.T) {
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	})
	h.authenticate(t)

	_, err := h.uc.Poll(t.Context(), model.PollState{})
	gt.Error(t, err).Is(gh.ErrUnauthorized)

	// The next start has to go through the device flow rather than fail again.
	gt.False(t, h.uc.Authenticated())
	gt.Equal(t, h.tokens.deletes, 1)
}
