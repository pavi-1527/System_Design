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
// Provided: the errors and defaults, Clock, Limit (Interval, Valid), Source,
// Decision, the Decider/Store/Pool interfaces, the config types, FailMode and
// the checkRequest/badArg helpers. Implement the rest; each step's tests run
// alone with -run (see labs/c2/README.md).
//
// Run: go test -race ./labs/c2/distlimit/
package distlimit

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sync"
	"time"
)

var errNotImplemented = errors.New("distlimit: not implemented")

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

// MemoryStore is an in-process Store with TTLs on the store's own clock,
// fault injection and an operation count. Safe for concurrent use.
type MemoryStore struct {
	// TODO(lab): the clock, an atomic op count, and under a mutex the map of
	// entries (value + expiry time) and the injected error.
	mu sync.Mutex
}

// NewMemoryStore returns an empty store whose TTLs run on clock (nil:
// SystemClock).
func NewMemoryStore(clock Clock) *MemoryStore {
	// TODO(lab): default a nil clock to SystemClock{}; make the map.
	_ = clock
	return &MemoryStore{}
}

// Load implements Store.
func (s *MemoryStore) Load(ctx context.Context, key string) (int64, error) {
	// TODO(lab): count the call first (failed calls count too), honour ctx,
	// return the injected error, and treat an expired entry as absent (0).
	_, _ = ctx, key
	return 0, errNotImplemented
}

// CompareAndSwap implements Store. The comparison and the write happen under
// one lock acquisition.
func (s *MemoryStore) CompareAndSwap(ctx context.Context, key string, old, new int64, ttl time.Duration) (bool, error) {
	// TODO(lab): count, honour ctx, injected error; then under ONE lock
	// acquisition: current value (0 if absent or expired) == old ? write
	// (new == 0 deletes; ttl > 0 sets expiry at clock.Now()+ttl) : false.
	_, _, _, _, _ = ctx, key, old, new, ttl
	return false, errNotImplemented
}

// Fail makes every later Load and CompareAndSwap return err; Fail(nil)
// restores normal operation.
func (s *MemoryStore) Fail(err error) {
	// TODO(lab)
	_ = err
}

// Ops reports how many Load and CompareAndSwap calls the store has received,
// including failed ones.
func (s *MemoryStore) Ops() int64 {
	// TODO(lab)
	return 0
}

// Len reports the number of unexpired keys.
func (s *MemoryStore) Len() int {
	// TODO(lab): count only unexpired keys.
	return 0
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
	// TODO(lab): store, clock, maxRetries.
}

// NewLimiter returns a Limiter. ErrBadArgument if Store is nil or MaxRetries
// is negative.
func NewLimiter(cfg LimiterConfig) (*Limiter, error) {
	// TODO(lab): validate, apply the defaults.
	_ = cfg
	return nil, errNotImplemented
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
	// TODO(lab): checkRequest; then up to 1+MaxRetries attempts of
	// ctx check → Load → GCRA arithmetic in int64 nanoseconds → deny (no
	// write) or CompareAndSwap(stored, newTAT, ttl). Note: CompareAndSwap
	// compares against the value you LOADED, not the clamped tat.
	_, _, _, _ = ctx, key, limit, n
	return Decision{}, errNotImplemented
}

// Refund gives back n tokens taken earlier (a LeaseClient returning what it
// did not spend): TAT moves back by n×T, but never below now, so a refund can
// at most refill the bucket, never overfill it. When the bucket ends up full
// the key is deleted. A key that is absent or already full needs no refund.
// Errors as for Decide (n <= 0 is ErrBadArgument; n larger than Burst is
// treated as Burst).
func (l *Limiter) Refund(ctx context.Context, key string, limit Limit, n int) error {
	// TODO(lab): the same loop; TAT moves back by n×T but never below now
	// (delete the key with new == 0 when the bucket would be full).
	_, _, _, _ = ctx, key, limit, n
	return errNotImplemented
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
	// TODO(lab): the config (with defaults applied) and an atomic counter
	// of degraded decisions.
}

// NewGuard returns a Guard. ErrBadArgument for a nil Primary, an unknown
// Mode, FailLocal without a Fallback, or a negative duration or scale.
func NewGuard(cfg GuardConfig) (*Guard, error) {
	// TODO(lab): validate, apply the defaults.
	_ = cfg
	return nil, errNotImplemented
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
	// TODO(lab): context.WithTimeout around the Primary call; then decide
	// which errors are outages (everything except ErrBadArgument and the
	// CALLER's context being done) and apply the Mode.
	_, _, _, _ = ctx, key, limit, n
	return Decision{}, errNotImplemented
}

// Degraded reports how many decisions did not come from the Primary.
func (g *Guard) Degraded() int64 {
	// TODO(lab)
	return 0
}

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
	// TODO(lab): pool, clock, batch, ttl; a mutex guarding a map of per-key
	// state and the closed flag. Each per-key state has its own mutex.
	mu sync.Mutex
}

// NewLeaseClient returns a LeaseClient. ErrBadArgument for a nil Pool or a
// negative Batch or LeaseTTL.
func NewLeaseClient(cfg LeaseConfig) (*LeaseClient, error) {
	// TODO(lab): validate, apply the defaults.
	_ = cfg
	return nil, errNotImplemented
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
	// TODO(lab): per-key state with its own mutex (held, expires, denyUntil,
	// the limit for refunds); the client-wide mutex only guards the map.
	_, _, _, _ = ctx, key, limit, n
	return Decision{}, errNotImplemented
}

// Held reports the unexpired tokens this node holds, summed over keys.
func (c *LeaseClient) Held() int {
	// TODO(lab)
	return 0
}

// Close refunds every unexpired held token to the Pool (a draining node
// gives its share back instead of stranding it until the lease expires) and
// makes later Decide calls return ErrClosed. Refund errors are joined and
// returned; the tokens are forgotten either way. Closing twice is a no-op.
func (c *LeaseClient) Close(ctx context.Context) error {
	// TODO(lab): mark closed and swap the map out under the client mutex,
	// then refund outside it (never hold a lock across a store call).
	_ = ctx
	return errNotImplemented
}
