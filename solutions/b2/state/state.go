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
	"fmt"
	"sort"
	"sync"
	"time"
)

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

type rule struct {
	to    State
	guard func(*Transfer) error // nil = always allowed; called with t.mu held
}

// table is the single source of truth for the lifecycle.
var table = map[State]map[Event]rule{
	Initiated:  {Authorise: {to: Authorised}, Fail: {to: Failed}},
	Authorised: {Settle: {to: Settled}, Fail: {to: Failed}},
	Settled:    {Reverse: {to: Reversed, guard: reversalWindowOpen}},
	// Failed and Reversed are terminal: no entries.
}

func reversalWindowOpen(t *Transfer) error {
	if t.now().Sub(t.settledAt) > ReversalWindow {
		return fmt.Errorf("%w: settled %s ago", ErrReversalWindow, t.now().Sub(t.settledAt))
	}
	return nil
}

// IsTerminal reports whether no event can move a transfer out of s.
func IsTerminal(s State) bool { return len(table[s]) == 0 }

// Transfer is one money movement. Safe for concurrent use.
type Transfer struct {
	mu        sync.Mutex
	id        string
	amount    int64
	state     State
	history   []Transition
	now       func() time.Time
	settledAt time.Time
}

// New creates a transfer in the Initiated state. id must be non-empty and
// amount (minor units) positive; now defaults to time.Now.
func New(id string, amount int64, now func() time.Time) (*Transfer, error) {
	if id == "" || amount <= 0 {
		return nil, fmt.Errorf("%w: id %q amount %d", ErrInvalid, id, amount)
	}
	if now == nil {
		now = time.Now
	}
	return &Transfer{id: id, amount: amount, state: Initiated, now: now}, nil
}

// State returns the current state.
func (t *Transfer) State() State {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.state
}

// Apply moves the transfer according to the table.
//
// An event not in the table for the current state is ErrIllegalTransition
// (wrapped with the state and event), except when it equals the event that
// produced the current state, which is ErrDuplicate. A failing guard (the
// reversal window) returns its error and leaves the transfer unchanged. Apply
// is atomic: of several concurrent events, each is evaluated against the state
// the previous one left.
func (t *Transfer) Apply(ev Event) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	r, ok := table[t.state][ev]
	if !ok {
		if n := len(t.history); n > 0 && t.history[n-1].Event == ev {
			return fmt.Errorf("%w: %q already applied to %s", ErrDuplicate, ev, t.id)
		}
		return fmt.Errorf("%w: %q in state %q", ErrIllegalTransition, ev, t.state)
	}
	if r.guard != nil {
		if err := r.guard(t); err != nil {
			return err
		}
	}
	at := t.now()
	t.history = append(t.history, Transition{From: t.state, To: r.to, Event: ev, At: at})
	t.state = r.to
	if r.to == Settled {
		t.settledAt = at
	}
	return nil
}

// Allowed lists, sorted, the events the table accepts in the current state
// (guards are not evaluated). Useful for UIs and for exhaustive tests.
func (t *Transfer) Allowed() []Event {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]Event, 0, len(table[t.state]))
	for ev := range table[t.state] {
		out = append(out, ev)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// History returns a copy of the recorded transitions, oldest first.
func (t *Transfer) History() []Transition {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]Transition(nil), t.history...)
}
