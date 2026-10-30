// Package fanout calls several vendors concurrently and gathers their
// answers under explicit time and concurrency budgets.
//
// Lab B0 Unit 2 §4 (capstone). The scenario: a credit-union member asks for
// an FX quote; you ask every configured FX vendor and show the best rate.
// Vendors are slow, flaky, and some adapters ignore context cancellation.
// The caller gives you an overall deadline (ctx); each vendor also gets its
// own timeout; you must not hammer vendors with unbounded concurrency; and
// you must choose what "done" means: fail fast on the first error, or
// return whatever you have (partial results).
package fanout

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

// Request is what we ask every vendor.
type Request struct {
	From, To    string // ISO 4217 currency codes, e.g. "USD", "EUR"
	AmountMinor int64  // amount in minor units (cents); never float for money
}

// Quote is one vendor's answer.
type Quote struct {
	Vendor     string
	RateMicros int64 // exchange rate × 1e6, e.g. 0.92 EUR per USD = 920000
}

// Vendor is one quote provider (an adapter around an HTTP API, typically).
// Implementations SHOULD return promptly when ctx is done, but QuoteAll must
// not rely on that.
type Vendor interface {
	Name() string
	Quote(ctx context.Context, req Request) (Quote, error)
}

// Policy decides how QuoteAll reacts to a vendor error.
type Policy int

const (
	// BestEffort waits for every vendor (each bounded by its timeout and
	// the overall deadline) and succeeds if at least MinSuccesses quotes
	// arrived. Use when any good quote is useful (show the best rate).
	BestEffort Policy = iota
	// FailFast cancels all outstanding calls on the first vendor error and
	// returns that error. Use when you need ALL answers (e.g. a compliance
	// check that must pass at every provider).
	FailFast
)

// Options are the budgets for one QuoteAll call.
type Options struct {
	// PerVendorTimeout bounds each vendor call, measured from when THAT
	// call starts (not from when QuoteAll starts). 0 means no per-vendor
	// timeout; the overall ctx deadline still applies.
	PerVendorTimeout time.Duration
	// MaxConcurrency bounds how many vendor calls are in flight at once.
	// <= 0 means no bound.
	MaxConcurrency int
	// Policy: BestEffort (default) or FailFast.
	Policy Policy
	// MinSuccesses is the BestEffort success threshold; <= 0 means 1.
	MinSuccesses int
}

// Result is the outcome for one vendor. Exactly one of Quote/Err is
// meaningful: Err == nil means Quote is valid.
type Result struct {
	Vendor string
	Quote  Quote
	Err    error
}

var (
	// ErrInsufficientQuotes: BestEffort got fewer than MinSuccesses quotes.
	ErrInsufficientQuotes = errors.New("fanout: insufficient quotes")
	// ErrVendorPanic: a vendor adapter panicked; the panic was contained.
	ErrVendorPanic = errors.New("fanout: vendor panicked")
)

// QuoteAll asks every vendor concurrently and returns one Result per vendor,
// index-aligned with vendors (results[i] is vendors[i]), always — even when
// it also returns an error.
//
// Guarantees:
//   - It returns by ≈ the earliest of: all vendors answered; ctx's deadline;
//     (FailFast) the first vendor error. A vendor that ignores ctx is
//     abandoned: its Result gets the context error and its late answer is
//     discarded.
//   - A timed-out vendor's Err satisfies errors.Is(err, context.DeadlineExceeded);
//     a vendor cancelled because a sibling failed (FailFast) satisfies
//     errors.Is(err, context.Canceled).
//   - No vendor is called after ctx is done.
//   - A panicking vendor yields ErrVendorPanic instead of crashing the process.
//
// Concurrency accounting choice (documented, not tested): a MaxConcurrency
// slot is released when the vendor call ACTUALLY returns, not when we stop
// waiting for it. An abandoned call still holds a socket and still counts
// against the vendor's limits, so releasing early would let real concurrency
// exceed the bound. The cost: a vendor that hangs forever pins a slot.
func QuoteAll(ctx context.Context, vendors []Vendor, req Request, opts Options) ([]Result, error) {
	results := make([]Result, len(vendors))
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)

	var sem chan struct{}
	if opts.MaxConcurrency > 0 {
		sem = make(chan struct{}, opts.MaxConcurrency)
	}

	var (
		wg       sync.WaitGroup
		failOnce sync.Once
		firstErr error
	)
	for i, v := range vendors {
		results[i].Vendor = v.Name()
		wg.Add(1)
		go func() {
			defer wg.Done()
			q, err := callOne(ctx, v, req, opts.PerVendorTimeout, sem)
			// Each goroutine writes only its own element: no lock needed,
			// and wg.Wait() makes the writes visible to the caller.
			results[i].Quote, results[i].Err = q, err
			if err != nil && opts.Policy == FailFast {
				failOnce.Do(func() {
					firstErr = fmt.Errorf("vendor %s: %w", results[i].Vendor, err)
					cancel(firstErr)
				})
			}
		}()
	}
	wg.Wait()

	if opts.Policy == FailFast {
		return results, firstErr
	}

	need := opts.MinSuccesses
	if need <= 0 {
		need = 1
	}
	ok := 0
	var errs []error
	for _, r := range results {
		if r.Err == nil {
			ok++
		} else {
			errs = append(errs, fmt.Errorf("vendor %s: %w", r.Vendor, r.Err))
		}
	}
	if ok < need {
		return results, fmt.Errorf("%w: %d of %d vendors answered, need %d: %w",
			ErrInsufficientQuotes, ok, len(vendors), need, errors.Join(errs...))
	}
	return results, nil
}

// callOne acquires a concurrency slot, applies the per-vendor timeout, and
// runs the vendor call in its own goroutine so we can stop waiting for a
// vendor that ignores ctx.
func callOne(ctx context.Context, v Vendor, req Request, timeout time.Duration, sem chan struct{}) (Quote, error) {
	if err := ctx.Err(); err != nil {
		return Quote{}, err // never start a call on a dead context
	}
	if sem != nil {
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			return Quote{}, ctx.Err()
		}
		// select picks randomly when both cases are ready: re-check so a
		// queued call never starts after the deadline.
		if err := ctx.Err(); err != nil {
			<-sem
			return Quote{}, err
		}
	}

	vctx := ctx
	if timeout > 0 {
		var cancel context.CancelFunc
		vctx, cancel = context.WithTimeout(ctx, timeout) // starts NOW, when this call starts
		defer cancel()
	}

	type outcome struct {
		q   Quote
		err error
	}
	ch := make(chan outcome, 1) // buffered: an abandoned call can still deliver and exit
	go func() {
		defer func() {
			if sem != nil {
				<-sem // release only when the call really returned
			}
		}()
		defer func() {
			if r := recover(); r != nil {
				ch <- outcome{err: fmt.Errorf("%w: %s: %v", ErrVendorPanic, v.Name(), r)}
			}
		}()
		q, err := v.Quote(vctx, req)
		ch <- outcome{q, err}
	}()

	select {
	case o := <-ch:
		return o.q, o.err
	case <-vctx.Done():
		// Prefer an answer that arrived at the same instant as the deadline.
		select {
		case o := <-ch:
			return o.q, o.err
		default:
		}
		return Quote{}, vctx.Err()
	}
}
