package parkinglot

import (
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"
)

func mustHourly(t *testing.T, cfg HourlyConfig) Pricer {
	t.Helper()
	p, err := NewHourlyPricer(cfg)
	if err != nil {
		t.Fatalf("NewHourlyPricer(%+v) = %v", cfg, err)
	}
	if p == nil {
		t.Fatal("NewHourlyPricer returned a nil Pricer with a nil error")
	}
	return p
}

func mustFlat(t *testing.T, fee map[VehicleKind]Cents) Pricer {
	t.Helper()
	p, err := NewFlatPricer(fee)
	if err != nil {
		t.Fatalf("NewFlatPricer(%v) = %v", fee, err)
	}
	if p == nil {
		t.Fatal("NewFlatPricer returned a nil Pricer with a nil error")
	}
	return p
}

// priceStay prices a stay of d for a vehicle of the given kind, entering at the
// fake clock's epoch.
func priceStay(p Pricer, kind VehicleKind, d time.Duration) Cents {
	entered := newFakeClock().Now()
	return p.Price(Ticket{Kind: kind, EnteredAt: entered}, entered.Add(d))
}

// ---- allocation --------------------------------------------------------------

func TestStage2_BuiltInAllocators(t *testing.T) {
	type step struct {
		kind VehicleKind
		want Spot
	}
	twoLevels := levels(2, LevelConfig{Small: 1, Compact: 1, Large: 1})
	threeFloors := levels(3, LevelConfig{Compact: 2})
	cases := []struct {
		name  string
		alloc Allocator
		cfg   Config
		steps []step
	}{
		{"BestFit is the stage-1 rule", BestFit(), twoLevels, []step{
			{Motorcycle, sp(0, 0, Small)}, {Motorcycle, sp(1, 0, Small)}, {Motorcycle, sp(0, 1, Compact)},
			{Car, sp(1, 1, Compact)}, {Car, sp(0, 2, Large)}, {Truck, sp(1, 2, Large)},
		}},
		{"FirstFit: lowest level, then smallest size", FirstFit(), twoLevels, []step{
			// A motorcycle takes the level-0 compact spot although a level-1 small one is free.
			{Motorcycle, sp(0, 0, Small)}, {Motorcycle, sp(0, 1, Compact)}, {Motorcycle, sp(0, 2, Large)},
			{Motorcycle, sp(1, 0, Small)}, {Car, sp(1, 1, Compact)}, {Truck, sp(1, 2, Large)},
		}},
		{"SpreadLevels: emptiest level, ties to the lowest", SpreadLevels(), threeFloors, []step{
			{Car, sp(0, 0, Compact)}, {Car, sp(1, 0, Compact)}, {Car, sp(2, 0, Compact)},
			{Car, sp(0, 1, Compact)}, {Car, sp(1, 1, Compact)}, {Car, sp(2, 1, Compact)},
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			l := mustNew(t, tc.cfg, WithAllocator(tc.alloc))
			for i, s := range tc.steps {
				tk := park(t, l, fmt.Sprintf("v%d", i), s.kind)
				if tk.Spot != s.want {
					t.Fatalf("step %d (%v): spot %+v, want %+v", i+1, s.kind, tk.Spot, s.want)
				}
			}
		})
	}
}

// recAllocator records what the lot shows it and delegates to FirstFit.
type recAllocator struct {
	mu    sync.Mutex
	calls []recCall
}

type recCall struct {
	v     Vehicle
	pools []Pool
}

func (r *recAllocator) Pick(v Vehicle, pools []Pool) int {
	r.mu.Lock()
	r.calls = append(r.calls, recCall{v, append([]Pool(nil), pools...)})
	r.mu.Unlock()
	return 0
}

func TestStage2_AllocatorSeesOnlyFittingNonEmptyPoolsInOrder(t *testing.T) {
	rec := &recAllocator{}
	l := mustNew(t, levels(2, LevelConfig{Small: 1, Compact: 1, Large: 1}), WithAllocator(rec))
	park(t, l, "m", Motorcycle)
	park(t, l, "c", Car)
	park(t, l, "t", Truck)
	if len(rec.calls) != 3 {
		t.Fatalf("Pick called %d times for 3 successful Parks, want 3", len(rec.calls))
	}
	want := [][]Pool{
		{{0, Small, 1}, {0, Compact, 1}, {0, Large, 1}, {1, Small, 1}, {1, Compact, 1}, {1, Large, 1}},
		// m took (0,Small). The car sees no Small pools, and level 0's Small pool is gone anyway.
		{{0, Compact, 1}, {0, Large, 1}, {1, Compact, 1}, {1, Large, 1}},
		// c took (0,Compact). The truck sees Large pools only.
		{{0, Large, 1}, {1, Large, 1}},
	}
	for i, w := range want {
		if !reflect.DeepEqual(rec.calls[i].pools, w) {
			t.Errorf("Pick call %d saw pools %v, want %v (fitting, Free > 0, ordered by level then size)", i+1, rec.calls[i].pools, w)
		}
	}

	// Calls that fail before allocation must not reach the allocator, and Pick
	// is never called with an empty slice.
	before := len(rec.calls)
	l.Park(Vehicle{Plate: "m", Kind: Motorcycle}) // already parked
	l.Park(Vehicle{Plate: "", Kind: Car})         // invalid
	park(t, l, "t2", Truck)                       // takes (1,Large)
	if _, err := l.Park(Vehicle{Plate: "t3", Kind: Truck}); !errors.Is(err, ErrLotFull) {
		t.Fatalf("third truck: err = %v, want ErrLotFull", err)
	}
	if got := len(rec.calls) - before; got != 1 {
		t.Errorf("Pick called %d times for 1 success + 3 failures, want 1: failures must be decided before the strategy runs", got)
	}
	for _, c := range rec.calls {
		if len(c.pools) == 0 {
			t.Error("Pick was called with an empty pool list")
		}
	}
}

// pickFn adapts a function to Allocator, so each test can define its strategy inline.
type pickFn func(v Vehicle, pools []Pool) int

func (f pickFn) Pick(v Vehicle, pools []Pool) int { return f(v, pools) }

func TestStage2_CustomAllocatorIsHonouredWithoutTouchingTheLot(t *testing.T) {
	// A strategy the lot has never heard of: highest level first, then size.
	topFloor := pickFn(func(_ Vehicle, pools []Pool) int {
		top := pools[len(pools)-1].Level
		for i, p := range pools {
			if p.Level == top {
				return i
			}
		}
		return 0
	})
	l := mustNew(t, levels(3, LevelConfig{Compact: 1}), WithAllocator(topFloor))
	for i, wantLevel := range []int{2, 1, 0} {
		if tk := park(t, l, fmt.Sprintf("v%d", i), Car); tk.Spot.Level != wantLevel {
			t.Fatalf("car #%d on level %d, want %d (the custom strategy prefers the top floor)", i+1, tk.Spot.Level, wantLevel)
		}
	}
}

func TestStage2_InvalidAllocatorChoiceFailsCleanly(t *testing.T) {
	choice := 0
	l := mustNew(t, oneLevel(0, 2, 0), WithAllocator(pickFn(func(Vehicle, []Pool) int { return choice })))
	for _, bad := range []int{-1, 1, 99} { // only one pool is a candidate, so index 1 is out of range
		choice = bad
		if _, err := l.Park(Vehicle{Plate: "p", Kind: Car}); !errors.Is(err, ErrBadAllocation) {
			t.Fatalf("Pick returned %d: Park err = %v, want ErrBadAllocation", bad, err)
		}
		wantAvail(t, l, 2, 2) // a buggy plug-in must not leak a spot or mark the plate as parked
	}
	choice = 0
	park(t, l, "p", Car) // the same plate is parkable: nothing was recorded by the failed attempts
	wantAvail(t, l, 2, 1)
}

func TestStage2_NilOptionsKeepDefaults(t *testing.T) {
	l := mustNew(t, oneLevel(1, 0, 1), WithAllocator(nil), WithPricer(nil), WithClock(nil))
	tk := park(t, l, "m", Motorcycle)
	if tk.Spot.Size != Small {
		t.Fatalf("with a nil allocator option the default (BestFit) must stay: motorcycle got a %v spot", tk.Spot.Size)
	}
	if tk.EnteredAt.IsZero() {
		t.Fatal("with a nil clock option the system clock must stay: EnteredAt is the zero time")
	}
	if r := unpark(t, l, tk.ID); r.Fee != 0 {
		t.Fatalf("Fee = %d with a nil pricer option, want 0", r.Fee)
	}
}

// ---- pricing -----------------------------------------------------------------

func rates(m, c, tr Cents) map[VehicleKind]Cents {
	return map[VehicleKind]Cents{Motorcycle: m, Car: c, Truck: tr}
}

func TestStage2_HourlyPricingWithGraceRoundingAndDailyCap(t *testing.T) {
	p := mustHourly(t, HourlyConfig{
		Grace:    15 * time.Minute,
		PerHour:  rates(100, 300, 600),
		DailyCap: rates(700, 2000, 4000),
	})
	const h, m = time.Hour, time.Minute
	cases := []struct {
		kind VehicleKind
		stay time.Duration
		want Cents
		why  string
	}{
		{Car, 0, 0, "zero stay"},
		{Car, 15 * m, 0, "exactly the grace period is free (inclusive)"},
		{Car, 15*m + 1, 300, "1ns over grace costs a whole started hour: grace is a free pass, not a deduction"},
		{Car, 59 * m, 300, "partial hour rounds up"},
		{Car, h, 300, "exactly one hour"},
		{Car, h + 1, 600, "1ns into the second hour"},
		{Car, 6 * h, 1800, "below the cap"},
		{Car, 6*h + 40*m, 2000, "7 started hours = 2100, capped at 2000"},
		{Car, 24 * h, 2000, "exactly one 24h block"},
		{Car, 24*h + 1, 2300, "a second block starts: cap + 1 hour"},
		{Car, 47*h + 59*m, 4000, "block 1 capped, block 2 (23h59m = 24 started hours) capped"},
		{Car, 50 * h, 4600, "2 full blocks + 2 hours"},
		{Car, 72 * h, 6000, "3 full blocks"},
		{Motorcycle, 3 * h, 300, "motorcycle rate"},
		{Motorcycle, 10 * h, 700, "motorcycle cap"},
		{Motorcycle, 30 * h, 1300, "cap + 6 hours (600 < cap 700)"},
		{Truck, 2 * h, 1200, "truck rate"},
		{Truck, 7 * h, 4000, "truck cap"},
		{Car, -time.Hour, 0, "exit before entry is a zero stay"},
	}
	for _, tc := range cases {
		if got := priceStay(p, tc.kind, tc.stay); got != tc.want {
			t.Errorf("%v stay %v: fee = %d, want %d (%s)", tc.kind, tc.stay, got, tc.want, tc.why)
		}
	}
}

func TestStage2_HourlyWithoutCapOrGrace(t *testing.T) {
	p := mustHourly(t, HourlyConfig{PerHour: rates(100, 300, 600)})
	cases := []struct {
		kind VehicleKind
		stay time.Duration
		want Cents
	}{
		{Car, 0, 0}, // a zero-length stay is still free
		{Car, 1, 300},
		{Car, 24 * time.Hour, 7200},
		{Car, 30 * time.Hour, 9000},
		{Truck, 90 * time.Minute, 1200},
		{Motorcycle, 100 * time.Hour, 10000},
	}
	for _, tc := range cases {
		if got := priceStay(p, tc.kind, tc.stay); got != tc.want {
			t.Errorf("no cap, %v stay %v: fee = %d, want %d", tc.kind, tc.stay, got, tc.want)
		}
	}
	// A cap of 0 (or a missing entry) means "no cap", not "free".
	capless := mustHourly(t, HourlyConfig{PerHour: rates(100, 300, 600), DailyCap: map[VehicleKind]Cents{Car: 0}})
	if got := priceStay(capless, Car, 30*time.Hour); got != 9000 {
		t.Errorf("cap 0: fee = %d, want 9000 (0 means no cap)", got)
	}
}

func TestStage2_PricerConstructorsValidateAndCopyConfig(t *testing.T) {
	bad := []HourlyConfig{
		{PerHour: map[VehicleKind]Cents{Car: 100}},                              // missing kinds
		{PerHour: rates(1, 2, -3)},                                              // negative rate
		{Grace: -time.Minute, PerHour: rates(1, 2, 3)},                          // negative grace
		{PerHour: rates(1, 2, 3), DailyCap: rates(5, -1, 5)},                    // negative cap
		{PerHour: map[VehicleKind]Cents{Car: 1, Truck: 1, Motorcycle: 1, 0: 5}}, // unknown kind
	}
	for i, cfg := range bad {
		if p, err := NewHourlyPricer(cfg); !errors.Is(err, ErrInvalidPricing) || p != nil {
			t.Errorf("bad hourly config #%d: (%v, %v), want (nil, ErrInvalidPricing)", i, p, err)
		}
	}
	for i, fee := range []map[VehicleKind]Cents{nil, {Car: 5}, rates(1, 2, -3)} {
		if p, err := NewFlatPricer(fee); !errors.Is(err, ErrInvalidPricing) || p != nil {
			t.Errorf("bad flat config #%d: (%v, %v), want (nil, ErrInvalidPricing)", i, p, err)
		}
	}

	// The pricer copies its config: editing the caller's map later must not change prices.
	perHour := rates(100, 300, 600)
	p := mustHourly(t, HourlyConfig{PerHour: perHour})
	perHour[Car] = 1_000_000
	if got := priceStay(p, Car, time.Hour); got != 300 {
		t.Errorf("fee = %d after the caller mutated its map, want 300: copy the config in the constructor", got)
	}
	flatFees := rates(200, 500, 1500)
	f := mustFlat(t, flatFees)
	flatFees[Car] = 1
	if got := priceStay(f, Car, 0); got != 500 {
		t.Errorf("flat fee = %d after the caller mutated its map, want 500", got)
	}
}

func TestStage2_FlatPricerChargesPerEntry(t *testing.T) {
	p := mustFlat(t, rates(200, 500, 1500))
	cases := []struct {
		kind VehicleKind
		stay time.Duration
		want Cents
	}{
		{Car, 0, 500}, {Car, 40 * time.Hour, 500}, {Motorcycle, time.Minute, 200}, {Truck, time.Hour, 1500},
	}
	for _, tc := range cases {
		if got := priceStay(p, tc.kind, tc.stay); got != tc.want {
			t.Errorf("flat %v stay %v: fee = %d, want %d", tc.kind, tc.stay, got, tc.want)
		}
	}
}

func TestStage2_UnparkChargesThroughThePricer(t *testing.T) {
	clk := newFakeClock()
	pricer := mustHourly(t, HourlyConfig{Grace: 10 * time.Minute, PerHour: rates(100, 300, 600), DailyCap: rates(700, 2000, 4000)})
	l := mustNew(t, oneLevel(1, 1, 1), WithClock(clk), WithPricer(pricer))
	car := park(t, l, "car", Car)
	moto := park(t, l, "moto", Motorcycle)
	clk.Advance(5 * time.Minute)
	if r := unpark(t, l, moto.ID); r.Fee != 0 || r.Stay != 5*time.Minute {
		t.Errorf("5 min stay: Fee = %d, Stay = %v; want 0 (inside grace) and 5m", r.Fee, r.Stay)
	}
	clk.Advance(2*time.Hour + 5*time.Minute) // car total: 2h10m -> 3 started hours
	r := unpark(t, l, car.ID)
	if r.Fee != 900 || r.Stay != 2*time.Hour+10*time.Minute {
		t.Errorf("Fee = %d, Stay = %v; want 900 (3 hours x 300) and 2h10m", r.Fee, r.Stay)
	}
}

// recPricer records the arguments of every Price call.
type recPricer struct {
	mu    sync.Mutex
	calls []priceCall
	fee   Cents
}

type priceCall struct {
	t    Ticket
	exit time.Time
}

func (p *recPricer) Price(t Ticket, exit time.Time) Cents {
	p.mu.Lock()
	p.calls = append(p.calls, priceCall{t, exit})
	p.mu.Unlock()
	return p.fee
}

func TestStage2_PricerGetsTicketAndANeverNegativeExit(t *testing.T) {
	clk := newFakeClock()
	rec := &recPricer{fee: 777}
	l := mustNew(t, oneLevel(0, 2, 0), WithClock(clk), WithPricer(rec))

	a := park(t, l, "a", Car)
	clk.Advance(3 * time.Hour)
	r := unpark(t, l, a.ID)
	if r.Fee != 777 {
		t.Errorf("Fee = %d, want the pricer's 777", r.Fee)
	}
	if len(rec.calls) != 1 || rec.calls[0].t != a || !rec.calls[0].exit.Equal(a.EnteredAt.Add(3*time.Hour)) {
		t.Errorf("Price calls = %+v, want exactly one with ticket %+v and exit = entry+3h", rec.calls, a)
	}

	b := park(t, l, "b", Car)
	clk.Set(b.EnteredAt.Add(-time.Hour))
	unpark(t, l, b.ID)
	if got := rec.calls[len(rec.calls)-1].exit; !got.Equal(b.EnteredAt) {
		t.Errorf("pricer was given exit %v for a clock that stepped back, want EnteredAt %v", got, b.EnteredAt)
	}

	// A failed Unpark never reaches the pricer.
	n := len(rec.calls)
	l.Unpark("nope")
	if len(rec.calls) != n {
		t.Error("Price was called for an unknown ticket")
	}
}

// ---- lost tickets --------------------------------------------------------------

func TestStage2_LostTicket(t *testing.T) {
	clk := newFakeClock()
	pricer := mustHourly(t, HourlyConfig{PerHour: rates(100, 300, 600), DailyCap: rates(700, 2000, 4000)})
	l := mustNew(t, oneLevel(0, 2, 0), WithClock(clk), WithPricer(pricer), WithLostTicketFee(5000))

	tk := park(t, l, "LOST1", Car)
	clk.Advance(2 * time.Hour) // normal fee 600 < lost fee
	r, err := l.UnparkLost("LOST1")
	if err != nil {
		t.Fatalf("UnparkLost = %v", err)
	}
	if !r.Lost || r.Fee != 5000 || r.Stay != 2*time.Hour || r.Ticket != tk {
		t.Errorf("receipt = %+v, want Lost, Fee 5000 (lost-ticket minimum), Stay 2h, the original ticket", r)
	}
	wantAvail(t, l, 2, 2)
	if _, err := l.Unpark(tk.ID); !errors.Is(err, ErrUnknownTicket) {
		t.Errorf("the lost ticket's ID still works: err = %v, want ErrUnknownTicket", err)
	}
	if _, err := l.UnparkLost("LOST1"); !errors.Is(err, ErrUnknownPlate) {
		t.Errorf("second UnparkLost: err = %v, want ErrUnknownPlate", err)
	}
	if _, err := l.UnparkLost("never-here"); !errors.Is(err, ErrUnknownPlate) {
		t.Errorf("UnparkLost of an unknown plate: err = %v, want ErrUnknownPlate", err)
	}
	park(t, l, "LOST1", Car) // the plate can come back

	// Claiming a lost ticket never beats leaving honestly: fee = max(normal, lost).
	l2 := mustNew(t, oneLevel(0, 1, 0), WithClock(clk), WithPricer(pricer), WithLostTicketFee(500))
	park(t, l2, "LONG", Car)
	clk.Advance(5 * time.Hour) // normal fee 1500 > 500
	if r, _ := l2.UnparkLost("LONG"); r.Fee != 1500 || !r.Lost {
		t.Errorf("receipt = %+v, want Fee 1500 (the normal fee is higher than the lost-ticket fee), Lost", r)
	}

	// Defaults: free pricer, no lost-ticket fee.
	l3 := mustNew(t, oneLevel(0, 1, 0))
	park(t, l3, "FREE", Car)
	if r, err := l3.UnparkLost("FREE"); err != nil || r.Fee != 0 || !r.Lost {
		t.Errorf("default lot: UnparkLost = (%+v, %v), want Fee 0, Lost", r, err)
	}
}
