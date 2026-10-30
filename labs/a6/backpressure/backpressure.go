// Package backpressure is a toolbox for keeping queues bounded: a bounded
// queue with three overflow policies, a priority queue that sheds low
// priorities first, credit-based flow control, and a deterministic simulator
// that shows how queue length turns into latency (Little's law). It is the
// STRETCH lab for A6 Unit 3.
//
// Suggested order: Queue, PriorityQueue, CreditGate, Simulate.
// Run: go test -race ./labs/a6/backpressure/
//
// # Queue
//
// Queue[T] is a FIFO of fixed capacity. What Offer does when it is full is
// the Policy:
//
//   - Block: Offer waits until there is room, the context is done (it
//     returns ctx.Err() and enqueues nothing) or the queue is closed.
//   - DropNewest: Offer returns ErrFull immediately and the queue is
//     unchanged (the producer is told no: reject at the door).
//   - DropOldest: Offer evicts the oldest queued item, enqueues the new one
//     and returns nil (freshness over completeness).
//
// Take returns the oldest item, waiting for one if the queue is empty, until
// ctx is done (ctx.Err()) or the queue is closed and empty (ErrClosed).
// Close makes Offer return ErrClosed, wakes blocked Offers with ErrClosed,
// and lets Take drain what is left before returning ErrClosed. Close is
// idempotent. Stats counts items Accepted (Offer returned nil), Rejected
// (ErrFull), Evicted (pushed out by DropOldest) and Taken. All methods are
// safe for concurrent use.
//
// # PriorityQueue
//
// PriorityQueue[T] has a capacity and, per priority, an admission threshold
// in (0, 1]: an item of priority p is admitted only while Len() is below
// floor(threshold[p] * capacity); otherwise Offer returns ErrShed and counts
// it in Shed()[p]. Defaults (used for any priority missing from the map
// passed to the constructor): Critical 1.0, Standard 0.8, Bulk 0.5. A
// threshold below the capacity of one item still admits one item when the
// queue is empty. Take is non-blocking and returns the highest priority
// (lowest number) first, FIFO within a priority; ok is false when empty.
//
// # CreditGate
//
// CreditGate is credit-based flow control: the receiver grants credits, and
// each send consumes one. Acquire blocks until a credit is available or ctx
// is done (ctx.Err(), nothing consumed). TryAcquire never blocks. Grant adds
// n credits (n <= 0 is ignored) and wakes waiters. Available reports the
// current credits.
//
// # Simulate
//
// Simulate runs a deterministic FIFO queueing simulation: request i arrives
// at i/ArrivalRate seconds for as long as that is before Duration; each
// request needs ServiceTime on one of Servers servers; at most QueueCap
// requests may wait (not counting those in service; 0 = unbounded) and an
// arrival that finds QueueCap waiting is dropped. The simulation continues
// until every admitted request has finished. Wait is the time from arrival to
// the start of service; Sojourn is wait + service. End is when the last
// request finished. MeanInSystem is the time-average number of requests
// (waiting + in service) over [0, End], computed from arrival and completion
// events, so Little's law (MeanInSystem = Served/End * MeanSojourn) can be
// checked against it.
package backpressure

import (
	"context"
	"errors"
	"time"
)

var errNotImplemented = errors.New("backpressure: not implemented")

// Errors returned by the package.
var (
	ErrFull   = errors.New("backpressure: queue full")
	ErrClosed = errors.New("backpressure: queue closed")
	ErrShed   = errors.New("backpressure: load shed")
)

// Policy is what a full Queue does on Offer.
type Policy int

// Overflow policies.
const (
	Block Policy = iota
	DropNewest
	DropOldest
)

// Stats are cumulative counters of a Queue.
type Stats struct {
	Accepted, Rejected, Evicted, Taken int64
}

// Queue is a bounded FIFO.
type Queue[T any] struct {
	// TODO(lab): a buffered channel is enough for Block and DropNewest;
	// DropOldest needs a way to remove the head atomically with the insert
	// (a mutex around a slice, or a loop that retries). Close must wake
	// goroutines blocked in Offer and Take without closing the data channel
	// under a sender (that panics): use a separate "closed" channel.
}

// NewQueue returns a Queue holding at most capacity items (minimum 1).
func NewQueue[T any](capacity int, p Policy) *Queue[T] {
	// TODO(lab)
	return &Queue[T]{}
}

// Offer adds v according to the queue's Policy.
func (q *Queue[T]) Offer(ctx context.Context, v T) error {
	// TODO(lab)
	return errNotImplemented
}

// Take removes and returns the oldest item, waiting if necessary.
func (q *Queue[T]) Take(ctx context.Context) (T, error) {
	// TODO(lab)
	var zero T
	return zero, errNotImplemented
}

// Len is the number of queued items.
func (q *Queue[T]) Len() int {
	// TODO(lab)
	return 0
}

// Close closes the queue. Idempotent.
func (q *Queue[T]) Close() {
	// TODO(lab)
}

// Stats returns the cumulative counters.
func (q *Queue[T]) Stats() Stats {
	// TODO(lab)
	return Stats{}
}

// Priority of a request; lower numbers are more important.
type Priority int

// Priorities.
const (
	Critical Priority = iota
	Standard
	Bulk
)

// PriorityQueue admits low priorities only while the queue is short.
type PriorityQueue[T any] struct {
	// TODO(lab): a mutex, one FIFO per priority, the total length, the
	// per-priority admission limits and the shed counters.
}

// NewPriorityQueue returns a PriorityQueue. thresholds may be nil; see the
// package documentation for the defaults.
func NewPriorityQueue[T any](capacity int, thresholds map[Priority]float64) *PriorityQueue[T] {
	// TODO(lab)
	return &PriorityQueue[T]{}
}

// Offer admits v if the queue is shorter than p's limit, else ErrShed.
func (q *PriorityQueue[T]) Offer(p Priority, v T) error {
	// TODO(lab)
	return errNotImplemented
}

// Take removes the most important, oldest item (ok is false when empty).
func (q *PriorityQueue[T]) Take() (v T, p Priority, ok bool) {
	// TODO(lab)
	return v, p, false
}

// Len is the number of queued items.
func (q *PriorityQueue[T]) Len() int {
	// TODO(lab)
	return 0
}

// Shed returns a copy of the per-priority shed counters.
func (q *PriorityQueue[T]) Shed() map[Priority]int64 {
	// TODO(lab)
	return nil
}

// CreditGate is credit-based flow control.
type CreditGate struct {
	// TODO(lab): a mutex, the credit count, and a way to wake waiters that
	// also honours ctx (for example a channel that is closed and replaced on
	// every Grant).
}

// NewCreditGate returns a gate with initial credits.
func NewCreditGate(initial int) *CreditGate {
	// TODO(lab)
	return &CreditGate{}
}

// Grant adds n credits (n <= 0 is ignored).
func (g *CreditGate) Grant(n int) {
	// TODO(lab)
}

// TryAcquire consumes a credit if one is available.
func (g *CreditGate) TryAcquire() bool {
	// TODO(lab)
	return false
}

// Acquire waits for a credit and consumes it; ctx.Err() if ctx ends first.
func (g *CreditGate) Acquire(ctx context.Context) error {
	// TODO(lab)
	return errNotImplemented
}

// Available is the number of unused credits.
func (g *CreditGate) Available() int {
	// TODO(lab)
	return 0
}

// SimConfig describes a deterministic queueing simulation.
type SimConfig struct {
	ArrivalRate float64       // requests per second, evenly spaced
	ServiceTime time.Duration // per request
	Servers     int           // default 1
	QueueCap    int           // max waiting requests; 0 = unbounded
	Duration    time.Duration // arrivals stop here
}

// SimResult summarises a simulation.
type SimResult struct {
	Arrived, Served, Dropped int
	MaxQueue                 int // most requests waiting (not in service) right after an admitted arrival
	MeanWait, MaxWait        time.Duration
	MeanSojourn              time.Duration
	MeanInSystem             float64
	End                      time.Duration
}

// Simulate runs the simulation. See the package documentation.
func Simulate(cfg SimConfig) SimResult {
	// TODO(lab)
	return SimResult{}
}
