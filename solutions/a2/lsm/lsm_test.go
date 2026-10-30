package lsm

import (
	"fmt"
	"math"
	"math/rand/v2"
	"reflect"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// ---------- helpers (prefixed with t to avoid clashing with your code) ----------

func tKey(i int) string { return fmt.Sprintf("k%06d", i) }

// tRandKey maps i to a pseudo-random 16-hex-digit key, so the keys of every
// table span nearly the whole key range (range checks cannot rescue reads).
func tRandKey(i int) string {
	z := uint64(i) + 0x9e3779b97f4a7c15
	z = (z ^ (z >> 30)) * 0xbf58476d1ce4e5b9
	z = (z ^ (z >> 27)) * 0x94d049bb133111eb
	z ^= z >> 31
	return fmt.Sprintf("%016x", z)
}

func tOpen(t *testing.T, o Options) *DB {
	t.Helper()
	db, err := Open(o)
	if err != nil {
		t.Fatalf("Open(%+v): %v", o, err)
	}
	return db
}

func tPut(t *testing.T, db *DB, k, v string) {
	t.Helper()
	if err := db.Put(k, v); err != nil {
		t.Fatalf("Put(%q): %v", k, err)
	}
}

func tDel(t *testing.T, db *DB, k string) {
	t.Helper()
	if err := db.Delete(k); err != nil {
		t.Fatalf("Delete(%q): %v", k, err)
	}
}

func tExpectGet(t *testing.T, db *DB, k, want string, wantOK bool) {
	t.Helper()
	got, ok := db.Get(k)
	if ok != wantOK || got != want {
		t.Fatalf("Get(%q) = %q, %v; want %q, %v", k, got, ok, want, wantOK)
	}
}

func tRun(es ...Entry) []Entry { return es }

func tLive(k, v string) Entry { return Entry{Key: k, Value: v} }
func tTomb(k string) Entry    { return Entry{Key: k, Tombstone: true} }

func tTrim(levels []int) []int {
	n := len(levels)
	for n > 0 && levels[n-1] == 0 {
		n--
	}
	return levels[:n]
}

// tDigits returns k in base b, least significant digit first.
func tDigits(k, b int) []int {
	var d []int
	for k > 0 {
		d = append(d, k%b)
		k /= b
	}
	return d
}

// ---------- bloom filter ----------

func TestBloom_NeverReportsAnAddedKeyAsAbsent(t *testing.T) {
	for _, bits := range []int{1, 4, 10, 16} {
		b := NewBloom(10_000, bits)
		for i := 0; i < 10_000; i++ {
			b.Add(tKey(i))
		}
		for i := 0; i < 10_000; i++ {
			if !b.MayContain(tKey(i)) {
				t.Fatalf("bitsPerKey=%d: MayContain(%s) = false for an added key; a bloom filter must have no false negatives or reads would miss live data", bits, tKey(i))
			}
		}
	}
}

func TestBloom_FalsePositiveRateMatchesTheory(t *testing.T) {
	rate := func(bits int) float64 {
		b := NewBloom(10_000, bits)
		for i := 0; i < 10_000; i++ {
			b.Add(tKey(i))
		}
		fp := 0
		const probes = 40_000
		for i := 0; i < probes; i++ {
			if b.MayContain(tKey(1_000_000 + i)) {
				fp++
			}
		}
		return float64(fp) / probes
	}
	// 10 bits/key with the optimal k=7: theory ~0.8%. One hash function would
	// give ~9.5%; a filter that ignores half its bits would be far worse.
	if r := rate(10); r > 0.025 || r < 0.001 {
		t.Errorf("10 bits/key: false-positive rate %.3f, want ~0.008 (between 0.001 and 0.025)", r)
	}
	// 4 bits/key with k=3: theory ~14.7%.
	if r := rate(4); r < 0.08 || r > 0.22 {
		t.Errorf("4 bits/key: false-positive rate %.3f, want ~0.147 (between 0.08 and 0.22)", r)
	}
}

func TestBloom_SizeAndProbeCountFollowTheFormulas(t *testing.T) {
	cases := []struct{ n, bits, wantK int }{{1000, 10, 7}, {1000, 4, 3}, {1000, 1, 1}, {1000, 20, 14}, {1000, 40, 28}, {1000, 100, 30}}
	for _, c := range cases {
		b := NewBloom(c.n, c.bits)
		if b == nil {
			t.Fatalf("NewBloom(%d,%d) = nil", c.n, c.bits)
		}
		if b.K() != c.wantK {
			t.Errorf("bitsPerKey=%d: k = %d, want round(bits*ln2) clamped to [1,30] = %d", c.bits, b.K(), c.wantK)
		}
		if b.Bits() < c.n*c.bits || b.Bits() >= c.n*c.bits+64 || b.Bits()%64 != 0 {
			t.Errorf("bitsPerKey=%d n=%d: %d bits, want n*bits rounded up to a multiple of 64", c.bits, c.n, b.Bits())
		}
	}
	if b := NewBloom(3, 10); b.Bits() != 64 {
		t.Errorf("tiny filter has %d bits, want the 64-bit minimum", b.Bits())
	}
}

func TestBloom_DisabledFilterIsNilAndAlwaysSaysMaybe(t *testing.T) {
	b := NewBloom(100, 0)
	if b != nil {
		t.Fatalf("NewBloom(100, 0) = %v, want nil (filters disabled)", b)
	}
	b.Add("x") // must not panic
	if !b.MayContain("anything") {
		t.Error("a disabled filter must answer 'maybe' for every key")
	}
}

// ---------- merge ----------

func TestMerge_NewestRunWinsAndOutputIsSortedAndUnique(t *testing.T) {
	newest := tRun(tLive("b", "new-b"), tLive("d", "new-d"))
	middle := tRun(tLive("a", "mid-a"), tLive("b", "mid-b"), tLive("e", "mid-e"))
	oldest := tRun(tLive("a", "old-a"), tLive("c", "old-c"), tLive("d", "old-d"), tLive("f", "old-f"))
	got := Merge([][]Entry{newest, middle, oldest}, false)
	want := []Entry{tLive("a", "mid-a"), tLive("b", "new-b"), tLive("c", "old-c"), tLive("d", "new-d"), tLive("e", "mid-e"), tLive("f", "old-f")}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Merge = %v\n want %v\n(runs[0] is the newest; an older run must never override a newer one)", got, want)
	}
}

func TestMerge_TombstonesShadowOlderVersionsAndAreDroppedOnlyOnRequest(t *testing.T) {
	newer := tRun(tTomb("b"), tLive("c", "3"))
	older := tRun(tLive("a", "1"), tLive("b", "2"))
	keep := Merge([][]Entry{newer, older}, false)
	if want := []Entry{tLive("a", "1"), tTomb("b"), tLive("c", "3")}; !reflect.DeepEqual(keep, want) {
		t.Errorf("dropTombstones=false: %v, want %v (the tombstone must survive so even older tables stay shadowed)", keep, want)
	}
	drop := Merge([][]Entry{newer, older}, true)
	if want := []Entry{tLive("a", "1"), tLive("c", "3")}; !reflect.DeepEqual(drop, want) {
		t.Errorf("dropTombstones=true: %v, want %v (the old value of b must not reappear when the tombstone is dropped)", drop, want)
	}
	// A live entry in a newer run beats a tombstone in an older one.
	if got := Merge([][]Entry{tRun(tLive("x", "back")), tRun(tTomb("x"))}, true); !reflect.DeepEqual(got, tRun(tLive("x", "back"))) {
		t.Errorf("re-insert after delete: %v, want the live value", got)
	}
}

func TestMerge_EdgeCasesAndInputsAreNotModified(t *testing.T) {
	if got := Merge(nil, false); got == nil || len(got) != 0 {
		t.Errorf("Merge(nil) = %#v, want a non-nil empty slice", got)
	}
	if got := Merge([][]Entry{nil, {}, nil}, true); got == nil || len(got) != 0 {
		t.Errorf("Merge of empty runs = %#v, want a non-nil empty slice", got)
	}
	a := tRun(tLive("a", "1"), tTomb("c"))
	b := tRun(tLive("b", "2"), tLive("c", "3"))
	ac, bc := append([]Entry(nil), a...), append([]Entry(nil), b...)
	Merge([][]Entry{a, b}, true)
	if !reflect.DeepEqual(a, ac) || !reflect.DeepEqual(b, bc) {
		t.Error("Merge modified its input runs")
	}
	// A single run passes through (minus tombstones when dropping).
	if got := Merge([][]Entry{a}, true); !reflect.DeepEqual(got, tRun(tLive("a", "1"))) {
		t.Errorf("single run, drop: %v", got)
	}
}

func TestMerge_ManyRunsMatchAMapFold(t *testing.T) {
	r := rand.New(rand.NewPCG(5, 6))
	for round := 0; round < 50; round++ {
		nruns := 1 + r.IntN(6)
		runs := make([][]Entry, nruns)
		want := map[string]Entry{}
		for ri := nruns - 1; ri >= 0; ri-- { // oldest first so newer overwrite
			seen := map[string]bool{}
			for n := r.IntN(20); n > 0; n-- {
				k := tKey(r.IntN(30))
				if seen[k] {
					continue
				}
				seen[k] = true
				e := tLive(k, fmt.Sprintf("r%d", ri))
				if r.IntN(4) == 0 {
					e = tTomb(k)
				}
				runs[ri] = append(runs[ri], e)
				want[k] = e
			}
			sort.Slice(runs[ri], func(i, j int) bool { return runs[ri][i].Key < runs[ri][j].Key })
		}
		for _, drop := range []bool{false, true} {
			var exp []Entry
			for _, e := range want {
				if e.Tombstone && drop {
					continue
				}
				exp = append(exp, e)
			}
			sort.Slice(exp, func(i, j int) bool { return exp[i].Key < exp[j].Key })
			got := Merge(runs, drop)
			if len(got) != len(exp) || (len(exp) > 0 && !reflect.DeepEqual(got, exp)) {
				t.Fatalf("round %d drop=%v:\n got  %v\n want %v", round, drop, got, exp)
			}
		}
	}
}

// ---------- table ----------

func TestTable_LookupSkipsByRangeThenBloomBeforeSearching(t *testing.T) {
	var es []Entry
	for i := 100; i < 200; i += 2 { // even ids only
		es = append(es, tLive(tKey(i), fmt.Sprint(i)))
	}
	es = append(es, tTomb(tKey(300)))
	tb := NewTable(es, 10)

	if _, out := tb.Lookup(tKey(5)); out != OutRange {
		t.Errorf("key below min: outcome %v, want OutRange", out)
	}
	if _, out := tb.Lookup(tKey(999)); out != OutRange {
		t.Errorf("key above max: outcome %v, want OutRange", out)
	}
	e, out := tb.Lookup(tKey(150))
	if out != OutHit || e.Value != "150" {
		t.Errorf("present key: %v %v, want OutHit with value 150", e, out)
	}
	if e, out := tb.Lookup(tKey(300)); out != OutHit || !e.Tombstone {
		t.Errorf("tombstone key: %v %v, want OutHit returning the tombstone", e, out)
	}
	// Absent keys inside the range end either in the bloom filter or, rarely,
	// in a wasted binary search. With 10 bits/key almost all must stop early.
	bloom, miss := 0, 0
	for i := 101; i < 200; i += 2 {
		switch _, out := tb.Lookup(tKey(i)); out {
		case OutBloom:
			bloom++
		case OutMiss:
			miss++
		default:
			t.Fatalf("absent in-range key %d: outcome %v, want OutBloom or OutMiss", i, out)
		}
	}
	if bloom < 45 {
		t.Errorf("only %d of 50 absent keys were rejected by the bloom filter (%d searched); Lookup must consult the filter before searching", bloom, miss)
	}
	// A table without a filter can only miss by searching.
	nf := NewTable(es, 0)
	if _, out := nf.Lookup(tKey(101)); out != OutMiss {
		t.Errorf("no filter, absent in-range key: outcome %v, want OutMiss", out)
	}
	if _, out := NewTable(nil, 10).Lookup("a"); out != OutRange {
		t.Errorf("empty table lookup: outcome %v, want OutRange", out)
	}
}

// ---------- DB basics ----------

func TestOpen_RejectsOptionsThatCannotWork(t *testing.T) {
	for _, o := range []Options{{0, 4, 10}, {10, 1, 10}, {10, 4, -1}, {}} {
		if _, err := Open(o); err == nil {
			t.Errorf("Open(%+v) succeeded", o)
		}
	}
}

func TestPutGetOverwriteDelete_MemtableOnly(t *testing.T) {
	db := tOpen(t, Options{MemtableEntries: 1000, Fanout: 4, BloomBitsPerKey: 10})
	if err := db.Put("", "x"); err != ErrEmptyKey {
		t.Errorf("Put(\"\") = %v, want ErrEmptyKey", err)
	}
	if err := db.Delete(""); err != ErrEmptyKey {
		t.Errorf("Delete(\"\") = %v, want ErrEmptyKey", err)
	}
	tExpectGet(t, db, "a", "", false)
	tPut(t, db, "a", "1")
	tPut(t, db, "a", "2")
	tExpectGet(t, db, "a", "2", true)
	tDel(t, db, "a")
	tExpectGet(t, db, "a", "", false)
	tPut(t, db, "a", "3")
	tExpectGet(t, db, "a", "3", true)
	if s := db.Stats(); s.Tables != 0 || s.Flushes != 0 {
		t.Errorf("nothing should be flushed yet: %+v", s)
	}
}

func TestFlush_MemtableBecomesALevel0TableAndReadsKeepWorking(t *testing.T) {
	db := tOpen(t, Options{MemtableEntries: 1000, Fanout: 4, BloomBitsPerKey: 10})
	for i := 0; i < 10; i++ {
		tPut(t, db, tKey(i), fmt.Sprint(i))
	}
	db.Flush()
	s := db.Stats()
	if s.Flushes != 1 || s.Tables != 1 || s.Entries != 10 || !reflect.DeepEqual(tTrim(s.Levels), []int{1}) {
		t.Fatalf("after Flush: %+v, want one level-0 table with 10 entries", s)
	}
	for i := 0; i < 10; i++ {
		tExpectGet(t, db, tKey(i), fmt.Sprint(i), true)
	}
	db.Flush() // empty memtable: nothing to do
	if s2 := db.Stats(); s2.Flushes != 1 || s2.Tables != 1 {
		t.Errorf("flushing an empty memtable changed the tree: %+v", s2)
	}
}

func TestFlush_TriggersAutomaticallyAtMemtableEntriesDistinctKeys(t *testing.T) {
	db := tOpen(t, Options{MemtableEntries: 5, Fanout: 10, BloomBitsPerKey: 10})
	for i := 0; i < 4; i++ {
		tPut(t, db, tKey(i), "v")
	}
	// Overwriting does not grow the memtable.
	for i := 0; i < 20; i++ {
		tPut(t, db, tKey(0), fmt.Sprint(i))
	}
	if s := db.Stats(); s.Flushes != 0 {
		t.Fatalf("flushed after %d distinct keys with MemtableEntries=5: overwrites of one key do not fill the memtable", 4)
	}
	tPut(t, db, tKey(4), "v")
	if s := db.Stats(); s.Flushes != 1 || s.Tables != 1 || s.Entries != 5 {
		t.Fatalf("5th distinct key: %+v, want exactly one flush of 5 entries", s)
	}
	tExpectGet(t, db, tKey(0), "19", true)
}

func TestLevels_TieredShapeIsTheBaseFanoutRepresentationOfTheFlushCount(t *testing.T) {
	for _, fanout := range []int{2, 3, 4} {
		db := tOpen(t, Options{MemtableEntries: 1, Fanout: fanout, BloomBitsPerKey: 8})
		for k := 1; k <= 130; k++ {
			tPut(t, db, tKey(k), "v") // every Put flushes one table
			got, want := tTrim(db.Stats().Levels), tDigits(k, fanout)
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("fanout %d after %d flushes: tables per level = %v, want %v (level i merges into level i+1 when it reaches Fanout tables)", fanout, k, got, want)
			}
		}
		for k := 1; k <= 130; k += 7 {
			tExpectGet(t, db, tKey(k), "v", true)
		}
	}
}

func TestGet_NewerTablesShadowOlderOnes(t *testing.T) {
	db := tOpen(t, Options{MemtableEntries: 1000, Fanout: 10, BloomBitsPerKey: 10})
	for gen := 1; gen <= 4; gen++ {
		tPut(t, db, "x", fmt.Sprintf("gen%d", gen))
		tPut(t, db, tKey(gen), "filler")
		db.Flush()
		tExpectGet(t, db, "x", fmt.Sprintf("gen%d", gen), true)
	}
	if s := db.Stats(); s.Tables != 4 {
		t.Fatalf("expected 4 separate level-0 tables, got %+v", s)
	}
	tPut(t, db, "x", "memtable-wins")
	tExpectGet(t, db, "x", "memtable-wins", true)
}

func TestDelete_TombstoneInANewerTableHidesTheOlderValue(t *testing.T) {
	db := tOpen(t, Options{MemtableEntries: 1000, Fanout: 10, BloomBitsPerKey: 10})
	tPut(t, db, "acct", "100")
	db.Flush()
	tDel(t, db, "acct")
	tExpectGet(t, db, "acct", "", false) // tombstone still in the memtable
	db.Flush()
	tExpectGet(t, db, "acct", "", false) // tombstone in a table, value in an older table
	tPut(t, db, "acct", "7")
	tExpectGet(t, db, "acct", "7", true) // re-created
	db.Flush()
	tExpectGet(t, db, "acct", "7", true)
}

// ---------- tombstones and compaction ----------

func TestCompaction_TombstoneSurvivesWhileOlderDataExistsThenIsDropped(t *testing.T) {
	db := tOpen(t, Options{MemtableEntries: 1000, Fanout: 3, BloomBitsPerKey: 10})
	flushKeys := func(keys ...string) {
		for _, k := range keys {
			tPut(t, db, k, "x")
		}
		db.Flush()
	}
	// Round 1: three flushes merge into one level-1 table holding "a".
	tPut(t, db, "a", "1")
	flushKeys("b")
	flushKeys("c")
	flushKeys("d0")
	if got := tTrim(db.Stats().Levels); !reflect.DeepEqual(got, []int{0, 1}) {
		t.Fatalf("after 3 flushes levels = %v, want [0 1]", got)
	}
	// Round 2: delete a. Its tombstone is merged into level 1, next to the
	// older table that still holds a=1, so it must be kept.
	tDel(t, db, "a")
	db.Flush()
	tExpectGet(t, db, "a", "", false)
	flushKeys("d")
	flushKeys("e")
	s := db.Stats()
	if got := tTrim(s.Levels); !reflect.DeepEqual(got, []int{0, 2}) {
		t.Fatalf("after round 2 levels = %v, want [0 2]", got)
	}
	tExpectGet(t, db, "a", "", false)
	if s.Tombstones != 1 {
		t.Fatalf("tombstones stored = %d, want 1: the tombstone for a must be kept while an older table may still hold a=1 (dropping it resurrects the deleted account)", s.Tombstones)
	}
	// Round 3: level 1 reaches 3 tables and merges into a brand-new level 2.
	// Nothing older exists, so the tombstone and the value it shadowed vanish.
	flushKeys("f")
	flushKeys("g")
	flushKeys("h")
	s = db.Stats()
	if got := tTrim(s.Levels); !reflect.DeepEqual(got, []int{0, 0, 1}) {
		t.Fatalf("after round 3 levels = %v, want [0 0 1]", got)
	}
	if s.Tombstones != 0 {
		t.Errorf("tombstones stored = %d after merging into the deepest level, want 0 (they have done their job)", s.Tombstones)
	}
	tExpectGet(t, db, "a", "", false)
	for _, k := range []string{"b", "c", "d0", "d", "e", "f", "g", "h"} {
		tExpectGet(t, db, k, "x", true)
	}
	if s.Entries != 8 {
		t.Errorf("entries stored = %d, want 8 live keys (a's value and tombstone are both gone)", s.Entries)
	}
}

func TestCompaction_ReclaimsOverwrittenVersions(t *testing.T) {
	db := tOpen(t, Options{MemtableEntries: 4, Fanout: 2, BloomBitsPerKey: 10})
	for round := 0; round < 64; round++ {
		for i := 0; i < 4; i++ {
			tPut(t, db, tKey(i), fmt.Sprintf("round%d", round))
		}
	}
	s := db.Stats()
	if s.Flushes != 64 || s.Entries != 4 || s.Tables != 1 {
		t.Fatalf("after 64 rewrites of the same 4 keys: %+v; want everything merged into 1 table of 4 entries (space amplification gone)", s)
	}
	for i := 0; i < 4; i++ {
		tExpectGet(t, db, tKey(i), "round63", true)
	}
}

func TestCompactAll_MergesEverythingAndDropsTombstones(t *testing.T) {
	db := tOpen(t, Options{MemtableEntries: 7, Fanout: 4, BloomBitsPerKey: 10})
	for i := 0; i < 100; i++ {
		tPut(t, db, tKey(i), "v1")
	}
	for i := 0; i < 100; i += 3 {
		tDel(t, db, tKey(i))
	}
	for i := 1; i < 100; i += 5 {
		tPut(t, db, tKey(i), "v2")
	}
	want := db.Scan("", "", 0)
	db.CompactAll()
	s := db.Stats()
	if s.Tables != 1 || s.Tombstones != 0 || s.Entries != len(want) {
		t.Fatalf("after CompactAll: %+v, want 1 table holding exactly the %d live entries", s, len(want))
	}
	if got := db.Scan("", "", 0); !reflect.DeepEqual(got, want) {
		t.Fatalf("CompactAll changed the visible data")
	}
	if len(db.Scan("", "", 0)) == 0 {
		t.Fatal("scan returned nothing")
	}
	// Idempotent and safe on an empty tree.
	db.CompactAll()
	empty := tOpen(t, Options{MemtableEntries: 3, Fanout: 2, BloomBitsPerKey: 10})
	empty.CompactAll()
	tPut(t, empty, "a", "1")
	tDel(t, empty, "a")
	empty.CompactAll()
	if s := empty.Stats(); s.Tables != 0 || s.Entries != 0 {
		t.Errorf("a key that was written and deleted should leave nothing behind after CompactAll: %+v", s)
	}
}

// ---------- scans ----------

func TestScan_MergesMemtableAndTablesNewestWinsTombstonesHide(t *testing.T) {
	db := tOpen(t, Options{MemtableEntries: 1000, Fanout: 10, BloomBitsPerKey: 10})
	for _, k := range []string{"a", "b", "c", "d", "e"} {
		tPut(t, db, k, "old-"+k)
	}
	db.Flush()
	tPut(t, db, "c", "table2-c")
	tDel(t, db, "b")
	tPut(t, db, "f", "table2-f")
	db.Flush()
	tPut(t, db, "d", "mem-d")
	tDel(t, db, "e")
	tPut(t, db, "g", "mem-g")

	got := db.Scan("", "", 0)
	want := []Entry{tLive("a", "old-a"), tLive("c", "table2-c"), tLive("d", "mem-d"), tLive("f", "table2-f"), tLive("g", "mem-g")}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("full scan = %v\n want %v", got, want)
	}
	if got := db.Scan("c", "f", 0); !reflect.DeepEqual(got, []Entry{tLive("c", "table2-c"), tLive("d", "mem-d")}) {
		t.Errorf("Scan(c,f) = %v (lo inclusive, hi exclusive, deleted e hidden)", got)
	}
	if got := db.Scan("c", "", 2); !reflect.DeepEqual(got, []Entry{tLive("c", "table2-c"), tLive("d", "mem-d")}) {
		t.Errorf("Scan(c,,2) = %v, want limit applied after merging", got)
	}
	if got := db.Scan("x", "", 0); got == nil || len(got) != 0 {
		t.Errorf("Scan beyond the end = %#v, want a non-nil empty slice", got)
	}
}

// ---------- bloom filters in the read path ----------

func TestGet_BloomFiltersKeepAbsentKeyLookupsOffTheTables(t *testing.T) {
	build := func(bits int) *DB {
		db := tOpen(t, Options{MemtableEntries: 50, Fanout: 4, BloomBitsPerKey: bits})
		for i := 0; i < 2000; i++ {
			tPut(t, db, tRandKey(i), "v")
		}
		return db
	}
	const misses = 2000
	with, without := build(10), build(0)
	if with.Stats().Tables != 4 {
		t.Fatalf("setup: %d tables, want 4 (2000 keys / 50 per flush = 40 flushes = 220 in base 4)", with.Stats().Tables)
	}
	for _, db := range []*DB{with, without} {
		for i := 0; i < 2000; i += 97 {
			tExpectGet(t, db, tRandKey(i), "v", true)
		}
		before := db.Stats()
		for i := 0; i < misses; i++ {
			if _, ok := db.Get(tRandKey(1_000_000 + i)); ok {
				t.Fatal("found a key that was never written")
			}
		}
		after := db.Stats()
		lookups := after.TableLookups - before.TableLookups
		bloom := after.BloomSkips - before.BloomSkips
		if db == with {
			if lookups > 250 || bloom < 7000 {
				t.Errorf("with 10 bits/key: %d tables binary-searched and %d skipped by bloom for %d absent-key reads over 4 tables; want < 250 searched and > 7000 skipped (~1%% false positives)", lookups, bloom, misses)
			}
		} else if lookups < 7000 || bloom != 0 {
			t.Errorf("without filters: %d tables binary-searched (bloom skips %d); every absent-key read should search every overlapping table (~8000)", lookups, bloom)
		}
	}
}

// ---------- amplification ----------

func TestWriteAmp_FewerLevelsMeansLessRewriting(t *testing.T) {
	// 64 flushes of equal size. With fanout F the data is rewritten once per
	// level it passes through: F=2 has 6 merge levels, F=4 has 3, F=8 has 2.
	// Write amplification = 1 (the flush) + number of merge levels.
	run := func(fanout int) Stats {
		db := tOpen(t, Options{MemtableEntries: 50, Fanout: fanout, BloomBitsPerKey: 10})
		for i := 0; i < 64*50; i++ {
			tPut(t, db, tKey(i), "value-value-value")
		}
		return db.Stats()
	}
	want := map[int]float64{2: 7, 4: 4, 8: 3}
	got := map[int]float64{}
	for f, w := range want {
		s := run(f)
		got[f] = s.WriteAmp()
		if math.Abs(got[f]-w) > 0.05 {
			t.Errorf("fanout %d: write amplification %.2f, want %.1f (bytes written %d for %d user bytes)", f, got[f], w, s.BytesWritten, s.UserBytes)
		}
	}
	if !(got[2] > got[4] && got[4] > got[8]) {
		t.Errorf("write amplification should fall as fanout grows: %v", got)
	}
}

func TestReadAmp_LargerFanoutLeavesMoreTablesToCheck(t *testing.T) {
	// 63 flushes: popcount(63)=6 tables at fanout 2, digit sum 7+7=14 at fanout 8.
	tables := func(fanout int) int {
		db := tOpen(t, Options{MemtableEntries: 50, Fanout: fanout, BloomBitsPerKey: 10})
		for i := 0; i < 63*50; i++ {
			tPut(t, db, tKey(i), "v")
		}
		return db.Stats().Tables
	}
	t2, t8 := tables(2), tables(8)
	if t2 != 6 || t8 != 14 {
		t.Errorf("tables after 63 flushes: fanout 2 -> %d (want 6), fanout 8 -> %d (want 14); fewer merges means more tables a read may have to consult", t2, t8)
	}
}

func TestStats_ByteAccounting(t *testing.T) {
	db := tOpen(t, Options{MemtableEntries: 1000, Fanout: 100, BloomBitsPerKey: 10})
	var user int64
	for i := 0; i < 10; i++ {
		e := Entry{Key: tKey(i), Value: "0123456789"}
		user += int64(e.Size())
		tPut(t, db, e.Key, e.Value)
	}
	// Overwrite one key and delete another: both count as user bytes, but only
	// the surviving versions are written out by the flush.
	over := Entry{Key: tKey(0), Value: "x"}
	user += int64(over.Size())
	tPut(t, db, over.Key, over.Value)
	tomb := Entry{Key: tKey(1), Tombstone: true}
	user += int64(tomb.Size())
	tDel(t, db, tomb.Key)
	if s := db.Stats(); s.UserBytes != user || s.BytesWritten != 0 {
		t.Fatalf("before flush: UserBytes=%d (want %d) BytesWritten=%d (want 0)", s.UserBytes, user, s.BytesWritten)
	}
	db.Flush()
	var flushed int64
	for _, e := range db.Scan("", "", 0) {
		flushed += int64(e.Size())
	}
	flushed += int64(tomb.Size()) // the tombstone is written too
	if s := db.Stats(); s.BytesWritten != flushed {
		t.Errorf("BytesWritten=%d after flush, want %d (surviving versions plus the tombstone)", s.BytesWritten, flushed)
	}
}

// ---------- randomised model check ----------

func TestRandomOps_MatchAMap(t *testing.T) {
	configs := []Options{
		{MemtableEntries: 1, Fanout: 2, BloomBitsPerKey: 8},
		{MemtableEntries: 6, Fanout: 3, BloomBitsPerKey: 8},
		{MemtableEntries: 16, Fanout: 4, BloomBitsPerKey: 0},
	}
	for ci, cfg := range configs {
		for seed := uint64(1); seed <= 3; seed++ {
			t.Run(fmt.Sprintf("config%d/seed%d", ci, seed), func(t *testing.T) {
				db := tOpen(t, cfg)
				model := map[string]string{}
				r := rand.New(rand.NewPCG(seed, uint64(ci)))
				const space = 60
				modelScan := func(lo, hi string, limit int) []Entry {
					var keys []string
					for k := range model {
						if k >= lo && (hi == "" || k < hi) {
							keys = append(keys, k)
						}
					}
					sort.Strings(keys)
					if limit > 0 && len(keys) > limit {
						keys = keys[:limit]
					}
					out := []Entry{}
					for _, k := range keys {
						out = append(out, tLive(k, model[k]))
					}
					return out
				}
				for op := 0; op < 3000; op++ {
					k := tKey(r.IntN(space))
					switch x := r.IntN(100); {
					case x < 40:
						v := fmt.Sprint(op)
						tPut(t, db, k, v)
						model[k] = v
					case x < 60:
						tDel(t, db, k)
						delete(model, k)
					case x < 85:
						want, ok := model[k]
						tExpectGet(t, db, k, want, ok)
					case x < 95:
						lo, hi := tKey(r.IntN(space)), tKey(r.IntN(space))
						if r.IntN(4) == 0 {
							lo = ""
						}
						if r.IntN(4) == 0 {
							hi = ""
						}
						limit := r.IntN(10)
						if got, want := db.Scan(lo, hi, limit), modelScan(lo, hi, limit); !reflect.DeepEqual(got, want) {
							t.Fatalf("op %d Scan(%q,%q,%d):\n got  %v\n want %v", op, lo, hi, limit, got, want)
						}
					case x < 98:
						db.Flush()
					default:
						db.CompactAll()
					}
				}
				if got, want := db.Scan("", "", 0), modelScan("", "", 0); !reflect.DeepEqual(got, want) {
					t.Fatalf("final scan differs:\n got  %v\n want %v", got, want)
				}
			})
		}
	}
}

// ---------- concurrency ----------

func TestConcurrent_WritersReadersAndCompactionsUnderTheRaceDetector(t *testing.T) {
	db := tOpen(t, Options{MemtableEntries: 16, Fanout: 3, BloomBitsPerKey: 8})
	const writers, perWriter = 4, 400
	var wg, readers sync.WaitGroup
	stop := make(chan struct{})
	var bad atomic.Value

	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < perWriter; i++ {
				k := fmt.Sprintf("w%d-%04d", w, i)
				if err := db.Put(k, fmt.Sprint(i)); err != nil {
					bad.Store(err.Error())
					return
				}
				if i%3 == 0 {
					_ = db.Delete(k)
				}
			}
		}(w)
	}
	for g := 0; g < 3; g++ {
		readers.Add(1)
		go func(g int) {
			defer readers.Done()
			r := rand.New(rand.NewPCG(uint64(g), 1))
			for {
				select {
				case <-stop:
					return
				default:
				}
				db.Get(fmt.Sprintf("w%d-%04d", r.IntN(writers), r.IntN(perWriter)))
				es := db.Scan("w1-", "w2-", 20)
				for i := 1; i < len(es); i++ {
					if es[i-1].Key >= es[i].Key || es[i].Tombstone {
						bad.Store(fmt.Sprintf("scan returned %v then %v", es[i-1], es[i]))
						return
					}
				}
			}
		}(g)
	}
	done := make(chan struct{})
	go func() { wg.Wait(); close(stop); readers.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		close(stop)
		t.Fatal("timed out: writers or readers are stuck (deadlock?)")
	}
	if v := bad.Load(); v != nil {
		t.Fatal(v)
	}
	for w := 0; w < writers; w++ {
		for i := 0; i < perWriter; i++ {
			k := fmt.Sprintf("w%d-%04d", w, i)
			if i%3 == 0 {
				tExpectGet(t, db, k, "", false)
			} else {
				tExpectGet(t, db, k, fmt.Sprint(i), true)
			}
		}
	}
}
