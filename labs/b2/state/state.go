// Package state models a payment transfer's lifecycle as a table-driven state
// machine.
//
// Lab B2 Unit 1 §3. A transfer goes initiated -> authorised -> settled and can
// later be reversed; it can fail before settling. The anti-pattern is a
// `status` string compared in a dozen files, each with its own idea of which
// moves are legal. The pattern is: one table is the single source of truth
// for legal (state, event) -> state moves, with guards for rules that depend
// on more than the state (the reversal window), and one method applies events.
//
//	initiated  --authorise--> authorised  --settle--> settled --reverse--> reversed
//	    |                         |
//	    +--------- fail ----------+--> failed
package state

import (
	"errors"
	"time"
)

var errNotImplemented = errors.New("state: not implemented")

// State is a lifecycle state.
type State string

// Event is something that happens to a transfer.
type Event string

// Lifecycle states.
const (
	Initiated  State = "initiated"
	Authorised State = "authorised"
	Settled    State = "settled"
	Failed     State = "failed"
	Reversed   State = "reversed"
)

// Events.
const (
	Authorise Event = "authorise"
	Settle    Event = "settle"
	Fail      Event = "fail"
	Reverse   Event = "reverse"
)

// ReversalWindow is how long after settlement a transfer may still be reversed
// (inclusive).
const ReversalWindow = 24 * time.Hour

var (
	// ErrIllegalTransition: the event is not allowed in the current state.
	ErrIllegalTransition = errors.New("state: illegal transition")
	// ErrDuplicate: the event is the one that produced the current state, so a
	// retried or duplicated callback. Callers usually treat it as success.
	ErrDuplicate = errors.New("state: duplicate event")
	// ErrReversalWindow: the transfer settled more than ReversalWindow ago.
	ErrReversalWindow = errors.New("state: reversal window closed")
	// ErrInvalid: bad constructor arguments.
	ErrInvalid = errors.New("state: invalid transfer")
)

// Transition is one recorded move.
type Transition struct {
	From, To State
	Event    Event
	At       time.Time
}

// TODO(lab): the transition table. A map[State]map[Event]rule where a rule
// holds the target state and an optional guard (for the reversal window) is
// one good shape. Failed and Reversed have no entries: they are terminal.

// IsTerminal reports whether no event can move a transfer out of s.
func IsTerminal(s State) bool {
	// TODO(lab): derive it from the table, do not hard-code the two states.
	_ = s
	return false
}

// Transfer is one money movement. Safe for concurrent use.
type Transfer struct {
	// TODO(lab): mutex, id, amount, state, history, clock, settledAt.
}

// New creates a transfer in the Initiated state. id must be non-empty and
// amount (minor units) positive (otherwise ErrInvalid); now defaults to
// time.Now.
func New(id string, amount int64, now func() time.Time) (*Transfer, error) {
	// TODO(lab)
	_, _, _ = id, amount, now
	return nil, errNotImplemented
}

// State returns the current state.
func (t *Transfer) State() State {
	// TODO(lab)
	return ""
}

// Apply moves the transfer according to the table.
//
// An event not in the table for the current state is ErrIllegalTransition
// (wrapped with the state and event), except when it equals the event that
// produced the current state, which is ErrDuplicate. A failing guard (the
// reversal window) returns its error and leaves the transfer unchanged. Apply
// is atomic: of several concurrent events, each is evaluated against the state
// the previous one left. Rejected events leave no trace in History.
func (t *Transfer) Apply(ev Event) error {
	// TODO(lab)
	_ = ev
	return errNotImplemented
}

// Allowed lists, sorted, the events the table accepts in the current state
// (guards are not evaluated). Useful for UIs and for exhaustive tests.
func (t *Transfer) Allowed() []Event {
	// TODO(lab)
	return nil
}

// History returns a copy of the recorded transitions, oldest first.
func (t *Transfer) History() []Transition {
	// TODO(lab)
	return nil
}
