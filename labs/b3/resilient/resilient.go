// Package resilient is a resilient HTTP client built from small
// http.RoundTripper decorators.
//
// Lab B3 Unit 7 §2 (problem 15), time box 60 minutes. Think of your own vendor
// adapters (KYC, imaging, SSO): slow responses, 503s, duplicate callbacks. Work
// in stages and open the next stage's file only when you get there:
//
//	Stage 1 (≈ 25 min)  this file:  Retrier — backoff with jitter, Retry-After,
//	                                 retry only what is safe to repeat
//	Stage 2 (≈ 20 min)  stage2.go:  Breaker — closed/open/half-open, per-host
//	Stage 3 (≈ 15 min)  stage3.go:  Budget, per-attempt timeouts, Bulkhead, Stats
//
// Run one stage at a time:
//
//	go test -race -run 'Stage1' ./labs/b3/resilient/
//
// No test touches the network: the "vendor" is a fake http.RoundTripper, the
// sleeper and the clock are injected, and every wait has a timeout.
package resilient

import (
	"context"
	"errors"
	"net/http"
	"time"
)

var errNotImplemented = errors.New("resilient: not implemented")

// Wrapper is anything that decorates a RoundTripper.
type Wrapper interface {
	Wrap(next http.RoundTripper) http.RoundTripper
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// Chain stacks decorators over base (given; nothing to implement). ws[0] is
// the OUTERMOST layer: a request meets ws[0] first and base last. A nil base
// means http.DefaultTransport.
func Chain(base http.RoundTripper, ws ...Wrapper) http.RoundTripper {
	if base == nil {
		base = http.DefaultTransport
	}
	for i := len(ws) - 1; i >= 0; i-- {
		base = ws[i].Wrap(base)
	}
	return base
}

// RetryConfig configures a Retrier.
type RetryConfig struct {
	// MaxAttempts is the TOTAL number of attempts, including the first.
	// Values below 1 mean 1 (never retry).
	MaxAttempts int
	// BaseDelay is the backoff ceiling before the first retry (default 100ms);
	// the ceiling doubles per retry up to MaxDelay (default 10s).
	BaseDelay time.Duration
	MaxDelay  time.Duration
	// Sleep waits for d or until ctx is done, returning ctx.Err() in the second
	// case. nil means a timer-based implementation. Tests inject their own.
	Sleep func(ctx context.Context, d time.Duration) error
	// Rand returns a float in [0,1) used for jitter. It must be safe for
	// concurrent use. nil means math/rand.Float64.
	Rand func() float64
	// Now is used only to interpret Retry-After HTTP dates. nil means time.Now.
	Now func() time.Time
}

// RetryOption customises a Retrier. Later stages add options without changing
// NewRetrier's signature.
type RetryOption func(*Retrier)

// RetryError is returned when a request was retried (at least two attempts)
// and the last attempt still ended in a transport error. Err is that last
// error. A final HTTP response, even a 503, is returned as a response with a
// nil error instead.
type RetryError struct {
	Attempts int
	Err      error
}

func (e *RetryError) Error() string { return "resilient: retries exhausted: " + e.Err.Error() }
func (e *RetryError) Unwrap() error { return e.Err }

// Retrier retries safe requests with exponential backoff and full jitter.
//
// A request is retried only if ALL hold:
//   - its method is idempotent (GET, HEAD, PUT, DELETE, OPTIONS), or it
//     carries a non-empty Idempotency-Key header (POST without one is NEVER
//     resent: the vendor may already have acted);
//   - its body can be replayed: no body, http.NoBody, or req.GetBody != nil;
//   - the outcome is retriable: a transport error, or status 429, 502, 503 or
//     504. Status 500 is NOT retried (it usually means this very request
//     triggers a bug). A transport error caused by the CALLER's context ending
//     is never retried.
//
// Delay before retry n (1 = first retry): uniform in [0, min(MaxDelay,
// BaseDelay*2^(n-1))), i.e. cfg.Rand() times the ceiling ("full jitter"). If
// the response has a Retry-After header (seconds, or an HTTP date relative to
// cfg.Now), wait the larger of that and the jittered delay; if Retry-After is
// longer than MaxDelay, do not retry at all and return that response.
//
// Every discarded response must have its body drained (a bounded read) and
// closed before the next attempt. Each attempt must see the full request body.
// Cancelling the request's context during a backoff returns promptly with an
// error satisfying errors.Is(err, context.Canceled).
type Retrier struct {
	// TODO(lab): config, counters, and (later stages) budget and attempt timeout.
	cfg RetryConfig
}

// NewRetrier validates cfg, fills defaults (see RetryConfig) and applies opts.
func NewRetrier(cfg RetryConfig, opts ...RetryOption) *Retrier {
	// TODO(lab)
	r := &Retrier{cfg: cfg}
	for _, o := range opts {
		o(r)
	}
	return r
}

// Wrap implements Wrapper.
func (r *Retrier) Wrap(next http.RoundTripper) http.RoundTripper {
	// TODO(lab): return a RoundTripper that performs the attempts. Work on a
	// private copy of the request for each attempt (req.Clone), never mutate
	// the caller's request. Hints: decide "is this safe to repeat" once, up
	// front; keep the decision "should I retry this outcome" in one function;
	// the order of the gate checks matters (Stage 3 will add two more).
	return roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		_ = next
		return nil, errNotImplemented
	})
}
