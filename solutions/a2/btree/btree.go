// Package btree is an in-memory B+tree that models how a database index lays
// out its pages: sorted keys in fixed-capacity nodes, values only in the
// leaves, leaves chained left to right for range scans, and nodes that split
// in two when they overflow. It is the lab for A2 Unit 1 §2.
//
// What the tests check (they depend on every line of this list):
//
//   - Keys are non-empty strings ordered bytewise. A node holds at most
//     MaxKeys keys. Put on an existing key replaces the value and does not
//     change Len.
//   - Leaf split: when a leaf reaches MaxKeys+1 keys, the first (n/2) keys
//     stay in the left node, the rest move to a new right node, and the right
//     node's first key is copied up into the parent as the separator.
//   - Internal split: when an internal node reaches MaxKeys+1 keys, the key
//     at index (n/2) moves up (it is not kept in either half); keys and
//     children after it go to the new right node.
//   - Root split: the tree grows one level. Height of a tree that is one leaf
//     is 1. Every leaf is at the same depth.
//   - Separators: everything under child i is < keys[i], everything under
//     child i+1 is >= keys[i].
//   - Get descends one node per level and Scan descends once, then follows
//     the leaf chain (no re-descending). Both count node visits through
//     touch, which the tests read with Reads.
//   - Delete removes the key from its leaf and nothing else: no merging and
//     no borrowing, so leaves may become under-full or empty. Many production
//     engines make the same choice because merging is expensive and
//     contended.
//
// The tree is safe for concurrent use: Put/Delete take the write lock, Get,
// Scan and the statistics take the read lock. Real engines latch pages
// individually (latch coupling); one RWMutex keeps the lab about the
// structure.
//
// Suggested order: Get, Put without splits, leaf splits, root growth,
// internal splits, Scan, Delete. Run: go test -race ./labs/a2/btree/
package btree

import (
	"errors"
	"fmt"
	"sort"
	"sync"
	"sync/atomic"
)

// MinMaxKeys is the smallest node capacity New accepts.
const MinMaxKeys = 3

var (
	// ErrEmptyKey is returned by Put for the empty key (it is reserved to
	// mean "unbounded" in Scan).
	ErrEmptyKey = errors.New("btree: empty key")
	// ErrBadNodeSize is returned by New when maxKeys < MinMaxKeys.
	ErrBadNodeSize = errors.New("btree: maxKeys must be at least 3")
)

// Entry is one key/value pair returned by Scan.
type Entry struct {
	Key   string
	Value string
}

// Stats is a snapshot of the tree's shape.
type Stats struct {
	Height    int     // levels; a single leaf has height 1
	Leaves    int     // leaf nodes
	Internals int     // internal nodes, including the root if it is one
	Keys      int     // entries stored
	Splits    int     // node splits since New (leaf and internal)
	LeafFill  float64 // mean leaf occupancy: Keys / (Leaves * MaxKeys)
}

// node is one page. Leaves have keys/vals/next; internal nodes have
// keys/kids with len(kids) == len(keys)+1. Provided.
type node struct {
	id   int
	leaf bool
	keys []string
	vals []string // leaf only, parallel to keys
	kids []*node  // internal only
	next *node    // leaf only: right sibling
}

// Tree is a B+tree.
type Tree struct {
	mu      sync.RWMutex
	maxKeys int
	root    *node
	size    int
	splits  int
	nextID  int

	reads   atomic.Int64
	onTouch func(id int)
}

// New returns an empty tree whose nodes hold at most maxKeys keys. Provided.
func New(maxKeys int) (*Tree, error) {
	if maxKeys < MinMaxKeys {
		return nil, ErrBadNodeSize
	}
	t := &Tree{maxKeys: maxKeys}
	t.root = t.newNode(true)
	return t, nil
}

// newNode allocates a node with a fresh page id. Callers hold the write lock
// (or are in New). Provided.
func (t *Tree) newNode(leaf bool) *node {
	t.nextID++
	return &node{id: t.nextID, leaf: leaf}
}

// touch records that a node (page) was visited. Call it exactly once for each
// node a traversal visits, when it arrives there. Provided.
func (t *Tree) touch(n *node) {
	t.reads.Add(1)
	if t.onTouch != nil {
		t.onTouch(n.id)
	}
}

// SetTouchHook installs fn to be called with the page id of every node visit,
// e.g. to simulate a buffer pool. Not safe to call while the tree is in use.
// Provided.
func (t *Tree) SetTouchHook(fn func(pageID int)) { t.onTouch = fn }

// Reads returns the cumulative number of node visits (see touch). Provided.
func (t *Tree) Reads() int64 { return t.reads.Load() }

// MaxKeys returns the node capacity. Provided.
func (t *Tree) MaxKeys() int { return t.maxKeys }

// Len returns the number of entries. Provided.
func (t *Tree) Len() int {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.size
}

// Put inserts or replaces key. It reports whether an existing entry was
// replaced. Provided wrapper; the work is in put.
func (t *Tree) Put(key, value string) (replaced bool, err error) {
	if key == "" {
		return false, ErrEmptyKey
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.put(key, value), nil
}

// Get returns the value for key. Provided wrapper; the work is in get.
func (t *Tree) Get(key string) (string, bool) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.get(key)
}

// Delete removes key and reports whether it was present. Provided wrapper;
// the work is in del.
func (t *Tree) Delete(key string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.del(key)
}

// Scan returns, in ascending key order, the entries with lo <= key < hi.
// An empty lo means "from the first key"; an empty hi means "to the last
// key". If limit > 0 at most limit entries are returned. The result is never
// nil. Provided wrapper; the work is in scan.
func (t *Tree) Scan(lo, hi string, limit int) []Entry {
	t.mu.RLock()
	defer t.mu.RUnlock()
	out := t.scan(lo, hi, limit)
	if out == nil {
		out = []Entry{}
	}
	return out
}

// childIndex returns which child of internal node n a search for key
// descends into: the number of separators <= key. Provided.
func childIndex(n *node, key string) int {
	return sort.Search(len(n.keys), func(i int) bool { return n.keys[i] > key })
}

func insertAt[T any](s []T, i int, v T) []T {
	var zero T
	s = append(s, zero)
	copy(s[i+1:], s[i:])
	s[i] = v
	return s
}

// get finds key. Visit exactly one node per level, calling touch on each.
func (t *Tree) get(key string) (string, bool) {
	n := t.root
	for {
		t.touch(n)
		if n.leaf {
			i := sort.SearchStrings(n.keys, key)
			if i < len(n.keys) && n.keys[i] == key {
				return n.vals[i], true
			}
			return "", false
		}
		n = n.kids[childIndex(n, key)]
	}
}

// put inserts or replaces key and reports whether it replaced an existing
// entry. It must keep t.size and t.splits up to date and grow the tree when
// the root splits.
func (t *Tree) put(key, value string) bool {
	replaced, sep, right := t.insert(t.root, key, value)
	if right != nil {
		root := t.newNode(false)
		root.keys = []string{sep}
		root.kids = []*node{t.root, right}
		t.root = root
	}
	return replaced
}

// insert adds key under n. If n overflowed it splits and returns the
// separator to insert in the parent and the new right sibling.
func (t *Tree) insert(n *node, key, value string) (replaced bool, sep string, right *node) {
	t.touch(n)
	if n.leaf {
		i := sort.SearchStrings(n.keys, key)
		if i < len(n.keys) && n.keys[i] == key {
			n.vals[i] = value
			return true, "", nil
		}
		n.keys = insertAt(n.keys, i, key)
		n.vals = insertAt(n.vals, i, value)
		t.size++
		if len(n.keys) <= t.maxKeys {
			return false, "", nil
		}
		mid := len(n.keys) / 2
		r := t.newNode(true)
		r.keys = append([]string(nil), n.keys[mid:]...)
		r.vals = append([]string(nil), n.vals[mid:]...)
		n.keys, n.vals = n.keys[:mid], n.vals[:mid]
		r.next, n.next = n.next, r
		t.splits++
		return false, r.keys[0], r
	}

	ci := childIndex(n, key)
	replaced, csep, cright := t.insert(n.kids[ci], key, value)
	if cright == nil {
		return replaced, "", nil
	}
	n.keys = insertAt(n.keys, ci, csep)
	n.kids = insertAt(n.kids, ci+1, cright)
	if len(n.keys) <= t.maxKeys {
		return replaced, "", nil
	}
	mid := len(n.keys) / 2
	sep = n.keys[mid]
	r := t.newNode(false)
	r.keys = append([]string(nil), n.keys[mid+1:]...)
	r.kids = append([]*node(nil), n.kids[mid+1:]...)
	n.keys, n.kids = n.keys[:mid], n.kids[:mid+1]
	t.splits++
	return replaced, sep, r
}

// del removes key from its leaf (no rebalancing) and reports whether it was
// there. Visit one node per level.
func (t *Tree) del(key string) bool {
	n := t.root
	for {
		t.touch(n)
		if n.leaf {
			break
		}
		n = n.kids[childIndex(n, key)]
	}
	i := sort.SearchStrings(n.keys, key)
	if i >= len(n.keys) || n.keys[i] != key {
		return false
	}
	n.keys = append(n.keys[:i], n.keys[i+1:]...)
	n.vals = append(n.vals[:i], n.vals[i+1:]...)
	t.size--
	return true
}

// scan implements Scan (without the nil-to-empty conversion). Descend once to
// the leaf containing lo (touching each node), then follow next pointers,
// touching each further leaf when you move to it.
func (t *Tree) scan(lo, hi string, limit int) []Entry {
	n := t.root
	for {
		t.touch(n)
		if n.leaf {
			break
		}
		n = n.kids[childIndex(n, lo)]
	}
	var out []Entry
	for first := true; n != nil; first = false {
		if !first {
			t.touch(n)
		}
		for i := sort.SearchStrings(n.keys, lo); i < len(n.keys); i++ {
			if hi != "" && n.keys[i] >= hi {
				return out
			}
			out = append(out, Entry{n.keys[i], n.vals[i]})
			if limit > 0 && len(out) >= limit {
				return out
			}
		}
		n = n.next
	}
	return out
}

// Height returns the number of levels (1 for a lone leaf). Provided.
func (t *Tree) Height() int {
	t.mu.RLock()
	defer t.mu.RUnlock()
	h := 1
	for n := t.root; !n.leaf; n = n.kids[0] {
		h++
	}
	return h
}

// Stats returns a snapshot of the tree's shape. Provided.
func (t *Tree) Stats() Stats {
	t.mu.RLock()
	defer t.mu.RUnlock()
	s := Stats{Keys: t.size, Splits: t.splits}
	for n := t.root; ; n = n.kids[0] {
		s.Height++
		if n.leaf {
			break
		}
	}
	var walk func(n *node)
	walk = func(n *node) {
		if n.leaf {
			s.Leaves++
			return
		}
		s.Internals++
		for _, k := range n.kids {
			walk(k)
		}
	}
	walk(t.root)
	s.LeafFill = float64(s.Keys) / float64(s.Leaves*t.maxKeys)
	return s
}

// LeafKeys returns the keys of every leaf, left to right, by walking the leaf
// chain. Tests use it to see page boundaries. Provided.
func (t *Tree) LeafKeys() [][]string {
	t.mu.RLock()
	defer t.mu.RUnlock()
	n := t.root
	for !n.leaf {
		n = n.kids[0]
	}
	var out [][]string
	for ; n != nil; n = n.next {
		out = append(out, append([]string{}, n.keys...))
	}
	return out
}

// Check verifies every structural invariant and returns the first violation:
// node capacity, sorted keys, children = keys+1, separator bounds, uniform
// leaf depth, leaf-chain order, and that the entry count matches Len.
// Provided: the tests call it after mutations, so a bug in your split logic
// shows up as a precise message instead of a wrong Get later.
func (t *Tree) Check() error {
	t.mu.RLock()
	defer t.mu.RUnlock()
	var leaves []*node
	leafDepth := -1
	var walk func(n *node, depth int, lo, hi *string) error
	walk = func(n *node, depth int, lo, hi *string) error {
		if len(n.keys) > t.maxKeys {
			return fmt.Errorf("node %d holds %d keys, capacity is %d", n.id, len(n.keys), t.maxKeys)
		}
		for i, k := range n.keys {
			if k == "" {
				return fmt.Errorf("node %d contains the empty key", n.id)
			}
			if i > 0 && n.keys[i-1] >= k {
				return fmt.Errorf("node %d keys not strictly increasing: %q then %q", n.id, n.keys[i-1], k)
			}
			if lo != nil && k < *lo {
				return fmt.Errorf("node %d key %q is below its lower bound %q", n.id, k, *lo)
			}
			if hi != nil && k >= *hi {
				return fmt.Errorf("node %d key %q is not below its upper bound %q", n.id, k, *hi)
			}
		}
		if n.leaf {
			if len(n.vals) != len(n.keys) {
				return fmt.Errorf("leaf %d has %d keys but %d values", n.id, len(n.keys), len(n.vals))
			}
			if leafDepth == -1 {
				leafDepth = depth
			} else if leafDepth != depth {
				return fmt.Errorf("leaf %d at depth %d, other leaves at depth %d", n.id, depth, leafDepth)
			}
			leaves = append(leaves, n)
			return nil
		}
		if len(n.kids) != len(n.keys)+1 {
			return fmt.Errorf("internal node %d has %d keys but %d children", n.id, len(n.keys), len(n.kids))
		}
		if len(n.keys) == 0 {
			return fmt.Errorf("internal node %d has no separators", n.id)
		}
		for i, c := range n.kids {
			clo, chi := lo, hi
			if i > 0 {
				clo = &n.keys[i-1]
			}
			if i < len(n.keys) {
				chi = &n.keys[i]
			}
			if err := walk(c, depth+1, clo, chi); err != nil {
				return err
			}
		}
		return nil
	}
	if err := walk(t.root, 0, nil, nil); err != nil {
		return err
	}
	count := 0
	prev := ""
	for i, l := range leaves {
		if i+1 < len(leaves) && l.next != leaves[i+1] {
			return fmt.Errorf("leaf %d: next pointer does not lead to the next leaf in tree order (leaf chain broken)", l.id)
		}
		if i+1 == len(leaves) && l.next != nil {
			return fmt.Errorf("last leaf %d has a next pointer", l.id)
		}
		for _, k := range l.keys {
			if prev != "" && k <= prev {
				return fmt.Errorf("keys out of order across leaves: %q after %q", k, prev)
			}
			prev = k
			count++
		}
	}
	if count != t.size {
		return fmt.Errorf("leaves hold %d entries but Len() is %d", count, t.size)
	}
	return nil
}
