// Package batcher groups items into batches flushed by size or by time.
//
// Lab B0 Unit 1 §2. Think of an audit-log writer: every request produces an
// audit event, and one INSERT per event at 2,000 req/s is 2,000 round trips
// per second. Batching 100 events per INSERT cuts that to ≈ 20, at the cost of
// up to Linger of extra latency for the first event in each batch.
//
// Suggested ownership model: one goroutine (the run loop) owns the
// in-progress batch and the timer. Add hands items to it over a channel;
// nothing else touches the batch, so no mutex is needed. Think hard about
// whether that channel should be buffered (the tests will tell you).
package batcher

import (
	"context"
	"errors"
	"time"
)

// ErrClosed is returned by Add after Close has been called.
var ErrClosed = errors.New("batcher: closed")

var errNotImplemented = errors.New("batcher: not implemented")

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
	// TODO(lab): fields (config, flush func, channels, a sync.Once for Close...).
	cfg   Config
	flush func([]T)
}

// New validates cfg (MaxItems > 0, Linger > 0, flush != nil) and starts the
// batcher's run loop.
func New[T any](cfg Config, flush func([]T)) (*Batcher[T], error) {
	// TODO(lab): validate, default cfg.NewTimer to a time.NewTimer wrapper,
	// start the run loop goroutine.
	_, _ = cfg, flush
	return nil, errNotImplemented
}

// Add hands item to the batcher. It blocks while the run loop is busy (for
// example inside a slow flush): that is backpressure, and ctx bounds how long
// the caller is willing to wait. It returns ErrClosed after Close.
//
// Contract: when Add returns nil, the item is already part of the current
// batch and WILL be flushed (by size, by linger, or by Close).
func (b *Batcher[T]) Add(ctx context.Context, item T) error {
	// TODO(lab)
	_, _ = ctx, item
	return errNotImplemented
}

// Close stops accepting items, flushes whatever is buffered, waits for that
// final flush to return, and then returns. It is safe to call Close more
// than once and from several goroutines; every call waits for the final
// flush.
func (b *Batcher[T]) Close() error {
	// TODO(lab)
	return errNotImplemented
}

// TODO(lab): func (b *Batcher[T]) run() — the loop that owns the batch.
// Hints: a nil channel in a select blocks forever, which is exactly what you
// want for "no timer running"; stop the timer when a batch flushes by size;
// never reuse a slice you have handed to flush.
