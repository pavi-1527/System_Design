// Package decorator wraps a vendor client with retries, a circuit breaker, a
// per-attempt timeout and logging, without the client or its callers changing.
//
// Lab B2 Unit 1 §6. Every vendor call on the platform needs the same
// plumbing. The anti-pattern is copy-pasting retry loops and log lines into
// each adapter (300 lines per vendor, each slightly different). The pattern:
// a Decorator is a function from Client to Client, so behaviours stack like
// net/http middleware, and the ORDER of the stack is a design decision with
// consequences you can count (see the order tests).
package decorator

import (
	"context"
	"errors"
	"time"
)

var errNotImplemented = errors.New("decorator: not implemented")

// Request and Response are deliberately tiny: the pattern is not about payloads.
type Request struct {
	Key         string // idempotency key
	AmountMinor int64
}

// Response is a vendor's answer.
type Response struct{ ID string }

// Client is the one-method interface every layer implements and wraps.
type Client interface {
	Do(ctx context.Context, req Request) (Response, error)
}

// ClientFunc adapts a function to Client (like http.HandlerFunc).
type ClientFunc func(ctx context.Context, req Request) (Response, error)

// Do calls f.
func (f ClientFunc) Do(ctx context.Context, req Request) (Response, error) { return f(ctx, req) }

// Decorator wraps a Client with extra behaviour.
type Decorator func(Client) Client

// Chain applies decorators so that the FIRST one is the OUTERMOST: a request
// passes through ds[0] first and ds[len-1] last, then reaches c.
func Chain(c Client, ds ...Decorator) Client {
	// TODO(lab): wrap from the last decorator to the first.
	_, _ = c, ds
	return ClientFunc(func(ctx context.Context, req Request) (Response, error) {
		return Response{}, errNotImplemented
	})
}

// ErrOpen is returned by the breaker while the circuit is open. It is never retried.
var ErrOpen = errors.New("decorator: circuit open")

// ---- retry --------------------------------------------------------------------

// RetryConfig configures WithRetry.
type RetryConfig struct {
	// MaxAttempts is the total number of tries (not retries); values < 1 mean 1.
	MaxAttempts int
	// Backoff returns the wait before the next try, given the number of the
	// attempt that just failed (1 = after the first failure). nil means no wait.
	Backoff func(attempt int) time.Duration
	// Sleep waits for d or until ctx is done (returning ctx's error). nil
	// means a real timer. Tests inject a recorder so nothing really sleeps.
	Sleep func(ctx context.Context, d time.Duration) error
	// Retryable decides whether an error is worth another try. nil means every
	// error is. Whatever it says, ErrOpen is never retried and nothing is
	// retried once the caller's ctx is done.
	Retryable func(error) bool
}

// WithRetry retries failed calls up to cfg.MaxAttempts with backoff.
//
// A non-retryable error is returned as is, immediately. After the last
// attempt the error is wrapped ("retry: gave up after N attempts: %w"). If
// ctx ends before or between attempts the result wraps both ctx's error and
// the last vendor error, and no further attempt is made. No wait after the
// last attempt, none before the first, and the default wait must select on
// ctx.Done() (a time.Sleep cannot be cancelled).
func WithRetry(cfg RetryConfig) Decorator {
	// TODO(lab)
	_ = cfg
	return func(next Client) Client {
		return ClientFunc(func(ctx context.Context, req Request) (Response, error) {
			return Response{}, errNotImplemented
		})
	}
}

// ---- circuit breaker ------------------------------------------------------------

// BreakerConfig configures WithBreaker.
type BreakerConfig struct {
	// Threshold is the number of CONSECUTIVE failures that opens the circuit; < 1 means 1.
	Threshold int
	// Cooldown is how long the circuit stays open before one probe is allowed.
	Cooldown time.Duration
	// Now is the clock; nil means time.Now.
	Now func() time.Time
}

// WithBreaker fails fast with ErrOpen once Threshold consecutive calls have
// failed, without calling the vendor. After Cooldown it lets exactly ONE
// probe through (half-open): success closes the circuit, failure reopens it
// for another Cooldown. Calls that fail because the CALLER's ctx ended say
// nothing about the vendor and count as neither failure nor success (and a
// cancelled probe must release the probe slot). A late success from a call
// admitted before the circuit opened must not close it: only a probe may.
// Each wrapped client gets its own breaker state.
func WithBreaker(cfg BreakerConfig) Decorator {
	// TODO(lab): states closed / open / half-open, guarded by one mutex.
	_ = cfg
	return func(next Client) Client {
		return ClientFunc(func(ctx context.Context, req Request) (Response, error) {
			return Response{}, errNotImplemented
		})
	}
}

// ---- timeout and logging ----------------------------------------------------------

// WithTimeout gives each call to the wrapped client its own deadline of d
// (never longer than the caller's) and releases the timer when the call returns.
func WithTimeout(d time.Duration) Decorator {
	// TODO(lab)
	_ = d
	return func(next Client) Client {
		return ClientFunc(func(ctx context.Context, req Request) (Response, error) {
			return Response{}, errNotImplemented
		})
	}
}

// Entry is one log record.
type Entry struct {
	Key      string
	Duration time.Duration
	Err      error
}

// WithLogging calls log once per call that reaches this layer, after it
// returns, with the elapsed time measured by now (nil means time.Now). A nil
// log makes the decorator a no-op.
func WithLogging(log func(Entry), now func() time.Time) Decorator {
	// TODO(lab)
	_, _ = log, now
	return func(next Client) Client {
		return ClientFunc(func(ctx context.Context, req Request) (Response, error) {
			return Response{}, errNotImplemented
		})
	}
}
