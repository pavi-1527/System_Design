package logx

import (
	"context"
	"errors"
	"time"
)

// Stage 2 (≈ 20 min): an asynchronous, batching sink with a backpressure
// policy. A slow destination (a database, a log shipper) must not slow the
// code that logs.

// ErrClosed is returned by Write and Flush on an AsyncSink that has been closed.
var ErrClosed = errors.New("logx: sink closed")

// BatchSink consumes entries in batches (for example one INSERT per batch).
// The slice passed to WriteBatch is owned by the callee: AsyncSink never
// touches it again, never passes the same slice twice, and never reuses its
// backing array.
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

// AsyncConfig configures an AsyncSink. QueueSize, MaxBatch and Linger must be > 0.
type AsyncConfig struct {
	// QueueSize bounds how many entries may wait in the queue. The batch that
	// is being written right now is not counted.
	QueueSize int
	// MaxBatch is the largest batch handed to WriteBatch.
	MaxBatch int
	// Linger is how long a partial batch may wait for more entries, measured
	// from the first entry that is waiting. A full batch is written at once.
	Linger time.Duration
	// Policy is the overflow policy. The zero value is DropNewest.
	Policy Policy
	// NewTimer is a clock hook for tests: it must return a channel that
	// receives once after d, and a stop function. nil means time.NewTimer.
	// Create the linger timer when entries start waiting, and stop it when
	// they are flushed: a stale timer must never flush a later batch early.
	NewTimer func(d time.Duration) (c <-chan time.Time, stop func() bool)
}

// Stats is a snapshot of an AsyncSink's counters.
//
// Invariant, once the sink is quiescent (after Flush or Close has returned):
//
//	Received == Written + Dropped + Failed
type Stats struct {
	Received uint64 // entries handed to Write while the sink was open (a rejected Write is not counted)
	Written  uint64 // entries in batches that WriteBatch accepted
	Dropped  uint64 // discarded by the overflow policy, or abandoned at a Close deadline
	Failed   uint64 // lost because WriteBatch failed (Stage 3 adds retries before giving up)
	Retries  uint64 // retry attempts made (Stage 3)
	Batches  uint64 // WriteBatch calls that succeeded
	Queued   int    // entries waiting in the queue right now
}

// AsyncSink is a Sink that queues entries and writes them to a BatchSink from
// one background goroutine (the only goroutine that calls the destination).
// It is safe for concurrent use.
type AsyncSink struct {
	// TODO(lab): destination, config, a mutex-protected queue and counters,
	// a way to wake the worker, and a way to tell it to stop.
}

// NewAsync validates cfg (dst != nil, sizes and Linger > 0, known Policy) and
// starts the background goroutine. In Stage 2 a failed batch is simply counted
// as Failed; Stage 3 adds NewAsyncWithFailure and NewAsync becomes a thin
// wrapper around it.
func NewAsync(dst BatchSink, cfg AsyncConfig) (*AsyncSink, error) {
	// TODO(lab)
	_, _ = dst, cfg
	return nil, errNotImplemented
}

// Write queues e according to the overflow policy. It returns ErrClosed after
// Close and otherwise nil: a dropped entry is counted in Stats, not reported
// as an error to every caller. With Block it waits for space, and Close
// releases blocked writers with ErrClosed.
func (a *AsyncSink) Write(e Entry) error {
	// TODO(lab)
	_ = e
	return errNotImplemented
}

// Flush blocks until every entry accepted before the call has been written,
// dropped or failed, or ctx is done (it then returns ctx.Err()). It does not
// wait for the linger timer. It returns ErrClosed if the sink is closed.
func (a *AsyncSink) Flush(ctx context.Context) error {
	// TODO(lab)
	_ = ctx
	return errNotImplemented
}

// Stats returns a snapshot of the counters.
func (a *AsyncSink) Stats() Stats {
	// TODO(lab)
	return Stats{}
}

// Close stops accepting entries (Write returns ErrClosed from now on, and
// writers blocked by the Block policy are released), drains the queue to the
// destination without waiting for the linger timer, and waits for the
// background goroutine to exit. It is idempotent and safe to call
// concurrently; every call waits for the drain. Stage 2: if ctx is done first,
// return an error satisfying errors.Is(err, ctx.Err()). Stage 3 makes that
// path stop the worker too and report what was lost.
func (a *AsyncSink) Close(ctx context.Context) error {
	// TODO(lab)
	_ = ctx
	return errNotImplemented
}
