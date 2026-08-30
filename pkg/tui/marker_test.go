package tui_test

import (
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/m-mizutani/gt"
	"github.com/m-mizutani/octify/pkg/domain/model"
	"github.com/m-mizutani/octify/pkg/domain/types"
	"github.com/m-mizutani/octify/pkg/tui"
	"github.com/m-mizutani/octify/pkg/usecase"
)

// markerColumns covers the author bar and selection, then the unread, review
// request and state markers with the single space between each.
const markerColumns = 8

// SGR parameters, as they appear in the rendered output.
const (
	sgrBold    = "1"
	sgrFaint   = "2"
	sgrReverse = "7"
	sgrBlue    = "34"
	sgrMagenta = "35"
	sgrCyan    = "36"
	sgrRed     = "31"
	sgrYellow  = "33"
)

var sgrRe = regexp.MustCompile(`\x1b\[([0-9;]*)m`)

// sgrRuns lists the parameters of every SGR sequence in s, skipping the plain
// resets that close each run.
func sgrRuns(s string) []string {
	var out []string
	for _, m := range sgrRe.FindAllStringSubmatch(s, -1) {
		if m[1] == "" || m[1] == "0" {
			continue
		}
		out = append(out, m[1])
	}
	return out
}

// sgrBefore returns the parameters of the SGR sequence that opens the run
// containing the first occurrence of sub.
func sgrBefore(t *testing.T, s, sub string) string {
	t.Helper()

	at := strings.Index(s, sub)
	gt.True(t, at >= 0)

	last := ""
	for _, m := range sgrRe.FindAllStringSubmatchIndex(s, -1) {
		if m[0] >= at {
			break
		}
		last = s[m[2]:m[3]]
	}
	return last
}

func hasParam(params, want string) bool {
	for _, p := range strings.Split(params, ";") {
		if p == want {
			return true
		}
	}
	return false
}

// loadWithStates puts the model into the ready state with a list and the
// subject states a poll would have resolved for it.
func (h *harness) loadWithStates(t *testing.T, ns []model.Notification, states model.SubjectStates) {
	t.Helper()
	h.send(t, h.m.PollResultMsg(&usecase.PollResult{
		Notifications:  ns,
		ReviewRequests: model.ReviewRequests{},
		SubjectStates:  states,
		NextInterval:   time.Minute,
	}, nil))
	gt.Equal(t, h.m.Phase(), tui.PhaseReady)
}

// markerText returns the marker columns of one rendered row, with the styling
// removed. It slices runes, not bytes: the bar and the unread dot are both
// multi-byte.
func markerText(rendered string, row int) string {
	lines := strings.Split(stripANSI(rendered), "\n")
	if row >= len(lines) {
		return ""
	}
	runes := []rune(lines[row])
	if len(runes) < markerColumns {
		return string(runes)
	}
	return string(runes[:markerColumns])
}

// rawRow returns one line of the rendered output with its styling intact.
func rawRow(rendered string, row int) string {
	lines := strings.Split(rendered, "\n")
	if row >= len(lines) {
		return ""
	}
	return lines[row]
}

func TestRenderShowsAuthorAndStateMarkers(t *testing.T) {
	list := []model.Notification{
		notification("1", types.SubjectPullRequest, "acme/tools", 1, true),
		notification("2", types.SubjectPullRequest, "acme/tools", 2, true),
		notification("3", types.SubjectPullRequest, "acme/tools", 3, true),
		notification("4", types.SubjectPullRequest, "acme/tools", 4, true),
	}

	h := newHarness(t)
	h.resize(t, 100, 20)
	h.loadWithStates(t, list, model.SubjectStates{
		{Repo: "acme/tools", Number: 1}: {Authored: true, Merged: true},
		{Repo: "acme/tools", Number: 2}: {Closed: true},
		{Repo: "acme/tools", Number: 3}: {Authored: true},
		{Repo: "acme/tools", Number: 4}: {},
	})

	out := h.m.Render()

	// Columns are: author bar, selection, unread, review request, state.
	gt.Equal(t, markerText(out, 1), "▏  ●   M")
	gt.Equal(t, markerText(out, 2), "   ●   C")
	gt.Equal(t, markerText(out, 3), "▏  ●    ")
	gt.Equal(t, markerText(out, 4), "   ●    ")
}

func TestRenderColoursTheMarkers(t *testing.T) {
	list := []model.Notification{
		notification("1", types.SubjectPullRequest, "acme/tools", 1, true),
		notification("2", types.SubjectPullRequest, "acme/tools", 2, true),
	}

	h := newHarness(t)
	h.resize(t, 100, 20)
	h.send(t, h.m.PollResultMsg(&usecase.PollResult{
		Notifications: list,
		ReviewRequests: model.ReviewRequests{
			model.SubjectRef{Repo: "acme/tools", Number: 1}: struct{}{},
		},
		SubjectStates: model.SubjectStates{
			{Repo: "acme/tools", Number: 1}: {Authored: true},
			{Repo: "acme/tools", Number: 2}: {Merged: true},
		},
		NextInterval: time.Minute,
	}, nil))

	first := rawRow(h.m.Render(), 1)
	gt.True(t, hasParam(sgrBefore(t, first, "▏"), sgrCyan))
	gt.True(t, hasParam(sgrBefore(t, first, "●"), sgrBlue))
	gt.True(t, hasParam(sgrBefore(t, first, "R"), sgrYellow))

	second := rawRow(h.m.Render(), 2)
	gt.True(t, hasParam(sgrBefore(t, second, "M"), sgrMagenta))

	h.loadWithStates(t, list, model.SubjectStates{
		{Repo: "acme/tools", Number: 1}: {Closed: true},
	})
	gt.True(t, hasParam(sgrBefore(t, rawRow(h.m.Render(), 1), "C"), sgrRed))
}

func TestRenderWithoutStatesShowsNoMarkers(t *testing.T) {
	h := newHarness(t)
	h.resize(t, 100, 20)
	h.loadWithStates(t, sampleList(), model.SubjectStates{})

	out := h.m.Render()

	// Nothing resolved, so no row claims an author or a finished state.
	gt.S(t, stripANSI(out)).NotContains("▏")
	for row := 1; row <= 5; row++ {
		gt.Equal(t, markerText(out, row), "   ●    ")
	}
}

func TestRenderIgnoresStatesForSubjectsWithoutARef(t *testing.T) {
	list := []model.Notification{
		notification("1", types.SubjectCheckSuite, "acme/tools", 1, true),
	}

	h := newHarness(t)
	h.resize(t, 100, 20)
	// A state that would match if check suites had references at all.
	h.loadWithStates(t, list, model.SubjectStates{
		{Repo: "acme/tools", Number: 1}: {Authored: true, Merged: true},
	})

	gt.Equal(t, markerText(h.m.Render(), 1), "   ●    ")
}

func TestRenderWidthIsUnaffectedByMarkers(t *testing.T) {
	list := []model.Notification{notification("1", types.SubjectPullRequest, "acme/tools", 1, true)}

	for _, width := range []int{40, 60, 80, 120, 200} {
		plain := newHarness(t)
		plain.resize(t, width, 20)
		plain.loadWithStates(t, list, model.SubjectStates{})

		marked := newHarness(t)
		marked.resize(t, width, 20)
		marked.loadWithStates(t, list, model.SubjectStates{
			{Repo: "acme/tools", Number: 1}: {Authored: true, Merged: true},
		})

		plainRow := rawRow(plain.m.Render(), 1)
		markedRow := rawRow(marked.m.Render(), 1)

		// The colours cost no columns, and neither row runs past the terminal.
		gt.Equal(t, ansi.StringWidth(markedRow), ansi.StringWidth(plainRow))
		gt.True(t, ansi.StringWidth(markedRow) <= width)
	}
}

// The row context has to reach every column. Wrapping the finished line would
// let the first coloured marker's reset cancel it for everything after.
func TestRenderRowContextReachesEveryColumn(t *testing.T) {
	list := []model.Notification{notification("1", types.SubjectPullRequest, "acme/tools", 1, true)}

	t.Run("cursor", func(t *testing.T) {
		h := newHarness(t)
		h.resize(t, 100, 20)
		h.loadWithStates(t, list, model.SubjectStates{
			{Repo: "acme/tools", Number: 1}: {Authored: true, Merged: true},
		})

		runs := sgrRuns(rawRow(h.m.Render(), 1))
		gt.True(t, len(runs) >= markerColumns)
		for _, run := range runs {
			gt.True(t, hasParam(run, sgrReverse))
		}
	})

	t.Run("finished", func(t *testing.T) {
		h := newHarness(t)
		h.resize(t, 100, 20)
		h.loadWithStates(t, []model.Notification{
			notification("0", types.SubjectPullRequest, "acme/tools", 9, true),
			notification("1", types.SubjectPullRequest, "acme/tools", 1, true),
		}, model.SubjectStates{
			{Repo: "acme/tools", Number: 1}: {Authored: true, Merged: true},
		})

		// Row 2 is the merged one; the cursor sits on row 1.
		runs := sgrRuns(rawRow(h.m.Render(), 2))
		gt.True(t, len(runs) >= markerColumns)
		for _, run := range runs {
			gt.True(t, hasParam(run, sgrFaint))
			gt.False(t, hasParam(run, sgrReverse))
		}
	})
}

// A finished row sinks without losing its unread marker: the row is dim and the
// dot keeps its colour and weight.
func TestRenderFinishedRowKeepsTheUnreadMarker(t *testing.T) {
	h := newHarness(t)
	h.resize(t, 100, 20)
	h.loadWithStates(t, []model.Notification{
		notification("0", types.SubjectPullRequest, "acme/tools", 9, true),
		notification("1", types.SubjectPullRequest, "acme/tools", 1, true),
	}, model.SubjectStates{
		{Repo: "acme/tools", Number: 1}: {Merged: true},
	})

	params := sgrBefore(t, rawRow(h.m.Render(), 2), "●")
	gt.True(t, hasParam(params, sgrFaint))
	gt.True(t, hasParam(params, sgrBold))
	gt.True(t, hasParam(params, sgrBlue))
}

func TestRenderTruncatesWithoutBreakingEscapes(t *testing.T) {
	n := notification("1", types.SubjectPullRequest, "acme/tools", 1, true)
	n.Subject.Title = strings.Repeat("very long title ", 40)

	// The fixed columns are 52 wide and the title never falls below 8, so any
	// terminal narrower than 60 forces a cut.
	for _, width := range []int{40, 50, 59} {
		h := newHarness(t)
		h.resize(t, width, 20)
		h.loadWithStates(t, []model.Notification{n}, model.SubjectStates{
			{Repo: "acme/tools", Number: 1}: {Authored: true, Merged: true},
		})

		row := rawRow(h.m.Render(), 1)
		gt.Equal(t, ansi.StringWidth(row), width)

		// Every escape byte that survives must still belong to a complete SGR
		// sequence, so nothing was cut in half.
		gt.Equal(t, strings.Count(row, "\x1b"), len(sgrRe.FindAllString(row, -1)))
	}

	// Wider terminals leave the row uncut. Every column is padded, so the row
	// still fills the width exactly and the cursor highlight reaches the edge.
	for _, width := range []int{80, 120, 200} {
		h := newHarness(t)
		h.resize(t, width, 20)
		h.loadWithStates(t, []model.Notification{n}, model.SubjectStates{
			{Repo: "acme/tools", Number: 1}: {Authored: true, Merged: true},
		})

		row := rawRow(h.m.Render(), 1)
		gt.Equal(t, ansi.StringWidth(row), width)
		gt.Equal(t, strings.Count(row, "\x1b"), len(sgrRe.FindAllString(row, -1)))
	}
}

func TestRenderColoursTheActiveTab(t *testing.T) {
	h := newHarness(t)
	h.resize(t, 100, 20)
	h.loadWithStates(t, sampleList(), model.SubjectStates{})

	header := rawRow(h.m.Render(), 0)

	// The active tab is tab 1 on load; the others stay faint. Underlining makes
	// lipgloss style the active label character by character, so the assertion
	// looks at one character of it rather than the whole label.
	gt.True(t, hasParam(sgrBefore(t, header, "A"), sgrCyan))
	gt.True(t, hasParam(sgrBefore(t, header, "[2] PR"), sgrFaint))
	gt.False(t, hasParam(sgrBefore(t, header, "[2] PR"), sgrCyan))
}

func TestRenderColoursTheUnreadCount(t *testing.T) {
	h := newHarness(t)
	h.resize(t, 100, 20)
	h.loadWithStates(t, sampleList(), model.SubjectStates{})

	status := rawRow(h.m.Render(), 19)
	gt.True(t, hasParam(sgrBefore(t, status, "5 unread"), sgrCyan))

	// With nothing unread the count recedes into the rest of the status line.
	h.loadWithStates(t, nil, model.SubjectStates{})
	status = rawRow(h.m.Render(), 19)
	gt.True(t, hasParam(sgrBefore(t, status, "0 unread"), sgrFaint))
	gt.False(t, hasParam(sgrBefore(t, status, "0 unread"), sgrCyan))
}

func TestRenderHelpExplainsTheMarkers(t *testing.T) {
	h := newHarness(t)
	h.loadList(t, sampleList()...)
	h.send(t, press('?'))

	out := h.m.Render()
	plain := stripANSI(out)

	gt.S(t, plain).Contains("Markers")
	gt.S(t, plain).Contains("you opened it")
	gt.S(t, plain).Contains("review requested")
	gt.S(t, plain).Contains("merged")
	gt.S(t, plain).Contains("closed without merging")

	// The legend teaches the colour as well as the shape. Each symbol is styled
	// on its own, so the colour sits immediately before it.
	gt.S(t, out).Contains("\x1b[" + sgrCyan + "m▏")
	gt.S(t, out).Contains("\x1b[" + sgrMagenta + "mM")
	gt.S(t, out).Contains("\x1b[" + sgrRed + "mC")
	gt.S(t, out).Contains("\x1b[" + sgrBold + ";" + sgrYellow + "mR")
}
