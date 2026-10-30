// Package ticketing is an in-memory seat inventory with temporary holds,
// expiry and idempotent confirmation.
//
// Lab B3 Problem 9 (90-minute box). Stage 1 is this file; stage 2 (expiry,
// per-user limits, availability snapshots, extending a hold) lives in
// stage2.go and stage 3 (idempotent confirmation, invariant audit, booking
// log, confirmation hook) in stage3.go. Treat the later files as sealed until
// the interviewer "announces" them (see unit-4-algorithms-and-contention.md §3).
//
// Stage 1 contract:
//   - AddEvent registers an event whose seats all start Available. Event and
//     seat IDs must be non-empty; seat IDs must be unique within the event.
//   - Hold claims EVERY requested seat for a user, or none of them. It is
//     never allowed to leave a partial hold behind, whichever seat fails and
//     whatever other goroutines are doing. The error names the first seat (in
//     request order) that is unknown (ErrUnknownSeat) or not Available
//     (ErrSeatUnavailable). A request with no seats, an empty user, a ttl <= 0
//     or a seat listed twice is ErrInvalidRequest; an unknown event is
//     ErrUnknownEvent. Hold records ExpiresAt = now + ttl; enforcing it is
//     stage 2.
//   - Seat states are Available, Held and Booked. Confirm turns an active hold
//     into a Booking (its seats become Booked). A hold can be confirmed once:
//     a second Confirm is ErrAlreadyConfirmed.
//   - Release gives an active hold's seats back and forgets the hold. After a
//     Release the hold is ErrUnknownHold; releasing a confirmed hold is
//     ErrAlreadyConfirmed. Confirm or Release of an ID that was never issued
//     is ErrUnknownHold.
//   - Returned values (Hold, Booking) never share memory with the inventory,
//     and the inventory never keeps the caller's seat slice.
//   - All exported methods are safe for concurrent use. Exactly one of any
//     number of racing Holds wins a given seat. Document your lock strategy
//     (one lock per event, per-seat locks in sorted order, ...) in a comment
//     and be ready to defend it: stage 3 tests include requests for the same
//     seats in opposite orders.
//   - Time comes from the injected Clock; nothing in this package may call
//     time.Now directly except the default clock.
package ticketing

import (
	"errors"
	"time"
)

// Errors are values: callers compare with errors.Is. Errors that concern one
// seat should wrap the sentinel and name the seat, e.g.
// fmt.Errorf("%w: seat %s", ErrSeatUnavailable, id).
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

var errNotImplemented = errors.New("ticketing: not implemented")

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
	return "invalid"
}

// Clock is the only thing the inventory needs from time.
type Clock interface {
	Now() time.Time
}

// Option customises an Inventory at construction time. Stage 2 and 3 add
// options (WithMaxActiveHoldsPerUser, WithOnConfirmed); your New must apply
// them.
type Option func(*Inventory)

// WithClock sets the clock. The default is the system clock.
func WithClock(c Clock) Option {
	return func(inv *Inventory) {
		// TODO(lab)
		_ = c
	}
}

// Hold is a snapshot of a temporary claim on seats. Callers get copies.
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
	// TODO(lab): fields. Think about: what owns the seats, what guards them,
	// how a hold ID finds its event, and (later stages) the clock, an expiry
	// index, per-user counts, idempotency keys and the booking log.
}

// New returns an empty inventory. Options are applied in order. It returns an
// error if the resulting configuration is unusable (for example a nil clock).
func New(opts ...Option) (*Inventory, error) {
	// TODO(lab)
	_ = opts
	return nil, errNotImplemented
}

// AddEvent registers an event whose seats all start Available.
func (inv *Inventory) AddEvent(eventID string, seatIDs []string) error {
	// TODO(lab)
	_, _ = eventID, seatIDs
	return errNotImplemented
}

// Hold claims every seat in seatIDs for userID until now+ttl, or none of them.
func (inv *Inventory) Hold(eventID, userID string, seatIDs []string, ttl time.Duration) (Hold, error) {
	// TODO(lab)
	_, _, _, _ = eventID, userID, seatIDs, ttl
	return Hold{}, errNotImplemented
}

// Confirm turns an active hold into a Booking. A hold can be confirmed once.
func (inv *Inventory) Confirm(holdID string) (Booking, error) {
	// TODO(lab)
	_ = holdID
	return Booking{}, errNotImplemented
}

// Release gives an active hold's seats back and forgets the hold.
func (inv *Inventory) Release(holdID string) error {
	// TODO(lab)
	_ = holdID
	return errNotImplemented
}

// SeatStatus reports the current state of one seat.
func (inv *Inventory) SeatStatus(eventID, seatID string) (SeatState, error) {
	// TODO(lab)
	_, _ = eventID, seatID
	return 0, errNotImplemented
}
