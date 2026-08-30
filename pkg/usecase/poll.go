package usecase

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/m-mizutani/octify/pkg/domain/model"
	"github.com/m-mizutani/octify/pkg/infra/gh"
	"github.com/m-mizutani/octify/pkg/infra/readstate"
)

// maxBackoffShift caps the exponential backoff at 8x the base interval.
const maxBackoffShift = 3

// refreshAfterNotModified is how many consecutive 304 answers may pass before a
// cycle asks unconditionally.
//
// The markers come from the subjects, not from the notification threads, so a
// pull request can be merged without its thread being touched — GitHub does not
// notify you of your own actions. The conditional request would then keep
// answering 304 and the row would keep its stale marker. Ten cycles is at most
// ten minutes at the default interval, and the unconditional cycle costs the
// same as any ordinary one.
const refreshAfterNotModified = 10

type PollResult struct {
	NotModified    bool
	Notifications  []model.Notification
	ReviewRequests model.ReviewRequests
	// ReviewErr is set when only the review search failed. The notification list
	// is still usable in that case.
	ReviewErr error
	// SubjectStates is what GitHub says about the issues and pull requests in
	// Notifications. It is empty when the lookup failed.
	SubjectStates model.SubjectStates
	// StateErr is set when only the state lookup failed. The notification list
	// is still usable in that case.
	StateErr error
	// Truncated is true when the page limit cut the list short.
	Truncated bool
	// Reconciled counts the read records dropped during this cycle.
	Reconciled   int
	ReconcileErr error
	// CacheErr is set when only the snapshot could not be saved. Nothing about
	// this cycle is lost by it; the next start just has nothing to draw.
	CacheErr     error
	NextInterval time.Duration
	NextState    model.PollState
}

// Poll runs one cycle: fetch, enrich with review requests, and tidy the read
// records.
//
// On failure the returned result is still non-nil and carries NextInterval and
// NextState, because the caller has to schedule the retry either way. The error
// is returned alongside and must not be ignored.
func (u *UseCase) Poll(ctx context.Context, st model.PollState) (*PollResult, error) {
	// Hold on to one pointer for the whole cycle: a sign-out from the archive
	// goroutine must not turn it into nil halfway through. The sequence number
	// is what keeps this cycle from saving over a newer one.
	client, seq := u.beginPoll()
	if client == nil {
		err := notAuthenticated()
		return u.failure(st, 0, err), err
	}

	// Dropping the conditional header is what makes the cycle unconditional: the
	// list comes back in full and everything downstream, the state lookup
	// included, runs as it would on any changed cycle.
	lastModified := st.LastModified
	if st.NotModifiedStreak >= refreshAfterNotModified {
		lastModified = ""
	}

	first, err := client.ListNotifications(ctx, gh.ListNotificationsInput{
		// Read notifications are always fetched: without them, anything read in
		// the web UI would vanish from octify and could not be marked unread.
		All:          true,
		LastModified: lastModified,
		PerPage:      gh.MaxPerPage,
		Page:         1,
	})
	if err != nil {
		u.handleAuthFailure(ctx, err)
		return u.failure(st, 0, err), err
	}

	if first.NotModified {
		return &PollResult{
			NotModified:  true,
			NextInterval: u.nextInterval(first.PollInterval, 0, 0),
			NextState: model.PollState{
				LastModified:      st.LastModified,
				NotModifiedStreak: st.NotModifiedStreak + 1,
			},
		}, nil
	}

	notifications := first.Notifications
	truncated := false
	fetched := 1
	for page := first.NextPage; page != 0; {
		if fetched >= u.cfg.MaxPages {
			truncated = true
			break
		}
		// Only the first page carries the conditional header; the rest are plain
		// page fetches of the same snapshot.
		next, err := client.ListNotifications(ctx, gh.ListNotificationsInput{
			All:     true,
			PerPage: gh.MaxPerPage,
			Page:    page,
		})
		if err != nil {
			u.handleAuthFailure(ctx, err)
			// The interval GitHub asked for on page 1 still applies to the retry.
			return u.failure(st, first.PollInterval, err), err
		}
		fetched++
		notifications = append(notifications, next.Notifications...)
		page = next.NextPage
	}

	result := &PollResult{
		Notifications: notifications,
		Truncated:     truncated,
		NextState:     model.PollState{LastModified: first.LastModified},
	}

	// A failed search must not cost the notification list; the review marker is
	// simply unavailable for this cycle.
	reviews, reviewErr := client.ListReviewRequestedPullRequests(ctx)
	if reviewErr != nil {
		u.handleAuthFailure(ctx, reviewErr)
		result.ReviewRequests = model.ReviewRequests{}
		result.ReviewErr = reviewErr
	} else {
		result.ReviewRequests = reviews
	}

	// The state lookup is treated like the search above: losing the markers for
	// one cycle is cheaper than losing the list.
	states, stateErr := client.ListSubjectStates(ctx, subjectRefs(notifications))
	if stateErr != nil {
		u.handleAuthFailure(ctx, stateErr)
		result.SubjectStates = model.SubjectStates{}
		result.StateErr = stateErr
	} else {
		result.SubjectStates = states
	}

	removed, reconcileErr := u.reads.Reconcile(notifications, readstate.ReconcileOption{
		// Dropping records for notifications that were merely not fetched would
		// silently mark them unread again.
		PruneMissing: !truncated,
		TTL:          u.cfg.StateTTL,
		Now:          u.now(),
	})
	result.Reconciled = removed
	result.ReconcileErr = reconcileErr
	result.CacheErr = u.saveSnapshot(seq, result)

	result.NextInterval = u.nextInterval(first.PollInterval, 0, 0)
	return result, nil
}

// Snapshot returns the list saved by the last cycle of a previous run, so the
// caller has something to draw before its own first poll answers.
//
// A missing or unusable file is not an error the user can act on, so it is
// reported as (nil, err) and the caller is expected to carry on without it.
func (u *UseCase) Snapshot() (*model.PollSnapshot, error) {
	if u.cache == nil {
		return nil, nil
	}
	return u.cache.Load()
}

// saveSnapshot records what this cycle put on screen, markers included.
//
// A cycle whose review search or state lookup failed is saved with those
// markers empty, exactly as it is drawn: restoring a fuller picture than the
// one the user last saw would make the next start disagree with the screen they
// left behind.
//
// Two cycles never fight over the file. The lock is the same one the credential
// is guarded by, so a cycle that ran while GitHub rejected the token finds no
// client and writes nothing, and a cycle overtaken by a newer one finds a
// higher savedSeq and leaves that newer list in place.
func (u *UseCase) saveSnapshot(seq uint64, res *PollResult) error {
	u.mu.Lock()
	defer u.mu.Unlock()

	if u.cache == nil || u.client == nil || seq < u.savedSeq {
		return nil
	}

	if err := u.cache.Save(&model.PollSnapshot{
		SavedAt:        u.now(),
		Notifications:  res.Notifications,
		ReviewRequests: res.ReviewRequests,
		SubjectStates:  res.SubjectStates,
	}); err != nil {
		return err
	}
	u.savedSeq = seq
	return nil
}

// subjectRefs collects the issues and pull requests the list points at, in the
// order they appear and without repeats. One subject can carry several
// notification threads, and each of them would otherwise cost a slot in the
// batched query.
func subjectRefs(notifications []model.Notification) []model.SubjectRef {
	seen := make(map[model.SubjectRef]struct{}, len(notifications))
	out := make([]model.SubjectRef, 0, len(notifications))

	for _, n := range notifications {
		ref, ok := n.SubjectRef()
		if !ok {
			continue
		}
		if _, dup := seen[ref]; dup {
			continue
		}
		seen[ref] = struct{}{}
		out = append(out, ref)
	}
	return out
}

// failure builds the scheduling half of a failed cycle and rewrites the display
// text so the user sees when the next attempt happens.
func (u *UseCase) failure(st model.PollState, serverInterval time.Duration, err error) *PollResult {
	failures := st.Failures + 1
	interval := u.nextInterval(serverInterval, failures, retryAfterOf(err))

	return &PollResult{
		NextInterval: interval,
		NextState: model.PollState{
			LastModified: st.LastModified,
			Failures:     failures,
			// Carried, not reset: a failed cycle answered nothing, so it must not
			// push the unconditional refresh further away.
			NotModifiedStreak: st.NotModifiedStreak,
		},
	}
}

// nextInterval honours GitHub's x-poll-interval as a floor, then applies the
// backoff and any explicit Retry-After.
func (u *UseCase) nextInterval(serverInterval time.Duration, failures int, retryAfter time.Duration) time.Duration {
	base := u.cfg.MinInterval
	if serverInterval > base {
		base = serverInterval
	}

	if failures > 0 {
		shift := min(failures, maxBackoffShift)
		base <<= shift
	}
	if retryAfter > base {
		base = retryAfter
	}
	return base
}

func retryAfterOf(err error) time.Duration {
	var rate *gh.RateLimitError
	if errors.As(err, &rate) {
		return rate.RetryAfter
	}
	return 0
}

// handleAuthFailure drops the credential when GitHub says it is no longer valid.
func (u *UseCase) handleAuthFailure(ctx context.Context, err error) {
	if errors.Is(err, gh.ErrUnauthorized) {
		u.forgetCredential(ctx)
	}
}

// DescribeRetry rewrites an error's display text to name the wait before the
// next attempt, which the raw error cannot know.
func DescribeRetry(err error, interval time.Duration) error {
	if err == nil {
		return nil
	}
	msg, ok := model.UserMessageOf(err)
	if !ok {
		return err
	}
	msg.Action = fmt.Sprintf("retrying in %s", interval.Round(time.Second))
	return model.WithUserMessage(err, msg)
}
