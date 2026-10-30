package scheduler

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

// settle gives the worker that has just returned from a job a moment to record
// its completion. A test cannot observe that moment through the public API, and
// whether a tick that arrives right after counts as "overlapping" depends on
// it. It is tiny and only used where a completion is followed by a tick.
func settle() { time.Sleep(10 * time.Millisecond) }

// every schedules a recurring job on the harness' scheduler and logs each run.
func (h *harness) every(interval time.Duration, name string, opts ...RecurringOption) JobID {
	h.t.Helper()
	id, err := h.s.ScheduleEvery(interval, h.job(name), opts...)
	if err != nil {
		h.t.Fatalf("ScheduleEvery(%v) returned error %v", interval, err)
	}
	return id
}

func plus(d time.Duration) time.Time { return epoch.Add(d) }

func (h *harness) wantStarts(want ...time.Duration) {
	h.t.Helper()
	got := h.log.snapshot()
	if len(got) != len(want) {
		h.t.Fatalf("%d runs %v, want %d runs at %v", len(got), starts(got), len(want), want)
	}
	for i, w := range want {
		if !got[i].at.Equal(plus(w)) {
			h.t.Fatalf("run %d started at +%v, want +%v (all starts: %v)", i+1, got[i].at.Sub(epoch), w, starts(got))
		}
	}
}

func starts(es []runEntry) []time.Duration {
	var out []time.Duration
	for _, e := range es {
		out = append(out, e.at.Sub(epoch))
	}
	return out
}

// gatedJob logs its start and then holds until the test sends on gate (or the
// job is cancelled). This is how a test makes a run "take" some fake time:
// advance the clock, then let the run finish.
func (h *harness) gatedJob(name string, gate <-chan struct{}) Job {
	return func(ctx context.Context) error {
		h.log.add(name, h.clk.Now())
		select {
		case <-gate:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func TestStage2_ScheduleEveryValidatesArguments(t *testing.T) {
	h := newHarness(t)
	for _, d := range []time.Duration{0, -time.Second} {
		if _, err := h.s.ScheduleEvery(d, h.job("x")); err != ErrInvalidInterval {
			t.Errorf("ScheduleEvery(%v) error = %v, want ErrInvalidInterval (a zero interval makes a catch-up loop spin forever)", d, err)
		}
	}
	if _, err := h.s.ScheduleEvery(time.Second, nil); err != ErrNilJob {
		t.Errorf("ScheduleEvery(nil job) error = %v, want ErrNilJob", err)
	}
	if _, err := h.s.ScheduleEvery(time.Second, h.job("x"), WithMode(Mode(99))); err != ErrInvalidOption {
		t.Errorf("unknown Mode error = %v, want ErrInvalidOption", err)
	}
	if _, err := h.s.ScheduleEvery(time.Second, h.job("x"), WithOverlap(Overlap(0))); err != ErrInvalidOption {
		t.Errorf("unknown Overlap error = %v, want ErrInvalidOption", err)
	}
}

func TestStage2_FixedRateRunsOnTheGrid(t *testing.T) {
	h := newHarness(t)
	h.every(time.Second, "tick")
	for k := 1; k <= 5; k++ {
		h.clk.StepTo(t, plus(time.Duration(k)*time.Second))
		h.waitRuns(k)
	}
	h.wantStarts(1*time.Second, 2*time.Second, 3*time.Second, 4*time.Second, 5*time.Second)
}

func TestStage2_InitialDelay(t *testing.T) {
	h := newHarness(t)
	h.every(time.Second, "now", WithInitialDelay(0)) // runs right away, then every second
	h.waitRuns(1)
	h.wantStarts(0)
	h.clk.StepTo(t, plus(time.Second))
	h.waitRuns(2)

	h2 := newHarness(t)
	h2.every(time.Second, "late", WithInitialDelay(10*time.Second))
	h2.clk.WaitArmed(t, plus(10*time.Second))
	h2.clk.Advance(9 * time.Second)
	quiet()
	if got := h2.log.names(); len(got) != 0 {
		t.Fatalf("recurring job ran before its initial delay: %v", got)
	}
	h2.clk.StepTo(t, plus(10*time.Second))
	h2.waitRuns(1)
}

func TestStage2_FixedRateDoesNotDrift(t *testing.T) {
	// Every run takes 300ms of fake time. A fixed-rate job must still start at
	// exactly k seconds; an implementation that re-arms "interval after the run
	// started or finished" drifts by 0.3s per run. AllowOverlap keeps this test
	// about drift and not about completion timing.
	h := newHarness(t)
	gate := make(chan struct{})
	if _, err := h.s.ScheduleEvery(time.Second, h.gatedJob("tick", gate), WithOverlap(AllowOverlap)); err != nil {
		t.Fatal(err)
	}
	var want []time.Duration
	for k := 1; k <= 10; k++ {
		h.clk.StepTo(t, plus(time.Duration(k)*time.Second))
		h.waitRuns(k)
		h.clk.WaitArmed(t, plus(time.Duration(k+1)*time.Second)) // the next tick is on the grid
		h.clk.Advance(300 * time.Millisecond)                    // the run "works" for 300ms
		gate <- struct{}{}
		want = append(want, time.Duration(k)*time.Second)
	}
	h.wantStarts(want...)
}

func TestStage2_FixedDelayWaitsAfterCompletionAndNeverOverlaps(t *testing.T) {
	h := newHarness(t)
	gate := make(chan struct{})
	var running, peak atomic.Int32
	job := func(ctx context.Context) error {
		n := running.Add(1)
		for {
			p := peak.Load()
			if n <= p || peak.CompareAndSwap(p, n) {
				break
			}
		}
		defer running.Add(-1)
		return h.gatedJob("tick", gate)(ctx)
	}
	// AllowOverlap must be ignored: with a fixed delay there is nothing to overlap.
	if _, err := h.s.ScheduleEvery(time.Second, job, WithMode(FixedDelay), WithOverlap(AllowOverlap)); err != nil {
		t.Fatal(err)
	}
	// Each run takes 300ms: starts at 1s, finishes 1.3s, next at 2.3s, 3.6s, 4.9s.
	want := []time.Duration{
		1000 * time.Millisecond, 2300 * time.Millisecond, 3600 * time.Millisecond, 4900 * time.Millisecond,
	}
	for k, w := range want {
		h.clk.StepTo(t, plus(w))
		h.waitRuns(k + 1)
		h.clk.Advance(300 * time.Millisecond)
		gate <- struct{}{}
	}
	h.wantStarts(want...)
	if p := peak.Load(); p != 1 {
		t.Fatalf("peak concurrency %d for a fixed-delay job, want 1", p)
	}
}

func TestStage2_OverlapSkipDropsTicksWhileARunIsActive(t *testing.T) {
	h := newHarness(t)
	b := newBlocker()
	defer b.open()
	if _, err := h.s.ScheduleEvery(time.Second, func(ctx context.Context) error {
		h.log.add("run", h.clk.Now())
		return b.job(ctx)
	}); err != nil { // defaults: FixedRate + SkipOverlap
		t.Fatal(err)
	}
	h.clk.StepTo(t, plus(1*time.Second))
	h.waitRuns(1)
	h.clk.StepTo(t, plus(2*time.Second))    // due while run 1 is still going
	h.clk.StepTo(t, plus(3*time.Second))    // ditto
	h.clk.WaitArmed(t, plus(4*time.Second)) // the dispatcher has processed the 3s tick
	quiet()
	if n := len(h.log.snapshot()); n != 1 {
		t.Fatalf("%d runs after two overlapping ticks, want 1: SkipOverlap must drop them", n)
	}
	h.clk.Advance(500 * time.Millisecond) // 3.5s
	b.open()                              // run 1 finishes
	waitFor(t, "run 1 to finish", func() bool { return b.done.Load() == 1 })
	settle()
	quiet()
	if n := len(h.log.snapshot()); n != 1 {
		t.Fatalf("%d runs right after run 1 finished, want 1: SkipOverlap must not replay the skipped ticks", n)
	}
	h.clk.StepTo(t, plus(4*time.Second))
	h.waitRuns(2)
	h.wantStarts(1*time.Second, 4*time.Second)
}

func TestStage2_OverlapQueueRemembersExactlyOneTick(t *testing.T) {
	h := newHarness(t)
	b := newBlocker()
	defer b.open()
	if _, err := h.s.ScheduleEvery(time.Second, func(ctx context.Context) error {
		h.log.add("run", h.clk.Now())
		return b.job(ctx)
	}, WithOverlap(QueueOverlap)); err != nil {
		t.Fatal(err)
	}
	h.clk.StepTo(t, plus(1*time.Second))
	h.waitRuns(1)
	h.clk.StepTo(t, plus(2*time.Second))    // remembered
	h.clk.StepTo(t, plus(3*time.Second))    // coalesced into the remembered one
	h.clk.WaitArmed(t, plus(4*time.Second)) // the dispatcher has processed the 3s tick
	h.clk.Advance(500 * time.Millisecond)
	b.open() // run 1 finishes at 3.5s: the remembered tick runs NOW, once
	h.waitRuns(2)
	settle()
	h.clk.StepTo(t, plus(4*time.Second))
	h.waitRuns(3)
	quiet()
	// 1s (first), 3.5s (the one remembered tick), 4s (regular). Not 2s and 3s as well.
	h.wantStarts(1*time.Second, 3500*time.Millisecond, 4*time.Second)
}

func TestStage2_OverlapAllowRunsConcurrently(t *testing.T) {
	h := newHarness(t, WithWorkers(4))
	b := newBlocker()
	defer b.open()
	if _, err := h.s.ScheduleEvery(time.Second, b.job, WithOverlap(AllowOverlap)); err != nil {
		t.Fatal(err)
	}
	for k := 1; k <= 3; k++ {
		h.clk.StepTo(t, plus(time.Duration(k)*time.Second))
		b.waitStarted(t, k)
	}
	if n := b.running.Load(); n != 3 {
		t.Fatalf("%d runs in flight after three ticks of a slow AllowOverlap job, want 3", n)
	}
	b.open()
	waitFor(t, "runs to finish", func() bool { return b.done.Load() == 3 })
}

func TestStage2_CancelRecurringBetweenRuns(t *testing.T) {
	for _, mode := range []struct {
		name string
		m    Mode
	}{{"fixed-rate", FixedRate}, {"fixed-delay", FixedDelay}} {
		t.Run(mode.name, func(t *testing.T) {
			h := newHarness(t)
			id := h.every(time.Second, "tick", WithMode(mode.m))
			h.clk.StepTo(t, plus(time.Second))
			h.waitRuns(1)
			h.clk.StepTo(t, plus(2*time.Second))
			h.waitRuns(2)
			settle()
			if !h.s.Cancel(id) {
				t.Fatal("Cancel of a recurring job between runs = false, want true")
			}
			if h.s.Cancel(id) {
				t.Fatal("second Cancel = true, want false")
			}
			h.clk.Advance(time.Hour)
			quiet()
			if n := len(h.log.snapshot()); n != 2 {
				t.Fatalf("%d runs, want 2: a cancelled recurring job ran again", n)
			}
		})
	}
}

func TestStage2_CancelRecurringDuringARun(t *testing.T) {
	cases := []struct {
		name     string
		opts     []RecurringOption
		remember bool // let a second tick arrive during the run (QueueOverlap remembers it)
	}{
		{"fixed-rate skip", nil, false},
		{"fixed-rate queue (a remembered tick must be discarded)", []RecurringOption{WithOverlap(QueueOverlap)}, true},
		{"fixed-delay (completion must not re-arm)", []RecurringOption{WithMode(FixedDelay)}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t)
			sawCancel := make(chan error, 4)
			id, err := h.s.ScheduleEvery(time.Second, func(ctx context.Context) error {
				h.log.add("run", h.clk.Now())
				<-ctx.Done()
				sawCancel <- ctx.Err()
				return ctx.Err()
			}, c.opts...)
			if err != nil {
				t.Fatal(err)
			}
			h.clk.StepTo(t, plus(time.Second))
			h.waitRuns(1)
			if c.remember {
				h.clk.StepTo(t, plus(2*time.Second))
				h.clk.WaitArmed(t, plus(3*time.Second)) // the 2s tick has been processed
			}
			if !h.s.Cancel(id) {
				t.Fatal("Cancel of a recurring job during a run = false, want true")
			}
			select {
			case err := <-sawCancel:
				if err != context.Canceled {
					t.Fatalf("running invocation saw %v, want context.Canceled", err)
				}
			case <-time.After(testTimeout):
				t.Fatal("Cancel did not cancel the context of the running invocation")
			}
			settle()
			h.clk.Advance(time.Hour)
			quiet()
			if n := len(h.log.snapshot()); n != 1 {
				t.Fatalf("%d runs, want 1: the series must stop for good once cancelled", n)
			}
		})
	}
}

func TestStage2_ForwardClockJumpDoesNotReplayMissedTicks(t *testing.T) {
	// The process was frozen (or NTP stepped the clock) for an hour. Replaying
	// 3,600 missed ticks would hammer the vendor; the job runs once and rejoins
	// its grid. AllowOverlap so that only the catch-up rule can explain one run.
	h := newHarness(t)
	h.every(time.Second, "tick", WithOverlap(AllowOverlap))
	h.clk.WaitArmed(t, plus(time.Second))
	h.clk.Advance(time.Hour + 500*time.Millisecond)
	h.waitRuns(1)
	quiet()
	if n := len(h.log.snapshot()); n != 1 {
		t.Fatalf("%d runs after a one-hour clock jump, want exactly 1: missed ticks must be coalesced, not replayed", n)
	}
	h.clk.StepTo(t, plus(3601*time.Second)) // next grid instant after 3600.5s
	h.waitRuns(2)
	h.wantStarts(time.Hour+500*time.Millisecond, 3601*time.Second)
}
