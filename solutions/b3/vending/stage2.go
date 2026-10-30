package vending

import (
	"errors"
	"time"
)

// Stage 2 (≈ 20 min): maintenance mode, idle timeout, and notices.

// ErrInvalidArgument is returned for a bad quantity, price or denomination
// passed to an admin operation.
var ErrInvalidArgument = errors.New("vending: invalid argument")

// WithClock injects the time source used to timestamp coin insertions.
func WithClock(now func() time.Time) Option { return func(m *Machine) { m.now = now } }

// WithIdleTimeout makes Tick refund a session that has seen no coin for d.
// Zero (the default) disables the timeout.
func WithIdleTimeout(d time.Duration) Option { return func(m *Machine) { m.idleTO = d } }

// WithLowStockAlert makes a sale that leaves a slot with threshold or fewer
// items emit a NoticeLowStock. Zero disables the alert.
func WithLowStockAlert(threshold int) Option { return func(m *Machine) { m.lowStock = threshold } }

// EnterService puts an Idle machine out of service for maintenance.
func (m *Machine) EnterService() error {
	m.mu.Lock()
	err := m.st.enterService(m)
	m.leave()
	return err
}

// ExitService brings an OutOfService machine back to Idle.
func (m *Machine) ExitService() error {
	m.mu.Lock()
	err := m.st.exitService(m)
	m.leave()
	return err
}

// admin runs fn under the lock if the current state allows maintenance.
func (m *Machine) admin(fn func() error) error {
	m.mu.Lock()
	defer m.leave()
	if err := m.st.admin(m); err != nil {
		return err
	}
	return fn()
}

// Restock adds qty items to a slot. OutOfService only.
func (m *Machine) Restock(code string, qty int) error {
	return m.admin(func() error {
		p, ok := m.slots[code]
		if !ok {
			return ErrUnknownSlot
		}
		if qty <= 0 {
			return ErrInvalidArgument
		}
		p.Qty += qty
		return nil
	})
}

// SetPrice changes a slot's price. OutOfService only.
func (m *Machine) SetPrice(code string, price int) error {
	return m.admin(func() error {
		p, ok := m.slots[code]
		if !ok {
			return ErrUnknownSlot
		}
		if price <= 0 {
			return ErrInvalidArgument
		}
		p.Price = price
		return nil
	})
}

// LoadCoins adds n coins of one denomination to the change box. OutOfService
// only.
func (m *Machine) LoadCoins(denom, n int) error {
	return m.admin(func() error {
		if !m.valid[denom] {
			return ErrBadCoin
		}
		if n <= 0 {
			return ErrInvalidArgument
		}
		m.box[denom] += n
		return nil
	})
}

// CollectCash empties the change box down to keep coins of each denomination
// and returns what was removed. OutOfService only.
func (m *Machine) CollectCash(keep int) (map[int]int, error) {
	var out map[int]int
	err := m.admin(func() error {
		if keep < 0 {
			return ErrInvalidArgument
		}
		out = map[int]int{}
		for d, n := range m.box {
			if n > keep {
				out[d] = n - keep
				m.box[d] = keep
			}
		}
		return nil
	})
	return out, err
}

// Tick tells the machine what time it is. It is the one event valid in every
// state (anything else would make a ticking clock an error source). In HasMoney
// it refunds a session idle for longer than the idle timeout, returning the
// refunded coins; in AwaitingCard it expires an overdue card sale. Otherwise it
// returns (nil, nil).
func (m *Machine) Tick(now time.Time) (map[int]int, error) {
	m.mu.Lock()
	refund := m.st.tick(m, now)
	m.leave()
	return refund, nil
}

// NoticeKind says what a Notice reports.
type NoticeKind int

// Notice kinds.
const (
	NoticeStateChanged   NoticeKind = iota + 1 // From and To are set
	NoticeDispensed                            // Slot, Amount (price)
	NoticeRefunded                             // Amount, Reason ("cancel" or "timeout")
	NoticeLowStock                             // Slot, Qty remaining
	NoticeDispenseFailed                       // Slot
)

// Notice is something that happened to the machine. Notices are delivered in
// the order the effects occurred.
type Notice struct {
	Kind     NoticeKind
	From, To State
	Slot     string
	Amount   int
	Qty      int
	Reason   string
}

// Subscribe registers fn to receive every Notice and returns a function that
// unsubscribes. Delivery contract: notices reach each listener in order, one
// at a time, with the machine's lock NOT held, so a listener may call back into
// the machine. A panicking listener is contained. A slow listener delays only
// the goroutine that happens to be draining the queue; other goroutines' calls
// enqueue and return.
func (m *Machine) Subscribe(fn func(Notice)) (cancel func()) {
	m.mu.Lock()
	id := m.nextSub
	m.nextSub++
	m.subs[id] = fn
	m.mu.Unlock()
	return func() {
		m.mu.Lock()
		delete(m.subs, id)
		m.mu.Unlock()
	}
}

// emit queues a notice. Caller holds m.mu and must finish with leave.
func (m *Machine) emit(n Notice) { m.pending = append(m.pending, n) }

// leave releases m.mu and delivers queued notices. At most one goroutine
// drains at a time (m.draining), which keeps delivery ordered; a goroutine
// that finds a drain in progress just returns, because the drainer re-checks
// the queue before it stops.
func (m *Machine) leave() {
	if len(m.pending) == 0 || m.draining {
		m.mu.Unlock()
		return
	}
	m.draining = true
	for {
		batch := m.pending
		m.pending = nil
		subs := make([]func(Notice), 0, len(m.subs))
		for _, fn := range m.subs {
			subs = append(subs, fn)
		}
		m.mu.Unlock()
		for _, n := range batch {
			for _, fn := range subs {
				func() {
					defer func() { _ = recover() }()
					fn(n)
				}()
			}
		}
		m.mu.Lock()
		if len(m.pending) == 0 {
			m.draining = false
			m.mu.Unlock()
			return
		}
	}
}
