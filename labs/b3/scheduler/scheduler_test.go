package scheduler

import (
	"bytes"
	"context"
	"fmt"
	"reflect"
	"runtime"
	"runtime/pprof"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const testTimeout = 5 * time.Second

var epoch = time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

// ---- fake clock -------------------------------------------------------------

// fakeClock is a manually driven Clock. Timers fire only when the test moves
// time past their deadline, and they fire in deadline order. Safe for
// concurrent use.
type fakeClock struct {
	mu       sync.Mutex
	now      time.Time
	armed    map[*fakeTimer]struct{}
	created  int // timers ever created
	maxArmed int // most timers armed at the same moment
}

type fakeTimer struct {
	clk *fakeClock
	c   chan time.Time
	at  time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{now: epoch, armed: make(map[*fakeTimer]struct{})}
}

func (f *fakeClock) Now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.now
}

func (f *fakeClock) NewTimer(d time.Duration) Timer {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.created++
	tm := &fakeTimer{clk: f, c: make(chan time.Time, 1), at: f.now.Add(d)}
	if d <= 0 {
		tm.c <- f.now
		return tm
	}
	f.armed[tm] = struct{}{}
	f.maxArmed = max(f.maxArmed, len(f.armed))
	return tm
}

func (tm *fakeTimer) C() <-chan time.Time { return tm.c }

func (tm *fakeTimer) Stop() bool {
	tm.clk.mu.Lock()
	defer tm.clk.mu.Unlock()
	_, was := tm.clk.armed[tm]
	delete(tm.clk.armed, tm)
	return was
}

// setLocked moves time to `to` (forwards or backwards) and fires, in deadline
// order, every armed timer whose deadline has been reached.
func (f *fakeClock) setLocked(to time.Time) {
	f.now = to
	var due []*fakeTimer
	for tm := range f.armed {
		if !tm.at.After(to) {
			due = append(due, tm)
		}
	}
	slices.SortFunc(due, func(a, b *fakeTimer) int { return a.at.Compare(b.at) })
	for _, tm := range due {
		delete(f.armed, tm)
		tm.c <- tm.at // buffered(1) and fires at most once: never blocks
	}
}

// Advance jumps time forward by d and fires every timer that became due.
//
// Call it only when the dispatcher is parked: after WaitArmed for the instant
// it should be sleeping towards, or when nothing is pending. The dispatcher
// reads Now() and then creates its timer as two separate steps; moving time in
// between would leave it sleeping towards the wrong deadline. (A real clock has
// the same microsecond-wide gap; only a fake clock makes it observable.)
func (f *fakeClock) Advance(d time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.setLocked(f.now.Add(d))
}

// Set jumps time to t, which may be earlier than now (a backwards step).
func (f *fakeClock) Set(t time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.setLocked(t)
}

// StepTo waits until the scheduler's dispatcher is asleep on a timer that is
// due at or before `at`, moves time to exactly that timer's deadline and fires
// it; it repeats until now == at. Use it when `at` is the next instant at which
// something must happen: it is deterministic because time only moves once the
// dispatcher has really armed its timer. If the dispatcher never arms one (it
// is sleeping on a stale, later job, or lost a wake-up, or a job was never
// scheduled) the test fails after testTimeout instead of hanging.
//
// StepTo returns when the CLOCK has moved, not when the dispatcher has finished
// reacting to it. A test that must know the tick was processed (before it frees
// a worker, say) waits for the timer the dispatcher arms next: WaitArmed.
func (f *fakeClock) StepTo(t *testing.T, at time.Time) {
	t.Helper()
	deadline := time.Now().Add(testTimeout)
	for {
		f.mu.Lock()
		if !f.now.Before(at) {
			f.mu.Unlock()
			return
		}
		var earliest time.Time
		for tm := range f.armed {
			if earliest.IsZero() || tm.at.Before(earliest) {
				earliest = tm.at
			}
		}
		if !earliest.IsZero() && !earliest.After(at) {
			f.setLocked(earliest)
			f.mu.Unlock()
			continue
		}
		now := f.now
		f.mu.Unlock()
		if time.Now().After(deadline) {
			t.Fatalf("no timer armed for a deadline <= %v (clock is at %v) within %v: the dispatcher is not sleeping until the earliest pending job (stale timer after an earlier job was inserted? lost wake-up? blocked on a busy worker pool?)",
				at.Sub(epoch), now.Sub(epoch), testTimeout)
		}
		time.Sleep(50 * time.Microsecond)
	}
}

// WaitArmed blocks until some timer is armed for exactly `at`.
func (f *fakeClock) WaitArmed(t *testing.T, at time.Time) {
	t.Helper()
	waitFor(t, fmt.Sprintf("the dispatcher to arm a timer for +%v", at.Sub(epoch)), func() bool {
		f.mu.Lock()
		defer f.mu.Unlock()
		for tm := range f.armed {
			if tm.at.Equal(at) {
				return true
			}
		}
		return false
	})
}

func (f *fakeClock) timersCreated() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.created
}

func (f *fakeClock) maxArmedTimers() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.maxArmed
}

// ---- helpers ----------------------------------------------------------------

// waitFor polls cond until it holds or testTimeout passes.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(testTimeout)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out after %v waiting for %s", testTimeout, what)
		}
		time.Sleep(100 * time.Microsecond)
	}
}

// quiet gives a wrongly eager implementation a moment to do the thing the test
// says must NOT happen. It is the one real-time wait in these tests and it is
// tiny; the positive assertions never rely on it.
func quiet() { time.Sleep(15 * time.Millisecond) }

// within fails the test if f does not return within testTimeout, so a
// deadlock or an accidental O(n^2) fails instead of hanging the run.
func within(t *testing.T, what string, f func()) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		f()
	}()
	select {
	case <-done:
	case <-time.After(testTimeout):
		t.Fatalf("%s did not finish within %v (deadlock, or a per-operation cost that grows with the number of jobs)", what, testTimeout)
	}
}

func waitForGoroutines(t *testing.T, baseline int) {
	t.Helper()
	deadline := time.Now().Add(testTimeout)
	for {
		n := runtime.NumGoroutine()
		if n <= baseline {
			return
		}
		if time.Now().After(deadline) {
			var buf bytes.Buffer
			_ = pprof.Lookup("goroutine").WriteTo(&buf, 1)
			t.Fatalf("goroutine leak: %d goroutines still running, baseline was %d. Live stacks:\n%s", n, baseline, buf.String())
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func mustNew(t *testing.T, opts ...Option) *Scheduler {
	t.Helper()
	s, err := New(opts...)
	if err != nil {
		t.Fatalf("New returned error %v", err)
	}
	if s == nil {
		t.Fatal("New returned a nil *Scheduler with a nil error")
	}
	t.Cleanup(func() { stopWithin(t, s) })
	return s
}

func stopWithin(t *testing.T, s *Scheduler) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		s.Stop()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(testTimeout):
		t.Errorf("Stop did not return within %v: a goroutine owned by the scheduler is stuck (blocked on a lock or channel, or a job that ignores its context)", testTimeout)
	}
}

type runEntry struct {
	name string
	at   time.Time // fake-clock time at which the run started
}

// runLog records job starts in the order they happened.
type runLog struct {
	mu      sync.Mutex
	entries []runEntry
}

func (l *runLog) add(name string, at time.Time) {
	l.mu.Lock()
	l.entries = append(l.entries, runEntry{name, at})
	l.mu.Unlock()
}

func (l *runLog) snapshot() []runEntry {
	l.mu.Lock()
	defer l.mu.Unlock()
	return slices.Clone(l.entries)
}

func (l *runLog) names() []string {
	var out []string
	for _, e := range l.snapshot() {
		out = append(out, e.name)
	}
	return out
}

// harness bundles a scheduler on a fake clock with a run log.
type harness struct {
	t   *testing.T
	clk *fakeClock
	s   *Scheduler
	log *runLog
}

func newHarness(t *testing.T, opts ...Option) *harness {
	t.Helper()
	clk := newFakeClock()
	s := mustNew(t, append([]Option{WithClock(clk)}, opts...)...)
	return &harness{t: t, clk: clk, s: s, log: &runLog{}}
}

// job returns a Job that logs its name and the fake time it started at.
func (h *harness) job(name string) Job {
	return func(context.Context) error {
		h.log.add(name, h.clk.Now())
		return nil
	}
}

func (h *harness) after(d time.Duration, name string) JobID {
	h.t.Helper()
	id, err := h.s.ScheduleAfter(d, h.job(name))
	if err != nil {
		h.t.Fatalf("ScheduleAfter(%v) returned error %v", d, err)
	}
	return id
}

func (h *harness) at(at time.Time, name string) JobID {
	h.t.Helper()
	id, err := h.s.ScheduleAt(at, h.job(name))
	if err != nil {
		h.t.Fatalf("ScheduleAt(+%v) returned error %v", at.Sub(epoch), err)
	}
	return id
}

func (h *harness) waitRuns(n int) {
	h.t.Helper()
	waitFor(h.t, fmt.Sprintf("%d job runs (have %d: %v)", n, len(h.log.snapshot()), h.log.names()), func() bool {
		return len(h.log.snapshot()) >= n
	})
}

func (h *harness) wantNames(want ...string) {
	h.t.Helper()
	if got := h.log.names(); !reflect.DeepEqual(got, want) {
		h.t.Fatalf("jobs ran in order %v, want %v", got, want)
	}
}

// blocker is a job body that signals it has started and then holds a worker
// until the test opens the gate. It deliberately ignores its context.
type blocker struct {
	started atomic.Int32
	running atomic.Int32
	peak    atomic.Int32
	done    atomic.Int32
	release chan struct{}
	once    sync.Once
}

func newBlocker() *blocker { return &blocker{release: make(chan struct{})} }

func (b *blocker) job(ctx context.Context) error {
	n := b.running.Add(1)
	for {
		p := b.peak.Load()
		if n <= p || b.peak.CompareAndSwap(p, n) {
			break
		}
	}
	b.started.Add(1)
	<-b.release
	b.running.Add(-1)
	b.done.Add(1)
	return nil
}

func (b *blocker) open() { b.once.Do(func() { close(b.release) }) }

func (b *blocker) waitStarted(t *testing.T, n int) {
	t.Helper()
	waitFor(t, fmt.Sprintf("%d jobs to start (have %d)", n, b.started.Load()), func() bool { return int(b.started.Load()) >= n })
}

// ---- stage 1 ----------------------------------------------------------------

func TestStage1_ConstructorAndScheduleValidateArguments(t *testing.T) {
	for _, n := range []int{0, -3} {
		s, err := New(WithWorkers(n))
		if err != ErrInvalidWorkers {
			t.Errorf("New(WithWorkers(%d)) error = %v, want ErrInvalidWorkers", n, err)
		}
		if s != nil {
			s.Stop()
			t.Errorf("New(WithWorkers(%d)) returned a non-nil scheduler alongside an error", n)
		}
	}
	h := newHarness(t)
	if _, err := h.s.ScheduleAfter(time.Second, nil); err != ErrNilJob {
		t.Errorf("ScheduleAfter(nil job) error = %v, want ErrNilJob", err)
	}
	if _, err := h.s.ScheduleAt(epoch, nil); err != ErrNilJob {
		t.Errorf("ScheduleAt(nil job) error = %v, want ErrNilJob: a nil job would panic a worker later, far from the bug", err)
	}
}

func TestStage1_JobRunsAtItsTimeNotBefore(t *testing.T) {
	h := newHarness(t)
	h.after(5*time.Second, "a")
	h.clk.WaitArmed(t, epoch.Add(5*time.Second))
	h.clk.Advance(5*time.Second - time.Nanosecond)
	quiet()
	if got := h.log.names(); len(got) != 0 {
		t.Fatalf("job ran 1ns before its run time: %v", got)
	}
	h.clk.StepTo(t, epoch.Add(5*time.Second))
	h.waitRuns(1)
	if got := h.log.snapshot()[0].at; !got.Equal(epoch.Add(5 * time.Second)) {
		t.Fatalf("job started at +%v, want +5s", got.Sub(epoch))
	}
}

func TestStage1_RunsInRunTimeOrderNotInsertionOrder(t *testing.T) {
	h := newHarness(t, WithWorkers(1)) // one worker: start order == completion order
	h.after(3*time.Second, "c")
	h.after(time.Second, "a")
	h.at(epoch.Add(-time.Hour), "overdue") // run time already in the past
	h.after(2*time.Second, "b")
	h.after(-time.Minute, "negative-delay") // clamps to "now", scheduled after overdue
	h.clk.WaitArmed(t, epoch.Add(time.Second))
	h.clk.Advance(3 * time.Second)
	h.waitRuns(5)
	h.wantNames("overdue", "negative-delay", "a", "b", "c")
}

func TestStage1_SameInstantRunsInScheduleOrder(t *testing.T) {
	h := newHarness(t, WithWorkers(1))
	at := epoch.Add(time.Second)
	var want []string
	for i := 0; i < 30; i++ {
		name := fmt.Sprintf("job-%02d", i)
		h.at(at, name)
		want = append(want, name)
	}
	h.at(epoch.Add(500*time.Millisecond), "early") // inserted last, due first
	h.at(at, "last")                               // same instant as the 30, scheduled after them
	h.clk.StepTo(t, at)
	h.waitRuns(32)
	h.wantNames(append(append([]string{"early"}, want...), "last")...)
}

func TestStage1_InsertingAnEarlierJobWakesTheDispatcher(t *testing.T) {
	h := newHarness(t)
	h.after(time.Hour, "late")
	h.clk.WaitArmed(t, epoch.Add(time.Hour)) // the dispatcher is now asleep until +1h
	h.after(time.Second, "soon")
	// If the dispatcher keeps sleeping on the stale +1h timer, StepTo fails.
	h.clk.StepTo(t, epoch.Add(time.Second))
	h.waitRuns(1)
	quiet()
	h.wantNames("soon")
}

func TestStage1_CancelPendingJobNeverRuns(t *testing.T) {
	h := newHarness(t)
	a := h.after(time.Second, "a")
	b := h.after(2*time.Second, "b")
	if !h.s.Cancel(a) {
		t.Fatal("Cancel of a pending job = false, want true")
	}
	if h.s.Cancel(a) {
		t.Fatal("second Cancel of the same job = true, want false")
	}
	if h.s.Cancel(JobID(123456)) {
		t.Fatal("Cancel of an unknown id = true, want false")
	}
	// Cancelling the earliest job must not stop the later one from firing.
	h.clk.StepTo(t, epoch.Add(2*time.Second))
	h.waitRuns(1)
	quiet()
	h.wantNames("b")
	if h.s.Cancel(b) {
		t.Fatal("Cancel of a job that already finished = true, want false")
	}
}

func TestStage1_CancelRunningJobCancelsItsContext(t *testing.T) {
	h := newHarness(t)
	started := make(chan struct{})
	result := make(chan error, 1)
	id, err := h.s.ScheduleAfter(0, func(ctx context.Context) error {
		close(started)
		<-ctx.Done()
		result <- ctx.Err()
		return ctx.Err()
	})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(testTimeout):
		t.Fatal("job never started")
	}
	if !h.s.Cancel(id) {
		t.Fatal("Cancel of a running job = false, want true")
	}
	select {
	case err := <-result:
		if err != context.Canceled {
			t.Fatalf("job saw ctx.Err() = %v, want context.Canceled", err)
		}
	case <-time.After(testTimeout):
		t.Fatal("Cancel did not cancel the running job's context")
	}
	if h.s.Cancel(id) {
		t.Fatal("second Cancel of a running job = true, want false")
	}
}

func TestStage1_WorkerPoolBoundsConcurrency(t *testing.T) {
	const workers, jobs = 3, 10
	h := newHarness(t, WithWorkers(workers))
	b := newBlocker()
	defer b.open()
	for i := 0; i < jobs; i++ {
		if _, err := h.s.ScheduleAfter(0, b.job); err != nil {
			t.Fatal(err)
		}
	}
	b.waitStarted(t, workers)
	quiet()
	if n := b.running.Load(); n != workers {
		t.Fatalf("%d jobs running at once with Workers(%d) and %d due jobs, want exactly %d", n, workers, jobs, workers)
	}
	b.open()
	waitFor(t, "all jobs to finish", func() bool { return int(b.done.Load()) == jobs })
	if p := b.peak.Load(); p > workers {
		t.Fatalf("peak concurrency %d exceeded Workers(%d)", p, workers)
	}
}

func TestStage1_SlowJobsDoNotStopTheDispatcher(t *testing.T) {
	t.Run("a free worker still runs later jobs on time", func(t *testing.T) {
		h := newHarness(t, WithWorkers(2))
		slow := newBlocker()
		defer slow.open()
		if _, err := h.s.ScheduleAfter(0, slow.job); err != nil {
			t.Fatal(err)
		}
		slow.waitStarted(t, 1)
		h.after(time.Second, "x")
		h.after(2*time.Second, "y")
		h.clk.StepTo(t, epoch.Add(time.Second))
		h.waitRuns(1)
		h.clk.StepTo(t, epoch.Add(2*time.Second))
		h.waitRuns(2)
		h.wantNames("x", "y")
		if slow.done.Load() != 0 {
			t.Fatal("the slow job should still be running")
		}
	})
	t.Run("a saturated pool does not block the dispatcher, Schedule or Cancel", func(t *testing.T) {
		h := newHarness(t, WithWorkers(1))
		slow := newBlocker()
		defer slow.open()
		if _, err := h.s.ScheduleAfter(0, slow.job); err != nil {
			t.Fatal(err)
		}
		slow.waitStarted(t, 1) // the only worker is now busy
		h.after(time.Second, "x")
		y := h.after(2*time.Second, "y")
		h.after(time.Minute, "z")
		h.clk.StepTo(t, epoch.Add(time.Second)) // x is due but has no worker
		// If the dispatcher blocks handing x to the busy worker, it never arms a
		// timer for y and this StepTo fails.
		h.clk.StepTo(t, epoch.Add(2*time.Second))
		// The dispatcher has moved y out of the heap and parked on z's timer: y is
		// now due and queued behind the busy worker.
		h.clk.WaitArmed(t, epoch.Add(time.Minute))
		within(t, "Cancel and ScheduleAfter while every worker is busy", func() {
			if !h.s.Cancel(y) {
				t.Error("Cancel of a due-but-queued job = false, want true")
			}
			if _, err := h.s.ScheduleAfter(time.Hour, h.job("later")); err != nil {
				t.Errorf("ScheduleAfter while the pool is saturated: %v", err)
			}
		})
		slow.open()
		h.waitRuns(1)
		quiet()
		h.wantNames("x") // y was cancelled while queued, so it never runs
	})
}

func TestStage1_JobsMayScheduleAndCancelJobs(t *testing.T) {
	// One worker: if Schedule needed a free worker, or blocked on the
	// dispatcher while the dispatcher waited for this worker, this deadlocks.
	h := newHarness(t, WithWorkers(1))
	var children atomic.Int32
	victim, err := h.s.ScheduleAfter(time.Hour, func(context.Context) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	var cancelled atomic.Bool
	if _, err := h.s.ScheduleAfter(0, func(context.Context) error {
		for i := 0; i < 100; i++ {
			if _, err := h.s.ScheduleAfter(0, func(context.Context) error { children.Add(1); return nil }); err != nil {
				return err
			}
		}
		cancelled.Store(h.s.Cancel(victim))
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "100 children scheduled from inside a job to run", func() bool { return children.Load() == 100 })
	if !cancelled.Load() {
		t.Fatal("Cancel called from inside a job returned false for a pending job")
	}

	// A chain: each job schedules the next one.
	const depth = 500
	var links atomic.Int32
	var link Job
	link = func(context.Context) error {
		if links.Add(1) < depth {
			_, err := h.s.ScheduleAfter(0, link)
			return err
		}
		return nil
	}
	if _, err := h.s.ScheduleAfter(0, link); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the chain of self-scheduling jobs to finish", func() bool { return links.Load() == depth })
}

func TestStage1_Scale_OneDispatcherNotOnePerJob(t *testing.T) {
	const n = 100_000
	baseline := runtime.NumGoroutine()
	h := newHarness(t, WithWorkers(1)) // one worker: execution order is observable
	// Offsets repeat on purpose (n jobs over n/2 distinct instants) so the
	// tie-break is exercised at scale too.
	offsets := make([]time.Duration, n)
	x := uint32(12345)
	for i := range offsets {
		x = x*1664525 + 1013904223
		offsets[i] = time.Duration(1+(x>>8)%(n/2)) * time.Millisecond
	}
	var order []int // appended by the single worker only
	var ran atomic.Int32
	within(t, fmt.Sprintf("scheduling %d jobs", n), func() {
		for i := 0; i < n; i++ {
			i := i
			if _, err := h.s.ScheduleAfter(offsets[i], func(context.Context) error {
				order = append(order, i)
				ran.Add(1)
				return nil
			}); err != nil {
				t.Errorf("ScheduleAfter #%d: %v", i, err)
				return
			}
		}
	})
	if extra := runtime.NumGoroutine() - baseline; extra > 8 {
		t.Fatalf("%d extra goroutines with %d pending jobs: expected one dispatcher plus a fixed worker pool, not a goroutine (or time.AfterFunc) per job", extra, n)
	}
	if got := h.clk.maxArmedTimers(); got > 2 {
		t.Fatalf("%d clock timers were armed at once with %d pending jobs: the dispatcher should sleep on ONE timer for the earliest job", got, n)
	}
	h.clk.WaitArmed(t, epoch.Add(slices.Min(offsets)))
	h.clk.Advance(time.Hour) // everything is due
	waitFor(t, fmt.Sprintf("all %d jobs to run (ran %d)", n, ran.Load()), func() bool { return int(ran.Load()) == n })
	for k := 1; k < n; k++ {
		a, b := order[k-1], order[k]
		if offsets[a] > offsets[b] || (offsets[a] == offsets[b] && a > b) {
			t.Fatalf("run %d: job %d (+%v) ran before job %d (+%v): order must be (run time, schedule order)", k, a, offsets[a], b, offsets[b])
		}
	}
}

func TestStage1_Scale_CancelDoesNotScanEveryJob(t *testing.T) {
	// 100,000 cancels that each scan 100,000 jobs is 5*10^9 steps and misses
	// the time cap; O(log n) (or O(1) tombstones) finishes in milliseconds.
	const n = 100_000
	h := newHarness(t)
	ids := make([]JobID, n)
	within(t, fmt.Sprintf("scheduling %d jobs", n), func() {
		for i := range ids {
			id, err := h.s.ScheduleAfter(time.Duration(n-i)*time.Second, h.job("never"))
			if err != nil {
				t.Errorf("ScheduleAfter: %v", err)
				return
			}
			ids[i] = id
		}
	})
	within(t, fmt.Sprintf("cancelling %d jobs", n), func() {
		for i := 0; i < n; i++ {
			j := (i * 7919) % n // a scattered order, not front-to-back
			if !h.s.Cancel(ids[j]) {
				t.Errorf("Cancel(%d) = false for a pending job", ids[j])
				return
			}
		}
	})
	h.clk.Advance(24 * time.Hour)
	quiet()
	if got := h.log.names(); len(got) != 0 {
		t.Fatalf("%d cancelled jobs ran anyway", len(got))
	}
}

func TestStage1_ConcurrentScheduleAndCancelIsRaceFree(t *testing.T) {
	const goroutines, per = 16, 500
	h := newHarness(t, WithWorkers(4))
	var ran, cancelled, scheduled atomic.Int64
	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < per; i++ {
				id, err := h.s.ScheduleAfter(time.Duration(1+(g+i)%10)*time.Second, func(context.Context) error {
					ran.Add(1)
					return nil
				})
				if err != nil {
					t.Errorf("ScheduleAfter: %v", err)
					return
				}
				scheduled.Add(1)
				if i%3 == 0 && h.s.Cancel(id) {
					cancelled.Add(1)
				}
			}
		}(g)
	}
	within(t, "concurrent Schedule/Cancel", wg.Wait)
	want := scheduled.Load() - cancelled.Load()
	// Every job is due after a minute and none can have run before. Keep nudging
	// time forward until they have all run (the dispatcher may be mid-arm).
	waitFor(t, fmt.Sprintf("%d surviving jobs to run (ran %d)", want, ran.Load()), func() bool {
		h.clk.Advance(time.Minute)
		return ran.Load() >= want
	})
	quiet()
	if got := ran.Load(); got != want {
		t.Fatalf("%d jobs ran, want %d (= %d scheduled - %d cancelled): a cancelled job ran, a job ran twice or a job was lost",
			got, want, scheduled.Load(), cancelled.Load())
	}
}

func TestStage1_StopCancelsRunningDiscardsPendingAndRefusesNewJobs(t *testing.T) {
	baseline := runtime.NumGoroutine()
	for i := 0; i < 20; i++ {
		h := newHarness(t, WithWorkers(3))
		started := make(chan struct{})
		sawCancel := make(chan error, 1)
		if _, err := h.s.ScheduleAfter(0, func(ctx context.Context) error {
			close(started)
			<-ctx.Done()
			sawCancel <- ctx.Err()
			return ctx.Err()
		}); err != nil {
			t.Fatal(err)
		}
		pending := h.after(time.Hour, "pending")
		<-started
		within(t, "Stop", h.s.Stop)
		select {
		case err := <-sawCancel:
			if err != context.Canceled {
				t.Fatalf("running job saw %v after Stop, want context.Canceled", err)
			}
		default:
			t.Fatal("Stop returned while a running job's context was not cancelled (it must cancel, then wait for jobs to return)")
		}
		within(t, "second Stop", h.s.Stop) // idempotent
		if _, err := h.s.ScheduleAfter(time.Second, h.job("x")); err != ErrClosed {
			t.Fatalf("ScheduleAfter after Stop error = %v, want ErrClosed", err)
		}
		if _, err := h.s.ScheduleAt(epoch, h.job("x")); err != ErrClosed {
			t.Fatalf("ScheduleAt after Stop error = %v, want ErrClosed", err)
		}
		if h.s.Cancel(pending) {
			t.Fatal("Cancel after Stop of a discarded job = true, want false")
		}
		h.clk.Advance(2 * time.Hour)
		if got := h.log.names(); len(got) != 0 {
			t.Fatalf("jobs ran after Stop: %v", got)
		}
	}
	waitForGoroutines(t, baseline)
}
