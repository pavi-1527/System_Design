package ticketing

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"reflect"
	"runtime"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// chargeLog stands in for the payment provider: every new booking is a charge.
type chargeLog struct {
	mu       sync.Mutex
	charged  []Booking
	reenter  func() // optional: called inside the hook to prove it runs outside every lock
	reentErr error
}

func (c *chargeLog) hook(b Booking) {
	if c.reenter != nil {
		c.reenter()
	}
	c.mu.Lock()
	c.charged = append(c.charged, b)
	c.mu.Unlock()
}

func (c *chargeLog) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.charged)
}

func mustInvariants(t *testing.T, inv *Inventory) {
	t.Helper()
	if err := inv.CheckInvariants(); err != nil {
		t.Fatalf("CheckInvariants found violations:\n%v", err)
	}
}

// ---- idempotent confirmation ----------------------------------------------------

func TestStage3_ConfirmWithKeyReplayReturnsTheSameBooking(t *testing.T) {
	charges := &chargeLog{}
	inv, clk := newInv(t, WithOnConfirmed(charges.hook))
	mustAddEvent(t, inv, "show", seatRange("A", 4))
	h := mustHold(t, inv, "show", "alice", []string{"A1", "A2"}, time.Minute)

	first, err := inv.ConfirmWithKey(h.ID, "pay-evt-1")
	if err != nil {
		t.Fatalf("ConfirmWithKey = %v", err)
	}
	clk.Advance(time.Hour) // the retry arrives long after the hold's TTL
	for i := 0; i < 3; i++ {
		again, err := inv.ConfirmWithKey(h.ID, "pay-evt-1")
		if err != nil {
			t.Fatalf("replay %d = %v, want the original booking (a booking never expires)", i, err)
		}
		if !reflect.DeepEqual(again, first) {
			t.Fatalf("replay %d returned %+v, want the identical booking %+v", i, again, first)
		}
	}
	bs, err := inv.Bookings("show")
	if err != nil || len(bs) != 1 {
		t.Fatalf("Bookings = %+v, %v; want exactly one booking after four identical callbacks", bs, err)
	}
	if n := charges.count(); n != 1 {
		t.Fatalf("customer charged %d times, want 1: a duplicate payment callback must not charge again", n)
	}
	wantSeat(t, inv, "show", "A1", Booked)
	mustInvariants(t, inv)
}

func TestStage3_ReplayDoesNotAliasTheStoredBooking(t *testing.T) {
	inv, _ := newInv(t)
	mustAddEvent(t, inv, "show", seatRange("A", 3))
	h := mustHold(t, inv, "show", "alice", []string{"A1", "A2"}, time.Minute)
	first, _ := inv.ConfirmWithKey(h.ID, "k")
	first.Seats[0] = "HACKED"
	again, err := inv.ConfirmWithKey(h.ID, "k")
	if err != nil || again.Seats[0] != "A1" {
		t.Fatalf("replay = %+v, %v; want seats [A1 A2]: the caller's slice must not be the stored one", again, err)
	}
	bs, _ := inv.Bookings("show")
	bs[0].Seats[1] = "HACKED"
	if bs2, _ := inv.Bookings("show"); bs2[0].Seats[1] != "A2" {
		t.Fatalf("Bookings returned the internal slice: stored booking now %+v", bs2[0])
	}
	mustInvariants(t, inv)
}

func TestStage3_ConcurrentDuplicateCallbacksChargeOnce(t *testing.T) {
	for round := 0; round < 10; round++ {
		charges := &chargeLog{}
		inv, _ := newInv(t, WithOnConfirmed(charges.hook))
		mustAddEvent(t, inv, "show", seatRange("A", 4))
		h := mustHold(t, inv, "show", "alice", []string{"A1", "A2"}, time.Hour)
		const dups = 64
		results := make([]Booking, dups)
		errs := make([]error, dups)
		var wg sync.WaitGroup
		start := make(chan struct{})
		for i := 0; i < dups; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				<-start
				results[i], errs[i] = inv.ConfirmWithKey(h.ID, "pay-evt-7")
			}(i)
		}
		close(start)
		within(t, "duplicate callbacks", wg.Wait)
		for i := range results {
			if errs[i] != nil {
				t.Fatalf("round %d: duplicate %d failed with %v; every retry of a successful confirm must succeed", round, i, errs[i])
			}
			if !reflect.DeepEqual(results[i], results[0]) {
				t.Fatalf("round %d: duplicates saw different bookings: %+v vs %+v", round, results[i], results[0])
			}
		}
		if n := charges.count(); n != 1 {
			t.Fatalf("round %d: charged %d times for one payment, want 1", round, n)
		}
		if bs, _ := inv.Bookings("show"); len(bs) != 1 {
			t.Fatalf("round %d: %d bookings, want 1", round, len(bs))
		}
	}
}

func TestStage3_SameKeyDifferentHoldIsRejected(t *testing.T) {
	charges := &chargeLog{}
	inv, _ := newInv(t, WithOnConfirmed(charges.hook))
	mustAddEvent(t, inv, "e1", seatRange("A", 3))
	mustAddEvent(t, inv, "e2", seatRange("A", 3))
	h1 := mustHold(t, inv, "e1", "alice", []string{"A1"}, time.Hour)
	h2 := mustHold(t, inv, "e1", "alice", []string{"A2"}, time.Hour)
	h3 := mustHold(t, inv, "e2", "alice", []string{"A1"}, time.Hour) // another event, same key space

	if _, err := inv.ConfirmWithKey(h1.ID, "K"); err != nil {
		t.Fatal(err)
	}
	for _, other := range []Hold{h2, h3} {
		_, err := inv.ConfirmWithKey(other.ID, "K")
		wantErr(t, "reusing key K for "+other.ID, err, ErrIdempotencyKeyReused)
	}
	wantSeat(t, inv, "e1", "A2", Held) // the rejected confirm must not have booked anything
	wantSeat(t, inv, "e2", "A1", Held)
	if n := charges.count(); n != 1 {
		t.Fatalf("charged %d times, want 1", n)
	}
	// The other holds are still perfectly confirmable with their own keys.
	if _, err := inv.ConfirmWithKey(h2.ID, "K2"); err != nil {
		t.Fatalf("ConfirmWithKey with a fresh key = %v", err)
	}
	mustInvariants(t, inv)
}

func TestStage3_ConcurrentSameKeyDifferentHoldsHaveOneWinner(t *testing.T) {
	// Two payment callbacks carry the same key but name different holds
	// (a buggy upstream, or a replayed key): exactly one may book.
	for round := 0; round < 20; round++ {
		charges := &chargeLog{}
		inv, _ := newInv(t, WithOnConfirmed(charges.hook))
		mustAddEvent(t, inv, "e1", seatRange("A", 3))
		mustAddEvent(t, inv, "e2", seatRange("A", 3))
		holds := []Hold{
			mustHold(t, inv, "e1", "alice", []string{"A1"}, time.Hour),
			mustHold(t, inv, "e2", "bob", []string{"A1"}, time.Hour),
		}
		var wins, reused, other atomic.Int64
		var wg sync.WaitGroup
		start := make(chan struct{})
		for i := 0; i < 32; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				<-start
				_, err := inv.ConfirmWithKey(holds[i%2].ID, "SHARED")
				switch {
				case err == nil:
					wins.Add(1)
				case errors.Is(err, ErrIdempotencyKeyReused):
					reused.Add(1)
				default:
					other.Add(1)
				}
			}(i)
		}
		close(start)
		within(t, "racing confirmations", wg.Wait)
		if other.Load() != 0 || wins.Load() == 0 || wins.Load()+reused.Load() != 32 {
			t.Fatalf("round %d: wins %d, reused %d, other %d", round, wins.Load(), reused.Load(), other.Load())
		}
		if n := charges.count(); n != 1 {
			t.Fatalf("round %d: charged %d times for one key, want 1 (two holds were confirmed under one key)", round, n)
		}
		mustInvariants(t, inv)
	}
}

func TestStage3_KeyAndHoldStateMatrix(t *testing.T) {
	inv, clk := newInv(t)
	mustAddEvent(t, inv, "show", seatRange("A", 6))
	keyed := mustHold(t, inv, "show", "alice", []string{"A1"}, time.Hour)
	plain := mustHold(t, inv, "show", "bob", []string{"A2"}, time.Hour)
	doomed := mustHold(t, inv, "show", "carol", []string{"A3"}, time.Minute)

	if _, err := inv.ConfirmWithKey(keyed.ID, ""); !errors.Is(err, ErrInvalidRequest) {
		t.Errorf("empty key = %v, want ErrInvalidRequest", err)
	}
	if _, err := inv.ConfirmWithKey("show#404", "x"); !errors.Is(err, ErrUnknownHold) {
		t.Errorf("unknown hold = %v, want ErrUnknownHold", err)
	}
	if _, err := inv.ConfirmWithKey("garbage", "x"); !errors.Is(err, ErrUnknownHold) {
		t.Errorf("malformed hold ID = %v, want ErrUnknownHold", err)
	}
	// An unknown hold must not bind the key it arrived with.
	if _, err := inv.ConfirmWithKey(keyed.ID, "x"); err != nil {
		t.Errorf("key x was burned by a call on an unknown hold: %v", err)
	}

	// Different key on an already confirmed hold: a second payment for the same seats.
	_, err := inv.ConfirmWithKey(keyed.ID, "other-key")
	wantErr(t, "new key on a confirmed hold", err, ErrAlreadyConfirmed)
	// Keyless Confirm on a keyed booking is not a replay either.
	_, err = inv.Confirm(keyed.ID)
	wantErr(t, "keyless Confirm on a keyed booking", err, ErrAlreadyConfirmed)
	// Keyed call on a hold already confirmed without a key.
	if _, err := inv.Confirm(plain.ID); err != nil {
		t.Fatal(err)
	}
	_, err = inv.ConfirmWithKey(plain.ID, "late-key")
	wantErr(t, "keyed call on a keyless booking", err, ErrAlreadyConfirmed)

	// A key's first use failing keeps the key bound to that hold.
	clk.Advance(time.Minute)
	_, err = inv.ConfirmWithKey(doomed.ID, "doomed-key")
	wantErr(t, "keyed confirm of an expired hold", err, ErrHoldExpired)
	_, err = inv.ConfirmWithKey(doomed.ID, "doomed-key")
	wantErr(t, "retry of the failed keyed confirm", err, ErrHoldExpired)
	fresh := mustHold(t, inv, "show", "carol", []string{"A3"}, time.Hour)
	_, err = inv.ConfirmWithKey(fresh.ID, "doomed-key")
	wantErr(t, "key bound to the expired hold, used on a new hold", err, ErrIdempotencyKeyReused)
	mustInvariants(t, inv)
}

func TestStage3_ConfirmHookRunsOutsideLocks(t *testing.T) {
	var inv *Inventory
	charges := &chargeLog{}
	charges.reenter = func() {
		if _, err := inv.Availability("show"); err != nil {
			charges.reentErr = err
		}
		if _, err := inv.Hold("show", "hook-user", []string{"A9"}, time.Hour); err != nil && charges.reentErr == nil {
			charges.reentErr = err
		}
	}
	inv, _ = newInv(t, WithOnConfirmed(charges.hook))
	mustAddEvent(t, inv, "show", seatRange("A", 9))
	h := mustHold(t, inv, "show", "alice", []string{"A1"}, time.Hour)
	within(t, "Confirm whose hook calls back into the inventory", func() {
		if _, err := inv.ConfirmWithKey(h.ID, "k"); err != nil {
			t.Error(err)
		}
	})
	if charges.reentErr != nil {
		t.Fatalf("hook's re-entrant calls failed: %v", charges.reentErr)
	}
	wantSeat(t, inv, "show", "A9", Held)
	// A replay must not run the hook again.
	if _, err := inv.ConfirmWithKey(h.ID, "k"); err != nil {
		t.Fatal(err)
	}
	if charges.count() != 1 {
		t.Fatalf("hook ran %d times, want 1 (replays are not new bookings)", charges.count())
	}
}

// ---- contention ------------------------------------------------------------------

func TestStage3_OppositeOrderRequestsDoNotDeadlock(t *testing.T) {
	// Two goroutine families want the same pair of seats in opposite orders.
	// A design that locks seat-by-seat in request order deadlocks here
	// (A holds s1 waiting for s2 while B holds s2 waiting for s1). The test
	// also checks exclusivity: while one hold is active no other may exist.
	inv, _ := newInv(t)
	mustAddEvent(t, inv, "show", []string{"s1", "s2", "s3", "s4"})
	const workers, rounds = 32, 300
	pairs := [][]string{{"s1", "s2"}, {"s2", "s1"}, {"s3", "s4"}, {"s4", "s3"}}
	var active [2]atomic.Int64 // concurrent holders per pair
	var violations, wins atomic.Int64
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			seats := pairs[w%4]
			pair := (w % 4) / 2
			for i := 0; i < rounds; i++ {
				h, err := inv.Hold("show", fmt.Sprintf("u%d", w), seats, time.Hour)
				if err != nil {
					if !errors.Is(err, ErrSeatUnavailable) {
						violations.Add(1)
					}
					runtime.Gosched()
					continue
				}
				wins.Add(1)
				if active[pair].Add(1) != 1 {
					violations.Add(1) // two live holds on the same seats
				}
				active[pair].Add(-1)
				if err := inv.Release(h.ID); err != nil {
					violations.Add(1)
				}
			}
		}(w)
	}
	within(t, "opposite-order multi-seat holds (deadlock?)", wg.Wait)
	if violations.Load() != 0 {
		t.Fatalf("%d violations: a seat pair had two holders at once, or an operation failed with an unexpected error", violations.Load())
	}
	if wins.Load() == 0 {
		t.Fatal("no Hold ever succeeded")
	}
	mustInvariants(t, inv)
}

func TestStage3_RandomOverlappingRequestsEachSeatHasOneWinner(t *testing.T) {
	const seats, workers = 60, 300
	charges := &chargeLog{}
	inv, _ := newInv(t, WithOnConfirmed(charges.hook))
	ids := seatRange("S", seats)
	mustAddEvent(t, inv, "arena", ids)

	type win struct {
		seats     []string
		confirmed bool
	}
	var mu sync.Mutex
	var wins []win
	var failures atomic.Int64
	var wg sync.WaitGroup
	start := make(chan struct{})
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			rng := rand.New(rand.NewSource(int64(w)))
			// 1-4 random seats, in random order, so requests overlap and collide in both directions.
			want := make([]string, 1+rng.Intn(4))
			for i, p := range rng.Perm(seats)[:len(want)] {
				want[i] = ids[p]
			}
			<-start
			h, err := inv.Hold("arena", fmt.Sprintf("u%d", w), want, time.Hour)
			if err != nil {
				if !errors.Is(err, ErrSeatUnavailable) {
					failures.Add(1)
				}
				return
			}
			confirmed := w%2 == 0
			if confirmed {
				if _, err := inv.Confirm(h.ID); err != nil {
					failures.Add(1)
					return
				}
			}
			mu.Lock()
			wins = append(wins, win{want, confirmed})
			mu.Unlock()
		}(w)
	}
	close(start)
	within(t, "overlapping multi-seat requests", wg.Wait)
	if failures.Load() != 0 {
		t.Fatalf("%d operations failed with an error other than ErrSeatUnavailable", failures.Load())
	}

	owner := map[string]int{}
	var heldWant, bookedWant int
	for i, w := range wins {
		for _, s := range w.seats {
			if prev, dup := owner[s]; dup {
				t.Fatalf("seat %s was granted to winners %d and %d: oversold", s, prev, i)
			}
			owner[s] = i
			if w.confirmed {
				bookedWant++
			} else {
				heldWant++
			}
		}
	}
	a, err := inv.Availability("arena")
	if err != nil {
		t.Fatal(err)
	}
	if a.Held != heldWant || a.Booked != bookedWant || a.Available != seats-heldWant-bookedWant {
		t.Fatalf("Availability = %+v, but winners hold %d seats and booked %d of %d", a, heldWant, bookedWant, seats)
	}
	for s, i := range owner {
		want := Held
		if wins[i].confirmed {
			want = Booked
		}
		if a.Seats[s] != want {
			t.Fatalf("seat %s is %v, want %v", s, a.Seats[s], want)
		}
	}
	mustInvariants(t, inv)
}

func TestStage3_SnapshotsStayConsistentDuringTheStorm(t *testing.T) {
	// While writers hammer one event, an observer keeps taking snapshots:
	// Available+Held+Booked == Total must hold in every one of them.
	const seats = 40
	inv, _ := newInv(t)
	ids := seatRange("S", seats)
	mustAddEvent(t, inv, "arena", ids)
	var stop atomic.Bool
	var bad atomic.Value
	var snapshots atomic.Int64
	var obs sync.WaitGroup
	obs.Add(1)
	go func() {
		defer obs.Done()
		for !stop.Load() {
			a, err := inv.Availability("arena")
			if err != nil {
				bad.Store(err.Error())
				return
			}
			held, booked, avail := 0, 0, 0
			for _, st := range a.Seats {
				switch st {
				case Held:
					held++
				case Booked:
					booked++
				case Available:
					avail++
				}
			}
			if a.Available+a.Held+a.Booked != a.Total || held != a.Held || booked != a.Booked || avail != a.Available || a.Total != seats {
				bad.Store(fmt.Sprintf("torn snapshot: total %d, available %d, held %d, booked %d, but the per-seat map says %d/%d/%d", a.Total, a.Available, a.Held, a.Booked, avail, held, booked))
				return
			}
			snapshots.Add(1)
		}
	}()
	var wg sync.WaitGroup
	for w := 0; w < 16; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			rng := rand.New(rand.NewSource(int64(w) + 100))
			for i := 0; i < 400; i++ {
				req := []string{ids[rng.Intn(seats)], ids[rng.Intn(seats)]}
				if req[0] == req[1] {
					req = req[:1]
				}
				h, err := inv.Hold("arena", fmt.Sprintf("u%d", w), req, time.Hour)
				if err != nil {
					continue
				}
				switch rng.Intn(3) {
				case 0:
					_, _ = inv.Confirm(h.ID)
				case 1:
					_ = inv.Release(h.ID)
				}
			}
		}(w)
	}
	within(t, "writers", wg.Wait)
	stop.Store(true)
	within(t, "observer", obs.Wait)
	if v := bad.Load(); v != nil {
		t.Fatal(v)
	}
	if snapshots.Load() == 0 {
		t.Fatal("observer never completed a snapshot")
	}
	mustInvariants(t, inv)
}

func TestStage3_ConfirmRacingReleaseHasOneOutcome(t *testing.T) {
	for round := 0; round < 50; round++ {
		inv, _ := newInv(t)
		mustAddEvent(t, inv, "show", seatRange("A", 2))
		h := mustHold(t, inv, "show", "alice", []string{"A1", "A2"}, time.Hour)
		var confirmErr, releaseErr error
		var wg sync.WaitGroup
		start := make(chan struct{})
		wg.Add(2)
		go func() { defer wg.Done(); <-start; _, confirmErr = inv.Confirm(h.ID) }()
		go func() { defer wg.Done(); <-start; releaseErr = inv.Release(h.ID) }()
		close(start)
		within(t, "Confirm racing Release", wg.Wait)
		switch {
		case confirmErr == nil && errors.Is(releaseErr, ErrAlreadyConfirmed):
			wantSeat(t, inv, "show", "A1", Booked)
		case releaseErr == nil && errors.Is(confirmErr, ErrUnknownHold):
			wantSeat(t, inv, "show", "A1", Available)
		default:
			t.Fatalf("round %d: Confirm = %v, Release = %v; exactly one must win and the loser must say so", round, confirmErr, releaseErr)
		}
		mustInvariants(t, inv)
	}
}

func TestStage3_ChaosKeepsInvariants(t *testing.T) {
	// Everything at once: holds, confirms (keyed, replayed and not), releases,
	// extensions, a reaper on a tick channel, Sweep calls and a clock that
	// jumps forward. Afterwards the books must balance from the outside too.
	const seats, workers, ops = 80, 16, 600
	baseline := runtime.NumGoroutine()
	charges := &chargeLog{}
	inv, clk := newInv(t, WithOnConfirmed(charges.hook), WithMaxActiveHoldsPerUser(3))
	ids := seatRange("S", seats)
	mustAddEvent(t, inv, "arena", ids)
	mustAddEvent(t, inv, "side", seatRange("X", 10))

	ctx, cancel := context.WithCancel(context.Background())
	ticks := make(chan time.Time)
	reaperDone := make(chan struct{})
	go func() { defer close(reaperDone); inv.RunReaper(ctx, ticks) }()

	var stop atomic.Bool
	var bg sync.WaitGroup
	bg.Add(1)
	go func() { // clock + reaper driver
		defer bg.Done()
		for !stop.Load() {
			clk.Advance(150 * time.Millisecond)
			select {
			case ticks <- clk.Now():
			case <-time.After(time.Millisecond):
			}
			runtime.Gosched()
		}
	}()

	var okBooks atomic.Int64
	var unexpected atomic.Value
	note := func(op string, err error) {
		unexpected.CompareAndSwap(nil, fmt.Sprintf("%s: %v", op, err))
	}
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			rng := rand.New(rand.NewSource(int64(w) + 7))
			user := fmt.Sprintf("u%d", w%6) // several workers share a user: the limit gets exercised
			for i := 0; i < ops; i++ {
				req := make([]string, 1+rng.Intn(3))
				for j, p := range rng.Perm(seats)[:len(req)] {
					req[j] = ids[p]
				}
				ev := "arena"
				if rng.Intn(10) == 0 {
					ev, req = "side", []string{fmt.Sprintf("X%d", 1+rng.Intn(10))}
				}
				h, err := inv.Hold(ev, user, req, time.Duration(1+rng.Intn(4))*time.Second)
				if err != nil {
					if !errors.Is(err, ErrSeatUnavailable) && !errors.Is(err, ErrTooManyHolds) {
						note("Hold", err)
					}
					continue
				}
				switch rng.Intn(5) {
				case 0, 1:
					key := fmt.Sprintf("k-%d-%d", w, i)
					_, err := inv.ConfirmWithKey(h.ID, key)
					if err == nil {
						okBooks.Add(1)
						if again, err := inv.ConfirmWithKey(h.ID, key); err != nil || again.HoldID != h.ID {
							note("replay", err)
						}
					} else if !errors.Is(err, ErrHoldExpired) {
						note("ConfirmWithKey", err)
					}
				case 2:
					if err := inv.Release(h.ID); err != nil && !errors.Is(err, ErrHoldExpired) {
						note("Release", err)
					}
				case 3:
					if _, err := inv.Extend(h.ID, 2*time.Second); err != nil && !errors.Is(err, ErrHoldExpired) {
						note("Extend", err)
					}
				}
				if i%50 == 0 {
					inv.Sweep()
				}
			}
		}(w)
	}
	within(t, "chaos workload", wg.Wait)
	stop.Store(true)
	within(t, "clock driver", bg.Wait)
	cancel()
	select {
	case <-reaperDone:
	case <-time.After(testTimeout):
		t.Fatal("RunReaper did not stop")
	}
	if v := unexpected.Load(); v != nil {
		t.Fatalf("unexpected error: %v", v)
	}

	clk.Advance(24 * time.Hour)
	inv.Sweep()
	mustInvariants(t, inv)
	for _, ev := range []string{"arena", "side"} {
		bs, err := inv.Bookings(ev)
		if err != nil {
			t.Fatal(err)
		}
		seen := map[string]bool{}
		for _, b := range bs {
			for _, s := range b.Seats {
				if seen[s] {
					t.Fatalf("seat %s/%s appears in two bookings", ev, s)
				}
				seen[s] = true
			}
		}
		a, _ := inv.Availability(ev)
		if a.Held != 0 || a.Booked != len(seen) || a.Available+a.Booked != a.Total {
			t.Fatalf("%s after everything expired: %+v, bookings cover %d seats", ev, a, len(seen))
		}
	}
	if want, got := int(okBooks.Load()), charges.count(); want != got {
		t.Fatalf("%d successful first confirmations but %d charges", want, got)
	}
	var charged []string
	charges.mu.Lock()
	for _, b := range charges.charged {
		charged = append(charged, b.ID)
	}
	charges.mu.Unlock()
	sort.Strings(charged)
	for i := 1; i < len(charged); i++ {
		if charged[i] == charged[i-1] {
			t.Fatalf("booking %s was charged twice", charged[i])
		}
	}
	waitForGoroutines(t, baseline)
}
