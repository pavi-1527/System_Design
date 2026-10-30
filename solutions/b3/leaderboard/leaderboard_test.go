package leaderboard

import (
	"fmt"
	"math/rand"
	"reflect"
	"sort"
	"testing"
	"time"
)

const testTimeout = 5 * time.Second

// within fails the test if fn does not return in time, so a hang in your
// implementation becomes a message instead of a stuck test run.
func within(t *testing.T, what string, d time.Duration, fn func()) {
	t.Helper()
	done := make(chan struct{})
	go func() { defer close(done); fn() }()
	select {
	case <-done:
	case <-time.After(d):
		t.Fatalf("%s did not finish within %v", what, d)
	}
}

// model is the obviously-correct reference the randomized tests compare with:
// a map plus a sort on every query. It encodes the documented ordering: score
// descending, then whoever reached that score first (a logical clock that
// ticks on every change of any player's score).
type model struct {
	score map[string]int64
	at    map[string]int
	clock int
}

func newModel() *model { return &model{score: map[string]int64{}, at: map[string]int{}} }

func (m *model) set(p string, s int64) {
	if old, ok := m.score[p]; ok && old == s {
		return
	}
	m.clock++
	m.score[p], m.at[p] = s, m.clock
}

func (m *model) add(p string, d int64) int64 {
	old, ok := m.score[p]
	if !ok || d != 0 {
		m.set(p, old+d) // a missing player is created even for d == 0
	}
	return m.score[p]
}

func (m *model) remove(p string) bool {
	_, ok := m.score[p]
	delete(m.score, p)
	delete(m.at, p)
	return ok
}

func (m *model) ranking() []Entry {
	es := make([]Entry, 0, len(m.score))
	for p, s := range m.score {
		es = append(es, Entry{Player: p, Score: s})
	}
	sort.Slice(es, func(i, j int) bool {
		a, b := es[i], es[j]
		if a.Score != b.Score {
			return a.Score > b.Score
		}
		return m.at[a.Player] < m.at[b.Player]
	})
	for i := range es {
		es[i].Rank = i + 1
	}
	return es
}

func players(es []Entry) []string {
	out := make([]string, len(es))
	for i, e := range es {
		out[i] = e.Player
	}
	return out
}

func mustScore(t *testing.T, b *Board, p string, want int64) {
	t.Helper()
	got, ok := b.Score(p)
	if !ok || got != want {
		t.Fatalf("Score(%q) = %d, %v; want %d, true", p, got, ok, want)
	}
}

func wantOrder(t *testing.T, b *Board, want ...string) {
	t.Helper()
	got := players(b.TopK(len(want) + 10))
	if len(got) == 0 && len(want) == 0 {
		return
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ranking = %q, want %q", got, want)
	}
}

// ---------------------------------------------------------------- Stage 1

func TestStage1_AddCreatesAndAccumulates(t *testing.T) {
	b := New()
	if got := b.Add("ann", 10); got != 10 {
		t.Fatalf("Add(ann, 10) = %d, want 10 (a new player starts at 0)", got)
	}
	if got := b.Add("ann", -3); got != 7 {
		t.Fatalf("Add(ann, -3) = %d, want 7", got)
	}
	mustScore(t, b, "ann", 7)
	if got := b.Add("zero", 0); got != 0 {
		t.Fatalf("Add(zero, 0) = %d, want 0", got)
	}
	if b.Len() != 2 {
		t.Fatalf("Len = %d, want 2: Add(p, 0) still creates p", b.Len())
	}
	if _, ok := b.Score("nobody"); ok {
		t.Fatal("Score of an unknown player reported ok")
	}
}

func TestStage1_SetOverwritesAndRemoveDeletes(t *testing.T) {
	b := New()
	b.Set("ann", 5)
	b.Set("ann", 9)
	mustScore(t, b, "ann", 9)
	if b.Len() != 1 {
		t.Fatalf("Len = %d, want 1: Set on an existing player must not add a second entry", b.Len())
	}
	if !b.Remove("ann") {
		t.Fatal("Remove(ann) = false, want true")
	}
	if b.Remove("ann") {
		t.Fatal("second Remove(ann) = true, want false")
	}
	if _, ok := b.Score("ann"); ok {
		t.Fatal("removed player still has a score")
	}
	if _, ok := b.Rank("ann"); ok {
		t.Fatal("removed player still has a rank")
	}
	if b.Len() != 0 || len(b.TopK(5)) != 0 {
		t.Fatalf("board not empty after removing its only player (Len %d)", b.Len())
	}
}

func TestStage1_TopKIsBestFirstWithRanks(t *testing.T) {
	b := New()
	for i, p := range []string{"c", "a", "e", "b", "d"} {
		b.Set(p, int64(i*10)) // c=0 a=10 e=20 b=30 d=40
	}
	got := b.TopK(3)
	want := []Entry{{1, "d", 40}, {2, "b", 30}, {3, "e", 20}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("TopK(3) = %+v, want %+v", got, want)
	}
	if all := b.TopK(100); len(all) != 5 || all[4].Player != "c" || all[4].Rank != 5 {
		t.Fatalf("TopK(100) = %+v, want all 5 players ending with c at rank 5", all)
	}
	for _, k := range []int{0, -1, -100} {
		if got := b.TopK(k); len(got) != 0 {
			t.Fatalf("TopK(%d) = %+v, want empty", k, got)
		}
	}
	got[0].Score = -999 // the result belongs to the caller
	if s, _ := b.Score("d"); s != 40 {
		t.Fatalf("mutating TopK's result changed the board (d = %d)", s)
	}
}

func TestStage1_NegativeAndZeroScoresSortCorrectly(t *testing.T) {
	b := New()
	b.Set("neg", -5)
	b.Set("zero", 0)
	b.Set("pos", 5)
	b.Set("neg2", -50)
	wantOrder(t, b, "pos", "zero", "neg", "neg2")
}

func TestStage1_TiesGoToWhoeverReachedTheScoreFirst(t *testing.T) {
	b := New()
	b.Set("late", 10)
	b.Set("early", 10)
	wantOrder(t, b, "late", "early") // late reached 10 first
	if r, _ := b.Rank("early"); r != 2 {
		t.Fatalf("Rank(early) = %d, want 2", r)
	}
	// Reaching 10 again is not a change: late keeps its place.
	b.Set("late", 10)
	b.Add("late", 0)
	wantOrder(t, b, "late", "early")
	// Leaving the score and coming back means reaching it LATER.
	b.Set("late", 4)
	b.Set("late", 10)
	wantOrder(t, b, "early", "late")
	// A new arrival at the same score goes behind everyone already there.
	b.Set("newest", 10)
	wantOrder(t, b, "early", "late", "newest")
	// Remove and re-add counts as a new arrival.
	b.Remove("early")
	b.Set("early", 10)
	wantOrder(t, b, "late", "newest", "early")
}

func TestStage1_AddPathReachesScoresTooForTiebreaks(t *testing.T) {
	b := New()
	b.Add("x", 5)
	b.Add("y", 3)
	b.Add("y", 2) // y reaches 5 after x did
	wantOrder(t, b, "x", "y")
	b.Add("x", 1)
	b.Add("x", -1) // x is back at 5, but later than y
	wantOrder(t, b, "y", "x")
}

func TestStage1_RankAgreesWithTopKPosition(t *testing.T) {
	b := New()
	for i := 0; i < 50; i++ {
		b.Set(fmt.Sprintf("p%02d", i), int64(i%7)) // many ties
	}
	top := b.TopK(50)
	if len(top) != 50 {
		t.Fatalf("TopK(50) returned %d entries for 50 players", len(top))
	}
	for i, e := range top {
		if e.Rank != i+1 {
			t.Fatalf("TopK entry %d has Rank %d, want %d", i, e.Rank, i+1)
		}
		if r, ok := b.Rank(e.Player); !ok || r != i+1 {
			t.Fatalf("Rank(%s) = %d, %v; want %d (its position in TopK)", e.Player, r, ok, i+1)
		}
	}
	if _, ok := b.Rank("ghost"); ok {
		t.Fatal("Rank of an unknown player reported ok")
	}
}

func TestStage1_DifferentialAgainstNaiveModel(t *testing.T) {
	r := rand.New(rand.NewSource(1))
	b, m := New(), newModel()
	names := make([]string, 40)
	for i := range names {
		names[i] = fmt.Sprintf("player-%02d", i)
	}
	for step := 0; step < 20000; step++ {
		p := names[r.Intn(len(names))]
		switch op := r.Intn(10); {
		case op < 4:
			d := int64(r.Intn(7) - 3) // small deltas: lots of ties and zero-deltas
			if got, want := b.Add(p, d), m.add(p, d); got != want {
				t.Fatalf("step %d: Add(%s, %d) = %d, model says %d", step, p, d, got, want)
			}
		case op < 7:
			s := int64(r.Intn(8))
			b.Set(p, s)
			m.set(p, s)
		case op < 8:
			if got, want := b.Remove(p), m.remove(p); got != want {
				t.Fatalf("step %d: Remove(%s) = %v, model says %v", step, p, got, want)
			}
		default:
			want := m.ranking()
			if b.Len() != len(want) {
				t.Fatalf("step %d: Len = %d, model says %d", step, b.Len(), len(want))
			}
			if got := b.TopK(10); !reflect.DeepEqual(got, want[:min(10, len(want))]) && !(len(got) == 0 && len(want) == 0) {
				t.Fatalf("step %d: TopK(10) = %+v\nmodel says       %+v", step, got, want[:min(10, len(want))])
			}
			q := names[r.Intn(len(names))]
			gotRank, ok := b.Rank(q)
			_, inModel := m.score[q]
			if ok != inModel {
				t.Fatalf("step %d: Rank(%s) ok=%v, model says present=%v", step, q, ok, inModel)
			}
			if ok {
				wantRank := 0
				for _, e := range want {
					if e.Player == q {
						wantRank = e.Rank
					}
				}
				if gotRank != wantRank {
					t.Fatalf("step %d: Rank(%s) = %d, model says %d", step, q, gotRank, wantRank)
				}
			}
		}
	}
}
