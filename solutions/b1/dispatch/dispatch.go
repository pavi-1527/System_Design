// Package dispatch fans events out to pluggable sinks (webhooks, audit log,
// e-mail, a message bus) with retries, typed errors and no global state. It is
// the target design for the legacy dispatcher in legacy.go.
//
// Lab B1 Unit 2. Implement everything marked TODO(lab) so that dispatch_test.go
// passes under -race. The tests include an "extension" test that adds a new
// sink type and a decorator using ONLY code in the test file: if you find
// yourself editing the Dispatcher to support a new sink, the design is wrong.
package dispatch

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// Event is what gets delivered.
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
// Dispatcher to retry; any other error is permanent.
type Sink interface {
	Deliver(ctx context.Context, e Event) error
}

// SinkFunc adapts a function to Sink, like http.HandlerFunc.
type SinkFunc func(ctx context.Context, e Event) error

// Deliver implements Sink.
func (f SinkFunc) Deliver(ctx context.Context, e Event) error { return f(ctx, e) }

// Sentinel errors; callers match them with errors.Is.
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

type temporary interface{ Temporary() bool }

type temporaryError struct{ err error }

func (e temporaryError) Error() string   { return e.err.Error() }
func (e temporaryError) Unwrap() error   { return e.err }
func (e temporaryError) Temporary() bool { return true }

// Temporary marks err as worth retrying. Temporary(nil) is nil.
func Temporary(err error) error {
	if err == nil {
		return nil
	}
	return temporaryError{err}
}

// IsTemporary reports whether any error in err's chain says Temporary() is
// true. It never looks at message text.
func IsTemporary(err error) bool {
	var t temporary
	return errors.As(err, &t) && t.Temporary()
}

// Match reports whether an event type matches a subscription pattern:
// "*" matches everything, "a.b" matches only "a.b", and "a.*" matches every
// type that starts with "a." and has something after it ("a.b", "a.b.c").
func Match(pattern, eventType string) bool {
	switch {
	case pattern == "*":
		return true
	case strings.HasSuffix(pattern, ".*"):
		prefix := strings.TrimSuffix(pattern, "*")
		return len(eventType) > len(prefix) && strings.HasPrefix(eventType, prefix)
	default:
		return pattern == eventType
	}
}

func validPattern(p string) bool {
	if p == "" || strings.ContainsAny(p, " \t\n") {
		return false
	}
	if p == "*" {
		return true
	}
	body := strings.TrimSuffix(p, ".*")
	return body != "" && !strings.Contains(body, "*") && !strings.HasPrefix(body, ".") && !strings.HasSuffix(body, ".")
}

// SinkStats are per-sink counters.
type SinkStats struct {
	Delivered   int       // events delivered successfully
	Failed      int       // events that ended in a DeliveryError
	Attempts    int       // Deliver calls made, including retries
	LastSuccess time.Time // from the injected clock; zero if never
}

type settings struct {
	maxAttempts    int
	backoff        func(attempt int) time.Duration
	sleep          func(ctx context.Context, d time.Duration) error
	attemptTimeout time.Duration
	clock          func() time.Time
}

// Option configures New.
type Option func(*settings) error

// WithMaxAttempts sets the total tries per sink per event (default 3, min 1).
func WithMaxAttempts(n int) Option {
	return func(s *settings) error {
		if n < 1 {
			return fmt.Errorf("WithMaxAttempts(%d): must be >= 1", n)
		}
		s.maxAttempts = n
		return nil
	}
}

// WithBackoff sets the delay after failed attempt number `attempt` (1-based).
// Default: 100ms, 200ms, 400ms, ... capped at 5s.
func WithBackoff(f func(attempt int) time.Duration) Option {
	return func(s *settings) error {
		if f == nil {
			return errors.New("WithBackoff(nil)")
		}
		s.backoff = f
		return nil
	}
}

// WithSleep replaces the sleeper used between attempts. It must return
// ctx.Err() if ctx ends first. Tests inject a fake; production uses a timer.
func WithSleep(f func(ctx context.Context, d time.Duration) error) Option {
	return func(s *settings) error {
		if f == nil {
			return errors.New("WithSleep(nil)")
		}
		s.sleep = f
		return nil
	}
}

// WithAttemptTimeout bounds each Deliver call (0, the default, means no extra bound).
// An attempt that ends because of this timeout, while the caller's ctx is still
// alive, is retried.
func WithAttemptTimeout(d time.Duration) Option {
	return func(s *settings) error {
		if d < 0 {
			return fmt.Errorf("WithAttemptTimeout(%v): negative", d)
		}
		s.attemptTimeout = d
		return nil
	}
}

// WithClock replaces time.Now for SinkStats.LastSuccess.
func WithClock(f func() time.Time) Option {
	return func(s *settings) error {
		if f == nil {
			return errors.New("WithClock(nil)")
		}
		s.clock = f
		return nil
	}
}

type registration struct {
	name     string
	sink     Sink
	patterns []string
}

// Dispatcher routes events to registered sinks. The zero value is ready to use
// with default settings. It is safe for concurrent use and must not be copied
// after first use.
type Dispatcher struct {
	mu    sync.RWMutex
	set   settings
	regs  map[string]*registration
	stats map[string]*SinkStats
}

// New builds a Dispatcher. Every option failure wraps ErrInvalidConfig.
func New(opts ...Option) (*Dispatcher, error) {
	d := &Dispatcher{}
	for _, o := range opts {
		if o == nil {
			return nil, fmt.Errorf("%w: nil option", ErrInvalidConfig)
		}
		if err := o(&d.set); err != nil {
			return nil, fmt.Errorf("%w: %v", ErrInvalidConfig, err)
		}
	}
	return d, nil
}

// Register subscribes sink under a unique name to the given patterns (none
// means "*"). Errors wrap ErrInvalidSink (empty name, nil sink, bad pattern) or
// ErrDuplicateSink. Safe to call while Dispatch is running, including from
// inside a sink.
func (d *Dispatcher) Register(name string, s Sink, patterns ...string) error {
	if name == "" || s == nil {
		return fmt.Errorf("%w: need a name and a sink (name=%q)", ErrInvalidSink, name)
	}
	if len(patterns) == 0 {
		patterns = []string{"*"}
	}
	for _, p := range patterns {
		if !validPattern(p) {
			return fmt.Errorf("%w: sink %q has bad pattern %q", ErrInvalidSink, name, p)
		}
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.regs == nil {
		d.regs = map[string]*registration{}
		d.stats = map[string]*SinkStats{}
	}
	if _, dup := d.regs[name]; dup {
		return fmt.Errorf("%w: %q", ErrDuplicateSink, name)
	}
	d.regs[name] = &registration{name: name, sink: s, patterns: append([]string(nil), patterns...)}
	d.stats[name] = &SinkStats{}
	return nil
}

// Stats returns a copy of every sink's counters.
func (d *Dispatcher) Stats() map[string]SinkStats {
	d.mu.RLock()
	defer d.mu.RUnlock()
	out := make(map[string]SinkStats, len(d.stats))
	for name, s := range d.stats {
		out[name] = *s
	}
	return out
}

func (d *Dispatcher) routes(eventType string) []*registration {
	d.mu.RLock()
	defer d.mu.RUnlock()
	var out []*registration
	for _, r := range d.regs {
		for _, p := range r.patterns {
			if Match(p, eventType) {
				out = append(out, r)
				break
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].name < out[j].name })
	return out
}

// Dispatch delivers e to every sink subscribed to e.Type, concurrently, and
// waits for all of them.
//
// Returns nil when every delivery succeeded; an error wrapping ErrInvalidEvent
// for an event without ID or Type; an error wrapping ErrNoRoute (naming the
// event) when nobody is subscribed; the context's error if ctx is already done;
// otherwise errors.Join of one *DeliveryError per failed sink, ordered by sink
// name, so callers can inspect each (Unwrap() []error) and errors.Is/As reach
// every cause.
//
// Per sink: up to MaxAttempts calls. A failed attempt is retried only if its
// error satisfies IsTemporary, or it was cut off by the attempt timeout while
// ctx is still alive. Panics are recovered into an error wrapping ErrSinkPanic
// and never retried. No lock is held while a sink runs.
func (d *Dispatcher) Dispatch(ctx context.Context, e Event) error {
	if e.ID == "" || e.Type == "" {
		return fmt.Errorf("%w: id=%q type=%q", ErrInvalidEvent, e.ID, e.Type)
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("dispatch: event %q: %w", e.ID, err)
	}
	rs := d.routes(e.Type)
	if len(rs) == 0 {
		return fmt.Errorf("dispatch: event %q type %q: %w", e.ID, e.Type, ErrNoRoute)
	}
	errs := make([]error, len(rs))
	var wg sync.WaitGroup
	for i, r := range rs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = d.deliver(ctx, r, e)
		}()
	}
	wg.Wait()
	return errors.Join(errs...)
}

func (d *Dispatcher) deliver(ctx context.Context, r *registration, e Event) error {
	set := d.snapshotSettings()
	var lastErr error
	attempts := 0
	for attempt := 1; attempt <= set.maxAttempts; attempt++ {
		attempts = attempt
		err := d.attempt(ctx, set, r.sink, e)
		if err == nil {
			d.record(r.name, attempts, true, set.clock())
			return nil
		}
		lastErr = err
		retry := !errors.Is(err, ErrSinkPanic) && ctx.Err() == nil &&
			(IsTemporary(err) || (set.attemptTimeout > 0 && errors.Is(err, context.DeadlineExceeded)))
		if !retry || attempt == set.maxAttempts {
			break
		}
		if serr := set.sleep(ctx, set.backoff(attempt)); serr != nil {
			lastErr = fmt.Errorf("backoff interrupted: %w (last error: %w)", serr, err)
			break
		}
	}
	d.record(r.name, attempts, false, time.Time{})
	return &DeliveryError{Sink: r.name, EventID: e.ID, Attempts: attempts, Err: lastErr}
}

func (d *Dispatcher) attempt(ctx context.Context, set settings, s Sink, e Event) (err error) {
	if set.attemptTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, set.attemptTimeout)
		defer cancel()
	}
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("%w: %v", ErrSinkPanic, r)
		}
	}()
	return s.Deliver(ctx, e)
}

func (d *Dispatcher) record(name string, attempts int, ok bool, at time.Time) {
	d.mu.Lock()
	defer d.mu.Unlock()
	st := d.stats[name]
	if st == nil {
		return
	}
	st.Attempts += attempts
	if ok {
		st.Delivered++
		st.LastSuccess = at
	} else {
		st.Failed++
	}
}

// snapshotSettings fills in defaults for anything the caller did not set, so
// the zero Dispatcher works.
func (d *Dispatcher) snapshotSettings() settings {
	d.mu.RLock()
	s := d.set
	d.mu.RUnlock()
	if s.maxAttempts == 0 {
		s.maxAttempts = 3
	}
	if s.backoff == nil {
		s.backoff = defaultBackoff
	}
	if s.sleep == nil {
		s.sleep = sleepCtx
	}
	if s.clock == nil {
		s.clock = time.Now
	}
	return s
}

func defaultBackoff(attempt int) time.Duration {
	d := 100 * time.Millisecond
	for i := 1; i < attempt && d < 5*time.Second; i++ {
		d *= 2
	}
	if d > 5*time.Second {
		d = 5 * time.Second
	}
	return d
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
