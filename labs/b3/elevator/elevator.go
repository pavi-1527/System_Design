// Package elevator is a deterministic elevator-control core with a thin
// concurrent shell.
//
// Lab B3 Problem 8 (90-minute box). Stage 1 is this file (one Car and its
// state machine); stage 2 (several cars and a Dispatcher strategy) lives in
// stage2.go; stage 3 (capacity, maintenance, emergency stop and the concurrent
// Controller) in stage3.go. Treat the later files as sealed until the
// interviewer "announces" them (see unit-4-algorithms-and-contention.md §2).
//
// The design stance this lab is about: the core is a PURE state machine. Time
// passes only when somebody calls Step, one tick per call; Step never sleeps,
// never starts a goroutine and returns the events of that tick. Concurrency is
// added at the very end, by one small type (Controller) that owns the core.
//
// Stage 1 contract (a Car is NOT safe for concurrent use):
//
//   - Floors are 0..floors-1. Requests: RequestCar(floor) is a destination
//     button inside the car; RequestHall(floor, dir) is a landing button.
//     Up is invalid at the top floor, Down at floor 0, DirNone always.
//     Rejected requests leave no trace. Duplicates are coalesced.
//   - State is an explicit enum (Idle, MovingUp, MovingDown, DoorsOpen), never
//     "direction + a bool for the doors". CanTransition is the table; every
//     state change in your code must go through one function that consults it.
//     Legal: Idle -> MovingUp | MovingDown | DoorsOpen; MovingX -> DoorsOpen |
//     Idle (settle at a floor when the work ahead vanished); DoorsOpen -> Idle.
//     Everything else is illegal, notably MovingUp <-> MovingDown (no reversal
//     mid-shaft) and DoorsOpen -> Moving* (doors must close, and the car must
//     be Idle, before it leaves).
//   - One Step is one tick, one action. From Idle with work elsewhere the car
//     picks a direction and moves one floor in that same tick (Moved). A
//     moving car moves one floor per tick. When it arrives at a floor it must
//     stop at, the SAME tick also reports Arrived (with the lantern direction
//     in Event.Dir) and Opened, and the state is DoorsOpen with DoorLeft =
//     DoorTicks. Each later DoorsOpen tick counts DoorLeft down; the tick on
//     which it reaches 0 closes the doors (Closed) and the state is Idle. The
//     next tick is the earliest the car can move. Example, DoorTicks 2,
//     RequestCar(3) at floor 0:
//     "move@1", "move@2", "move@3 arrive@3/up open@3", "-", "close@3".
//   - A car passing a floor in direction d stops for a car call, for a hall
//     call in direction d, and for a hall call in the OPPOSITE direction only
//     if nothing lies further ahead (the turnaround). It never reverses while
//     there is work ahead (LOOK, not SCAN: it turns at the farthest request,
//     not at the end of the shaft), and never stops between floors: a request
//     for the floor it has just passed waits for the return trip.
//   - Choosing a direction from Idle: keep the heading if there is work ahead
//     in it; otherwise the side that has work; with no heading yet, the nearer
//     side (ties go up). Work on the current floor is served first, except that
//     a floor the car has just finished a stop at is NOT re-opened while other
//     work is waiting elsewhere (leftovers there are picked up on the return
//     pass; this is what stops one hammered button from starving the building).
//   - The lantern (Arrived.Dir) is the direction the car will leave in: it
//     keeps its heading while there is work ahead or a hall call in that
//     direction at this floor; otherwise it turns around (opposite hall call
//     here, or work behind); with no heading and nothing else pending it faces
//     the hall call, or DirNone for a car call alone. A stop clears the car call
//     and the hall call in the lantern direction; the other hall button, if
//     pressed, gets its own stop right after the doors close.
//   - While the doors are open at floor f: RequestCar(f) and RequestHall(f, d)
//     with d == the lantern direction are absorbed by the current stop
//     (nothing recorded, the door timer is NOT extended). The opposite button
//     is recorded.
//   - Step costs O(1) in the number of floors. A test runs a 200,000-floor
//     trip under a time cap.
package elevator

import (
	"errors"
	"fmt"
)

// Errors returned by the Car and Fleet request methods.
var (
	ErrInvalidFloor     = errors.New("elevator: floor out of range")
	ErrInvalidDirection = errors.New("elevator: invalid hall-call direction")
	ErrInvalidConfig    = errors.New("elevator: invalid configuration")
)

var errNotImplemented = errors.New("elevator: not implemented")

// Direction is a travel direction. The zero value means "no direction".
type Direction int8

// Directions.
const (
	DirNone Direction = iota
	DirUp
	DirDown
)

func (d Direction) String() string {
	switch d {
	case DirUp:
		return "up"
	case DirDown:
		return "down"
	}
	return "none"
}

// State is the motion state of a car. The zero value is not a valid state.
type State int

// Car states. Stage 3 adds Halted (see stage3.go).
const (
	Idle State = iota + 1
	MovingUp
	MovingDown
	DoorsOpen
)

func (s State) String() string {
	switch s {
	case Idle:
		return "Idle"
	case MovingUp:
		return "MovingUp"
	case MovingDown:
		return "MovingDown"
	case DoorsOpen:
		return "DoorsOpen"
	case Halted:
		return "Halted"
	}
	return fmt.Sprintf("State(%d)", int(s))
}

// CanTransition reports whether the state machine permits from -> to.
// Staying in the same state is not a transition and reports false.
// Stage 3 extends the table with Halted; this function is the one stage-1 body
// you revisit.
func CanTransition(from, to State) bool {
	// TODO(lab): a table (map[State][]State) reads better than a nested switch.
	_, _ = from, to
	return false
}

// EventKind says what happened during a tick.
type EventKind int

// Event kinds. Stage 3 adds Overloaded, EmergencyStopped and OutOfService.
const (
	Moved   EventKind = iota + 1 // the car advanced one floor; Event.Floor is the new floor
	Arrived                      // the car stopped at Event.Floor; Event.Dir is the lantern (direction it will leave in)
	Opened                       // the doors opened at Event.Floor
	Closed                       // the doors closed at Event.Floor
)

func (k EventKind) String() string {
	switch k {
	case Moved:
		return "Moved"
	case Arrived:
		return "Arrived"
	case Opened:
		return "Opened"
	case Closed:
		return "Closed"
	case Overloaded:
		return "Overloaded"
	case EmergencyStopped:
		return "EmergencyStopped"
	case OutOfService:
		return "OutOfService"
	}
	return fmt.Sprintf("EventKind(%d)", int(k))
}

// Event is one thing that happened during a Step. Dir is only meaningful for
// Arrived.
type Event struct {
	Kind  EventKind
	Floor int
	Dir   Direction
}

// HallCall is a button pressed on a landing: somebody at Floor wants to go in
// Dir. Up is invalid at the top floor and Down at floor 0.
type HallCall struct {
	Floor int
	Dir   Direction
}

// Status is a cheap point-in-time snapshot of a car (O(1) to build).
type Status struct {
	ID        int       // index within a Fleet; 0 for a standalone Car
	Floors    int       // the car serves floors 0..Floors-1
	Floor     int       // the floor the car is at (or last passed)
	State     State     //
	Heading   Direction // direction of the current/last commitment; DirNone before the first move
	Pending   int       // distinct floors with at least one outstanding request
	Hi, Lo    int       // highest / lowest pending floor; -1 when Pending == 0
	DoorTicks int       // ticks the doors stay open per stop
	DoorLeft  int       // ticks until the doors try to close (0 unless DoorsOpen)
}

// CarOption customises a Car at construction time.
type CarOption func(*Car)

// WithStartFloor sets the floor the car starts at (default 0).
func WithStartFloor(floor int) CarOption {
	return func(c *Car) {
		// TODO(lab)
		_ = floor
	}
}

// WithDoorTicks sets how many ticks the doors stay open at each stop
// (default 2, must be >= 1).
func WithDoorTicks(n int) CarOption {
	return func(c *Car) {
		// TODO(lab)
		_ = n
	}
}

// Car is one elevator car: a state machine plus the set of floors it has been
// asked to visit. The zero value is not usable; call NewCar.
//
// Concurrency contract: NOT safe for concurrent use. That is deliberate.
type Car struct {
	// TODO(lab): fields. Think about: floors, current floor, state, heading,
	// door timer, the pending requests (what structure answers "is there
	// anything above me?" in O(1)?), the floor just served, and (stage 3)
	// capacity, load, service mode and events queued between ticks.
}

// NewCar returns an idle car serving floors 0..floors-1, starting at floor 0.
// It returns ErrInvalidConfig when floors < 2 or the door ticks are < 1, and
// ErrInvalidFloor when the start floor is out of range (wrap them with %w).
// Options are applied in order. Stage 3 adds WithCapacity; negative capacity
// is ErrInvalidConfig.
func NewCar(floors int, opts ...CarOption) (*Car, error) {
	// TODO(lab)
	_, _ = floors, opts
	return nil, errNotImplemented
}

// RequestCar records a car call: a passenger inside pressed a destination
// button. It returns ErrInvalidFloor for a floor outside [0, floors).
func (c *Car) RequestCar(floor int) error {
	// TODO(lab)
	_ = floor
	return errNotImplemented
}

// RequestHall records a hall call. It returns ErrInvalidFloor or
// ErrInvalidDirection (see the package doc).
func (c *Car) RequestHall(floor int, dir Direction) error {
	// TODO(lab)
	_, _ = floor, dir
	return errNotImplemented
}

// HasCarCall reports whether a car call for floor is outstanding.
func (c *Car) HasCarCall(floor int) bool {
	// TODO(lab)
	_ = floor
	return false
}

// HasHallCall reports whether a hall call (floor, dir) is outstanding.
func (c *Car) HasHallCall(floor int, dir Direction) bool {
	// TODO(lab)
	_, _ = floor, dir
	return false
}

// Status returns a snapshot of the car. Hi and Lo are -1 when nothing is
// pending. O(1).
func (c *Car) Status() Status {
	// TODO(lab)
	return Status{}
}

// Step advances the car by exactly one tick and returns what happened, in
// order (nil or empty when nothing did). It never blocks.
func (c *Car) Step() []Event {
	// TODO(lab)
	return nil
}
