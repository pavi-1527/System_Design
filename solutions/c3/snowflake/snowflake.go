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
	"fmt"
	"math"
	"strconv"
	"sync"
	"time"
)

// Errors returned by this package. They are wrapped with detail; callers use
// errors.Is.
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
// at least one time bit, a positive Tick and a non-zero Epoch.
func (l Layout) Validate() error {
	switch {
	case l.TimeBits == 0:
		return fmt.Errorf("%w: TimeBits must be at least 1", ErrBadLayout)
	case l.TimeBits > 63 || l.NodeBits > 63 || l.SeqBits > 63 || l.TimeBits+l.NodeBits+l.SeqBits > 63:
		return fmt.Errorf("%w: %d+%d+%d bits do not fit in 63 (the top bit stays 0 so ids fit a signed 64-bit column)",
			ErrBadLayout, l.TimeBits, l.NodeBits, l.SeqBits)
	case l.Tick <= 0:
		return fmt.Errorf("%w: Tick must be positive, got %v", ErrBadLayout, l.Tick)
	case l.Epoch.IsZero():
		return fmt.Errorf("%w: Epoch is not set", ErrBadLayout)
	}
	return nil
}

// Nodes is the number of distinct node ids, 2^NodeBits.
func (l Layout) Nodes() uint64 { return 1 << l.NodeBits }

// PerTick is the number of ids one node can mint in one tick, 2^SeqBits.
func (l Layout) PerTick() uint64 { return 1 << l.SeqBits }

// Lifetime is how long the time field lasts from Epoch, 2^TimeBits × Tick,
// saturating at the largest time.Duration (≈ 292 years).
func (l Layout) Lifetime() time.Duration {
	ticks := math.Ldexp(1, int(l.TimeBits))
	if ticks*float64(l.Tick) >= math.MaxInt64 {
		return time.Duration(math.MaxInt64)
	}
	return time.Duration(uint64(1)<<l.TimeBits) * l.Tick
}

// NodeRate is the most ids per second one node can mint, PerTick / Tick.
func (l Layout) NodeRate() float64 { return float64(l.PerTick()) / l.Tick.Seconds() }

// Compose packs the three fields into an id. It returns ErrOverflow if any
// field does not fit its bits.
func (l Layout) Compose(tick, node, seq uint64) (ID, error) {
	switch {
	case tick >= 1<<l.TimeBits:
		return 0, fmt.Errorf("%w: tick %d needs more than %d bits (lifetime over)", ErrOverflow, tick, l.TimeBits)
	case node >= l.Nodes():
		return 0, fmt.Errorf("%w: node %d needs more than %d bits", ErrOverflow, node, l.NodeBits)
	case seq >= l.PerTick():
		return 0, fmt.Errorf("%w: seq %d needs more than %d bits", ErrOverflow, seq, l.SeqBits)
	}
	return ID(tick<<(l.NodeBits+l.SeqBits) | node<<l.SeqBits | seq), nil
}

// Decompose is the inverse of Compose.
func (l Layout) Decompose(id ID) Parts {
	v := uint64(id)
	return Parts{
		Tick: v >> (l.NodeBits + l.SeqBits),
		Node: (v >> l.SeqBits) & (l.Nodes() - 1),
		Seq:  v & (l.PerTick() - 1),
	}
}

// TickOf returns the tick that contains t. It returns ErrClockBackwards if t is
// before Epoch and ErrOverflow if the tick does not fit TimeBits.
func (l Layout) TickOf(t time.Time) (uint64, error) {
	if t.Before(l.Epoch) {
		return 0, fmt.Errorf("%w: %v is before the epoch %v", ErrClockBackwards, t, l.Epoch)
	}
	tick := uint64(t.Sub(l.Epoch) / l.Tick)
	if tick >= 1<<l.TimeBits {
		return 0, fmt.Errorf("%w: tick %d needs more than %d bits (lifetime over)", ErrOverflow, tick, l.TimeBits)
	}
	return tick, nil
}

// TimeOf returns the start of the tick an id was minted in.
func (l Layout) TimeOf(id ID) time.Time { return l.start(l.Decompose(id).Tick) }

func (l Layout) start(tick uint64) time.Time { return l.Epoch.Add(time.Duration(tick) * l.Tick) }

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
	layout   Layout
	node     uint64
	clock    Clock
	maxBack  time.Duration
	maxAhead uint64 // in ticks
	maxSeq   uint64

	mu    sync.Mutex
	last  uint64 // tick of the last id handed out (initially Floor)
	seq   uint64 // its sequence number (initially maxSeq: the floor tick counts as used up)
	limit uint64 // 0 = none
}

// NewGenerator validates cfg and returns a Generator. It returns the layout's
// error for a bad layout, and ErrBadConfig for a nil Clock, a node id that does
// not fit, a negative MaxBackwards or MaxAhead, or a non-zero Limit <= Floor.
func NewGenerator(cfg Config) (*Generator, error) {
	if err := cfg.Layout.Validate(); err != nil {
		return nil, err
	}
	switch {
	case cfg.Clock == nil:
		return nil, fmt.Errorf("%w: nil Clock", ErrBadConfig)
	case cfg.Node >= cfg.Layout.Nodes():
		return nil, fmt.Errorf("%w: node %d does not fit %d bits", ErrBadConfig, cfg.Node, cfg.Layout.NodeBits)
	case cfg.MaxBackwards < 0 || cfg.MaxAhead < 0:
		return nil, fmt.Errorf("%w: MaxBackwards and MaxAhead must not be negative", ErrBadConfig)
	case cfg.Limit != 0 && cfg.Limit <= cfg.Floor:
		return nil, fmt.Errorf("%w: Limit %d must be greater than Floor %d", ErrBadConfig, cfg.Limit, cfg.Floor)
	}
	maxSeq := cfg.Layout.PerTick() - 1
	return &Generator{
		layout:   cfg.Layout,
		node:     cfg.Node,
		clock:    cfg.Clock,
		maxBack:  cfg.MaxBackwards,
		maxAhead: uint64(cfg.MaxAhead / cfg.Layout.Tick),
		maxSeq:   maxSeq,
		last:     cfg.Floor,
		seq:      maxSeq,
		limit:    cfg.Limit,
	}, nil
}

// Next returns the next id, following the rules in the package doc. On error no
// id is handed out and the generator's state is unchanged.
func (g *Generator) Next() (ID, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	tick := g.layout.Tick
	ahead := time.Duration(g.maxAhead) * tick
	budget := g.maxBack + ahead + 2*tick
	var slept time.Duration
	for {
		t := g.clock.Now()
		now, err := g.layout.TickOf(t)
		if err != nil {
			return 0, err
		}
		if now > g.last {
			if g.limit != 0 && now > g.limit {
				return 0, fmt.Errorf("%w: tick %d is past the limit %d", ErrLeaseExpired, now, g.limit)
			}
			id, err := g.layout.Compose(now, g.node, 0)
			if err != nil {
				return 0, err
			}
			g.last, g.seq = now, 0
			return id, nil
		}

		// The clock has not moved past the last tick used.
		want, seq := g.last, g.seq+1
		if g.seq >= g.maxSeq {
			want, seq = g.last+1, 0
		}
		if g.limit != 0 && want > g.limit {
			return 0, fmt.Errorf("%w: tick %d is past the limit %d", ErrLeaseExpired, want, g.limit)
		}
		if want-now <= g.maxAhead {
			id, err := g.layout.Compose(want, g.node, seq)
			if err != nil {
				return 0, err
			}
			g.last, g.seq = want, seq
			return id, nil
		}
		if behind := time.Duration(g.last-now) * tick; behind > g.maxBack+ahead {
			return 0, fmt.Errorf("%w: clock is %v behind the last tick used (waits out at most %v)",
				ErrClockBackwards, behind, g.maxBack+ahead)
		}
		if slept >= budget {
			return 0, fmt.Errorf("%w: slept %v and the clock is still at tick %d, needs %d",
				ErrClockStalled, slept, now, want-g.maxAhead)
		}
		d := g.layout.start(want - g.maxAhead).Sub(t)
		g.clock.Sleep(d)
		slept += d
	}
}

// Extend raises the last tick the generator may use to limit, if that is
// higher. It never lowers the limit, and a generator with no limit keeps none.
// A Worker calls it after each successful lease renewal.
func (g *Generator) Extend(limit uint64) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.limit != 0 && limit > g.limit {
		g.limit = limit
	}
}

// Last returns the tick of the last id handed out (Floor if none yet).
func (g *Generator) Last() uint64 {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.last
}

// Node returns the generator's node id.
func (g *Generator) Node() uint64 { return g.node }

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

type record struct {
	held      bool
	holder    string
	token     uint64
	expires   time.Time
	highWater uint64
}

// Registry hands out node ids as leases with a TTL. It is safe for concurrent
// use; every method is one atomic step, as a compare-and-set on a row would be.
type Registry struct {
	clock Clock
	ttl   time.Duration

	mu      sync.Mutex
	records []record
	tokens  uint64
}

// NewRegistry returns a registry for node ids [0, nodes). It returns
// ErrBadConfig for nodes == 0 or > 2^20, a non-positive ttl or a nil clock.
func NewRegistry(nodes uint64, ttl time.Duration, clock Clock) (*Registry, error) {
	switch {
	case nodes == 0 || nodes > 1<<20:
		return nil, fmt.Errorf("%w: nodes must be in [1, 2^20], got %d", ErrBadConfig, nodes)
	case ttl <= 0:
		return nil, fmt.Errorf("%w: ttl must be positive, got %v", ErrBadConfig, ttl)
	case clock == nil:
		return nil, fmt.Errorf("%w: nil Clock", ErrBadConfig)
	}
	return &Registry{clock: clock, ttl: ttl, records: make([]record, nodes)}, nil
}

// Nodes returns the number of node ids the registry manages.
func (r *Registry) Nodes() uint64 { return uint64(len(r.records)) }

// TTL returns the lease duration.
func (r *Registry) TTL() time.Duration { return r.ttl }

// Acquire grants the lowest node id that is free (never held, released, or
// expired: the registry's clock is at or past its expiry). The lease gets a new
// token, expires TTL from now, and carries the node's previous high-water mark
// as its Floor; the node's high-water mark becomes max(previous, limit), where
// limit is the last tick the new holder intends to use. ErrNoFreeNode if none.
func (r *Registry) Acquire(holder string, limit uint64) (Lease, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.clock.Now()
	for i := range r.records {
		rec := &r.records[i]
		if rec.held && now.Before(rec.expires) {
			continue
		}
		floor := rec.highWater
		r.tokens++
		rec.held, rec.holder, rec.token, rec.expires = true, holder, r.tokens, now.Add(r.ttl)
		if limit > rec.highWater {
			rec.highWater = limit
		}
		return Lease{Node: uint64(i), Holder: holder, Token: rec.token, Expires: rec.expires, Floor: floor}, nil
	}
	return Lease{}, fmt.Errorf("%w: all %d node ids are leased", ErrNoFreeNode, len(r.records))
}

// Renew extends a lease to TTL from now and raises the node's high-water mark
// to limit if that is higher. It returns ErrLeaseLost if the lease's token is
// not the node's current one, or the lease has expired (even if nobody has
// taken the node yet: an expired lease is gone).
func (r *Registry) Renew(l Lease, limit uint64) (Lease, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	rec, err := r.current(l)
	if err != nil {
		return Lease{}, err
	}
	now := r.clock.Now()
	if !now.Before(rec.expires) {
		return Lease{}, fmt.Errorf("%w: node %d expired at %v", ErrLeaseLost, l.Node, rec.expires)
	}
	rec.expires = now.Add(r.ttl)
	if limit > rec.highWater {
		rec.highWater = limit
	}
	l.Expires = rec.expires
	return l, nil
}

// Release frees the node and records used, the last tick the holder actually
// used, as the node's high-water mark, so the next holder does not have to wait
// out the rest of this lease's limit. It returns ErrLeaseLost if the token is
// not the node's current one (taken over, or already released). Releasing an
// expired lease that nobody has taken over is allowed.
func (r *Registry) Release(l Lease, used uint64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	rec, err := r.current(l)
	if err != nil {
		return err
	}
	rec.held = false
	rec.highWater = used
	return nil
}

// current returns the record of a lease that is still the node's current grant.
func (r *Registry) current(l Lease) (*record, error) {
	if l.Node >= uint64(len(r.records)) {
		return nil, fmt.Errorf("%w: no node %d", ErrLeaseLost, l.Node)
	}
	rec := &r.records[l.Node]
	if !rec.held || rec.token != l.Token {
		return nil, fmt.Errorf("%w: node %d token %d is not current", ErrLeaseLost, l.Node, l.Token)
	}
	return rec, nil
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
	reg *Registry
	cfg WorkerConfig
	gen *Generator

	mu    sync.RWMutex
	lease Lease
	done  bool // stopped, or the lease was lost
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
	if err := cfg.Layout.Validate(); err != nil {
		return nil, err
	}
	switch {
	case reg == nil || cfg.Clock == nil:
		return nil, fmt.Errorf("%w: nil Registry or Clock", ErrBadConfig)
	case cfg.Margin < 0 || cfg.Margin >= reg.TTL():
		return nil, fmt.Errorf("%w: Margin %v must be in [0, TTL %v)", ErrBadConfig, cfg.Margin, reg.TTL())
	case reg.Nodes() > cfg.Layout.Nodes():
		return nil, fmt.Errorf("%w: registry has %d node ids, layout holds %d", ErrBadConfig, reg.Nodes(), cfg.Layout.Nodes())
	}
	limit, err := limitFrom(cfg.Layout, cfg.Clock.Now(), reg.TTL()-cfg.Margin)
	if err != nil {
		return nil, err
	}
	lease, err := reg.Acquire(holder, limit)
	if err != nil {
		return nil, err
	}
	if limit <= lease.Floor {
		_ = reg.Release(lease, lease.Floor)
		return nil, fmt.Errorf("%w: limit tick %d is not above the node's floor %d", ErrClockBackwards, limit, lease.Floor)
	}
	gen, err := NewGenerator(Config{
		Layout: cfg.Layout, Node: lease.Node, Clock: cfg.Clock,
		MaxBackwards: cfg.MaxBackwards, MaxAhead: cfg.MaxAhead,
		Floor: lease.Floor, Limit: limit,
	})
	if err != nil {
		_ = reg.Release(lease, lease.Floor)
		return nil, err
	}
	return &Worker{reg: reg, cfg: cfg, gen: gen, lease: lease}, nil
}

// limitFrom is the tick that contains sent + d.
func limitFrom(l Layout, sent time.Time, d time.Duration) (uint64, error) {
	return l.TickOf(sent.Add(d))
}

// Next mints an id. It returns ErrLeaseLost after Stop or a failed Renew, and
// the generator's errors otherwise (ErrLeaseExpired once the limit is reached).
func (w *Worker) Next() (ID, error) {
	w.mu.RLock()
	defer w.mu.RUnlock()
	if w.done {
		return 0, fmt.Errorf("%w: worker on node %d has stopped", ErrLeaseLost, w.lease.Node)
	}
	return w.gen.Next()
}

// Renew extends the lease and raises the generator's Limit to the tick that
// contains sent + TTL − Margin, with sent read before the registry call. If the
// registry says the lease is lost, the worker stops for good and Renew returns
// ErrLeaseLost.
func (w *Worker) Renew() error {
	w.mu.RLock()
	lease, done := w.lease, w.done
	w.mu.RUnlock()
	if done {
		return fmt.Errorf("%w: worker on node %d has stopped", ErrLeaseLost, lease.Node)
	}
	limit, err := limitFrom(w.cfg.Layout, w.cfg.Clock.Now(), w.reg.TTL()-w.cfg.Margin)
	if err != nil {
		return err
	}
	renewed, err := w.reg.Renew(lease, limit)

	w.mu.Lock()
	defer w.mu.Unlock()
	if err != nil {
		w.done = true
		return err
	}
	w.lease = renewed
	w.gen.Extend(limit)
	return nil
}

// Stop stops minting and releases the node id with the last tick used, so the
// next holder can start right after it. A second Stop returns ErrLeaseLost.
func (w *Worker) Stop() error {
	w.mu.Lock()
	if w.done {
		w.mu.Unlock()
		return fmt.Errorf("%w: worker on node %d has already stopped", ErrLeaseLost, w.lease.Node)
	}
	w.done = true
	lease, used := w.lease, w.gen.Last()
	w.mu.Unlock()
	return w.reg.Release(lease, used)
}

// Node returns the leased node id.
func (w *Worker) Node() uint64 { return w.gen.Node() }

// Lease returns the current lease.
func (w *Worker) Lease() Lease {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.lease
}
