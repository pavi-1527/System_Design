//go:build breakit

package ratelimit

// Break-it experiments for A7 Unit 2 §2–§3. These are measurements, not
// pass/fail tests: they print tables so you can compare algorithms.
//
// Run: go test -tags breakit -run BreakIt -v ./labs/a7/ratelimit/

import (
	"fmt"
	"math/rand/v2"
	"runtime"
	"sort"
	"testing"
	"time"
)

func breakitClock() *tClock { return tNewClock() }

// TestBreakIt_BoundaryBurst: a client that knows the window boundaries sends
// 100 requests 10 ms before a boundary and 100 requests 10 ms after it
// (limit: 100 per second). How many does each limiter admit in those 20 ms?
// Predict each row first.
func TestBreakIt_BoundaryBurst(t *testing.T) {
	mk := []struct {
		name string
		mk   func(c *tClock) Limiter
	}{
		{"fixed window", func(c *tClock) Limiter {
			return NewFixedWindow(WindowConfig{Limit: 100, Window: time.Second, Now: c.Now})
		}},
		{"sliding window log", func(c *tClock) Limiter {
			return NewSlidingWindowLog(WindowConfig{Limit: 100, Window: time.Second, Now: c.Now})
		}},
		{"sliding window counter", func(c *tClock) Limiter {
			return NewSlidingWindowCounter(WindowConfig{Limit: 100, Window: time.Second, Now: c.Now})
		}},
		{"token bucket rate 100 burst 100", func(c *tClock) Limiter {
			return NewTokenBucket(TokenBucketConfig{Rate: 100, Burst: 100, Now: c.Now})
		}},
		{"token bucket rate 100 burst 10", func(c *tClock) Limiter {
			return NewTokenBucket(TokenBucketConfig{Rate: 100, Burst: 10, Now: c.Now})
		}},
	}
	t.Logf("%-34s %s", "limiter (limit 100/s)", "admitted in the 20 ms around the boundary")
	for _, m := range mk {
		c := breakitClock()
		l := m.mk(c)
		admitted := 0
		c.Advance(990 * time.Millisecond)
		for i := 0; i < 100; i++ {
			if l.Allow("k").Allowed {
				admitted++
			}
		}
		c.Advance(20 * time.Millisecond)
		for i := 0; i < 100; i++ {
			if l.Allow("k").Allowed {
				admitted++
			}
		}
		t.Logf("%-34s %d", m.name, admitted)
	}
	t.Log("Question: the token bucket with burst 100 also admits ≈ 2× the rate in a short span. Is that a bug? What does 'burst' mean for the dependency you are protecting?")
}

// maxInAnySecond returns the largest number of admitted requests inside any
// half-open span (t − 1 s, t].
func maxInAnySecond(ts []time.Duration) int {
	best, lo := 0, 0
	for hi := range ts {
		for ts[hi]-ts[lo] >= time.Second {
			lo++
		}
		best = max(best, hi-lo+1)
	}
	return best
}

// TestBreakIt_CounterVersusLog: the sliding-window counter is an
// approximation. Feed the exact log and the counter the same traffic
// (limit 100 per second) and compare what each admits in total and in the
// worst one-second span. Three traffic shapes: steady Poisson at 150/s;
// bursty (every ~0.9 s a burst of 60 requests inside 30 ms); and a nasty
// adversary that fires 100 requests at the end of one window and then again
// at the middle of the next.
func TestBreakIt_CounterVersusLog(t *testing.T) {
	shapes := []struct {
		name string
		gen  func(r *rand.Rand) []time.Duration // attempt times
	}{
		{"steady Poisson 150/s", func(r *rand.Rand) []time.Duration {
			var out []time.Duration
			for at := 0.0; at < 60; {
				at += r.ExpFloat64() / 150
				out = append(out, time.Duration(at*float64(time.Second)))
			}
			return out
		}},
		{"bursts of 60 every ~0.9 s", func(r *rand.Rand) []time.Duration {
			var out []time.Duration
			for at := 0.0; at < 60; at += 0.7 + 0.4*r.Float64() {
				for i := 0; i < 60; i++ {
					out = append(out, time.Duration((at+0.03*r.Float64())*float64(time.Second)))
				}
			}
			sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
			return out
		}},
		{"adversary: 100 at 0.99 s, 100 at 1.5 s", func(r *rand.Rand) []time.Duration {
			var out []time.Duration
			for w := 0; w < 20; w++ {
				base := time.Duration(w) * 2 * time.Second
				for i := 0; i < 100; i++ {
					out = append(out, base+990*time.Millisecond+time.Duration(i)*time.Microsecond)
				}
				for i := 0; i < 100; i++ {
					out = append(out, base+1500*time.Millisecond+time.Duration(i)*time.Microsecond)
				}
			}
			return out
		}},
	}
	t.Logf("%-40s %-10s %10s %20s", "traffic", "limiter", "admitted", "worst 1 s span")
	for _, s := range shapes {
		attempts := s.gen(rand.New(rand.NewPCG(3, 4)))
		for _, lim := range []string{"log", "counter"} {
			c := breakitClock()
			cfg := WindowConfig{Limit: 100, Window: time.Second, Now: c.Now}
			var l Limiter = NewSlidingWindowLog(cfg)
			if lim == "counter" {
				l = NewSlidingWindowCounter(cfg)
			}
			origin := c.Now()
			var admitted []time.Duration
			for _, at := range attempts {
				c.mu.Lock()
				c.t = origin.Add(at)
				c.mu.Unlock()
				if l.Allow("k").Allowed {
					admitted = append(admitted, at)
				}
			}
			t.Logf("%-40s %-10s %10d %20d", s.name, lim, len(admitted), maxInAnySecond(admitted))
		}
	}
	t.Log("Question: for which of these shapes is the counter's answer 'wrong', by how much, and would you accept that error to save O(limit) memory per key?")
}

// TestBreakIt_MemoryPerKey: how much memory does each limiter retain?
//
// Part 1: half a million distinct keys, one request each (an attacker
// rotating API keys, or a tenant ID per request): what does the key cap buy?
// Part 2: ten thousand keys that each made 100 requests inside the window
// (limit 100): what does exactness cost per ACTIVE key?
func TestBreakIt_MemoryPerKey(t *testing.T) {
	heap := func() uint64 {
		runtime.GC()
		runtime.GC()
		var m runtime.MemStats
		runtime.ReadMemStats(&m)
		return m.HeapAlloc
	}
	measure := func(name string, keys, perKey int, mk func() (Limiter, func() int)) {
		before := heap()
		l, length := mk()
		for i := 0; i < keys; i++ {
			k := fmt.Sprintf("tenant-%d", i)
			for j := 0; j < perKey; j++ {
				l.Allow(k)
			}
		}
		after := heap()
		n := length()
		runtime.KeepAlive(l)
		per := 0.0
		if n > 0 {
			per = float64(after-before) / float64(n)
		}
		t.Logf("%-42s tracked %7d keys, %6.1f MB retained, ≈ %5.0f B/key", name, n, float64(after-before)/1e6, per)
	}
	c := breakitClock()

	t.Log("part 1: 500,000 distinct keys, 1 request each")
	measure("fixed window (no cap)", 500_000, 1, func() (Limiter, func() int) {
		l := NewFixedWindow(WindowConfig{Limit: 100, Now: c.Now})
		return l, l.Len
	})
	measure("token bucket, MaxKeys 50,000", 500_000, 1, func() (Limiter, func() int) {
		l := NewTokenBucket(TokenBucketConfig{Rate: 100, Burst: 100, MaxKeys: 50_000, Now: c.Now})
		return l, l.Len
	})
	measure("sliding counter, MaxKeys 50,000", 500_000, 1, func() (Limiter, func() int) {
		l := NewSlidingWindowCounter(WindowConfig{Limit: 100, MaxKeys: 50_000, Now: c.Now})
		return l, l.Len
	})

	t.Log("part 2: 10,000 active keys, 100 requests each inside the window (limit 100)")
	measure("token bucket", 10_000, 100, func() (Limiter, func() int) {
		l := NewTokenBucket(TokenBucketConfig{Rate: 100, Burst: 100, Now: c.Now})
		return l, l.Len
	})
	measure("sliding counter", 10_000, 100, func() (Limiter, func() int) {
		l := NewSlidingWindowCounter(WindowConfig{Limit: 100, Now: c.Now})
		return l, l.Len
	})
	measure("sliding log", 10_000, 100, func() (Limiter, func() int) {
		l := NewSlidingWindowLog(WindowConfig{Limit: 100, Now: c.Now})
		return l, l.Len
	})
	t.Log("Question: with MaxKeys set, an attacker who rotates keys gets a fresh bucket each time. What does the cap protect, and what protects the dependency from key rotation?")
}
