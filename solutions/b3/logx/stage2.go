package logx

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

// ErrClosed is returned by Write and Flush on an AsyncSink that has been closed.
var ErrClosed = errors.New("logx: sink closed")

// BatchSink consumes entries in batches (for example one INSERT per batch).
// The slice passed to WriteBatch is owned by the callee: AsyncSink never
// touches it again and never passes the same slice twice.
type BatchSink interface {
	WriteBatch(batch []Entry) error
}

// Policy decides what AsyncSink.Write does when the queue is full. The zero
// value is DropNewest: a logging library must never take the application down
// because its sink is slow.
type Policy int

const (
	// DropNewest discards the entry being written and counts it as Dropped.
	DropNewest Policy = iota
	// DropOldest discards the oldest queued entry to make room.
	DropOldest
	// Block makes Write wait for space (or for Close). Opt-in only.
	Block
)

// AsyncConfig configures an AsyncSink. All sizes and durations must be > 0.
type AsyncConfig struct {
	// QueueSize bounds how many entries may wait in the queue. The batch that
	// is being written right now is not counted.
	QueueSize int
	// MaxBatch is the largest batch handed to WriteBatch.
	MaxBatch int
	// Linger is how long a partial batch may wait for more entries, measured
	// from the first entry that is waiting.
	Linger time.Duration
	// Policy is the overflow policy. The zero value is DropNewest.
	Policy Policy
	// NewTimer is a clock hook for tests: it must return a channel that
	// receives once after d, and a stop function. nil means time.NewTimer.
	NewTimer func(d time.Duration) (c <-chan time.Time, stop func() bool)
}

// Stats is a snapshot of an AsyncSink's counters.
//
// Invariant, once the sink is quiescent (after Flush or Close has returned):
//
//	Received == Written + Dropped + Failed
type Stats struct {
	Received uint64 // entries handed to Write while the sink was open
	Written  uint64 // entries in batches that WriteBatch accepted
	Dropped  uint64 // discarded by the overflow policy, or abandoned at a Close deadline
	Failed   uint64 // lost because WriteBatch kept failing (after any retries)
	Retries  uint64 // retry attempts made (Stage 3)
	Batches  uint64 // WriteBatch calls that succeeded
	Queued   int    // entries waiting in the queue right now
}

// AsyncSink is a Sink that queues entries and writes them to a BatchSink from
// one background goroutine, so a slow destination never slows the caller (with
// a Drop policy). It is safe for concurrent use.
type AsyncSink struct {
	dst BatchSink
	cfg AsyncConfig
	fp  FailurePolicy

	mu      sync.Mutex
	notFull *sync.Cond // Block writers wait here
	queue   []Entry
	closed  bool
	abandon bool
	lost    uint64
	stats   Stats

	wake    chan struct{}      // cap 1: something changed for the worker
	flushCh chan chan struct{} // Flush requests
	done    chan struct{}      // closed when the worker has exited
	wctx    context.Context    // cancelled when Close gives up; interrupts retry sleeps
	wcancel context.CancelFunc
}

// NewAsync validates cfg and starts the background goroutine. Failures are not
// retried (see NewAsyncWithFailure).
func NewAsync(dst BatchSink, cfg AsyncConfig) (*AsyncSink, error) {
	return NewAsyncWithFailure(dst, cfg, FailurePolicy{})
}

func newAsync(dst BatchSink, cfg AsyncConfig, fp FailurePolicy) (*AsyncSink, error) {
	switch {
	case dst == nil:
		return nil, errors.New("logx: nil BatchSink")
	case cfg.QueueSize <= 0 || cfg.MaxBatch <= 0 || cfg.Linger <= 0:
		return nil, fmt.Errorf("logx: QueueSize, MaxBatch and Linger must be > 0 (got %d, %d, %v)", cfg.QueueSize, cfg.MaxBatch, cfg.Linger)
	case cfg.Policy < DropNewest || cfg.Policy > Block:
		return nil, fmt.Errorf("logx: unknown Policy %d", cfg.Policy)
	}
	if cfg.NewTimer == nil {
		cfg.NewTimer = func(d time.Duration) (<-chan time.Time, func() bool) {
			t := time.NewTimer(d)
			return t.C, t.Stop
		}
	}
	a := &AsyncSink{
		dst: dst, cfg: cfg, fp: fp,
		wake:    make(chan struct{}, 1),
		flushCh: make(chan chan struct{}),
		done:    make(chan struct{}),
	}
	a.notFull = sync.NewCond(&a.mu)
	a.wctx, a.wcancel = context.WithCancel(context.Background())
	go a.run()
	return a, nil
}

func (a *AsyncSink) signal() {
	select {
	case a.wake <- struct{}{}:
	default:
	}
}

// Write queues e according to the overflow policy. It returns ErrClosed after
// Close and otherwise nil: a dropped entry is counted in Stats, not reported
// as an error to every caller. With the Block policy it waits for space; Close
// wakes blocked writers with ErrClosed.
func (a *AsyncSink) Write(e Entry) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	for {
		if a.closed {
			return ErrClosed
		}
		if len(a.queue) < a.cfg.QueueSize {
			break
		}
		switch a.cfg.Policy {
		case Block:
			a.notFull.Wait()
			continue
		case DropNewest:
			a.stats.Received++
			a.stats.Dropped++
			return nil
		case DropOldest:
			a.queue = a.queue[1:]
			a.stats.Dropped++
		}
		break
	}
	a.stats.Received++
	a.queue = append(a.queue, e)
	a.signal()
	return nil
}

// Flush blocks until every entry accepted before the call has been written,
// dropped or failed, or ctx is done (it then returns ctx.Err()). It returns
// ErrClosed if the sink is closed.
func (a *AsyncSink) Flush(ctx context.Context) error {
	a.mu.Lock()
	closed := a.closed
	a.mu.Unlock()
	if closed {
		return ErrClosed
	}
	req := make(chan struct{})
	select {
	case a.flushCh <- req:
	case <-a.done:
		return ErrClosed
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case <-req:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Stats returns a snapshot of the counters.
func (a *AsyncSink) Stats() Stats {
	a.mu.Lock()
	defer a.mu.Unlock()
	s := a.stats
	s.Queued = len(a.queue)
	return s
}

// Close stops accepting entries (Write returns ErrClosed from now on, and
// writers blocked by the Block policy are released), drains the queue to the
// destination, and waits for the background goroutine to exit. It is
// idempotent and safe to call concurrently. If ctx is done first, Close stops
// waiting, tells the worker to abandon whatever is still queued (counted as
// Dropped), and returns an *UnflushedError that wraps ctx.Err().
func (a *AsyncSink) Close(ctx context.Context) error {
	a.mu.Lock()
	if !a.closed {
		a.closed = true
		a.notFull.Broadcast()
	}
	a.mu.Unlock()
	a.signal()

	select {
	case <-a.done:
		return a.closeResult()
	case <-ctx.Done():
	}
	a.mu.Lock()
	a.abandon = true
	s := a.stats
	pending := s.Received - s.Written - s.Dropped - s.Failed
	a.mu.Unlock()
	a.wcancel()
	a.signal()
	if pending == 0 {
		<-a.done
		return a.closeResult()
	}
	return &UnflushedError{Remaining: pending, Err: ctx.Err()}
}

func (a *AsyncSink) closeResult() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.lost > 0 {
		return &UnflushedError{Remaining: a.lost}
	}
	return nil
}

type takeState int

const (
	takeBatch takeState = iota // a batch was taken
	takeWait                   // entries are waiting for more company or the linger timer
	takeIdle                   // the queue is empty
	takeQuit                   // closed and drained, or abandoned
)

// take removes the next batch from the queue. force means "flush whatever is
// queued even if the batch is not full" (linger expired or Flush pending).
func (a *AsyncSink) take(force bool) ([]Entry, takeState) {
	a.mu.Lock()
	defer a.mu.Unlock()
	n := len(a.queue)
	if a.abandon {
		a.stats.Dropped += uint64(n)
		a.lost += uint64(n)
		a.queue = nil
		return nil, takeQuit
	}
	if n == 0 {
		if a.closed {
			return nil, takeQuit
		}
		return nil, takeIdle
	}
	if n < a.cfg.MaxBatch && !force && !a.closed {
		return nil, takeWait
	}
	n = min(n, a.cfg.MaxBatch)
	batch := a.queue[:n:n] // the worker now owns this part of the backing array
	a.queue = a.queue[n:]
	if len(a.queue) == 0 {
		a.queue = nil
	}
	a.notFull.Broadcast()
	return batch, takeBatch
}

// run is the only goroutine that calls the destination. It owns the linger
// timer and the list of Flush callers.
func (a *AsyncSink) run() {
	defer close(a.done)
	var (
		timerC  <-chan time.Time
		stopT   func() bool
		due     bool
		waiters []chan struct{}
	)
	stopTimer := func() {
		if stopT != nil {
			stopT()
		}
		stopT, timerC = nil, nil
	}
	defer func() {
		stopTimer()
		for _, w := range waiters {
			close(w)
		}
	}()
	for {
	drain:
		for {
			batch, st := a.take(due || len(waiters) > 0)
			switch st {
			case takeBatch:
				a.writeBatch(batch)
			case takeWait:
				if timerC == nil {
					timerC, stopT = a.cfg.NewTimer(a.cfg.Linger)
				}
				break drain
			case takeIdle:
				stopTimer()
				due = false
				for _, w := range waiters {
					close(w)
				}
				waiters = nil
				break drain
			case takeQuit:
				return
			}
		}
		select {
		case <-a.wake:
		case <-timerC:
			stopT, timerC = nil, nil
			due = true
		case w := <-a.flushCh:
			waiters = append(waiters, w)
		}
	}
}
