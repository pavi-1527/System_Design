package ticketing

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

func TestStage2_HoldExpiresAtExactlyTTL(t *testing.T) {
	inv, clk := newInv(t)
	mustAddEvent(t, inv, "show", seatRange("A", 3))
	mustHold(t, inv, "show", "alice", []string{"A1", "A2"}, 10*time.Second)

	clk.Advance(10*time.Second - time.Nanosecond)
	wantSeat(t, inv, "show", "A1", Held)
	if _, err := inv.Hold("show", "bob", []string{"A1"}, time.Hour); !errors.Is(err, ErrSeatUnavailable) {
		t.Fatalf("1ns before expiry, Hold of the seat = %v, want ErrSeatUnavailable", err)
	}

	clk.Advance(time.Nanosecond) // now == ExpiresAt: expired means now >= expiry
	wantSeat(t, inv, "show", "A1", Available)
	wantSeat(t, inv, "show", "A2", Available)
	mustHold(t, inv, "show", "bob", []string{"A1", "A2"}, time.Hour)
}

func TestStage2_ExpiryIsLazy_NoSweepNeeded(t *testing.T) {
	// Nobody calls Sweep and no reaper runs: an expired hold must never block
	// anyone, including a multi-seat request that overlaps it only partly.
	inv, clk := newInv(t)
	mustAddEvent(t, inv, "show", seatRange("A", 6))
	mustHold(t, inv, "show", "alice", []string{"A1", "A2", "A3"}, time.Minute)
	mustHold(t, inv, "show", "carol", []string{"A5"}, time.Hour)
	clk.Advance(time.Minute)
	mustHold(t, inv, "show", "bob", []string{"A3", "A4"}, time.Hour) // A3 was alice's, now expired
	wantSeat(t, inv, "show", "A1", Available)                        // her other seats came back with it
	wantSeat(t, inv, "show", "A2", Available)
	if _, err := inv.Hold("show", "bob", []string{"A5"}, time.Hour); !errors.Is(err, ErrSeatUnavailable) {
		t.Fatalf("Hold of carol's live seat = %v, want ErrSeatUnavailable: expiry must only free expired holds", err)
	}
}

func TestStage2_ConfirmAtAndAfterExpiryFails(t *testing.T) {
	t.Run("1ns before expiry succeeds", func(t *testing.T) {
		inv, clk := newInv(t)
		mustAddEvent(t, inv, "show", seatRange("A", 2))
		h := mustHold(t, inv, "show", "alice", []string{"A1"}, time.Minute)
		clk.Advance(time.Minute - time.Nanosecond)
		if _, err := inv.Confirm(h.ID); err != nil {
			t.Fatalf("Confirm 1ns before expiry = %v, want success", err)
		}
		wantSeat(t, inv, "show", "A1", Booked)
	})
	t.Run("exactly at expiry fails", func(t *testing.T) {
		inv, clk := newInv(t)
		mustAddEvent(t, inv, "show", seatRange("A", 2))
		h := mustHold(t, inv, "show", "alice", []string{"A1"}, time.Minute)
		clk.Advance(time.Minute)
		_, err := inv.Confirm(h.ID)
		wantErr(t, "Confirm at now == ExpiresAt", err, ErrHoldExpired)
		wantSeat(t, inv, "show", "A1", Available)
	})
	t.Run("long after expiry fails even if nothing reaped it", func(t *testing.T) {
		inv, clk := newInv(t)
		mustAddEvent(t, inv, "show", seatRange("A", 2))
		h := mustHold(t, inv, "show", "alice", []string{"A1"}, time.Minute)
		clk.Advance(time.Hour)
		_, err := inv.Confirm(h.ID)
		wantErr(t, "Confirm an hour late", err, ErrHoldExpired)
	})
}

func TestStage2_ExpiredHoldErrorsAreExactWhetherOrNotSwept(t *testing.T) {
	// Callers (a payment callback arriving late) must get ErrHoldExpired, not
	// ErrUnknownHold, no matter whether a reaper got there first.
	inv, clk := newInv(t)
	mustAddEvent(t, inv, "show", seatRange("A", 4))
	swept := mustHold(t, inv, "show", "alice", []string{"A1"}, time.Minute)
	lazy := mustHold(t, inv, "show", "bob", []string{"A2"}, time.Minute)
	clk.Advance(time.Minute)
	if n := inv.Sweep(); n != 2 {
		t.Fatalf("Sweep = %d, want 2", n)
	}
	for _, h := range []Hold{swept, lazy} {
		_, err := inv.Confirm(h.ID)
		wantErr(t, "Confirm "+h.ID, err, ErrHoldExpired)
		wantErr(t, "Release "+h.ID, inv.Release(h.ID), ErrHoldExpired)
		_, err = inv.Extend(h.ID, time.Hour)
		wantErr(t, "Extend "+h.ID, err, ErrHoldExpired)
	}
	wantSeat(t, inv, "show", "A1", Available)
}

func TestStage2_SweepFreesExpiredOnly(t *testing.T) {
	inv, clk := newInv(t)
	mustAddEvent(t, inv, "e1", seatRange("A", 4))
	mustAddEvent(t, inv, "e2", seatRange("A", 4))
	mustHold(t, inv, "e1", "u1", []string{"A1", "A2"}, time.Minute) // expires
	mustHold(t, inv, "e2", "u2", []string{"A1"}, time.Minute)       // expires
	mustHold(t, inv, "e1", "u3", []string{"A3"}, time.Hour)         // live
	booked := mustHold(t, inv, "e2", "u4", []string{"A2"}, time.Minute)
	if _, err := inv.Confirm(booked.ID); err != nil {
		t.Fatal(err)
	}

	if n := inv.Sweep(); n != 0 {
		t.Fatalf("Sweep before anything expired = %d, want 0", n)
	}
	clk.Advance(2 * time.Minute)
	if n := inv.Sweep(); n != 2 {
		t.Fatalf("Sweep = %d, want 2 holds expired (counted per hold, across events)", n)
	}
	if n := inv.Sweep(); n != 0 {
		t.Fatalf("second Sweep = %d, want 0", n)
	}
	wantSeat(t, inv, "e1", "A1", Available)
	wantSeat(t, inv, "e1", "A2", Available)
	wantSeat(t, inv, "e1", "A3", Held)
	wantSeat(t, inv, "e2", "A1", Available)
	// The reaper must never free a booked seat, however old its hold is.
	clk.Advance(24 * time.Hour)
	inv.Sweep()
	wantSeat(t, inv, "e2", "A2", Booked)
}

func TestStage2_ReleasedAndConfirmedHoldsAreNotExpiredLater(t *testing.T) {
	// A hold that left the "active" set must not be freed again when its old
	// deadline passes: the seat may belong to someone else by then.
	inv, clk := newInv(t)
	mustAddEvent(t, inv, "show", seatRange("A", 3))
	old := mustHold(t, inv, "show", "alice", []string{"A1"}, time.Minute)
	if err := inv.Release(old.ID); err != nil {
		t.Fatal(err)
	}
	next := mustHold(t, inv, "show", "bob", []string{"A1"}, time.Hour) // new owner of A1
	clk.Advance(time.Minute)                                           // alice's original deadline
	if n := inv.Sweep(); n != 0 {
		t.Fatalf("Sweep = %d, want 0: alice's released hold must not expire again", n)
	}
	wantSeat(t, inv, "show", "A1", Held)
	if _, err := inv.Confirm(next.ID); err != nil {
		t.Fatalf("bob's Confirm = %v: his hold was freed by someone else's stale expiry entry", err)
	}
}

func TestStage2_PerUserActiveHoldLimit(t *testing.T) {
	inv, clk := newInv(t, WithMaxActiveHoldsPerUser(2))
	mustAddEvent(t, inv, "show", seatRange("A", 10))
	mustAddEvent(t, inv, "other", seatRange("A", 10))
	h1 := mustHold(t, inv, "show", "alice", []string{"A1"}, time.Minute)
	h2 := mustHold(t, inv, "show", "alice", []string{"A2"}, time.Hour)
	_, err := inv.Hold("show", "alice", []string{"A3"}, time.Hour)
	wantErr(t, "third active hold", err, ErrTooManyHolds)
	wantSeat(t, inv, "show", "A3", Available)

	mustHold(t, inv, "show", "bob", []string{"A3"}, time.Hour)    // the limit is per user
	mustHold(t, inv, "other", "alice", []string{"A1"}, time.Hour) // and per event

	// A refused request must not consume quota.
	if _, err := inv.Hold("show", "carol", []string{"A1"}, time.Hour); !errors.Is(err, ErrSeatUnavailable) {
		t.Fatalf("Hold of a taken seat = %v, want ErrSeatUnavailable", err)
	}
	mustHold(t, inv, "show", "carol", []string{"A4"}, time.Hour)
	mustHold(t, inv, "show", "carol", []string{"A5"}, time.Hour)

	// Quota comes back through release, confirm and expiry.
	if err := inv.Release(h2.ID); err != nil {
		t.Fatal(err)
	}
	h3 := mustHold(t, inv, "show", "alice", []string{"A6"}, time.Hour)
	if _, err := inv.Hold("show", "alice", []string{"A7"}, time.Hour); !errors.Is(err, ErrTooManyHolds) {
		t.Fatalf("Hold = %v, want ErrTooManyHolds (alice has h1 and h3 active)", err)
	}
	if _, err := inv.Confirm(h3.ID); err != nil {
		t.Fatal(err)
	}
	mustHold(t, inv, "show", "alice", []string{"A7"}, time.Hour) // a confirmed hold is no longer "active"
	if _, err := inv.Hold("show", "alice", []string{"A8"}, time.Hour); !errors.Is(err, ErrTooManyHolds) {
		t.Fatalf("Hold = %v, want ErrTooManyHolds (h1 and A7 are active)", err)
	}
	clk.Advance(time.Minute) // h1 expires; no Sweep, no other call touched it
	mustHold(t, inv, "show", "alice", []string{"A8"}, time.Hour)
	_ = h1
}

func TestStage2_AvailabilitySnapshotNeverCountsExpiredHolds(t *testing.T) {
	inv, clk := newInv(t)
	mustAddEvent(t, inv, "show", seatRange("A", 10))
	short := mustHold(t, inv, "show", "alice", []string{"A1", "A2", "A3"}, time.Minute)
	long := mustHold(t, inv, "show", "bob", []string{"A4", "A5"}, time.Hour)
	if _, err := inv.Confirm(long.ID); err != nil {
		t.Fatal(err)
	}
	mustHold(t, inv, "show", "carol", []string{"A6"}, time.Hour)

	a, err := inv.Availability("show")
	if err != nil {
		t.Fatal(err)
	}
	if a.EventID != "show" || a.Total != 10 || a.Available != 4 || a.Held != 4 || a.Booked != 2 {
		t.Fatalf("Availability = %+v, want total 10, available 4, held 4, booked 2", a)
	}

	clk.Advance(time.Minute) // no Sweep: the snapshot itself must apply expiry
	a, err = inv.Availability("show")
	if err != nil {
		t.Fatal(err)
	}
	if a.Total != 10 || a.Available != 7 || a.Held != 1 || a.Booked != 2 {
		t.Fatalf("Availability after expiry = %+v, want available 7, held 1, booked 2 (expired holds are not held)", a)
	}
	if a.Seats["A1"] != Available || a.Seats["A4"] != Booked || a.Seats["A6"] != Held || len(a.Seats) != 10 {
		t.Fatalf("Availability.Seats = %v: wrong per-seat states", a.Seats)
	}
	_ = short

	a.Seats["A1"] = Booked // the snapshot belongs to the caller
	wantSeat(t, inv, "show", "A1", Available)
	if _, err := inv.Availability("nope"); !errors.Is(err, ErrUnknownEvent) {
		t.Fatalf("Availability of an unknown event = %v, want ErrUnknownEvent", err)
	}
}

func TestStage2_ExtendOnce(t *testing.T) {
	inv, clk := newInv(t)
	mustAddEvent(t, inv, "show", seatRange("A", 3))
	h := mustHold(t, inv, "show", "alice", []string{"A1"}, time.Minute)

	ext, err := inv.Extend(h.ID, 5*time.Minute)
	if err != nil {
		t.Fatalf("Extend = %v", err)
	}
	if want := h.ExpiresAt.Add(5 * time.Minute); !ext.ExpiresAt.Equal(want) || ext.ID != h.ID {
		t.Fatalf("extended hold = %+v, want ExpiresAt %v (old expiry + 5m) and the same ID", ext, want)
	}
	_, err = inv.Extend(h.ID, time.Minute)
	wantErr(t, "second Extend", err, ErrAlreadyExtended)
	_, err = inv.Extend(h.ID, 0)
	wantErr(t, "Extend by zero", err, ErrInvalidRequest)

	// The reaper must honour the NEW deadline, not the one it queued first.
	clk.Advance(time.Minute) // the original expiry
	if n := inv.Sweep(); n != 0 {
		t.Fatalf("Sweep at the original expiry = %d, want 0: the hold was extended", n)
	}
	wantSeat(t, inv, "show", "A1", Held)
	clk.Advance(5*time.Minute - time.Nanosecond)
	wantSeat(t, inv, "show", "A1", Held)
	clk.Advance(time.Nanosecond)
	wantSeat(t, inv, "show", "A1", Available)
	_, err = inv.Extend(h.ID, time.Hour)
	wantErr(t, "Extend after expiry", err, ErrHoldExpired)
}

func TestStage2_ExtendReordersWhoExpiresFirst(t *testing.T) {
	// alice's hold is the earliest to expire. Extending it must not leave it
	// at the front of the line: bob's hold expires first now, and an expired
	// hold must never hide behind a live one.
	inv, clk := newInv(t)
	mustAddEvent(t, inv, "show", seatRange("A", 4))
	alice := mustHold(t, inv, "show", "alice", []string{"A1"}, time.Minute)
	mustHold(t, inv, "show", "bob", []string{"A2"}, 2*time.Minute)
	if _, err := inv.Extend(alice.ID, 10*time.Minute); err != nil {
		t.Fatal(err)
	}
	clk.Advance(2 * time.Minute)
	if n := inv.Sweep(); n != 1 {
		t.Fatalf("Sweep = %d, want 1: bob's hold expired, alice's was extended (is the expiry index re-ordered after Extend?)", n)
	}
	wantSeat(t, inv, "show", "A2", Available)
	wantSeat(t, inv, "show", "A1", Held)
}

func TestStage2_ExtendRejectsConfirmedAndUnknownHolds(t *testing.T) {
	inv, _ := newInv(t)
	mustAddEvent(t, inv, "show", seatRange("A", 3))
	h := mustHold(t, inv, "show", "alice", []string{"A1"}, time.Minute)
	if _, err := inv.Confirm(h.ID); err != nil {
		t.Fatal(err)
	}
	_, err := inv.Extend(h.ID, time.Minute)
	wantErr(t, "Extend of a confirmed hold", err, ErrAlreadyConfirmed)
	_, err = inv.Extend("show#404", time.Minute)
	wantErr(t, "Extend of an unknown hold", err, ErrUnknownHold)
}

func TestStage2_ReaperSweepsOnEachTick(t *testing.T) {
	inv, clk := newInv(t)
	mustAddEvent(t, inv, "show", seatRange("A", 3))
	mustHold(t, inv, "show", "alice", []string{"A1"}, time.Minute)

	baseline := runtime.NumGoroutine()
	ctx, cancel := context.WithCancel(context.Background())
	ticks := make(chan time.Time) // unbuffered: a send completes only when the reaper receives it
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		inv.RunReaper(ctx, ticks)
	}()
	send := func() {
		t.Helper()
		select {
		case ticks <- clk.Now():
		case <-time.After(testTimeout):
			t.Fatal("RunReaper did not receive a tick (is it reading the ticks channel?)")
		}
	}

	send()
	send() // returns only after the first tick's Sweep has finished
	if n := inv.Sweep(); n != 0 {
		t.Fatalf("manual Sweep = %d, want 0 (nothing has expired yet)", n)
	}
	clk.Advance(time.Minute)
	send()
	send() // the reaper handled the tick for the expired hold; we never touched the inventory
	// Peek without letting a lazy path hide a missing reaper: the hold is
	// gone, so a manual Sweep has nothing left to do.
	if n := inv.Sweep(); n != 0 {
		t.Fatalf("manual Sweep = %d, want 0: the reaper should already have swept the expired hold on its tick", n)
	}

	cancel()
	select {
	case <-stopped:
	case <-time.After(testTimeout):
		t.Fatal("RunReaper did not return after its context was cancelled")
	}
	waitForGoroutines(t, baseline)

	// Closing the tick channel also stops it.
	done := make(chan struct{})
	closed := make(chan time.Time)
	go func() { inv.RunReaper(context.Background(), closed); close(done) }()
	close(closed)
	select {
	case <-done:
	case <-time.After(testTimeout):
		t.Fatal("RunReaper did not return after the tick channel was closed")
	}
}

func TestStage2_ConfirmRacingSweepAtTheBoundary(t *testing.T) {
	// With the clock frozen, the outcome of each hold is fully determined by
	// the clock alone, however Confirm and Sweep interleave: at exactly
	// expiry Confirm must lose; 1ns earlier it must win and Sweep must not
	// free what was booked.
	for _, tc := range []struct {
		name      string
		advance   time.Duration
		wantBooks int
	}{
		{"at expiry", time.Minute, 0},
		{"1ns before expiry", time.Minute - time.Nanosecond, 100},
	} {
		t.Run(tc.name, func(t *testing.T) {
			const holds = 100
			inv, clk := newInv(t)
			ids := seatRange("S", holds)
			mustAddEvent(t, inv, "arena", ids)
			hs := make([]Hold, holds)
			for i := range hs {
				hs[i] = mustHold(t, inv, "arena", fmt.Sprintf("u%d", i), []string{ids[i]}, time.Minute)
			}
			clk.Advance(tc.advance)
			var booked, expired, other atomic.Int64
			var wg sync.WaitGroup
			start := make(chan struct{})
			for g := 0; g < 8; g++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					<-start
					for _, h := range hs {
						_, err := inv.Confirm(h.ID)
						switch {
						case err == nil:
							booked.Add(1)
						case errors.Is(err, ErrHoldExpired):
							expired.Add(1)
						case errors.Is(err, ErrAlreadyConfirmed):
						default:
							other.Add(1)
						}
					}
				}()
			}
			for g := 0; g < 4; g++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					<-start
					for i := 0; i < 50; i++ {
						inv.Sweep()
					}
				}()
			}
			close(start)
			within(t, "racing Confirm and Sweep", wg.Wait)
			if int(booked.Load()) != tc.wantBooks || other.Load() != 0 {
				t.Fatalf("%d bookings (want %d), %d unexpected errors: at the boundary the clock alone decides, not the interleaving",
					booked.Load(), tc.wantBooks, other.Load())
			}
			a, err := inv.Availability("arena")
			if err != nil {
				t.Fatal(err)
			}
			if a.Booked != tc.wantBooks || a.Held != 0 || a.Available != holds-tc.wantBooks {
				t.Fatalf("Availability = %+v, want booked %d, held 0, available %d", a, tc.wantBooks, holds-tc.wantBooks)
			}
		})
	}
}

func TestStage2_ConfirmRacingExpiryUnderAMovingClock(t *testing.T) {
	// Here the clock moves while Confirm runs, so each hold may legitimately
	// end up booked OR expired. What must hold: Confirm's answer and the
	// seat's final state agree, and no seat is ever both.
	const holds = 400
	inv, clk := newInv(t)
	ids := seatRange("S", holds)
	mustAddEvent(t, inv, "arena", ids)
	hs := make([]Hold, holds)
	for i := range hs {
		hs[i] = mustHold(t, inv, "arena", fmt.Sprintf("u%d", i), []string{ids[i]}, time.Duration(i+1)*time.Millisecond)
	}
	ok := make([]bool, holds)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for g := 0; g < 4; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			<-start
			for i := g; i < holds; i += 4 {
				_, err := inv.Confirm(hs[i].ID)
				switch {
				case err == nil:
					ok[i] = true
				case errors.Is(err, ErrHoldExpired):
				default:
					t.Errorf("Confirm(%s) = %v, want success or ErrHoldExpired", hs[i].ID, err)
				}
			}
		}(g)
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		<-start
		for i := 0; i < holds; i++ {
			clk.Advance(time.Millisecond)
			if i%7 == 0 {
				inv.Sweep()
			}
			runtime.Gosched()
		}
	}()
	close(start)
	within(t, "Confirm racing a moving clock", wg.Wait)
	clk.Advance(time.Hour)
	inv.Sweep()
	for i := range hs {
		want := Available
		if ok[i] {
			want = Booked
		}
		wantSeat(t, inv, "arena", ids[i], want)
	}
}

func TestStage2_OperationsDoNotScanAllHolds(t *testing.T) {
	// 50,000 live holds sit in the event. Every later operation must find
	// "which holds have expired?" without walking them: a scan per call is
	// 50,000 x 50,000 steps and misses the cap.
	const live = 50_000
	inv, clk := newInv(t)
	ids := seatRange("S", live*2)
	mustAddEvent(t, inv, "arena", ids)
	hs := make([]Hold, live)
	withinD(t, 3*time.Second, "creating 50,000 holds", func() {
		for i := 0; i < live; i++ {
			h, err := inv.Hold("arena", "u", []string{ids[i]}, time.Hour+time.Duration(i)*time.Second)
			if err != nil {
				panic(err) // t.Fatal is not allowed off the test goroutine
			}
			hs[i] = h
		}
	})
	withinD(t, 3*time.Second, "50,000 more operations next to 50,000 live holds", func() {
		for i := 0; i < live; i++ {
			clk.Advance(time.Microsecond)
			h, err := inv.Hold("arena", "v", []string{ids[live+i]}, time.Minute)
			if err != nil {
				panic(err)
			}
			if i%2 == 0 {
				if _, err := inv.Confirm(h.ID); err != nil {
					panic(err)
				}
			} else if _, err := inv.SeatStatus("arena", ids[i]); err != nil {
				panic(err)
			}
		}
	})
	withinD(t, 3*time.Second, "expiring 25,000 holds in one sweep", func() {
		clk.Advance(time.Minute + time.Hour + 25_000*time.Second)
		if n := inv.Sweep(); n < 25_000 {
			panic(fmt.Sprintf("Sweep expired %d holds, want >= 25000", n))
		}
	})
}
