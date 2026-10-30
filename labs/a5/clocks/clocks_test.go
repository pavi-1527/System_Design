package clocks

import (
	"fmt"
	"math/rand/v2"
	"reflect"
	"sort"
	"sync"
	"testing"
	"time"
)

// ---------- helpers (prefixed with t to avoid clashing with your code) ----------

// tWait waits for wg, failing the test instead of hanging if it takes longer
// than d (e.g. a deadlock in the implementation under test).
func tWait(t *testing.T, wg *sync.WaitGroup, d time.Duration, what string) {
	t.Helper()
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(d):
		t.Fatalf("%s: still running after %v; a lock is probably never released", what, d)
	}
}

func tVec(kv ...any) Vector {
	v := Vector{}
	for i := 0; i < len(kv); i += 2 {
		v[kv[i].(string)] = uint64(kv[i+1].(int))
	}
	return v
}

func tRandVec(r *rand.Rand) Vector {
	v := Vector{}
	for _, a := range []string{"a", "b", "c", "d"} {
		if n := r.IntN(4); n > 0 || r.IntN(5) == 0 { // sometimes an explicit zero entry
			v[a] = uint64(n)
		}
	}
	return v
}

func tValues(vs []Versioned) []string {
	out := make([]string, len(vs))
	for i, v := range vs {
		out[i] = v.Value
	}
	sort.Strings(out)
	return out
}

// ---------- Lamport clocks ----------

func TestLamport_TickCountsLocalEvents(t *testing.T) {
	var l Lamport
	if got := l.Time(); got != 0 {
		t.Fatalf("zero-value Lamport clock reads %d, want 0", got)
	}
	for want := uint64(1); want <= 3; want++ {
		if got := l.Tick(); got != want {
			t.Fatalf("Tick() = %d, want %d", got, want)
		}
	}
	if got := l.Time(); got != 3 {
		t.Fatalf("Time() = %d after three ticks, want 3 (Time must not advance the clock)", got)
	}
}

func TestLamport_ObserveJumpsPastTheSenderAndNeverGoesBackwards(t *testing.T) {
	var l Lamport
	l.Tick()
	l.Tick() // local time 2
	if got := l.Observe(10); got != 11 {
		t.Fatalf("Observe(10) at local time 2 = %d, want 11 = max(2,10)+1: a receive must sort after its send", got)
	}
	if got := l.Observe(5); got != 12 {
		t.Fatalf("Observe(5) at local time 11 = %d, want 12: an old message must not move the clock backwards", got)
	}
	if got := l.Observe(12); got != 13 {
		t.Fatalf("Observe(12) at local time 12 = %d, want 13 (still +1 when equal)", got)
	}
}

func TestLamport_ConcurrentTicksAreUniqueAndGapFree(t *testing.T) {
	const goroutines, per = 50, 200
	var l Lamport
	seen := make([][]uint64, goroutines)
	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < per; i++ {
				seen[g] = append(seen[g], l.Tick())
				if i%50 == 0 {
					_ = l.Time()
				}
			}
		}()
	}
	tWait(t, &wg, 5*time.Second, "concurrent Tick")
	all := map[uint64]bool{}
	for g, vals := range seen {
		for i, v := range vals {
			if i > 0 && v <= vals[i-1] {
				t.Fatalf("goroutine %d saw %d after %d: one caller's ticks must increase", g, v, vals[i-1])
			}
			if all[v] {
				t.Fatalf("time %d handed out twice: two events would get the same Lamport time", v)
			}
			all[v] = true
		}
	}
	if got := l.Time(); got != goroutines*per || len(all) != goroutines*per {
		t.Fatalf("final time %d with %d distinct values, want %d of each (lost update under contention)", got, len(all), goroutines*per)
	}
}

func TestStamp_TotalOrderBreaksTiesByNode(t *testing.T) {
	stamps := []Stamp{{5, "c"}, {3, "z"}, {5, "a"}, {3, "b"}, {4, "m"}}
	sort.Slice(stamps, func(i, j int) bool { return stamps[i].Less(stamps[j]) })
	want := []Stamp{{3, "b"}, {3, "z"}, {4, "m"}, {5, "a"}, {5, "c"}}
	if !reflect.DeepEqual(stamps, want) {
		t.Fatalf("sorted by Stamp.Less = %v, want %v", stamps, want)
	}
	a, b := Stamp{1, "x"}, Stamp{1, "x"}
	if a.Less(b) || b.Less(a) {
		t.Fatalf("equal stamps must not be Less than each other (irreflexive order)")
	}
}

// ---------- a small message-passing simulator ----------

type tEv struct {
	proc string
	kind string // "local", "send", "recv"
	msg  string // message name for send/recv
}

type tStamped struct {
	ev tEv
	l  uint64
	v  Vector
}

// tRun executes the script in order (a recv must come after its send) and
// returns every event with its Lamport time and vector clock, plus the
// ground-truth happened-before relation computed from program order and
// message edges alone (no clocks involved).
func tRun(script []tEv) ([]tStamped, [][]bool) {
	lam := map[string]*Lamport{}
	vec := map[string]Vector{}
	type wire struct {
		l uint64
		v Vector
	}
	inflight := map[string]wire{}
	out := make([]tStamped, len(script))
	for i, e := range script {
		if lam[e.proc] == nil {
			lam[e.proc] = &Lamport{}
		}
		switch e.kind {
		case "local":
			out[i] = tStamped{e, lam[e.proc].Tick(), nil}
			vec[e.proc] = vec[e.proc].Tick(e.proc)
		case "send":
			l := lam[e.proc].Tick()
			vec[e.proc] = vec[e.proc].Tick(e.proc)
			inflight[e.msg] = wire{l, vec[e.proc].Clone()}
			out[i] = tStamped{e, l, nil}
		case "recv":
			w := inflight[e.msg]
			out[i] = tStamped{e, lam[e.proc].Observe(w.l), nil}
			vec[e.proc] = Merge(vec[e.proc], w.v).Tick(e.proc)
		}
		out[i].v = vec[e.proc].Clone()
	}
	n := len(script)
	hb := make([][]bool, n)
	for i := range hb {
		hb[i] = make([]bool, n)
	}
	lastOn := map[string]int{}
	sendAt := map[string]int{}
	for i, e := range script {
		if p, ok := lastOn[e.proc]; ok {
			hb[p][i] = true
		}
		lastOn[e.proc] = i
		if e.kind == "send" {
			sendAt[e.msg] = i
		}
		if e.kind == "recv" {
			hb[sendAt[e.msg]][i] = true
		}
	}
	for k := 0; k < n; k++ { // transitive closure
		for i := 0; i < n; i++ {
			for j := 0; j < n; j++ {
				if hb[i][k] && hb[k][j] {
					hb[i][j] = true
				}
			}
		}
	}
	return out, hb
}

var tScript = []tEv{
	{"P", "local", ""},  // 0
	{"P", "send", "m1"}, // 1
	{"Q", "local", ""},  // 2  concurrent with P's first two events
	{"Q", "local", ""},  // 3
	{"Q", "local", ""},  // 4
	{"Q", "recv", "m1"}, // 5  Lamport jumps to max(3,2)+1 = 4
	{"R", "local", ""},  // 6  concurrent with everything on P and Q so far
	{"Q", "send", "m2"}, // 7
	{"R", "recv", "m2"}, // 8
	{"P", "local", ""},  // 9  concurrent with Q's receive
	{"R", "send", "m3"}, // 10
	{"P", "recv", "m3"}, // 11
}

func TestClocks_CausalityImpliesSmallerLamportTime(t *testing.T) {
	evs, hb := tRun(tScript)
	for i := range evs {
		for j := range evs {
			if hb[i][j] && evs[i].l >= evs[j].l {
				t.Errorf("event %d happened-before event %d but Lamport times are %d and %d: a cause must always have a smaller time than its effect", i, j, evs[i].l, evs[j].l)
			}
		}
	}
}

func TestVector_CompareMatchesTrueHappenedBefore(t *testing.T) {
	evs, hb := tRun(tScript)
	for i := range evs {
		for j := range evs {
			if i == j {
				continue
			}
			got := Compare(evs[i].v, evs[j].v)
			var want Ordering
			switch {
			case hb[i][j]:
				want = Before
			case hb[j][i]:
				want = After
			default:
				want = Concurrent
			}
			if got != want {
				t.Errorf("Compare(event %d %v, event %d %v) = %v, want %v", i, evs[i].v, j, evs[j].v, got, want)
			}
		}
	}
}

func TestLamport_CannotDetectConcurrency(t *testing.T) {
	evs, hb := tRun(tScript)
	found := 0
	for i := range evs {
		for j := range evs {
			if i != j && !hb[i][j] && !hb[j][i] && evs[i].l < evs[j].l {
				found++
				if Compare(evs[i].v, evs[j].v) != Concurrent {
					t.Errorf("events %d and %d are concurrent but vectors say %v", i, j, Compare(evs[i].v, evs[j].v))
				}
			}
		}
	}
	if found == 0 {
		t.Fatalf("expected at least one concurrent pair whose Lamport times are ordered (L(a) < L(b) does not imply a happened-before b); the Lamport clock in this script is not behaving like a Lamport clock")
	}
}

// ---------- vector clocks ----------

func TestVectorTick_ReturnsACopyAndNilIsEmpty(t *testing.T) {
	orig := tVec("a", 1)
	got := orig.Tick("a").Tick("b")
	if !reflect.DeepEqual(got, tVec("a", 2, "b", 1)) {
		t.Fatalf("Tick result = %v, want {a:2 b:1}", got)
	}
	if !reflect.DeepEqual(orig, tVec("a", 1)) {
		t.Fatalf("Tick mutated its receiver: %v; clocks are shared between versions, so mutation corrupts other replicas' data", orig)
	}
	var nilClock Vector
	if got := nilClock.Tick("x"); !reflect.DeepEqual(got, tVec("x", 1)) {
		t.Fatalf("nil.Tick(x) = %v, want {x:1}", got)
	}
}

func TestCompare_Table(t *testing.T) {
	cases := []struct {
		name string
		a, b Vector
		want Ordering
	}{
		{"both empty", nil, Vector{}, Equal},
		{"identical", tVec("a", 2, "b", 1), tVec("a", 2, "b", 1), Equal},
		{"explicit zero equals missing", tVec("a", 0, "b", 1), tVec("b", 1), Equal},
		{"a behind b on one actor", tVec("a", 1), tVec("a", 2), Before},
		{"b has an extra actor", tVec("a", 1), tVec("a", 1, "b", 1), Before},
		{"empty is before anything", nil, tVec("a", 1), Before},
		{"a ahead", tVec("a", 3, "b", 1), tVec("a", 2, "b", 1), After},
		{"a has extra actor", tVec("a", 1, "c", 4), tVec("a", 1), After},
		{"each ahead on a different actor", tVec("a", 2, "b", 1), tVec("a", 1, "b", 2), Concurrent},
		{"disjoint actors", tVec("a", 1), tVec("b", 1), Concurrent},
		{"same count, different actor (two writes from the same base)", tVec("a", 1, "x", 1), tVec("a", 1, "y", 1), Concurrent},
	}
	for _, c := range cases {
		if got := Compare(c.a, c.b); got != c.want {
			t.Errorf("%s: Compare(%v, %v) = %v, want %v", c.name, c.a, c.b, got, c.want)
		}
	}
}

func TestVectorLaws_RandomClocks(t *testing.T) {
	r := rand.New(rand.NewPCG(7, 11))
	flip := map[Ordering]Ordering{Equal: Equal, Before: After, After: Before, Concurrent: Concurrent}
	for i := 0; i < 500; i++ {
		a, b, c := tRandVec(r), tRandVec(r), tRandVec(r)
		if Compare(a, b) != flip[Compare(b, a)] {
			t.Fatalf("Compare is not antisymmetric for %v, %v: %v vs %v", a, b, Compare(a, b), Compare(b, a))
		}
		if !reflect.DeepEqual(Merge(a, b), Merge(b, a)) {
			t.Fatalf("Merge is not commutative for %v, %v", a, b)
		}
		if !reflect.DeepEqual(Merge(Merge(a, b), c), Merge(a, Merge(b, c))) {
			t.Fatalf("Merge is not associative for %v, %v, %v", a, b, c)
		}
		if !reflect.DeepEqual(Merge(a, a), a.Clone()) {
			t.Fatalf("Merge is not idempotent for %v", a)
		}
		m := Merge(a, b)
		if !m.Descends(a) || !m.Descends(b) {
			t.Fatalf("Merge(%v, %v) = %v must descend both inputs (it is their least upper bound)", a, b, m)
		}
		if ord := Compare(a, b); (ord == After || ord == Equal) && !a.Descends(b) {
			t.Fatalf("Compare says %v but Descends(%v, %v) is false", ord, a, b)
		}
	}
}

func TestMerge_PointwiseMaxWithoutAliasingOrZeroEntries(t *testing.T) {
	a, b := tVec("a", 3, "b", 0), tVec("b", 0, "c", 2, "a", 1)
	m := Merge(a, b)
	if want := tVec("a", 3, "c", 2); !reflect.DeepEqual(m, want) {
		t.Fatalf("Merge = %v, want %v (no zero entries)", m, want)
	}
	m["a"] = 99
	m["z"] = 1
	if a["a"] != 3 || b["a"] != 1 || len(a) != 2 || len(b) != 3 {
		t.Fatalf("mutating Merge's result changed an input: a=%v b=%v", a, b)
	}
	if got := Merge(nil, nil); got == nil || len(got) != 0 {
		t.Fatalf("Merge(nil, nil) = %#v, want an empty non-nil Vector", got)
	}
}

// ---------- Reconcile ----------

func TestReconcile_DropsDominatedKeepsConcurrent(t *testing.T) {
	vs := []Versioned{
		{tVec("a", 1), "v1"},
		{tVec("a", 1, "b", 1), "v2-b"},    // supersedes v1
		{tVec("a", 1, "c", 1), "v2-c"},    // concurrent with v2-b, supersedes v1
		{tVec("a", 1, "b", 1), "v2-b"},    // exact duplicate of the second entry
		{tVec("d", 1), "unrelated-write"}, // concurrent with everything
	}
	got := Reconcile(vs)
	if want := []string{"unrelated-write", "v2-b", "v2-c"}; !reflect.DeepEqual(tValues(got), want) {
		t.Fatalf("Reconcile kept %v, want %v: dominated versions must go, concurrent ones must stay, duplicates collapse", tValues(got), want)
	}
	if want := []string{"v2-b", "v2-c", "unrelated-write"}; !reflect.DeepEqual(valuesInOrder(got), want) {
		t.Fatalf("result order = %v, want first-seen order %v", valuesInOrder(got), want)
	}
}

func TestReconcile_EqualClocksWithDifferentValuesAreBothKept(t *testing.T) {
	// This should not happen when every actor ticks only its own entry, but if
	// it ever does, silently picking one would lose a write.
	got := Reconcile([]Versioned{{tVec("a", 1), "left"}, {tVec("a", 1), "right"}})
	if want := []string{"left", "right"}; !reflect.DeepEqual(tValues(got), want) {
		t.Fatalf("Reconcile kept %v, want both %v: only exact duplicates (same clock AND value) may be collapsed", tValues(got), want)
	}
}

func valuesInOrder(vs []Versioned) []string {
	out := make([]string, len(vs))
	for i, v := range vs {
		out[i] = v.Value
	}
	return out
}

func TestReconcile_DoesNotMutateInputAndHandlesEmpty(t *testing.T) {
	if got := Reconcile(nil); len(got) != 0 {
		t.Fatalf("Reconcile(nil) = %v, want empty", got)
	}
	in := []Versioned{{tVec("a", 1), "x"}, {tVec("a", 2), "y"}}
	snapshot := []Versioned{{tVec("a", 1), "x"}, {tVec("a", 2), "y"}}
	out := Reconcile(in)
	if len(out) == 0 {
		t.Fatalf("Reconcile returned nothing for a non-empty input")
	}
	out[0].Clock["a"] = 77
	out[0].Value = "changed"
	if !reflect.DeepEqual(in, snapshot) {
		t.Fatalf("Reconcile aliased its input: %v, want %v", in, snapshot)
	}
}

func TestReconcile_ResultDoesNotDependOnInputOrder(t *testing.T) {
	base := []Versioned{
		{tVec("a", 1), "v1"},
		{tVec("a", 2), "v2"},
		{tVec("a", 1, "b", 1), "v2b"},
		{tVec("a", 2, "c", 1), "v3c"},
		{tVec("a", 2, "c", 1), "v3c"},
		{tVec("d", 1), "solo"},
	}
	want := Reconcile(base)
	r := rand.New(rand.NewPCG(3, 4))
	for i := 0; i < 50; i++ {
		perm := append([]Versioned(nil), base...)
		r.Shuffle(len(perm), func(i, j int) { perm[i], perm[j] = perm[j], perm[i] })
		if got := Reconcile(perm); !reflect.DeepEqual(tValues(got), tValues(want)) {
			t.Fatalf("Reconcile of a permutation kept %v, want %v: replicas that read in different orders would disagree", tValues(got), tValues(want))
		}
	}
	if !reflect.DeepEqual(tValues(want), []string{"solo", "v2b", "v3c"}) {
		t.Fatalf("Reconcile kept %v, want [solo v2b v3c]", tValues(want))
	}
}

// ---------- last-write-wins ----------

func TestLWW_HighestTimestampWinsAndTiesConverge(t *testing.T) {
	var r LWWRegister
	if _, _, ok := r.Get(); ok {
		t.Fatalf("empty register reports a value")
	}
	if !r.Write("a", 10) || !r.Write("b", 20) {
		t.Fatalf("writes with increasing timestamps must be accepted")
	}
	if r.Write("old", 15) {
		t.Fatalf("a write with an older timestamp was accepted")
	}
	if v, ts, _ := r.Get(); v != "b" || ts != 20 {
		t.Fatalf("Get = (%q, %d), want (b, 20)", v, ts)
	}
	// Equal timestamps: both replicas must pick the same winner whatever order they see them in.
	var x, y LWWRegister
	x.Write("left", 5)
	x.Write("right", 5)
	y.Write("right", 5)
	y.Write("left", 5)
	vx, _, _ := x.Get()
	vy, _, _ := y.Get()
	if vx != vy {
		t.Fatalf("replicas diverged on a timestamp tie: %q vs %q; ties need a deterministic tie-break or replicas never converge", vx, vy)
	}
	if vx != "right" {
		t.Fatalf("tie-break picked %q, want \"right\" (the lexicographically greater value, as documented)", vx)
	}
}

func TestLWW_MergeConvergesInAnyOrder(t *testing.T) {
	var a, b, c LWWRegister
	a.Write("from-a", 100)
	b.Write("from-b", 300)
	c.Write("from-c", 200)
	x, y := a, a
	x.Merge(&b)
	x.Merge(&c)
	y.Merge(&c)
	y.Merge(&b)
	var empty LWWRegister
	y.Merge(&empty)
	y.Merge(nil)
	vx, _, _ := x.Get()
	vy, _, _ := y.Get()
	if vx != "from-b" || vy != "from-b" {
		t.Fatalf("merge results %q and %q, want from-b for both (highest timestamp)", vx, vy)
	}
}

// The core data-loss story: Bob READ Alice's value and then changed it, so his
// write is causally later. But his laptop's clock is 30 s slow.
func TestLWW_CausallyLaterWriteIsLostUnderClockSkew(t *testing.T) {
	const skew = -30_000 // Bob's clock is 30 s behind (ms)
	realNow := int64(1_700_000_000_000)
	var replica LWWRegister

	replica.Write("limit=500", realNow) // Alice, accurate clock
	bobSaw, _, _ := replica.Get()
	if bobSaw != "limit=500" {
		t.Fatalf("setup broken")
	}
	accepted := replica.Write("limit=800", realNow+2_000+skew) // Bob, 2 s later by the wall clock, stamped with a slow clock

	got, _, _ := replica.Get()
	if accepted || got != "limit=500" {
		t.Fatalf("expected last-write-wins to DISCARD Bob's causally later write (accepted=%v, value=%q); this test documents the data loss, so an LWW register that keeps it is not last-write-wins by timestamp", accepted, got)
	}
}

func TestLWW_ConcurrentWritesSilentlyLoseOne(t *testing.T) {
	var r1, r2 LWWRegister
	r1.Write("alice@new.example", 1_000)
	r2.Write("alice@work.example", 1_001) // concurrent: neither writer saw the other
	a, b := r1, r2
	a.Merge(&r2)
	b.Merge(&r1)
	va, _, _ := a.Get()
	vb, _, _ := b.Get()
	if va != vb {
		t.Fatalf("replicas diverged after merging: %q vs %q", va, vb)
	}
	if va != "alice@work.example" {
		t.Fatalf("winner = %q, want the higher timestamp; the other write is gone and nobody was told", va)
	}
}

// ---------- siblings ----------

func TestSibling_ConcurrentWritesAreBothKept(t *testing.T) {
	var r1, r2 SiblingRegister
	r1.Put("alice", nil, "alice@new.example")
	r2.Put("bob", nil, "alice@work.example")
	a, b := r1, r2
	a.Merge(&r2)
	b.Merge(&r1)
	va, ctxA := a.Get()
	vb, ctxB := b.Get()
	if want := []string{"alice@new.example", "alice@work.example"}; !reflect.DeepEqual(tValues(va), want) || !reflect.DeepEqual(tValues(vb), want) {
		t.Fatalf("after merge replicas hold %v and %v, want both writes on both replicas: concurrent writes must be kept as siblings, not dropped", tValues(va), tValues(vb))
	}
	if !reflect.DeepEqual(ctxA, tVec("alice", 1, "bob", 1)) || !reflect.DeepEqual(ctxB, ctxA) {
		t.Fatalf("contexts %v and %v, want {alice:1 bob:1}: the context must cover every sibling the reader saw", ctxA, ctxB)
	}
}

func TestSibling_WriteWithContextSupersedesWhatItSaw(t *testing.T) {
	var r1, r2 SiblingRegister
	r1.Put("alice", nil, "A")
	r2.Put("bob", nil, "B")
	r1.Merge(&r2)
	vs, ctx := r1.Get()
	if len(vs) != 2 {
		t.Fatalf("setup: want 2 siblings, got %d", len(vs))
	}
	clock := r1.Put("carol", ctx, "A+B merged")
	got, _ := r1.Get()
	if len(got) != 1 || got[0].Value != "A+B merged" {
		t.Fatalf("after a resolving write the register holds %v, want just the merged value", tValues(got))
	}
	if !clock.Descends(tVec("alice", 1, "bob", 1)) || clock["carol"] != 1 {
		t.Fatalf("resolving write clock = %v, want it to descend both siblings and carry carol:1", clock)
	}
	// A replica that still holds the old siblings converges to the resolution after a merge.
	r2.Merge(&r1)
	got2, _ := r2.Get()
	if len(got2) != 1 || got2[0].Value != "A+B merged" {
		t.Fatalf("stale replica holds %v after merging the resolved one, want the resolution", tValues(got2))
	}
}

// Same scenario as TestLWW_CausallyLaterWriteIsLostUnderClockSkew: no
// timestamps are involved, so the skew cannot hurt.
func TestSibling_CausallyLaterWriteSurvivesClockSkew(t *testing.T) {
	var replica SiblingRegister
	replica.Put("alice", nil, "limit=500")
	seen, ctx := replica.Get() // Bob reads, and gets a context
	if len(seen) != 1 {
		t.Fatalf("setup: want 1 version, got %d", len(seen))
	}
	replica.Put("bob", ctx, "limit=800")
	got, _ := replica.Get()
	if len(got) != 1 || got[0].Value != "limit=800" {
		t.Fatalf("register holds %v, want only limit=800: a write made after reading must supersede it regardless of any wall clock", tValues(got))
	}
}

func TestSibling_StaleWriteIsDropped(t *testing.T) {
	var r SiblingRegister
	r.Put("a", nil, "v1")
	_, ctx := r.Get()
	r.Put("a", ctx, "v2") // stored clock {a:2}
	_, ctx2 := r.Get()
	r.Put("b", ctx2, "v3") // stored clock {a:2 b:1}
	// A client holding the ancient context {a:1} writes as b: clock {a:1 b:1}, which the stored {a:2 b:1} strictly descends.
	r.Put("b", tVec("a", 1), "late")
	got, _ := r.Get()
	if want := []string{"v3"}; !reflect.DeepEqual(tValues(got), want) {
		t.Fatalf("register holds %v, want %v: a write that is strictly older than what is stored must not resurrect itself", tValues(got), want)
	}
}

func TestSibling_GetReturnsCopiesAndEmptyRegisterIsEmpty(t *testing.T) {
	var r SiblingRegister
	if vs, ctx := r.Get(); len(vs) != 0 || len(ctx) != 0 {
		t.Fatalf("empty register Get = (%v, %v), want nothing", vs, ctx)
	}
	r.Put("a", nil, "x")
	vs, ctx := r.Get()
	if len(vs) == 0 || ctx == nil {
		t.Fatalf("Get after a Put returned (%v, %v), want one version and a context", vs, ctx)
	}
	vs[0].Value = "tampered"
	vs[0].Clock["a"] = 42
	ctx["a"] = 42
	again, ctx2 := r.Get()
	if len(again) == 0 || again[0].Value != "x" || again[0].Clock["a"] != 1 || ctx2["a"] != 1 {
		t.Fatalf("Get leaked internal state: %v %v", again, ctx2)
	}
}

func TestSibling_ReplicasConvergeWhateverTheMergeOrder(t *testing.T) {
	r := rand.New(rand.NewPCG(5, 6))
	for round := 0; round < 30; round++ {
		reps := make([]SiblingRegister, 3)
		for i := range reps {
			reps[i].Put(fmt.Sprintf("n%d", i), nil, fmt.Sprintf("base-%d", i))
		}
		// some replicas also overwrite after reading their own state
		for i := range reps {
			if r.IntN(2) == 0 {
				_, ctx := reps[i].Get()
				reps[i].Put(fmt.Sprintf("n%d", i), ctx, fmt.Sprintf("edit-%d", i))
			}
		}
		// gossip in a random order until everyone has merged with everyone, twice
		for pass := 0; pass < 2; pass++ {
			for _, i := range r.Perm(3) {
				for _, j := range r.Perm(3) {
					if i != j {
						reps[i].Merge(&reps[j])
					}
				}
			}
		}
		first, _ := reps[0].Get()
		for i := 1; i < 3; i++ {
			other, _ := reps[i].Get()
			if !reflect.DeepEqual(tValues(first), tValues(other)) {
				t.Fatalf("round %d: replica 0 holds %v but replica %d holds %v after full gossip: Merge must be a join (commutative, associative, idempotent)", round, tValues(first), i, tValues(other))
			}
		}
	}
}
