// Package lfu is a frequency-aware cache in the style of TinyLFU: a
// count-min sketch estimates how often each key has been requested, and a
// full cache only admits a new key if it has been requested more often than
// the entry it would displace. A one-pass scan of cold keys then cannot flush
// the hot set the way it flushes a plain LRU.
//
// It is the STRETCH lab for A3 Unit 1 §3. Suggested order: Sketch
// (Increment, Estimate, the halving), then Cache.Get, then Cache.Set's
// admission rule. Run: go test -race ./labs/a3/lfu/
//
// # Sketch
//
// A count-min sketch with Rows = 4 rows of 8-bit counters. Row r uses index
// Hashes(key)[r] & mask, where the width is NewSketch's counters argument
// rounded up to a power of two.
//
//   - Increment(key) applies the conservative update: compute the minimum
//     of the key's four counters and raise only the counters equal to that
//     minimum (by one). Counters saturate at MaxCount (15). This keeps
//     Estimate an upper bound on the true count (until saturation) with less
//     over-counting than incrementing all four.
//   - Estimate(key) is the minimum of the key's four counters.
//   - Aging: after every sampleSize calls to Increment, every counter is
//     halved (integer shift right by one) and the call counter restarts from
//     zero. Frequencies therefore decay, so yesterday's hot keys eventually
//     stop blocking today's.
//
// # Cache
//
// A fixed-capacity cache (entries, not bytes) with LRU order among its
// residents and a frequency-based admission filter in front.
//
//   - Get records the access in the sketch (hit or miss; misses matter, they
//     are how a key earns frequency before it is ever admitted), then on a
//     hit promotes the entry to most-recently-used.
//   - Set on an existing key updates its value and promotes it. Set on a new
//     key while there is room inserts it. Set on a new key when the cache is
//     full compares candidate and victim, where the victim is the
//     least-recently-used entry: the candidate is admitted only if
//     Estimate(candidate) > Estimate(victim) (a tie keeps the incumbent). If
//     admitted, the victim is evicted (Stats.Evictions). If not, nothing
//     changes except Stats.Rejections, and Set returns false. Set itself does
//     not touch the sketch.
//   - Safe for concurrent use.
package lfu

import (
	"container/list"
	"hash/fnv"
	"sync"
)

// Sketch parameters.
const (
	Rows     = 4
	MaxCount = 15
)

// Sketch is a count-min sketch with conservative update and periodic halving.
type Sketch struct {
	rows      [Rows][]uint8
	mask      uint64
	sample    int
	additions int
}

// Hashes returns the four row hashes of key. Provided.
func Hashes(key string) [Rows]uint64 {
	h := fnv.New64a()
	h.Write([]byte(key))
	x := h.Sum64()
	var out [Rows]uint64
	for i := range out {
		// splitmix64 finaliser, seeded differently per row.
		x += 0x9e3779b97f4a7c15
		z := x
		z = (z ^ (z >> 30)) * 0xbf58476d1ce4e5b9
		z = (z ^ (z >> 27)) * 0x94d049bb133111eb
		out[i] = z ^ (z >> 31)
	}
	return out
}

// NewSketch returns a sketch with at least counters counters per row
// (rounded up to a power of two) that halves every sampleSize increments.
func NewSketch(counters, sampleSize int) *Sketch {
	w := 1
	for w < counters {
		w <<= 1
	}
	s := &Sketch{mask: uint64(w - 1), sample: sampleSize}
	for i := range s.rows {
		s.rows[i] = make([]uint8, w)
	}
	return s
}

// Increment records one occurrence of key.
func (s *Sketch) Increment(key string) {
	hs := Hashes(key)
	min := uint8(MaxCount)
	for r := 0; r < Rows; r++ {
		if c := s.rows[r][hs[r]&s.mask]; c < min {
			min = c
		}
	}
	if min < MaxCount {
		for r := 0; r < Rows; r++ {
			if i := hs[r] & s.mask; s.rows[r][i] == min {
				s.rows[r][i]++
			}
		}
	}
	s.additions++
	if s.sample > 0 && s.additions >= s.sample {
		for r := range s.rows {
			for i := range s.rows[r] {
				s.rows[r][i] >>= 1
			}
		}
		s.additions = 0
	}
}

// Estimate returns the estimated number of recent occurrences of key.
func (s *Sketch) Estimate(key string) int {
	hs := Hashes(key)
	min := MaxCount
	for r := 0; r < Rows; r++ {
		if c := int(s.rows[r][hs[r]&s.mask]); c < min {
			min = c
		}
	}
	return min
}

// Config configures a Cache.
type Config struct {
	Capacity       int // entries; must be > 0
	SketchCounters int // per row; default 10 * Capacity
	SampleSize     int // increments between halvings; default 10 * Capacity
}

// Stats is a snapshot of the counters.
type Stats struct {
	Hits       uint64
	Misses     uint64
	Evictions  uint64
	Rejections uint64 // Sets refused by the admission filter
}

type item[V any] struct {
	key   string
	value V
}

// Cache is the admission-filtered cache.
type Cache[V any] struct {
	cfg    Config
	mu     sync.Mutex
	sketch *Sketch
	ll     *list.List // front = most recently used
	m      map[string]*list.Element
	st     Stats
}

// New returns an empty cache. A Capacity <= 0 is treated as 1.
func New[V any](cfg Config) *Cache[V] {
	if cfg.Capacity <= 0 {
		cfg.Capacity = 1
	}
	if cfg.SketchCounters <= 0 {
		cfg.SketchCounters = 10 * cfg.Capacity
	}
	if cfg.SampleSize <= 0 {
		cfg.SampleSize = 10 * cfg.Capacity
	}
	return &Cache[V]{
		cfg:    cfg,
		sketch: NewSketch(cfg.SketchCounters, cfg.SampleSize),
		ll:     list.New(),
		m:      make(map[string]*list.Element),
	}
}

// Get returns the value for key, recording the access.
func (c *Cache[V]) Get(key string) (V, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sketch.Increment(key)
	el, ok := c.m[key]
	if !ok {
		c.st.Misses++
		var zero V
		return zero, false
	}
	c.ll.MoveToFront(el)
	c.st.Hits++
	return el.Value.(*item[V]).value, true
}

// Set stores key, subject to the admission rule. It reports whether the
// entry is now in the cache.
func (c *Cache[V]) Set(key string, value V) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.m[key]; ok {
		el.Value.(*item[V]).value = value
		c.ll.MoveToFront(el)
		return true
	}
	if c.ll.Len() >= c.cfg.Capacity {
		victim := c.ll.Back()
		vk := victim.Value.(*item[V]).key
		if c.sketch.Estimate(key) <= c.sketch.Estimate(vk) {
			c.st.Rejections++
			return false
		}
		c.ll.Remove(victim)
		delete(c.m, vk)
		c.st.Evictions++
	}
	c.m[key] = c.ll.PushFront(&item[V]{key: key, value: value})
	return true
}

// Frequency returns the sketch's estimate for key. Provided for tests and
// debugging.
func (c *Cache[V]) Frequency(key string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.sketch.Estimate(key)
}

// Len returns the number of resident entries.
func (c *Cache[V]) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.ll.Len()
}

// Keys returns the resident keys, most recently used first.
func (c *Cache[V]) Keys() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	keys := make([]string, 0, c.ll.Len())
	for el := c.ll.Front(); el != nil; el = el.Next() {
		keys = append(keys, el.Value.(*item[V]).key)
	}
	return keys
}

// Stats returns a snapshot of the counters.
func (c *Cache[V]) Stats() Stats {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.st
}
