package gh

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/m-mizutani/octify/pkg/domain/model"
	"github.com/m-mizutani/octify/pkg/domain/types"
	"github.com/m-mizutani/octify/pkg/utils/safe"
)

// reviewRequestedQuery asks for the pull requests that are still waiting on the
// authenticated user. One search per poll keeps the cost independent of how many
// notifications there are.
const reviewRequestedQuery = "is:open is:pr review-requested:@me"

const (
	searchPerPage = 100
	// maxSearchPages bounds the review-request search. A missing marker reads as
	// "no review needed", so stopping at the first page would quietly mislead
	// anyone with a large queue; the cap keeps the cost bounded all the same.
	// Search allows 30 requests per minute, and this runs once per poll.
	maxSearchPages = 5
)

type searchIssuesResponse struct {
	TotalCount        int  `json:"total_count"`
	IncompleteResults bool `json:"incomplete_results"`
	Items             []struct {
		Number        int             `json:"number"`
		RepositoryURL string          `json:"repository_url"`
		PullRequest   json.RawMessage `json:"pull_request"`
	} `json:"items"`
}

// ListReviewRequestedPullRequests returns the pull requests the user is
// currently asked to review, following pagination up to maxSearchPages.
func (c *Client) ListReviewRequestedPullRequests(ctx context.Context) (model.ReviewRequests, error) {
	out := make(model.ReviewRequests)

	for page := 1; page <= maxSearchPages; page++ {
		body, next, err := c.searchReviewRequestedPage(ctx, page)
		if err != nil {
			return nil, err
		}

		for _, item := range body.Items {
			// Without a pull_request object the item is an issue, not a PR.
			if len(item.PullRequest) == 0 || string(item.PullRequest) == "null" {
				continue
			}
			full := repoFullNameFromURL(item.RepositoryURL)
			if full == "" || item.Number <= 0 {
				continue
			}
			out[model.PullRequestRef{Repo: full, Number: item.Number}] = struct{}{}
		}

		if next == 0 {
			break
		}
	}

	return out, nil
}

func (c *Client) searchReviewRequestedPage(ctx context.Context, page int) (*searchIssuesResponse, int, error) {
	q := url.Values{}
	q.Set("q", reviewRequestedQuery)
	q.Set("per_page", strconv.Itoa(searchPerPage))
	q.Set("page", strconv.Itoa(page))

	req, err := c.newRequest(ctx, http.MethodGet, "/search/issues", q)
	if err != nil {
		return nil, 0, err
	}

	resp, err := c.do(req, "search")
	if err != nil {
		return nil, 0, err
	}
	defer safe.Close(ctx, resp.Body)

	var body searchIssuesResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, 0, invalidResponse(err, "search")
	}
	return &body, nextPage(resp.Header), nil
}

// repoFullNameFromURL extracts "owner/name" from a repository API URL.
func repoFullNameFromURL(rawURL string) types.RepoFullName {
	const marker = "/repos/"
	idx := strings.Index(rawURL, marker)
	if idx < 0 {
		return ""
	}
	parts := strings.Split(rawURL[idx+len(marker):], "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return ""
	}
	return types.RepoFullName(parts[0] + "/" + parts[1])
}
