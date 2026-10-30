package safemap

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// runWithTimeout runs f in a goroutine and fails the test if it does not
// return within d (so a deadlocked implementation fails instead of hanging).
func runWithTimeout(t *testing.T, d time.Duration, what string, f func()) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		f()
	}()
	select {
	case <-done:
	case <-time.After(d):
		t.Fatalf("%s did not finish within %v: likely a deadlock (a lock held while calling back into the map, or a lock never released)", what, d)
	}
}

func TestNew_RoundsShardsUpToPowerOfTwo(t *testing.T) {
	cases := []struct{ in, want int }{{1, 1}, {2, 2}, {3, 4}, {10, 16}, {64, 64}, {0, DefaultShards}, {-5, DefaultShards}}
	for _, c := range cases {
		m := New[string, int](c.in, HashString)
		if got := m.Shards(); got != c.want {
			t.Errorf("New(%d).Shards() = %d, want %d (power of two lets shardFor use hash&mask instead of a modulo)", c.in, got, c.want)
		}
	}
}

func TestBasic_SetGetDeleteLen(t *testing.T) {
	m := New[string, int](8, HashString)
	if _, ok := m.Get("missing"); ok {
		t.Fatal("Get on an empty map reported ok=true")
	}
	m.Set("a", 1)
	m.Set("b", 2)
	m.Set("a", 3)
	if v, ok := m.Get("a"); !ok || v != 3 {
		t.Fatalf("Get(a) = %d,%v; want 3,true (Set must overwrite)", v, ok)
	}
	if n := m.Len(); n != 2 {
		t.Fatalf("Len() = %d, want 2", n)
	}
	if !m.Delete("a") {
		t.Fatal("Delete(a) = false, want true for a present key")
	}
	if m.Delete("a") {
		t.Fatal("second Delete(a) = true, want false for an absent key")
	}
	if _, ok := m.Get("a"); ok {
		t.Fatal("Get(a) after Delete still ok=true")
	}
	if n := m.Len(); n != 1 {
		t.Fatalf("Len() = %d after delete, want 1", n)
	}
}

func TestDefaultHasher_IntAndStructKeys(t *testing.T) {
	type acct struct {
		Tenant string
		ID     int
	}
	m := New[acct, string](16, nil)
	for i := 0; i < 100; i++ {
		m.Set(acct{"cu-1", i}, fmt.Sprint(i))
	}
	for i := 0; i < 100; i++ {
		if v, ok := m.Get(acct{"cu-1", i}); !ok || v != fmt.Sprint(i) {
			t.Fatalf("Get(%d) = %q,%v; equal struct keys must hash to the same shard", i, v, ok)
		}
	}
	mi := New[int, int](4, nil)
	mi.Set(7, 49)
	if v, ok := mi.Get(7); !ok || v != 49 {
		t.Fatalf("int key with nil hasher: Get(7) = %d,%v", v, ok)
	}
}

func TestShardLens_KeysSpreadAcrossShards(t *testing.T) {
	m := New[int, int](16, HashInteger[int])
	const n = 16000
	for i := 0; i < n; i++ {
		m.Set(i, i)
	}
	lens := m.ShardLens()
	if len(lens) != 16 {
		t.Fatalf("ShardLens returned %d entries, want 16", len(lens))
	}
	sum := 0
	for i, l := range lens {
		sum += l
		// Expect ≈ 1000 per shard; allow a very generous band so this is not flaky.
		if l < n/16/2 || l > n/16*2 {
			t.Errorf("shard %d holds %d keys (expected ≈ %d): keys are not spread, so one lock is hot", i, l, n/16)
		}
	}
	if sum != n {
		t.Fatalf("ShardLens sums to %d, want %d", sum, n)
	}
}

func TestUpdate_InsertModifyDelete(t *testing.T) {
	m := New[string, int](4, HashString)
	v, ok := m.Update("k", func(old int, exists bool) (int, bool) {
		if exists {
			t.Error("fn saw exists=true for an absent key")
		}
		return old + 10, true
	})
	if !ok || v != 10 {
		t.Fatalf("Update insert = %d,%v; want 10,true", v, ok)
	}
	v, ok = m.Update("k", func(old int, exists bool) (int, bool) {
		if !exists || old != 10 {
			t.Errorf("fn saw old=%d exists=%v, want 10,true", old, exists)
		}
		return old * 2, true
	})
	if !ok || v != 20 {
		t.Fatalf("Update modify = %d,%v; want 20,true", v, ok)
	}
	_, ok = m.Update("k", func(old int, exists bool) (int, bool) { return 0, false })
	if ok {
		t.Fatal("Update with keep=false returned ok=true")
	}
	if _, present := m.Get("k"); present {
		t.Fatal("key still present after Update returned keep=false")
	}
}

// The core property: concurrent read-modify-write must not lose updates.
// A Get-then-Set implementation passes single-threaded tests and loses
// increments here (and -race flags nothing, because each call is locked!).
func TestUpdate_ConcurrentIncrementsAreNotLost(t *testing.T) {
	m := New[string, int](8, HashString)
	const goroutines, perG, keys = 32, 500, 10
	runWithTimeout(t, 30*time.Second, "concurrent Update", func() {
		var wg sync.WaitGroup
		for g := 0; g < goroutines; g++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for i := 0; i < perG; i++ {
					key := fmt.Sprintf("acct-%d", (g+i)%keys)
					m.Update(key, func(old int, _ bool) (int, bool) { return old + 1, true })
				}
			}()
		}
		wg.Wait()
	})
	total := 0
	for k := 0; k < keys; k++ {
		v, _ := m.Get(fmt.Sprintf("acct-%d", k))
		total += v
	}
	if want := goroutines * perG; total != want {
		t.Fatalf("sum of counters = %d, want %d: %d increments were lost (read-modify-write is not atomic)", total, want, want-total)
	}
}

func TestConcurrent_MixedOperationsUnderRace(t *testing.T) {
	m := New[int, int](16, HashInteger[int])
	var stop atomic.Bool
	runWithTimeout(t, 30*time.Second, "mixed concurrent operations", func() {
		var wg sync.WaitGroup
		for w := 0; w < 8; w++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for i := 0; i < 2000; i++ {
					k := (w*7919 + i) % 512
					switch i % 5 {
					case 0:
						m.Set(k, i)
					case 1:
						m.Get(k)
					case 2:
						m.Delete(k)
					case 3:
						m.Update(k, func(old int, _ bool) (int, bool) { return old + 1, true })
					case 4:
						m.Len()
					}
				}
			}()
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			for !stop.Load() {
				m.Range(func(int, int) bool { return true })
				m.ShardLens()
			}
		}()
		time.Sleep(10 * time.Millisecond)
		stop.Store(true)
		wg.Wait()
	})
	if l := m.Len(); l < 0 || l > 512 {
		t.Fatalf("Len() = %d, must be within [0, 512] distinct keys", l)
	}
}

func TestRange_VisitsAllAndStopsEarly(t *testing.T) {
	m := New[int, int](8, HashInteger[int])
	for i := 0; i < 100; i++ {
		m.Set(i, i*i)
	}
	seen := map[int]int{}
	m.Range(func(k, v int) bool {
		seen[k] = v
		return true
	})
	if len(seen) != 100 {
		t.Fatalf("Range visited %d keys, want 100", len(seen))
	}
	for k, v := range seen {
		if v != k*k {
			t.Fatalf("Range gave %d=>%d, want %d", k, v, k*k)
		}
	}
	calls := 0
	m.Range(func(int, int) bool {
		calls++
		return calls < 5
	})
	if calls != 5 {
		t.Fatalf("Range called fn %d times after it returned false on call 5; want exactly 5", calls)
	}
}

// Calling user code while holding a lock is the classic re-entrancy deadlock.
func TestRange_CallbackMayMutateMapWithoutDeadlock(t *testing.T) {
	m := New[int, int](4, HashInteger[int])
	for i := 0; i < 50; i++ {
		m.Set(i, i)
	}
	runWithTimeout(t, 5*time.Second, "Range with a callback that calls Set/Delete", func() {
		m.Range(func(k, v int) bool {
			m.Set(k, v+1000)
			m.Delete(k + 10000)
			return true
		})
	})
	for i := 0; i < 50; i++ {
		if v, _ := m.Get(i); v != i+1000 {
			t.Fatalf("Get(%d) = %d after Range mutated it, want %d", i, v, i+1000)
		}
	}
}

// ---------------------------------------------------------------------------
// STRETCH: benchmarks. Run with
//   go test -run '^$' -bench . -benchtime 1x ./labs/b0/safemap/   (smoke)
//   go test -run '^$' -bench . -cpu 1,4,8 ./solutions/b0/safemap/
// Compare a single-mutex map, sync.Map and the sharded map under a 90% read /
// 10% write mix over 10k keys. Write down your prediction first.

type mutexMap struct {
	mu sync.RWMutex
	m  map[int]int
}

func (mm *mutexMap) get(k int) (int, bool) {
	mm.mu.RLock()
	v, ok := mm.m[k]
	mm.mu.RUnlock()
	return v, ok
}

func (mm *mutexMap) set(k, v int) {
	mm.mu.Lock()
	mm.m[k] = v
	mm.mu.Unlock()
}

const benchKeys = 10000

// benchStart gives each RunParallel goroutine a different starting key, so
// goroutines do not walk the key space in lockstep and collide on one shard.
var benchGoroutine atomic.Int64

func benchStart() int { return int(benchGoroutine.Add(1)) * 104729 }

func BenchmarkMixed90Read_SingleRWMutex(b *testing.B) {
	mm := &mutexMap{m: make(map[int]int)}
	for i := 0; i < benchKeys; i++ {
		mm.set(i, i)
	}
	b.RunParallel(func(pb *testing.PB) {
		i := benchStart()
		for pb.Next() {
			k := (i * 7919) % benchKeys
			if i%10 == 0 {
				mm.set(k, i)
			} else {
				mm.get(k)
			}
			i++
		}
	})
}

func BenchmarkMixed90Read_SyncMap(b *testing.B) {
	var sm sync.Map
	for i := 0; i < benchKeys; i++ {
		sm.Store(i, i)
	}
	b.RunParallel(func(pb *testing.PB) {
		i := benchStart()
		for pb.Next() {
			k := (i * 7919) % benchKeys
			if i%10 == 0 {
				sm.Store(k, i)
			} else {
				sm.Load(k)
			}
			i++
		}
	})
}

func BenchmarkMixed90Read_Sharded32(b *testing.B) {
	m := New[int, int](32, HashInteger[int])
	for i := 0; i < benchKeys; i++ {
		m.Set(i, i)
	}
	b.RunParallel(func(pb *testing.PB) {
		i := benchStart()
		for pb.Next() {
			k := (i * 7919) % benchKeys
			if i%10 == 0 {
				m.Set(k, i)
			} else {
				m.Get(k)
			}
			i++
		}
	})
}

// work simulates a non-trivial critical section (≈ tens of ns), e.g.
// recomputing a per-account rolling counter. Sharding pays off when callers
// actually queue on a held lock, not when the critical section is a single
// map lookup.
func work(n int) int {
	x := 0
	for j := 0; j < n; j++ {
		x += j*j ^ x
	}
	return x
}

func BenchmarkUpdateWithWork_SingleMutex(b *testing.B) {
	var mu sync.Mutex
	mm := make(map[int]int)
	b.RunParallel(func(pb *testing.PB) {
		i := benchStart()
		for pb.Next() {
			k := (i * 7919) % benchKeys
			mu.Lock()
			mm[k] += work(100)
			mu.Unlock()
			i++
		}
	})
}

func BenchmarkUpdateWithWork_Sharded32(b *testing.B) {
	m := New[int, int](32, HashInteger[int])
	b.RunParallel(func(pb *testing.PB) {
		i := benchStart()
		for pb.Next() {
			k := (i * 7919) % benchKeys
			m.Update(k, func(old int, _ bool) (int, bool) { return old + work(100), true })
			i++
		}
	})
}
