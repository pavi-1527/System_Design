package inventory

import (
	"errors"
	"time"
)

// Stage 2 (≈ 25 min): carts, retries and corrections.

// Stage 2 errors.
var (
	ErrNoLines     = errors.New("inventory: no lines")
	ErrKeyConflict = errors.New("inventory: idempotency key reused with a different request")
)

// ReserveAll holds every line or none: if any line cannot be satisfied nothing
// is held (no partial reservation may be left behind, even briefly visible to
// other goroutines). Duplicate SKUs are summed. One reservation, one ID, one
// expiry; Commit and Release act on all its lines. It must not deadlock when
// goroutines reserve overlapping SKUs in opposite orders ({a,b} vs {b,a}).
// Errors: ErrNoLines, ErrInvalidQty, ErrInvalidTTL, ErrUnknownSKU,
// ErrInsufficient. Reservation.Lines is normalized (summed, sorted by SKU).
func (inv *Inventory) ReserveAll(lines []Line, ttl time.Duration) (Reservation, error) {
	// TODO(lab): Reserve should become the one-line special case of this.
	_, _ = lines, ttl
	return Reservation{}, errNotImplemented
}

// ReserveKeyed is ReserveAll with an idempotency key (think: the order ID a
// payment callback carries). The first successful call with a key creates the
// reservation and remembers it; a retry with the same key and the same request
// (same lines in any order, same ttl) returns the ORIGINAL Reservation value
// unchanged, whatever has happened to it since, and takes no further stock.
// The same key with a different request is ErrKeyConflict. A FAILED attempt is
// not remembered, so it can be retried under the same key. An empty key
// behaves like ReserveAll. Concurrent duplicates must create one reservation.
func (inv *Inventory) ReserveKeyed(key string, lines []Line, ttl time.Duration) (Reservation, error) {
	// TODO(lab)
	_, _, _ = key, lines, ttl
	return Reservation{}, errNotImplemented
}

// Extend moves an active reservation's expiry to now+ttl (earlier or later)
// and returns the updated Reservation. It takes no extra stock.
// Errors: ErrInvalidTTL, ErrNotFound, ErrExpired, ErrAlreadyDone.
func (inv *Inventory) Extend(id string, ttl time.Duration) (Reservation, error) {
	// TODO(lab): whatever orders your reservations by expiry must stay correct
	// when one of them changes its expiry.
	_, _ = id, ttl
	return Reservation{}, errNotImplemented
}

// Restock adds units to a SKU's Total. Errors: ErrUnknownSKU, ErrInvalidQty.
func (inv *Inventory) Restock(sku string, qty int) error {
	// TODO(lab)
	_, _ = sku, qty
	return errNotImplemented
}
