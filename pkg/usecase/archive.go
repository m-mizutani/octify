package usecase

import (
	"context"
	"errors"

	"github.com/m-mizutani/octify/pkg/domain/types"
	"github.com/m-mizutani/octify/pkg/infra/gh"
	"github.com/m-mizutani/octify/pkg/utils/async"
)

// ArchiveEvent reports the outcome of one thread in an archive job.
type ArchiveEvent struct {
	Index int
	Total int
	ID    types.ThreadID
	// Err is nil on success. A thread that GitHub no longer knows about counts
	// as success: the goal was for it to be gone.
	Err error
	// Fatal marks a failure that stops the rest of the job.
	Fatal bool
}

// Archive marks the given threads as done on GitHub, one at a time.
//
// Requests are serialised and spaced by Config.ArchiveGap because GitHub asks
// for exactly that on mutating calls. The work runs in the background so the
// list stays usable while a long batch drains; the returned channel is closed
// when the job ends.
func (u *UseCase) Archive(ctx context.Context, ids []types.ThreadID) <-chan ArchiveEvent {
	events := make(chan ArchiveEvent)

	async.Go(ctx, func(ctx context.Context) {
		defer close(events)

		// One pointer for the whole job, so a concurrent sign-out cannot turn it
		// into nil between two threads.
		client := u.currentClient()
		if client == nil {
			select {
			case events <- ArchiveEvent{Total: len(ids), Err: notAuthenticated(), Fatal: true}:
			case <-ctx.Done():
			}
			return
		}

		var succeeded []types.ThreadID
		defer func() {
			// A record for an archived thread has nothing left to describe.
			if len(succeeded) > 0 {
				_ = u.reads.Remove(succeeded...)
			}
		}()

		for i, id := range ids {
			if i > 0 {
				u.sleep(ctx, u.cfg.ArchiveGap)
			}
			if ctx.Err() != nil {
				return
			}

			err := client.MarkThreadDone(ctx, id)

			// A request aborted because the user stopped the job is not a failure
			// to report; reporting it would show "1 failed" for an action they
			// deliberately cancelled.
			if ctx.Err() != nil {
				return
			}

			if errors.Is(err, gh.ErrNotFound) {
				err = nil
			}

			event := ArchiveEvent{Index: i, Total: len(ids), ID: id, Err: err}
			switch {
			case err == nil:
				succeeded = append(succeeded, id)
			case errors.Is(err, gh.ErrUnauthorized):
				u.forgetCredential(ctx)
				event.Fatal = true
			case isRateLimited(err):
				// Not fatal: wait out the window and carry on with the next one.
				u.sleep(ctx, retryAfterOf(err))
			}

			select {
			case events <- event:
			case <-ctx.Done():
				return
			}

			if event.Fatal {
				return
			}
		}
	})

	return events
}

func isRateLimited(err error) bool {
	var rate *gh.RateLimitError
	return errors.As(err, &rate)
}
