// Package retry is a small retry executor: exponential backoff with jitter,
// an overall deadline budget, error classification, Retry-After handling, an
// idempotency gate, and a retry budget that can be shared by many callers.
//
// It is the lab for A7 Unit 1 (§2–§3). The shape mirrors what the AWS SDKs,
// gRPC's retry policy and Envoy's retry budgets do, minus everything that is
// not about the decisions: may I retry this? how long do I wait? is there
// time left? is the dependency already overloaded by everybody else's retries?
//
// Suggested order: Delay, then Do with attempts/backoff only, then
// classification and the idempotency gate, then the deadline budget and
// Retry-After, then Budget. Run: go test -race ./labs/a7/retry/
package retry

import (
	"context"
	"errors"
	"fmt"
	"math"
	"math/rand/v2"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Jitter selects how randomness is applied to the exponential backoff cap.
// The zero value is FullJitter, the safest default.
type Jitter int

const (
	// FullJitter sleeps rnd × cap.
	FullJitter Jitter = iota
	// EqualJitter sleeps cap/2 + rnd × cap/2.
	EqualJitter
	// DecorrelatedJitter sleeps min(Max, Base + rnd × (3×prev − Base)).
	DecorrelatedJitter
	// NoJitter sleeps exactly cap (synchronises clients; for comparison only).
	NoJitter
)

func (j Jitter) String() string {
	switch j {
	case FullJitter:
		return "full"
	case EqualJitter:
		return "equal"
	case DecorrelatedJitter:
		return "decorrelated"
	case NoJitter:
		return "none"
	}
	return fmt.Sprintf("Jitter(%d)", int(j))
}

// Sentinel reasons carried by *GiveUpError.
var (
	ErrMaxAttempts     = errors.New("retry: max attempts reached")
	ErrBudgetExhausted = errors.New("retry: retry budget exhausted")
	ErrDeadlineBudget  = errors.New("retry: not enough time left in the deadline for another attempt")
)

// GiveUpError is returned when Do stops retrying although the operation could
// in principle be retried. Reason is one of the sentinels above or the
// context's error; Err is the last error the operation returned. Both are
// reachable with errors.Is / errors.As.
type GiveUpError struct {
	Reason   error
	Attempts int
	Err      error
}

func (e *GiveUpError) Error() string {
	return fmt.Sprintf("retry: giving up after %d attempt(s) (%v): %v", e.Attempts, e.Reason, e.Err)
}

func (e *GiveUpError) Unwrap() []error { return []error{e.Reason, e.Err} }

type permanentError struct{ err error }

func (e *permanentError) Error() string { return e.err.Error() }
func (e *permanentError) Unwrap() error { return e.err }

// Permanent marks err as not worth retrying (a 400, a validation failure).
// Permanent(nil) is nil.
func Permanent(err error) error {
	if err == nil {
		return nil
	}
	return &permanentError{err}
}

type notSentError struct{ err error }

func (e *notSentError) Error() string { return e.err.Error() }
func (e *notSentError) Unwrap() error { return e.err }

// NotSent marks err as proof that the request never reached the server
// (connection refused, DNS failure, TLS handshake failure). Such failures
// are safe to retry even for non-idempotent calls. NotSent(nil) is nil.
func NotSent(err error) error {
	if err == nil {
		return nil
	}
	return &notSentError{err}
}

// RetryAfterError carries a server-requested minimum delay (a Retry-After
// header on a 429 or 503). Do waits at least After before the next attempt.
type RetryAfterError struct {
	Err   error
	After time.Duration
}

func (e *RetryAfterError) Error() string {
	return fmt.Sprintf("%v (retry after %v)", e.Err, e.After)
}
func (e *RetryAfterError) Unwrap() error { return e.Err }

// ParseRetryAfter parses an HTTP Retry-After header value: either a whole
// number of seconds ("120") or an HTTP-date. Dates are measured against now;
// a date in the past yields 0. It reports false for anything else, including
// negative numbers. Huge values are clamped so the Duration cannot overflow.
func ParseRetryAfter(value string, now time.Time) (time.Duration, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, false
	}
	if n, err := strconv.ParseUint(value, 10, 63); err == nil {
		if n > uint64(math.MaxInt64/int64(time.Second)) {
			return time.Duration(math.MaxInt64), true
		}
		return time.Duration(n) * time.Second, true
	}
	if t, err := http.ParseTime(value); err == nil {
		if d := t.Sub(now); d > 0 {
			return d, true
		}
		return 0, true
	}
	return 0, false
}

// Delay returns how long to wait before retry number attempt (0 is the wait
// after the first failure). The exponential cap is
// min(max, base × 2^attempt); jitter then applies:
//
//	FullJitter         rnd × cap
//	EqualJitter        cap/2 + rnd × (cap − cap/2)
//	DecorrelatedJitter min(max, base + rnd × (3×prev − base)), prev ≥ base
//	NoJitter           cap
//
// rnd is a uniform value in [0, 1) (values outside are clamped). A max below
// base is raised to base; a non-positive base yields 0. Large attempt numbers
// must not overflow.
func Delay(j Jitter, base, max time.Duration, attempt int, prev time.Duration, rnd float64) time.Duration {
	if base <= 0 {
		return 0
	}
	if max < base {
		max = base
	}
	if rnd < 0 {
		rnd = 0
	}
	if rnd > 1 {
		rnd = 1
	}
	c := base
	for i := 0; i < attempt && c < max; i++ {
		if c > max/2 {
			c = max
			break
		}
		c *= 2
	}
	if c > max {
		c = max
	}
	switch j {
	case NoJitter:
		return c
	case EqualJitter:
		half := c / 2
		return half + time.Duration(rnd*float64(c-half))
	case DecorrelatedJitter:
		if prev < base {
			prev = base
		}
		hi := max
		if prev <= max/3 {
			hi = prev * 3
		}
		d := base + time.Duration(rnd*float64(hi-base))
		if d > max {
			d = max
		}
		return d
	default:
		return time.Duration(rnd * float64(c))
	}
}

// Budget limits retries relative to traffic so that a failing dependency is
// not hit with a multiple of its normal load. Every first attempt calls
// Deposit (adding ratio of a token, capped at max); every retry must
// Withdraw a whole token first. Over any period, retries ≤ ratio × requests
// + initial. It is safe for concurrent use and meant to be shared.
//
// Tokens are counted in thousandths, so ratio has 0.001 resolution and there
// is no floating-point drift (ten deposits of 0.1 are exactly one token).
type Budget struct {
	mu      sync.Mutex
	milli   int64
	maxMill int64
	perDep  int64
}

// NewBudget returns a budget that earns ratio tokens per Deposit, holds at
// most max tokens, and starts with initial tokens (clamped to max).
func NewBudget(ratio float64, max, initial int) *Budget {
	b := &Budget{
		perDep:  int64(math.Round(ratio * 1000)),
		maxMill: int64(max) * 1000,
		milli:   int64(initial) * 1000,
	}
	if b.perDep < 0 {
		b.perDep = 0
	}
	if b.milli > b.maxMill {
		b.milli = b.maxMill
	}
	return b
}

// Deposit records one first attempt.
func (b *Budget) Deposit() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.milli += b.perDep
	if b.milli > b.maxMill {
		b.milli = b.maxMill
	}
}

// Withdraw takes one token if at least one is available.
func (b *Budget) Withdraw() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.milli < 1000 {
		return false
	}
	b.milli -= 1000
	return true
}

// Tokens reports the current balance.
func (b *Budget) Tokens() float64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return float64(b.milli) / 1000
}

// Config configures Do. Zero values get the defaults noted.
type Config struct {
	// MaxAttempts is the total number of attempts including the first.
	// Default 3.
	MaxAttempts int
	// BaseDelay and MaxDelay shape the exponential backoff. Defaults 100ms
	// and 10s.
	BaseDelay, MaxDelay time.Duration
	// Jitter selects the jitter strategy. Default FullJitter.
	Jitter Jitter
	// AttemptTimeout bounds each attempt (0 = only the caller's deadline).
	// It is further capped by the time left until the caller's deadline.
	AttemptTimeout time.Duration
	// Idempotent declares the operation safe to re-send (GET, PUT, DELETE,
	// or any call whose effect is naturally idempotent).
	Idempotent bool
	// IdempotencyKey, when non-empty, also makes the call safe to re-send:
	// the server deduplicates (A1 Unit 3 §2). Do never inspects the key.
	IdempotencyKey string
	// IsRetryable classifies errors that passed the idempotency gate. nil
	// means every error is retryable. Permanent errors are never retried
	// whatever this says.
	IsRetryable func(error) bool
	// Budget, if non-nil, is shared with other callers; see Budget.
	Budget *Budget
	// Rand returns a uniform float64 in [0, 1). Default: math/rand/v2.
	Rand func() float64
	// Sleep waits d or until ctx is done (returning ctx.Err()). Default: a
	// timer. Tests inject a fake that advances a fake clock.
	Sleep func(ctx context.Context, d time.Duration) error
	// Now is the clock used for deadline arithmetic. Default time.Now.
	Now func() time.Time
	// OnRetry, if non-nil, is called before each wait with the number of
	// the attempt that just failed, its error and the delay about to be
	// slept.
	OnRetry func(attempt int, err error, delay time.Duration)
}

func (c Config) withDefaults() Config {
	if c.MaxAttempts <= 0 {
		c.MaxAttempts = 3
	}
	if c.BaseDelay <= 0 {
		c.BaseDelay = 100 * time.Millisecond
	}
	if c.MaxDelay <= 0 {
		c.MaxDelay = 10 * time.Second
	}
	if c.Rand == nil {
		c.Rand = rand.Float64
	}
	if c.Sleep == nil {
		c.Sleep = sleepCtx
	}
	if c.Now == nil {
		c.Now = time.Now
	}
	return c
}

func sleepCtx(ctx context.Context, d time.Duration) error {
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

func hasType[T error](err error) bool {
	var t T
	return errors.As(err, &t)
}

func retryable(cfg Config, err error) bool {
	if hasType[*permanentError](err) {
		return false
	}
	if !(cfg.Idempotent || cfg.IdempotencyKey != "") && !hasType[*notSentError](err) {
		return false
	}
	if cfg.IsRetryable != nil {
		return cfg.IsRetryable(err)
	}
	return true
}

// Do calls op until it succeeds, returns a non-retryable error, or a limit
// is hit. Contract (the tests check each point):
//
//   - If ctx is already done, op is never called and ctx.Err() is returned.
//   - If a Budget is configured, Deposit is called once, before the first
//     attempt; Withdraw is called before every retry (after the deadline
//     check, so a token is never spent on a wait that cannot happen).
//   - Each attempt gets its own context with the AttemptTimeout, capped by
//     the time left before ctx's deadline (measured with cfg.Now).
//   - After a failure: if ctx is done, return a *GiveUpError whose Reason is
//     ctx.Err(). If the error is not retryable (Permanent; or the call is
//     not idempotent and the error is not NotSent; or IsRetryable says no)
//     return it unchanged. If attempts are used up, give up with
//     ErrMaxAttempts.
//   - Delay = Delay(cfg.Jitter, …, attempt−1, prevDelay, cfg.Rand()); if the
//     error carries a *RetryAfterError larger than that, the larger value
//     wins (Retry-After is a floor, never a ceiling).
//   - If the delay does not fit in the time left before ctx's deadline,
//     give up immediately with ErrDeadlineBudget instead of sleeping. Then
//     Withdraw (ErrBudgetExhausted), then OnRetry, then Sleep; a Sleep error
//     is returned as the Reason of a *GiveUpError.
//
// Give-up errors are *GiveUpError wrapping the Reason and the last error.
func Do[T any](ctx context.Context, cfg Config, op func(ctx context.Context) (T, error)) (T, error) {
	var zero T
	cfg = cfg.withDefaults()
	if err := ctx.Err(); err != nil {
		return zero, err
	}
	if cfg.Budget != nil {
		cfg.Budget.Deposit()
	}
	prev := cfg.BaseDelay
	for attempt := 1; ; attempt++ {
		actx, cancel := attemptContext(ctx, cfg)
		v, err := op(actx)
		cancel()
		if err == nil {
			return v, nil
		}
		giveUp := func(reason error) (T, error) {
			return zero, &GiveUpError{Reason: reason, Attempts: attempt, Err: err}
		}
		if cerr := ctx.Err(); cerr != nil {
			return giveUp(cerr)
		}
		if !retryable(cfg, err) {
			return zero, err
		}
		if attempt >= cfg.MaxAttempts {
			return giveUp(ErrMaxAttempts)
		}
		delay := Delay(cfg.Jitter, cfg.BaseDelay, cfg.MaxDelay, attempt-1, prev, cfg.Rand())
		prev = delay
		var ra *RetryAfterError
		if errors.As(err, &ra) && ra.After > delay {
			delay = ra.After
		}
		if dl, ok := ctx.Deadline(); ok && dl.Sub(cfg.Now()) <= delay {
			return giveUp(ErrDeadlineBudget)
		}
		if cfg.Budget != nil && !cfg.Budget.Withdraw() {
			return giveUp(ErrBudgetExhausted)
		}
		if cfg.OnRetry != nil {
			cfg.OnRetry(attempt, err, delay)
		}
		if serr := cfg.Sleep(ctx, delay); serr != nil {
			return giveUp(serr)
		}
	}
}

// DoErr is Do for operations that return only an error.
func DoErr(ctx context.Context, cfg Config, op func(ctx context.Context) error) error {
	_, err := Do(ctx, cfg, func(ctx context.Context) (struct{}, error) {
		return struct{}{}, op(ctx)
	})
	return err
}

func attemptContext(ctx context.Context, cfg Config) (context.Context, context.CancelFunc) {
	if cfg.AttemptTimeout <= 0 {
		return context.WithCancel(ctx)
	}
	d := cfg.AttemptTimeout
	if dl, ok := ctx.Deadline(); ok {
		if rem := dl.Sub(cfg.Now()); rem < d {
			d = rem
		}
	}
	return context.WithTimeout(ctx, d)
}
