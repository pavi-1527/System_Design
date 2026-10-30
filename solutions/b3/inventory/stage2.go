package inventory

import (
	"container/heap"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Stage 2 errors.
var (
	ErrNoLines     = errors.New("inventory: no lines")
	ErrKeyConflict = errors.New("inventory: idempotency key reused with a different request")
)

type keyEntry struct {
	fingerprint string
	res         Reservation // the response to replay
	id          string
}

func fingerprint(norm []Line, ttl time.Duration) string {
	var b strings.Builder
	fmt.Fprintf(&b, "ttl=%d", ttl)
	for _, l := range norm {
		fmt.Fprintf(&b, "|%q=%d", l.SKU, l.Qty)
	}
	return b.String()
}

// ReserveAll holds every line or none: if any line cannot be satisfied nothing
// is held (no partial reservation is left behind). Duplicate SKUs are summed.
// One reservation, one ID, one expiry. Errors: ErrNoLines, ErrInvalidQty,
// ErrInvalidTTL, ErrUnknownSKU, ErrInsufficient.
func (inv *Inventory) ReserveAll(lines []Line, ttl time.Duration) (Reservation, error) {
	inv.mu.Lock()
	defer inv.mu.Unlock()
	now := inv.tick()
	r, err := inv.reserveLocked(now, lines, ttl)
	if err != nil {
		return Reservation{}, err
	}
	return r.snapshot(), nil
}

// ReserveKeyed is ReserveAll with an idempotency key. The first successful call
// with a key creates the reservation and remembers it; a retry with the same
// key and the same request (same lines in any order, same ttl) returns the
// ORIGINAL Reservation value unchanged, whatever has happened to it since, and
// takes no further stock. The same key with a different request is
// ErrKeyConflict. A failed attempt is not remembered, so it can be retried
// under the same key. An empty key behaves like ReserveAll.
func (inv *Inventory) ReserveKeyed(key string, lines []Line, ttl time.Duration) (Reservation, error) {
	inv.mu.Lock()
	defer inv.mu.Unlock()
	now := inv.tick()
	if key == "" {
		r, err := inv.reserveLocked(now, lines, ttl)
		if err != nil {
			return Reservation{}, err
		}
		return r.snapshot(), nil
	}
	norm, err := normalize(lines)
	if err != nil {
		return Reservation{}, err
	}
	fp := fingerprint(norm, ttl)
	if e, ok := inv.keys[key]; ok {
		if e.fingerprint != fp {
			return Reservation{}, ErrKeyConflict
		}
		out := e.res
		out.Lines = append([]Line(nil), e.res.Lines...)
		return out, nil
	}
	r, err := inv.reserveLocked(now, lines, ttl)
	if err != nil {
		return Reservation{}, err
	}
	r.key = key
	inv.keys[key] = &keyEntry{fingerprint: fp, res: r.snapshot(), id: r.id}
	return r.snapshot(), nil
}

// Extend moves an active reservation's expiry to now+ttl (earlier or later).
// It takes no extra stock. Errors: ErrNotFound, ErrExpired, ErrAlreadyDone,
// ErrInvalidTTL.
func (inv *Inventory) Extend(id string, ttl time.Duration) (Reservation, error) {
	if ttl <= 0 {
		return Reservation{}, ErrInvalidTTL
	}
	inv.mu.Lock()
	defer inv.mu.Unlock()
	now := inv.tick()
	r, err := inv.activeResv(id)
	if err != nil {
		return Reservation{}, err
	}
	r.expires = now.Add(ttl)
	heap.Fix(&inv.exp, r.idx)
	return r.snapshot(), nil
}

// Restock adds units to a SKU's Total. Errors: ErrUnknownSKU, ErrInvalidQty.
func (inv *Inventory) Restock(name string, qty int) error {
	if qty <= 0 {
		return ErrInvalidQty
	}
	inv.mu.Lock()
	defer inv.mu.Unlock()
	now := inv.tick()
	s, ok := inv.skus[name]
	if !ok {
		return ErrUnknownSKU
	}
	s.total += qty
	inv.serve(s, now)
	return nil
}
