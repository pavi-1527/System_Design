package ratelimit

import (
	"context"
	"time"
)

// NewTimerFunc creates a timer that fires once after d. It returns the
// channel to receive on and a stop function. Wait uses it so tests can drive
// time by hand; production uses the real clock's timers.
type NewTimerFunc func(d time.Duration) (c <-chan time.Time, stop func() bool)

func realTimer(d time.Duration) (<-chan time.Time, func() bool) {
	t := time.NewTimer(d)
	return t.C, t.Stop
}

// WithTimer sets the timer source used by Wait. Use it together with
// WithClock so both come from the same (fake) clock.
func WithTimer(f NewTimerFunc) Option {
	return func(l *Limiter) {
		if f == nil {
			l.cfgErr = ErrInvalidConfig
			return
		}
		l.newTimer = f
	}
}

// WithMaxKeys hard-caps the number of tracked keys (approximately: the cap is
// split evenly over the shards, rounding up). When a new key arrives at a full
// shard, that shard's least recently used key is forgotten even if it is not
// idle. That FAILS OPEN: the forgotten key gets a full allowance the next time
// it shows up. Idle keys are reclaimed long before this matters; the cap is
// the backstop for key-flooding attacks.
func WithMaxKeys(n int) Option {
	return func(l *Limiter) {
		if n < 1 {
			l.cfgErr = ErrInvalidConfig
			return
		}
		l.maxKeys = n
	}
}

// Stats is a point-in-time copy of the limiter's counters. Shards are read one
// after another, so under concurrent traffic the fields are not mutually
// consistent to the instant.
type Stats struct {
	Allowed       uint64 // decisions that granted permits
	Denied        uint64 // decisions that did not (each retry inside Wait counts)
	Keys          int    // keys currently tracked
	IdleEvicted   uint64 // keys dropped because they were idle: free, invisible
	ForcedEvicted uint64 // keys dropped by WithMaxKeys while NOT idle: fail-open
}

// Stats returns a snapshot of the counters.
func (l *Limiter) Stats() Stats {
	var s Stats
	for _, sh := range l.shards {
		sh.mu.Lock()
		s.Allowed += sh.allowed
		s.Denied += sh.denied
		s.Keys += len(sh.m)
		s.IdleEvicted += sh.evicted
		s.ForcedEvicted += sh.forced
		sh.mu.Unlock()
	}
	return s
}

// Sweep drops every idle key it can reach and returns how many it dropped.
// New keys already trigger a little sweeping each (so memory is bounded
// without it); call Sweep from a ticker to release memory after a burst of
// traffic ends and the limiter goes quiet. Sweeping stops at the first
// non-idle key in least-recently-used order, so a long-recovering key can
// delay reclaiming the keys behind it by at most one recovery time.
func (l *Limiter) Sweep() int {
	now := l.since()
	removed := 0
	for _, sh := range l.shards {
		sh.mu.Lock()
		t := now
		if t < sh.hwm {
			t = sh.hwm
		} else {
			sh.hwm = t
		}
		removed += sh.sweep(t, -1)
		sh.mu.Unlock()
	}
	return removed
}

// Wait blocks until one permit for key is available and consumes it, or until
// ctx is done. It is WaitN(ctx, key, 1).
func (l *Limiter) Wait(ctx context.Context, key string) error { return l.WaitN(ctx, key, 1) }

// WaitN blocks until n permits for key are available and consumes them. It
// returns ErrInvalidN or ErrExceedsLimit immediately for requests that can
// never succeed, and ctx.Err() if ctx is done first, in which case nothing
// was consumed. A ctx that is already done returns ctx.Err() without
// consuming anything, even if permits are available.
//
// Waiters are not queued: each one sleeps until the decision's RetryAfter and
// then tries again, so wake-up order is not FIFO and a large n can be starved
// by a stream of small ones. No goroutine is started; the only resource held
// while waiting is one timer, which is stopped on every exit path.
func (l *Limiter) WaitN(ctx context.Context, key string, n int) error {
	for {
		// select picks randomly when both are ready, so check ctx explicitly
		// before every attempt: a cancelled caller must never take a permit.
		if err := ctx.Err(); err != nil {
			return err
		}
		d, err := l.take(key, n)
		if err != nil {
			return err
		}
		if d.Allowed {
			return nil
		}
		c, stop := l.newTimer(d.RetryAfter)
		select {
		case <-c:
		case <-ctx.Done():
			stop() // release the timer now, not when it would have fired
			return ctx.Err()
		}
	}
}

// ---- LRU list and eviction (all require sh.mu) --------------------------------

func (sh *shard) pushFront(e *entry) {
	e.prev, e.next = &sh.root, sh.root.next
	sh.root.next.prev = e
	sh.root.next = e
}

func (sh *shard) unlink(e *entry) {
	e.prev.next, e.next.prev = e.next, e.prev
	e.prev, e.next = nil, nil
}

func (sh *shard) moveToFront(e *entry) {
	if sh.root.next == e {
		return
	}
	sh.unlink(e)
	sh.pushFront(e)
}

// sweep drops up to budget idle entries from the LRU end (all of them when
// budget < 0). It stops at the first entry that is not idle.
func (sh *shard) sweep(now time.Duration, budget int) int {
	removed := 0
	for budget != 0 {
		e := sh.root.prev
		if e == &sh.root || !e.s.Idle(now) {
			break
		}
		sh.unlink(e)
		delete(sh.m, e.key)
		sh.evicted++
		removed++
		budget--
	}
	return removed
}

// trim enforces the per-shard key cap after an insertion.
func (sh *shard) trim(now time.Duration) {
	for sh.cap > 0 && len(sh.m) > sh.cap {
		e := sh.root.prev
		sh.unlink(e)
		delete(sh.m, e.key)
		if e.s.Idle(now) {
			sh.evicted++
		} else {
			sh.forced++
		}
	}
}
