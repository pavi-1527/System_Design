package suggest

import (
	"fmt"
	"math/rand/v2"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"
)

const testTimeout = 5 * time.Second

// ---------- helpers (prefixed with t to avoid clashing with your code) ----------

// tBrute is the reference ranking: every query with the prefix, sorted by
// Less, cut to k. It is O(n log n) per call, which is exactly what the
// index exists to avoid.
func tBrute(counts map[string]int64, prefix string, k int) []Suggestion {
	var out []Suggestion
	for q, c := range counts {
		if strings.HasPrefix(q, prefix) {
			out = append(out, Suggestion{Query: q, Count: c})
		}
	}
	sort.Slice(out, func(i, j int) bool { return Less(out[i], out[j]) })
	if len(out) > k {
		out = out[:k]
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func tQueries(s []Suggestion) []string {
	out := make([]string, len(s))
	for i, x := range s {
		out[i] = x.Query
	}
	return out
}

func tEqual(a, b []Suggestion) bool {
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

// tCorpus builds n random queries over a tiny alphabet so that prefixes are
// shared heavily and counts collide often (ties are the interesting case).
func tCorpus(seed uint64, n int) map[string]int64 {
	r := rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15))
	const letters = "abc"
	counts := make(map[string]int64, n)
	for len(counts) < n {
		var sb strings.Builder
		words := 1 + r.IntN(2)
		for w := 0; w < words; w++ {
			if w > 0 {
				sb.WriteByte(' ')
			}
			l := 1 + r.IntN(5)
			for i := 0; i < l; i++ {
				sb.WriteByte(letters[r.IntN(len(letters))])
			}
		}
		counts[sb.String()] = int64(1 + r.IntN(20))
	}
	return counts
}

// tWait fails the test if wg does not finish within testTimeout.
func tWait(t *testing.T, wg *sync.WaitGroup, what string) {
	t.Helper()
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(testTimeout):
		t.Fatalf("timed out after %v waiting for %s", testTimeout, what)
	}
}

// ---------- Normalize ----------

func TestNormalize_FoldsCaseTrimsAndCollapsesWhitespace(t *testing.T) {
	cases := map[string]string{
		"  Pizza  Near   Me ": "pizza near me",
		"CRÈME brûlée":        "crème brûlée",
		"\tgo\n":              "go",
		"":                    "",
		"   ":                 "",
	}
	for in, want := range cases {
		if got := Normalize(in); got != want {
			t.Errorf("Normalize(%q) = %q, want %q", in, got, want)
		}
	}
}

// ---------- Build / Suggest ----------

func TestBuild_TopKMatchesBruteForce(t *testing.T) {
	for _, k := range []int{1, 3, 5} {
		counts := tCorpus(uint64(k), 3000)
		ix := Build(counts, Options{K: k, MaxPrefix: 50})
		if ix == nil {
			t.Fatal("Build returned nil")
		}
		if ix.Len() != len(counts) {
			t.Fatalf("K=%d: Len() = %d, want %d distinct queries", k, ix.Len(), len(counts))
		}
		// every prefix of every query, plus prefixes that match nothing
		prefixes := map[string]bool{"zzz": true, "ab ": true, "a b": true}
		for q := range counts {
			for i := range q {
				prefixes[q[:i+1]] = true
			}
		}
		checked := 0
		for p := range prefixes {
			want := tBrute(counts, Normalize(p), k) // Suggest normalises its prefix the same way
			got := ix.Suggest(p, k)
			if !tEqual(got, want) {
				t.Fatalf("K=%d Suggest(%q): got %v, want %v (count desc, then query asc)", k, p, got, want)
			}
			checked++
		}
		if checked < 1000 {
			t.Fatalf("only %d prefixes checked; corpus generator broken", checked)
		}
	}
}

func TestSuggest_TiesBreakByQueryAscending(t *testing.T) {
	ix := Build(map[string]int64{
		"delta": 7, "alpha": 7, "charlie": 7, "bravo": 7, "echo": 9, "alphabet": 1,
	}, Options{K: 5})
	got := tQueries(ix.Suggest("", 5))
	if len(got) != 0 {
		t.Fatalf("empty prefix returned %v; the index serves completions of a prefix, not a trending list", got)
	}
	want := []string{"alpha", "alphabet"}
	if got := tQueries(ix.Suggest("al", 5)); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("Suggest(\"al\") = %v, want %v", got, want)
	}
	// all five share count 7 except echo (9): echo first, then alphabetical
	ix2 := Build(map[string]int64{"ea": 7, "ec": 7, "eb": 7, "ed": 7, "ee": 9}, Options{K: 5})
	want = []string{"ee", "ea", "eb", "ec", "ed"}
	if got := tQueries(ix2.Suggest("e", 5)); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("Suggest(\"e\") = %v, want %v: equal counts must be ordered by query ascending so every replica answers identically", got, want)
	}
}

func TestSuggest_LimitClampsToK(t *testing.T) {
	counts := map[string]int64{}
	for i := 0; i < 20; i++ {
		counts[fmt.Sprintf("q%02d", i)] = int64(100 - i)
	}
	ix := Build(counts, Options{K: 5})
	if got := ix.Suggest("q", 0); len(got) != 5 {
		t.Fatalf("limit 0 returned %d suggestions, want K=5", len(got))
	}
	if got := ix.Suggest("q", 50); len(got) != 5 {
		t.Fatalf("limit 50 returned %d suggestions, want K=5 (the index only stores K per prefix)", len(got))
	}
	got := ix.Suggest("q", 2)
	if want := []string{"q00", "q01"}; strings.Join(tQueries(got), ",") != strings.Join(want, ",") {
		t.Fatalf("limit 2 returned %v, want %v", tQueries(got), want)
	}
}

func TestSuggest_EmptyPrefixAndMissesReturnNothing(t *testing.T) {
	ix := Build(map[string]int64{"alpha": 1}, Options{})
	for _, p := range []string{"", "   ", "\t", "beta", "alphab", "alpha beta"} {
		if got := ix.Suggest(p, 5); len(got) != 0 {
			t.Errorf("Suggest(%q) = %v, want nothing", p, got)
		}
	}
	var nilIx *Index
	if got := nilIx.Suggest("a", 5); got != nil {
		t.Errorf("nil *Index Suggest = %v, want nil (a Service may serve nil before its first Swap)", got)
	}
	empty := Build(nil, Options{})
	if empty == nil {
		t.Fatal("Build(nil) returned nil; want an empty index")
	}
	if empty.Len() != 0 || empty.Suggest("a", 5) != nil {
		t.Errorf("empty index: Len()=%d Suggest=%v, want 0 and nil", empty.Len(), empty.Suggest("a", 5))
	}
}

func TestSuggest_NormalisesPrefixAndHandlesUnicode(t *testing.T) {
	ix := Build(map[string]int64{"Crème Brûlée recipe": 10, "crème fraîche": 4, "cream cheese": 6, "日本 料理": 3, "日本語": 8}, Options{})
	cases := map[string][]string{
		"crè":        {"crème brûlée recipe", "crème fraîche"},
		"CRÈ":        {"crème brûlée recipe", "crème fraîche"},
		"  Crème B ": {"crème brûlée recipe"},
		"cr":         {"crème brûlée recipe", "cream cheese", "crème fraîche"},
		"日本":         {"日本語", "日本 料理"},
		"日本 ":        {"日本語", "日本 料理"}, // trailing whitespace is trimmed by Normalize
		"日本 料":       {"日本 料理"},
	}
	for p, want := range cases {
		if got := tQueries(ix.Suggest(p, 5)); strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("Suggest(%q) = %v, want %v", p, got, want)
		}
	}
}

func TestBuild_MergesQueriesThatNormaliseEqual(t *testing.T) {
	ix := Build(map[string]int64{"Pizza": 3, " pizza ": 4, "PIZZA  near me": 1, "pizza near me": 1}, Options{})
	if ix.Len() != 2 {
		t.Fatalf("Len() = %d, want 2: \"Pizza\" and \" pizza \" are one query", ix.Len())
	}
	got := ix.Suggest("p", 5)
	want := []Suggestion{{"pizza", 7}, {"pizza near me", 2}}
	if !tEqual(got, want) {
		t.Fatalf("Suggest(\"p\") = %v, want %v (counts summed across spellings)", got, want)
	}
}

func TestBuild_DropsBelowMinCountAndNonPositive(t *testing.T) {
	ix := Build(map[string]int64{"rare": 1, "rarer": 2, "rarest": 3, "negative": -5, "zero": 0}, Options{MinCount: 2})
	if ix.Len() != 2 {
		t.Fatalf("Len() = %d, want 2 (rarer, rarest)", ix.Len())
	}
	got := tQueries(ix.Suggest("r", 5))
	if want := []string{"rarest", "rarer"}; strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("Suggest(\"r\") = %v, want %v: a query seen once is noise, and possibly one person's private search", got, want)
	}
	if got := ix.Suggest("n", 5); got != nil {
		t.Fatalf("non-positive counts must be ignored, got %v", got)
	}
}

func TestBuild_BlocklistMatchesWholeWordsAndPhrases(t *testing.T) {
	counts := map[string]int64{
		"cat videos": 50, "my cat": 40, "catalogue": 30, "CAT": 20, "cat": 5,
		"free money now": 60, "free money": 55, "money free": 15, "freemoney": 10,
	}
	ix := Build(counts, Options{Blocklist: []string{" Cat ", "free money"}})
	got := tQueries(ix.Suggest("c", 5))
	if want := []string{"catalogue"}; strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("Suggest(\"c\") = %v, want %v: whole-word match, so \"catalogue\" survives and \"cat\"/\"my cat\"/\"cat videos\" are gone", got, want)
	}
	got = tQueries(ix.Suggest("f", 5))
	if want := []string{"freemoney"}; strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("Suggest(\"f\") = %v, want %v: the phrase blocks \"free money now\" and \"free money\" but not \"freemoney\"", got, want)
	}
	if got := tQueries(ix.Suggest("m", 5)); strings.Join(got, ",") != "money free" {
		t.Fatalf("Suggest(\"m\") = %v, want [money free]: word order matters for a phrase entry", got)
	}
	if ix.Len() != 3 {
		t.Fatalf("Len() = %d, want 3", ix.Len())
	}
}

func TestSuggest_PrefixLongerThanMaxPrefixIsTruncatedThenFiltered(t *testing.T) {
	ix := Build(map[string]int64{"abcd": 10, "abce": 5, "abcdef": 4, "abz": 1}, Options{K: 2, MaxPrefix: 3})
	// the node for "abc" holds the best 2 of {abcd:10, abce:5, abcdef:4}
	if got := tQueries(ix.Suggest("abc", 5)); strings.Join(got, ",") != "abcd,abce" {
		t.Fatalf("Suggest(\"abc\") = %v, want [abcd abce]", got)
	}
	if got := tQueries(ix.Suggest("abcd", 5)); strings.Join(got, ",") != "abcd" {
		t.Fatalf("Suggest(\"abcd\") = %v, want [abcd]: truncate to 3 runes, then keep only candidates with the full prefix", got)
	}
	if got := ix.Suggest("abcdef", 5); got != nil {
		t.Fatalf("Suggest(\"abcdef\") = %v, want nothing: abcdef was pushed out of the top-2 at depth 3, so it is unreachable; that is the price of the depth cap", got)
	}
	if got := ix.Suggest("abcx", 5); got != nil {
		t.Fatalf("Suggest(\"abcx\") = %v, want nothing: no candidate at \"abc\" starts with \"abcx\"", got)
	}
	if n := ix.Nodes(); n != 1+3+1 {
		t.Fatalf("Nodes() = %d, want 5 (root, a, ab, abc, abz): depth must be capped at MaxPrefix", n)
	}
}

func TestSuggest_ResultIsACopy(t *testing.T) {
	ix := Build(map[string]int64{"go": 5, "golang": 4, "gopher": 3}, Options{})
	first := ix.Suggest("go", 5)
	if len(first) != 3 {
		t.Fatalf("got %v", first)
	}
	first[0].Query = "CORRUPTED"
	first[1].Count = 0
	second := ix.Suggest("go", 5)
	if second[0].Query != "go" || second[1].Count != 4 {
		t.Fatalf("mutating a returned slice changed the index: %v (return a copy; the index is shared by every reader)", second)
	}
}

func TestIndex_NodesBoundedByPrefixCount(t *testing.T) {
	counts := tCorpus(42, 2000)
	opts := Options{MaxPrefix: 4}
	ix := Build(counts, opts)
	maxNodes := 1
	for q := range counts {
		n := utf8.RuneCountInString(q)
		if n > opts.MaxPrefix {
			n = opts.MaxPrefix
		}
		maxNodes += n
	}
	if ix.Nodes() < 2 || ix.Nodes() > maxNodes {
		t.Fatalf("Nodes() = %d, want between 2 and %d (one node per distinct prefix, at most MaxPrefix per query)", ix.Nodes(), maxNodes)
	}
	// with a 3-letter alphabet, 4 levels and 2000 queries, prefixes are shared heavily
	if ix.Nodes() > maxNodes/4 {
		t.Fatalf("Nodes() = %d for %d queries: distinct prefixes are not being shared (max possible %d)", ix.Nodes(), len(counts), maxNodes)
	}
}

// ---------- Service ----------

func TestService_NilIndexServesNothing(t *testing.T) {
	s := NewService(nil)
	if got := s.Suggest("a", 5); got != nil {
		t.Fatalf("Suggest before the first Swap = %v, want nil", got)
	}
	if s.Current() != nil {
		t.Fatal("Current() should be nil before the first Swap")
	}
	ix := Build(map[string]int64{"a": 1}, Options{})
	if old := s.Swap(ix); old != nil {
		t.Fatalf("Swap returned %v as the previous snapshot, want nil", old)
	}
	if s.Current() != ix || len(s.Suggest("a", 1)) != 1 {
		t.Fatal("Swap did not publish the new index")
	}
}

func TestService_SwapIsAtomicUnderConcurrentReaders(t *testing.T) {
	ixA := Build(map[string]int64{"sa1": 3, "sa2": 2, "sa3": 1}, Options{K: 3})
	ixB := Build(map[string]int64{"sb1": 3, "sb2": 2, "sb3": 1}, Options{K: 3})
	wantA, wantB := ixA.Suggest("s", 3), ixB.Suggest("s", 3)
	s := NewService(ixA)

	stop := make(chan struct{})
	var wg sync.WaitGroup
	var mu sync.Mutex
	var bad []string
	seen := map[string]bool{}
	for r := 0; r < 8; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				got := s.Suggest("s", 3)
				key := strings.Join(tQueries(got), ",")
				mu.Lock()
				seen[key] = true
				if !tEqual(got, wantA) && !tEqual(got, wantB) && len(bad) < 5 {
					bad = append(bad, key)
				}
				mu.Unlock()
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer close(stop)
		cur := ixA
		for i := 0; i < 20000; i++ {
			if cur == ixA {
				cur = ixB
			} else {
				cur = ixA
			}
			if old := s.Swap(cur); old == nil {
				mu.Lock()
				bad = append(bad, "Swap returned nil instead of the previous snapshot")
				mu.Unlock()
				return
			}
		}
	}()
	tWait(t, &wg, "readers and the swapping writer")
	if len(bad) > 0 {
		t.Fatalf("a reader observed a result that belongs to neither snapshot: %v (publish must be one atomic pointer swap)", bad)
	}
	if !seen["sa1,sa2,sa3"] || !seen["sb1,sb2,sb3"] {
		t.Fatalf("readers never saw both snapshots (%v); Swap must publish to concurrent readers", seen)
	}
}

// ---------- Counter ----------

func TestCounter_RecordNormalisesAndCounts(t *testing.T) {
	c := NewCounter(CounterOptions{})
	for _, q := range []string{"Pizza", " pizza ", "PIZZA", "pasta", "", "   "} {
		c.Record(q)
	}
	got := c.Snapshot()
	want := map[string]int64{"pizza": 3, "pasta": 1}
	if len(got) != len(want) || got["pizza"] != 3 || got["pasta"] != 1 {
		t.Fatalf("Snapshot() = %v, want %v (normalised, empty ignored)", got, want)
	}
	if c.Len() != 2 {
		t.Fatalf("Len() = %d, want 2", c.Len())
	}
}

func TestCounter_HalfLifeDecay(t *testing.T) {
	t0 := time.Date(2026, 6, 1, 9, 0, 0, 0, time.UTC)
	now := t0
	c := NewCounter(CounterOptions{HalfLife: time.Hour, Now: func() time.Time { return now }})
	for i := 0; i < 8; i++ {
		c.Record("breaking news")
	}
	c.Record("evergreen")
	steps := []struct {
		after time.Duration
		want  int64
	}{{0, 8}, {time.Hour, 4}, {2 * time.Hour, 2}, {3 * time.Hour, 1}}
	for _, st := range steps {
		now = t0.Add(st.after)
		if got := c.Snapshot()["breaking news"]; got != st.want {
			t.Fatalf("after %v: count = %d, want %d (half-life 1h: 8 → 4 → 2 → 1)", st.after, got, st.want)
		}
	}
	// a repeat after decay adds to the decayed value, not the original
	now = t0.Add(3 * time.Hour)
	c.Record("breaking news")
	if got := c.Snapshot()["breaking news"]; got != 2 {
		t.Fatalf("after a repeat at 3h: count = %d, want 2 (1 decayed + 1 new)", got)
	}
	now = t0.Add(30 * time.Hour)
	snap := c.Snapshot()
	if _, ok := snap["breaking news"]; ok {
		t.Fatalf("after 27 more half-lives the query should be forgotten, got %v", snap)
	}
	if _, ok := snap["evergreen"]; ok {
		t.Fatalf("\"evergreen\" (count 1, 30 half-lives ago) should be forgotten too, got %v", snap)
	}
	if c.Len() != 0 {
		t.Fatalf("Len() = %d after everything decayed to zero; Snapshot must prune, or the counter grows forever", c.Len())
	}
}

func TestCounter_ConcurrentRecordsAreNotLost(t *testing.T) {
	c := NewCounter(CounterOptions{})
	const goroutines, perGoroutine, distinct = 50, 200, 10
	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < perGoroutine; i++ {
				c.Record(fmt.Sprintf("query %d", i%distinct))
			}
		}()
	}
	tWait(t, &wg, "concurrent Record calls")
	snap := c.Snapshot()
	want := int64(goroutines * perGoroutine / distinct)
	for i := 0; i < distinct; i++ {
		q := fmt.Sprintf("query %d", i)
		if snap[q] != want {
			t.Fatalf("%q counted %d times, want %d: increments were lost under concurrency", q, snap[q], want)
		}
	}
}

func TestCounter_SnapshotFeedsBuildAndSwap(t *testing.T) {
	t0 := time.Date(2026, 6, 1, 9, 0, 0, 0, time.UTC)
	now := t0
	c := NewCounter(CounterOptions{HalfLife: 24 * time.Hour, Now: func() time.Time { return now }})
	svc := NewService(nil)
	record := func(q string, n int) {
		for i := 0; i < n; i++ {
			c.Record(q)
		}
	}
	record("world cup final", 100)
	record("world news", 60)
	record("world map", 30)
	svc.Swap(Build(c.Snapshot(), Options{K: 2}))
	if got := tQueries(svc.Suggest("wor", 2)); strings.Join(got, ",") != "world cup final,world news" {
		t.Fatalf("first snapshot: %v", got)
	}
	// Three days later the final is old news; a new query takes off.
	now = t0.Add(72 * time.Hour)
	record("world map", 20)
	record("world weather", 50)
	svc.Swap(Build(c.Snapshot(), Options{K: 2}))
	if got := tQueries(svc.Suggest("wor", 2)); strings.Join(got, ",") != "world weather,world map" {
		t.Fatalf("second snapshot: %v, want [world weather world map] (100 decayed to ~13 over three half-lives; 30 decayed to ~4 plus 20 new)", got)
	}
}

// ---------- benchmark (not run by default; see labs/c12/README.md) ----------

func BenchmarkSuggest(b *testing.B) {
	counts := tCorpus(7, 50000)
	ix := Build(counts, Options{})
	prefixes := []string{"a", "ab", "abc", "b", "ca", "bb"}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ix.Suggest(prefixes[i%len(prefixes)], 5)
	}
}
