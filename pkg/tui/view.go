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

	repoWidth = 28
	kindWidth = 8
	timeWidth = 4
	// markerWidth counts the author bar, selection, unread, review request and
	// state columns.
	markerWidth = 5
	// separators is the single space between each of the eight columns. The
	// author bar is glued to the row's left edge and takes none of them.
	separators = 7
)

// authorBar marks a row the signed-in user opened. It is a shape rather than a
// letter because it is scanned, not read: it has to register in peripheral
// vision while the eye is on the titles.
const authorBar = "▏"

// Colours come from the terminal's own 4-bit palette rather than fixed values,
// so the user's theme decides the hues and the list stays legible on a light
// scheme and a dark one alike.
var (
	styleTabActive   = lipgloss.NewStyle().Bold(true).Underline(true).Foreground(lipgloss.Cyan)
	styleTabInactive = lipgloss.NewStyle().Faint(true)
	styleStatus      = lipgloss.NewStyle().Faint(true)
	styleCount       = lipgloss.NewStyle().Foreground(lipgloss.Cyan)

	stylePlain     = lipgloss.NewStyle()
	styleAuthorBar = lipgloss.NewStyle().Foreground(lipgloss.Cyan)
	styleSelected  = lipgloss.NewStyle().Bold(true)
	styleUnread    = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Blue)
	styleRead      = lipgloss.NewStyle().Faint(true)
	styleReview    = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Yellow)
	styleMerged    = lipgloss.NewStyle().Foreground(lipgloss.Magenta)
	styleClosed    = lipgloss.NewStyle().Foreground(lipgloss.Red)
	styleMeta      = lipgloss.NewStyle().Faint(true)
	styleTitle     = lipgloss.NewStyle()
)

// rowContext is what applies to every column of one row.
//
// Composing it into each column's own style is what keeps the row intact.
// Wrapping the finished line instead - which is what this file used to do -
// emits the outer attribute once and then lets the first coloured column's
// reset cancel it for everything after: ESC[7m x ESC[36m A ESC[m rest… leaves
// "rest" unhighlighted. Composed, the same row reads ESC[1;7;2;34m and has no
// inner reset at all.
type rowContext struct {
	cursor   bool
	finished bool
}

func (r rowContext) apply(s lipgloss.Style) lipgloss.Style {
	if r.finished {
		s = s.Faint(true)
	}
	if r.cursor {
		s = s.Reverse(true)
	}
	return s
}

// cell renders one column, which the caller has already padded to its width.
func (r rowContext) cell(text string, s lipgloss.Style) string {
	return r.apply(s).Render(text)
}

// gap renders the single space between two columns. It carries the context too,
// or the cursor highlight would break at every column boundary.
func (r rowContext) gap() string {
	return r.apply(stylePlain).Render(" ")
}

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

	// The legend draws each symbol in the style the list uses, so the colour is
	// part of the explanation rather than something to memorise separately.
	lines = append(lines, "", "Markers", "")
	for _, e := range markerLegend() {
		lines = append(lines, "  "+pad(e.style.Render(e.symbol), 12)+"  "+e.desc)
	}

	lines = append(lines, "", styleStatus.Render("esc or ? to close"))
	return strings.Join(lines, "\n")
}

func (m Model) renderList() string {
	lines := []string{m.renderTabs()}

	if height := m.listHeight(); height > 0 {
		lines = append(lines, m.renderRows(height)...)
	}

	lines = append(lines, m.renderStatus())
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

// renderRow builds one row column by column. Each column carries its own style
// with the row's context composed in, which is what lets the markers be
// coloured without the cursor highlight stopping at the first one.
func (m Model) renderRow(n model.Notification, atCursor bool) string {
	var (
		st    model.SubjectState
		known bool
	)
	if ref, ok := n.SubjectRef(); ok {
		st, known = m.states.Lookup(ref)
	}

	ctx := rowContext{cursor: atCursor, finished: known && st.Finished()}

	bar, barStyle := " ", stylePlain
	if known && st.Authored {
		bar, barStyle = authorBar, styleAuthorBar
	}

	selection, selectionStyle := " ", stylePlain
	if _, ok := m.selected[n.ID]; ok {
		selection, selectionStyle = "x", styleSelected
	}

	unread, unreadStyle := "○", styleRead
	if m.uc.Unread(n) {
		unread, unreadStyle = "●", styleUnread
	}

	review, reviewStyle := " ", stylePlain
	if ref, ok := n.PullRequestRef(); ok && m.reviews.Has(ref) {
		review, reviewStyle = "R", styleReview
	}

	state, stateStyle := " ", stylePlain
	switch {
	case known && st.Merged:
		state, stateStyle = "M", styleMerged
	case known && st.Closed:
		state, stateStyle = "C", styleClosed
	}

	kind := subjectKind(n) + " " + subjectNumber(n)

	// Everything except the title is fixed width, so the title absorbs the rest.
	fixed := markerWidth + repoWidth + kindWidth + timeWidth + separators
	titleWidth := max(m.width-fixed, 8)

	cells := []string{
		ctx.cell(bar, barStyle) + ctx.cell(selection, selectionStyle),
		ctx.cell(unread, unreadStyle),
		ctx.cell(review, reviewStyle),
		ctx.cell(state, stateStyle),
		ctx.cell(pad(truncateLeft(string(n.Repo.FullName), repoWidth), repoWidth), styleMeta),
		ctx.cell(pad(kind, kindWidth), styleMeta),
		ctx.cell(pad(truncate(n.Subject.Title, titleWidth), titleWidth), styleTitle),
		// Padded, unlike before: a relative time is usually shorter than its
		// budget, and the leftover columns would end the cursor highlight short
		// of the right edge now that the row is styled column by column.
		ctx.cell(pad(relativeTime(n.UpdatedAt, m.cfg.Now()), timeWidth), styleMeta),
	}
	return truncate(strings.Join(cells, ctx.gap()), m.width)
}

// renderStatus styles each part separately rather than wrapping the finished
// line: the unread count carries a colour, and its reset would end the faint
// treatment of everything after it.
func (m Model) renderStatus() string {
	parts := make([]string, 0, 5)

	if n := len(m.selected); n > 0 {
		parts = append(parts, styleStatus.Render(strconv.Itoa(n)+" selected"))
	}

	unread := strconv.Itoa(m.unreadCount()) + " unread"
	if m.unreadCount() > 0 {
		parts = append(parts, styleCount.Render(unread))
	} else {
		parts = append(parts, styleStatus.Render(unread))
	}

	if !m.showRead {
		parts = append(parts, styleStatus.Render("unread only"))
	}
	if m.filtering {
		parts = append(parts, styleStatus.Render("filter: "+m.filter+"▌"))
	} else if m.filter != "" {
		parts = append(parts, styleStatus.Render("filter: "+m.filter))
	}
	if !m.nextPollAt.IsZero() {
		if d := m.nextPollAt.Sub(m.cfg.Now()); d > 0 {
			parts = append(parts, styleStatus.Render("poll in "+shortDuration(d)))
		}
	}

	line := strings.Join(parts, styleStatus.Render(" · "))
	if m.status.Summary != "" {
		// The message gets whatever the counters left over; its action is the
		// first thing to go when that is not enough.
		remaining := m.width - ansi.StringWidth(line) - 3
		if message := joinMessage(m.status, remaining); message != "" {
			line += styleStatus.Render(" · " + message)
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
