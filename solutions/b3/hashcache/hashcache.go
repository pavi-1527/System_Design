// Package hashcache is a consistent-hashing cache client: a hash ring with
// virtual nodes (Stage 1), a replicating, health-aware client over per-node
// backends (Stage 2), and membership changes without a cache stampede
// (Stage 3, in stage3.go).
//
// Lab B3 Unit 8 §2 (problem 18, STRETCH). Stage 1 is this file.
//
// Reference design: the ring is a sorted slice of (hash, node) points; Get is
// a binary search plus wraparound. A node with weight w owns w*vnodes points,
// each at hash64("node#i"), so adding a node never moves another node's points
// and a key only ever moves to the node that was added.
package hashcache

import (
	"errors"
	"sort"
	"strconv"
	"sync"
)

// DefaultVnodes is the number of virtual nodes per unit of weight when
// NewRing is given a value below 1.
const DefaultVnodes = 150

// ErrInvalidWeight is returned by Ring.Add for a weight below 1.
var ErrInvalidWeight = errors.New("hashcache: weight must be >= 1")

// hash64 is FNV-1a followed by the murmur3 64-bit finaliser. It is
// deterministic across processes and runs (no per-process seed), which makes
// placement reproducible; the finaliser fixes FNV's weak avalanche on short,
// similar strings such as "node-1#17".
func hash64(s string) uint64 {
	h := uint64(14695981039346656037)
	for i := 0; i < len(s); i++ {
		h ^= uint64(s[i])
		h *= 1099511628211
	}
	h ^= h >> 33
	h *= 0xff51afd7ed558ccd
	h ^= h >> 33
	h *= 0xc4ceb9fe1a85ec53
	h ^= h >> 33
	return h
}

type point struct {
	hash uint64
	node string
}

// Ring is a consistent-hash ring. It is safe for concurrent use.
type Ring struct {
	mu      sync.RWMutex
	vnodes  int
	weights map[string]int
	points  []point // sorted by (hash, node)
}

// NewRing returns an empty ring in which a node of weight 1 owns vnodes
// points. vnodes < 1 means DefaultVnodes.
func NewRing(vnodes int) *Ring {
	if vnodes < 1 {
		vnodes = DefaultVnodes
	}
	return &Ring{vnodes: vnodes, weights: map[string]int{}}
}

// Add inserts node with the given weight (a node of weight w owns w times the
// virtual nodes of a weight-1 node and so receives about w times the keys).
// Adding an existing node changes its weight. Positions depend only on the
// node's name, never on which other nodes exist or the order of Add calls.
func (r *Ring) Add(node string, weight int) error {
	if weight < 1 {
		return ErrInvalidWeight
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.removeLocked(node)
	r.weights[node] = weight
	for i := 0; i < weight*r.vnodes; i++ {
		r.points = append(r.points, point{hash64(node + "#" + strconv.Itoa(i)), node})
	}
	sort.Slice(r.points, func(i, j int) bool {
		a, b := r.points[i], r.points[j]
		if a.hash != b.hash {
			return a.hash < b.hash
		}
		return a.node < b.node
	})
	return nil
}

// Remove deletes node; removing an unknown node is a no-op.
func (r *Ring) Remove(node string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.removeLocked(node)
}

func (r *Ring) removeLocked(node string) {
	if _, ok := r.weights[node]; !ok {
		return
	}
	delete(r.weights, node)
	kept := r.points[:0]
	for _, p := range r.points {
		if p.node != node {
			kept = append(kept, p)
		}
	}
	r.points = kept
}

// Nodes returns the member nodes, sorted.
func (r *Ring) Nodes() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.weights))
	for n := range r.weights {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// Get returns the node that owns key: the first virtual node clockwise from
// hash64(key), wrapping past the end. ok is false on an empty ring.
func (r *Ring) Get(key string) (node string, ok bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if len(r.points) == 0 {
		return "", false
	}
	return r.points[r.search(hash64(key))].node, true
}

// search returns the index of the first point with hash >= h, wrapping to 0.
// Caller holds r.mu and the ring is non-empty.
func (r *Ring) search(h uint64) int {
	i := sort.Search(len(r.points), func(i int) bool { return r.points[i].hash >= h })
	if i == len(r.points) {
		i = 0
	}
	return i
}

// Replicas returns up to n DISTINCT physical nodes in clockwise order from the
// key's position; the first is Get(key). It returns fewer than n only when the
// ring has fewer than n nodes, and nil for n < 1 or an empty ring.
func (r *Ring) Replicas(key string, n int) []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if n < 1 || len(r.points) == 0 {
		return nil
	}
	if n > len(r.weights) {
		n = len(r.weights)
	}
	out := make([]string, 0, n)
	seen := make(map[string]struct{}, n)
	start := r.search(hash64(key))
	for i := 0; i < len(r.points) && len(out) < n; i++ {
		p := r.points[(start+i)%len(r.points)]
		if _, dup := seen[p.node]; dup {
			continue
		}
		seen[p.node] = struct{}{}
		out = append(out, p.node)
	}
	return out
}
