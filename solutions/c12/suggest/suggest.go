// Package suggest is the lab for C12 (search autocomplete): an in-memory
// prefix index with a precomputed top-K per node, an atomically swappable
// serving snapshot, and a decaying counter that feeds the next build.
//
// The three types are the three halves of the pipeline from the case study:
//
//   - Counter is the aggregation step: it turns a stream of observed queries
//     into counts with a half-life, so last month's news stops dominating
//     this morning's suggestions.
//   - Build is the index build: counts in, an immutable Index out. Every node
//     of the index already holds its best K completions, so a lookup is
//     O(len(prefix)) and never walks a subtree.
//   - Service is a serving replica: it holds the current Index behind an
//     atomic pointer so that a new snapshot is published with one pointer
//     swap while readers keep reading, with no lock on the read path.
//
// Reference solution. The index is a rune trie (one node per distinct
// prefix, capped at Options.MaxPrefix runes); each node carries a sorted
// slice of at most K suggestions. Build cost is O(total runes × K) with the
// insertion-into-sorted-slice approach used here; memory is one node per
// distinct prefix plus K suggestions per node; lookup is O(len(prefix) + K).
package suggest

import (
	"math"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"
)

// Defaults applied by Build when an Options field is zero.
const (
	DefaultK         = 5  // suggestions kept per prefix
	DefaultMaxPrefix = 20 // longest prefix (in runes) that gets its own node
)

// Options configures Build.
type Options struct {
	// K is how many suggestions each prefix keeps (and the most Suggest ever
	// returns). 0 means DefaultK.
	K int
	// MaxPrefix caps the depth of the index in runes. Queries longer than this
	// are still suggested, but are reachable only through their first
	// MaxPrefix runes; lookups with longer prefixes are truncated to MaxPrefix
	// runes and the candidates are then filtered by the full prefix (so the
	// result may be shorter than K). 0 means DefaultMaxPrefix.
	MaxPrefix int
	// MinCount drops queries with a (merged) count below it at build time:
	// the long tail is noise, and it is also where one person's private
	// query lives. 0 keeps everything with a positive count.
	MinCount int64
	// Blocklist lists terms that must never be suggested. Entries are
	// normalised like queries; a query is dropped when an entry equals it or
	// appears in it as a whole word (or whole phrase, for multi-word
	// entries). "cat" blocks "cat videos" and "my cat", not "catalogue".
	Blocklist []string
}

func (o Options) withDefaults() Options {
	if o.K <= 0 {
		o.K = DefaultK
	}
	if o.MaxPrefix <= 0 {
		o.MaxPrefix = DefaultMaxPrefix
	}
	return o
}

// Suggestion is one ranked completion.
type Suggestion struct {
	Query string `json:"query"`
	Count int64  `json:"count"`
}

// Less reports whether a ranks before b: higher count first, then query
// ascending (byte order of the normalised query), so that the ranking is a
// total order and every implementation returns the same list. Provided.
func Less(a, b Suggestion) bool {
	if a.Count != b.Count {
		return a.Count > b.Count
	}
	return a.Query < b.Query
}

// Normalize returns the canonical form of a query or prefix: Unicode-aware
// lower case, surrounding whitespace removed, internal runs of whitespace
// collapsed to one space. Two inputs that normalise equal are the same
// query. Provided.
func Normalize(s string) string {
	return strings.Join(strings.Fields(strings.ToLower(s)), " ")
}

// blocked reports whether q (normalised) contains any of the normalised
// blocklist terms as a whole word or phrase.
func blocked(q string, terms []string) bool {
	padded := " " + q + " "
	for _, t := range terms {
		if strings.Contains(padded, " "+t+" ") {
			return true
		}
	}
	return false
}

// node is one distinct prefix. top holds at most K suggestions sorted by Less.
type node struct {
	children map[rune]*node
	top      []Suggestion
}

// Index is an immutable prefix index built by Build. It is safe for any
// number of concurrent readers; nothing mutates it after Build returns.
type Index struct {
	opts  Options
	root  *node
	nodes int
	n     int
}

// Build constructs an Index from query → count. Queries are normalised and
// merged ("Pizza" and " pizza " are one query with the summed count),
// non-positive counts and empty queries are ignored, then MinCount and
// Blocklist filtering apply. Every surviving query is pushed into the top-K
// of each of its prefixes (up to MaxPrefix runes deep).
func Build(counts map[string]int64, opts Options) *Index {
	opts = opts.withDefaults()
	merged := make(map[string]int64, len(counts))
	for q, c := range counts {
		if c <= 0 {
			continue
		}
		nq := Normalize(q)
		if nq == "" {
			continue
		}
		merged[nq] += c
	}
	var block []string
	for _, t := range opts.Blocklist {
		if nt := Normalize(t); nt != "" {
			block = append(block, nt)
		}
	}
	ix := &Index{opts: opts, root: &node{}, nodes: 1}
	for q, c := range merged {
		if c < opts.MinCount || blocked(q, block) {
			continue
		}
		ix.n++
		ix.insert(Suggestion{Query: q, Count: c})
	}
	return ix
}

// insert pushes s into the top-K of every prefix of s.Query, creating nodes
// as needed, down to MaxPrefix runes. Order independence: pushTop keeps the
// K best under the total order Less, so the result does not depend on the
// order in which queries are inserted (map iteration order is random).
func (ix *Index) insert(s Suggestion) {
	n := ix.root
	depth := 0
	for _, r := range s.Query {
		if depth == ix.opts.MaxPrefix {
			break
		}
		child := n.children[r]
		if child == nil {
			if n.children == nil {
				n.children = map[rune]*node{}
			}
			child = &node{}
			n.children[r] = child
			ix.nodes++
		}
		n = child
		depth++
		n.top = pushTop(n.top, s, ix.opts.K)
	}
}

// pushTop inserts s into the sorted slice top (len ≤ k) if it ranks inside
// the best k, dropping the worst entry when the slice is full.
func pushTop(top []Suggestion, s Suggestion, k int) []Suggestion {
	i := sort.Search(len(top), func(i int) bool { return Less(s, top[i]) })
	if i >= k {
		return top
	}
	if len(top) < k {
		top = append(top, Suggestion{})
	}
	copy(top[i+1:], top[i:len(top)-1])
	top[i] = s
	return top
}

// Suggest returns up to limit suggestions for prefix, best first (count
// descending, then query ascending). The prefix is normalised like a query.
// An empty (normalised) prefix returns nil: the index serves completions, not
// a global "trending" list. limit ≤ 0 or limit > K means K. The returned
// slice is a fresh copy. Calling Suggest on a nil *Index returns nil.
func (ix *Index) Suggest(prefix string, limit int) []Suggestion {
	if ix == nil || ix.root == nil {
		return nil
	}
	p := Normalize(prefix)
	if p == "" {
		return nil
	}
	if limit <= 0 || limit > ix.opts.K {
		limit = ix.opts.K
	}
	n := ix.root
	depth := 0
	for _, r := range p {
		if depth == ix.opts.MaxPrefix {
			break
		}
		if n = n.children[r]; n == nil {
			return nil
		}
		depth++
	}
	truncated := depth < utf8.RuneCountInString(p)
	out := make([]Suggestion, 0, limit)
	for _, s := range n.top {
		if len(out) == limit {
			break
		}
		if truncated && !strings.HasPrefix(s.Query, p) {
			continue
		}
		out = append(out, s)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// Nodes returns the number of distinct prefixes the index holds, including
// the root. It is the "memory" of the index: multiply by your per-node cost.
func (ix *Index) Nodes() int {
	if ix == nil {
		return 0
	}
	return ix.nodes
}

// Len returns the number of distinct queries that survived filtering.
func (ix *Index) Len() int {
	if ix == nil {
		return 0
	}
	return ix.n
}

// Service is one serving replica: the current snapshot behind an atomic
// pointer. Readers never take a lock; a publish is one pointer swap, so a
// reader sees either the whole old index or the whole new one, never a mix.
// The old index stays alive until the last reader that loaded it is done
// with it, which the garbage collector handles for us.
type Service struct {
	cur atomic.Pointer[Index]
}

// NewService returns a service serving ix (nil is allowed: it serves nothing
// until the first Swap).
func NewService(ix *Index) *Service {
	s := &Service{}
	s.cur.Store(ix)
	return s
}

// Current returns the snapshot currently being served.
func (s *Service) Current() *Index { return s.cur.Load() }

// Swap publishes ix and returns the previous snapshot. Safe to call while
// Suggest runs on other goroutines.
func (s *Service) Swap(ix *Index) *Index { return s.cur.Swap(ix) }

// Suggest answers from whichever snapshot is current at the moment of the
// call.
func (s *Service) Suggest(prefix string, limit int) []Suggestion {
	return s.cur.Load().Suggest(prefix, limit)
}

// CounterOptions configures NewCounter.
type CounterOptions struct {
	// HalfLife is the time after which an unrepeated query counts half as
	// much. 0 means no decay (plain counts).
	HalfLife time.Duration
	// Now is the clock; nil means time.Now. Tests inject a fake clock.
	Now func() time.Time
}

type entry struct {
	count float64
	at    time.Time
}

// Counter accumulates query observations for the next Build, with
// exponential decay: each observation is worth 1 now and 1/2 after one
// half-life. Safe for concurrent use.
type Counter struct {
	opts CounterOptions
	mu   sync.Mutex
	m    map[string]*entry
}

// NewCounter returns an empty counter.
func NewCounter(opts CounterOptions) *Counter {
	if opts.Now == nil {
		opts.Now = time.Now
	}
	return &Counter{opts: opts, m: map[string]*entry{}}
}

// decay brings e's count forward to now. Decaying lazily, only when an entry
// is touched or snapshotted, keeps Record O(1).
func (c *Counter) decay(e *entry, now time.Time) {
	if c.opts.HalfLife <= 0 {
		return
	}
	if dt := now.Sub(e.at); dt > 0 {
		e.count *= math.Exp2(-float64(dt) / float64(c.opts.HalfLife))
		e.at = now
	}
}

// Record counts one observation of query (normalised; empty is ignored).
func (c *Counter) Record(query string) {
	q := Normalize(query)
	if q == "" {
		return
	}
	now := c.opts.Now()
	c.mu.Lock()
	e := c.m[q]
	if e == nil {
		e = &entry{at: now}
		c.m[q] = e
	}
	c.decay(e, now)
	e.count++
	c.mu.Unlock()
}

// Snapshot returns the decayed counts as of now, rounded to integers, in the
// form Build takes. Queries whose decayed count rounds to zero are omitted
// and forgotten, which bounds the counter's memory over time.
func (c *Counter) Snapshot() map[string]int64 {
	now := c.opts.Now()
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make(map[string]int64, len(c.m))
	for q, e := range c.m {
		c.decay(e, now)
		n := int64(math.Round(e.count))
		if n < 1 {
			delete(c.m, q)
			continue
		}
		out[q] = n
	}
	return out
}

// Len returns the number of queries currently tracked.
func (c *Counter) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.m)
}
