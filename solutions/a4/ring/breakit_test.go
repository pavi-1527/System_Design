//go:build breakit

package ring

// Break-it experiments for A4 Unit 2 (§5–§7). These are measurements, not
// pass/fail tests: they print tables so you can compare schemes. Predict the
// numbers first.
//
// Run: go test -tags breakit -run BreakIt -v ./labs/a4/ring/

import (
	"fmt"
	"sort"
	"testing"
)

// TestBreakIt_ModuloVersusRing: add one node to a cluster of N. What fraction
// of keys has to move under hash(key) mod N versus a ring with 200 virtual
// nodes per node? (Also compare with 1/(N+1), the theoretical minimum.)
func TestBreakIt_ModuloVersusRing(t *testing.T) {
	keys := tKeys(50000)
	t.Logf("%4s %10s %10s %10s", "N", "modulo", "ring", "1/(N+1)")
	for _, n := range []int{2, 3, 4, 8, 16, 32, 64} {
		old, grown := tNames(n), tNames(n+1)
		moved := 0
		for _, k := range keys {
			if ModOwner(old, k, nil) != ModOwner(grown, k, nil) {
				moved++
			}
		}
		before := New(Config{VNodes: 200})
		for _, name := range old {
			_ = before.Add(name, 1)
		}
		after := before.Clone()
		_ = after.Add(grown[n], 1)
		t.Logf("%4d %9.1f%% %9.1f%% %9.1f%%", n, 100*float64(moved)/float64(len(keys)),
			100*MovedFraction(before, after, keys), 100/float64(n+1))
	}
}

// TestBreakIt_VirtualNodesVersusSkew: 10 nodes, 100k keys. How does the load
// of the busiest node (relative to the mean) fall as virtual nodes increase?
// What is the memory cost (number of points on the ring)?
func TestBreakIt_VirtualNodesVersusSkew(t *testing.T) {
	keys := tKeys(100000)
	t.Logf("%8s %8s %10s %10s %10s", "vnodes", "points", "max/mean", "min/mean", "stddev/mean")
	for _, v := range []int{1, 2, 4, 16, 64, 256, 1024, 4096} {
		r := New(Config{VNodes: v})
		for _, name := range tNames(10) {
			_ = r.Add(name, 1)
		}
		s := Summarize(Distribution(r, keys))
		t.Logf("%8d %8d %10.2f %10.2f %10.3f", v, 10*v, s.MaxOverMean, float64(s.Min)/s.Mean, s.StdDev/s.Mean)
	}
}

// TestBreakIt_WhoDonatesKeys: when node 11 joins a 10-node ring, which
// existing nodes give up keys, and how many? With 1 virtual node per node the
// donors are neighbours of the new points; with many, everybody donates a bit
// (the rebalancing load is spread, which is the operational point of vnodes).
func TestBreakIt_WhoDonatesKeys(t *testing.T) {
	keys := tKeys(100000)
	for _, v := range []int{1, 16, 256} {
		before := New(Config{VNodes: v})
		for _, name := range tNames(10) {
			_ = before.Add(name, 1)
		}
		after := before.Clone()
		_ = after.Add("db-new", 1)
		donated := map[string]int{}
		for _, k := range keys {
			a, _ := before.Lookup(k)
			b, _ := after.Lookup(k)
			if a != b {
				donated[a]++
			}
		}
		names := make([]string, 0, len(donated))
		for n := range donated {
			names = append(names, n)
		}
		sort.Strings(names)
		line := ""
		for _, n := range names {
			line += fmt.Sprintf(" %s:%d", n, donated[n])
		}
		t.Logf("vnodes=%-4d donors=%d of 10 moved=%.1f%% |%s", v, len(donated), 100*MovedFraction(before, after, keys), line)
	}
}

// TestBreakIt_BoundedLoad: 10 nodes with only 8 virtual nodes each (a skewed
// ring). How much load does the busiest node shed as epsilon shrinks, and how
// many keys leave their natural owner to pay for it?
func TestBreakIt_BoundedLoad(t *testing.T) {
	keys := tKeys(50000)
	r := New(Config{VNodes: 8})
	for _, name := range tNames(10) {
		_ = r.Add(name, 1)
	}
	t.Logf("%10s %10s %18s", "epsilon", "max/mean", "keys off-owner")
	for _, eps := range []float64{100, 1.0, 0.5, 0.25, 0.1, 0.05, 0} {
		asg, err := AssignBounded(r, keys, eps)
		if err != nil {
			t.Fatal(err)
		}
		counts := map[string]int{}
		off := 0
		for _, k := range keys {
			counts[asg[k]]++
			if owner, _ := r.Lookup(k); owner != asg[k] {
				off++
			}
		}
		t.Logf("%10.2f %10.3f %17.1f%%", eps, Summarize(counts).MaxOverMean, 100*float64(off)/float64(len(keys)))
	}
}
