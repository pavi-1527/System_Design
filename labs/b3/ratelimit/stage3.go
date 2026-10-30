package ratelimit

import (
	"context"
	"time"
)

// ---- Stage 3: bounded memory, blocking Wait, stats (minute 45) --------------
//
// Contract:
//   - Memory is bounded by recent activity, not by key cardinality. A key
//     whose Strategy is Idle at time now may be forgotten with no visible
//     effect, and MUST be forgotten eventually: 200,000 one-off keys, 1ms
//     apart, with a 1s recovery time must never leave more than a few
//     thousand tracked. No janitor goroutine is needed: reclaim as a side
//     effect of new keys arriving (amortised O(1) per call), plus the
//     explicit Sweep. NEVER forget a key that is not idle: that hands the
//     caller a free burst.
//   - WithMaxKeys(n) is a hard backstop for key-flooding: a new key arriving
//     at a full limiter evicts the least recently used key even if it is not
//     idle. That fails OPEN (the evicted key starts fresh) and is counted
//     separately in Stats.ForcedEvicted.
//   - WaitN blocks until the permits are available and takes them, or ctx is
//     done. A done ctx returns ctx.Err() and takes NOTHING, even if permits
//     are available. A request that can never succeed returns
//     ErrExceedsLimit / ErrInvalidN immediately, without sleeping. Sleeping
//     uses the injected timer source, so tests drive it with a fake clock; the
//     timer is released on every exit path and no goroutine is started.
//   - Stats counts decisions (every try inside Wait counts) and tracked keys.

// NewTimerFunc creates a timer that fires once after d. It returns the
// channel to receive on and a stop function.
type NewTimerFunc func(d time.Duration) (c <-chan time.Time, stop func() bool)

// WithTimer sets the timer source used by Wait. The default uses the real
// clock's timers.
func WithTimer(f NewTimerFunc) Option {
	return func(l *Limiter) {
		// TODO(lab)
		_ = f
	}
}

// WithMaxKeys hard-caps the number of tracked keys. n < 1 makes the
// constructor return ErrInvalidConfig. With several shards the cap may be
// split between them (rounding up).
func WithMaxKeys(n int) Option {
	return func(l *Limiter) {
		// TODO(lab)
		_ = n
	}
}

// Stats is a point-in-time copy of the limiter's counters.
type Stats struct {
	Allowed       uint64 // decisions that granted permits
	Denied        uint64 // decisions that did not (each retry inside Wait counts)
	Keys          int    // keys currently tracked
	IdleEvicted   uint64 // keys dropped because they were idle
	ForcedEvicted uint64 // keys dropped by WithMaxKeys while NOT idle
}

// Stats returns a snapshot of the counters.
func (l *Limiter) Stats() Stats {
	// TODO(lab)
	return Stats{}
}

// Sweep drops every idle key it can find and returns how many it dropped.
func (l *Limiter) Sweep() int {
	// TODO(lab)
	return 0
}

// Wait blocks until one permit for key is available and takes it, or ctx is
// done. It is WaitN(ctx, key, 1).
func (l *Limiter) Wait(ctx context.Context, key string) error {
	// TODO(lab)
	_, _ = ctx, key
	return errNotImplemented
}

// WaitN blocks until n permits for key are available and takes them, or ctx
// is done. See the stage 3 contract above.
func (l *Limiter) WaitN(ctx context.Context, key string, n int) error {
	// TODO(lab)
	_, _, _ = ctx, key, n
	return errNotImplemented
}
