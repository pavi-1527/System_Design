// Package ticketing is an in-memory seat inventory with temporary holds,
// expiry and idempotent confirmation.
//
// Lab B3 Problem 9 (90-minute box). Stage 1 is this file (all-or-nothing
// holds, confirm, release); stage 2 (expiry, per-user limits, availability
// snapshots, extending a hold) lives in stage2.go and stage 3 (idempotent
// confirmation, invariant audit, booking log, confirmation hook) in stage3.go.
//
// Reference design, in one paragraph: an Inventory owns a map of events; each
// event owns its seats, its holds, a min-heap of active holds ordered by
// expiry, and ONE mutex that guards all of them. Every operation on an event
// runs as: lock, read the injected clock, reap holds that expired at or
// before "now", validate EVERYTHING, then commit. Validating before mutating
// is what makes a multi-seat hold all-or-nothing without any rollback code,
// and having one lock per event is what makes lock-order deadlock impossible:
// no code path ever holds two event locks.
//
// Lock order (the only nesting that exists):
//
//	Inventory.mu  (RLock, only to find an event; released before the next lock)
//	event.mu      (one at a time, never two)
//	  -> Inventory.keyMu (leaf: nothing is acquired while holding it)
//
// Concurrency contract: every exported method is safe for concurrent use. The
// clock is read INSIDE the event's critical section, so "now" is consistent
// with the order in which operations are applied; a Clock must therefore be
// fast and must not call back into the Inventory.
package ticketing

import (
	"container/heap"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Errors are values: compare with errors.Is. Errors that concern one seat wrap
// the sentinel and name the seat ("ticketing: seat unavailable: seat A1").
var (
	ErrInvalidRequest   = errors.New("ticketing: invalid request")
	ErrEventExists      = errors.New("ticketing: event already exists")
	ErrUnknownEvent     = errors.New("ticketing: unknown event")
	ErrUnknownSeat      = errors.New("ticketing: unknown seat")
	ErrSeatUnavailable  = errors.New("ticketing: seat unavailable")
	ErrUnknownHold      = errors.New("ticketing: unknown hold")
	ErrHoldExpired      = errors.New("ticketing: hold expired")
	ErrAlreadyConfirmed = errors.New("ticketing: hold already confirmed")
)

// SeatState is where a seat is in its life: Available -> Held -> Booked, with
// Held falling back to Available on release or expiry. Booked is final.
type SeatState int

// Seat states. The zero value is deliberately not a valid state.
const (
	Available SeatState = iota + 1
	Held
	Booked
)

func (s SeatState) String() string {
	switch s {
	case Available:
		return "available"
	case Held:
		return "held"
	case Booked:
		return "booked"
	}
	return "SeatState(" + strconv.Itoa(int(s)) + ")"
}

// Clock is the only thing the inventory needs from time.
type Clock interface {
	Now() time.Time
}

type systemClock struct{}

func (systemClock) Now() time.Time { return time.Now() }

// Option customises an Inventory at construction time. Stage 2 and 3 add
// options; your New must apply them.
type Option func(*Inventory)

// WithClock sets the clock. The default is the system clock.
func WithClock(c Clock) Option {
	return func(inv *Inventory) { inv.clock = c }
}

// Hold is a snapshot of a temporary claim on seats. Callers get copies; the
// inventory never shares memory with them.
type Hold struct {
	ID        string
	EventID   string
	UserID    string
	Seats     []string // in the order requested
	ExpiresAt time.Time
}

// Booking is the permanent record of a confirmed hold.
type Booking struct {
	ID          string
	HoldID      string
	EventID     string
	UserID      string
	Seats       []string
	ConfirmedAt time.Time
}

// Inventory holds many events. The zero value is not usable; call New.
type Inventory struct {
	mu     sync.RWMutex // guards events (the map, not the events' contents)
	events map[string]*event

	clock Clock

	// Immutable after New (stage 2 and 3 options).
	maxHoldsPerUser int
	onConfirmed     func(Booking)

	keyMu sync.Mutex        // leaf lock: guards keys
	keys  map[string]string // idempotency key -> the hold it is bound to
}

type seat struct {
	state  SeatState
	holder *hold // non-nil exactly while state == Held
}

type holdStatus int

const (
	holdActive holdStatus = iota + 1
	holdConfirmed
	holdExpired
)

// hold is the internal record. Confirmed holds are kept so a retried
// confirmation can be answered with the same booking; expired holds are kept
// as small tombstones so callers get ErrHoldExpired, not ErrUnknownHold, no
// matter whether a reaper or a lazy access noticed the expiry first. Both
// grow without bound in this lab; see the Senior bar for how production
// bounds them.
type hold struct {
	id        string
	user      string
	seats     []string
	expiresAt time.Time
	status    holdStatus
	extended  bool
	heapIdx   int // position in event.expiry while active, else -1
	key       string
	booking   Booking
}

// event is one show: its seats and everything that changes them.
type event struct {
	id string
	mu sync.Mutex // guards every field below

	seats                  map[string]*seat
	nAvail, nHeld, nBooked int
	holds                  map[string]*hold
	expiry                 holdHeap
	byUser                 map[string]int // active holds per user
	bookings               []Booking
	seq                    uint64
}

// New returns an empty inventory. Options are applied in order.
func New(opts ...Option) (*Inventory, error) {
	inv := &Inventory{
		events: make(map[string]*event),
		keys:   make(map[string]string),
		clock:  systemClock{},
	}
	for _, opt := range opts {
		opt(inv)
	}
	if inv.clock == nil {
		return nil, fmt.Errorf("%w: nil clock", ErrInvalidRequest)
	}
	return inv, nil
}

// AddEvent registers an event whose seats all start Available. Seat IDs must
// be non-empty and unique.
func (inv *Inventory) AddEvent(eventID string, seatIDs []string) error {
	if eventID == "" || len(seatIDs) == 0 {
		return ErrInvalidRequest
	}
	e := &event{
		id:     eventID,
		seats:  make(map[string]*seat, len(seatIDs)),
		holds:  make(map[string]*hold),
		byUser: make(map[string]int),
	}
	for _, id := range seatIDs {
		if _, dup := e.seats[id]; dup || id == "" {
			return fmt.Errorf("%w: bad or duplicate seat %q", ErrInvalidRequest, id)
		}
		e.seats[id] = &seat{state: Available}
	}
	e.nAvail = len(seatIDs)
	inv.mu.Lock()
	defer inv.mu.Unlock()
	if _, exists := inv.events[eventID]; exists {
		return ErrEventExists
	}
	inv.events[eventID] = e
	return nil
}

// Hold claims every seat in seatIDs for userID until now+ttl, or claims none
// of them. The first seat (in request order) that is unknown or not Available
// is named in the error and nothing is changed. Seat IDs must be unique within
// the request. Stage 2 makes the hold expire; stage 1 only records ExpiresAt.
func (inv *Inventory) Hold(eventID, userID string, seatIDs []string, ttl time.Duration) (Hold, error) {
	if userID == "" || len(seatIDs) == 0 || ttl <= 0 || hasDuplicate(seatIDs) {
		return Hold{}, ErrInvalidRequest
	}
	e, ok := inv.event(eventID)
	if !ok {
		return Hold{}, ErrUnknownEvent
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	now := inv.clock.Now()
	e.reap(now)
	if inv.maxHoldsPerUser > 0 && e.byUser[userID] >= inv.maxHoldsPerUser {
		return Hold{}, ErrTooManyHolds
	}

	// Phase 1: validate everything. Nothing is mutated yet, so a failure at
	// seat k leaves seats 0..k-1 exactly as they were: no rollback to forget.
	picked := make([]*seat, len(seatIDs))
	for i, id := range seatIDs {
		s, ok := e.seats[id]
		if !ok {
			return Hold{}, fmt.Errorf("%w: seat %s", ErrUnknownSeat, id)
		}
		if s.state != Available {
			return Hold{}, fmt.Errorf("%w: seat %s", ErrSeatUnavailable, id)
		}
		picked[i] = s
	}

	// Phase 2: commit. Cannot fail.
	e.seq++
	h := &hold{
		id:        e.id + "#" + strconv.FormatUint(e.seq, 10),
		user:      userID,
		seats:     append([]string(nil), seatIDs...),
		expiresAt: now.Add(ttl),
		status:    holdActive,
	}
	for _, s := range picked {
		s.state, s.holder = Held, h
	}
	e.nAvail -= len(picked)
	e.nHeld += len(picked)
	e.holds[h.id] = h
	heap.Push(&e.expiry, h)
	e.byUser[userID]++
	return h.view(e.id), nil
}

// Confirm turns an active hold into a Booking. A hold can be confirmed once:
// a second call returns ErrAlreadyConfirmed (stage 3's ConfirmWithKey is the
// retry-safe variant). A hold whose expiry has been reached, i.e. now >=
// ExpiresAt, fails with ErrHoldExpired whether or not anything has reaped it.
func (inv *Inventory) Confirm(holdID string) (Booking, error) {
	return inv.confirm(holdID, "")
}

// Release gives an active hold's seats back and forgets the hold. Releasing an
// unknown or already released hold is ErrUnknownHold, an expired one
// ErrHoldExpired, a confirmed one ErrAlreadyConfirmed (cancelling a booking is
// a different feature).
func (inv *Inventory) Release(holdID string) error {
	e, ok := inv.eventOfHold(holdID)
	if !ok {
		return ErrUnknownHold
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.reap(inv.clock.Now())
	h, err := e.lookup(holdID)
	if err != nil {
		return err
	}
	e.freeSeats(h)
	heap.Remove(&e.expiry, h.heapIdx)
	e.byUser[h.user]--
	delete(e.holds, h.id)
	return nil
}

// SeatStatus reports the current state of one seat. A seat whose hold has
// expired is Available, reaped or not.
func (inv *Inventory) SeatStatus(eventID, seatID string) (SeatState, error) {
	e, ok := inv.event(eventID)
	if !ok {
		return 0, ErrUnknownEvent
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.reap(inv.clock.Now())
	s, ok := e.seats[seatID]
	if !ok {
		return 0, fmt.Errorf("%w: seat %s", ErrUnknownSeat, seatID)
	}
	return s.state, nil
}

// ---- internals --------------------------------------------------------------

func (inv *Inventory) event(id string) (*event, bool) {
	inv.mu.RLock()
	defer inv.mu.RUnlock()
	e, ok := inv.events[id]
	return e, ok
}

// eventOfHold routes a hold ID to its event. Hold IDs are "<event>#<n>", so no
// global hold index (and no global lock on the hot path) is needed.
func (inv *Inventory) eventOfHold(holdID string) (*event, bool) {
	i := strings.LastIndexByte(holdID, '#')
	if i < 0 {
		return nil, false
	}
	return inv.event(holdID[:i])
}

func hasDuplicate(ids []string) bool {
	seen := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		if _, dup := seen[id]; dup {
			return true
		}
		seen[id] = struct{}{}
	}
	return false
}

func (h *hold) view(eventID string) Hold {
	return Hold{
		ID:        h.id,
		EventID:   eventID,
		UserID:    h.user,
		Seats:     append([]string(nil), h.seats...),
		ExpiresAt: h.expiresAt,
	}
}

func (b Booking) clone() Booking {
	b.Seats = append([]string(nil), b.Seats...)
	return b
}

// lookup finds a hold and maps every non-active status to its error. Requires
// e.mu and a prior e.reap(now).
func (e *event) lookup(holdID string) (*hold, error) {
	h, ok := e.holds[holdID]
	if !ok {
		return nil, ErrUnknownHold
	}
	switch h.status {
	case holdConfirmed:
		return nil, ErrAlreadyConfirmed
	case holdExpired:
		return nil, ErrHoldExpired
	}
	return h, nil
}

// freeSeats returns h's seats to Available. The owner check makes the
// operation safe even if a seat has somehow moved on to another hold: a hold
// may only free what it still owns. Requires e.mu.
func (e *event) freeSeats(h *hold) {
	for _, id := range h.seats {
		if s := e.seats[id]; s.holder == h {
			s.state, s.holder = Available, nil
			e.nHeld--
			e.nAvail++
		}
	}
}

// confirm is shared by Confirm (key == "") and ConfirmWithKey.
func (inv *Inventory) confirm(holdID, key string) (Booking, error) {
	e, ok := inv.eventOfHold(holdID)
	if !ok {
		return Booking{}, ErrUnknownHold
	}
	b, fresh, err := inv.confirmLocked(e, holdID, key)
	// The hook runs after the lock is released, exactly once per new booking
	// and never for a replay, so it may call back into the inventory.
	if fresh && inv.onConfirmed != nil {
		inv.onConfirmed(b)
	}
	return b, err
}

func (inv *Inventory) confirmLocked(e *event, holdID, key string) (b Booking, fresh bool, err error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	now := inv.clock.Now()
	e.reap(now)
	h, ok := e.holds[holdID]
	if !ok {
		return Booking{}, false, ErrUnknownHold
	}
	if key != "" {
		// Bind the key to this hold BEFORE looking at the hold's state, under
		// the event lock, so two racing callbacks can never both pass.
		if err := inv.bindKey(key, holdID); err != nil {
			return Booking{}, false, err
		}
	}
	switch h.status {
	case holdConfirmed:
		if key != "" && h.key == key {
			return h.booking.clone(), false, nil // replay: same booking, no new effect
		}
		return Booking{}, false, ErrAlreadyConfirmed
	case holdExpired:
		return Booking{}, false, ErrHoldExpired
	}
	for _, id := range h.seats {
		e.seats[id].state, e.seats[id].holder = Booked, nil
	}
	e.nHeld -= len(h.seats)
	e.nBooked += len(h.seats)
	heap.Remove(&e.expiry, h.heapIdx)
	e.byUser[h.user]--
	e.seq++
	h.status, h.key = holdConfirmed, key
	h.booking = Booking{
		ID:          e.id + "#b" + strconv.FormatUint(e.seq, 10),
		HoldID:      h.id,
		EventID:     e.id,
		UserID:      h.user,
		Seats:       append([]string(nil), h.seats...),
		ConfirmedAt: now,
	}
	e.bookings = append(e.bookings, h.booking.clone())
	return h.booking.clone(), true, nil
}

// ---- expiry heap ---------------------------------------------------------------

// holdHeap is a min-heap of ACTIVE holds by expiry time. Each hold knows its
// index so it can be removed or re-keyed in O(log n).
type holdHeap []*hold

func (h holdHeap) Len() int           { return len(h) }
func (h holdHeap) Less(i, j int) bool { return h[i].expiresAt.Before(h[j].expiresAt) }
func (h holdHeap) Swap(i, j int) {
	h[i], h[j] = h[j], h[i]
	h[i].heapIdx, h[j].heapIdx = i, j
}
func (h *holdHeap) Push(x any) {
	hd := x.(*hold)
	hd.heapIdx = len(*h)
	*h = append(*h, hd)
}
func (h *holdHeap) Pop() any {
	old := *h
	n := len(old)
	hd := old[n-1]
	old[n-1] = nil
	hd.heapIdx = -1
	*h = old[:n-1]
	return hd
}
