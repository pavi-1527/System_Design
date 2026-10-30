package scheduler

import "context"

// ---- Stage 3: lifecycle, panics and stats (announced at about minute 60) ----
//
// Contract:
//   - Shutdown(ctx) is the graceful exit. It stops accepting jobs at once
//     (ErrClosed), then lets the work that is OWED finish: running jobs, and
//     jobs that are already due (their run time has been reached when
//     Shutdown is called), including queued ones waiting for a worker. Jobs
//     due in the future are dropped, and recurring jobs get no further runs
//     (a recurring job that is due right now gets that one run). Running jobs
//     are NOT cancelled while the drain is within ctx's deadline. It returns
//     nil when everything has drained and every goroutine has exited. If ctx
//     ends first it cancels the context of every running job (as Stop does)
//     and returns ctx.Err() without waiting further, since a job that ignores
//     its context must not hold the caller hostage; its goroutine exits when
//     the job returns. Stop after Shutdown escalates; Shutdown after Stop
//     returns nil.
//   - A job that panics does not kill the process or its worker: the panic is
//     recovered, counted as Failed, and reported to the OnError hook as a
//     *PanicError carrying the value and the stack.
//   - A job that returns an error is counted as Failed and reported to the
//     OnError hook, except an error that is just the job's cancelled context
//     (errors.Is(err, ctx.Err()) after Cancel or Stop): cancellation is not a
//     failure. OnError runs on the worker goroutine OUTSIDE the scheduler's
//     lock, so it may call Schedule, Cancel and Stats (this is how a retry
//     with backoff is built). A panic inside OnError is contained too.
//   - Stats is a consistent snapshot; see the Stats fields.

// WithOnError registers fn to be called for every failed run (error or
// panic). fn runs on a worker goroutine, concurrently with other jobs.
func WithOnError(fn func(id JobID, err error)) Option {
	return func(s *Scheduler) {
		// TODO(lab)
		_ = fn
	}
}

// PanicError is the error reported for a job that panicked.
type PanicError struct {
	Value any    // the value passed to panic
	Stack []byte // stack of the panicking goroutine (runtime/debug.Stack)
}

func (p *PanicError) Error() string { return "scheduler: job panicked" }

// Unwrap lets errors.Is/As see through a panic(err). TODO(lab): return the
// panicked value when it is an error.
func (p *PanicError) Unwrap() error { return nil }

// Stats is a point-in-time snapshot.
type Stats struct {
	Pending   int    // occurrences waiting: in the heap or queued for a worker
	Running   int    // invocations executing right now
	Completed uint64 // runs that returned nil
	Failed    uint64 // runs that returned an error (other than cancellation) or panicked
	Skipped   uint64 // recurring ticks that never ran: overlap skips and ticks swallowed by a clock jump
	Canceled  uint64 // successful Cancel calls
}

// Stats returns a snapshot of the counters.
func (s *Scheduler) Stats() Stats {
	// TODO(lab)
	return Stats{}
}

// Shutdown drains gracefully; see the stage 3 contract above.
func (s *Scheduler) Shutdown(ctx context.Context) error {
	// TODO(lab)
	_ = ctx
	return errNotImplemented
}
