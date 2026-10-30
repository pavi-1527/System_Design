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
	"fmt"
	"sync"
	"time"
)

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
	for i := len(ds) - 1; i >= 0; i-- {
		c = ds[i](c)
	}
	return c
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

func sleep(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// WithRetry retries failed calls up to cfg.MaxAttempts with backoff.
//
// A non-retryable error is returned as is, immediately. After the last
// attempt the error is wrapped ("retry: gave up after N attempts: %w"). If
// ctx ends before or between attempts the result wraps both ctx's error and
// the last vendor error, and no further attempt is made.
func WithRetry(cfg RetryConfig) Decorator {
	if cfg.MaxAttempts < 1 {
		cfg.MaxAttempts = 1
	}
	if cfg.Sleep == nil {
		cfg.Sleep = sleep
	}
	return func(next Client) Client {
		return ClientFunc(func(ctx context.Context, req Request) (Response, error) {
			var last error
			for attempt := 1; ; attempt++ {
				if err := ctx.Err(); err != nil {
					if last != nil {
						return Response{}, fmt.Errorf("retry: %w; last error: %w", err, last)
					}
					return Response{}, err
				}
				resp, err := next.Do(ctx, req)
				if err == nil {
					return resp, nil
				}
				last = err
				if errors.Is(err, ErrOpen) || ctx.Err() != nil || (cfg.Retryable != nil && !cfg.Retryable(err)) {
					return Response{}, err
				}
				if attempt >= cfg.MaxAttempts {
					return Response{}, fmt.Errorf("retry: gave up after %d attempts: %w", attempt, err)
				}
				var wait time.Duration
				if cfg.Backoff != nil {
					wait = cfg.Backoff(attempt)
				}
				if serr := cfg.Sleep(ctx, wait); serr != nil {
					return Response{}, fmt.Errorf("retry: %w; last error: %w", serr, last)
				}
			}
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

type bstate int

const (
	closed bstate = iota
	open
	halfOpen
)

type breaker struct {
	cfg      BreakerConfig
	mu       sync.Mutex
	state    bstate
	failures int
	openedAt time.Time
	probing  bool
}

// WithBreaker fails fast with ErrOpen once Threshold consecutive calls have
// failed, without calling the vendor. After Cooldown it lets exactly ONE
// probe through (half-open): success closes the circuit, failure reopens it
// for another Cooldown. Calls that fail because the CALLER's ctx ended say
// nothing about the vendor and count as neither failure nor success. Each
// wrapped client gets its own breaker state.
func WithBreaker(cfg BreakerConfig) Decorator {
	if cfg.Threshold < 1 {
		cfg.Threshold = 1
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return func(next Client) Client {
		b := &breaker{cfg: cfg}
		return ClientFunc(func(ctx context.Context, req Request) (Response, error) {
			probe, err := b.admit()
			if err != nil {
				return Response{}, err
			}
			resp, err := next.Do(ctx, req)
			b.record(probe, err != nil && ctx.Err() != nil, err)
			return resp, err
		})
	}
}

func (b *breaker) admit() (probe bool, err error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	switch b.state {
	case open:
		if b.cfg.Now().Sub(b.openedAt) < b.cfg.Cooldown {
			return false, ErrOpen
		}
		b.state, b.probing = halfOpen, true
		return true, nil
	case halfOpen:
		if b.probing {
			return false, ErrOpen
		}
		b.probing = true
		return true, nil
	}
	return false, nil
}

func (b *breaker) record(probe, callerGaveUp bool, err error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if probe {
		b.probing = false // a cancelled probe leaves us half-open: the next call probes again
	}
	switch {
	case callerGaveUp:
	case err == nil:
		if probe || b.state == closed { // a straggler admitted before the circuit opened must not close it
			b.state, b.failures = closed, 0
		}
	case probe:
		b.state, b.openedAt = open, b.cfg.Now()
	case b.state == closed:
		b.failures++
		if b.failures >= b.cfg.Threshold {
			b.state, b.openedAt = open, b.cfg.Now()
		}
	}
}

// ---- timeout and logging ----------------------------------------------------------

// WithTimeout gives each call to the wrapped client its own deadline of d
// (never longer than the caller's) and releases the timer when the call returns.
func WithTimeout(d time.Duration) Decorator {
	return func(next Client) Client {
		return ClientFunc(func(ctx context.Context, req Request) (Response, error) {
			ctx, cancel := context.WithTimeout(ctx, d)
			defer cancel()
			return next.Do(ctx, req)
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
	if now == nil {
		now = time.Now
	}
	return func(next Client) Client {
		if log == nil {
			return next
		}
		return ClientFunc(func(ctx context.Context, req Request) (Response, error) {
			start := now()
			resp, err := next.Do(ctx, req)
			log(Entry{Key: req.Key, Duration: now().Sub(start), Err: err})
			return resp, err
		})
	}
}
