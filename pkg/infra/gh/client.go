package gh

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"time"

	"github.com/m-mizutani/goerr/v2"
	"github.com/m-mizutani/octify/pkg/domain/types"
)

const (
	// DefaultAPIBase is the REST root for github.com.
	DefaultAPIBase = "https://api.github.com"
	// DefaultWebBase is where the device flow endpoints and the web UI live.
	DefaultWebBase = "https://github.com"

	apiVersion       = "2022-11-28"
	defaultUserAgent = "octify"
)

// Client talks to the handful of GitHub endpoints octify needs. It is written
// directly on net/http because every call depends on request and response
// headers (If-Modified-Since, Last-Modified, x-poll-interval, Retry-After).
type Client struct {
	token     types.AccessToken
	hc        *http.Client
	apiBase   string
	userAgent string
}

type Option func(*Client)

func WithHTTPClient(hc *http.Client) Option {
	return func(c *Client) {
		if hc != nil {
			c.hc = hc
		}
	}
}

func WithAPIBase(rawURL string) Option {
	return func(c *Client) {
		if rawURL != "" {
			c.apiBase = trimSlash(rawURL)
		}
	}
}

func WithUserAgent(ua string) Option {
	return func(c *Client) {
		if ua != "" {
			c.userAgent = ua
		}
	}
}

func New(token types.AccessToken, opts ...Option) *Client {
	c := &Client{
		token:     token,
		hc:        &http.Client{Timeout: 30 * time.Second},
		apiBase:   DefaultAPIBase,
		userAgent: defaultUserAgent,
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

func (c *Client) newRequest(ctx context.Context, method, path string, query url.Values) (*http.Request, error) {
	full := c.apiBase + path
	if len(query) > 0 {
		full += "?" + query.Encode()
	}

	req, err := http.NewRequestWithContext(ctx, method, full, nil)
	if err != nil {
		return nil, goerr.Wrap(err, "failed to build request",
			goerr.V("method", method), goerr.V("url", full))
	}

	req.Header.Set("Authorization", "Bearer "+string(c.token))
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", apiVersion)
	req.Header.Set("User-Agent", c.userAgent)
	return req, nil
}

// do sends the request and converts anything that is not a success or 304 into
// a classified error. The caller owns closing the body of a returned response.
func (c *Client) do(req *http.Request, resource string) (*http.Response, error) {
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, transportError(err, req.URL.Host, resource)
	}

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return resp, nil
	}
	if resp.StatusCode == http.StatusNotModified {
		return resp, nil
	}

	// The body of an error response is not surfaced to the user, but draining it
	// lets the connection be reused.
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
	_ = resp.Body.Close()
	return nil, classify(resp, resource)
}

func trimSlash(s string) string {
	for len(s) > 0 && s[len(s)-1] == '/' {
		s = s[:len(s)-1]
	}
	return s
}

// pollInterval reads x-poll-interval. A missing or malformed value is reported
// as zero rather than an error, because the caller has its own floor.
func pollInterval(h http.Header) time.Duration {
	v := h.Get("X-Poll-Interval")
	if v == "" {
		return 0
	}
	secs, err := strconv.Atoi(v)
	if err != nil || secs < 0 {
		return 0
	}
	return time.Duration(secs) * time.Second
}

var linkNextRe = regexp.MustCompile(`<([^>]+)>\s*;\s*rel="next"`)

// nextPage returns the page number of the rel="next" link, or 0 when there is
// none. A malformed header is treated as "no next page" rather than an error.
func nextPage(h http.Header) int {
	m := linkNextRe.FindStringSubmatch(h.Get("Link"))
	if len(m) != 2 {
		return 0
	}
	u, err := url.Parse(m[1])
	if err != nil {
		return 0
	}
	page, err := strconv.Atoi(u.Query().Get("page"))
	if err != nil || page <= 0 {
		return 0
	}
	return page
}
