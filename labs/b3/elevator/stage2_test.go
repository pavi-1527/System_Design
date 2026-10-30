package elevator

import (
	"errors"
	"fmt"
	"math/rand"
	"reflect"
	"testing"
)

func mustNewFleet(t *testing.T, floors, cars int, opts ...FleetOption) *Fleet {
	t.Helper()
	f, err := NewFleet(floors, cars, opts...)
	if err != nil {
		t.Fatalf("NewFleet(%d, %d) returned error %v", floors, cars, err)
	}
	if f == nil {
		t.Fatal("NewFleet returned a nil *Fleet with a nil error")
	}
	return f
}

func fleetIdle(f *Fleet) bool {
	if f.Outstanding() != 0 {
		return false
	}
	for _, s := range f.Statuses() {
		if s.Pending != 0 || s.State != Idle {
			return false
		}
	}
	return true
}

// fleetRunToIdle ticks until every call is served and every car is idle.
func fleetRunToIdle(t *testing.T, f *Fleet, maxTicks int) []FleetEvent {
	t.Helper()
	var all []FleetEvent
	for i := 0; !fleetIdle(f); i++ {
		if i >= maxTicks {
			t.Fatalf("fleet still busy after %d ticks (outstanding %d, statuses %+v): livelock or lost request", maxTicks, f.Outstanding(), f.Statuses())
		}
		all = append(all, f.Step()...)
	}
	return all
}

// park sends one car to a floor and waits until the whole fleet is quiet.
func park(t *testing.T, f *Fleet, car, floor int) {
	t.Helper()
	if err := f.RequestCar(car, floor); err != nil {
		t.Fatalf("RequestCar(%d, %d) = %v", car, floor, err)
	}
	fleetRunToIdle(t, f, 1_000)
	if s := f.Statuses()[car]; s.Floor != floor {
		t.Fatalf("car %d parked at %d, want %d", car, s.Floor, floor)
	}
}

func hall(t *testing.T, f *Fleet, floor int, dir Direction) int {
	t.Helper()
	car, err := f.RequestHall(floor, dir)
	if err != nil {
		t.Fatalf("RequestHall(%d, %v) = %v", floor, dir, err)
	}
	return car
}

func hallArrivals(evs []FleetEvent) []FleetEvent {
	var out []FleetEvent
	for _, e := range evs {
		if e.Kind == Arrived {
			out = append(out, e)
		}
	}
	return out
}

// pickFunc adapts a function to the Dispatcher interface.
type pickFunc func(call HallCall, cars []Status) (int, bool)

func (p pickFunc) Pick(call HallCall, cars []Status) (int, bool) { return p(call, cars) }

func TestStage2_NewFleetValidates(t *testing.T) {
	if f, err := NewFleet(10, 0); !errors.Is(err, ErrInvalidConfig) || f != nil {
		t.Errorf("NewFleet(10, 0) = (%v, %v), want (nil, ErrInvalidConfig)", f, err)
	}
	if f, err := NewFleet(1, 2); !errors.Is(err, ErrInvalidConfig) || f != nil {
		t.Errorf("NewFleet(1, 2) = (%v, %v), want (nil, ErrInvalidConfig): the car's own validation must surface", f, err)
	}
	f := mustNewFleet(t, 10, 3)
	for i, s := range f.Statuses() {
		if s.ID != i || s.Floor != 0 || s.State != Idle {
			t.Fatalf("Statuses()[%d] = %+v, want ID %d, Idle at floor 0", i, s, i)
		}
	}
	if _, err := f.RequestHall(0, DirDown); !errors.Is(err, ErrInvalidDirection) {
		t.Errorf("RequestHall(0, Down) = %v, want ErrInvalidDirection", err)
	}
	if err := f.RequestCar(3, 1); !errors.Is(err, ErrInvalidCar) {
		t.Errorf("RequestCar(3, 1) = %v, want ErrInvalidCar", err)
	}
	if err := f.RequestCar(0, 10); !errors.Is(err, ErrInvalidFloor) {
		t.Errorf("RequestCar(0, 10) = %v, want ErrInvalidFloor", err)
	}
	if f.Outstanding() != 0 {
		t.Fatalf("Outstanding = %d after only rejected requests", f.Outstanding())
	}
}

func TestStage2_NearestCarPicksTheCloserIdleCar(t *testing.T) {
	f := mustNewFleet(t, 12, 2, WithDispatcher(NearestCar{}))
	park(t, f, 1, 9)
	if car := hall(t, f, 8, DirDown); car != 1 {
		t.Errorf("call at 8 went to car %d, want car 1 (parked at 9, car 0 is at 0)", car)
	}
	if car := hall(t, f, 1, DirUp); car != 0 {
		t.Errorf("call at 1 went to car %d, want car 0", car)
	}
}

func TestStage2_NearestCarIsDirectionAware(t *testing.T) {
	// Car 0 is at 3 heading up to 9; car 1 sits idle at 9. A hall call at 5:
	//   going UP   -> car 0 passes 5 in 2 ticks, car 1 needs 4: car 0.
	//   going DOWN -> car 0 must ride to 9 and come back (10 ticks), car 1 needs
	//                 4: car 1, although car 0 is physically closer (2 floors).
	f := mustNewFleet(t, 12, 2)
	park(t, f, 1, 9)
	_ = f.RequestCar(0, 9)
	for i := 0; i < 3; i++ {
		f.Step()
	}
	if s := f.Statuses()[0]; s.Floor != 3 || s.State != MovingUp {
		t.Fatalf("setup: car 0 status %+v, want MovingUp at 3", s)
	}
	if car := hall(t, f, 5, DirUp); car != 0 {
		t.Errorf("up-call at 5 went to car %d, want car 0 (it passes 5 in 2 ticks)", car)
	}
	if car := hall(t, f, 5, DirDown); car != 1 {
		t.Errorf("down-call at 5 went to car %d, want car 1: car 0 is only 2 floors away but is heading AWAY from this passenger's direction", car)
	}
	fleetRunToIdle(t, f, 500)
}

func TestStage2_LeastLoadedPrefersTheIdleCarOverTheNearbyBusyOne(t *testing.T) {
	setup := func(d Dispatcher) *Fleet {
		f := mustNewFleet(t, 12, 2, WithDispatcher(d))
		park(t, f, 1, 9)
		_ = f.RequestCar(0, 2) // car 0 is busy, but it is passing floor 1 on the way
		return f
	}
	if car := hall(t, setup(NearestCar{}), 1, DirUp); car != 0 {
		t.Errorf("NearestCar sent the call at 1 to car %d, want car 0 (1 tick away vs 8)", car)
	}
	if car := hall(t, setup(LeastLoaded{}), 1, DirUp); car != 1 {
		t.Errorf("LeastLoaded sent the call at 1 to car %d, want the idle car 1", car)
	}
}

func TestStage2_AssignmentIsMadeOnceAndNeverRevisited(t *testing.T) {
	// A dispatcher that deliberately picks the LAST candidate (car 1) while
	// car 0 wanders next to the caller. If the fleet re-evaluated assignments
	// (or called Pick again for a duplicate), calls would flap; the counter
	// proves Pick runs exactly once per call.
	var picks int
	last := pickFunc(func(call HallCall, cars []Status) (int, bool) {
		picks++
		return len(cars) - 1, true
	})
	f := mustNewFleet(t, 12, 2, WithDispatcher(last))
	_ = f.RequestCar(0, 5)
	if car := hall(t, f, 6, DirUp); car != 1 {
		t.Fatalf("call went to car %d, want car 1 (the dispatcher's choice)", car)
	}
	var evs []FleetEvent
	for i := 0; i < 30 && !fleetIdle(f); i++ {
		if owner, ok := f.Owner(HallCall{6, DirUp}); ok { // still pending: press again
			if owner != 1 {
				t.Fatalf("Owner = %d at tick %d, want car 1 for as long as the call is pending", owner, i)
			}
			if car := hall(t, f, 6, DirUp); car != 1 {
				t.Fatalf("duplicate call answered with car %d, want the original car 1", car)
			}
		}
		evs = append(evs, f.Step()...)
	}
	if picks != 1 {
		t.Fatalf("Pick called %d times for one call, want 1: assignments must be stable (and duplicates coalesced before dispatch)", picks)
	}
	for _, e := range hallArrivals(evs) {
		if e.Floor == 6 && e.Car != 1 {
			t.Fatalf("car %d served the call at 6, but it was assigned to car 1", e.Car)
		}
	}
}

func TestStage2_DuplicateHallCallsAreCoalescedAcrossTheFleet(t *testing.T) {
	f := mustNewFleet(t, 10, 3)
	first := hall(t, f, 5, DirUp)
	for i := 0; i < 9; i++ {
		if car := hall(t, f, 5, DirUp); car != first {
			t.Fatalf("duplicate answered with car %d, want %d", car, first)
		}
	}
	if f.Outstanding() != 1 {
		t.Fatalf("Outstanding = %d after 10 identical calls, want 1", f.Outstanding())
	}
	evs := fleetRunToIdle(t, f, 200)
	if got := hallArrivals(evs); len(got) != 1 || got[0].Floor != 5 || got[0].Dir != DirUp || got[0].Car != first {
		t.Fatalf("arrivals = %+v, want exactly one stop at 5/up by car %d", got, first)
	}
	for _, e := range evs {
		if e.Car != first {
			t.Fatalf("car %d produced %+v although the call belongs to car %d: duplicate presses must not wake other cars", e.Car, e.Event, first)
		}
	}
	if _, ok := f.Owner(HallCall{5, DirUp}); ok || f.Outstanding() != 0 {
		t.Fatal("served call is still tracked")
	}
}

func TestStage2_DispatcherMayDeclineAndTheCallWaitsInsteadOfBeingLost(t *testing.T) {
	allow := false
	gate := pickFunc(func(call HallCall, cars []Status) (int, bool) { return 0, allow })
	f := mustNewFleet(t, 10, 2, WithDispatcher(gate))
	if car := hall(t, f, 3, DirUp); car != -1 {
		t.Fatalf("declined call returned car %d, want -1 (queued)", car)
	}
	if car := hall(t, f, 3, DirUp); car != -1 || f.Outstanding() != 1 {
		t.Fatalf("duplicate of a waiting call = car %d, outstanding %d; want -1 and 1", car, f.Outstanding())
	}
	if owner, ok := f.Owner(HallCall{3, DirUp}); !ok || owner != -1 {
		t.Fatalf("Owner = (%d, %v), want (-1, true) while waiting", owner, ok)
	}
	for i := 0; i < 5; i++ {
		if evs := f.Step(); len(evs) != 0 {
			t.Fatalf("events %+v while nobody was assigned", evs)
		}
	}
	allow = true
	f.Step() // the waiting call is assigned at the end of this tick
	if owner, ok := f.Owner(HallCall{3, DirUp}); !ok || owner != 0 {
		t.Fatalf("Owner = (%d, %v) after the dispatcher relented, want (0, true)", owner, ok)
	}
	evs := fleetRunToIdle(t, f, 100)
	if got := hallArrivals(evs); len(got) != 1 || got[0].Floor != 3 {
		t.Fatalf("arrivals = %+v, want one stop at 3", got)
	}
}

func TestStage2_BadDispatcherAnswerIsAnErrorNotACorruptedFleet(t *testing.T) {
	bad := pickFunc(func(call HallCall, cars []Status) (int, bool) { return len(cars), true })
	f := mustNewFleet(t, 10, 2, WithDispatcher(bad))
	if _, err := f.RequestHall(4, DirUp); !errors.Is(err, ErrInvalidDispatch) {
		t.Fatalf("RequestHall = %v, want ErrInvalidDispatch", err)
	}
	if f.Outstanding() != 0 {
		t.Fatalf("Outstanding = %d: a failed assignment must leave no phantom call behind", f.Outstanding())
	}
}

func TestStage2_FarCallIsServedWithinABoundUnderContinuousNearbyCalls(t *testing.T) {
	// Guarantee: with LOOK a car reverses only when nothing lies ahead, never
	// stops twice at a floor in one pass, and leaves leftovers at the floor it
	// just served for the return pass. So a pending call waits at most two
	// sweeps of the shaft plus one door cycle per stop:
	//   2*(floors-1) moves + 2*floors stops * (doorTicks+2) ticks.
	// Nearest-first policies have no such bound: the far call is never the
	// nearest while new calls keep appearing.
	const floors, doorTicks = 20, 2
	bound := 2*(floors-1) + 2*floors*(doorTicks+2)
	cases := []struct {
		name  string
		start int
		far   HallCall
		lo    int // the adversary hammers floors lo..lo+4
	}{
		{"far call at the bottom, traffic near the top", 15, HallCall{0, DirUp}, 13},
		{"far call at the top, traffic near the bottom", 3, HallCall{floors - 1, DirDown}, 2},
	}
	for _, tc := range cases {
		f := mustNewFleet(t, floors, 1, WithCarOptions(WithStartFloor(tc.start), WithDoorTicks(doorTicks)))
		hall(t, f, tc.far.Floor, tc.far.Dir)
		servedAt := -1
		for tk := 0; tk < bound+100 && servedAt < 0; tk++ {
			dir := DirUp
			if (tk/5)%2 == 1 {
				dir = DirDown
			}
			_, _ = f.RequestHall(tc.lo+tk%5, dir) // continuous nearby traffic
			for _, e := range f.Step() {
				if e.Kind == Arrived && e.Floor == tc.far.Floor {
					servedAt = tk + 1
				}
			}
		}
		if servedAt < 0 || servedAt > bound {
			t.Errorf("%s: far call served at tick %d, want within %d ticks (starved by nearby traffic)", tc.name, servedAt, bound)
		}
	}
}

// runWorkload drives a 3-car fleet with a seeded random workload and drains
// it. It returns every event and the set of hall calls that were accepted.
func runWorkload(t *testing.T, seed int64) ([]FleetEvent, map[HallCall]bool) {
	t.Helper()
	const floors = 20
	rng := rand.New(rand.NewSource(seed))
	f := mustNewFleet(t, floors, 3, WithCarOptions(WithDoorTicks(2)))
	accepted := map[HallCall]bool{}
	var all []FleetEvent
	for i := 0; i < 500; i++ {
		switch r := rng.Intn(10); {
		case r < 4:
			fl := rng.Intn(floors)
			d := DirUp
			if fl == floors-1 || fl > 0 && rng.Intn(2) == 0 {
				d = DirDown
			}
			if _, err := f.RequestHall(fl, d); err != nil {
				t.Fatalf("RequestHall(%d, %v) = %v", fl, d, err)
			}
			accepted[HallCall{fl, d}] = true
		case r < 6:
			if err := f.RequestCar(rng.Intn(3), rng.Intn(floors)); err != nil {
				t.Fatalf("RequestCar: %v", err)
			}
		}
		all = append(all, f.Step()...)
	}
	all = append(all, fleetRunToIdle(t, f, 20_000)...)
	return all, accepted
}

func TestStage2_EveryAcceptedHallCallIsEventuallyServed(t *testing.T) {
	for seed := int64(1); seed <= 5; seed++ {
		evs, accepted := runWorkload(t, seed)
		served := map[HallCall]bool{}
		for _, e := range hallArrivals(evs) {
			served[HallCall{e.Floor, e.Dir}] = true
		}
		for call := range accepted {
			if !served[call] {
				t.Fatalf("seed %d: hall call %+v was accepted but no car ever stopped there facing that way: a request was lost", seed, call)
			}
		}
	}
}

func TestStage2_FleetIsDeterministic(t *testing.T) {
	// Same inputs, same trace. Iterating a Go map to pick a car or to reassign
	// calls makes this fail (randomly): determinism is what makes the core
	// testable at all.
	a, _ := runWorkload(t, 42)
	b, _ := runWorkload(t, 42)
	if !reflect.DeepEqual(a, b) {
		t.Fatalf("two runs of the same workload produced different event traces (%d vs %d events)", len(a), len(b))
	}
}

func TestStage2_TicksOfAnIdleBuildingAreCheap(t *testing.T) {
	// 100 idle cars in 5,000-floor shafts for 20,000 ticks. A Step that scans
	// the floors to find work costs 10^10 steps here; an idle car must cost O(1).
	const floors, cars, ticks = 5_000, 100, 20_000
	f := mustNewFleet(t, floors, cars)
	var events int
	within(t, fmt.Sprintf("%d ticks of %d idle cars", ticks, cars), func() {
		for i := 0; i < ticks; i++ {
			events += len(f.Step())
		}
	})
	if events != 0 {
		t.Fatalf("an idle fleet produced %d events", events)
	}
}
