package tui_test

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/m-mizutani/goerr/v2"
	"github.com/m-mizutani/gt"
	"github.com/m-mizutani/octify/pkg/domain/model"
	"github.com/m-mizutani/octify/pkg/domain/types"
	"github.com/m-mizutani/octify/pkg/usecase"
)

// widestLine reports the longest rendered line in display cells, which is what
// the terminal actually has to fit. Counting runes would miss the CJK case
// entirely, since those occupy two columns each.
func widestLine(s string) int {
	widest := 0
	for _, line := range strings.Split(s, "\n") {
		if n := ansi.StringWidth(line); n > widest {
			widest = n
		}
	}
	return widest
}

// stripANSI removes the styling so widths can be measured.
func stripANSI(s string) string {
	var out strings.Builder
	inEscape := false
	for _, r := range s {
		switch {
		case r == '\x1b':
			inEscape = true
		case inEscape && r == 'm':
			inEscape = false
		case inEscape:
		default:
			out.WriteRune(r)
		}
	}
	return out.String()
}

func TestViewUsesTheAlternateScreen(t *testing.T) {
	h := newHarness(t)
	h.loadList(t, sampleList()...)

	// The list owns the terminal for as long as octify runs.
	gt.True(t, h.m.View().AltScreen)
}

func TestRenderFitsTheTerminal(t *testing.T) {
	for _, width := range []int{40, 60, 80, 120, 200} {
		h := newHarness(t)
		h.resize(t, width, 20)
		h.send(t, h.m.PollResultMsg(&usecase.PollResult{
			Notifications:  sampleList(),
			ReviewRequests: model.ReviewRequests{},
			NextInterval:   time.Minute,
		}, nil))

		gt.N(t, widestLine(h.m.Render())).LessOrEqual(width)
	}
}

// A Japanese title occupies two columns per rune, so a row that fits by rune
// count can still run off the edge and wrap.
func TestRenderFitsTheTerminalWithDoubleWidthText(t *testing.T) {
	n := notification("1", types.SubjectPullRequest, "acme/tools", 1, true)
	n.Subject.Title = strings.Repeat("通知の一覧を端末で仕分ける", 8)

	for _, width := range []int{40, 80, 120} {
		h := newHarness(t)
		h.resize(t, width, 20)
		h.send(t, h.m.PollResultMsg(&usecase.PollResult{
			Notifications:  []model.Notification{n},
			ReviewRequests: model.ReviewRequests{},
			NextInterval:   time.Minute,
		}, nil))

		gt.N(t, widestLine(h.m.Render())).LessOrEqual(width)
	}
}

func TestRenderEmptyMessageFitsANarrowTerminal(t *testing.T) {
	h := newHarness(t)
	h.resize(t, 40, 20)
	h.send(t, h.m.PollResultMsg(&usecase.PollResult{
		Notifications:  nil,
		ReviewRequests: model.ReviewRequests{},
		NextInterval:   time.Minute,
	}, nil))
	h.send(t, press('/'))
	for _, r := range "zzzz" {
		h.send(t, press(r))
	}

	// The longest empty-state wording is 57 columns; without truncation it wraps
	// and pushes the status row off the screen.
	gt.N(t, widestLine(h.m.Render())).LessOrEqual(40)
}

func TestRenderTruncatesLongTitles(t *testing.T) {
	long := notification("1", types.SubjectPullRequest, "acme/tools", 1, true)
	long.Subject.Title = strings.Repeat("very long title ", 40)

	h := newHarness(t)
	h.resize(t, 80, 20)
	h.send(t, h.m.PollResultMsg(&usecase.PollResult{
		Notifications:  []model.Notification{long},
		ReviewRequests: model.ReviewRequests{},
		NextInterval:   time.Minute,
	}, nil))

	out := h.m.Render()
	gt.N(t, widestLine(out)).LessOrEqual(80)
	gt.S(t, stripANSI(out)).Contains("…")
}

func TestRenderTruncatesLongRepositoryNamesFromTheLeft(t *testing.T) {
	n := notification("1", types.SubjectPullRequest,
		"a-very-long-organisation-name/a-very-long-repository-name", 1, true)

	h := newHarness(t)
	h.resize(t, 120, 20)
	h.send(t, h.m.PollResultMsg(&usecase.PollResult{
		Notifications:  []model.Notification{n},
		ReviewRequests: model.ReviewRequests{},
		NextInterval:   time.Minute,
	}, nil))

	// The tail identifies the repository, so that is the half to keep.
	gt.S(t, stripANSI(h.m.Render())).Contains("repository-name")
}

func TestRenderShowsTheReviewMarker(t *testing.T) {
	h := newHarness(t)
	h.resize(t, 100, 20)
	h.send(t, h.m.PollResultMsg(&usecase.PollResult{
		Notifications: sampleList(),
		ReviewRequests: model.ReviewRequests{
			model.PullRequestRef{Repo: "acme/tools", Number: 1}: struct{}{},
		},
		NextInterval: time.Minute,
	}, nil))

	lines := strings.Split(stripANSI(h.m.Render()), "\n")
	// Row 1 is the pull request the user is asked to review; row 2 is not.
	gt.S(t, lines[1]).Contains("R")
	gt.S(t, lines[2]).ContainsNone("R ")
}

func TestRenderTabCounts(t *testing.T) {
	h := newHarness(t)
	h.loadList(t, sampleList()...)

	header := stripANSI(strings.Split(h.m.Render(), "\n")[0])
	gt.S(t, header).Contains("[1] All 5")
	gt.S(t, header).Contains("[2] PR 2")
	gt.S(t, header).Contains("[3] Issue 1")
	gt.S(t, header).Contains("[4] Actions 1")
	gt.S(t, header).Contains("[5] Other 1")
}

func TestRenderEmptyStates(t *testing.T) {
	testCases := map[string]struct {
		setup func(t *testing.T, h *harness)
		want  string
	}{
		"unread only and nothing unread": {
			setup: func(t *testing.T, h *harness) { h.loadList(t) },
			want:  "No unread notifications",
		},
		"showing everything and nothing at all": {
			setup: func(t *testing.T, h *harness) {
				h.loadList(t)
				h.send(t, press('a'))
			},
			want: "No notifications.",
		},
		"tab is empty": {
			setup: func(t *testing.T, h *harness) {
				h.loadList(t, notification("1", types.SubjectIssue, "acme/tools", 1, true))
				h.send(t, press('2')) // PR tab
			},
			want: "Nothing in this tab",
		},
		"filter matches nothing": {
			setup: func(t *testing.T, h *harness) {
				h.loadList(t, sampleList()...)
				h.send(t, press('/'))
				for _, r := range "zzzz" {
					h.send(t, press(r))
				}
			},
			want: "No notification matches the filter",
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			tc.setup(t, h)
			gt.S(t, stripANSI(h.m.Render())).Contains(tc.want)
		})
	}
}

func TestRenderStatusLine(t *testing.T) {
	h := newHarness(t)
	h.loadList(t, sampleList()...)
	h.send(t, press('x'))

	status := stripANSI(lastLine(h.m.Render()))
	gt.S(t, status).Contains("1 selected")
	gt.S(t, status).Contains("5 unread")
	gt.S(t, status).Contains("unread only")
	gt.S(t, status).Contains("poll in")
}

func TestRenderStatusLineDropsTheActionBeforeTheSummary(t *testing.T) {
	long := model.UserMessage{
		Summary: "GitHub rate limit reached",
		Action:  "retrying in 15m0s after the limit resets at the top of the hour",
	}
	err := model.WithUserMessage(goerr.New("rate limited"), long)

	wide := newHarness(t)
	wide.loadList(t, sampleList()...)
	wide.resize(t, 200, 20)
	wide.send(t, wide.m.PollResultMsg(&usecase.PollResult{NextInterval: time.Minute}, err))
	gt.S(t, stripANSI(lastLine(wide.m.Render()))).Contains("retrying in")

	narrow := newHarness(t)
	narrow.loadList(t, sampleList()...)
	narrow.resize(t, 45, 20)
	narrow.send(t, narrow.m.PollResultMsg(&usecase.PollResult{NextInterval: time.Minute}, err))

	out := stripANSI(lastLine(narrow.m.Render()))
	gt.N(t, len([]rune(out))).LessOrEqual(45)
	gt.S(t, out).ContainsNone("top of the hour")
}

func TestRenderNeverLeaksTheErrorString(t *testing.T) {
	const marker = "goerr-internal-marker-9f2a"
	err := model.WithUserMessage(goerr.New(marker),
		model.UserMessage{Summary: "GitHub returned 503", Action: "retrying in 2m0s"})

	h := newHarness(t)
	h.loadList(t, sampleList()...)
	h.send(t, h.m.PollResultMsg(&usecase.PollResult{NextInterval: 2 * time.Minute}, err))

	out := stripANSI(h.m.Render())
	gt.S(t, out).Contains("GitHub returned 503")
	// The chain and the stack belong in the log, never on screen.
	gt.S(t, out).ContainsNone(marker)
}

func TestRenderUnknownErrorUsesTheDefaultMessage(t *testing.T) {
	const marker = "unwrapped-internal-detail-4c81"

	h := newHarness(t)
	h.loadList(t, sampleList()...)
	h.send(t, h.m.PollResultMsg(&usecase.PollResult{NextInterval: time.Minute}, goerr.New(marker)))

	out := stripANSI(h.m.Render())
	gt.S(t, out).Contains("something went wrong")
	gt.S(t, out).ContainsNone(marker)
}

func TestRenderNarrowTerminal(t *testing.T) {
	h := newHarness(t)
	h.loadList(t, sampleList()...)

	h.resize(t, 30, 20)
	gt.Equal(t, h.m.Render(), "terminal too narrow")
}

func TestRenderShortTerminal(t *testing.T) {
	h := newHarness(t)
	h.loadList(t, sampleList()...)

	// Two rows of chrome and nothing left for the list.
	h.resize(t, 100, 2)
	out := h.m.Render()
	gt.Equal(t, len(strings.Split(out, "\n")), 2)

	h.resize(t, 100, 1)
	gt.NotEqual(t, h.m.Render(), "")
}

func TestRenderBeforeTheFirstWindowSize(t *testing.T) {
	h := newHarness(t)
	// Width and height are still zero here; drawing must not panic.
	h.send(t, h.m.PollResultMsg(&usecase.PollResult{
		Notifications:  sampleList(),
		ReviewRequests: model.ReviewRequests{},
		NextInterval:   time.Minute,
	}, nil))

	gt.NotEqual(t, h.m.Render(), "")
}

func TestRenderHelpOverlay(t *testing.T) {
	h := newHarness(t)
	h.loadList(t, sampleList()...)
	h.send(t, press('?'))

	out := stripANSI(h.m.Render())
	gt.S(t, out).Contains("archive (done)")
	gt.S(t, out).Contains("mark unread")
	gt.S(t, out).Contains("g g")
	gt.S(t, out).Contains("* a")
}

func TestRenderAuthScreen(t *testing.T) {
	h := newHarness(t)
	h.resize(t, 100, 20)

	out := stripANSI(h.m.Render())
	gt.S(t, out).Contains("Not signed in")
	gt.S(t, out).Contains("Press o to sign in")
}

func lastLine(s string) string {
	lines := strings.Split(s, "\n")
	return lines[len(lines)-1]
}
