// Package dispatch is the lab for C7 (case study: notification service),
// Unit 2 §5–§6: the core of a notification worker, the part of the system
// that turns "a notification was requested" into "a provider accepted it",
// or into a recorded reason why not.
//
// One Dispatcher owns:
//
//   - a dedupe set keyed by Notification.ID: a given ID is accepted once,
//     however many times a retrying caller sends it (idempotency);
//   - two queues, Urgent and Normal, each ordered by due time and then by
//     arrival: an urgent one-time passcode never waits behind a marketing
//     blast;
//   - the suppression rules that apply to Normal notifications before any
//     provider is called: opt-out (recorded, dropped), quiet hours (deferred
//     until they end) and a per-user, per-channel token bucket (recorded,
//     dropped). Urgent notifications bypass all three: in this lab "urgent"
//     means a security or regulatory message the user must receive;
//   - an ordered list of providers per channel with one circuit breaker per
//     provider name, failover to the next provider when one fails or its
//     breaker is open, a per-call timeout, bounded attempts with backoff
//     between passes, and a dead-letter list for notifications that
//     exhausted them;
//   - a delivery log: one Record per outcome.
//
// There are no background goroutines. Time comes from Config.Now, and
// Process does one pass over everything that is due at that time, so tests
// (and a real service's run loop) decide when work happens. A real service
// loops: Process, then sleep until NextDue or the next Enqueue, whichever
// comes first.
package dispatch

import (
	"container/heap"
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

// Errors returned by New and Enqueue.
var (
	// ErrConfig: Config is unusable (no clock, a rate limit or breaker
	// without a window, a provider without a name).
	ErrConfig = errors.New("dispatch: invalid config")
	// ErrInvalid: the notification has no ID, no user or no channel, or an
	// unknown priority.
	ErrInvalid = errors.New("dispatch: invalid notification")
	// ErrDuplicate: a notification with this ID was already accepted.
	ErrDuplicate = errors.New("dispatch: duplicate notification id")
)

// Channel is a delivery channel. The dispatcher treats channels as opaque
// keys into Config.Providers; these three are the ones the tests use.
type Channel string

// The channels used by the tests.
const (
	Push  Channel = "push"
	SMS   Channel = "sms"
	Email Channel = "email"
)

// Priority is the lane a notification is queued in.
type Priority int

const (
	// Normal: marketing, digests, informational alerts. Subject to opt-out,
	// quiet hours and the per-user rate limit; dequeued after Urgent.
	Normal Priority = iota
	// Urgent: one-time passcodes, fraud alerts, anything the user must
	// receive now. Bypasses opt-out, quiet hours and the rate limit, and is
	// dequeued before any Normal notification that is due.
	Urgent
)

func (p Priority) String() string {
	switch p {
	case Normal:
		return "normal"
	case Urgent:
		return "urgent"
	}
	return fmt.Sprintf("priority(%d)", int(p))
}

// Notification is one message to one user on one channel. ID is the
// caller's idempotency key: Enqueue accepts a given ID once.
type Notification struct {
	ID       string
	TenantID string
	UserID   string
	Channel  Channel
	Priority Priority
	Body     string
}

// Outcome is what happened to a notification, as recorded in the log.
type Outcome string

// Outcomes.
const (
	Delivered  Outcome = "delivered"  // a provider accepted it
	OptedOut   Outcome = "opted_out"  // the user opted out of the channel; dropped
	Suppressed Outcome = "suppressed" // the per-user rate limit; dropped
	Deferred   Outcome = "deferred"   // quiet hours; requeued for Record.NextAt
	Failed     Outcome = "failed"     // attempts exhausted or no provider; dead-lettered
)

// Attempt is one provider call.
type Attempt struct {
	At       time.Time
	Provider string
	Err      error // nil means the provider accepted the notification
}

// Record is one entry in the delivery log.
type Record struct {
	Notification Notification
	Outcome      Outcome
	Reason       string    // human-readable detail for every outcome but Delivered
	Attempts     []Attempt // every provider call so far, in order
	At           time.Time // when the outcome was recorded
	NextAt       time.Time // Deferred only: when the notification is due again
}

// Preferences answers the two questions the dispatcher asks about a user
// before sending a Normal notification. Implementations must be safe for
// concurrent use.
type Preferences interface {
	// OptedOut reports whether the user has opted out of ch.
	OptedOut(tenant, user string, ch Channel) bool
	// Quiet reports whether now falls in the user's quiet hours and, if so,
	// when they end. quiet=true with until <= now is treated as not quiet.
	Quiet(tenant, user string, now time.Time) (quiet bool, until time.Time)
}

// Provider is one delivery vendor for one or more channels. Send returns
// nil when the provider accepted the notification, which is not the same
// as the user having received it (Unit 2 §5). It must respect ctx.
type Provider interface {
	Name() string
	Send(ctx context.Context, n Notification) error
}

// RateLimit is a token bucket per (tenant, user, channel): Burst tokens,
// refilled continuously at Burst per Window. Burst <= 0 disables it.
type RateLimit struct {
	Burst  int
	Window time.Duration
}

// BreakerConfig configures the per-provider circuit breaker: the breaker
// opens after FailureThreshold consecutive failures and lets one probe
// through once Cooldown has passed. FailureThreshold <= 0 disables breakers.
type BreakerConfig struct {
	FailureThreshold int
	Cooldown         time.Duration
}

// Config configures a Dispatcher. Now is required; everything else has a
// default or may be left empty.
type Config struct {
	Now         func() time.Time              // the clock; tests inject a fake one
	Providers   map[Channel][]Provider        // per channel, in failover order
	Preferences Preferences                   // nil: nobody is opted out, no quiet hours
	RateLimit   RateLimit                     // zero: unlimited
	Breaker     BreakerConfig                 // zero: no breakers
	MaxAttempts int                           // provider calls per notification before dead-lettering; default DefaultMaxAttempts
	Backoff     func(retry int) time.Duration // delay before retry number retry (1-based); default DefaultBackoff
	SendTimeout time.Duration                 // per provider call; default DefaultSendTimeout
}

// Defaults.
const (
	DefaultMaxAttempts = 3
	DefaultSendTimeout = 2 * time.Second
	MaxBackoff         = 5 * time.Minute
)

// DefaultBackoff is exponential: 1s, 2s, 4s, ... capped at MaxBackoff.
// Production code wraps it with jitter (A7 Unit 1 §2); the lab keeps it
// deterministic so tests can reason about due times.
func DefaultBackoff(retry int) time.Duration {
	if retry < 1 {
		retry = 1
	}
	if retry-1 >= 20 {
		return MaxBackoff
	}
	d := time.Second << uint(retry-1)
	if d > MaxBackoff {
		return MaxBackoff
	}
	return d
}

// item is a queued notification.
type item struct {
	n        Notification
	seq      uint64    // arrival order, for FIFO among equal due times
	dueAt    time.Time // not before this time
	retries  int       // passes that ended without delivery
	attempts []Attempt
}

// itemHeap orders items by due time, then arrival. Provided.
type itemHeap []*item

func (h itemHeap) Len() int { return len(h) }
func (h itemHeap) Less(i, j int) bool {
	if !h[i].dueAt.Equal(h[j].dueAt) {
		return h[i].dueAt.Before(h[j].dueAt)
	}
	return h[i].seq < h[j].seq
}
func (h itemHeap) Swap(i, j int) { h[i], h[j] = h[j], h[i] }
func (h *itemHeap) Push(x any)   { *h = append(*h, x.(*item)) }
func (h *itemHeap) Pop() any {
	old := *h
	n := len(old)
	it := old[n-1]
	old[n-1] = nil
	*h = old[:n-1]
	return it
}

// rateKey is the token-bucket key for a notification. Provided.
func rateKey(n Notification) string {
	return n.TenantID + "/" + n.UserID + "/" + string(n.Channel)
}

// Dispatcher is safe for concurrent use: any number of goroutines may call
// Enqueue while others call Process. The mutex covers the queues, the
// dedupe set and the log; it is never held during a provider call, so a
// slow provider never blocks Enqueue.
type Dispatcher struct {
	cfg Config

	mu       sync.Mutex
	seen     map[string]struct{}
	seq      uint64
	urgent   itemHeap
	normal   itemHeap
	log      []Record
	dead     []Record
	limiter  *limiter
	breakers map[string]*breaker
}

// New validates cfg, applies defaults and builds one breaker per provider
// name (a provider listed under two channels shares one breaker: its
// health is a property of the vendor, not of the channel).
func New(cfg Config) (*Dispatcher, error) {
	if cfg.Now == nil {
		return nil, fmt.Errorf("%w: Now is required", ErrConfig)
	}
	if cfg.RateLimit.Burst > 0 && cfg.RateLimit.Window <= 0 {
		return nil, fmt.Errorf("%w: RateLimit.Window must be > 0 when Burst is set", ErrConfig)
	}
	if cfg.Breaker.FailureThreshold > 0 && cfg.Breaker.Cooldown <= 0 {
		return nil, fmt.Errorf("%w: Breaker.Cooldown must be > 0 when FailureThreshold is set", ErrConfig)
	}
	if cfg.MaxAttempts <= 0 {
		cfg.MaxAttempts = DefaultMaxAttempts
	}
	if cfg.SendTimeout <= 0 {
		cfg.SendTimeout = DefaultSendTimeout
	}
	if cfg.Backoff == nil {
		cfg.Backoff = DefaultBackoff
	}
	d := &Dispatcher{cfg: cfg, seen: map[string]struct{}{}, breakers: map[string]*breaker{}}
	for ch, provs := range cfg.Providers {
		for _, p := range provs {
			if p == nil || p.Name() == "" {
				return nil, fmt.Errorf("%w: provider for channel %q has no name", ErrConfig, ch)
			}
			if _, ok := d.breakers[p.Name()]; !ok {
				d.breakers[p.Name()] = &breaker{threshold: cfg.Breaker.FailureThreshold, cooldown: cfg.Breaker.Cooldown}
			}
		}
	}
	if cfg.RateLimit.Burst > 0 {
		d.limiter = &limiter{burst: cfg.RateLimit.Burst, window: cfg.RateLimit.Window, buckets: map[string]*bucket{}}
	}
	return d, nil
}

// Enqueue accepts n for delivery at the current time. It returns
// ErrInvalid for a malformed notification and ErrDuplicate when n.ID was
// already accepted (even if it has since been delivered or dead-lettered):
// the check and the insert are one atomic step, so concurrent duplicates
// are accepted exactly once.
func (d *Dispatcher) Enqueue(n Notification) error {
	if n.ID == "" || n.UserID == "" || n.Channel == "" || (n.Priority != Normal && n.Priority != Urgent) {
		return ErrInvalid
	}
	now := d.cfg.Now()
	d.mu.Lock()
	defer d.mu.Unlock()
	if _, dup := d.seen[n.ID]; dup {
		return ErrDuplicate
	}
	d.seen[n.ID] = struct{}{}
	d.seq++
	d.push(&item{n: n, seq: d.seq, dueAt: now})
	return nil
}

// push adds it to its priority's heap. Caller holds d.mu.
func (d *Dispatcher) push(it *item) {
	if it.n.Priority == Urgent {
		heap.Push(&d.urgent, it)
	} else {
		heap.Push(&d.normal, it)
	}
}

// popDue removes and returns the next notification due at now: the oldest
// due Urgent one, else the oldest due Normal one.
func (d *Dispatcher) popDue(now time.Time) (*item, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, q := range []*itemHeap{&d.urgent, &d.normal} {
		if q.Len() > 0 && !(*q)[0].dueAt.After(now) {
			return heap.Pop(q).(*item), true
		}
	}
	return nil, false
}

// Process handles every notification that is due at the current time, in
// priority order (Urgent first, then Normal; FIFO within a lane), and
// returns how many it handled. A handled notification ends up delivered,
// dropped with a recorded reason, deferred, requeued for a later retry, or
// dead-lettered. Process never waits for time to pass: anything due in the
// future stays queued. It stops early when ctx is done.
func (d *Dispatcher) Process(ctx context.Context) int {
	handled := 0
	for ctx.Err() == nil {
		now := d.cfg.Now()
		it, ok := d.popDue(now)
		if !ok {
			break
		}
		handled++
		d.handle(ctx, it, now)
	}
	return handled
}

// handle applies the suppression rules and then the provider chain.
func (d *Dispatcher) handle(ctx context.Context, it *item, now time.Time) {
	n := it.n
	if n.Priority == Normal {
		if p := d.cfg.Preferences; p != nil {
			if p.OptedOut(n.TenantID, n.UserID, n.Channel) {
				d.record(Record{Notification: n, Outcome: OptedOut, Reason: "opted out of " + string(n.Channel), Attempts: it.attempts, At: now})
				return
			}
			if quiet, until := p.Quiet(n.TenantID, n.UserID, now); quiet && until.After(now) {
				it.dueAt = until
				d.record(Record{Notification: n, Outcome: Deferred, Reason: "quiet hours until " + until.Format(time.RFC3339), Attempts: it.attempts, At: now, NextAt: until})
				d.requeue(it)
				return
			}
		}
		if d.limiter != nil && !d.limiter.allow(rateKey(n), now) {
			d.record(Record{Notification: n, Outcome: Suppressed, Reason: fmt.Sprintf("rate limit: %d per %s", d.cfg.RateLimit.Burst, d.cfg.RateLimit.Window), Attempts: it.attempts, At: now})
			return
		}
	}

	provs := d.cfg.Providers[n.Channel]
	if len(provs) == 0 {
		d.deadLetter(it, now, "no provider for channel "+string(n.Channel))
		return
	}
	var earliest time.Time // when the first open breaker may close
	called := false
	for _, p := range provs {
		if len(it.attempts) >= d.cfg.MaxAttempts {
			break
		}
		b := d.breakers[p.Name()]
		if !b.allow(now) {
			if at, ok := b.availableAt(); ok && (earliest.IsZero() || at.Before(earliest)) {
				earliest = at
			}
			continue
		}
		called = true
		err := d.call(ctx, p, n)
		it.attempts = append(it.attempts, Attempt{At: now, Provider: p.Name(), Err: err})
		if err == nil {
			b.success()
			d.record(Record{Notification: n, Outcome: Delivered, Attempts: it.attempts, At: now})
			return
		}
		b.failure(now)
	}
	if len(it.attempts) >= d.cfg.MaxAttempts {
		d.deadLetter(it, now, fmt.Sprintf("%d attempts exhausted", len(it.attempts)))
		return
	}
	it.retries++
	due := now.Add(d.cfg.Backoff(it.retries))
	if !called && earliest.After(due) {
		due = earliest // nothing to retry against before a breaker can close
	}
	it.dueAt = due
	d.requeue(it)
}

// call invokes one provider with the per-call timeout and contains panics:
// a panicking vendor SDK is a failed attempt, not a dead worker.
func (d *Dispatcher) call(ctx context.Context, p Provider, n Notification) (err error) {
	cctx, cancel := context.WithTimeout(ctx, d.cfg.SendTimeout)
	defer cancel()
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("provider %s panicked: %v", p.Name(), r)
		}
	}()
	return p.Send(cctx, n)
}

func (d *Dispatcher) requeue(it *item) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.push(it)
}

func (d *Dispatcher) record(r Record) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.log = append(d.log, r)
}

func (d *Dispatcher) deadLetter(it *item, now time.Time, reason string) {
	r := Record{Notification: it.n, Outcome: Failed, Reason: reason, Attempts: it.attempts, At: now}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.log = append(d.log, r)
	d.dead = append(d.dead, r)
}

// Pending returns how many notifications are queued, including ones
// waiting for quiet hours to end or for a retry to become due.
func (d *Dispatcher) Pending() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.urgent.Len() + d.normal.Len()
}

// NextDue returns the earliest time at which a queued notification becomes
// due, and false when nothing is queued.
func (d *Dispatcher) NextDue() (time.Time, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	var t time.Time
	ok := false
	for _, q := range []*itemHeap{&d.urgent, &d.normal} {
		if q.Len() > 0 && (!ok || (*q)[0].dueAt.Before(t)) {
			t, ok = (*q)[0].dueAt, true
		}
	}
	return t, ok
}

// Log returns a copy of the delivery log, oldest first.
func (d *Dispatcher) Log() []Record {
	d.mu.Lock()
	defer d.mu.Unlock()
	return copyRecords(d.log)
}

// DeadLetters returns a copy of the dead-letter list, oldest first.
func (d *Dispatcher) DeadLetters() []Record {
	d.mu.Lock()
	defer d.mu.Unlock()
	return copyRecords(d.dead)
}

// Counts returns the number of log records per outcome.
func (d *Dispatcher) Counts() map[Outcome]int {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := map[Outcome]int{}
	for _, r := range d.log {
		out[r.Outcome]++
	}
	return out
}

func copyRecords(in []Record) []Record {
	out := make([]Record, len(in))
	for i, r := range in {
		r.Attempts = append([]Attempt(nil), r.Attempts...)
		out[i] = r
	}
	return out
}

// breaker is a per-provider circuit breaker evaluated lazily from the
// clock passed to allow and failure: closed → open after threshold
// consecutive failures → half-open after cooldown (one probe at a time) →
// closed on a successful probe, open again on a failed one.
type breaker struct {
	threshold int
	cooldown  time.Duration

	mu        sync.Mutex
	failures  int
	state     breakerState
	openUntil time.Time
	probing   bool // a half-open probe is in flight
}

type breakerState int

const (
	closed breakerState = iota
	open
	halfOpen
)

// allow reports whether a call may be made now. In the half-open state it
// admits exactly one probe at a time.
func (b *breaker) allow(now time.Time) bool {
	if b.threshold <= 0 {
		return true
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	switch b.state {
	case open:
		if now.Before(b.openUntil) {
			return false
		}
		b.state = halfOpen
		b.probing = true
		return true
	case halfOpen:
		if b.probing {
			return false
		}
		b.probing = true
		return true
	}
	return true
}

// availableAt returns when an open breaker may admit a probe (and true),
// or false when the breaker is closed or a probe is already in flight.
func (b *breaker) availableAt() (time.Time, bool) {
	if b.threshold <= 0 {
		return time.Time{}, false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.state == open {
		return b.openUntil, true
	}
	return time.Time{}, false
}

func (b *breaker) success() {
	if b.threshold <= 0 {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.state = closed
	b.failures = 0
	b.probing = false
}

func (b *breaker) failure(now time.Time) {
	if b.threshold <= 0 {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.state == halfOpen {
		b.state = open
		b.openUntil = now.Add(b.cooldown)
		b.probing = false
		return
	}
	b.failures++
	if b.failures >= b.threshold {
		b.state = open
		b.openUntil = now.Add(b.cooldown)
	}
}

// limiter is a map of token buckets keyed by rateKey.
type limiter struct {
	burst  int
	window time.Duration

	mu      sync.Mutex
	buckets map[string]*bucket
}

type bucket struct {
	tokens float64
	last   time.Time
}

// allow refills the key's bucket from the time elapsed since its last
// visit (never above burst), then takes one token if one is available.
func (l *limiter) allow(key string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	b, ok := l.buckets[key]
	if !ok {
		b = &bucket{tokens: float64(l.burst), last: now}
		l.buckets[key] = b
	}
	if elapsed := now.Sub(b.last); elapsed > 0 {
		b.tokens += float64(elapsed) / float64(l.window) * float64(l.burst)
		if b.tokens > float64(l.burst) {
			b.tokens = float64(l.burst)
		}
		b.last = now
	}
	if b.tokens >= 1-1e-9 {
		b.tokens--
		return true
	}
	return false
}
