package logx

import (
	"context"
	"fmt"
	"time"
)

// Stage 3 (≈ 15 min): failure isolation and a shutdown that cannot hang.

// FailurePolicy says what an AsyncSink does when WriteBatch fails or panics.
type FailurePolicy struct {
	// MaxRetries is the number of retries after the first attempt (0 = none).
	MaxRetries int
	// Backoff is the delay before the first retry; it doubles for each
	// further retry. Zero means retry immediately.
	Backoff time.Duration
	// Sleep waits for d or until ctx is done (returning ctx.Err()). nil means a
	// real, cancellable sleep. Tests inject a fake. The ctx you pass must be
	// cancelled when Close gives up, so a worker never sleeps through a shutdown.
	Sleep func(ctx context.Context, d time.Duration) error
	// OnError is called from the worker goroutine for every failed attempt
	// and once more (Final) for each batch that is given up. A panic in
	// OnError must be recovered and ignored. nil ignores errors (they are
	// still counted in Stats).
	OnError func(ErrorEvent)
}

// ErrorEvent describes one failed WriteBatch attempt.
type ErrorEvent struct {
	Err     error
	Entries int  // size of the batch
	Attempt int  // 1-based
	Final   bool // true if the batch is being given up (Failed += Entries); exactly one such event per lost batch
}

// PanicError is the error an AsyncSink reports when WriteBatch panics (given).
type PanicError struct{ Value any }

func (p *PanicError) Error() string { return fmt.Sprintf("logx: sink panicked: %v", p.Value) }

// UnflushedError is returned by Close when entries could not be delivered (given).
type UnflushedError struct {
	// Remaining is the number of accepted entries that had not been written,
	// dropped or failed when Close gave up (or, on a later call, the number
	// that were abandoned at the deadline).
	Remaining uint64
	// Err is the context error that ended the wait; nil on a later call.
	Err error
}

func (e *UnflushedError) Error() string {
	s := fmt.Sprintf("logx: %d entries not flushed", e.Remaining)
	if e.Err != nil {
		s += ": " + e.Err.Error()
	}
	return s
}

func (e *UnflushedError) Unwrap() error { return e.Err }

// NewAsyncWithFailure is NewAsync with a retry/error policy. Rules:
//
//   - A WriteBatch that returns an error or panics (recover it into a
//     *PanicError; the worker goroutine must survive) is retried up to
//     MaxRetries times with doubling backoff, then given up: Failed += batch
//     size, and the worker moves on to the next batch.
//   - Every attempt gets its own copy of the batch (the destination owns, and
//     may modify, the slice it receives).
//   - Close(ctx) whose ctx is done before the drain finishes must not hang: it
//     tells the worker to abandon the queue (counted as Dropped), cancels the
//     context passed to Sleep, and returns *UnflushedError{Remaining, Err: ctx.Err()}
//     where Remaining = accepted but not yet written, dropped or failed. The
//     worker exits as soon as the destination call it is in returns. A later
//     Close returns *UnflushedError{Remaining: <abandoned>} once the worker is done.
//   - Conservation: Received == Written + Dropped + Failed once quiescent.
func NewAsyncWithFailure(dst BatchSink, cfg AsyncConfig, fp FailurePolicy) (*AsyncSink, error) {
	// TODO(lab): make NewAsync call this with FailurePolicy{}.
	_, _, _ = dst, cfg, fp
	return nil, errNotImplemented
}
