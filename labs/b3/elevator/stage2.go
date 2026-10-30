package elevator

import "errors"

// ---- Stage 2: several cars and a Dispatcher (announced at minute 30) --
//
// Contract:
//   - A Fleet is a bank of identical cars sharing one set of hall buttons.
//     Car calls go to a specific car (RequestCar(car, floor)); hall calls go
//     to the Fleet, which asks a Dispatcher which car should serve them.
//   - Dispatcher is a strategy interface you can swap without touching the
//     Fleet. Pick receives a snapshot (Status) of each eligible car and
//     returns a position in that slice, or ok=false to decline.
//   - A hall call is assigned ONCE, when it first arrives. The Dispatcher is
//     not consulted again for it (not on a duplicate press, not on later
//     ticks), so assignments cannot flap. RequestHall returns the car and
//     coalesces duplicates: the same car comes back for as long as the call
//     is pending. Fleet.Owner reports the owner (-1 while waiting).
//   - If nobody can take the call right now (the dispatcher declines), the
//     call WAITS in a FIFO queue (RequestHall returns -1, nil; Owner reports
//     -1) and is assigned at the end of a later Step. It is never dropped.
//     A dispatcher answering with a position outside the slice is
//     ErrInvalidDispatch, and the call is not recorded.
//   - When a car serves a hall call (an Arrived event whose Dir matches), the
//     Fleet stops tracking it. A call absorbed by a car whose doors are
//     already open at that floor facing that way is served on the spot and is
//     never tracked (nothing would ever clear it).
//   - NearestCar picks the smallest ETA, ties to the lower car ID. ETA
//     is direction-aware travel only: a car heading away from the caller
//     needs a full turnaround. LeastLoaded picks the fewest pending stops,
//     then smallest ETA, then lower ID.
//   - Fairness guarantee under LOOK: a pending call is served within
//     2*(floors-1) + 2*floors*(doorTicks+2) ticks no matter how much nearby
//     traffic keeps arriving. A test hammers floors near the car while a far
//     call waits.
//   - Everything is deterministic: same inputs, same event trace. (Do not
//     range over a map to decide anything that matters.) Idle cars cost O(1)
//     per tick.

// FleetEvent is an Event tagged with the car it came from.
type FleetEvent struct {
	Car int
	Event
}

// Dispatcher is the strategy that decides which car serves a NEW hall call.
type Dispatcher interface {
	// Pick returns the position in cars of the car that should serve call.
	// cars holds only cars that may take work, in car-ID order, and is never
	// empty. Return ok=false to decline. Pick must be a pure function of its
	// arguments.
	Pick(call HallCall, cars []Status) (pos int, ok bool)
}

// ETA estimates the ticks until this car could reach call.Floor under the
// LOOK policy: the travel along the sweep it is already on (up to its
// farthest pending stop, then back) plus any remaining door time.
// Direction-aware, ignores intermediate stops, O(1). Hint: handle a car
// heading down by mirroring the shaft (floor -> Floors-1-floor, swap
// directions) so you only write the "heading up" case.
func (s Status) ETA(call HallCall) int {
	// TODO(lab)
	_ = call
	return 0
}

// NearestCar assigns a call to the car with the smallest ETA (ties go to the
// lower car ID).
type NearestCar struct{}

// Pick implements Dispatcher.
func (NearestCar) Pick(call HallCall, cars []Status) (int, bool) {
	// TODO(lab)
	_, _ = call, cars
	return -1, false
}

// LeastLoaded assigns a call to the car with the fewest pending stops (so an
// idle car always wins), breaking ties by ETA and then by car ID.
type LeastLoaded struct{}

// Pick implements Dispatcher.
func (LeastLoaded) Pick(call HallCall, cars []Status) (int, bool) {
	// TODO(lab)
	_, _ = call, cars
	return -1, false
}

// ErrInvalidDispatch is returned by Fleet.RequestHall when the Dispatcher
// answered with a position outside the slice it was given.
var ErrInvalidDispatch = errors.New("elevator: dispatcher returned an invalid car position")

// FleetOption customises a Fleet at construction time.
type FleetOption func(*Fleet)

// WithDispatcher sets the assignment strategy (default NearestCar).
func WithDispatcher(d Dispatcher) FleetOption {
	return func(f *Fleet) {
		// TODO(lab)
		_ = d
	}
}

// WithCarOptions applies the same CarOptions to every car in the fleet.
func WithCarOptions(opts ...CarOption) FleetOption {
	return func(f *Fleet) {
		// TODO(lab)
		_ = opts
	}
}

// Fleet is a bank of cars sharing one set of hall buttons.
//
// Concurrency contract: like Car, NOT safe for concurrent use; stage 3 wraps
// it in a Controller.
type Fleet struct {
	// TODO(lab): fields. Think about: the cars, the dispatcher, which car owns
	// each pending hall call, and a FIFO of calls waiting for a car.
}

// NewFleet returns a fleet of cars identical cars serving floors 0..floors-1.
// ErrInvalidConfig for cars < 1; errors from NewCar (floors < 2...) surface.
func NewFleet(floors, cars int, opts ...FleetOption) (*Fleet, error) {
	// TODO(lab)
	_, _, _ = floors, cars, opts
	return nil, errNotImplemented
}

// RequestHall records a hall call and returns the car it is assigned to, or
// -1 (and no error) when it has to wait. Duplicates are coalesced. Errors as
// for Car.RequestHall, plus ErrInvalidDispatch.
func (f *Fleet) RequestHall(floor int, dir Direction) (int, error) {
	// TODO(lab)
	_, _ = floor, dir
	return -1, errNotImplemented
}

// RequestCar records a car call inside car. ErrInvalidCar (stage 3 file) for
// an unknown car.
func (f *Fleet) RequestCar(car, floor int) error {
	// TODO(lab)
	_, _ = car, floor
	return errNotImplemented
}

// Step advances every car by one tick, in car order, and returns all events.
// Calls that were waiting for a car are assigned at the end of the tick.
func (f *Fleet) Step() []FleetEvent {
	// TODO(lab)
	return nil
}

// Statuses returns a snapshot of every car, indexed by car ID (Status.ID set).
func (f *Fleet) Statuses() []Status {
	// TODO(lab)
	return nil
}

// Owner reports which car a pending hall call is assigned to (-1 while it
// waits for a car). ok is false when the call is not pending.
func (f *Fleet) Owner(call HallCall) (car int, ok bool) {
	// TODO(lab)
	_ = call
	return 0, false
}

// Outstanding returns the number of pending hall calls, assigned or waiting.
func (f *Fleet) Outstanding() int {
	// TODO(lab)
	return 0
}
