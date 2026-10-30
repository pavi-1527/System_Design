package leaderboard

import (
	"fmt"
	"math/rand"
	"reflect"
	"testing"
	"time"
)

func seeded(n int) *Board {
	b := New()
	for i := 0; i < n; i++ {
		b.Set(fmt.Sprintf("p%02d", i), int64(1000-i*10)) // p00 best ... strictly decreasing
	}
	return b
}

func TestStage2_PageWalksTheRanking(t *testing.T) {
	b := seeded(25)
	all := b.TopK(25)
	if len(all) != 25 {
		t.Fatalf("TopK(25) returned %d entries on a 25-player board", len(all))
	}
	p := b.Page(0, 10)
	if !reflect.DeepEqual(p, all[:10]) {
		t.Fatalf("Page(0,10) = %+v, want the first 10 of the ranking", p)
	}
	if p := b.Page(10, 10); !reflect.DeepEqual(p, all[10:20]) || p[0].Rank != 11 {
		t.Fatalf("Page(10,10) = %+v, want ranks 11-20", p)
	}
	if p := b.Page(20, 10); !reflect.DeepEqual(p, all[20:]) || len(p) != 5 {
		t.Fatalf("Page(20,10) = %+v, want the 5 remaining entries", p)
	}
	for _, c := range [][2]int{{25, 10}, {100, 10}, {0, 0}, {0, -3}, {5, 0}} {
		if p := b.Page(c[0], c[1]); len(p) != 0 {
			t.Fatalf("Page(%d,%d) = %+v, want empty", c[0], c[1], p)
		}
	}
	if p := b.Page(-5, 3); !reflect.DeepEqual(p, all[:3]) {
		t.Fatalf("Page(-5,3) = %+v, want a negative offset to behave as 0", p)
	}
	if p := b.Page(0, 1000); len(p) != 25 {
		t.Fatalf("Page(0,1000) returned %d entries, want all 25", len(p))
	}
	if p := New().Page(0, 10); len(p) != 0 {
		t.Fatalf("Page on an empty board = %+v", p)
	}
}

func TestStage2_PagesPartitionTheRankingExactlyOnce(t *testing.T) {
	b := New()
	r := rand.New(rand.NewSource(7))
	for i := 0; i < 1000; i++ {
		b.Set(fmt.Sprintf("p%04d", i), int64(r.Intn(50)))
	}
	if b.Len() != 1000 {
		t.Fatalf("Len = %d, want 1000", b.Len())
	}
	var got []Entry
	for off := 0; off < b.Len(); off += 37 {
		got = append(got, b.Page(off, 37)...)
	}
	if want := b.TopK(b.Len()); !reflect.DeepEqual(got, want) {
		t.Fatalf("concatenated pages differ from TopK(Len): %d vs %d entries", len(got), len(want))
	}
}

func TestStage2_AroundCentresOnThePlayer(t *testing.T) {
	b := seeded(30)
	got, ok := b.Around("p10", 2)
	if !ok {
		t.Fatal("Around(p10) reported unknown player")
	}
	if want := []string{"p08", "p09", "p10", "p11", "p12"}; !reflect.DeepEqual(players(got), want) {
		t.Fatalf("Around(p10, 2) = %q, want %q", players(got), want)
	}
	if got[0].Rank != 9 || got[2].Rank != 11 || got[4].Rank != 13 {
		t.Fatalf("Around ranks = %d..%d, want 9..13", got[0].Rank, got[4].Rank)
	}
	top, _ := b.Around("p01", 3) // only one entry above
	if want := []string{"p00", "p01", "p02", "p03", "p04"}; !reflect.DeepEqual(players(top), want) {
		t.Fatalf("Around(p01, 3) = %q, want the window clipped at the top: %q", players(top), want)
	}
	bot, _ := b.Around("p29", 3)
	if want := []string{"p26", "p27", "p28", "p29"}; !reflect.DeepEqual(players(bot), want) {
		t.Fatalf("Around(p29, 3) = %q, want the window clipped at the bottom: %q", players(bot), want)
	}
	if self, _ := b.Around("p05", 0); len(self) != 1 || self[0].Player != "p05" || self[0].Rank != 6 {
		t.Fatalf("Around(p05, 0) = %+v, want just p05 at rank 6", self)
	}
	if neg, _ := b.Around("p05", -4); len(neg) != 1 {
		t.Fatalf("Around(p05, -4) = %+v, want negative n treated as 0", neg)
	}
	if all, _ := b.Around("p05", 1000); len(all) != 30 {
		t.Fatalf("Around(p05, 1000) returned %d entries, want the whole board", len(all))
	}
	if _, ok := b.Around("ghost", 2); ok {
		t.Fatal("Around of an unknown player reported ok")
	}
}

func TestStage2_UpdatesMoveAPlayerThroughTheRanking(t *testing.T) {
	b := seeded(10) // p00 best
	b.Add("p09", 5000)
	if r, _ := b.Rank("p09"); r != 1 {
		t.Fatalf("Rank(p09) after a big gain = %d, want 1", r)
	}
	if r, _ := b.Rank("p00"); r != 2 {
		t.Fatalf("Rank(p00) = %d, want 2 (pushed down by one)", r)
	}
	b.Set("p09", -1)
	if r, _ := b.Rank("p09"); r != 10 {
		t.Fatalf("Rank(p09) after a big loss = %d, want 10", r)
	}
	if b.Len() != 10 {
		t.Fatalf("Len = %d, want 10: updates must not duplicate players", b.Len())
	}
}

func TestStage2_RanksFormAPermutationUnderHeavyTies(t *testing.T) {
	b := New()
	r := rand.New(rand.NewSource(3))
	const n = 5000
	for i := 0; i < n; i++ {
		b.Set(fmt.Sprintf("p%d", i), int64(r.Intn(5))) // only 5 distinct scores
	}
	seen := make([]bool, n+1)
	for i := 0; i < n; i++ {
		rk, ok := b.Rank(fmt.Sprintf("p%d", i))
		if !ok || rk < 1 || rk > n || seen[rk] {
			t.Fatalf("Rank(p%d) = %d, %v: ranks must be unique values in 1..%d", i, rk, ok, n)
		}
		seen[rk] = true
	}
}

func TestStage2_DifferentialAroundAndPageAgainstNaiveModel(t *testing.T) {
	r := rand.New(rand.NewSource(11))
	b, m := New(), newModel()
	for step := 0; step < 6000; step++ {
		p := fmt.Sprintf("p%02d", r.Intn(60))
		switch r.Intn(6) {
		case 0, 1:
			d := int64(r.Intn(5) - 2)
			b.Add(p, d)
			m.add(p, d)
		case 2, 3:
			s := int64(r.Intn(6))
			b.Set(p, s)
			m.set(p, s)
		case 4:
			b.Remove(p)
			m.remove(p)
		case 5:
			rk := m.ranking()
			off, lim := r.Intn(len(rk)+3)-1, r.Intn(12)
			lo := max(off, 0)
			var wantPage []Entry
			if lim > 0 && lo < len(rk) {
				wantPage = rk[lo:min(len(rk), lo+lim)]
			}
			if got := b.Page(off, lim); !(len(got) == 0 && len(wantPage) == 0) && !reflect.DeepEqual(got, wantPage) {
				t.Fatalf("step %d: Page(%d,%d) = %+v, model says %+v", step, off, lim, got, wantPage)
			}
			if len(rk) == 0 {
				continue
			}
			c := rk[r.Intn(len(rk))]
			n := r.Intn(4)
			got, ok := b.Around(c.Player, n)
			lo2, hi2 := max(c.Rank-1-n, 0), min(len(rk), c.Rank+n)
			if !ok || !reflect.DeepEqual(got, rk[lo2:hi2]) {
				t.Fatalf("step %d: Around(%s, %d) = %+v, %v; model says %+v", step, c.Player, n, got, ok, rk[lo2:hi2])
			}
		}
	}
}

// The scale tests have a generous wall-clock budget per workload. They are not
// benchmarks: they separate "sub-linear per operation" from "O(n) per
// operation" by two orders of magnitude (an O(n) insert into a sorted slice of
// 100,000 entries needs more than a minute for the workload below). The
// budgets apply under -race too. A test aborts as soon as it is over budget.
const scaleN = 100000

type budget struct {
	t     *testing.T
	what  string
	start time.Time
	limit time.Duration
}

func newBudget(t *testing.T, what string, limit time.Duration) *budget {
	return &budget{t: t, what: what, start: time.Now(), limit: limit}
}

func (b *budget) check(done int) {
	b.t.Helper()
	if el := time.Since(b.start); el > b.limit {
		b.t.Fatalf("%s: over the %v budget after %d operations (%v elapsed). Per-operation cost must grow like log n, not n: an O(n) insert or rank query cannot pass this", b.what, b.limit, done, el.Round(time.Millisecond))
	}
}

func TestStage2_ScalesRandomScores(t *testing.T) {
	b := New()
	r := rand.New(rand.NewSource(1))
	bud := newBudget(t, "random scores", 45*time.Second)
	for i := 0; i < scaleN; i++ {
		b.Set(fmt.Sprintf("p%d", i), r.Int63n(1_000_000))
		if i%100 == 0 {
			bud.check(i)
		}
	}
	for i := 0; i < scaleN; i++ { // updates move players across the whole range
		b.Add(fmt.Sprintf("p%d", r.Intn(scaleN)), r.Int63n(2001)-1000)
		if i%100 == 0 {
			bud.check(scaleN + i)
		}
	}
	for i := 0; i < scaleN/2; i++ {
		if _, ok := b.Rank(fmt.Sprintf("p%d", r.Intn(scaleN))); !ok {
			t.Fatal("Rank lost a player")
		}
		if i%100 == 0 {
			b.TopK(10)
			b.Around(fmt.Sprintf("p%d", r.Intn(scaleN)), 5)
		}
		if i%100 == 0 {
			bud.check(2*scaleN + i)
		}
	}
	if b.Len() != scaleN {
		t.Fatalf("Len = %d, want %d", b.Len(), scaleN)
	}
}

func TestStage2_ScalesAscendingScores(t *testing.T) {
	// Sorted input is the classic killer of an unbalanced search tree: every
	// new player becomes the new maximum.
	b := New()
	bud := newBudget(t, "ascending scores", 30*time.Second)
	for i := 0; i < scaleN; i++ {
		b.Set(fmt.Sprintf("p%d", i), int64(i))
		if i%100 == 0 {
			bud.check(i)
		}
	}
	if r, _ := b.Rank("p0"); r != scaleN {
		t.Fatalf("Rank(p0) = %d, want %d", r, scaleN)
	}
	for i := 0; i < scaleN; i++ { // every update moves the player to the top
		b.Add(fmt.Sprintf("p%d", i), int64(scaleN))
		if i%100 == 0 {
			bud.check(scaleN + i)
		}
	}
	if top := b.TopK(1); len(top) != 1 || top[0].Player != fmt.Sprintf("p%d", scaleN-1) {
		t.Fatalf("TopK(1) = %+v, want p%d", top, scaleN-1)
	}
}

func TestStage2_ScalesAllTied(t *testing.T) {
	// Every player has the same score, so ONLY the tiebreak orders them.
	b := New()
	bud := newBudget(t, "all tied", 30*time.Second)
	for i := 0; i < scaleN; i++ {
		b.Set(fmt.Sprintf("p%d", i), 42)
		if i%100 == 0 {
			bud.check(i)
		}
	}
	for i := 0; i < scaleN; i += 3 {
		b.Remove(fmt.Sprintf("p%d", i))
		if i%99 == 0 {
			bud.check(scaleN + i)
		}
	}
	if r, _ := b.Rank("p1"); r != 1 {
		t.Fatalf("Rank(p1) = %d, want 1 (p0 was removed; p1 reached 42 first)", r)
	}
	if got, want := b.Len(), scaleN-(scaleN+2)/3; got != want {
		t.Fatalf("Len = %d, want %d", got, want)
	}
	if page := b.Page(1000, 5); len(page) != 5 || page[0].Rank != 1001 {
		t.Fatalf("Page(1000,5) starts at rank %d, want 1001", page[0].Rank)
	}
}
