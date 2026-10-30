package elevator

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

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
// MovingUp or DoorsOpen. (Modelling it as extra State values would need a
// MovingUpDraining, DoorsOpenDraining, ... for every motion state.)
type ServiceMode int

// Service modes.
const (
	InService ServiceMode = iota // takes hall calls and car calls
	Draining                     // finishes the car calls on board, takes nothing new, then goes Offline
	Offline                      // parked and ignoring requests
)

// WithCapacity sets the car's passenger capacity (default 0: unlimited).
func WithCapacity(n int) CarOption { return func(c *Car) { c.capacity = n } }

// SetLoad reports the number of passengers on board (a load sensor). It
// returns ErrInvalidLoad for n < 0.
func (c *Car) SetLoad(n int) error {
	if n < 0 {
		return fmt.Errorf("%w: %d", ErrInvalidLoad, n)
	}
	c.load = n
	return nil
}

// Load returns the last reported passenger count.
func (c *Car) Load() int { return c.load }

// Overloaded reports load > capacity. An overloaded car's doors do not close
// (it emits Overloaded once per stop) and they close on the first tick after
// the load is back within capacity.
func (c *Car) Overloaded() bool { return c.capacity > 0 && c.load > c.capacity }

// Full reports load >= capacity. A Fleet gives a full car no NEW hall calls.
func (c *Car) Full() bool { return c.capacity > 0 && c.load >= c.capacity }

// Service returns the car's service mode.
func (c *Car) Service() ServiceMode { return c.mode }

// Drain starts taking the car out of service: it keeps serving the car calls
// already on board, refuses new hall calls, and goes Offline (emitting
// OutOfService on a later Step) once Idle with nothing pending.
func (c *Car) Drain() {
	if c.mode == InService {
		c.mode = Draining
	}
}

// Restore puts a Draining or Offline car back in service.
func (c *Car) Restore() { c.mode = InService }

// EmergencyStop halts the car immediately, from any state. Pending requests
// are kept. The EmergencyStopped event is delivered by the next Step. A car
// that is already halted is left alone.
func (c *Car) EmergencyStop() {
	if c.state == Halted {
		return
	}
	c.haltedOpen = c.state == DoorsOpen
	c.to(Halted)
	c.queued = append(c.queued, Event{Kind: EmergencyStopped, Floor: c.floor})
}

// Resume releases a halted car; it becomes Idle and carries on with whatever
// is still pending. It does nothing for a car that is not halted.
func (c *Car) Resume() {
	if c.state == Halted {
		c.to(Idle)
		c.lastStop = -1
		if c.haltedOpen { // keep the audit trail balanced: every Opened gets a Closed
			c.haltedOpen = false
			c.queued = append(c.queued, Event{Kind: Closed, Floor: c.floor})
		}
	}
}

// dispatchable: may a Fleet hand this car a NEW hall call?
func (c *Car) dispatchable() bool { return c.mode == InService && c.state != Halted && !c.Full() }

// settleService is the tail of an idle tick with no work: a draining car
// finishes going offline.
func (c *Car) settleService(ev []Event) []Event {
	if c.mode == Draining {
		c.mode = Offline
		ev = append(ev, Event{Kind: OutOfService, Floor: c.floor})
	}
	return ev
}

// takeHallCalls removes and returns every hall call this car holds, in floor
// order (Up before Down), so a Fleet can reassign them deterministically.
// Car calls (passengers already inside) stay.
func (c *Car) takeHallCalls() []HallCall {
	var out []HallCall
	if c.occ.n == 0 {
		return nil
	}
	for f := c.occ.lo; f >= 0; f = c.occ.above(f) {
		if c.mask[f]&upBit != 0 {
			out = append(out, HallCall{f, DirUp})
		}
		if c.mask[f]&downBit != 0 {
			out = append(out, HallCall{f, DirDown})
		}
	}
	for _, call := range out {
		c.drop(call.Floor, hallBit(call.Dir))
	}
	return out
}

// ---- Fleet: capacity, maintenance, emergency stop ---------------------------

func (f *Fleet) car(i int) (*Car, error) {
	if i < 0 || i >= len(f.cars) {
		return nil, fmt.Errorf("%w: %d", ErrInvalidCar, i)
	}
	return f.cars[i], nil
}

// SetLoad reports car i's passenger count. A car that becomes full takes no
// new hall calls; one that stops being full can take them again.
func (f *Fleet) SetLoad(i, n int) error {
	c, err := f.car(i)
	if err != nil {
		return err
	}
	if err := c.SetLoad(n); err != nil {
		return err
	}
	f.assignWaiting()
	return nil
}

// TakeOutOfService starts draining car i and immediately reassigns its hall
// calls to other cars (or queues them if there is none). Nothing is lost:
// the calls are removed from the car and placed again in one step.
func (f *Fleet) TakeOutOfService(i int) error {
	c, err := f.car(i)
	if err != nil {
		return err
	}
	c.Drain()
	f.reassign(c)
	return nil
}

// Restore puts car i back in service and gives waiting calls a chance.
func (f *Fleet) Restore(i int) error {
	c, err := f.car(i)
	if err != nil {
		return err
	}
	c.Restore()
	f.assignWaiting()
	return nil
}

// EmergencyStop halts car i and reassigns its hall calls; the car calls of
// the people stuck inside stay with it.
func (f *Fleet) EmergencyStop(i int) error {
	c, err := f.car(i)
	if err != nil {
		return err
	}
	c.EmergencyStop()
	f.reassign(c)
	return nil
}

// Resume releases a halted car i.
func (f *Fleet) Resume(i int) error {
	c, err := f.car(i)
	if err != nil {
		return err
	}
	c.Resume()
	f.assignWaiting()
	return nil
}

// reassign moves every hall call off c. Each call is removed and placed again
// without any point where it exists nowhere (this runs inside one
// single-threaded Fleet method, or under the Controller's lock).
func (f *Fleet) reassign(c *Car) {
	for _, call := range c.takeHallCalls() {
		delete(f.owner, call)
		if _, placed, err := f.place(call); !placed || err != nil {
			f.enqueue(call)
		}
	}
}

// ---- Controller: the concurrent shell ---------------------------------------

// Controller makes a Fleet safe to use from many goroutines.
//
// Ownership model: ONE mutex guards the Fleet. Requests lock it for a few
// microseconds; Tick locks it for one Step. The core stays deterministic
// because every Tick is a single atomic Step: a request lands either wholly
// before it or wholly after it, never in the middle. (The alternative is a
// single owner goroutine that receives requests over a channel; it gives the
// same atomicity but makes every request wait for the owner to be scheduled
// and needs an answer channel for RequestHall's return value.)
//
// After NewController the Fleet belongs to the Controller: do not touch it
// directly.
type Controller struct {
	tickMu  sync.Mutex // serialises Ticks and observer calls; lock order: tickMu, then mu
	mu      sync.Mutex // guards fleet and ticks
	fleet   *Fleet
	ticks   uint64
	observe func(tick uint64, events []FleetEvent)
	running atomic.Bool
}

// ControllerOption customises a Controller.
type ControllerOption func(*Controller)

// WithObserver registers fn, called after EVERY tick (possibly with no
// events) with the tick number (starting at 1) and that tick's events. It runs
// outside the fleet lock, on the goroutine that called Tick, one tick at a
// time and in tick order, so it may call back into the Controller but must not
// call Tick or Run. Use it for audit logging and for tests.
func WithObserver(fn func(tick uint64, events []FleetEvent)) ControllerOption {
	return func(c *Controller) { c.observe = fn }
}

// NewController wraps f. It returns ErrInvalidConfig for a nil fleet.
func NewController(f *Fleet, opts ...ControllerOption) (*Controller, error) {
	if f == nil {
		return nil, fmt.Errorf("%w: nil fleet", ErrInvalidConfig)
	}
	c := &Controller{fleet: f}
	for _, opt := range opts {
		opt(c)
	}
	return c, nil
}

// RequestHall is Fleet.RequestHall, safe for concurrent use.
func (c *Controller) RequestHall(floor int, dir Direction) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.fleet.RequestHall(floor, dir)
}

// RequestCar is Fleet.RequestCar, safe for concurrent use.
func (c *Controller) RequestCar(car, floor int) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.fleet.RequestCar(car, floor)
}

// SetLoad is Fleet.SetLoad, safe for concurrent use.
func (c *Controller) SetLoad(car, n int) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.fleet.SetLoad(car, n)
}

// TakeOutOfService is Fleet.TakeOutOfService, safe for concurrent use.
func (c *Controller) TakeOutOfService(car int) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.fleet.TakeOutOfService(car)
}

// Restore is Fleet.Restore, safe for concurrent use.
func (c *Controller) Restore(car int) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.fleet.Restore(car)
}

// EmergencyStop is Fleet.EmergencyStop, safe for concurrent use.
func (c *Controller) EmergencyStop(car int) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.fleet.EmergencyStop(car)
}

// Resume is Fleet.Resume, safe for concurrent use.
func (c *Controller) Resume(car int) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.fleet.Resume(car)
}

// Statuses returns a snapshot of every car.
func (c *Controller) Statuses() []Status {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.fleet.Statuses()
}

// Outstanding returns the number of pending hall calls.
func (c *Controller) Outstanding() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.fleet.Outstanding()
}

// Tick advances the whole fleet by one tick and returns the events. Run calls
// it for you; call it directly to drive the Controller by hand (tests,
// simulations). Ticks are serialised, so it is safe to call concurrently with
// requests and with other Ticks.
func (c *Controller) Tick() []FleetEvent {
	c.tickMu.Lock()
	defer c.tickMu.Unlock()
	c.mu.Lock()
	c.ticks++
	n := c.ticks
	evs := c.fleet.Step()
	observe := c.observe
	c.mu.Unlock()
	if observe != nil {
		observe(n, evs) // outside the fleet lock: the observer may call back in
	}
	return evs
}

// Run calls Tick once for every value received from ticks. It returns
// ctx.Err() when ctx is cancelled, nil when ticks is closed, and
// ErrAlreadyRunning if another Run is active. It blocks on its two channels
// and does nothing in between: no polling, no sleeping.
func (c *Controller) Run(ctx context.Context, ticks <-chan struct{}) error {
	if !c.running.CompareAndSwap(false, true) {
		return ErrAlreadyRunning
	}
	defer c.running.Store(false)
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case _, ok := <-ticks:
			if !ok {
				return nil
			}
			if ctx.Err() != nil { // select picks randomly when both are ready
				return ctx.Err()
			}
			c.Tick()
		}
	}
}

// Ticker is the production tick source: it sends on the returned channel every
// d until ctx is cancelled, then closes it. Like time.Ticker it never queues:
// if the consumer is slow, ticks are dropped (simulated time slows down; it
// does not pile up and then fast-forward). It returns ErrInvalidConfig for
// d <= 0.
func Ticker(ctx context.Context, d time.Duration) (<-chan struct{}, error) {
	if d <= 0 {
		return nil, fmt.Errorf("%w: tick interval must be > 0, got %v", ErrInvalidConfig, d)
	}
	out := make(chan struct{}, 1)
	go func() {
		defer close(out)
		t := time.NewTicker(d)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				select {
				case out <- struct{}{}:
				default:
				}
			}
		}
	}()
	return out, nil
}
