//go:build breakit

package breaker

// Break-it experiments for A7 Unit 2 §1. These are measurements, not
// pass/fail tests: they print tables so you can compare policies.
//
// Run: go test -tags breakit -run BreakIt -v ./labs/a7/breaker/

import (
	"container/heap"
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

// A vendor is healthy except between t=10 s and t=70 s, when it hangs: every
// call takes 2 s and then fails with a timeout. Requests arrive every 10 ms
// (100 req/s). Virtual time: nothing sleeps.
//
// The breaker is optional so the same loop shows both worlds. Outcomes are
// delivered when the call FINISHES (t + latency), which is how a real breaker
// learns about them.

type finish struct {
	at   time.Time
	done func(error)
	err  error
}
type finishHeap []finish

func (h finishHeap) Len() int           { return len(h) }
func (h finishHeap) Less(i, j int) bool { return h[i].at.Before(h[j].at) }
func (h finishHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }
func (h *finishHeap) Push(x any)        { *h = append(*h, x.(finish)) }
func (h *finishHeap) Pop() any {
	old := *h
	x := old[len(old)-1]
	*h = old[:len(old)-1]
	return x
}

type outageResult struct {
	vendorCalls    int
	duringOutage   int
	rejected       int
	maxInFlight    int
	failedByVendor int
	firstSuccess   time.Duration // first successful call after the vendor recovered
	transitions    []string
	tripAt         time.Duration // when the breaker first opened (0 = never)
	callsAtTrip    int           // calls into the dead vendor before that moment
}

func runOutage(withBreaker bool, cfg Config) outageResult {
	start := time.Date(2026, 5, 4, 12, 0, 0, 0, time.UTC)
	now := start
	var res outageResult
	cfg.Now = func() time.Time { return now }
	cfg.OnStateChange = func(from, to State) {
		res.transitions = append(res.transitions, fmt.Sprintf("%5.1fs %s→%s", now.Sub(start).Seconds(), from, to))
		if to == Open && res.tripAt == 0 {
			res.tripAt, res.callsAtTrip = now.Sub(start), res.duringOutage
		}
	}
	b := New(cfg)
	var pending finishHeap
	inFlight := 0
	outageFrom, outageTo := 10*time.Second, 70*time.Second
	errTimeout := errors.New("vendor timeout")

	for step := 0; step < 9000; step++ { // 90 s
		now = start.Add(time.Duration(step) * 10 * time.Millisecond)
		for len(pending) > 0 && !pending[0].at.After(now) {
			f := heap.Pop(&pending).(finish)
			inFlight--
			if f.done != nil {
				f.done(f.err)
			}
		}
		el := now.Sub(start)
		down := el >= outageFrom && el < outageTo
		var done func(error)
		if withBreaker {
			d, err := b.Allow()
			if err != nil {
				res.rejected++
				continue
			}
			done = d
		}
		res.vendorCalls++
		if down {
			res.duringOutage++
			res.failedByVendor++
		}
		inFlight++
		res.maxInFlight = max(res.maxInFlight, inFlight)
		lat, err := 50*time.Millisecond, error(nil)
		if down {
			lat, err = 2*time.Second, errTimeout
		} else if el >= outageTo && res.firstSuccess == 0 {
			res.firstSuccess = el
		}
		heap.Push(&pending, finish{at: now.Add(lat), done: done, err: err})
	}
	return res
}

// TestBreakIt_VendorOutage: a 60 s outage at 100 req/s, with and without a
// breaker. Predict the number of calls that reach the dead vendor in each
// case, when the breaker opens, the peak number of requests in flight
// (Little's law: arrival rate × time in system), and how long after recovery
// the first call succeeds.
func TestBreakIt_VendorOutage(t *testing.T) {
	cfg := Config{Window: 10 * time.Second, Buckets: 10, MinRequests: 20, FailureRatio: 0.5,
		OpenFor: 10 * time.Second, HalfOpenMax: 1, HalfOpenSuccesses: 1}
	none := runOutage(false, cfg)
	with := runOutage(true, cfg)
	t.Logf("%-34s %12s %12s", "(outage: t = 10 s .. 70 s)", "no breaker", "breaker")
	t.Logf("%-34s %12d %12d", "calls into the dead vendor", none.duringOutage, with.duringOutage)
	t.Logf("%-34s %12s %12s", "  of which before the breaker opened", "-", fmt.Sprint(with.callsAtTrip))
	t.Logf("%-34s %12s %12s", "breaker first opened at", "-", with.tripAt)
	t.Logf("%-34s %12d %12d", "rejected locally (fast)", none.rejected, with.rejected)
	t.Logf("%-34s %12d %12d", "peak requests in flight", none.maxInFlight, with.maxInFlight)
	t.Logf("%-34s %12s %12s", "first success after recovery", none.firstSuccess.Round(10*time.Millisecond), with.firstSuccess.Round(10*time.Millisecond))
	t.Logf("breaker transitions:")
	for _, s := range with.transitions {
		t.Logf("  %s", s)
	}
	t.Log("Question: why does the breaker open 6 s after the outage began, not within 20 failures? And why does it re-open several times during the outage?")
}

// TestBreakIt_DetectionSpeed: how quickly does each trip rule notice the
// outage? Failures only arrive 2 s after their calls started (the vendor
// hangs), and a 10 s window is still full of earlier successes.
func TestBreakIt_DetectionSpeed(t *testing.T) {
	t.Logf("%-40s %14s %18s", "rule", "opens at", "calls before it opens")
	for _, c := range []struct {
		name string
		cfg  Config
	}{
		{"ratio 50%, 10 s window, min 20", Config{Window: 10 * time.Second, MinRequests: 20, FailureRatio: 0.5}},
		{"ratio 50%, 2 s window, min 20", Config{Window: 2 * time.Second, MinRequests: 20, FailureRatio: 0.5}},
		{"ratio 20%, 10 s window, min 20", Config{Window: 10 * time.Second, MinRequests: 20, FailureRatio: 0.2}},
		{"20 consecutive failures", Config{ConsecutiveFailures: 20}},
		{"5 consecutive failures", Config{ConsecutiveFailures: 5}},
	} {
		c.cfg.OpenFor, c.cfg.HalfOpenMax, c.cfg.HalfOpenSuccesses = 10*time.Second, 1, 1
		r := runOutage(true, c.cfg)
		t.Logf("%-40s %14s %18d", c.name, r.tripAt, r.callsAtTrip)
	}
	t.Log("Question: what would a 5-consecutive-failures rule do to a healthy dependency that fails 1 call in 20 at random?")
}

// TestBreakIt_OpenForTradeoff: the same outage with different OpenFor values.
// Short values probe more often (more wasted probes); long values keep the
// breaker open after the vendor is back (slower recovery).
func TestBreakIt_OpenForTradeoff(t *testing.T) {
	t.Logf("%-10s %22s %26s", "OpenFor", "probes after first trip", "first success after recovery")
	for _, openFor := range []time.Duration{time.Second, 5 * time.Second, 10 * time.Second, 30 * time.Second, 60 * time.Second} {
		cfg := Config{Window: 10 * time.Second, Buckets: 10, MinRequests: 20, FailureRatio: 0.5,
			OpenFor: openFor, HalfOpenMax: 1, HalfOpenSuccesses: 1}
		r := runOutage(true, cfg)
		t.Logf("%-10s %22d %26s", openFor, r.duringOutage-r.callsAtTrip, r.firstSuccess.Round(10*time.Millisecond))
	}
}

var _ = context.Background
