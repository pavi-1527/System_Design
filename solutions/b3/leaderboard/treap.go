package leaderboard

// An order-statistic treap: a binary search tree on (score desc, seq asc)
// plus a max-heap on a pseudo-random priority, which keeps the expected depth
// near 1.4·log2(n) for ANY insertion order (ascending scores included). Each
// node stores its subtree size, which turns "how many players are ahead of me"
// and "give me positions 100..109" into a walk down one root-to-leaf path.
//
// Nodes are mutated in place; the Board's lock guards the whole tree.

type rec struct {
	player string
	score  int64
	seq    uint64 // logical time at which the player reached this score
}

// before reports whether a ranks strictly ahead of b.
func before(a, b *rec) bool {
	if a.score != b.score {
		return a.score > b.score
	}
	return a.seq < b.seq
}

type node struct {
	rec  rec
	pri  uint32
	size int
	l, r *node
}

func size(n *node) int {
	if n == nil {
		return 0
	}
	return n.size
}

func (n *node) fix() { n.size = 1 + size(n.l) + size(n.r) }

// priority derives a node's heap priority from its (unique) sequence number
// with the splitmix64 finaliser: deterministic, so runs are reproducible.
func priority(seq uint64) uint32 {
	z := seq + 0x9e3779b97f4a7c15
	z = (z ^ (z >> 30)) * 0xbf58476d1ce4e5b9
	z = (z ^ (z >> 27)) * 0x94d049bb133111eb
	return uint32((z ^ (z >> 31)) >> 32)
}

// split returns (nodes ahead of key, nodes not ahead of key).
func split(t *node, key *rec) (l, r *node) {
	if t == nil {
		return nil, nil
	}
	if before(&t.rec, key) {
		a, b := split(t.r, key)
		t.r = a
		t.fix()
		return t, b
	}
	a, b := split(t.l, key)
	t.l = b
	t.fix()
	return a, t
}

// merge joins a and b, where everything in a ranks ahead of everything in b.
func merge(a, b *node) *node {
	switch {
	case a == nil:
		return b
	case b == nil:
		return a
	case a.pri > b.pri:
		a.r = merge(a.r, b)
		a.fix()
		return a
	default:
		b.l = merge(a, b.l)
		b.fix()
		return b
	}
}

func insert(t *node, r rec) *node {
	l, rt := split(t, &r)
	n := &node{rec: r, pri: priority(r.seq), size: 1}
	return merge(merge(l, n), rt)
}

// remove deletes the node whose record equals key's ordering position.
func remove(t *node, key *rec) *node {
	if t == nil {
		return nil
	}
	switch {
	case before(key, &t.rec):
		t.l = remove(t.l, key)
	case before(&t.rec, key):
		t.r = remove(t.r, key)
	default:
		return merge(t.l, t.r)
	}
	t.fix()
	return t
}

// countBefore returns how many nodes rank strictly ahead of key.
func countBefore(t *node, key *rec) int {
	n := 0
	for t != nil {
		if before(&t.rec, key) {
			n += size(t.l) + 1
			t = t.r
		} else {
			t = t.l
		}
	}
	return n
}

// collect appends the records at in-order positions [lo, hi) of t.
func collect(t *node, lo, hi int, out *[]rec) {
	if t == nil || lo >= hi {
		return
	}
	ls := size(t.l)
	if lo < ls {
		collect(t.l, lo, min(hi, ls), out)
	}
	if lo <= ls && ls < hi {
		*out = append(*out, t.rec)
	}
	if hi > ls+1 {
		collect(t.r, max(lo-ls-1, 0), hi-ls-1, out)
	}
}

// clone deep-copies a treap (O(n)); the copy shares nothing with the original.
func clone(t *node) *node {
	if t == nil {
		return nil
	}
	c := *t
	c.l, c.r = clone(t.l), clone(t.r)
	return &c
}
