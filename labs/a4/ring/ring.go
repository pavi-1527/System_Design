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
	"strconv"
	"sync"
)

var errNotImplemented = errors.New("ring: not implemented")

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

// Ring is a consistent-hash ring. All methods are safe for concurrent use.
type Ring struct {
	cfg Config

	mu sync.RWMutex
	// TODO(lab): the node set (name → weight) and the ring itself: a slice of
	// (position, node) points kept sorted by position, ties broken by node
	// name so the ring never depends on insertion order.
}

// New returns an empty ring. Provided: applies the Config defaults.
func New(cfg Config) *Ring {
	if cfg.VNodes <= 0 {
		cfg.VNodes = 128
	}
	if cfg.Hash == nil {
		cfg.Hash = Hash64
	}
	return &Ring{cfg: cfg}
}

// Add inserts node with the given weight (>= 1): it gets weight*VNodes
// virtual nodes, virtual node i at cfg.Hash(VNodeLabel(node, i)). It returns
// ErrNodeExists if the name is present and ErrBadArgument for an empty name
// or weight < 1.
func (r *Ring) Add(node string, weight int) error {
	// TODO(lab): validate, then insert the virtual nodes and keep the points
	// sorted by (position, node name). Hold the write lock.
	return errNotImplemented
}

// Remove deletes node and all its virtual nodes. It returns ErrUnknownNode
// if the name is not present.
func (r *Ring) Remove(node string) error {
	// TODO(lab): drop the node's points (all of them) and its weight entry.
	return errNotImplemented
}

// Nodes returns the node names in sorted order.
func (r *Ring) Nodes() []string {
	// TODO(lab)
	return nil
}

// Len returns the number of physical nodes.
func (r *Ring) Len() int {
	// TODO(lab)
	return 0
}

// Clone returns an independent copy of the ring (same config, nodes and
// weights). Use it to take a "before" snapshot for MovedFraction.
func (r *Ring) Clone() *Ring {
	// TODO(lab): copy the node set and the points; share nothing mutable.
	return New(r.cfg)
}

// Lookup returns the node that owns key: the node of the first virtual node
// at or clockwise after Hash(key) (position >= hash), wrapping around to the
// first point if there is none. ErrEmptyRing if there are no nodes.
func (r *Ring) Lookup(key string) (string, error) {
	// TODO(lab): binary search (sort.Search) over the sorted points, under a
	// read lock.
	return "", errNotImplemented
}

// LookupN returns up to n distinct physical nodes for key, in clockwise
// order starting at Lookup(key): the replica set. Walk the points from the
// successor and skip virtual nodes of nodes already chosen. If the ring has
// fewer than n nodes it returns all of them. n <= 0 returns an empty slice.
// ErrEmptyRing if there are no nodes.
func (r *Ring) LookupN(key string, n int) ([]string, error) {
	// TODO(lab)
	return nil, errNotImplemented
}

// Distribution counts how many of keys each node owns. Every node of r
// appears in the result, with 0 if it owns none.
func Distribution(r *Ring, keys []string) map[string]int {
	// TODO(lab)
	return nil
}

// MovedFraction returns the fraction of keys whose owner differs between the
// two rings (a key that has no owner in one of them counts as moved). It
// returns 0 for an empty key list.
func MovedFraction(before, after *Ring, keys []string) float64 {
	// TODO(lab)
	return 0
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
	// TODO(lab)
	return Stats{}
}

// AssignBounded assigns each key to a node with the "consistent hashing with
// bounded loads" rule: no node may hold more than
// ceil((1+epsilon) * len(keys) / nodes) keys. Keys are processed in the given
// order; each goes to the first node on its clockwise walk (LookupN order)
// that still has room. Keys must be unique (a repeated key keeps its last
// assignment). epsilon < 0 returns ErrBadArgument; an empty ring ErrEmptyRing.
func AssignBounded(r *Ring, keys []string, epsilon float64) (map[string]string, error) {
	// TODO(lab): compute the cap (round UP), then walk LookupN(k, nodes).
	return nil, errNotImplemented
}

// Rendezvous (highest-random-weight) hashing: the owner of key is the node
// with the highest hash(node + "|" + key); ties go to the lexicographically
// smaller node name. Returns "" for no nodes. A nil hash means Hash64.
func Rendezvous(nodes []string, key string, hash HashFunc) string {
	// TODO(lab)
	return ""
}

// JumpHash is the Lamping–Veach jump consistent hash: it maps a 64-bit key
// to a bucket in [0, buckets) such that growing buckets from n to n+1 moves
// only the keys that land in the new bucket n. Returns -1 if buckets <= 0.
//
// The algorithm (from the paper, 2014): start with b = -1, j = 0; while
// j < buckets { b = j; key = key*2862933555777941757 + 1;
// j = int((b+1) * (2^31 / ((key>>33) + 1))) }; return b.
func JumpHash(key uint64, buckets int) int {
	// TODO(lab)
	return -1
}
