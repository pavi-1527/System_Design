package hashcache

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

// Stage 2 errors.
var (
	// ErrNotFound is returned by a Backend (and by Client.Get) for a miss.
	ErrNotFound = errors.New("hashcache: not found")
	// ErrExists is returned by Backend.Add when the key is already present.
	ErrExists = errors.New("hashcache: already exists")
	// ErrUnavailable is returned by Client.Get when no replica could answer.
	ErrUnavailable = errors.New("hashcache: no replica available")
	// ErrQuorum is returned by Client.Set/Delete when fewer than WriteQuorum
	// replicas acknowledged. The replicas that did acknowledge keep the change.
	ErrQuorum = errors.New("hashcache: write quorum not reached")
	// ErrNoNodes is returned when a client would have no backends.
	ErrNoNodes = errors.New("hashcache: no backends")
)

// Backend is one cache node (think memcached or Redis). Implementations must
// be safe for concurrent use. Get returns ErrNotFound on a miss; any other
// error means the node failed.
type Backend interface {
	Get(ctx context.Context, key string) ([]byte, error)
	Set(ctx context.Context, key string, val []byte, ttl time.Duration) error
	// Add stores val only if key is absent, else returns ErrExists
	// (memcached add, Redis SET NX). Stage 3 uses it for migration.
	Add(ctx context.Context, key string, val []byte, ttl time.Duration) error
	Delete(ctx context.Context, key string) error
}

// Config configures a Client. Zero values take the defaults shown.
type Config struct {
	Vnodes        int           // virtual nodes per node; default DefaultVnodes
	Replicas      int           // R: copies of each key; default 1
	WriteQuorum   int           // W: replica acks needed by Set/Delete; default 1, at most Replicas
	FailThreshold int           // consecutive failures that mark a node down; default 3
	DownFor       time.Duration // how long a down node is skipped; default 5s
	Now           func() time.Time
}

func (c *Config) defaults() {
	if c.Vnodes < 1 {
		c.Vnodes = DefaultVnodes
	}
	if c.Replicas < 1 {
		c.Replicas = 1
	}
	if c.WriteQuorum < 1 {
		c.WriteQuorum = 1
	}
	if c.WriteQuorum > c.Replicas {
		c.WriteQuorum = c.Replicas
	}
	if c.FailThreshold < 1 {
		c.FailThreshold = 3
	}
	if c.DownFor <= 0 {
		c.DownFor = 5 * time.Second
	}
	if c.Now == nil {
		c.Now = time.Now
	}
}

// state is everything one request needs, immutable once published. A request
// loads the pointer once and uses that snapshot throughout, so a concurrent
// membership change can never give it a ring from one generation and backends
// from another.
type state struct {
	ring     *Ring
	backends map[string]Backend

	prev       *state // previous membership during a migration grace window
	prevUntil  time.Time
	migrateTTL time.Duration
}

type nodeHealth struct {
	fails     int
	downUntil time.Time
}

// Client routes keys to backends with a consistent-hash ring.
//
//   - Placement is identical to a Ring built with NewRing(cfg.Vnodes) and
//     Add(node, 1) for every backend, so tests (and you) can predict it.
//   - A key lives on the first R HEALTHY nodes clockwise from its position.
//     A down node is skipped, so with R=1 its keys fail over to the next
//     node: the cache degrades, it does not fail.
//   - Get tries those replicas in order and returns the first hit.
//   - Set and Delete go to every replica (sequentially, in ring order) and
//     succeed iff at least WriteQuorum acknowledged; they never stop early.
//   - A node is marked down after FailThreshold consecutive errors (a clean
//     ErrNotFound is a success; errors caused by the caller's cancelled
//     context are ignored) and is skipped for DownFor; afterwards requests
//     reach it again and one more failure marks it down immediately.
//
// A Client is safe for concurrent use.
type Client struct {
	cfg   Config
	st    atomic.Pointer[state]
	setMu sync.Mutex // serialises membership changes (stage3.go)

	hmu    sync.Mutex
	health map[string]*nodeHealth
}

// NewClient returns a Client over the given backends (name -> backend).
func NewClient(cfg Config, backends map[string]Backend) (*Client, error) {
	cfg.defaults()
	c := &Client{cfg: cfg, health: map[string]*nodeHealth{}}
	st, err := c.build(backends)
	if err != nil {
		return nil, err
	}
	c.st.Store(st)
	return c, nil
}

func (c *Client) build(backends map[string]Backend) (*state, error) {
	if len(backends) == 0 {
		return nil, ErrNoNodes
	}
	ring := NewRing(c.cfg.Vnodes)
	own := make(map[string]Backend, len(backends))
	for n, b := range backends {
		if b == nil {
			return nil, fmt.Errorf("hashcache: nil backend for %q", n)
		}
		if err := ring.Add(n, 1); err != nil {
			return nil, err
		}
		own[n] = b
	}
	return &state{ring: ring, backends: own}, nil
}

// Healthy reports whether node is currently considered up.
func (c *Client) Healthy(node string) bool {
	c.hmu.Lock()
	defer c.hmu.Unlock()
	return c.healthyLocked(node)
}

func (c *Client) healthyLocked(node string) bool {
	h := c.health[node]
	return h == nil || !c.cfg.Now().Before(h.downUntil)
}

// record updates a node's health after a call.
func (c *Client) record(ctx context.Context, node string, err error) {
	c.hmu.Lock()
	defer c.hmu.Unlock()
	h := c.health[node]
	if h == nil {
		h = &nodeHealth{}
		c.health[node] = h
	}
	switch {
	case err == nil || errors.Is(err, ErrNotFound) || errors.Is(err, ErrExists):
		h.fails = 0
		h.downUntil = time.Time{}
	case ctx.Err() != nil:
		// The caller gave up; that says nothing about the node.
	default:
		h.fails++
		if h.fails >= c.cfg.FailThreshold {
			h.downUntil = c.cfg.Now().Add(c.cfg.DownFor)
		}
	}
}

// replicasFor returns the first R healthy nodes clockwise for key in st.
func (c *Client) replicasFor(st *state, key string) []string {
	c.hmu.Lock()
	down := 0
	for n := range st.backends {
		if !c.healthyLocked(n) {
			down++
		}
	}
	cands := st.ring.Replicas(key, c.cfg.Replicas+down)
	out := make([]string, 0, c.cfg.Replicas)
	for _, n := range cands {
		if len(out) == c.cfg.Replicas {
			break
		}
		if c.healthyLocked(n) {
			out = append(out, n)
		}
	}
	c.hmu.Unlock()
	return out
}

// getFrom reads key from st's replicas: the value, ErrNotFound if at least one
// replica answered with a miss, or ErrUnavailable if none could answer.
func (c *Client) getFrom(ctx context.Context, st *state, key string) ([]byte, error) {
	missed := false
	for _, n := range c.replicasFor(st, key) {
		v, err := st.backends[n].Get(ctx, key)
		c.record(ctx, n, err)
		switch {
		case err == nil:
			return v, nil
		case errors.Is(err, ErrNotFound):
			missed = true
		}
	}
	if missed {
		return nil, ErrNotFound
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return nil, ErrUnavailable
}

// Get returns the value for key, ErrNotFound on a miss, or ErrUnavailable if
// no replica could answer. During a Stage 3 migration window it also consults
// the previous owners (see SetMembership).
func (c *Client) Get(ctx context.Context, key string) ([]byte, error) {
	st := c.st.Load()
	v, err := c.getFrom(ctx, st, key)
	if err == nil || (!errors.Is(err, ErrNotFound) && !errors.Is(err, ErrUnavailable)) {
		return v, err
	}
	if pv, ok := c.getFromPrev(ctx, st, key); ok {
		return pv, nil
	}
	return nil, err
}

// writeAll applies op to every replica and counts acknowledgements.
func (c *Client) writeAll(ctx context.Context, st *state, key string, op func(Backend) error) error {
	nodes := c.replicasFor(st, key)
	acks := 0
	var last error
	for _, n := range nodes {
		err := op(st.backends[n])
		c.record(ctx, n, err)
		if err == nil {
			acks++
		} else {
			last = err
		}
	}
	if acks < c.cfg.WriteQuorum {
		if last == nil {
			last = ErrUnavailable
		}
		return fmt.Errorf("%w: %d/%d acks (need %d): %v", ErrQuorum, acks, len(nodes), c.cfg.WriteQuorum, last)
	}
	return nil
}

// Set stores val under key on every replica with the given TTL. It returns
// nil iff at least WriteQuorum replicas acknowledged, otherwise an error
// wrapping ErrQuorum.
func (c *Client) Set(ctx context.Context, key string, val []byte, ttl time.Duration) error {
	st := c.st.Load()
	err := c.writeAll(ctx, st, key, func(b Backend) error { return b.Set(ctx, key, val, ttl) })
	c.dropFromPrev(ctx, st, key)
	return err
}

// Delete removes key from every replica, with the same quorum rule as Set.
// A replica that was down keeps its stale copy until its TTL expires.
func (c *Client) Delete(ctx context.Context, key string) error {
	st := c.st.Load()
	err := c.writeAll(ctx, st, key, func(b Backend) error {
		if err := b.Delete(ctx, key); err != nil && !errors.Is(err, ErrNotFound) {
			return err
		}
		return nil
	})
	c.dropFromPrev(ctx, st, key)
	return err
}
