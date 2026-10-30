package resilient

import (
	"context"
	"errors"
	"io"
	"net/http"
	"sync"
	"sync/atomic"
	"time"
)

// ErrBulkheadFull is returned when a Bulkhead has no free slot and no room to wait.
var ErrBulkheadFull = errors.New("resilient: bulkhead full")

// Budget limits retries to a fraction of recent traffic, so a brownout does
// not multiply the load on a struggling vendor. Every first attempt deposits
// ratio tokens (up to burst); every retry withdraws one whole token or is
// denied. With ratio 0.1, at most about one retry per ten requests is allowed
// in steady state, plus a burst of initial credit.
type Budget struct {
	mu     sync.Mutex
	tokens float64
	burst  float64
	ratio  float64
}

// NewBudget returns a budget that starts full (burst tokens).
func NewBudget(ratio, burst float64) *Budget {
	return &Budget{tokens: burst, burst: burst, ratio: ratio}
}

// Deposit records one new request.
func (b *Budget) Deposit() {
	b.mu.Lock()
	b.tokens = min(b.burst, b.tokens+b.ratio)
	b.mu.Unlock()
}

// TryWithdraw takes one token for a retry, reporting whether one was available.
func (b *Budget) TryWithdraw() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// Tokens reports the current balance.
func (b *Budget) Tokens() float64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.tokens
}

// WithBudget makes a Retrier spend from b for every retry.
func WithBudget(b *Budget) RetryOption { return func(r *Retrier) { r.budget = b } }

// WithAttemptTimeout bounds every attempt to perAttempt (never beyond the
// request's own deadline, which always wins) and stops retrying when less than
// minRemaining of the request's deadline would be left after the backoff.
func WithAttemptTimeout(perAttempt, minRemaining time.Duration) RetryOption {
	return func(r *Retrier) { r.attemptTimeout, r.minRemaining = perAttempt, minRemaining }
}

// RetryStats counts Retrier activity.
type RetryStats struct {
	Requests     int64 // logical requests seen
	Attempts     int64 // total tries, first attempts included
	Retries      int64 // tries after the first
	BudgetDenied int64 // retries refused by the Budget
	GaveUp       int64 // retries not made: attempts used up, deadline, or Retry-After too long
}

// Stats returns a snapshot of the counters.
func (r *Retrier) Stats() RetryStats {
	return RetryStats{
		Requests:     r.requests.Load(),
		Attempts:     r.attempts.Load(),
		Retries:      r.retries.Load(),
		BudgetDenied: r.budgetDenied.Load(),
		GaveUp:       r.gaveUp.Load(),
	}
}

// BulkheadConfig configures a Bulkhead.
type BulkheadConfig struct {
	// MaxInFlight is the number of requests allowed at once (minimum 1). A slot
	// is held until the response body is closed, not merely until headers arrive.
	MaxInFlight int
	// MaxQueue is how many further callers may wait for a slot; the next one is
	// refused immediately with ErrBulkheadFull. 0 means never wait.
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
	cfg      BulkheadConfig
	sem      chan struct{}
	mu       sync.Mutex
	waiting  int
	rejected atomic.Int64
	peak     atomic.Int64
}

// NewBulkhead creates a bulkhead.
func NewBulkhead(cfg BulkheadConfig) *Bulkhead {
	if cfg.MaxInFlight < 1 {
		cfg.MaxInFlight = 1
	}
	return &Bulkhead{cfg: cfg, sem: make(chan struct{}, cfg.MaxInFlight)}
}

// InFlight reports how many slots are in use right now.
func (b *Bulkhead) InFlight() int { return len(b.sem) }

// Waiting reports how many callers are queued for a slot right now.
func (b *Bulkhead) Waiting() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.waiting
}

// Stats returns a snapshot of the counters.
func (b *Bulkhead) Stats() BulkheadStats {
	return BulkheadStats{Rejected: b.rejected.Load(), Peak: b.peak.Load()}
}

func (b *Bulkhead) noteAcquired() {
	n := int64(len(b.sem))
	for {
		p := b.peak.Load()
		if n <= p || b.peak.CompareAndSwap(p, n) {
			return
		}
	}
}

func (b *Bulkhead) acquire(ctx context.Context) error {
	select {
	case b.sem <- struct{}{}:
		b.noteAcquired()
		return nil
	default:
	}
	b.mu.Lock()
	if b.waiting >= b.cfg.MaxQueue {
		b.mu.Unlock()
		b.rejected.Add(1)
		return ErrBulkheadFull
	}
	b.waiting++
	b.mu.Unlock()
	defer func() {
		b.mu.Lock()
		b.waiting--
		b.mu.Unlock()
	}()
	select {
	case b.sem <- struct{}{}:
		b.noteAcquired()
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (b *Bulkhead) release() { <-b.sem }

type releaseOnClose struct {
	io.ReadCloser
	once    sync.Once
	release func()
}

func (r *releaseOnClose) Close() error {
	err := r.ReadCloser.Close()
	r.once.Do(r.release)
	return err
}

// Wrap implements Wrapper.
func (b *Bulkhead) Wrap(next http.RoundTripper) http.RoundTripper {
	return roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		if err := b.acquire(req.Context()); err != nil {
			return nil, err
		}
		handedOver := false
		defer func() {
			if !handedOver {
				b.release()
			}
		}()
		resp, err := next.RoundTrip(req)
		if err != nil || resp == nil || resp.Body == nil {
			return resp, err
		}
		handedOver = true
		resp.Body = &releaseOnClose{ReadCloser: resp.Body, release: b.release}
		return resp, nil
	})
}
