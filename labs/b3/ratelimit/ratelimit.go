// Package ratelimit is a keyed, concurrency-safe rate limiter library.
//
// Lab B3 Problem 2 (60-minute box). Stage 1 is this file: a per-key token
// bucket. Stage 2 (stage2.go) puts the algorithm behind a small Strategy
// interface, adds a second algorithm, retry-after information and lock
// sharding. Stage 3 (stage3.go) bounds memory, adds a blocking Wait and
// Stats. Treat the later files as sealed until the interviewer "announces"
// them (see unit-1-caches-and-limiters.md §2).
//
// Stage 1 contract:
//   - New(rate, burst) gives every key its own token bucket. A bucket holds at
//     most burst tokens, starts full, and refills continuously at rate.Events
//     per rate.Per. Allow takes one token, AllowN takes n, all or nothing.
//   - Refill is LAZY: compute it from the elapsed time on each call, from the
//     injected Clock. There is no background goroutine and no Close.
//   - Refill must be exact. A test admits requests for 100 simulated seconds
//     and expects exactly the right count, so float64 token counters that
//     drift will fail. Think integers: nanoseconds and a common unit.
//   - Time may jump: a clock step of centuries must not overflow, and a clock
//     that goes BACKWARDS must not mint tokens (neither during the rewind nor
//     when it catches up again).
//   - All methods are safe for concurrent use. Operations on one key are
//     atomic: N goroutines asking for one key with burst B admit exactly B.
//   - n < 1, and n greater than the burst (which can never succeed), return
//     false and must not create any per-key state.
//   - Len returns the number of keys currently tracked.
package ratelimit

import (
	"errors"
	"time"
)

// ErrInvalidConfig is returned by constructors for impossible parameters.
var ErrInvalidConfig = errors.New("ratelimit: invalid configuration")

var errNotImplemented = errors.New("ratelimit: not implemented")

// Clock is the only thing the limiter needs from time.
type Clock interface {
	Now() time.Time
}

// Rate is "Events per Per", for example Rate{Events: 100, Per: time.Minute}.
type Rate struct {
	Events int
	Per    time.Duration
}

// Option customises a Limiter at construction time. Stages 2 and 3 add
// options (WithShards, WithTimer, WithMaxKeys); your constructors must apply
// them, in order, before any key exists.
type Option func(*Limiter)

// WithClock sets the clock. The default is the system clock.
func WithClock(c Clock) Option {
	return func(l *Limiter) {
		// TODO(lab)
		_ = c
	}
}

// Limiter rate-limits independent keys. The zero value is not usable; call New.
type Limiter struct {
	// TODO(lab): fields. Think about: the clock, the per-key state and who
	// protects it, how a key's time is measured (an offset from a fixed
	// epoch is compact and monotonic-friendly), and later stages' needs:
	// shards, a strategy per key, an LRU of keys, counters, a timer source.
}

// New returns a limiter that gives every key a token bucket refilling at rate
// and holding at most burst tokens. A key's bucket starts full. It returns
// ErrInvalidConfig for Events < 1, Per < 1ns, burst < 1, or values that would
// overflow your arithmetic.
func New(rate Rate, burst int, opts ...Option) (*Limiter, error) {
	// TODO(lab)
	_, _, _ = rate, burst, opts
	return nil, errNotImplemented
}

// Allow reports whether one event for key may happen now, consuming a token
// if so.
func (l *Limiter) Allow(key string) bool {
	// TODO(lab)
	_ = key
	return false
}

// AllowN reports whether n events for key may happen now, consuming n tokens
// if so. All or nothing.
func (l *Limiter) AllowN(key string, n int) bool {
	// TODO(lab)
	_, _ = key, n
	return false
}

// Len returns the number of keys currently tracked.
func (l *Limiter) Len() int {
	// TODO(lab)
	return 0
}
