package ticketing

import (
	"errors"
	"fmt"
)

// Stage 3 errors.
var ErrIdempotencyKeyReused = errors.New("ticketing: idempotency key already used for a different hold")

// WithOnConfirmed registers fn to run once for every NEW booking, i.e. the
// place where a real system would charge the card. It runs on the confirming
// goroutine after every lock is released, so it may call back into the
// inventory, and it never runs for a replayed confirmation.
func WithOnConfirmed(fn func(Booking)) Option {
	return func(inv *Inventory) { inv.onConfirmed = fn }
}

// ConfirmWithKey is Confirm made safe to retry. key is the caller's
// idempotency key (for example the payment provider's event ID):
//   - the first call confirms the hold and binds key to it forever;
//   - any later call with the same key and the same hold returns an equal
//     Booking and changes nothing, even after the hold's TTL has passed (a
//     booking never expires), however many goroutines race;
//   - the same key with a different hold is ErrIdempotencyKeyReused, and that
//     other hold is left untouched;
//   - a different key for an already confirmed hold is ErrAlreadyConfirmed.
//
// The key is bound before the hold's state is examined, so a key whose first
// use failed (say ErrHoldExpired) stays bound to that hold. An empty key is
// ErrInvalidRequest.
func (inv *Inventory) ConfirmWithKey(holdID, key string) (Booking, error) {
	if key == "" {
		return Booking{}, ErrInvalidRequest
	}
	return inv.confirm(holdID, key)
}

// bindKey binds key to holdID, or fails if it is bound to another hold. It is
// called with an event lock held; keyMu is a leaf, so that cannot deadlock.
func (inv *Inventory) bindKey(key, holdID string) error {
	inv.keyMu.Lock()
	defer inv.keyMu.Unlock()
	if bound, ok := inv.keys[key]; ok && bound != holdID {
		return fmt.Errorf("%w: key %q belongs to %s", ErrIdempotencyKeyReused, key, bound)
	}
	inv.keys[key] = holdID
	return nil
}

// Bookings returns the event's bookings in confirmation order. The caller owns
// the returned slice.
func (inv *Inventory) Bookings(eventID string) ([]Booking, error) {
	e, ok := inv.event(eventID)
	if !ok {
		return nil, ErrUnknownEvent
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]Booking, len(e.bookings))
	for i, b := range e.bookings {
		out[i] = b.clone()
	}
	return out, nil
}

// CheckInvariants audits every event and returns nil, or an error joining
// every violation it found. It checks that:
//   - Available + Held + Booked == Total, and the counters match the seats;
//   - every Held seat belongs to exactly one active hold that lists it, and
//     every active hold's seats are Held by that hold (no seat in two holds);
//   - every Booked seat appears in exactly one booking, and every booked seat
//     of a booking is Booked (no double-booking);
//   - the expiry heap contains exactly the active holds, and the per-user
//     counters match them.
//
// It does not reap, so it audits the state operations actually left behind.
func (inv *Inventory) CheckInvariants() error {
	inv.mu.RLock()
	evs := make([]*event, 0, len(inv.events))
	for _, e := range inv.events {
		evs = append(evs, e)
	}
	inv.mu.RUnlock()
	var errs []error
	for _, e := range evs {
		e.mu.Lock()
		errs = append(errs, e.audit()...)
		e.mu.Unlock()
	}
	return errors.Join(errs...)
}

// audit requires e.mu.
func (e *event) audit() []error {
	var errs []error
	bad := func(format string, args ...any) {
		errs = append(errs, fmt.Errorf("event %s: "+format, append([]any{e.id}, args...)...))
	}

	var avail, held, booked int
	for id, s := range e.seats {
		switch s.state {
		case Available:
			avail++
			if s.holder != nil {
				bad("seat %s is Available but has holder %s", id, s.holder.id)
			}
		case Held:
			held++
			if s.holder == nil || s.holder.status != holdActive {
				bad("seat %s is Held without an active holder", id)
			}
		case Booked:
			booked++
			if s.holder != nil {
				bad("seat %s is Booked but still has a holder", id)
			}
		default:
			bad("seat %s has invalid state %d", id, int(s.state))
		}
	}
	if avail != e.nAvail || held != e.nHeld || booked != e.nBooked {
		bad("counters (%d/%d/%d) disagree with seats (%d/%d/%d) available/held/booked",
			e.nAvail, e.nHeld, e.nBooked, avail, held, booked)
	}
	if avail+held+booked != len(e.seats) {
		bad("available %d + held %d + booked %d != total %d", avail, held, booked, len(e.seats))
	}

	users := map[string]int{}
	activeSeats := 0
	activeHolds := 0
	for id, h := range e.holds {
		if h.id != id {
			bad("hold stored under %s has id %s", id, h.id)
		}
		if h.status != holdActive {
			continue
		}
		activeHolds++
		users[h.user]++
		activeSeats += len(h.seats)
		for _, sid := range h.seats {
			if s := e.seats[sid]; s == nil || s.state != Held || s.holder != h {
				bad("active hold %s lists seat %s which is not Held by it", h.id, sid)
			}
		}
		if h.heapIdx < 0 || h.heapIdx >= len(e.expiry) || e.expiry[h.heapIdx] != h {
			bad("active hold %s is not in the expiry heap at its index", h.id)
		}
	}
	if activeSeats != held {
		bad("active holds cover %d seats but %d seats are Held (a seat is in two holds, or in none)", activeSeats, held)
	}
	if len(e.expiry) != activeHolds {
		bad("expiry heap has %d entries for %d active holds", len(e.expiry), activeHolds)
	}
	for u, n := range e.byUser {
		if n != users[u] {
			bad("user %s counter is %d but has %d active holds", u, n, users[u])
		}
	}

	seen := map[string]string{}
	for _, b := range e.bookings {
		for _, sid := range b.Seats {
			if prev, dup := seen[sid]; dup {
				bad("seat %s is in bookings %s and %s: double-booked", sid, prev, b.ID)
			}
			seen[sid] = b.ID
			if s := e.seats[sid]; s == nil || s.state != Booked {
				bad("booking %s lists seat %s which is not Booked", b.ID, sid)
			}
		}
	}
	if len(seen) != booked {
		bad("bookings cover %d seats but %d seats are Booked", len(seen), booked)
	}
	return errs
}
