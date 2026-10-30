package scheduler

import (
	"container/heap"
	"errors"
	"time"
)

// ---- Stage 2: recurring jobs (announced at about minute 35) ----------------
//
// Contract:
//   - ScheduleEvery runs job repeatedly, every interval, until Cancel or
//     shutdown. The first run is at Now()+interval unless WithInitialDelay
//     says otherwise (0 means "right away"). interval <= 0 is
//     ErrInvalidInterval: a zero interval would make the catch-up loop spin.
//   - FixedRate (the default): runs are due at first, first+interval,
//     first+2*interval, ... regardless of how long runs take. There is no
//     drift: the n-th run is due at exactly first+n*interval.
//   - FixedDelay: the next run is due `interval` after the previous run
//     COMPLETED, so runs never overlap and the cadence stretches by the run
//     time. The overlap policy does not apply.
//   - Overlap (FixedRate only: what to do with a tick that is due while the
//     previous run is still running or waiting for a worker):
//       SkipOverlap  (default) drop the tick.
//       QueueOverlap remember ONE tick and run it as soon as the previous run
//                    finishes; further ticks meanwhile are dropped. Backlog is
//                    bounded at one on purpose: an unbounded queue turns a job
//                    that is persistently slower than its interval into
//                    unbounded memory and ever-staler runs.
//       AllowOverlap run it concurrently (limited only by the worker pool).
//   - Missed ticks are never replayed in a burst. If the dispatcher finds a
//     FixedRate job more than one interval late (the process was paused, the
//     clock jumped forward), it runs once and the next run is the next
//     grid instant in the future.
//   - A run that returns an error or panics does not stop the series.
//   - Cancel of a recurring job stops it both between runs and during a run
//     (the running invocation's context is cancelled and nothing, including a
//     remembered QueueOverlap tick, runs afterwards).

// Errors specific to recurring jobs.
var (
	// ErrInvalidInterval is returned when the interval is <= 0.
	ErrInvalidInterval = errors.New("scheduler: interval must be > 0")
	// ErrInvalidOption is returned for an unknown Mode or Overlap value.
	ErrInvalidOption = errors.New("scheduler: invalid option value")
)

// Mode says what "every interval" means.
type Mode int

// Recurrence modes.
const (
	// FixedRate keeps a fixed grid of run times.
	FixedRate Mode = iota + 1
	// FixedDelay waits `interval` after each run completes.
	FixedDelay
)

// Overlap says what happens to a FixedRate tick that is due while the previous
// run is still active.
type Overlap int

// Overlap policies.
const (
	SkipOverlap Overlap = iota + 1
	QueueOverlap
	AllowOverlap
)

// RecurringOption customises ScheduleEvery.
type RecurringOption func(*recurSpec)

type recurSpec struct {
	mode         Mode
	overlap      Overlap
	initialDelay time.Duration
	hasDelay     bool
}

// WithMode selects FixedRate (default) or FixedDelay.
func WithMode(m Mode) RecurringOption { return func(r *recurSpec) { r.mode = m } }

// WithOverlap selects the overlap policy (default SkipOverlap).
func WithOverlap(o Overlap) RecurringOption { return func(r *recurSpec) { r.overlap = o } }

// WithInitialDelay sets the delay before the first run (default: interval).
func WithInitialDelay(d time.Duration) RecurringOption {
	return func(r *recurSpec) { r.initialDelay, r.hasDelay = d, true }
}

// ScheduleEvery runs job every interval. See the stage 2 contract above.
func (s *Scheduler) ScheduleEvery(interval time.Duration, job Job, opts ...RecurringOption) (JobID, error) {
	if job == nil {
		return 0, ErrNilJob
	}
	if interval <= 0 {
		return 0, ErrInvalidInterval
	}
	spec := recurSpec{mode: FixedRate, overlap: SkipOverlap, initialDelay: interval}
	for _, opt := range opts {
		opt(&spec)
	}
	if spec.mode != FixedRate && spec.mode != FixedDelay {
		return 0, ErrInvalidOption
	}
	if spec.overlap < SkipOverlap || spec.overlap > AllowOverlap {
		return 0, ErrInvalidOption
	}
	if spec.initialDelay < 0 {
		spec.initialDelay = 0
	}
	return s.insert(&task{
		at:        s.clock.Now().Add(spec.initialDelay),
		fn:        job,
		recurring: true,
		mode:      spec.mode,
		overlap:   spec.overlap,
		interval:  interval,
	})
}

// rearmOnGrid moves the FixedRate task t (currently the heap root) to its next
// grid instant strictly after now and fixes the heap. If the dispatcher is
// more than one interval late, the missed grid points are skipped, not
// replayed. Requires s.mu.
func (s *Scheduler) rearmOnGrid(t *task, now time.Time) {
	next := t.at.Add(t.interval)
	if !next.After(now) {
		missed := now.Sub(next)/t.interval + 1
		next = next.Add(missed * t.interval)
		s.stats.Skipped += uint64(missed)
	}
	t.at = next
	s.nextSeq++
	t.seq = s.nextSeq
	heap.Fix(&s.heap, t.idx)
}
