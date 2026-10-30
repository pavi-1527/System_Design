package inventory

import (
	"errors"
	"sort"
	"strings"
	"time"
)

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

type loan struct {
	Loan
	returned   bool
	returnedAt time.Time
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

type ticket struct {
	Ticket
	ttl       time.Duration
	res       *resv
	cancelled bool
	ended     time.Time
}

// SetMemberLimit caps how many unreturned loans one member may hold across all
// SKUs. Zero or negative means unlimited.
func (inv *Inventory) SetMemberLimit(n int) {
	inv.mu.Lock()
	defer inv.mu.Unlock()
	inv.limit = n
}

func (inv *Inventory) limitOK(member string) bool {
	return inv.limit <= 0 || inv.active[member] < inv.limit
}

// lend records a loan for a unit that is already counted as committed.
func (inv *Inventory) lend(s *sku, member string, due time.Time) Loan {
	inv.active[member]++
	l := &loan{Loan: Loan{ID: inv.nextID("l"), SKU: s.name, Member: member, DueAt: due}}
	inv.loans[l.ID] = l
	return l.Loan
}

// Borrow lends one unit of sku to member until due. Errors: ErrInvalidWho,
// ErrInvalidDue, ErrUnknownSKU, ErrLimitReached (checked before stock),
// ErrInsufficient (also while others are waitlisted for the SKU).
func (inv *Inventory) Borrow(member, name string, due time.Time) (Loan, error) {
	inv.mu.Lock()
	defer inv.mu.Unlock()
	now := inv.tick()
	if member == "" {
		return Loan{}, ErrInvalidWho
	}
	if !due.After(now) {
		return Loan{}, ErrInvalidDue
	}
	s, ok := inv.skus[name]
	if !ok {
		return Loan{}, ErrUnknownSKU
	}
	if !inv.limitOK(member) {
		return Loan{}, ErrLimitReached
	}
	if len(s.waiters) > 0 || s.avail() < 1 {
		return Loan{}, ErrInsufficient
	}
	s.committed++
	return inv.lend(s, member, due), nil
}

// BorrowHeld converts an active single-unit reservation (typically a waitlist
// hold) into a loan for member. Errors: ErrNotFound, ErrExpired,
// ErrAlreadyDone, ErrNotYours (the hold was made for another member),
// ErrInvalidQty (not exactly one unit of one SKU), ErrInvalidDue,
// ErrLimitReached.
func (inv *Inventory) BorrowHeld(id, member string, due time.Time) (Loan, error) {
	inv.mu.Lock()
	defer inv.mu.Unlock()
	now := inv.tick()
	if member == "" {
		return Loan{}, ErrInvalidWho
	}
	r, err := inv.activeResv(id)
	if err != nil {
		return Loan{}, err
	}
	if r.member != "" && r.member != member {
		return Loan{}, ErrNotYours
	}
	if len(r.lines) != 1 || r.lines[0].Qty != 1 {
		return Loan{}, ErrInvalidQty
	}
	if !due.After(now) {
		return Loan{}, ErrInvalidDue
	}
	if !inv.limitOK(member) {
		return Loan{}, ErrLimitReached
	}
	s := inv.skus[r.lines[0].SKU]
	inv.commitLocked(now, r)
	return inv.lend(s, member, due), nil
}

// Return takes a loaned unit back and offers it to the waitlist. Errors:
// ErrNotFound, ErrAlreadyDone.
func (inv *Inventory) Return(loanID string) error {
	inv.mu.Lock()
	defer inv.mu.Unlock()
	now := inv.tick()
	l, ok := inv.loans[loanID]
	if !ok {
		return ErrNotFound
	}
	if l.returned {
		return ErrAlreadyDone
	}
	l.returned, l.returnedAt = true, now
	s := inv.skus[l.SKU]
	s.committed--
	inv.active[l.Member]--
	inv.serve(s, now)
	return nil
}

// Loans lists the member's unreturned loans in the order they were made.
func (inv *Inventory) Loans(member string) []Loan {
	inv.mu.Lock()
	defer inv.mu.Unlock()
	inv.tick()
	var out []Loan
	for _, l := range inv.loans {
		if l.Member == member && !l.returned {
			out = append(out, l.Loan)
		}
	}
	sortLoans(out)
	return out
}

// Enqueue puts member at the back of sku's waitlist for qty units. When the
// units free up (Release, expiry, Return, Restock) the oldest ticket is served
// first: a hold of holdTTL is placed for it and its Status becomes Ready.
// Service is strictly FIFO: if the oldest ticket wants more than is available,
// later tickets wait behind it, and direct Reserve/Borrow calls on the SKU are
// refused (ErrInsufficient) so nobody can barge past the queue.
func (inv *Inventory) Enqueue(member, name string, qty int, holdTTL time.Duration) (Ticket, error) {
	inv.mu.Lock()
	defer inv.mu.Unlock()
	now := inv.tick()
	if member == "" {
		return Ticket{}, ErrInvalidWho
	}
	if qty <= 0 {
		return Ticket{}, ErrInvalidQty
	}
	if holdTTL <= 0 {
		return Ticket{}, ErrInvalidTTL
	}
	s, ok := inv.skus[name]
	if !ok {
		return Ticket{}, ErrUnknownSKU
	}
	t := &ticket{Ticket: Ticket{ID: inv.nextID("t"), Member: member, SKU: name, Qty: qty}, ttl: holdTTL}
	inv.tickets[t.ID] = t
	s.waiters = append(s.waiters, t)
	inv.serve(s, now)
	return t.Ticket, nil
}

// serve hands units to waiters, oldest first, while the head's request fits.
// Caller holds inv.mu.
func (inv *Inventory) serve(s *sku, now time.Time) {
	for len(s.waiters) > 0 {
		t := s.waiters[0]
		if s.avail() < t.Qty {
			return
		}
		s.waiters = s.waiters[1:]
		t.res = inv.newResv(now, []Line{{SKU: s.name, Qty: t.Qty}}, t.ttl, t.Member)
	}
}

// Status reports a ticket's state. Errors: ErrNotFound.
func (inv *Inventory) Status(ticketID string) (TicketStatus, error) {
	inv.mu.Lock()
	defer inv.mu.Unlock()
	inv.tick()
	t, ok := inv.tickets[ticketID]
	if !ok {
		return TicketStatus{}, ErrNotFound
	}
	return inv.statusLocked(t), nil
}

func (inv *Inventory) statusLocked(t *ticket) TicketStatus {
	switch {
	case t.cancelled:
		return TicketStatus{State: Cancelled}
	case t.res == nil:
		for i, w := range inv.skus[t.SKU].waiters {
			if w == t {
				return TicketStatus{State: Waiting, Position: i + 1}
			}
		}
		return TicketStatus{State: Waiting}
	}
	st := TicketStatus{Reservation: t.res.snapshot()}
	switch t.res.state {
	case stActive:
		st.State = Ready
	case stCommitted:
		st.State = Claimed
	case stExpired:
		st.State = Expired
	default:
		st.State = Cancelled
	}
	return st
}

// CancelWait withdraws a ticket. A Waiting ticket leaves the queue; a Ready
// ticket's hold is released (and offered to the next waiter). Errors:
// ErrNotFound, ErrAlreadyDone (claimed or already cancelled), ErrExpired.
func (inv *Inventory) CancelWait(ticketID string) error {
	inv.mu.Lock()
	defer inv.mu.Unlock()
	now := inv.tick()
	t, ok := inv.tickets[ticketID]
	if !ok {
		return ErrNotFound
	}
	switch st := inv.statusLocked(t).State; st {
	case Waiting:
		s := inv.skus[t.SKU]
		for i, w := range s.waiters {
			if w == t {
				s.waiters = append(s.waiters[:i:i], s.waiters[i+1:]...)
				break
			}
		}
		t.cancelled, t.ended = true, now
		inv.serve(s, now) // the head may now fit
		return nil
	case Ready:
		inv.releaseLocked(now, t.res)
		return nil
	case Expired:
		return ErrExpired
	default:
		return ErrAlreadyDone
	}
}

// Prune forgets records that ended at least age ago: committed, released and
// expired reservations, returned loans and finished tickets, together with the
// idempotency keys of pruned reservations. Afterwards their IDs are
// ErrNotFound and their keys are free to be reused. It is the compaction step
// that stands in for a janitor goroutine; call it from whatever scheduler you
// have. It returns how many records it removed. Active reservations, unreturned
// loans and queued tickets are never touched.
func (inv *Inventory) Prune(age time.Duration) int {
	inv.mu.Lock()
	defer inv.mu.Unlock()
	now := inv.tick()
	cutoff := now.Add(-age)
	n := 0
	for id, r := range inv.resv {
		if r.state != stActive && !r.ended.After(cutoff) {
			delete(inv.resv, id)
			if r.key != "" {
				delete(inv.keys, r.key)
			}
			n++
		}
	}
	for id, l := range inv.loans {
		if l.returned && !l.returnedAt.After(cutoff) {
			delete(inv.loans, id)
			n++
		}
	}
	for id, t := range inv.tickets {
		done := t.cancelled && !t.ended.After(cutoff)
		if t.res != nil {
			if _, kept := inv.resv[t.res.id]; !kept {
				done = true
			}
		}
		if done {
			delete(inv.tickets, id)
			n++
		}
	}
	return n
}

func sortLoans(ls []Loan) {
	// IDs are "l-<seq>"; sort numerically by sequence so borrow order is stable.
	sort.Slice(ls, func(i, j int) bool { return seqOf(ls[i].ID) < seqOf(ls[j].ID) })
}

func seqOf(id string) int {
	n := 0
	for _, c := range id[strings.IndexByte(id, '-')+1:] {
		n = n*10 + int(c-'0')
	}
	return n
}
