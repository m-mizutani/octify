package tui

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"github.com/m-mizutani/goerr/v2"
	"github.com/m-mizutani/octify/pkg/domain/model"
	"github.com/m-mizutani/octify/pkg/domain/types"
	"github.com/m-mizutani/octify/pkg/infra/gh"
	"github.com/m-mizutani/octify/pkg/usecase"
)

// slowDownPenalty is what GitHub documents for the device flow's slow_down
// response: add five seconds to the polling interval.
const slowDownPenalty = 5 * time.Second

type Config struct {
	WebBase string
	// OpenURL launches the browser. It is injected so tests do not spawn one.
	OpenURL  func(ctx context.Context, url string) error
	ShowRead bool
	Now      func() time.Time
}

type phase int

const (
	phaseUnauthenticated phase = iota
	phaseAuthenticating
	phaseLoading
	phaseReady
)

type archiveState struct {
	ch       <-chan usecase.ArchiveEvent
	cancel   context.CancelFunc
	total    int
	done     int
	failed   int
	finished bool
	// fatal records that the run was stopped by a failure the user needs to see,
	// so the completion summary does not overwrite that message.
	fatal bool
}

// Model holds every piece of screen state. The context is carried here because
// Bubble Tea commands are closures with no argument of their own, and every
// command in this program needs the program's context to be cancellable.
type Model struct {
	ctx  context.Context
	uc   *usecase.UseCase
	cfg  Config
	keys keyMap

	phase  phase
	width  int
	height int

	device       *gh.DeviceCode
	authInterval time.Duration

	all        []model.Notification
	reviews    model.ReviewRequests
	states     model.SubjectStates
	pollState  model.PollState
	nextPollAt time.Time

	tab      types.Tab
	cursor   int
	offset   int
	selected map[types.ThreadID]struct{}

	filter    string
	filtering bool
	showRead  bool

	pending  string
	helpOpen bool

	// pollGeneration identifies the current poll chain; anything from an older
	// one is ignored.
	pollGeneration int

	archive *archiveState
	status  model.UserMessage
}

func NewModel(ctx context.Context, uc *usecase.UseCase, cfg Config) Model {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return Model{
		ctx:      ctx,
		uc:       uc,
		cfg:      cfg,
		keys:     defaultKeyMap(),
		phase:    phaseUnauthenticated,
		showRead: cfg.ShowRead,
		selected: make(map[types.ThreadID]struct{}),
		reviews:  model.ReviewRequests{},
		states:   model.SubjectStates{},
	}
}

// Run drives the terminal program until the user quits or ctx ends.
//
// A signal that ends ctx is how a person leaves octify, so it is returned as
// context.Canceled rather than dressed up as a failure.
func Run(ctx context.Context, uc *usecase.UseCase, cfg Config) error {
	program := tea.NewProgram(NewModel(ctx, uc, cfg), tea.WithContext(ctx))
	if _, err := program.Run(); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return goerr.Wrap(err, "terminal program failed")
	}
	return nil
}

// --- messages ---

type restoreMsg struct{ err error }
type deviceCodeMsg struct {
	dc  *gh.DeviceCode
	err error
}
type authTickMsg struct{}
type authResultMsg struct{ err error }

// pollTickMsg and pollResultMsg carry the generation of the poll chain they
// belong to. A manual refresh starts a new chain, and without this the old
// chain's tick would keep firing and scheduling more of its own: every refresh
// would permanently double the request rate.
type pollTickMsg struct{ generation int }
type pollResultMsg struct {
	generation int
	res        *usecase.PollResult
	err        error
}
type archiveEventMsg struct {
	ch <-chan usecase.ArchiveEvent
	ev usecase.ArchiveEvent
	ok bool
}
type openResultMsg struct{ err error }

// --- lifecycle ---

func (m Model) Init() tea.Cmd {
	return func() tea.Msg {
		_, _, err := m.uc.Restore(m.ctx)
		return restoreMsg{err: err}
	}
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.clampCursor()
		return m, nil

	case tea.KeyPressMsg:
		return m.handleKey(msg)

	case restoreMsg:
		if msg.err != nil {
			m.phase = phaseUnauthenticated
			m.status = messageOf(msg.err)
			return m, nil
		}
		m.phase = phaseLoading
		return m, m.pollCmd()

	case deviceCodeMsg:
		if msg.err != nil {
			m.phase = phaseUnauthenticated
			m.status = messageOf(msg.err)
			return m, nil
		}
		m.device = msg.dc
		m.authInterval = msg.dc.Interval
		m.phase = phaseAuthenticating
		m.status = model.UserMessage{
			Summary: "waiting for authorization on GitHub",
			Action:  "enter " + msg.dc.UserCode + " at " + msg.dc.VerificationURI,
		}
		return m, tea.Tick(m.authInterval, func(time.Time) tea.Msg { return authTickMsg{} })

	case authTickMsg:
		if m.device == nil {
			return m, nil
		}
		if m.cfg.Now().After(m.device.ExpiresAt) {
			m.phase = phaseUnauthenticated
			m.device = nil
			m.status = model.UserMessage{Summary: "the device code expired", Action: "press o to start over"}
			return m, nil
		}
		return m, m.authAttemptCmd()

	case authResultMsg:
		return m.handleAuthResult(msg)

	case pollTickMsg:
		if msg.generation != m.pollGeneration {
			// A tick left over from a chain a manual refresh replaced.
			return m, nil
		}
		return m, m.pollCmd()

	case pollResultMsg:
		if msg.generation != m.pollGeneration {
			return m, nil
		}
		return m.handlePollResult(msg)

	case archiveEventMsg:
		return m.handleArchiveEvent(msg)

	case openResultMsg:
		if msg.err != nil {
			m.status = messageOf(msg.err)
		}
		return m, nil
	}

	return m, nil
}

// --- commands ---

func (m Model) pollCmd() tea.Cmd {
	state := m.pollState
	generation := m.pollGeneration
	return func() tea.Msg {
		res, err := m.uc.Poll(m.ctx, state)
		return pollResultMsg{generation: generation, res: res, err: err}
	}
}

// restartPolling abandons the pending chain and begins a new one, so that a
// manual refresh replaces the schedule instead of running alongside it.
func (m *Model) restartPolling() tea.Cmd {
	m.pollGeneration++
	return m.pollCmd()
}

func (m Model) authAttemptCmd() tea.Cmd {
	dc := m.device
	return func() tea.Msg {
		_, _, err := m.uc.TryCompleteDeviceFlow(m.ctx, dc)
		return authResultMsg{err: err}
	}
}

func (m Model) startDeviceFlowCmd() tea.Cmd {
	return func() tea.Msg {
		dc, err := m.uc.StartDeviceFlow(m.ctx)
		return deviceCodeMsg{dc: dc, err: err}
	}
}

func waitArchive(ch <-chan usecase.ArchiveEvent) tea.Cmd {
	return func() tea.Msg {
		ev, ok := <-ch
		return archiveEventMsg{ch: ch, ev: ev, ok: ok}
	}
}

// --- message handlers ---

func (m Model) handleAuthResult(msg authResultMsg) (tea.Model, tea.Cmd) {
	switch {
	case msg.err == nil:
		m.device = nil
		m.phase = phaseLoading
		m.status = model.UserMessage{}
		return m, m.pollCmd()

	case errors.Is(msg.err, gh.ErrAuthorizationPending):
		return m, tea.Tick(m.authInterval, func(time.Time) tea.Msg { return authTickMsg{} })

	case errors.Is(msg.err, gh.ErrSlowDown):
		// GitHub documents exactly this penalty for slow_down.
		m.authInterval += slowDownPenalty
		return m, tea.Tick(m.authInterval, func(time.Time) tea.Msg { return authTickMsg{} })

	default:
		m.phase = phaseUnauthenticated
		m.device = nil
		m.status = messageOf(msg.err)
		return m, nil
	}
}

func (m Model) handlePollResult(msg pollResultMsg) (tea.Model, tea.Cmd) {
	interval := time.Minute
	if msg.res != nil {
		interval = msg.res.NextInterval
		m.pollState = msg.res.NextState
	}

	if msg.err != nil {
		if errors.Is(msg.err, gh.ErrUnauthorized) {
			m.phase = phaseUnauthenticated
			m.all = nil
			m.selected = make(map[types.ThreadID]struct{})
			m.status = messageOf(msg.err)
			return m, nil
		}
		// The list from the previous cycle stays on screen; only the reason and
		// the time of the next attempt change.
		if m.phase == phaseLoading {
			m.phase = phaseReady
		}
		m.status = messageOf(usecase.DescribeRetry(msg.err, interval))
		m.nextPollAt = m.cfg.Now().Add(interval)
		return m, m.scheduleNextPoll(interval)
	}

	m.phase = phaseReady
	m.nextPollAt = m.cfg.Now().Add(interval)

	// A success always carries a result; the guard above exists for the failure
	// path, where only the schedule comes back.
	if msg.res == nil {
		return m, m.scheduleNextPoll(interval)
	}

	if !msg.res.NotModified {
		m.applyNotifications(msg.res)
	}
	m.status = pollStatus(msg.res)

	return m, m.scheduleNextPoll(interval)
}

func (m Model) scheduleNextPoll(d time.Duration) tea.Cmd {
	generation := m.pollGeneration
	return tea.Tick(d, func(time.Time) tea.Msg { return pollTickMsg{generation: generation} })
}

// applyNotifications replaces the list wholesale: GitHub's response is the
// authority on what is in the inbox, so merging would keep rows the web UI has
// already dealt with.
func (m *Model) applyNotifications(res *usecase.PollResult) {
	cursorID := m.cursorID()

	m.all = res.Notifications
	m.reviews = res.ReviewRequests
	m.states = res.SubjectStates

	alive := make(map[types.ThreadID]struct{}, len(m.all))
	for _, n := range m.all {
		alive[n.ID] = struct{}{}
	}
	for id := range m.selected {
		if _, ok := alive[id]; !ok {
			delete(m.selected, id)
		}
	}

	m.restoreCursor(cursorID)
}

func (m Model) handleArchiveEvent(msg archiveEventMsg) (tea.Model, tea.Cmd) {
	if m.archive == nil {
		return m, nil
	}

	// Ignore anything from a job that has already been replaced.
	if msg.ch != m.archive.ch {
		return m, nil
	}

	if !msg.ok {
		m.archive.finished = true
		// Releasing the child context keeps a completed job from staying attached
		// to the program-lifetime one.
		m.archive.cancel()
		if !m.archive.fatal {
			m.status = archiveSummary(m.archive)
		}
		return m, nil
	}

	m.archive.done++
	switch {
	case msg.ev.Err == nil:
		m.removeNotification(msg.ev.ID)
	case msg.ev.Fatal:
		m.archive.failed++
		m.archive.fatal = true
		m.phase = phaseUnauthenticated
		m.all = nil
		m.selected = make(map[types.ThreadID]struct{})
		m.status = messageOf(msg.ev.Err)
		return m, waitArchive(msg.ch)
	default:
		m.archive.failed++
	}

	m.status = model.UserMessage{
		Summary: "archiving " + strconv.Itoa(m.archive.done) + "/" + strconv.Itoa(m.archive.total),
		Action:  "esc to stop",
	}
	return m, waitArchive(msg.ch)
}

func (m *Model) removeNotification(id types.ThreadID) {
	cursorID := m.cursorID()

	out := m.all[:0]
	for _, n := range m.all {
		if n.ID != id {
			out = append(out, n)
		}
	}
	m.all = out
	delete(m.selected, id)

	if cursorID == id {
		m.clampCursor()
		return
	}
	m.restoreCursor(cursorID)
}

// --- derived state ---

func (m Model) matches(n model.Notification) bool {
	if m.tab != types.TabAll && n.Tab() != m.tab {
		return false
	}
	if !m.showRead && !m.uc.Unread(n) {
		return false
	}
	if m.filter == "" {
		return true
	}
	q := strings.ToLower(m.filter)
	return strings.Contains(strings.ToLower(string(n.Repo.FullName)), q) ||
		strings.Contains(strings.ToLower(n.Subject.Title), q)
}

// visible is the list under the current tab and filter.
func (m Model) visible() []model.Notification {
	out := make([]model.Notification, 0, len(m.all))
	for _, n := range m.all {
		if m.matches(n) {
			out = append(out, n)
		}
	}
	return out
}

// tabCounts applies the filter but not the tab, so each tab can show how much
// it holds.
func (m Model) tabCounts() map[types.Tab]int {
	counts := make(map[types.Tab]int, len(types.AllTabs))
	saved := m.tab
	m.tab = types.TabAll
	for _, n := range m.all {
		if !m.matches(n) {
			continue
		}
		counts[types.TabAll]++
		counts[n.Tab()]++
	}
	m.tab = saved
	return counts
}

func (m Model) unreadCount() int {
	count := 0
	for _, n := range m.all {
		if m.uc.Unread(n) {
			count++
		}
	}
	return count
}

func (m Model) cursorID() types.ThreadID {
	rows := m.visible()
	if m.cursor < 0 || m.cursor >= len(rows) {
		return ""
	}
	return rows[m.cursor].ID
}

// restoreCursor keeps the cursor on the same notification across a refresh, and
// falls back to the same position when that notification is gone.
func (m *Model) restoreCursor(id types.ThreadID) {
	if id != "" {
		for i, n := range m.visible() {
			if n.ID == id {
				m.cursor = i
				m.clampCursor()
				return
			}
		}
	}
	m.clampCursor()
}

func (m *Model) clampCursor() {
	rows := len(m.visible())
	if rows == 0 {
		m.cursor, m.offset = 0, 0
		return
	}
	if m.cursor >= rows {
		m.cursor = rows - 1
	}
	if m.cursor < 0 {
		m.cursor = 0
	}

	height := m.listHeight()
	if height <= 0 {
		m.offset = 0
		return
	}
	if m.cursor < m.offset {
		m.offset = m.cursor
	}
	if m.cursor >= m.offset+height {
		m.offset = m.cursor - height + 1
	}
	if m.offset > rows-height {
		m.offset = rows - height
	}
	if m.offset < 0 {
		m.offset = 0
	}
}

// targets is what an action applies to: the selection, or the cursor row when
// nothing is selected.
func (m Model) targets() []model.Notification {
	rows := m.visible()
	if len(m.selected) > 0 {
		out := make([]model.Notification, 0, len(m.selected))
		for _, n := range m.all {
			if _, ok := m.selected[n.ID]; ok {
				out = append(out, n)
			}
		}
		return out
	}
	if m.cursor >= 0 && m.cursor < len(rows) {
		return []model.Notification{rows[m.cursor]}
	}
	return nil
}

func messageOf(err error) model.UserMessage {
	if msg, ok := model.UserMessageOf(err); ok {
		return msg
	}
	return model.UserMessage{
		Summary: "something went wrong",
		Action:  "rerun with --log-file to capture details",
	}
}

func pollStatus(res *usecase.PollResult) model.UserMessage {
	switch {
	case res.ReconcileErr != nil:
		return messageOf(res.ReconcileErr)
	case res.ReviewErr != nil:
		return model.UserMessage{Summary: "review status unavailable"}
	case res.Truncated:
		return model.UserMessage{Summary: "showing the first " + strconv.Itoa(len(res.Notifications)) + " notifications"}
	default:
		return model.UserMessage{}
	}
}

func archiveSummary(st *archiveState) model.UserMessage {
	succeeded := st.done - st.failed
	summary := strconv.Itoa(succeeded) + " archived"
	if st.failed > 0 {
		summary += " · " + strconv.Itoa(st.failed) + " failed"
	}
	return model.UserMessage{Summary: summary}
}

// --- key handling ---

func (m Model) handleKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	// Text entry swallows everything except the two keys that end it, so that a
	// query can contain j, q or any other command key.
	if m.filtering {
		return m.handleFilterKey(msg)
	}

	if m.pending != "" {
		return m.resolvePending(msg)
	}

	if key.Matches(msg, m.keys.Quit) {
		return m, tea.Quit
	}
	if key.Matches(msg, m.keys.Help) {
		m.helpOpen = !m.helpOpen
		return m, nil
	}

	if m.phase == phaseUnauthenticated {
		if key.Matches(msg, m.keys.AuthStart) {
			return m, m.startDeviceFlowCmd()
		}
		return m, nil
	}
	if m.phase == phaseAuthenticating {
		return m, nil
	}

	switch msg.String() {
	case prefixGoto, prefixSelect:
		m.pending = msg.String()
		return m, nil
	case "1", "2", "3", "4", "5":
		index, _ := strconv.Atoi(msg.String())
		m.setTab(types.AllTabs[index-1])
		return m, nil
	}

	return m.handleListKey(msg)
}

func (m Model) handleListKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch {
	case key.Matches(msg, m.keys.Escape):
		return m.handleEscape()

	case key.Matches(msg, m.keys.Down):
		m.cursor++
		m.clampCursor()

	case key.Matches(msg, m.keys.Up):
		m.cursor--
		m.clampCursor()

	case key.Matches(msg, m.keys.Bottom):
		m.cursor = len(m.visible()) - 1
		m.clampCursor()

	case key.Matches(msg, m.keys.HalfDown):
		m.cursor += max(m.listHeight()/2, 1)
		m.clampCursor()

	case key.Matches(msg, m.keys.HalfUp):
		m.cursor -= max(m.listHeight()/2, 1)
		m.clampCursor()

	case key.Matches(msg, m.keys.NextTab):
		m.setTab(types.AllTabs[(int(m.tab)+1)%len(types.AllTabs)])

	case key.Matches(msg, m.keys.PrevTab):
		m.setTab(types.AllTabs[(int(m.tab)+len(types.AllTabs)-1)%len(types.AllTabs)])

	case key.Matches(msg, m.keys.Select):
		rows := m.visible()
		if m.cursor < len(rows) {
			id := rows[m.cursor].ID
			if _, ok := m.selected[id]; ok {
				delete(m.selected, id)
			} else {
				m.selected[id] = struct{}{}
			}
		}

	case key.Matches(msg, m.keys.ToggleShowRead):
		// A display filter over what is already fetched, not a fetch condition.
		m.showRead = !m.showRead
		m.clampCursor()

	case key.Matches(msg, m.keys.MarkRead):
		return m.setReadState(model.ReadStateRead)

	case key.Matches(msg, m.keys.MarkUnread):
		return m.setReadState(model.ReadStateUnread)

	case key.Matches(msg, m.keys.Archive):
		return m.startArchive()

	case key.Matches(msg, m.keys.Refresh):
		return m, m.restartPolling()

	case key.Matches(msg, m.keys.Filter):
		m.filtering = true

	case key.Matches(msg, m.keys.Open):
		return m.openCursor()
	}

	return m, nil
}

// handleEscape resolves the overloaded key in a fixed order so the outcome is
// never ambiguous.
func (m Model) handleEscape() (tea.Model, tea.Cmd) {
	switch {
	case m.helpOpen:
		m.helpOpen = false
	case m.filter != "":
		m.filter = ""
		m.clampCursor()
	case m.archive != nil && !m.archive.finished:
		m.archive.cancel()
	}
	return m, nil
}

func (m Model) handleFilterKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "enter":
		m.filtering = false
	case "esc":
		m.filtering = false
		m.filter = ""
	case "backspace":
		if r := []rune(m.filter); len(r) > 0 {
			m.filter = string(r[:len(r)-1])
		}
	default:
		if text := msg.Key().Text; text != "" {
			m.filter += text
		}
	}
	m.clampCursor()
	return m, nil
}

func (m Model) resolvePending(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	prefix := m.pending
	m.pending = ""

	switch {
	case prefix == prefixGoto && msg.String() == "g":
		m.cursor = 0
		m.clampCursor()
		return m, nil
	case prefix == prefixSelect && msg.String() == "a":
		for _, n := range m.visible() {
			m.selected[n.ID] = struct{}{}
		}
		return m, nil
	case prefix == prefixSelect && msg.String() == "n":
		m.selected = make(map[types.ThreadID]struct{})
		return m, nil
	}

	// An unrelated key cancels the prefix and is handled normally.
	return m.handleKey(msg)
}

func (m *Model) setTab(tab types.Tab) {
	m.tab = tab
	m.cursor, m.offset = 0, 0
}

func (m Model) setReadState(state model.ReadState) (tea.Model, tea.Cmd) {
	targets := m.targets()
	if len(targets) == 0 {
		return m, nil
	}

	// No network call is involved, so this completes before the next redraw
	// rather than going through a command.
	if err := m.uc.SetReadState(state, targets); err != nil {
		m.status = messageOf(err)
	} else {
		m.status = model.UserMessage{}
	}

	m.selected = make(map[types.ThreadID]struct{})
	m.clampCursor()
	return m, nil
}

func (m Model) startArchive() (tea.Model, tea.Cmd) {
	if m.archive != nil && !m.archive.finished {
		m.status = model.UserMessage{Summary: "already archiving", Action: "esc to stop"}
		return m, nil
	}

	targets := m.targets()
	if len(targets) == 0 {
		return m, nil
	}

	ids := make([]types.ThreadID, 0, len(targets))
	for _, n := range targets {
		ids = append(ids, n.ID)
	}

	ctx, cancel := context.WithCancel(m.ctx)
	ch := m.uc.Archive(ctx, ids)

	// The job is recorded here rather than on a message from the returned
	// command: a second key press arriving before that message would otherwise
	// pass the guard above, start a second job, and overwrite the first one's
	// cancel so that esc could no longer stop it.
	m.archive = &archiveState{ch: ch, cancel: cancel, total: len(ids)}
	m.selected = make(map[types.ThreadID]struct{})

	return m, waitArchive(ch)
}

func (m Model) openCursor() (tea.Model, tea.Cmd) {
	rows := m.visible()
	if m.cursor >= len(rows) {
		return m, nil
	}
	url := rows[m.cursor].WebURL(m.cfg.WebBase)
	open := m.cfg.OpenURL

	return m, func() tea.Msg {
		if open == nil {
			return openResultMsg{}
		}
		return openResultMsg{err: open(m.ctx, url)}
	}
}
