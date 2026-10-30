package scheduler

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestStage3_ShutdownDrainsOwedWorkAndRejectsNewJobs(t *testing.T) {
	h := newHarness(t, WithWorkers(1))
	running := make(chan struct{})
	release := make(chan struct{})
	endErr := make(chan error, 1)
	if _, err := h.s.ScheduleAfter(0, func(ctx context.Context) error {
		close(running)
		<-release
		endErr <- ctx.Err()
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	<-running // the only worker is busy
	h.after(0, "due-1")
	h.after(0, "due-2")
	h.after(time.Hour, "future")
	h.every(time.Second, "recurring")

	result := make(chan error, 1)
	go func() { result <- h.s.Shutdown(context.Background()) }()
	waitFor(t, "Shutdown to start refusing new jobs (ErrClosed)", func() bool {
		_, err := h.s.ScheduleAfter(time.Hour, h.job("probe"))
		return err == ErrClosed
	})
	quiet()
	select {
	case err := <-result:
		t.Fatalf("Shutdown returned (%v) while a job was still running and two due jobs were still queued", err)
	default:
	}
	close(release)
	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("Shutdown = %v, want nil after a full drain", err)
		}
	case <-time.After(testTimeout):
		t.Fatal("Shutdown did not return after the running job finished")
	}
	if err := <-endErr; err != nil {
		t.Fatalf("running job's context was cancelled (%v) although Shutdown drained within its deadline", err)
	}
	h.clk.Advance(2 * time.Hour)
	quiet()
	// Due work ran, in order; future and recurring work was dropped.
	h.wantNames("due-1", "due-2")
}

func TestStage3_ShutdownDeadlineCancelsRunningJobsAndDoesNotHang(t *testing.T) {
	baseline := runtime.NumGoroutine()
	h := newHarness(t, WithWorkers(2))
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
	stubborn := newBlocker() // ignores its context, like a buggy vendor adapter
	defer stubborn.open()
	if _, err := h.s.ScheduleAfter(0, stubborn.job); err != nil {
		t.Fatal(err)
	}
	<-started
	stubborn.waitStarted(t, 1)

	sctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() { result <- h.s.Shutdown(sctx) }()
	quiet()
	select {
	case err := <-result:
		t.Fatalf("Shutdown returned (%v) before its deadline although jobs were still running", err)
	default:
	}
	cancel()
	select {
	case err := <-result:
		if err != context.Canceled {
			t.Fatalf("Shutdown = %v after its ctx was cancelled, want context.Canceled", err)
		}
	case <-time.After(testTimeout):
		t.Fatal("Shutdown kept waiting for a job that ignores its context after its own deadline passed")
	}
	select {
	case err := <-sawCancel:
		if err != context.Canceled {
			t.Fatalf("running job saw %v, want context.Canceled: an expired Shutdown must cancel running jobs", err)
		}
	case <-time.After(testTimeout):
		t.Fatal("an expired Shutdown did not cancel the running job's context")
	}
	stubborn.open() // now the stuck job returns, and every scheduler goroutine must exit
	waitForGoroutines(t, baseline)
}

func TestStage3_StopEscalatesAGracefulShutdown(t *testing.T) {
	h := newHarness(t)
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
	<-started
	result := make(chan error, 1)
	go func() { result <- h.s.Shutdown(context.Background()) }() // would wait forever
	quiet()
	select {
	case <-result:
		t.Fatal("Shutdown returned while a job was still running")
	default:
	}
	within(t, "Stop", h.s.Stop)
	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("Shutdown = %v once Stop finished the job, want nil", err)
		}
	case <-time.After(testTimeout):
		t.Fatal("Stop did not release a pending Shutdown")
	}
	if err := <-sawCancel; err != context.Canceled {
		t.Fatalf("job saw %v after Stop, want context.Canceled", err)
	}
	if err := h.s.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown after Stop = %v, want nil", err)
	}
}

func TestStage3_PanicIsContainedAndReported(t *testing.T) {
	errs := make(chan error, 4)
	h := newHarness(t, WithWorkers(1), WithOnError(func(_ JobID, err error) { errs <- err }))
	sentinel := errors.New("sentinel")
	if _, err := h.s.ScheduleAfter(0, func(context.Context) error { panic("boom") }); err != nil {
		t.Fatal(err)
	}
	if _, err := h.s.ScheduleAfter(0, func(context.Context) error { panic(sentinel) }); err != nil {
		t.Fatal(err)
	}
	h.after(0, "after-the-panics") // the single worker must still be alive to run this

	var pe *PanicError
	err := recvErr(t, errs)
	if !errors.As(err, &pe) || pe.Value != "boom" || len(pe.Stack) == 0 {
		t.Fatalf("first reported error = %#v, want a *PanicError{Value: \"boom\"} with a stack", err)
	}
	err = recvErr(t, errs)
	if !errors.As(err, &pe) || !errors.Is(err, sentinel) {
		t.Fatalf("second reported error = %#v, want a *PanicError wrapping the panicked error", err)
	}
	h.waitRuns(1)
	waitFor(t, "both panics to be counted as failures", func() bool { return h.s.Stats().Failed == 2 })
}

func recvErr(t *testing.T, ch <-chan error) error {
	t.Helper()
	select {
	case err := <-ch:
		return err
	case <-time.After(testTimeout):
		t.Fatal("OnError was not called")
		return nil
	}
}

func TestStage3_ErrorsAreReportedButCancellationIsNot(t *testing.T) {
	type report struct {
		id  JobID
		err error
	}
	reports := make(chan report, 4)
	h := newHarness(t, WithOnError(func(id JobID, err error) { reports <- report{id, err} }))
	boom := errors.New("boom")
	failing, err := h.s.ScheduleAfter(0, func(context.Context) error { return boom })
	if err != nil {
		t.Fatal(err)
	}
	select {
	case r := <-reports:
		if r.id != failing || r.err != boom {
			t.Fatalf("OnError(%v, %v), want (%v, boom)", r.id, r.err, failing)
		}
	case <-time.After(testTimeout):
		t.Fatal("OnError was not called for a job that returned an error")
	}

	started := make(chan struct{})
	cancelled, err := h.s.ScheduleAfter(0, func(ctx context.Context) error {
		close(started)
		<-ctx.Done()
		return fmt.Errorf("giving up: %w", ctx.Err())
	})
	if err != nil {
		t.Fatal(err)
	}
	<-started
	h.s.Cancel(cancelled)
	waitFor(t, "the cancelled job to return", func() bool { return h.s.Stats().Running == 0 })
	quiet()
	select {
	case r := <-reports:
		t.Fatalf("OnError(%v, %v) called for a cancelled job: cancellation is not a failure", r.id, r.err)
	default:
	}
	if st := h.s.Stats(); st.Failed != 1 || st.Completed != 0 || st.Canceled != 1 {
		t.Fatalf("Stats = %+v, want Failed 1, Completed 0, Canceled 1", st)
	}
}

func TestStage3_RecurringJobSurvivesPanicsAndErrors(t *testing.T) {
	var failures atomic.Int32
	h := newHarness(t, WithWorkers(1), WithOnError(func(JobID, error) { failures.Add(1) }))
	var n atomic.Int32
	_, err := h.s.ScheduleEvery(time.Second, func(context.Context) error {
		h.log.add("run", h.clk.Now())
		switch n.Add(1) {
		case 1:
			panic("first run explodes")
		case 2:
			return errors.New("second run fails")
		}
		return nil
	}, WithMode(FixedDelay))
	if err != nil {
		t.Fatal(err)
	}
	for k := 1; k <= 3; k++ {
		h.clk.StepTo(t, plus(time.Duration(k)*time.Second))
		h.waitRuns(k)
	}
	waitFor(t, "the third run to complete", func() bool { return h.s.Stats().Completed == 1 })
	if got := failures.Load(); got != 2 {
		t.Fatalf("OnError called %d times, want 2 (one panic, one error): a panic must not end the series, as it does in some executors", got)
	}
}

func TestStage3_StatsTrackTheLifecycle(t *testing.T) {
	h := newHarness(t, WithWorkers(1))
	b := newBlocker()
	defer b.open()
	if _, err := h.s.ScheduleAfter(0, b.job); err != nil {
		t.Fatal(err)
	}
	b.waitStarted(t, 1)
	h.after(0, "queued") // due, waiting for the busy worker
	future := h.after(time.Hour, "future")
	if st := h.s.Stats(); st.Pending != 2 || st.Running != 1 {
		t.Fatalf("Stats = %+v, want Pending 2 (one queued, one in the future) and Running 1", st)
	}
	if !h.s.Cancel(future) {
		t.Fatal("Cancel(future) = false")
	}
	if st := h.s.Stats(); st.Pending != 1 || st.Canceled != 1 {
		t.Fatalf("Stats after Cancel = %+v, want Pending 1, Canceled 1", st)
	}
	b.open()
	waitFor(t, "both jobs to complete", func() bool {
		st := h.s.Stats()
		return st.Completed == 2 && st.Running == 0 && st.Pending == 0
	})
	if _, err := h.s.ScheduleAfter(0, func(context.Context) error { return errors.New("nope") }); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the failing job to be counted", func() bool { return h.s.Stats().Failed == 1 })
}

func TestStage3_StatsCountSkippedTicks(t *testing.T) {
	h := newHarness(t)
	b := newBlocker()
	defer b.open()
	if _, err := h.s.ScheduleEvery(time.Second, b.job); err != nil { // fixed-rate, SkipOverlap
		t.Fatal(err)
	}
	h.clk.StepTo(t, plus(time.Second))
	b.waitStarted(t, 1)
	h.clk.StepTo(t, plus(2*time.Second))
	h.clk.StepTo(t, plus(3*time.Second))
	h.clk.WaitArmed(t, plus(4*time.Second))
	if st := h.s.Stats(); st.Skipped != 2 || st.Running != 1 {
		t.Fatalf("Stats = %+v, want Skipped 2 (ticks at 2s and 3s) and Running 1", st)
	}
}

func TestStage3_OnErrorRunsOutsideTheLockAndCanRetry(t *testing.T) {
	// The classic retry hook: on failure, schedule the same work again with a
	// backoff. It calls Schedule and Stats from inside OnError on the only
	// worker, so a hook invoked under the scheduler's lock deadlocks here.
	var h *harness
	var attempts atomic.Int32
	var job Job
	job = func(context.Context) error {
		n := attempts.Add(1)
		h.log.add(fmt.Sprintf("attempt-%d", n), h.clk.Now())
		if n < 3 {
			return errors.New("flaky vendor")
		}
		return nil
	}
	h = newHarness(t, WithWorkers(1), WithOnError(func(JobID, error) {
		_ = h.s.Stats()
		n := attempts.Load()
		if _, err := h.s.ScheduleAfter(time.Duration(n)*time.Second, job); err != nil {
			t.Errorf("retry ScheduleAfter: %v", err)
		}
	}))
	if _, err := h.s.ScheduleAfter(0, job); err != nil {
		t.Fatal(err)
	}
	h.waitRuns(1)
	h.clk.StepTo(t, plus(1*time.Second)) // attempt 1 failed at 0: retry after 1s
	h.waitRuns(2)
	h.clk.StepTo(t, plus(3*time.Second)) // attempt 2 failed at 1s: retry after 2s
	h.waitRuns(3)
	h.wantStarts(0, 1*time.Second, 3*time.Second)
	waitFor(t, "stats to settle", func() bool {
		st := h.s.Stats()
		return st.Failed == 2 && st.Completed == 1
	})

	// A panic inside the hook must not take the worker down.
	h2 := newHarness(t, WithWorkers(1), WithOnError(func(JobID, error) { panic("hook bug") }))
	if _, err := h2.s.ScheduleAfter(0, func(context.Context) error { return errors.New("x") }); err != nil {
		t.Fatal(err)
	}
	h2.after(0, "still-alive")
	h2.waitRuns(1)
}

func TestStage3_ClockStepBackwardsDelaysJobsWithoutSpinning(t *testing.T) {
	h := newHarness(t)
	h.after(10*time.Second, "once")
	h.every(time.Second, "tick")
	h.clk.StepTo(t, plus(time.Second))
	h.waitRuns(1)
	h.clk.WaitArmed(t, plus(2*time.Second))
	before := h.clk.timersCreated()
	h.clk.Set(plus(-time.Hour)) // NTP steps the wall clock back an hour
	quiet()
	if got := h.clk.timersCreated() - before; got > 10 {
		t.Fatalf("%d timers created in a quiet window after the clock stepped back: the dispatcher is spinning", got)
	}
	if n := len(h.log.snapshot()); n != 1 {
		t.Fatalf("%d runs after the clock stepped back, want still 1: nothing is due yet", n)
	}
	// When time gets back to the schedule, it resumes on the original grid.
	h.clk.StepTo(t, plus(2*time.Second))
	h.waitRuns(2)
	h.wantStarts(time.Second, 2*time.Second)
}

func TestStage3_NoGoroutineLeakAcrossLifecycles(t *testing.T) {
	baseline := runtime.NumGoroutine()
	for i := 0; i < 30; i++ {
		h := newHarness(t, WithWorkers(3))
		h.after(0, "due")
		h.after(time.Hour, "future")
		h.every(time.Second, "recurring")
		started := make(chan struct{})
		if _, err := h.s.ScheduleAfter(0, func(ctx context.Context) error {
			close(started)
			<-ctx.Done()
			return ctx.Err()
		}); err != nil {
			t.Fatal(err)
		}
		<-started
		if i%2 == 0 {
			within(t, "Stop", h.s.Stop)
		} else {
			sctx, cancel := context.WithCancel(context.Background())
			cancel() // an already-expired deadline: Shutdown must cancel and not leak
			if err := h.s.Shutdown(sctx); err != context.Canceled {
				t.Fatalf("Shutdown(expired ctx) = %v, want context.Canceled", err)
			}
		}
	}
	waitForGoroutines(t, baseline)
}

func TestStage3_ShutdownRacesWithSchedulersWithoutLosingAcceptedJobs(t *testing.T) {
	h := newHarness(t, WithWorkers(4))
	var ran, accepted atomic.Int64
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				_, err := h.s.ScheduleAfter(0, func(context.Context) error { ran.Add(1); return nil })
				if err == ErrClosed {
					return
				}
				if err != nil {
					t.Errorf("ScheduleAfter: %v", err)
					return
				}
				accepted.Add(1)
			}
		}()
	}
	waitFor(t, "some jobs to run", func() bool { return ran.Load() > 100 })
	sctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()
	if err := h.s.Shutdown(sctx); err != nil {
		t.Fatalf("Shutdown = %v, want nil", err)
	}
	within(t, "schedulers to notice ErrClosed", wg.Wait)
	if r, a := ran.Load(), accepted.Load(); r != a {
		t.Fatalf("%d jobs ran but %d were accepted: a job accepted before Shutdown must run, and none may run twice", r, a)
	}
}
