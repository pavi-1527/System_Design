package ticketing

import (
	"container/heap"
	"context"
	"errors"
	"time"
)

// Stage 2 errors.
var (
	ErrTooManyHolds    = errors.New("ticketing: too many active holds")
	ErrAlreadyExtended = errors.New("ticketing: hold already extended")
)

// WithMaxActiveHoldsPerUser caps how many ACTIVE holds one user may have per
// event. Active means not confirmed, not released, not expired. n <= 0 means
// unlimited (the default).
func WithMaxActiveHoldsPerUser(n int) Option {
	return func(inv *Inventory) { inv.maxHoldsPerUser = n }
}

// Availability is a consistent snapshot of one event: all numbers and the
// per-seat map were read under the same lock, and expired holds are never
// counted as Held. Total == Available+Held+Booked always holds.
type Availability struct {
	EventID                        string
	Total, Available, Held, Booked int
	Seats                          map[string]SeatState // a copy; the caller owns it
}

// Availability reports the event's seats as of now. Reading it reaps expired
// holds first, which is why it takes the full lock, not a read lock: with lazy
// expiry a "read" mutates.
func (inv *Inventory) Availability(eventID string) (Availability, error) {
	e, ok := inv.event(eventID)
	if !ok {
		return Availability{}, ErrUnknownEvent
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.reap(inv.clock.Now())
	a := Availability{
		EventID:   e.id,
		Total:     len(e.seats),
		Available: e.nAvail,
		Held:      e.nHeld,
		Booked:    e.nBooked,
		Seats:     make(map[string]SeatState, len(e.seats)),
	}
	for id, s := range e.seats {
		a.Seats[id] = s.state
	}
	return a, nil
}

// Extend pushes an active hold's expiry back by extra (ExpiresAt += extra),
// once per hold. Extending is relative to the CURRENT expiry, so a hold cannot
// be revived: an expired hold fails with ErrHoldExpired like everything else.
func (inv *Inventory) Extend(holdID string, extra time.Duration) (Hold, error) {
	if extra <= 0 {
		return Hold{}, ErrInvalidRequest
	}
	e, ok := inv.eventOfHold(holdID)
	if !ok {
		return Hold{}, ErrUnknownHold
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.reap(inv.clock.Now())
	h, err := e.lookup(holdID)
	if err != nil {
		return Hold{}, err
	}
	if h.extended {
		return Hold{}, ErrAlreadyExtended
	}
	h.extended = true
	h.expiresAt = h.expiresAt.Add(extra)
	heap.Fix(&e.expiry, h.heapIdx) // the reaper must see the new deadline
	return h.view(e.id), nil
}

// Sweep reaps every expired hold in every event and returns how many holds it
// expired. Nothing needs it for correctness (every operation reaps its own
// event first); it exists so seats are freed, and Availability is cheap, even
// for events nobody is touching.
func (inv *Inventory) Sweep() int {
	inv.mu.RLock()
	evs := make([]*event, 0, len(inv.events))
	for _, e := range inv.events {
		evs = append(evs, e)
	}
	inv.mu.RUnlock()
	n := 0
	for _, e := range evs { // one event lock at a time, never nested
		e.mu.Lock()
		n += e.reap(inv.clock.Now())
		e.mu.Unlock()
	}
	return n
}

// RunReaper calls Sweep once per value received on ticks (pass
// time.NewTicker(d).C in production, a channel the test controls in tests)
// and returns when ctx is done or ticks is closed. It starts no goroutines of
// its own: the caller decides where it runs, and cancelling ctx stops it.
func (inv *Inventory) RunReaper(ctx context.Context, ticks <-chan time.Time) {
	for {
		select {
		case <-ctx.Done():
			return
		case _, ok := <-ticks:
			if !ok {
				return
			}
			inv.Sweep()
		}
	}
}

// reap expires every active hold whose expiry is <= now: now >= ExpiresAt is
// "expired". Cost is O(k log n) for k expired holds, independent of how many
// live ones exist. Requires e.mu. Returns the number of holds expired.
func (e *event) reap(now time.Time) int {
	n := 0
	for len(e.expiry) > 0 && !now.Before(e.expiry[0].expiresAt) {
		h := heap.Pop(&e.expiry).(*hold)
		e.freeSeats(h)
		e.byUser[h.user]--
		h.status = holdExpired
		h.seats = nil // keep only the tombstone
		n++
	}
	return n
}
