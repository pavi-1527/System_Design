package hashcache

import (
	"errors"
	"fmt"
	"math"
	"reflect"
	"sort"
	"sync"
	"testing"
	"time"
)

const testTimeout = 5 * time.Second

// within fails the test if fn does not return in time, so a deadlock in your
// implementation becomes a message instead of a hung suite.
func within(t *testing.T, what string, fn func()) {
	t.Helper()
	done := make(chan struct{})
	go func() { defer close(done); fn() }()
	select {
	case <-done:
	case <-time.After(testTimeout):
		t.Fatalf("%s did not finish within %v (deadlock?)", what, testTimeout)
	}
}

func keyN(i int) string  { return fmt.Sprintf("key-%d", i) }
func nodeN(i int) string { return fmt.Sprintf("node-%d", i) }

func keyList(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = keyN(i)
	}
	return out
}

func ringOf(t *testing.T, vnodes, nodes int) *Ring {
	t.Helper()
	r := NewRing(vnodes)
	for i := 0; i < nodes; i++ {
		if err := r.Add(nodeN(i), 1); err != nil {
			t.Fatalf("Add(%q, 1) = %v, want nil", nodeN(i), err)
		}
	}
	return r
}

func owners(t *testing.T, r *Ring, keys []string) map[string]string {
	t.Helper()
	out := make(map[string]string, len(keys))
	for _, k := range keys {
		n, ok := r.Get(k)
		if !ok {
			t.Fatalf("Get(%q) on a non-empty ring returned ok=false", k)
		}
		out[k] = n
	}
	return out
}

func loads(r *Ring, keys []string) map[string]int {
	out := map[string]int{}
	for _, n := range r.Nodes() {
		out[n] = 0
	}
	for _, k := range keys {
		n, _ := r.Get(k)
		out[n]++
	}
	return out
}

// spread returns max/mean, min/mean and the coefficient of variation of the
// per-node key counts.
func spread(c map[string]int) (maxRatio, minRatio, cv float64) {
	var sum float64
	for _, v := range c {
		sum += float64(v)
	}
	mean := sum / float64(len(c))
	minRatio = math.Inf(1)
	var sq float64
	for _, v := range c {
		f := float64(v)
		maxRatio = math.Max(maxRatio, f/mean)
		minRatio = math.Min(minRatio, f/mean)
		sq += (f - mean) * (f - mean)
	}
	return maxRatio, minRatio, math.Sqrt(sq/float64(len(c))) / mean
}

// ---------------------------------------------------------------- Stage 1

func TestStage1_PlacementIsDeterministicAndIndependentOfInsertionOrder(t *testing.T) {
	forward := NewRing(100)
	backward := NewRing(100)
	for i := 0; i < 8; i++ {
		_ = forward.Add(nodeN(i), 1)
		_ = backward.Add(nodeN(7-i), 1)
	}
	keys := keyList(5000)
	a, b := owners(t, forward, keys), owners(t, backward, keys)
	if !reflect.DeepEqual(a, b) {
		t.Fatal("the same nodes added in a different order own different keys: positions must depend only on the node's name, and ties must break deterministically")
	}
	for _, k := range keys[:200] {
		if n, _ := forward.Get(k); n != a[k] {
			t.Fatalf("Get(%q) changed between calls on an unchanged ring", k)
		}
	}
}

func TestStage1_EmptyRingAndMembership(t *testing.T) {
	r := NewRing(50)
	if n, ok := r.Get("k"); ok || n != "" {
		t.Fatalf("Get on an empty ring = (%q, %v), want (\"\", false)", n, ok)
	}
	if got := r.Replicas("k", 3); len(got) != 0 {
		t.Fatalf("Replicas on an empty ring = %q, want none", got)
	}
	for _, n := range []string{"c", "a", "b"} {
		if err := r.Add(n, 1); err != nil {
			t.Fatal(err)
		}
	}
	if got := r.Nodes(); !reflect.DeepEqual(got, []string{"a", "b", "c"}) {
		t.Fatalf("Nodes() = %q, want sorted [a b c]", got)
	}
	members := map[string]bool{"a": true, "b": true, "c": true}
	for _, k := range keyList(1000) {
		if n, ok := r.Get(k); !ok || !members[n] {
			t.Fatalf("Get(%q) = (%q, %v), want a member node", k, n, ok)
		}
	}
	r.Remove("nope") // unknown: no-op
	r.Remove("b")
	if got := r.Nodes(); !reflect.DeepEqual(got, []string{"a", "c"}) {
		t.Fatalf("after Remove(b) Nodes() = %q, want [a c]", got)
	}
	for _, k := range keyList(1000) {
		if n, _ := r.Get(k); n == "b" {
			t.Fatalf("Get(%q) still returns the removed node b: its virtual nodes must all go", k)
		}
	}
	r.Remove("a")
	r.Remove("c")
	if _, ok := r.Get("k"); ok {
		t.Fatal("Get on a ring emptied by Remove returned ok=true")
	}
}

func TestStage1_AddValidatesWeightAndReAddReplaces(t *testing.T) {
	r := NewRing(50)
	if err := r.Add("a", 0); !errors.Is(err, ErrInvalidWeight) {
		t.Fatalf("Add(a, 0) = %v, want ErrInvalidWeight", err)
	}
	if err := r.Add("a", -3); !errors.Is(err, ErrInvalidWeight) {
		t.Fatalf("Add(a, -3) = %v, want ErrInvalidWeight", err)
	}
	if len(r.Nodes()) != 0 {
		t.Fatal("a rejected Add must not add the node")
	}
	_ = r.Add("a", 1)
	_ = r.Add("b", 1)
	before := loads(r, keyList(20000))
	_ = r.Add("b", 4) // re-adding changes the weight; it must not duplicate the node
	if got := r.Nodes(); !reflect.DeepEqual(got, []string{"a", "b"}) {
		t.Fatalf("Nodes() = %q after re-adding b, want [a b]", got)
	}
	after := loads(r, keyList(20000))
	if after["b"] <= before["b"] || after["a"] >= before["a"] {
		t.Fatalf("raising b's weight from 1 to 4 should shift keys to b: before %v after %v", before, after)
	}
}

func TestStage1_ManyVnodesBalanceTheLoad(t *testing.T) {
	r := ringOf(t, 150, 8)
	maxRatio, minRatio, _ := spread(loads(r, keyList(60000)))
	if maxRatio > 1.35 || minRatio < 0.65 {
		t.Fatalf("with 150 vnodes/node over 60k keys the busiest node has %.2fx and the idlest %.2fx the mean load; want within [0.65, 1.35]", maxRatio, minRatio)
	}
}

func TestStage1_FewVnodesBalanceMuchWorse(t *testing.T) {
	// Averaged over 20 different node-name sets so one lucky draw cannot decide it.
	keys := keyList(20000)
	var cvOne, cvMany float64
	const sets = 20
	for s := 0; s < sets; s++ {
		one, many := NewRing(1), NewRing(150)
		for i := 0; i < 5; i++ {
			name := fmt.Sprintf("set%d-node-%d", s, i)
			if err := one.Add(name, 1); err != nil {
				t.Fatalf("Add = %v", err)
			}
			if err := many.Add(name, 1); err != nil {
				t.Fatalf("Add = %v", err)
			}
		}
		_, _, c1 := spread(loads(one, keys))
		_, _, c2 := spread(loads(many, keys))
		cvOne += c1 / sets
		cvMany += c2 / sets
	}
	if cvMany <= 0 || cvOne < 3*cvMany {
		t.Fatalf("mean load variation (stddev/mean) is %.3f with 1 vnode/node and %.3f with 150: virtual nodes should cut it by at least 3x", cvOne, cvMany)
	}
}

func TestStage1_WeightsScaleShare(t *testing.T) {
	r := NewRing(500)
	_ = r.Add("a", 1)
	_ = r.Add("b", 1)
	_ = r.Add("c", 2)
	const n = 30000
	c := loads(r, keyList(n))
	for node, want := range map[string]float64{"a": 0.25, "b": 0.25, "c": 0.5} {
		if got := float64(c[node]) / n; math.Abs(got-want) > 0.06 {
			t.Fatalf("node %s (weights 1:1:2) owns %.3f of the keys, want ≈ %.2f ± 0.06", node, got, want)
		}
	}
}

func TestStage1_AddingANodeMovesAboutOneOverNKeysAllToTheNewNode(t *testing.T) {
	r := ringOf(t, 150, 8)
	keys := keyList(30000)
	before := owners(t, r, keys)
	_ = r.Add(nodeN(8), 1)
	after := owners(t, r, keys)
	moved := 0
	for _, k := range keys {
		if before[k] == after[k] {
			continue
		}
		moved++
		if after[k] != nodeN(8) {
			t.Fatalf("key %q moved from %s to %s: when a node is added, keys may move only TO the new node (existing nodes' positions must not change)", k, before[k], after[k])
		}
	}
	if f := float64(moved) / float64(len(keys)); f < 0.07 || f > 0.16 {
		t.Fatalf("%.1f%% of keys moved when going from 8 to 9 nodes, want ≈ 1/9 = 11.1%% (between 7%% and 16%%)", f*100)
	}
}

func TestStage1_RemovingANodeMovesOnlyItsKeys(t *testing.T) {
	r := ringOf(t, 150, 9)
	keys := keyList(30000)
	before := owners(t, r, keys)
	r.Remove(nodeN(4))
	after := owners(t, r, keys)
	moved := 0
	for _, k := range keys {
		if before[k] != nodeN(4) {
			if after[k] != before[k] {
				t.Fatalf("key %q was on %s, not the removed node, but moved to %s: removal must only reassign the removed node's keys", k, before[k], after[k])
			}
			continue
		}
		moved++
		if after[k] == nodeN(4) {
			t.Fatalf("key %q still maps to the removed node", k)
		}
	}
	if f := float64(moved) / float64(len(keys)); f < 0.07 || f > 0.16 {
		t.Fatalf("the removed node owned %.1f%% of the keys, want ≈ 1/9 = 11.1%% (between 7%% and 16%%)", f*100)
	}
}

func TestStage1_ReplicasAreDistinctNodesInClockwiseOrder(t *testing.T) {
	r := ringOf(t, 100, 6)
	for _, k := range keyList(2000) {
		three := r.Replicas(k, 3)
		if len(three) != 3 {
			t.Fatalf("Replicas(%q, 3) = %q, want 3 nodes", k, three)
		}
		seen := map[string]bool{}
		for _, n := range three {
			if seen[n] {
				t.Fatalf("Replicas(%q, 3) = %q repeats a node: replicas must be distinct physical nodes, not distinct virtual nodes", k, three)
			}
			seen[n] = true
		}
		if owner, _ := r.Get(k); three[0] != owner {
			t.Fatalf("Replicas(%q, 3)[0] = %s but Get = %s: the first replica is the owner", k, three[0], owner)
		}
		if two := r.Replicas(k, 2); !reflect.DeepEqual(two, three[:2]) {
			t.Fatalf("Replicas(%q, 2) = %q is not a prefix of Replicas(%q, 3) = %q", k, two, k, three)
		}
	}
	all := r.Replicas("k", 99)
	sorted := append([]string(nil), all...)
	sort.Strings(sorted)
	if !reflect.DeepEqual(sorted, r.Nodes()) {
		t.Fatalf("Replicas(k, 99) = %q, want every node exactly once", all)
	}
	if got := r.Replicas("k", 0); len(got) != 0 {
		t.Fatalf("Replicas(k, 0) = %q, want none", got)
	}
}

func TestStage1_RemovingTheOwnerHandsKeysToTheNextReplica(t *testing.T) {
	r := ringOf(t, 100, 6)
	keys := keyList(3000)
	type pair struct{ first, second string }
	want := map[string]pair{}
	for _, k := range keys {
		rs := r.Replicas(k, 2)
		want[k] = pair{rs[0], rs[1]}
	}
	r.Remove(nodeN(2))
	for _, k := range keys {
		if want[k].first != nodeN(2) {
			continue
		}
		if got, _ := r.Get(k); got != want[k].second {
			t.Fatalf("key %q: after its owner left, Get = %s, want the old second replica %s (the next node clockwise takes over)", k, got, want[k].second)
		}
	}
}

func TestStage1_KeysPastTheLastVirtualNodeWrapAroundToTheFirst(t *testing.T) {
	// With one virtual node per node the arc between the last point and the
	// first one (the wrap-around) is large, so this is easy to hit: removing
	// any node must hand its keys to the next node clockwise, including the
	// node whose point is last on the ring.
	keys := keyList(3000)
	for victim := 0; victim < 5; victim++ {
		r := ringOf(t, 1, 5)
		second := map[string]string{}
		for _, k := range keys {
			if n, _ := r.Get(k); n == nodeN(victim) {
				second[k] = r.Replicas(k, 2)[1]
			}
		}
		if len(second) == 0 {
			continue
		}
		r.Remove(nodeN(victim))
		for k, want := range second {
			if got, _ := r.Get(k); got != want {
				t.Fatalf("1 vnode/node, node %d removed: key %q went to %s, want its clockwise successor %s (does a hash past the last point wrap to the first point?)", victim, k, got, want)
			}
		}
	}
}

func TestStage1_RingIsSafeForConcurrentUse(t *testing.T) {
	r := ringOf(t, 50, 4)
	var wg sync.WaitGroup
	stop := make(chan struct{})
	for g := 0; g < 4; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; ; i++ {
				select {
				case <-stop:
					return
				default:
				}
				k := keyN(i)
				if n, ok := r.Get(k); ok && n == "" {
					t.Errorf("Get(%q) returned ok=true with an empty node", k)
					return
				}
				_ = r.Replicas(k, 2)
				_ = r.Nodes()
			}
		}()
	}
	for i := 0; i < 40; i++ {
		_ = r.Add(nodeN(10+i%3), 1)
		r.Remove(nodeN(10 + (i+1)%3))
	}
	close(stop)
	within(t, "ring readers", wg.Wait)
}
