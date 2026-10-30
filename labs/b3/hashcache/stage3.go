package hashcache

import (
	"errors"
	"time"
)

// Stage 3 (≈ 20 min): change the membership of a live cache without emptying
// it, and place keys with a load bound.

// Stage 3 errors.
var (
	ErrEmptyRing     = errors.New("hashcache: empty ring")
	ErrInvalidFactor = errors.New("hashcache: bounded-load factor must be >= 1")
)

// Migration controls what happens to keys whose owner changes in SetMembership.
type Migration struct {
	// Grace is how long the previous membership stays readable. Zero means a
	// hard cut-over: moved keys simply miss (consistent hashing keeps that to
	// about 1/(N+1) of the keys when one node is added).
	Grace time.Duration
	// TTL is applied to entries copied forward from a previous owner (their
	// remaining TTL is unknown). Zero means one minute.
	TTL time.Duration
}

// SetMembership atomically replaces the set of backends. Requests already in
// flight finish against the membership they started with; new requests see
// the new one. ErrNoNodes (and no change) for an empty map. Only the
// immediately previous membership is kept for the grace window.
//
// During the window (Migration.Grace > 0, measured on Config.Now):
//   - A Get that misses, or finds no healthy replica, on the current owners
//     asks the previous owners, unless they are exactly the same nodes (asking
//     twice would be wasted load). On a hit it copies the value forward to
//     the current owners with Backend.Add (never Set: a newer concurrent write
//     must not be overwritten) and returns it. A moved key is migrated once;
//     later reads hit the new owner directly.
//   - Set and Delete also delete the key from previous owners that are no
//     longer owners, so a stale copy cannot be migrated back later (a deleted
//     key must stay deleted).
//   - Backends of removed nodes stay readable but receive no new writes.
func (c *Client) SetMembership(backends map[string]Backend, m Migration) error {
	// TODO(lab)
	_, _ = backends, m
	return errNotImplemented
}

// AssignBounded places keys on the ring's nodes with "consistent hashing with
// bounded loads": no node receives more than ceil(c * U / N) keys, where U is
// the number of DISTINCT keys and N the number of nodes. Keys are processed in
// order (duplicates ignored); each goes to its natural owner unless that node
// is full, then to the next node clockwise that has room. c must be at least 1
// (ErrInvalidFactor); an empty ring is ErrEmptyRing. c = 1.25 is typical. The
// result depends on key order, which is the price of the bound.
func (r *Ring) AssignBounded(keys []string, c float64) (map[string]string, error) {
	// TODO(lab)
	_, _ = keys, c
	return nil, errNotImplemented
}
