package saga

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// ---------- helpers (prefixed with t to avoid clashing with your code) ----------

var (
	tErrCrash   = errors.New("simulated crash")
	tErrNetwork = errors.New("bank: payment network rejected the transfer")
)

// tLedger is a fake downstream service: a counter ("net") that actions
// increment and compensations decrement.
//
//   - idem: calls are deduplicated by idempotency key, like a well-built
//     payment API. Off, every call changes the counter (a naive service).
//   - tombstones: a compensation that arrives for an action that never ran
//     leaves a marker so a LATE action with that key is refused. Off, the late
//     action still applies after it was "undone".
type tLedger struct {
	mu         sync.Mutex
	idem       bool
	tombstones bool
	applied    map[string]bool
	reverted   map[string]bool
	tomb       map[string]bool
	calls      map[string]int
	net        int
}

func tNewLedger() *tLedger {
	return &tLedger{idem: true, tombstones: true, applied: map[string]bool{}, reverted: map[string]bool{}, tomb: map[string]bool{}, calls: map[string]int{}}
}

func (l *tLedger) apply(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.calls[key]++
	if l.idem && (l.applied[key] || l.tomb[key]) {
		return
	}
	l.applied[key] = true
	l.net++
}

func (l *tLedger) revert(actionKey string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.calls["undo:"+actionKey]++
	if !l.idem {
		l.net--
		return
	}
	if l.reverted[actionKey] {
		return
	}
	l.reverted[actionKey] = true
	if l.applied[actionKey] {
		l.net--
	} else if l.tombstones {
		l.tomb[actionKey] = true
	}
}

func (l *tLedger) callCount(key string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.calls[key]
}

func (l *tLedger) value() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.net
}

// tGate lets a test hold an action in flight.
type tGate struct {
	started   chan struct{}
	release   chan struct{}
	ignoreCtx bool
}

func tNewGate(ignoreCtx bool) *tGate {
	return &tGate{started: make(chan struct{}, 8), release: make(chan struct{}), ignoreCtx: ignoreCtx}
}

// tWorld is the set of fake services behind a payout saga.
type tWorld struct {
	funds, records, network, notices, finals *tLedger

	mu       sync.Mutex
	attempts map[string]int
	failures map[string]func(attempt int) error // by call key
	gates    map[string]*tGate                  // by call key
}

func tNewWorld() *tWorld {
	return &tWorld{
		funds: tNewLedger(), records: tNewLedger(), network: tNewLedger(), notices: tNewLedger(), finals: tNewLedger(),
		attempts: map[string]int{}, failures: map[string]func(int) error{}, gates: map[string]*tGate{},
	}
}

func (w *tWorld) failKey(key string, f func(attempt int) error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.failures[key] = f
}

func (w *tWorld) gateKey(key string, g *tGate) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.gates[key] = g
}

func (w *tWorld) enter(ctx context.Context, key string) error {
	w.mu.Lock()
	g := w.gates[key]
	w.attempts[key]++
	n := w.attempts[key]
	f := w.failures[key]
	w.mu.Unlock()
	if g != nil {
		g.started <- struct{}{}
		if g.ignoreCtx {
			<-g.release
		} else {
			select {
			case <-g.release:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	}
	if f != nil {
		return f(n)
	}
	return nil
}

func (w *tWorld) act(l *tLedger) func(context.Context, Call) error {
	return func(ctx context.Context, c Call) error {
		if err := w.enter(ctx, c.Key); err != nil {
			return err
		}
		l.apply(c.Key)
		return nil
	}
}

func (w *tWorld) undo(l *tLedger) func(context.Context, Call) error {
	return func(ctx context.Context, c Call) error {
		if err := w.enter(ctx, c.Key); err != nil {
			return err
		}
		l.revert(Key(c.SagaID, c.Step, false))
		return nil
	}
}

// transferSteps: reserve funds -> record the transfer -> submit to the payment
// network. Every step can be undone.
func (w *tWorld) transferSteps() []Step {
	return []Step{
		{Name: "reserve", Action: w.act(w.funds), Compensate: w.undo(w.funds)},
		{Name: "record", Action: w.act(w.records), Compensate: w.undo(w.records)},
		{Name: "submit", Action: w.act(w.network), Compensate: w.undo(w.network), Timeout: 7 * time.Second},
	}
}

// pivotSteps: reserve funds -> submit to the network (the point of no return)
// -> notify the member -> post the final ledger entry.
func (w *tWorld) pivotSteps() []Step {
	return []Step{
		{Name: "reserve", Action: w.act(w.funds), Compensate: w.undo(w.funds)},
		{Name: "submit", Action: w.act(w.network), Pivot: true},
		{Name: "notify", Action: w.act(w.notices), Retriable: true},
		{Name: "finalize", Action: w.act(w.finals), Retriable: true},
	}
}

func (w *tWorld) nets() string {
	return fmt.Sprintf("funds=%d records=%d network=%d notices=%d finals=%d", w.funds.value(), w.records.value(), w.network.value(), w.notices.value(), w.finals.value())
}

// tTimers replaces real timers so tests decide when a step times out.
type tTimers struct {
	mu    sync.Mutex
	chans []chan time.Time
	durs  []time.Duration
}

func (tt *tTimers) NewTimer(d time.Duration) (<-chan time.Time, func()) {
	tt.mu.Lock()
	defer tt.mu.Unlock()
	c := make(chan time.Time, 1)
	tt.chans = append(tt.chans, c)
	tt.durs = append(tt.durs, d)
	return c, func() {}
}

func (tt *tTimers) fire(t *testing.T, i int) {
	t.Helper()
	tt.mu.Lock()
	defer tt.mu.Unlock()
	if i >= len(tt.chans) {
		t.Fatalf("timer %d was never created (only %d timers exist): does every Action call get its own timeout timer?", i, len(tt.chans))
	}
	tt.chans[i] <- time.Now()
}

func (tt *tTimers) duration(i int) time.Duration {
	tt.mu.Lock()
	defer tt.mu.Unlock()
	if i >= len(tt.durs) {
		return -1
	}
	return tt.durs[i]
}

func tOrch(t *testing.T, steps []Step, store Store, mut ...func(*Config)) *Orchestrator {
	t.Helper()
	cfg := Config{Store: store, Steps: steps, Sleep: func(context.Context, time.Duration) error { return nil }}
	for _, m := range mut {
		m(&cfg)
	}
	o, err := New(cfg)
	if err != nil || o == nil {
		t.Fatalf("New: (%v, %v)", o, err)
	}
	return o
}

func tCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func tTrail(st State) string { return strings.Join(st.Trail, " ") }

// tRunWithin runs fn in a goroutine and fails the test if it does not finish.
func tRunWithin(t *testing.T, d time.Duration, what string, fn func()) {
	t.Helper()
	done := make(chan struct{})
	go func() { fn(); close(done) }()
	select {
	case <-done:
	case <-time.After(d):
		t.Fatalf("%s did not finish within %v (the orchestrator is blocked waiting for something it should have given up on)", what, d)
	}
}

func tEventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(time.Millisecond)
	}
}

// ---------- store ----------

func TestMemoryStore_SaveRequiresTheCurrentVersion(t *testing.T) {
	s := NewMemoryStore()
	ctx := context.Background()
	if _, ok, err := s.Load(ctx, "s1"); ok || err != nil {
		t.Fatalf("Load of an unknown saga = (_, %v, %v), want (_, false, nil)", ok, err)
	}
	st, err := s.Save(ctx, State{ID: "s1", Status: StatusRunning})
	if err != nil || st.Version != 1 {
		t.Fatalf("first Save = (%+v, %v), want Version 1", st, err)
	}
	if _, err := s.Save(ctx, State{ID: "s1", Status: StatusRunning}); !errors.Is(err, ErrConflict) {
		t.Fatalf("Save of an existing saga with Version 0: err = %v, want ErrConflict", err)
	}
	stale := st
	st.Next = 1
	st2, err := s.Save(ctx, st)
	if err != nil || st2.Version != 2 {
		t.Fatalf("Save at the current version = (%+v, %v), want Version 2", st2, err)
	}
	if _, err := s.Save(ctx, stale); !errors.Is(err, ErrConflict) {
		t.Fatalf("Save with a stale version: err = %v, want ErrConflict: two runners must not both advance the same saga", err)
	}
	if _, err := s.Save(ctx, State{ID: "s2", Version: 5}); !errors.Is(err, ErrConflict) {
		t.Fatalf("Save of a new saga with a non-zero version: err = %v, want ErrConflict", err)
	}
}

func TestMemoryStore_ReturnsCopies(t *testing.T) {
	s := NewMemoryStore()
	ctx := context.Background()
	st, err := s.Save(ctx, State{ID: "s1", Status: StatusRunning, Trail: []string{"do:a"}})
	if err != nil || len(st.Trail) != 1 {
		t.Fatalf("Save = (%+v, %v), want the saved state back", st, err)
	}
	st.Trail[0] = "tampered"
	got, _, _ := s.Load(ctx, "s1")
	if len(got.Trail) != 1 || got.Trail[0] != "do:a" {
		t.Fatalf("stored trail = %v: the store aliased the caller's slice", got.Trail)
	}
	got.Trail[0] = "tampered"
	again, _, _ := s.Load(ctx, "s1")
	if len(again.Trail) != 1 || again.Trail[0] != "do:a" {
		t.Fatalf("Load returned a slice that aliases the stored state")
	}
}

// ---------- definition validation ----------

func TestNew_RejectsInvalidStepDefinitions(t *testing.T) {
	ok := func(context.Context, Call) error { return nil }
	cases := []struct {
		name  string
		steps []Step
	}{
		{"no steps", nil},
		{"empty name", []Step{{Name: "", Action: ok}}},
		{"duplicate name", []Step{{Name: "a", Action: ok}, {Name: "a", Action: ok}}},
		{"nil action", []Step{{Name: "a"}}},
		{"retriable without a pivot before it", []Step{{Name: "a", Action: ok, Retriable: true}}},
		{"non-retriable step after the pivot", []Step{{Name: "p", Action: ok, Pivot: true}, {Name: "b", Action: ok}}},
		{"compensation after the pivot", []Step{{Name: "p", Action: ok, Pivot: true}, {Name: "b", Action: ok, Retriable: true, Compensate: ok}}},
		{"two pivots", []Step{{Name: "p", Action: ok, Pivot: true}, {Name: "q", Action: ok, Pivot: true}}},
	}
	for _, c := range cases {
		if _, err := New(Config{Steps: c.steps}); !errors.Is(err, ErrInvalidSteps) {
			t.Errorf("%s: New err = %v, want ErrInvalidSteps. A malformed definition must fail at startup, not halfway through someone's payment", c.name, err)
		}
	}
	if _, err := New(Config{Steps: []Step{{Name: "a", Action: ok}, {Name: "p", Action: ok, Pivot: true}, {Name: "n", Action: ok, Retriable: true}}}); err != nil {
		t.Errorf("valid definition rejected: %v", err)
	}
}

// ---------- forward execution ----------

func TestSaga_HappyPathRunsEveryStepOnceInOrder(t *testing.T) {
	w, store := tNewWorld(), NewMemoryStore()
	o := tOrch(t, w.transferSteps(), store)
	st, err := o.Run(tCtx(t), "s1")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if st.Status != StatusCompleted || tTrail(st) != "do:reserve do:record do:submit" || st.Next != 3 {
		t.Fatalf("state = %+v, want completed with trail 'do:reserve do:record do:submit'", st)
	}
	if got := w.nets(); got != "funds=1 records=1 network=1 notices=0 finals=0" {
		t.Fatalf("effects = %s, want one of each", got)
	}
	saved, ok, _ := store.Load(context.Background(), "s1")
	if !ok || saved.Status != StatusCompleted {
		t.Fatalf("persisted state = %+v (found=%v), want completed: state must be saved, not just returned", saved, ok)
	}
}

func TestSaga_TerminalSagaIsNotRunAgain(t *testing.T) {
	w, store := tNewWorld(), NewMemoryStore()
	o := tOrch(t, w.transferSteps(), store)
	if _, err := o.Run(tCtx(t), "s1"); err != nil {
		t.Fatal(err)
	}
	k := Key("s1", "reserve", false)
	before := w.funds.callCount(k)
	st, err := o.Run(tCtx(t), "s1")
	if err != nil || st.Status != StatusCompleted {
		t.Fatalf("second Run = (%+v, %v), want the completed state and no error", st, err)
	}
	if after := w.funds.callCount(k); after != before {
		t.Fatalf("reserve was called %d more time(s) for a saga that already completed; a re-delivered 'start' command would move money twice", after-before)
	}
}

// ---------- compensation ----------

func TestSaga_FailureCompensatesCompletedStepsInReverseOrder(t *testing.T) {
	w, store := tNewWorld(), NewMemoryStore()
	w.failKey(Key("s1", "submit", false), func(int) error { return tErrNetwork })
	o := tOrch(t, w.transferSteps(), store)
	st, err := o.Run(tCtx(t), "s1")
	if err != nil {
		t.Fatalf("Run: %v (a compensated saga is an orderly outcome, not an error)", err)
	}
	if st.Status != StatusCompensated {
		t.Fatalf("status = %s, want compensated", st.Status)
	}
	if got := tTrail(st); got != "do:reserve do:record undo:record undo:reserve" {
		t.Fatalf("trail = %q, want 'do:reserve do:record undo:record undo:reserve': undo in reverse order, and the step that failed definitively is not undone", got)
	}
	if !strings.Contains(st.Err, "payment network rejected") {
		t.Fatalf("Err = %q, want the failure that caused the rollback", st.Err)
	}
	if got := w.nets(); got != "funds=0 records=0 network=0 notices=0 finals=0" {
		t.Fatalf("after rollback: %s; the member's money is still held somewhere", got)
	}
	if n := w.network.callCount("undo:" + Key("s1", "submit", false)); n != 0 {
		t.Fatalf("the failed step's compensation ran %d times; a step that definitively failed has nothing to undo", n)
	}
}

func TestSaga_FirstStepFailingNeedsNoCompensation(t *testing.T) {
	w := tNewWorld()
	w.failKey(Key("s1", "reserve", false), func(int) error { return errors.New("insufficient funds") })
	st, err := tOrch(t, w.transferSteps(), NewMemoryStore()).Run(tCtx(t), "s1")
	if err != nil || st.Status != StatusCompensated || len(st.Trail) != 0 {
		t.Fatalf("Run = (%+v, %v), want compensated with an empty trail", st, err)
	}
	if got := w.nets(); got != "funds=0 records=0 network=0 notices=0 finals=0" {
		t.Fatalf("effects = %s, want none", got)
	}
}

func TestSaga_StepWithoutCompensateIsSkippedWhenRollingBack(t *testing.T) {
	w := tNewWorld()
	steps := w.transferSteps()
	steps[1].Compensate = nil // "record" has nothing to undo
	w.failKey(Key("s1", "submit", false), func(int) error { return tErrNetwork })
	st, err := tOrch(t, steps, NewMemoryStore()).Run(tCtx(t), "s1")
	if err != nil || st.Status != StatusCompensated || tTrail(st) != "do:reserve do:record undo:reserve" {
		t.Fatalf("Run = (%s, %v), want compensated with trail 'do:reserve do:record undo:reserve'", tTrail(st), err)
	}
}

func TestSaga_CompensationIsRetriedWithBackoffUntilItSucceeds(t *testing.T) {
	w := tNewWorld()
	w.failKey(Key("s1", "submit", false), func(int) error { return tErrNetwork })
	w.failKey(Key("s1", "record", true), func(n int) error {
		if n <= 2 {
			return errors.New("records service unavailable")
		}
		return nil
	})
	var sleeps []time.Duration
	o := tOrch(t, w.transferSteps(), NewMemoryStore(), func(c *Config) {
		c.MaxAttempts = 5
		c.Backoff = func(attempt int) time.Duration { return time.Duration(attempt) * 100 * time.Millisecond }
		c.Sleep = func(_ context.Context, d time.Duration) error { sleeps = append(sleeps, d); return nil }
	})
	st, err := o.Run(tCtx(t), "s1")
	if err != nil || st.Status != StatusCompensated {
		t.Fatalf("Run = (%+v, %v), want compensated after the retries", st, err)
	}
	if got := w.records.callCount("undo:" + Key("s1", "record", false)); got != 1 {
		t.Fatalf("the record compensation applied %d times, want 1 (failures happen before the effect)", got)
	}
	if fmt.Sprint(sleeps) != "[100ms 200ms]" {
		t.Fatalf("backoff sleeps = %v, want [100ms 200ms]: Backoff(attempt) is consulted after each failed attempt", sleeps)
	}
	if got := w.nets(); got != "funds=0 records=0 network=0 notices=0 finals=0" {
		t.Fatalf("effects = %s, want all undone", got)
	}
	if st.Attempts != 0 {
		t.Fatalf("Attempts = %d after success, want 0", st.Attempts)
	}
}

func TestSaga_CompensationThatKeepsFailingEndsStuckAndResumeFinishesTheJob(t *testing.T) {
	w, store := tNewWorld(), NewMemoryStore()
	var healed atomic.Bool
	w.failKey(Key("s1", "submit", false), func(int) error { return tErrNetwork })
	w.failKey(Key("s1", "record", true), func(int) error {
		if healed.Load() {
			return nil
		}
		return errors.New("records service down")
	})
	o := tOrch(t, w.transferSteps(), store, func(c *Config) { c.MaxAttempts = 3 })
	st, err := o.Run(tCtx(t), "s1")
	if !errors.Is(err, ErrStuck) || st.Status != StatusStuck || st.StuckIn != StatusCompensating {
		t.Fatalf("Run = (%+v, %v), want ErrStuck with Status stuck and StuckIn compensating", st, err)
	}
	if st.Attempts != 3 || !strings.Contains(st.Err, "records service down") {
		t.Fatalf("Attempts=%d Err=%q, want 3 attempts and the last error", st.Attempts, st.Err)
	}
	if w.funds.value() != 1 {
		t.Fatalf("funds held = %d: reserve must not be released while the saga is stuck behind the failing step", w.funds.value())
	}
	again, err := o.Run(tCtx(t), "s1")
	if err != nil || again.Status != StatusStuck {
		t.Fatalf("Run on a stuck saga = (%+v, %v): a stuck saga waits for an operator, it is not retried forever", again, err)
	}
	if _, err := o.Resume(tCtx(t), "nope"); !errors.Is(err, ErrNotStuck) {
		t.Fatalf("Resume of an unknown saga: err = %v, want ErrNotStuck", err)
	}
	healed.Store(true)
	done, err := o.Resume(tCtx(t), "s1")
	if err != nil || done.Status != StatusCompensated {
		t.Fatalf("Resume = (%+v, %v), want compensated", done, err)
	}
	if got := w.nets(); got != "funds=0 records=0 network=0 notices=0 finals=0" {
		t.Fatalf("after resume: %s, want everything released", got)
	}
	if _, err := o.Resume(tCtx(t), "s1"); !errors.Is(err, ErrNotStuck) {
		t.Fatalf("Resume of a finished saga: err = %v, want ErrNotStuck", err)
	}
}

// ---------- timeouts ----------

func TestSaga_TimeoutIsAmbiguousSoTheTimedOutStepIsCompensatedToo(t *testing.T) {
	w, store := tNewWorld(), NewMemoryStore()
	gate := tNewGate(true) // the payment network call hangs and ignores its context
	w.gateKey(Key("s1", "submit", false), gate)
	timers := &tTimers{}
	o := tOrch(t, w.transferSteps(), store, func(c *Config) { c.NewTimer = timers.NewTimer; c.StepTimeout = 3 * time.Second })

	var st State
	var err error
	finished := make(chan struct{})
	go func() { st, err = o.Run(tCtx(t), "s1"); close(finished) }()

	select {
	case <-gate.started:
	case <-time.After(5 * time.Second):
		t.Fatal("the submit step never started")
	}
	if d := timers.duration(0); d != 3*time.Second {
		t.Fatalf("timer for 'reserve' = %v, want the default StepTimeout 3s", d)
	}
	if d := timers.duration(2); d != 7*time.Second {
		t.Fatalf("timer for 'submit' = %v, want its own Timeout 7s", d)
	}
	timers.fire(t, 2) // the deadline passes while submit is still stuck
	select {
	case <-finished:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not give up on the hung step at its deadline; a step that ignores its context must not be able to hang the saga")
	}
	if err != nil || st.Status != StatusCompensated {
		t.Fatalf("Run = (%+v, %v), want compensated", st, err)
	}
	if got := tTrail(st); got != "do:reserve do:record undo:submit undo:record undo:reserve" {
		t.Fatalf("trail = %q: after a timeout the outcome is unknown, so the timed-out step is compensated as well", got)
	}
	// Now the hung call finally goes through, long after it was "undone".
	close(gate.release)
	tEventually(t, "the late submit call to arrive", func() bool { return w.network.callCount(Key("s1", "submit", false)) == 1 })
	if got := w.network.value(); got != 0 {
		t.Fatalf("network effects = %d after the late call: the compensation must stop a late action from happening, or money leaves after the saga was rolled back", got)
	}
	if got := w.nets(); got != "funds=0 records=0 network=0 notices=0 finals=0" {
		t.Fatalf("effects = %s, want all zero", got)
	}
}

func TestSaga_ContextCancellationStopsRunAndTheSagaCanBeRunAgain(t *testing.T) {
	w, store := tNewWorld(), NewMemoryStore()
	gate := tNewGate(false) // respects its context
	w.gateKey(Key("s1", "record", false), gate)
	o := tOrch(t, w.transferSteps(), store)

	ctx, cancel := context.WithCancel(context.Background())
	var st State
	var err error
	finished := make(chan struct{})
	go func() { st, err = o.Run(ctx, "s1"); close(finished) }()
	select {
	case <-gate.started:
	case <-time.After(5 * time.Second):
		t.Fatal("the record step never started")
	}
	cancel()
	select {
	case <-finished:
	case <-time.After(5 * time.Second):
		t.Fatal("Run ignored context cancellation")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run err = %v, want context.Canceled", err)
	}
	if st.Status != StatusRunning {
		t.Fatalf("status = %s after cancellation, want running: cancelling the caller is not a business failure and must not start a rollback", st.Status)
	}
	close(gate.release)
	done, err := o.Run(tCtx(t), "s1")
	if err != nil || done.Status != StatusCompleted {
		t.Fatalf("Run after cancellation = (%+v, %v), want completed", done, err)
	}
	if got := w.nets(); got != "funds=1 records=1 network=1 notices=0 finals=0" {
		t.Fatalf("effects = %s, want one of each", got)
	}
}

func TestSaga_PanickingStepIsAFailureNotACrashOfTheOrchestrator(t *testing.T) {
	w := tNewWorld()
	steps := w.transferSteps()
	steps[2].Action = func(context.Context, Call) error { panic("nil pointer in the vendor adapter") }
	st, err := tOrch(t, steps, NewMemoryStore()).Run(tCtx(t), "s1")
	if err != nil || st.Status != StatusCompensated || !strings.Contains(st.Err, "panicked") {
		t.Fatalf("Run = (%+v, %v), want compensated with a 'panicked' error recorded", st, err)
	}
}

// ---------- crash and resume ----------

func TestSaga_RestartedOrchestratorSkipsStepsThatAlreadyCompleted(t *testing.T) {
	w, store := tNewWorld(), NewMemoryStore()
	crash := func(point string) error {
		if point == "before-action:submit" {
			return tErrCrash
		}
		return nil
	}
	first := tOrch(t, w.transferSteps(), store, func(c *Config) { c.Hook = crash })
	if _, err := first.Run(tCtx(t), "s1"); !errors.Is(err, tErrCrash) {
		t.Fatalf("first Run err = %v, want the crash", err)
	}
	saved, _, _ := store.Load(context.Background(), "s1")
	if saved.Next != 2 || tTrail(saved) != "do:reserve do:record" {
		t.Fatalf("persisted state at the crash = %+v, want Next 2 and trail 'do:reserve do:record'", saved)
	}
	second := tOrch(t, w.transferSteps(), store) // a new process, same store
	st, err := second.Run(tCtx(t), "s1")
	if err != nil || st.Status != StatusCompleted {
		t.Fatalf("resumed Run = (%+v, %v), want completed", st, err)
	}
	for _, step := range []string{"reserve", "record"} {
		var calls int
		switch step {
		case "reserve":
			calls = w.funds.callCount(Key("s1", step, false))
		case "record":
			calls = w.records.callCount(Key("s1", step, false))
		}
		if calls != 1 {
			t.Fatalf("%s was called %d times across the restart, want 1: completed steps are skipped", step, calls)
		}
	}
}

func TestSaga_CrashAfterTheCallBeforeTheSaveRepeatsTheCallWithTheSameKey(t *testing.T) {
	w, store := tNewWorld(), NewMemoryStore()
	var keys []string
	steps := w.transferSteps()
	inner := steps[1].Action
	steps[1].Action = func(ctx context.Context, c Call) error { keys = append(keys, c.Key); return inner(ctx, c) }
	crash := func(point string) error {
		if point == "after-action:record" {
			return tErrCrash
		}
		return nil
	}
	if _, err := tOrch(t, steps, store, func(c *Config) { c.Hook = crash }).Run(tCtx(t), "s1"); !errors.Is(err, tErrCrash) {
		t.Fatalf("first Run err = %v, want the crash", err)
	}
	st, err := tOrch(t, steps, store).Run(tCtx(t), "s1")
	if err != nil || st.Status != StatusCompleted {
		t.Fatalf("resumed Run = (%+v, %v)", st, err)
	}
	if len(keys) != 2 || keys[0] != keys[1] || keys[0] != Key("s1", "record", false) {
		t.Fatalf("record action was called with keys %v, want the same key twice: the orchestrator cannot know the first call's effect committed, so it repeats it, and only an idempotency key makes that safe", keys)
	}
	if w.records.value() != 1 {
		t.Fatalf("records effect = %d, want exactly 1", w.records.value())
	}
}

// tSweep crashes the orchestrator at every hook point in turn (discovered by a
// dry run), resumes with a fresh orchestrator, and calls check on the result.
func tSweep(t *testing.T, build func(w *tWorld) []Step, prep func(w *tWorld), check func(t *testing.T, point string, w *tWorld, st State)) int {
	t.Helper()
	var points []string
	{
		w := tNewWorld()
		prep(w)
		o := tOrch(t, build(w), NewMemoryStore(), func(c *Config) {
			c.Hook = func(p string) error { points = append(points, p); return nil }
		})
		if _, err := o.Run(tCtx(t), "s1"); err != nil {
			t.Fatalf("dry run: %v", err)
		}
	}
	if len(points) < 4 {
		t.Fatalf("dry run hit only %d hook points (%v): Hook must be called before and after every Action/Compensate", len(points), points)
	}
	for k := 1; k <= len(points); k++ {
		w, store := tNewWorld(), NewMemoryStore()
		prep(w)
		n := 0
		crashing := tOrch(t, build(w), store, func(c *Config) {
			c.Hook = func(p string) error {
				n++
				if n == k {
					return tErrCrash
				}
				return nil
			}
		})
		if _, err := crashing.Run(tCtx(t), "s1"); !errors.Is(err, tErrCrash) {
			t.Fatalf("crash #%d (%s): Run err = %v, want the crash", k, points[k-1], err)
		}
		st, err := tOrch(t, build(w), store).Run(tCtx(t), "s1")
		if err != nil {
			t.Fatalf("crash #%d (%s): resumed Run err = %v", k, points[k-1], err)
		}
		check(t, fmt.Sprintf("#%d %s", k, points[k-1]), w, st)
	}
	return len(points)
}

func TestSaga_CrashAtEveryPointThenResumeSucceedsWithEachEffectAppliedOnce(t *testing.T) {
	n := tSweep(t,
		func(w *tWorld) []Step { return w.transferSteps() },
		func(w *tWorld) {},
		func(t *testing.T, point string, w *tWorld, st State) {
			if st.Status != StatusCompleted {
				t.Fatalf("crash %s: status %s, want completed", point, st.Status)
			}
			if got := w.nets(); got != "funds=1 records=1 network=1 notices=0 finals=0" {
				t.Fatalf("crash %s: %s; want exactly one of each effect. A crash must never double-apply a step", point, got)
			}
		})
	t.Logf("swept %d crash points on the success path", n)
}

func TestSaga_CrashAtEveryPointDuringRollbackStillEndsFullyCompensated(t *testing.T) {
	n := tSweep(t,
		func(w *tWorld) []Step { return w.transferSteps() },
		func(w *tWorld) { w.failKey(Key("s1", "submit", false), func(int) error { return tErrNetwork }) },
		func(t *testing.T, point string, w *tWorld, st State) {
			if st.Status != StatusCompensated {
				t.Fatalf("crash %s: status %s, want compensated", point, st.Status)
			}
			if got := w.nets(); got != "funds=0 records=0 network=0 notices=0 finals=0" {
				t.Fatalf("crash %s: %s; want every effect undone exactly once, including when the crash lands between a compensation and its save", point, got)
			}
		})
	t.Logf("swept %d crash points on the rollback path", n)
}

// ---------- pivot and retriable steps ----------

func TestSaga_AfterThePivotFailuresAreRetriedForwardAndNeverCompensated(t *testing.T) {
	w := tNewWorld()
	w.failKey(Key("s1", "notify", false), func(n int) error {
		if n < 3 {
			return errors.New("notification service timeout")
		}
		return nil
	})
	var sleeps int
	o := tOrch(t, w.pivotSteps(), NewMemoryStore(), func(c *Config) {
		c.Backoff = func(int) time.Duration { return time.Millisecond }
		c.Sleep = func(context.Context, time.Duration) error { sleeps++; return nil }
	})
	st, err := o.Run(tCtx(t), "s1")
	if err != nil || st.Status != StatusCompleted {
		t.Fatalf("Run = (%+v, %v), want completed", st, err)
	}
	if got := w.notices.callCount(Key("s1", "notify", false)); got != 1 {
		t.Fatalf("notify effect applied %d times, want 1", got)
	}
	if got := w.nets(); got != "funds=1 records=0 network=1 notices=1 finals=1" {
		t.Fatalf("effects = %s, want the transfer fully carried through", got)
	}
	if tTrail(st) != "do:reserve do:submit do:notify do:finalize" || sleeps != 2 {
		t.Fatalf("trail %q, backoff sleeps %d; want no 'undo:' entries and 2 sleeps for the 2 failed attempts", tTrail(st), sleeps)
	}
}

func TestSaga_RetriableStepThatNeverRecoversEndsStuckWithoutRollingBackThePivot(t *testing.T) {
	w := tNewWorld()
	w.failKey(Key("s1", "finalize", false), func(int) error { return errors.New("general ledger unavailable") })
	o := tOrch(t, w.pivotSteps(), NewMemoryStore(), func(c *Config) { c.MaxAttempts = 4 })
	st, err := o.Run(tCtx(t), "s1")
	if !errors.Is(err, ErrStuck) || st.Status != StatusStuck || st.StuckIn != StatusRunning || st.Attempts != 4 {
		t.Fatalf("Run = (%+v, %v), want ErrStuck, StuckIn running, 4 attempts", st, err)
	}
	if got := w.nets(); got != "funds=1 records=0 network=1 notices=1 finals=0" {
		t.Fatalf("effects = %s: after the pivot the money has left, so the saga must not release the hold; it waits for the ledger and pages a human", got)
	}
	if strings.Contains(tTrail(st), "undo:") {
		t.Fatalf("trail %q contains compensation after the pivot", tTrail(st))
	}
}

func TestSaga_PivotThatFailsStillRollsBackEarlierSteps(t *testing.T) {
	w := tNewWorld()
	w.failKey(Key("s1", "submit", false), func(int) error { return tErrNetwork })
	st, err := tOrch(t, w.pivotSteps(), NewMemoryStore()).Run(tCtx(t), "s1")
	if err != nil || st.Status != StatusCompensated || tTrail(st) != "do:reserve undo:reserve" {
		t.Fatalf("Run = (%s %s, %v), want compensated with trail 'do:reserve undo:reserve': until the pivot succeeds, the saga can still be rolled back", st.Status, tTrail(st), err)
	}
}

// ---------- concurrency ----------

func TestSaga_ConcurrentRunnersOfTheSameSagaApplyEachEffectOnce(t *testing.T) {
	w, store := tNewWorld(), NewMemoryStore()
	o := tOrch(t, w.transferSteps(), store)
	var wg sync.WaitGroup
	var ok, conflicts atomic.Int64
	start := make(chan struct{})
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := o.Run(context.Background(), "s1")
			switch {
			case err == nil:
				ok.Add(1)
			case errors.Is(err, ErrConflict):
				conflicts.Add(1)
			default:
				t.Errorf("Run: %v", err)
			}
		}()
	}
	close(start)
	tRunWithin(t, 15*time.Second, "8 concurrent runners", wg.Wait)
	if ok.Load() == 0 {
		t.Fatalf("no runner finished the saga (conflicts: %d)", conflicts.Load())
	}
	saved, _, _ := store.Load(context.Background(), "s1")
	if saved.Status != StatusCompleted || tTrail(saved) != "do:reserve do:record do:submit" {
		t.Fatalf("persisted state = %+v, want completed with each step recorded once", saved)
	}
	if got := w.nets(); got != "funds=1 records=1 network=1 notices=0 finals=0" {
		t.Fatalf("effects = %s: runners that raced on the same saga applied a step twice", got)
	}
}
