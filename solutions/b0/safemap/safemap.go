// Package safemap is a generic, sharded, concurrency-safe map.
//
// Lab B0 Unit 1 §3. A single map guarded by one sync.Mutex is correct but
// serialises every caller on one lock. Splitting the key space into N shards,
// each with its own lock, lets callers that touch different shards proceed in
// parallel. The interesting parts are: choosing a shard (hashing), making
// read-modify-write atomic (Update), and never calling user code while
// holding a lock (Range).
package safemap

import (
	"encoding/binary"
	"fmt"
	"hash/maphash"
	"sync"
)

// DefaultShards is used when New is called with shards <= 0.
const DefaultShards = 32

// Hasher maps a key to a 64-bit hash using the given seed. Equal keys MUST
// produce equal hashes, or the same key could land in two shards.
type Hasher[K comparable] func(seed maphash.Seed, key K) uint64

// HashString is a Hasher for string keys.
func HashString(seed maphash.Seed, s string) uint64 { return maphash.String(seed, s) }

// HashInteger is a Hasher for any integer key type.
func HashInteger[K ~int | ~int8 | ~int16 | ~int32 | ~int64 | ~uint | ~uint8 | ~uint16 | ~uint32 | ~uint64 | ~uintptr](seed maphash.Seed, k K) uint64 {
	var b [8]byte
	binary.LittleEndian.PutUint64(b[:], uint64(k))
	return maphash.Bytes(seed, b[:])
}

// defaultHasher handles strings and integers directly and falls back to
// formatting the key with %#v. The fallback is slow and is only correct for
// keys whose %#v form is equal exactly when the keys are equal (true for most
// structs of strings/ints; false for e.g. float fields holding -0 and +0).
// Go 1.24 adds maphash.Comparable, which solves this properly, but this
// module's go.mod says go 1.22, so `go vet` (stdversion) would reject it.
func defaultHasher[K comparable](seed maphash.Seed, key K) uint64 {
	switch k := any(key).(type) {
	case string:
		return maphash.String(seed, k)
	case int:
		return HashInteger(seed, k)
	case int64:
		return HashInteger(seed, k)
	case int32:
		return HashInteger(seed, k)
	case uint64:
		return HashInteger(seed, k)
	case uint32:
		return HashInteger(seed, k)
	case uint:
		return HashInteger(seed, k)
	default:
		return maphash.String(seed, fmt.Sprintf("%#v", key))
	}
}

// shard is one independently locked partition of the key space.
type shard[K comparable, V any] struct {
	mu sync.RWMutex
	m  map[K]V
	// Padding so neighbouring shards' locks are less likely to share a
	// 64-byte cache line (false sharing). Measured effect is workload
	// dependent; see the STRETCH benchmark.
	_ [40]byte
}

// ShardedMap is a concurrency-safe map split into a power-of-two number of
// shards. The zero value is NOT usable; call New.
type ShardedMap[K comparable, V any] struct {
	seed   maphash.Seed
	hash   Hasher[K]
	mask   uint64
	shards []shard[K, V]
}

// New returns an empty map with the number of shards rounded up to a power
// of two (shards <= 0 means DefaultShards). hash may be nil, in which case a
// default hasher is used (see defaultHasher's caveats).
func New[K comparable, V any](shards int, hash Hasher[K]) *ShardedMap[K, V] {
	if shards <= 0 {
		shards = DefaultShards
	}
	n := 1
	for n < shards {
		n <<= 1
	}
	if hash == nil {
		hash = defaultHasher[K]
	}
	m := &ShardedMap[K, V]{
		seed:   maphash.MakeSeed(),
		hash:   hash,
		mask:   uint64(n - 1),
		shards: make([]shard[K, V], n),
	}
	for i := range m.shards {
		m.shards[i].m = make(map[K]V)
	}
	return m
}

func (m *ShardedMap[K, V]) shardFor(key K) *shard[K, V] {
	return &m.shards[m.hash(m.seed, key)&m.mask]
}

// Shards reports the number of shards (always a power of two).
func (m *ShardedMap[K, V]) Shards() int { return len(m.shards) }

// Get returns the value for key and whether it was present.
func (m *ShardedMap[K, V]) Get(key K) (V, bool) {
	s := m.shardFor(key)
	s.mu.RLock()
	v, ok := s.m[key]
	s.mu.RUnlock()
	return v, ok
}

// Set stores value under key, replacing any previous value.
func (m *ShardedMap[K, V]) Set(key K, value V) {
	s := m.shardFor(key)
	s.mu.Lock()
	s.m[key] = value
	s.mu.Unlock()
}

// Delete removes key and reports whether it was present.
func (m *ShardedMap[K, V]) Delete(key K) bool {
	s := m.shardFor(key)
	s.mu.Lock()
	_, ok := s.m[key]
	delete(s.m, key)
	s.mu.Unlock()
	return ok
}

// Update atomically reads, transforms and writes the value for key.
// fn receives the current value (zero value if absent) and whether it
// existed; it returns the new value and keep. If keep is false the key is
// deleted. Update returns the resulting value and whether the key is now
// present.
//
// fn runs while the shard's write lock is held: it must be fast and must not
// call back into the same ShardedMap (that would deadlock if the key hashes
// to the same shard, and sync.RWMutex is not re-entrant).
func (m *ShardedMap[K, V]) Update(key K, fn func(old V, exists bool) (newValue V, keep bool)) (V, bool) {
	s := m.shardFor(key)
	s.mu.Lock()
	defer s.mu.Unlock() // defer: if fn panics, the lock is still released
	old, ok := s.m[key]
	nv, keep := fn(old, ok)
	if !keep {
		delete(s.m, key)
		var zero V
		return zero, false
	}
	s.m[key] = nv
	return nv, true
}

// Len returns the number of entries. Under concurrent writes the result is
// a best-effort count: each shard is counted at a slightly different moment,
// so it is not a consistent snapshot of the whole map.
func (m *ShardedMap[K, V]) Len() int {
	total := 0
	for i := range m.shards {
		s := &m.shards[i]
		s.mu.RLock()
		total += len(s.m)
		s.mu.RUnlock()
	}
	return total
}

// ShardLens returns the number of entries in each shard (useful for spotting
// a hot shard caused by a bad hash or a skewed key space).
func (m *ShardedMap[K, V]) ShardLens() []int {
	out := make([]int, len(m.shards))
	for i := range m.shards {
		s := &m.shards[i]
		s.mu.RLock()
		out[i] = len(s.m)
		s.mu.RUnlock()
	}
	return out
}

// Range calls fn for each entry until fn returns false. It copies one shard
// at a time and calls fn WITHOUT holding any lock, so fn may call Get, Set,
// Delete or Update on this map. Entries changed during Range may or may not
// be observed (per-shard snapshot, not a global one).
func (m *ShardedMap[K, V]) Range(fn func(key K, value V) bool) {
	type kv struct {
		k K
		v V
	}
	var buf []kv
	for i := range m.shards {
		s := &m.shards[i]
		s.mu.RLock()
		buf = buf[:0]
		for k, v := range s.m {
			buf = append(buf, kv{k, v})
		}
		s.mu.RUnlock()
		for _, e := range buf {
			if !fn(e.k, e.v) {
				return
			}
		}
	}
}
