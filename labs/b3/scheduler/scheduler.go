// Package scheduler runs jobs at a point in time (delayed jobs) or on a
// cadence (recurring jobs, stage 2) on a bounded pool of workers, with
// cancellation.
//
// Lab B3 Problem 6 (90-minute box). Stage 1 is this file (delayed jobs,
// cancellation, worker pool); stage 2 (recurring jobs) lives in stage2.go and
// stage 3 (graceful shutdown, panics, stats) in stage3.go. Treat the later
// files as sealed until the interviewer "announces" them (see
// unit-3-messaging-and-scheduling.md §2).
//
// Stage 1 contract:
//   - ScheduleAt, ScheduleAfter, Cancel, Stop and the Stage 2/3 methods are
//     safe for concurrent use, including from inside a running job.
//   - There is ONE dispatcher goroutine and a FIXED pool of Workers
//     goroutines, however many jobs are pending. A scale test schedules
//     100,000 jobs and checks that the process did not grow 100,000
//     goroutines or timers: the dispatcher sleeps on a single clock timer
//     armed for the earliest pending job (timer duration = run time minus
//     Clock.Now()). Use a min-heap keyed by (run time, sequence).
//   - A job runs once its run time has been reached (Clock.Now() >= at); a
//     run time in the past runs as soon as a worker is free. Jobs run in
//     (run time, schedule order) order: two jobs due at the same instant
//     start in the order they were scheduled.
//   - Cancel reports whether the job was still pending or running. A pending
//     job never runs; a running job has its context cancelled. A second
//     Cancel of the same id reports false. Cancelling many far-future jobs
//     must not cost O(n) each (a test cancels 100,000).
//   - Scheduling a job never blocks on the worker pool, and a slow job never
//     stops the dispatcher from firing other due jobs: due jobs wait in a
//     FIFO ready queue until a worker is free. A job may call Schedule and
//     Cancel without deadlocking, even with Workers(1).
//   - Jobs get a context that is cancelled by Cancel and by Stop.
//   - Stop discards pending jobs, cancels running ones and returns once every
//     goroutine owned by the scheduler has exited (so a job that ignores its
//     context delays Stop). Do not call Stop from inside a job: it would wait
//     for itself. After Stop, scheduling returns ErrClosed. Stop is
//     idempotent.
//   - Time comes from the injected Clock so tests never sleep. The dispatcher
//     must create a NEW timer for each sleep and Stop it on wake-up (reusing
//     one with Reset invites the drain race); the fake clock never fires a
//     stopped timer.
package scheduler

import (
	"context"
	"errors"
	"time"
)

// Errors returned by the scheduler.
var (
	// ErrClosed is returned when scheduling on a stopped or shut-down scheduler.
	ErrClosed = errors.New("scheduler: closed")
	// ErrNilJob is returned when the job function is nil.
	ErrNilJob = errors.New("scheduler: nil job")
	// ErrInvalidWorkers is returned by New when Workers < 1.
	ErrInvalidWorkers = errors.New("scheduler: workers must be >= 1")
)

var errNotImplemented = errors.New("scheduler: not implemented")

// Clock is all the scheduler needs from time: the current instant and a
// one-shot timer. Defining it here (consumer side) is what lets tests drive
// time by hand.
type Clock interface {
	Now() time.Time
	NewTimer(d time.Duration) Timer
}

// Timer is a one-shot timer. C delivers one value when it fires; Stop
// prevents a pending fire and reports whether it did. A Timer is never reused.
type Timer interface {
	C() <-chan time.Time
	Stop() bool
}

// JobID identifies a scheduled job. IDs are never reused.
type JobID uint64

// Job is the work to run. Its context is cancelled when the job is cancelled
// or the scheduler stops; a well-behaved job returns promptly when that
// happens.
type Job func(ctx context.Context) error

// Option customises a Scheduler at construction time. Stage 3 adds WithOnError.
type Option func(*Scheduler)

// WithClock sets the clock. The default is the system clock (systemClock below).
func WithClock(c Clock) Option {
	return func(s *Scheduler) {
		// TODO(lab)
		_ = c
	}
}

// WithWorkers sets the number of worker goroutines (default 4). New rejects
// values below 1 with ErrInvalidWorkers.
func WithWorkers(n int) Option {
	return func(s *Scheduler) {
		// TODO(lab)
		_ = n
	}
}

// Scheduler runs jobs on a fixed worker pool. The zero value is not usable;
// call New.
type Scheduler struct {
	// TODO(lab): fields. Think about: ONE lock, a min-heap of tasks ordered by
	// (run time, sequence) where each task knows its heap index (so Cancel can
	// remove it), an id->task map, a FIFO ready queue between dispatcher and
	// workers that never blocks the dispatcher, a wake channel with capacity 1
	// so an insert at the head is never missed, and (later stages) the
	// recurrence policy, counters and the OnError hook.
}

// New starts a scheduler with one dispatcher goroutine and Workers worker
// goroutines. It returns ErrInvalidWorkers when Workers < 1. Options are
// applied in order.
func New(opts ...Option) (*Scheduler, error) {
	// TODO(lab)
	_ = opts
	return nil, errNotImplemented
}

// ScheduleAt runs job once, at the instant at (or as soon as possible if at
// has passed). It returns ErrNilJob for a nil job and ErrClosed after Stop.
func (s *Scheduler) ScheduleAt(at time.Time, job Job) (JobID, error) {
	// TODO(lab)
	_, _ = at, job
	return 0, errNotImplemented
}

// ScheduleAfter runs job once, d from now. A d <= 0 means "as soon as
// possible".
func (s *Scheduler) ScheduleAfter(d time.Duration, job Job) (JobID, error) {
	// TODO(lab)
	_, _ = d, job
	return 0, errNotImplemented
}

// Cancel stops the job. A pending job will never run; a running job has its
// context cancelled. It reports whether the job was still pending or running:
// false for unknown ids, finished jobs and jobs already cancelled.
func (s *Scheduler) Cancel(id JobID) bool {
	// TODO(lab)
	_ = id
	return false
}

// Stop discards every pending job, cancels the context of every running job
// and waits until all scheduler goroutines have exited. It is idempotent.
func (s *Scheduler) Stop() {
	// TODO(lab)
}

// ---- provided: production clock (not the point of the exercise) ------------

// systemClock is the production Clock; use it as the default in New.
type systemClock struct{}

func (systemClock) Now() time.Time { return time.Now() }

func (systemClock) NewTimer(d time.Duration) Timer { return realTimer{time.NewTimer(d)} }

type realTimer struct{ t *time.Timer }

func (r realTimer) C() <-chan time.Time { return r.t.C }
func (r realTimer) Stop() bool          { return r.t.Stop() }
