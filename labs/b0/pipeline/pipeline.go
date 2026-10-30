// Package pipeline provides generic, cancellable pipeline stages.
//
// Lab B0 Unit 2 §3. Every stage must follow the same contract:
//
//   - A stage owns its output channel: it is the only sender and it closes
//     the channel when it is done (input exhausted, or ctx cancelled).
//   - Every send and every receive is inside a select with <-ctx.Done(), so
//     a stage never blocks forever once ctx is cancelled.
//   - A consumer that stops reading early MUST cancel ctx; then every
//     goroutine in the pipeline exits promptly. That is the whole leak story.
//
// Stubs return an already-closed channel so tests fail instead of hanging.
package pipeline

import "context"

func closed[T any]() <-chan T {
	c := make(chan T)
	close(c)
	return c
}

// Generate emits items in order on the returned channel, then closes it.
// It stops early (and closes) if ctx is cancelled.
func Generate[T any](ctx context.Context, items ...T) <-chan T {
	// TODO(lab): one goroutine; select on send vs ctx.Done(); defer close.
	_, _ = ctx, items
	return closed[T]()
}

// Map applies fn to every value from in using `workers` goroutines (minimum
// 1) and emits results in COMPLETION order: output order is NOT the input
// order. It closes the output when in is closed and all workers are done,
// or promptly after ctx is cancelled. fn receives ctx and should honour it.
func Map[In, Out any](ctx context.Context, in <-chan In, workers int, fn func(context.Context, In) Out) <-chan Out {
	// TODO(lab): N workers ranging over in (with ctx checks), plus a closer
	// goroutine that closes the output after a WaitGroup says every worker
	// (every sender) has stopped.
	_, _, _, _ = ctx, in, workers, fn
	return closed[Out]()
}

// MapOrdered is Map, but results are emitted in INPUT order. Up to
// `workers` items are in flight at once; a slow item holds back the ones
// behind it (head-of-line blocking), which bounds the reorder buffer.
//
// Hint (one good design, not the only one): a dispatcher reads in and, for
// each item, pushes a one-shot result channel (a "future", buffered 1) onto
// an ordered queue `chan chan Out` with capacity `workers`, and hands the job
// to a worker. A collector reads futures in order and forwards each result.
func MapOrdered[In, Out any](ctx context.Context, in <-chan In, workers int, fn func(context.Context, In) Out) <-chan Out {
	// TODO(lab)
	_, _, _, _ = ctx, in, workers, fn
	return closed[Out]()
}

// Merge fans in: it forwards every value from every input to one output
// channel (order across inputs is unspecified) and closes the output once
// all inputs are closed, or promptly after ctx is cancelled.
func Merge[T any](ctx context.Context, ins ...<-chan T) <-chan T {
	// TODO(lab): one forwarding goroutine per input + a closer goroutine.
	_, _ = ctx, ins
	return closed[T]()
}
