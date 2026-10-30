package hashcache

import (
	"context"
	"errors"
	"time"
)

// Stage 2 (≈ 25 min): a client that routes keys to backends with the ring,
// keeps R copies, tolerates failures and remembers which nodes are down.

// Stage 2 errors.
var (
	// ErrNotFound is returned by a Backend (and by Client.Get) for a miss.
	ErrNotFound = errors.New("hashcache: not found")
	// ErrExists is returned by Backend.Add when the key is already present.
	ErrExists = errors.New("hashcache: already exists")
	// ErrUnavailable is returned by Client.Get when no replica could answer.
	ErrUnavailable = errors.New("hashcache: no replica available")
	// ErrQuorum is returned (wrapped) by Client.Set/Delete when fewer than
	// WriteQuorum replicas acknowledged. Replicas that did acknowledge keep
	// the change.
	ErrQuorum = errors.New("hashcache: write quorum not reached")
	// ErrNoNodes is returned when a client would have no backends.
	ErrNoNodes = errors.New("hashcache: no backends")
)

// Backend is one cache node (think memcached or Redis). Implementations are
// safe for concurrent use. Get returns ErrNotFound on a miss; any other error
// means the node failed.
type Backend interface {
	Get(ctx context.Context, key string) ([]byte, error)
	Set(ctx context.Context, key string, val []byte, ttl time.Duration) error
	// Add stores val only if key is absent, else returns ErrExists
	// (memcached add, Redis SET NX). Stage 3 uses it.
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

// Client routes keys to backends with a consistent-hash ring.
//
// Contract:
//   - Placement is identical to a Ring built with NewRing(cfg.Vnodes) and
//     Add(node, 1) for every backend, so tests (and you) can predict it.
//   - A key lives on the first R HEALTHY nodes clockwise from its position.
//     A down node is skipped, so with R=1 its keys fail over to the next node:
//     the cache degrades, it does not fail.
//   - Get tries those replicas in order and returns the first hit. If none
//     hits but at least one answered with a miss: ErrNotFound. If none could
//     answer at all: ErrUnavailable (never report a failure as a miss).
//   - Set and Delete go to every replica and succeed iff at least WriteQuorum
//     acknowledged (an error wrapping ErrQuorum otherwise); they never stop
//     early, so the replicas converge. Delete of an absent key is not an error.
//   - A node is marked down after FailThreshold CONSECUTIVE failures. A clean
//     ErrNotFound is a success. Errors while the caller's own ctx is done say
//     nothing about the node and are ignored. A down node is skipped until
//     DownFor has passed on cfg.Now; after that requests reach it again, and
//     ONE more failure marks it down immediately. Any success resets the count.
//
// A Client is safe for concurrent use.
type Client struct {
	// TODO(lab): config, how a request gets its ring+backends, health state.
	cfg Config
}

// NewClient returns a Client over the given backends (name -> backend).
// Errors: ErrNoNodes for an empty map; an error for a nil backend.
func NewClient(cfg Config, backends map[string]Backend) (*Client, error) {
	// TODO(lab): apply defaults, build the ring, keep ring and backends together.
	_, _ = cfg, backends
	return nil, errNotImplemented
}

// Healthy reports whether node is currently considered up.
func (c *Client) Healthy(node string) bool {
	// TODO(lab)
	_ = node
	return false
}

// Get returns the value for key, ErrNotFound on a miss or ErrUnavailable.
func (c *Client) Get(ctx context.Context, key string) ([]byte, error) {
	// TODO(lab)
	_, _ = ctx, key
	return nil, errNotImplemented
}

// Set stores val under key with the given TTL on every replica.
func (c *Client) Set(ctx context.Context, key string, val []byte, ttl time.Duration) error {
	// TODO(lab)
	_, _, _, _ = ctx, key, val, ttl
	return errNotImplemented
}

// Delete removes key from every replica, with the same quorum rule as Set.
func (c *Client) Delete(ctx context.Context, key string) error {
	// TODO(lab)
	_, _ = ctx, key
	return errNotImplemented
}
