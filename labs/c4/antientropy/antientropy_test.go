package antientropy

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"sort"
	"sync"
	"testing"
	"time"
)

// ---------- helpers (prefixed with t to avoid clashing with your code) ----------

// tWait waits for wg, failing the test instead of hanging if it takes longer
// than d (a deadlock between two Repairs, for example).
func tWait(t *testing.T, wg *sync.WaitGroup, d time.Duration, what string) {
	t.Helper()
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(d):
		t.Fatalf("%s: still running after %v (deadlock? Repair must not hold one replica's lock while calling the other)", what, d)
	}
}

func tTree(t *testing.T, r *Replica, depth int) *Tree {
	t.Helper()
	tr, err := BuildTree(r, depth)
	if err != nil {
		t.Fatalf("BuildTree(%s, %d): %v", r.Name(), depth, err)
	}
	if tr == nil {
		t.Fatalf("BuildTree(%s, %d) returned a nil tree and no error", r.Name(), depth)
	}
	return tr
}

func tKey(i int) string { return fmt.Sprintf("member:%06d", i) }

// tFill writes keys 0..n-1 with value "v<i>" at stamp 100 to every replica.
func tFill(n int, rs ...*Replica) {
	for i := 0; i < n; i++ {
		for _, r := range rs {
			r.Put(tKey(i), []byte(fmt.Sprintf("v%d", i)), 100)
		}
	}
}

// tSame fails unless a and b hold exactly the same entries.
func tSame(t *testing.T, a, b *Replica, keys []string) {
	t.Helper()
	if a.Len() != b.Len() {
		t.Fatalf("%s has %d entries, %s has %d: the replicas did not converge", a.Name(), a.Len(), b.Name(), b.Len())
	}
	for _, k := range keys {
		ea, oka := a.Lookup(k)
		eb, okb := b.Lookup(k)
		if oka != okb || ea.Stamp != eb.Stamp || ea.Deleted != eb.Deleted || string(ea.Value) != string(eb.Value) {
			t.Fatalf("key %q: %s has %+v (present %v), %s has %+v (present %v): the replicas did not converge",
				k, a.Name(), ea, oka, b.Name(), eb, okb)
		}
	}
}

func tKeys(n int) []string {
	ks := make([]string, n)
	for i := range ks {
		ks[i] = tKey(i)
	}
	return ks
}

// ---------- 1. entries and replicas ----------

func TestWins_HigherStampThenTombstoneThenLargerValue(t *testing.T) {
	live := func(v string, s int64) Entry { return Entry{Key: "k", Value: []byte(v), Stamp: s} }
	tomb := func(s int64) Entry { return Entry{Key: "k", Stamp: s, Deleted: true} }
	cases := []struct {
		name string
		e, o Entry
		want bool
	}{
		{"higher stamp wins", live("a", 2), live("z", 1), true},
		{"lower stamp loses", live("z", 1), live("a", 2), false},
		{"newer write beats older delete", live("a", 5), tomb(4), true},
		{"newer delete beats older write", tomb(5), live("a", 4), true},
		{"tombstone wins a tie", tomb(3), live("a", 3), true},
		{"live value loses a tie to a tombstone", live("a", 3), tomb(3), false},
		{"larger value wins a tie", live("b", 3), live("a", 3), true},
		{"smaller value loses a tie", live("a", 3), live("b", 3), false},
		{"an identical entry does not win", live("a", 3), live("a", 3), false},
		{"an identical tombstone does not win", tomb(3), tomb(3), false},
	}
	for _, c := range cases {
		if got := c.e.Wins(c.o); got != c.want {
			t.Errorf("%s: %+v.Wins(%+v) = %v, want %v (every replica must pick the same winner, or they never converge)",
				c.name, c.e, c.o, got, c.want)
		}
	}
}

func TestReplica_ApplyKeepsTheWinnerAndIsIdempotent(t *testing.T) {
	r := NewReplica("a")
	if r.Name() != "a" {
		t.Fatalf("Name() = %q, want %q", r.Name(), "a")
	}
	if !r.Put("k", []byte("v1"), 10) {
		t.Fatal("first Put returned false; it changed the replica")
	}
	if r.Put("k", []byte("v1"), 10) {
		t.Error("re-applying the same write returned true; Apply must be idempotent (repair re-sends entries)")
	}
	if r.Put("k", []byte("old"), 5) {
		t.Error("an older write replaced a newer one")
	}
	if v, ok := r.Get("k"); !ok || string(v) != "v1" {
		t.Errorf("Get(k) = %q, %v; want \"v1\", true", v, ok)
	}
	if !r.Delete("k", 20) {
		t.Fatal("a newer Delete returned false")
	}
	if v, ok := r.Get("k"); ok {
		t.Errorf("Get after Delete = %q, true; a tombstone must hide the value", v)
	}
	e, ok := r.Lookup("k")
	if !ok || !e.Deleted || e.Stamp != 20 || e.Value != nil {
		t.Errorf("Lookup after Delete = %+v, %v; want a tombstone at stamp 20 with a nil Value", e, ok)
	}
	if r.Len() != 1 {
		t.Errorf("Len() = %d, want 1: a tombstone is an entry until Compact removes it", r.Len())
	}
	if r.Put("k", []byte("stale"), 15) {
		t.Error("a write older than the delete resurrected the key")
	}
	if !r.Put("k", []byte("v2"), 30) {
		t.Error("a write newer than the delete was rejected")
	}
	if v, ok := r.Get("k"); !ok || string(v) != "v2" {
		t.Errorf("Get = %q, %v; want \"v2\", true", v, ok)
	}
	if _, ok := r.Get("missing"); ok {
		t.Error("Get of a key that was never written returned ok")
	}
	if _, ok := r.Lookup("missing"); ok {
		t.Error("Lookup of a key that was never written returned ok")
	}
}

func TestReplica_CopiesValuesInAndOut(t *testing.T) {
	r := NewReplica("a")
	buf := []byte("hello")
	r.Put("k", buf, 1)
	buf[0] = 'J'
	if v, _ := r.Get("k"); string(v) != "hello" {
		t.Fatalf("after the caller reused its buffer, Get = %q; the replica must keep its own copy of the value", v)
	}
	v, _ := r.Get("k")
	v[0] = 'X'
	if v2, _ := r.Get("k"); string(v2) != "hello" {
		t.Fatalf("modifying the slice returned by Get changed the stored value to %q", v2)
	}
	e, _ := r.Lookup("k")
	e.Value[0] = 'Y'
	if v3, _ := r.Get("k"); string(v3) != "hello" {
		t.Fatalf("modifying the Entry returned by Lookup changed the stored value to %q", v3)
	}
	r.Apply(Entry{Key: "t", Value: []byte("ignored"), Stamp: 2, Deleted: true})
	if e, _ := r.Lookup("t"); e.Value != nil {
		t.Errorf("a tombstone was stored with Value %q; tombstones carry no value", e.Value)
	}
}

func TestReplica_CompactRemovesOnlyOldTombstones(t *testing.T) {
	r := NewReplica("a")
	r.Put("live-old", []byte("x"), 1)
	r.Put("gone-old", []byte("x"), 1)
	r.Delete("gone-old", 5)
	r.Put("gone-new", []byte("x"), 1)
	r.Delete("gone-new", 50)
	r.Delete("edge", 10)
	if n := r.Compact(10); n != 1 {
		t.Fatalf("Compact(10) removed %d entries, want 1 (only the tombstone at stamp 5; the cutoff is exclusive)", n)
	}
	if _, ok := r.Lookup("gone-old"); ok {
		t.Error("the tombstone at stamp 5 survived Compact(10)")
	}
	for _, k := range []string{"live-old", "gone-new", "edge"} {
		if _, ok := r.Lookup(k); !ok {
			t.Errorf("Compact(10) removed %q; it must remove only tombstones with Stamp < cutoff", k)
		}
	}
	if n := r.Compact(1000); n != 2 {
		t.Errorf("Compact(1000) removed %d entries, want 2 (the remaining tombstones; never a live value)", n)
	}
	if _, ok := r.Get("live-old"); !ok {
		t.Error("Compact removed a live value")
	}
}

// ---------- 2. buckets and trees ----------

func TestBucket_TopBitsOfTheToken(t *testing.T) {
	for _, k := range []string{"", "a", "member:000001", "hello world"} {
		for _, d := range []int{1, 4, 10, 20} {
			want := int(Token(k) >> (64 - uint(d)))
			if got := Bucket(k, d); got != want {
				t.Errorf("Bucket(%q, %d) = %d, want %d (the top %d bits of Token)", k, d, got, want, d)
			}
		}
		if got := Bucket(k, 0); got != 0 {
			t.Errorf("Bucket(%q, 0) = %d, want 0 (one leaf)", k, got)
		}
	}
	if Bucket("k", -1) != -1 || Bucket("k", MaxDepth+1) != -1 {
		t.Error("Bucket must return -1 for a depth outside [0, MaxDepth]")
	}
	// Sequential keys spread over the leaves.
	counts := make([]int, 64)
	for i := 0; i < 6400; i++ {
		counts[Bucket(tKey(i), 6)]++
	}
	for b, c := range counts {
		if c < 50 || c > 150 {
			t.Errorf("leaf %d holds %d of 6,400 sequential keys, want ≈ 100: keys must be hashed before bucketing", b, c)
		}
	}
}

func TestBuildTree_ShapeAndBadDepth(t *testing.T) {
	r := NewReplica("a")
	for _, d := range []int{-1, MaxDepth + 1} {
		if _, err := BuildTree(r, d); !errors.Is(err, ErrBadArgument) {
			t.Errorf("BuildTree(depth %d) error = %v, want ErrBadArgument", d, err)
		}
	}
	tr := tTree(t, r, 6)
	if tr.Depth() != 6 || tr.Leaves() != 64 {
		t.Fatalf("Depth, Leaves = %d, %d; want 6, 64", tr.Depth(), tr.Leaves())
	}
	if !tr.Root().IsZero() {
		t.Errorf("empty replica has root %v, want the zero Hash", tr.Root())
	}
	tFill(200, r)
	tr = tTree(t, r, 6)
	if tr.Root().IsZero() {
		t.Fatal("a replica with 200 keys has a zero root")
	}
	if tr.Node(0, 0) != tr.Root() {
		t.Error("Node(0, 0) must be the root")
	}
	if !tr.Node(7, 0).IsZero() || !tr.Node(6, 64).IsZero() || !tr.Node(-1, 0).IsZero() {
		t.Error("out-of-range Node must return the zero Hash")
	}
	// Every leaf that holds a key is non-zero; every empty leaf is zero.
	full := make(map[int]bool)
	for i := 0; i < 200; i++ {
		full[Bucket(tKey(i), 6)] = true
	}
	for i := 0; i < 64; i++ {
		if got := !tr.Node(6, i).IsZero(); got != full[i] {
			t.Errorf("leaf %d non-zero = %v, but it holds keys = %v", i, got, full[i])
		}
	}
}

func TestBuildTree_SameContentSameRootWhateverTheOrder(t *testing.T) {
	a, b := NewReplica("a"), NewReplica("b")
	for i := 0; i < 500; i++ {
		a.Put(tKey(i), []byte("x"), int64(i))
	}
	for i := 499; i >= 0; i-- {
		b.Put(tKey(i), []byte("x"), int64(i))
	}
	for _, d := range []int{0, 3, 10} {
		if ra, rb := tTree(t, a, d).Root(), tTree(t, b, d).Root(); ra != rb {
			t.Errorf("depth %d: roots %v and %v differ for identical content inserted in a different order (sort each leaf by key)", d, ra, rb)
		}
	}
}

func TestBuildTree_EveryFieldIsInTheHash(t *testing.T) {
	base := func() *Replica {
		r := NewReplica("x")
		r.Put("k1", []byte("v"), 10)
		r.Put("k2", []byte("w"), 10)
		return r
	}
	ref := tTree(t, base(), 0).Root()
	variants := map[string]func(r *Replica){
		"a newer stamp, same value":    func(r *Replica) { r.Put("k1", []byte("v"), 11) },
		"a different value":            func(r *Replica) { r.Put("k1", []byte("z"), 11) },
		"a tombstone instead":          func(r *Replica) { r.Delete("k1", 11) },
		"an extra key":                 func(r *Replica) { r.Put("k3", []byte("v"), 10) },
		"a tombstone for an extra key": func(r *Replica) { r.Delete("k3", 10) },
	}
	for name, mutate := range variants {
		r := base()
		mutate(r)
		if tTree(t, r, 0).Root() == ref {
			t.Errorf("%s: root unchanged; repair would never notice this divergence", name)
		}
	}
	// Ambiguous concatenation: ("ab","c") vs ("a","bc") must not collide.
	x, y := NewReplica("x"), NewReplica("y")
	x.Put("ab", []byte("c"), 1)
	y.Put("a", []byte("bc"), 1)
	if tTree(t, x, 0).Root() == tTree(t, y, 0).Root() {
		t.Error(`key "ab" value "c" hashes like key "a" value "bc": length-prefix the variable fields`)
	}
	// A tombstone and a live value at the same stamp.
	p, q := NewReplica("p"), NewReplica("q")
	p.Put("k", nil, 1)
	q.Delete("k", 1)
	if tTree(t, p, 0).Root() == tTree(t, q, 0).Root() {
		t.Error("an empty live value and a tombstone at the same stamp hash the same; the Deleted flag must be in the hash")
	}
}

// ---------- 3. diff and repair ----------

func TestDiff_IdenticalTreesCostOneComparison(t *testing.T) {
	a, b := NewReplica("a"), NewReplica("b")
	tFill(1000, a, b)
	got, cmp, err := Diff(tTree(t, a, 12), tTree(t, b, 12))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 || cmp != 1 {
		t.Errorf("Diff of identical trees = %v leaves, %d comparisons; want none and exactly 1 (compare the roots, stop)", got, cmp)
	}
}

func TestDiff_OneDifferenceCostsOnePlusTwoPerLevel(t *testing.T) {
	a, b := NewReplica("a"), NewReplica("b")
	tFill(5000, a, b)
	b.Put(tKey(1234), []byte("changed"), 200)
	for _, d := range []int{1, 8, 16} {
		got, cmp, err := Diff(tTree(t, a, d), tTree(t, b, d))
		if err != nil {
			t.Fatal(err)
		}
		want := Bucket(tKey(1234), d)
		if len(got) != 1 || got[0] != want {
			t.Errorf("depth %d: differing leaves %v, want [%d]", d, got, want)
		}
		if cmp != 1+2*d {
			t.Errorf("depth %d: %d comparisons, want %d (1 root + 2 children per level): the cost of a repair grows with depth, not with keys", d, cmp, 1+2*d)
		}
	}
}

func TestDiff_EverythingDifferentVisitsEveryNode(t *testing.T) {
	a, b := NewReplica("a"), NewReplica("b")
	for i := 0; i < 4000; i++ {
		a.Put(tKey(i), []byte("a"), 1)
		b.Put(tKey(i), []byte("b"), 1)
	}
	const d = 6
	got, cmp, err := Diff(tTree(t, a, d), tTree(t, b, d))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1<<d {
		t.Fatalf("%d differing leaves, want %d (every leaf holds keys with different values)", len(got), 1<<d)
	}
	if !sort.IntsAreSorted(got) {
		t.Errorf("differing leaves %v are not in ascending order", got)
	}
	if want := 1<<(d+1) - 1; cmp != want {
		t.Errorf("%d comparisons, want %d (every node of the tree once)", cmp, want)
	}
}

func TestDiff_RejectsDifferentDepths(t *testing.T) {
	r := NewReplica("a")
	if _, _, err := Diff(tTree(t, r, 3), tTree(t, r, 4)); !errors.Is(err, ErrBadArgument) {
		t.Errorf("Diff of depth 3 and depth 4 trees: error %v, want ErrBadArgument", err)
	}
}

func TestRepair_ShipsOnlyTheDifferingLeaves(t *testing.T) {
	const n, depth = 10000, 10
	a, b := NewReplica("a"), NewReplica("b")
	tFill(n, a, b)
	// Three divergences: a newer value on a, a key only b has, a delete only a saw.
	a.Put(tKey(17), []byte("newer"), 200)
	b.Put("member:only-on-b", []byte("x"), 150)
	a.Delete(tKey(9001), 300)

	wantBuckets := map[int]bool{
		Bucket(tKey(17), depth):           true,
		Bucket("member:only-on-b", depth): true,
		Bucket(tKey(9001), depth):         true,
	}
	wantSent := 0
	for bk := range wantBuckets {
		wantSent += len(a.BucketEntries(bk, depth)) + len(b.BucketEntries(bk, depth))
	}

	st, err := Repair(context.Background(), a, b, depth)
	if err != nil {
		t.Fatal(err)
	}
	if st.Buckets != len(wantBuckets) {
		t.Errorf("Stats.Buckets = %d, want %d", st.Buckets, len(wantBuckets))
	}
	if st.Applied != 3 {
		t.Errorf("Stats.Applied = %d, want 3 (one per divergent key; everything else was already equal)", st.Applied)
	}
	if st.Sent != wantSent {
		t.Errorf("Stats.Sent = %d, want %d (every entry of each differing leaf, both directions)", st.Sent, wantSent)
	}
	if st.Sent > n/50 {
		t.Errorf("Repair shipped %d entries for 3 divergent keys out of %d; anti-entropy must not ship the whole range", st.Sent, n)
	}
	if st.Comparisons < 1 || st.Comparisons > 1+2*depth*3 {
		t.Errorf("Stats.Comparisons = %d, want between 1 and %d", st.Comparisons, 1+2*depth*3)
	}
	if ra, rb := tTree(t, a, depth).Root(), tTree(t, b, depth).Root(); ra != rb {
		t.Fatalf("after Repair the roots differ (%v vs %v)", ra, rb)
	}
	tSame(t, a, b, append(tKeys(n), "member:only-on-b"))
	if v, _ := b.Get(tKey(17)); string(v) != "newer" {
		t.Errorf("b has %q for key 17, want the newer value", v)
	}
	if _, ok := b.Get(tKey(9001)); ok {
		t.Error("the delete did not reach b: repair must ship tombstones like values")
	}
	if v, ok := a.Get("member:only-on-b"); !ok || string(v) != "x" {
		t.Error("a did not receive the key only b had: repair runs in both directions")
	}

	st2, err := Repair(context.Background(), a, b, depth)
	if err != nil {
		t.Fatal(err)
	}
	if st2 != (Stats{Comparisons: 1}) {
		t.Errorf("a second Repair of converged replicas = %+v, want {Comparisons:1} and nothing else", st2)
	}
}

func TestRepair_RespectsContextAndBadDepth(t *testing.T) {
	a, b := NewReplica("a"), NewReplica("b")
	tFill(100, a)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Repair(ctx, a, b, 4); !errors.Is(err, context.Canceled) {
		t.Errorf("Repair with a cancelled context: error %v, want context.Canceled", err)
	}
	if b.Len() != 0 {
		t.Errorf("Repair with a cancelled context applied %d entries; it must check ctx before doing any work", b.Len())
	}
	if _, err := Repair(context.Background(), a, b, MaxDepth+1); !errors.Is(err, ErrBadArgument) {
		t.Errorf("Repair at depth %d: error %v, want ErrBadArgument", MaxDepth+1, err)
	}
}

func TestRepair_ConcurrentWritersAndRepairsConverge(t *testing.T) {
	rs := []*Replica{NewReplica("a"), NewReplica("b"), NewReplica("c")}
	tFill(2000, rs...)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	var writers, repairers sync.WaitGroup
	stop := make(chan struct{})
	for w := 0; w < 6; w++ {
		writers.Add(1)
		go func(w int) {
			defer writers.Done()
			rng := rand.New(rand.NewSource(int64(w)))
			r := rs[w%3]
			for i := 0; i < 400; i++ {
				k := tKey(rng.Intn(3000))
				if rng.Intn(5) == 0 {
					r.Delete(k, int64(1000+w*1000+i))
				} else {
					r.Put(k, []byte(fmt.Sprintf("w%d-%d", w, i)), int64(1000+w*1000+i))
				}
			}
		}(w)
	}
	for p := 0; p < 3; p++ {
		repairers.Add(1)
		go func(x, y *Replica) {
			defer repairers.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				if _, err := Repair(ctx, x, y, 8); err != nil {
					t.Errorf("Repair(%s, %s): %v", x.Name(), y.Name(), err)
					return
				}
			}
		}(rs[p], rs[(p+1)%3])
	}
	tWait(t, &writers, 15*time.Second, "writers")
	close(stop)
	tWait(t, &repairers, 15*time.Second, "concurrent Repairs on overlapping pairs")

	// Quiesced: a ring of repairs converges everyone.
	for _, pair := range [][2]int{{0, 1}, {1, 2}, {0, 1}} {
		if _, err := Repair(context.Background(), rs[pair[0]], rs[pair[1]], 8); err != nil {
			t.Fatal(err)
		}
	}
	keys := tKeys(3000)
	tSame(t, rs[0], rs[1], keys)
	tSame(t, rs[1], rs[2], keys)
}

// ---------- 4. tombstones, grace periods and zombies ----------

// tZombieSetup: three replicas hold k = "secret" at stamp 10. c goes down, and
// the delete at stamp 20 reaches only a and b.
func tZombieSetup() (a, b, c *Replica) {
	a, b, c = NewReplica("a"), NewReplica("b"), NewReplica("c")
	for _, r := range []*Replica{a, b, c} {
		r.Put("k", []byte("secret"), 10)
	}
	a.Delete("k", 20)
	b.Delete("k", 20)
	return a, b, c
}

func TestTombstones_RepairBeforeCompactionMakesTheDeleteStick(t *testing.T) {
	a, b, c := tZombieSetup()
	// c comes back; repair runs before any compaction.
	if _, err := Repair(context.Background(), a, c, 4); err != nil {
		t.Fatal(err)
	}
	if _, ok := c.Get("k"); ok {
		t.Fatal("after repair, c still serves the deleted value")
	}
	for _, r := range []*Replica{a, b, c} {
		r.Compact(1000)
	}
	if _, err := Repair(context.Background(), a, c, 4); err != nil {
		t.Fatal(err)
	}
	for _, r := range []*Replica{a, b, c} {
		if _, ok := r.Get("k"); ok {
			t.Errorf("%s serves k after delete, repair and compaction", r.Name())
		}
		if r.Len() != 0 {
			t.Errorf("%s still has %d entries after every replica compacted the tombstone", r.Name(), r.Len())
		}
	}
}

func TestTombstones_CompactionBeforeRepairResurrectsTheValue(t *testing.T) {
	a, b, c := tZombieSetup()
	// gc grace expires on a and b while c is still down: they drop the tombstone.
	a.Compact(1000)
	b.Compact(1000)
	// c comes back and anti-entropy runs.
	if _, err := Repair(context.Background(), a, c, 4); err != nil {
		t.Fatal(err)
	}
	v, ok := a.Get("k")
	if !ok || string(v) != "secret" {
		t.Fatalf("a.Get(k) = %q, %v. A correct repair copies c's value back, because a no longer has anything "+
			"that says it was deleted: this zombie is exactly why tombstones must outlive the longest outage + repair interval", v, ok)
	}
}

func TestSafeCutoff_IsTheOlderOfGraceAndLastFullRepair(t *testing.T) {
	cases := []struct{ now, grace, last, want int64 }{
		{1000, 100, 500, 500},  // last full repair is older: it wins
		{1000, 600, 900, 400},  // grace period is older: it wins
		{1000, 100, 2000, 900}, // a repair "in the future" never extends the cutoff past the grace period
	}
	for _, c := range cases {
		if got := SafeCutoff(c.now, c.grace, c.last); got != c.want {
			t.Errorf("SafeCutoff(now %d, grace %d, last full repair %d) = %d, want %d", c.now, c.grace, c.last, got, c.want)
		}
	}
	// The zombie scenario again, with the safe cutoff: the last repair that
	// covered c started at stamp 15, before the delete at 20.
	a, b, c := tZombieSetup()
	cut := SafeCutoff(1000, 100, 15)
	a.Compact(cut)
	b.Compact(cut)
	if _, err := Repair(context.Background(), a, c, 4); err != nil {
		t.Fatal(err)
	}
	if _, ok := a.Get("k"); ok {
		t.Error("with SafeCutoff the value still came back: a tombstone newer than the last full repair must survive compaction")
	}
	if _, ok := c.Get("k"); ok {
		t.Error("c still serves the deleted value: the surviving tombstone should have reached it")
	}
}
