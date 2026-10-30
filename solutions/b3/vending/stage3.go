package vending

import (
	"errors"
	"fmt"
	"time"
)

// Stage 3 (≈ 15 min): hardware that can fail, card payments, concurrency.

// Stage 3 errors.
var (
	ErrDispenseFailed = errors.New("vending: dispenser failed")
	ErrUnknownSale    = errors.New("vending: unknown sale")
	ErrSaleClosed     = errors.New("vending: sale is closed")
)

// Dispenser is the motor/hardware. Dispense may be slow and may fail. The
// machine never holds its lock while calling it.
type Dispenser interface {
	Dispense(slot string) error
}

// Gateway settles card payments that a terminal has already authorised.
// Every authorisation must end in exactly one Capture or one Void.
type Gateway interface {
	Capture(authID string) error
	Void(authID string) error
}

type noopDispenser struct{}

func (noopDispenser) Dispense(string) error { return nil }

type noopGateway struct{}

func (noopGateway) Capture(string) error { return nil }
func (noopGateway) Void(string) error    { return nil }

// WithDispenser injects the hardware.
func WithDispenser(d Dispenser) Option { return func(m *Machine) { m.disp = d } }

// WithGateway injects the card settlement gateway.
func WithGateway(g Gateway) Option { return func(m *Machine) { m.gw = g } }

// WithCardTimeout makes Tick expire a card sale still waiting for its terminal
// callback after d. Zero disables expiry.
func WithCardTimeout(d time.Duration) Option { return func(m *Machine) { m.cardTO = d } }

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
	SaleCaptureFailed                   // dispensed but Capture returned an error
)

// SaleInfo describes a card sale.
type SaleInfo struct {
	ID     SaleID
	Slot   string
	Price  int // the amount the terminal must authorise
	Status SaleStatus
	AuthID string
}

type sale struct {
	SaleInfo
	started     time.Time
	voidClaimed bool
}

// PayByCard starts a card sale for slot code. Idle only. The terminal
// authorises SaleInfo.Price and later calls CardApproved or CardDeclined.
func (m *Machine) PayByCard(code string) (SaleInfo, error) {
	m.mu.Lock()
	info, err := m.st.payByCard(m, code)
	m.leave()
	return info, err
}

func (m *Machine) startSale(code string) (SaleInfo, error) {
	p, ok := m.slots[code]
	switch {
	case !ok:
		return SaleInfo{}, ErrUnknownSlot
	case p.Qty <= 0:
		return SaleInfo{}, ErrSoldOut
	}
	m.nextSale++
	s := &sale{started: m.now()}
	s.ID = SaleID(fmt.Sprintf("sale-%d", m.nextSale))
	s.Slot, s.Price, s.Status = code, p.Price, SalePending
	m.sales[s.ID] = s // TODO(prod): bound this history (TTL or ring buffer)
	m.cur = s
	m.setState(AwaitingCard)
	return s.SaleInfo, nil
}

// Sale returns the current information for a sale.
func (m *Machine) Sale(id SaleID) (SaleInfo, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sales[id]
	if !ok {
		return SaleInfo{}, false
	}
	return s.SaleInfo, true
}

func (awaitingCardState) id() State { return AwaitingCard }

type awaitingCardState struct{ baseState }

// approve is phase 1 of a card sale: reserve the stock and enter Dispensing.
func (awaitingCardState) approve(m *Machine, s *sale) error {
	m.slots[s.Slot].Qty--
	s.Status = SaleDispensing
	m.setState(Dispensing)
	return nil
}

func (awaitingCardState) decline(m *Machine, s *sale) error {
	s.Status = SaleDeclined
	m.cur = nil
	m.setState(Idle)
	return nil
}

func (awaitingCardState) tick(m *Machine, now time.Time) map[int]int {
	if s := m.cur; s != nil && m.cardTO > 0 && now.Sub(s.started) >= m.cardTO {
		s.Status = SaleExpired
		m.cur = nil
		m.setState(Idle)
	}
	return nil
}

// CardApproved is the terminal's "authorised" callback. It is idempotent per
// sale: duplicates (and concurrent duplicates) of an approval that is being
// processed, or already completed, return nil without dispensing or capturing
// again. An approval that arrives after the sale was declined, expired or
// failed returns ErrSaleClosed, and for an expired sale the authorisation is
// voided exactly once.
func (m *Machine) CardApproved(id SaleID, authID string) error {
	m.mu.Lock()
	s, ok := m.sales[id]
	if !ok {
		m.leave()
		return ErrUnknownSale
	}
	switch s.Status {
	case SaleDispensing, SaleCompleted, SaleCaptureFailed:
		m.leave()
		return nil
	case SaleDeclined, SaleFailed:
		m.leave()
		return ErrSaleClosed
	case SaleExpired:
		claim := !s.voidClaimed
		s.voidClaimed = true
		m.leave()
		if claim {
			_ = call(func() error { return m.gw.Void(authID) })
		}
		return ErrSaleClosed
	}
	s.AuthID = authID
	if err := m.st.approve(m, s); err != nil {
		m.leave()
		return err
	}
	m.leave()

	derr := call(func() error { return m.disp.Dispense(s.Slot) }) // lock NOT held
	if derr != nil {
		m.mu.Lock()
		m.slots[s.Slot].Qty++
		s.Status = SaleFailed
		s.voidClaimed = true
		m.cur = nil
		m.emit(Notice{Kind: NoticeDispenseFailed, Slot: s.Slot})
		m.setState(Idle)
		m.leave()
		_ = call(func() error { return m.gw.Void(authID) })
		return fmt.Errorf("%w: %v", ErrDispenseFailed, derr)
	}

	m.mu.Lock()
	m.emit(Notice{Kind: NoticeDispensed, Slot: s.Slot, Amount: s.Price})
	if p := m.slots[s.Slot]; m.lowStock > 0 && p.Qty <= m.lowStock {
		m.emit(Notice{Kind: NoticeLowStock, Slot: s.Slot, Qty: p.Qty})
	}
	m.leave()
	cerr := call(func() error { return m.gw.Capture(authID) }) // lock NOT held

	m.mu.Lock()
	s.Status = SaleCompleted
	if cerr != nil {
		s.Status = SaleCaptureFailed
	}
	m.cur = nil
	m.setState(Idle)
	m.leave()
	return nil
}

// CardDeclined is the terminal's "declined" callback: AwaitingCard -> Idle,
// nothing dispensed, nothing captured. Idempotent.
func (m *Machine) CardDeclined(id SaleID) error {
	m.mu.Lock()
	defer m.leave()
	s, ok := m.sales[id]
	switch {
	case !ok:
		return ErrUnknownSale
	case s.Status == SaleDeclined:
		return nil
	case s.Status != SalePending:
		return ErrSaleClosed
	}
	return m.st.decline(m, s)
}
