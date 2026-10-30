package orderbook

import "errors"

// Stage 2 (≈ 30 min): order types, time in force, amend, self-trade prevention.

// OrderType selects how an order is priced.
type OrderType int8

// Order types. The zero value is Limit.
const (
	// Limit trades at its Price or better.
	Limit OrderType = iota
	// Market trades at any price. It sweeps the opposite side and never
	// rests: any remainder is dropped. Its Price must be 0 (else
	// ErrInvalidOrder), and it cannot be PostOnly.
	Market
)

// TIF is the time in force: what happens to the part of an order that did not
// trade immediately.
type TIF int8

// Time-in-force values. The zero value is GTC.
const (
	// GTC (good till cancelled): a limit order's remainder rests in the book.
	// A Market order with TIF GTC behaves as IOC: market orders never rest.
	GTC TIF = iota
	// IOC (immediate or cancel): trade what you can now, drop the remainder.
	IOC
	// FOK (fill or kill): trade the whole quantity now or do nothing at all.
	// If it cannot be filled completely Submit returns ErrNotFillable with no
	// trades and the book UNCHANGED, so check feasibility before mutating.
	FOK
)

// Stage 2 errors.
var (
	// ErrWouldCross: a PostOnly order would have traded on arrival (its price
	// is equal to or better than the best opposite price). Nothing changes.
	ErrWouldCross = errors.New("orderbook: post-only order would cross")
	// ErrNotFillable: a FOK order cannot be filled in full.
	ErrNotFillable = errors.New("orderbook: fill-or-kill order cannot be filled")
)

// Additional validation for Submit in this stage: a TIF outside GTC..FOK is
// ErrInvalidOrder; PostOnly requires a Limit order with TIF GTC.

// SelfTradePolicy says what happens when an incoming order would trade against
// a resting order with the same NON-EMPTY Owner (empty means "no owner").
type SelfTradePolicy int8

// Self-trade prevention policies. The zero value, STPAllow, trades normally.
const (
	STPAllow SelfTradePolicy = iota
	// STPCancelIncoming stops matching and drops the rest of the incoming
	// order. Trades already made with other owners' better-priority orders
	// stand; the resting order is untouched.
	STPCancelIncoming
	// STPCancelResting cancels the resting order and keeps matching the
	// incoming order against the next one.
	STPCancelResting
)

// SetSelfTradePolicy sets the policy applied by later Submit and Amend calls.
// A FOK order must honour the policy when deciding whether it is fillable.
func (b *Book) SetSelfTradePolicy(p SelfTradePolicy) {
	// TODO(lab)
	_ = p
}

// Amend changes a resting order's REMAINING quantity and/or price.
//
//   - Same price, smaller quantity: the order keeps its place in the queue.
//   - Same price, larger quantity: the order goes to the back of its level.
//   - Different price: the order is cancelled and re-entered at the new price
//     with a fresh time priority, so it may trade immediately (those trades
//     are returned). A PostOnly order whose new price would cross is rejected
//     with ErrWouldCross and left exactly as it was.
//   - Same price and quantity: nothing changes.
//
// Errors: ErrUnknownOrder, ErrInvalidOrder (newQty <= 0 or newPrice <= 0; use
// Cancel to remove an order).
func (b *Book) Amend(id string, newQty, newPrice int64) ([]Trade, error) {
	// TODO(lab)
	_, _, _ = id, newQty, newPrice
	return nil, errNotImplemented
}
