package vending

import (
	"errors"
	"time"
)

// Stage 3 (≈ 15 min): hardware that can fail, card payments, concurrency.
//
// Concurrency contract (tested): all events are serialised, but the machine
// must NEVER hold its lock while calling the Dispenser or the Gateway. While
// the motor runs the state is Dispensing and any other customer event fails
// fast with ErrInvalidState instead of queueing behind the hardware. Select
// and Cancel racing for the same session: exactly one wins.

// Stage 3 errors.
var (
	ErrDispenseFailed = errors.New("vending: dispenser failed")
	ErrUnknownSale    = errors.New("vending: unknown sale")
	ErrSaleClosed     = errors.New("vending: sale is closed")
)

// Dispenser is the motor/hardware. Dispense may be slow and may fail. Reserve
// the stock BEFORE calling it (a snapshot taken meanwhile shows qty - 1), and
// release the reservation if it fails.
//
// A failed cash Select returns ErrDispenseFailed (wrapped), changes nothing
// else: the money stays in escrow, the state goes back to HasMoney, and a
// NoticeDispenseFailed is announced. Treat a panic in a collaborator as a
// failure too: the machine must not be stuck in Dispensing.
type Dispenser interface {
	Dispense(slot string) error
}

// Gateway settles card payments that a terminal has already authorised.
// Every authorisation must end in exactly one Capture or one Void.
type Gateway interface {
	Capture(authID string) error
	Void(authID string) error
}

// WithDispenser injects the hardware (default: always succeeds instantly).
func WithDispenser(d Dispenser) Option {
	// TODO(lab)
	_ = d
	return func(*Machine) {}
}

// WithGateway injects the card settlement gateway (default: no-op).
func WithGateway(g Gateway) Option {
	// TODO(lab)
	_ = g
	return func(*Machine) {}
}

// WithCardTimeout makes Tick expire a card sale still waiting for its terminal
// callback after d (measured from PayByCard, with the injected clock). Zero
// disables expiry.
func WithCardTimeout(d time.Duration) Option {
	// TODO(lab)
	_ = d
	return func(*Machine) {}
}

// SaleID identifies one card sale.
type SaleID string

// SaleStatus is where a card sale is.
type SaleStatus int

// Sale statuses.
const (
	SalePending       SaleStatus = iota // waiting for the terminal
	SaleDispensing                      // approved; motor running, then capture
	SaleCompleted                       // dispensed and captured
	SaleDeclined                        // terminal declined; nothing dispensed
	SaleExpired                         // no callback in time; nothing dispensed
	SaleFailed                          // approved but the motor failed; auth voided
	SaleCaptureFailed                   // dispensed, but Capture returned an error
)

// SaleInfo describes a card sale.
type SaleInfo struct {
	ID     SaleID
	Slot   string
	Price  int // the amount the terminal must authorise
	Status SaleStatus
	AuthID string
}

// PayByCard starts a card sale for slot code: Idle -> AwaitingCard. Idle only
// (ErrInvalidState otherwise); ErrUnknownSlot and ErrSoldOut are checked
// as in Select. The returned SaleInfo has a unique ID and Price for
// the terminal to authorise. Nothing is dispensed yet.
func (m *Machine) PayByCard(code string) (SaleInfo, error) {
	// TODO(lab)
	_ = code
	return SaleInfo{}, errNotImplemented
}

// Sale returns the current information for a sale (false if unknown).
func (m *Machine) Sale(id SaleID) (SaleInfo, bool) {
	// TODO(lab)
	_ = id
	return SaleInfo{}, false
}

// CardApproved is the terminal's "authorised" callback (authID names the
// authorisation). Terminals retry callbacks, so it must be idempotent per sale:
//
//   - first approval of a pending sale: reserve stock, AwaitingCard ->
//     Dispensing, dispense (lock released), then Capture(authID) exactly once
//     (lock released), then Idle. A Capture error is recorded as
//     SaleCaptureFailed; the callback still returns nil (the customer has the
//     item). If dispensing fails: release the reservation, Void(authID)
//     exactly once, status SaleFailed, back to Idle, return ErrDispenseFailed.
//   - a duplicate while the sale is being processed, or after it completed:
//     return nil, with no second dispense, capture or void. This holds for
//     concurrent duplicates too.
//   - the sale was declined or failed: ErrSaleClosed.
//   - the sale EXPIRED (Tick) before the approval arrived: ErrSaleClosed, and
//     the authorisation is Voided exactly once, however many late callbacks
//     arrive (the customer's hold must be released; they are never charged).
//   - unknown sale: ErrUnknownSale.
func (m *Machine) CardApproved(id SaleID, authID string) error {
	// TODO(lab)
	_, _ = id, authID
	return errNotImplemented
}

// CardDeclined is the terminal's "declined" callback: AwaitingCard -> Idle,
// status SaleDeclined, nothing dispensed or captured. Idempotent (a duplicate
// returns nil); on a sale that ended some other way it is ErrSaleClosed;
// ErrUnknownSale for an unknown sale.
func (m *Machine) CardDeclined(id SaleID) error {
	// TODO(lab)
	_ = id
	return errNotImplemented
}
