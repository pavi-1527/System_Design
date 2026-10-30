//go:build breakit

package quorum

// Break-it experiments for A5 Unit 1 §3–§5. These are measurements, not
// pass/fail tests: they print tables so you can compare configurations. All
// randomness comes from a fixed seed, so the numbers are reproducible.
//
// Run: go test -tags breakit -run BreakIt -v ./labs/a5/quorum/

import (
	"fmt"
	"math/rand/v2"
	"testing"
	"time"
)

// TestBreakIt_StaleAndPartialTable: N=3. Each replica independently fails to
// apply a write synchronously (replication lag) with probability 30%. For each
// (R, W) we run 20,000 trials of "write v2 over v1, then read".
//
//	ok writes    : share of writes that got >= W acknowledgements
//	stale|ok     : share of reads that return v1 after an ACKNOWLEDGED write
//	visible|fail : share of reads that return v2 after a write that was REJECTED
func TestBreakIt_StaleAndPartialTable(t *testing.T) {
	const trials, pLag = 20000, 0.30
	ids := []string{"A", "B", "C"}
	t.Logf("N=3, each replica lags with p=%.2f, %d trials per row", pLag, trials)
	t.Logf("%-5s %-5s %-5s %10s %12s %14s", "R", "W", "R+W", "ok writes", "stale|ok", "visible|fail")
	for _, rw := range [][2]int{{1, 1}, {2, 1}, {1, 2}, {2, 2}, {3, 1}, {1, 3}, {2, 3}} {
		r, w := rw[0], rw[1]
		rng := rand.New(rand.NewPCG(1, 2))
		var ok, okReads, stale, failReads, visible int
		for i := 0; i < trials; i++ {
			c, sim := tCluster(t, Config{N: 3, R: r, W: w}, ids...)
			ctx := tPut(t, c, "c1", "k", "v1", nil)
			for _, id := range ids {
				sim.SetLag(id, rng.Float64() < pLag)
			}
			_, err := c.Put("c1", "k", "v2", ctx)
			sim.Heal()
			for j, p := range rng.Perm(3) { // a random replica answers first
				sim.SetLatency(ids[p], time.Duration(j+1)*time.Millisecond)
			}
			res, rerr := c.Get("k")
			if rerr != nil {
				t.Fatalf("read failed: %v", rerr)
			}
			isV2 := len(res.Values()) == 1 && res.Values()[0] == "v2"
			if err == nil {
				ok++
				okReads++
				if !isV2 {
					stale++
				}
			} else {
				failReads++
				if isV2 {
					visible++
				}
			}
		}
		pct := func(a, b int) string {
			if b == 0 {
				return "n/a"
			}
			return fmt.Sprintf("%.1f%%", 100*float64(a)/float64(b))
		}
		t.Logf("%-5d %-5d %-5d %10s %12s %14s", r, w, r+w, pct(ok, trials), pct(stale, okReads), pct(visible, failReads))
	}
}

// TestBreakIt_SloppyQuorumTable: 5 nodes, N=3, W=2, R=2. Each node is down
// independently with probability 30% at write time (everyone is back at read
// time, but hints have not been delivered yet).
//
//	ok writes   : share of writes accepted
//	miss|ok     : share of reads (R=2, random pair from the preference list) that
//	              do NOT return the acknowledged write, before handoff
//	miss after  : same, after DeliverHints
func TestBreakIt_SloppyQuorumTable(t *testing.T) {
	const trials, pDown = 20000, 0.30
	ids := []string{"A", "B", "C", "D", "E"}
	t.Logf("5 nodes, N=3 R=2 W=2, each node down with p=%.2f at write time, %d trials per row", pDown, trials)
	t.Logf("%-8s %10s %10s %12s", "mode", "ok writes", "miss|ok", "miss after")
	for _, sloppy := range []bool{false, true} {
		rng := rand.New(rand.NewPCG(3, 4))
		var ok, miss, missAfter int
		for i := 0; i < trials; i++ {
			c, sim := tCluster(t, Config{N: 3, R: 2, W: 2, Sloppy: sloppy}, ids...)
			for _, id := range ids {
				sim.SetDown(id, rng.Float64() < pDown)
			}
			_, err := c.Put("c1", "k", "v", nil)
			if err != nil {
				continue
			}
			ok++
			sim.Heal()
			order := rng.Perm(3)
			for j, p := range order {
				sim.SetLatency(ids[p], time.Duration(j+1)*time.Millisecond)
			}
			res, _ := c.Get("k")
			if len(res.Versions) == 0 {
				miss++
			}
			c.DeliverHints()
			res, _ = c.Get("k")
			if len(res.Versions) == 0 {
				missAfter++
			}
		}
		mode := "strict"
		if sloppy {
			mode = "sloppy"
		}
		t.Logf("%-8s %9.1f%% %9.1f%% %11.1f%%", mode, 100*float64(ok)/trials, 100*float64(miss)/float64(max(ok, 1)), 100*float64(missAfter)/float64(max(ok, 1)))
	}
}
