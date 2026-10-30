package ring

import (
	"context"
	"fmt"
	"math"
	"sort"
	"sync"
	"testing"
	"time"
)

// ---------- helpers (prefixed with t to avoid clashing with your code) ----------

func tKeys(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = fmt.Sprintf("account-%06d", i)
	}
	return out
}

func tNames(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = fmt.Sprintf("db-%02d", i)
	}
	return out
}

func tRing(t *testing.T, cfg Config, names ...string) *Ring {
	t.Helper()
	r := New(cfg)
	for _, n := range names {
		if err := r.Add(n, 1); err != nil {
			t.Fatalf("Add(%q): %v", n, err)
		}
	}
	return r
}

// tTableHash places labels at hand-chosen positions so expected owners can be
// worked out on paper. Anything not in the table gets its position from the
// default hash, which is fine for tests that only use table keys.
func tTableHash(table map[string]uint64) HashFunc {
	return func(s string) uint64 {
		if v, ok := table[s]; ok {
			return v
		}
		return Hash64(s)
	}
}

func tWithin(t *testing.T, d time.Duration, fn func()) {
	t.Helper()
	done := make(chan struct{})
	go func() { defer close(done); fn() }()
	select {
	case <-done:
	case <-time.After(d):
		t.Fatalf("timed out after %v (deadlock or livelock?)", d)
	}
}

// ---------- hand-checkable placement ----------

func TestLookup_ReturnsClockwiseSuccessorAndWraps(t *testing.T) {
	// Positions: a#0=100, b#0=200, c#0=300.
	h := tTableHash(map[string]uint64{
		VNodeLabel("a", 0): 100, VNodeLabel("b", 0): 200, VNodeLabel("c", 0): 300,
		"k-50": 50, "k-100": 100, "k-101": 101, "k-250": 250, "k-300": 300, "k-301": 301, "k-max": math.MaxUint64,
	})
	r := tRing(t, Config{VNodes: 1, Hash: h}, "a", "b", "c")
	cases := []struct{ key, want, why string }{
		{"k-50", "a", "50 is before the first position: owned by a (100)"},
		{"k-100", "a", "a key exactly on a position belongs to that position (>=, not >)"},
		{"k-101", "b", "just past a: next position is b (200)"},
		{"k-250", "c", "between b and c"},
		{"k-300", "c", "exactly on c"},
		{"k-301", "a", "past the largest position: wrap around to the smallest (a)"},
		{"k-max", "a", "largest possible hash wraps to a"},
	}
	for _, c := range cases {
		got, err := r.Lookup(c.key)
		if err != nil || got != c.want {
			t.Errorf("Lookup(%s) = %q, %v; want %q (%s)", c.key, got, err, c.want, c.why)
		}
	}
}

func TestLookup_EmptyRingReturnsErrEmptyRing(t *testing.T) {
	r := New(Config{})
	if _, err := r.Lookup("x"); err != ErrEmptyRing {
		t.Errorf("Lookup on empty ring: err = %v, want ErrEmptyRing", err)
	}
	if _, err := r.LookupN("x", 2); err != ErrEmptyRing {
		t.Errorf("LookupN on empty ring: err = %v, want ErrEmptyRing", err)
	}
}

func TestRing_PositionTieIsBrokenByNodeNameNotInsertionOrder(t *testing.T) {
	// Both virtual nodes sit at position 500. Whatever order they were added
	// in, the owner of keys at or before 500 must be the same ("x" < "y").
	h := tTableHash(map[string]uint64{
		VNodeLabel("x", 0): 500, VNodeLabel("y", 0): 500, VNodeLabel("z", 0): 900, "k": 400,
	})
	r1 := tRing(t, Config{VNodes: 1, Hash: h}, "x", "y", "z")
	r2 := tRing(t, Config{VNodes: 1, Hash: h}, "z", "y", "x")
	a, _ := r1.Lookup("k")
	b, _ := r2.Lookup("k")
	if a != "x" || b != "x" {
		t.Errorf("owner of a key hashing before a tied position: %q (x,y,z order) vs %q (z,y,x order); want %q for both: ties must go to the smaller node name so the ring does not depend on history", a, b, "x")
	}
}

func TestLookup_DoesNotDependOnInsertionOrder(t *testing.T) {
	names := tNames(7)
	rev := append([]string(nil), names...)
	sort.Sort(sort.Reverse(sort.StringSlice(rev)))
	a := tRing(t, Config{VNodes: 50}, names...)
	b := tRing(t, Config{VNodes: 50}, rev...)
	for _, k := range tKeys(2000) {
		x, _ := a.Lookup(k)
		y, _ := b.Lookup(k)
		if x != y {
			t.Fatalf("key %s: %s with ascending adds, %s with descending adds; placement must depend only on the node set", k, x, y)
		}
	}
}

func TestLookup_IsDeterministicAcrossRingInstances(t *testing.T) {
	a := tRing(t, Config{VNodes: 64}, tNames(5)...)
	b := tRing(t, Config{VNodes: 64}, tNames(5)...)
	for _, k := range tKeys(1000) {
		x, _ := a.Lookup(k)
		y, _ := b.Lookup(k)
		if x != y {
			t.Fatalf("two rings built the same way disagree on %s: %s vs %s", k, x, y)
		}
	}
}

// ---------- add / remove / errors ----------

func TestAddRemove_Errors(t *testing.T) {
	r := New(Config{VNodes: 4})
	if err := r.Add("", 1); err != ErrBadArgument {
		t.Errorf("Add(\"\") = %v, want ErrBadArgument", err)
	}
	if err := r.Add("a", 0); err != ErrBadArgument {
		t.Errorf("Add(a, weight 0) = %v, want ErrBadArgument", err)
	}
	if err := r.Add("a", 1); err != nil {
		t.Fatalf("Add(a) = %v", err)
	}
	if err := r.Add("a", 1); err != ErrNodeExists {
		t.Errorf("second Add(a) = %v, want ErrNodeExists", err)
	}
	if err := r.Remove("nope"); err != ErrUnknownNode {
		t.Errorf("Remove(nope) = %v, want ErrUnknownNode", err)
	}
	if err := r.Remove("a"); err != nil {
		t.Errorf("Remove(a) = %v", err)
	}
	if r.Len() != 0 {
		t.Errorf("Len after removing the only node = %d, want 0", r.Len())
	}
	if _, err := r.Lookup("k"); err != ErrEmptyRing {
		t.Errorf("Lookup after removing every node = %v, want ErrEmptyRing (virtual nodes must be removed too)", err)
	}
}

func TestNodes_SortedAndLen(t *testing.T) {
	r := tRing(t, Config{VNodes: 3}, "c", "a", "b")
	got := r.Nodes()
	if fmt.Sprint(got) != "[a b c]" || r.Len() != 3 {
		t.Errorf("Nodes() = %v, Len() = %d; want [a b c] and 3", got, r.Len())
	}
}

func TestRemove_ThenReAddRestoresTheSamePlacement(t *testing.T) {
	r := tRing(t, Config{VNodes: 32}, tNames(5)...)
	keys := tKeys(3000)
	before := make([]string, len(keys))
	for i, k := range keys {
		before[i], _ = r.Lookup(k)
	}
	if err := r.Remove("db-02"); err != nil {
		t.Fatal(err)
	}
	if err := r.Add("db-02", 1); err != nil {
		t.Fatal(err)
	}
	for i, k := range keys {
		if got, _ := r.Lookup(k); got != before[i] {
			t.Fatalf("key %s: owner %s before remove+re-add, %s after; the ring must be a pure function of the node set", k, before[i], got)
		}
	}
}

func TestClone_IsIndependent(t *testing.T) {
	r := tRing(t, Config{VNodes: 16}, "a", "b", "c")
	c := r.Clone()
	if err := r.Add("d", 1); err != nil {
		t.Fatal(err)
	}
	if c.Len() != 3 {
		t.Errorf("clone has %d nodes after the original gained one; Clone must copy, not share", c.Len())
	}
	if err := c.Remove("a"); err != nil {
		t.Fatal(err)
	}
	if r.Len() != 4 {
		t.Errorf("original has %d nodes after the clone lost one; want 4", r.Len())
	}
}

// ---------- monotonicity: the property that makes it consistent hashing ----------

func TestAdd_KeysOnlyMoveToTheNewNode(t *testing.T) {
	keys := tKeys(20000)
	r := tRing(t, Config{VNodes: 100}, tNames(8)...)
	before := make([]string, len(keys))
	for i, k := range keys {
		before[i], _ = r.Lookup(k)
	}
	if err := r.Add("db-new", 1); err != nil {
		t.Fatal(err)
	}
	moved := 0
	for i, k := range keys {
		after, _ := r.Lookup(k)
		if after == before[i] {
			continue
		}
		moved++
		if after != "db-new" {
			t.Fatalf("key %s moved from %s to %s; on Add only the new node may gain keys (otherwise data shuffles between nodes that did not change)", k, before[i], after)
		}
	}
	if moved == 0 {
		t.Fatal("no key moved to the new node; Add did not take effect")
	}
}

func TestRemove_OnlyTheRemovedNodesKeysMove(t *testing.T) {
	keys := tKeys(20000)
	r := tRing(t, Config{VNodes: 100}, tNames(8)...)
	before := make([]string, len(keys))
	for i, k := range keys {
		before[i], _ = r.Lookup(k)
	}
	if err := r.Remove("db-03"); err != nil {
		t.Fatal(err)
	}
	for i, k := range keys {
		after, _ := r.Lookup(k)
		if before[i] != "db-03" && after != before[i] {
			t.Fatalf("key %s moved from %s to %s although %s was not removed", k, before[i], after, before[i])
		}
		if before[i] == "db-03" && after == "db-03" {
			t.Fatalf("key %s still maps to removed node db-03", k)
		}
	}
}

func TestMovedFraction_AddingNodeNPlus1MovesAboutOneOverNPlus1(t *testing.T) {
	keys := tKeys(30000)
	for _, n := range []int{4, 9, 19} {
		before := tRing(t, Config{VNodes: 200}, tNames(n)...)
		after := before.Clone()
		if err := after.Add("db-new", 1); err != nil {
			t.Fatal(err)
		}
		got := MovedFraction(before, after, keys)
		want := 1.0 / float64(n+1)
		if got < want*0.6 || got > want*1.5 {
			t.Errorf("N=%d→%d: moved fraction = %.3f, want ≈ %.3f (1/(N+1)); modulo hashing would move ≈ %.2f", n, n+1, got, want, float64(n)/float64(n+1))
		}
	}
}

func TestMovedFraction_ModuloBaselineMovesNearlyEverything(t *testing.T) {
	// The provided baseline, measured with the same helper shape: shows what
	// the ring is being compared with. Also checks MovedFraction's semantics
	// on rings that differ in every key (empty vs non-empty).
	keys := tKeys(10000)
	moved := 0
	old, grown := tNames(9), tNames(10)
	for _, k := range keys {
		if ModOwner(old, k, nil) != ModOwner(grown, k, nil) {
			moved++
		}
	}
	if f := float64(moved) / float64(len(keys)); f < 0.8 {
		t.Errorf("modulo 9→10 moved %.2f of keys, expected ≈ 0.9 (provided baseline is broken?)", f)
	}
	empty := New(Config{})
	full := tRing(t, Config{VNodes: 8}, "a")
	if got := MovedFraction(empty, full, keys); got != 1 {
		t.Errorf("MovedFraction(empty, one-node ring) = %v, want 1: keys with no owner before count as moved", got)
	}
	if got := MovedFraction(full, full, keys); got != 0 {
		t.Errorf("MovedFraction(r, r) = %v, want 0", got)
	}
	if got := MovedFraction(full, full, nil); got != 0 {
		t.Errorf("MovedFraction with no keys = %v, want 0", got)
	}
}

// ---------- balance and weights ----------

func TestBalance_ManyVirtualNodesGiveEvenLoad(t *testing.T) {
	keys := tKeys(50000)
	r := tRing(t, Config{VNodes: 256}, tNames(10)...)
	s := Summarize(Distribution(r, keys))
	if s.Nodes != 10 || s.Total != len(keys) {
		t.Fatalf("Summarize: Nodes=%d Total=%d, want 10 and %d", s.Nodes, s.Total, len(keys))
	}
	if s.MaxOverMean > 1.25 {
		t.Errorf("with 256 vnodes/node the busiest node has %.2f× the mean load (max=%d mean=%.0f); want ≤ 1.25", s.MaxOverMean, s.Max, s.Mean)
	}
}

func TestBalance_OneVirtualNodePerNodeIsBadlySkewed(t *testing.T) {
	// The reason virtual nodes exist: with one point per node the arcs are
	// wildly unequal. This also proves Config.VNodes is honoured.
	keys := tKeys(50000)
	few := Summarize(Distribution(tRing(t, Config{VNodes: 1}, tNames(10)...), keys))
	many := Summarize(Distribution(tRing(t, Config{VNodes: 256}, tNames(10)...), keys))
	if few.MaxOverMean < 1.5 {
		t.Errorf("1 vnode/node: max/mean = %.2f; expected clear skew (≥ 1.5). Is Config.VNodes ignored?", few.MaxOverMean)
	}
	if few.MaxOverMean < many.MaxOverMean*1.3 {
		t.Errorf("skew with 1 vnode (%.2f) should be much worse than with 256 vnodes (%.2f)", few.MaxOverMean, many.MaxOverMean)
	}
}

func TestVNodeCount_FollowsConfigAndWeight(t *testing.T) {
	// a has weight 3 and VNodes 2, so exactly 6 virtual nodes a#0..a#5 at
	// 100..600. b has weight 1: b#0=1000 and b#1=9000. Decoy positions for a#6
	// and a#7 exist in the table; they must NOT be used.
	table := map[string]uint64{
		VNodeLabel("b", 0): 1000, VNodeLabel("b", 1): 9000,
		VNodeLabel("a", 6): 7777, VNodeLabel("a", 7): 8888,
	}
	for i := 0; i < 6; i++ {
		table[VNodeLabel("a", i)] = uint64(100 * (i + 1))
	}
	for i := 0; i < 6; i++ {
		table[fmt.Sprintf("in-arc-%d", i)] = uint64(100*(i+1) - 50) // 50, 150, ... 550
	}
	table["past-a"] = 650    // owned by b#0 (1000)
	table["in-decoy"] = 3000 // owned by b#1 (9000) unless a has an a#6 at 7777
	r := New(Config{VNodes: 2, Hash: tTableHash(table)})
	if err := r.Add("a", 3); err != nil {
		t.Fatal(err)
	}
	if err := r.Add("b", 1); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 6; i++ {
		k := fmt.Sprintf("in-arc-%d", i)
		if got, _ := r.Lookup(k); got != "a" {
			t.Errorf("Lookup(%s) = %q, want a: a should have virtual node a#%d at %d", k, got, i, 100*(i+1))
		}
	}
	if got, _ := r.Lookup("past-a"); got != "b" {
		t.Errorf("Lookup(past-a) = %q, want b: a has no virtual node between 600 and 1000", got)
	}
	if got, _ := r.Lookup("in-decoy"); got != "b" {
		t.Errorf("Lookup(in-decoy) = %q, want b: a must have exactly weight*VNodes = 6 virtual nodes, not 8", got)
	}
}

func TestWeights_HeavierNodeGetsProportionalShare(t *testing.T) {
	keys := tKeys(60000)
	r := New(Config{VNodes: 200})
	for _, n := range []string{"small-1", "small-2", "small-3"} {
		if err := r.Add(n, 1); err != nil {
			t.Fatal(err)
		}
	}
	if err := r.Add("big", 3); err != nil {
		t.Fatal(err)
	}
	d := Distribution(r, keys)
	// big should hold 3/6 = 50%, each small 1/6 ≈ 16.7%.
	if f := float64(d["big"]) / float64(len(keys)); f < 0.42 || f > 0.58 {
		t.Errorf("weight-3 node holds %.1f%% of keys, want ≈ 50%% (3 of 6 weight units)", 100*f)
	}
	for _, n := range []string{"small-1", "small-2", "small-3"} {
		if f := float64(d[n]) / float64(len(keys)); f < 0.11 || f > 0.23 {
			t.Errorf("%s holds %.1f%% of keys, want ≈ 16.7%%", n, 100*f)
		}
	}
}

// ---------- replica sets ----------

func TestLookupN_ReturnsDistinctNodesNotAdjacentVirtualNodes(t *testing.T) {
	// Points: a#0=10, a#1=20, b#0=30, a#2=40, c#0=50. A naive "next 3
	// virtual nodes" from key 5 would give [a a b].
	h := tTableHash(map[string]uint64{
		VNodeLabel("a", 0): 10, VNodeLabel("a", 1): 20, VNodeLabel("a", 2): 40,
		VNodeLabel("b", 0): 30, VNodeLabel("c", 0): 50, "k5": 5, "k45": 45, "k55": 55,
	})
	r := New(Config{VNodes: 1, Hash: h})
	// weights: a=3 gives a#0..a#2 with VNodes=1; b and c weight 1.
	for name, w := range map[string]int{"a": 3, "b": 1, "c": 1} {
		if err := r.Add(name, w); err != nil {
			t.Fatal(err)
		}
	}
	cases := []struct {
		key  string
		n    int
		want string
	}{
		{"k5", 3, "[a b c]"},
		{"k5", 2, "[a b]"},
		{"k45", 3, "[c a b]"}, // c(50), wrap to a(10), then b(30)
		{"k55", 3, "[a b c]"}, // wraps immediately
		{"k5", 1, "[a]"},
	}
	for _, c := range cases {
		got, err := r.LookupN(c.key, c.n)
		if err != nil || fmt.Sprint(got) != c.want {
			t.Errorf("LookupN(%s, %d) = %v, %v; want %s", c.key, c.n, got, err, c.want)
		}
	}
}

func TestLookupN_FirstReplicaIsLookupAndAllDistinct(t *testing.T) {
	r := tRing(t, Config{VNodes: 64}, tNames(6)...)
	for _, k := range tKeys(2000) {
		owner, _ := r.Lookup(k)
		set, err := r.LookupN(k, 3)
		if err != nil || len(set) != 3 {
			t.Fatalf("LookupN(%s, 3) = %v, %v; want 3 nodes", k, set, err)
		}
		if set[0] != owner {
			t.Fatalf("LookupN(%s,3)[0] = %s but Lookup = %s", k, set[0], owner)
		}
		if set[0] == set[1] || set[0] == set[2] || set[1] == set[2] {
			t.Fatalf("replica set for %s has duplicates: %v (three copies on one machine is no replication)", k, set)
		}
	}
}

func TestLookupN_FewerNodesThanRequested(t *testing.T) {
	r := tRing(t, Config{VNodes: 8}, "a", "b")
	got, err := r.LookupN("k", 5)
	if err != nil || len(got) != 2 {
		t.Errorf("LookupN(k, 5) on 2 nodes = %v, %v; want both nodes", got, err)
	}
	got, err = r.LookupN("k", 0)
	if err != nil || len(got) != 0 {
		t.Errorf("LookupN(k, 0) = %v, %v; want empty", got, err)
	}
}

func TestLookupN_RemovingANodeOnlyShiftsReplicaSetsThatContainedIt(t *testing.T) {
	r := tRing(t, Config{VNodes: 64}, tNames(8)...)
	keys := tKeys(5000)
	before := make([][]string, len(keys))
	for i, k := range keys {
		before[i], _ = r.LookupN(k, 3)
	}
	if err := r.Remove("db-05"); err != nil {
		t.Fatal(err)
	}
	for i, k := range keys {
		after, _ := r.LookupN(k, 3)
		var want []string
		had := false
		for _, n := range before[i] {
			if n == "db-05" {
				had = true
			} else {
				want = append(want, n)
			}
		}
		if !had {
			if fmt.Sprint(after) != fmt.Sprint(before[i]) {
				t.Fatalf("key %s: replica set changed from %v to %v though it did not include the removed node", k, before[i], after)
			}
			continue
		}
		if len(after) != 3 || fmt.Sprint(after[:2]) != fmt.Sprint(want) {
			t.Fatalf("key %s: replica set %v → %v; want the surviving replicas %v followed by one new node", k, before[i], after, want)
		}
	}
}

// ---------- statistics helpers ----------

func TestSummarize_ComputesMinMaxMeanStdDev(t *testing.T) {
	s := Summarize(map[string]int{"a": 10, "b": 20, "c": 30, "d": 40})
	if s.Nodes != 4 || s.Total != 100 || s.Min != 10 || s.Max != 40 {
		t.Errorf("Summarize basics = %+v", s)
	}
	if math.Abs(s.Mean-25) > 1e-9 || math.Abs(s.StdDev-math.Sqrt(125)) > 1e-9 || math.Abs(s.MaxOverMean-1.6) > 1e-9 {
		t.Errorf("Mean=%v StdDev=%v MaxOverMean=%v; want 25, √125 ≈ 11.18 (population), 1.6", s.Mean, s.StdDev, s.MaxOverMean)
	}
	if z := Summarize(nil); z != (Stats{}) {
		t.Errorf("Summarize(nil) = %+v, want zero Stats", z)
	}
	if z := Summarize(map[string]int{"a": 0, "b": 0}); z.MaxOverMean != 0 || z.Nodes != 2 {
		t.Errorf("all-zero loads: %+v; MaxOverMean must be 0, not NaN", z)
	}
}

func TestDistribution_IncludesNodesThatOwnNothing(t *testing.T) {
	r := tRing(t, Config{VNodes: 1}, "a", "b", "c")
	d := Distribution(r, []string{"only-one-key"})
	if len(d) != 3 {
		t.Errorf("Distribution has %d entries, want 3 (every node, even with 0 keys)", len(d))
	}
	total := 0
	for _, c := range d {
		total += c
	}
	if total != 1 {
		t.Errorf("counts sum to %d, want 1", total)
	}
}

// ---------- bounded loads ----------

func TestAssignBounded_NoNodeExceedsTheCap(t *testing.T) {
	keys := tKeys(10000)
	r := tRing(t, Config{VNodes: 4}, tNames(10)...) // deliberately few vnodes: plain ring is skewed
	plain := Summarize(Distribution(r, keys))
	for _, eps := range []float64{0.05, 0.25, 1.0} {
		asg, err := AssignBounded(r, keys, eps)
		if err != nil || len(asg) != len(keys) {
			t.Fatalf("eps=%v: err=%v, assigned %d of %d", eps, err, len(asg), len(keys))
		}
		counts := map[string]int{}
		for _, n := range asg {
			counts[n]++
		}
		limit := int(math.Ceil((1 + eps) * float64(len(keys)) / 10))
		for n, c := range counts {
			if c > limit {
				t.Errorf("eps=%v: node %s holds %d keys, cap is %d", eps, n, c, limit)
			}
		}
	}
	if plain.MaxOverMean < 1.3 {
		t.Fatalf("test setup: plain ring should be skewed with 4 vnodes, got max/mean %.2f", plain.MaxOverMean)
	}
}

func TestAssignBounded_PrefersTheOwnerAndSpillsClockwise(t *testing.T) {
	// Three nodes, six keys, eps=0 → cap 2. All keys hash just before a#0, so
	// without a bound a would own all six.
	table := map[string]uint64{
		VNodeLabel("a", 0): 100, VNodeLabel("b", 0): 200, VNodeLabel("c", 0): 300,
	}
	keys := []string{"k1", "k2", "k3", "k4", "k5", "k6"}
	for _, k := range keys {
		table[k] = 50
	}
	r := tRing(t, Config{VNodes: 1, Hash: tTableHash(table)}, "a", "b", "c")
	asg, err := AssignBounded(r, keys, 0)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"k1": "a", "k2": "a", "k3": "b", "k4": "b", "k5": "c", "k6": "c"}
	for k, w := range want {
		if asg[k] != w {
			t.Errorf("key %s assigned to %q, want %q (owner a first; once a is full spill to b, then c, in key order)", k, asg[k], w)
		}
	}
}

func TestAssignBounded_CapIsRoundedUpSoEveryKeyIsPlaced(t *testing.T) {
	// 7 keys over 3 nodes with epsilon 0: the cap is ceil(7/3) = 3, not 2.
	// A cap of 2 has room for only 6 keys and the seventh would be dropped.
	keys := tKeys(7)
	r := tRing(t, Config{VNodes: 8}, "a", "b", "c")
	asg, err := AssignBounded(r, keys, 0)
	if err != nil || len(asg) != 7 {
		t.Fatalf("assigned %d of 7 keys, err=%v; the capacity must be rounded up", len(asg), err)
	}
	counts := map[string]int{}
	for _, n := range asg {
		counts[n]++
	}
	for n, c := range counts {
		if c > 3 {
			t.Errorf("node %s holds %d keys, cap is 3", n, c)
		}
	}
}

func TestAssignBounded_LargeEpsilonEqualsPlainLookup(t *testing.T) {
	keys := tKeys(2000)
	r := tRing(t, Config{VNodes: 64}, tNames(5)...)
	asg, err := AssignBounded(r, keys, 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range keys {
		if want, _ := r.Lookup(k); asg[k] != want {
			t.Fatalf("with a huge epsilon the bound never binds, but %s went to %s instead of its owner %s", k, asg[k], want)
		}
	}
}

func TestAssignBounded_Errors(t *testing.T) {
	r := tRing(t, Config{VNodes: 2}, "a")
	if _, err := AssignBounded(r, []string{"k"}, -0.1); err != ErrBadArgument {
		t.Errorf("negative epsilon: err = %v, want ErrBadArgument", err)
	}
	if _, err := AssignBounded(New(Config{}), []string{"k"}, 0.1); err != ErrEmptyRing {
		t.Errorf("empty ring: err = %v, want ErrEmptyRing", err)
	}
}

// ---------- alternatives ----------

func TestRendezvous_MinimalMovementOnAddAndRemove(t *testing.T) {
	keys := tKeys(20000)
	nodes := tNames(9)
	grown := append(append([]string(nil), nodes...), "db-new")
	moved := 0
	for _, k := range keys {
		a, b := Rendezvous(nodes, k, nil), Rendezvous(grown, k, nil)
		if a != b {
			moved++
			if b != "db-new" {
				t.Fatalf("key %s moved %s → %s on adding db-new; only moves to the new node are allowed", k, a, b)
			}
		}
	}
	f := float64(moved) / float64(len(keys))
	if f < 0.07 || f > 0.14 {
		t.Errorf("moved fraction = %.3f, want ≈ 0.10 (1/10)", f)
	}
	// Order of the node slice must not matter.
	rev := append([]string(nil), grown...)
	sort.Sort(sort.Reverse(sort.StringSlice(rev)))
	for _, k := range keys[:500] {
		if Rendezvous(grown, k, nil) != Rendezvous(rev, k, nil) {
			t.Fatalf("Rendezvous result depends on slice order for %s", k)
		}
	}
	if got := Rendezvous(nil, "k", nil); got != "" {
		t.Errorf("Rendezvous with no nodes = %q, want \"\"", got)
	}
}

func TestRendezvous_IsEvenAndTieBreaksByName(t *testing.T) {
	counts := map[string]int{}
	keys := tKeys(30000)
	for _, k := range keys {
		counts[Rendezvous(tNames(6), k, nil)]++
	}
	if s := Summarize(counts); s.MaxOverMean > 1.08 || s.Min == 0 {
		t.Errorf("rendezvous over 6 nodes: max/mean = %.3f, want ≤ 1.08 (it needs no virtual nodes)", s.MaxOverMean)
	}
	constHash := func(string) uint64 { return 7 }
	if got := Rendezvous([]string{"zeta", "alpha", "mid"}, "k", constHash); got != "alpha" {
		t.Errorf("all scores equal: got %q, want %q (smallest name wins ties)", got, "alpha")
	}
}

func TestJumpHash_MovesOnlyToTheNewBucketAndAboutOneOverNPlus1(t *testing.T) {
	const n = 40000
	for _, buckets := range []int{1, 2, 5, 10, 31} {
		moved := 0
		for i := 0; i < n; i++ {
			key := Hash64(fmt.Sprintf("k%d", i))
			a, b := JumpHash(key, buckets), JumpHash(key, buckets+1)
			if a < 0 || a >= buckets || b < 0 || b > buckets {
				t.Fatalf("bucket out of range: %d (of %d), %d (of %d)", a, buckets, b, buckets+1)
			}
			if a != b {
				moved++
				if b != buckets {
					t.Fatalf("key moved %d → %d when growing %d→%d buckets; jump hash only moves keys into the new bucket", a, b, buckets, buckets+1)
				}
			}
		}
		f, want := float64(moved)/n, 1/float64(buckets+1)
		if f < want*0.85 || f > want*1.15 {
			t.Errorf("%d→%d buckets: moved %.4f, want ≈ %.4f", buckets, buckets+1, f, want)
		}
	}
	if JumpHash(123, 0) != -1 || JumpHash(123, -3) != -1 {
		t.Error("JumpHash with no buckets must return -1")
	}
	if JumpHash(123, 1) != 0 {
		t.Error("JumpHash into one bucket must return 0")
	}
}

func TestJumpHash_MatchesReferenceValues(t *testing.T) {
	// Golden values for the Lamping–Veach algorithm (LCG multiplier
	// 2862933555777941757): compatible with other implementations of jump hash.
	cases := []struct {
		key  uint64
		want [3]int // for 10, 100, 1000 buckets
	}{
		{0, [3]int{0, 0, 0}},
		{1, [3]int{6, 55, 549}},
		{42, [3]int{2, 43, 571}},
		{1 << 40, [3]int{9, 29, 145}},
		{0xdeadbeefcafef00d, [3]int{1, 32, 774}},
	}
	for _, c := range cases {
		got := [3]int{JumpHash(c.key, 10), JumpHash(c.key, 100), JumpHash(c.key, 1000)}
		if got != c.want {
			t.Errorf("JumpHash(%d, {10,100,1000}) = %v, want %v", c.key, got, c.want)
		}
	}
}

func TestJumpHash_IsEven(t *testing.T) {
	counts := map[string]int{}
	for i := 0; i < 40000; i++ {
		counts[fmt.Sprint(JumpHash(Hash64(fmt.Sprintf("k%d", i)), 8))]++
	}
	if s := Summarize(counts); s.Nodes != 8 || s.MaxOverMean > 1.06 {
		t.Errorf("jump hash over 8 buckets: %+v", s)
	}
}

// ---------- concurrency ----------

func TestRing_ConcurrentLookupDuringAddRemove(t *testing.T) {
	base := tNames(4)
	r := tRing(t, Config{VNodes: 32}, base...)
	keys := tKeys(200)
	baseOwner := make([]string, len(keys))
	for i, k := range keys {
		baseOwner[i], _ = r.Lookup(k)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	stop := make(chan struct{})
	var wg sync.WaitGroup
	errc := make(chan string, 16)
	report := func(s string) {
		select {
		case errc <- s:
		default:
		}
	}
	for g := 0; g < 6; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; ; i++ {
				select {
				case <-stop:
					return
				case <-ctx.Done():
					return
				default:
				}
				idx := (i*7 + g) % len(keys)
				got, err := r.Lookup(keys[idx])
				if err != nil || got == "" {
					report(fmt.Sprintf("Lookup(%s) = %q, %v during Add/Remove; the ring was never empty", keys[idx], got, err))
					return
				}
				// Monotonic: owner is the base owner or one of the dynamic nodes.
				if got != baseOwner[idx] && len(got) < 4 || got != baseOwner[idx] && got[:4] != "dyn-" {
					report(fmt.Sprintf("Lookup(%s) = %s: neither base owner %s nor a dynamic node", keys[idx], got, baseOwner[idx]))
					return
				}
				if set, err := r.LookupN(keys[idx], 3); err != nil || len(set) != 3 {
					report(fmt.Sprintf("LookupN(%s,3) = %v, %v", keys[idx], set, err))
					return
				}
				_ = r.Nodes()
			}
		}(g)
	}
	tWithin(t, 25*time.Second, func() {
		for round := 0; round < 5; round++ {
			for i := 0; i < 10; i++ {
				if err := r.Add(fmt.Sprintf("dyn-%d", i), 1+i%2); err != nil {
					report(fmt.Sprintf("Add: %v", err))
				}
			}
			for i := 0; i < 10; i++ {
				if err := r.Remove(fmt.Sprintf("dyn-%d", i)); err != nil {
					report(fmt.Sprintf("Remove: %v", err))
				}
			}
		}
		close(stop)
		wg.Wait()
	})
	select {
	case msg := <-errc:
		t.Fatal(msg)
	default:
	}
	for i, k := range keys {
		if got, _ := r.Lookup(k); got != baseOwner[i] {
			t.Fatalf("after adding and removing the same nodes, %s moved from %s to %s", k, baseOwner[i], got)
		}
	}
}
