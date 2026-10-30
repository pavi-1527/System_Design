//go:build breakit

package raftlite

// Break-it experiments for A5 Unit 3. Measurements, not pass/fail tests.
// Everything is seeded, so the numbers are reproducible.
//
// Run: go test -tags breakit -run BreakIt -v ./labs/a5/raftlite/
//
// (The other break-it, skipping the vote's log check, is the build-tag
// constant in breakit_on.go; see labs/a5/README.md.)

import (
	"math/rand/v2"
	"sort"
	"testing"
)

// TestBreakIt_IdenticalTimeoutsNeverElect: five nodes with the SAME election
// timeout. They all time out on the same tick, all vote for themselves, and
// every vote request is refused ("already voted this term"). Compare with
// randomized timeouts.
func TestBreakIt_IdenticalTimeoutsNeverElect(t *testing.T) {
	same := NewClusterWith(tIDs5, 3, func(ID) func() int { return func() int { return 15 } })
	same.Run(600)
	maxTerm := uint64(0)
	for _, id := range tIDs5 {
		maxTerm = max(maxTerm, same.Node(id).Term())
	}
	t.Logf("identical timeouts (15 ticks), 600 ticks: leader = %q, highest term = %d", same.Leader(), maxTerm)

	ticks := make([]int, 0, 200)
	for seed := uint64(1); seed <= 200; seed++ {
		c := NewCluster(tIDs5, seed)
		c.RunUntil(2000, func() bool { return c.Leader() != "" })
		ticks = append(ticks, c.Ticks())
	}
	sort.Ints(ticks)
	t.Logf("randomized timeouts in [10,20), 200 seeds: first leader after median %d ticks, p99 %d, max %d", ticks[len(ticks)/2], ticks[len(ticks)*99/100], ticks[len(ticks)-1])
}

// TestBreakIt_TimeoutRange: how wide must the randomization window be?
// Time to the first leader (5 nodes, heartbeat 3, one tick of network delay)
// over 300 seeds for different windows of [10, 10+width).
func TestBreakIt_TimeoutRange(t *testing.T) {
	const seeds = 300
	t.Logf("%-6s %9s %8s %8s %8s %10s", "width", "no leader", "median", "p99", "max", "term > 1")
	for _, width := range []int{1, 2, 3, 5, 10, 40} {
		var ticks []int
		var none, later int
		for seed := uint64(1); seed <= seeds; seed++ {
			c := NewClusterWith(tIDs5, 3, func(id ID) func() int {
				var h uint64
				for _, b := range []byte(id) {
					h = h*131 + uint64(b)
				}
				r := rand.New(rand.NewPCG(seed, h))
				return func() int { return 10 + r.IntN(width) }
			})
			if !c.RunUntil(3000, func() bool { return c.Leader() != "" }) {
				none++
				continue
			}
			ticks = append(ticks, c.Ticks())
			if c.Node(c.Leader()).Term() > 1 {
				later++ // the first leader came from a later term: at least one election failed
			}
		}
		if len(ticks) == 0 {
			t.Logf("%-6d %9d %8s %8s %8s %10s", width, none, "-", "-", "-", "-")
			continue
		}
		sort.Ints(ticks)
		t.Logf("%-6d %9d %8d %8d %8d %9.1f%%", width, none, ticks[len(ticks)/2], ticks[len(ticks)*99/100], ticks[len(ticks)-1], 100*float64(later)/float64(len(ticks)))
	}
}
