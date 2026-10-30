// Package vending is a vending machine modelled with the State pattern.
//
// Lab B3 Unit 6 §1 (problem 12). Stage 1 is this file (plus the helpers in
// states.go and change.go); Stage 2 lives in stage2.go and Stage 3 in
// stage3.go. Open those only when the interviewer (the unit text) asks.
//
// Reference design: a Machine owns all mutable state behind one mutex. The
// current state is an object that implements every event (InsertCoin,
// Select, Cancel, ...); states that do not accept an event inherit a default
// that returns ErrInvalidState. Slow collaborators (the dispenser motor and
// the card gateway) are called with the mutex RELEASED while the machine sits
// in an explicit Dispensing state, so other events fail fast instead of
// queueing behind hardware.
package vending

import (
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"
)

// State is where the machine is in its life cycle.
type State int

// The machine's states. Select moves HasMoney -> Dispensing -> Idle (or back
// to HasMoney if the motor fails). AwaitingCard exists from Stage 3.
const (
	Idle State = iota
	HasMoney
	Dispensing
	OutOfService
	AwaitingCard
)

func (s State) String() string {
	switch s {
	case Idle:
		return "Idle"
	case HasMoney:
		return "HasMoney"
	case Dispensing:
		return "Dispensing"
	case OutOfService:
		return "OutOfService"
	case AwaitingCard:
		return "AwaitingCard"
	}
	return fmt.Sprintf("State(%d)", int(s))
}

// Errors. Compare with errors.Is.
var (
	ErrBadConfig         = errors.New("vending: invalid configuration")
	ErrInvalidState      = errors.New("vending: event not valid in this state")
	ErrUnknownSlot       = errors.New("vending: unknown slot")
	ErrSoldOut           = errors.New("vending: sold out")
	ErrInsufficientFunds = errors.New("vending: insufficient funds")
	ErrBadCoin           = errors.New("vending: coin not accepted")
	ErrNoChange          = errors.New("vending: cannot make exact change")
)

// Product is one slot's contents. Prices are integer cents.
type Product struct {
	Name  string
	Price int
	Qty   int
}

// Config describes a machine at start-up. The maps are copied.
type Config struct {
	Denominations []int              // accepted coins in cents, e.g. 5, 10, 25, 100
	Slots         map[string]Product // slot code -> product
	Coins         map[int]int        // initial change box: denomination -> count
}

// Receipt is the result of a successful Select.
type Receipt struct {
	Slot    string
	Product string
	Price   int
	Change  map[int]int // denomination -> count; empty for exact payment
}

// Snapshot is a consistent copy of the machine's observable state.
type Snapshot struct {
	State   State
	Balance int         // cents inserted in the current session
	Escrow  map[int]int // coins inserted in the current session
	Box     map[int]int // the change box (excludes escrow)
	Slots   map[string]Product
}

// Option configures optional behaviour (Stages 2 and 3).
type Option func(*Machine)

// Machine is a vending machine. All methods are safe for concurrent use.
type Machine struct {
	mu     sync.Mutex
	st     state
	denoms []int // ascending
	valid  map[int]bool
	slots  map[string]*Product
	box    map[int]int
	escrow map[int]int
	bal    int
	tx     *cashTx

	// Stage 2
	now        func() time.Time
	idleTO     time.Duration
	lowStock   int
	lastInsert time.Time
	subs       map[int]func(Notice)
	nextSub    int
	pending    []Notice
	draining   bool

	// Stage 3
	disp     Dispenser
	gw       Gateway
	cardTO   time.Duration
	sales    map[SaleID]*sale
	nextSale int
	cur      *sale
}

func copyCounts(in map[int]int) map[int]int {
	out := make(map[int]int, len(in))
	for k, v := range in {
		if v != 0 {
			out[k] = v
		}
	}
	return out
}

// New validates cfg and returns an Idle machine.
func New(cfg Config, opts ...Option) (*Machine, error) {
	if len(cfg.Denominations) == 0 {
		return nil, fmt.Errorf("%w: no denominations", ErrBadConfig)
	}
	m := &Machine{
		st:     states[Idle],
		valid:  map[int]bool{},
		slots:  map[string]*Product{},
		box:    map[int]int{},
		escrow: map[int]int{},
		now:    time.Now,
		subs:   map[int]func(Notice){},
		disp:   noopDispenser{},
		gw:     noopGateway{},
		sales:  map[SaleID]*sale{},
	}
	for _, d := range cfg.Denominations {
		if d <= 0 || m.valid[d] {
			return nil, fmt.Errorf("%w: denomination %d must be positive and unique", ErrBadConfig, d)
		}
		m.valid[d] = true
		m.denoms = append(m.denoms, d)
	}
	sort.Ints(m.denoms)
	for code, p := range cfg.Slots {
		if code == "" || p.Price <= 0 || p.Qty < 0 {
			return nil, fmt.Errorf("%w: slot %q needs a code, a positive price and qty >= 0", ErrBadConfig, code)
		}
		cp := p
		m.slots[code] = &cp
	}
	for d, n := range cfg.Coins {
		if !m.valid[d] || n < 0 {
			return nil, fmt.Errorf("%w: coin stock %d x %d does not match the denominations", ErrBadConfig, n, d)
		}
		if n > 0 {
			m.box[d] = n
		}
	}
	for _, o := range opts {
		o(m)
	}
	m.lastInsert = m.now()
	return m, nil
}

// State returns the current state.
func (m *Machine) State() State {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.st.id()
}

// Balance returns the cents inserted in the current session.
func (m *Machine) Balance() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.bal
}

// Snapshot returns a consistent copy of the machine's state. It is a query,
// not an event: it is allowed in every state.
func (m *Machine) Snapshot() Snapshot {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := Snapshot{
		State:   m.st.id(),
		Balance: m.bal,
		Escrow:  copyCounts(m.escrow),
		Box:     copyCounts(m.box),
		Slots:   make(map[string]Product, len(m.slots)),
	}
	for c, p := range m.slots {
		s.Slots[c] = *p
	}
	return s
}

// InsertCoin accepts one coin. Valid in Idle (-> HasMoney) and HasMoney.
// An unaccepted denomination is ErrBadCoin and changes nothing.
func (m *Machine) InsertCoin(cents int) error {
	m.mu.Lock()
	err := m.st.insertCoin(m, cents)
	m.leave()
	return err
}

// Select buys the product in slot code with the money inserted, returning the
// product's receipt and any change. Valid in HasMoney only. Every failure
// (ErrUnknownSlot, ErrSoldOut, ErrInsufficientFunds, ErrNoChange,
// ErrDispenseFailed) leaves the session untouched: nothing is dispensed and no
// money is lost; the customer can pick again or Cancel.
func (m *Machine) Select(code string) (Receipt, error) {
	m.mu.Lock()
	tx, err := m.st.selectSlot(m, code)
	m.leave()
	if err != nil {
		return Receipt{}, err
	}
	derr := call(func() error { return m.disp.Dispense(code) }) // mutex NOT held
	return m.finishCash(tx, derr)
}

// Cancel refunds exactly the coins inserted this session. Valid in HasMoney.
func (m *Machine) Cancel() (map[int]int, error) {
	m.mu.Lock()
	refund, err := m.st.cancel(m)
	m.leave()
	return refund, err
}

// call runs fn and converts a panic into an error, so a misbehaving
// collaborator can never leave the machine stuck in Dispensing.
func call(fn func() error) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("panic: %v", r)
		}
	}()
	return fn()
}

// cashTx is a purchase in flight between the reserve and commit phases.
type cashTx struct {
	code   string
	name   string
	price  int
	change map[int]int
}

// finishCash is phase 2 of Select: commit or roll back after the motor ran.
func (m *Machine) finishCash(tx *cashTx, derr error) (Receipt, error) {
	m.mu.Lock()
	defer m.leave()
	p := m.slots[tx.code]
	m.tx = nil
	if derr != nil {
		p.Qty++ // release the reservation
		m.emit(Notice{Kind: NoticeDispenseFailed, Slot: tx.code})
		m.setState(HasMoney) // money untouched, still in escrow
		return Receipt{}, fmt.Errorf("%w: %v", ErrDispenseFailed, derr)
	}
	for d, n := range m.escrow {
		m.box[d] += n
	}
	for d, n := range tx.change {
		m.box[d] -= n
	}
	m.escrow, m.bal = map[int]int{}, 0
	m.emit(Notice{Kind: NoticeDispensed, Slot: tx.code, Amount: tx.price})
	if m.lowStock > 0 && p.Qty <= m.lowStock {
		m.emit(Notice{Kind: NoticeLowStock, Slot: tx.code, Qty: p.Qty})
	}
	m.setState(Idle)
	return Receipt{Slot: tx.code, Product: tx.name, Price: tx.price, Change: tx.change}, nil
}
