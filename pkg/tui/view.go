package tui

import (
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/m-mizutani/octify/pkg/domain/model"
	"github.com/m-mizutani/octify/pkg/domain/types"
)

const (
	// minWidth is the narrowest terminal the list can still be read in.
	minWidth = 40
	// chrome is the tab row plus the status row.
	chrome = 2

	repoWidth   = 28
	kindWidth   = 8
	timeWidth   = 4
	markerWidth = 3 // selection, unread, review request
)

var (
	styleTabActive   = lipgloss.NewStyle().Bold(true).Underline(true)
	styleTabInactive = lipgloss.NewStyle().Faint(true)
	styleUnread      = lipgloss.NewStyle().Bold(true)
	styleCursor      = lipgloss.NewStyle().Reverse(true)
	styleStatus      = lipgloss.NewStyle().Faint(true)
)

// listHeight is how many notification rows fit on screen.
func (m Model) listHeight() int {
	h := m.height - chrome
	if h < 0 {
		return 0
	}
	return h
}

func (m Model) View() tea.View {
	view := tea.NewView(m.render())
	// The list owns the whole terminal for as long as octify runs.
	view.AltScreen = true
	return view
}

func (m Model) render() string {
	if m.width > 0 && m.width < minWidth {
		return "terminal too narrow"
	}
	if m.phase == phaseUnauthenticated || m.phase == phaseAuthenticating {
		return m.renderAuth()
	}
	if m.helpOpen {
		return m.renderHelp()
	}
	return m.renderList()
}

func (m Model) renderAuth() string {
	lines := []string{"octify", ""}

	switch {
	case m.phase == phaseAuthenticating && m.device != nil:
		lines = append(lines,
			"Open "+m.device.VerificationURI,
			"and enter the code:  "+m.device.UserCode,
			"",
			"Waiting for authorization…",
		)
	default:
		lines = append(lines, "Not signed in.", "", "Press o to sign in with GitHub.")
	}

	if m.status.Summary != "" {
		lines = append(lines, "", styleStatus.Render(joinMessage(m.status, m.width)))
	}
	lines = append(lines, "", styleStatus.Render("q to quit"))
	return strings.Join(lines, "\n")
}

func (m Model) renderHelp() string {
	lines := []string{"Keys", ""}
	for _, e := range m.keys.helpEntries() {
		lines = append(lines, "  "+pad(e.keys, 12)+"  "+e.desc)
	}
	lines = append(lines, "", styleStatus.Render("esc or ? to close"))
	return strings.Join(lines, "\n")
}

func (m Model) renderList() string {
	lines := []string{m.renderTabs()}

	if height := m.listHeight(); height > 0 {
		lines = append(lines, m.renderRows(height)...)
	}

	lines = append(lines, styleStatus.Render(m.renderStatus()))
	return strings.Join(lines, "\n")
}

// renderTabs lays out on plain text and styles afterwards. Measuring or cutting
// an already-styled string counts escape bytes as columns and can slice through
// an escape sequence, which then swallows whatever follows it on screen.
func (m Model) renderTabs() string {
	counts := m.tabCounts()

	labels := make([]string, 0, len(types.AllTabs))
	for i, tab := range types.AllTabs {
		labels = append(labels, "["+strconv.Itoa(i+1)+"] "+tab.String()+" "+strconv.Itoa(counts[tab]))
	}

	plain := strings.Join(labels, "  ")
	if ansi.StringWidth(plain) > m.width {
		// No room for the full row, so drop the styling rather than risk cutting
		// an escape sequence.
		return truncate(plain, m.width)
	}

	styled := make([]string, 0, len(labels))
	for i, label := range labels {
		if types.AllTabs[i] == m.tab {
			styled = append(styled, styleTabActive.Render(label))
		} else {
			styled = append(styled, styleTabInactive.Render(label))
		}
	}
	return strings.Join(styled, "  ")
}

func (m Model) renderRows(height int) []string {
	rows := m.visible()
	if len(rows) == 0 {
		out := make([]string, height)
		out[0] = truncate(m.emptyMessage(), m.width)
		return out
	}

	out := make([]string, 0, height)
	for i := m.offset; i < len(rows) && len(out) < height; i++ {
		out = append(out, m.renderRow(rows[i], i == m.cursor))
	}
	for len(out) < height {
		out = append(out, "")
	}
	return out
}

func (m Model) emptyMessage() string {
	switch {
	case m.filter != "":
		return "No notification matches the filter. Press esc to clear it."
	case len(m.all) == 0 && !m.showRead:
		return "No unread notifications. Press a to show everything."
	case len(m.all) == 0:
		return "No notifications."
	default:
		return "Nothing in this tab."
	}
}

// renderRow builds the whole row as plain text and applies at most one style to
// the finished line. The markers are distinguished by the characters themselves
// so that nothing has to be styled mid-line, where a later truncation could cut
// an escape sequence in half.
func (m Model) renderRow(n model.Notification, atCursor bool) string {
	selection := " "
	if _, ok := m.selected[n.ID]; ok {
		selection = "x"
	}

	unread := "○"
	isUnread := m.uc.Unread(n)
	if isUnread {
		unread = "●"
	}

	review := " "
	if ref, ok := n.PullRequestRef(); ok && m.reviews.Has(ref) {
		review = "R"
	}

	kind := subjectKind(n) + " " + subjectNumber(n)

	// Everything except the title is fixed width, so the title absorbs the rest.
	fixed := markerWidth + 1 + repoWidth + 1 + kindWidth + 1 + timeWidth + 3
	titleWidth := max(m.width-fixed, 8)

	line := truncate(strings.Join([]string{
		selection, unread, review,
		pad(truncateLeft(string(n.Repo.FullName), repoWidth), repoWidth),
		pad(kind, kindWidth),
		pad(truncate(n.Subject.Title, titleWidth), titleWidth),
		relativeTime(n.UpdatedAt, m.cfg.Now()),
	}, " "), m.width)

	switch {
	case atCursor:
		return styleCursor.Render(line)
	case isUnread:
		return styleUnread.Render(line)
	default:
		return line
	}
}

func (m Model) renderStatus() string {
	parts := make([]string, 0, 5)

	if n := len(m.selected); n > 0 {
		parts = append(parts, strconv.Itoa(n)+" selected")
	}
	parts = append(parts, strconv.Itoa(m.unreadCount())+" unread")
	if !m.showRead {
		parts = append(parts, "unread only")
	}
	if m.filtering {
		parts = append(parts, "filter: "+m.filter+"▌")
	} else if m.filter != "" {
		parts = append(parts, "filter: "+m.filter)
	}
	if !m.nextPollAt.IsZero() {
		if d := m.nextPollAt.Sub(m.cfg.Now()); d > 0 {
			parts = append(parts, "poll in "+shortDuration(d))
		}
	}

	line := strings.Join(parts, " · ")
	if m.status.Summary != "" {
		// The message gets whatever the counters left over; its action is the
		// first thing to go when that is not enough.
		remaining := m.width - ansi.StringWidth(line) - 3
		if message := joinMessage(m.status, remaining); message != "" {
			line += " · " + message
		}
	}
	return truncate(line, m.width)
}

// joinMessage lays out a user message, dropping the action first when the width
// runs out and only then truncating the summary.
func joinMessage(msg model.UserMessage, width int) string {
	if width <= 0 {
		return msg.Summary
	}
	full := msg.Summary
	if msg.Action != "" {
		full = msg.Summary + " · " + msg.Action
	}
	if ansi.StringWidth(full) <= width {
		return full
	}
	return truncate(msg.Summary, width)
}

func subjectKind(n model.Notification) string {
	switch n.Subject.Type {
	case types.SubjectPullRequest:
		return "PR"
	case types.SubjectIssue:
		return "IS"
	case types.SubjectCheckSuite, types.SubjectWorkflowRun:
		return "CI"
	case types.SubjectCommit:
		return "CM"
	case types.SubjectRelease:
		return "RL"
	case types.SubjectDiscussion:
		return "DS"
	default:
		return "--"
	}
}

func subjectNumber(n model.Notification) string {
	if n.Subject.Number <= 0 {
		return "—"
	}
	return "#" + strconv.Itoa(n.Subject.Number)
}

func relativeTime(t, now time.Time) string {
	if t.IsZero() {
		return "-"
	}
	d := now.Sub(t)
	if d < 0 {
		d = 0
	}
	return shortDuration(d)
}

func shortDuration(d time.Duration) string {
	switch {
	case d < time.Minute:
		return strconv.Itoa(int(d.Seconds())) + "s"
	case d < time.Hour:
		return strconv.Itoa(int(d.Minutes())) + "m"
	case d < 24*time.Hour:
		return strconv.Itoa(int(d.Hours())) + "h"
	default:
		return strconv.Itoa(int(d.Hours()/24)) + "d"
	}
}

// Widths are counted in display cells, not runes: a Japanese title takes two
// columns per rune, so measuring runes would let a row run past the edge of the
// terminal and wrap, pushing everything below it down.

func pad(s string, width int) string {
	if w := ansi.StringWidth(s); w < width {
		return s + strings.Repeat(" ", width-w)
	}
	return truncate(s, width)
}

func truncate(s string, width int) string {
	if width <= 0 {
		return ""
	}
	if ansi.StringWidth(s) <= width {
		return s
	}
	return ansi.Truncate(s, width, "…")
}

// truncateLeft keeps the tail, which is the identifying half of a repository
// name.
func truncateLeft(s string, width int) string {
	if width <= 1 || ansi.StringWidth(s) <= width {
		return s
	}
	return ansi.TruncateLeft(s, ansi.StringWidth(s)-width+1, "…")
}
