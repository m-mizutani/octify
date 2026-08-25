package usecase_test

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/m-mizutani/gt"
	"github.com/m-mizutani/octify/pkg/domain/model"
	"github.com/m-mizutani/octify/pkg/domain/types"
	"github.com/m-mizutani/octify/pkg/infra/gh"
	"github.com/m-mizutani/octify/pkg/usecase"
)

// archiveServer answers DELETE requests according to a per-thread status map.
func archiveServer(t *testing.T, status map[string]int, headers map[string]map[string]string) (http.HandlerFunc, *[]string) {
	t.Helper()
	var mu sync.Mutex
	seen := make([]string, 0)

	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		id := strings.TrimPrefix(r.URL.Path, "/notifications/threads/")

		mu.Lock()
		seen = append(seen, id)
		mu.Unlock()

		for k, v := range headers[id] {
			w.Header().Set(k, v)
		}
		code, ok := status[id]
		if !ok {
			code = http.StatusNoContent
		}
		w.WriteHeader(code)
	}, &seen
}

func drain(t *testing.T, ch <-chan usecase.ArchiveEvent) []usecase.ArchiveEvent {
	t.Helper()
	events := make([]usecase.ArchiveEvent, 0)
	for ev := range ch {
		events = append(events, ev)
	}
	return events
}

func ids(n int) []types.ThreadID {
	out := make([]types.ThreadID, 0, n)
	for i := 1; i <= n; i++ {
		out = append(out, types.ThreadID(string(rune('0'+i))))
	}
	return out
}

func TestArchiveAllSucceed(t *testing.T) {
	handler, seen := archiveServer(t, nil, nil)
	h := newHarness(t, handler)
	h.authenticate(t)

	// Records for the threads about to disappear.
	gt.NoError(t, h.uc.SetReadState(model.ReadStateRead, []model.Notification{
		sampleNotification("1", fixedNow, true),
		sampleNotification("2", fixedNow, true),
		sampleNotification("3", fixedNow, true),
		sampleNotification("4", fixedNow, true),
		sampleNotification("5", fixedNow, true),
	}))

	events := drain(t, h.uc.Archive(t.Context(), ids(5)))

	gt.A(t, events).Length(5)
	for i, ev := range events {
		gt.Nil(t, ev.Err)
		gt.False(t, ev.Fatal)
		gt.Equal(t, ev.Index, i)
		gt.Equal(t, ev.Total, 5)
	}
	gt.A(t, *seen).Length(5)

	// A record for an archived thread has nothing left to describe.
	for _, id := range ids(5) {
		_, ok := h.uc.ReadOverride(id)
		gt.False(t, ok)
	}
}

func TestArchiveIndividualFailureContinues(t *testing.T) {
	handler, seen := archiveServer(t, map[string]int{"3": http.StatusInternalServerError}, nil)
	h := newHarness(t, handler)
	h.authenticate(t)
	gt.NoError(t, h.uc.SetReadState(model.ReadStateRead, []model.Notification{
		sampleNotification("3", fixedNow, true),
	}))

	events := drain(t, h.uc.Archive(t.Context(), ids(5)))

	gt.A(t, events).Length(5)
	gt.Error(t, events[2].Err).Is(gh.ErrUnexpectedStatus)
	gt.False(t, events[2].Fatal)
	gt.Nil(t, events[3].Err)
	gt.Nil(t, events[4].Err)
	gt.A(t, *seen).Length(5)

	// The failed thread is still there, so its record must stay.
	_, ok := h.uc.ReadOverride("3")
	gt.True(t, ok)
}

func TestArchiveNotFoundCountsAsSuccess(t *testing.T) {
	handler, _ := archiveServer(t, map[string]int{"3": http.StatusNotFound}, nil)
	h := newHarness(t, handler)
	h.authenticate(t)
	gt.NoError(t, h.uc.SetReadState(model.ReadStateRead, []model.Notification{
		sampleNotification("3", fixedNow, true),
	}))

	events := drain(t, h.uc.Archive(t.Context(), ids(5)))

	gt.A(t, events).Length(5)
	// The thread is already gone, which is the outcome that was wanted.
	gt.Nil(t, events[2].Err)
	_, ok := h.uc.ReadOverride("3")
	gt.False(t, ok)
}

func TestArchiveUnauthorizedStopsTheJob(t *testing.T) {
	handler, seen := archiveServer(t, map[string]int{"3": http.StatusUnauthorized}, nil)
	h := newHarness(t, handler)
	h.authenticate(t)

	events := drain(t, h.uc.Archive(t.Context(), ids(5)))

	gt.A(t, events).Length(3)
	gt.Error(t, events[2].Err).Is(gh.ErrUnauthorized)
	gt.True(t, events[2].Fatal)
	// The 4th and 5th threads must not be attempted.
	gt.A(t, *seen).Length(3)
	gt.False(t, h.uc.Authenticated())
}

func TestArchiveRateLimitWaitsAndContinues(t *testing.T) {
	handler, seen := archiveServer(t,
		map[string]int{"3": http.StatusForbidden},
		map[string]map[string]string{"3": {"Retry-After": "2"}},
	)
	h := newHarness(t, handler)
	h.authenticate(t)

	events := drain(t, h.uc.Archive(t.Context(), ids(5)))

	gt.A(t, events).Length(5)
	gt.Error(t, events[2].Err)
	gt.False(t, events[2].Fatal)
	gt.A(t, *seen).Length(5)

	// The gap between requests plus the extra wait the rate limit asked for.
	gt.A(t, h.slept).Has(2 * time.Second)
}

func TestArchiveGapBetweenRequests(t *testing.T) {
	handler, _ := archiveServer(t, nil, nil)
	h := newHarness(t, handler, withConfig(func(c *usecase.Config) { c.ArchiveGap = 3 * time.Second }))
	h.authenticate(t)

	drain(t, h.uc.Archive(t.Context(), ids(4)))

	// Three gaps for four requests; the first one is not delayed.
	gt.A(t, h.slept).Equal([]time.Duration{3 * time.Second, 3 * time.Second, 3 * time.Second})
}

func TestArchiveNoGapWhenZero(t *testing.T) {
	handler, _ := archiveServer(t, nil, nil)
	h := newHarness(t, handler, withConfig(func(c *usecase.Config) { c.ArchiveGap = 0 }))
	h.authenticate(t)

	drain(t, h.uc.Archive(t.Context(), ids(3)))

	gt.A(t, h.slept).Equal([]time.Duration{0, 0})
}

func TestArchiveCancellation(t *testing.T) {
	var mu sync.Mutex
	seen := 0
	ctx, cancel := context.WithCancel(t.Context())

	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen++
		current := seen
		mu.Unlock()

		// Cancel while the second request is in flight.
		if current == 2 {
			cancel()
		}
		w.WriteHeader(http.StatusNoContent)
	})
	h.authenticate(t)

	events := drain(t, h.uc.Archive(ctx, ids(5)))

	// Whatever completed before the cancellation must not be lost.
	gt.N(t, len(events)).LessOrEqual(2)
	mu.Lock()
	gt.N(t, seen).LessOrEqual(2)
	mu.Unlock()
}

// Archiving runs in the background while the caller keeps drawing the list and
// polling. Both reach the read records and the GitHub client, so this drives
// all three at once; under -race it fails if either is left unguarded.
func TestArchiveRunsSafelyAlongsidePollingAndDrawing(t *testing.T) {
	handler, _ := archiveServer(t, map[string]int{"4": http.StatusUnauthorized}, nil)
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/notifications":
			_, _ = w.Write([]byte("[" + notificationJSON("9", fixedNow) + "]"))
		case "/search/issues":
			emptySearch(w)
		default:
			handler(w, r)
		}
	}, withConfig(func(c *usecase.Config) { c.ArchiveGap = 0 }))
	h.authenticate(t)

	gt.NoError(t, h.uc.SetReadState(model.ReadStateRead, []model.Notification{
		sampleNotification("1", fixedNow, true),
	}))

	var wg sync.WaitGroup

	wg.Add(1)
	go func() {
		defer wg.Done()
		drain(t, h.uc.Archive(t.Context(), ids(5)))
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		for range 20 {
			// The 401 on thread 4 clears the credential mid-run; polling must not
			// dereference a client that disappears underneath it.
			_, _ = h.uc.Poll(t.Context(), model.PollState{})
		}
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		for range 200 {
			h.uc.Unread(sampleNotification("1", fixedNow, true))
			_, _ = h.uc.ReadOverride("1")
			_ = h.uc.Authenticated()
		}
	}()

	wg.Wait()
}

func TestArchiveEmptyInput(t *testing.T) {
	handler, seen := archiveServer(t, nil, nil)
	h := newHarness(t, handler)
	h.authenticate(t)

	events := drain(t, h.uc.Archive(t.Context(), nil))

	gt.A(t, events).Length(0)
	gt.A(t, *seen).Length(0)
}

func TestArchiveWithoutCredential(t *testing.T) {
	handler, seen := archiveServer(t, nil, nil)
	h := newHarness(t, handler)

	events := drain(t, h.uc.Archive(t.Context(), ids(3)))

	gt.A(t, events).Length(1)
	gt.Error(t, events[0].Err).Is(usecase.ErrNotAuthenticated)
	gt.True(t, events[0].Fatal)
	gt.A(t, *seen).Length(0)
}
