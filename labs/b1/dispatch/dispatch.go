// Package dispatch fans events out to pluggable sinks (webhooks, audit log,
// e-mail, a message bus) with retries, typed errors and no global state. It is
// the target design for the legacy dispatcher in legacy.go.
//
// Lab B1 Unit 2. Implement everything marked TODO(lab) so that dispatch_test.go
// passes under -race. The tests include an "extension" test that adds a new
// sink type and two decorators using ONLY code in the test file: if you find
// yourself editing the Dispatcher to support a new sink, the design is wrong.
//
// Suggested order: Match, Temporary/IsTemporary, options and New, Register and
// Stats, then Dispatch (one sink, retries, panics, then concurrency).
//
//	go test -race -run 'TestMatch|TestIsTemporary' ./labs/b1/dispatch/
package dispatch

import (
	"context"
	"errors"
	"fmt"
	"time"
)

var errNotImplemented = errors.New("dispatch: not implemented")

// Event is what gets delivered. (GIVEN)
type Event struct {
	ID      string // unique, required
	Type    string // dotted, e.g. "payment.settled", required
	Tenant  string
	Payload []byte
}

// Sink is the one thing the Dispatcher needs from a destination. It is declared
// here, at the consumer; implementations live in other packages (or in tests)
// and satisfy it implicitly.
//
// Deliver must honour ctx. Returning an error marked with Temporary asks the
// Dispatcher to retry; any other error is permanent. (GIVEN)
type Sink interface {
	Deliver(ctx context.Context, e Event) error
}

// SinkFunc adapts a function to Sink, like http.HandlerFunc. (GIVEN)
type SinkFunc func(ctx context.Context, e Event) error

// Deliver implements Sink.
func (f SinkFunc) Deliver(ctx context.Context, e Event) error { return f(ctx, e) }

// Sentinel errors; callers match them with errors.Is. (GIVEN)
var (
	ErrInvalidConfig = errors.New("dispatch: invalid configuration")
	ErrInvalidEvent  = errors.New("dispatch: invalid event")
	ErrInvalidSink   = errors.New("dispatch: invalid sink registration")
	ErrDuplicateSink = errors.New("dispatch: sink already registered")
	ErrNoRoute       = errors.New("dispatch: no sink subscribed to event type")
	ErrSinkPanic     = errors.New("dispatch: sink panicked")
)

// DeliveryError says which sink failed which event, after how many attempts,
// and why. It unwraps to the sink's own error, so errors.Is/As see through it.
// (GIVEN)
type DeliveryError struct {
	Sink     string
	EventID  string
	Attempts int
	Err      error
}

// Error renders a message an operator can act on without opening the code.
func (e *DeliveryError) Error() string {
	return fmt.Sprintf("dispatch: sink %q failed event %q after %d attempt(s): %v", e.Sink, e.EventID, e.Attempts, e.Err)
}

// Unwrap returns the underlying cause.
func (e *DeliveryError) Unwrap() error { return e.Err }

// Temporary marks err as worth retrying: the returned error keeps err's
// message, unwraps to err, and has a Temporary() bool method returning true.
// Temporary(nil) is nil.
func Temporary(err error) error {
	// TODO(lab): a small unexported wrapper type.
	return err
}

// IsTemporary reports whether any error in err's chain has a Temporary() bool
// method that returns true. It must never look at message text, and it must
// also recognise Temporary() methods on types you did not write (the pattern
// net.Error uses).
func IsTemporary(err error) bool {
	// TODO(lab): errors.As with an interface target.
	_ = err
	return false
}

// Match reports whether an event type matches a subscription pattern:
// "*" matches everything, "a.b" matches only "a.b", and "a.*" matches every
// type that starts with "a." and has something after it ("a.b", "a.b.c"; not
// "a" and not "a.").
func Match(pattern, eventType string) bool {
	// TODO(lab)
	_, _ = pattern, eventType
	return false
}

// SinkStats are per-sink counters. (GIVEN)
type SinkStats struct {
	Delivered   int       // events delivered successfully
	Failed      int       // events that ended in a DeliveryError
	Attempts    int       // Deliver calls made, including retries
	LastSuccess time.Time // from the injected clock; zero if never
}

// settings is the configuration the options fill in. TODO(lab): add fields
// (max attempts, backoff, sleep, attempt timeout, clock). The zero value must
// mean "defaults": 3 attempts, backoff 100ms doubling to a 5s cap, a
// context-aware timer sleep, no attempt timeout, time.Now.
type settings struct{}

// Option configures New.
type Option func(*settings) error

// WithMaxAttempts sets the total tries per sink per event (default 3, min 1).
func WithMaxAttempts(n int) Option {
	// TODO(lab)
	_ = n
	return func(*settings) error { return errNotImplemented }
}

// WithBackoff sets the delay after failed attempt number `attempt` (1-based).
// nil is an error.
func WithBackoff(f func(attempt int) time.Duration) Option {
	// TODO(lab)
	_ = f
	return func(*settings) error { return errNotImplemented }
}

// WithSleep replaces the sleeper used between attempts. It must return
// ctx.Err() if ctx ends first. nil is an error.
func WithSleep(f func(ctx context.Context, d time.Duration) error) Option {
	// TODO(lab)
	_ = f
	return func(*settings) error { return errNotImplemented }
}

// WithAttemptTimeout bounds each Deliver call (0, the default, means no extra
// bound; negative is an error). An attempt that ends because of this timeout,
// while the caller's ctx is still alive, is retried.
func WithAttemptTimeout(d time.Duration) Option {
	// TODO(lab)
	_ = d
	return func(*settings) error { return errNotImplemented }
}

// WithClock replaces time.Now for SinkStats.LastSuccess. nil is an error.
func WithClock(f func() time.Time) Option {
	// TODO(lab)
	_ = f
	return func(*settings) error { return errNotImplemented }
}

// Dispatcher routes events to registered sinks. The zero value is ready to use
// with default settings (the tests use one). It is safe for concurrent use and
// must not be copied after first use.
// TODO(lab): add the fields you need (a lock, the registrations, the stats, settings).
type Dispatcher struct{}

// New builds a Dispatcher. Every option failure wraps ErrInvalidConfig.
func New(opts ...Option) (*Dispatcher, error) {
	// TODO(lab)
	_ = opts
	return nil, errNotImplemented
}

// Register subscribes sink under a unique name to the given patterns (none
// means "*"). Errors wrap ErrInvalidSink (empty name, nil sink, a bad pattern:
// empty, containing whitespace, or with '*' anywhere except as the whole
// pattern or the final ".*") or ErrDuplicateSink. A rejected registration
// leaves no trace. Safe to call while Dispatch is running, including from
// inside a sink.
func (d *Dispatcher) Register(name string, s Sink, patterns ...string) error {
	// TODO(lab)
	_, _, _ = name, s, patterns
	return errNotImplemented
}

// Stats returns a COPY of every sink's counters.
func (d *Dispatcher) Stats() map[string]SinkStats {
	// TODO(lab)
	return map[string]SinkStats{}
}

// Dispatch delivers e to every sink subscribed to e.Type, concurrently, and
// waits for all of them.
//
// Returns nil when every delivery succeeded; an error wrapping ErrInvalidEvent
// for an event without ID or Type; the context's error (wrapped) if ctx is
// already done; an error wrapping ErrNoRoute and naming the event ID and type
// when nobody is subscribed; otherwise errors.Join of one *DeliveryError per
// failed sink, ordered by sink name, so callers can inspect each
// (Unwrap() []error) and errors.Is/As reach every cause.
//
// Per sink: up to MaxAttempts calls. A failed attempt is retried only if its
// error satisfies IsTemporary, or it was cut off by the attempt timeout while
// ctx is still alive; never once ctx is done; never after a panic. Between
// attempts, sleep backoff(attempt) using the injected sleeper; if the sleep is
// interrupted, stop and report an error in which BOTH the interruption and
// the sink's last error are reachable with errors.Is. Panics are recovered into
// an error wrapping ErrSinkPanic that includes the panic value. No lock may be
// held while a sink runs. Update the stats once per sink per event.
func (d *Dispatcher) Dispatch(ctx context.Context, e Event) error {
	// TODO(lab)
	_, _ = ctx, e
	return errNotImplemented
}
