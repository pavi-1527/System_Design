// Package ring is a consistent-hash ring with virtual nodes, N-replica
// successor lookup, weighted nodes, key-movement and load-balance statistics,
// a bounded-load assignment, and two alternatives (rendezvous and jump hash).
//
// It is the lab for A4 Unit 2 (§5–§7). The ring is the placement function
// behind Dynamo-style stores, Cassandra token rings and distributed caches:
// given a key and the current set of nodes, which node(s) own it, such that
// adding or removing one node moves only about 1/N of the keys.
//
// What the tests check:
//
//   - Lookup returns the successor: the first virtual node at or clockwise
//     after hash(key), wrapping past the largest position to the smallest.
//     Virtual node i of node X sits at Hash(VNodeLabel(X, i)). If two virtual
//     nodes land on the same position, the one whose node name sorts first
//     wins, so the ring does not depend on insertion order.
//   - Monotonicity: after Add(X), every key either keeps its old owner or
//     moves to X (never between two old nodes); after Remove(X), only the
//     keys X owned move. This is what a modulo scheme cannot do.
//   - Balance: with enough virtual nodes the busiest node carries close to
//     the mean load; with one virtual node per node it does not. Weight w
//     gives a node about w times the share.
//   - LookupN returns up to n DISTINCT physical nodes walking clockwise
//     (the replica set), first element equal to Lookup.
//   - Lookup is safe to call while another goroutine does Add/Remove (run
//     with -race), and never observes a half-updated ring.
//   - Distribution, MovedFraction and Summarize report what happened.
//   - AssignBounded caps every node at ceil((1+epsilon) * keys / nodes).
//   - Rendezvous and JumpHash have the same minimal-movement property.
//
// Suggested order: Add/Lookup/Remove, LookupN, the statistics helpers,
// AssignBounded, then Rendezvous and JumpHash.
// Run: go test -race ./labs/a4/ring/
package ring

import (
	"errors"
	"math"
	"sort"
	"strconv"
	"sync"
)

// Errors returned by Ring methods.
var (
	// ErrEmptyRing is returned by lookups on a ring with no nodes.
	ErrEmptyRing = errors.New("ring: no nodes")
	// ErrNodeExists is returned by Add for a name that is already present.
	ErrNodeExists = errors.New("ring: node already exists")
	// ErrUnknownNode is returned by Remove for a name that is not present.
	ErrUnknownNode = errors.New("ring: unknown node")
	// ErrBadArgument is returned for an empty node name, a weight < 1 or a
	// negative epsilon.
	ErrBadArgument = errors.New("ring: bad argument")
)

// HashFunc maps a string to a position on the 64-bit ring.
type HashFunc func(s string) uint64

// Hash64 is the default hash: FNV-1a (64-bit) followed by the splitmix64
// finalizer, so that similar inputs ("n1#0", "n1#1") spread over the whole
// ring. Deterministic and allocation-free.
//
// Provided.
func Hash64(s string) uint64 {
	h := uint64(14695981039346656037)
	for i := 0; i < len(s); i++ {
		h ^= uint64(s[i])
		h *= 1099511628211
	}
	h ^= h >> 30
	h *= 0xbf58476d1ce4e5b9
	h ^= h >> 27
	h *= 0x94d049bb133111eb
	h ^= h >> 31
	return h
}

// VNodeLabel is the string hashed to place virtual node i of node: the node
// name, a '#', and the decimal index ("db-3#17"). Tests with a hand-written
// hash function rely on this exact format.
//
// Provided.
func VNodeLabel(node string, i int) string { return node + "#" + strconv.Itoa(i) }

// ModOwner is the baseline the ring replaces: hash(key) mod len(nodes). It is
// provided so tests and break-it experiments can compare against it. Do not
// use it for placement that must survive a change in len(nodes).
//
// Provided.
func ModOwner(nodes []string, key string, hash HashFunc) string {
	if len(nodes) == 0 {
		return ""
	}
	if hash == nil {
		hash = Hash64
	}
	return nodes[hash(key)%uint64(len(nodes))]
}

// Config configures a Ring. Zero values get defaults.
type Config struct {
	// VNodes is the number of virtual nodes per unit of weight. Default 128.
	VNodes int
	// Hash positions both virtual nodes and keys. Default Hash64.
	Hash HashFunc
}

type point struct {
	pos  uint64
	node string
}

// Ring is a consistent-hash ring. All methods are safe for concurrent use.
type Ring struct {
	cfg Config

	mu      sync.RWMutex
	weights map[string]int
	points  []point // sorted by (pos, node)
}

// New returns an empty ring.
func New(cfg Config) *Ring {
	if cfg.VNodes <= 0 {
		cfg.VNodes = 128
	}
	if cfg.Hash == nil {
		cfg.Hash = Hash64
	}
	return &Ring{cfg: cfg, weights: make(map[string]int)}
}

func (r *Ring) sortPoints() {
	sort.Slice(r.points, func(i, j int) bool {
		a, b := r.points[i], r.points[j]
		if a.pos != b.pos {
			return a.pos < b.pos
		}
		return a.node < b.node
	})
}

// Add inserts node with the given weight (>= 1): it gets weight*VNodes
// virtual nodes. It returns ErrNodeExists if the name is present and
// ErrBadArgument for an empty name or weight < 1.
func (r *Ring) Add(node string, weight int) error {
	if node == "" || weight < 1 {
		return ErrBadArgument
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.weights[node]; ok {
		return ErrNodeExists
	}
	r.weights[node] = weight
	for i := 0; i < weight*r.cfg.VNodes; i++ {
		r.points = append(r.points, point{pos: r.cfg.Hash(VNodeLabel(node, i)), node: node})
	}
	r.sortPoints()
	return nil
}

// Remove deletes node and all its virtual nodes. It returns ErrUnknownNode
// if the name is not present.
func (r *Ring) Remove(node string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.weights[node]; !ok {
		return ErrUnknownNode
	}
	delete(r.weights, node)
	kept := r.points[:0:0]
	for _, p := range r.points {
		if p.node != node {
			kept = append(kept, p)
		}
	}
	r.points = kept
	return nil
}

// Nodes returns the node names in sorted order.
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

// Len returns the number of physical nodes.
func (r *Ring) Len() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.weights)
}

// Clone returns an independent copy of the ring (same config, nodes and
// weights). Use it to take a "before" snapshot for MovedFraction.
func (r *Ring) Clone() *Ring {
	r.mu.RLock()
	defer r.mu.RUnlock()
	c := &Ring{cfg: r.cfg, weights: make(map[string]int, len(r.weights))}
	for n, w := range r.weights {
		c.weights[n] = w
	}
	c.points = append([]point(nil), r.points...)
	return c
}

// successor returns the index of the first point with pos >= h, wrapping to
// 0. The caller holds a lock and has checked the ring is non-empty.
func (r *Ring) successor(h uint64) int {
	i := sort.Search(len(r.points), func(i int) bool { return r.points[i].pos >= h })
	if i == len(r.points) {
		return 0
	}
	return i
}

// Lookup returns the node that owns key: the node of the first virtual node
// at or clockwise after Hash(key), wrapping around. ErrEmptyRing if there are
// no nodes.
func (r *Ring) Lookup(key string) (string, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if len(r.points) == 0 {
		return "", ErrEmptyRing
	}
	return r.points[r.successor(r.cfg.Hash(key))].node, nil
}

// LookupN returns up to n distinct physical nodes for key, in clockwise
// order starting at Lookup(key): the replica set. If the ring has fewer than
// n nodes it returns all of them. n <= 0 returns an empty slice. ErrEmptyRing
// if there are no nodes.
func (r *Ring) LookupN(key string, n int) ([]string, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if len(r.points) == 0 {
		return nil, ErrEmptyRing
	}
	if n > len(r.weights) {
		n = len(r.weights)
	}
	out := make([]string, 0, max(n, 0))
	if n <= 0 {
		return out, nil
	}
	seen := make(map[string]bool, n)
	start := r.successor(r.cfg.Hash(key))
	for k := 0; k < len(r.points) && len(out) < n; k++ {
		p := r.points[(start+k)%len(r.points)]
		if !seen[p.node] {
			seen[p.node] = true
			out = append(out, p.node)
		}
	}
	return out, nil
}

// Distribution counts how many of keys each node owns. Every node of r
// appears in the result, with 0 if it owns none.
func Distribution(r *Ring, keys []string) map[string]int {
	counts := make(map[string]int)
	for _, n := range r.Nodes() {
		counts[n] = 0
	}
	for _, k := range keys {
		if n, err := r.Lookup(k); err == nil {
			counts[n]++
		}
	}
	return counts
}

// MovedFraction returns the fraction of keys whose owner differs between the
// two rings (a key that has no owner in one of them counts as moved). It
// returns 0 for an empty key list.
func MovedFraction(before, after *Ring, keys []string) float64 {
	if len(keys) == 0 {
		return 0
	}
	moved := 0
	for _, k := range keys {
		a, errA := before.Lookup(k)
		b, errB := after.Lookup(k)
		if errA != nil || errB != nil || a != b {
			moved++
		}
	}
	return float64(moved) / float64(len(keys))
}

// Stats summarises a per-node load distribution.
type Stats struct {
	Nodes       int     // number of entries in the map
	Total       int     // sum of loads
	Min, Max    int     // least and most loaded node
	Mean        float64 // Total / Nodes
	StdDev      float64 // population standard deviation
	MaxOverMean float64 // Max / Mean: 1.0 is perfect balance (0 if Mean is 0)
}

// Summarize computes Stats for a node→load map (such as Distribution's
// result). An empty map gives the zero Stats.
func Summarize(counts map[string]int) Stats {
	var s Stats
	if len(counts) == 0 {
		return s
	}
	first := true
	for _, c := range counts {
		s.Nodes++
		s.Total += c
		if first || c < s.Min {
			s.Min = c
		}
		if first || c > s.Max {
			s.Max = c
		}
		first = false
	}
	s.Mean = float64(s.Total) / float64(s.Nodes)
	var ss float64
	for _, c := range counts {
		d := float64(c) - s.Mean
		ss += d * d
	}
	s.StdDev = math.Sqrt(ss / float64(s.Nodes))
	if s.Mean > 0 {
		s.MaxOverMean = float64(s.Max) / s.Mean
	}
	return s
}

// AssignBounded assigns each key to a node with the "consistent hashing with
// bounded loads" rule: no node may hold more than
// ceil((1+epsilon) * len(keys) / nodes) keys. Keys are processed in the given
// order; each goes to the first node on its clockwise walk (LookupN order)
// that still has room. Keys must be unique (a repeated key keeps its last
// assignment). epsilon < 0 returns ErrBadArgument; an empty ring ErrEmptyRing.
func AssignBounded(r *Ring, keys []string, epsilon float64) (map[string]string, error) {
	if epsilon < 0 {
		return nil, ErrBadArgument
	}
	n := r.Len()
	if n == 0 {
		return nil, ErrEmptyRing
	}
	capacity := int(math.Ceil((1 + epsilon) * float64(len(keys)) / float64(n)))
	load := make(map[string]int, n)
	out := make(map[string]string, len(keys))
	for _, k := range keys {
		walk, err := r.LookupN(k, n)
		if err != nil {
			return nil, err
		}
		for _, cand := range walk {
			if load[cand] < capacity {
				load[cand]++
				out[k] = cand
				break
			}
		}
	}
	return out, nil
}

// Rendezvous (highest-random-weight) hashing: the owner of key is the node
// with the highest hash(node + "|" + key); ties go to the lexicographically
// smaller node name. Returns "" for no nodes. A nil hash means Hash64.
func Rendezvous(nodes []string, key string, hash HashFunc) string {
	if hash == nil {
		hash = Hash64
	}
	best, bestScore := "", uint64(0)
	for i, n := range nodes {
		s := hash(n + "|" + key)
		if i == 0 || s > bestScore || (s == bestScore && n < best) {
			best, bestScore = n, s
		}
	}
	return best
}

// JumpHash is the Lamping–Veach jump consistent hash: it maps a 64-bit key
// to a bucket in [0, buckets) such that growing buckets from n to n+1 moves
// only the keys that land in the new bucket n. Returns -1 if buckets <= 0.
func JumpHash(key uint64, buckets int) int {
	if buckets <= 0 {
		return -1
	}
	var b, j int64 = -1, 0
	for j < int64(buckets) {
		b = j
		key = key*2862933555777941757 + 1
		j = int64(float64(b+1) * (float64(int64(1)<<31) / float64((key>>33)+1)))
	}
	return int(b)
}
