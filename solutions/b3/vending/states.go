package vending

import (
	"slices"
	"time"
)

// state is the State pattern: one method per event. Methods run with
// Machine.mu held and must not block or call collaborators. Anything not
// overridden inherits baseState and is ErrInvalidState.
type state interface {
	id() State
	insertCoin(m *Machine, cents int) error
	selectSlot(m *Machine, code string) (*cashTx, error)
	cancel(m *Machine) (map[int]int, error)
	enterService(m *Machine) error
	exitService(m *Machine) error
	admin(m *Machine) error
	payByCard(m *Machine, code string) (SaleInfo, error)
	approve(m *Machine, s *sale) error
	decline(m *Machine, s *sale) error
	tick(m *Machine, now time.Time) map[int]int
}

// edges is the transition table as data: the only legal state changes.
// setState enforces it, so a handler bug cannot create an impossible state.
var edges = map[State][]State{
	Idle:         {HasMoney, AwaitingCard, OutOfService},
	HasMoney:     {Idle, Dispensing},
	Dispensing:   {Idle, HasMoney},
	AwaitingCard: {Idle, Dispensing},
	OutOfService: {Idle},
}

var states = map[State]state{
	Idle:         idleState{},
	HasMoney:     hasMoneyState{},
	Dispensing:   dispensingState{},
	AwaitingCard: awaitingCardState{},
	OutOfService: outOfServiceState{},
}

// setState moves the machine along a legal edge and announces it.
func (m *Machine) setState(to State) {
	from := m.st.id()
	if from == to {
		return
	}
	if !slices.Contains(edges[from], to) {
		panic("vending: illegal transition " + from.String() + " -> " + to.String())
	}
	m.st = states[to]
	m.emit(Notice{Kind: NoticeStateChanged, From: from, To: to})
}

// baseState rejects every event. Concrete states embed it and override only
// what they accept; adding a new event means adding one method here, not
// touching the existing states.
type baseState struct{}

func (baseState) insertCoin(*Machine, int) error { return ErrInvalidState }
func (baseState) selectSlot(*Machine, string) (*cashTx, error) {
	return nil, ErrInvalidState
}
func (baseState) cancel(*Machine) (map[int]int, error) { return nil, ErrInvalidState }
func (baseState) enterService(*Machine) error          { return ErrInvalidState }
func (baseState) exitService(*Machine) error           { return ErrInvalidState }
func (baseState) admin(*Machine) error                 { return ErrInvalidState }
func (baseState) payByCard(*Machine, string) (SaleInfo, error) {
	return SaleInfo{}, ErrInvalidState
}
func (baseState) approve(*Machine, *sale) error        { return ErrInvalidState }
func (baseState) decline(*Machine, *sale) error        { return ErrInvalidState }
func (baseState) tick(*Machine, time.Time) map[int]int { return nil }

// takeCoin books one accepted coin into escrow.
func (m *Machine) takeCoin(cents int) error {
	if !m.valid[cents] {
		return ErrBadCoin
	}
	m.escrow[cents]++
	m.bal += cents
	m.lastInsert = m.now()
	return nil
}

// refundEscrow hands back exactly the inserted coins and ends the session.
func (m *Machine) refundEscrow(reason string) map[int]int {
	refund := m.escrow
	amount := m.bal
	m.escrow, m.bal = map[int]int{}, 0
	m.emit(Notice{Kind: NoticeRefunded, Amount: amount, Reason: reason})
	return refund
}

// ---- Idle

type idleState struct{ baseState }

func (idleState) id() State { return Idle }
func (idleState) insertCoin(m *Machine, cents int) error {
	if err := m.takeCoin(cents); err != nil {
		return err
	}
	m.setState(HasMoney)
	return nil
}
func (idleState) enterService(m *Machine) error {
	m.setState(OutOfService)
	return nil
}
func (idleState) payByCard(m *Machine, code string) (SaleInfo, error) { return m.startSale(code) }

// ---- HasMoney

type hasMoneyState struct{ baseState }

func (hasMoneyState) id() State { return HasMoney }
func (hasMoneyState) insertCoin(m *Machine, cents int) error {
	return m.takeCoin(cents)
}

// selectSlot is phase 1 of a purchase: validate everything, find change,
// reserve the stock and enter Dispensing. Nothing irreversible happens yet.
func (hasMoneyState) selectSlot(m *Machine, code string) (*cashTx, error) {
	p, ok := m.slots[code]
	switch {
	case !ok:
		return nil, ErrUnknownSlot
	case p.Qty <= 0:
		return nil, ErrSoldOut
	case m.bal < p.Price:
		return nil, ErrInsufficientFunds
	}
	pool := copyCounts(m.box)
	for d, n := range m.escrow {
		pool[d] += n // inserted coins can be handed back as change
	}
	change, ok := makeChange(m.bal-p.Price, m.denoms, pool)
	if !ok {
		return nil, ErrNoChange
	}
	p.Qty--
	m.tx = &cashTx{code: code, name: p.Name, price: p.Price, change: change}
	m.setState(Dispensing)
	return m.tx, nil
}

func (hasMoneyState) cancel(m *Machine) (map[int]int, error) {
	refund := m.refundEscrow("cancel")
	m.setState(Idle)
	return refund, nil
}

func (hasMoneyState) tick(m *Machine, now time.Time) map[int]int {
	if m.idleTO <= 0 || now.Sub(m.lastInsert) < m.idleTO {
		return nil
	}
	refund := m.refundEscrow("timeout")
	m.setState(Idle)
	return refund
}

// ---- Dispensing: the motor is running; every customer event fails fast.

type dispensingState struct{ baseState }

func (dispensingState) id() State { return Dispensing }

// ---- OutOfService

type outOfServiceState struct{ baseState }

func (outOfServiceState) id() State { return OutOfService }
func (outOfServiceState) exitService(m *Machine) error {
	m.setState(Idle)
	return nil
}
func (outOfServiceState) admin(*Machine) error { return nil }
