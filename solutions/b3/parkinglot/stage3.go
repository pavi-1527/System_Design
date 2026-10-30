package parkinglot

import "sort"

// ---- Stage 3: many gates at once (announced at about minute 50) -------------
//
// Contract:
//   - Park, Unpark, UnparkLost, Availability and Parked are safe to call from
//     any number of goroutines (entry gates and exit gates).
//   - No spot is ever held by two vehicles, no plate is ever parked twice,
//     and a ticket is redeemed at most once, however the calls interleave.
//     Of N concurrent Park calls for the same plate exactly one succeeds
//     (the rest get ErrAlreadyParked); of N concurrent Unpark/UnparkLost calls
//     for the same vehicle exactly one succeeds.
//   - Availability is a consistent snapshot: Free + Occupied == Total and the
//     per-size and per-level counts sum to Free, even while other gates work.
//   - A slow Pricer or Clock must not stop other gates from parking or
//     leaving: user code does not run under the lot's lock (the Allocator is
//     the documented exception).
//   - Cost per Park/Unpark must not grow with the number of spots: a test
//     fills and drains a 50,000-spot lot under a time cap.
//   - Parked returns a snapshot of every live ticket, ordered by (Level,
//     Number). The caller owns the slice.
//   - Choose a lock granularity and be ready to defend it in the review.

// Parked returns every live ticket, ordered by spot (Level, then Number).
func (l *Lot) Parked() []Ticket {
	l.mu.Lock()
	out := make([]Ticket, 0, len(l.byTicket))
	for _, t := range l.byTicket {
		out = append(out, t)
	}
	l.mu.Unlock()
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i].Spot, out[j].Spot
		if a.Level != b.Level {
			return a.Level < b.Level
		}
		return a.Number < b.Number
	})
	return out
}
