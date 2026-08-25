package gh_test

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/m-mizutani/gt"
	"github.com/m-mizutani/octify/pkg/domain/model"
	"github.com/m-mizutani/octify/pkg/domain/types"
	"github.com/m-mizutani/octify/pkg/infra/gh"
)

var fixedNow = time.Date(2026, 8, 25, 9, 0, 0, 0, time.UTC)

func nowFunc() time.Time { return fixedNow }

func newWebServer(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return srv
}

func TestRequestDeviceCode(t *testing.T) {
	var gotClientID, gotScope, gotAccept, gotPath string

	srv := newWebServer(t, func(w http.ResponseWriter, r *http.Request) {
		gt.NoError(t, r.ParseForm())
		gotPath = r.URL.Path
		gotClientID = r.PostForm.Get("client_id")
		gotScope = r.PostForm.Get("scope")
		gotAccept = r.Header.Get("Accept")

		_, _ = w.Write([]byte(`{
		  "device_code": "dc-123",
		  "user_code": "ABCD-EFGH",
		  "verification_uri": "https://github.com/login/device",
		  "expires_in": 900,
		  "interval": 5
		}`))
	})

	dc := gt.R1(gh.RequestDeviceCode(t.Context(), srv.Client(), srv.URL,
		"client-id", []string{"repo", "notifications"}, nowFunc)).NoError(t)

	gt.Equal(t, gotPath, "/login/device/code")
	gt.Equal(t, gotClientID, "client-id")
	gt.Equal(t, gotScope, "repo notifications")
	gt.Equal(t, gotAccept, "application/json")

	gt.Equal(t, dc.DeviceCode, "dc-123")
	gt.Equal(t, dc.UserCode, "ABCD-EFGH")
	gt.Equal(t, dc.VerificationURI, "https://github.com/login/device")
	gt.Equal(t, dc.Interval, 5*time.Second)
	gt.Equal(t, dc.ExpiresAt, fixedNow.Add(900*time.Second))
}

func TestRequestDeviceCodeDefaults(t *testing.T) {
	srv := newWebServer(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"device_code": "dc", "user_code": "UC"}`))
	})

	dc := gt.R1(gh.RequestDeviceCode(t.Context(), srv.Client(), srv.URL, "id", nil, nowFunc)).NoError(t)

	// GitHub documents 900s and a 5s interval; fall back to those when omitted.
	gt.Equal(t, dc.ExpiresAt, fixedNow.Add(900*time.Second))
	gt.Equal(t, dc.Interval, 5*time.Second)
}

func TestRequestDeviceCodeMissingFields(t *testing.T) {
	srv := newWebServer(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"user_code": "UC"}`))
	})
	_, err := gh.RequestDeviceCode(t.Context(), srv.Client(), srv.URL, "id", nil, nowFunc)
	gt.Error(t, err).Is(gh.ErrInvalidResponse)
}

func TestExchangeDeviceCodeSuccess(t *testing.T) {
	var gotGrantType, gotDeviceCode, gotClientID string

	srv := newWebServer(t, func(w http.ResponseWriter, r *http.Request) {
		gt.NoError(t, r.ParseForm())
		gotGrantType = r.PostForm.Get("grant_type")
		gotDeviceCode = r.PostForm.Get("device_code")
		gotClientID = r.PostForm.Get("client_id")

		_, _ = w.Write([]byte(`{
		  "access_token": "gho_token",
		  "token_type": "bearer",
		  "scope": "repo,notifications"
		}`))
	})

	cred := gt.R1(gh.ExchangeDeviceCode(t.Context(), srv.Client(), srv.URL, "client-id", "dc-123", nowFunc)).NoError(t)

	gt.Equal(t, gotGrantType, "urn:ietf:params:oauth:grant-type:device_code")
	gt.Equal(t, gotDeviceCode, "dc-123")
	gt.Equal(t, gotClientID, "client-id")

	gt.Equal(t, cred.Version, model.CredentialVersion)
	gt.Equal(t, cred.AccessToken, types.AccessToken("gho_token"))
	gt.Equal(t, cred.TokenType, "bearer")
	gt.Equal(t, cred.Scope, "repo,notifications")
	gt.Equal(t, cred.StoredAt, fixedNow)
	gt.NoError(t, cred.Validate())
}

func TestExchangeDeviceCodeHostFromWebBase(t *testing.T) {
	srv := newWebServer(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"access_token": "t", "token_type": "bearer"}`))
	})

	cred := gt.R1(gh.ExchangeDeviceCode(t.Context(), srv.Client(), srv.URL, "id", "dc", nowFunc)).NoError(t)
	// The host is taken from the configured web base, not hardcoded.
	gt.NotEqual(t, cred.Host, "")
	gt.S(t, cred.Host).Contains("127.0.0.1")
}

func TestExchangeDeviceCodeErrors(t *testing.T) {
	testCases := map[string]struct {
		code       string
		wantErr    error
		wantHidden bool
	}{
		// Pending and slow_down happen on every poll, so they carry no display text.
		"authorization_pending": {code: "authorization_pending", wantErr: gh.ErrAuthorizationPending, wantHidden: true},
		"slow_down":             {code: "slow_down", wantErr: gh.ErrSlowDown, wantHidden: true},
		"expired_token":         {code: "expired_token", wantErr: gh.ErrExpiredToken},
		"access_denied":         {code: "access_denied", wantErr: gh.ErrAccessDenied},
		"device_flow_disabled":  {code: "device_flow_disabled", wantErr: gh.ErrDeviceFlowDisabled},
		"unknown code":          {code: "something_else", wantErr: gh.ErrUnexpectedStatus},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			srv := newWebServer(t, func(w http.ResponseWriter, r *http.Request) {
				// GitHub answers device flow errors with HTTP 200.
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`{"error": "` + tc.code + `"}`))
			})

			_, err := gh.ExchangeDeviceCode(t.Context(), srv.Client(), srv.URL, "id", "dc", nowFunc)
			gt.Error(t, err).Is(tc.wantErr)

			_, hasMessage := model.UserMessageOf(err)
			gt.Equal(t, hasMessage, !tc.wantHidden)
		})
	}
}

func TestExchangeDeviceCodeMissingToken(t *testing.T) {
	srv := newWebServer(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"token_type": "bearer"}`))
	})
	_, err := gh.ExchangeDeviceCode(t.Context(), srv.Client(), srv.URL, "id", "dc", nowFunc)
	gt.Error(t, err).Is(gh.ErrInvalidResponse)
}

func TestDeviceFlowTransportFailure(t *testing.T) {
	srv := newWebServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	})
	_, err := gh.ExchangeDeviceCode(t.Context(), srv.Client(), srv.URL, "id", "dc", nowFunc)
	gt.Error(t, err).Is(gh.ErrUnexpectedStatus)
}
