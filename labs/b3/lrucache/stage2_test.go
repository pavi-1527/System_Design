package lrucache

import (
	"sync"
	"testing"
	"time"
)

// fakeClock is a manually advanced clock, safe for concurrent use.
type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{now: time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)}
}

func (f *fakeClock) Now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.now
}

func (f *fakeClock) Advance(d time.Duration) {
	f.mu.Lock()
	f.now = f.now.Add(d)
	f.mu.Unlock()
}

func ttlCache(t *testing.T, capacity int, ttl time.Duration) (*Cache[string, int], *fakeClock) {
	t.Helper()
	clk := newFakeClock()
	c := mustNew(t, capacity,
		WithClock[string, int](clk),
		WithDefaultTTL[string, int](ttl),
	)
	return c, clk
}

func TestStage2_EntryExpiresAtExactlyTTL(t *testing.T) {
	c, clk := ttlCache(t, 4, 10*time.Second)
	c.Put("a", 1)
	clk.Advance(10*time.Second - time.Nanosecond)
	if _, ok := c.Get("a"); !ok {
		t.Fatal("a should still be live 1ns before its TTL elapses")
	}
	clk.Advance(time.Nanosecond)
	if _, ok := c.Get("a"); ok {
		t.Fatal("a should be expired at exactly putTime+ttl (expired means now >= expiresAt)")
	}
}

func TestStage2_GetOfExpiredEntryRemovesIt(t *testing.T) {
	c, clk := ttlCache(t, 4, time.Second)
	c.Put("a", 1)
	c.Put("b", 2)
	clk.Advance(2 * time.Second)
	if c.Len() != 2 {
		t.Fatalf("Len = %d before any access, want 2: expiry is lazy, Len counts stored entries", c.Len())
	}
	if _, ok := c.Get("a"); ok {
		t.Fatal("expired a returned as a hit")
	}
	if c.Len() != 1 {
		t.Fatalf("Len = %d after Get of expired a, want 1: the expired entry must be removed", c.Len())
	}
	wantKeys(t, c, "b")
}

func TestStage2_ReadsDoNotExtendTTL(t *testing.T) {
	c, clk := ttlCache(t, 4, 10*time.Second)
	c.Put("a", 1)
	for i := 0; i < 4; i++ {
		clk.Advance(2 * time.Second)
		if _, ok := c.Get("a"); !ok {
			t.Fatalf("a expired too early at +%ds", (i+1)*2)
		}
	}
	clk.Advance(2 * time.Second) // +10s since the Put, despite four reads
	if _, ok := c.Get("a"); ok {
		t.Fatal("a is still live 10s after Put: reads must not slide the TTL")
	}
}

func TestStage2_PutRestartsTTL(t *testing.T) {
	c, clk := ttlCache(t, 4, 10*time.Second)
	c.Put("a", 1)
	clk.Advance(8 * time.Second)
	c.Put("a", 2) // new value, new 10s
	clk.Advance(8 * time.Second)
	if v, ok := c.Get("a"); !ok || v != 2 {
		t.Fatalf("Get(a) = (%v, %v), want (2, true): re-putting must restart the TTL", v, ok)
	}
}

func TestStage2_PutWithTTLOverridesDefault(t *testing.T) {
	c, clk := ttlCache(t, 4, time.Minute)
	c.PutWithTTL("short", 1, time.Second)
	c.PutWithTTL("dflt", 2, 0) // <= 0 means default
	c.PutWithTTL("neg", 3, -1) // also default
	c.PutWithTTL("long", 4, 2*time.Hour)
	clk.Advance(2 * time.Second)
	if _, ok := c.Get("short"); ok {
		t.Fatal("short (1s TTL) should be expired")
	}
	for _, k := range []string{"dflt", "neg", "long"} {
		if _, ok := c.Get(k); !ok {
			t.Fatalf("%s should be live after 2s", k)
		}
	}
	clk.Advance(2 * time.Minute)
	for _, k := range []string{"dflt", "neg"} {
		if _, ok := c.Get(k); ok {
			t.Fatalf("%s should have expired with the default TTL of 1m", k)
		}
	}
	if _, ok := c.Get("long"); !ok {
		t.Fatal("long (2h TTL) should still be live")
	}
}

func TestStage2_ZeroDefaultTTLMeansNeverExpires(t *testing.T) {
	clk := newFakeClock()
	c := mustNew(t, 2, WithClock[string, int](clk))
	c.Put("a", 1)
	c.PutWithTTL("b", 2, time.Second)
	clk.Advance(1000 * time.Hour)
	if _, ok := c.Get("a"); !ok {
		t.Fatal("a has no TTL and must never expire")
	}
	if _, ok := c.Get("b"); ok {
		t.Fatal("b had a 1s TTL and must be expired")
	}
}

func TestStage2_ExpiredGetDoesNotPromoteAnything(t *testing.T) {
	c, clk := ttlCache(t, 3, time.Hour)
	c.Put("a", 1)
	c.PutWithTTL("b", 2, time.Second)
	c.Put("c", 3)
	clk.Advance(time.Minute)
	c.Get("b") // expired: removed, must not be re-inserted
	wantKeys(t, c, "c", "a")
}

func TestStage2_RemoveExpired(t *testing.T) {
	c, clk := ttlCache(t, 10, time.Hour)
	for i, k := range []string{"a", "b", "c", "d"} {
		c.PutWithTTL(k, i, time.Duration(i+1)*time.Second) // 1s,2s,3s,4s
	}
	c.Put("forever", 9)
	if n := c.RemoveExpired(); n != 0 {
		t.Fatalf("RemoveExpired = %d before anything expired, want 0", n)
	}
	clk.Advance(2500 * time.Millisecond) // a and b expired
	if n := c.RemoveExpired(); n != 2 {
		t.Fatalf("RemoveExpired = %d, want 2 (a and b)", n)
	}
	if c.Len() != 3 {
		t.Fatalf("Len = %d, want 3", c.Len())
	}
	if n := c.RemoveExpired(); n != 0 {
		t.Fatalf("second RemoveExpired = %d, want 0", n)
	}
}

func TestStage2_ExpiredEntriesStillObeyCapacity(t *testing.T) {
	c, clk := ttlCache(t, 2, time.Second)
	c.Put("a", 1)
	c.Put("b", 2)
	clk.Advance(time.Minute)
	c.Put("c", 3)
	c.Put("d", 4)
	if c.Len() > 2 {
		t.Fatalf("Len = %d, capacity is 2", c.Len())
	}
	if v, ok := c.Get("d"); !ok || v != 4 {
		t.Fatalf("Get(d) = (%v, %v), want (4, true)", v, ok)
	}
}

func TestStage2_ConcurrentWithTTL(t *testing.T) {
	c, clk := ttlCache(t, 32, 50*time.Millisecond)
	var wg sync.WaitGroup
	for w := 0; w < 8; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < 3_000; i++ {
				k := string(rune('a' + (w+i)%40))
				switch i % 4 {
				case 0:
					c.Put(k, i)
				case 1:
					c.PutWithTTL(k, i, time.Duration(i%7)*time.Millisecond)
				case 2:
					c.Get(k)
				default:
					clk.Advance(time.Millisecond)
					c.RemoveExpired()
				}
			}
		}(w)
	}
	within(t, "concurrent TTL workload", wg.Wait)
	if c.Len() > 32 {
		t.Fatalf("Len = %d exceeds capacity 32", c.Len())
	}
}
