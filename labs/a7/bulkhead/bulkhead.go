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
// cancellation), Group, then AIMD. Run:
//
//	go test -race ./labs/a7/bulkhead/
package bulkhead

import (
	"context"
	"errors"
	"time"
)

var errNotImplemented = errors.New("bulkhead: not implemented")

// ErrFull is returned when a call is rejected because every slot is busy and
// the wait queue (if any) is full too. Rejecting fast is the point: the
// caller can degrade, retry elsewhere, or fail, instead of piling up.
var ErrFull = errors.New("bulkhead: full")

// ErrUnknown is returned by Group.Do for a dependency with no bulkhead.
var ErrUnknown = errors.New("bulkhead: unknown dependency")

// Config configures a Bulkhead. Provided.
type Config struct {
	// MaxConcurrent is the number of calls that may run at once
	// (default 10 if ≤ 0).
	MaxConcurrent int
	// MaxQueue is the number of additional callers that may wait for a
	// slot. 0 means reject immediately when all slots are busy.
	MaxQueue int
}

// Stats is a snapshot of a bulkhead. Provided.
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
//   - Slots are handed to waiters in arrival order (FIFO). Handing over a
//     slot keeps InFlight unchanged.
//   - A waiter whose ctx ends leaves the queue and returns ctx.Err(); it
//     never leaks a slot, even if a slot was handed to it at the same
//     moment (the select may pick either case: check under the lock).
//   - An already-cancelled ctx is refused before anything is consumed.
//
// Hint: a mutex, an in-flight counter and a container/list of waiters, each
// with a channel that is closed when a slot is handed over and a flag
// recording that it was.
type Bulkhead struct {
	// TODO(lab)
}

// New returns a Bulkhead (MaxConcurrent ≤ 0 means 10; MaxQueue < 0 means 0).
func New(cfg Config) *Bulkhead {
	// TODO(lab)
	return &Bulkhead{}
}

// Acquire obtains a slot. The returned release function must be called
// exactly once when the work is done; calling it again has no effect.
func (b *Bulkhead) Acquire(ctx context.Context) (release func(), err error) {
	// TODO(lab)
	return nil, errNotImplemented
}

// Do runs fn inside a slot. Rejection returns ErrFull without running fn;
// the slot is released even if fn panics. fn's own error is returned.
func (b *Bulkhead) Do(ctx context.Context, fn func(ctx context.Context) error) error {
	// TODO(lab)
	return errNotImplemented
}

// Stats returns a snapshot.
func (b *Bulkhead) Stats() Stats {
	// TODO(lab)
	return Stats{}
}

// Group holds one Bulkhead per named dependency, so that saturating one
// cannot consume another's slots.
type Group struct {
	// TODO(lab)
}

// NewGroup creates a bulkhead for every entry of cfgs.
func NewGroup(cfgs map[string]Config) *Group {
	// TODO(lab)
	return &Group{}
}

// Do runs fn inside dep's bulkhead, or returns ErrUnknown.
func (g *Group) Do(ctx context.Context, dep string, fn func(ctx context.Context) error) error {
	// TODO(lab)
	return errNotImplemented
}

// Stats returns a snapshot per dependency.
func (g *Group) Stats() map[string]Stats {
	// TODO(lab)
	return nil
}

// ---------- adaptive concurrency limit (AIMD) ----------

// Outcome tells the AIMD limiter what a finished call says about load.
// Provided.
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
// Provided.
type AIMDConfig struct {
	Initial float64 // starting limit (default 10), clamped into [Min, Max]
	Min     float64 // never below (default 1)
	Max     float64 // never above (default 1000)
	// Increase is the additive step: each successful call under load adds
	// Increase/limit, so the limit grows by about Increase per `limit`
	// successes (≈ per round trip). Default 1.
	Increase float64
	// Decrease is the multiplicative factor applied on overload, in (0, 1)
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
//   - TryAcquire admits a call iff inFlight < floor(limit); otherwise it
//     reports ok=false and the caller sheds the work (no queueing).
//   - Success: if the call was admitted while at least half of the limit was
//     already in use (inFlightAtAdmit × 2 ≥ floor(limit)), limit +=
//     Increase/limit (capped at Max). Growing while idle would teach
//     nothing about capacity, so successes at low utilisation leave the
//     limit alone.
//   - Dropped: if at least Cooldown has passed since the last decrease (or
//     there was none), limit = max(Min, limit × Decrease); otherwise no
//     change.
//   - Ignore: only frees the slot.
//   - The returned done func must be called once; repeat calls do nothing.
type AIMD struct {
	// TODO(lab)
}

// NewAIMD returns an AIMD limiter with defaults applied.
func NewAIMD(cfg AIMDConfig) *AIMD {
	// TODO(lab)
	return &AIMD{}
}

// Limit reports the current limit.
func (a *AIMD) Limit() float64 {
	// TODO(lab)
	return 0
}

// InFlight reports the number of admitted calls that have not finished.
func (a *AIMD) InFlight() int {
	// TODO(lab)
	return 0
}

// TryAcquire admits a call or reports ok=false.
func (a *AIMD) TryAcquire() (done func(Outcome), ok bool) {
	// TODO(lab)
	return nil, false
}
