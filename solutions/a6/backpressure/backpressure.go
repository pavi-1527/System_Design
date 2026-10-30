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
	"math"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

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
	ch     chan T
	policy Policy
	closed chan struct{}
	once   sync.Once

	accepted, rejected, evicted, taken atomic.Int64
}

// NewQueue returns a Queue holding at most capacity items (minimum 1).
func NewQueue[T any](capacity int, p Policy) *Queue[T] {
	if capacity < 1 {
		capacity = 1
	}
	return &Queue[T]{ch: make(chan T, capacity), policy: p, closed: make(chan struct{})}
}

// Offer adds v according to the queue's Policy.
func (q *Queue[T]) Offer(ctx context.Context, v T) error {
	select {
	case <-q.closed:
		return ErrClosed
	default:
	}
	switch q.policy {
	case DropNewest:
		select {
		case q.ch <- v:
			q.accepted.Add(1)
			return nil
		default:
			q.rejected.Add(1)
			return ErrFull
		}
	case DropOldest:
		for {
			select {
			case q.ch <- v:
				q.accepted.Add(1)
				return nil
			default:
			}
			select {
			case <-q.ch:
				q.evicted.Add(1)
			default:
			}
		}
	default: // Block
		select {
		case q.ch <- v:
			q.accepted.Add(1)
			return nil
		case <-q.closed:
			return ErrClosed
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// Take removes and returns the oldest item, waiting if necessary.
func (q *Queue[T]) Take(ctx context.Context) (T, error) {
	var zero T
	select {
	case v := <-q.ch:
		q.taken.Add(1)
		return v, nil
	default:
	}
	select {
	case v := <-q.ch:
		q.taken.Add(1)
		return v, nil
	case <-q.closed:
		select {
		case v := <-q.ch:
			q.taken.Add(1)
			return v, nil
		default:
			return zero, ErrClosed
		}
	case <-ctx.Done():
		return zero, ctx.Err()
	}
}

// Len is the number of queued items.
func (q *Queue[T]) Len() int { return len(q.ch) }

// Close closes the queue. Idempotent.
func (q *Queue[T]) Close() { q.once.Do(func() { close(q.closed) }) }

// Stats returns the cumulative counters.
func (q *Queue[T]) Stats() Stats {
	return Stats{Accepted: q.accepted.Load(), Rejected: q.rejected.Load(), Evicted: q.evicted.Load(), Taken: q.taken.Load()}
}

// Priority of a request; lower numbers are more important.
type Priority int

// Priorities.
const (
	Critical Priority = iota
	Standard
	Bulk
)

var defaultThresholds = map[Priority]float64{Critical: 1.0, Standard: 0.8, Bulk: 0.5}

// PriorityQueue admits low priorities only while the queue is short.
type PriorityQueue[T any] struct {
	mu     sync.Mutex
	cap    int
	limits map[Priority]int
	lists  map[Priority][]T
	n      int
	shed   map[Priority]int64
}

// NewPriorityQueue returns a PriorityQueue. thresholds may be nil.
func NewPriorityQueue[T any](capacity int, thresholds map[Priority]float64) *PriorityQueue[T] {
	if capacity < 1 {
		capacity = 1
	}
	q := &PriorityQueue[T]{cap: capacity, limits: map[Priority]int{}, lists: map[Priority][]T{}, shed: map[Priority]int64{}}
	for p, th := range defaultThresholds {
		if v, ok := thresholds[p]; ok {
			th = v
		}
		q.limits[p] = admitLimit(capacity, th)
	}
	for p, th := range thresholds {
		if _, known := defaultThresholds[p]; !known {
			q.limits[p] = admitLimit(capacity, th)
		}
	}
	return q
}

func admitLimit(capacity int, th float64) int {
	n := int(math.Floor(th * float64(capacity)))
	if n < 1 {
		n = 1
	}
	if n > capacity {
		n = capacity
	}
	return n
}

// Offer admits v if the queue is shorter than p's limit, else ErrShed.
func (q *PriorityQueue[T]) Offer(p Priority, v T) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	limit, ok := q.limits[p]
	if !ok {
		limit = q.limits[Bulk]
	}
	if q.n >= limit {
		q.shed[p]++
		return ErrShed
	}
	q.lists[p] = append(q.lists[p], v)
	q.n++
	return nil
}

// Take removes the most important, oldest item.
func (q *PriorityQueue[T]) Take() (v T, p Priority, ok bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	prios := make([]Priority, 0, len(q.lists))
	for pr, l := range q.lists {
		if len(l) > 0 {
			prios = append(prios, pr)
		}
	}
	if len(prios) == 0 {
		return v, 0, false
	}
	sort.Slice(prios, func(i, j int) bool { return prios[i] < prios[j] })
	p = prios[0]
	v = q.lists[p][0]
	q.lists[p] = q.lists[p][1:]
	q.n--
	return v, p, true
}

// Len is the number of queued items.
func (q *PriorityQueue[T]) Len() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.n
}

// Shed returns a copy of the per-priority shed counters.
func (q *PriorityQueue[T]) Shed() map[Priority]int64 {
	q.mu.Lock()
	defer q.mu.Unlock()
	out := make(map[Priority]int64, len(q.shed))
	for k, v := range q.shed {
		out[k] = v
	}
	return out
}

// CreditGate is credit-based flow control.
type CreditGate struct {
	mu      sync.Mutex
	credits int
	wake    chan struct{} // closed and replaced on every Grant
}

// NewCreditGate returns a gate with initial credits.
func NewCreditGate(initial int) *CreditGate {
	if initial < 0 {
		initial = 0
	}
	return &CreditGate{credits: initial, wake: make(chan struct{})}
}

// Grant adds n credits.
func (g *CreditGate) Grant(n int) {
	if n <= 0 {
		return
	}
	g.mu.Lock()
	g.credits += n
	close(g.wake)
	g.wake = make(chan struct{})
	g.mu.Unlock()
}

// TryAcquire consumes a credit if one is available.
func (g *CreditGate) TryAcquire() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.credits > 0 {
		g.credits--
		return true
	}
	return false
}

// Acquire waits for a credit and consumes it.
func (g *CreditGate) Acquire(ctx context.Context) error {
	for {
		g.mu.Lock()
		if g.credits > 0 {
			g.credits--
			g.mu.Unlock()
			return nil
		}
		wake := g.wake
		g.mu.Unlock()
		select {
		case <-wake:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// Available is the number of unused credits.
func (g *CreditGate) Available() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.credits
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
	servers := cfg.Servers
	if servers < 1 {
		servers = 1
	}
	var res SimResult
	if cfg.ArrivalRate <= 0 || cfg.ServiceTime <= 0 {
		return res
	}
	free := make([]time.Duration, servers) // when each server becomes free
	type iv struct{ arrive, start, done time.Duration }
	var served []iv
	var starts []time.Duration // service start of every admitted request, non-decreasing
	head := 0                  // starts[:head] have started by "now"
	var totalWait, totalSojourn time.Duration
	for i := 0; ; i++ {
		arrive := time.Duration(float64(i) / cfg.ArrivalRate * float64(time.Second))
		if arrive >= cfg.Duration {
			break
		}
		res.Arrived++
		for head < len(starts) && starts[head] <= arrive {
			head++
		}
		waiting := len(starts) - head
		if cfg.QueueCap > 0 && waiting >= cfg.QueueCap {
			res.Dropped++
			continue
		}
		s := 0
		for j := range free {
			if free[j] < free[s] {
				s = j
			}
		}
		start := arrive
		if free[s] > start {
			start = free[s]
		}
		done := start + cfg.ServiceTime
		free[s] = done
		queued := waiting
		if start > arrive {
			queued++ // this request has to wait too
		}
		if queued > res.MaxQueue {
			res.MaxQueue = queued
		}
		starts = append(starts, start)
		served = append(served, iv{arrive, start, done})
		wait := start - arrive
		totalWait += wait
		totalSojourn += done - arrive
		if wait > res.MaxWait {
			res.MaxWait = wait
		}
		if done > res.End {
			res.End = done
		}
	}
	res.Served = len(served)
	if res.Served == 0 {
		return res
	}
	res.MeanWait = totalWait / time.Duration(res.Served)
	res.MeanSojourn = totalSojourn / time.Duration(res.Served)

	// Time-average number in system from events, independent of the means above.
	type ev struct {
		t time.Duration
		d int
	}
	evs := make([]ev, 0, 2*len(served))
	for _, s := range served {
		evs = append(evs, ev{s.arrive, +1}, ev{s.done, -1})
	}
	sort.Slice(evs, func(i, j int) bool {
		if evs[i].t != evs[j].t {
			return evs[i].t < evs[j].t
		}
		return evs[i].d < evs[j].d
	})
	var area float64
	n, last := 0, time.Duration(0)
	for _, e := range evs {
		area += float64(n) * float64(e.t-last)
		n += e.d
		last = e.t
	}
	res.MeanInSystem = area / float64(res.End)
	return res
}
