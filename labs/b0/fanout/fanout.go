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
	"time"
)

var errNotImplemented = errors.New("fanout: not implemented")

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
// Design decision for you (not tested either way; write it in your
// concurrency note): when you abandon a vendor that ignores ctx, do you
// release its MaxConcurrency slot immediately, or only when the call
// actually returns? What does each choice cost?
func QuoteAll(ctx context.Context, vendors []Vendor, req Request, opts Options) ([]Result, error) {
	// TODO(lab): suggested shape
	//   1. results := make([]Result, len(vendors)); fill Vendor names.
	//   2. ctx, cancel := context.WithCancelCause(ctx); defer cancel(nil)
	//   3. a semaphore (buffered channel) if MaxConcurrency > 0
	//   4. one goroutine per vendor -> callOne; each writes only results[i]
	//   5. FailFast: on the first error, record it once and cancel(err)
	//   6. wg.Wait(); BestEffort: count successes vs MinSuccesses, and
	//      return ErrInsufficientQuotes wrapping errors.Join(vendor errors)
	_, _, _, _ = ctx, vendors, req, opts
	return nil, errNotImplemented
}

// TODO(lab): func callOne(ctx context.Context, v Vendor, req Request,
// timeout time.Duration, sem chan struct{}) (Quote, error)
//   - if ctx is already done, return ctx.Err() without calling anybody
//   - acquire the slot with a select against ctx.Done(); then re-check
//     ctx.Err() (select is random when both cases are ready)
//   - start the per-vendor timeout HERE, when the call starts
//   - run v.Quote in its own goroutine that delivers into a BUFFERED(1)
//     channel and recovers panics into ErrVendorPanic
//   - select on that channel vs the per-vendor ctx's Done()
var _ = time.Second
