package orderbook

import (
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
)

// Stage 3 (≈ 25 min): many symbols, trade streams, executable invariants.

// Stage 3 errors.
var (
	ErrClosed        = errors.New("orderbook: exchange closed")
	ErrUnknownSymbol = errors.New("orderbook: unknown symbol")
)

// CheckInvariants verifies the book's internal structure and returns the first
// violation, or nil. It is meant to be called after every operation in tests.
// It checks that: levels are strictly ordered with no duplicate prices; no level
// is empty; each level's Qty and Orders equal the sum over its orders; every
// order has Qty > 0, the right side and price for its level, and a strictly
// increasing sequence number from head to tail (time priority); the id map
// holds exactly the resting orders; and the book is not crossed.
func (b *Book) CheckInvariants() error {
	count := 0
	for _, s := range []*bookSide{&b.bids, &b.asks} {
		for i, l := range s.levels {
			if i > 0 && !s.worse(s.levels[i-1].price, l.price) {
				return fmt.Errorf("levels not strictly ordered worst-to-best at index %d (%d then %d)", i, s.levels[i-1].price, l.price)
			}
			if l.n == 0 || l.head == nil {
				return fmt.Errorf("empty level at price %d left in the book", l.price)
			}
			var qty int64
			n := 0
			var lastSeq uint64
			var prev *node
			for x := l.head; x != nil; x = x.next {
				if x.lvl != l || x.prev != prev {
					return fmt.Errorf("order %s at price %d has broken links", x.o.ID, l.price)
				}
				if x.o.Qty <= 0 {
					return fmt.Errorf("order %s resting with qty %d", x.o.ID, x.o.Qty)
				}
				if x.o.Price != l.price || (x.o.Side == Buy) != s.buy {
					return fmt.Errorf("order %s is on the wrong side or level", x.o.ID)
				}
				if x.seq <= lastSeq {
					return fmt.Errorf("level %d violates time priority: seq %d after %d", l.price, x.seq, lastSeq)
				}
				if b.ids[x.o.ID] != x {
					return fmt.Errorf("order %s missing from the id map", x.o.ID)
				}
				lastSeq, prev = x.seq, x
				qty += x.o.Qty
				n++
			}
			if prev != l.tail {
				return fmt.Errorf("level %d tail pointer is wrong", l.price)
			}
			if qty != l.qty || n != l.n {
				return fmt.Errorf("level %d aggregate says qty %d / %d orders but orders sum to %d / %d", l.price, l.qty, l.n, qty, n)
			}
			count += n
		}
	}
	if count != len(b.ids) {
		return fmt.Errorf("id map has %d entries but %d orders rest", len(b.ids), count)
	}
	if bb, ba := b.bids.best(), b.asks.best(); bb != nil && ba != nil && bb.price >= ba.price {
		return fmt.Errorf("book is crossed: best bid %d >= best ask %d", bb.price, ba.price)
	}
	return nil
}

// Exchange runs one Book per symbol and makes them safe for concurrent use.
//
// Concurrency model: each symbol's Book is guarded by its own mutex, so
// operations on one symbol are serialised (a single writer at a time, which is
// what keeps matching deterministic) while different symbols run in parallel.
// The symbol table has its own RWMutex. Trade fan-out to subscribers happens
// inside the book's critical section, so every subscriber sees trades in
// execution order, but it never blocks: see Subscribe.
type Exchange struct {
	mu     sync.RWMutex
	books  map[string]*market
	closed bool
}

type market struct {
	mu   sync.Mutex
	book *Book
	subs []*Subscription
}

// NewExchange returns an empty exchange. Books are created on first use.
func NewExchange() *Exchange { return &Exchange{books: map[string]*market{}} }

func (x *Exchange) market(symbol string, create bool) (*market, error) {
	x.mu.RLock()
	m, ok := x.books[symbol]
	closed := x.closed
	x.mu.RUnlock()
	if closed {
		return nil, ErrClosed
	}
	if ok {
		return m, nil
	}
	if !create {
		return nil, ErrUnknownSymbol
	}
	x.mu.Lock()
	defer x.mu.Unlock()
	if x.closed {
		return nil, ErrClosed
	}
	if m, ok = x.books[symbol]; !ok {
		m = &market{book: NewBook(symbol)}
		x.books[symbol] = m
	}
	return m, nil
}

// Submit routes o to the symbol's book (created on first use).
func (x *Exchange) Submit(symbol string, o Order) ([]Trade, error) {
	m, err := x.market(symbol, true)
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	trades, err := m.book.Submit(o)
	m.publish(trades)
	return trades, err
}

// Cancel cancels a resting order. ErrUnknownSymbol, ErrUnknownOrder.
func (x *Exchange) Cancel(symbol, id string) error {
	m, err := x.market(symbol, false)
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.book.Cancel(id)
}

// Amend is Book.Amend routed by symbol; trades it causes are published.
func (x *Exchange) Amend(symbol, id string, newQty, newPrice int64) ([]Trade, error) {
	m, err := x.market(symbol, false)
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	trades, err := m.book.Amend(id, newQty, newPrice)
	m.publish(trades)
	return trades, err
}

// Depth is Book.Depth routed by symbol. ErrUnknownSymbol if the symbol has
// never been used.
func (x *Exchange) Depth(symbol string, n int) (bids, asks []Level, err error) {
	m, err := x.market(symbol, false)
	if err != nil {
		return nil, nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	bids, asks = m.book.Depth(n)
	return bids, asks, nil
}

// CheckInvariants runs Book.CheckInvariants for symbol under its lock.
func (x *Exchange) CheckInvariants(symbol string) error {
	m, err := x.market(symbol, false)
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.book.CheckInvariants()
}

// Subscription is a live feed of one symbol's trades.
//
// Slow-subscriber policy: C has a fixed buffer. If it is full when a trade is
// published, THAT trade is dropped for this subscriber (and counted in Dropped);
// matching never waits. Trade IDs are consecutive per symbol, so a consumer
// detects a gap by looking at the IDs.
type Subscription struct {
	C       <-chan Trade
	ch      chan Trade
	m       *market
	dropped atomic.Uint64
	closed  bool // guarded by m.mu
}

// Subscribe starts a feed of the symbol's trades with the given buffer
// (minimum 1). The symbol is created if it does not exist yet. Only trades
// executed after Subscribe returns are delivered.
func (x *Exchange) Subscribe(symbol string, buf int) (*Subscription, error) {
	m, err := x.market(symbol, true)
	if err != nil {
		return nil, err
	}
	if buf < 1 {
		buf = 1
	}
	ch := make(chan Trade, buf)
	s := &Subscription{C: ch, ch: ch, m: m}
	m.mu.Lock()
	defer m.mu.Unlock()
	// Re-check under the book's lock: Close may have swept this book already.
	x.mu.RLock()
	closed := x.closed
	x.mu.RUnlock()
	if closed {
		return nil, ErrClosed
	}
	m.subs = append(m.subs, s)
	return s, nil
}

// Dropped is how many trades were not delivered because the buffer was full.
func (s *Subscription) Dropped() uint64 { return s.dropped.Load() }

// Close ends the feed and closes C (buffered trades can still be read). It is
// safe to call more than once.
func (s *Subscription) Close() {
	s.m.mu.Lock()
	defer s.m.mu.Unlock()
	s.m.closeSub(s)
}

// closeSub: caller holds m.mu.
func (m *market) closeSub(s *Subscription) {
	if s.closed {
		return
	}
	s.closed = true
	close(s.ch)
	for i, o := range m.subs {
		if o == s {
			m.subs = append(m.subs[:i], m.subs[i+1:]...)
			break
		}
	}
}

// publish: caller holds m.mu. Channel sends and closes both happen under m.mu,
// so a send can never race with a close.
func (m *market) publish(trades []Trade) {
	for _, t := range trades {
		for _, s := range m.subs {
			select {
			case s.ch <- t:
			default:
				s.dropped.Add(1)
			}
		}
	}
}

// Close shuts the exchange down: every subscription is closed and later calls
// return ErrClosed. It is idempotent.
func (x *Exchange) Close() {
	x.mu.Lock()
	x.closed = true
	ms := make([]*market, 0, len(x.books))
	for _, m := range x.books {
		ms = append(ms, m)
	}
	x.mu.Unlock()
	for _, m := range ms {
		m.mu.Lock()
		for len(m.subs) > 0 {
			m.closeSub(m.subs[0])
		}
		m.mu.Unlock()
	}
}
