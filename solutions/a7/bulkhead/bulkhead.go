// Package bulkhead isolates dependencies from each other and from overload:
// a semaphore bulkhead with a bounded wait queue, a Group that gives every
// dependency its own bulkhead, and an AIMD adaptive concurrency limit.
//
// It is the STRETCH lab for A7 Unit 3 (§1–§2). A bulkhead's job is to make
// sure that one slow vendor can take at most its own share of your threads,
// connections or goroutines; the adaptive limit's job is to find that share
// without a human guessing it.
//
// Suggested order: Bulkhead.Acquire/Do (fast path, then the queue, then
// cancellation), Group, then AIMD. Run: go test -race ./labs/a7/bulkhead/
package bulkhead

import (
	"container/list"
	"context"
	"errors"
	"math"
	"sync"
	"time"
)

// ErrFull is returned when a call is rejected because every slot is busy and
// the wait queue (if any) is full too. Rejecting fast is the point: the
// caller can degrade, retry elsewhere, or fail, instead of piling up.
var ErrFull = errors.New("bulkhead: full")

// ErrUnknown is returned by Group.Do for a dependency with no bulkhead.
var ErrUnknown = errors.New("bulkhead: unknown dependency")

// Config configures a Bulkhead.
type Config struct {
	// MaxConcurrent is the number of calls that may run at once
	// (default 10 if ≤ 0).
	MaxConcurrent int
	// MaxQueue is the number of additional callers that may wait for a
	// slot. 0 means reject immediately when all slots are busy.
	MaxQueue int
}

// Stats is a snapshot of a bulkhead.
type Stats struct {
	InFlight int   // calls holding a slot
	Waiting  int   // callers queued for a slot
	Admitted int64 // calls that got a slot, ever
	Rejected int64 // calls rejected with ErrFull, ever
}

// Bulkhead limits concurrency with an optional bounded FIFO wait queue. It is
// safe for concurrent use.
//
//   - A caller takes a free slot immediately if there is one.
//   - Otherwise, if fewer than MaxQueue callers are waiting, it joins the
//     queue and waits for a slot or for ctx to be done.
//   - Otherwise it is rejected with ErrFull without waiting.
//   - Slots are handed to waiters in arrival order (FIFO).
//   - A waiter whose ctx ends leaves the queue and returns ctx.Err(); it
//     never leaks a slot, even if a slot was handed to it at the same
//     moment.
type Bulkhead struct {
	cfg Config

	mu       sync.Mutex
	inflight int
	waiters  *list.List // of *waiter
	admitted int64
	rejected int64
}

type waiter struct {
	ready   chan struct{} // closed when a slot has been handed over
	granted bool
}

// New returns a Bulkhead.
func New(cfg Config) *Bulkhead {
	if cfg.MaxConcurrent <= 0 {
		cfg.MaxConcurrent = 10
	}
	if cfg.MaxQueue < 0 {
		cfg.MaxQueue = 0
	}
	return &Bulkhead{cfg: cfg, waiters: list.New()}
}

// Acquire obtains a slot. The returned release function must be called
// exactly once when the work is done; calling it again has no effect.
func (b *Bulkhead) Acquire(ctx context.Context) (release func(), err error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	b.mu.Lock()
	if b.inflight < b.cfg.MaxConcurrent {
		b.inflight++
		b.admitted++
		b.mu.Unlock()
		return b.releaser(), nil
	}
	if b.waiters.Len() >= b.cfg.MaxQueue {
		b.rejected++
		b.mu.Unlock()
		return nil, ErrFull
	}
	w := &waiter{ready: make(chan struct{})}
	el := b.waiters.PushBack(w)
	b.mu.Unlock()

	select {
	case <-w.ready:
		return b.releaser(), nil
	case <-ctx.Done():
		b.mu.Lock()
		if w.granted {
			// A slot was handed to us as we gave up: pass it on.
			b.mu.Unlock()
			b.release()
			return nil, ctx.Err()
		}
		b.waiters.Remove(el)
		b.mu.Unlock()
		return nil, ctx.Err()
	}
}

func (b *Bulkhead) releaser() func() {
	var once sync.Once
	return func() { once.Do(b.release) }
}

// release hands the slot to the first waiter, or frees it.
func (b *Bulkhead) release() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if front := b.waiters.Front(); front != nil {
		w := b.waiters.Remove(front).(*waiter)
		w.granted = true
		b.admitted++
		close(w.ready)
		return
	}
	b.inflight--
}

// Do runs fn inside a slot. Rejection returns ErrFull without running fn;
// the slot is released even if fn panics.
func (b *Bulkhead) Do(ctx context.Context, fn func(ctx context.Context) error) error {
	release, err := b.Acquire(ctx)
	if err != nil {
		return err
	}
	defer release()
	return fn(ctx)
}

// Stats returns a snapshot.
func (b *Bulkhead) Stats() Stats {
	b.mu.Lock()
	defer b.mu.Unlock()
	return Stats{InFlight: b.inflight, Waiting: b.waiters.Len(), Admitted: b.admitted, Rejected: b.rejected}
}

// Group holds one Bulkhead per named dependency, so that saturating one
// cannot consume another's slots.
type Group struct {
	bulkheads map[string]*Bulkhead
}

// NewGroup creates a bulkhead for every entry of cfgs.
func NewGroup(cfgs map[string]Config) *Group {
	g := &Group{bulkheads: make(map[string]*Bulkhead, len(cfgs))}
	for name, c := range cfgs {
		g.bulkheads[name] = New(c)
	}
	return g
}

// Do runs fn inside dep's bulkhead, or returns ErrUnknown.
func (g *Group) Do(ctx context.Context, dep string, fn func(ctx context.Context) error) error {
	b, ok := g.bulkheads[dep]
	if !ok {
		return ErrUnknown
	}
	return b.Do(ctx, fn)
}

// Stats returns a snapshot per dependency.
func (g *Group) Stats() map[string]Stats {
	out := make(map[string]Stats, len(g.bulkheads))
	for name, b := range g.bulkheads {
		out[name] = b.Stats()
	}
	return out
}

// ---------- adaptive concurrency limit (AIMD) ----------

// Outcome tells the AIMD limiter what a finished call says about load.
type Outcome int

const (
	// Success: the call completed normally.
	Success Outcome = iota
	// Dropped: the dependency signalled overload (timeout, 429, 503,
	// queue-full): the limit is too high.
	Dropped
	// Ignore: says nothing about load (the caller cancelled, a 4xx).
	Ignore
)

// AIMDConfig configures an AIMD limiter. Zero values get the defaults noted.
type AIMDConfig struct {
	Initial float64 // starting limit (default 10)
	Min     float64 // never below (default 1)
	Max     float64 // never above (default 1000)
	// Increase is the additive step: each successful call under load adds
	// Increase/limit, so the limit grows by about Increase per `limit`
	// successes (≈ per round trip). Default 1.
	Increase float64
	// Decrease is the multiplicative factor applied on overload
	// (default 0.5).
	Decrease float64
	// Cooldown is the minimum time between two decreases; a burst of
	// drops from one overload event must count once (default 1s).
	Cooldown time.Duration
	// Now is the clock (default time.Now).
	Now func() time.Time
}

// AIMD is an additive-increase / multiplicative-decrease concurrency limiter,
// the same control law as TCP congestion avoidance. It is safe for
// concurrent use.
//
//   - TryAcquire admits a call iff inFlight < floor(limit).
//   - Success: if the call was admitted while at least half of the limit was
//     already in use, limit += Increase/limit (capped at Max). Growing while
//     idle would teach nothing about capacity, so successes at low
//     utilisation leave the limit alone.
//   - Dropped: if at least Cooldown has passed since the last decrease (or
//     there was none), limit = max(Min, limit × Decrease); otherwise no
//     change.
//   - Ignore: only frees the slot.
//   - The returned done func must be called once; repeat calls do nothing.
type AIMD struct {
	cfg AIMDConfig

	mu           sync.Mutex
	limit        float64
	inflight     int
	lastDecrease time.Time
	decreased    bool
}

// NewAIMD returns an AIMD limiter with defaults applied.
func NewAIMD(cfg AIMDConfig) *AIMD {
	if cfg.Initial <= 0 {
		cfg.Initial = 10
	}
	if cfg.Min <= 0 {
		cfg.Min = 1
	}
	if cfg.Max <= 0 {
		cfg.Max = 1000
	}
	if cfg.Increase <= 0 {
		cfg.Increase = 1
	}
	if cfg.Decrease <= 0 || cfg.Decrease >= 1 {
		cfg.Decrease = 0.5
	}
	if cfg.Cooldown <= 0 {
		cfg.Cooldown = time.Second
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	cfg.Initial = math.Min(cfg.Max, math.Max(cfg.Min, cfg.Initial))
	return &AIMD{cfg: cfg, limit: cfg.Initial}
}

// Limit reports the current limit.
func (a *AIMD) Limit() float64 {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.limit
}

// InFlight reports the number of admitted calls that have not finished.
func (a *AIMD) InFlight() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.inflight
}

// TryAcquire admits a call or reports ok=false (shed it: do not queue).
func (a *AIMD) TryAcquire() (done func(Outcome), ok bool) {
	a.mu.Lock()
	if a.inflight >= int(math.Floor(a.limit)) {
		a.mu.Unlock()
		return nil, false
	}
	atAdmit := a.inflight
	a.inflight++
	underLoad := float64(atAdmit)*2 >= math.Floor(a.limit)
	a.mu.Unlock()
	var once sync.Once
	return func(o Outcome) { once.Do(func() { a.finish(o, underLoad) }) }, true
}

func (a *AIMD) finish(o Outcome, underLoad bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.inflight--
	switch o {
	case Success:
		if underLoad {
			a.limit = math.Min(a.cfg.Max, a.limit+a.cfg.Increase/a.limit)
		}
	case Dropped:
		now := a.cfg.Now()
		if !a.decreased || now.Sub(a.lastDecrease) >= a.cfg.Cooldown {
			a.limit = math.Max(a.cfg.Min, a.limit*a.cfg.Decrease)
			a.lastDecrease, a.decreased = now, true
		}
	}
}
