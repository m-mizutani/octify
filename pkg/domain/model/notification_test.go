package model_test

import (
	"testing"

	"github.com/m-mizutani/gt"
	"github.com/m-mizutani/octify/pkg/domain/model"
	"github.com/m-mizutani/octify/pkg/domain/types"
)

func TestNotificationTab(t *testing.T) {
	testCases := map[string]struct {
		subjectType types.SubjectType
		want        types.Tab
	}{
		"pull request":       {types.SubjectPullRequest, types.TabPullRequest},
		"issue":              {types.SubjectIssue, types.TabIssue},
		"check suite":        {types.SubjectCheckSuite, types.TabActions},
		"workflow run":       {types.SubjectWorkflowRun, types.TabActions},
		"commit":             {types.SubjectCommit, types.TabOther},
		"release":            {types.SubjectRelease, types.TabOther},
		"discussion":         {types.SubjectDiscussion, types.TabOther},
		"unknown type":       {types.SubjectType("SomethingNew"), types.TabOther},
		"empty type":         {types.SubjectType(""), types.TabOther},
		"lowercase mismatch": {types.SubjectType("pullrequest"), types.TabOther},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			n := model.Notification{Subject: model.Subject{Type: tc.subjectType}}
			gt.Equal(t, n.Tab(), tc.want)
		})
	}
}

func TestNotificationWebURL(t *testing.T) {
	const webBase = "https://github.com"
	repo := model.Repository{
		FullName: "m-mizutani/octify",
		HTMLURL:  "https://github.com/m-mizutani/octify",
	}

	testCases := map[string]struct {
		subject model.Subject
		repo    model.Repository
		want    string
	}{
		"pull request": {
			subject: model.Subject{
				Type: types.SubjectPullRequest,
				URL:  "https://api.github.com/repos/m-mizutani/octify/pulls/12",
			},
			repo: repo,
			want: "https://github.com/m-mizutani/octify/pull/12",
		},
		"issue": {
			subject: model.Subject{
				Type: types.SubjectIssue,
				URL:  "https://api.github.com/repos/m-mizutani/octify/issues/12",
			},
			repo: repo,
			want: "https://github.com/m-mizutani/octify/issues/12",
		},
		"commit": {
			subject: model.Subject{
				Type: types.SubjectCommit,
				URL:  "https://api.github.com/repos/m-mizutani/octify/commits/0123456789abcdef",
			},
			repo: repo,
			want: "https://github.com/m-mizutani/octify/commit/0123456789abcdef",
		},
		"check suite without url falls back to actions": {
			subject: model.Subject{Type: types.SubjectCheckSuite, URL: ""},
			repo:    repo,
			want:    "https://github.com/m-mizutani/octify/actions",
		},
		"discussion without url falls back to repository": {
			subject: model.Subject{Type: types.SubjectDiscussion, URL: ""},
			repo:    repo,
			want:    "https://github.com/m-mizutani/octify",
		},
		"non numeric pull request number falls back": {
			subject: model.Subject{
				Type: types.SubjectPullRequest,
				URL:  "https://api.github.com/repos/m-mizutani/octify/pulls/abc",
			},
			repo: repo,
			want: "https://github.com/m-mizutani/octify",
		},
		"unknown api path falls back": {
			subject: model.Subject{
				Type: types.SubjectRelease,
				URL:  "https://api.github.com/repos/m-mizutani/octify/releases/99",
			},
			repo: repo,
			want: "https://github.com/m-mizutani/octify",
		},
		"url without repos marker falls back": {
			subject: model.Subject{
				Type: types.SubjectPullRequest,
				URL:  "https://example.com/something/else",
			},
			repo: repo,
			want: "https://github.com/m-mizutani/octify",
		},
		"trailing slash falls back": {
			subject: model.Subject{
				Type: types.SubjectPullRequest,
				URL:  "https://api.github.com/repos/m-mizutani/octify/pulls/12/",
			},
			repo: repo,
			want: "https://github.com/m-mizutani/octify",
		},
		"enterprise host is rewritten to the web base": {
			subject: model.Subject{
				Type: types.SubjectIssue,
				URL:  "https://ghe.example.com/api/v3/repos/acme/tools/issues/7",
			},
			repo: model.Repository{FullName: "acme/tools", HTMLURL: "https://ghe.example.com/acme/tools"},
			want: "https://github.com/acme/tools/issues/7",
		},
		"no repository url falls back to the web base": {
			subject: model.Subject{Type: types.SubjectDiscussion},
			repo:    model.Repository{FullName: "acme/tools"},
			want:    webBase,
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			n := model.Notification{Repo: tc.repo, Subject: tc.subject}
			gt.Equal(t, n.WebURL(webBase), tc.want)
		})
	}
}

func TestNotificationWebURLTrimsTrailingSlashOnWebBase(t *testing.T) {
	n := model.Notification{
		Repo: model.Repository{FullName: "acme/tools"},
		Subject: model.Subject{
			Type: types.SubjectIssue,
			URL:  "https://api.github.com/repos/acme/tools/issues/7",
		},
	}
	gt.Equal(t, n.WebURL("https://github.com/"), "https://github.com/acme/tools/issues/7")
}

func TestNotificationPullRequestRef(t *testing.T) {
	testCases := map[string]struct {
		notification model.Notification
		wantOK       bool
		wantRef      model.PullRequestRef
	}{
		"pull request with number": {
			notification: model.Notification{
				Repo:    model.Repository{FullName: "acme/tools"},
				Subject: model.Subject{Type: types.SubjectPullRequest, Number: 12},
			},
			wantOK:  true,
			wantRef: model.PullRequestRef{Repo: "acme/tools", Number: 12},
		},
		"issue is not a pull request": {
			notification: model.Notification{
				Repo:    model.Repository{FullName: "acme/tools"},
				Subject: model.Subject{Type: types.SubjectIssue, Number: 12},
			},
		},
		"missing number": {
			notification: model.Notification{
				Repo:    model.Repository{FullName: "acme/tools"},
				Subject: model.Subject{Type: types.SubjectPullRequest},
			},
		},
		"missing repository": {
			notification: model.Notification{
				Subject: model.Subject{Type: types.SubjectPullRequest, Number: 12},
			},
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			ref, ok := tc.notification.PullRequestRef()
			gt.Equal(t, ok, tc.wantOK)
			gt.Equal(t, ref, tc.wantRef)
		})
	}
}

func TestReviewRequestsHas(t *testing.T) {
	ref := model.PullRequestRef{Repo: "acme/tools", Number: 12}
	reviews := model.ReviewRequests{ref: struct{}{}}

	gt.True(t, reviews.Has(ref))
	gt.False(t, reviews.Has(model.PullRequestRef{Repo: "acme/other", Number: 12}))
	gt.False(t, reviews.Has(model.PullRequestRef{Repo: "acme/tools", Number: 13}))

	var empty model.ReviewRequests
	gt.False(t, empty.Has(ref))
}

func TestReasonShort(t *testing.T) {
	gt.Equal(t, model.ReasonReviewRequested.Short(), "review")
	gt.Equal(t, model.ReasonCIActivity.Short(), "ci")

	// Unknown reasons stay visible instead of being hidden behind a placeholder.
	gt.Equal(t, model.Reason("brand_new_reason").Short(), "brand_new_reason")

	for reason, short := range map[model.Reason]string{
		model.ReasonApprovalRequested:      "approval",
		model.ReasonAssign:                 "assign",
		model.ReasonAuthor:                 "author",
		model.ReasonComment:                "comment",
		model.ReasonInvitation:             "invite",
		model.ReasonManual:                 "manual",
		model.ReasonMemberFeatureRequested: "member",
		model.ReasonMention:                "mention",
		model.ReasonSecurityAdvisoryCredit: "advisory",
		model.ReasonSecurityAlert:          "security",
		model.ReasonStateChange:            "state",
		model.ReasonSubscribed:             "subscr",
		model.ReasonTeamMention:            "team",
	} {
		gt.Equal(t, reason.Short(), short)
		gt.N(t, len(short)).LessOrEqual(8)
	}
}
