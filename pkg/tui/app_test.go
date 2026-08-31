package tui_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/m-mizutani/goerr/v2"
	"github.com/m-mizutani/gt"
	"github.com/m-mizutani/octify/pkg/domain/model"
	"github.com/m-mizutani/octify/pkg/domain/types"
	"github.com/m-mizutani/octify/pkg/infra/gh"
	"github.com/m-mizutani/octify/pkg/infra/readstate"
	"github.com/m-mizutani/octify/pkg/infra/tokenstore"
	"github.com/m-mizutani/octify/pkg/tui"
	"github.com/m-mizutani/octify/pkg/usecase"
)

var fixedNow = time.Date(2026, 8, 25, 9, 0, 0, 0, time.UTC)

// --- key construction ---

func press(r rune) tea.KeyPressMsg {
	return tea.KeyPressMsg{Code: r, Text: string(r)}
}

func special(code rune) tea.KeyPressMsg {
	return tea.KeyPressMsg{Code: code}
}

func ctrl(r rune) tea.KeyPressMsg {
	return tea.KeyPressMsg{Code: r, Mod: tea.ModCtrl}
}

var (
	keyEsc       = special(tea.KeyEscape)
	keyTab       = special(tea.KeyTab)
	keyShiftTab  = tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift}
	keyEnter     = special(tea.KeyEnter)
	keyBackspace = special(tea.KeyBackspace)
)

// --- harness ---

type fakeTokenStore struct{ cred *model.Credential }

func (s *fakeTokenStore) Load(ctx context.Context) (*model.Credential, tokenstore.Backend, error) {
	if s.cred == nil {
		return nil, "", goerr.Wrap(tokenstore.ErrNotFound, "nothing saved")
	}
	return s.cred, tokenstore.BackendFile, nil
}
func (s *fakeTokenStore) Save(ctx context.Context, cred *model.Credential) (tokenstore.Backend, error) {
	s.cred = cred
	return tokenstore.BackendFile, nil
}
func (s *fakeTokenStore) Delete(ctx context.Context) error {
	s.cred = nil
	return nil
}

type harness struct {
	m       tui.Model
	uc      *usecase.UseCase
	opened  []string
	openErr error

	// announcing decides whether the model is given somewhere to send toasts,
	// which is what separates a run inside a herdr pane from every other run.
	announcing  bool
	announced   []toast
	announceErr error

	// reporting decides whether the model is given somewhere to report its
	// activity and unread count.
	reporting bool
	reports   []report
	reportErr error

	// fastArchive lets archive requests answer at once instead of hanging, so a
	// test may run the commands an archive produces rather than only feeding it
	// events by hand.
	fastArchive bool
}

type toast struct{ title, body string }

type report struct {
	seq      uint64
	activity tui.Activity
	unread   int
}

// withAnnounce builds the harness as a run inside a herdr pane.
func withAnnounce(h *harness) { h.announcing = true }

// withReport builds the harness as a run that can report to the workspace.
func withReport(h *harness) { h.reporting = true }

// withFastArchive lets archive requests finish instead of hanging.
func withFastArchive(h *harness) { h.fastArchive = true }

func newHarness(t *testing.T, opts ...func(*harness)) *harness {
	t.Helper()

	h := &harness{}
	for _, opt := range opts {
		opt(h)
	}

	// Archive requests hang until the context ends by default. That keeps a
	// started job in flight and silent, so a test can feed it the events it
	// wants to examine without the real goroutine racing them onto the same
	// channel.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete && !h.fastArchive {
			<-r.Context().Done()
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(srv.Close)

	reads := readstate.New(filepath.Join(t.TempDir(), "read-state.json"), "github.com")
	gt.NoError(t, reads.Load())

	// The use case starts signed in: these tests are about the list, and an
	// archive job would otherwise stop immediately with "not signed in".
	tokens := &fakeTokenStore{cred: &model.Credential{
		Version:     model.CredentialVersion,
		Host:        "github.com",
		AccessToken: "gho_test",
		TokenType:   "bearer",
	}}

	uc := usecase.New(tokens, reads, usecase.Config{
		ClientID:    "id",
		APIBase:     srv.URL,
		WebBase:     "https://github.com",
		MinInterval: time.Minute,
		MaxPages:    10,
		StateTTL:    time.Hour,
	}, usecase.WithHTTPClient(srv.Client()), usecase.WithClock(func() time.Time { return fixedNow }))

	_, _, restoreErr := uc.Restore(t.Context())
	gt.NoError(t, restoreErr)

	h.uc = uc

	cfg := tui.Config{
		WebBase: "https://github.com",
		Now:     func() time.Time { return fixedNow },
		OpenURL: func(ctx context.Context, url string) error {
			h.opened = append(h.opened, url)
			return h.openErr
		},
	}
	if h.announcing {
		cfg.Announce = func(ctx context.Context, title, body string) error {
			h.announced = append(h.announced, toast{title: title, body: body})
			return h.announceErr
		}
	}
	if h.reporting {
		cfg.Report = func(ctx context.Context, seq uint64, activity tui.Activity, unread int) error {
			h.reports = append(h.reports, report{seq: seq, activity: activity, unread: unread})
			return h.reportErr
		}
	}

	h.m = tui.NewModel(t.Context(), uc, cfg)
	return h
}

// send drives one message through Update and keeps the resulting model.
func (h *harness) send(t *testing.T, msg tea.Msg) tea.Cmd {
	t.Helper()
	next, cmd := h.m.Update(msg)
	h.m = next.(tui.Model)
	return cmd
}

func (h *harness) resize(t *testing.T, w, height int) {
	t.Helper()
	h.send(t, tea.WindowSizeMsg{Width: w, Height: height})
}

func notification(id types.ThreadID, subject types.SubjectType, repo string, number int, unread bool) model.Notification {
	return model.Notification{
		ID:   id,
		Repo: model.Repository{FullName: types.RepoFullName(repo), HTMLURL: "https://github.com/" + repo},
		Subject: model.Subject{
			Title:  "title of " + string(id),
			Type:   subject,
			URL:    "https://api.github.com/repos/" + repo + "/pulls/" + itoa(number),
			Number: number,
		},
		Reason:       model.ReasonComment,
		ServerUnread: unread,
		UpdatedAt:    fixedNow.Add(-time.Minute),
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	digits := ""
	for n > 0 {
		digits = string(rune('0'+n%10)) + digits
		n /= 10
	}
	return digits
}

// loadList puts the model into the ready state with the given notifications.
func (h *harness) loadList(t *testing.T, ns ...model.Notification) {
	t.Helper()
	h.resize(t, 100, 20)
	h.send(t, h.m.PollResultMsg(&usecase.PollResult{
		Notifications:  ns,
		ReviewRequests: model.ReviewRequests{},
		NextInterval:   time.Minute,
	}, nil))
	gt.Equal(t, h.m.Phase(), tui.PhaseReady)
}

func sampleList() []model.Notification {
	return []model.Notification{
		notification("1", types.SubjectPullRequest, "acme/tools", 1, true),
		notification("2", types.SubjectIssue, "acme/tools", 2, true),
		notification("3", types.SubjectCheckSuite, "acme/other", 3, true),
		notification("4", types.SubjectPullRequest, "acme/other", 4, true),
		notification("5", types.SubjectRelease, "acme/third", 5, true),
	}
}

// --- cursor ---

func TestCursorMovement(t *testing.T) {
	h := newHarness(t)
	h.loadList(t, sampleList()...)

	gt.Equal(t, h.m.Cursor(), 0)

	h.send(t, press('j'))
	gt.Equal(t, h.m.Cursor(), 1)

	h.send(t, press('k'))
	gt.Equal(t, h.m.Cursor(), 0)

	// The cursor must not run off either end.
	h.send(t, press('k'))
	gt.Equal(t, h.m.Cursor(), 0)

	for range 10 {
		h.send(t, press('j'))
	}
	gt.Equal(t, h.m.Cursor(), 4)
}

func TestCursorJumps(t *testing.T) {
	h := newHarness(t)
	h.loadList(t, sampleList()...)

	h.send(t, tea.KeyPressMsg{Code: 'G', Text: "G"})
	gt.Equal(t, h.m.Cursor(), 4)

	h.send(t, press('g'))
	gt.Equal(t, h.m.Pending(), "g")
	h.send(t, press('g'))
	gt.Equal(t, h.m.Cursor(), 0)
	gt.Equal(t, h.m.Pending(), "")
}

func TestPendingPrefixIsReleasedByAnUnrelatedKey(t *testing.T) {
	h := newHarness(t)
	h.loadList(t, sampleList()...)

	h.send(t, press('g'))
	gt.Equal(t, h.m.Pending(), "g")

	// The second key is not part of a g-sequence, so it acts on its own.
	h.send(t, press('j'))
	gt.Equal(t, h.m.Pending(), "")
	gt.Equal(t, h.m.Cursor(), 1)
}

func TestHalfPageMovement(t *testing.T) {
	h := newHarness(t)
	h.resize(t, 100, 8) // 6 list rows
	h.send(t, h.m.PollResultMsg(&usecase.PollResult{
		Notifications:  sampleList(),
		ReviewRequests: model.ReviewRequests{},
		NextInterval:   time.Minute,
	}, nil))

	h.send(t, ctrl('d'))
	gt.Equal(t, h.m.Cursor(), 3)
	h.send(t, ctrl('u'))
	gt.Equal(t, h.m.Cursor(), 0)
}

func TestScrollOffsetFollowsCursor(t *testing.T) {
	h := newHarness(t)
	h.resize(t, 100, 5) // 3 list rows
	h.send(t, h.m.PollResultMsg(&usecase.PollResult{
		Notifications:  sampleList(),
		ReviewRequests: model.ReviewRequests{},
		NextInterval:   time.Minute,
	}, nil))

	gt.Equal(t, h.m.Offset(), 0)

	// The offset only moves once the cursor would leave the window.
	h.send(t, press('j'))
	h.send(t, press('j'))
	gt.Equal(t, h.m.Offset(), 0)

	h.send(t, press('j'))
	gt.Equal(t, h.m.Cursor(), 3)
	gt.Equal(t, h.m.Offset(), 1)
}

// --- tabs ---

func TestTabCycling(t *testing.T) {
	h := newHarness(t)
	h.loadList(t, sampleList()...)

	gt.Equal(t, h.m.CurrentTab(), types.TabAll)
	for _, want := range []types.Tab{types.TabPullRequest, types.TabIssue, types.TabActions, types.TabOther, types.TabAll} {
		h.send(t, keyTab)
		gt.Equal(t, h.m.CurrentTab(), want)
	}

	h.send(t, keyShiftTab)
	gt.Equal(t, h.m.CurrentTab(), types.TabOther)
}

func TestTabByNumber(t *testing.T) {
	h := newHarness(t)
	h.loadList(t, sampleList()...)

	for i, want := range types.AllTabs {
		h.send(t, press(rune('1'+i)))
		gt.Equal(t, h.m.CurrentTab(), want)
	}
}

func TestTabCounts(t *testing.T) {
	h := newHarness(t)
	h.loadList(t, sampleList()...)

	counts := h.m.TabCounts()
	gt.Equal(t, counts[types.TabAll], 5)
	gt.Equal(t, counts[types.TabPullRequest], 2)
	gt.Equal(t, counts[types.TabIssue], 1)
	gt.Equal(t, counts[types.TabActions], 1)
	gt.Equal(t, counts[types.TabOther], 1)
}

func TestTabSwitchResetsCursor(t *testing.T) {
	h := newHarness(t)
	h.loadList(t, sampleList()...)

	h.send(t, press('j'))
	h.send(t, press('j'))
	gt.Equal(t, h.m.Cursor(), 2)

	h.send(t, keyTab)
	gt.Equal(t, h.m.Cursor(), 0)
	gt.Equal(t, h.m.Offset(), 0)
}

// --- selection ---

func TestSelectionToggle(t *testing.T) {
	h := newHarness(t)
	h.loadList(t, sampleList()...)

	h.send(t, press('x'))
	gt.A(t, h.m.SelectedIDs()).Equal([]types.ThreadID{"1"})

	h.send(t, press('x'))
	gt.A(t, h.m.SelectedIDs()).Length(0)
}

func TestSelectionSurvivesTabSwitch(t *testing.T) {
	h := newHarness(t)
	h.loadList(t, sampleList()...)

	h.send(t, press('x'))
	h.send(t, keyTab)
	gt.A(t, h.m.SelectedIDs()).Equal([]types.ThreadID{"1"})
}

func TestSelectAllRespectsTheCurrentView(t *testing.T) {
	h := newHarness(t)
	h.loadList(t, sampleList()...)

	h.send(t, press('2')) // PR tab
	h.send(t, press('*'))
	gt.Equal(t, h.m.Pending(), "*")
	h.send(t, press('a'))

	// Only the rows on screen may be selected.
	gt.A(t, h.m.SelectedIDs()).Equal([]types.ThreadID{"1", "4"})

	h.send(t, press('*'))
	h.send(t, press('n'))
	gt.A(t, h.m.SelectedIDs()).Length(0)
}

func TestSelectAllRespectsTheFilter(t *testing.T) {
	h := newHarness(t)
	h.loadList(t, sampleList()...)

	h.send(t, press('/'))
	for _, r := range "other" {
		h.send(t, press(r))
	}
	h.send(t, keyEnter)

	h.send(t, press('*'))
	h.send(t, press('a'))
	gt.A(t, h.m.SelectedIDs()).Equal([]types.ThreadID{"3", "4"})
}

// --- read state ---

func TestMarkReadAndUnread(t *testing.T) {
	h := newHarness(t)
	h.loadList(t, sampleList()...)

	h.send(t, press('x'))
	h.send(t, press('j'))
	h.send(t, press('x'))
	gt.A(t, h.m.SelectedIDs()).Length(2)

	cmd := h.send(t, tea.KeyPressMsg{Code: 'I', Text: "I"})
	// Read state never leaves the machine, so no command is produced.
	gt.True(t, cmd == nil)
	gt.A(t, h.m.SelectedIDs()).Length(0)

	ov, ok := h.uc.ReadOverride("1")
	gt.True(t, ok)
	gt.Equal(t, ov.State, model.ReadStateRead)

	// Unread only shows 3 of the 5 rows now.
	gt.A(t, h.m.VisibleIDs()).Equal([]types.ThreadID{"3", "4", "5"})
	gt.Equal(t, h.m.UnreadCount(), 3)
}

func TestMarkUnreadIsReversible(t *testing.T) {
	h := newHarness(t)
	h.loadList(t, sampleList()...)

	h.send(t, tea.KeyPressMsg{Code: 'I', Text: "I"})
	gt.A(t, h.m.VisibleIDs()).Equal([]types.ThreadID{"2", "3", "4", "5"})

	// Show everything so the row can be reached again.
	h.send(t, press('a'))
	gt.A(t, h.m.VisibleIDs()).Length(5)

	h.send(t, tea.KeyPressMsg{Code: 'U', Text: "U"})
	gt.Equal(t, h.m.UnreadCount(), 5)
}

func TestMarkReadFallsBackToTheCursorRow(t *testing.T) {
	h := newHarness(t)
	h.loadList(t, sampleList()...)

	h.send(t, press('j'))
	h.send(t, tea.KeyPressMsg{Code: 'I', Text: "I"})

	_, ok := h.uc.ReadOverride("2")
	gt.True(t, ok)
	_, ok = h.uc.ReadOverride("1")
	gt.False(t, ok)
}

func TestMarkReadOnAnEmptyListDoesNothing(t *testing.T) {
	h := newHarness(t)
	h.loadList(t)

	cmd := h.send(t, tea.KeyPressMsg{Code: 'I', Text: "I"})
	gt.True(t, cmd == nil)
	gt.Equal(t, h.m.Status().Summary, "")
}

func TestToggleShowReadDoesNotPoll(t *testing.T) {
	h := newHarness(t)
	h.loadList(t, sampleList()...)
	h.send(t, tea.KeyPressMsg{Code: 'I', Text: "I"})

	gt.A(t, h.m.VisibleIDs()).Length(4)

	// The toggle is a view filter, not a fetch condition.
	cmd := h.send(t, press('a'))
	gt.True(t, cmd == nil)
	gt.True(t, h.m.ShowRead())
	gt.A(t, h.m.VisibleIDs()).Length(5)
}

// --- filter ---

func TestFilterInput(t *testing.T) {
	h := newHarness(t)
	h.loadList(t, sampleList()...)

	h.send(t, press('/'))
	gt.True(t, h.m.Filtering())

	// While typing, command keys are literal text.
	for _, r := range "tools" {
		h.send(t, press(r))
	}
	gt.Equal(t, h.m.Filter(), "tools")
	gt.Equal(t, h.m.Cursor(), 0)
	gt.A(t, h.m.VisibleIDs()).Equal([]types.ThreadID{"1", "2"})

	h.send(t, keyBackspace)
	gt.Equal(t, h.m.Filter(), "tool")

	h.send(t, keyEnter)
	gt.False(t, h.m.Filtering())
	gt.Equal(t, h.m.Filter(), "tool")
}

func TestFilterSwallowsCommandKeys(t *testing.T) {
	h := newHarness(t)
	h.loadList(t, sampleList()...)

	h.send(t, press('/'))
	h.send(t, press('j'))
	h.send(t, press('q'))

	gt.Equal(t, h.m.Filter(), "jq")
	gt.Equal(t, h.m.Cursor(), 0)
}

func TestFilterMatchesTitleAndRepositoryCaseInsensitively(t *testing.T) {
	h := newHarness(t)
	h.loadList(t, sampleList()...)

	h.send(t, press('/'))
	for _, r := range "ACME/THIRD" {
		h.send(t, press(r))
	}
	gt.A(t, h.m.VisibleIDs()).Equal([]types.ThreadID{"5"})

	h.send(t, keyEsc)
	gt.Equal(t, h.m.Filter(), "")

	h.send(t, press('/'))
	for _, r := range "TITLE OF 3" {
		h.send(t, press(r))
	}
	gt.A(t, h.m.VisibleIDs()).Equal([]types.ThreadID{"3"})
}

// --- escape ordering ---

func TestEscapePriority(t *testing.T) {
	h := newHarness(t)
	h.loadList(t, sampleList()...)

	h.send(t, press('/'))
	for _, r := range "tools" {
		h.send(t, press(r))
	}
	h.send(t, keyEnter)
	h.send(t, press('?'))
	gt.True(t, h.m.HelpOpen())

	// Help first.
	h.send(t, keyEsc)
	gt.False(t, h.m.HelpOpen())
	gt.Equal(t, h.m.Filter(), "tools")

	// Then the filter.
	h.send(t, keyEsc)
	gt.Equal(t, h.m.Filter(), "")

	// Then nothing, without error.
	h.send(t, keyEsc)
	gt.Equal(t, h.m.Filter(), "")
	gt.False(t, h.m.HelpOpen())
}

func TestEscapeStopsArchiving(t *testing.T) {
	h := newHarness(t)
	h.loadList(t, sampleList()...)

	h.send(t, press('e'))
	gt.True(t, h.m.Archiving())
	ch := h.m.ArchiveChannel()

	h.send(t, keyEsc)

	// Cancelling aborts the request in flight, which ends the job and closes the
	// channel. Waiting on the close is what proves the cancel reached it.
	select {
	case _, ok := <-ch:
		gt.False(t, ok)
	case <-time.After(5 * time.Second):
		t.Fatal("archive did not stop after esc")
	}
}

// --- polling ---

func TestPollResultReplacesTheList(t *testing.T) {
	h := newHarness(t)
	h.loadList(t, sampleList()...)

	h.send(t, press('x'))
	h.send(t, press('j'))
	h.send(t, press('x'))
	gt.A(t, h.m.SelectedIDs()).Equal([]types.ThreadID{"1", "2"})

	// The next response no longer contains notification 1.
	h.send(t, h.m.PollResultMsg(&usecase.PollResult{
		Notifications: []model.Notification{
			notification("2", types.SubjectIssue, "acme/tools", 2, true),
			notification("6", types.SubjectIssue, "acme/tools", 6, true),
		},
		ReviewRequests: model.ReviewRequests{},
		NextInterval:   time.Minute,
	}, nil))

	gt.A(t, h.m.VisibleIDs()).Equal([]types.ThreadID{"2", "6"})
	// A selection for a row that is gone must not linger.
	gt.A(t, h.m.SelectedIDs()).Equal([]types.ThreadID{"2"})
	// The cursor follows the notification it was on.
	gt.Equal(t, h.m.Cursor(), 0)
}

func TestPollResultNotModifiedKeepsTheList(t *testing.T) {
	h := newHarness(t)
	h.loadList(t, sampleList()...)
	h.send(t, press('j'))

	h.send(t, h.m.PollResultMsg(&usecase.PollResult{
		NotModified:  true,
		NextInterval: time.Minute,
	}, nil))

	gt.A(t, h.m.VisibleIDs()).Length(5)
	gt.Equal(t, h.m.Cursor(), 1)
}

func TestPollFailureKeepsTheList(t *testing.T) {
	h := newHarness(t)
	h.loadList(t, sampleList()...)

	err := model.WithUserMessage(gh.ErrUnexpectedStatus,
		model.UserMessage{Summary: "GitHub returned 503"})
	h.send(t, h.m.PollResultMsg(&usecase.PollResult{NextInterval: 2 * time.Minute}, err))

	gt.Equal(t, h.m.Phase(), tui.PhaseReady)
	gt.A(t, h.m.VisibleIDs()).Length(5)
	gt.Equal(t, h.m.Status().Summary, "GitHub returned 503")
	gt.Equal(t, h.m.Status().Action, "retrying in 2m0s")
}

func TestPollUnauthorizedReturnsToTheAuthScreen(t *testing.T) {
	h := newHarness(t)
	h.loadList(t, sampleList()...)

	err := model.WithUserMessage(gh.ErrUnauthorized,
		model.UserMessage{Summary: "GitHub rejected the saved token", Action: "press o to sign in again"})
	h.send(t, h.m.PollResultMsg(&usecase.PollResult{NextInterval: time.Minute}, err))

	gt.Equal(t, h.m.Phase(), tui.PhaseUnauthenticated)
	gt.A(t, h.m.VisibleIDs()).Length(0)
	gt.Equal(t, h.m.Status().Summary, "GitHub rejected the saved token")
}

func TestPollStatusMessages(t *testing.T) {
	testCases := map[string]struct {
		res  usecase.PollResult
		want string
	}{
		"search failed": {
			res:  usecase.PollResult{ReviewErr: goerr.New("search failed")},
			want: "review status unavailable",
		},
		"state lookup failed": {
			res:  usecase.PollResult{StateErr: goerr.New("graphql failed")},
			want: "marker status unavailable",
		},
		"truncated": {
			res:  usecase.PollResult{Truncated: true},
			want: "showing the first 0 notifications",
		},
		"clean": {
			res:  usecase.PollResult{},
			want: "",
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			h.resize(t, 100, 20)
			res := tc.res
			res.NextInterval = time.Minute
			res.ReviewRequests = model.ReviewRequests{}
			h.send(t, h.m.PollResultMsg(&res, nil))
			gt.Equal(t, h.m.Status().Summary, tc.want)
		})
	}
}

// --- archive ---

func TestArchiveStartsWithTheSelection(t *testing.T) {
	h := newHarness(t)
	h.loadList(t, sampleList()...)

	h.send(t, press('x'))
	h.send(t, press('j'))
	h.send(t, press('x'))

	cmd := h.send(t, press('e'))
	gt.True(t, cmd != nil)
	// The selection is handed to the job and cleared, so later selections do not
	// affect a run already in flight.
	gt.A(t, h.m.SelectedIDs()).Length(0)

	// The job is recorded before the command runs, so a second key press in the
	// same frame cannot start another one.
	gt.True(t, h.m.Archiving())
}

func TestArchiveIgnoresASecondPressInTheSameFrame(t *testing.T) {
	h := newHarness(t)
	h.loadList(t, sampleList()...)

	h.send(t, press('e'))
	first := h.m.ArchiveChannel()

	// No message has been delivered in between; this is the key-repeat case.
	cmd := h.send(t, press('e'))
	gt.True(t, cmd == nil)
	gt.Equal(t, h.m.Status().Summary, "already archiving")

	// The first job's channel, and therefore its cancel, must still be the one
	// the model holds.
	gt.True(t, h.m.ArchiveChannel() == first)
}

func TestArchiveOnAnEmptyListDoesNothing(t *testing.T) {
	h := newHarness(t)
	h.loadList(t)

	cmd := h.send(t, press('e'))
	gt.True(t, cmd == nil)
	gt.False(t, h.m.Archiving())
}

func TestArchiveProgressRemovesRows(t *testing.T) {
	h := newHarness(t)
	h.loadList(t, sampleList()...)

	h.send(t, press('x'))
	h.send(t, press('j'))
	h.send(t, press('x'))
	h.send(t, press('e'))
	ch := h.m.ArchiveChannel()

	h.send(t, tui.ArchiveEventMsg(ch, usecase.ArchiveEvent{Index: 0, Total: 2, ID: "1"}, true))
	gt.A(t, h.m.VisibleIDs()).Equal([]types.ThreadID{"2", "3", "4", "5"})
	gt.Equal(t, h.m.Status().Summary, "archiving 1/2")

	failure := model.WithUserMessage(gh.ErrUnexpectedStatus, model.UserMessage{Summary: "GitHub returned 500"})
	h.send(t, tui.ArchiveEventMsg(ch, usecase.ArchiveEvent{Index: 1, Total: 2, ID: "2", Err: failure}, true))
	// A row that could not be archived stays.
	gt.A(t, h.m.VisibleIDs()).Equal([]types.ThreadID{"2", "3", "4", "5"})

	h.send(t, tui.ArchiveEventMsg(ch, usecase.ArchiveEvent{}, false))
	gt.False(t, h.m.Archiving())
	gt.Equal(t, h.m.Status().Summary, "1 archived · 1 failed")
}

func TestArchiveIgnoresEventsFromAReplacedJob(t *testing.T) {
	h := newHarness(t)
	h.loadList(t, sampleList()...)

	h.send(t, press('e'))
	ch := h.m.ArchiveChannel()

	// Finish the job so another can start.
	h.send(t, tui.ArchiveEventMsg(ch, usecase.ArchiveEvent{}, false))
	gt.False(t, h.m.Archiving())

	h.send(t, press('j'))
	h.send(t, press('e'))
	gt.True(t, h.m.ArchiveChannel() != ch)

	// A straggler from the finished job must not move the new job's counters or
	// remove a row it never touched.
	before := h.m.Status().Summary
	h.send(t, tui.ArchiveEventMsg(ch, usecase.ArchiveEvent{Index: 0, Total: 1, ID: "1"}, true))
	gt.Equal(t, h.m.Status().Summary, before)
	gt.A(t, h.m.VisibleIDs()).Length(5)
}

func TestArchiveFatalEventReturnsToTheAuthScreen(t *testing.T) {
	h := newHarness(t)
	h.loadList(t, sampleList()...)

	h.send(t, press('e'))
	ch := h.m.ArchiveChannel()

	err := model.WithUserMessage(gh.ErrUnauthorized, model.UserMessage{Summary: "GitHub rejected the saved token"})
	h.send(t, tui.ArchiveEventMsg(ch, usecase.ArchiveEvent{Index: 0, Total: 2, ID: "1", Err: err, Fatal: true}, true))

	gt.Equal(t, h.m.Phase(), tui.PhaseUnauthenticated)
	gt.Equal(t, h.m.Status().Summary, "GitHub rejected the saved token")

	// The job ends right after a fatal event. The completion summary must not
	// bury the reason the user is now looking at an empty screen.
	h.send(t, tui.ArchiveEventMsg(ch, usecase.ArchiveEvent{}, false))
	gt.False(t, h.m.Archiving())
	gt.Equal(t, h.m.Status().Summary, "GitHub rejected the saved token")
}

func TestArchiveIsNotStartedTwice(t *testing.T) {
	h := newHarness(t)
	h.loadList(t, sampleList()...)

	h.send(t, press('e'))

	cmd := h.send(t, press('e'))
	gt.True(t, cmd == nil)
	gt.Equal(t, h.m.Status().Summary, "already archiving")

	// Marking read costs nothing, so it stays available during a run.
	h.send(t, tea.KeyPressMsg{Code: 'I', Text: "I"})
	_, ok := h.uc.ReadOverride("1")
	gt.True(t, ok)
}

// --- authentication ---

func TestAuthScreenStart(t *testing.T) {
	h := newHarness(t)
	h.resize(t, 100, 20)
	h.send(t, tui.RestoreMsg(goerr.Wrap(tokenstore.ErrNotFound, "nothing saved")))
	gt.Equal(t, h.m.Phase(), tui.PhaseUnauthenticated)

	cmd := h.send(t, press('o'))
	gt.True(t, cmd != nil)
}

func TestAuthWaitingState(t *testing.T) {
	h := newHarness(t)
	h.resize(t, 100, 20)

	dc := &gh.DeviceCode{
		DeviceCode:      "dc",
		UserCode:        "ABCD-EFGH",
		VerificationURI: "https://github.com/login/device",
		ExpiresAt:       fixedNow.Add(15 * time.Minute),
		Interval:        5 * time.Second,
	}
	h.send(t, tui.DeviceCodeMsg(dc, nil))

	gt.Equal(t, h.m.Phase(), tui.PhaseAuthenticating)
	gt.S(t, h.m.Render()).Contains("ABCD-EFGH")
	gt.S(t, h.m.Render()).Contains("https://github.com/login/device")
}

func TestAuthOutcomes(t *testing.T) {
	testCases := map[string]struct {
		err         error
		wantPhase   tui.Phase
		wantSummary string
	}{
		"pending keeps waiting": {
			err:       goerr.Wrap(gh.ErrAuthorizationPending, "waiting"),
			wantPhase: tui.PhaseAuthenticating,
		},
		"slow down keeps waiting": {
			err:       goerr.Wrap(gh.ErrSlowDown, "slow down"),
			wantPhase: tui.PhaseAuthenticating,
		},
		"denied": {
			err: model.WithUserMessage(gh.ErrAccessDenied,
				model.UserMessage{Summary: "authorization was declined on GitHub"}),
			wantPhase:   tui.PhaseUnauthenticated,
			wantSummary: "authorization was declined on GitHub",
		},
		"expired": {
			err: model.WithUserMessage(gh.ErrExpiredToken,
				model.UserMessage{Summary: "the device code expired"}),
			wantPhase:   tui.PhaseUnauthenticated,
			wantSummary: "the device code expired",
		},
		"device flow disabled": {
			err: model.WithUserMessage(gh.ErrDeviceFlowDisabled,
				model.UserMessage{Summary: "device flow is turned off for this OAuth app"}),
			wantPhase:   tui.PhaseUnauthenticated,
			wantSummary: "device flow is turned off for this OAuth app",
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			h.resize(t, 100, 20)
			h.send(t, tui.DeviceCodeMsg(&gh.DeviceCode{
				UserCode:  "UC",
				ExpiresAt: fixedNow.Add(time.Minute),
				Interval:  5 * time.Second,
			}, nil))

			h.send(t, tui.AuthResultMsg(tc.err))
			gt.Equal(t, h.m.Phase(), tc.wantPhase)
			if tc.wantSummary != "" {
				gt.Equal(t, h.m.Status().Summary, tc.wantSummary)
			}
		})
	}
}

func TestAuthExpiryIsDetectedLocally(t *testing.T) {
	h := newHarness(t)
	h.resize(t, 100, 20)
	h.send(t, tui.DeviceCodeMsg(&gh.DeviceCode{
		UserCode:  "UC",
		ExpiresAt: fixedNow.Add(-time.Second),
		Interval:  5 * time.Second,
	}, nil))

	h.send(t, tui.AuthTickMsg())
	gt.Equal(t, h.m.Phase(), tui.PhaseUnauthenticated)
	gt.Equal(t, h.m.Status().Summary, "the device code expired")
}

func TestAuthSuccessStartsPolling(t *testing.T) {
	h := newHarness(t)
	h.resize(t, 100, 20)
	h.send(t, tui.DeviceCodeMsg(&gh.DeviceCode{
		UserCode:  "UC",
		ExpiresAt: fixedNow.Add(time.Minute),
		Interval:  5 * time.Second,
	}, nil))

	cmd := h.send(t, tui.AuthResultMsg(nil))
	gt.Equal(t, h.m.Phase(), tui.PhaseLoading)
	gt.True(t, cmd != nil)
}

// --- browser ---

func TestOpenInBrowser(t *testing.T) {
	h := newHarness(t)
	h.loadList(t, sampleList()...)

	cmd := h.send(t, keyEnter)
	gt.True(t, cmd != nil)
	h.send(t, cmd())

	gt.A(t, h.opened).Equal([]string{"https://github.com/acme/tools/pull/1"})
	gt.Equal(t, h.m.Status().Summary, "")
}

func TestOpenInBrowserFailureIsShown(t *testing.T) {
	h := newHarness(t)
	h.loadList(t, sampleList()...)
	h.openErr = model.WithUserMessage(goerr.New("no launcher"),
		model.UserMessage{Summary: "could not open the browser", Action: "https://github.com/acme/tools/pull/1"})

	cmd := h.send(t, keyEnter)
	h.send(t, cmd())

	gt.Equal(t, h.m.Status().Summary, "could not open the browser")
	// The user needs the URL itself in order to copy it.
	gt.Equal(t, h.m.Status().Action, "https://github.com/acme/tools/pull/1")
}

// --- quitting ---

func TestQuitKeys(t *testing.T) {
	for _, msg := range []tea.KeyPressMsg{press('q'), ctrl('c')} {
		h := newHarness(t)
		h.loadList(t, sampleList()...)

		cmd := h.send(t, msg)
		gt.True(t, cmd != nil)
		gt.Equal(t, cmd(), tea.Quit())
	}
}

func TestQuitKeyIsTextWhileFiltering(t *testing.T) {
	h := newHarness(t)
	h.loadList(t, sampleList()...)

	h.send(t, press('/'))
	cmd := h.send(t, press('q'))
	gt.True(t, cmd == nil)
	gt.Equal(t, h.m.Filter(), "q")
}

// --- the saved list at start-up ---

// savedSnapshot is what a previous run left behind.
func savedSnapshot(ns ...model.Notification) *model.PollSnapshot {
	return &model.PollSnapshot{
		SavedAt:        fixedNow.Add(-time.Hour),
		Notifications:  ns,
		ReviewRequests: model.ReviewRequests{},
		SubjectStates:  model.SubjectStates{},
	}
}

func TestRestoreDrawsTheSavedList(t *testing.T) {
	h := newHarness(t)
	h.resize(t, 100, 20)

	h.send(t, tui.RestoreMsgWithSnapshot(savedSnapshot(sampleList()...)))

	// The rows are there before this run has polled anything.
	gt.Equal(t, h.m.VisibleIDs(), []types.ThreadID{"1", "2", "3", "4", "5"})
	gt.Equal(t, h.m.Phase(), tui.PhaseLoading)
	gt.True(t, h.m.ShowingCache())
	gt.True(t, h.m.Polling())
}

func TestRestoreWithoutASavedList(t *testing.T) {
	h := newHarness(t)
	h.resize(t, 100, 20)

	h.send(t, tui.RestoreMsgWithSnapshot(nil))

	gt.Equal(t, len(h.m.VisibleIDs()), 0)
	gt.Equal(t, h.m.Phase(), tui.PhaseLoading)
	gt.False(t, h.m.ShowingCache())
	gt.True(t, h.m.Polling())
}

func TestRestoreOfAnEmptySavedList(t *testing.T) {
	h := newHarness(t)
	h.resize(t, 100, 20)

	h.send(t, tui.RestoreMsgWithSnapshot(savedSnapshot()))

	// An empty saved list looks exactly like no saved list, so there is nothing
	// to tell the user about.
	gt.Equal(t, len(h.m.VisibleIDs()), 0)
	gt.False(t, h.m.ShowingCache())
}

func TestFailedRestoreIgnoresTheSavedList(t *testing.T) {
	h := newHarness(t)
	h.resize(t, 100, 20)

	h.send(t, tui.RestoreMsg(goerr.New("no credential")))

	gt.Equal(t, h.m.Phase(), tui.PhaseUnauthenticated)
	gt.Equal(t, len(h.m.VisibleIDs()), 0)
	gt.False(t, h.m.ShowingCache())
	gt.False(t, h.m.Polling())
	// A list nobody can act on is worse than the sign-in prompt.
	gt.S(t, h.m.Render()).Contains("Press o to sign in")
}

func TestFirstPollReplacesTheSavedList(t *testing.T) {
	h := newHarness(t)
	h.resize(t, 100, 20)
	h.send(t, tui.RestoreMsgWithSnapshot(savedSnapshot(sampleList()...)))

	h.send(t, h.m.PollResultMsg(&usecase.PollResult{
		Notifications: []model.Notification{
			notification("7", types.SubjectPullRequest, "acme/tools", 7, true),
		},
		ReviewRequests: model.ReviewRequests{},
		NextInterval:   time.Minute,
	}, nil))

	gt.Equal(t, h.m.VisibleIDs(), []types.ThreadID{"7"})
	gt.Equal(t, h.m.Phase(), tui.PhaseReady)
	gt.False(t, h.m.ShowingCache())
	gt.False(t, h.m.Polling())
}

func TestSavedListSurvivesAFailedFirstPoll(t *testing.T) {
	h := newHarness(t)
	h.resize(t, 100, 20)
	h.send(t, tui.RestoreMsgWithSnapshot(savedSnapshot(sampleList()...)))

	h.send(t, h.m.PollResultMsg(&usecase.PollResult{NextInterval: time.Minute},
		goerr.New("github is unreachable")))

	// What is on screen is still the saved list, so it must still say so.
	gt.Equal(t, h.m.VisibleIDs(), []types.ThreadID{"1", "2", "3", "4", "5"})
	gt.True(t, h.m.ShowingCache())
	gt.False(t, h.m.Polling())
	gt.Equal(t, h.m.Phase(), tui.PhaseReady)
}

func TestRejectedTokenDropsTheSavedList(t *testing.T) {
	h := newHarness(t)
	h.resize(t, 100, 20)
	h.send(t, tui.RestoreMsgWithSnapshot(savedSnapshot(sampleList()...)))

	h.send(t, h.m.PollResultMsg(&usecase.PollResult{NextInterval: time.Minute},
		goerr.Wrap(gh.ErrUnauthorized, "rejected")))

	gt.Equal(t, h.m.Phase(), tui.PhaseUnauthenticated)
	gt.Equal(t, len(h.m.VisibleIDs()), 0)
	gt.False(t, h.m.ShowingCache())
}

// --- polling in flight ---

func TestManualRefreshMarksPollingInFlight(t *testing.T) {
	h := newHarness(t)
	h.loadList(t, sampleList()...)
	gt.False(t, h.m.Polling())

	h.send(t, press('r'))
	gt.True(t, h.m.Polling())

	h.send(t, h.m.PollResultMsg(&usecase.PollResult{
		Notifications:  sampleList(),
		ReviewRequests: model.ReviewRequests{},
		NextInterval:   time.Minute,
	}, nil))
	gt.False(t, h.m.Polling())
}

func TestScheduledPollMarksPollingInFlight(t *testing.T) {
	h := newHarness(t)
	h.loadList(t, sampleList()...)

	h.send(t, h.m.PollTickMsg())
	gt.True(t, h.m.Polling())
}

func TestStaleResultLeavesTheRunningPollAlone(t *testing.T) {
	h := newHarness(t)
	h.loadList(t, sampleList()...)

	// The refresh starts a new chain; the result of the one it replaced must
	// not report that chain as finished.
	stale := h.m.PollResultMsg(&usecase.PollResult{NextInterval: time.Minute}, nil)
	h.send(t, press('r'))
	h.send(t, stale)

	gt.True(t, h.m.Polling())
}

// --- desktop notifications ---

// poll drives one polling result through the model. The scheduling interval is
// kept short so that the timer handlePollResult returns can be run in a test
// without waiting out a real polling cycle.
func (h *harness) poll(t *testing.T, ns ...model.Notification) tea.Cmd {
	t.Helper()
	return h.send(t, h.m.PollResultMsg(&usecase.PollResult{
		Notifications:  ns,
		ReviewRequests: model.ReviewRequests{},
		NextInterval:   time.Millisecond,
	}, nil))
}

// runCmd executes everything a polling result produced, following a batch into
// the commands it holds. What the model announced is then read from the
// harness rather than from the shape of the batch, which tea.Batch makes no
// promise about. The scheduling timer is one of the commands run here, which is
// why poll keeps the interval short.
func runCmd(t *testing.T, cmd tea.Cmd) {
	t.Helper()
	if cmd == nil {
		return
	}
	if batch, ok := cmd().(tea.BatchMsg); ok {
		for _, c := range batch {
			runCmd(t, c)
		}
	}
}

func arrival(id types.ThreadID, repo string, number int) model.Notification {
	return notification(id, types.SubjectPullRequest, repo, number, true)
}

func TestNoAnnouncementWithoutSomewhereToSendOne(t *testing.T) {
	h := newHarness(t)
	h.resize(t, 100, 20)

	h.poll(t, sampleList()...)
	cmd := h.poll(t, append(sampleList(), arrival("9", "acme/tools", 9))...)

	runCmd(t, cmd)
	gt.Equal(t, 0, len(h.announced))
}

func TestFirstPollOfASessionAnnouncesNothing(t *testing.T) {
	h := newHarness(t, withAnnounce)
	h.resize(t, 100, 20)

	gt.False(t, h.m.Baselined())
	cmd := h.poll(t, sampleList()...)

	runCmd(t, cmd)
	gt.True(t, h.m.Baselined())
}

func TestOneArrivalIsAnnouncedWithItsRepositoryAndTitle(t *testing.T) {
	h := newHarness(t, withAnnounce)
	h.resize(t, 100, 20)

	h.poll(t, sampleList()...)
	cmd := h.poll(t, append(sampleList(), arrival("9", "acme/tools", 9))...)

	runCmd(t, cmd)

	gt.Equal(t, 1, len(h.announced))
	gt.Equal(t, "octify · acme/tools", h.announced[0].title)
	gt.Equal(t, "title of 9", h.announced[0].body)
}

func TestSeveralArrivalsAreAnnouncedAsOneToast(t *testing.T) {
	h := newHarness(t, withAnnounce)
	h.resize(t, 100, 20)

	h.poll(t, sampleList()...)
	cmd := h.poll(t, append(sampleList(),
		arrival("7", "acme/tools", 7),
		arrival("8", "acme/other", 8),
		arrival("9", "acme/third", 9),
	)...)

	runCmd(t, cmd)

	gt.Equal(t, 1, len(h.announced))
	gt.Equal(t, "octify · 3 new notifications", h.announced[0].title)
	gt.Equal(t, "acme/tools: title of 7 (+2 more)", h.announced[0].body)
}

func TestAPollWithNothingToAnnounceReturnsOnlyItsSchedule(t *testing.T) {
	h := newHarness(t, withAnnounce)
	h.resize(t, 100, 20)
	h.poll(t, sampleList()...)

	// Nothing new: the model must hand back the scheduling timer exactly as it
	// did before announcements existed, not a batch wrapping it.
	cmd := h.poll(t, sampleList()...)
	gt.True(t, cmd != nil).Required()
	if _, batched := cmd().(tea.BatchMsg); batched {
		t.Error("expected only the poll schedule, got a batch")
	}
}

func TestAnArrivalGitHubReportsAsReadIsNotAnnounced(t *testing.T) {
	h := newHarness(t, withAnnounce)
	h.resize(t, 100, 20)

	h.poll(t, sampleList()...)
	read := notification("9", types.SubjectPullRequest, "acme/tools", 9, false)
	cmd := h.poll(t, append(sampleList(), read)...)

	runCmd(t, cmd)
	gt.Equal(t, 0, len(h.announced))
}

func TestAnArrivalReadInsideOctifyIsNotAnnounced(t *testing.T) {
	h := newHarness(t, withAnnounce)
	h.resize(t, 100, 20)
	h.poll(t, sampleList()...)

	// GitHub still reports it unread. The record the user made inside octify is
	// what has to silence it.
	fresh := arrival("9", "acme/tools", 9)
	gt.NoError(t, h.uc.SetReadState(model.ReadStateRead, []model.Notification{fresh}))

	cmd := h.poll(t, append(sampleList(), fresh)...)
	runCmd(t, cmd)

	gt.Equal(t, 0, len(h.announced))
}

func TestANewCommentOnAThreadReadInsideOctifyIsAnnounced(t *testing.T) {
	h := newHarness(t, withAnnounce)
	h.resize(t, 100, 20)

	read := sampleList()[0]
	gt.NoError(t, h.uc.SetReadState(model.ReadStateRead, []model.Notification{read}))
	h.poll(t, sampleList()...)

	// The record was written against the old timestamp, so an update supersedes
	// it and the thread counts as unread again.
	updated := sampleList()
	updated[0].UpdatedAt = fixedNow
	cmd := h.poll(t, updated...)
	runCmd(t, cmd)

	gt.Equal(t, 1, len(h.announced))
	gt.Equal(t, "octify · acme/tools", h.announced[0].title)
	gt.Equal(t, "title of 1", h.announced[0].body)
}

func TestAnUpdatedThreadIsAnnouncedAgain(t *testing.T) {
	h := newHarness(t, withAnnounce)
	h.resize(t, 100, 20)

	h.poll(t, sampleList()...)

	// A new comment does not add a row; it moves the thread's timestamp.
	updated := sampleList()
	updated[0].UpdatedAt = fixedNow
	cmd := h.poll(t, updated...)

	runCmd(t, cmd)

	gt.Equal(t, 1, len(h.announced))
	gt.Equal(t, "octify · acme/tools", h.announced[0].title)
	gt.Equal(t, "title of 1", h.announced[0].body)
}

func TestANotModifiedPollAnnouncesNothing(t *testing.T) {
	h := newHarness(t, withAnnounce)
	h.resize(t, 100, 20)
	h.poll(t, sampleList()...)

	cmd := h.send(t, h.m.PollResultMsg(&usecase.PollResult{
		NotModified:  true,
		NextInterval: time.Millisecond,
	}, nil))

	runCmd(t, cmd)
	gt.Equal(t, 0, len(h.announced))
}

func TestAFailedPollAnnouncesNothingAndKeepsTheBaseline(t *testing.T) {
	h := newHarness(t, withAnnounce)
	h.resize(t, 100, 20)
	h.poll(t, sampleList()...)

	failure := model.WithUserMessage(goerr.New("boom"), model.UserMessage{Summary: "GitHub is unreachable"})
	cmd := h.send(t, h.m.PollResultMsg(&usecase.PollResult{NextInterval: time.Millisecond}, failure))

	runCmd(t, cmd)
	gt.True(t, h.m.Baselined())
	gt.Equal(t, 0, len(h.announced))
}

func TestAFailedAnnouncementLeavesTheStatusLineAlone(t *testing.T) {
	h := newHarness(t, withAnnounce)
	h.announceErr = goerr.New("no herdr server")
	h.resize(t, 100, 20)

	h.poll(t, sampleList()...)
	cmd := h.poll(t, append(sampleList(), arrival("9", "acme/tools", 9))...)

	before := h.m.Status()
	runCmd(t, cmd)

	gt.Equal(t, before, h.m.Status())
}

func TestARejectedTokenResetsTheBaseline(t *testing.T) {
	h := newHarness(t, withAnnounce)
	h.resize(t, 100, 20)
	h.poll(t, sampleList()...)

	rejected := model.WithUserMessage(gh.ErrUnauthorized, model.UserMessage{Summary: "GitHub rejected the saved token"})
	h.send(t, h.m.PollResultMsg(&usecase.PollResult{NextInterval: time.Millisecond}, rejected))
	gt.False(t, h.m.Baselined())

	// The first list after signing in again is a fresh basis, not an inbox to
	// announce.
	cmd := h.poll(t, sampleList()...)
	runCmd(t, cmd)
	gt.Equal(t, 0, len(h.announced))
}

func TestAFatalArchiveFailureResetsTheBaseline(t *testing.T) {
	h := newHarness(t, withAnnounce)
	h.resize(t, 100, 20)
	h.poll(t, sampleList()...)
	gt.True(t, h.m.Baselined())

	h.send(t, press('e'))
	ch := h.m.ArchiveChannel()

	err := model.WithUserMessage(gh.ErrUnauthorized, model.UserMessage{Summary: "GitHub rejected the saved token"})
	h.send(t, tui.ArchiveEventMsg(ch, usecase.ArchiveEvent{Index: 0, Total: 1, ID: "1", Err: err, Fatal: true}, true))

	gt.False(t, h.m.Baselined())
}

func TestAnnounceText(t *testing.T) {
	one := func(repo, subject string) model.Notification {
		return model.Notification{
			Repo:    model.Repository{FullName: types.RepoFullName(repo)},
			Subject: model.Subject{Title: subject},
		}
	}

	t.Run("a single arrival names its repository", func(t *testing.T) {
		title, body := tui.AnnounceText([]model.Notification{one("acme/tools", "Fix the parser")})
		gt.Equal(t, "octify · acme/tools", title)
		gt.Equal(t, "Fix the parser", body)
	})

	t.Run("a single arrival without a repository name still has a title", func(t *testing.T) {
		title, body := tui.AnnounceText([]model.Notification{one("", "Fix the parser")})
		gt.Equal(t, "octify", title)
		gt.Equal(t, "Fix the parser", body)
	})

	t.Run("several arrivals are counted", func(t *testing.T) {
		title, body := tui.AnnounceText([]model.Notification{
			one("acme/tools", "Fix the parser"),
			one("acme/other", "Bump the runtime"),
		})
		gt.Equal(t, "octify · 2 new notifications", title)
		gt.Equal(t, "acme/tools: Fix the parser (+1 more)", body)
	})

	t.Run("several arrivals where the first has no repository name", func(t *testing.T) {
		title, body := tui.AnnounceText([]model.Notification{
			one("", "Fix the parser"),
			one("acme/other", "Bump the runtime"),
		})
		gt.Equal(t, "octify · 2 new notifications", title)
		gt.Equal(t, "Fix the parser (+1 more)", body)
	})
}

// --- reporting to the workspace ---

// step drives one message and runs whatever it produced, so what the harness
// records is what a running program would have done.
func (h *harness) step(t *testing.T, msg tea.Msg) {
	t.Helper()
	runCmd(t, h.send(t, msg))
}

// ready puts the model into the ready state with the given list and clears the
// reports that getting there produced, so a test can look only at what follows.
func (h *harness) ready(t *testing.T, ns ...model.Notification) {
	t.Helper()
	h.step(t, tea.WindowSizeMsg{Width: 100, Height: 20})
	runCmd(t, h.poll(t, ns...))
	gt.Equal(t, h.m.Phase(), tui.PhaseReady)
	h.reports = nil
}

func TestNoReportWithoutSomewhereToSendOne(t *testing.T) {
	h := newHarness(t)
	h.ready(t, sampleList()...)

	h.step(t, press('I'))
	runCmd(t, h.poll(t, sampleList()[:2]...))

	gt.Equal(t, 0, len(h.reports))
}

func TestTheFirstMessageReportsWhatOctifyIsDoing(t *testing.T) {
	h := newHarness(t, withReport)

	// Nothing has been reported yet, so the very first message says so even
	// though there is nothing signed in and nothing unread.
	h.step(t, tea.WindowSizeMsg{Width: 100, Height: 20})

	gt.Equal(t, 1, len(h.reports))
	gt.Equal(t, tui.ActivitySignedOut, h.reports[0].activity)
	gt.Equal(t, 0, h.reports[0].unread)
	gt.Equal(t, uint64(1), h.reports[0].seq)
}

func TestTheSameStateIsNotReportedTwice(t *testing.T) {
	h := newHarness(t, withReport)
	h.ready(t, sampleList()...)

	// None of these change the phase or the unread count.
	h.step(t, press('j'))
	h.step(t, press('k'))
	h.step(t, keyTab)
	h.step(t, press('a'))
	runCmd(t, h.send(t, h.m.PollResultMsg(&usecase.PollResult{
		NotModified:  true,
		NextInterval: time.Millisecond,
	}, nil)))

	gt.Equal(t, 0, len(h.reports))
}

func TestReportFollowsTheUnreadCount(t *testing.T) {
	t.Run("marking one read", func(t *testing.T) {
		h := newHarness(t, withReport)
		h.ready(t, sampleList()...)

		h.step(t, press('I'))

		gt.Equal(t, 1, len(h.reports))
		gt.Equal(t, tui.ActivityReady, h.reports[0].activity)
		gt.Equal(t, 4, h.reports[0].unread)
	})

	t.Run("marking one unread again", func(t *testing.T) {
		h := newHarness(t, withReport)
		h.ready(t, sampleList()...)

		// Read rows are hidden by default, so without this the row just marked
		// read would leave the list and the next key would act on another one.
		h.step(t, press('a'))
		h.step(t, press('I'))
		h.step(t, tea.KeyPressMsg{Code: 'U', Text: "U"})

		gt.Equal(t, 2, len(h.reports))
		gt.Equal(t, 4, h.reports[0].unread)
		gt.Equal(t, 5, h.reports[1].unread)
	})

	t.Run("archiving an unread row", func(t *testing.T) {
		// The archive job is allowed to finish here, because the commands it
		// produces are run rather than only the events fed in by hand.
		h := newHarness(t, withReport, withFastArchive)
		h.ready(t, sampleList()...)

		h.step(t, press('e'))
		ch := h.m.ArchiveChannel()
		h.step(t, tui.ArchiveEventMsg(ch, usecase.ArchiveEvent{Index: 0, Total: 1, ID: "1"}, true))

		gt.True(t, len(h.reports) >= 1)
		gt.Equal(t, 4, h.reports[len(h.reports)-1].unread)
	})

	t.Run("a poll that brings more unread", func(t *testing.T) {
		h := newHarness(t, withReport)
		h.ready(t, sampleList()...)

		runCmd(t, h.poll(t, append(sampleList(), arrival("9", "acme/tools", 9))...))

		gt.Equal(t, 1, len(h.reports))
		gt.Equal(t, 6, h.reports[0].unread)
	})
}

func TestASavedListIsReportedAsSoonAsItIsDrawn(t *testing.T) {
	h := newHarness(t, withReport)
	h.step(t, tea.WindowSizeMsg{Width: 100, Height: 20})
	h.reports = nil

	h.step(t, tui.RestoreMsgWithSnapshot(&model.PollSnapshot{
		SavedAt:        fixedNow,
		Notifications:  sampleList(),
		ReviewRequests: model.ReviewRequests{},
	}))

	// Restoring moves the phase to loading and puts five unread rows on screen.
	gt.Equal(t, 1, len(h.reports))
	gt.Equal(t, tui.ActivityLoading, h.reports[0].activity)
	gt.Equal(t, 5, h.reports[0].unread)
}

func TestARejectedTokenIsReportedAsSignedOut(t *testing.T) {
	h := newHarness(t, withReport)
	h.ready(t, sampleList()...)

	rejected := model.WithUserMessage(gh.ErrUnauthorized, model.UserMessage{Summary: "GitHub rejected the saved token"})
	h.step(t, h.m.PollResultMsg(&usecase.PollResult{NextInterval: time.Millisecond}, rejected))

	gt.Equal(t, 1, len(h.reports))
	gt.Equal(t, tui.ActivitySignedOut, h.reports[0].activity)
	gt.Equal(t, 0, h.reports[0].unread)
}

func TestReportSequenceRisesByOne(t *testing.T) {
	h := newHarness(t, withReport)
	h.step(t, tea.WindowSizeMsg{Width: 100, Height: 20})
	runCmd(t, h.poll(t, sampleList()...))
	h.step(t, press('I'))

	gt.Equal(t, 3, len(h.reports))
	for i, r := range h.reports {
		gt.Equal(t, uint64(i+1), r.seq)
	}
}

func TestAFailedReportLeavesTheStatusLineAlone(t *testing.T) {
	h := newHarness(t, withReport)
	h.reportErr = goerr.New("no herdr server")
	h.ready(t, sampleList()...)

	before := h.m.Status()
	h.step(t, press('I'))

	gt.Equal(t, 1, len(h.reports))
	gt.Equal(t, before, h.m.Status())
}

func TestAReportAToastAndTheNextPollAllSurviveOneUpdate(t *testing.T) {
	h := newHarness(t, withAnnounce, withReport)
	h.ready(t, sampleList()...)

	// One poll that both brings something new and changes the count, so all
	// three commands come out of the same update.
	cmd := h.poll(t, append(sampleList(), arrival("9", "acme/tools", 9))...)
	gt.True(t, cmd != nil).Required()
	runCmd(t, cmd)

	gt.Equal(t, 1, len(h.announced))
	gt.Equal(t, 1, len(h.reports))
	gt.Equal(t, 6, h.reports[0].unread)
	gt.Equal(t, tui.PhaseReady, h.m.Phase())
}

func TestActivityFollowsThePhase(t *testing.T) {
	h := newHarness(t, withReport)
	gt.Equal(t, tui.ActivitySignedOut, h.m.CurrentActivity())

	h.step(t, tea.WindowSizeMsg{Width: 100, Height: 20})
	h.step(t, tui.DeviceCodeMsg(&gh.DeviceCode{
		UserCode:        "ABCD-EFGH",
		VerificationURI: "https://github.com/login/device",
		Interval:        time.Second,
		ExpiresAt:       fixedNow.Add(time.Minute),
	}, nil))
	gt.Equal(t, tui.ActivityAuthenticating, h.m.CurrentActivity())

	h.step(t, tui.RestoreMsg(nil))
	gt.Equal(t, tui.ActivityLoading, h.m.CurrentActivity())

	runCmd(t, h.poll(t, sampleList()...))
	gt.Equal(t, tui.ActivityReady, h.m.CurrentActivity())
}
