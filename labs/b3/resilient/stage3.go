package resilient

import (
	"errors"
	"net/http"
	"time"
)

// Stage 3 (≈ 15 min): bound the damage. A retry budget stops retries from
// multiplying load during a brownout; attempt timeouts keep each try inside the
// caller's deadline; a bulkhead caps concurrency; Stats make it observable.
//
// Stage 3 also changes Stage 1 code: the Retrier consults the Budget and the
// attempt timeout. Think about WHERE in your retry loop each new gate goes: a
// token must only be spent on a retry that will really happen.

// ErrBulkheadFull is returned when a Bulkhead has no free slot and no room to wait.
var ErrBulkheadFull = errors.New("resilient: bulkhead full")

// Budget limits retries to a fraction of recent traffic. Every first attempt
// deposits ratio tokens (capped at burst); every retry withdraws one whole
// token or is denied. With ratio 0.1, at most about one retry per ten requests
// in steady state, plus an initial burst. Safe for concurrent use.
type Budget struct {
	// TODO(lab)
	ratio, burst float64
}

// NewBudget returns a budget that starts full (burst tokens).
func NewBudget(ratio, burst float64) *Budget {
	// TODO(lab)
	return &Budget{ratio: ratio, burst: burst}
}

// Deposit records one new request (adds ratio tokens, up to burst).
func (b *Budget) Deposit() {
	// TODO(lab)
}

// TryWithdraw takes one token for a retry, reporting whether one was available.
func (b *Budget) TryWithdraw() bool {
	// TODO(lab)
	return false
}

// Tokens reports the current balance.
func (b *Budget) Tokens() float64 {
	// TODO(lab)
	return 0
}

// WithBudget makes a Retrier spend from b for every retry, and deposit into it
// for every request (first attempt).
func WithBudget(b *Budget) RetryOption {
	// TODO(lab): store b on the Retrier and use it in the retry loop.
	return func(r *Retrier) { _ = b }
}

// WithAttemptTimeout bounds every attempt to perAttempt (never beyond the
// request's own deadline, which always wins), and stops retrying when less
// than minRemaining of the request's deadline would be left after the backoff.
//
// Also, independent of this option: if the chosen backoff is at least as long
// as the time left until the request's deadline, do not sleep: return the last
// result (a real 503 beats a late "context deadline exceeded").
//
// Careful: the attempt's context must stay alive until the response body is
// closed, or the caller's read of the body fails. A discarded attempt's
// context is cancelled when you close its body.
func WithAttemptTimeout(perAttempt, minRemaining time.Duration) RetryOption {
	// TODO(lab)
	return func(r *Retrier) { _, _ = perAttempt, minRemaining }
}

// RetryStats counts Retrier activity. Semantics (the tests check them):
//   - Requests: logical requests seen. Attempts: tries, first attempts included.
//   - Retries: tries after the first.
//   - BudgetDenied: times the Budget refused a retry that was otherwise due.
//   - GaveUp: retries not made for any other reason: attempts used up, not
//     enough deadline left, or a Retry-After longer than MaxDelay.
type RetryStats struct {
	Requests     int64
	Attempts     int64
	Retries      int64
	BudgetDenied int64
	GaveUp       int64
}

// Stats returns a snapshot of the counters.
func (r *Retrier) Stats() RetryStats {
	// TODO(lab)
	return RetryStats{}
}

// BulkheadConfig configures a Bulkhead.
type BulkheadConfig struct {
	// MaxInFlight is the number of requests allowed at once (minimum 1). A slot
	// is held until the response body is closed, not merely until headers
	// arrive; closing twice must release once.
	MaxInFlight int
	// MaxQueue is how many further callers may wait for a slot (they give up
	// when their own context ends); the next one is refused immediately with
	// ErrBulkheadFull. 0 means never wait.
	MaxQueue int
}

// BulkheadStats counts Bulkhead activity.
type BulkheadStats struct {
	Rejected int64 // refused with ErrBulkheadFull
	Peak     int64 // highest number of slots ever in use at once
}

// Bulkhead caps concurrent requests to one dependency, so a slow vendor can
// occupy at most MaxInFlight of your goroutines and connections.
type Bulkhead struct {
	// TODO(lab)
	cfg BulkheadConfig
}

// NewBulkhead creates a bulkhead.
func NewBulkhead(cfg BulkheadConfig) *Bulkhead {
	// TODO(lab)
	return &Bulkhead{cfg: cfg}
}

// InFlight reports how many slots are in use right now.
func (b *Bulkhead) InFlight() int {
	// TODO(lab)
	return 0
}

// Waiting reports how many callers are queued for a slot right now.
func (b *Bulkhead) Waiting() int {
	// TODO(lab)
	return 0
}

// Stats returns a snapshot of the counters.
func (b *Bulkhead) Stats() BulkheadStats {
	// TODO(lab)
	return BulkheadStats{}
}

// Wrap implements Wrapper. Release the slot on error paths and panics too.
func (b *Bulkhead) Wrap(next http.RoundTripper) http.RoundTripper {
	// TODO(lab)
	return roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		_ = next
		return nil, errNotImplemented
	})
}
