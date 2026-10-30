// Package orderbook is a single-symbol limit order book with a price-time
// priority matching engine, plus (Stage 3) a multi-symbol Exchange.
//
// Lab B3 Unit 6 §2 (problem 13), time box 90 minutes. Work in stages and open
// the next stage's file only when you get there, like an interviewer who adds
// requirements as you go:
//
//	Stage 1 (≈ 35 min)  this file:  Submit, Cancel, Lookup, Depth: limit orders,
//	                                 price-time priority, partial fills
//	Stage 2 (≈ 30 min)  stage2.go:   market / IOC / FOK / post-only orders,
//	                                 Amend, self-trade prevention
//	Stage 3 (≈ 25 min)  stage3.go:   Exchange (many symbols, trade streams),
//	                                 CheckInvariants, determinism
//
// Run one stage at a time:
//
//	go test -race -run 'Stage1' ./labs/b3/orderbook/
//
// Ground rules for the whole problem:
//
//   - Prices are int64 ticks and quantities int64 lots. Never float64.
//   - Time priority is a logical sequence number your book assigns (a counter),
//     never time.Now. The same input sequence must always give the same output.
//   - Book is the single-threaded engine core: it needs no locks. Making many
//     symbols safe for concurrent use is the Exchange's job (Stage 3).
package orderbook

import "errors"

// Errors returned by Book and Exchange operations. Compare with errors.Is.
var (
	ErrInvalidOrder = errors.New("orderbook: invalid order")
	ErrDuplicateID  = errors.New("orderbook: duplicate order id")
	ErrUnknownOrder = errors.New("orderbook: unknown order")
)

var errNotImplemented = errors.New("orderbook: not implemented")

// Side is the side of the book an order belongs to. The zero value is invalid.
type Side int8

// Order sides.
const (
	Buy Side = iota + 1
	Sell
)

func (s Side) String() string {
	switch s {
	case Buy:
		return "buy"
	case Sell:
		return "sell"
	}
	return "invalid"
}

// Order is an instruction to buy or sell. Price is in ticks and Qty in lots.
//
// Stage 1 uses only ID, Owner, Side, Price and Qty; the zero values of Type,
// TIF and PostOnly (defined in stage2.go) mean "a plain limit order that rests
// until filled or cancelled": Limit, GTC, false.
type Order struct {
	ID       string // unique among live (resting) orders
	Owner    string // optional; used by self-trade prevention (Stage 2)
	Side     Side
	Type     OrderType // Stage 2
	TIF      TIF       // Stage 2
	PostOnly bool      // Stage 2
	Price    int64     // ticks, > 0 for limit orders, must be 0 for market orders
	Qty      int64     // lots, > 0
}

// Trade records one match between an incoming (taker) and a resting (maker)
// order. It always executes at the MAKER's price.
type Trade struct {
	ID         uint64 // 1, 2, 3, ... per book, in execution order
	Symbol     string
	Price      int64
	Qty        int64
	TakerID    string
	MakerID    string
	TakerOwner string
	MakerOwner string
	TakerSide  Side
}

// Level is the aggregate of the resting orders at one price.
type Level struct {
	Price  int64
	Qty    int64 // total remaining quantity at this price
	Orders int   // number of resting orders at this price
}

// Book is a limit order book for one symbol. It is NOT safe for concurrent
// use: it is the single-threaded core that Exchange serialises access to.
type Book struct {
	// TODO(lab): bid and ask sides, an id -> order index, a sequence counter,
	// a trade-id counter, the symbol. Think about the data structure first:
	// what do you need to be fast? (best price, oldest-first within a price,
	// cancel by id.)
	symbol string
}

// NewBook returns an empty book for symbol.
func NewBook(symbol string) *Book {
	// TODO(lab)
	return &Book{symbol: symbol}
}

// Submit validates o, matches it against the opposite side by price-time
// priority (best price first; within a price, the order that arrived first),
// and rests any unfilled remainder of a limit order. Trades execute at the
// RESTING order's price. The returned trades are in execution order; trade IDs
// are consecutive per book starting at 1.
//
// Errors, all leaving the book unchanged and returning no trades:
//   - ErrInvalidOrder: empty ID, side not Buy/Sell, Qty <= 0, or a limit order
//     with Price <= 0.
//   - ErrDuplicateID: the ID belongs to a resting order (check this BEFORE
//     matching). An ID that has been filled or cancelled is free again.
//
// After Submit returns the book is never crossed: best bid < best ask.
func (b *Book) Submit(o Order) ([]Trade, error) {
	// TODO(lab)
	_ = o
	return nil, errNotImplemented
}

// Cancel removes a resting order. ErrUnknownOrder if no resting order has that
// ID (never existed, already filled, or already cancelled). It must not change
// the time priority of any other order.
func (b *Book) Cancel(id string) error {
	// TODO(lab)
	_ = id
	return errNotImplemented
}

// Lookup returns a copy of the resting order with this ID; Qty is the
// REMAINING quantity. ok is false if there is no such resting order.
func (b *Book) Lookup(id string) (o Order, ok bool) {
	// TODO(lab)
	_ = id
	return Order{}, false
}

// Depth returns up to n price levels per side, best first: bids from the
// highest price down, asks from the lowest price up. n <= 0 means all levels.
// A price with no resting orders never appears.
func (b *Book) Depth(n int) (bids, asks []Level) {
	// TODO(lab)
	_ = n
	return nil, nil
}
