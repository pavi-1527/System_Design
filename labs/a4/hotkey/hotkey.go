// Package hotkey finds hot keys in a stream and spreads them: a Space-Saving
// heavy-hitter summary, a Count-Min sketch, a windowed Detector built on the
// summary, and a Router that splits a hot key into several physical keys for
// writes and fans in over all of them for reads.
//
// It is the STRETCH lab for A4 Unit 3 (§8). The problem it solves: one
// celebrity account, one viral merchant or one monotonically increasing key
// sends a large share of the traffic to a single partition, and adding nodes
// does not help because the key still lives on one.
//
// What the tests check:
//
//   - SpaceSaving keeps at most `capacity` counters. Every monitored key's
//     Count is an over-estimate of its true frequency and Count-Err is an
//     under-estimate; any key whose true frequency exceeds Total/capacity is
//     guaranteed to be monitored. A new key evicts the counter with the
//     smallest count (ties: smallest key) and inherits that count as its
//     error.
//   - CountMin never underestimates; with enough width and depth most
//     estimates are within a small error of the truth, and depth matters.
//   - Detector reports a key hot when its guaranteed count (Count-Err)
//     reaches both MinCount and Threshold of the window's traffic; a window
//     can be rotated, after which last window's hot keys stay hot for one more
//     window.
//   - Router sends cold keys to themselves and spreads hot keys over Fanout
//     suffixed keys; ReadKeys returns every key a read must visit. Under a
//     Zipf-like stream the busiest partition's share of writes drops sharply.
//
// Run: go test -race ./labs/a4/hotkey/
package hotkey

import (
	"strconv"
	"sync"
)

// Item is one entry of a heavy-hitter report.
type Item struct {
	Key   string
	Count uint64 // upper bound on the key's true frequency
	Err   uint64 // Count-Err is a lower bound on the true frequency
}

// counter is one monitored key. Provided, together with counterHeap, a
// min-heap ordered by (count, key) whose idx fields track positions so that
// container/heap's heap.Fix(&s.heap, c.idx) can restore order after a count
// changes (import "container/heap"). You may use it or replace it with
// something simpler.
type counter struct {
	key        string
	count, err uint64
	idx        int
}

type counterHeap []*counter

func (h counterHeap) Len() int { return len(h) }
func (h counterHeap) Less(i, j int) bool {
	if h[i].count != h[j].count {
		return h[i].count < h[j].count
	}
	return h[i].key < h[j].key
}
func (h counterHeap) Swap(i, j int) {
	h[i], h[j] = h[j], h[i]
	h[i].idx, h[j].idx = i, j
}
func (h *counterHeap) Push(x any) { c := x.(*counter); c.idx = len(*h); *h = append(*h, c) }
func (h *counterHeap) Pop() any {
	old := *h
	n := len(old)
	c := old[n-1]
	*h = old[:n-1]
	return c
}

// SpaceSaving is the Metwally–Agrawal–El Abbadi heavy-hitter summary: a fixed
// number of counters that approximately track the most frequent keys of a
// stream in O(capacity) memory. Safe for concurrent use.
type SpaceSaving struct {
	mu    sync.Mutex
	cap   int
	total uint64
	items map[string]*counter
	heap  counterHeap // min-heap on (count, key)
}

// NewSpaceSaving returns a summary with the given number of counters
// (capacity < 1 is treated as 1).
func NewSpaceSaving(capacity int) *SpaceSaving {
	if capacity < 1 {
		capacity = 1
	}
	return &SpaceSaving{cap: capacity, items: make(map[string]*counter, capacity)}
}

// Observe records one occurrence of key.
func (s *SpaceSaving) Observe(key string) { s.ObserveN(key, 1) }

// ObserveN records n occurrences of key (n == 0 is a no-op).
func (s *SpaceSaving) ObserveN(key string, n uint64) {
	// TODO(lab): Space-Saving update (see the type comment and the package
	// doc): monitored key → add n; room left → new counter; otherwise replace
	// the minimum counter (smallest count, ties: smallest key), the newcomer
	// gets count = min+n and err = min. Keep the heap ordered with heap.Fix.
	// Total counts every observation, including n.
}

// Estimate returns the counter for key: Count (upper bound), Err, and whether
// the key is currently monitored.
func (s *SpaceSaving) Estimate(key string) (count, err uint64, ok bool) {
	// TODO(lab)
	return 0, 0, false
}

// Top returns up to k monitored keys, most frequent first (ties: smaller key
// first). k <= 0 returns nil.
func (s *SpaceSaving) Top(k int) []Item {
	// TODO(lab): sort by Count descending, ties by Key ascending; at most k.
	return nil
}

// Total returns the number of occurrences observed since creation or Reset.
func (s *SpaceSaving) Total() uint64 {
	// TODO(lab)
	return 0
}

// Reset forgets everything.
func (s *SpaceSaving) Reset() {
	// TODO(lab)
}

// hash64 is FNV-1a (64-bit). Provided.
func hash64(s string) uint64 {
	h := uint64(14695981039346656037)
	for i := 0; i < len(s); i++ {
		h ^= uint64(s[i])
		h *= 1099511628211
	}
	return h
}

// mix64 is the splitmix64 finalizer. Provided.
func mix64(h uint64) uint64 {
	h ^= h >> 30
	h *= 0xbf58476d1ce4e5b9
	h ^= h >> 27
	h *= 0x94d049bb133111eb
	h ^= h >> 31
	return h
}

// PartitionOf maps a physical key to one of `partitions` partitions by hash
// modulo; it is what the Router tests use to measure load. Provided.
func PartitionOf(key string, partitions int) int {
	if partitions <= 0 {
		return 0
	}
	return int(mix64(hash64(key)) % uint64(partitions))
}

// CountMin is a Count-Min sketch: depth rows of width counters; a key
// increments one counter per row and its estimate is the minimum over the
// rows. It never underestimates; the over-estimate is at most about
// e/width × (total count) with probability 1 − e^(−depth). Safe for
// concurrent use.
type CountMin struct {
	mu    sync.Mutex
	width int
	depth int
	rows  [][]uint64
	total uint64
}

// NewCountMin returns a sketch with the given width and depth (each < 1 is
// treated as 1).
func NewCountMin(width, depth int) *CountMin {
	if width < 1 {
		width = 1
	}
	if depth < 1 {
		depth = 1
	}
	rows := make([][]uint64, depth)
	for i := range rows {
		rows[i] = make([]uint64, width)
	}
	return &CountMin{width: width, depth: depth, rows: rows}
}

// cell returns the column of row i for key, by double hashing:
// (h1 + i*h2) mod width with two independent-looking 64-bit hashes.
// Provided.
func (c *CountMin) cell(key string, i int) int {
	h1 := hash64(key)
	h2 := mix64(h1) | 1
	return int((h1 + uint64(i)*h2) % uint64(c.width))
}

// Add records n occurrences of key.
func (c *CountMin) Add(key string, n uint64) {
	// TODO(lab): for each row i, rows[i][cell(key, i)] += n.
}

// Estimate returns an upper bound on key's count: the minimum over the rows.
func (c *CountMin) Estimate(key string) uint64 {
	// TODO(lab): the minimum over the rows of the key's counter.
	return 0
}

// Total returns the sum of everything added.
func (c *CountMin) Total() uint64 {
	// TODO(lab)
	return 0
}

// DetectorConfig configures a Detector. Zero values get defaults.
type DetectorConfig struct {
	// Capacity is the number of Space-Saving counters per window. Default 64.
	Capacity int
	// Threshold is the share of the window's traffic a key must have, as a
	// fraction in (0, 1]. Default 0.05.
	Threshold float64
	// MinCount is the least guaranteed count a key needs, so that a quiet
	// window does not declare everything hot. Default 20.
	MinCount uint64
}

// Detector flags hot keys over a sliding pair of windows. Safe for
// concurrent use.
type Detector struct {
	cfg DetectorConfig

	mu       sync.Mutex
	cur      *SpaceSaving
	previous map[string]bool // keys that were hot when the last window ended
}

// NewDetector returns a detector with an empty first window.
func NewDetector(cfg DetectorConfig) *Detector {
	if cfg.Capacity <= 0 {
		cfg.Capacity = 64
	}
	if cfg.Threshold <= 0 || cfg.Threshold > 1 {
		cfg.Threshold = 0.05
	}
	if cfg.MinCount == 0 {
		cfg.MinCount = 20
	}
	return &Detector{cfg: cfg, cur: NewSpaceSaving(cfg.Capacity), previous: map[string]bool{}}
}

// Observe records one request for key in the current window.
func (d *Detector) Observe(key string) {
	// TODO(lab): count the key in the current window's SpaceSaving.
}

// hotInWindow reports whether key's guaranteed count in ss meets both
// thresholds.
func (d *Detector) hotInWindow(ss *SpaceSaving, key string) bool {
	// TODO(lab): the key must be monitored and its GUARANTEED count
	// (Count-Err) must be >= cfg.MinCount and >= cfg.Threshold * ss.Total().
	return false
}

// IsHot reports whether key is hot in the current window (by its guaranteed
// count, Count-Err, so unlucky inheritance of an evicted counter's count
// cannot create false positives) or was hot when the previous window ended.
func (d *Detector) IsHot(key string) bool {
	// TODO(lab): hot in the current window, or in d.previous.
	return false
}

// HotKeys returns the keys that are hot in the current window, most frequent
// first.
func (d *Detector) HotKeys() []string {
	// TODO(lab): walk cur.Top(cfg.Capacity) and keep the keys for which
	// hotInWindow is true (already in descending count order).
	return nil
}

// Rotate ends the current window: the keys that were hot in it stay hot for
// the next window (so the router does not flap), and counting starts afresh.
func (d *Detector) Rotate() {
	// TODO(lab): remember the current hot keys in d.previous, then start a
	// fresh SpaceSaving window.
}

// SplitKey returns the i-th physical key of a split hot key: "key#i".
// Provided.
func SplitKey(key string, i int) string { return key + "#" + strconv.Itoa(i) }

// Router decides which physical key(s) a logical key's reads and writes use.
// Safe for concurrent use if Rand is.
type Router struct {
	det    *Detector
	fanout int
	rand   func(n int) int
}

// NewRouter returns a router that splits keys det reports as hot into fanout
// physical keys. rand(n) returns a uniform int in [0, n); if rand is nil the
// router cycles 0, 1, 2, ... (deterministic round robin). fanout < 1 is
// treated as 1.
func NewRouter(det *Detector, fanout int, rand func(n int) int) *Router {
	if fanout < 1 {
		fanout = 1
	}
	r := &Router{det: det, fanout: fanout, rand: rand}
	if rand == nil {
		var mu sync.Mutex
		next := 0
		r.rand = func(n int) int {
			mu.Lock()
			defer mu.Unlock()
			v := next % n
			next++
			return v
		}
	}
	return r
}

// WriteKey returns the physical key a write to logical key should use: key
// itself when it is cold (or fanout is 1), otherwise SplitKey(key, rand(fanout)).
func (r *Router) WriteKey(key string) string {
	// TODO(lab)
	return key
}

// ReadKeys returns every physical key a read of logical key must visit and
// combine: just [key] when it is cold; when hot, key itself FIRST (writes made
// before the key was classified hot still live there) followed by
// SplitKey(key, 0..fanout-1).
func (r *Router) ReadKeys(key string) []string {
	// TODO(lab)
	return []string{key}
}
