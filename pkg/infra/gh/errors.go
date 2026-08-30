package gh

import (
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/m-mizutani/goerr/v2"
	"github.com/m-mizutani/octify/pkg/domain/model"
)

var (
	ErrUnauthorized     = goerr.New("github: unauthorized")
	ErrForbidden        = goerr.New("github: forbidden")
	ErrNotFound         = goerr.New("github: resource not found")
	ErrUnexpectedStatus = goerr.New("github: unexpected status")
	ErrInvalidResponse  = goerr.New("github: invalid response body")
	// ErrGraphQLRequestFailed covers a GraphQL response that answered nothing.
	// GitHub reports an exhausted point budget or a rejected document as HTTP
	// 200 with a top-level error, so the status code alone cannot catch it.
	ErrGraphQLRequestFailed = goerr.New("github: graphql request failed")
)

// RateLimitError reports that GitHub asked the caller to slow down. RetryAfter
// is how long to wait; it is zero when GitHub did not say.
type RateLimitError struct {
	RetryAfter time.Duration
	Resource   string
}

func (e *RateLimitError) Error() string {
	return fmt.Sprintf("github: rate limited on %s, retry after %s", e.Resource, e.RetryAfter)
}

// classify turns a non-success response into an error carrying both the display
// text and the values needed to diagnose it from the log.
func classify(resp *http.Response, resource string) error {
	base := []goerr.Option{
		goerr.V("status", resp.StatusCode),
		goerr.V("method", resp.Request.Method),
		goerr.V("path", resp.Request.URL.Path),
		goerr.V("resource", resource),
	}

	switch resp.StatusCode {
	case http.StatusUnauthorized:
		return model.WithUserMessage(
			goerr.Wrap(ErrUnauthorized, "github rejected the credential", base...),
			model.UserMessage{
				Summary: "GitHub rejected the saved token",
				Action:  "press o to sign in again",
			},
		)

	case http.StatusForbidden, http.StatusTooManyRequests:
		if d, ok := retryAfter(resp); ok {
			return model.WithUserMessage(
				goerr.Wrap(&RateLimitError{RetryAfter: d, Resource: resource},
					"github rate limited the request",
					append(base, goerr.V("retry_after", d.String()))...),
				model.UserMessage{
					Summary: "GitHub rate limit reached",
					Action:  "waiting for the limit to reset",
				},
			)
		}
		return model.WithUserMessage(
			goerr.Wrap(ErrForbidden, "github denied the request", base...),
			model.UserMessage{
				Summary: "GitHub denied the request",
				Action:  "check that the token has the repo and notifications scopes",
			},
		)

	case http.StatusNotFound:
		return model.WithUserMessage(
			goerr.Wrap(ErrNotFound, "github resource not found", base...),
			model.UserMessage{Summary: "that notification no longer exists on GitHub"},
		)

	default:
		return model.WithUserMessage(
			goerr.Wrap(ErrUnexpectedStatus, "unexpected github response", base...),
			model.UserMessage{
				Summary: fmt.Sprintf("GitHub returned %d", resp.StatusCode),
			},
		)
	}
}

// retryAfter reads how long to wait, preferring the explicit Retry-After header
// and falling back to the rate limit reset time when the budget is exhausted.
func retryAfter(resp *http.Response) (time.Duration, bool) {
	if v := resp.Header.Get("Retry-After"); v != "" {
		if secs, err := strconv.Atoi(v); err == nil && secs >= 0 {
			return time.Duration(secs) * time.Second, true
		}
	}

	if resp.Header.Get("X-RateLimit-Remaining") == "0" {
		if v := resp.Header.Get("X-RateLimit-Reset"); v != "" {
			if unix, err := strconv.ParseInt(v, 10, 64); err == nil {
				d := time.Until(time.Unix(unix, 0))
				if d < 0 {
					d = 0
				}
				return d, true
			}
		}
	}

	return 0, false
}

func invalidResponse(err error, resource string) error {
	return model.WithUserMessage(
		goerr.Wrap(ErrInvalidResponse, "could not decode github response",
			goerr.V("cause", err.Error()), goerr.V("resource", resource)),
		model.UserMessage{Summary: "could not read GitHub's response"},
	)
}

// transportError covers everything that stops the request from reaching GitHub.
func transportError(err error, host, resource string) error {
	return model.WithUserMessage(
		goerr.Wrap(err, "github request failed", goerr.V("host", host), goerr.V("resource", resource)),
		model.UserMessage{Summary: "cannot reach " + host},
	)
}
