package ticketing

import "errors"

// ---- Stage 3: contention and idempotency (announced at about minute 50) ------
//
// Contract:
//   - Hundreds of goroutines may race for overlapping sets of seats, requested
//     in any order. Every seat ends up with at most one winner, nothing
//     deadlocks, and no Hold leaves a partial result.
//   - ConfirmWithKey(holdID, key) is Confirm made safe to retry, for duplicate
//     payment callbacks:
//     the first call confirms the hold and binds key to it;
//     the same key and the same hold again returns an equal Booking and has no
//     new effect (no second booking, no second WithOnConfirmed call), even
//     after the hold's TTL has passed, and however many goroutines race;
//     the same key with a DIFFERENT hold is ErrIdempotencyKeyReused and that
//     other hold is untouched (keys are global: another event is another
//     hold, not another key space);
//     a different key for an already confirmed hold is ErrAlreadyConfirmed;
//     an empty key is ErrInvalidRequest; an unknown hold must not bind the key.
//     The key is bound BEFORE the hold's state is examined, so a key whose
//     first use failed (say ErrHoldExpired) stays bound to that hold.
//   - WithOnConfirmed(fn) runs fn once per NEW booking (Confirm or
//     ConfirmWithKey), on the confirming goroutine, after every lock is
//     released: fn may call back into the inventory. It never runs for a
//     replay.
//   - Bookings(eventID) returns the event's bookings in confirmation order;
//     the caller owns the slice.
//   - CheckInvariants audits every event and returns nil or an error joining
//     the violations: Available+Held+Booked == Total; each Held seat belongs
//     to exactly one active hold and vice versa; each Booked seat is in
//     exactly one booking; counters and indexes agree with the seats. It is
//     for tests and ops: it must be safe to call concurrently with traffic.

// ErrIdempotencyKeyReused is returned when a key already belongs to a
// different hold.
var ErrIdempotencyKeyReused = errors.New("ticketing: idempotency key already used for a different hold")

// WithOnConfirmed registers fn to run once for every new booking.
func WithOnConfirmed(fn func(Booking)) Option {
	return func(inv *Inventory) {
		// TODO(lab)
		_ = fn
	}
}

// ConfirmWithKey is Confirm made safe to retry with an idempotency key.
func (inv *Inventory) ConfirmWithKey(holdID, key string) (Booking, error) {
	// TODO(lab)
	_, _ = holdID, key
	return Booking{}, errNotImplemented
}

// Bookings returns the event's bookings in confirmation order.
func (inv *Inventory) Bookings(eventID string) ([]Booking, error) {
	// TODO(lab)
	_ = eventID
	return nil, errNotImplemented
}

// CheckInvariants audits every event and returns nil if the books balance.
func (inv *Inventory) CheckInvariants() error {
	// TODO(lab)
	return errNotImplemented
}
