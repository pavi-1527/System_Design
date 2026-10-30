package resilient

import (
	"errors"
	"net/http"
	"time"
)

// Stage 2 (≈ 20 min): a circuit breaker.
//
// A Retrier makes one request more reliable; a Breaker protects the vendor
// (and your threads) when it is already struggling. When wired as
// Chain(base, retrier, breaker), the Retrier must treat ErrCircuitOpen as final:
// do not back off and retry a rejection (you will need to touch Stage 1 code).

// ErrCircuitOpen is returned, without calling the next layer, while a breaker
// is rejecting requests.
var ErrCircuitOpen = errors.New("resilient: circuit open")

// State is a circuit breaker state.
type State int

const (
	Closed   State = iota // requests flow; failures are counted
	Open                  // requests are rejected until the cooldown ends
	HalfOpen              // a few probe requests are let through to test recovery
)

func (s State) String() string {
	switch s {
	case Closed:
		return "closed"
	case Open:
		return "open"
	case HalfOpen:
		return "half-open"
	}
	return "unknown"
}

// BreakerConfig configures a Breaker.
type BreakerConfig struct {
	// ConsecutiveFailures opens the breaker after this many failures in a row
	// (0 disables this trigger).
	ConsecutiveFailures int
	// Window and FailureRatio open the breaker when, among the last Window
	// outcomes (only once Window outcomes have been seen), at least
	// FailureRatio of them were failures. It is checked each time a failure is
	// recorded. Window 0 disables this trigger. The window is rolling: it
	// slides over the most recent outcomes, it does not reset every Window.
	Window       int
	FailureRatio float64
	// Cooldown is how long the breaker stays open before allowing probes.
	Cooldown time.Duration
	// HalfOpenProbes (K, default 1) is how many requests are admitted while
	// half-open. The breaker closes when all K succeed, and reopens as soon as
	// one fails; callers beyond K get ErrCircuitOpen.
	HalfOpenProbes int
	// Now is the clock (default time.Now).
	Now func() time.Time
	// OnStateChange is called on every transition, in order, while the breaker
	// holds its lock: it must be fast and must not call back into the breaker.
	OnStateChange func(from, to State)
}

// BreakerStats counts breaker activity.
type BreakerStats struct {
	Rejected int64 // requests refused with ErrCircuitOpen
	Opened   int64 // times the breaker has opened
}

// Breaker is a circuit breaker for one dependency.
//
// What counts as a failure: a transport error or a status >= 500. Statuses
// below 500 (all 4xx, including 429) are successes as far as vendor health
// goes. A request cancelled by its caller is ignored entirely: neither a
// success nor a failure (a member closing the app says nothing about the
// vendor). Note that an attempt timeout (context.DeadlineExceeded) IS a failure.
//
// Subtleties the tests probe:
//   - The Open → HalfOpen transition happens lazily on the first request after
//     the cooldown, using the injected clock.
//   - A result from a request that was admitted BEFORE the state last changed
//     must not influence the current state.
//   - Closing resets all counters.
type Breaker struct {
	// TODO(lab): state machine fields guarded by a mutex.
	cfg BreakerConfig
}

// NewBreaker creates a closed breaker (fill in defaults: HalfOpenProbes 1,
// Now time.Now).
func NewBreaker(cfg BreakerConfig) *Breaker {
	// TODO(lab)
	return &Breaker{cfg: cfg}
}

// State returns the current state.
func (b *Breaker) State() State {
	// TODO(lab)
	return Closed
}

// Stats returns a snapshot of the counters.
func (b *Breaker) Stats() BreakerStats {
	// TODO(lab)
	return BreakerStats{}
}

// Wrap implements Wrapper. Do not hold the breaker's lock while calling next.
func (b *Breaker) Wrap(next http.RoundTripper) http.RoundTripper {
	// TODO(lab)
	return roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		_ = next
		return nil, errNotImplemented
	})
}

// PerHostBreakers keeps one independent Breaker per request URL host, so one
// failing vendor cannot trip the breaker for another.
type PerHostBreakers struct {
	// TODO(lab)
	cfg BreakerConfig
}

// NewPerHost creates per-host breakers that all share cfg.
func NewPerHost(cfg BreakerConfig) *PerHostBreakers {
	// TODO(lab)
	return &PerHostBreakers{cfg: cfg}
}

// State reports the state of the breaker for host (Closed if never used).
func (p *PerHostBreakers) State(host string) State {
	// TODO(lab)
	return Closed
}

// Wrap implements Wrapper.
func (p *PerHostBreakers) Wrap(next http.RoundTripper) http.RoundTripper {
	// TODO(lab)
	return roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		_ = next
		return nil, errNotImplemented
	})
}
