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
//     armed for the earliest pending job.
//   - A job runs once its run time has been reached (Clock.Now() >= at); a
//     run time in the past runs as soon as a worker is free. Jobs run in
//     (run time, schedule order) order: two jobs due at the same instant
//     start in the order they were scheduled.
//   - Cancel reports whether the job was still pending or running. A pending
//     job never runs; a running job has its context cancelled. A second
//     Cancel of the same id reports false.
//   - Scheduling a job never blocks on the worker pool, and a slow job never
//     stops the dispatcher from firing other due jobs: due jobs wait in a
//     FIFO ready queue until a worker is free.
//   - Jobs get a context that is cancelled by Cancel and by Stop.
//   - Stop discards pending jobs, cancels running ones and returns once every
//     goroutine owned by the scheduler has exited (so a job that ignores its
//     context delays Stop). Do not call Stop from inside a job: it would wait
//     for itself. After Stop, scheduling returns ErrClosed. Stop is
//     idempotent.
//   - Time comes from the injected Clock so tests never sleep.
//
// Reference design, in one paragraph: one mutex guards a min-heap of tasks
// ordered by (run time, sequence), an id->task map, a FIFO ready queue and the
// counters. The dispatcher pops due tasks into the ready queue and then sleeps
// on one timer (or on a wake channel when nothing is pending); workers wait on
// a sync.Cond for ready work. Critical sections are O(log n) and never call
// user code: jobs, the OnError hook and the clock's timers all run outside
// the lock (the clock is only asked for Now and NewTimer).
package scheduler

import (
	"container/heap"
	"context"
	"errors"
	"sync"
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

// Clock is all the scheduler needs from time: the current instant and a
// one-shot timer. Defining it here (consumer side) is what lets tests drive
// time by hand.
type Clock interface {
	Now() time.Time
	NewTimer(d time.Duration) Timer
}

// Timer is a one-shot timer. C delivers one value when it fires; Stop
// prevents a pending fire and reports whether it did. A Timer is never reused:
// the scheduler creates a new one for every sleep, which sidesteps the
// Reset/drain races of reusing a time.Timer.
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

// WithClock sets the clock. The default is the system clock.
func WithClock(c Clock) Option {
	return func(s *Scheduler) {
		if c != nil {
			s.clock = c
		}
	}
}

// WithWorkers sets the number of worker goroutines (default 4). New rejects
// values below 1.
func WithWorkers(n int) Option {
	return func(s *Scheduler) { s.workers = n }
}

const (
	defaultWorkers = 4
	// fireBatch bounds how many due tasks the dispatcher moves per lock hold,
	// so a burst of 100,000 due jobs cannot starve Schedule and Cancel.
	fireBatch = 512
)

// task is one scheduled job: a one-shot or a recurring series. All fields are
// guarded by Scheduler.mu.
type task struct {
	id  JobID
	seq uint64    // tie-break among tasks with the same run time
	at  time.Time // next run time while in the heap
	fn  Job
	idx int // position in the heap, -1 when not in it

	// stage 2
	recurring bool
	mode      Mode
	overlap   Overlap
	interval  time.Duration
	deferred  bool // QueueOverlap: one run is remembered for after the current one

	cancelled bool
	queued    int                      // invocations waiting in the ready queue
	running   map[*invocation]struct{} // invocations executing right now
}

// invocation is one execution of a task, from ready queue to completion.
type invocation struct {
	t      *task
	cancel context.CancelFunc
}

// Scheduler runs jobs on a fixed worker pool. The zero value is not usable;
// call New.
type Scheduler struct {
	clock   Clock
	workers int
	onError func(JobID, error) // stage 3

	mu         sync.Mutex // guards everything below
	cond       *sync.Cond // signalled when ready gets work or the scheduler closes
	heap       taskHeap
	tasks      map[JobID]*task // live tasks: pending, queued or running
	ready      []*invocation
	readyHead  int
	queuedLive int // invocations in ready that belong to live tasks
	nextID     JobID
	nextSeq    uint64
	closed     bool // no new jobs; dispatcher exits; workers exit when ready is empty
	aborted    bool // Stop: ready work is discarded
	alive      int  // goroutines still running (dispatcher + workers)
	running    int
	stats      Stats

	wake chan struct{} // cap 1: "the heap's earliest entry may have changed"
	quit chan struct{} // closed when the scheduler closes
	done chan struct{} // closed when every goroutine has exited
	base context.Context
	stop context.CancelFunc
}

// New starts a scheduler with one dispatcher goroutine and Workers worker
// goroutines. It returns ErrInvalidWorkers when Workers < 1. Options are
// applied in order.
func New(opts ...Option) (*Scheduler, error) {
	s := &Scheduler{
		clock:   systemClock{},
		workers: defaultWorkers,
		tasks:   make(map[JobID]*task),
		wake:    make(chan struct{}, 1),
		quit:    make(chan struct{}),
		done:    make(chan struct{}),
	}
	s.cond = sync.NewCond(&s.mu)
	for _, opt := range opts {
		opt(s)
	}
	if s.workers < 1 {
		return nil, ErrInvalidWorkers
	}
	s.base, s.stop = context.WithCancel(context.Background())
	s.alive = 1 + s.workers
	go s.dispatch()
	for i := 0; i < s.workers; i++ {
		go s.worker()
	}
	return s, nil
}

// ScheduleAt runs job once, at the instant at (or as soon as possible if at
// has passed).
func (s *Scheduler) ScheduleAt(at time.Time, job Job) (JobID, error) {
	if job == nil {
		return 0, ErrNilJob
	}
	return s.insert(&task{at: at, fn: job})
}

// ScheduleAfter runs job once, d from now. A d <= 0 means "as soon as
// possible".
func (s *Scheduler) ScheduleAfter(d time.Duration, job Job) (JobID, error) {
	if job == nil {
		return 0, ErrNilJob
	}
	if d < 0 {
		d = 0
	}
	return s.insert(&task{at: s.clock.Now().Add(d), fn: job})
}

// insert registers t. It is the only way tasks enter the heap from outside.
func (s *Scheduler) insert(t *task) (JobID, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return 0, ErrClosed
	}
	s.nextID++
	s.nextSeq++
	t.id, t.seq = s.nextID, s.nextSeq
	s.tasks[t.id] = t
	heap.Push(&s.heap, t)
	if t.idx == 0 { // new earliest entry: the dispatcher may be sleeping on a later one
		s.poke()
	}
	return t.id, nil
}

// Cancel stops the job. A pending job will never run; a running job has its
// context cancelled; a recurring job also never runs again. It reports
// whether the job was still pending or running: false for unknown ids,
// finished one-shot jobs and jobs already cancelled.
func (s *Scheduler) Cancel(id JobID) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.tasks[id]
	if !ok {
		return false
	}
	delete(s.tasks, id)
	t.cancelled = true
	if t.idx >= 0 {
		wasTop := t.idx == 0
		heap.Remove(&s.heap, t.idx) // O(log n): cancelled far-future jobs do not pile up
		if wasTop {
			s.poke()
		}
	}
	// Invocations still queued are dropped by the worker that pops them.
	s.queuedLive -= t.queued
	t.queued = 0
	t.deferred = false
	for inv := range t.running {
		inv.cancel()
	}
	s.stats.Canceled++
	return true
}

// Stop discards every pending job, cancels the context of every running job
// and waits until all scheduler goroutines have exited. It is idempotent and
// may be called after Shutdown to escalate. Do not call it from inside a job.
func (s *Scheduler) Stop() {
	s.mu.Lock()
	s.aborted = true
	s.closeLocked()
	for _, t := range s.heap {
		t.idx = -1
	}
	s.heap = nil
	s.ready, s.readyHead, s.queuedLive = nil, 0, 0
	clear(s.tasks)
	s.stop() // cancels the parent of every job context
	s.cond.Broadcast()
	s.mu.Unlock()
	<-s.done
}

// closeLocked marks the scheduler closed and releases the dispatcher.
func (s *Scheduler) closeLocked() {
	if !s.closed {
		s.closed = true
		close(s.quit)
	}
}

// poke wakes the dispatcher without blocking. The channel holds one token, so
// pokes coalesce and a poke sent while the dispatcher is busy is not lost.
func (s *Scheduler) poke() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// exit is deferred by every goroutine the scheduler starts.
func (s *Scheduler) exit() {
	s.mu.Lock()
	s.alive--
	last := s.alive == 0
	s.mu.Unlock()
	if last {
		close(s.done)
	}
}

// dispatch is the single timer goroutine: move everything that is due onto the
// ready queue, then sleep until the earliest remaining run time, a poke, or
// close.
func (s *Scheduler) dispatch() {
	defer s.exit()
	for {
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			return
		}
		now := s.clock.Now()
		more := s.fireDue(now)
		var wait time.Duration
		pending := len(s.heap) > 0
		if pending {
			wait = s.heap[0].at.Sub(now)
		}
		s.mu.Unlock()

		switch {
		case more:
			continue // batch limit hit: release the lock so others get in, then go on
		case !pending:
			select {
			case <-s.wake:
			case <-s.quit:
			}
		default:
			// wait > 0 here: fireDue popped everything with at <= now, so this
			// cannot spin even if the clock steps backwards.
			t := s.clock.NewTimer(wait)
			select {
			case <-t.C():
			case <-s.wake:
			case <-s.quit:
			}
			t.Stop()
		}
	}
}

// fireDue moves up to fireBatch due tasks from the heap to the ready queue and
// reports whether more are due. Requires s.mu.
func (s *Scheduler) fireDue(now time.Time) (more bool) {
	for n := 0; len(s.heap) > 0 && !s.heap[0].at.After(now); n++ {
		if n == fireBatch {
			return true
		}
		t := s.heap[0]
		if t.recurring && t.mode == FixedRate {
			s.rearmOnGrid(t, now) // before enqueue: the grid never depends on run time
		} else {
			heap.Pop(&s.heap)
		}
		s.enqueue(t)
	}
	return false
}

// enqueue appends one invocation of t to the ready queue, after applying the
// overlap policy. Requires s.mu.
func (s *Scheduler) enqueue(t *task) {
	if t.recurring && t.queued+len(t.running) > 0 {
		switch t.overlap {
		case SkipOverlap:
			s.stats.Skipped++
			return
		case QueueOverlap:
			if t.deferred {
				s.stats.Skipped++ // already one run remembered: coalesce
			}
			t.deferred = true
			return
		case AllowOverlap:
		}
	}
	s.pushReady(t)
}

// pushReady unconditionally queues one invocation of t. Requires s.mu.
func (s *Scheduler) pushReady(t *task) {
	s.ready = append(s.ready, &invocation{t: t})
	t.queued++
	s.queuedLive++
	s.cond.Signal()
}

// popReady removes the oldest ready invocation. Requires s.mu and a
// non-empty queue.
func (s *Scheduler) popReady() *invocation {
	inv := s.ready[s.readyHead]
	s.ready[s.readyHead] = nil
	s.readyHead++
	switch {
	case s.readyHead == len(s.ready):
		s.ready, s.readyHead = s.ready[:0], 0
	case s.readyHead >= 4096 && s.readyHead*2 >= len(s.ready):
		n := copy(s.ready, s.ready[s.readyHead:])
		clear(s.ready[n:])
		s.ready, s.readyHead = s.ready[:n], 0
	}
	return inv
}

// worker runs ready invocations until the scheduler is closed and the queue
// is empty.
func (s *Scheduler) worker() {
	defer s.exit()
	s.mu.Lock()
	for {
		for s.readyHead == len(s.ready) && !s.closed {
			s.cond.Wait()
		}
		if s.readyHead == len(s.ready) {
			s.mu.Unlock()
			return
		}
		inv := s.popReady()
		t := inv.t
		if t.cancelled || s.aborted {
			continue // cancelled while queued; Cancel already fixed the counters
		}
		t.queued--
		s.queuedLive--
		ctx, cancel := context.WithCancel(s.base)
		inv.cancel = cancel
		if t.running == nil {
			t.running = make(map[*invocation]struct{}, 1)
		}
		t.running[inv] = struct{}{}
		s.running++
		onError := s.onError
		s.mu.Unlock()

		err := s.invoke(ctx, t.fn) // user code: never under the lock
		cancelled := err != nil && ctx.Err() != nil && errors.Is(err, ctx.Err())
		if err != nil && !cancelled && onError != nil {
			s.report(onError, t.id, err)
		}

		s.mu.Lock()
		s.finish(inv, err, cancelled)
		cancel()
	}
}

// finish records the outcome of inv and, for recurring tasks, schedules what
// comes next. Requires s.mu.
func (s *Scheduler) finish(inv *invocation, err error, cancelled bool) {
	t := inv.t
	delete(t.running, inv)
	s.running--
	switch {
	case cancelled:
		// Not a failure: the job stopped because it was asked to.
	case err != nil:
		s.stats.Failed++
	default:
		s.stats.Completed++
	}
	if t.cancelled {
		return // Cancel already removed it and nothing else may be scheduled
	}
	if !t.recurring || s.closed {
		delete(s.tasks, t.id)
		return
	}
	if t.mode == FixedDelay {
		// The delay is measured from completion, so the next run is only
		// scheduled now. Between runs the task is in the heap; during a run it
		// is not, which is why a fixed-delay job can never overlap itself.
		t.at = s.clock.Now().Add(t.interval)
		s.nextSeq++
		t.seq = s.nextSeq
		heap.Push(&s.heap, t)
		if t.idx == 0 {
			s.poke()
		}
		return
	}
	if t.deferred {
		t.deferred = false
		s.pushReady(t)
	}
}

// taskHeap is a min-heap ordered by (run time, sequence). Each task knows its
// own index so Cancel can remove it in O(log n).
type taskHeap []*task

func (h taskHeap) Len() int { return len(h) }

func (h taskHeap) Less(i, j int) bool {
	a, b := h[i], h[j]
	if !a.at.Equal(b.at) {
		return a.at.Before(b.at)
	}
	return a.seq < b.seq
}

func (h taskHeap) Swap(i, j int) {
	h[i], h[j] = h[j], h[i]
	h[i].idx, h[j].idx = i, j
}

func (h *taskHeap) Push(x any) {
	t := x.(*task)
	t.idx = len(*h)
	*h = append(*h, t)
}

func (h *taskHeap) Pop() any {
	old := *h
	n := len(old)
	t := old[n-1]
	old[n-1] = nil
	*h = old[:n-1]
	t.idx = -1
	return t
}

// systemClock is the production Clock.
type systemClock struct{}

func (systemClock) Now() time.Time { return time.Now() }

func (systemClock) NewTimer(d time.Duration) Timer { return realTimer{time.NewTimer(d)} }

type realTimer struct{ t *time.Timer }

func (r realTimer) C() <-chan time.Time { return r.t.C }
func (r realTimer) Stop() bool          { return r.t.Stop() }
