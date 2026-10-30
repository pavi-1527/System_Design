// Package elevator is a deterministic elevator-control core with a thin
// concurrent shell.
//
// Lab B3 Problem 8 (90-minute box). Stage 1 is this file (one Car and its
// state machine); stage 2 (several cars and a Dispatcher strategy) lives in
// stage2.go; stage 3 (capacity, maintenance, emergency stop and the concurrent
// Controller) in stage3.go.
//
// Reference design, in one paragraph: the Car is a PURE state machine. Time
// only passes when somebody calls Step, one tick per call; Step never sleeps,
// never starts a goroutine and returns the events that happened during that
// tick. Because nothing in the core knows about clocks or threads, a test
// can script "press 5, tick, tick, tick" and compare the exact event trace.
// The state is an explicit enum guarded by a transition table, so an illegal
// transition (moving with the doors open, reversing mid-shaft) is a bug the
// program stops on instead of a state it silently reaches. Concurrency is
// added at the very end by one small type (Controller) that owns the core
// behind a mutex.
package elevator

import (
	"errors"
	"fmt"
	"math/bits"
	"slices"
)

// Errors returned by the Car and Fleet request methods.
var (
	ErrInvalidFloor     = errors.New("elevator: floor out of range")
	ErrInvalidDirection = errors.New("elevator: invalid hall-call direction")
	ErrInvalidConfig    = errors.New("elevator: invalid configuration")
)

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

func (d Direction) opposite() Direction {
	switch d {
	case DirUp:
		return DirDown
	case DirDown:
		return DirUp
	}
	return DirNone
}

// State is the motion state of a car. The zero value is not a valid state.
// Doors are NOT a boolean next to the motion state: "doors open while moving"
// is unrepresentable because DoorsOpen is one of the states.
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

// transitions is the state machine. It is the specification: every state
// change in this package goes through Car.to, which consults this table.
//
//	Idle       -> a move starts, the doors open (a request for this floor), or an emergency stop
//	Moving*    -> the car reaches a stop and opens, or settles at a floor (work vanished), or e-stop
//	DoorsOpen  -> doors close (Idle first: no leaving with the doors open), or e-stop
//	Halted     -> only Resume, back to Idle
//
// Notably absent: MovingUp -> MovingDown (a car cannot reverse mid-shaft; it
// must settle first) and DoorsOpen -> Moving* (no moving with the doors open).
var transitions = map[State][]State{
	Idle:       {MovingUp, MovingDown, DoorsOpen, Halted},
	MovingUp:   {DoorsOpen, Idle, Halted},
	MovingDown: {DoorsOpen, Idle, Halted},
	DoorsOpen:  {Idle, Halted},
	Halted:     {Idle},
}

// CanTransition reports whether the state machine permits from -> to.
// Staying in the same state is not a transition and reports false.
func CanTransition(from, to State) bool {
	return slices.Contains(transitions[from], to)
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

// request bits kept per floor.
const (
	carBit uint8 = 1 << iota
	upBit
	downBit
)

func hallBit(d Direction) uint8 {
	if d == DirUp {
		return upBit
	}
	return downBit
}

// CarOption customises a Car at construction time.
type CarOption func(*Car)

// WithStartFloor sets the floor the car starts at (default 0).
func WithStartFloor(floor int) CarOption { return func(c *Car) { c.floor = floor } }

// WithDoorTicks sets how many ticks the doors stay open at each stop
// (default 2, must be >= 1).
func WithDoorTicks(n int) CarOption { return func(c *Car) { c.doorTicks = n } }

// Car is one elevator car: a state machine plus the set of floors it has been
// asked to visit. The zero value is not usable; call NewCar.
//
// Concurrency contract: a Car is NOT safe for concurrent use. It is a
// deterministic value owned by one caller (a test, a Fleet, or a Controller
// holding a lock). That is deliberate: see Controller in stage3.go.
type Car struct {
	floors    int
	floor     int
	state     State
	heading   Direction
	doorTicks int
	doorLeft  int
	lastStop  int // floor of the stop we just finished, -1 once we have departed

	mask []uint8  // per floor: which requests are outstanding (carBit|upBit|downBit)
	occ  floorSet // floors with mask != 0; gives O(1) "anything above/below me?"

	queued []Event // events produced by commands between ticks, delivered by the next Step

	// stage 3
	capacity         int // 0 = unlimited
	load             int
	overloadReported bool
	haltedOpen       bool // halted while the doors were open: Resume owes a Closed event
	mode             ServiceMode
}

// NewCar returns an idle car serving floors 0..floors-1. It returns
// ErrInvalidConfig when floors < 2 or the door ticks are < 1, and
// ErrInvalidFloor when the start floor is out of range.
func NewCar(floors int, opts ...CarOption) (*Car, error) {
	if floors < 2 {
		return nil, fmt.Errorf("%w: need at least 2 floors, got %d", ErrInvalidConfig, floors)
	}
	c := &Car{
		floors:    floors,
		state:     Idle,
		doorTicks: 2,
		lastStop:  -1,
		mask:      make([]uint8, floors),
		occ:       newFloorSet(floors),
		mode:      InService,
	}
	for _, opt := range opts {
		opt(c)
	}
	switch {
	case c.floor < 0 || c.floor >= floors:
		return nil, fmt.Errorf("%w: start floor %d not in [0,%d)", ErrInvalidFloor, c.floor, floors)
	case c.doorTicks < 1:
		return nil, fmt.Errorf("%w: door ticks must be >= 1, got %d", ErrInvalidConfig, c.doorTicks)
	case c.capacity < 0:
		return nil, fmt.Errorf("%w: capacity must be >= 0, got %d", ErrInvalidConfig, c.capacity)
	}
	return c, nil
}

// RequestCar records a car call: a passenger inside pressed a destination
// button. Duplicates are coalesced. A call for the floor the doors are open
// at is absorbed by the current stop. It returns ErrInvalidFloor for a floor
// outside [0, floors).
func (c *Car) RequestCar(floor int) error {
	if floor < 0 || floor >= c.floors {
		return fmt.Errorf("%w: %d not in [0,%d)", ErrInvalidFloor, floor, c.floors)
	}
	if c.mode == Offline {
		return ErrOutOfService
	}
	if c.state == DoorsOpen && floor == c.floor {
		return nil
	}
	c.add(floor, carBit)
	return nil
}

// RequestHall records a hall call. Duplicates are coalesced. A call for the
// floor the doors are open at, in the direction the car is about to leave in,
// is absorbed by the current stop. It returns ErrInvalidFloor or
// ErrInvalidDirection (DirNone, Up at the top floor, Down at floor 0).
func (c *Car) RequestHall(floor int, dir Direction) error {
	if err := validateHall(c.floors, floor, dir); err != nil {
		return err
	}
	if c.mode != InService {
		return ErrOutOfService
	}
	if c.state == DoorsOpen && floor == c.floor && dir == c.heading {
		return nil
	}
	c.add(floor, hallBit(dir))
	return nil
}

func validateHall(floors, floor int, dir Direction) error {
	if floor < 0 || floor >= floors {
		return fmt.Errorf("%w: %d not in [0,%d)", ErrInvalidFloor, floor, floors)
	}
	if dir != DirUp && dir != DirDown || dir == DirUp && floor == floors-1 || dir == DirDown && floor == 0 {
		return fmt.Errorf("%w: %v at floor %d of %d", ErrInvalidDirection, dir, floor, floors)
	}
	return nil
}

// HasCarCall reports whether a car call for floor is outstanding.
func (c *Car) HasCarCall(floor int) bool {
	return floor >= 0 && floor < c.floors && c.mask[floor]&carBit != 0
}

// HasHallCall reports whether a hall call (floor, dir) is outstanding.
func (c *Car) HasHallCall(floor int, dir Direction) bool {
	return floor >= 0 && floor < c.floors && (dir == DirUp || dir == DirDown) && c.mask[floor]&hallBit(dir) != 0
}

// Status returns a snapshot of the car.
func (c *Car) Status() Status {
	s := Status{
		Floors: c.floors, Floor: c.floor, State: c.state, Heading: c.heading,
		Pending: c.occ.n, Hi: -1, Lo: -1, DoorTicks: c.doorTicks, DoorLeft: c.doorLeft,
	}
	if c.occ.n > 0 {
		s.Hi, s.Lo = c.occ.hi, c.occ.lo
	}
	return s
}

// Step advances the car by exactly one tick and returns what happened, in
// order. It is O(1) amortised in the number of floors and never blocks.
//
// One tick is one action: a move of one floor, or one door phase. Arriving at
// a stop emits Moved, Arrived and Opened in the same tick; the doors then stay
// open for DoorTicks ticks and close on the next one (Closed). The tick after
// that is the first one on which the car can leave.
func (c *Car) Step() []Event {
	ev := c.queued
	c.queued = nil
	switch c.state {
	case Idle:
		ev = c.stepIdle(ev)
	case MovingUp, MovingDown:
		ev = c.stepMoving(ev)
	case DoorsOpen:
		ev = c.stepDoors(ev)
	case Halted:
		// frozen until Resume; queued events were delivered above
	}
	return ev
}

func (c *Car) stepIdle(ev []Event) []Event {
	if c.occ.n == 0 {
		return c.settleService(ev)
	}
	// Serve this floor first, except that a floor we have only just left is
	// not re-opened while other work is waiting: leftovers here (the opposite
	// direction's button) are picked up on the return pass. This is what keeps
	// a hammered button from starving the rest of the building.
	if c.mask[c.floor] != 0 && (c.occ.n == 1 || c.lastStop != c.floor) {
		return c.openHere(ev)
	}
	dir := c.pickDirection()
	if dir == DirNone {
		return ev // unreachable: work exists on some other floor
	}
	c.heading = dir
	if dir == DirUp {
		c.to(MovingUp)
	} else {
		c.to(MovingDown)
	}
	return c.advance(ev, dir)
}

func (c *Car) stepMoving(ev []Event) []Event {
	dir := DirUp
	if c.state == MovingDown {
		dir = DirDown
	}
	if !c.ahead(dir) {
		// The work we were heading for vanished (a hall call was reassigned).
		// Settle at this floor instead of driving off the end of the shaft.
		c.to(Idle)
		return c.stepIdle(ev)
	}
	return c.advance(ev, dir)
}

// advance moves one floor in dir and decides whether to stop there.
func (c *Car) advance(ev []Event, dir Direction) []Event {
	c.lastStop = -1
	if dir == DirUp {
		c.floor++
	} else {
		c.floor--
	}
	ev = append(ev, Event{Kind: Moved, Floor: c.floor})
	if c.shouldStop(dir) {
		return c.openHere(ev)
	}
	return ev
}

// shouldStop: a car passing the current floor in direction dir stops for a
// car call, for a hall call in its own direction, and for a hall call in the
// OPPOSITE direction only when nothing lies further ahead (the turnaround).
func (c *Car) shouldStop(dir Direction) bool {
	m := c.mask[c.floor]
	if m == 0 {
		return false
	}
	if m&carBit != 0 || m&hallBit(dir) != 0 {
		return true
	}
	return m&hallBit(dir.opposite()) != 0 && !c.ahead(dir)
}

// openHere starts a stop at the current floor: pick the lantern direction,
// clear what that stop serves, open the doors.
func (c *Car) openHere(ev []Event) []Event {
	lantern := c.lantern()
	served := carBit
	if lantern != DirNone {
		served |= hallBit(lantern)
		c.heading = lantern
	}
	c.drop(c.floor, served)
	c.lastStop = c.floor
	c.doorLeft = c.doorTicks
	c.overloadReported = false
	c.to(DoorsOpen)
	return append(ev,
		Event{Kind: Arrived, Floor: c.floor, Dir: lantern},
		Event{Kind: Opened, Floor: c.floor},
	)
}

// lantern decides which direction this stop serves. The car keeps its heading
// while there is work ahead or a hall call in that direction right here;
// otherwise it turns around.
func (c *Car) lantern() Direction {
	m := c.mask[c.floor]
	h := c.heading
	if h == DirNone {
		h = c.pickDirection()
	}
	switch h {
	case DirUp:
		if c.ahead(DirUp) || m&upBit != 0 {
			return DirUp
		}
		if m&downBit != 0 || c.ahead(DirDown) {
			return DirDown
		}
		return DirUp
	case DirDown:
		if c.ahead(DirDown) || m&downBit != 0 {
			return DirDown
		}
		if m&upBit != 0 || c.ahead(DirUp) {
			return DirUp
		}
		return DirDown
	}
	// No heading and nothing else pending: face the person who called.
	switch {
	case m&upBit != 0:
		return DirUp
	case m&downBit != 0:
		return DirDown
	}
	return DirNone
}

func (c *Car) stepDoors(ev []Event) []Event {
	if c.doorLeft > 0 {
		c.doorLeft--
	}
	if c.doorLeft > 0 {
		return ev
	}
	if c.Overloaded() {
		// The doors do not close on an overloaded car. Say so once per stop.
		if !c.overloadReported {
			c.overloadReported = true
			ev = append(ev, Event{Kind: Overloaded, Floor: c.floor})
		}
		return ev
	}
	c.to(Idle)
	return append(ev, Event{Kind: Closed, Floor: c.floor})
}

// pickDirection chooses where to go next from the current floor, ignoring
// work on this floor: keep the heading if there is work ahead, else take the
// side that has work, else (no heading yet) the nearer side, ties going up.
func (c *Car) pickDirection() Direction {
	up, down := c.ahead(DirUp), c.ahead(DirDown)
	switch {
	case up && down:
		if c.heading != DirNone {
			return c.heading
		}
		if c.floor-c.occ.below(c.floor) < c.occ.above(c.floor)-c.floor {
			return DirDown
		}
		return DirUp
	case up:
		return DirUp
	case down:
		return DirDown
	}
	return DirNone
}

// ahead reports whether any request exists strictly beyond the current floor
// in direction dir. O(1) thanks to the cached extremes of the pending set.
func (c *Car) ahead(dir Direction) bool {
	if c.occ.n == 0 {
		return false
	}
	if dir == DirUp {
		return c.occ.hi > c.floor
	}
	return c.occ.lo < c.floor
}

func (c *Car) add(floor int, bit uint8) {
	c.mask[floor] |= bit
	c.occ.add(floor)
}

func (c *Car) drop(floor int, bitsToClear uint8) {
	c.mask[floor] &^= bitsToClear
	if c.mask[floor] == 0 {
		c.occ.del(floor)
	}
}

// to is the only place the state changes. An illegal transition is a bug in
// this package (it cannot be reached through the public API), so we stop
// rather than carry on in an undefined state.
func (c *Car) to(next State) {
	if !CanTransition(c.state, next) {
		panic(fmt.Sprintf("elevator: illegal transition %v -> %v at floor %d", c.state, next, c.floor))
	}
	c.state = next
}

// ---- floorSet: a bitset with cached extremes --------------------------------

// floorSet is a set of floors with O(1) add, has, count, lowest and highest.
// Removing the current extreme rescans word by word (floors/64 words at worst),
// which happens once per stop at an extreme, not once per tick.
type floorSet struct {
	words  []uint64
	n      int
	lo, hi int // valid only while n > 0
}

func newFloorSet(floors int) floorSet { return floorSet{words: make([]uint64, (floors+63)/64)} }

func (s *floorSet) has(f int) bool { return s.words[f>>6]&(1<<(uint(f)&63)) != 0 }

func (s *floorSet) add(f int) {
	if s.has(f) {
		return
	}
	s.words[f>>6] |= 1 << (uint(f) & 63)
	if s.n == 0 {
		s.lo, s.hi = f, f
	} else {
		s.lo, s.hi = min(s.lo, f), max(s.hi, f)
	}
	s.n++
}

func (s *floorSet) del(f int) {
	if !s.has(f) {
		return
	}
	s.words[f>>6] &^= 1 << (uint(f) & 63)
	s.n--
	if s.n == 0 {
		return
	}
	if f == s.hi {
		s.hi = s.below(f)
	}
	if f == s.lo {
		s.lo = s.above(f)
	}
}

// above returns the smallest member greater than f, or -1.
func (s *floorSet) above(f int) int {
	i := f + 1
	w := i >> 6
	if w >= len(s.words) {
		return -1
	}
	x := s.words[w] &^ (uint64(1)<<(uint(i)&63) - 1)
	for {
		if x != 0 {
			return w<<6 + bits.TrailingZeros64(x)
		}
		w++
		if w >= len(s.words) {
			return -1
		}
		x = s.words[w]
	}
}

// below returns the largest member smaller than f, or -1.
func (s *floorSet) below(f int) int {
	if f <= 0 {
		return -1
	}
	i := f - 1
	w := i >> 6
	x := s.words[w] & (^uint64(0) >> (63 - uint(i)&63))
	for {
		if x != 0 {
			return w<<6 + 63 - bits.LeadingZeros64(x)
		}
		w--
		if w < 0 {
			return -1
		}
		x = s.words[w]
	}
}
