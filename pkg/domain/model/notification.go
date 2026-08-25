package model

import (
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/m-mizutani/octify/pkg/domain/types"
)

// Repository is the repository a notification came from.
type Repository struct {
	FullName types.RepoFullName
	HTMLURL  string
	Private  bool
}

// Subject is the object a notification points at.
type Subject struct {
	Title string
	Type  types.SubjectType
	// URL is the API URL of the subject. GitHub returns null for CheckSuite and
	// Discussion, in which case this is empty.
	URL string
	// Number is the pull request or issue number, or 0 when URL does not end
	// with a decimal segment.
	Number int
}

// Notification is one GitHub notification thread.
type Notification struct {
	ID      types.ThreadID
	Repo    Repository
	Subject Subject
	Reason  Reason
	// ServerUnread is GitHub's own unread flag. The displayed unread state is
	// decided by Unread, not by this field.
	ServerUnread bool
	UpdatedAt    time.Time
}

// Tab reports which tab the notification belongs to. Subject types that are not
// listed fall into TabOther.
func (n Notification) Tab() types.Tab {
	switch n.Subject.Type {
	case types.SubjectPullRequest:
		return types.TabPullRequest
	case types.SubjectIssue:
		return types.TabIssue
	case types.SubjectCheckSuite, types.SubjectWorkflowRun:
		return types.TabActions
	default:
		return types.TabOther
	}
}

// WebURL derives the browser URL for the notification by rewriting the subject's
// API URL. When the URL cannot be rewritten it falls back to the repository,
// which is always reachable.
func (n Notification) WebURL(webBase string) string {
	if u, ok := n.rewriteSubjectURL(webBase); ok {
		return u
	}
	if n.Repo.HTMLURL == "" {
		return webBase
	}
	if n.Subject.Type == types.SubjectCheckSuite || n.Subject.Type == types.SubjectWorkflowRun {
		return n.Repo.HTMLURL + "/actions"
	}
	return n.Repo.HTMLURL
}

// rewriteSubjectURL turns ".../repos/{owner}/{repo}/{kind}/{id}" into the
// matching page under webBase.
func (n Notification) rewriteSubjectURL(webBase string) (string, bool) {
	owner, repo, kind, id, ok := splitSubjectPath(n.Subject.URL)
	if !ok {
		return "", false
	}

	var segment string
	switch kind {
	case "pulls":
		segment = "pull"
	case "issues":
		segment = "issues"
	case "commits":
		segment = "commit"
	default:
		return "", false
	}

	if kind != "commits" {
		if _, err := strconv.Atoi(id); err != nil {
			return "", false
		}
	}

	return strings.TrimSuffix(webBase, "/") + "/" + owner + "/" + repo + "/" + segment + "/" + id, true
}

// splitSubjectPath extracts the owner, repository, kind and identifier from a
// subject API URL. It matches on the "/repos/" marker so that both api.github.com
// and GitHub Enterprise URLs are handled.
func splitSubjectPath(rawURL string) (owner, repo, kind, id string, ok bool) {
	const marker = "/repos/"
	idx := strings.Index(rawURL, marker)
	if idx < 0 {
		return "", "", "", "", false
	}
	rest := rawURL[idx+len(marker):]
	if q := strings.IndexAny(rest, "?#"); q >= 0 {
		rest = rest[:q]
	}
	parts := strings.Split(rest, "/")
	if len(parts) != 4 || slices.Contains(parts, "") {
		return "", "", "", "", false
	}
	return parts[0], parts[1], parts[2], parts[3], true
}

// PullRequestRef identifies a pull request across the notification list and the
// search result.
type PullRequestRef struct {
	Repo   types.RepoFullName
	Number int
}

// PullRequestRef reports the pull request this notification points at.
func (n Notification) PullRequestRef() (PullRequestRef, bool) {
	if n.Subject.Type != types.SubjectPullRequest {
		return PullRequestRef{}, false
	}
	if n.Subject.Number <= 0 || n.Repo.FullName == "" {
		return PullRequestRef{}, false
	}
	return PullRequestRef{Repo: n.Repo.FullName, Number: n.Subject.Number}, true
}

// ReviewRequests is the set of pull requests currently awaiting the user's review.
type ReviewRequests map[PullRequestRef]struct{}

func (r ReviewRequests) Has(ref PullRequestRef) bool {
	if r == nil {
		return false
	}
	_, ok := r[ref]
	return ok
}

// Reason is why GitHub generated the notification.
type Reason string

const (
	ReasonApprovalRequested      Reason = "approval_requested"
	ReasonAssign                 Reason = "assign"
	ReasonAuthor                 Reason = "author"
	ReasonCIActivity             Reason = "ci_activity"
	ReasonComment                Reason = "comment"
	ReasonInvitation             Reason = "invitation"
	ReasonManual                 Reason = "manual"
	ReasonMemberFeatureRequested Reason = "member_feature_requested"
	ReasonMention                Reason = "mention"
	ReasonReviewRequested        Reason = "review_requested"
	ReasonSecurityAdvisoryCredit Reason = "security_advisory_credit"
	ReasonSecurityAlert          Reason = "security_alert"
	ReasonStateChange            Reason = "state_change"
	ReasonSubscribed             Reason = "subscribed"
	ReasonTeamMention            Reason = "team_mention"
)

var reasonShort = map[Reason]string{
	ReasonApprovalRequested:      "approval",
	ReasonAssign:                 "assign",
	ReasonAuthor:                 "author",
	ReasonCIActivity:             "ci",
	ReasonComment:                "comment",
	ReasonInvitation:             "invite",
	ReasonManual:                 "manual",
	ReasonMemberFeatureRequested: "member",
	ReasonMention:                "mention",
	ReasonReviewRequested:        "review",
	ReasonSecurityAdvisoryCredit: "advisory",
	ReasonSecurityAlert:          "security",
	ReasonStateChange:            "state",
	ReasonSubscribed:             "subscr",
	ReasonTeamMention:            "team",
}

// Short returns a display form of at most 8 characters. Unknown reasons are
// returned unchanged so that new GitHub values remain visible.
func (r Reason) Short() string {
	if s, ok := reasonShort[r]; ok {
		return s
	}
	return string(r)
}
