//go:build breakit

package bulkhead

// Break-it experiments for A7 Unit 3 §1–§2. These are measurements, not
// pass/fail tests: they print tables so you can compare designs.
// TestBreakIt_SharedVersusPerVendor runs ≈ 2 s of real time per
// configuration (millisecond-scale latencies; counts vary by a few percent
// between runs, the shape does not).
//
// Run: go test -tags breakit -run BreakIt -v ./labs/a7/bulkhead/

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"sort"
	"sync"
	"testing"
	"time"
)

type vendorStats struct {
	mu       sync.Mutex
	ok       int
	full     int
	timedOut int
	lat      []time.Duration
}

func (s *vendorStats) record(err error, d time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case err == nil:
		s.ok++
		s.lat = append(s.lat, d)
	case errors.Is(err, ErrFull):
		s.full++
	default:
		s.timedOut++
	}
}

func (s *vendorStats) pct(p float64) time.Duration {
	if len(s.lat) == 0 {
		return 0
	}
	sort.Slice(s.lat, func(i, j int) bool { return s.lat[i] < s.lat[j] })
	return s.lat[int(p*float64(len(s.lat)-1))]
}

// runVendors sends 200 req/s for 2 s; half go to a degraded vendor that
// takes imagingLat ("imaging"), half to a healthy one that takes 5 ms ("fx").
// Every request has a 500 ms client deadline. If honoursCtx is false the
// degraded vendor's adapter ignores its context, like a raw-socket client
// without deadlines (B0 Unit 2 §4). do runs one call through the isolation
// design under test.
func runVendors(imagingLat time.Duration, honoursCtx bool, do func(ctx context.Context, vendor string, work func(context.Context) error) error) (img, fx *vendorStats) {
	img, fx = &vendorStats{}, &vendorStats{}
	r := rand.New(rand.NewPCG(8, 8))
	var wg sync.WaitGroup
	tick := time.NewTicker(5 * time.Millisecond)
	defer tick.Stop()
	end := time.After(2 * time.Second)
loop:
	for {
		select {
		case <-end:
			break loop
		case <-tick.C:
		}
		vendor, lat, st := "fx", 5*time.Millisecond, fx
		if r.Float64() < 0.5 {
			vendor, lat, st = "imaging", imagingLat, img
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
			defer cancel()
			start := time.Now()
			err := do(ctx, vendor, func(ctx context.Context) error {
				if vendor == "imaging" && !honoursCtx {
					time.Sleep(lat)
					return nil
				}
				select {
				case <-time.After(lat):
					return nil
				case <-ctx.Done():
					return ctx.Err()
				}
			})
			if err == nil && ctx.Err() != nil {
				err = ctx.Err() // finished, but too late for the client
			}
			st.record(err, time.Since(start))
		}()
	}
	wg.Wait()
	return img, fx
}

// TestBreakIt_SharedVersusPerVendor: one pool of 20 slots shared by both
// vendors versus a bulkhead per vendor, with the degraded vendor first slow
// (200 ms: demand 100 req/s × 0.2 s = 20 concurrent calls, exactly the whole
// shared pool) and then hung (2 s). Predict the healthy vendor's success
// rate and p99 in each design before you run it.
func TestBreakIt_SharedVersusPerVendor(t *testing.T) {
	scenarios := []struct {
		lat  time.Duration
		coop bool
		desc string
	}{
		{200 * time.Millisecond, true, "imaging is slow (200 ms)"},
		{2 * time.Second, true, "imaging hangs (2 s) but its adapter honours the context"},
		{2 * time.Second, false, "imaging hangs (2 s) and its adapter IGNORES the context"},
	}
	for _, sc := range scenarios {
		shared := New(Config{MaxConcurrent: 20, MaxQueue: 1000})
		perVendor := NewGroup(map[string]Config{"imaging": {MaxConcurrent: 10}, "fx": {MaxConcurrent: 10}})
		perVendorQ := NewGroup(map[string]Config{"imaging": {MaxConcurrent: 10, MaxQueue: 20}, "fx": {MaxConcurrent: 10, MaxQueue: 20}})

		designs := []struct {
			name string
			do   func(ctx context.Context, vendor string, work func(context.Context) error) error
		}{
			{"one shared pool: 20 slots, queue 1000", func(ctx context.Context, _ string, w func(context.Context) error) error { return shared.Do(ctx, w) }},
			{"per vendor: 10 + 10 slots, no queue", func(ctx context.Context, v string, w func(context.Context) error) error {
				return perVendor.Do(ctx, v, w)
			}},
			{"per vendor: 10 + 10 slots, queue 20 each", func(ctx context.Context, v string, w func(context.Context) error) error {
				return perVendorQ.Do(ctx, v, w)
			}},
		}
		t.Logf("\n%s; 200 req/s for 2 s, half to each vendor, 500 ms client deadline", sc.desc)
		t.Logf("%-42s | %-34s | %s", "design", "healthy vendor (fx, 5 ms)", "degraded vendor (imaging)")
		t.Logf("%-42s | %8s %8s %8s %6s | %8s %8s %8s", "", "ok", "p50", "p99", "lost", "ok", "rejected", "timeout")
		for _, d := range designs {
			img, fx := runVendors(sc.lat, sc.coop, d.do)
			fxTotal := fx.ok + fx.full + fx.timedOut
			t.Logf("%-42s | %7.0f%% %8s %8s %6d | %8d %8d %8d", d.name,
				100*float64(fx.ok)/float64(max(fxTotal, 1)), fx.pct(.5).Round(time.Millisecond), fx.pct(.99).Round(time.Millisecond), fx.full+fx.timedOut,
				img.ok, img.full, img.timedOut)
		}
	}
	t.Log("Question: with a queue of 20 per vendor, imaging gets WORSE (more timeouts) than with no queue. Use Little's law on the queue to explain why, and pick a queue size from the 500 ms deadline.")
}

// TestBreakIt_AIMDTracksCapacity: an AIMD limiter in front of a server whose
// capacity changes (40 → 15 → 60 concurrent calls). Virtual time: one round
// per second. Watch the limit chase the capacity, saw-toothing between about
// half and all of it.
func TestBreakIt_AIMDTracksCapacity(t *testing.T) {
	c := tNewClock()
	a := NewAIMD(AIMDConfig{Initial: 5, Min: 1, Max: 1000, Cooldown: time.Second, Now: c.Now})
	capacity := func(round int) int {
		switch {
		case round < 150:
			return 40
		case round < 300:
			return 15
		}
		return 60
	}
	t.Logf("%-8s %-9s %s", "round", "capacity", "limit")
	for round := 0; round < 450; round++ {
		var ds []func(Outcome)
		for i := 0; i < 200; i++ { // 200 callers per round try to get in
			if d, ok := a.TryAcquire(); ok {
				ds = append(ds, d)
			}
		}
		for i, d := range ds {
			if i < capacity(round) {
				d(Success)
			} else {
				d(Dropped)
			}
		}
		c.Advance(time.Second)
		if round%25 == 24 {
			t.Logf("%-8d %-9d %s %.1f", round+1, capacity(round), bar(a.Limit()), a.Limit())
		}
	}
}

func bar(x float64) string {
	n := int(x / 2)
	b := make([]byte, n)
	for i := range b {
		b[i] = '#'
	}
	return fmt.Sprintf("%-32s", b)
}
