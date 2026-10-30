package elevator

import (
	"context"
	"errors"
	"time"
)

// ---- Stage 3: constraints and the concurrent shell (announced at minute 60) --
//
// Contract:
//   - Capacity. WithCapacity(n) (default 0: unlimited). SetLoad reports the
//     passenger count (a load sensor; negative is ErrInvalidLoad). Overloaded
//     is load > capacity: the doors do NOT close; the tick on which they would
//     have closed reports Overloaded (once per stop) and they stay open until
//     the load is back within capacity, closing on the first tick after that.
//     Full is load >= capacity: a Fleet gives a full car NO NEW hall calls
//     (it is not offered to the Dispatcher). Calls already assigned stay; this
//     is on purpose, reassigning on every load flicker would make calls flap.
//     If every car is full the call waits (-1) and is assigned the moment a
//     car has room (SetLoad triggers it).
//   - Emergency stop. EmergencyStop halts a car from ANY state: state Halted,
//     position frozen, ticks do nothing, requests are still accepted and
//     kept. The next Step reports EmergencyStopped. Idempotent. Resume puts
//     it back to Idle and it carries on with what is pending; if the doors
//     were open when it stopped, the first tick after Resume reports Closed
//     (every Opened gets a Closed in the audit trail). Add the Halted edges
//     to CanTransition: any state -> Halted, Halted -> Idle only.
//   - Maintenance. Drain makes a car refuse hall calls (and, once Offline, car
//     calls: ErrOutOfService) while it finishes its own car calls. When it is
//     Idle with nothing pending, the next Step reports OutOfService and the
//     car is Offline. Restore puts it back. Fleet.TakeOutOfService drains the
//     car AND moves its hall calls to other cars in the same step: removed
//     and placed again with no moment where the call exists nowhere. If no car
//     can take them they wait (Owner == -1) and are placed when one can
//     (Restore, Resume, SetLoad). EmergencyStop reassigns the halted car's
//     hall calls the same way; the car calls of the people inside stay.
//     A moving car whose work ahead vanished (calls were taken away) must
//     settle at the next floor it reaches (Moving -> Idle), never drive on.
//   - Controller owns a Fleet behind ONE mutex. Every request method is
//     Fleet's, made safe. Tick is one atomic Fleet.Step: a request lands
//     entirely before or entirely after it. Requests never wait for a tick.
//     Ticks are serialised (tickMu) and the observer is called after each
//     tick, outside the fleet lock, in tick order, exactly once per tick
//     (numbering from 1). Observers may call back into the Controller (they
//     must not call Tick or Run).
//   - Run(ctx, ticks) calls Tick once per value received, returns ctx.Err()
//     when cancelled (without ticking again), nil when ticks is closed,
//     ErrAlreadyRunning if another Run is active. It BLOCKS between ticks:
//     a polling loop fails the tests. Ticker is the production tick source: a
//     goroutine feeding a channel every d until ctx is done, then closing it;
//     it never queues ticks (drops them if the consumer is slow) and leaks
//     nothing.

// Errors added in stage 3.
var (
	ErrOutOfService   = errors.New("elevator: car is out of service")
	ErrInvalidCar     = errors.New("elevator: no such car")
	ErrInvalidLoad    = errors.New("elevator: load must be >= 0")
	ErrAlreadyRunning = errors.New("elevator: controller is already running")
)

// Halted is the emergency-stop state: the car holds its position and ignores
// ticks until Resume. Only Resume leaves it (-> Idle).
const Halted State = DoorsOpen + 1

// More event kinds.
const (
	Overloaded       EventKind = iota + 10 // the doors are held open: load > capacity
	EmergencyStopped                       // the car was halted
	OutOfService                           // a draining car finished its last stop and is now offline
)

// ServiceMode is orthogonal to State: a car can be Draining while it is
// MovingUp or DoorsOpen.
type ServiceMode int

// Service modes.
const (
	InService ServiceMode = iota // takes hall calls and car calls
	Draining                     // finishes the car calls on board, takes nothing new, then goes Offline
	Offline                      // parked and ignoring requests
)

// WithCapacity sets the car's passenger capacity (default 0: unlimited).
func WithCapacity(n int) CarOption {
	return func(c *Car) {
		// TODO(lab)
		_ = n
	}
}

// SetLoad reports the number of passengers on board. ErrInvalidLoad for n < 0.
func (c *Car) SetLoad(n int) error {
	// TODO(lab)
	_ = n
	return errNotImplemented
}

// Load returns the last reported passenger count.
func (c *Car) Load() int {
	// TODO(lab)
	return 0
}

// Overloaded reports load > capacity (never with unlimited capacity).
func (c *Car) Overloaded() bool {
	// TODO(lab)
	return false
}

// Full reports load >= capacity (never with unlimited capacity).
func (c *Car) Full() bool {
	// TODO(lab)
	return false
}

// Service returns the car's service mode.
func (c *Car) Service() ServiceMode {
	// TODO(lab)
	return InService
}

// Drain starts taking the car out of service (see the stage 3 contract).
func (c *Car) Drain() {
	// TODO(lab)
}

// Restore puts a Draining or Offline car back in service.
func (c *Car) Restore() {
	// TODO(lab)
}

// EmergencyStop halts the car immediately, from any state. Pending requests
// are kept. A car that is already halted is left alone.
func (c *Car) EmergencyStop() {
	// TODO(lab)
}

// Resume releases a halted car; it becomes Idle. No effect on a car that is
// not halted.
func (c *Car) Resume() {
	// TODO(lab)
}

// SetLoad reports car i's passenger count (ErrInvalidCar, ErrInvalidLoad).
// A car that stops being full can take waiting calls again.
func (f *Fleet) SetLoad(i, n int) error {
	// TODO(lab)
	_, _ = i, n
	return errNotImplemented
}

// TakeOutOfService starts draining car i and immediately reassigns its hall
// calls to other cars (or queues them if there is none).
func (f *Fleet) TakeOutOfService(i int) error {
	// TODO(lab)
	_ = i
	return errNotImplemented
}

// Restore puts car i back in service and gives waiting calls a chance.
func (f *Fleet) Restore(i int) error {
	// TODO(lab)
	_ = i
	return errNotImplemented
}

// EmergencyStop halts car i and reassigns its hall calls; the car calls of
// the people stuck inside stay with it.
func (f *Fleet) EmergencyStop(i int) error {
	// TODO(lab)
	_ = i
	return errNotImplemented
}

// Resume releases a halted car i and gives waiting calls a chance.
func (f *Fleet) Resume(i int) error {
	// TODO(lab)
	_ = i
	return errNotImplemented
}

// Controller makes a Fleet safe to use from many goroutines. After
// NewController the Fleet belongs to the Controller: do not touch it directly.
type Controller struct {
	// TODO(lab): fields. Think about: the lock(s), the fleet, the tick counter,
	// the observer, and how Run knows another Run is active.
}

// ControllerOption customises a Controller.
type ControllerOption func(*Controller)

// WithObserver registers fn, called after EVERY tick (possibly with no
// events) with the tick number (starting at 1) and that tick's events.
func WithObserver(fn func(tick uint64, events []FleetEvent)) ControllerOption {
	return func(c *Controller) {
		// TODO(lab)
		_ = fn
	}
}

// NewController wraps f. It returns ErrInvalidConfig for a nil fleet.
func NewController(f *Fleet, opts ...ControllerOption) (*Controller, error) {
	// TODO(lab)
	_, _ = f, opts
	return nil, errNotImplemented
}

// RequestHall is Fleet.RequestHall, safe for concurrent use.
func (c *Controller) RequestHall(floor int, dir Direction) (int, error) {
	// TODO(lab)
	_, _ = floor, dir
	return -1, errNotImplemented
}

// RequestCar is Fleet.RequestCar, safe for concurrent use.
func (c *Controller) RequestCar(car, floor int) error {
	// TODO(lab)
	_, _ = car, floor
	return errNotImplemented
}

// SetLoad is Fleet.SetLoad, safe for concurrent use.
func (c *Controller) SetLoad(car, n int) error {
	// TODO(lab)
	_, _ = car, n
	return errNotImplemented
}

// TakeOutOfService is Fleet.TakeOutOfService, safe for concurrent use.
func (c *Controller) TakeOutOfService(car int) error {
	// TODO(lab)
	_ = car
	return errNotImplemented
}

// Restore is Fleet.Restore, safe for concurrent use.
func (c *Controller) Restore(car int) error {
	// TODO(lab)
	_ = car
	return errNotImplemented
}

// EmergencyStop is Fleet.EmergencyStop, safe for concurrent use.
func (c *Controller) EmergencyStop(car int) error {
	// TODO(lab)
	_ = car
	return errNotImplemented
}

// Resume is Fleet.Resume, safe for concurrent use.
func (c *Controller) Resume(car int) error {
	// TODO(lab)
	_ = car
	return errNotImplemented
}

// Statuses returns a snapshot of every car.
func (c *Controller) Statuses() []Status {
	// TODO(lab)
	return nil
}

// Outstanding returns the number of pending hall calls.
func (c *Controller) Outstanding() int {
	// TODO(lab)
	return 0
}

// Tick advances the whole fleet by one tick and returns the events. Safe to
// call concurrently with requests and with other Ticks.
func (c *Controller) Tick() []FleetEvent {
	// TODO(lab)
	return nil
}

// Run calls Tick once for every value received from ticks (see the contract).
func (c *Controller) Run(ctx context.Context, ticks <-chan struct{}) error {
	// TODO(lab)
	_, _ = ctx, ticks
	return errNotImplemented
}

// Ticker is the production tick source (see the contract). It returns
// ErrInvalidConfig for d <= 0.
func Ticker(ctx context.Context, d time.Duration) (<-chan struct{}, error) {
	// TODO(lab)
	_, _ = ctx, d
	return nil, errNotImplemented
}
