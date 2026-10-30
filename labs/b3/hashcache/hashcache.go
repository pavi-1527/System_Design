// Package hashcache is a consistent-hashing cache client: a hash ring with
// virtual nodes (Stage 1), a replicating, health-aware client over per-node
// backends (Stage 2, stage2.go), and membership changes without a cache
// stampede (Stage 3, stage3.go).
//
// Lab B3 Unit 8 §2 (problem 18, STRETCH), time box 75 minutes. Work in stages
// and open the next stage's file only when you get there, like an interviewer
// who adds requirements as you go:
//
//	Stage 1 (≈ 30 min)  this file:  Ring: Add, Remove, Nodes, Get, Replicas
//	Stage 2 (≈ 25 min)  stage2.go:  Client over Backends: replication, quorum, health
//	Stage 3 (≈ 20 min)  stage3.go:  SetMembership with lazy migration, AssignBounded
//
// Run one stage at a time:
//
//	go test -race -run 'Stage1' ./labs/b3/hashcache/
//
// Two things to decide before you type:
//
//   - The hash. It must be deterministic across runs (no per-process random
//     seed) and well mixed. hash/fnv alone is NOT well mixed for short,
//     similar strings like "node-1#17" and "node-1#18"; the balance tests will
//     tell you if your ring is lumpy.
//   - What a request sees. Membership changes in Stage 3, while requests are
//     in flight; think now about how a request gets a ring and backends that
//     belong together.
package hashcache

import "errors"

// DefaultVnodes is the number of virtual nodes per unit of weight when
// NewRing is given a value below 1.
const DefaultVnodes = 150

// ErrInvalidWeight is returned by Ring.Add for a weight below 1.
var ErrInvalidWeight = errors.New("hashcache: weight must be >= 1")

var errNotImplemented = errors.New("hashcache: not implemented")

// Ring is a consistent-hash ring. It is safe for concurrent use.
type Ring struct {
	// TODO(lab): a lock, the vnode count, node weights, and the sorted
	// virtual-node points (hash, node).
	vnodes int
}

// NewRing returns an empty ring in which a node of weight 1 owns vnodes
// points. vnodes < 1 means DefaultVnodes.
func NewRing(vnodes int) *Ring {
	// TODO(lab)
	return &Ring{vnodes: vnodes}
}

// Add inserts node with the given weight (a node of weight w owns w times the
// virtual nodes of a weight-1 node and so receives about w times the keys).
// Adding an existing node changes its weight. A weight below 1 is
// ErrInvalidWeight and changes nothing.
//
// Positions must depend only on the node's name and the virtual-node index,
// never on which other nodes exist or the order of Add calls: that is what
// makes "adding a node moves keys only to the new node" true. Break ties
// between equal hashes deterministically.
func (r *Ring) Add(node string, weight int) error {
	// TODO(lab)
	_, _ = node, weight
	return errNotImplemented
}

// Remove deletes node and all its virtual nodes; removing an unknown node is
// a no-op.
func (r *Ring) Remove(node string) {
	// TODO(lab)
	_ = node
}

// Nodes returns the member nodes, sorted.
func (r *Ring) Nodes() []string {
	// TODO(lab)
	return nil
}

// Get returns the node that owns key: the first virtual node clockwise from
// the key's hash (the smallest point >= hash, wrapping past the end back to
// the first). ok is false on an empty ring.
func (r *Ring) Get(key string) (node string, ok bool) {
	// TODO(lab): binary search (sort.Search) over the sorted points.
	_ = key
	return "", false
}

// Replicas returns up to n DISTINCT physical nodes in clockwise order from
// the key's position; the first is Get(key). It returns fewer than n only when
// the ring has fewer than n nodes, and nil for n < 1 or an empty ring.
func (r *Ring) Replicas(key string, n int) []string {
	// TODO(lab): walk clockwise, skipping virtual nodes of nodes already chosen.
	_, _ = key, n
	return nil
}
