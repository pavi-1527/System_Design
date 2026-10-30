package parkinglot

import (
	"errors"
	"fmt"
	"sync"
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

func (f *fakeClock) Set(t time.Time) {
	f.mu.Lock()
	f.now = t
	f.mu.Unlock()
}

func mustNew(t *testing.T, cfg Config, opts ...Option) *Lot {
	t.Helper()
	l, err := New(cfg, opts...)
	if err != nil {
		t.Fatalf("New(%+v) returned error %v", cfg, err)
	}
	if l == nil {
		t.Fatal("New returned a nil *Lot with a nil error")
	}
	return l
}

// within fails the test if f does not return within testTimeout, so a deadlock
// or an accidental O(n^2) fails instead of hanging the run.
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
		t.Fatalf("%s did not finish within %v (deadlock, or a per-operation cost that grows with the lot size)", what, testTimeout)
	}
}

// oneLevel is a single-level lot config.
func oneLevel(small, compact, large int) Config {
	return Config{Levels: []LevelConfig{{Small: small, Compact: compact, Large: large}}}
}

// levels repeats the same level n times.
func levels(n int, lc LevelConfig) Config {
	cfg := Config{}
	for i := 0; i < n; i++ {
		cfg.Levels = append(cfg.Levels, lc)
	}
	return cfg
}

func sp(level, number int, size SpotSize) Spot { return Spot{Level: level, Number: number, Size: size} }

// park parks and fails the test on error.
func park(t *testing.T, l *Lot, plate string, kind VehicleKind) Ticket {
	t.Helper()
	tk, err := l.Park(Vehicle{Plate: plate, Kind: kind})
	if err != nil {
		t.Fatalf("Park(%s %v) = %v, want success", plate, kind, err)
	}
	return tk
}

func unpark(t *testing.T, l *Lot, id string) Receipt {
	t.Helper()
	r, err := l.Unpark(id)
	if err != nil {
		t.Fatalf("Unpark(%s) = %v, want success", id, err)
	}
	return r
}

func wantAvail(t *testing.T, l *Lot, total, free int) {
	t.Helper()
	a := l.Availability()
	if a.Total != total || a.Free != free || a.Occupied != total-free {
		t.Fatalf("Availability = {Total %d, Free %d, Occupied %d}, want {%d, %d, %d}",
			a.Total, a.Free, a.Occupied, total, free, total-free)
	}
}

func TestStage1_NewRejectsBadConfig(t *testing.T) {
	cases := map[string]Config{
		"no levels":      {},
		"level, no spot": oneLevel(0, 0, 0),
		"negative count": oneLevel(-1, 5, 5),
		"negative large": {Levels: []LevelConfig{{Compact: 2}, {Large: -3}}},
	}
	for name, cfg := range cases {
		l, err := New(cfg)
		if !errors.Is(err, ErrInvalidConfig) {
			t.Errorf("%s: New error = %v, want ErrInvalidConfig", name, err)
		}
		if l != nil {
			t.Errorf("%s: New returned a non-nil lot alongside an error", name)
		}
	}
}

func TestStage1_ParkIssuesTicketWithSpotAndEntryTime(t *testing.T) {
	clk := newFakeClock()
	l := mustNew(t, oneLevel(1, 1, 1), WithClock(clk))
	entered := clk.Now()
	tk := park(t, l, "KA01AB1234", Car)
	if tk.ID == "" {
		t.Error("ticket ID is empty")
	}
	if tk.Plate != "KA01AB1234" || tk.Kind != Car {
		t.Errorf("ticket = %+v, want plate KA01AB1234 and kind car", tk)
	}
	if want := sp(0, 1, Compact); tk.Spot != want {
		t.Errorf("a car in an empty {1 small, 1 compact, 1 large} level got spot %+v, want %+v", tk.Spot, want)
	}
	if !tk.EnteredAt.Equal(entered) {
		t.Errorf("EnteredAt = %v, want the injected clock's time %v", tk.EnteredAt, entered)
	}
	wantAvail(t, l, 3, 2)
}

func TestStage1_FitRule(t *testing.T) {
	// What fits where is domain data, so the test spells it out rather than
	// calling Fits: motorcycles fit anywhere, cars need Compact or Large,
	// trucks need Large.
	fits := map[VehicleKind]map[SpotSize]bool{
		Motorcycle: {Small: true, Compact: true, Large: true},
		Car:        {Small: false, Compact: true, Large: true},
		Truck:      {Small: false, Compact: false, Large: true},
	}
	cfgFor := map[SpotSize]Config{Small: oneLevel(1, 0, 0), Compact: oneLevel(0, 1, 0), Large: oneLevel(0, 0, 1)}
	for kind, bySize := range fits {
		for size, want := range bySize {
			l := mustNew(t, cfgFor[size])
			tk, err := l.Park(Vehicle{Plate: "X1", Kind: kind})
			switch {
			case want && err != nil:
				t.Errorf("%v in a lot with one %v spot: err = %v, want it to fit", kind, size, err)
			case want && tk.Spot.Size != size:
				t.Errorf("%v parked in a %v spot, want the only spot (%v)", kind, tk.Spot.Size, size)
			case !want && !errors.Is(err, ErrLotFull):
				t.Errorf("%v in a lot with one %v spot: err = %v, want ErrLotFull (it does not fit)", kind, size, err)
			}
		}
	}
}

func TestStage1_DefaultAllocationIsBestFit(t *testing.T) {
	// Rule: of all free spots that fit, take the SMALLEST size; among equals the
	// lowest level, then the lowest number. Numbers within a level are assigned
	// Small first, then Compact, then Large.
	cfg := levels(2, LevelConfig{Small: 1, Compact: 1, Large: 1})
	cases := []struct {
		kind VehicleKind
		want []Spot
	}{
		{Motorcycle, []Spot{
			sp(0, 0, Small), sp(1, 0, Small), sp(0, 1, Compact), sp(1, 1, Compact), sp(0, 2, Large), sp(1, 2, Large)}},
		{Car, []Spot{sp(0, 1, Compact), sp(1, 1, Compact), sp(0, 2, Large), sp(1, 2, Large)}},
		{Truck, []Spot{sp(0, 2, Large), sp(1, 2, Large)}},
	}
	for _, tc := range cases {
		l := mustNew(t, cfg)
		for i, want := range tc.want {
			tk := park(t, l, fmt.Sprintf("%v-%d", tc.kind, i), tc.kind)
			if tk.Spot != want {
				t.Fatalf("%v #%d got spot %+v, want %+v (smallest fitting size, then lowest level)", tc.kind, i+1, tk.Spot, want)
			}
		}
		if _, err := l.Park(Vehicle{Plate: "one-too-many", Kind: tc.kind}); !errors.Is(err, ErrLotFull) {
			t.Fatalf("%v after %d: err = %v, want ErrLotFull", tc.kind, len(tc.want), err)
		}
	}
	// Numbering inside a level: 2 small, 3 compact, 1 large -> 0-1, 2-4, 5.
	l := mustNew(t, oneLevel(2, 3, 1))
	for i := 0; i < 6; i++ {
		tk := park(t, l, fmt.Sprintf("m%d", i), Motorcycle)
		if tk.Spot.Number != i {
			t.Fatalf("motorcycle #%d got spot number %d, want %d", i+1, tk.Spot.Number, i)
		}
	}
}

func TestStage1_LotFullIsPerKindAndFailedParkChangesNothing(t *testing.T) {
	l := mustNew(t, oneLevel(1, 1, 0))
	if _, err := l.Park(Vehicle{Plate: "T1", Kind: Truck}); !errors.Is(err, ErrLotFull) {
		t.Fatalf("truck with only small+compact free: err = %v, want ErrLotFull", err)
	}
	wantAvail(t, l, 2, 2) // the failed Park must not consume or half-consume a spot
	park(t, l, "C1", Car)
	if _, err := l.Park(Vehicle{Plate: "C2", Kind: Car}); !errors.Is(err, ErrLotFull) {
		t.Fatalf("second car with the compact spot taken: err = %v, want ErrLotFull", err)
	}
	// A small spot is still free: ErrLotFull for cars must not block motorcycles.
	park(t, l, "M1", Motorcycle)
	wantAvail(t, l, 2, 0)
	if _, err := l.Park(Vehicle{Plate: "M2", Kind: Motorcycle}); !errors.Is(err, ErrLotFull) {
		t.Fatalf("motorcycle in a full lot: err = %v, want ErrLotFull", err)
	}
	wantAvail(t, l, 2, 0)
}

func TestStage1_ParkRejectsInvalidAndDuplicateVehicles(t *testing.T) {
	l := mustNew(t, oneLevel(0, 1, 0))
	for _, v := range []Vehicle{{Plate: "", Kind: Car}, {Plate: "P", Kind: 0}, {Plate: "P", Kind: 99}} {
		if _, err := l.Park(v); !errors.Is(err, ErrInvalidVehicle) {
			t.Errorf("Park(%+v) err = %v, want ErrInvalidVehicle", v, err)
		}
	}
	wantAvail(t, l, 1, 1)

	first := park(t, l, "DUP", Car)
	if _, err := l.Park(Vehicle{Plate: "DUP", Kind: Car}); !errors.Is(err, ErrAlreadyParked) {
		t.Errorf("same plate again: err = %v, want ErrAlreadyParked", err)
	}
	// The lot is full now. A vehicle that is already inside must be reported as
	// such, not as "lot full": precedence is invalid > already parked > full.
	if _, err := l.Park(Vehicle{Plate: "DUP", Kind: Car}); !errors.Is(err, ErrAlreadyParked) {
		t.Errorf("same plate, full lot: err = %v, want ErrAlreadyParked (checked before capacity)", err)
	}
	// Plates are opaque and case-sensitive: no normalisation.
	if _, err := l.Park(Vehicle{Plate: "dup", Kind: Car}); !errors.Is(err, ErrLotFull) {
		t.Errorf("plate %q differs from %q: err = %v, want ErrLotFull (a different vehicle)", "dup", "DUP", err)
	}
	wantAvail(t, l, 1, 0)
	unpark(t, l, first.ID)
	park(t, l, "DUP", Car) // leaving makes the plate parkable again
}

func TestStage1_UnparkReturnsReceiptAndStayFromTheClock(t *testing.T) {
	clk := newFakeClock()
	l := mustNew(t, oneLevel(0, 2, 0), WithClock(clk))
	tk := park(t, l, "R1", Car)
	clk.Advance(90 * time.Minute)
	r := unpark(t, l, tk.ID)
	if r.Ticket != tk {
		t.Errorf("Receipt.Ticket = %+v, want the ticket that was issued %+v", r.Ticket, tk)
	}
	if !r.ExitedAt.Equal(tk.EnteredAt.Add(90 * time.Minute)) {
		t.Errorf("ExitedAt = %v, want entry + 90m", r.ExitedAt)
	}
	if r.Stay != 90*time.Minute {
		t.Errorf("Stay = %v, want 1h30m", r.Stay)
	}
	if r.Fee != 0 || r.Lost {
		t.Errorf("Fee = %d, Lost = %v; with no pricer configured parking is free", r.Fee, r.Lost)
	}
	wantAvail(t, l, 2, 2)
}

func TestStage1_ClockStepBackNeverGivesNegativeStay(t *testing.T) {
	clk := newFakeClock()
	l := mustNew(t, oneLevel(0, 1, 0), WithClock(clk))
	tk := park(t, l, "R1", Car)
	clk.Set(tk.EnteredAt.Add(-time.Hour)) // NTP step, DST bug, bad test double: it happens
	r := unpark(t, l, tk.ID)
	if r.Stay != 0 || !r.ExitedAt.Equal(tk.EnteredAt) {
		t.Fatalf("Stay = %v, ExitedAt = %v; want 0 and EnteredAt: a stay is never negative", r.Stay, r.ExitedAt)
	}
}

func TestStage1_UnparkReusesLowestFreeSpot(t *testing.T) {
	l := mustNew(t, oneLevel(0, 3, 0))
	a, b, c := park(t, l, "a", Car), park(t, l, "b", Car), park(t, l, "c", Car)
	if a.Spot.Number != 0 || b.Spot.Number != 1 || c.Spot.Number != 2 {
		t.Fatalf("spots = %d,%d,%d, want 0,1,2", a.Spot.Number, b.Spot.Number, c.Spot.Number)
	}
	unpark(t, l, b.ID)
	if d := park(t, l, "d", Car); d.Spot.Number != 1 {
		t.Fatalf("d got spot %d, want 1: the freed spot is the only free one", d.Spot.Number)
	}
	unpark(t, l, c.ID)
	unpark(t, l, a.ID)
	// Free: 0 and 2. Lowest number first, regardless of the order they were freed.
	if e := park(t, l, "e", Car); e.Spot.Number != 0 {
		t.Fatalf("e got spot %d, want 0 (lowest free number, although 0 was freed last)", e.Spot.Number)
	}
	if f := park(t, l, "f", Car); f.Spot.Number != 2 {
		t.Fatalf("f got spot %d, want 2", f.Spot.Number)
	}
}

func TestStage1_TicketWorksExactlyOnce(t *testing.T) {
	l := mustNew(t, oneLevel(0, 2, 0))
	tk := park(t, l, "A", Car)
	for _, id := range []string{"", "no-such-ticket"} {
		if _, err := l.Unpark(id); !errors.Is(err, ErrUnknownTicket) {
			t.Errorf("Unpark(%q) err = %v, want ErrUnknownTicket", id, err)
		}
	}
	unpark(t, l, tk.ID)
	for i := 0; i < 3; i++ {
		if _, err := l.Unpark(tk.ID); !errors.Is(err, ErrUnknownTicket) {
			t.Errorf("redeeming ticket %s again: err = %v, want ErrUnknownTicket", tk.ID, err)
		}
	}
	// A double free would put the spot on the free list twice and hand it to
	// two cars. Fill the lot: exactly two cars fit, in two different spots.
	x, y := park(t, l, "x", Car), park(t, l, "y", Car)
	if x.Spot == y.Spot {
		t.Fatalf("x and y both got %+v: a ticket redeemed twice freed its spot twice", x.Spot)
	}
	if _, err := l.Park(Vehicle{Plate: "z", Kind: Car}); !errors.Is(err, ErrLotFull) {
		t.Fatalf("third car in a 2-spot lot: err = %v, want ErrLotFull", err)
	}
	wantAvail(t, l, 2, 0)
}

func TestStage1_ReparkGetsFreshTicketAndIDsAreUnique(t *testing.T) {
	l := mustNew(t, oneLevel(0, 4, 0))
	seen := map[string]bool{}
	lastByPlate := map[string]Ticket{}
	for i := 0; i < 3000; i++ {
		plate := fmt.Sprintf("car-%d", i%7) // plates and spots get reused constantly
		tk := park(t, l, plate, Car)
		if seen[tk.ID] {
			t.Fatalf("ticket ID %q issued twice (after %d parks)", tk.ID, i)
		}
		seen[tk.ID] = true
		if old, ok := lastByPlate[plate]; ok {
			if old.ID == tk.ID {
				t.Fatalf("plate %s re-entered and got its old ticket ID %q back", plate, old.ID)
			}
			if _, err := l.Unpark(old.ID); !errors.Is(err, ErrUnknownTicket) {
				t.Fatalf("old ticket %s still redeemable after the plate re-entered: err = %v", old.ID, err)
			}
		}
		lastByPlate[plate] = tk
		unpark(t, l, tk.ID)
	}
}

func TestStage1_AvailabilityCountsBySizeAndLevel(t *testing.T) {
	cfg := Config{Levels: []LevelConfig{{Small: 1, Compact: 2, Large: 1}, {Small: 0, Compact: 1, Large: 2}}}
	l := mustNew(t, cfg)
	want := func(total, free int, bySize map[SpotSize]int, byLevel []int) {
		t.Helper()
		a := l.Availability()
		if a.Total != total || a.Free != free || a.Occupied != total-free {
			t.Fatalf("Availability = %+v, want total %d free %d occupied %d", a, total, free, total-free)
		}
		for _, s := range []SpotSize{Small, Compact, Large} {
			if a.FreeBySize[s] != bySize[s] {
				t.Fatalf("FreeBySize[%v] = %d, want %d (full: %+v)", s, a.FreeBySize[s], bySize[s], a)
			}
		}
		if fmt.Sprint(a.FreeByLevel) != fmt.Sprint(byLevel) {
			t.Fatalf("FreeByLevel = %v, want %v", a.FreeByLevel, byLevel)
		}
	}
	want(7, 7, map[SpotSize]int{Small: 1, Compact: 3, Large: 3}, []int{4, 3})
	car := park(t, l, "c1", Car) // best fit: compact on level 0
	want(7, 6, map[SpotSize]int{Small: 1, Compact: 2, Large: 3}, []int{3, 3})
	truck := park(t, l, "t1", Truck) // large on level 0
	want(7, 5, map[SpotSize]int{Small: 1, Compact: 2, Large: 2}, []int{2, 3})
	unpark(t, l, car.ID)
	unpark(t, l, truck.ID)
	want(7, 7, map[SpotSize]int{Small: 1, Compact: 3, Large: 3}, []int{4, 3})

	// The snapshot belongs to the caller: scribbling on it changes nothing.
	a := l.Availability()
	a.FreeBySize[Small] = 99
	a.FreeByLevel[0] = 99
	want(7, 7, map[SpotSize]int{Small: 1, Compact: 3, Large: 3}, []int{4, 3})
}
