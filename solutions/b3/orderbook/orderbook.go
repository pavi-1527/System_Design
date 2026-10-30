// Package orderbook is a single-symbol limit order book with a price-time
// priority matching engine, plus (Stage 3) a multi-symbol Exchange.
//
// Lab B3 Unit 6 §2 (problem 13). Stage 1 is this file; Stage 2 lives in
// stage2.go and Stage 3 in stage3.go. Open those only when the interviewer
// (the unit text) has asked for them.
//
// Reference design:
//
//   - Prices and quantities are int64 (ticks and lots). Never floats.
//   - Each side is a slice of price levels sorted worst-to-best, so the best
//     level is the LAST element: taking it is O(1) and inserting a new level is
//     a binary search plus a copy.
//   - Each level is an intrusive doubly linked FIFO of resting orders, and an
//     id -> node map gives O(1) cancel.
//   - Time priority is a logical sequence number assigned by the book, never
//     time.Now: the same input sequence always produces the same output.
//   - Book is a single-threaded engine core. It has no locks. Concurrency is
//     the Exchange's job (one mutex per book).
package orderbook

import (
	"errors"
	"sort"
)

// Errors returned by Book and Exchange operations. Compare with errors.Is.
var (
	ErrInvalidOrder = errors.New("orderbook: invalid order")
	ErrDuplicateID  = errors.New("orderbook: duplicate order id")
	ErrUnknownOrder = errors.New("orderbook: unknown order")
)

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

func (s Side) opposite() Side {
	if s == Buy {
		return Sell
	}
	return Buy
}

// Order is an instruction to buy or sell. Price is in ticks and Qty in lots.
//
// Stage 1 uses only ID, Owner, Side, Price and Qty; the zero values of Type,
// TIF and PostOnly mean "a plain limit order that rests until filled or
// cancelled" (Limit, GTC, false).
type Order struct {
	ID       string // unique among live orders
	Owner    string // optional; used by self-trade prevention (Stage 2)
	Side     Side
	Type     OrderType // Stage 2
	TIF      TIF       // Stage 2
	PostOnly bool      // Stage 2
	Price    int64     // ticks, > 0 for limit orders, must be 0 for market orders
	Qty      int64     // lots, > 0
}

// Trade records one match between an incoming (taker) and a resting (maker)
// order. It always executes at the maker's price.
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

type node struct {
	o          Order // o.Qty is the REMAINING quantity
	seq        uint64
	lvl        *level
	prev, next *node
}

type level struct {
	price      int64
	qty        int64
	n          int
	head, tail *node
}

// bookSide holds levels sorted worst-first, so the best level is levels[len-1].
type bookSide struct {
	buy    bool
	levels []*level
}

func (s *bookSide) worse(a, b int64) bool {
	if s.buy {
		return a < b
	}
	return a > b
}

func (s *bookSide) best() *level {
	if len(s.levels) == 0 {
		return nil
	}
	return s.levels[len(s.levels)-1]
}

// search returns the index where price is (found) or would be inserted.
func (s *bookSide) search(price int64) (int, bool) {
	i := sort.Search(len(s.levels), func(i int) bool { return !s.worse(s.levels[i].price, price) })
	return i, i < len(s.levels) && s.levels[i].price == price
}

func (s *bookSide) levelFor(price int64) *level {
	i, found := s.search(price)
	if found {
		return s.levels[i]
	}
	l := &level{price: price}
	s.levels = append(s.levels, nil)
	copy(s.levels[i+1:], s.levels[i:])
	s.levels[i] = l
	return l
}

func (s *bookSide) drop(l *level) {
	if i, found := s.search(l.price); found && s.levels[i] == l {
		copy(s.levels[i:], s.levels[i+1:])
		s.levels[len(s.levels)-1] = nil
		s.levels = s.levels[:len(s.levels)-1]
	}
}

// Book is a limit order book for one symbol. It is NOT safe for concurrent
// use: it is the single-threaded core that Exchange serialises access to.
type Book struct {
	symbol  string
	bids    bookSide
	asks    bookSide
	ids     map[string]*node
	seq     uint64 // logical clock for time priority
	tradeID uint64
	stp     SelfTradePolicy
}

// NewBook returns an empty book for symbol.
func NewBook(symbol string) *Book {
	return &Book{symbol: symbol, bids: bookSide{buy: true}, ids: map[string]*node{}}
}

func (b *Book) side(s Side) *bookSide {
	if s == Buy {
		return &b.bids
	}
	return &b.asks
}

func validate(o Order) error {
	switch {
	case o.ID == "":
		return ErrInvalidOrder
	case o.Side != Buy && o.Side != Sell:
		return ErrInvalidOrder
	case o.Qty <= 0:
		return ErrInvalidOrder
	case o.TIF < GTC || o.TIF > FOK:
		return ErrInvalidOrder
	}
	switch o.Type {
	case Limit:
		if o.Price <= 0 {
			return ErrInvalidOrder
		}
		if o.PostOnly && o.TIF != GTC {
			return ErrInvalidOrder
		}
	case Market:
		if o.Price != 0 || o.PostOnly {
			return ErrInvalidOrder
		}
	default:
		return ErrInvalidOrder
	}
	return nil
}

// Submit validates o, matches it against the opposite side by price-time
// priority and, for limit orders with TIF GTC, rests any remainder. Trades
// execute at the resting order's price. Errors: ErrInvalidOrder,
// ErrDuplicateID (the id belongs to a resting order), and the Stage 2 errors.
// A rejected Submit changes nothing.
func (b *Book) Submit(o Order) ([]Trade, error) {
	if err := validate(o); err != nil {
		return nil, err
	}
	if _, dup := b.ids[o.ID]; dup {
		return nil, ErrDuplicateID
	}
	return b.execute(o)
}

// execute runs an already validated order: reject, match, rest.
func (b *Book) execute(o Order) ([]Trade, error) {
	opp := b.side(o.Side.opposite())
	if o.PostOnly && crossesBest(&o, opp) {
		return nil, ErrWouldCross
	}
	if o.TIF == FOK && !b.fillable(o) {
		return nil, ErrNotFillable
	}
	trades := b.match(&o)
	if o.Qty > 0 && o.Type == Limit && o.TIF == GTC {
		b.rest(o)
	}
	return trades, nil
}

// crossesAt reports whether an incoming order would trade at price.
func crossesAt(in *Order, price int64) bool {
	switch {
	case in.Type == Market:
		return true
	case in.Side == Buy:
		return price <= in.Price
	default:
		return price >= in.Price
	}
}

func crossesBest(in *Order, opp *bookSide) bool {
	l := opp.best()
	return l != nil && crossesAt(in, l.price)
}

func (b *Book) match(in *Order) []Trade {
	opp := b.side(in.Side.opposite())
	var trades []Trade
	for in.Qty > 0 {
		lvl := opp.best()
		if lvl == nil || !crossesAt(in, lvl.price) {
			break
		}
		mk := lvl.head
		if in.Owner != "" && mk.o.Owner == in.Owner && b.stp != STPAllow {
			if b.stp == STPCancelIncoming {
				in.Qty = 0 // drop the rest of the incoming order
				break
			}
			b.unlink(mk) // STPCancelResting: remove the resting order, keep matching
			continue
		}
		q := min(in.Qty, mk.o.Qty)
		b.tradeID++
		trades = append(trades, Trade{
			ID: b.tradeID, Symbol: b.symbol, Price: lvl.price, Qty: q,
			TakerID: in.ID, MakerID: mk.o.ID, TakerOwner: in.Owner, MakerOwner: mk.o.Owner,
			TakerSide: in.Side,
		})
		in.Qty -= q
		mk.o.Qty -= q
		lvl.qty -= q
		if mk.o.Qty == 0 {
			b.unlink(mk)
		}
	}
	return trades
}

// rest appends o at the back of its price level (time priority = arrival).
func (b *Book) rest(o Order) *node {
	lvl := b.side(o.Side).levelFor(o.Price)
	b.seq++
	n := &node{o: o, seq: b.seq, lvl: lvl}
	lvl.push(n)
	b.ids[o.ID] = n
	return n
}

func (l *level) push(n *node) {
	n.lvl, n.prev, n.next = l, l.tail, nil
	if l.tail != nil {
		l.tail.next = n
	} else {
		l.head = n
	}
	l.tail = n
	l.qty += n.o.Qty
	l.n++
}

// detach removes n from its level's list (adjusting the aggregates) but keeps
// the level and the id map entry.
func (l *level) detach(n *node) {
	if n.prev != nil {
		n.prev.next = n.next
	} else {
		l.head = n.next
	}
	if n.next != nil {
		n.next.prev = n.prev
	} else {
		l.tail = n.prev
	}
	n.prev, n.next = nil, nil
	l.qty -= n.o.Qty
	l.n--
}

// unlink removes a resting order entirely: list, id map and (if it was the
// last order) the empty level.
func (b *Book) unlink(n *node) {
	lvl := n.lvl
	lvl.detach(n)
	delete(b.ids, n.o.ID)
	if lvl.n == 0 {
		b.side(n.o.Side).drop(lvl)
	}
}

// Cancel removes a resting order. ErrUnknownOrder if there is no live order
// with that id (never existed, already filled, or already cancelled).
func (b *Book) Cancel(id string) error {
	n, ok := b.ids[id]
	if !ok {
		return ErrUnknownOrder
	}
	b.unlink(n)
	return nil
}

// Lookup returns a copy of the resting order with this id; Qty is the
// REMAINING quantity.
func (b *Book) Lookup(id string) (Order, bool) {
	n, ok := b.ids[id]
	if !ok {
		return Order{}, false
	}
	return n.o, true
}

// Depth returns up to n price levels per side, best first (bids from the
// highest price down, asks from the lowest price up). n <= 0 means all.
func (b *Book) Depth(n int) (bids, asks []Level) {
	return b.bids.snapshot(n), b.asks.snapshot(n)
}

func (s *bookSide) snapshot(n int) []Level {
	if n <= 0 || n > len(s.levels) {
		n = len(s.levels)
	}
	out := make([]Level, 0, n)
	for i := len(s.levels) - 1; i >= 0 && len(out) < n; i-- {
		l := s.levels[i]
		out = append(out, Level{Price: l.price, Qty: l.qty, Orders: l.n})
	}
	return out
}
