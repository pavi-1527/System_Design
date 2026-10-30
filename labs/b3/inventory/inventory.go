// Package inventory tracks quantity-based stock (SKUs with a number of units)
// and the temporary holds ("reservations") placed on it. It is the core of a
// shop's checkout, a card-authorisation hold, and a library's lending desk.
//
// Lab B3 Unit 7 §1 (problem 14), time box 75 minutes. Work in stages and open
// the next stage's file only when you get there, like an interviewer who adds
// requirements as you go:
//
//	Stage 1 (≈ 30 min)  this file:  AddSKU, Reserve, Commit, Release, Available, Stock
//	Stage 2 (≈ 25 min)  stage2.go:  ReserveAll, ReserveKeyed, Extend, Restock
//	Stage 3 (≈ 20 min)  stage3.go:  Borrow/Return, member limits, FIFO waitlist, Prune
//
// Run one stage at a time:
//
//	go test -race -run 'Stage1' ./labs/b3/inventory/
//
// # Model and invariants
//
// For every SKU, at every instant visible to a caller:
//
//	Total     = units ever stocked (AddSKU + Restock)
//	Committed = units that have left (Commit, Borrow) and not come back (Return)
//	Reserved  = units held by ACTIVE reservations (not expired, committed or released)
//	Available = Total - Committed - Reserved, and Available >= 0 always.
//
// A reservation is active while now < ExpiresAt. Expiry is LAZY: there is no
// Close method, so no goroutine and no timer may be started. Every operation
// first discards the reservations whose time has come, so an expired hold
// stops counting the moment anyone looks.
//
// Decide your locking model before you write Reserve. Stage 2 asks for
// all-or-nothing carts that overlap in opposite orders, and Stage 3 hammers
// the whole thing with 1,000 goroutines.
package inventory

import (
	"errors"
	"time"
)

// Errors. Compare with errors.Is.
var (
	ErrUnknownSKU   = errors.New("inventory: unknown SKU")
	ErrSKUExists    = errors.New("inventory: SKU already exists")
	ErrInsufficient = errors.New("inventory: insufficient stock")
	ErrInvalidQty   = errors.New("inventory: quantity must be positive")
	ErrInvalidTTL   = errors.New("inventory: ttl must be positive")
	ErrNotFound     = errors.New("inventory: reservation not found")
	ErrExpired      = errors.New("inventory: reservation expired")
	ErrAlreadyDone  = errors.New("inventory: reservation already committed or released")
)

var errNotImplemented = errors.New("inventory: not implemented")

// Line is a quantity of one SKU.
type Line struct {
	SKU string
	Qty int
}

// Reservation is a temporary claim on stock. Lines is normalized: duplicate
// SKUs are summed and the lines are sorted by SKU. The value is a COPY;
// changing it must not affect the inventory.
type Reservation struct {
	ID        string
	Lines     []Line
	ExpiresAt time.Time
}

// Stock is a point-in-time view of one SKU.
type Stock struct {
	Total     int
	Committed int
	Reserved  int
	Available int
}

// Inventory is safe for concurrent use.
type Inventory struct {
	// TODO(lab): state, a lock, an expiry structure, the clock.
	now func() time.Time
}

// New returns an empty inventory reading time from now (time.Now if nil).
// Tests pass a fake clock so expiry is deterministic.
func New(now func() time.Time) *Inventory {
	// TODO(lab)
	return &Inventory{now: now}
}

// AddSKU registers a SKU with total units; total may be zero.
// Errors: ErrInvalidQty (negative), ErrSKUExists.
func (inv *Inventory) AddSKU(sku string, total int) error {
	// TODO(lab)
	_, _ = sku, total
	return errNotImplemented
}

// Available is Total - Committed - Reserved for the SKU right now (expired
// reservations no longer count). Errors: ErrUnknownSKU.
func (inv *Inventory) Available(sku string) (int, error) {
	// TODO(lab)
	_ = sku
	return 0, errNotImplemented
}

// Stock returns the SKU's four counters as of now. Errors: ErrUnknownSKU.
func (inv *Inventory) Stock(sku string) (Stock, error) {
	// TODO(lab)
	_ = sku
	return Stock{}, errNotImplemented
}

// Reserve holds qty units of sku until now+ttl. A failed call changes nothing.
// Errors: ErrInvalidQty, ErrInvalidTTL, ErrUnknownSKU, ErrInsufficient.
//
// The returned Reservation has Lines = [{sku, qty}], a unique non-empty ID and
// ExpiresAt = now+ttl.
func (inv *Inventory) Reserve(sku string, qty int, ttl time.Duration) (Reservation, error) {
	// TODO(lab)
	_, _, _ = sku, qty, ttl
	return Reservation{}, errNotImplemented
}

// Commit turns an ACTIVE reservation into a permanent removal of stock
// (Committed grows, Reserved shrinks). It is what happens when the payment is
// captured. Errors: ErrNotFound, ErrExpired (now >= ExpiresAt), ErrAlreadyDone
// (already committed or released).
func (inv *Inventory) Commit(id string) error {
	// TODO(lab)
	_ = id
	return errNotImplemented
}

// Release gives an active reservation's units back. Errors: ErrNotFound,
// ErrExpired (it already lapsed), ErrAlreadyDone. Releasing twice must not
// return the units twice.
func (inv *Inventory) Release(id string) error {
	// TODO(lab)
	_ = id
	return errNotImplemented
}
