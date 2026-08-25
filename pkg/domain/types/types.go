package types

// ThreadID is the identifier GitHub assigns to a notification thread.
type ThreadID string

// RepoFullName is a repository name in "owner/name" form.
type RepoFullName string

// SubjectType is the kind of object a notification points at.
type SubjectType string

const (
	SubjectPullRequest SubjectType = "PullRequest"
	SubjectIssue       SubjectType = "Issue"
	SubjectCheckSuite  SubjectType = "CheckSuite"
	// SubjectWorkflowRun is not listed in the current GitHub REST documentation.
	// It is kept as a catcher so that Actions notifications are not silently
	// classified as Other if GitHub starts emitting it.
	SubjectWorkflowRun SubjectType = "WorkflowRun"
	SubjectCommit      SubjectType = "Commit"
	SubjectRelease     SubjectType = "Release"
	SubjectDiscussion  SubjectType = "Discussion"
)

// AccessToken holds a GitHub access token. masq redacts values of this type in
// every log record, so the token cannot leak through an attribute added later.
type AccessToken string

// String hides the token from fmt verbs. Use string(t) to obtain the real value.
func (t AccessToken) String() string { return "[REDACTED]" }

// Tab is one of the categories the notification list is split into.
type Tab int

const (
	TabAll Tab = iota
	TabPullRequest
	TabIssue
	TabActions
	TabOther
)

// AllTabs lists every tab in display order.
var AllTabs = []Tab{TabAll, TabPullRequest, TabIssue, TabActions, TabOther}

func (t Tab) String() string {
	switch t {
	case TabAll:
		return "All"
	case TabPullRequest:
		return "PR"
	case TabIssue:
		return "Issue"
	case TabActions:
		return "Actions"
	case TabOther:
		return "Other"
	default:
		return "Other"
	}
}
