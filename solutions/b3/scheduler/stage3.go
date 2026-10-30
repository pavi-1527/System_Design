package scheduler

import (
	"container/heap"
	"context"
	"fmt"
	"runtime/debug"
)

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
//     (cancellation is not a failure). OnError runs on the worker goroutine
//     OUTSIDE the scheduler's lock, so it may call Schedule, Cancel and Stats.
//     A panic inside OnError is contained too.
//   - Stats is a consistent snapshot; see the Stats fields.

// WithOnError registers fn to be called for every failed run (error or
// panic). fn runs on a worker goroutine, concurrently with other jobs.
func WithOnError(fn func(id JobID, err error)) Option {
	return func(s *Scheduler) { s.onError = fn }
}

// PanicError is the error reported for a job that panicked.
type PanicError struct {
	Value any    // the value passed to panic
	Stack []byte // stack of the panicking goroutine
}

func (p *PanicError) Error() string { return fmt.Sprintf("scheduler: job panicked: %v", p.Value) }

// Unwrap lets errors.Is/As see through a panic(err).
func (p *PanicError) Unwrap() error {
	err, _ := p.Value.(error)
	return err
}

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
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.stats
	st.Pending = len(s.heap) + s.queuedLive
	st.Running = s.running
	return st
}

// Shutdown drains gracefully; see the stage 3 contract above.
func (s *Scheduler) Shutdown(ctx context.Context) error {
	s.mu.Lock()
	if !s.closed {
		s.closeLocked()
		now := s.clock.Now()
		for len(s.heap) > 0 && !s.heap[0].at.After(now) {
			t := heap.Pop(&s.heap).(*task)
			s.pushReady(t) // owed: due before Shutdown was called
		}
		for _, t := range s.heap { // the future is dropped
			t.idx = -1
			delete(s.tasks, t.id)
		}
		s.heap = nil
		s.cond.Broadcast()
	}
	s.mu.Unlock()

	select {
	case <-s.done:
		return nil
	case <-ctx.Done():
		s.stop() // cancel running jobs; their goroutines exit as they return
		return ctx.Err()
	}
}

// invoke runs fn and turns a panic into a *PanicError.
func (s *Scheduler) invoke(ctx context.Context, fn Job) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = &PanicError{Value: r, Stack: debug.Stack()}
		}
	}()
	return fn(ctx)
}

// report calls the OnError hook, containing a panic inside the hook itself.
func (s *Scheduler) report(fn func(JobID, error), id JobID, err error) {
	defer func() { _ = recover() }()
	fn(id, err)
}
