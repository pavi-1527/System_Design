package ticketing

import (
	"bytes"
	"errors"
	"fmt"
	"runtime"
	"runtime/pprof"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const testTimeout = 5 * time.Second

// fakeClock is a manually advanced clock, safe for concurrent use.
type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{now: time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)}
}

func (f *fakeClock) Now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.now
}

func (f *fakeClock) Advance(d time.Duration) {
	f.mu.Lock()
	f.now = f.now.Add(d)
	f.mu.Unlock()
}

// newInv returns an inventory driven by a fake clock. Extra options (limits,
// hooks) are applied after WithClock.
func newInv(t *testing.T, opts ...Option) (*Inventory, *fakeClock) {
	t.Helper()
	clk := newFakeClock()
	inv, err := New(append([]Option{WithClock(clk)}, opts...)...)
	if err != nil {
		t.Fatalf("New returned error %v", err)
	}
	if inv == nil {
		t.Fatal("New returned a nil *Inventory with a nil error")
	}
	return inv, clk
}

// seatRange returns prefix1..prefixN, e.g. seatRange("A", 3) = A1, A2, A3.
func seatRange(prefix string, n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = fmt.Sprintf("%s%d", prefix, i+1)
	}
	return out
}

func mustAddEvent(t *testing.T, inv *Inventory, id string, seats []string) {
	t.Helper()
	if err := inv.AddEvent(id, seats); err != nil {
		t.Fatalf("AddEvent(%q, %d seats) = %v", id, len(seats), err)
	}
}

func mustHold(t *testing.T, inv *Inventory, ev, user string, seats []string, ttl time.Duration) Hold {
	t.Helper()
	h, err := inv.Hold(ev, user, seats, ttl)
	if err != nil {
		t.Fatalf("Hold(%s, %s, %v, %v) = %v, want success", ev, user, seats, ttl, err)
	}
	if h.ID == "" {
		t.Fatalf("Hold(%s, %s, %v) succeeded with an empty hold ID", ev, user, seats)
	}
	return h
}

func wantSeat(t *testing.T, inv *Inventory, ev, seat string, want SeatState) {
	t.Helper()
	got, err := inv.SeatStatus(ev, seat)
	if err != nil {
		t.Fatalf("SeatStatus(%s, %s) = %v", ev, seat, err)
	}
	if got != want {
		t.Fatalf("seat %s is %v, want %v", seat, got, want)
	}
}

func wantErr(t *testing.T, what string, err, want error) {
	t.Helper()
	if !errors.Is(err, want) {
		t.Fatalf("%s: error = %v, want %v", what, err, want)
	}
}

// withinD fails the test if f does not return within d, so a deadlock or an
// accidental O(n^2) fails instead of hanging the run.
func withinD(t *testing.T, d time.Duration, what string, f func()) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		f()
	}()
	select {
	case <-done:
	case <-time.After(d):
		t.Fatalf("%s did not finish within %v (deadlock, or a per-operation cost that grows with the amount of state)", what, d)
	}
}

func within(t *testing.T, what string, f func()) {
	t.Helper()
	withinD(t, testTimeout, what, f)
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

// ---- Stage 1 ----------------------------------------------------------------

func TestStage1_AddEventValidation(t *testing.T) {
	inv, _ := newInv(t)
	mustAddEvent(t, inv, "show", seatRange("A", 3))
	wantErr(t, "duplicate event", inv.AddEvent("show", seatRange("B", 2)), ErrEventExists)
	wantErr(t, "empty event ID", inv.AddEvent("", seatRange("A", 2)), ErrInvalidRequest)
	wantErr(t, "no seats", inv.AddEvent("e2", nil), ErrInvalidRequest)
	wantErr(t, "duplicate seat IDs", inv.AddEvent("e3", []string{"A1", "A2", "A1"}), ErrInvalidRequest)
	wantErr(t, "empty seat ID", inv.AddEvent("e4", []string{"A1", ""}), ErrInvalidRequest)
	// A rejected AddEvent must not leave a half-registered event behind.
	if _, err := inv.SeatStatus("e3", "A1"); !errors.Is(err, ErrUnknownEvent) {
		t.Fatalf("event e3 exists after a rejected AddEvent (SeatStatus error = %v)", err)
	}
}

func TestStage1_HoldMarksSeatsHeld(t *testing.T) {
	inv, clk := newInv(t)
	mustAddEvent(t, inv, "show", seatRange("A", 5))
	h := mustHold(t, inv, "show", "alice", []string{"A3", "A1"}, 10*time.Minute)
	if h.EventID != "show" || h.UserID != "alice" {
		t.Fatalf("hold = %+v, want EventID show and UserID alice", h)
	}
	if strings.Join(h.Seats, ",") != "A3,A1" {
		t.Fatalf("hold.Seats = %v, want [A3 A1] (request order)", h.Seats)
	}
	if want := clk.Now().Add(10 * time.Minute); !h.ExpiresAt.Equal(want) {
		t.Fatalf("hold.ExpiresAt = %v, want now+ttl = %v", h.ExpiresAt, want)
	}
	wantSeat(t, inv, "show", "A1", Held)
	wantSeat(t, inv, "show", "A3", Held)
	for _, s := range []string{"A2", "A4", "A5"} {
		wantSeat(t, inv, "show", s, Available)
	}
}

func TestStage1_HoldIDsAreUnique(t *testing.T) {
	inv, _ := newInv(t)
	mustAddEvent(t, inv, "e1", seatRange("A", 4))
	mustAddEvent(t, inv, "e2", seatRange("A", 4))
	seen := map[string]bool{}
	for _, ev := range []string{"e1", "e2"} {
		for _, s := range seatRange("A", 4) {
			h := mustHold(t, inv, ev, "u", []string{s}, time.Hour)
			if seen[h.ID] {
				t.Fatalf("hold ID %q issued twice: Confirm(holdID) could book someone else's seats", h.ID)
			}
			seen[h.ID] = true
		}
	}
}

func TestStage1_HoldIsAllOrNothing(t *testing.T) {
	inv, _ := newInv(t)
	mustAddEvent(t, inv, "show", seatRange("A", 6))
	mustHold(t, inv, "show", "alice", []string{"A3"}, time.Hour)

	// A3 is taken: bob's request must fail naming A3 and leave A1, A2 and A4 alone.
	_, err := inv.Hold("show", "bob", []string{"A1", "A2", "A3", "A4"}, time.Hour)
	wantErr(t, "hold including a taken seat", err, ErrSeatUnavailable)
	if !strings.Contains(err.Error(), "A3") {
		t.Fatalf("error %q does not name the unavailable seat A3", err)
	}
	for _, s := range []string{"A1", "A2", "A4"} {
		wantSeat(t, inv, "show", s, Available) // a partial hold would show Held here
	}

	// An unknown seat in the middle must not leave the earlier seats held either.
	_, err = inv.Hold("show", "bob", []string{"A5", "ZZ9", "A6"}, time.Hour)
	wantErr(t, "hold including an unknown seat", err, ErrUnknownSeat)
	if !strings.Contains(err.Error(), "ZZ9") {
		t.Fatalf("error %q does not name the unknown seat ZZ9", err)
	}
	wantSeat(t, inv, "show", "A5", Available)
	wantSeat(t, inv, "show", "A6", Available)

	// And the seats are genuinely free: bob can take them now.
	mustHold(t, inv, "show", "bob", []string{"A1", "A2", "A4", "A5", "A6"}, time.Hour)
}

func TestStage1_SeatCannotBeHeldTwice(t *testing.T) {
	inv, _ := newInv(t)
	mustAddEvent(t, inv, "show", seatRange("A", 3))
	h := mustHold(t, inv, "show", "alice", []string{"A1"}, time.Hour)
	_, err := inv.Hold("show", "bob", []string{"A1"}, time.Hour)
	wantErr(t, "second hold of a held seat", err, ErrSeatUnavailable)
	if _, err := inv.Hold("show", "alice", []string{"A1"}, time.Hour); !errors.Is(err, ErrSeatUnavailable) {
		t.Fatalf("holding your own held seat again = %v, want ErrSeatUnavailable (a second hold would be a second owner)", err)
	}
	if _, err := inv.Confirm(h.ID); err != nil {
		t.Fatalf("Confirm = %v", err)
	}
	_, err = inv.Hold("show", "bob", []string{"A1"}, time.Hour)
	wantErr(t, "hold of a booked seat", err, ErrSeatUnavailable)
}

func TestStage1_InvalidHoldRequests(t *testing.T) {
	inv, _ := newInv(t)
	mustAddEvent(t, inv, "show", seatRange("A", 3))
	cases := []struct {
		name  string
		event string
		user  string
		seats []string
		ttl   time.Duration
		want  error
	}{
		{"empty user", "show", "", []string{"A1"}, time.Hour, ErrInvalidRequest},
		{"no seats", "show", "u", nil, time.Hour, ErrInvalidRequest},
		{"zero ttl", "show", "u", []string{"A1"}, 0, ErrInvalidRequest},
		{"negative ttl", "show", "u", []string{"A1"}, -time.Second, ErrInvalidRequest},
		{"duplicate seat in request", "show", "u", []string{"A1", "A2", "A1"}, time.Hour, ErrInvalidRequest},
		{"unknown event", "nope", "u", []string{"A1"}, time.Hour, ErrUnknownEvent},
	}
	for _, c := range cases {
		if _, err := inv.Hold(c.event, c.user, c.seats, c.ttl); !errors.Is(err, c.want) {
			t.Errorf("%s: Hold error = %v, want %v", c.name, err, c.want)
		}
	}
	for _, s := range seatRange("A", 3) {
		wantSeat(t, inv, "show", s, Available) // a rejected request must change nothing
	}
}

func TestStage1_ConfirmBooksSeats(t *testing.T) {
	inv, clk := newInv(t)
	mustAddEvent(t, inv, "show", seatRange("A", 4))
	h := mustHold(t, inv, "show", "alice", []string{"A2", "A4"}, time.Hour)
	b, err := inv.Confirm(h.ID)
	if err != nil {
		t.Fatalf("Confirm = %v", err)
	}
	if b.ID == "" || b.HoldID != h.ID || b.EventID != "show" || b.UserID != "alice" {
		t.Fatalf("booking = %+v, want ID set, HoldID %s, EventID show, UserID alice", b, h.ID)
	}
	if strings.Join(b.Seats, ",") != "A2,A4" {
		t.Fatalf("booking.Seats = %v, want [A2 A4]", b.Seats)
	}
	if !b.ConfirmedAt.Equal(clk.Now()) {
		t.Fatalf("booking.ConfirmedAt = %v, want the clock's now %v", b.ConfirmedAt, clk.Now())
	}
	wantSeat(t, inv, "show", "A2", Booked)
	wantSeat(t, inv, "show", "A4", Booked)
	wantSeat(t, inv, "show", "A1", Available)

	_, err = inv.Confirm(h.ID)
	wantErr(t, "second Confirm of the same hold", err, ErrAlreadyConfirmed)
	if b2, _ := inv.Confirm(h.ID); b2.ID != "" {
		t.Fatalf("a refused Confirm returned a non-zero booking %+v", b2)
	}
}

func TestStage1_ReleaseFreesSeats(t *testing.T) {
	inv, _ := newInv(t)
	mustAddEvent(t, inv, "show", seatRange("A", 3))
	h := mustHold(t, inv, "show", "alice", []string{"A1", "A2"}, time.Hour)
	if err := inv.Release(h.ID); err != nil {
		t.Fatalf("Release = %v", err)
	}
	wantSeat(t, inv, "show", "A1", Available)
	wantSeat(t, inv, "show", "A2", Available)
	mustHold(t, inv, "show", "bob", []string{"A1", "A2"}, time.Hour) // reusable at once

	wantErr(t, "second Release", inv.Release(h.ID), ErrUnknownHold)
	_, err := inv.Confirm(h.ID)
	wantErr(t, "Confirm after Release", err, ErrUnknownHold)
	wantSeat(t, inv, "show", "A1", Held) // bob's hold must be untouched by alice's stale calls

	h2 := mustHold(t, inv, "show", "carol", []string{"A3"}, time.Hour)
	if _, err := inv.Confirm(h2.ID); err != nil {
		t.Fatal(err)
	}
	wantErr(t, "Release after Confirm", inv.Release(h2.ID), ErrAlreadyConfirmed)
	wantSeat(t, inv, "show", "A3", Booked)
}

func TestStage1_UnknownHoldsAndEvents(t *testing.T) {
	inv, _ := newInv(t)
	mustAddEvent(t, inv, "show", seatRange("A", 2))
	for _, id := range []string{"", "nope", "show#999", "ghost#1", "#", "show#"} {
		if _, err := inv.Confirm(id); !errors.Is(err, ErrUnknownHold) {
			t.Errorf("Confirm(%q) = %v, want ErrUnknownHold", id, err)
		}
		if err := inv.Release(id); !errors.Is(err, ErrUnknownHold) {
			t.Errorf("Release(%q) = %v, want ErrUnknownHold", id, err)
		}
	}
	if _, err := inv.SeatStatus("nope", "A1"); !errors.Is(err, ErrUnknownEvent) {
		t.Errorf("SeatStatus of an unknown event = %v, want ErrUnknownEvent", err)
	}
	if _, err := inv.SeatStatus("show", "Z1"); !errors.Is(err, ErrUnknownSeat) {
		t.Errorf("SeatStatus of an unknown seat = %v, want ErrUnknownSeat", err)
	}
}

func TestStage1_EventsAreIndependent(t *testing.T) {
	inv, _ := newInv(t)
	mustAddEvent(t, inv, "friday", seatRange("A", 2))
	mustAddEvent(t, inv, "saturday", seatRange("A", 2))
	h := mustHold(t, inv, "friday", "alice", []string{"A1"}, time.Hour)
	wantSeat(t, inv, "saturday", "A1", Available)
	mustHold(t, inv, "saturday", "bob", []string{"A1"}, time.Hour) // same seat label, other show
	if _, err := inv.Confirm(h.ID); err != nil {
		t.Fatal(err)
	}
	wantSeat(t, inv, "friday", "A1", Booked)
	wantSeat(t, inv, "saturday", "A1", Held)
}

func TestStage1_ReturnedValuesDoNotAliasInternalState(t *testing.T) {
	inv, _ := newInv(t)
	mustAddEvent(t, inv, "show", seatRange("A", 4))
	req := []string{"A1", "A2"}
	h := mustHold(t, inv, "show", "alice", req, time.Hour)
	req[0] = "A4"     // caller reuses its slice
	h.Seats[1] = "A3" // caller scribbles on the returned hold
	wantSeat(t, inv, "show", "A4", Available)
	wantSeat(t, inv, "show", "A3", Available)
	b, err := inv.Confirm(h.ID)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(b.Seats, ",") != "A1,A2" {
		t.Fatalf("booking seats = %v, want [A1 A2]: the inventory kept the caller's slice instead of copying it", b.Seats)
	}
	wantSeat(t, inv, "show", "A1", Booked)
	wantSeat(t, inv, "show", "A2", Booked)
}

func TestStage1_HoldingManySeatsIsCheapPerSeat(t *testing.T) {
	// 50,000 holds of 2 seats each in a 100,000-seat event. An implementation
	// that scans the event (to count free seats, to look for a seat, to find
	// an owner) on every call does 5 billion steps and misses the cap.
	const seats = 100_000
	inv, _ := newInv(t)
	ids := make([]string, seats)
	for i := range ids {
		ids[i] = fmt.Sprintf("S%d", i)
	}
	mustAddEvent(t, inv, "arena", ids)
	withinD(t, 3*time.Second, "50,000 two-seat holds and confirms", func() {
		for i := 0; i < seats; i += 2 {
			h, err := inv.Hold("arena", "u", []string{ids[i], ids[i+1]}, time.Hour)
			if err != nil {
				panic(err)
			}
			if i%4 == 0 {
				if _, err := inv.Confirm(h.ID); err != nil {
					panic(err)
				}
			}
		}
	})
	wantSeat(t, inv, "arena", "S0", Booked)
	wantSeat(t, inv, "arena", "S2", Held)
}

func TestStage1_ConcurrentSingleSeatHasExactlyOneWinner(t *testing.T) {
	// Check-then-act: "is it free? ... mark it held" with a gap between the two
	// lets several goroutines pass the check.
	for round := 0; round < 20; round++ {
		inv, _ := newInv(t)
		mustAddEvent(t, inv, "show", seatRange("A", 3))
		const contenders = 200
		var wins, unavailable, other atomic.Int64
		var wg sync.WaitGroup
		start := make(chan struct{})
		for u := 0; u < contenders; u++ {
			wg.Add(1)
			go func(u int) {
				defer wg.Done()
				<-start
				_, err := inv.Hold("show", fmt.Sprintf("user%d", u), []string{"A2"}, time.Hour)
				switch {
				case err == nil:
					wins.Add(1)
				case errors.Is(err, ErrSeatUnavailable):
					unavailable.Add(1)
				default:
					other.Add(1)
				}
			}(u)
		}
		close(start)
		within(t, "contending Holds", wg.Wait)
		if wins.Load() != 1 || unavailable.Load() != contenders-1 || other.Load() != 0 {
			t.Fatalf("round %d: %d winners, %d ErrSeatUnavailable, %d other errors; want exactly 1, %d, 0: the seat was sold more than once",
				round, wins.Load(), unavailable.Load(), other.Load(), contenders-1)
		}
	}
}

func TestStage1_ConcurrentDisjointHoldsAreAllGranted(t *testing.T) {
	const n = 1_000
	inv, _ := newInv(t)
	ids := seatRange("S", n)
	mustAddEvent(t, inv, "arena", ids)
	var failures atomic.Int64
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			h, err := inv.Hold("arena", fmt.Sprintf("u%d", i), []string{ids[i]}, time.Hour)
			if err != nil {
				failures.Add(1)
				return
			}
			if i%2 == 0 {
				if _, err := inv.Confirm(h.ID); err != nil {
					failures.Add(1)
				}
			}
		}(i)
	}
	within(t, "disjoint concurrent Holds", wg.Wait)
	if failures.Load() != 0 {
		t.Fatalf("%d operations failed although every goroutine used its own seat: operations on different seats must not interfere", failures.Load())
	}
	for i := 0; i < n; i += 97 {
		want := Held
		if i%2 == 0 {
			want = Booked
		}
		wantSeat(t, inv, "arena", ids[i], want)
	}
}
