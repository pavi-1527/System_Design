//go:build breakit

package antientropy

import (
	"context"
	"fmt"
	"math/rand"
	"testing"
)

// TestBreakIt_DepthSweep repairs two replicas of 100,000 keys that differ in
// 20 of them, at tree depths 0 to 16. Predict the shape of the Comparisons
// and Sent columns before you run it:
//
//	go test -tags breakit -run BreakIt_DepthSweep -v ./labs/c4/antientropy/
func TestBreakIt_DepthSweep(t *testing.T) {
	const n, diffs = 100000, 20
	t.Logf("%5s %8s %12s %12s %8s %10s", "depth", "leaves", "tree bytes", "comparisons", "buckets", "sent")
	for depth := 0; depth <= 16; depth += 2 {
		a, b := NewReplica("a"), NewReplica("b")
		for i := 0; i < n; i++ {
			k := fmt.Sprintf("member:%06d", i)
			a.Put(k, []byte("v"), 1)
			b.Put(k, []byte("v"), 1)
		}
		rng := rand.New(rand.NewSource(7))
		for i := 0; i < diffs; i++ {
			b.Put(fmt.Sprintf("member:%06d", rng.Intn(n)), []byte("changed"), 2)
		}
		st, err := Repair(context.Background(), a, b, depth)
		if err != nil {
			t.Fatal(err)
		}
		nodes := 1<<(depth+1) - 1
		t.Logf("%5d %8d %12d %12d %8d %10d", depth, 1<<depth, nodes*32, st.Comparisons, st.Buckets, st.Sent)
	}
	t.Log("Shallow trees ship whole leaves of keys; deep trees cost hash comparisons and memory per range.")
	t.Log("Where is the knee for 100k keys? Where would it be for 500 GB of 1 KB values per node?")
}

// TestBreakIt_ZombieTimeline simulates days. Three replicas hold 1,000 keys;
// c goes down on day 1 for `outage` days; keys 0..99 are deleted on day 2
// (only a and b see it); every day each replica compacts tombstones older
// than gc grace; every `every` days, a and b repair with c if c is up. Count
// the deleted keys that are live again on day 40, with the plain grace cutoff
// and with SafeCutoff (last full repair = day 0).
//
//	go test -tags breakit -run BreakIt_ZombieTimeline -v ./labs/c4/antientropy/
func TestBreakIt_ZombieTimeline(t *testing.T) {
	const every = 7
	t.Logf("repair every %d days", every)
	t.Logf("%10s %12s %18s %18s", "gc grace", "outage days", "zombies (grace)", "zombies (safe)")
	for _, grace := range []int64{3, 7, 10, 14} {
		for _, outage := range []int64{2, 5, 9, 12, 20} {
			t.Logf("%10d %12d %18d %18d", grace, outage,
				tZombies(grace, outage, every, false), tZombies(grace, outage, every, true))
		}
	}
	t.Log("Rule to derive: no zombies while gc grace > outage + time to the next repair.")
}

func tZombies(grace, outage, every int64, safe bool) int {
	a, b, c := NewReplica("a"), NewReplica("b"), NewReplica("c")
	for i := 0; i < 1000; i++ {
		for _, r := range []*Replica{a, b, c} {
			r.Put(fmt.Sprintf("k%04d", i), []byte("v"), 0)
		}
	}
	lastFull := int64(0)
	for day := int64(1); day <= 40; day++ {
		cUp := day < 1 || day > 1+outage
		if day == 2 {
			for i := 0; i < 100; i++ {
				a.Delete(fmt.Sprintf("k%04d", i), 2)
				b.Delete(fmt.Sprintf("k%04d", i), 2)
			}
		}
		cut := day - grace
		if safe {
			cut = SafeCutoff(day, grace, lastFull)
		}
		a.Compact(cut)
		b.Compact(cut)
		if cUp {
			c.Compact(cut)
		}
		if cUp && day%every == 0 {
			Repair(context.Background(), a, c, 6)
			Repair(context.Background(), b, c, 6)
			Repair(context.Background(), a, b, 6)
			lastFull = day
		}
	}
	zombies := 0
	for i := 0; i < 100; i++ {
		if _, ok := a.Get(fmt.Sprintf("k%04d", i)); ok {
			zombies++
		}
	}
	return zombies
}
