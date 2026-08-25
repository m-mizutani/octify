package gh_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/m-mizutani/gt"
	"github.com/m-mizutani/octify/pkg/domain/model"
	"github.com/m-mizutani/octify/pkg/domain/types"
	"github.com/m-mizutani/octify/pkg/infra/gh"
)

const oneNotification = `[
  {
    "id": "13845982",
    "unread": true,
    "reason": "review_requested",
    "updated_at": "2026-08-25T08:55:12Z",
    "subject": {
      "title": "Fix notification parser",
      "url": "https://api.github.com/repos/m-mizutani/octify/pulls/123",
      "type": "PullRequest"
    },
    "repository": {
      "full_name": "m-mizutani/octify",
      "html_url": "https://github.com/m-mizutani/octify",
      "private": false
    }
  }
]`

// newClient points a client at a test server so no request leaves the machine.
func newClient(t *testing.T, handler http.HandlerFunc) *gh.Client {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return gh.New("test-token", gh.WithAPIBase(srv.URL), gh.WithHTTPClient(srv.Client()))
}

func TestListNotificationsSuccess(t *testing.T) {
	var gotQuery, gotAuth, gotAccept, gotVersion, gotUserAgent string

	client := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		gotAuth = r.Header.Get("Authorization")
		gotAccept = r.Header.Get("Accept")
		gotVersion = r.Header.Get("X-GitHub-Api-Version")
		gotUserAgent = r.Header.Get("User-Agent")

		w.Header().Set("Last-Modified", "Mon, 25 Aug 2026 08:55:12 GMT")
		w.Header().Set("X-Poll-Interval", "60")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(oneNotification))
	})

	out := gt.R1(client.ListNotifications(t.Context(), gh.ListNotificationsInput{All: true})).NoError(t)

	gt.False(t, out.NotModified)
	gt.Equal(t, out.LastModified, "Mon, 25 Aug 2026 08:55:12 GMT")
	gt.Equal(t, out.PollInterval, 60*time.Second)
	gt.Equal(t, out.NextPage, 0)

	gt.A(t, out.Notifications).Length(1).At(0, func(t testing.TB, n model.Notification) {
		gt.Equal(t, n.ID, types.ThreadID("13845982"))
		gt.Equal(t, n.Repo.FullName, types.RepoFullName("m-mizutani/octify"))
		gt.Equal(t, n.Repo.HTMLURL, "https://github.com/m-mizutani/octify")
		gt.False(t, n.Repo.Private)
		gt.Equal(t, n.Subject.Title, "Fix notification parser")
		gt.Equal(t, n.Subject.Type, types.SubjectPullRequest)
		gt.Equal(t, n.Subject.URL, "https://api.github.com/repos/m-mizutani/octify/pulls/123")
		gt.Equal(t, n.Subject.Number, 123)
		gt.Equal(t, n.Reason, model.ReasonReviewRequested)
		gt.True(t, n.ServerUnread)
		gt.Equal(t, n.UpdatedAt, time.Date(2026, 8, 25, 8, 55, 12, 0, time.UTC))
	})

	gt.S(t, gotQuery).Contains("all=true")
	gt.S(t, gotQuery).Contains("per_page=50")
	gt.S(t, gotQuery).Contains("page=1")
	gt.Equal(t, gotAuth, "Bearer test-token")
	gt.Equal(t, gotAccept, "application/vnd.github+json")
	gt.Equal(t, gotVersion, "2022-11-28")
	gt.Equal(t, gotUserAgent, "octify")
}

func TestListNotificationsConditionalRequest(t *testing.T) {
	const lastModified = "Mon, 25 Aug 2026 08:55:12 GMT"
	var gotIfModifiedSince string

	client := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotIfModifiedSince = r.Header.Get("If-Modified-Since")
		w.Header().Set("X-Poll-Interval", "60")
		w.WriteHeader(http.StatusNotModified)
	})

	out := gt.R1(client.ListNotifications(t.Context(), gh.ListNotificationsInput{
		LastModified: lastModified,
	})).NoError(t)

	gt.Equal(t, gotIfModifiedSince, lastModified)
	gt.True(t, out.NotModified)
	gt.A(t, out.Notifications).Length(0)
	// The caller must keep polling against the same marker.
	gt.Equal(t, out.LastModified, lastModified)
	gt.Equal(t, out.PollInterval, 60*time.Second)
}

func TestListNotificationsOmitsConditionalHeaderWhenUnset(t *testing.T) {
	sawHeader := true
	client := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		_, sawHeader = r.Header["If-Modified-Since"]
		_, _ = w.Write([]byte(`[]`))
	})

	gt.R1(client.ListNotifications(t.Context(), gh.ListNotificationsInput{})).NoError(t)
	gt.False(t, sawHeader)
}

func TestListNotificationsPaging(t *testing.T) {
	testCases := map[string]struct {
		link string
		want int
	}{
		"next link present": {
			link: `<https://api.github.com/notifications?page=2>; rel="next", ` +
				`<https://api.github.com/notifications?page=9>; rel="last"`,
			want: 2,
		},
		"only last link":      {link: `<https://api.github.com/notifications?page=9>; rel="last"`, want: 0},
		"no link header":      {link: "", want: 0},
		"malformed link":      {link: `this is not a link header`, want: 0},
		"next without a page": {link: `<https://api.github.com/notifications>; rel="next"`, want: 0},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			client := newClient(t, func(w http.ResponseWriter, r *http.Request) {
				if tc.link != "" {
					w.Header().Set("Link", tc.link)
				}
				_, _ = w.Write([]byte(`[]`))
			})
			out := gt.R1(client.ListNotifications(t.Context(), gh.ListNotificationsInput{})).NoError(t)
			gt.Equal(t, out.NextPage, tc.want)
		})
	}
}

func TestListNotificationsMissingHeaders(t *testing.T) {
	testCases := map[string]struct {
		pollInterval string
		want         time.Duration
	}{
		"absent":      {pollInterval: "", want: 0},
		"not numeric": {pollInterval: "soon", want: 0},
		"negative":    {pollInterval: "-5", want: 0},
		"valid":       {pollInterval: "120", want: 120 * time.Second},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			client := newClient(t, func(w http.ResponseWriter, r *http.Request) {
				if tc.pollInterval != "" {
					w.Header().Set("X-Poll-Interval", tc.pollInterval)
				}
				_, _ = w.Write([]byte(`[]`))
			})
			out := gt.R1(client.ListNotifications(t.Context(), gh.ListNotificationsInput{})).NoError(t)
			gt.Equal(t, out.PollInterval, tc.want)
			gt.Equal(t, out.LastModified, "")
		})
	}
}

func TestListNotificationsSubjectWithoutURL(t *testing.T) {
	const body = `[
      {
        "id": "1",
        "unread": true,
        "reason": "ci_activity",
        "updated_at": "2026-08-25T08:00:00Z",
        "subject": {"title": "CI failed on main", "url": null, "type": "CheckSuite"},
        "repository": {"full_name": "acme/tools", "html_url": "https://github.com/acme/tools"}
      }
    ]`

	client := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(body))
	})

	out := gt.R1(client.ListNotifications(t.Context(), gh.ListNotificationsInput{})).NoError(t)
	gt.A(t, out.Notifications).Length(1).At(0, func(t testing.TB, n model.Notification) {
		gt.Equal(t, n.Subject.URL, "")
		gt.Equal(t, n.Subject.Number, 0)
		gt.Equal(t, n.Subject.Type, types.SubjectCheckSuite)
	})
}

func TestListNotificationsTolerateBrokenEntries(t *testing.T) {
	const body = `[
      {"id": "1", "unread": true, "reason": "comment", "updated_at": "2026-08-25T08:00:00Z",
       "subject": {"title": "a", "url": "", "type": "Issue"},
       "repository": {"full_name": "acme/tools", "html_url": "https://github.com/acme/tools"}},
      {"id": "", "unread": true, "reason": "comment", "updated_at": "2026-08-25T08:00:00Z",
       "subject": {"title": "dropped", "url": "", "type": "Issue"},
       "repository": {"full_name": "acme/tools", "html_url": "https://github.com/acme/tools"}},
      {"id": "3", "unread": true, "reason": "comment", "updated_at": "not-a-timestamp",
       "subject": {"title": "c", "url": "", "type": "Issue"},
       "repository": {"full_name": "acme/tools", "html_url": "https://github.com/acme/tools"}}
    ]`

	client := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(body))
	})

	out := gt.R1(client.ListNotifications(t.Context(), gh.ListNotificationsInput{})).NoError(t)

	// The entry without an id is dropped; the one with a bad timestamp is kept.
	gt.A(t, out.Notifications).Length(2)
	gt.Equal(t, out.Notifications[0].ID, types.ThreadID("1"))
	gt.Equal(t, out.Notifications[1].ID, types.ThreadID("3"))
	gt.True(t, out.Notifications[1].UpdatedAt.IsZero())
}

func TestListNotificationsReadEntries(t *testing.T) {
	const body = `[
      {"id": "1", "unread": false, "reason": "comment", "updated_at": "2026-08-25T08:00:00Z",
       "subject": {"title": "a", "url": "", "type": "Issue"},
       "repository": {"full_name": "acme/tools", "html_url": "https://github.com/acme/tools"}}
    ]`

	client := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(body))
	})

	out := gt.R1(client.ListNotifications(t.Context(), gh.ListNotificationsInput{All: true})).NoError(t)
	gt.A(t, out.Notifications).Length(1)
	gt.False(t, out.Notifications[0].ServerUnread)
}

func TestListNotificationsErrors(t *testing.T) {
	testCases := map[string]struct {
		status  int
		headers map[string]string
		body    string
		assert  func(t *testing.T, err error)
	}{
		"unauthorized": {
			status: http.StatusUnauthorized,
			assert: func(t *testing.T, err error) { gt.Error(t, err).Is(gh.ErrUnauthorized) },
		},
		"not found": {
			status: http.StatusNotFound,
			assert: func(t *testing.T, err error) { gt.Error(t, err).Is(gh.ErrNotFound) },
		},
		"forbidden without rate limit hints": {
			status: http.StatusForbidden,
			assert: func(t *testing.T, err error) { gt.Error(t, err).Is(gh.ErrForbidden) },
		},
		"forbidden with retry-after": {
			status:  http.StatusForbidden,
			headers: map[string]string{"Retry-After": "60"},
			assert: func(t *testing.T, err error) {
				var target *gh.RateLimitError
				gt.True(t, errors.As(err, &target))
				gt.Equal(t, target.RetryAfter, 60*time.Second)
				gt.Equal(t, target.Resource, "notifications")
			},
		},
		"too many requests with retry-after": {
			status:  http.StatusTooManyRequests,
			headers: map[string]string{"Retry-After": "30"},
			assert: func(t *testing.T, err error) {
				var target *gh.RateLimitError
				gt.True(t, errors.As(err, &target))
				gt.Equal(t, target.RetryAfter, 30*time.Second)
			},
		},
		"forbidden with exhausted rate limit": {
			status: http.StatusForbidden,
			headers: map[string]string{
				"X-RateLimit-Remaining": "0",
				"X-RateLimit-Reset":     resetIn(90 * time.Second),
			},
			assert: func(t *testing.T, err error) {
				var target *gh.RateLimitError
				gt.True(t, errors.As(err, &target))
				// Allow for the second that elapses while the test runs.
				gt.True(t, target.RetryAfter > 80*time.Second)
				gt.True(t, target.RetryAfter <= 90*time.Second)
			},
		},
		"server error": {
			status: http.StatusServiceUnavailable,
			assert: func(t *testing.T, err error) { gt.Error(t, err).Is(gh.ErrUnexpectedStatus) },
		},
		"broken json": {
			status: http.StatusOK,
			body:   `{"not": "an array"}`,
			assert: func(t *testing.T, err error) { gt.Error(t, err).Is(gh.ErrInvalidResponse) },
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			client := newClient(t, func(w http.ResponseWriter, r *http.Request) {
				for k, v := range tc.headers {
					w.Header().Set(k, v)
				}
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			})

			_, err := client.ListNotifications(t.Context(), gh.ListNotificationsInput{})
			gt.Error(t, err)
			tc.assert(t, err)

			// Every failure the user can see must carry display text.
			msg, ok := model.UserMessageOf(err)
			gt.True(t, ok)
			gt.NotEqual(t, msg.Summary, "")
		})
	}
}

func TestMarkThreadDone(t *testing.T) {
	var gotMethod, gotPath string

	client := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		if r.Method == http.MethodPatch {
			t.Error("PATCH must never be sent: read state is local to octify")
		}
		w.WriteHeader(http.StatusNoContent)
	})

	gt.NoError(t, client.MarkThreadDone(t.Context(), "13845982"))
	gt.Equal(t, gotMethod, http.MethodDelete)
	gt.Equal(t, gotPath, "/notifications/threads/13845982")
}

func TestMarkThreadDoneNotFound(t *testing.T) {
	client := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	gt.Error(t, client.MarkThreadDone(t.Context(), "1")).Is(gh.ErrNotFound)
}

func TestRequestCancellation(t *testing.T) {
	client := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	})

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	_, err := client.ListNotifications(ctx, gh.ListNotificationsInput{})
	gt.Error(t, err).Is(context.Canceled)
}

// resetIn renders an X-RateLimit-Reset value d from now.
func resetIn(d time.Duration) string {
	return strconv.FormatInt(time.Now().Add(d).Unix(), 10)
}
