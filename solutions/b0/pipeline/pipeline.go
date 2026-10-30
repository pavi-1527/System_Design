// Package pipeline provides generic, cancellable pipeline stages.
//
// Lab B0 Unit 2 §3. Every stage follows the same contract:
//
//   - A stage owns its output channel: it is the only sender and it closes
//     the channel when it is done (input exhausted, or ctx cancelled).
//   - Every send and every receive is inside a select with <-ctx.Done(), so
//     a stage never blocks forever once ctx is cancelled.
//   - A consumer that stops reading early MUST cancel ctx; then every
//     goroutine in the pipeline exits promptly. That is the whole leak story.
package pipeline

import (
	"context"
	"sync"
)

// Generate emits items in order on the returned channel, then closes it.
// It stops early (and closes) if ctx is cancelled.
func Generate[T any](ctx context.Context, items ...T) <-chan T {
	out := make(chan T)
	go func() {
		defer close(out)
		for _, it := range items {
			select {
			case out <- it:
			case <-ctx.Done():
				return
			}
		}
	}()
	return out
}

// Map applies fn to every value from in using `workers` goroutines (minimum
// 1) and emits results in COMPLETION order: output order is NOT the input
// order. It closes the output when in is closed and all workers are done,
// or promptly after ctx is cancelled. fn receives ctx and should honour it.
func Map[In, Out any](ctx context.Context, in <-chan In, workers int, fn func(context.Context, In) Out) <-chan Out {
	if workers < 1 {
		workers = 1
	}
	out := make(chan Out)
	var wg sync.WaitGroup
	wg.Add(workers)
	for i := 0; i < workers; i++ {
		go func() {
			defer wg.Done()
			for {
				var v In
				var ok bool
				select {
				case v, ok = <-in:
					if !ok {
						return
					}
				case <-ctx.Done():
					return
				}
				r := fn(ctx, v)
				select {
				case out <- r:
				case <-ctx.Done():
					return
				}
			}
		}()
	}
	go func() {
		wg.Wait() // the closer: only after every sender has stopped
		close(out)
	}()
	return out
}

// MapOrdered is Map, but results are emitted in INPUT order. Up to
// `workers` items are in flight at once; a slow item holds back the ones
// behind it (head-of-line blocking), which bounds the reorder buffer to
// `workers` results.
//
// Design: a dispatcher reads in and, for each item, pushes a one-shot result
// channel (a "future") onto an ordered queue and hands the job to a worker.
// The collector reads futures in order and forwards each result.
func MapOrdered[In, Out any](ctx context.Context, in <-chan In, workers int, fn func(context.Context, In) Out) <-chan Out {
	if workers < 1 {
		workers = 1
	}
	type job struct {
		v   In
		res chan Out // buffered(1): a worker's send never blocks, so workers never leak
	}
	out := make(chan Out)
	jobs := make(chan job)
	futures := make(chan chan Out, workers) // the bounded, ordered window

	// Dispatcher.
	go func() {
		defer close(futures)
		defer close(jobs)
		for {
			var v In
			var ok bool
			select {
			case v, ok = <-in:
				if !ok {
					return
				}
			case <-ctx.Done():
				return
			}
			res := make(chan Out, 1)
			select {
			case futures <- res: // blocks when `workers` results are pending: backpressure
			case <-ctx.Done():
				return
			}
			select {
			case jobs <- job{v, res}:
			case <-ctx.Done():
				return
			}
		}
	}()

	// Workers.
	for i := 0; i < workers; i++ {
		go func() {
			for j := range jobs {
				j.res <- fn(ctx, j.v)
			}
		}()
	}

	// Collector.
	go func() {
		defer close(out)
		for res := range futures {
			var r Out
			select {
			case r = <-res:
			case <-ctx.Done():
				return
			}
			select {
			case out <- r:
			case <-ctx.Done():
				return
			}
		}
	}()
	return out
}

// Merge fans in: it forwards every value from every input to one output
// channel (order across inputs is unspecified) and closes the output once
// all inputs are closed, or promptly after ctx is cancelled.
func Merge[T any](ctx context.Context, ins ...<-chan T) <-chan T {
	out := make(chan T)
	var wg sync.WaitGroup
	wg.Add(len(ins))
	for _, c := range ins {
		go func(c <-chan T) {
			defer wg.Done()
			for {
				select {
				case v, ok := <-c:
					if !ok {
						return
					}
					select {
					case out <- v:
					case <-ctx.Done():
						return
					}
				case <-ctx.Done():
					return
				}
			}
		}(c)
	}
	go func() {
		wg.Wait()
		close(out)
	}()
	return out
}
