// Package distlimit enforces one rate limit across a fleet, which is the
// hardest component of case study C2: forty gateways (or a dozen limiter
// service instances) must agree on how many requests a key has made, without
// adding more than a millisecond to each request, without one hot key melting
// the shared store, and without the limiter becoming the outage when that
// store is down.
//
// It is the lab for C2 Unit 1 §5–§7. Four pieces, in the order the tests
// expect you to build them:
//
//  1. MemoryStore: the shared counter store (Redis, DynamoDB, etcd) reduced
//     to the two operations a lock-free limiter needs, Load and an atomic
//     CompareAndSwap with a TTL, plus fault injection and operation counts.
//  2. Limiter: GCRA (one timestamp per key) over a Store. Every decision is
//     one Load and, when the request is admitted, one CompareAndSwap, retried
//     when another gateway moved first. Denials never write.
//  3. Guard: wraps any Decider with a timeout and a failure policy (fail
//     open, fail closed, or fall back to a node-local limit), so a slow or
//     dead store degrades protection instead of availability.
//  4. LeaseClient: takes tokens from the shared Limiter in batches and spends
//     them locally, caching denials, so a hot key costs the store one round
//     trip per batch instead of one per request. The price is a bounded
//     overshoot of at most nodes × batch tokens.
//
// Run: go test -race ./labs/c2/distlimit/
package distlimit

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sync"
	"sync/atomic"
	"time"
)

// Errors returned by this package. Wrap them; callers use errors.Is.
var (
	// ErrBadArgument is returned for an invalid Limit, a cost n <= 0, a cost
	// larger than the burst (it could never be admitted), or an invalid
	// configuration.
	ErrBadArgument = errors.New("distlimit: bad argument")
	// ErrContended is returned by Limiter when MaxRetries+1 compare-and-swap
	// attempts in a row lost to other writers of the same key.
	ErrContended = errors.New("distlimit: key contended")
	// ErrClosed is returned by LeaseClient.Decide after Close.
	ErrClosed = errors.New("distlimit: lease client closed")
)

// Defaults applied when a config field is zero.
const (
	DefaultMaxRetries       = 16
	DefaultTimeout          = 10 * time.Millisecond
	DefaultClosedRetryAfter = time.Second
	DefaultBatch            = 10
	DefaultLeaseTTL         = time.Second
)

// Clock is the time source. Tests inject a fake; production uses SystemClock.
type Clock interface {
	Now() time.Time
}

// SystemClock is the wall clock.
type SystemClock struct{}

// Now implements Clock.
func (SystemClock) Now() time.Time { return time.Now() }

// ---------------------------------------------------------------------------
// Limits and decisions (provided)
// ---------------------------------------------------------------------------

// Limit is a GCRA / token-bucket limit: a sustained Rate (requests per
// second) and a Burst (how many may arrive at once after the key has been
// idle). In any span of length W at most Burst + Rate×W requests are
// admitted.
type Limit struct {
	Rate  float64
	Burst int
}

// Interval is the emission interval T = 1/Rate: the time one request "costs".
func (l Limit) Interval() time.Duration {
	if !(l.Rate > 0) || math.IsInf(l.Rate, 0) {
		return 0
	}
	return time.Duration(float64(time.Second) / l.Rate)
}

// Valid reports whether the limit can be enforced: Rate > 0 with an interval
// of at least 1 ns, Burst >= 1, and Burst × Interval small enough not to
// overflow.
func (l Limit) Valid() bool {
	t := l.Interval()
	return t > 0 && l.Burst >= 1 && int64(t) <= math.MaxInt64/4/int64(l.Burst)
}

// Source says which path produced a Decision. Log it: "which limiter denied
// this, and was it degraded?" is the first support question.
type Source string

// The decision sources.
const (
	SourceStore      Source = "store"       // decided against the shared store
	SourceLease      Source = "lease"       // spent from tokens this node already held
	SourceDenyCache  Source = "deny-cache"  // denied locally, remembering the store's last denial
	SourceFallback   Source = "fallback"    // store unavailable: node-local limit
	SourceFailOpen   Source = "fail-open"   // store unavailable: admitted
	SourceFailClosed Source = "fail-closed" // store unavailable: denied
)

// Decision is the outcome of one Decide call.
type Decision struct {
	Allowed bool
	// Remaining is how many more requests of cost 1 could be admitted right
	// now (never negative). For a LeaseClient it is the tokens this node
	// still holds for the key.
	Remaining int
	// RetryAfter is, for a denial, how long until a request of the same cost
	// could be admitted. Zero when allowed.
	RetryAfter time.Duration
	// ResetAfter is how long until the key's bucket is full again, which is
	// also the TTL of its state in the store. Zero when unknown.
	ResetAfter time.Duration
	Source     Source
}

// Decider decides whether n units of cost for key fit within limit.
// *Limiter, *Guard and *LeaseClient all implement it, so they compose:
// Guard{Primary: LeaseClient{Pool: Limiter{Store: ...}}}.
type Decider interface {
	Decide(ctx context.Context, key string, limit Limit, n int) (Decision, error)
}

func badArg(format string, args ...any) error {
	return fmt.Errorf("%w: "+format, append([]any{ErrBadArgument}, args...)...)
}

func checkRequest(limit Limit, n int) error {
	if !limit.Valid() {
		return badArg("invalid limit %+v", limit)
	}
	if n <= 0 || n > limit.Burst {
		return badArg("cost %d outside 1..burst (%d)", n, limit.Burst)
	}
	return nil
}

// ---------------------------------------------------------------------------
// 1. The shared store
// ---------------------------------------------------------------------------

// Store is the shared counter store reduced to what a lock-free limiter
// needs. A value of 0 means "absent": no key ever stores 0.
//
// Implementations must make CompareAndSwap atomic: of N concurrent calls with
// the same old value, exactly one succeeds. In Redis that is a Lua script or
// WATCH/MULTI; in DynamoDB a conditional write; in etcd a transaction on the
// key's revision.
type Store interface {
	// Load returns the value at key, or 0 if the key is absent or expired.
	Load(ctx context.Context, key string) (int64, error)
	// CompareAndSwap sets key to new, expiring after ttl (ttl <= 0: never),
	// if and only if its current value is old (0 = absent or expired). new
	// == 0 deletes the key. It reports whether the swap happened.
	CompareAndSwap(ctx context.Context, key string, old, new int64, ttl time.Duration) (bool, error)
}

type memEntry struct {
	v       int64
	expires time.Time // zero: never
}

// MemoryStore is an in-process Store with TTLs on the store's own clock,
// fault injection and an operation count. Safe for concurrent use.
type MemoryStore struct {
	clock Clock
	ops   atomic.Int64

	mu   sync.Mutex
	m    map[string]memEntry
	fail error
}

// NewMemoryStore returns an empty store whose TTLs run on clock (nil:
// SystemClock).
func NewMemoryStore(clock Clock) *MemoryStore {
	if clock == nil {
		clock = SystemClock{}
	}
	return &MemoryStore{clock: clock, m: make(map[string]memEntry)}
}

// live returns the unexpired entry for key, deleting it if it has expired.
// Callers hold s.mu.
func (s *MemoryStore) live(key string, now time.Time) (memEntry, bool) {
	e, ok := s.m[key]
	if !ok {
		return memEntry{}, false
	}
	if !e.expires.IsZero() && !now.Before(e.expires) {
		delete(s.m, key)
		return memEntry{}, false
	}
	return e, true
}

// Load implements Store.
func (s *MemoryStore) Load(ctx context.Context, key string) (int64, error) {
	s.ops.Add(1)
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fail != nil {
		return 0, s.fail
	}
	e, _ := s.live(key, s.clock.Now())
	return e.v, nil
}

// CompareAndSwap implements Store. The comparison and the write happen under
// one lock acquisition.
func (s *MemoryStore) CompareAndSwap(ctx context.Context, key string, old, new int64, ttl time.Duration) (bool, error) {
	s.ops.Add(1)
	if err := ctx.Err(); err != nil {
		return false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fail != nil {
		return false, s.fail
	}
	now := s.clock.Now()
	cur, _ := s.live(key, now)
	if cur.v != old {
		return false, nil
	}
	if new == 0 {
		delete(s.m, key)
		return true, nil
	}
	e := memEntry{v: new}
	if ttl > 0 {
		e.expires = now.Add(ttl)
	}
	s.m[key] = e
	return true, nil
}

// Fail makes every later Load and CompareAndSwap return err; Fail(nil)
// restores normal operation.
func (s *MemoryStore) Fail(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.fail = err
}

// Ops reports how many Load and CompareAndSwap calls the store has received,
// including failed ones.
func (s *MemoryStore) Ops() int64 { return s.ops.Load() }

// Len reports the number of unexpired keys.
func (s *MemoryStore) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.clock.Now()
	n := 0
	for k := range s.m {
		if _, ok := s.live(k, now); ok {
			n++
		}
	}
	return n
}

// ---------------------------------------------------------------------------
// 2. GCRA over the shared store
// ---------------------------------------------------------------------------

// LimiterConfig configures a Limiter.
type LimiterConfig struct {
	Store Store // required
	Clock Clock // nil: SystemClock
	// MaxRetries is how many times a lost compare-and-swap is retried before
	// Decide gives up with ErrContended. 0: DefaultMaxRetries; negative is
	// invalid.
	MaxRetries int
}

// Limiter enforces limits with GCRA, keeping one int64 per key in a shared
// Store: the theoretical arrival time (TAT, Unix nanoseconds) of the next
// request. Safe for concurrent use, and any number of Limiters (one per
// gateway) may share one Store.
type Limiter struct {
	store      Store
	clock      Clock
	maxRetries int
}

// NewLimiter returns a Limiter. ErrBadArgument if Store is nil or MaxRetries
// is negative.
func NewLimiter(cfg LimiterConfig) (*Limiter, error) {
	if cfg.Store == nil {
		return nil, badArg("nil Store")
	}
	if cfg.MaxRetries < 0 {
		return nil, badArg("MaxRetries %d < 0", cfg.MaxRetries)
	}
	if cfg.Clock == nil {
		cfg.Clock = SystemClock{}
	}
	if cfg.MaxRetries == 0 {
		cfg.MaxRetries = DefaultMaxRetries
	}
	return &Limiter{store: cfg.Store, clock: cfg.Clock, maxRetries: cfg.MaxRetries}, nil
}

// Decide implements Decider with GCRA. With T = limit.Interval() and
// tau = Burst × T, at time now:
//
//	tat    = max(stored, now)          // a TAT in the past means "full"
//	newTAT = tat + n×T
//	admit iff newTAT − now <= tau
//
// An admitted request stores newTAT with TTL newTAT − now (the key vanishes
// exactly when its bucket is full again) using CompareAndSwap against the
// value it loaded, and retries from the Load if another writer moved first.
// A denied request writes nothing; its RetryAfter is newTAT − tau − now.
// Remaining is floor((tau − (TAT − now)) / T) for the TAT after the decision.
//
// Errors: ErrBadArgument (invalid limit, n <= 0, n > Burst), ctx.Err() if the
// context is done before an attempt, the store's error (wrapped), or
// ErrContended after MaxRetries+1 lost swaps.
func (l *Limiter) Decide(ctx context.Context, key string, limit Limit, n int) (Decision, error) {
	if err := checkRequest(limit, n); err != nil {
		return Decision{}, err
	}
	t := int64(limit.Interval())
	tau := int64(limit.Burst) * t
	for attempt := 0; attempt <= l.maxRetries; attempt++ {
		if err := ctx.Err(); err != nil {
			return Decision{}, err
		}
		stored, err := l.store.Load(ctx, key)
		if err != nil {
			return Decision{}, fmt.Errorf("distlimit: load %q: %w", key, err)
		}
		now := l.clock.Now().UnixNano()
		tat := max(stored, now)
		newTAT := tat + int64(n)*t
		if newTAT-now > tau {
			return Decision{
				Remaining:  int(max(0, (tau-(tat-now))/t)),
				RetryAfter: time.Duration(newTAT - tau - now),
				ResetAfter: time.Duration(tat - now),
				Source:     SourceStore,
			}, nil
		}
		ok, err := l.store.CompareAndSwap(ctx, key, stored, newTAT, time.Duration(newTAT-now))
		if err != nil {
			return Decision{}, fmt.Errorf("distlimit: compare-and-swap %q: %w", key, err)
		}
		if ok {
			return Decision{
				Allowed:    true,
				Remaining:  int((tau - (newTAT - now)) / t),
				ResetAfter: time.Duration(newTAT - now),
				Source:     SourceStore,
			}, nil
		}
	}
	return Decision{}, fmt.Errorf("%w: %q lost %d compare-and-swaps in a row", ErrContended, key, l.maxRetries+1)
}

// Refund gives back n tokens taken earlier (a LeaseClient returning what it
// did not spend): TAT moves back by n×T, but never below now, so a refund can
// at most refill the bucket, never overfill it. When the bucket ends up full
// the key is deleted. A key that is absent or already full needs no refund.
// Errors as for Decide (n <= 0 is ErrBadArgument; n larger than Burst is
// treated as Burst).
func (l *Limiter) Refund(ctx context.Context, key string, limit Limit, n int) error {
	if !limit.Valid() {
		return badArg("invalid limit %+v", limit)
	}
	if n <= 0 {
		return badArg("refund of %d tokens", n)
	}
	n = min(n, limit.Burst)
	t := int64(limit.Interval())
	for attempt := 0; attempt <= l.maxRetries; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		stored, err := l.store.Load(ctx, key)
		if err != nil {
			return fmt.Errorf("distlimit: load %q: %w", key, err)
		}
		now := l.clock.Now().UnixNano()
		if stored <= now {
			return nil
		}
		newTAT, ttl := stored-int64(n)*t, time.Duration(0)
		if newTAT <= now {
			newTAT = 0
		} else {
			ttl = time.Duration(newTAT - now)
		}
		ok, err := l.store.CompareAndSwap(ctx, key, stored, newTAT, ttl)
		if err != nil {
			return fmt.Errorf("distlimit: compare-and-swap %q: %w", key, err)
		}
		if ok {
			return nil
		}
	}
	return fmt.Errorf("%w: refund %q lost %d compare-and-swaps in a row", ErrContended, key, l.maxRetries+1)
}

// ---------------------------------------------------------------------------
// 3. Guard: timeouts and the failure policy
// ---------------------------------------------------------------------------

// FailMode is what a Guard does when its primary Decider cannot answer.
type FailMode int

// The failure policies.
const (
	// FailOpen admits the request. For protective limits: the API stays up,
	// protection is gone until the store is back.
	FailOpen FailMode = iota
	// FailClosed denies the request. For security limits (login attempts,
	// OTP sends), where an outage must not become a brute-force window.
	FailClosed
	// FailLocal decides with a node-local Fallback at a scaled limit
	// (typically 2 × the node's fair share): degraded accuracy, bounded
	// damage.
	FailLocal
)

// GuardConfig configures a Guard.
type GuardConfig struct {
	Primary  Decider // required: the shared limiter (or a LeaseClient over it)
	Fallback Decider // required for FailLocal: a node-local limiter
	Mode     FailMode
	// Timeout bounds each Primary call. 0: DefaultTimeout; negative is
	// invalid.
	Timeout time.Duration
	// FallbackScale multiplies the limit for the Fallback: Rate × scale, and
	// Burst × scale rounded up (at least 1). 0: 1; negative is invalid.
	FallbackScale float64
	// ClosedRetryAfter is the RetryAfter of a FailClosed denial (and of a
	// FailLocal denial for a cost the scaled burst cannot hold).
	// 0: DefaultClosedRetryAfter; negative is invalid.
	ClosedRetryAfter time.Duration
}

// Guard makes the limiter's dependency on its store explicit: every Primary
// call is bounded by Timeout, and when the Primary fails (store error,
// timeout, contention) the Mode decides. Safe for concurrent use.
type Guard struct {
	cfg      GuardConfig
	degraded atomic.Int64
}

// NewGuard returns a Guard. ErrBadArgument for a nil Primary, an unknown
// Mode, FailLocal without a Fallback, or a negative duration or scale.
func NewGuard(cfg GuardConfig) (*Guard, error) {
	if cfg.Primary == nil {
		return nil, badArg("nil Primary")
	}
	switch cfg.Mode {
	case FailOpen, FailClosed:
	case FailLocal:
		if cfg.Fallback == nil {
			return nil, badArg("FailLocal needs a Fallback")
		}
	default:
		return nil, badArg("unknown FailMode %d", cfg.Mode)
	}
	if cfg.Timeout < 0 || cfg.FallbackScale < 0 || cfg.ClosedRetryAfter < 0 {
		return nil, badArg("negative Timeout, FallbackScale or ClosedRetryAfter")
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = DefaultTimeout
	}
	if cfg.FallbackScale == 0 {
		cfg.FallbackScale = 1
	}
	if cfg.ClosedRetryAfter == 0 {
		cfg.ClosedRetryAfter = DefaultClosedRetryAfter
	}
	return &Guard{cfg: cfg}, nil
}

// Decide implements Decider. It calls Primary with a context bounded by
// Timeout and returns its answer when it has one. Two errors pass through
// untouched because they are not outages: ErrBadArgument (the caller's bug)
// and the caller's own context being done (the caller is gone; admitting or
// denying on its behalf would be wrong). Every other error applies the Mode,
// counts one degraded decision, and returns a nil error:
//
//	FailOpen    Allowed, Source fail-open
//	FailClosed  denied, RetryAfter = ClosedRetryAfter, Source fail-closed
//	FailLocal   Fallback.Decide at the scaled limit, Source fallback
//	            (a cost larger than the scaled burst is denied with
//	            ClosedRetryAfter; a Fallback error is returned, wrapped)
func (g *Guard) Decide(ctx context.Context, key string, limit Limit, n int) (Decision, error) {
	if err := ctx.Err(); err != nil {
		return Decision{}, err
	}
	pctx, cancel := context.WithTimeout(ctx, g.cfg.Timeout)
	d, err := g.cfg.Primary.Decide(pctx, key, limit, n)
	cancel()
	if err == nil {
		return d, nil
	}
	if errors.Is(err, ErrBadArgument) {
		return Decision{}, err
	}
	if cerr := ctx.Err(); cerr != nil {
		return Decision{}, cerr
	}
	g.degraded.Add(1)
	switch g.cfg.Mode {
	case FailOpen:
		return Decision{Allowed: true, Source: SourceFailOpen}, nil
	case FailClosed:
		return Decision{RetryAfter: g.cfg.ClosedRetryAfter, Source: SourceFailClosed}, nil
	}
	scaled := Limit{
		Rate:  limit.Rate * g.cfg.FallbackScale,
		Burst: max(1, int(math.Ceil(float64(limit.Burst)*g.cfg.FallbackScale))),
	}
	if n > scaled.Burst {
		return Decision{RetryAfter: g.cfg.ClosedRetryAfter, Source: SourceFallback}, nil
	}
	fd, ferr := g.cfg.Fallback.Decide(ctx, key, scaled, n)
	if ferr != nil {
		return Decision{}, fmt.Errorf("distlimit: fallback after primary error (%v): %w", err, ferr)
	}
	fd.Source = SourceFallback
	return fd, nil
}

// Degraded reports how many decisions did not come from the Primary.
func (g *Guard) Degraded() int64 { return g.degraded.Load() }

// ---------------------------------------------------------------------------
// 4. LeaseClient: batched tokens for hot keys
// ---------------------------------------------------------------------------

// Pool is where a LeaseClient takes tokens from and returns unspent ones to.
// *Limiter implements it.
type Pool interface {
	Decider
	Refund(ctx context.Context, key string, limit Limit, n int) error
}

// LeaseConfig configures a LeaseClient.
type LeaseConfig struct {
	Pool  Pool  // required: the shared Limiter
	Clock Clock // nil: SystemClock
	// Batch is how many tokens one lease asks for. 0: DefaultBatch;
	// negative is invalid.
	Batch int
	// LeaseTTL is how long leased tokens may be held before they are
	// dropped. 0: DefaultLeaseTTL; negative is invalid.
	LeaseTTL time.Duration
}

type leaseState struct {
	mu        sync.Mutex
	held      int
	expires   time.Time
	denyUntil time.Time
	limit     Limit
}

// LeaseClient is one node's view of the shared limiter for hot keys. It
// takes tokens from the Pool a batch at a time and spends them locally, so
// the store sees about one Decide per Batch admitted requests, and it caches
// the Pool's denials until a batch could be available again, so a key that
// is over its limit does not cost the store a round trip per request either.
// Lease only hot keys: for a key a node sees a few times per LeaseTTL, the
// batch expires unspent and the extra wait (Batch × T) is large. Safe for
// concurrent use.
//
// Accuracy: after every decision a node holds fewer than Batch tokens per
// key, so in any span of length W a fleet of N nodes admits at most
// Burst + Rate×W + N×Batch requests for one key. Tokens that expire unspent
// are dropped (the fleet under-admits slightly), never handed back late.
type LeaseClient struct {
	pool  Pool
	clock Clock
	batch int
	ttl   time.Duration

	mu     sync.Mutex
	keys   map[string]*leaseState
	closed bool
}

// NewLeaseClient returns a LeaseClient. ErrBadArgument for a nil Pool or a
// negative Batch or LeaseTTL.
func NewLeaseClient(cfg LeaseConfig) (*LeaseClient, error) {
	if cfg.Pool == nil {
		return nil, badArg("nil Pool")
	}
	if cfg.Batch < 0 || cfg.LeaseTTL < 0 {
		return nil, badArg("negative Batch or LeaseTTL")
	}
	if cfg.Clock == nil {
		cfg.Clock = SystemClock{}
	}
	if cfg.Batch == 0 {
		cfg.Batch = DefaultBatch
	}
	if cfg.LeaseTTL == 0 {
		cfg.LeaseTTL = DefaultLeaseTTL
	}
	return &LeaseClient{
		pool: cfg.Pool, clock: cfg.Clock, batch: cfg.Batch, ttl: cfg.LeaseTTL,
		keys: make(map[string]*leaseState),
	}, nil
}

func (c *LeaseClient) state(key string) (*leaseState, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil, ErrClosed
	}
	s := c.keys[key]
	if s == nil {
		s = &leaseState{}
		c.keys[key] = s
	}
	return s, nil
}

// Decide implements Decider. Per key, under that key's own lock (so one
// caller refills while the others for the same key wait, and other keys are
// not blocked):
//
//  1. Held tokens past their LeaseTTL are dropped.
//  2. If the node holds >= n tokens: spend them. Source lease, no store call.
//  3. Else, if the last Pool denial's RetryAfter has not passed: deny with
//     the remaining wait. Source deny-cache, no store call.
//  4. Else lease: need = n − held, want = min(max(Batch, need), Burst).
//     Ask the Pool for want. If it denies but its Remaining >= need, ask
//     once more for exactly Remaining (take what is left).
//     Admitted: held += want, the lease expires after LeaseTTL, spend n.
//     Denied: remember the denial until now + the Pool's RetryAfter (the
//     wait for the whole `want`, at least one interval T) and deny with
//     that RetryAfter. Waiting for a batch rather than for one token is
//     what keeps a key over its limit from costing the store a round trip
//     per refilled token; the price is up to Batch × T of extra wait.
//     Source store either way.
//
// Errors: ErrBadArgument (invalid limit, n <= 0, n > Burst), ErrClosed, or
// the Pool's error unchanged (wrap the client in a Guard for a failure
// policy).
func (c *LeaseClient) Decide(ctx context.Context, key string, limit Limit, n int) (Decision, error) {
	if err := checkRequest(limit, n); err != nil {
		return Decision{}, err
	}
	s, err := c.state(key)
	if err != nil {
		return Decision{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := c.clock.Now()
	if !now.Before(s.expires) {
		s.held = 0
	}
	if s.held >= n {
		s.held -= n
		return Decision{Allowed: true, Remaining: s.held, Source: SourceLease}, nil
	}
	if now.Before(s.denyUntil) {
		return Decision{RetryAfter: s.denyUntil.Sub(now), Source: SourceDenyCache}, nil
	}
	need := n - s.held
	want := min(max(c.batch, need), limit.Burst)
	d, err := c.pool.Decide(ctx, key, limit, want)
	if err != nil {
		return Decision{}, err
	}
	if !d.Allowed && d.Remaining >= need {
		want = d.Remaining
		if d, err = c.pool.Decide(ctx, key, limit, want); err != nil {
			return Decision{}, err
		}
	}
	if !d.Allowed {
		retry := d.RetryAfter
		if retry <= 0 {
			retry = limit.Interval()
		}
		s.denyUntil = now.Add(retry)
		return Decision{RetryAfter: retry, Source: SourceStore}, nil
	}
	s.held += want - n
	s.expires = now.Add(c.ttl)
	s.limit = limit
	return Decision{Allowed: true, Remaining: s.held, ResetAfter: d.ResetAfter, Source: SourceStore}, nil
}

// Held reports the unexpired tokens this node holds, summed over keys.
func (c *LeaseClient) Held() int {
	c.mu.Lock()
	states := make([]*leaseState, 0, len(c.keys))
	for _, s := range c.keys {
		states = append(states, s)
	}
	c.mu.Unlock()
	now := c.clock.Now()
	total := 0
	for _, s := range states {
		s.mu.Lock()
		if now.Before(s.expires) {
			total += s.held
		}
		s.mu.Unlock()
	}
	return total
}

// Close refunds every unexpired held token to the Pool (a draining node
// gives its share back instead of stranding it until the lease expires) and
// makes later Decide calls return ErrClosed. Refund errors are joined and
// returned; the tokens are forgotten either way. Closing twice is a no-op.
func (c *LeaseClient) Close(ctx context.Context) error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	keys := c.keys
	c.keys = make(map[string]*leaseState)
	c.mu.Unlock()

	now := c.clock.Now()
	var errs []error
	for key, s := range keys {
		s.mu.Lock()
		held, limit, live := s.held, s.limit, now.Before(s.expires)
		s.held = 0
		s.mu.Unlock()
		if held > 0 && live {
			if err := c.pool.Refund(ctx, key, limit, held); err != nil {
				errs = append(errs, fmt.Errorf("refund %q: %w", key, err))
			}
		}
	}
	return errors.Join(errs...)
}
