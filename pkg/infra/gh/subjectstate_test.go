package gh_test

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/m-mizutani/gt"
	"github.com/m-mizutani/octify/pkg/domain/model"
	"github.com/m-mizutani/octify/pkg/infra/gh"
)

// capturedQuery is the request body a handler received, decoded.
type capturedQuery struct {
	Query     string         `json:"query"`
	Variables map[string]any `json:"variables"`
}

func decodeQuery(t *testing.T, r *http.Request) capturedQuery {
	t.Helper()
	raw := gt.R1(io.ReadAll(r.Body)).NoError(t)
	var got capturedQuery
	gt.NoError(t, json.Unmarshal(raw, &got))
	return got
}

// aliasesOf lists the alias names the query asked for, so a test can build a
// response that matches whatever batch it was handed.
func aliasesOf(query string) []string {
	var out []string
	for _, line := range strings.Split(query, "\n") {
		alias, _, ok := strings.Cut(strings.TrimSpace(line), ":")
		if ok && strings.HasPrefix(alias, "s") {
			out = append(out, alias)
		}
	}
	return out
}

// openPullRequests answers a batched query with every alias resolved as an open
// pull request nobody here authored.
func openPullRequests(t *testing.T, w http.ResponseWriter, query string) {
	t.Helper()
	data := map[string]any{}
	for _, alias := range aliasesOf(query) {
		data[alias] = map[string]any{"subject": map[string]any{
			"__typename": "PullRequest", "state": "OPEN", "merged": false, "viewerDidAuthor": false,
		}}
	}
	gt.NoError(t, json.NewEncoder(w).Encode(map[string]any{"data": data}))
}

func mustLookup(t *testing.T, states model.SubjectStates, ref model.SubjectRef) model.SubjectState {
	t.Helper()
	st, ok := states.Lookup(ref)
	gt.True(t, ok)
	return st
}

func TestListSubjectStates(t *testing.T) {
	const body = `{"data":{
	  "s0": {"subject": {"__typename":"PullRequest","state":"MERGED","merged":true,"viewerDidAuthor":true}},
	  "s1": {"subject": {"__typename":"PullRequest","state":"CLOSED","merged":false,"viewerDidAuthor":false}},
	  "s2": {"subject": {"__typename":"PullRequest","state":"OPEN","merged":false,"viewerDidAuthor":true}},
	  "s3": {"subject": {"__typename":"Issue","state":"CLOSED","viewerDidAuthor":true}},
	  "s4": {"subject": {"__typename":"Issue","state":"OPEN","viewerDidAuthor":false}}
	}}`

	client := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(body))
	})

	refs := []model.SubjectRef{
		{Repo: "acme/tools", Number: 1},
		{Repo: "acme/tools", Number: 2},
		{Repo: "acme/tools", Number: 3},
		{Repo: "acme/docs", Number: 4},
		{Repo: "acme/docs", Number: 5},
	}
	states := gt.R1(client.ListSubjectStates(t.Context(), refs)).NoError(t)

	gt.Equal(t, len(states), 5)

	// A merged pull request reports MERGED, never CLOSED, so the two markers
	// cannot both apply to one row.
	gt.Equal(t, mustLookup(t, states, refs[0]), model.SubjectState{Authored: true, Merged: true})
	gt.Equal(t, mustLookup(t, states, refs[1]), model.SubjectState{Closed: true})
	gt.Equal(t, mustLookup(t, states, refs[2]), model.SubjectState{Authored: true})
	gt.Equal(t, mustLookup(t, states, refs[3]), model.SubjectState{Authored: true, Closed: true})
	gt.Equal(t, mustLookup(t, states, refs[4]), model.SubjectState{})
}

func TestListSubjectStatesRequestShape(t *testing.T) {
	var (
		gotMethod      string
		gotPath        string
		gotContentType string
		gotAuth        string
		gotUserAgent   string
		gotQuery       capturedQuery
	)

	client := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotContentType = r.Header.Get("Content-Type")
		gotAuth = r.Header.Get("Authorization")
		gotUserAgent = r.Header.Get("User-Agent")
		gotQuery = decodeQuery(t, r)
		_, _ = w.Write([]byte(`{"data":{}}`))
	})

	refs := []model.SubjectRef{{Repo: "acme/tools", Number: 41}}
	gt.R1(client.ListSubjectStates(t.Context(), refs)).NoError(t)

	gt.Equal(t, gotMethod, http.MethodPost)
	gt.Equal(t, gotPath, "/graphql")
	gt.Equal(t, gotContentType, "application/json")
	gt.Equal(t, gotAuth, "Bearer test-token")
	gt.Equal(t, gotUserAgent, "octify")

	gt.Equal(t, gotQuery.Variables["o0"], any("acme"))
	gt.Equal(t, gotQuery.Variables["r0"], any("tools"))
	gt.Equal(t, gotQuery.Variables["n0"], any(float64(41)))

	// The owner and name must travel as variables, never spliced into the
	// document, so no repository name can alter the query's shape.
	gt.S(t, gotQuery.Query).NotContains("acme")
	gt.S(t, gotQuery.Query).NotContains("tools")
	gt.S(t, gotQuery.Query).Contains("$o0:String!")
	gt.S(t, gotQuery.Query).Contains("issueOrPullRequest(number:$n0)")
}

func TestListSubjectStatesSplitsIntoBatches(t *testing.T) {
	var sizes []int
	client := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		got := decodeQuery(t, r)
		sizes = append(sizes, len(aliasesOf(got.Query)))
		openPullRequests(t, w, got.Query)
	})

	refs := make([]model.SubjectRef, 0, 250)
	for i := 1; i <= 250; i++ {
		refs = append(refs, model.SubjectRef{Repo: "acme/tools", Number: i})
	}

	states := gt.R1(client.ListSubjectStates(t.Context(), refs)).NoError(t)

	gt.Equal(t, sizes, []int{100, 100, 50})
	gt.Equal(t, len(states), 250)
}

func TestListSubjectStatesStopsAtQueryCap(t *testing.T) {
	requests := 0
	client := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		requests++
		openPullRequests(t, w, decodeQuery(t, r).Query)
	})

	refs := make([]model.SubjectRef, 0, 1500)
	for i := 1; i <= 1500; i++ {
		refs = append(refs, model.SubjectRef{Repo: "acme/tools", Number: i})
	}

	states := gt.R1(client.ListSubjectStates(t.Context(), refs)).NoError(t)

	// Ten queries of a hundred, and the rest simply carry no marker.
	gt.Equal(t, requests, 10)
	gt.Equal(t, len(states), 1000)

	_, ok := states.Lookup(model.SubjectRef{Repo: "acme/tools", Number: 1001})
	gt.False(t, ok)
}

func TestListSubjectStatesWithoutRefs(t *testing.T) {
	requests := 0
	client := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		requests++
		_, _ = w.Write([]byte(`{"data":{}}`))
	})

	states := gt.R1(client.ListSubjectStates(t.Context(), nil)).NoError(t)

	gt.Equal(t, requests, 0)
	gt.Equal(t, len(states), 0)
}

func TestListSubjectStatesSkipsUnresolvableEntries(t *testing.T) {
	const resolved = `{"subject":{"__typename":"PullRequest","state":"OPEN","merged":false,"viewerDidAuthor":false}}`

	testCases := map[string]string{
		"repository is null": `{"data":{"s0":null,"s1":` + resolved + `},
		  "errors":[{"type":"NOT_FOUND","message":"Could not resolve to a Repository"}]}`,
		"subject is null":        `{"data":{"s0":{"subject":null},"s1":` + resolved + `}}`,
		"unknown typename":       `{"data":{"s0":{"subject":{"__typename":"Discussion","state":"OPEN"}},"s1":` + resolved + `}}`,
		"alias missing entirely": `{"data":{"s1":` + resolved + `}}`,
	}

	for name, body := range testCases {
		t.Run(name, func(t *testing.T) {
			client := newClient(t, func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write([]byte(body))
			})

			refs := []model.SubjectRef{
				{Repo: "acme/hidden", Number: 1},
				{Repo: "acme/tools", Number: 2},
			}
			states := gt.R1(client.ListSubjectStates(t.Context(), refs)).NoError(t)

			// The unresolvable one is absent; the one beside it is unaffected.
			gt.Equal(t, len(states), 1)

			_, ok := states.Lookup(refs[0])
			gt.False(t, ok)
			_, ok = states.Lookup(refs[1])
			gt.True(t, ok)
		})
	}
}

func TestListSubjectStatesSkipsMalformedRepoNames(t *testing.T) {
	var gotQuery capturedQuery
	client := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotQuery = decodeQuery(t, r)
		_, _ = w.Write([]byte(`{"data":{"s0":{"subject":{"__typename":"PullRequest","state":"MERGED","merged":true,"viewerDidAuthor":false}}}}`))
	})

	refs := []model.SubjectRef{
		{Repo: "no-slash", Number: 1},
		{Repo: "/tools", Number: 2},
		{Repo: "acme/", Number: 3},
		{Repo: "acme/tools", Number: 4},
	}
	states := gt.R1(client.ListSubjectStates(t.Context(), refs)).NoError(t)

	// Only the well-formed reference is asked for, and it resolves normally.
	gt.Equal(t, len(gotQuery.Variables), 3)
	gt.Equal(t, gotQuery.Variables["n0"], any(float64(4)))
	gt.Equal(t, len(states), 1)

	_, ok := states.Lookup(refs[3])
	gt.True(t, ok)
}

func TestListSubjectStatesSkipsBatchWithoutUsableRefs(t *testing.T) {
	requests := 0
	client := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		requests++
		_, _ = w.Write([]byte(`{"data":{}}`))
	})

	refs := []model.SubjectRef{{Repo: "no-slash", Number: 1}, {Repo: "also-no-slash", Number: 2}}
	states := gt.R1(client.ListSubjectStates(t.Context(), refs)).NoError(t)

	gt.Equal(t, requests, 0)
	gt.Equal(t, len(states), 0)
}

func TestListSubjectStatesErrors(t *testing.T) {
	refs := []model.SubjectRef{{Repo: "acme/tools", Number: 1}}

	t.Run("unauthorized", func(t *testing.T) {
		client := newClient(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
		})
		_, err := client.ListSubjectStates(t.Context(), refs)
		gt.Error(t, err).Is(gh.ErrUnauthorized)
	})

	t.Run("rate limited reports the graphql resource", func(t *testing.T) {
		client := newClient(t, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Retry-After", "30")
			w.WriteHeader(http.StatusForbidden)
		})

		_, err := client.ListSubjectStates(t.Context(), refs)
		gt.Error(t, err)

		var target *gh.RateLimitError
		gt.True(t, errors.As(err, &target))
		gt.Equal(t, target.Resource, "graphql")
		gt.Equal(t, target.RetryAfter.String(), "30s")
	})

	t.Run("broken json", func(t *testing.T) {
		client := newClient(t, func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`{`))
		})
		_, err := client.ListSubjectStates(t.Context(), refs)
		gt.Error(t, err).Is(gh.ErrInvalidResponse)
	})

	t.Run("unreachable endpoint", func(t *testing.T) {
		client := gh.New("t", gh.WithGraphQLBase("http://127.0.0.1:1/graphql"))
		_, err := client.ListSubjectStates(t.Context(), refs)
		gt.Error(t, err)
		gt.S(t, err.Error()).Contains("127.0.0.1:1")
	})
}

func TestListSubjectStatesUsesOnlyTheGraphQLEndpoint(t *testing.T) {
	var paths []string
	client := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		_, _ = w.Write([]byte(`{"data":{}}`))
	})

	refs := make([]model.SubjectRef, 0, 3)
	for i := 1; i <= 3; i++ {
		refs = append(refs, model.SubjectRef{Repo: "acme/tools", Number: i})
	}
	gt.R1(client.ListSubjectStates(t.Context(), refs)).NoError(t)

	// One request, and it went nowhere near the REST root.
	gt.Equal(t, paths, []string{"/graphql"})
}
