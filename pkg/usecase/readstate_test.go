package usecase_test

import (
	"net/http"
	"testing"
	"time"

	"github.com/m-mizutani/gt"
	"github.com/m-mizutani/octify/pkg/domain/model"
	"github.com/m-mizutani/octify/pkg/domain/types"
)

func sampleNotification(id types.ThreadID, updatedAt time.Time, serverUnread bool) model.Notification {
	return model.Notification{
		ID:           id,
		Repo:         model.Repository{FullName: "acme/tools"},
		Subject:      model.Subject{Title: "t", Type: "Issue"},
		ServerUnread: serverUnread,
		UpdatedAt:    updatedAt,
	}
}

func TestSetReadStateWritesRecords(t *testing.T) {
	requests := 0
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		requests++
	})

	ns := []model.Notification{
		sampleNotification("1", fixedNow, true),
		sampleNotification("2", fixedNow.Add(-time.Hour), true),
		sampleNotification("3", fixedNow.Add(-2*time.Hour), true),
	}

	gt.NoError(t, h.uc.SetReadState(model.ReadStateRead, ns))

	for _, n := range ns {
		ov, ok := h.uc.ReadOverride(n.ID)
		gt.True(t, ok)
		gt.Equal(t, ov.State, model.ReadStateRead)
		gt.Equal(t, ov.At, fixedNow)
		// The record has to remember which version of the notification it applies to.
		gt.Equal(t, ov.SubjectUpdatedAt, n.UpdatedAt)
	}

	// Read state is local: nothing may be sent to GitHub.
	gt.Equal(t, requests, 0)

}

func TestSetReadStateUnread(t *testing.T) {
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {})
	n := sampleNotification("1", fixedNow, false)

	gt.NoError(t, h.uc.SetReadState(model.ReadStateUnread, []model.Notification{n}))

	// A record saying unread overrides GitHub's read flag.
	gt.True(t, h.uc.Unread(n))
}

func TestSetReadStateIsReversible(t *testing.T) {
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {})
	n := sampleNotification("1", fixedNow, true)

	gt.NoError(t, h.uc.SetReadState(model.ReadStateRead, []model.Notification{n}))
	gt.False(t, h.uc.Unread(n))

	gt.NoError(t, h.uc.SetReadState(model.ReadStateUnread, []model.Notification{n}))
	gt.True(t, h.uc.Unread(n))
}

func TestSetReadStateEmptyInput(t *testing.T) {
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {})

	gt.NoError(t, h.uc.SetReadState(model.ReadStateRead, nil))

	_, ok := h.uc.ReadOverride("1")
	gt.False(t, ok)
}

func TestSetReadStateRejectsUnknownState(t *testing.T) {
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {})

	err := h.uc.SetReadState(model.ReadState("archived"),
		[]model.Notification{sampleNotification("1", fixedNow, true)})
	gt.Error(t, err).Is(model.ErrInvalidReadState)

	// A rejected state must leave no record behind.
	_, ok := h.uc.ReadOverride("1")
	gt.False(t, ok)
}

func TestUnreadFallsBackToServerFlag(t *testing.T) {
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {})

	// Without a record the server flag decides, which is what makes a first run
	// agree with the web UI.
	gt.True(t, h.uc.Unread(sampleNotification("1", fixedNow, true)))
	gt.False(t, h.uc.Unread(sampleNotification("2", fixedNow, false)))
}

func TestUnreadIgnoresSupersededRecord(t *testing.T) {
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {})
	n := sampleNotification("1", fixedNow, true)

	gt.NoError(t, h.uc.SetReadState(model.ReadStateRead, []model.Notification{n}))
	gt.False(t, h.uc.Unread(n))

	// A newer comment arrives: the record no longer applies.
	updated := sampleNotification("1", fixedNow.Add(time.Minute), true)
	gt.True(t, h.uc.Unread(updated))
}
