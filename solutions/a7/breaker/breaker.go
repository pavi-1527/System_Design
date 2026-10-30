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
// classification and the generation guard. Run: go test -race ./labs/a7/breaker/
package breaker

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

// State is the breaker's mode.
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

// Outcome is how one finished call is recorded.
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

// Config configures a Breaker. Zero values get the defaults noted.
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

func (c Config) withDefaults() Config {
	if c.Window <= 0 {
		c.Window = 10 * time.Second
	}
	if c.Buckets <= 0 {
		c.Buckets = 10
	}
	if c.Window/time.Duration(c.Buckets) <= 0 {
		c.Buckets = 1
	}
	if c.MinRequests <= 0 {
		c.MinRequests = 20
	}
	if c.FailureRatio <= 0 && c.ConsecutiveFailures <= 0 {
		c.FailureRatio = 0.5
	}
	if c.OpenFor <= 0 {
		c.OpenFor = 30 * time.Second
	}
	if c.HalfOpenMax <= 0 {
		c.HalfOpenMax = 1
	}
	if c.HalfOpenSuccesses <= 0 {
		c.HalfOpenSuccesses = 1
	}
	if c.Classify == nil {
		c.Classify = defaultClassify
	}
	if c.Now == nil {
		c.Now = time.Now
	}
	return c
}

func defaultClassify(err error) Outcome {
	switch {
	case err == nil:
		return Success
	case errors.Is(err, context.Canceled):
		return Ignore
	default:
		return Failure
	}
}

type bucket struct {
	id        int64
	successes int
	failures  int
}

type transition struct{ from, to State }

// Breaker is safe for concurrent use.
type Breaker struct {
	cfg Config
	bd  time.Duration // bucket duration

	mu          sync.Mutex
	state       State
	gen         uint64 // bumped on every state change; stale results are dropped
	openedAt    time.Time
	buckets     []bucket
	consecutive int
	probes      int // probes in flight (half-open)
	probeOK     int // probe successes so far (half-open)
	pending     []transition
}

// New returns a closed Breaker.
func New(cfg Config) *Breaker {
	cfg = cfg.withDefaults()
	return &Breaker{
		cfg:     cfg,
		bd:      cfg.Window / time.Duration(cfg.Buckets),
		buckets: make([]bucket, cfg.Buckets),
	}
}

// lockAndAdvance takes the lock and applies the one time-driven transition:
// Open → HalfOpen once OpenFor has elapsed. Callers must use unlock.
func (b *Breaker) lockAndAdvance() time.Time {
	b.mu.Lock()
	now := b.cfg.Now()
	if b.state == Open && !now.Before(b.openedAt.Add(b.cfg.OpenFor)) {
		b.setState(HalfOpen, now)
	}
	return now
}

// unlock releases the lock and then delivers queued transitions.
func (b *Breaker) unlock() {
	ev := b.pending
	b.pending = nil
	b.mu.Unlock()
	if b.cfg.OnStateChange != nil {
		for _, t := range ev {
			b.cfg.OnStateChange(t.from, t.to)
		}
	}
}

func (b *Breaker) setState(to State, now time.Time) {
	from := b.state
	if from == to {
		return
	}
	b.state = to
	b.gen++
	b.probes, b.probeOK = 0, 0
	switch to {
	case Open:
		b.openedAt = now
	case Closed:
		b.buckets = make([]bucket, len(b.buckets))
		b.consecutive = 0
	}
	b.pending = append(b.pending, transition{from, to})
}

// State reports the current state (applying the Open → HalfOpen timeout).
func (b *Breaker) State() State {
	b.lockAndAdvance()
	defer b.unlock()
	return b.state
}

// Counts reports successes and failures currently inside the rolling window.
func (b *Breaker) Counts() (successes, failures int) {
	now := b.lockAndAdvance()
	defer b.unlock()
	return b.counts(now)
}

func (b *Breaker) counts(now time.Time) (s, f int) {
	cur := now.UnixNano() / int64(b.bd)
	for _, bk := range b.buckets {
		if bk.id > cur-int64(len(b.buckets)) && bk.id <= cur {
			s += bk.successes
			f += bk.failures
		}
	}
	return s, f
}

func (b *Breaker) record(now time.Time, o Outcome) {
	cur := now.UnixNano() / int64(b.bd)
	bk := &b.buckets[int(((cur%int64(len(b.buckets)))+int64(len(b.buckets)))%int64(len(b.buckets)))]
	if bk.id != cur {
		*bk = bucket{id: cur}
	}
	if o == Success {
		bk.successes++
		b.consecutive = 0
	} else {
		bk.failures++
		b.consecutive++
	}
}

func (b *Breaker) shouldTrip(now time.Time) bool {
	if b.cfg.ConsecutiveFailures > 0 && b.consecutive >= b.cfg.ConsecutiveFailures {
		return true
	}
	if b.cfg.FailureRatio > 0 {
		s, f := b.counts(now)
		total := s + f
		if total >= b.cfg.MinRequests && float64(f) >= b.cfg.FailureRatio*float64(total) {
			return true
		}
	}
	return false
}

// admit decides whether a call may proceed.
func (b *Breaker) admit() (gen uint64, probe bool, err error) {
	b.lockAndAdvance()
	defer b.unlock()
	switch b.state {
	case Open:
		return 0, false, ErrOpen
	case HalfOpen:
		if b.probes >= b.cfg.HalfOpenMax {
			return 0, false, ErrOpen
		}
		b.probes++
		return b.gen, true, nil
	}
	return b.gen, false, nil
}

// report records the outcome of an admitted call. Results from an earlier
// generation (the breaker changed state while the call was running) are
// dropped: they describe a dependency we have already acted on.
func (b *Breaker) report(gen uint64, probe bool, o Outcome) {
	now := b.lockAndAdvance()
	defer b.unlock()
	if gen != b.gen {
		return
	}
	if probe {
		b.probes--
	}
	if o == Ignore {
		return
	}
	switch b.state {
	case Closed:
		b.record(now, o)
		if o == Failure && b.shouldTrip(now) {
			b.setState(Open, now)
		}
	case HalfOpen:
		if o == Failure {
			b.setState(Open, now)
			return
		}
		b.probeOK++
		if b.probeOK >= b.cfg.HalfOpenSuccesses {
			b.setState(Closed, now)
		}
	}
}

// Allow is the low-level API: it returns ErrOpen, or a done function that the
// caller must call exactly once with the call's error. Calling done more
// than once has no effect. Do is built on the same mechanism.
func (b *Breaker) Allow() (done func(err error), err error) {
	gen, probe, err := b.admit()
	if err != nil {
		return nil, err
	}
	var once sync.Once
	return func(callErr error) {
		once.Do(func() { b.report(gen, probe, b.cfg.Classify(callErr)) })
	}, nil
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
func (b *Breaker) Do(ctx context.Context, fn func(ctx context.Context) error) error {
	gen, probe, err := b.admit()
	if err != nil {
		return err
	}
	finished := false
	defer func() {
		if !finished {
			b.report(gen, probe, Failure)
		}
	}()
	callErr := fn(ctx)
	finished = true
	if callErr != nil && ctx.Err() != nil {
		b.report(gen, probe, Ignore)
		return callErr
	}
	b.report(gen, probe, b.cfg.Classify(callErr))
	return callErr
}
