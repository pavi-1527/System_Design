package inventory

import (
	"errors"
	"time"
)

// Stage 3 (≈ 20 min): a library on the same core, a fair waitlist, and the
// concurrency contract.

// Stage 3 errors.
var (
	ErrLimitReached = errors.New("inventory: member loan limit reached")
	ErrNotYours     = errors.New("inventory: hold belongs to another member")
	ErrInvalidDue   = errors.New("inventory: due date must be in the future")
	ErrInvalidWho   = errors.New("inventory: member must not be empty")
)

// Loan is a unit lent to a member until DueAt.
type Loan struct {
	ID     string
	SKU    string
	Member string
	DueAt  time.Time
}

// TicketState is where a waitlist ticket is in its life.
type TicketState string

// Ticket states.
const (
	Waiting   TicketState = "waiting"   // in the queue
	Ready     TicketState = "ready"     // a hold is in place; claim it before it expires
	Claimed   TicketState = "claimed"   // the hold was committed or borrowed
	Expired   TicketState = "expired"   // the hold lapsed unclaimed; the units moved on
	Cancelled TicketState = "cancelled" // withdrawn by the member (or the hold was released)
)

// Ticket is a place in a SKU's waitlist.
type Ticket struct {
	ID     string
	Member string
	SKU    string
	Qty    int
}

// TicketStatus describes a ticket now. Position is 1-based and only set while
// Waiting; Reservation is only set once a hold exists (Ready and later).
type TicketStatus struct {
	State       TicketState
	Position    int
	Reservation Reservation
}

// SetMemberLimit caps how many unreturned loans one member may hold across all
// SKUs. Zero or negative means unlimited.
func (inv *Inventory) SetMemberLimit(n int) {
	// TODO(lab)
	_ = n
}

// Borrow lends one unit of sku to member until due: it is Reserve+Commit as one
// atomic step, recorded as a Loan. Errors: ErrInvalidWho, ErrInvalidDue (due
// must be after now), ErrUnknownSKU, ErrLimitReached (checked before stock),
// ErrInsufficient (also while others are waitlisted for the SKU: no barging).
func (inv *Inventory) Borrow(member, sku string, due time.Time) (Loan, error) {
	// TODO(lab)
	_, _, _ = member, sku, due
	return Loan{}, errNotImplemented
}

// BorrowHeld converts an active single-unit reservation (typically a waitlist
// hold) into a loan for member; the unit counts once in Committed. Errors:
// ErrInvalidWho, ErrNotFound, ErrExpired, ErrAlreadyDone, ErrNotYours (the hold
// was made for another member), ErrInvalidQty (not exactly one unit of one
// SKU), ErrInvalidDue, ErrLimitReached (the hold survives a refusal).
func (inv *Inventory) BorrowHeld(reservationID, member string, due time.Time) (Loan, error) {
	// TODO(lab)
	_, _, _ = reservationID, member, due
	return Loan{}, errNotImplemented
}

// Return takes a loaned unit back and offers it to the waitlist.
// Errors: ErrNotFound, ErrAlreadyDone (returning twice must not free two units).
func (inv *Inventory) Return(loanID string) error {
	// TODO(lab)
	_ = loanID
	return errNotImplemented
}

// Loans lists the member's unreturned loans in the order they were made.
func (inv *Inventory) Loans(member string) []Loan {
	// TODO(lab)
	_ = member
	return nil
}

// Enqueue puts member at the back of sku's waitlist for qty units. When units
// free up (Release, expiry, Return, Restock, CancelWait) the OLDEST ticket is
// served first: a hold of holdTTL is placed for it and its Status becomes
// Ready. If the units are already available and nobody is queued, the ticket is
// Ready immediately.
//
// Service is strictly FIFO: if the oldest ticket wants more than is available,
// later tickets wait behind it, and direct Reserve/ReserveAll/Borrow calls on
// that SKU are refused (ErrInsufficient) so nobody can barge past the queue.
// A Ready hold nobody claims expires like any reservation, and its units go to
// the next waiter without anyone calling anything.
// Errors: ErrInvalidWho, ErrInvalidQty, ErrInvalidTTL, ErrUnknownSKU.
func (inv *Inventory) Enqueue(member, sku string, qty int, holdTTL time.Duration) (Ticket, error) {
	// TODO(lab)
	_, _, _, _ = member, sku, qty, holdTTL
	return Ticket{}, errNotImplemented
}

// Status reports a ticket's state. Errors: ErrNotFound.
func (inv *Inventory) Status(ticketID string) (TicketStatus, error) {
	// TODO(lab)
	_ = ticketID
	return TicketStatus{}, errNotImplemented
}

// CancelWait withdraws a ticket. A Waiting ticket leaves the queue (the new
// head may now fit); a Ready ticket's hold is released and offered to the next
// waiter. Errors: ErrNotFound, ErrExpired, ErrAlreadyDone (claimed or already
// cancelled).
func (inv *Inventory) CancelWait(ticketID string) error {
	// TODO(lab)
	_ = ticketID
	return errNotImplemented
}

// Prune forgets records that ended at least age ago: committed, released and
// expired reservations, returned loans and finished tickets, together with the
// idempotency keys of pruned reservations. Afterwards their IDs are
// ErrNotFound and their keys are free to be reused. This is the compaction
// step that stands in for a janitor goroutine. It returns how many records it
// removed. Active reservations, unreturned loans and queued tickets are never
// touched, and Stock counters are unaffected.
func (inv *Inventory) Prune(age time.Duration) int {
	// TODO(lab)
	_ = age
	return 0
}
