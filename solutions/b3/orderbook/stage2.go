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
	// rests: any remainder is dropped. Its Price must be 0.
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
	// trades and the book unchanged.
	FOK
)

// Stage 2 errors.
var (
	// ErrWouldCross: a PostOnly order would have traded on arrival.
	ErrWouldCross = errors.New("orderbook: post-only order would cross")
	// ErrNotFillable: a FOK order cannot be filled in full.
	ErrNotFillable = errors.New("orderbook: fill-or-kill order cannot be filled")
)

// SelfTradePolicy says what happens when an incoming order would trade against
// a resting order with the same non-empty Owner.
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
func (b *Book) SetSelfTradePolicy(p SelfTradePolicy) { b.stp = p }

// fillable reports, without mutating the book, whether o could trade its whole
// quantity right now. It honours the self-trade policy: an own resting order
// is skipped under STPCancelResting and ends the walk under STPCancelIncoming.
func (b *Book) fillable(o Order) bool {
	opp := b.side(o.Side.opposite())
	need := o.Qty
	for i := len(opp.levels) - 1; i >= 0 && need > 0; i-- {
		lvl := opp.levels[i]
		if !crossesAt(&o, lvl.price) {
			break
		}
		if o.Owner == "" || b.stp == STPAllow {
			need -= lvl.qty
			continue
		}
		for n := lvl.head; n != nil && need > 0; n = n.next {
			if n.o.Owner == o.Owner {
				if b.stp == STPCancelIncoming {
					return false
				}
				continue
			}
			need -= n.o.Qty
		}
	}
	return need <= 0
}

// Amend changes a resting order's REMAINING quantity and/or price.
//
//   - Same price, smaller quantity: the order keeps its place in the queue.
//   - Same price, larger quantity: the order goes to the back of its level.
//   - Different price: the order is cancelled and re-entered at the new price
//     with a fresh time priority, so it may trade immediately (the trades are
//     returned). A PostOnly order whose new price would cross is rejected
//     with ErrWouldCross and left untouched.
//   - Same price and quantity: nothing changes.
//
// Errors: ErrUnknownOrder, ErrInvalidOrder (newQty or newPrice <= 0).
func (b *Book) Amend(id string, newQty, newPrice int64) ([]Trade, error) {
	n, ok := b.ids[id]
	if !ok {
		return nil, ErrUnknownOrder
	}
	if newQty <= 0 || newPrice <= 0 {
		return nil, ErrInvalidOrder
	}
	o := n.o
	if newPrice == o.Price {
		switch {
		case newQty == o.Qty:
		case newQty < o.Qty:
			n.lvl.qty -= o.Qty - newQty
			n.o.Qty = newQty
		default:
			n.lvl.detach(n)
			n.o.Qty = newQty
			b.seq++
			n.seq = b.seq
			n.lvl.push(n)
		}
		return nil, nil
	}
	o.Qty, o.Price = newQty, newPrice
	if o.PostOnly && crossesBest(&o, b.side(o.Side.opposite())) {
		return nil, ErrWouldCross
	}
	b.unlink(n)
	return b.execute(o)
}
