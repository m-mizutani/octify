package usecase_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/m-mizutani/gt"
	"github.com/m-mizutani/octify/pkg/domain/model"
	"github.com/m-mizutani/octify/pkg/domain/types"
	"github.com/m-mizutani/octify/pkg/infra/gh"
	"github.com/m-mizutani/octify/pkg/usecase"
)

// notificationJSON renders one list entry with the given id and timestamp.
func notificationJSON(id string, updatedAt time.Time) string {
	return fmt.Sprintf(`{
	  "id": %q,
	  "unread": true,
	  "reason": "comment",
	  "updated_at": %q,
	  "subject": {"title": "t", "url": "https://api.github.com/repos/acme/tools/issues/1", "type": "Issue"},
	  "repository": {"full_name": "acme/tools", "html_url": "https://github.com/acme/tools"}
	}`, id, updatedAt.Format(time.RFC3339))
}

// checkSuiteJSON renders a list entry that points at nothing GitHub can resolve
// back to an issue or a pull request.
func checkSuiteJSON(id string, updatedAt time.Time) string {
	return fmt.Sprintf(`{
	  "id": %q,
	  "unread": true,
	  "reason": "ci_activity",
	  "updated_at": %q,
	  "subject": {"title": "build", "url": null, "type": "CheckSuite"},
	  "repository": {"full_name": "acme/tools", "html_url": "https://github.com/acme/tools"}
	}`, id, updatedAt.Format(time.RFC3339))
}

func emptySearch(w http.ResponseWriter) {
	_, _ = w.Write([]byte(`{"total_count":0,"incomplete_results":false,"items":[]}`))
}

// emptyStates answers the state lookup with every alias unresolved, which is
// what a poll sees when nothing in the list can be reached.
func emptyStates(w http.ResponseWriter) {
	_, _ = w.Write([]byte(`{"data":{}}`))
}

// statesJSON answers the state lookup by resolving every alias the query asked
// for as the same subject, which is enough for tests that only care that the
// states reached the result.
func statesJSON(t *testing.T, w http.ResponseWriter, r *http.Request) {
	t.Helper()

	var body struct {
		Query string `json:"query"`
	}
	gt.NoError(t, json.NewDecoder(r.Body).Decode(&body))

	data := map[string]any{}
	for _, line := range strings.Split(body.Query, "\n") {
		alias, _, ok := strings.Cut(strings.TrimSpace(line), ":")
		if !ok || !strings.HasPrefix(alias, "s") {
			continue
		}
		data[alias] = map[string]any{"subject": map[string]any{
			"__typename": "Issue", "state": "CLOSED", "viewerDidAuthor": true,
		}}
	}
	gt.NoError(t, json.NewEncoder(w).Encode(map[string]any{"data": data}))
}

func TestPollSuccess(t *testing.T) {
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/notifications":
			gt.Equal(t, r.URL.Query().Get("all"), "true")
			w.Header().Set("Last-Modified", "Mon, 25 Aug 2026 08:55:12 GMT")
			w.Header().Set("X-Poll-Interval", "60")
			_, _ = w.Write([]byte("[" + notificationJSON("1", fixedNow) + "]"))
		case "/search/issues":
			_, _ = w.Write([]byte(`{"total_count":1,"incomplete_results":false,"items":[
			  {"number":7,"repository_url":"https://api.github.com/repos/acme/tools","pull_request":{"url":"x"}}]}`))
		case "/graphql":
			statesJSON(t, w, r)
		}
	})
	h.authenticate(t)

	res := gt.R1(h.uc.Poll(t.Context(), model.PollState{})).NoError(t)

	gt.False(t, res.NotModified)
	gt.A(t, res.Notifications).Length(1)
	gt.True(t, res.ReviewRequests.Has(model.SubjectRef{Repo: "acme/tools", Number: 7}))
	gt.Nil(t, res.ReviewErr)

	gt.Nil(t, res.StateErr)
	state, ok := res.SubjectStates.Lookup(model.SubjectRef{Repo: "acme/tools", Number: 1})
	gt.True(t, ok)
	gt.Equal(t, state, model.SubjectState{Authored: true, Closed: true})
	gt.False(t, res.Truncated)
	gt.Equal(t, res.NextState.LastModified, "Mon, 25 Aug 2026 08:55:12 GMT")
	gt.Equal(t, res.NextState.Failures, 0)
	gt.Equal(t, res.NextInterval, 60*time.Second)
}

func TestPollNotModifiedSkipsSearch(t *testing.T) {
	searchCalls, stateCalls := 0, 0
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/notifications":
			w.Header().Set("X-Poll-Interval", "60")
			w.WriteHeader(http.StatusNotModified)
		case "/search/issues":
			searchCalls++
			emptySearch(w)
		case "/graphql":
			stateCalls++
			emptyStates(w)
		}
	})
	h.authenticate(t)

	res := gt.R1(h.uc.Poll(t.Context(), model.PollState{LastModified: "prev"})).NoError(t)

	gt.True(t, res.NotModified)
	gt.A(t, res.Notifications).Length(0)
	gt.Equal(t, res.NextState.LastModified, "prev")
	// A 304 costs nothing, so neither the search nor the state lookup must spend
	// a request on it.
	gt.Equal(t, searchCalls, 0)
	gt.Equal(t, stateCalls, 0)
}

// A conditional request that keeps answering 304 never reaches the state
// lookup, so a pull request merged without its thread being touched would keep
// a stale marker forever. The streak eventually forces an unconditional cycle.
func TestPollRefreshesAfterAStreakOfNotModified(t *testing.T) {
	var conditional []bool
	stateCalls := 0

	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/notifications":
			asked := r.Header.Get("If-Modified-Since") != ""
			conditional = append(conditional, asked)
			if asked {
				w.WriteHeader(http.StatusNotModified)
				return
			}
			w.Header().Set("Last-Modified", "Mon, 25 Aug 2026 08:55:12 GMT")
			_, _ = w.Write([]byte("[" + notificationJSON("1", fixedNow) + "]"))
		case "/search/issues":
			emptySearch(w)
		case "/graphql":
			stateCalls++
			statesJSON(t, w, r)
		}
	})
	h.authenticate(t)

	st := model.PollState{LastModified: "prev"}
	for i := 0; i < 10; i++ {
		res := gt.R1(h.uc.Poll(t.Context(), st)).NoError(t)
		gt.True(t, res.NotModified)
		gt.Equal(t, res.NextState.NotModifiedStreak, i+1)
		st = res.NextState
	}

	// Ten cycles cost nothing at all.
	gt.Equal(t, stateCalls, 0)

	// The eleventh asks unconditionally and the markers come back.
	res := gt.R1(h.uc.Poll(t.Context(), st)).NoError(t)
	gt.False(t, res.NotModified)
	gt.Equal(t, stateCalls, 1)
	gt.Equal(t, len(res.SubjectStates), 1)

	// A full answer starts the count over.
	gt.Equal(t, res.NextState.NotModifiedStreak, 0)
	gt.Equal(t, res.NextState.LastModified, "Mon, 25 Aug 2026 08:55:12 GMT")

	gt.Equal(t, conditional, []bool{true, true, true, true, true, true, true, true, true, true, false})
}

// A failed cycle answered nothing, so it must not push the unconditional
// refresh further away.
func TestPollFailureKeepsTheNotModifiedStreak(t *testing.T) {
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	})
	h.authenticate(t)

	res, err := h.uc.Poll(t.Context(), model.PollState{NotModifiedStreak: 7})
	gt.Error(t, err)
	gt.Equal(t, res.NextState.NotModifiedStreak, 7)
	gt.Equal(t, res.NextState.Failures, 1)
}

func TestPollNextInterval(t *testing.T) {
	testCases := map[string]struct {
		minInterval  time.Duration
		serverHeader string
		want         time.Duration
	}{
		"server value wins over a smaller setting": {10 * time.Second, "60", 60 * time.Second},
		"setting wins over a smaller server value": {120 * time.Second, "60", 120 * time.Second},
		"absent server value falls back":           {45 * time.Second, "", 45 * time.Second},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/notifications":
					if tc.serverHeader != "" {
						w.Header().Set("X-Poll-Interval", tc.serverHeader)
					}
					_, _ = w.Write([]byte(`[]`))
				case "/search/issues":
					emptySearch(w)
				case "/graphql":
					emptyStates(w)
				}
			}, withConfig(func(c *usecase.Config) { c.MinInterval = tc.minInterval }))
			h.authenticate(t)

			res := gt.R1(h.uc.Poll(t.Context(), model.PollState{})).NoError(t)
			gt.Equal(t, res.NextInterval, tc.want)
		})
	}
}

func TestPollBackoff(t *testing.T) {
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}, withConfig(func(c *usecase.Config) { c.MinInterval = 60 * time.Second }))
	h.authenticate(t)

	testCases := []struct {
		failures int
		want     time.Duration
	}{
		{0, 120 * time.Second},
		{1, 240 * time.Second},
		{2, 480 * time.Second},
		{3, 480 * time.Second},
		{9, 480 * time.Second},
	}

	for _, tc := range testCases {
		t.Run(strconv.Itoa(tc.failures), func(t *testing.T) {
			res, err := h.uc.Poll(t.Context(), model.PollState{Failures: tc.failures})
			gt.Error(t, err).Is(gh.ErrUnexpectedStatus)
			gt.NotNil(t, res)
			gt.Equal(t, res.NextInterval, tc.want)
			gt.Equal(t, res.NextState.Failures, tc.failures+1)
		})
	}
}

func TestPollSuccessResetsFailures(t *testing.T) {
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/notifications":
			_, _ = w.Write([]byte(`[]`))
		case "/search/issues":
			emptySearch(w)
		case "/graphql":
			emptyStates(w)
		}
	})
	h.authenticate(t)

	res := gt.R1(h.uc.Poll(t.Context(), model.PollState{Failures: 3})).NoError(t)
	gt.Equal(t, res.NextState.Failures, 0)
}

func TestPollRateLimitUsesRetryAfter(t *testing.T) {
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "900")
		w.WriteHeader(http.StatusForbidden)
	})
	h.authenticate(t)

	res, err := h.uc.Poll(t.Context(), model.PollState{})
	gt.Error(t, err)
	// 900s beats both the 60s floor and the first backoff step.
	gt.Equal(t, res.NextInterval, 900*time.Second)
}

func TestPollPageLimit(t *testing.T) {
	pages := 0
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/notifications":
			pages++
			w.Header().Set("Link", `<https://api.github.com/notifications?page=9>; rel="next"`)
			_, _ = w.Write([]byte("[" + notificationJSON(strconv.Itoa(pages), fixedNow) + "]"))
		case "/search/issues":
			emptySearch(w)
		case "/graphql":
			emptyStates(w)
		}
	}, withConfig(func(c *usecase.Config) { c.MaxPages = 4 }))
	h.authenticate(t)

	res := gt.R1(h.uc.Poll(t.Context(), model.PollState{})).NoError(t)

	gt.Equal(t, pages, 4)
	gt.True(t, res.Truncated)
	gt.A(t, res.Notifications).Length(4)
}

func TestPollSearchFailureIsNotFatal(t *testing.T) {
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/notifications":
			_, _ = w.Write([]byte("[" + notificationJSON("1", fixedNow) + "]"))
		case "/search/issues":
			w.WriteHeader(http.StatusForbidden)
		case "/graphql":
			statesJSON(t, w, r)
		}
	})
	h.authenticate(t)

	res := gt.R1(h.uc.Poll(t.Context(), model.PollState{})).NoError(t)

	// The list is what the user came for; the review marker is optional.
	gt.A(t, res.Notifications).Length(1)
	gt.Equal(t, len(res.ReviewRequests), 0)
	gt.Error(t, res.ReviewErr)

	// A failed search must not take the state lookup down with it.
	gt.Nil(t, res.StateErr)
	gt.Equal(t, len(res.SubjectStates), 1)
}

func TestPollStateFailureIsNotFatal(t *testing.T) {
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/notifications":
			_, _ = w.Write([]byte("[" + notificationJSON("1", fixedNow) + "]"))
		case "/search/issues":
			emptySearch(w)
		case "/graphql":
			w.WriteHeader(http.StatusInternalServerError)
		}
	})
	h.authenticate(t)

	res := gt.R1(h.uc.Poll(t.Context(), model.PollState{})).NoError(t)

	// The list stays; only the markers are missing for this cycle.
	gt.A(t, res.Notifications).Length(1)
	gt.Equal(t, len(res.SubjectStates), 0)
	gt.Error(t, res.StateErr)
	gt.Nil(t, res.ReviewErr)
}

func TestPollDeduplicatesSubjectRefs(t *testing.T) {
	var numbers []any
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/notifications":
			// Three threads: two on the same issue, one on a check suite that
			// carries no resolvable subject.
			_, _ = w.Write([]byte("[" +
				notificationJSON("1", fixedNow) + "," +
				notificationJSON("2", fixedNow) + "," +
				checkSuiteJSON("3", fixedNow) + "]"))
		case "/search/issues":
			emptySearch(w)
		case "/graphql":
			var body struct {
				Variables map[string]any `json:"variables"`
			}
			gt.NoError(t, json.NewDecoder(r.Body).Decode(&body))
			for key, value := range body.Variables {
				if strings.HasPrefix(key, "n") {
					numbers = append(numbers, value)
				}
			}
			emptyStates(w)
		}
	})
	h.authenticate(t)

	gt.R1(h.uc.Poll(t.Context(), model.PollState{})).NoError(t)

	// One slot in the query: the duplicate is folded in and the check suite is
	// left out entirely.
	gt.Equal(t, numbers, []any{float64(1)})
}

func TestPollStateUnauthorizedForgetsTheCredential(t *testing.T) {
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/notifications":
			_, _ = w.Write([]byte("[" + notificationJSON("1", fixedNow) + "]"))
		case "/search/issues":
			emptySearch(w)
		case "/graphql":
			w.WriteHeader(http.StatusUnauthorized)
		}
	})
	h.authenticate(t)

	res := gt.R1(h.uc.Poll(t.Context(), model.PollState{})).NoError(t)

	gt.Error(t, res.StateErr).Is(gh.ErrUnauthorized)

	// A rejected token is dropped so the next start goes through the device flow
	// instead of failing again.
	_, _, err := h.tokens.Load(t.Context())
	gt.Error(t, err)
}

func TestPollReconcilesReadState(t *testing.T) {
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/notifications":
			_, _ = w.Write([]byte("[" + notificationJSON("1", fixedNow.Add(time.Hour)) + "]"))
		case "/search/issues":
			emptySearch(w)
		case "/graphql":
			emptyStates(w)
		}
	})
	h.authenticate(t)

	// A record written against an older timestamp is superseded by the fetch.
	gt.NoError(t, h.reads.Put(map[types.ThreadID]model.ReadOverride{
		"1": {State: model.ReadStateRead, At: fixedNow, SubjectUpdatedAt: fixedNow},
	}))

	res := gt.R1(h.uc.Poll(t.Context(), model.PollState{})).NoError(t)

	gt.Equal(t, res.Reconciled, 1)
	gt.Nil(t, res.ReconcileErr)
	_, ok := h.reads.Lookup("1")
	gt.False(t, ok)
}

func TestPollTruncatedDoesNotPruneMissingRecords(t *testing.T) {
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/notifications":
			w.Header().Set("Link", `<https://api.github.com/notifications?page=2>; rel="next"`)
			_, _ = w.Write([]byte("[" + notificationJSON("1", fixedNow) + "]"))
		case "/search/issues":
			emptySearch(w)
		case "/graphql":
			emptyStates(w)
		}
	}, withConfig(func(c *usecase.Config) { c.MaxPages = 1 }))
	h.authenticate(t)

	// This record's notification is not in the (truncated) list, and it is older
	// than the TTL, yet it must survive.
	gt.NoError(t, h.reads.Put(map[types.ThreadID]model.ReadOverride{
		"missing": {State: model.ReadStateRead, At: fixedNow.Add(-90 * 24 * time.Hour), SubjectUpdatedAt: fixedNow},
	}))

	res := gt.R1(h.uc.Poll(t.Context(), model.PollState{})).NoError(t)

	gt.True(t, res.Truncated)
	gt.Equal(t, res.Reconciled, 0)
	_, ok := h.reads.Lookup("missing")
	gt.True(t, ok)
}

func TestPollWithoutCredential(t *testing.T) {
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {})

	res, err := h.uc.Poll(t.Context(), model.PollState{})
	gt.Error(t, err).Is(usecase.ErrNotAuthenticated)
	gt.NotNil(t, res)
}

func TestDescribeRetry(t *testing.T) {
	err := model.WithUserMessage(gh.ErrUnexpectedStatus,
		model.UserMessage{Summary: "GitHub returned 503", Action: ""})

	described := usecase.DescribeRetry(err, 120*time.Second)
	msg, ok := model.UserMessageOf(described)
	gt.True(t, ok)
	gt.Equal(t, msg.Summary, "GitHub returned 503")
	gt.Equal(t, msg.Action, "retrying in 2m0s")

	// The original error must still be identifiable.
	gt.Error(t, described).Is(gh.ErrUnexpectedStatus)

	// Errors without display text are left alone, and nil stays nil.
	gt.Nil(t, usecase.DescribeRetry(nil, time.Second))
	plain := gh.ErrForbidden
	gt.Equal(t, usecase.DescribeRetry(plain, time.Second), error(plain))
}
