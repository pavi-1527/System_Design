// Package snowflake generates 64-bit, roughly time-ordered unique ids with no
// coordinator on the hot path, which is case study C3: every service in the
// fleet mints its own ids, in-process, and they never collide, because each id
// is (time, node, sequence) and the only shared state is who owns which node id.
//
// It is the lab for C3 Unit 1 §2 and §5–§7. Four pieces, in the order the tests
// expect you to build them:
//
//  1. Layout: the bit budget. TimeBits of ticks since Epoch in the high bits,
//     then NodeBits, then SeqBits; the top bit stays 0 so an id fits a signed
//     64-bit column. Compose/Decompose, TickOf/TimeOf, and the capacity numbers
//     (Nodes, PerTick, Lifetime, NodeRate) the estimate in §2 is made of.
//  2. Generator: one node's minting loop. Strictly increasing ids under any
//     number of goroutines, a sequence that never wraps (it waits for the next
//     tick, or runs ahead within a bound), and a policy for a clock that moves
//     backwards: small regressions are waited out, large ones are refused.
//  3. Registry: node ids as leases in a coordination store (a stand-in for an
//     etcd key, a ZooKeeper node or a database row with compare-and-set). Every
//     grant gets a fresh fencing token and inherits the previous holder's
//     high-water mark.
//  4. Worker: a Generator bound to a leased node id. It stops minting before its
//     lease can expire (measured from when the request was sent), and it never
//     uses a tick at or below the previous holder's high-water mark, so a paused
//     old holder and its replacement cannot mint the same id.
//
// # Generator rules (what the tests check)
//
// Let last be the tick of the previous id (initially Floor, treated as fully
// used), now the clock's current tick, and L = MaxAhead in whole ticks.
//
//  1. now > last: the id is (now, node, 0).
//  2. Otherwise the next id must come from tick last (if its sequence is not used
//     up) or last+1 with sequence 0; call that tick want. If want-now <= L, use
//     it at once (with L = 0 that is only the ordinary "same tick, sequence
//     left" case).
//  3. Otherwise, if the clock is behind last by more than MaxBackwards+MaxAhead,
//     return ErrClockBackwards at once: no id, no sleep, no state change.
//  4. Otherwise sleep until the clock reaches tick want-L and start again. The
//     total sleep in one call is at most MaxBackwards + MaxAhead + 2 ticks; past
//     that, return ErrClockStalled (a clock that does not move is an error, not
//     a hang).
//  5. A tick beyond Limit (when Limit != 0) is never used: ErrLeaseExpired. A
//     clock before Epoch is ErrClockBackwards; a tick that does not fit
//     TimeBits is ErrOverflow (the layout's lifetime is over).
//
// Run: go test -race ./labs/c3/snowflake/
package snowflake

import (
	"errors"
	"strconv"
	"sync"
	"time"
)

var errNotImplemented = errors.New("snowflake: not implemented")

// Errors returned by this package. Wrap them with detail (fmt.Errorf("%w: …"));
// callers use errors.Is.
var (
	// ErrBadLayout is returned for a Layout that does not fit in 63 bits, has no
	// time bits, a non-positive Tick or a zero Epoch.
	ErrBadLayout = errors.New("snowflake: bad layout")
	// ErrBadConfig is returned for an invalid Config, WorkerConfig or Registry.
	ErrBadConfig = errors.New("snowflake: bad config")
	// ErrOverflow is returned when a value does not fit its field. For the time
	// field it means the layout's lifetime is over.
	ErrOverflow = errors.New("snowflake: value does not fit its field")
	// ErrClockBackwards is returned when the clock is before the Epoch, or
	// behind the last tick used (or the Floor) by more than the generator may
	// wait out.
	ErrClockBackwards = errors.New("snowflake: clock moved backwards")
	// ErrClockStalled is returned when the generator slept as long as it is
	// allowed to and the clock still has not reached the tick it needs.
	ErrClockStalled = errors.New("snowflake: clock did not advance")
	// ErrLeaseExpired is returned when the next id would need a tick beyond the
	// generator's Limit (for a Worker: the lease would no longer be safe).
	ErrLeaseExpired = errors.New("snowflake: lease limit reached")
	// ErrNoFreeNode is returned by Registry.Acquire when every node id is held
	// by an unexpired lease.
	ErrNoFreeNode = errors.New("snowflake: no free node id")
	// ErrLeaseLost is returned when a lease is no longer held: it expired, it was
	// granted to someone else (a newer token), or it was released.
	ErrLeaseLost = errors.New("snowflake: lease lost")
)

// DefaultEpoch is the zero of the time field in DefaultLayout. A custom epoch
// close to the system's launch buys decades: 2^41 ms from 1970 ran out in 2039.
var DefaultEpoch = time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)

// ID is a generated id. The top bit is always 0, so it also fits an int64
// (a signed BIGINT column).
type ID uint64

// String returns the id in decimal. Send ids to JavaScript clients as strings:
// a JSON number above 2^53 loses its low bits in a browser.
func (id ID) String() string { return strconv.FormatUint(uint64(id), 10) }

// Layout is the bit budget of an id: from the most significant end, TimeBits of
// ticks since Epoch, NodeBits of node id, SeqBits of per-tick sequence. Numeric
// order of ids is therefore (tick, node, seq) order.
type Layout struct {
	TimeBits uint
	NodeBits uint
	SeqBits  uint
	Tick     time.Duration // length of one tick (1 ms in DefaultLayout)
	Epoch    time.Time     // the instant tick 0 starts
}

// DefaultLayout is the classic 41/10/12 layout with 1 ms ticks: ≈ 69.7 years,
// 1,024 nodes, 4,096 ids per millisecond per node.
func DefaultLayout() Layout {
	return Layout{TimeBits: 41, NodeBits: 10, SeqBits: 12, Tick: time.Millisecond, Epoch: DefaultEpoch}
}

// Parts is a decomposed id.
type Parts struct {
	Tick, Node, Seq uint64
}

// Validate checks that the layout fits in 63 bits (the sign bit stays 0), has
// at least one time bit, a positive Tick and a non-zero Epoch. It returns
// ErrBadLayout (wrapped) otherwise.
func (l Layout) Validate() error {
	// TODO(lab): check each field on its own (> 63) before the sum, so that a
	// huge value cannot wrap the addition.
	return errNotImplemented
}

// Nodes is the number of distinct node ids, 2^NodeBits.
func (l Layout) Nodes() uint64 {
	// TODO(lab)
	return 0
}

// PerTick is the number of ids one node can mint in one tick, 2^SeqBits.
func (l Layout) PerTick() uint64 {
	// TODO(lab)
	return 0
}

// Lifetime is how long the time field lasts from Epoch, 2^TimeBits × Tick,
// saturating at the largest time.Duration (≈ 292 years).
func (l Layout) Lifetime() time.Duration {
	// TODO(lab): compare in float64 first (math.Ldexp(1, bits) × Tick against
	// math.MaxInt64); a 48-bit millisecond layout overflows time.Duration.
	return 0
}

// NodeRate is the most ids per second one node can mint, PerTick / Tick.
func (l Layout) NodeRate() float64 {
	// TODO(lab)
	return 0
}

// Compose packs the three fields into an id. It returns ErrOverflow if any
// field does not fit its bits.
func (l Layout) Compose(tick, node, seq uint64) (ID, error) {
	// TODO(lab): tick<<(NodeBits+SeqBits) | node<<SeqBits | seq, after the checks.
	_, _, _ = tick, node, seq
	return 0, errNotImplemented
}

// Decompose is the inverse of Compose.
func (l Layout) Decompose(id ID) Parts {
	// TODO(lab): shift and mask.
	_ = id
	return Parts{}
}

// TickOf returns the tick that contains t. It returns ErrClockBackwards if t is
// before Epoch and ErrOverflow if the tick does not fit TimeBits.
func (l Layout) TickOf(t time.Time) (uint64, error) {
	// TODO(lab): t.Sub(Epoch) / Tick.
	_ = t
	return 0, errNotImplemented
}

// TimeOf returns the start of the tick an id was minted in.
func (l Layout) TimeOf(id ID) time.Time {
	// TODO(lab)
	_ = id
	return time.Time{}
}

// ---------------------------------------------------------------------------
// Generator
// ---------------------------------------------------------------------------

// Clock is the generator's source of time. SystemClock is the real one; tests
// use a fake whose Sleep moves time forward without waiting.
type Clock interface {
	Now() time.Time
	Sleep(d time.Duration)
}

// SystemClock reads the wall clock and sleeps for real.
type SystemClock struct{}

// Now returns time.Now().
func (SystemClock) Now() time.Time { return time.Now() }

// Sleep calls time.Sleep.
func (SystemClock) Sleep(d time.Duration) { time.Sleep(d) }

// Config configures a Generator.
type Config struct {
	Layout Layout
	Node   uint64 // this generator's node id, < Layout.Nodes()
	Clock  Clock
	// MaxBackwards is the largest clock regression the generator waits out;
	// further behind, Next returns ErrClockBackwards at once. 0 refuses every
	// regression.
	MaxBackwards time.Duration
	// MaxAhead, if positive, lets the generator run its tick ahead of the clock
	// by up to this much instead of sleeping (on sequence overflow, or on a
	// small regression): logical time borrowed from the future.
	MaxAhead time.Duration
	// Floor: ticks <= Floor are never used (the previous holder of this node id
	// may have used them). 0 means only tick 0 is excluded.
	Floor uint64
	// Limit, if non-zero, is the last tick this generator may use. It must be
	// greater than Floor.
	Limit uint64
}

// Generator mints ids for one node id. It is safe for concurrent use.
type Generator struct {
	// TODO(lab): the layout, node, clock and bounds from Config (MaxAhead in
	// whole ticks), and, under a mutex, the last tick used (initially Floor),
	// its sequence number (initially the maximum, so the floor tick counts as
	// used up) and the limit.
	mu sync.Mutex
}

// NewGenerator validates cfg and returns a Generator. It returns the layout's
// error for a bad layout, and ErrBadConfig for a nil Clock, a node id that does
// not fit, a negative MaxBackwards or MaxAhead, or a non-zero Limit <= Floor.
func NewGenerator(cfg Config) (*Generator, error) {
	// TODO(lab)
	_ = cfg
	return nil, errNotImplemented
}

// Next returns the next id, following the rules in the package doc. On error no
// id is handed out and the generator's state is unchanged.
//
// Hints: the simplest correct shape holds the mutex for the whole call,
// including any sleep (the other callers would have to wait for the next tick
// anyway). Loop: read the clock, apply rules 1–5 to the current state, and
// keep a running total of what you asked the clock to sleep so a clock that
// never moves ends in ErrClockStalled. After a sleep, go round the loop again
// rather than using the tick you picked before it. The sleep for rule 4 is
// from the instant you read to the start of tick want−L:
// Epoch + (want−L) × Tick − t.
func (g *Generator) Next() (ID, error) {
	// TODO(lab)
	return 0, errNotImplemented
}

// Extend raises the last tick the generator may use to limit, if that is
// higher. It never lowers the limit, and a generator with no limit keeps none.
// A Worker calls it after each successful lease renewal.
func (g *Generator) Extend(limit uint64) {
	// TODO(lab)
	_ = limit
}

// Last returns the tick of the last id handed out (Floor if none yet).
func (g *Generator) Last() uint64 {
	// TODO(lab)
	return 0
}

// Node returns the generator's node id.
func (g *Generator) Node() uint64 {
	// TODO(lab)
	return 0
}

// ---------------------------------------------------------------------------
// Registry: node ids as leases
// ---------------------------------------------------------------------------

// Lease is a grant of one node id.
type Lease struct {
	Node    uint64
	Holder  string
	Token   uint64    // fencing token: strictly increasing across every grant of the registry
	Expires time.Time // the registry's expiry, on the registry's clock
	// Floor is the node's high-water mark when it was granted: the highest tick
	// any previous holder may have used. The new holder must use ticks > Floor.
	Floor uint64
}

// Registry hands out node ids as leases with a TTL. It is safe for concurrent
// use; every method is one atomic step, as a compare-and-set on a row would be.
type Registry struct {
	// TODO(lab): the clock and TTL, and under a mutex one record per node id
	// (held?, holder, token, expiry, high-water mark) plus the last token issued.
	mu sync.Mutex
}

// NewRegistry returns a registry for node ids [0, nodes). It returns
// ErrBadConfig for nodes == 0 or > 2^20, a non-positive ttl or a nil clock.
func NewRegistry(nodes uint64, ttl time.Duration, clock Clock) (*Registry, error) {
	// TODO(lab)
	_, _, _ = nodes, ttl, clock
	return nil, errNotImplemented
}

// Nodes returns the number of node ids the registry manages.
func (r *Registry) Nodes() uint64 {
	// TODO(lab)
	return 0
}

// TTL returns the lease duration.
func (r *Registry) TTL() time.Duration {
	// TODO(lab)
	return 0
}

// Acquire grants the lowest node id that is free (never held, released, or
// expired: the registry's clock is at or past its expiry). The lease gets a new
// token, expires TTL from now, and carries the node's previous high-water mark
// as its Floor; the node's high-water mark becomes max(previous, limit), where
// limit is the last tick the new holder intends to use. ErrNoFreeNode if none.
func (r *Registry) Acquire(holder string, limit uint64) (Lease, error) {
	// TODO(lab)
	_, _ = holder, limit
	return Lease{}, errNotImplemented
}

// Renew extends a lease to TTL from now and raises the node's high-water mark
// to limit if that is higher. It returns ErrLeaseLost if the lease's token is
// not the node's current one, or the lease has expired (even if nobody has
// taken the node yet: an expired lease is gone).
func (r *Registry) Renew(l Lease, limit uint64) (Lease, error) {
	// TODO(lab)
	_, _ = l, limit
	return Lease{}, errNotImplemented
}

// Release frees the node and records used, the last tick the holder actually
// used, as the node's high-water mark, so the next holder does not have to wait
// out the rest of this lease's limit. It returns ErrLeaseLost if the token is
// not the node's current one (taken over, or already released). Releasing an
// expired lease that nobody has taken over is allowed.
func (r *Registry) Release(l Lease, used uint64) error {
	// TODO(lab)
	_, _ = l, used
	return errNotImplemented
}

// ---------------------------------------------------------------------------
// Worker: a generator on a leased node id
// ---------------------------------------------------------------------------

// WorkerConfig configures a Worker.
type WorkerConfig struct {
	Layout Layout
	Clock  Clock
	// Margin: the worker stops minting this long before the registry would
	// expire its lease (it covers clock skew and the renewal's own latency).
	// It must be in [0, TTL).
	Margin       time.Duration
	MaxBackwards time.Duration
	MaxAhead     time.Duration
}

// Worker is a Generator bound to a node id leased from a Registry. It is safe
// for concurrent use.
type Worker struct {
	// TODO(lab): the registry, config and generator; under a read-write mutex,
	// the current lease and a "done" flag (stopped or lost).
	mu sync.RWMutex
}

// StartWorker leases a node id and returns a Worker minting on it. The worker's
// Limit is the tick containing sent + TTL − Margin, where sent is the clock
// reading taken before the Acquire call; its Floor is the lease's Floor. It
// returns ErrBadConfig for a nil registry or clock, a Margin outside [0, TTL),
// or a registry with more node ids than the layout can hold; the registry's
// error (ErrNoFreeNode); and ErrClockBackwards (after releasing the lease) if
// the worker's clock is so far behind the Floor that its Limit would not be
// above it.
func StartWorker(reg *Registry, holder string, cfg WorkerConfig) (*Worker, error) {
	// TODO(lab)
	_, _, _ = reg, holder, cfg
	return nil, errNotImplemented
}

// Next mints an id. It returns ErrLeaseLost after Stop or a failed Renew, and
// the generator's errors otherwise (ErrLeaseExpired once the limit is reached).
func (w *Worker) Next() (ID, error) {
	// TODO(lab): a read lock, so that Stop (write lock) waits for in-flight ids.
	return 0, errNotImplemented
}

// Renew extends the lease and raises the generator's Limit to the tick that
// contains sent + TTL − Margin, with sent read before the registry call. If the
// registry says the lease is lost, the worker stops for good and Renew returns
// ErrLeaseLost.
func (w *Worker) Renew() error {
	// TODO(lab): don't hold the lock across the registry call; every Next would
	// stall for the round trip.
	return errNotImplemented
}

// Stop stops minting and releases the node id with the last tick used, so the
// next holder can start right after it. A second Stop returns ErrLeaseLost.
func (w *Worker) Stop() error {
	// TODO(lab)
	return errNotImplemented
}

// Node returns the leased node id.
func (w *Worker) Node() uint64 {
	// TODO(lab)
	return 0
}

// Lease returns the current lease.
func (w *Worker) Lease() Lease {
	// TODO(lab)
	return Lease{}
}
