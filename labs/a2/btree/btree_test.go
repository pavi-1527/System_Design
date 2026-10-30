package btree

import (
	"fmt"
	"math"
	"math/rand/v2"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// ---------- helpers (prefixed with t to avoid clashing with your code) ----------

func tNew(t *testing.T, maxKeys int) *Tree {
	t.Helper()
	tr, err := New(maxKeys)
	if err != nil {
		t.Fatalf("New(%d): %v", maxKeys, err)
	}
	return tr
}

func tKey(i int) string { return fmt.Sprintf("k%08d", i) }

func tPut(t *testing.T, tr *Tree, k, v string) bool {
	t.Helper()
	replaced, err := tr.Put(k, v)
	if err != nil {
		t.Fatalf("Put(%q): %v", k, err)
	}
	return replaced
}

func tCheck(t *testing.T, tr *Tree, ctx string) {
	t.Helper()
	if err := tr.Check(); err != nil {
		t.Fatalf("%s: tree invariant violated: %v", ctx, err)
	}
}

func tKeysOf(es []Entry) []string {
	out := make([]string, len(es))
	for i, e := range es {
		out[i] = e.Key
	}
	return out
}

// tModel is a sorted-slice reference implementation.
type tModel struct{ m map[string]string }

func (tm *tModel) scan(lo, hi string, limit int) []Entry {
	keys := make([]string, 0, len(tm.m))
	for k := range tm.m {
		if k >= lo && (hi == "" || k < hi) {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	if limit > 0 && len(keys) > limit {
		keys = keys[:limit]
	}
	out := make([]Entry, len(keys))
	for i, k := range keys {
		out[i] = Entry{k, tm.m[k]}
	}
	return out
}

func tEqualEntries(a, b []Entry) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func tSeq(n int, desc bool) []int {
	s := make([]int, n)
	for i := range s {
		if desc {
			s[i] = n - 1 - i
		} else {
			s[i] = i
		}
	}
	return s
}

// ---------- construction and basics ----------

func TestNew_RejectsNodesTooSmallToSplit(t *testing.T) {
	for _, n := range []int{-1, 0, 1, 2} {
		if _, err := New(n); err == nil {
			t.Errorf("New(%d) succeeded; a node that holds fewer than 3 keys cannot split into two usable halves", n)
		}
	}
	if _, err := New(3); err != nil {
		t.Errorf("New(3): %v", err)
	}
}

func TestPut_RejectsEmptyKey(t *testing.T) {
	tr := tNew(t, 4)
	if _, err := tr.Put("", "x"); err != ErrEmptyKey {
		t.Fatalf("Put(\"\") error = %v, want ErrEmptyKey (the empty key means 'unbounded' in Scan)", err)
	}
}

func TestEmptyTree(t *testing.T) {
	tr := tNew(t, 4)
	if _, ok := tr.Get("a"); ok {
		t.Error("Get on an empty tree found a key")
	}
	if tr.Len() != 0 || tr.Height() != 1 {
		t.Errorf("empty tree: Len=%d Height=%d, want 0 and 1 (a lone empty leaf)", tr.Len(), tr.Height())
	}
	if got := tr.Scan("", "", 0); got == nil || len(got) != 0 {
		t.Errorf("Scan on empty tree = %#v, want a non-nil empty slice", got)
	}
	if tr.Delete("a") {
		t.Error("Delete on an empty tree reported success")
	}
	tCheck(t, tr, "empty tree")
}

func TestPutGet_ReplaceDoesNotGrowTheTree(t *testing.T) {
	tr := tNew(t, 4)
	if tPut(t, tr, "a", "1") {
		t.Error("first Put reported replaced=true")
	}
	if !tPut(t, tr, "a", "2") {
		t.Error("second Put of the same key reported replaced=false")
	}
	if v, ok := tr.Get("a"); !ok || v != "2" {
		t.Errorf("Get(a) = %q, %v; want 2, true", v, ok)
	}
	if tr.Len() != 1 {
		t.Errorf("Len = %d after replacing, want 1", tr.Len())
	}
	// Replace inside a multi-level tree too.
	for i := 0; i < 100; i++ {
		tPut(t, tr, tKey(i), "v")
	}
	before := tr.Len()
	if !tPut(t, tr, tKey(50), "new") {
		t.Error("replace in a deep tree reported replaced=false")
	}
	if tr.Len() != before {
		t.Errorf("Len changed from %d to %d on replace", before, tr.Len())
	}
	if v, _ := tr.Get(tKey(50)); v != "new" {
		t.Errorf("replaced value = %q, want new", v)
	}
	tCheck(t, tr, "after replace")
}

func TestKeys_AreOrderedBytewise(t *testing.T) {
	tr := tNew(t, 3)
	in := []string{"b", "a", "ab", "aa", "B", "ba", "a\x00", "z", "abc", "A"}
	for _, k := range in {
		tPut(t, tr, k, strings.ToUpper(k))
	}
	want := append([]string(nil), in...)
	sort.Strings(want)
	got := tKeysOf(tr.Scan("", "", 0))
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("scan order = %q, want %q (keys compare bytewise, a prefix sorts before its extensions)", got, want)
	}
	tCheck(t, tr, "bytewise order")
}

// ---------- splits ----------

func TestSplit_FirstLeafSplitMovesMiddleKeyUpAndGrowsTheRoot(t *testing.T) {
	tr := tNew(t, 4)
	for i := 1; i <= 4; i++ {
		tPut(t, tr, tKey(i), "v")
	}
	if st := tr.Stats(); st.Height != 1 || st.Leaves != 1 || st.Splits != 0 {
		t.Fatalf("4 keys at capacity 4: %+v; a node must not split until it overflows", st)
	}
	tPut(t, tr, tKey(5), "v")
	st := tr.Stats()
	if st.Height != 2 || st.Leaves != 2 || st.Internals != 1 || st.Splits != 1 {
		t.Fatalf("5th key: %+v; want height 2, 2 leaves, 1 internal (the new root), 1 split", st)
	}
	want := [][]string{{tKey(1), tKey(2)}, {tKey(3), tKey(4), tKey(5)}}
	if got := tr.LeafKeys(); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("leaves after the first split = %v, want %v (left keeps n/2 keys, right gets the rest)", got, want)
	}
	tCheck(t, tr, "first split")
	for i := 1; i <= 5; i++ {
		if _, ok := tr.Get(tKey(i)); !ok {
			t.Errorf("key %d lost by the split", i)
		}
	}

	tPut(t, tr, tKey(6), "v")
	tPut(t, tr, tKey(7), "v")
	want = [][]string{{tKey(1), tKey(2)}, {tKey(3), tKey(4)}, {tKey(5), tKey(6), tKey(7)}}
	if got := tr.LeafKeys(); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("leaves after 7 ascending keys = %v, want %v", got, want)
	}
	if h := tr.Height(); h != 2 {
		t.Errorf("height = %d, want 2 (a root with three leaves has room for more)", h)
	}
}

func TestSplit_InternalNodesSplitAndHeightGrowsExactlyWhenTheRootOverflows(t *testing.T) {
	// Capacity 3, ascending inserts. With the documented split rule the leaf
	// level has 2 keys per leaf, the root overflows at the 4th leaf (key 10),
	// and the next level overflows at key 28. These numbers follow from the
	// rule, so a different split position changes them.
	tr := tNew(t, 3)
	heightAt := map[int]int{}
	last := 1
	for i := 1; i <= 60; i++ {
		tPut(t, tr, tKey(i), "v")
		if h := tr.Height(); h != last {
			heightAt[h] = i
			last = h
		}
		tCheck(t, tr, fmt.Sprintf("after %d inserts", i))
	}
	if heightAt[2] != 4 || heightAt[3] != 10 || heightAt[4] != 28 {
		t.Fatalf("height grew to 2/3/4 at insert #%d/#%d/#%d, want #4/#10/#28 (leaf split keeps n/2 left; internal split pushes the middle key up)",
			heightAt[2], heightAt[3], heightAt[4])
	}
}

func TestSplit_KeysStayFindableThroughManySplitsAndInsertOrders(t *testing.T) {
	orders := map[string]func(n int) []int{
		"ascending":  func(n int) []int { return tSeq(n, false) },
		"descending": func(n int) []int { return tSeq(n, true) },
		"shuffled": func(n int) []int {
			s := tSeq(n, false)
			r := rand.New(rand.NewPCG(7, 7))
			r.Shuffle(len(s), func(i, j int) { s[i], s[j] = s[j], s[i] })
			return s
		},
	}
	for name, order := range orders {
		for _, mk := range []int{3, 4, 5, 8, 31} {
			t.Run(fmt.Sprintf("%s/maxKeys=%d", name, mk), func(t *testing.T) {
				tr := tNew(t, mk)
				ks := order(600)
				for n, i := range ks {
					tPut(t, tr, tKey(i), fmt.Sprint(i))
					if n%17 == 0 {
						tCheck(t, tr, fmt.Sprintf("after %d inserts", n+1))
					}
				}
				tCheck(t, tr, "final")
				for i := 0; i < 600; i++ {
					if v, ok := tr.Get(tKey(i)); !ok || v != fmt.Sprint(i) {
						t.Fatalf("Get(%d) = %q, %v", i, v, ok)
					}
				}
				got := tr.Scan("", "", 0)
				sorted := sort.SliceIsSorted(got, func(i, j int) bool { return got[i].Key < got[j].Key })
				if len(got) != 600 || !sorted {
					t.Fatalf("full scan returned %d entries (sorted=%v), want 600 in order", len(got), sorted)
				}
			})
		}
	}
}

func TestSplit_InsertOnlyLeavesAreAtLeastHalfFull(t *testing.T) {
	tr := tNew(t, 16)
	r := rand.New(rand.NewPCG(3, 4))
	for i := 0; i < 5000; i++ {
		tPut(t, tr, tKey(r.IntN(1<<30)), "v")
	}
	min := (tr.MaxKeys() + 1) / 2
	for i, l := range tr.LeafKeys() {
		if len(l) < min {
			t.Fatalf("leaf %d holds %d keys, want at least %d: a split must leave both halves at least half full", i, len(l), min)
		}
	}
}

// ---------- shape: height and fill factor ----------

func TestHeight_IsLogarithmicInTheFanout(t *testing.T) {
	// 100,000 random keys at 128 keys per node: ~1,100 leaves, ~17 internal
	// nodes, a root above them: three levels. Three page reads per lookup.
	tr := tNew(t, 128)
	r := rand.New(rand.NewPCG(1, 2))
	for i := 0; i < 100_000; i++ {
		tPut(t, tr, tKey(r.IntN(1<<30)), "v")
	}
	st := tr.Stats()
	if st.Height != 3 {
		t.Fatalf("height = %d for %d keys at 128 keys/node, want 3 (fan-out ~90 means log_90(1100) ~ 1.6 internal levels)", st.Height, st.Keys)
	}
	if st.Leaves < 1000 || st.Leaves > 1300 {
		t.Errorf("leaves = %d, want ~1,100-1,200 (about 70%% full leaves of 128)", st.Leaves)
	}
	// Generic bound for any capacity.
	for _, mk := range []int{3, 4, 7, 16, 50} {
		tr := tNew(t, mk)
		r := rand.New(rand.NewPCG(uint64(mk), 9))
		for i := 0; i < 20_000; i++ {
			tPut(t, tr, tKey(r.IntN(1<<30)), "v")
		}
		minFan := float64((mk + 1) / 2)
		minLeaf := float64((mk + 1) / 2)
		bound := 2 + int(math.Ceil(math.Log(float64(tr.Len())/minLeaf)/math.Log(minFan)))
		if h := tr.Height(); h > bound {
			t.Errorf("maxKeys=%d: height %d exceeds the B-tree bound %d for %d keys; the tree is not balanced", mk, h, bound, tr.Len())
		}
	}
}

func TestFillFactor_SequentialInsertsLeaveHalfEmptyPagesRandomInsertsAboutSeventyPercent(t *testing.T) {
	const n, mk = 20_000, 64
	seqT, rndT := tNew(t, mk), tNew(t, mk)
	r := rand.New(rand.NewPCG(1, 2))
	for i := 0; i < n; i++ {
		tPut(t, seqT, tKey(i), "v")
		tPut(t, rndT, tKey(r.IntN(1<<30)), "v")
	}
	seqFill, rndFill := seqT.Stats().LeafFill, rndT.Stats().LeafFill
	if seqFill < 0.48 || seqFill > 0.56 {
		t.Errorf("ascending inserts: mean leaf fill = %.2f, want ~0.50: every split leaves the left page half full and it never gets another key", seqFill)
	}
	if rndFill < 0.64 || rndFill > 0.76 {
		t.Errorf("random inserts: mean leaf fill = %.2f, want ~0.69 (ln 2), the classic steady-state B-tree utilisation", rndFill)
	}
}

// ---------- page reads ----------

func TestGet_ReadsExactlyOnePagePerLevel(t *testing.T) {
	tr := tNew(t, 8)
	for i := 0; i < 5000; i++ {
		tPut(t, tr, tKey(i), "v")
	}
	h := int64(tr.Height())
	for _, k := range []string{tKey(0), tKey(2500), tKey(4999), tKey(123456), "zzz"} {
		before := tr.Reads()
		tr.Get(k)
		if d := tr.Reads() - before; d != h {
			t.Errorf("Get(%q) visited %d pages, want %d (one per level, found or not)", k, d, h)
		}
	}
}

func TestScan_DescendsOnceThenFollowsTheLeafChain(t *testing.T) {
	tr := tNew(t, 6)
	for i := 0; i < 3000; i++ {
		tPut(t, tr, tKey(i), "v")
	}
	leaves := tr.LeafKeys()
	h := int64(tr.Height())
	first, last := 10, 30
	if len(leaves) <= last+1 {
		t.Fatalf("only %d leaves after 3000 inserts at capacity 6; expected hundreds (did Put split pages?)", len(leaves))
	}
	lo := leaves[first][0]
	hi := leaves[last][1] // stop inside leaf `last`, after its first key
	before := tr.Reads()
	got := tr.Scan(lo, hi, 0)
	reads := tr.Reads() - before
	wantReads := h + int64(last-first)
	if reads != wantReads {
		t.Fatalf("scan across leaves %d..%d visited %d pages, want %d = height %d + %d sibling hops (re-descending from the root per leaf costs %d)",
			first, last, reads, wantReads, h, last-first, int64(last-first+1)*h)
	}
	n := 0
	for i := first; i < last; i++ {
		n += len(leaves[i])
	}
	n++ // first key of the last leaf
	if len(got) != n {
		t.Errorf("scan returned %d entries, want %d", len(got), n)
	}
}

// ---------- scans ----------

func TestScan_BoundsLimitAndEdges(t *testing.T) {
	tr := tNew(t, 4)
	for i := 0; i < 100; i += 2 { // even keys only
		tPut(t, tr, tKey(i), fmt.Sprint(i))
	}
	cases := []struct {
		name      string
		lo, hi    string
		limit     int
		first     int // expected first key index, -1 for none
		wantCount int
	}{
		{"all", "", "", 0, 0, 50},
		{"lo inclusive hi exclusive", tKey(10), tKey(20), 0, 10, 5},
		{"lo between keys", tKey(11), tKey(20), 0, 12, 4},
		{"hi between keys", tKey(10), tKey(19), 0, 10, 5},
		{"open lo", "", tKey(6), 0, 0, 3},
		{"open hi", tKey(90), "", 0, 90, 5},
		{"limit", tKey(10), "", 3, 10, 3},
		{"limit larger than range", tKey(94), "", 50, 94, 3},
		{"empty range lo==hi", tKey(10), tKey(10), 0, -1, 0},
		{"inverted range", tKey(20), tKey(10), 0, -1, 0},
		{"beyond the end", tKey(1000), "", 0, -1, 0},
		{"before the start", "a", tKey(0), 0, -1, 0},
	}
	for _, c := range cases {
		got := tr.Scan(c.lo, c.hi, c.limit)
		if got == nil {
			t.Errorf("%s: Scan returned nil; want a non-nil empty slice", c.name)
			continue
		}
		if len(got) != c.wantCount {
			t.Errorf("%s: Scan(%q,%q,%d) returned %d entries, want %d", c.name, c.lo, c.hi, c.limit, len(got), c.wantCount)
			continue
		}
		if c.first >= 0 && got[0].Key != tKey(c.first) {
			t.Errorf("%s: first key %q, want %q", c.name, got[0].Key, tKey(c.first))
		}
	}
}

// ---------- delete ----------

func TestDelete_RemovesKeysAndKeepsTheTreeValid(t *testing.T) {
	tr := tNew(t, 4)
	for i := 0; i < 500; i++ {
		tPut(t, tr, tKey(i), "v")
	}
	for i := 0; i < 500; i += 2 {
		if !tr.Delete(tKey(i)) {
			t.Fatalf("Delete(%d) reported absent", i)
		}
	}
	if tr.Delete(tKey(0)) {
		t.Error("deleting an already deleted key reported success")
	}
	if tr.Len() != 250 {
		t.Errorf("Len = %d, want 250", tr.Len())
	}
	tCheck(t, tr, "after deleting half")
	for i := 0; i < 500; i++ {
		_, ok := tr.Get(tKey(i))
		if ok != (i%2 == 1) {
			t.Fatalf("Get(%d) found=%v after deleting evens", i, ok)
		}
	}
	got := tr.Scan("", "", 0)
	if len(got) != 250 || got[0].Key != tKey(1) {
		t.Fatalf("scan after deletes returned %d entries; want 250 starting at %q", len(got), tKey(1))
	}
}

func TestDelete_EverythingThenReuse(t *testing.T) {
	tr := tNew(t, 3)
	for i := 0; i < 200; i++ {
		tPut(t, tr, tKey(i), "v")
	}
	for i := 0; i < 200; i++ {
		tr.Delete(tKey(i))
	}
	if tr.Len() != 0 || len(tr.Scan("", "", 0)) != 0 {
		t.Fatalf("tree not empty after deleting everything: Len=%d", tr.Len())
	}
	if _, ok := tr.Get(tKey(7)); ok {
		t.Error("deleted key still found")
	}
	tCheck(t, tr, "after deleting everything")
	// Under-full and empty leaves must not block later inserts or scans.
	for i := 199; i >= 0; i-- {
		tPut(t, tr, tKey(i), "again")
	}
	tCheck(t, tr, "after reinserting")
	if got := tr.Scan(tKey(100), tKey(110), 0); len(got) != 10 {
		t.Errorf("scan after reuse returned %d entries, want 10", len(got))
	}
}

func TestDelete_ScanSkipsEmptyLeaves(t *testing.T) {
	tr := tNew(t, 4)
	for i := 0; i < 400; i++ {
		tPut(t, tr, tKey(i), "v")
	}
	// Empty out a run of whole leaves in the middle.
	leaves := tr.LeafKeys()
	if len(leaves) < 45 {
		t.Fatalf("only %d leaves after 400 inserts at capacity 4; expected over a hundred (did Put split pages?)", len(leaves))
	}
	for _, l := range leaves[20:40] {
		for _, k := range l {
			tr.Delete(k)
		}
	}
	got := tr.Scan("", "", 0)
	if len(got) != tr.Len() || tr.Len() == 0 {
		t.Fatalf("scan returned %d entries but Len is %d: the leaf chain must walk through empty leaves", len(got), tr.Len())
	}
	if got := tr.Scan(leaves[19][0], leaves[41][0], 0); len(got) == 0 {
		t.Error("scan across a run of empty leaves returned nothing")
	}
	tCheck(t, tr, "after emptying leaves")
}

// ---------- randomised model check ----------

func TestRandomOps_MatchASortedModel(t *testing.T) {
	for _, mk := range []int{3, 4, 5, 8, 16} {
		for seed := uint64(1); seed <= 3; seed++ {
			t.Run(fmt.Sprintf("maxKeys=%d/seed=%d", mk, seed), func(t *testing.T) {
				tr := tNew(t, mk)
				model := &tModel{m: map[string]string{}}
				r := rand.New(rand.NewPCG(seed, uint64(mk)))
				const space = 400
				for op := 0; op < 4000; op++ {
					k := tKey(r.IntN(space))
					switch x := r.IntN(10); {
					case x < 5:
						v := fmt.Sprint(op)
						_, existed := model.m[k]
						if got := tPut(t, tr, k, v); got != existed {
							t.Fatalf("op %d Put(%s) replaced=%v, model says existed=%v", op, k, got, existed)
						}
						model.m[k] = v
					case x < 7:
						_, existed := model.m[k]
						if got := tr.Delete(k); got != existed {
							t.Fatalf("op %d Delete(%s) = %v, model says existed=%v", op, k, got, existed)
						}
						delete(model.m, k)
					case x < 9:
						want, wok := model.m[k]
						if got, ok := tr.Get(k); ok != wok || got != want {
							t.Fatalf("op %d Get(%s) = %q,%v; model %q,%v", op, k, got, ok, want, wok)
						}
					default:
						lo, hi := tKey(r.IntN(space)), tKey(r.IntN(space))
						if r.IntN(4) == 0 {
							lo = ""
						}
						if r.IntN(4) == 0 {
							hi = ""
						}
						limit := r.IntN(12)
						if got, want := tr.Scan(lo, hi, limit), model.scan(lo, hi, limit); !tEqualEntries(got, want) {
							t.Fatalf("op %d Scan(%q,%q,%d):\n got  %v\n want %v", op, lo, hi, limit, tKeysOf(got), tKeysOf(want))
						}
					}
					if tr.Len() != len(model.m) {
						t.Fatalf("op %d: Len = %d, model has %d", op, tr.Len(), len(model.m))
					}
					if op%40 == 0 {
						tCheck(t, tr, fmt.Sprintf("op %d", op))
					}
				}
				tCheck(t, tr, "end")
			})
		}
	}
}

// ---------- concurrency ----------

func TestConcurrent_ReadersSeeAConsistentTreeWhileAWriterSplitsPages(t *testing.T) {
	tr := tNew(t, 6)
	const n = 6000
	var published atomic.Int64 // keys 0..published-1 are fully inserted
	var wg sync.WaitGroup
	errc := make(chan error, 16)
	fail := func(format string, a ...any) {
		select {
		case errc <- fmt.Errorf(format, a...):
		default:
		}
	}
	stop := make(chan struct{})

	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < n; i++ {
			if _, err := tr.Put(tKey(i), "v"); err != nil {
				fail("Put: %v", err)
				return
			}
			published.Store(int64(i + 1))
		}
	}()
	for g := 0; g < 4; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			r := rand.New(rand.NewPCG(uint64(g), 5))
			for {
				select {
				case <-stop:
					return
				default:
				}
				if p := int(published.Load()); p > 0 {
					k := r.IntN(p)
					if _, ok := tr.Get(tKey(k)); !ok {
						fail("key %d was published but Get could not find it", k)
						return
					}
				}
				got := tr.Scan(tKey(r.IntN(n)), "", 50)
				for i := 1; i < len(got); i++ {
					if got[i-1].Key >= got[i].Key {
						fail("scan out of order: %q then %q", got[i-1].Key, got[i].Key)
						return
					}
				}
			}
		}(g)
	}
	done := make(chan struct{})
	go func() {
		for published.Load() < n {
			time.Sleep(time.Millisecond)
		}
		close(stop)
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case err := <-errc:
		close(stop)
		t.Fatal(err)
	case <-time.After(20 * time.Second):
		close(stop)
		t.Fatal("timed out: writer or readers are stuck (deadlock?)")
	}
	select {
	case err := <-errc:
		t.Fatal(err)
	default:
	}
	tCheck(t, tr, "after concurrent run")
	if tr.Len() != n {
		t.Errorf("Len = %d, want %d", tr.Len(), n)
	}
}
