package vending

import (
	"errors"
	"time"
)

// Stage 2 (≈ 20 min): maintenance mode, idle timeout, and notices.

// ErrInvalidArgument is returned for a bad quantity, price or negative float
// passed to an admin operation.
var ErrInvalidArgument = errors.New("vending: invalid argument")

// WithClock injects the time source used to timestamp coin insertions
// (default time.Now).
func WithClock(now func() time.Time) Option {
	// TODO(lab)
	_ = now
	return func(*Machine) {}
}

// WithIdleTimeout makes Tick refund a session that has seen no coin for d.
// Zero (the default) disables the timeout.
func WithIdleTimeout(d time.Duration) Option {
	// TODO(lab)
	_ = d
	return func(*Machine) {}
}

// WithLowStockAlert makes a sale that leaves a slot with threshold or fewer
// items emit a NoticeLowStock. Zero disables the alert.
func WithLowStockAlert(threshold int) Option {
	// TODO(lab)
	_ = threshold
	return func(*Machine) {}
}

// EnterService puts an Idle machine out of service for maintenance. Idle only:
// with a customer's money inside it is ErrInvalidState.
func (m *Machine) EnterService() error {
	// TODO(lab)
	return errNotImplemented
}

// ExitService brings an OutOfService machine back to Idle. OutOfService only.
func (m *Machine) ExitService() error {
	// TODO(lab)
	return errNotImplemented
}

// Restock adds qty > 0 items to a slot. OutOfService only (other states:
// ErrInvalidState). ErrUnknownSlot, ErrInvalidArgument.
func (m *Machine) Restock(code string, qty int) error {
	// TODO(lab)
	_, _ = code, qty
	return errNotImplemented
}

// SetPrice changes a slot's price to price > 0. OutOfService only.
func (m *Machine) SetPrice(code string, price int) error {
	// TODO(lab)
	_, _ = code, price
	return errNotImplemented
}

// LoadCoins adds n > 0 coins of one accepted denomination to the change box.
// OutOfService only. ErrBadCoin for a denomination the machine doesn't accept.
func (m *Machine) LoadCoins(denom, n int) error {
	// TODO(lab)
	_, _ = denom, n
	return errNotImplemented
}

// CollectCash empties the change box down to keep (>= 0) coins of each
// denomination and returns what was removed. OutOfService only.
func (m *Machine) CollectCash(keep int) (map[int]int, error) {
	// TODO(lab)
	_ = keep
	return nil, errNotImplemented
}

// Tick tells the machine what time it is. It is the one event valid in every
// state, because a clock that ticks must not be an error source. In HasMoney,
// if no coin has been inserted for at least the idle timeout (now minus the
// time of the latest coin, from the injected clock), it refunds the session
// exactly like Cancel and returns the refunded coins. In every other case it
// returns (nil, nil). Stage 3 also uses Tick to expire card sales.
func (m *Machine) Tick(now time.Time) (map[int]int, error) {
	// TODO(lab)
	_ = now
	return nil, errNotImplemented
}

// NoticeKind says what a Notice reports.
type NoticeKind int

// Notice kinds.
const (
	NoticeStateChanged   NoticeKind = iota + 1 // From and To are set
	NoticeDispensed                            // Slot, Amount (the price)
	NoticeRefunded                             // Amount (cents), Reason "cancel" or "timeout"
	NoticeLowStock                             // Slot, Qty remaining
	NoticeDispenseFailed                       // Slot
)

// Notice is something that happened to the machine.
//
// Notices are announced in the order the effects occur. A purchase produces:
// StateChanged(HasMoney->Dispensing), Dispensed, [LowStock], StateChanged
// (Dispensing->Idle). A cancel or timeout produces Refunded and then
// StateChanged(HasMoney->Idle). Only real changes are announced: a second coin
// while already in HasMoney is not a state change.
type Notice struct {
	Kind     NoticeKind
	From, To State
	Slot     string
	Amount   int
	Qty      int
	Reason   string
}

// Subscribe registers fn to receive every Notice and returns a function that
// unsubscribes. Delivery contract (tested):
//   - each listener sees notices in order, one at a time;
//   - the machine's lock is NOT held while a listener runs, so a listener may
//     call back into the machine (State, Balance, Snapshot, even events);
//   - a panicking listener is contained: the machine and the other listeners
//     carry on;
//   - a stuck listener must not block other callers of the machine.
func (m *Machine) Subscribe(fn func(Notice)) (cancel func()) {
	// TODO(lab)
	_ = fn
	return func() {}
}
