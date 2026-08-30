package tui

import (
	tea "charm.land/bubbletea/v2"
	"github.com/m-mizutani/octify/pkg/domain/model"
	"github.com/m-mizutani/octify/pkg/domain/types"
	"github.com/m-mizutani/octify/pkg/infra/gh"
	"github.com/m-mizutani/octify/pkg/usecase"
)

// Phase names the screen state so tests can assert transitions without
// depending on the unexported constants.
type Phase string

const (
	PhaseUnauthenticated Phase = "unauthenticated"
	PhaseAuthenticating  Phase = "authenticating"
	PhaseLoading         Phase = "loading"
	PhaseReady           Phase = "ready"
)

func (m Model) Phase() Phase {
	switch m.phase {
	case phaseUnauthenticated:
		return PhaseUnauthenticated
	case phaseAuthenticating:
		return PhaseAuthenticating
	case phaseLoading:
		return PhaseLoading
	default:
		return PhaseReady
	}
}

func (m Model) Polling() bool                { return m.polling }
func (m Model) ShowingCache() bool           { return m.showingCache }
func (m Model) Cursor() int                  { return m.cursor }
func (m Model) Offset() int                  { return m.offset }
func (m Model) CurrentTab() types.Tab        { return m.tab }
func (m Model) Filter() string               { return m.filter }
func (m Model) Filtering() bool              { return m.filtering }
func (m Model) ShowRead() bool               { return m.showRead }
func (m Model) HelpOpen() bool               { return m.helpOpen }
func (m Model) Pending() string              { return m.pending }
func (m Model) Status() model.UserMessage    { return m.status }
func (m Model) Render() string               { return m.render() }
func (m Model) TabCounts() map[types.Tab]int { return m.tabCounts() }
func (m Model) UnreadCount() int             { return m.unreadCount() }

func (m Model) SelectedIDs() []types.ThreadID {
	out := make([]types.ThreadID, 0, len(m.selected))
	for _, n := range m.all {
		if _, ok := m.selected[n.ID]; ok {
			out = append(out, n.ID)
		}
	}
	return out
}

func (m Model) VisibleIDs() []types.ThreadID {
	rows := m.visible()
	out := make([]types.ThreadID, 0, len(rows))
	for _, n := range rows {
		out = append(out, n.ID)
	}
	return out
}

func (m Model) Archiving() bool {
	return m.archive != nil && !m.archive.finished
}

// Message constructors used by tests to drive Update directly.

func RestoreMsg(err error) tea.Msg { return restoreMsg{err: err} }

// RestoreMsgWithSnapshot is what start-up produces when a previous run left a
// list behind.
func RestoreMsgWithSnapshot(snap *model.PollSnapshot) tea.Msg {
	return restoreMsg{snapshot: snap}
}

func DeviceCodeMsg(dc *gh.DeviceCode, err error) tea.Msg {
	return deviceCodeMsg{dc: dc, err: err}
}

func AuthTickMsg() tea.Msg { return authTickMsg{} }

func AuthResultMsg(err error) tea.Msg { return authResultMsg{err: err} }

// PollResultMsg targets the model's current poll chain, which is what a real
// response would do.
func (m Model) PollResultMsg(res *usecase.PollResult, err error) tea.Msg {
	return pollResultMsg{generation: m.pollGeneration, res: res, err: err}
}

func (m Model) PollTickMsg() tea.Msg {
	return pollTickMsg{generation: m.pollGeneration}
}

// StalePollTickMsg belongs to a chain that has already been replaced.
func (m Model) StalePollTickMsg() tea.Msg {
	return pollTickMsg{generation: m.pollGeneration - 1}
}

// StartArchive begins a job the way the archive key does, so tests can observe
// the state without reaching into the model.
func (m Model) StartArchive() (Model, tea.Cmd) {
	next, cmd := m.startArchive()
	return next.(Model), cmd
}

func (m Model) ArchiveChannel() <-chan usecase.ArchiveEvent {
	if m.archive == nil {
		return nil
	}
	return m.archive.ch
}

func ArchiveEventMsg(ch <-chan usecase.ArchiveEvent, ev usecase.ArchiveEvent, ok bool) tea.Msg {
	return archiveEventMsg{ch: ch, ev: ev, ok: ok}
}

func OpenResultMsg(err error) tea.Msg { return openResultMsg{err: err} }
