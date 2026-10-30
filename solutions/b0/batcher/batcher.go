// Package batcher groups items into batches flushed by size or by time.
//
// Lab B0 Unit 1 §2. Think of an audit-log writer: every request produces an
// audit event, and one INSERT per event at 2,000 req/s is 2,000 round trips
// per second. Batching 100 events per INSERT cuts that to ≈ 20, at the cost of
// up to Linger of extra latency for the first event in each batch.
//
// Ownership model (the point of the lab): one goroutine (the run loop) owns
// the in-progress batch and the timer. Add hands items to it over an
// unbuffered channel; nothing else touches the batch, so no mutex is needed.
package batcher

import (
	"context"
	"errors"
	"sync"
	"time"
)

// ErrClosed is returned by Add after Close has been called.
var ErrClosed = errors.New("batcher: closed")

// Config controls when a batch is flushed.
type Config struct {
	// MaxItems flushes a batch as soon as it holds this many items. Must be > 0.
	MaxItems int
	// Linger flushes a non-empty batch this long after its FIRST item was
	// added, even if it is not full. Must be > 0.
	Linger time.Duration
	// NewTimer is an optional clock hook for tests. It must return a channel
	// that receives once after d, and a stop function. nil means time.NewTimer.
	NewTimer func(d time.Duration) (c <-chan time.Time, stop func() bool)
}

// Batcher accumulates items of type T and calls flush with each batch.
// flush is always called from a single goroutine, never concurrently, in
// the order the batches were formed. The slice passed to flush is owned by
// flush: the Batcher never touches it again.
type Batcher[T any] struct {
	cfg       Config
	flush     func([]T)
	in        chan T        // unbuffered: a successful send means the run loop has the item
	quit      chan struct{} // closed by Close
	done      chan struct{} // closed when the run loop has flushed the remainder and exited
	closeOnce sync.Once
}

// New validates cfg and starts the batcher's run loop.
func New[T any](cfg Config, flush func([]T)) (*Batcher[T], error) {
	if cfg.MaxItems <= 0 {
		return nil, errors.New("batcher: MaxItems must be > 0")
	}
	if cfg.Linger <= 0 {
		return nil, errors.New("batcher: Linger must be > 0")
	}
	if flush == nil {
		return nil, errors.New("batcher: flush func is required")
	}
	if cfg.NewTimer == nil {
		cfg.NewTimer = func(d time.Duration) (<-chan time.Time, func() bool) {
			t := time.NewTimer(d)
			return t.C, t.Stop
		}
	}
	b := &Batcher[T]{
		cfg:   cfg,
		flush: flush,
		in:    make(chan T),
		quit:  make(chan struct{}),
		done:  make(chan struct{}),
	}
	go b.run()
	return b, nil
}

// Add hands item to the batcher. It blocks while the run loop is busy (for
// example inside a slow flush): that is backpressure, and ctx bounds how long
// the caller is willing to wait. It returns ErrClosed after Close.
//
// Contract: when Add returns nil, the item is already part of the current
// batch and WILL be flushed (by size, by linger, or by Close).
func (b *Batcher[T]) Add(ctx context.Context, item T) error {
	// Why this is enough: `in` is unbuffered, so the send succeeds only at
	// the instant the run loop receives the item. Close closes quit BEFORE
	// the run loop can observe it, and closing wakes every Add parked here
	// with the quit case. So an Add either hands its item to a live run loop
	// (which will flush it) or returns ErrClosed; there is no third outcome.
	// With a BUFFERED `in` that argument breaks (see the Break-it exercise).
	select {
	case b.in <- item:
		return nil
	case <-b.quit:
		return ErrClosed
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Close stops accepting items, flushes whatever is buffered, waits for that
// final flush to return, and then returns. It is safe to call Close more
// than once and from several goroutines; every call waits for the final
// flush.
func (b *Batcher[T]) Close() error {
	b.closeOnce.Do(func() { close(b.quit) })
	<-b.done
	return nil
}

func (b *Batcher[T]) run() {
	defer close(b.done)

	var (
		batch  []T
		timerC <-chan time.Time // nil when no timer is running: a nil channel blocks forever in select
		stop   func() bool
	)
	flush := func() {
		if stop != nil {
			stop()
			stop, timerC = nil, nil
		}
		if len(batch) == 0 {
			return
		}
		out := batch
		batch = nil // hand ownership of out to flush; never reuse its backing array
		b.flush(out)
	}
	add := func(it T) {
		batch = append(batch, it)
		if len(batch) == 1 {
			timerC, stop = b.cfg.NewTimer(b.cfg.Linger)
		}
		if len(batch) >= b.cfg.MaxItems {
			flush()
		}
	}

	for {
		select {
		case it := <-b.in:
			add(it)
		case <-timerC:
			stop, timerC = nil, nil // already fired
			flush()
		case <-b.quit:
			// No drain needed: with an unbuffered `in`, every item whose Add
			// returned nil has already been received above.
			flush()
			return
		}
	}
}
