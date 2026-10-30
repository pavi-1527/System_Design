// Package resilient is a resilient HTTP client built from small
// http.RoundTripper decorators: Retrier (Stage 1), Breaker (Stage 2), and
// Budget plus Bulkhead (Stage 3). Each decorator wraps the next one, and Chain
// stacks them: the first decorator you pass is the outermost.
//
// Lab B3 Unit 7 §2 (problem 15). Stage 1 is this file, Stage 2 is stage2.go,
// Stage 3 is stage3.go.
//
// Reference design: every decorator is a small struct with its own mutex or
// atomics, guarding only its own state. None of them calls the next layer
// while holding a lock.
package resilient

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

// Wrapper is anything that decorates a RoundTripper.
type Wrapper interface {
	Wrap(next http.RoundTripper) http.RoundTripper
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// Chain stacks decorators over base. ws[0] is the OUTERMOST layer: a request
// meets ws[0] first and base last. A nil base means http.DefaultTransport.
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

// RetryError is returned when a request was retried and the last attempt still
// ended in a transport error. Err is that last error.
type RetryError struct {
	Attempts int
	Err      error
}

func (e *RetryError) Error() string {
	return fmt.Sprintf("resilient: gave up after %d attempts: %v", e.Attempts, e.Err)
}
func (e *RetryError) Unwrap() error { return e.Err }

// Retrier retries safe requests with exponential backoff and full jitter.
//
// A request is retried only if ALL hold: its method is idempotent (GET, HEAD,
// PUT, DELETE, OPTIONS) or it carries a non-empty Idempotency-Key header; its
// body can be replayed (no body, http.NoBody, or req.GetBody set); and the
// outcome is retriable: a transport error (but not the caller's own context
// ending, and not ErrCircuitOpen/ErrBulkheadFull), or status 429, 502, 503 or
// 504. Status 500 is NOT retried: it usually means a bug triggered by this very
// request, and repeating it multiplies load without changing the answer.
type Retrier struct {
	cfg RetryConfig

	budget         *Budget       // Stage 3
	attemptTimeout time.Duration // Stage 3
	minRemaining   time.Duration // Stage 3

	requests, attempts, retries, budgetDenied, gaveUp atomic.Int64
}

// NewRetrier validates cfg, fills defaults and applies options.
func NewRetrier(cfg RetryConfig, opts ...RetryOption) *Retrier {
	if cfg.MaxAttempts < 1 {
		cfg.MaxAttempts = 1
	}
	if cfg.BaseDelay <= 0 {
		cfg.BaseDelay = 100 * time.Millisecond
	}
	if cfg.MaxDelay <= 0 {
		cfg.MaxDelay = 10 * time.Second
	}
	if cfg.Sleep == nil {
		cfg.Sleep = sleepCtx
	}
	if cfg.Rand == nil {
		cfg.Rand = rand.Float64
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	r := &Retrier{cfg: cfg}
	for _, o := range opts {
		o(r)
	}
	return r
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Wrap implements Wrapper.
func (r *Retrier) Wrap(next http.RoundTripper) http.RoundTripper {
	return roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		return r.roundTrip(next, req)
	})
}

func isIdempotent(req *http.Request) bool {
	switch req.Method {
	case "", http.MethodGet, http.MethodHead, http.MethodPut, http.MethodDelete, http.MethodOptions, http.MethodTrace:
		return true
	}
	return req.Header.Get("Idempotency-Key") != ""
}

func replayable(req *http.Request) bool {
	return req.Body == nil || req.Body == http.NoBody || req.GetBody != nil
}

func retriableStatus(code int) bool {
	switch code {
	case http.StatusTooManyRequests, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return true
	}
	return false
}

// backoff returns the full-jitter delay before retry number n (1 = first retry):
// uniform in [0, min(MaxDelay, BaseDelay*2^(n-1))).
func (r *Retrier) backoff(n int) time.Duration {
	ceil := r.cfg.BaseDelay
	for i := 1; i < n; i++ {
		if ceil >= r.cfg.MaxDelay/2 { // doubling would reach the cap (and could overflow)
			ceil = r.cfg.MaxDelay
			break
		}
		ceil *= 2
	}
	if ceil > r.cfg.MaxDelay {
		ceil = r.cfg.MaxDelay
	}
	return time.Duration(r.cfg.Rand() * float64(ceil))
}

// retryAfter parses a Retry-After header (delta-seconds or HTTP date).
func (r *Retrier) retryAfter(resp *http.Response) (time.Duration, bool) {
	v := strings.TrimSpace(resp.Header.Get("Retry-After"))
	if v == "" {
		return 0, false
	}
	if secs, err := strconv.Atoi(v); err == nil && secs >= 0 {
		return time.Duration(secs) * time.Second, true
	}
	if t, err := http.ParseTime(v); err == nil {
		d := t.Sub(r.cfg.Now())
		if d < 0 {
			d = 0
		}
		return d, true
	}
	return 0, false
}

// drainClose discards a response we will not return, so the connection can be
// reused. It reads at most 4 KiB: a huge error page is not worth downloading.
func drainClose(resp *http.Response) {
	if resp == nil || resp.Body == nil {
		return
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	_ = resp.Body.Close()
}

func (r *Retrier) roundTrip(next http.RoundTripper, req *http.Request) (*http.Response, error) {
	ctx := req.Context()
	r.requests.Add(1)
	if r.budget != nil {
		r.budget.Deposit()
	}
	canRetry := isIdempotent(req) && replayable(req)

	for attempt := 1; ; attempt++ {
		r.attempts.Add(1)
		resp, err := r.attempt(next, req, attempt)

		finish := func() (*http.Response, error) {
			if err != nil && attempt > 1 {
				return nil, &RetryError{Attempts: attempt, Err: err}
			}
			return resp, err
		}

		// Decide whether this outcome is retriable, and how long the server asked us to wait.
		var serverWait time.Duration
		switch {
		case ctx.Err() != nil:
			return resp, err // the caller gave up: never retry, never wrap
		case err != nil:
			if errors.Is(err, ErrCircuitOpen) || errors.Is(err, ErrBulkheadFull) {
				return resp, err // fail fast: retrying a rejection defeats the protection
			}
		case retriableStatus(resp.StatusCode):
			if d, ok := r.retryAfter(resp); ok {
				if d > r.cfg.MaxDelay {
					r.gaveUp.Add(1)
					return resp, nil // the server wants longer than we are willing to wait
				}
				serverWait = d
			}
		default:
			return resp, err
		}
		if !canRetry {
			return finish()
		}
		if attempt >= r.cfg.MaxAttempts {
			r.gaveUp.Add(1)
			return finish()
		}

		delay := max(r.backoff(attempt), serverWait)
		if dl, ok := ctx.Deadline(); ok {
			left := time.Until(dl)
			if left < delay || (r.minRemaining > 0 && left-delay < r.minRemaining) {
				r.gaveUp.Add(1) // not enough budget left to wait and try again
				return finish()
			}
		}
		if r.budget != nil && !r.budget.TryWithdraw() {
			r.budgetDenied.Add(1)
			return finish()
		}

		drainClose(resp)
		r.retries.Add(1)
		if serr := r.cfg.Sleep(ctx, delay); serr != nil {
			return nil, serr
		}
	}
}

// attempt performs one try on a private copy of the request, so a retry never
// sees a half-consumed body and per-attempt timeouts never leak into req.
func (r *Retrier) attempt(next http.RoundTripper, req *http.Request, n int) (*http.Response, error) {
	ctx := req.Context()
	cancel := func() {}
	if r.attemptTimeout > 0 {
		ctx, cancel = context.WithTimeout(ctx, r.attemptTimeout)
	}
	ar := req.Clone(ctx)
	if n > 1 && req.GetBody != nil {
		body, err := req.GetBody()
		if err != nil {
			cancel()
			return nil, err
		}
		ar.Body = body
	}
	resp, err := next.RoundTrip(ar)
	if err != nil || resp == nil || resp.Body == nil {
		cancel()
		return resp, err
	}
	// The attempt's context must outlive RoundTrip: cancelling it now would
	// abort the caller's read of the response body. Cancel on Close instead.
	resp.Body = &cancelOnClose{ReadCloser: resp.Body, cancel: cancel}
	return resp, nil
}

type cancelOnClose struct {
	io.ReadCloser
	cancel func()
}

func (c *cancelOnClose) Close() error {
	err := c.ReadCloser.Close()
	c.cancel()
	return err
}
