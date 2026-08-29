package gh_test

import (
	"errors"
	"net/http"
	"strconv"
	"testing"

	"github.com/m-mizutani/gt"
	"github.com/m-mizutani/octify/pkg/domain/model"
	"github.com/m-mizutani/octify/pkg/infra/gh"
)

func TestListReviewRequestedPullRequests(t *testing.T) {
	const body = `{
      "total_count": 3,
      "incomplete_results": false,
      "items": [
        {"number": 123, "repository_url": "https://api.github.com/repos/m-mizutani/octify",
         "pull_request": {"url": "https://api.github.com/repos/m-mizutani/octify/pulls/123"}},
        {"number": 7, "repository_url": "https://api.github.com/repos/acme/tools",
         "pull_request": {"url": "https://api.github.com/repos/acme/tools/pulls/7"}},
        {"number": 45, "repository_url": "https://api.github.com/repos/acme/tools"}
      ]
    }`

	var gotQuery, gotPerPage, gotPath string
	client := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotQuery = r.URL.Query().Get("q")
		gotPerPage = r.URL.Query().Get("per_page")
		_, _ = w.Write([]byte(body))
	})

	reviews := gt.R1(client.ListReviewRequestedPullRequests(t.Context())).NoError(t)

	gt.Equal(t, gotPath, "/search/issues")
	gt.S(t, gotQuery).Contains("is:open")
	gt.S(t, gotQuery).Contains("is:pr")
	gt.S(t, gotQuery).Contains("review-requested:@me")
	gt.Equal(t, gotPerPage, "100")

	// The third item has no pull_request object, so it is an issue and is skipped.
	gt.Equal(t, len(reviews), 2)
	gt.True(t, reviews.Has(model.PullRequestRef{Repo: "m-mizutani/octify", Number: 123}))
	gt.True(t, reviews.Has(model.PullRequestRef{Repo: "acme/tools", Number: 7}))
	gt.False(t, reviews.Has(model.PullRequestRef{Repo: "acme/tools", Number: 45}))
}

func TestListReviewRequestedPullRequestsEdgeCases(t *testing.T) {
	testCases := map[string]struct {
		body      string
		wantCount int
	}{
		"no results": {
			body:      `{"total_count": 0, "incomplete_results": false, "items": []}`,
			wantCount: 0,
		},
		"incomplete results still yield the set": {
			body: `{"total_count": 1, "incomplete_results": true, "items": [
			  {"number": 1, "repository_url": "https://api.github.com/repos/acme/tools",
			   "pull_request": {"url": "x"}}]}`,
			wantCount: 1,
		},
		"null pull_request is skipped": {
			body: `{"total_count": 1, "incomplete_results": false, "items": [
			  {"number": 1, "repository_url": "https://api.github.com/repos/acme/tools",
			   "pull_request": null}]}`,
			wantCount: 0,
		},
		"unparsable repository url is skipped": {
			body: `{"total_count": 1, "incomplete_results": false, "items": [
			  {"number": 1, "repository_url": "https://example.com/nope",
			   "pull_request": {"url": "x"}}]}`,
			wantCount: 0,
		},
		"missing number is skipped": {
			body: `{"total_count": 1, "incomplete_results": false, "items": [
			  {"repository_url": "https://api.github.com/repos/acme/tools",
			   "pull_request": {"url": "x"}}]}`,
			wantCount: 0,
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			client := newClient(t, func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write([]byte(tc.body))
			})
			reviews := gt.R1(client.ListReviewRequestedPullRequests(t.Context())).NoError(t)
			gt.Equal(t, len(reviews), tc.wantCount)
		})
	}
}

func TestListReviewRequestedPullRequestsFollowsPagination(t *testing.T) {
	pages := 0
	client := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		pages++
		gt.Equal(t, r.URL.Query().Get("per_page"), "100")
		gt.Equal(t, r.URL.Query().Get("page"), strconv.Itoa(pages))

		if pages < 3 {
			w.Header().Set("Link",
				`<https://api.github.com/search/issues?page=`+strconv.Itoa(pages+1)+`>; rel="next"`)
		}
		_, _ = w.Write([]byte(`{"total_count":300,"incomplete_results":false,"items":[
		  {"number":` + strconv.Itoa(pages) + `,"repository_url":"https://api.github.com/repos/acme/tools","pull_request":{"url":"x"}}]}`))
	})

	reviews := gt.R1(client.ListReviewRequestedPullRequests(t.Context())).NoError(t)

	// A missing marker reads as "no review needed", so every page has to be read.
	gt.Equal(t, pages, 3)
	gt.Equal(t, len(reviews), 3)
	gt.True(t, reviews.Has(model.PullRequestRef{Repo: "acme/tools", Number: 3}))
}

func TestListReviewRequestedPullRequestsStopsAtPageCap(t *testing.T) {
	pages := 0
	client := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		pages++
		// Always advertise another page so only the cap can stop the loop.
		w.Header().Set("Link", `<https://api.github.com/search/issues?page=99>; rel="next"`)
		_, _ = w.Write([]byte(`{"total_count":9999,"incomplete_results":false,"items":[
		  {"number":` + strconv.Itoa(pages) + `,"repository_url":"https://api.github.com/repos/acme/tools","pull_request":{"url":"x"}}]}`))
	})

	reviews := gt.R1(client.ListReviewRequestedPullRequests(t.Context())).NoError(t)

	gt.Equal(t, pages, 5)
	gt.Equal(t, len(reviews), 5)
}

func TestListReviewRequestedPullRequestsErrors(t *testing.T) {
	t.Run("rate limited search reports the search resource", func(t *testing.T) {
		client := newClient(t, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Retry-After", "10")
			w.WriteHeader(http.StatusForbidden)
		})

		_, err := client.ListReviewRequestedPullRequests(t.Context())
		gt.Error(t, err)

		var target *gh.RateLimitError
		gt.True(t, errors.As(err, &target))
		gt.Equal(t, target.Resource, "search")
	})

	t.Run("broken json", func(t *testing.T) {
		client := newClient(t, func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`[`))
		})
		_, err := client.ListReviewRequestedPullRequests(t.Context())
		gt.Error(t, err).Is(gh.ErrInvalidResponse)
	})
}
