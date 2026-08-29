package gh

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/m-mizutani/goerr/v2"
	"github.com/m-mizutani/octify/pkg/domain/model"
	"github.com/m-mizutani/octify/pkg/domain/types"
	"github.com/m-mizutani/octify/pkg/utils/safe"
)

// deviceGrantType is the grant_type value the device flow requires.
const deviceGrantType = "urn:ietf:params:oauth:grant-type:device_code"

var (
	ErrAuthorizationPending = goerr.New("device flow: authorization pending")
	ErrSlowDown             = goerr.New("device flow: slow down")
	ErrExpiredToken         = goerr.New("device flow: code expired")
	ErrAccessDenied         = goerr.New("device flow: access denied")
	ErrDeviceFlowDisabled   = goerr.New("device flow: disabled for this OAuth app")
)

// DeviceCode is what GitHub hands back to start the device flow.
type DeviceCode struct {
	// DeviceCode is exchanged for a token and is redacted from logs by field name.
	DeviceCode string
	// UserCode is shown to the user, so it is not treated as a secret.
	UserCode        string
	VerificationURI string
	ExpiresAt       time.Time
	Interval        time.Duration
}

type deviceCodeResponse struct {
	DeviceCode      string `json:"device_code"`
	UserCode        string `json:"user_code"`
	VerificationURI string `json:"verification_uri"`
	ExpiresIn       int    `json:"expires_in"`
	Interval        int    `json:"interval"`
	Error           string `json:"error"`
}

type accessTokenResponse struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
	Scope       string `json:"scope"`
	Error       string `json:"error"`
}

// RequestDeviceCode starts the device flow. It talks to the web host, not the
// REST API host, and needs no credential.
func RequestDeviceCode(ctx context.Context, hc *http.Client, webBase, clientID string, scopes []string, now func() time.Time) (*DeviceCode, error) {
	form := url.Values{}
	form.Set("client_id", clientID)
	if len(scopes) > 0 {
		form.Set("scope", strings.Join(scopes, " "))
	}

	var body deviceCodeResponse
	if err := postForm(ctx, hc, trimSlash(webBase)+"/login/device/code", form, &body); err != nil {
		return nil, err
	}
	if body.Error != "" {
		return nil, deviceFlowError(body.Error)
	}
	if body.DeviceCode == "" || body.UserCode == "" {
		return nil, invalidResponse(goerr.New("device code missing in response"), "device_code")
	}

	expiresIn := body.ExpiresIn
	if expiresIn <= 0 {
		expiresIn = 900
	}
	interval := body.Interval
	if interval <= 0 {
		interval = 5
	}

	return &DeviceCode{
		DeviceCode:      body.DeviceCode,
		UserCode:        body.UserCode,
		VerificationURI: body.VerificationURI,
		ExpiresAt:       now().Add(time.Duration(expiresIn) * time.Second),
		Interval:        time.Duration(interval) * time.Second,
	}, nil
}

// ExchangeDeviceCode makes one attempt to turn the device code into a token.
// The caller drives the polling so that the UI stays responsive between tries.
func ExchangeDeviceCode(ctx context.Context, hc *http.Client, webBase, clientID, deviceCode string, now func() time.Time) (*model.Credential, error) {
	form := url.Values{}
	form.Set("client_id", clientID)
	form.Set("device_code", deviceCode)
	form.Set("grant_type", deviceGrantType)

	var body accessTokenResponse
	if err := postForm(ctx, hc, trimSlash(webBase)+"/login/oauth/access_token", form, &body); err != nil {
		return nil, err
	}
	if body.Error != "" {
		return nil, deviceFlowError(body.Error)
	}
	if body.AccessToken == "" {
		return nil, invalidResponse(goerr.New("access token missing in response"), "access_token")
	}

	host := "github.com"
	if u, err := url.Parse(webBase); err == nil && u.Host != "" {
		host = u.Host
	}

	return &model.Credential{
		Version:     model.CredentialVersion,
		Host:        host,
		AccessToken: types.AccessToken(body.AccessToken),
		TokenType:   body.TokenType,
		Scope:       body.Scope,
		StoredAt:    now(),
	}, nil
}

// postForm sends a form-encoded request and decodes a JSON response. The device
// flow endpoints answer with HTTP 200 even for errors, so the status check only
// catches transport-level problems.
func postForm(ctx context.Context, hc *http.Client, endpoint string, form url.Values, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return goerr.Wrap(err, "failed to build device flow request", goerr.V("url", endpoint))
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", defaultUserAgent)

	resp, err := hc.Do(req)
	if err != nil {
		return transportError(err, req.URL.Host, "device_flow")
	}
	defer safe.Close(ctx, resp.Body)

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return classify(resp, "device_flow")
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return invalidResponse(err, "device_flow")
	}
	return nil
}

func deviceFlowError(code string) error {
	switch code {
	case "authorization_pending":
		// Expected on every poll until the user finishes; not shown on screen.
		return goerr.Wrap(ErrAuthorizationPending, "waiting for the user to authorize")
	case "slow_down":
		return goerr.Wrap(ErrSlowDown, "github asked to poll less often")
	case "expired_token":
		return model.WithUserMessage(
			goerr.Wrap(ErrExpiredToken, "device code expired"),
			model.UserMessage{Summary: "the device code expired", Action: "press o to start over"},
		)
	case "access_denied":
		return model.WithUserMessage(
			goerr.Wrap(ErrAccessDenied, "user declined the authorization"),
			model.UserMessage{Summary: "authorization was declined on GitHub", Action: "press o to try again"},
		)
	case "device_flow_disabled":
		return model.WithUserMessage(
			goerr.Wrap(ErrDeviceFlowDisabled, "device flow is disabled for this oauth app"),
			model.UserMessage{
				Summary: "device flow is turned off for this OAuth app",
				Action:  "enable it in the app settings on GitHub",
			},
		)
	default:
		return model.WithUserMessage(
			goerr.Wrap(ErrUnexpectedStatus, "unknown device flow error", goerr.V("error", code)),
			model.UserMessage{Summary: "GitHub returned an unknown authorization error"},
		)
	}
}
