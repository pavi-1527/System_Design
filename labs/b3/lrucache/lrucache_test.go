package lrucache

import (
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"
)

const testTimeout = 5 * time.Second

func mustNew[K comparable, V any](t *testing.T, capacity int, opts ...Option[K, V]) *Cache[K, V] {
	t.Helper()
	c, err := New[K, V](capacity, opts...)
	if err != nil {
		t.Fatalf("New(%d) returned error %v", capacity, err)
	}
	if c == nil {
		t.Fatal("New returned a nil *Cache with a nil error")
	}
	return c
}

// within fails the test if f does not return within testTimeout, so a deadlock
// or an accidental O(n^2) fails instead of hanging the run.
func within(t *testing.T, what string, f func()) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		f()
	}()
	select {
	case <-done:
	case <-time.After(testTimeout):
		t.Fatalf("%s did not finish within %v (deadlock, or a per-operation cost that grows with the cache size)", what, testTimeout)
	}
}

func wantKeys(t *testing.T, c *Cache[string, int], want ...string) {
	t.Helper()
	got := c.Keys()
	if len(got) == 0 && len(want) == 0 {
		return
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Keys() = %v, want %v (most recently used first)", got, want)
	}
}

func TestStage1_NewRejectsBadCapacity(t *testing.T) {
	for _, n := range []int{0, -1} {
		c, err := New[string, int](n)
		if err != ErrInvalidCapacity {
			t.Errorf("New(%d) error = %v, want ErrInvalidCapacity", n, err)
		}
		if c != nil {
			t.Errorf("New(%d) returned a non-nil cache alongside an error", n)
		}
	}
}

func TestStage1_GetMissOnEmpty(t *testing.T) {
	c := mustNew[string, int](t, 2)
	v, ok := c.Get("nope")
	if ok || v != 0 {
		t.Fatalf("Get on empty cache = (%v, %v), want (0, false)", v, ok)
	}
}

func TestStage1_PutGetRoundTrip(t *testing.T) {
	c := mustNew[string, int](t, 3)
	c.Put("a", 1)
	c.Put("b", 2)
	if v, ok := c.Get("a"); !ok || v != 1 {
		t.Fatalf("Get(a) = (%v, %v), want (1, true)", v, ok)
	}
	if v, ok := c.Get("b"); !ok || v != 2 {
		t.Fatalf("Get(b) = (%v, %v), want (2, true)", v, ok)
	}
	if c.Len() != 2 {
		t.Fatalf("Len = %d, want 2", c.Len())
	}
}

func TestStage1_EvictsLeastRecentlyUsed(t *testing.T) {
	c := mustNew[string, int](t, 3)
	c.Put("a", 1)
	c.Put("b", 2)
	c.Put("c", 3)
	c.Put("d", 4) // a is the oldest
	if _, ok := c.Get("a"); ok {
		t.Fatal("a should have been evicted: it was the least recently used")
	}
	for _, k := range []string{"b", "c", "d"} {
		if _, ok := c.Get(k); !ok {
			t.Fatalf("%s should still be cached", k)
		}
	}
	if c.Len() != 3 {
		t.Fatalf("Len = %d, want 3", c.Len())
	}
}

func TestStage1_GetPromotes(t *testing.T) {
	c := mustNew[string, int](t, 3)
	c.Put("a", 1)
	c.Put("b", 2)
	c.Put("c", 3)
	c.Get("a")    // a becomes MRU; b is now the LRU
	c.Put("d", 4) // evicts b, not a
	if _, ok := c.Get("a"); !ok {
		t.Fatal("a was evicted although Get(a) made it most recently used")
	}
	if _, ok := c.Get("b"); ok {
		t.Fatal("b should have been evicted: it was the least recently used after Get(a)")
	}
}

func TestStage1_MissDoesNotChangeRecencyOrSize(t *testing.T) {
	c := mustNew[string, int](t, 2)
	c.Put("a", 1)
	c.Put("b", 2)
	c.Get("zzz")
	wantKeys(t, c, "b", "a")
	if c.Len() != 2 {
		t.Fatalf("Len = %d after a miss, want 2", c.Len())
	}
}

func TestStage1_PutExistingUpdatesValueAndPromotes(t *testing.T) {
	c := mustNew[string, int](t, 2)
	c.Put("a", 1)
	c.Put("b", 2)
	c.Put("a", 10) // update: a becomes MRU, size stays 2
	if c.Len() != 2 {
		t.Fatalf("Len = %d after updating an existing key, want 2", c.Len())
	}
	c.Put("c", 3) // evicts b
	if v, ok := c.Get("a"); !ok || v != 10 {
		t.Fatalf("Get(a) = (%v, %v), want (10, true)", v, ok)
	}
	if _, ok := c.Get("b"); ok {
		t.Fatal("b should have been evicted, a was refreshed by the update")
	}
}

func TestStage1_UpdatingExistingKeyNeverEvicts(t *testing.T) {
	c := mustNew[string, int](t, 2)
	c.Put("a", 1)
	c.Put("b", 2)
	for i := 0; i < 10; i++ {
		c.Put("a", i)
		c.Put("b", i)
	}
	if c.Len() != 2 {
		t.Fatalf("Len = %d, want 2: overwriting must not evict", c.Len())
	}
}

func TestStage1_CapacityOne(t *testing.T) {
	c := mustNew[string, int](t, 1)
	c.Put("a", 1)
	c.Put("b", 2)
	if _, ok := c.Get("a"); ok {
		t.Fatal("capacity 1: a must be evicted by b")
	}
	if v, ok := c.Get("b"); !ok || v != 2 {
		t.Fatalf("Get(b) = (%v, %v), want (2, true)", v, ok)
	}
	c.Put("b", 3)
	if v, _ := c.Get("b"); v != 3 || c.Len() != 1 {
		t.Fatalf("after overwrite: value %d, Len %d; want 3 and 1", v, c.Len())
	}
}

func TestStage1_Delete(t *testing.T) {
	c := mustNew[string, int](t, 3)
	c.Put("a", 1)
	c.Put("b", 2)
	if !c.Delete("a") {
		t.Fatal("Delete(a) = false, want true")
	}
	if c.Delete("a") {
		t.Fatal("second Delete(a) = true, want false")
	}
	if c.Delete("never") {
		t.Fatal("Delete of an unknown key = true, want false")
	}
	if _, ok := c.Get("a"); ok {
		t.Fatal("a still readable after Delete")
	}
	if c.Len() != 1 {
		t.Fatalf("Len = %d, want 1", c.Len())
	}
	// Freed capacity must be reusable without evicting b.
	c.Put("c", 3)
	c.Put("d", 4)
	if _, ok := c.Get("b"); !ok {
		t.Fatal("b was evicted although Delete(a) had freed a slot")
	}
}

func TestStage1_KeysOrderAndNoPromotion(t *testing.T) {
	c := mustNew[string, int](t, 4)
	wantKeys(t, c)
	c.Put("a", 1)
	c.Put("b", 2)
	c.Put("c", 3)
	wantKeys(t, c, "c", "b", "a")
	c.Get("a")
	wantKeys(t, c, "a", "c", "b")
	_ = c.Keys()
	wantKeys(t, c, "a", "c", "b") // Keys itself must not change recency
	// The returned slice belongs to the caller.
	ks := c.Keys()
	ks[0] = "mutated"
	wantKeys(t, c, "a", "c", "b")
}

func TestStage1_StoresZeroValuesAndPointers(t *testing.T) {
	c := mustNew[string, *int](t, 2)
	c.Put("nilptr", nil)
	if v, ok := c.Get("nilptr"); !ok || v != nil {
		t.Fatalf("Get(nilptr) = (%v, %v), want (nil, true): a stored zero value is still a hit", v, ok)
	}
	type key struct{ a, b int }
	ck := mustNew[key, string](t, 2)
	ck.Put(key{1, 2}, "x")
	if v, ok := ck.Get(key{1, 2}); !ok || v != "x" {
		t.Fatalf("struct key lookup = (%q, %v)", v, ok)
	}
}

func TestStage1_OperationsAreConstantTime(t *testing.T) {
	// A linear scan for the LRU entry (or for the key) does ~2×10^10 steps
	// here and misses the time cap by a wide margin; O(1) finishes in milliseconds.
	const capacity, ops = 100_000, 200_000
	c := mustNew[int, int](t, capacity)
	within(t, fmt.Sprintf("%d Puts and Gets on a %d-entry cache", ops, capacity), func() {
		for i := 0; i < ops; i++ {
			c.Put(i, i)
			c.Get(i - capacity/2)
		}
	})
	if c.Len() != capacity {
		t.Fatalf("Len = %d, want %d", c.Len(), capacity)
	}
}

func TestStage1_ConcurrentUseIsSafeAndBounded(t *testing.T) {
	const capacity, workers, perWorker = 64, 16, 5_000
	c := mustNew[int, int](t, capacity)
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < perWorker; i++ {
				k := (w*31 + i*7) % 200
				switch i % 5 {
				case 0, 1:
					c.Put(k, i)
				case 2, 3:
					if v, ok := c.Get(k); ok && v < 0 {
						panic("impossible value")
					}
				default:
					c.Delete(k)
				}
				if n := c.Len(); n > capacity {
					panic(fmt.Sprintf("Len = %d exceeds capacity %d", n, capacity))
				}
			}
		}(w)
	}
	within(t, "concurrent workload", wg.Wait)
	if n := c.Len(); n > capacity || n != len(c.Keys()) {
		t.Fatalf("Len = %d, len(Keys) = %d, capacity %d: map and list drifted apart", n, len(c.Keys()), capacity)
	}
}

func TestStage1_ConcurrentPutsOfDistinctKeysAreNotLost(t *testing.T) {
	const n = 1_000
	c := mustNew[int, int](t, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			c.Put(i, i*2)
		}(i)
	}
	within(t, "concurrent Puts", wg.Wait)
	if c.Len() != n {
		t.Fatalf("Len = %d, want %d: capacity equals the number of distinct keys, nothing may be evicted or lost", c.Len(), n)
	}
	for i := 0; i < n; i++ {
		if v, ok := c.Get(i); !ok || v != i*2 {
			t.Fatalf("Get(%d) = (%v, %v), want (%d, true)", i, v, ok, i*2)
		}
	}
}
