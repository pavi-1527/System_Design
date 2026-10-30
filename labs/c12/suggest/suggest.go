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
// This is the stub: it compiles and every TODO(lab) returns a zero value, so
// the tests fail fast until you implement it. Keep the exported API as is;
// the tests are byte-identical to the reference solution's.
package suggest

import (
	"strings"
	"time"
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

// Index is an immutable prefix index built by Build. It is safe for any
// number of concurrent readers; nothing mutates it after Build returns.
//
// The structure is yours to choose. The reference solution is a rune trie
// (one node per distinct prefix, at most MaxPrefix deep) whose nodes carry a
// sorted slice of at most K suggestions; a sorted []string of queries plus a
// map[prefix][]Suggestion also works. Whatever you pick, write its build
// cost, lookup cost and memory per node in this comment: an interviewer will
// ask for exactly those three numbers.
type Index struct {
	opts Options
	// TODO(lab): your fields.
}

// Build constructs an Index from query → count. Queries are normalised and
// merged ("Pizza" and " pizza " are one query with the summed count),
// non-positive counts and empty queries are ignored, then MinCount and
// Blocklist filtering apply. Every surviving query is pushed into the top-K
// of each of its prefixes (up to MaxPrefix runes deep).
//
// Note that map iteration order is random: the result must not depend on the
// order in which queries are inserted. Less is a total order, so "keep the K
// best under Less" is order independent.
func Build(counts map[string]int64, opts Options) *Index {
	// TODO(lab): apply the defaults (K, MaxPrefix), normalise and merge,
	// filter, then insert each query into every prefix node.
	return &Index{opts: opts}
}

// Suggest returns up to limit suggestions for prefix, best first (count
// descending, then query ascending). The prefix is normalised like a query.
// An empty (normalised) prefix returns nil: the index serves completions, not
// a global "trending" list. limit ≤ 0 or limit > K means K. The returned
// slice is a fresh copy. Calling Suggest on a nil *Index returns nil.
func (ix *Index) Suggest(prefix string, limit int) []Suggestion {
	// TODO(lab): walk to the prefix's node (at most MaxPrefix runes deep),
	// filter by the full prefix if it was truncated, copy up to limit.
	return nil
}

// Nodes returns the number of distinct prefixes the index holds, including
// the root. It is the "memory" of the index: multiply by your per-node cost.
func (ix *Index) Nodes() int {
	// TODO(lab)
	return 0
}

// Len returns the number of distinct queries that survived filtering.
func (ix *Index) Len() int {
	// TODO(lab)
	return 0
}

// Service is one serving replica: the current snapshot, published and
// replaced as a whole. Readers must never take a lock, and a reader must see
// either the whole old index or the whole new one, never a mix. The old index
// stays alive until the last reader that loaded it is done with it, which the
// garbage collector handles for us.
type Service struct {
	// TODO(lab): hold the current *Index so that Swap is one atomic
	// operation and Suggest never blocks (look at sync/atomic).
}

// NewService returns a service serving ix (nil is allowed: it serves nothing
// until the first Swap).
func NewService(ix *Index) *Service {
	// TODO(lab)
	return &Service{}
}

// Current returns the snapshot currently being served.
func (s *Service) Current() *Index {
	// TODO(lab)
	return nil
}

// Swap publishes ix and returns the previous snapshot. Safe to call while
// Suggest runs on other goroutines.
func (s *Service) Swap(ix *Index) *Index {
	// TODO(lab)
	return nil
}

// Suggest answers from whichever snapshot is current at the moment of the
// call.
func (s *Service) Suggest(prefix string, limit int) []Suggestion {
	// TODO(lab)
	return nil
}

// CounterOptions configures NewCounter.
type CounterOptions struct {
	// HalfLife is the time after which an unrepeated query counts half as
	// much. 0 means no decay (plain counts).
	HalfLife time.Duration
	// Now is the clock; nil means time.Now. Tests inject a fake clock.
	Now func() time.Time
}

// Counter accumulates query observations for the next Build, with
// exponential decay: each observation is worth 1 now and 1/2 after one
// half-life. Safe for concurrent use.
//
// Hint: store a float64 count and the time it was last brought up to date,
// and decay lazily (count *= 2^(-elapsed/halfLife)) whenever an entry is
// touched or snapshotted. That keeps Record O(1) with no timers.
type Counter struct {
	opts CounterOptions
	// TODO(lab): your fields.
}

// NewCounter returns an empty counter.
func NewCounter(opts CounterOptions) *Counter {
	if opts.Now == nil {
		opts.Now = time.Now
	}
	// TODO(lab)
	return &Counter{opts: opts}
}

// Record counts one observation of query (normalised; empty is ignored).
func (c *Counter) Record(query string) {
	// TODO(lab)
}

// Snapshot returns the decayed counts as of now, rounded to integers, in the
// form Build takes. Queries whose decayed count rounds to zero are omitted
// and forgotten, which bounds the counter's memory over time.
func (c *Counter) Snapshot() map[string]int64 {
	// TODO(lab)
	return nil
}

// Len returns the number of queries currently tracked.
func (c *Counter) Len() int {
	// TODO(lab)
	return 0
}
