package elevator

import (
	"errors"
	"fmt"
)

// FleetEvent is an Event tagged with the car it came from.
type FleetEvent struct {
	Car int
	Event
}

// Dispatcher is the strategy that decides which car serves a NEW hall call.
// It is consulted exactly once per call (see Fleet), so it can be as simple
// or as clever as you like without affecting stability.
type Dispatcher interface {
	// Pick returns the position in cars of the car that should serve call.
	// cars holds only cars that may take work, in car-ID order, and is never
	// empty. Return ok=false to decline (the call then waits in the Fleet's
	// queue until a later tick). Pick must be a pure function of its
	// arguments.
	Pick(call HallCall, cars []Status) (pos int, ok bool)
}

// ETA estimates the ticks until this car could reach call.Floor under the
// LOOK policy: the travel along the sweep it is already on (up to its
// farthest pending stop, then back) plus any remaining door time. It is
// direction-aware (a car moving away needs a full turnaround), deliberately
// ignores the time spent at intermediate stops (that is what LeastLoaded
// looks at), and is O(1).
func (s Status) ETA(call HallCall) int {
	f, d := call.Floor, call.Dir
	if s.Pending == 0 {
		return s.DoorLeft + abs(f-s.Floor)
	}
	c, hi, lo := s.Floor, s.Hi, s.Lo
	h := s.Heading
	if h == DirNone {
		h = DirUp
		if lo < c && (hi <= c || c-lo < hi-c) {
			h = DirDown
		}
	}
	if h == DirDown { // mirror the shaft so the sweep below only handles "up"
		top := s.Floors - 1
		c, hi, lo, f, d = top-c, top-lo, top-hi, top-f, d.opposite()
	}
	return s.DoorLeft + sweepUp(c, hi, lo, f, d)
}

// sweepUp is the travel distance for a car at c currently sweeping upwards
// with pending stops spanning [lo, hi], to reach a hall call (f, d).
func sweepUp(c, hi, lo, f int, d Direction) int {
	top := max(hi, c)
	switch {
	case d == DirUp && f >= c: // on the way up
		return f - c
	case d == DirDown && f >= c: // ride up to the turnaround, come back down to f
		t := max(top, f)
		return (t - c) + (t - f)
	case d == DirDown: // below us: up to the top, then down to f
		return (top - c) + (top - f)
	default: // an up call below us: up, all the way down past it, and up again
		b := min(lo, f)
		return (top - c) + (top - b) + (f - b)
	}
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

// NearestCar assigns a call to the car with the smallest ETA (ties go to the
// lower car ID).
type NearestCar struct{}

// Pick implements Dispatcher.
func (NearestCar) Pick(call HallCall, cars []Status) (int, bool) {
	best, bestETA := -1, 0
	for i, s := range cars {
		if e := s.ETA(call); best < 0 || e < bestETA {
			best, bestETA = i, e
		}
	}
	return best, best >= 0
}

// LeastLoaded assigns a call to the car with the fewest pending stops (so an
// idle car always wins), breaking ties by ETA and then by car ID.
type LeastLoaded struct{}

// Pick implements Dispatcher.
func (LeastLoaded) Pick(call HallCall, cars []Status) (int, bool) {
	best, bestETA := -1, 0
	for i, s := range cars {
		e := s.ETA(call)
		if best < 0 || s.Pending < cars[best].Pending || s.Pending == cars[best].Pending && e < bestETA {
			best, bestETA = i, e
		}
	}
	return best, best >= 0
}

// ErrInvalidDispatch is returned by Fleet.RequestHall when the Dispatcher
// answered with a position outside the slice it was given.
var ErrInvalidDispatch = errors.New("elevator: dispatcher returned an invalid car position")

// FleetOption customises a Fleet at construction time.
type FleetOption func(*Fleet)

// WithDispatcher sets the assignment strategy (default NearestCar).
func WithDispatcher(d Dispatcher) FleetOption { return func(f *Fleet) { f.disp = d } }

// WithCarOptions applies the same CarOptions to every car in the fleet.
func WithCarOptions(opts ...CarOption) FleetOption {
	return func(f *Fleet) { f.carOpts = append(f.carOpts, opts...) }
}

// Fleet is a bank of cars sharing one set of hall buttons.
//
// The one rule that makes it stable: a hall call is assigned to a car ONCE,
// when it first arrives, and stays with that car until the car serves it (or,
// in stage 3, the car leaves service). Nothing re-evaluates assignments on
// later ticks, so calls cannot flap between cars as positions change.
//
// Concurrency contract: like Car, a Fleet is deterministic and NOT safe for
// concurrent use; wrap it in a Controller (stage 3).
type Fleet struct {
	cars    []*Car
	floors  int
	disp    Dispatcher
	carOpts []CarOption

	owner   map[HallCall]int // pending hall call -> car index, or -1 while it waits for a car
	waiting []HallCall       // FIFO of the calls whose owner is -1
}

// NewFleet returns a fleet of cars identical cars serving floors 0..floors-1.
func NewFleet(floors, cars int, opts ...FleetOption) (*Fleet, error) {
	if cars < 1 {
		return nil, fmt.Errorf("%w: need at least 1 car, got %d", ErrInvalidConfig, cars)
	}
	f := &Fleet{floors: floors, disp: NearestCar{}, owner: make(map[HallCall]int)}
	for _, opt := range opts {
		opt(f)
	}
	for i := 0; i < cars; i++ {
		c, err := NewCar(floors, f.carOpts...)
		if err != nil {
			return nil, err
		}
		f.cars = append(f.cars, c)
	}
	return f, nil
}

// RequestHall records a hall call and returns the car it is assigned to.
// A call that is already pending (assigned or waiting) is coalesced: the same
// car is returned and the Dispatcher is not consulted again. It returns -1
// (and no error) when no car can take the call right now; the call is queued
// and assigned on a later tick, never dropped.
func (f *Fleet) RequestHall(floor int, dir Direction) (int, error) {
	if err := validateHall(f.floors, floor, dir); err != nil {
		return -1, err
	}
	call := HallCall{floor, dir}
	if id, ok := f.owner[call]; ok {
		return id, nil
	}
	id, placed, err := f.place(call)
	if err != nil {
		return -1, err
	}
	if !placed {
		f.enqueue(call)
	}
	return id, nil
}

// RequestCar records a car call inside car.
func (f *Fleet) RequestCar(car, floor int) error {
	if car < 0 || car >= len(f.cars) {
		return fmt.Errorf("%w: %d", ErrInvalidCar, car)
	}
	return f.cars[car].RequestCar(floor)
}

// Step advances every car by one tick, in car order, and returns all events.
// Calls that were waiting for a car are assigned at the end of the tick.
func (f *Fleet) Step() []FleetEvent {
	var out []FleetEvent
	for i, c := range f.cars {
		for _, ev := range c.Step() {
			if ev.Kind == Arrived && ev.Dir != DirNone {
				// The stop served hall call (floor, lantern): it is done.
				call := HallCall{ev.Floor, ev.Dir}
				if id, ok := f.owner[call]; ok && id == i {
					delete(f.owner, call)
				}
			}
			out = append(out, FleetEvent{Car: i, Event: ev})
		}
	}
	f.assignWaiting()
	return out
}

// Statuses returns a snapshot of every car, indexed by car ID.
func (f *Fleet) Statuses() []Status {
	out := make([]Status, len(f.cars))
	for i, c := range f.cars {
		out[i] = c.Status()
		out[i].ID = i
	}
	return out
}

// Owner reports which car a pending hall call is assigned to (-1 while it
// waits for a car). ok is false when the call is not pending.
func (f *Fleet) Owner(call HallCall) (car int, ok bool) {
	car, ok = f.owner[call]
	return car, ok
}

// Outstanding returns the number of pending hall calls, assigned or waiting.
func (f *Fleet) Outstanding() int { return len(f.owner) }

// place asks the dispatcher to pick among the cars that may take work.
// placed=false means "nobody right now" (no eligible car, or the dispatcher
// declined); the caller decides whether to queue.
func (f *Fleet) place(call HallCall) (id int, placed bool, err error) {
	cands := f.candidates()
	if len(cands) == 0 {
		return -1, false, nil
	}
	pos, ok := f.disp.Pick(call, cands)
	if !ok {
		return -1, false, nil
	}
	if pos < 0 || pos >= len(cands) {
		return -1, false, fmt.Errorf("%w: %d of %d", ErrInvalidDispatch, pos, len(cands))
	}
	id = cands[pos].ID
	c := f.cars[id]
	if err := c.RequestHall(call.Floor, call.Dir); err != nil {
		return -1, false, err
	}
	// A car whose doors are open at this floor, facing this way, absorbs the
	// call into the stop in progress: nothing is left pending on the car, so
	// there is nothing to track (tracking it would leak an entry no Arrived
	// event will ever clear).
	if c.HasHallCall(call.Floor, call.Dir) {
		f.owner[call] = id
	}
	return id, true, nil
}

func (f *Fleet) enqueue(call HallCall) {
	f.owner[call] = -1
	f.waiting = append(f.waiting, call)
}

// assignWaiting places queued calls in arrival order until one cannot be placed.
func (f *Fleet) assignWaiting() {
	for len(f.waiting) > 0 {
		call := f.waiting[0]
		if _, placed, err := f.place(call); !placed || err != nil {
			return
		}
		f.waiting = f.waiting[1:]
	}
}

func (f *Fleet) candidates() []Status {
	out := make([]Status, 0, len(f.cars))
	for i, c := range f.cars {
		if c.dispatchable() {
			s := c.Status()
			s.ID = i
			out = append(out, s)
		}
	}
	return out
}
