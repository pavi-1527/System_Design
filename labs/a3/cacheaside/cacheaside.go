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
	"sync"
	"time"
)

// ErrNotFound is returned (possibly wrapped) by a loader when the key does
// not exist in the source of truth.
var ErrNotFound = errors.New("cacheaside: not found")

var errNotImplemented = errors.New("cacheaside: not implemented")

// PanicError is returned to every caller of a Group call whose fn panicked.
type PanicError struct {
	Value any
	Stack []byte
}

func (e *PanicError) Error() string { return fmt.Sprintf("cacheaside: loader panicked: %v", e.Value) }

// ---------- Group ----------

// call is one in-flight execution of fn, shared by every caller of its key.
type call[V any] struct {
	done    chan struct{} // closed after val and err are set
	val     V
	err     error
	waiters int                // callers currently blocked in Do for this call
	cancel  context.CancelFunc // cancels the context handed to fn
}

// Group collapses concurrent calls with the same key. The zero value is ready
// to use. Provided: the types; the methods are yours.
type Group[V any] struct {
	mu    sync.Mutex
	calls map[string]*call[V]
}

// Do runs fn once for all concurrent callers of key; see the package doc for
// the exact rules.
//
// Hints:
//   - Under g.mu: look the key up. Joining = waiters++. Starting = create the
//     call with waiters 1, a context from context.WithCancel(
//     context.WithoutCancel(ctx)), put it in the map, and `go g.run(...)`.
//   - Then wait: select on c.done and ctx.Done(). On ctx.Done(), re-take g.mu,
//     waiters--; if it hit 0, remove the call from the map (only if the map
//     still holds THIS call) and cancel its context.
//   - Return (value, joined, err) where joined is true for callers that did
//     not start the call.
func (g *Group[V]) Do(ctx context.Context, key string, fn func(ctx context.Context) (V, error)) (V, bool, error) {
	// TODO(lab)
	var zero V
	return zero, false, errNotImplemented
}

// run executes fn for one call. Hints: recover() a panic into a *PanicError
// (runtime/debug.Stack() gives the stack); then, under g.mu, remove the call
// from the map if it is still there; cancel the call's context; and finally
// close(c.done). The order matters: waiters must be released even on panic.
func (g *Group[V]) run(key string, c *call[V], ctx context.Context, fn func(context.Context) (V, error)) {
	// TODO(lab)
}

// Waiters returns the number of callers currently blocked in Do for key
// (0 if no call is in flight). The tests use it to know when N goroutines
// have really joined a call.
func (g *Group[V]) Waiters(key string) int {
	// TODO(lab)
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

// Stats counts what the cache did. Every Get is counted exactly once in one
// of Hits, Misses, NegativeHits, ErrorHits or StaleServed.
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

// entry is what the cache stores per key. Provided.
type entry[V any] struct {
	kind       kind
	val        V
	err        error
	freshUntil time.Time
	staleUntil time.Time // only for kindValue
	refreshing bool      // a background refresh of this entry is running
}

// loadState is the bookkeeping for one in-flight load of a key; Invalidate
// sets invalidated so the load does not store its result. Provided.
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

// New returns a Cache that loads missing keys with load. Provided.
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
//
// Hints:
//   - Under c.mu look at the entry. now < freshUntil: a hit (kindValue), a
//     negative hit (return the stored error) or an error hit. kindValue past
//     freshUntil but before staleUntil: count StaleServed, return the value,
//     and if !e.refreshing set it and start `go c.refresh(ctx, key)`.
//   - Otherwise count a miss, unlock, and call c.group.Do with a function that
//     calls c.fill(ctx, key, false). Count Coalesced when Do says joined.
//   - Do not hold c.mu while waiting or while the loader runs.
//   - Break-it: when breakNoSingleflight is true (go test -tags breakit), call
//     c.fill directly instead of going through the group.
func (c *Cache[V]) Get(ctx context.Context, key string) (V, error) {
	// TODO(lab)
	var zero V
	return zero, errNotImplemented
}

// refresh reloads a stale entry in the background (detach ctx with
// context.WithoutCancel), then clears the entry's refreshing flag.
func (c *Cache[V]) refresh(ctx context.Context, key string) {
	// TODO(lab)
}

// fill is the body of a load: it runs once per Group call, in the leader.
// It registers a loadState in c.loading (and removes it when done, even on
// panic), counts a Load, calls c.load WITHOUT holding c.mu, then stores the
// outcome under c.mu unless the loadState was invalidated (and
// breakNoInvalidateGuard is false). What to store:
//
//	nil error                    -> kindValue for jittered(TTL), if TTL > 0
//	context.Canceled/DeadlineExceeded -> nothing
//	errors.Is(err, ErrNotFound)  -> kindNotFound for NegativeTTL, if > 0
//	                                 (when refreshing and NegativeTTL == 0,
//	                                 delete the stale entry)
//	any other error              -> kindError for ErrorTTL, if > 0; but when
//	                                 refreshing, store nothing so the stale
//	                                 value keeps being served
//
// Optional hardening: at the start, if a fresh value is already cached
// (another load just finished), return it instead of loading again.
func (c *Cache[V]) fill(ctx context.Context, key string, refreshing bool) (V, error) {
	// TODO(lab)
	var zero V
	return zero, errNotImplemented
}

// jittered returns ttl reduced by the configured jitter. Provided.
func (c *Cache[V]) jittered(ttl time.Duration) time.Duration {
	if c.cfg.Jitter <= 0 {
		return ttl
	}
	return time.Duration(float64(ttl) * (1 - c.cfg.Jitter*c.cfg.Rand()))
}

// Invalidate removes key and stops any load already in flight for it from
// storing its result (set loadState.invalidated).
func (c *Cache[V]) Invalidate(key string) {
	// TODO(lab)
}

// InFlight returns the number of Gets currently waiting on a load of key.
// Provided.
func (c *Cache[V]) InFlight(key string) int { return c.group.Waiters(key) }

// Stats returns a snapshot of the counters. Provided.
func (c *Cache[V]) Stats() Stats {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.st
}
