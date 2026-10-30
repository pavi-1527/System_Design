// Package antientropy is the background repair process of a Dynamo-style
// key-value store, which is the third deep dive of case study C4: two replicas
// of the same key range have drifted apart (a node was down, a hint was lost,
// a write reached two replicas out of three) and nobody is going to read the
// affected keys soon, so read repair will never fix them. The replicas must
// find the handful of keys that differ among millions without shipping
// millions of keys, and they must do it without bringing deleted data back.
//
// It is the lab for C4 Unit 2 §7. Four pieces, in the order the tests expect
// you to build them:
//
//  1. Entries and replicas: a versioned Entry (a live value or a tombstone)
//     with a deterministic winner rule (Wins), and an in-memory Replica that
//     merges entries with that rule (Apply, Put, Delete, Get, Lookup).
//  2. A Merkle tree over the hash space: Bucket maps a key to one of 2^depth
//     leaves, BuildTree hashes every leaf's entries and every pair of
//     children up to a single root.
//  3. Diff and Repair: compare two trees from the root down, descending only
//     into subtrees whose hashes differ, then exchange the entries of the
//     differing leaves in both directions and merge them.
//  4. Tombstone garbage collection: Compact drops tombstones older than a
//     cutoff, and SafeCutoff picks a cutoff that cannot resurrect a value on
//     a replica that missed the delete.
//
// Provided (identical in the reference solution): the errors, MaxDepth, Hash,
// Entry, the Replica and Tree structs, NewReplica, Name, Token and Stats.
// You implement everything else, in the order above; hashLeaf and hashPair
// are suggested helpers for BuildTree.
//
// Run: go test -race ./labs/c4/antientropy/
package antientropy

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"sort"
	"sync"
)

// ErrBadArgument is returned for a depth outside [0, MaxDepth] and for trees
// of different depths passed to Diff. Callers use errors.Is.
var ErrBadArgument = errors.New("antientropy: bad argument")

// MaxDepth is the deepest tree BuildTree accepts: 2^20 leaves, ≈ 2M hashes,
// ≈ 64 MB of tree. Real systems pick the depth per key range (see the unit).
const MaxDepth = 20

// Hash is a SHA-256 digest. The zero Hash stands for "nothing here": an empty
// leaf, and an internal node whose two children are both zero.
type Hash [32]byte

// IsZero reports whether h is the zero Hash.
func (h Hash) IsZero() bool { return h == Hash{} }

// String returns the first 8 hex digits, enough to tell hashes apart in a
// failure message.
func (h Hash) String() string { return fmt.Sprintf("%x", h[:4]) }

// Entry is one key's state on one replica: a live value, or a tombstone that
// records a delete. Stamp is the write's version (a hybrid logical clock or a
// microsecond timestamp, assigned by the coordinator): a higher Stamp wins.
type Entry struct {
	Key     string
	Value   []byte // nil for a tombstone
	Stamp   int64
	Deleted bool // a tombstone
}

// Wins reports whether e should replace o (an entry for the same key). The
// rule must give every replica the same answer for the same pair, whatever
// the order they see them in, or replicas never converge:
//
//   - the higher Stamp wins;
//   - on equal Stamps a tombstone beats a live value (a delete issued in the
//     same tick as a write is not undone by it);
//   - on equal Stamps between two live values, or two tombstones, the
//     bytewise-larger Value wins (bytes.Compare(e.Value, o.Value) > 0); equal
//     entries do not win (re-applying an entry is a no-op).
func (e Entry) Wins(o Entry) bool {
	if e.Stamp != o.Stamp {
		return e.Stamp > o.Stamp
	}
	if e.Deleted != o.Deleted {
		return e.Deleted
	}
	return bytes.Compare(e.Value, o.Value) > 0
}

// clone returns a deep copy of e (its own Value slice), so that a replica
// never shares memory with a caller or with another replica.
func (e Entry) clone() Entry {
	if e.Value != nil {
		e.Value = append([]byte(nil), e.Value...)
	}
	return e
}

// Replica is one node's copy of a key range, safe for concurrent use. It
// stores tombstones like any other entry until Compact removes them.
type Replica struct {
	name string
	mu   sync.RWMutex
	data map[string]Entry
}

// NewReplica returns an empty replica.
func NewReplica(name string) *Replica {
	return &Replica{name: name, data: make(map[string]Entry)}
}

// Name returns the name the replica was created with.
func (r *Replica) Name() string { return r.name }

// Apply merges e into the replica: it stores (a copy of) e if the replica has
// no entry for e.Key or if e.Wins over the stored entry, and reports whether
// it changed anything. A tombstone's Value is stored as nil.
func (r *Replica) Apply(e Entry) bool {
	e = e.clone()
	if e.Deleted {
		e.Value = nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if old, ok := r.data[e.Key]; ok && !e.Wins(old) {
		return false
	}
	r.data[e.Key] = e
	return true
}

// Put applies a live value written at stamp. The replica keeps its own copy
// of value: the caller may reuse the slice.
func (r *Replica) Put(key string, value []byte, stamp int64) bool {
	return r.Apply(Entry{Key: key, Value: value, Stamp: stamp})
}

// Delete applies a tombstone written at stamp.
func (r *Replica) Delete(key string, stamp int64) bool {
	return r.Apply(Entry{Key: key, Stamp: stamp, Deleted: true})
}

// Get returns a copy of the live value of key, or (nil, false) if the key is
// absent or deleted.
func (r *Replica) Get(key string) ([]byte, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	e, ok := r.data[key]
	if !ok || e.Deleted {
		return nil, false
	}
	return append([]byte(nil), e.Value...), true
}

// Lookup returns a copy of the stored entry for key, tombstones included.
func (r *Replica) Lookup(key string) (Entry, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	e, ok := r.data[key]
	return e.clone(), ok
}

// Len returns the number of stored entries, tombstones included.
func (r *Replica) Len() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.data)
}

// Compact removes every tombstone whose Stamp is strictly less than cutoff
// and returns how many it removed. Live values are never removed.
func (r *Replica) Compact(cutoff int64) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for k, e := range r.data {
		if e.Deleted && e.Stamp < cutoff {
			delete(r.data, k)
			n++
		}
	}
	return n
}

// BucketEntries returns copies of the entries (tombstones included) whose key
// falls in the given leaf at the given depth, sorted by key. It returns nil
// for a depth outside [0, MaxDepth] or a bucket outside [0, 2^depth).
func (r *Replica) BucketEntries(bucket, depth int) []Entry {
	if depth < 0 || depth > MaxDepth || bucket < 0 || bucket >= 1<<depth {
		return nil
	}
	r.mu.RLock()
	var out []Entry
	for k, e := range r.data {
		if Bucket(k, depth) == bucket {
			out = append(out, e.clone())
		}
	}
	r.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

// snapshot returns copies of every entry grouped by leaf at depth, each group
// sorted by key, taken under one read lock.
func (r *Replica) snapshot(depth int) [][]Entry {
	leaves := make([][]Entry, 1<<depth)
	r.mu.RLock()
	for k, e := range r.data {
		b := Bucket(k, depth)
		leaves[b] = append(leaves[b], e.clone())
	}
	r.mu.RUnlock()
	for _, l := range leaves {
		sort.Slice(l, func(i, j int) bool { return l[i].Key < l[j].Key })
	}
	return leaves
}

// Token is the key's position in the 64-bit hash space: FNV-1a (64-bit)
// followed by the splitmix64 finalizer, the same function as A4's ring
// (labs/a4/ring.Hash64), so that similar keys ("member:000001",
// "member:000002") spread over the whole space. Plain FNV-1a would not do:
// its top bits barely depend on the last bytes of the key.
//
// Provided.
func Token(key string) uint64 {
	h := uint64(14695981039346656037)
	for i := 0; i < len(key); i++ {
		h ^= uint64(key[i])
		h *= 1099511628211
	}
	h ^= h >> 30
	h *= 0xbf58476d1ce4e5b9
	h ^= h >> 27
	h *= 0x94d049bb133111eb
	h ^= h >> 31
	return h
}

// Bucket returns the leaf that key falls in for a tree of the given depth:
// the top depth bits of Token(key) (0 when depth is 0), so leaf i covers a
// contiguous slice of the hash space, the way a real tree covers a token
// range. It returns -1 for a depth outside [0, MaxDepth].
func Bucket(key string, depth int) int {
	if depth < 0 || depth > MaxDepth {
		return -1
	}
	if depth == 0 {
		return 0
	}
	return int(Token(key) >> (64 - uint(depth)))
}

// hashLeaf hashes a leaf's entries (sorted by key). Every field of every entry
// is in the hash, each variable-length field behind its length, so that a
// different stamp, a tombstone instead of a value, or ("ab","c") instead of
// ("a","bc") all give a different hash. An empty leaf is the zero Hash.
func hashLeaf(entries []Entry) Hash {
	if len(entries) == 0 {
		return Hash{}
	}
	h := sha256.New()
	var buf [binary.MaxVarintLen64]byte
	writeBytes := func(b []byte) {
		n := binary.PutUvarint(buf[:], uint64(len(b)))
		h.Write(buf[:n])
		h.Write(b)
	}
	for _, e := range entries {
		writeBytes([]byte(e.Key))
		var st [8]byte
		binary.BigEndian.PutUint64(st[:], uint64(e.Stamp))
		h.Write(st[:])
		if e.Deleted {
			h.Write([]byte{1})
		} else {
			h.Write([]byte{0})
		}
		writeBytes(e.Value)
	}
	var out Hash
	copy(out[:], h.Sum(nil))
	return out
}

// hashPair is an internal node: zero if both children are zero, otherwise
// SHA-256(left || right).
func hashPair(l, r Hash) Hash {
	if l.IsZero() && r.IsZero() {
		return Hash{}
	}
	var b [64]byte
	copy(b[:32], l[:])
	copy(b[32:], r[:])
	return sha256.Sum256(b[:])
}

// Tree is a Merkle tree over a replica's entries at one moment. Level 0 is
// the root; level Depth holds the 2^Depth leaves. Node i at level l has
// children 2i and 2i+1 at level l+1.
type Tree struct {
	depth  int
	levels [][]Hash
}

// BuildTree takes a consistent snapshot of r and hashes it into a tree of the
// given depth. It returns ErrBadArgument (wrapped) for a depth outside
// [0, MaxDepth]. An empty replica has a zero root.
func BuildTree(r *Replica, depth int) (*Tree, error) {
	if depth < 0 || depth > MaxDepth {
		return nil, fmt.Errorf("%w: depth %d not in [0, %d]", ErrBadArgument, depth, MaxDepth)
	}
	leaves := r.snapshot(depth)
	t := &Tree{depth: depth, levels: make([][]Hash, depth+1)}
	t.levels[depth] = make([]Hash, len(leaves))
	for i, l := range leaves {
		t.levels[depth][i] = hashLeaf(l)
	}
	for l := depth - 1; l >= 0; l-- {
		below := t.levels[l+1]
		t.levels[l] = make([]Hash, len(below)/2)
		for i := range t.levels[l] {
			t.levels[l][i] = hashPair(below[2*i], below[2*i+1])
		}
	}
	return t, nil
}

// Depth returns the tree's depth.
func (t *Tree) Depth() int { return t.depth }

// Leaves returns the number of leaves, 2^Depth.
func (t *Tree) Leaves() int { return 1 << t.depth }

// Root returns the root hash.
func (t *Tree) Root() Hash { return t.levels[0][0] }

// Node returns the hash of node index at level (0 = root). Out-of-range
// arguments return the zero Hash.
func (t *Tree) Node(level, index int) Hash {
	if level < 0 || level > t.depth || index < 0 || index >= len(t.levels[level]) {
		return Hash{}
	}
	return t.levels[level][index]
}

// Diff compares two trees of the same depth the way two replicas would over
// the network: compare the roots (one comparison); for every node that
// differs above the leaves, compare both of its children (two comparisons);
// never descend into a subtree whose hashes match. It returns the indices of
// the differing leaves in ascending order and the number of hash comparisons
// made. Identical trees cost exactly one comparison. Trees of different depths
// are ErrBadArgument (wrapped).
func Diff(a, b *Tree) ([]int, int, error) {
	if a == nil || b == nil || a.depth != b.depth {
		return nil, 0, fmt.Errorf("%w: trees must exist and have the same depth", ErrBadArgument)
	}
	comparisons := 1
	if a.Root() == b.Root() {
		return nil, comparisons, nil
	}
	differing := []int{0} // node indices at the current level that differ
	for level := 0; level < a.depth; level++ {
		var next []int
		for _, i := range differing {
			for _, c := range [2]int{2 * i, 2*i + 1} {
				comparisons++
				if a.levels[level+1][c] != b.levels[level+1][c] {
					next = append(next, c)
				}
			}
		}
		differing = next
	}
	return differing, comparisons, nil
}

// Stats describes one Repair.
type Stats struct {
	Comparisons int // hash comparisons made by Diff
	Buckets     int // differing leaves
	Sent        int // entries shipped, both directions: every entry of every differing leaf on each side
	Applied     int // entries that changed the replica they were shipped to
}

// Repair makes a and b agree on every key: it builds both trees at the given
// depth, diffs them, and for each differing leaf ships every entry of that
// leaf from a to b and from b to a, merging with Apply (Entry.Wins decides).
// Leaves whose hashes match are not touched. It checks ctx before each leaf
// and returns ctx.Err() (with the Stats so far) if it is done; work already
// applied stays applied, which is safe because Apply is idempotent.
//
// Repair never holds one replica's lock while calling into the other, so two
// Repairs running concurrently on overlapping pairs cannot deadlock. Writes
// that land during a Repair may or may not be included; a later Repair picks
// them up.
func Repair(ctx context.Context, a, b *Replica, depth int) (Stats, error) {
	var st Stats
	if err := ctx.Err(); err != nil {
		return st, err
	}
	ta, err := BuildTree(a, depth)
	if err != nil {
		return st, err
	}
	tb, err := BuildTree(b, depth)
	if err != nil {
		return st, err
	}
	buckets, cmp, err := Diff(ta, tb)
	if err != nil {
		return st, err
	}
	st.Comparisons, st.Buckets = cmp, len(buckets)
	for _, bk := range buckets {
		if err := ctx.Err(); err != nil {
			return st, err
		}
		fromA := a.BucketEntries(bk, depth)
		fromB := b.BucketEntries(bk, depth)
		st.Sent += len(fromA) + len(fromB)
		for _, e := range fromA {
			if b.Apply(e) {
				st.Applied++
			}
		}
		for _, e := range fromB {
			if a.Apply(e) {
				st.Applied++
			}
		}
	}
	return st, nil
}

// SafeCutoff returns the tombstone cutoff for Compact that cannot resurrect
// deleted data: the older of now-gcGrace (the usual grace period) and
// lastFullRepair (the start of the most recent repair that covered every
// replica of the range). A tombstone newer than the last full repair may not
// have reached a replica that was down, so it must stay until a repair has
// carried it everywhere, however old it is.
func SafeCutoff(now, gcGrace, lastFullRepair int64) int64 {
	return min(now-gcGrace, lastFullRepair)
}
