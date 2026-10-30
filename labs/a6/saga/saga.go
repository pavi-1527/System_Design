// Package saga is a small saga orchestrator: it runs a sequence of steps that
// each change a different service, and if a step fails it undoes the earlier
// ones with compensating actions. Progress is persisted after every step, so
// a restarted orchestrator resumes where the dead one stopped. It is the lab
// for A6 Unit 3.
//
// Suggested order: MemoryStore, New (validation), the happy path of Run, then
// compensation, then timeouts, then crash/resume, then pivots.
// Run: go test -race ./labs/a6/saga/
//
// # Model
//
// A saga is a list of Steps. A Step has an Action and an optional
// Compensate; both receive a Call whose Key is stable across retries and
// restarts (Key(sagaID, step, false) for actions, Key(..., true) for
// compensations). The services behind the steps are expected to be
// idempotent per Key, because the orchestrator can repeat a call (see
// "Crashes"). A step with Pivot set is the point of no return: once it has
// succeeded the saga only moves forward. Steps after the pivot must be
// Retriable (and have no Compensate); steps before it must not be Retriable.
// New validates this and returns ErrInvalidSteps otherwise, also for empty or
// duplicate step names, a nil Action, or more than one Pivot.
//
// # State and Store
//
// State is persisted through a Store after every transition:
//
//   - Status Running: Next is the index of the next forward step; when Next
//     reaches len(steps) the status becomes Completed.
//   - Status Compensating: Next is the index of the next step to compensate,
//     counting down; below 0 the status becomes Compensated. A nil Compensate
//     is skipped.
//   - Status Stuck: retries ran out (see below). StuckIn remembers the status
//     it was in. Resume puts it back and continues.
//   - Trail records "do:<step>" and "undo:<step>" for every completed call, in
//     order. Err holds the failure that started the compensation, or the
//     last error when stuck. Attempts counts failures of the current call.
//
// Store.Save(st) writes st only if the stored Version equals st.Version (0
// for a saga that does not exist yet), returns the state with the new
// Version, and returns ErrConflict otherwise. MemoryStore returns copies:
// callers cannot alter stored state through slices.
//
// # Run
//
// Run(ctx, id) loads the saga (creating it as Running, Next 0 if absent) and
// drives it until it reaches a terminal status (Completed, Compensated,
// Stuck), returning the final State. A saga that is already terminal is
// returned untouched and nothing is called.
//
//   - Forward: call step.Action with a timeout (step.Timeout, or
//     Config.StepTimeout). On success persist Next+1 and a "do:" trail entry.
//   - A failing non-Retriable step starts compensation: persist Status
//     Compensating, Err, and Next = index of the failed step - 1. If the
//     failure was ErrStepTimeout the outcome of the call is unknown (it may
//     still complete later), so the failed step is compensated too: Next =
//     index of the failed step. Compensations must therefore be safe to run
//     for an action that never happened (and must stop a late action from
//     happening).
//   - A failing Retriable step is retried: persist Attempts+1, wait
//     Config.Backoff(attempts) via Config.Sleep, call again. After
//     Config.MaxAttempts failures the saga becomes Stuck: it never
//     compensates after the pivot.
//   - A failing Compensate is retried the same way and, after MaxAttempts,
//     the saga becomes Stuck. Run returns the state and ErrStuck.
//   - A timeout is enforced with Config.NewTimer, not by the action
//     cooperating: Run stops waiting at the deadline even if the action
//     ignores its context (the action's goroutine is abandoned). A panic in
//     an action is recovered and treated as an error.
//   - If ctx is cancelled, Run returns ctx.Err() and the latest persisted
//     state; the saga can be run again later.
//   - If a Save fails with ErrConflict, another runner is driving this saga:
//     Run returns the error and does nothing else.
//
// # Crashes
//
// Config.Hook is called with a point name just before and just after every
// Action and Compensate call: "before-action:<step>", "after-action:<step>",
// "before-compensate:<step>", "after-compensate:<step>". If it returns an
// error, that is a crash: Run returns the error immediately, without
// persisting anything further. A new Orchestrator over the same Store and
// the same services then continues from the persisted state. A crash after a
// call returns but before the state is saved makes the next Run repeat the
// call with the SAME Key; that is why the services must be idempotent.
//
// Resume(ctx, id) moves a Stuck saga back to the status in StuckIn with
// Attempts reset, persists it, and continues like Run. ErrNotStuck if the
// saga is not stuck.
package saga

import (
	"context"
	"errors"
	"time"
)

var errNotImplemented = errors.New("saga: not implemented")

// Errors returned by the package.
var (
	ErrInvalidSteps = errors.New("saga: invalid step definition")
	ErrConflict     = errors.New("saga: state was changed by another runner")
	ErrStepTimeout  = errors.New("saga: step timed out")
	ErrStuck        = errors.New("saga: stuck, needs operator attention")
	ErrNotStuck     = errors.New("saga: not stuck")
)

// Status of a saga.
type Status string

// Saga statuses.
const (
	StatusRunning      Status = "running"
	StatusCompensating Status = "compensating"
	StatusCompleted    Status = "completed"
	StatusCompensated  Status = "compensated"
	StatusStuck        Status = "stuck"
)

// Terminal reports whether no further progress happens without an operator.
func (s Status) Terminal() bool {
	return s == StatusCompleted || s == StatusCompensated || s == StatusStuck
}

// Call identifies one invocation of an Action or Compensate.
type Call struct {
	SagaID string
	Step   string
	Key    string // idempotency key: stable across retries and restarts
}

// Key returns the idempotency key for a step of a saga.
func Key(sagaID, step string, compensate bool) string {
	if compensate {
		return sagaID + "/" + step + "/compensate"
	}
	return sagaID + "/" + step
}

// Step is one unit of work in a saga.
type Step struct {
	Name       string
	Action     func(ctx context.Context, call Call) error
	Compensate func(ctx context.Context, call Call) error // nil: nothing to undo
	Pivot      bool                                       // point of no return
	Retriable  bool                                       // after the pivot: retried, never compensated
	Timeout    time.Duration                              // 0: Config.StepTimeout
}

// State is the persisted state of one saga.
type State struct {
	ID       string
	Status   Status
	StuckIn  Status
	Next     int
	Attempts int
	Err      string
	Trail    []string
	Version  int64
}

// Store persists saga state with optimistic concurrency.
type Store interface {
	Load(ctx context.Context, id string) (State, bool, error)
	// Save writes st if the stored version equals st.Version (0 for new) and
	// returns st with its new Version; ErrConflict otherwise.
	Save(ctx context.Context, st State) (State, error)
}

// MemoryStore is an in-memory Store. Safe for concurrent use; it returns
// copies so callers cannot alter stored state through shared slices.
type MemoryStore struct {
	// TODO(lab): a mutex and a map[string]State.
}

// NewMemoryStore returns an empty MemoryStore.
func NewMemoryStore() *MemoryStore {
	// TODO(lab)
	return &MemoryStore{}
}

// Load implements Store.
func (s *MemoryStore) Load(_ context.Context, id string) (State, bool, error) {
	// TODO(lab)
	return State{}, false, errNotImplemented
}

// Save implements Store: version check, increment, copy.
func (s *MemoryStore) Save(_ context.Context, st State) (State, error) {
	// TODO(lab)
	return State{}, errNotImplemented
}

// Config configures an Orchestrator.
type Config struct {
	Store       Store                                            // default: NewMemoryStore()
	Steps       []Step                                           // required
	StepTimeout time.Duration                                    // default 30s
	MaxAttempts int                                              // default 5
	Backoff     func(attempt int) time.Duration                  // default: none
	Sleep       func(ctx context.Context, d time.Duration) error // default: real sleep that honours ctx
	NewTimer    func(d time.Duration) (<-chan time.Time, func()) // default: time.NewTimer (return its channel and Stop)
	Hook        func(point string) error
}

// Orchestrator drives sagas.
type Orchestrator struct {
	cfg Config
}

// New validates the step list (ErrInvalidSteps), applies the defaults and
// returns an Orchestrator.
func New(cfg Config) (*Orchestrator, error) {
	// TODO(lab)
	return nil, errNotImplemented
}

// Run drives saga id to a terminal status. See the package documentation.
func (o *Orchestrator) Run(ctx context.Context, id string) (State, error) {
	// TODO(lab)
	return State{}, errNotImplemented
}

// Resume continues a Stuck saga. See the package documentation.
func (o *Orchestrator) Resume(ctx context.Context, id string) (State, error) {
	// TODO(lab)
	return State{}, errNotImplemented
}
