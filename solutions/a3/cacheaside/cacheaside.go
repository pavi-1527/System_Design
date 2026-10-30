// Package cacheaside is a read-through cache-aside layer for one source of
// truth, plus the stampede protection it needs: an in-flight-call group
// ("singleflight"), negative caching, short error caching, TTL jitter,
// invalidation that cannot be undone by a slow concurrent load, and
// stale-while-revalidate.
//
// It is the lab for A3 Unit 2 §5–§7. Suggested order: Group (the tests named
// TestGroup_*), then Cache.Get's hit/miss path with TTLs, then negative and
// error caching, then Invalidate, then stale-while-revalidate. Run:
// go test -race ./labs/a3/cacheaside/
//
// # Group
//
// Group.Do(ctx, key, fn) collapses concurrent calls for one key into a
// single execution of fn:
//
//   - The first caller (the leader) starts fn in its own goroutine. Later
//     callers for the same key, while that call is in flight, join it and
//     receive the same value and error. Group keeps no results: once fn
//     returns, the next Do for the key runs fn again. Different keys never
//     block each other.
//   - fn's context is detached from every caller's cancellation (it keeps the
//     leader's values) and is cancelled only when ALL callers have given up.
//     One caller cancelling gets ctx.Err() immediately and nobody else is
//     affected; the shared load keeps running. When the last caller leaves,
//     the call is removed from the group (a new Do starts a fresh call even
//     if the abandoned fn is still winding down) and fn's context is
//     cancelled.
//   - A ctx that is already done returns ctx.Err() without starting or
//     joining anything.
//   - If fn panics, every caller gets a *PanicError instead of hanging, the
//     process survives, and the key is usable again.
//   - shared (the second return value) reports whether this caller joined a
//     call started by someone else.
//
// # Cache
//
// Cache.Get(ctx, key) returns a fresh cached value or loads it through Group.
//
//   - A value is fresh for TTL, reduced by jitter: effective TTL =
//     TTL * (1 - Jitter*Rand()). Jitter applies to positive entries only. Rand
//     is called once per stored value, with the cache's lock held, so it
//     needs no locking of its own.
//   - If the loader returns an error satisfying errors.Is(err, ErrNotFound),
//     the "not found" answer is cached for NegativeTTL (0 = not cached) and
//     every Get within that time returns the same error without calling the
//     loader. Other errors are cached for ErrorTTL (0 = not cached).
//     Errors that satisfy errors.Is(err, context.Canceled) or
//     context.DeadlineExceeded are never cached: they say nothing about the
//     key.
//   - Concurrent Gets for a cold key call the loader once (Stats.Coalesced
//     counts the Gets that joined). The loader's result is stored once, by
//     the leader, not by each waiter, so a waiter that gives up does not
//     lose the load.
//   - Invalidate(key) deletes the entry AND prevents a load that is already
//     in flight from storing its (possibly stale) result. The in-flight
//     callers still receive that result; the next Get loads again.
//   - Stale-while-revalidate: with StaleTTL > 0, a value that is past its
//     TTL but within StaleTTL after it is returned immediately (counted in
//     Stats.StaleServed) and refreshed by exactly one background load. If the
//     refresh fails with an ordinary error the stale value stays; if it says
//     ErrNotFound the entry is replaced by the negative entry (or removed).
//   - Good practice (not directly tested): before calling the loader the
//     leader re-checks the cache, so a Get that missed just before another
//     load finished does not load again.
package cacheaside

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"runtime/debug"
	"sync"
	"time"
)

// ErrNotFound is returned (possibly wrapped) by a loader when the key does
// not exist in the source of truth.
var ErrNotFound = errors.New("cacheaside: not found")

// PanicError is returned to every caller of a Group call whose fn panicked.
type PanicError struct {
	Value any
	Stack []byte
}

func (e *PanicError) Error() string { return fmt.Sprintf("cacheaside: loader panicked: %v", e.Value) }

// ---------- Group ----------

type call[V any] struct {
	done    chan struct{} // closed when val/err are set
	val     V
	err     error
	waiters int
	cancel  context.CancelFunc
}

// Group collapses concurrent calls with the same key. The zero value is ready
// to use.
type Group[V any] struct {
	mu    sync.Mutex
	calls map[string]*call[V]
}

// Do runs fn once for all concurrent callers of key; see the package doc.
func (g *Group[V]) Do(ctx context.Context, key string, fn func(ctx context.Context) (V, error)) (V, bool, error) {
	var zero V
	if err := ctx.Err(); err != nil {
		return zero, false, err
	}
	g.mu.Lock()
	if g.calls == nil {
		g.calls = make(map[string]*call[V])
	}
	c, joined := g.calls[key]
	if joined {
		c.waiters++
	} else {
		loadCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
		c = &call[V]{done: make(chan struct{}), waiters: 1, cancel: cancel}
		g.calls[key] = c
		go g.run(key, c, loadCtx, fn)
	}
	g.mu.Unlock()

	select {
	case <-c.done:
		return c.val, joined, c.err
	case <-ctx.Done():
		select {
		case <-c.done: // finished at the same instant: prefer the result
			return c.val, joined, c.err
		default:
		}
		g.mu.Lock()
		c.waiters--
		if c.waiters == 0 {
			if g.calls[key] == c {
				delete(g.calls, key)
			}
			c.cancel()
		}
		g.mu.Unlock()
		return zero, joined, ctx.Err()
	}
}

func (g *Group[V]) run(key string, c *call[V], ctx context.Context, fn func(context.Context) (V, error)) {
	defer func() {
		if r := recover(); r != nil {
			var zero V
			c.val, c.err = zero, &PanicError{Value: r, Stack: debug.Stack()}
		}
		g.mu.Lock()
		if g.calls[key] == c {
			delete(g.calls, key)
		}
		g.mu.Unlock()
		c.cancel()
		close(c.done)
	}()
	c.val, c.err = fn(ctx)
}

// Waiters returns the number of callers currently blocked in Do for key.
func (g *Group[V]) Waiters(key string) int {
	g.mu.Lock()
	defer g.mu.Unlock()
	if c, ok := g.calls[key]; ok {
		return c.waiters
	}
	return 0
}

// ---------- Cache ----------

// Config configures a Cache.
type Config struct {
	TTL         time.Duration    // freshness of a loaded value; must be > 0 to cache values
	NegativeTTL time.Duration    // freshness of an ErrNotFound answer; 0 = don't cache
	ErrorTTL    time.Duration    // freshness of other loader errors; 0 = don't cache
	StaleTTL    time.Duration    // extra window after TTL in which stale values are served; 0 = off
	Jitter      float64          // 0..1: effective TTL = TTL * (1 - Jitter*Rand())
	Rand        func() float64   // in [0,1); default math/rand.Float64
	Now         func() time.Time // default time.Now
}

// Stats counts what the cache did.
type Stats struct {
	Hits         uint64 // fresh positive hits
	Misses       uint64 // Gets that had to wait for a load (leaders and joiners)
	NegativeHits uint64 // fresh negative (ErrNotFound) hits
	ErrorHits    uint64 // fresh cached-error hits
	Loads        uint64 // loader invocations (including background refreshes)
	Coalesced    uint64 // Gets that joined a load started by another Get
	StaleServed  uint64 // Gets answered with a stale value
	Refreshes    uint64 // background refreshes started
}

type kind int

const (
	kindValue kind = iota + 1
	kindNotFound
	kindError
)

type entry[V any] struct {
	kind       kind
	val        V
	err        error
	freshUntil time.Time
	staleUntil time.Time // only for kindValue
	refreshing bool
}

// loadState is the bookkeeping for one in-flight load of a key.
type loadState struct{ invalidated bool }

// Cache is a cache-aside layer over a loader. It has no size bound; pair it
// with an eviction policy (see the lru lab) in real systems.
type Cache[V any] struct {
	cfg     Config
	load    func(ctx context.Context, key string) (V, error)
	group   Group[V]
	mu      sync.Mutex
	entries map[string]*entry[V]
	loading map[string]*loadState
	st      Stats
}

// New returns a Cache that loads missing keys with load.
func New[V any](cfg Config, load func(ctx context.Context, key string) (V, error)) *Cache[V] {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Rand == nil {
		cfg.Rand = rand.Float64
	}
	if cfg.Jitter < 0 {
		cfg.Jitter = 0
	}
	if cfg.Jitter > 1 {
		cfg.Jitter = 1
	}
	return &Cache[V]{
		cfg:     cfg,
		load:    load,
		entries: make(map[string]*entry[V]),
		loading: make(map[string]*loadState),
	}
}

// Get returns the value for key, loading it if needed. See the package doc.
func (c *Cache[V]) Get(ctx context.Context, key string) (V, error) {
	var zero V
	now := c.cfg.Now()
	c.mu.Lock()
	if e := c.entries[key]; e != nil {
		switch {
		case now.Before(e.freshUntil):
			switch e.kind {
			case kindValue:
				c.st.Hits++
				v := e.val
				c.mu.Unlock()
				return v, nil
			case kindNotFound:
				c.st.NegativeHits++
				err := e.err
				c.mu.Unlock()
				return zero, err
			default:
				c.st.ErrorHits++
				err := e.err
				c.mu.Unlock()
				return zero, err
			}
		case e.kind == kindValue && now.Before(e.staleUntil):
			c.st.StaleServed++
			start := !e.refreshing
			if start {
				e.refreshing = true
				c.st.Refreshes++
			}
			v := e.val
			c.mu.Unlock()
			if start {
				go c.refresh(ctx, key)
			}
			return v, nil
		}
	}
	c.st.Misses++
	c.mu.Unlock()

	if breakNoSingleflight {
		return c.fill(ctx, key, false)
	}
	v, joined, err := c.group.Do(ctx, key, func(ctx context.Context) (V, error) {
		return c.fill(ctx, key, false)
	})
	if joined {
		c.mu.Lock()
		c.st.Coalesced++
		c.mu.Unlock()
	}
	return v, err
}

// refresh reloads a stale entry in the background.
func (c *Cache[V]) refresh(ctx context.Context, key string) {
	ctx = context.WithoutCancel(ctx)
	_, _, _ = c.group.Do(ctx, key, func(ctx context.Context) (V, error) {
		return c.fill(ctx, key, true)
	})
	c.mu.Lock()
	if e := c.entries[key]; e != nil {
		e.refreshing = false
	}
	c.mu.Unlock()
}

// fill is the body of a load: executed once per Group call, by the leader.
func (c *Cache[V]) fill(ctx context.Context, key string, refreshing bool) (V, error) {
	var zero V
	c.mu.Lock()
	if e := c.entries[key]; e != nil && e.kind == kindValue && c.cfg.Now().Before(e.freshUntil) {
		v := e.val // another load finished between our miss and now
		c.mu.Unlock()
		return v, nil
	}
	ls := &loadState{}
	c.loading[key] = ls
	c.st.Loads++
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		if c.loading[key] == ls {
			delete(c.loading, key)
		}
		c.mu.Unlock()
	}()

	v, err := c.load(ctx, key) // a panic here unwinds through the defer above

	c.mu.Lock()
	defer c.mu.Unlock()
	if ls.invalidated && !breakNoInvalidateGuard {
		return v, err // hand the result to the callers, but do not store it
	}
	now := c.cfg.Now()
	switch {
	case err == nil:
		if c.cfg.TTL > 0 {
			fresh := now.Add(c.jittered(c.cfg.TTL))
			c.entries[key] = &entry[V]{kind: kindValue, val: v, freshUntil: fresh, staleUntil: fresh.Add(c.cfg.StaleTTL)}
		}
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		// Never cache: says nothing about the key.
	case errors.Is(err, ErrNotFound):
		if c.cfg.NegativeTTL > 0 {
			c.entries[key] = &entry[V]{kind: kindNotFound, err: err, freshUntil: now.Add(c.cfg.NegativeTTL)}
		} else if refreshing {
			delete(c.entries, key) // the stale value is known to be wrong
		}
	default:
		if refreshing {
			return zero, err // keep serving the stale value
		}
		if c.cfg.ErrorTTL > 0 {
			c.entries[key] = &entry[V]{kind: kindError, err: err, freshUntil: now.Add(c.cfg.ErrorTTL)}
		}
	}
	return v, err
}

func (c *Cache[V]) jittered(ttl time.Duration) time.Duration {
	if c.cfg.Jitter <= 0 {
		return ttl
	}
	return time.Duration(float64(ttl) * (1 - c.cfg.Jitter*c.cfg.Rand()))
}

// Invalidate removes key and stops any load already in flight for it from
// storing its result.
func (c *Cache[V]) Invalidate(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.entries, key)
	if ls := c.loading[key]; ls != nil {
		ls.invalidated = true
	}
}

// InFlight returns the number of Gets currently waiting on a load of key.
func (c *Cache[V]) InFlight(key string) int { return c.group.Waiters(key) }

// Stats returns a snapshot of the counters.
func (c *Cache[V]) Stats() Stats {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.st
}
