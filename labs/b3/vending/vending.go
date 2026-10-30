// Package vending is a vending machine modelled with the State pattern.
//
// Lab B3 Unit 6 §1 (problem 12), time box 60 minutes. Work in stages and open
// the next stage's file only when you get there, like an interviewer who adds
// requirements as you go:
//
//	Stage 1 (≈ 25 min)  this file:  coins, Select with exact change, Cancel, states
//	Stage 2 (≈ 20 min)  stage2.go:  maintenance mode, idle timeout, notices
//	Stage 3 (≈ 15 min)  stage3.go:  failing hardware, card payments, concurrency
//
// Run one stage at a time:
//
//	go test -race -run 'Stage1' ./labs/b3/vending/
//
// The point of the exercise is the design, not the arithmetic: model the
// life cycle as states with explicit, enumerable transitions, so that "what
// happens if X arrives in state Y?" has exactly one answer, written down in
// one place. Money is integer cents everywhere.
package vending

import (
	"errors"
	"fmt"
)

// State is where the machine is in its life cycle.
//
//	Idle          no session; waiting for a coin (or a card sale, Stage 3)
//	HasMoney      a session is open: coins are in escrow
//	Dispensing    the item is being delivered; every customer event is rejected
//	OutOfService  maintenance mode (Stage 2)
//	AwaitingCard  a card sale waits for the terminal's callback (Stage 3)
//
// Select moves HasMoney -> Dispensing -> Idle, or back to HasMoney if the
// delivery fails. Even while delivery is instantaneous (Stage 1) the machine
// passes through Dispensing; Stage 3 makes that visible.
type State int

// The machine's states.
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

// Errors. Compare with errors.Is; wrap them with context if you like.
var (
	ErrBadConfig         = errors.New("vending: invalid configuration")
	ErrInvalidState      = errors.New("vending: event not valid in this state")
	ErrUnknownSlot       = errors.New("vending: unknown slot")
	ErrSoldOut           = errors.New("vending: sold out")
	ErrInsufficientFunds = errors.New("vending: insufficient funds")
	ErrBadCoin           = errors.New("vending: coin not accepted")
	ErrNoChange          = errors.New("vending: cannot make exact change")
)

var errNotImplemented = errors.New("vending: not implemented")

// Product is one slot's contents. Prices are integer cents.
type Product struct {
	Name  string
	Price int
	Qty   int
}

// Config describes a machine at start-up. New must copy the maps.
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

// Snapshot is a consistent copy of the machine's observable state. Mutating
// it must never affect the machine.
type Snapshot struct {
	State   State
	Balance int         // cents inserted in the current session
	Escrow  map[int]int // coins inserted in the current session
	Box     map[int]int // the change box (excludes escrow)
	Slots   map[string]Product
}

// Option configures optional behaviour. Stages 2 and 3 add options.
type Option func(*Machine)

// Machine is a vending machine. All methods are safe for concurrent use
// (Stage 3 tests hammer it from many goroutines): decide your locking model
// before you write the second method.
type Machine struct {
	// TODO(lab): the current state object, slots, change box, escrow, balance,
	// a lock... State pattern: one object per state, each implementing every
	// event; states that don't accept an event return ErrInvalidState.
}

// New validates cfg and returns an Idle machine, applying opts in order.
// ErrBadConfig (use errors.Is) for: no denominations; a denomination <= 0 or
// repeated; a slot with an empty code, price <= 0 or Qty < 0; coin stock for a
// denomination that is not accepted, or a negative count.
func New(cfg Config, opts ...Option) (*Machine, error) {
	// TODO(lab)
	_, _ = cfg, opts
	return nil, errNotImplemented
}

// State returns the current state.
func (m *Machine) State() State {
	// TODO(lab)
	return Idle
}

// Balance returns the cents inserted in the current session.
func (m *Machine) Balance() int {
	// TODO(lab)
	return 0
}

// Snapshot returns a consistent copy of the machine's state. It is a query,
// not an event: it is allowed in every state.
func (m *Machine) Snapshot() Snapshot {
	// TODO(lab)
	return Snapshot{}
}

// InsertCoin accepts one coin into escrow. Valid in Idle (-> HasMoney) and
// HasMoney. An unaccepted denomination is ErrBadCoin and changes nothing.
// Every other state: ErrInvalidState.
func (m *Machine) InsertCoin(cents int) error {
	// TODO(lab)
	_ = cents
	return errNotImplemented
}

// Select buys the product in slot code with the money inserted. Valid in
// HasMoney only (any other state: ErrInvalidState, even for an unknown slot).
// Checks, in this order: ErrUnknownSlot, ErrSoldOut, ErrInsufficientFunds,
// ErrNoChange.
//
// Change must be EXACT, made from coins the machine really has: the change box
// PLUS the coins the customer inserted this session. Prefer fewest coins. If
// exact change is impossible, return ErrNoChange without dispensing: a
// greedy "largest coin first" algorithm gets this wrong when stock is finite
// (30 cents from one quarter and three dimes is three dimes).
//
// On any error the session is untouched: nothing dispensed, no money lost,
// state still HasMoney. On success the inserted coins join the change box,
// the change leaves it, the session ends and the state is Idle.
func (m *Machine) Select(code string) (Receipt, error) {
	// TODO(lab)
	_ = code
	return Receipt{}, errNotImplemented
}

// Cancel refunds exactly the coins inserted this session (the same coins, not
// an equivalent set) and ends the session. Valid in HasMoney only; in Idle it
// is ErrInvalidState, not a silent no-op.
func (m *Machine) Cancel() (map[int]int, error) {
	// TODO(lab)
	return nil, errNotImplemented
}
