//go:build breakit

package backpressure

// Break-it experiments for A6 Unit 3 §3. Measurements, not pass/fail tests.
//
// Run: go test -tags breakit -run BreakIt -v ./labs/a6/backpressure/

import (
	"context"
	"testing"
	"time"
)

// TestBreakIt_QueueGrowthVsLatency: one server that finishes 100 req/s, fed at
// different rates for 10 seconds, with and without a bound on the queue.
func TestBreakIt_QueueGrowthVsLatency(t *testing.T) {
	svc := 10 * time.Millisecond
	t.Logf("%-8s %-6s %-10s %9s %10s %10s %9s %9s", "arrivals", "util", "queue", "max queue", "mean wait", "max wait", "dropped", "L=λW ok")
	for _, rate := range []float64{80, 100, 110, 150, 300} {
		for _, cap := range []int{0, 50} {
			r := Simulate(SimConfig{ArrivalRate: rate, ServiceTime: svc, Servers: 1, QueueCap: cap, Duration: 10 * time.Second})
			if r.Served == 0 {
				t.Logf("Simulate returned nothing (is it implemented?)")
				return
			}
			name := "unbounded"
			if cap > 0 {
				name = "cap 50"
			}
			lam := float64(r.Served) / r.End.Seconds()
			ok := r.MeanInSystem/(lam*r.MeanSojourn.Seconds()) > 0.999 && r.MeanInSystem/(lam*r.MeanSojourn.Seconds()) < 1.001
			t.Logf("%5.0f/s  %4.0f%%  %-10s %9d %10v %10v %9d %9v", rate, 100*rate*svc.Seconds(), name, r.MaxQueue,
				r.MeanWait.Round(time.Millisecond), r.MaxWait.Round(time.Millisecond), r.Dropped, ok)
		}
	}
	t.Log("Question: at 110 req/s the server is only 10% over capacity. Why is the unbounded queue's worst wait already 1 second, and what does a client with a 1 s timeout see?")
}

// TestBreakIt_WhatDoesTheConsumerGetUnderOverload: three arrivals per tick, one
// service per tick, for 3000 ticks. How old is the item the consumer takes?
func TestBreakIt_WhatDoesTheConsumerGetUnderOverload(t *testing.T) {
	const ticks = 3000
	type row struct {
		name   string
		policy Policy
		cap    int
	}
	t.Logf("%-28s %10s %10s %10s %10s", "queue", "accepted", "dropped", "mean age", "max age")
	for _, r := range []row{{"unbounded (cap 1e6)", DropNewest, 1_000_000}, {"cap 20, DropNewest", DropNewest, 20}, {"cap 20, DropOldest", DropOldest, 20}} {
		q := NewQueue[int](r.cap, r.policy)
		var sum, max, n int
		for tick := 0; tick < ticks; tick++ {
			for a := 0; a < 3; a++ {
				_ = q.Offer(context.Background(), tick)
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
			v, err := q.Take(ctx)
			cancel()
			if err != nil {
				continue
			}
			age := tick - v
			sum += age
			n++
			if age > max {
				max = age
			}
		}
		st := q.Stats()
		if n == 0 {
			t.Logf("%-28s nothing was served (is Queue implemented?)", r.name)
			continue
		}
		t.Logf("%-28s %10d %10d %10.1f %10d", r.name, st.Accepted, st.Rejected+st.Evicted, float64(sum)/float64(n), max)
	}
	t.Log("Question: the consumer is equally busy in all three rows. Which row would you pick for (a) payment instructions, (b) a live balance widget, and why does 'age' decide it?")
}
