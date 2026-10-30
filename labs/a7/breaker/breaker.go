// Package breaker is a circuit breaker: closed → open → half-open, with a
// rolling window of outcomes, a failure-ratio or consecutive-failure
// threshold, a bounded number of half-open probes, and a state-change
// callback.
//
// It is the lab for A7 Unit 2 (§1). The design follows what resilience4j,
// Envoy's outlier detection and Polly do, reduced to the decisions: what
// counts as a failure, when to trip, how to probe for recovery without
// stampeding the dependency, and how to ignore results that say nothing about
// the dependency's health (the caller giving up, a 404).
//
// Suggested order: the rolling window (counts), Closed → Open, Open →
// HalfOpen (lazily, from the injected clock), probes, then outcome
// classification and the generation guard. Run:
//
//	go test -race ./labs/a7/breaker/
package breaker

import (
	"context"
	"errors"
	"fmt"
	"time"
)

var errNotImplemented = errors.New("breaker: not implemented")

// State is the breaker's mode. Provided.
type State int

const (
	// Closed passes every call through and records outcomes.
	Closed State = iota
	// Open rejects every call with ErrOpen until OpenFor has elapsed.
	Open
	// HalfOpen admits a limited number of probe calls to test recovery.
	HalfOpen
)

func (s State) String() string {
	switch s {
	case Closed:
		return "closed"
	case Open:
		return "open"
	case HalfOpen:
		return "half-open"
	}
	return fmt.Sprintf("State(%d)", int(s))
}

// ErrOpen is returned when a call is rejected: the breaker is open, or it is
// half-open and all probe slots are taken.
var ErrOpen = errors.New("breaker: open")

// Outcome is how one finished call is recorded. Provided.
type Outcome int

const (
	// Success: the dependency answered; counts toward health.
	Success Outcome = iota
	// Failure: the dependency is unhealthy (error, timeout, 5xx).
	Failure
	// Ignore: says nothing about the dependency (the caller cancelled, the
	// request was invalid). Not counted; a half-open probe slot is freed
	// without a verdict.
	Ignore
)

// Config configures a Breaker. Zero values get the defaults noted. Provided.
type Config struct {
	// Window is the length of the rolling window over which outcomes are
	// counted, divided into Buckets equal buckets. Defaults 10s and 10.
	Window  time.Duration
	Buckets int
	// MinRequests is the minimum number of calls in the window before the
	// failure ratio is evaluated. Default 20.
	MinRequests int
	// FailureRatio trips the breaker when failures/total in the window is
	// at least this value (and total ≥ MinRequests). 0 disables the ratio
	// rule, unless ConsecutiveFailures is also 0, in which case it
	// defaults to 0.5.
	FailureRatio float64
	// ConsecutiveFailures trips the breaker after this many failures in a
	// row (a success resets the run), regardless of volume. 0 disables it.
	ConsecutiveFailures int
	// OpenFor is how long the breaker stays open before it half-opens.
	// Default 30s.
	OpenFor time.Duration
	// HalfOpenMax is the maximum number of probe calls in flight while
	// half-open. Default 1.
	HalfOpenMax int
	// HalfOpenSuccesses is the number of probe successes needed to close.
	// Default 1.
	HalfOpenSuccesses int
	// Classify maps a call's error to an Outcome. Default: nil → Success,
	// context.Canceled → Ignore, anything else → Failure.
	Classify func(err error) Outcome
	// Now is the clock. Default time.Now.
	Now func() time.Time
	// OnStateChange, if non-nil, is called after every transition, outside
	// the breaker's lock (so it may call State or Counts), by the goroutine
	// that caused the transition.
	OnStateChange func(from, to State)
}

// Breaker is safe for concurrent use.
//
// Design notes (the tests rely on all of them):
//   - The rolling window is a ring of Buckets counters, each Window/Buckets
//     wide, identified by now/bucketWidth. A bucket whose id is older than
//     the window is ignored and reused. No background goroutine.
//   - Open → HalfOpen is lazy: any call to State, Counts or Allow/Do first
//     checks whether OpenFor has elapsed.
//   - Every state change bumps a generation number. A call remembers the
//     generation it was admitted under; if the generation changed by the
//     time it finishes, its result is dropped (a straggler from before the
//     trip must not close a half-open breaker or re-open it).
//   - Entering Closed clears the window, so old failures cannot re-trip it.
//   - Call OnStateChange after releasing the lock.
type Breaker struct {
	// TODO(lab): config with defaults applied, a mutex, the state, the
	// generation, the time the breaker opened, the bucket ring, the
	// consecutive-failure run, probes in flight and probe successes.
}

// New returns a closed Breaker with defaults applied to cfg.
func New(cfg Config) *Breaker {
	// TODO(lab): apply the defaults documented on Config.
	return &Breaker{}
}

// State reports the current state (applying the Open → HalfOpen timeout).
func (b *Breaker) State() State {
	// TODO(lab)
	return Closed
}

// Counts reports successes and failures currently inside the rolling window.
func (b *Breaker) Counts() (successes, failures int) {
	// TODO(lab)
	return 0, 0
}

// Allow is the low-level API: it returns ErrOpen, or a done function that the
// caller must call exactly once with the call's error. Calling done more
// than once has no effect. done classifies err with Config.Classify. A
// rejected call must not occupy a probe slot.
func (b *Breaker) Allow() (done func(err error), err error) {
	// TODO(lab)
	return nil, errNotImplemented
}

// Do runs fn if the breaker admits it and records the result.
//
//   - Rejected calls return ErrOpen without running fn.
//   - If fn returns an error while ctx is already done (the caller gave up
//     or ran out of budget), the result is Ignore whatever Classify says:
//     it tells us nothing about the dependency.
//   - Otherwise the result is Classify(err). An fn that returns
//     context.DeadlineExceeded while ctx is still live (the dependency was
//     too slow for its own timeout) is a Failure with the default Classify.
//   - If fn panics, the call is recorded as a Failure and the panic
//     continues.
//   - fn's own error is returned unchanged.
func (b *Breaker) Do(ctx context.Context, fn func(ctx context.Context) error) error {
	// TODO(lab): admit, run, report. Use a deferred function to catch the
	// panic case without recovering it.
	return errNotImplemented
}
