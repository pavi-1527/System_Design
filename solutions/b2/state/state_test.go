package state

import (
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"
)

var t0 = time.Date(2026, 3, 2, 9, 0, 0, 0, time.UTC)

// clock is a settable fake clock.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func newTransfer(t *testing.T, c *clock) *Transfer {
	t.Helper()
	tr, err := New("tr-1", 12500, c.now)
	if err != nil || tr == nil {
		t.Fatalf("New returned %v, %v", tr, err)
	}
	return tr
}

// paths lead from a fresh transfer to each state. They are written here, not
// read from the package, so the test is an independent statement of the lifecycle.
var paths = map[State][]Event{
	Initiated:  nil,
	Authorised: {Authorise},
	Settled:    {Authorise, Settle},
	Failed:     {Fail},
	Reversed:   {Authorise, Settle, Reverse},
}

var allStates = []State{Initiated, Authorised, Settled, Failed, Reversed}
var allEvents = []Event{Authorise, Settle, Fail, Reverse}

// legal is the lifecycle as the business states it: (state, event) -> next state.
var legal = map[State]map[Event]State{
	Initiated:  {Authorise: Authorised, Fail: Failed},
	Authorised: {Settle: Settled, Fail: Failed},
	Settled:    {Reverse: Reversed},
}

func driveTo(t *testing.T, c *clock, s State) *Transfer {
	t.Helper()
	tr := newTransfer(t, c)
	for _, ev := range paths[s] {
		if err := tr.Apply(ev); err != nil {
			t.Fatalf("driving to %s: Apply(%s) = %v", s, ev, err)
		}
	}
	if got := tr.State(); got != s {
		t.Fatalf("driving to %s ended in %s", s, got)
	}
	return tr
}

func TestNew_ValidatesAndStartsInitiated(t *testing.T) {
	for _, c := range []struct {
		id     string
		amount int64
	}{{"", 100}, {"x", 0}, {"x", -5}} {
		if tr, err := New(c.id, c.amount, nil); !errors.Is(err, ErrInvalid) || tr != nil {
			t.Errorf("New(%q, %d) = %v, %v; want nil, ErrInvalid", c.id, c.amount, tr, err)
		}
	}
	tr, err := New("ok", 1, nil) // nil clock defaults to time.Now
	if err != nil {
		t.Fatal(err)
	}
	if tr.State() != Initiated {
		t.Errorf("new transfer is %s, want initiated", tr.State())
	}
	if err := tr.Apply(Authorise); err != nil {
		t.Errorf("Apply with the default clock: %v", err)
	}
}

func TestApply_EveryStateEventPairMatchesTheLifecycle(t *testing.T) {
	for _, from := range allStates {
		for _, ev := range allEvents {
			c := &clock{t: t0}
			tr := driveTo(t, c, from)
			histBefore := tr.History()
			err := tr.Apply(ev)

			if to, ok := legal[from][ev]; ok {
				if err != nil {
					t.Errorf("%s --%s--> should be legal, got %v", from, ev, err)
				} else if got := tr.State(); got != to {
					t.Errorf("%s --%s--> ended in %s, want %s", from, ev, got, to)
				}
				continue
			}

			// Not in the lifecycle: must be rejected, and must change nothing.
			wasLast := len(paths[from]) > 0 && paths[from][len(paths[from])-1] == ev
			switch {
			case wasLast && !errors.Is(err, ErrDuplicate):
				t.Errorf("%s --%s--> repeats the event that produced the state; want ErrDuplicate, got %v", from, ev, err)
			case !wasLast && !errors.Is(err, ErrIllegalTransition):
				t.Errorf("%s --%s--> is not a legal edge; want ErrIllegalTransition, got %v (a missing or extra edge in the table?)", from, ev, err)
			}
			if got := tr.State(); got != from {
				t.Errorf("rejected %s --%s--> but state changed to %s", from, ev, got)
			}
			if !reflect.DeepEqual(tr.History(), histBefore) {
				t.Errorf("rejected %s --%s--> but history changed", from, ev)
			}
		}
	}
}

func TestApply_DuplicateIsNotIllegal(t *testing.T) {
	tr := driveTo(t, &clock{t: t0}, Settled)
	err := tr.Apply(Settle)
	if !errors.Is(err, ErrDuplicate) {
		t.Fatalf("second Settle = %v, want ErrDuplicate", err)
	}
	if errors.Is(err, ErrIllegalTransition) {
		t.Error("a duplicate must be distinguishable from an illegal transition: callers retry/ack duplicates but alert on illegal moves")
	}
}

func TestApply_UnknownEventIsIllegal(t *testing.T) {
	tr := driveTo(t, &clock{t: t0}, Authorised)
	if err := tr.Apply(Event("refund")); !errors.Is(err, ErrIllegalTransition) {
		t.Errorf("unknown event: %v, want ErrIllegalTransition", err)
	}
}

func TestAllowed_ListsTheLegalEventsSorted(t *testing.T) {
	want := map[State][]Event{
		Initiated:  {Authorise, Fail},
		Authorised: {Fail, Settle},
		Settled:    {Reverse},
		Failed:     {},
		Reversed:   {},
	}
	for _, s := range allStates {
		got := driveTo(t, &clock{t: t0}, s).Allowed()
		if len(got) == 0 && len(want[s]) == 0 {
			continue
		}
		if !reflect.DeepEqual(got, want[s]) {
			t.Errorf("Allowed() in %s = %v, want %v", s, got, want[s])
		}
	}
}

func TestIsTerminal(t *testing.T) {
	want := map[State]bool{Initiated: false, Authorised: false, Settled: false, Failed: true, Reversed: true}
	for s, w := range want {
		if got := IsTerminal(s); got != w {
			t.Errorf("IsTerminal(%s) = %v, want %v", s, got, w)
		}
	}
	if IsTerminal(State("nonsense")) != true {
		// An unknown state has no outgoing edges; treating it as terminal is the safe answer.
		t.Error("IsTerminal of an unknown state should be true (no way out)")
	}
}

func TestReverse_RespectsTheReversalWindow(t *testing.T) {
	c := &clock{t: t0}
	tr := driveTo(t, c, Authorised)
	c.advance(time.Hour)
	if err := tr.Apply(Settle); err != nil {
		t.Fatal(err)
	}
	settledAt := c.now()

	c.advance(ReversalWindow + time.Nanosecond) // one nanosecond too late
	err := tr.Apply(Reverse)
	if !errors.Is(err, ErrReversalWindow) {
		t.Fatalf("reverse after the window = %v, want ErrReversalWindow", err)
	}
	if errors.Is(err, ErrIllegalTransition) {
		t.Error("a closed window is not an illegal transition: the edge exists, the guard refused it")
	}
	if tr.State() != Settled || len(tr.History()) != 2 {
		t.Errorf("a refused reversal changed the transfer: state %s, history %d", tr.State(), len(tr.History()))
	}

	// The boundary is inclusive: exactly ReversalWindow after settlement still works.
	c.t = settledAt.Add(ReversalWindow)
	if err := tr.Apply(Reverse); err != nil {
		t.Fatalf("reverse exactly at the window boundary = %v, want nil", err)
	}
	if tr.State() != Reversed {
		t.Errorf("state = %s, want reversed", tr.State())
	}
}

func TestHistory_RecordsEveryMoveAndIsACopy(t *testing.T) {
	c := &clock{t: t0}
	tr := newTransfer(t, c)
	_ = tr.Apply(Authorise)
	c.advance(time.Minute)
	_ = tr.Apply(Settle)
	_ = tr.Apply(Settle) // rejected duplicates are not history

	want := []Transition{
		{From: Initiated, To: Authorised, Event: Authorise, At: t0},
		{From: Authorised, To: Settled, Event: Settle, At: t0.Add(time.Minute)},
	}
	got := tr.History()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("History() = %+v, want %+v", got, want)
	}
	got[0].To = Reversed
	if tr.History()[0].To != Authorised {
		t.Error("History() returned the internal slice; callers can rewrite the audit trail")
	}
}

func TestApply_ConcurrentDuplicateSettleSucceedsExactlyOnce(t *testing.T) {
	tr := driveTo(t, &clock{t: t0}, Authorised)
	const n = 50
	errs := make(chan error, n)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			errs <- tr.Apply(Settle)
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	var ok, dup int
	for err := range errs {
		switch {
		case err == nil:
			ok++
		case errors.Is(err, ErrDuplicate):
			dup++
		default:
			t.Errorf("unexpected error %v", err)
		}
	}
	if ok != 1 || dup != n-1 {
		t.Errorf("%d concurrent Settles: %d succeeded, %d duplicates; want 1 and %d (a retried callback must not settle twice)", n, ok, dup, n-1)
	}
	if got := len(tr.History()); got != 2 {
		t.Errorf("history has %d entries, want 2", got)
	}
}

func TestApply_ConcurrentConflictingEventsLeaveAConsistentTransfer(t *testing.T) {
	for round := 0; round < 200; round++ {
		tr := driveTo(t, &clock{t: t0}, Authorised)
		var wg sync.WaitGroup
		results := make([]error, 2)
		for i, ev := range []Event{Settle, Fail} {
			wg.Add(1)
			go func() {
				defer wg.Done()
				results[i] = tr.Apply(ev)
			}()
		}
		// readers run alongside the writers
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = tr.State()
			_ = tr.Allowed()
			_ = tr.History()
		}()
		wg.Wait()

		wins := 0
		for _, err := range results {
			if err == nil {
				wins++
			}
		}
		if wins != 1 {
			t.Fatalf("round %d: settle and fail both returned %v; exactly one must win", round, results)
		}
		last := tr.History()[len(tr.History())-1]
		if last.To != tr.State() {
			t.Fatalf("round %d: state %s disagrees with last history entry %+v", round, tr.State(), last)
		}
	}
}
