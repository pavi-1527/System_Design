package orderbook

import "errors"

// Stage 3 (≈ 25 min): many symbols, trade streams, executable invariants.

// Stage 3 errors.
var (
	ErrClosed        = errors.New("orderbook: exchange closed")
	ErrUnknownSymbol = errors.New("orderbook: unknown symbol")
)

// CheckInvariants verifies the book's internal structure and returns the first
// violation, or nil. The tests call it after every operation. It should check
// at least: price levels are strictly ordered with no duplicate price; no level
// is empty; each level's Qty and Orders equal the sum over its orders; every
// resting order has Qty > 0 and sits on the right side and price; time
// priority holds inside each level (sequence numbers strictly increase from
// oldest to newest); your id index contains exactly the resting orders; and the
// book is not crossed.
func (b *Book) CheckInvariants() error {
	// TODO(lab)
	return errNotImplemented
}

// Exchange runs one Book per symbol and makes them safe for concurrent use.
//
// Decide and document your concurrency model. The reference uses one mutex per
// book (operations on one symbol are serialised, different symbols run in
// parallel) plus an RWMutex for the symbol table; one goroutine per book fed
// by a channel is the other classic answer. Whichever you choose, matching
// for a symbol must behave as if its operations arrived one at a time, in some
// order, and trade IDs per symbol must be exactly 1..N.
type Exchange struct {
	// TODO(lab)
}

// NewExchange returns an empty exchange. Books are created on first use.
func NewExchange() *Exchange {
	// TODO(lab)
	return &Exchange{}
}

// Submit routes o to the symbol's book (created on first use) and publishes
// the resulting trades to the symbol's subscribers. ErrClosed after Close.
func (x *Exchange) Submit(symbol string, o Order) ([]Trade, error) {
	// TODO(lab)
	_, _ = symbol, o
	return nil, errNotImplemented
}

// Cancel cancels a resting order. ErrUnknownSymbol if the symbol has never
// been used, ErrUnknownOrder if the order is not resting.
func (x *Exchange) Cancel(symbol, id string) error {
	// TODO(lab)
	_, _ = symbol, id
	return errNotImplemented
}

// Amend is Book.Amend routed by symbol; trades it causes are published too.
func (x *Exchange) Amend(symbol, id string, newQty, newPrice int64) ([]Trade, error) {
	// TODO(lab)
	_, _, _, _ = symbol, id, newQty, newPrice
	return nil, errNotImplemented
}

// Depth is Book.Depth routed by symbol. ErrUnknownSymbol if the symbol has
// never been used. It must be safe to call while other goroutines trade.
func (x *Exchange) Depth(symbol string, n int) (bids, asks []Level, err error) {
	// TODO(lab)
	_, _ = symbol, n
	return nil, nil, errNotImplemented
}

// CheckInvariants runs Book.CheckInvariants for the symbol, safely.
func (x *Exchange) CheckInvariants(symbol string) error {
	// TODO(lab)
	_ = symbol
	return errNotImplemented
}

// Subscription is a live feed of one symbol's trades.
//
// Slow-subscriber policy (required): C has a fixed buffer; if it is full when a
// trade is published, THAT trade is dropped for this subscriber and counted in
// Dropped. Matching must never wait for a subscriber. Every subscriber sees
// the trades it does receive in execution order (trade IDs increasing).
type Subscription struct {
	// C delivers trades. It is closed by Close or by Exchange.Close.
	C <-chan Trade
	// TODO(lab): whatever else you need.
}

// Subscribe starts a feed of the symbol's trades with the given buffer
// (minimum 1). The symbol is created if it does not exist yet. Only trades
// executed after Subscribe returns are delivered. ErrClosed after Close.
func (x *Exchange) Subscribe(symbol string, buf int) (*Subscription, error) {
	// TODO(lab)
	_, _ = symbol, buf
	return nil, errNotImplemented
}

// Dropped is how many trades were not delivered because the buffer was full.
func (s *Subscription) Dropped() uint64 {
	// TODO(lab)
	return 0
}

// Close ends the feed and closes C (trades already buffered can still be
// read). It is safe to call more than once and concurrently with trading.
func (s *Subscription) Close() {
	// TODO(lab)
}

// Close shuts the exchange down: every subscription is closed and later calls
// return ErrClosed. It is idempotent.
func (x *Exchange) Close() {
	// TODO(lab)
}
