package ticketing

import (
	"context"
	"errors"
	"time"
)

// ---- Stage 2: expiry, limits, snapshots (announced at about minute 25) -------
//
// Contract:
//   - A hold expires at ExpiresAt = holdTime + ttl. At exactly that instant it
//     is already expired (expired means now >= ExpiresAt). Its seats become
//     Available again, all of them together.
//   - Expiry is applied lazily on access: no operation (Hold, Confirm,
//     Release, SeatStatus, Availability, Extend) may ever be blocked by, or
//     act on, a hold that has expired, whether or not Sweep or a reaper has
//     run. Finding the expired holds must not scan every hold: a test keeps
//     50,000 holds alive and does 50,000 more operations under a time cap.
//   - Confirm, Release and Extend of an expired hold return ErrHoldExpired,
//     and they return it whether or not a Sweep already freed the seats (so a
//     late payment callback always gets the same, precise answer). That means
//     expired holds leave a small tombstone behind; say in a comment how you
//     would bound it in production.
//   - A hold that was released or confirmed must never be "expired" later:
//     its old deadline must not free seats that now belong to someone else.
//   - Sweep reaps every expired hold in every event and returns how many holds
//     it expired. RunReaper calls Sweep once per value received on ticks and
//     returns when ctx is done or ticks is closed; it starts no goroutine of
//     its own and the tests never sleep.
//   - WithMaxActiveHoldsPerUser(n) caps ACTIVE holds (not confirmed, not
//     released, not expired) per user per event; the n+1th Hold is
//     ErrTooManyHolds. n <= 0 means unlimited. A refused Hold uses no quota.
//   - Availability returns one consistent snapshot of an event; expired holds
//     are never counted as Held; Total == Available+Held+Booked; the caller
//     owns the Seats map.
//   - Extend(holdID, extra) moves ExpiresAt to ExpiresAt+extra, once per hold
//     (second call: ErrAlreadyExtended). extra <= 0 is ErrInvalidRequest.

// Stage 2 errors.
var (
	ErrTooManyHolds    = errors.New("ticketing: too many active holds")
	ErrAlreadyExtended = errors.New("ticketing: hold already extended")
)

// WithMaxActiveHoldsPerUser caps how many active holds one user may have per
// event. n <= 0 means unlimited (the default).
func WithMaxActiveHoldsPerUser(n int) Option {
	return func(inv *Inventory) {
		// TODO(lab)
		_ = n
	}
}

// Availability is a consistent snapshot of one event.
type Availability struct {
	EventID                        string
	Total, Available, Held, Booked int
	Seats                          map[string]SeatState // a copy; the caller owns it
}

// Availability reports the event's seats as of now.
func (inv *Inventory) Availability(eventID string) (Availability, error) {
	// TODO(lab)
	_ = eventID
	return Availability{}, errNotImplemented
}

// Extend pushes an active hold's expiry back by extra, once per hold.
func (inv *Inventory) Extend(holdID string, extra time.Duration) (Hold, error) {
	// TODO(lab)
	_, _ = holdID, extra
	return Hold{}, errNotImplemented
}

// Sweep reaps every expired hold in every event and returns how many holds it
// expired.
func (inv *Inventory) Sweep() int {
	// TODO(lab)
	return 0
}

// RunReaper calls Sweep once per value received on ticks and returns when ctx
// is done or ticks is closed.
func (inv *Inventory) RunReaper(ctx context.Context, ticks <-chan time.Time) {
	// TODO(lab)
	_, _ = ctx, ticks
}
