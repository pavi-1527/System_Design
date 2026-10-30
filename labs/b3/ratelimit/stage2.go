package ratelimit

import (
	"errors"
	"time"
)

// ---- Stage 2: a second algorithm, retry-after, sharding (announced at about minute 25) ----
//
// Contract:
//   - The per-key algorithm sits behind the Strategy interface and is chosen
//     with a Factory. New(rate, burst) must keep working and means
//     NewWithStrategy(TokenBucket(rate, burst)). Stage 1's public API does
//     not change.
//   - A Factory is called lazily, once per key, on that key's first
//     request (and at most once more at construction, to learn Burst).
//   - A Strategy is a single-threaded state machine: the Limiter never calls
//     one concurrently, so implementations need no locks. Time passed to a
//     Strategy is an offset from an arbitrary epoch and NEVER decreases, even
//     if the Clock steps backwards; it tracks the Clock exactly otherwise.
//   - SlidingWindow(limit, window) is an exact sliding-window LOG: at most
//     limit permits in any interval of length window. A permit taken at t
//     stops counting AT t+window. Memory per key is O(limit), not O(requests).
//   - Check returns a Decision. RetryAfter is exact: the same request is
//     denied 1ns earlier and allowed at RetryAfter, if nothing else happens.
//     A denial consumes nothing. n < 1 returns ErrInvalidN, n above the
//     Strategy's Burst returns ErrExceedsLimit; neither creates per-key state.
//   - WithShards(n) splits the key space over n independently locked shards,
//     so decisions for keys in different shards never wait for each other.
//     The hash of a key must not be predictable by clients (seed it randomly).

// Errors returned by Check (and by Wait in stage 3).
var (
	// ErrInvalidN is returned for n < 1.
	ErrInvalidN = errors.New("ratelimit: n must be >= 1")
	// ErrExceedsLimit is returned when n is larger than the burst (or the
	// window limit): the request can never succeed, however long you wait.
	ErrExceedsLimit = errors.New("ratelimit: n exceeds the limit and can never be allowed")
)

// Decision is the outcome of one Check.
type Decision struct {
	Allowed bool
	// Remaining is how many more single permits would be allowed right now,
	// after this decision.
	Remaining int
	// RetryAfter is 0 when Allowed. Otherwise the shortest delay after which
	// the SAME request would be allowed, assuming no other traffic for the key.
	RetryAfter time.Duration
}

// Strategy is one key's algorithm. See the stage 2 contract above.
type Strategy interface {
	// Take tries to consume n permits at time now (1 <= n <= Burst()). On
	// success the permits are consumed; on failure nothing is.
	Take(now time.Duration, n int) Decision
	// Burst is the largest n that can ever succeed. It must not change.
	Burst() int
	// Idle reports whether the state at now is indistinguishable from a
	// freshly created Strategy, so dropping it changes no future decision.
	// It must not modify the strategy. (Used by stage 3.)
	Idle(now time.Duration) bool
}

// Factory creates the Strategy for a new key. now is the creation time, in
// the same units as Strategy.Take. Every Strategy from one Factory has the
// same Burst.
type Factory func(now time.Duration) Strategy

// NewWithStrategy returns a limiter whose per-key algorithm comes from f.
// It returns ErrInvalidConfig for a nil f or an invalid option.
func NewWithStrategy(f Factory, opts ...Option) (*Limiter, error) {
	// TODO(lab)
	_, _ = f, opts
	return nil, errNotImplemented
}

// TokenBucket returns a Factory for the stage 1 token bucket. New buckets
// start full. It returns ErrInvalidConfig under the same conditions as New.
func TokenBucket(rate Rate, burst int) (Factory, error) {
	// TODO(lab)
	_, _ = rate, burst
	return nil, errNotImplemented
}

// SlidingWindow returns a Factory for an exact sliding-window log: at most
// limit permits in any interval of length window. It returns
// ErrInvalidConfig for limit < 1 or window < 1.
func SlidingWindow(limit int, window time.Duration) (Factory, error) {
	// TODO(lab)
	_, _ = limit, window
	return nil, errNotImplemented
}

// WithShards sets the number of lock shards. n < 1 makes the constructor
// return ErrInvalidConfig.
func WithShards(n int) Option {
	return func(l *Limiter) {
		// TODO(lab)
		_ = n
	}
}

// Check is AllowN with the details: it reports Remaining and RetryAfter.
func (l *Limiter) Check(key string, n int) (Decision, error) {
	// TODO(lab)
	_, _ = key, n
	return Decision{}, errNotImplemented
}
