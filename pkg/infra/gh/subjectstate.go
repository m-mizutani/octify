package gh

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/m-mizutani/goerr/v2"
	"github.com/m-mizutani/octify/pkg/domain/model"
	"github.com/m-mizutani/octify/pkg/utils/logging"
	"github.com/m-mizutani/octify/pkg/utils/safe"
)

const (
	// maxSubjectsPerQuery is how many subjects go into one GraphQL document. One
	// hundred resolves in a single request at a rate limit cost of 1.
	maxSubjectsPerQuery = 100
	// maxSubjectQueries bounds one lookup. --max-pages defaults to 10, so a full
	// list is 500 subjects and 5 queries; the cap leaves room without letting an
	// unusually large list turn one poll into an unbounded run of requests.
	maxSubjectQueries = 10
)

const (
	subjectTypePullRequest = "PullRequest"
	subjectTypeIssue       = "Issue"
	subjectStateClosed     = "CLOSED"
)

type graphQLRequest struct {
	Query     string         `json:"query"`
	Variables map[string]any `json:"variables"`
}

type graphQLResponse struct {
	Data   map[string]json.RawMessage `json:"data"`
	Errors []graphQLError             `json:"errors"`
}

type graphQLError struct {
	Type    string `json:"type"`
	Message string `json:"message"`
	// Path names the field that failed. An error without one did not come from
	// a single alias: it rejected the whole request.
	Path []any `json:"path"`
}

// repositoryNode is one alias of the batched document. Both the node and the
// repository around it come back null when GitHub cannot resolve them.
type repositoryNode struct {
	Subject *subjectNode `json:"subject"`
}

type subjectNode struct {
	TypeName        string `json:"__typename"`
	State           string `json:"state"`
	Merged          bool   `json:"merged"`
	ViewerDidAuthor bool   `json:"viewerDidAuthor"`
}

// ListSubjectStates returns the current state of each issue and pull request in
// refs. Subjects GitHub declines to resolve are absent from the result rather
// than reported as an error, because one unreachable repository must not cost
// the markers of everything batched alongside it.
func (c *Client) ListSubjectStates(ctx context.Context, refs []model.SubjectRef) (model.SubjectStates, error) {
	out := make(model.SubjectStates, len(refs))

	for queries := 0; len(refs) > 0 && queries < maxSubjectQueries; queries++ {
		batch := refs
		if len(batch) > maxSubjectsPerQuery {
			batch = batch[:maxSubjectsPerQuery]
		}
		refs = refs[len(batch):]

		if err := c.collectSubjectStates(ctx, batch, out); err != nil {
			return nil, err
		}
	}

	// --max-pages is only checked for being at least 1, so a large enough
	// setting can put more subjects in the list than the cap will resolve. Say
	// so, or the rows past the cap look like open pull requests nobody wrote.
	if len(refs) > 0 {
		logging.From(ctx).Warn("state lookup stopped at the query cap",
			slog.Int("unresolved", len(refs)),
			slog.Int("cap", maxSubjectQueries*maxSubjectsPerQuery))
	}

	return out, nil
}

func (c *Client) collectSubjectStates(ctx context.Context, batch []model.SubjectRef, out model.SubjectStates) error {
	query, variables, aliases := buildSubjectQuery(batch)
	if len(aliases) == 0 {
		return nil
	}

	body, err := json.Marshal(graphQLRequest{Query: query, Variables: variables})
	if err != nil {
		return goerr.Wrap(err, "failed to encode graphql request")
	}

	req, err := c.newBodyRequest(ctx, http.MethodPost, c.graphqlBase, body)
	if err != nil {
		return err
	}

	resp, err := c.do(req, "graphql")
	if err != nil {
		return err
	}
	defer safe.Close(ctx, resp.Body)

	var decoded graphQLResponse
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		return invalidResponse(err, "graphql")
	}

	// GitHub answers an exhausted point budget or a rejected document with HTTP
	// 200 and a top-level error, so c.do cannot catch it. Such an error carries
	// no path, unlike the per-alias NOT_FOUND that a repository the token cannot
	// see produces. Only the latter is safe to absorb: absorbing the former
	// would report "nothing here is finished" for the whole rate limit window.
	if err := requestLevelError(decoded.Errors); err != nil {
		return err
	}
	if len(decoded.Errors) > 0 {
		logging.From(ctx).Debug("graphql could not resolve some subjects",
			slog.Int("count", len(decoded.Errors)),
			slog.String("first", decoded.Errors[0].Message))
	}

	for alias, ref := range aliases {
		raw, ok := decoded.Data[alias]
		if !ok {
			continue
		}
		var node repositoryNode
		if err := json.Unmarshal(raw, &node); err != nil || node.Subject == nil {
			continue
		}
		state, ok := subjectStateOf(*node.Subject)
		if !ok {
			continue
		}
		out[ref] = state
	}
	return nil
}

// requestLevelError reports the first error that rejected the request as a
// whole rather than one alias within it.
func requestLevelError(errs []graphQLError) error {
	for _, e := range errs {
		if len(e.Path) > 0 {
			continue
		}
		return model.WithUserMessage(
			goerr.Wrap(ErrGraphQLRequestFailed, "graphql rejected the request",
				goerr.V("type", e.Type), goerr.V("message", e.Message)),
			model.UserMessage{Summary: "GitHub could not answer the marker lookup"},
		)
	}
	return nil
}

func subjectStateOf(node subjectNode) (model.SubjectState, bool) {
	switch node.TypeName {
	case subjectTypePullRequest:
		return model.SubjectState{
			Authored: node.ViewerDidAuthor,
			Merged:   node.Merged,
			// A merged pull request reports state MERGED, so this stays false and
			// the two markers remain mutually exclusive.
			Closed: node.State == subjectStateClosed,
		}, true
	case subjectTypeIssue:
		return model.SubjectState{
			Authored: node.ViewerDidAuthor,
			Closed:   node.State == subjectStateClosed,
		}, true
	default:
		return model.SubjectState{}, false
	}
}

// buildSubjectQuery renders one batched document and the variables that fill
// it, plus the alias-to-reference mapping needed to read the response back.
//
// The owner and name travel as GraphQL variables rather than as text spliced
// into the document. Splicing would be shorter, but it would rest on GitHub
// never allowing a repository name outside [A-Za-z0-9._-], and guarding that
// assumption means silently dropping references whose markers then go missing
// with nothing to point at.
func buildSubjectQuery(batch []model.SubjectRef) (string, map[string]any, map[string]model.SubjectRef) {
	var decls, body strings.Builder
	variables := make(map[string]any, len(batch)*3)
	aliases := make(map[string]model.SubjectRef, len(batch))

	for _, ref := range batch {
		owner, name, ok := strings.Cut(string(ref.Repo), "/")
		if !ok || owner == "" || name == "" || ref.Number <= 0 {
			continue
		}

		i := strconv.Itoa(len(aliases))
		alias, oVar, rVar, nVar := "s"+i, "o"+i, "r"+i, "n"+i

		if decls.Len() > 0 {
			decls.WriteString(",")
		}
		decls.WriteString("$" + oVar + ":String!,$" + rVar + ":String!,$" + nVar + ":Int!")

		body.WriteString("\n  " + alias + ": repository(owner:$" + oVar + ", name:$" + rVar + ") {" +
			" subject: issueOrPullRequest(number:$" + nVar + ") {" +
			" __typename" +
			" ... on PullRequest { state merged viewerDidAuthor }" +
			" ... on Issue { state viewerDidAuthor }" +
			" } }")

		variables[oVar] = owner
		variables[rVar] = name
		variables[nVar] = ref.Number
		aliases[alias] = ref
	}

	if len(aliases) == 0 {
		return "", nil, nil
	}
	return "query(" + decls.String() + ") {" + body.String() + "\n}", variables, aliases
}
