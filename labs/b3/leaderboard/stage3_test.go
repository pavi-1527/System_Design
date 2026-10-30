package leaderboard

import (
	"fmt"
	"math/rand"
	"reflect"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// checkRanking verifies the properties every consistent view of a ranking has:
// ranks run from first..first+len-1, scores never increase, nobody appears twice.
func checkRanking(es []Entry, firstRank int) error {
	seen := map[string]bool{}
	for i, e := range es {
		if e.Rank != firstRank+i {
			return fmt.Errorf("entry %d (%s) has Rank %d, want %d", i, e.Player, e.Rank, firstRank+i)
		}
		if i > 0 && e.Score > es[i-1].Score {
			return fmt.Errorf("entry %d (%s, %d) outscores the one above it (%s, %d)", i, e.Player, e.Score, es[i-1].Player, es[i-1].Score)
		}
		if seen[e.Player] {
			return fmt.Errorf("%s appears twice", e.Player)
		}
		seen[e.Player] = true
	}
	return nil
}

func TestStage3_ConcurrentAddsAreAtomic(t *testing.T) {
	b := New()
	const workers, per = 32, 500
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < per; i++ {
				b.Add("hot", 1)
				b.Add(fmt.Sprintf("own-%d", w), 2)
			}
		}()
	}
	within(t, "concurrent Adds", testTimeout*4, wg.Wait)
	mustScore(t, b, "hot", workers*per)
	for w := 0; w < workers; w++ {
		mustScore(t, b, fmt.Sprintf("own-%d", w), 2*per)
	}
	if got, want := b.Len(), workers+1; got != want {
		t.Fatalf("Len = %d, want %d", got, want)
	}
	if r, _ := b.Rank("hot"); r != 1 {
		t.Fatalf("Rank(hot) = %d, want 1 (%d points beats %d)", r, workers*per, 2*per)
	}
}

func TestStage3_ConcurrentReadersAlwaysSeeAConsistentRanking(t *testing.T) {
	b := New()
	const names = 200
	var writers, readers sync.WaitGroup
	var stop atomic.Bool
	for w := 0; w < 6; w++ {
		writers.Add(1)
		go func() {
			defer writers.Done()
			r := rand.New(rand.NewSource(int64(w)))
			for i := 0; i < 3000; i++ {
				p := fmt.Sprintf("p%03d", r.Intn(names))
				switch r.Intn(5) {
				case 0, 1:
					b.Add(p, int64(r.Intn(21)-10))
				case 2, 3:
					b.Set(p, int64(r.Intn(30)))
				case 4:
					b.Remove(p)
				}
			}
		}()
	}
	for rd := 0; rd < 4; rd++ {
		readers.Add(1)
		go func() {
			defer readers.Done()
			r := rand.New(rand.NewSource(int64(100 + rd)))
			for i := 0; i < 400 && !stop.Load(); i++ {
				runtime.Gosched()
				if err := checkRanking(b.TopK(25), 1); err != nil {
					t.Errorf("TopK(25) was inconsistent: %v", err)
					return
				}
				off := r.Intn(60)
				if err := checkRanking(b.Page(off, 15), off+1); err != nil {
					t.Errorf("Page(%d,15) was inconsistent: %v", off, err)
					return
				}
				p := fmt.Sprintf("p%03d", r.Intn(names))
				if around, ok := b.Around(p, 3); ok {
					if err := checkRanking(around, around[0].Rank); err != nil {
						t.Errorf("Around(%s,3) was inconsistent: %v", p, err)
						return
					}
				}
				if rk, ok := b.Rank(p); ok && (rk < 1 || rk > names) {
					t.Errorf("Rank(%s) = %d, outside 1..%d", p, rk, names)
					return
				}
				_ = b.Len()
			}
		}()
	}
	within(t, "writers", testTimeout*6, writers.Wait)
	stop.Store(true)
	within(t, "readers", testTimeout, readers.Wait)

	if b.Len() < names/2 {
		t.Fatalf("only %d of %d players survived the workload; the workload should leave most of them on the board", b.Len(), names)
	}
	all := b.TopK(b.Len() + 10)
	if len(all) != b.Len() {
		t.Fatalf("TopK(Len+10) returned %d entries, Len says %d", len(all), b.Len())
	}
	if err := checkRanking(all, 1); err != nil {
		t.Fatalf("final ranking: %v", err)
	}
	for _, e := range all {
		if s, ok := b.Score(e.Player); !ok || s != e.Score {
			t.Fatalf("Score(%s) = %d, %v; TopK says %d", e.Player, s, ok, e.Score)
		}
		if rk, _ := b.Rank(e.Player); rk != e.Rank {
			t.Fatalf("Rank(%s) = %d; TopK says %d", e.Player, rk, e.Rank)
		}
	}
}

func TestStage3_SnapshotIsAnIndependentPointInTimeCopy(t *testing.T) {
	b := seeded(10)
	snap := b.Snapshot()
	if snap == nil {
		t.Fatal("Snapshot returned nil")
	}
	before := snap.TopK(100)
	b.Add("p09", 5000)
	b.Remove("p00")
	b.Set("newcomer", 7)
	if got := snap.TopK(100); !reflect.DeepEqual(got, before) {
		t.Fatalf("snapshot changed after writes to the live board:\nbefore %+v\nafter  %+v", before, got)
	}
	if snap.Len() != 10 {
		t.Fatalf("snapshot Len = %d, want 10", snap.Len())
	}
	if r, _ := snap.Rank("p09"); r != 10 {
		t.Fatalf("snapshot Rank(p09) = %d, want 10 (the big gain happened after the snapshot)", r)
	}
	// Writes to the copy do not leak back, and it keeps working as a normal board.
	snap.Set("only-in-snapshot", 99999)
	if _, ok := b.Score("only-in-snapshot"); ok {
		t.Fatal("a write to the snapshot appeared on the live board")
	}
	if r, _ := snap.Rank("only-in-snapshot"); r != 1 {
		t.Fatalf("Rank in the written-to snapshot = %d, want 1", r)
	}
	if got := New().Snapshot(); got == nil || got.Len() != 0 {
		t.Fatal("snapshot of an empty board must be an empty board")
	}
}

func TestStage3_SnapshotTiebreaksStayStable(t *testing.T) {
	b := New()
	b.Set("a", 5)
	b.Set("b", 5)
	snap := b.Snapshot()
	b.Set("a", 1)
	b.Set("a", 5) // a is now LATER than b on the live board
	wantOrder(t, snap, "a", "b")
	wantOrder(t, b, "b", "a")
	snap.Set("c", 5) // arrives after both inside the snapshot
	wantOrder(t, snap, "a", "b", "c")
}

func TestStage3_SnapshotPagingIsDuplicateFreeWhileWritersRun(t *testing.T) {
	b := New()
	const n = 2000
	for i := 0; i < n; i++ {
		b.Set(fmt.Sprintf("p%04d", i), int64(i%97))
	}
	var stop atomic.Bool
	var wg sync.WaitGroup
	for w := 0; w < 4; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r := rand.New(rand.NewSource(int64(w)))
			for !stop.Load() {
				// every write moves a player across many ranks
				b.Add(fmt.Sprintf("p%04d", r.Intn(n)), int64(r.Intn(201)-100))
			}
		}()
	}
	defer func() { stop.Store(true); wg.Wait() }()
	for round := 0; round < 5; round++ {
		snap := b.Snapshot()
		var all []Entry
		for off := 0; off < snap.Len(); off += 100 {
			all = append(all, snap.Page(off, 100)...)
			time.Sleep(time.Millisecond) // let the writers move the live board between pages
		}
		if len(all) != n {
			t.Fatalf("round %d: paging a snapshot returned %d entries, want %d", round, len(all), n)
		}
		if err := checkRanking(all, 1); err != nil {
			t.Fatalf("round %d: paging a snapshot: %v (a consistent snapshot lists everyone exactly once, in order)", round, err)
		}
	}
}

// ---------------------------------------------------------------- Sharded

func TestStage3_Sharded_MatchesASingleBoardOperationForOperation(t *testing.T) {
	for _, shards := range []int{1, 3, 8} {
		t.Run(fmt.Sprintf("shards=%d", shards), func(t *testing.T) {
			single, sh := New(), NewSharded(shards)
			r := rand.New(rand.NewSource(int64(shards)))
			for step := 0; step < 5000; step++ {
				p := fmt.Sprintf("player-%02d", r.Intn(70))
				switch r.Intn(6) {
				case 0, 1:
					d := int64(r.Intn(5) - 2)
					if a, b := single.Add(p, d), sh.Add(p, d); a != b {
						t.Fatalf("step %d: Add(%s,%d): board %d, sharded %d", step, p, d, a, b)
					}
				case 2, 3:
					s := int64(r.Intn(6))
					single.Set(p, s)
					sh.Set(p, s)
				case 4:
					if a, b := single.Remove(p), sh.Remove(p); a != b {
						t.Fatalf("step %d: Remove(%s): board %v, sharded %v", step, p, a, b)
					}
				case 5:
					if a, b := single.Len(), sh.Len(); a != b {
						t.Fatalf("step %d: Len: board %d, sharded %d", step, a, b)
					}
					k := r.Intn(25)
					if a, b := single.TopK(k), sh.TopK(k); !reflect.DeepEqual(a, b) && !(len(a) == 0 && len(b) == 0) {
						t.Fatalf("step %d: TopK(%d) differs:\nboard   %+v\nsharded %+v\n(ties across shards need ONE logical clock shared by all shards)", step, k, a, b)
					}
					q := fmt.Sprintf("player-%02d", r.Intn(70))
					ra, oka := single.Rank(q)
					rb, okb := sh.Rank(q)
					if ra != rb || oka != okb {
						t.Fatalf("step %d: Rank(%s): board %d,%v sharded %d,%v", step, q, ra, oka, rb, okb)
					}
					sa, _ := single.Score(q)
					sb, _ := sh.Score(q)
					if sa != sb {
						t.Fatalf("step %d: Score(%s): board %d sharded %d", step, q, sa, sb)
					}
				}
			}
			if single.Len() < 30 || sh.Len() != single.Len() {
				t.Fatalf("after the workload: board has %d players, sharded %d", single.Len(), sh.Len())
			}
		})
	}
}

func TestStage3_Sharded_TopKWhenKExceedsEveryShard(t *testing.T) {
	sh := NewSharded(8)
	for i := 0; i < 10; i++ {
		sh.Set(fmt.Sprintf("p%d", i), int64(100-i))
	}
	got := sh.TopK(10)
	if len(got) != 10 {
		t.Fatalf("TopK(10) over 10 players on 8 shards returned %d entries", len(got))
	}
	if err := checkRanking(got, 1); err != nil {
		t.Fatal(err)
	}
	if got[0].Player != "p0" || got[9].Player != "p9" {
		t.Fatalf("TopK(10) = %+v, want p0 first and p9 last", got)
	}
	if len(sh.TopK(0)) != 0 || len(sh.TopK(-3)) != 0 || len(NewSharded(0).TopK(5)) != 0 {
		t.Fatal("TopK with k <= 0, or on an empty board, must be empty (and NewSharded(0) must still work)")
	}
}

func TestStage3_Sharded_ConcurrentUseKeepsInvariants(t *testing.T) {
	sh := NewSharded(8)
	var writers, readers sync.WaitGroup
	var stop atomic.Bool
	for w := 0; w < 8; w++ {
		writers.Add(1)
		go func() {
			defer writers.Done()
			r := rand.New(rand.NewSource(int64(w)))
			for i := 0; i < 2500; i++ {
				p := fmt.Sprintf("p%03d", r.Intn(300))
				switch r.Intn(5) {
				case 0, 1, 2:
					sh.Add(p, int64(r.Intn(11)-5))
				case 3:
					sh.Set(p, int64(r.Intn(20)))
				case 4:
					sh.Remove(p)
				}
			}
		}()
	}
	for rd := 0; rd < 3; rd++ {
		readers.Add(1)
		go func() {
			defer readers.Done()
			for i := 0; i < 400 && !stop.Load(); i++ {
				runtime.Gosched()
				top := sh.TopK(20)
				for i, e := range top {
					if i > 0 && e.Score > top[i-1].Score {
						t.Errorf("TopK not sorted: %+v", top)
						return
					}
				}
				_, _ = sh.Rank("p001")
				_ = sh.Len()
			}
		}()
	}
	within(t, "sharded writers", testTimeout*6, writers.Wait)
	stop.Store(true)
	within(t, "sharded readers", testTimeout, readers.Wait)

	if sh.Len() < 150 {
		t.Fatalf("only %d of 300 players survived the workload", sh.Len())
	}
	all := sh.TopK(sh.Len() + 5)
	if len(all) != sh.Len() {
		t.Fatalf("TopK(Len+5) returned %d entries, Len = %d", len(all), sh.Len())
	}
	if err := checkRanking(all, 1); err != nil {
		t.Fatal(err)
	}
	for _, e := range all {
		if rk, ok := sh.Rank(e.Player); !ok || rk != e.Rank {
			t.Fatalf("Rank(%s) = %d, %v; TopK says %d", e.Player, rk, ok, e.Rank)
		}
	}
}

// --------------------------------------------------------------- Windowed

type fakeClock struct{ ns atomic.Int64 }

func newFakeClock() *fakeClock {
	c := &fakeClock{}
	c.ns.Store(time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC).UnixNano())
	return c
}
func (c *fakeClock) Now() time.Time          { return time.Unix(0, c.ns.Load()).UTC() }
func (c *fakeClock) Advance(d time.Duration) { c.ns.Add(int64(d)) }

const day = 24 * time.Hour

func TestStage3_Windowed_AccumulatesWithinAWindow(t *testing.T) {
	clk := newFakeClock()
	w := NewWindowed(clk.Now, day, 3)
	if got := w.Add("ann", 5); got != 5 {
		t.Fatalf("Add = %d, want 5", got)
	}
	clk.Advance(3 * time.Hour) // same calendar day
	if got := w.Add("ann", 2); got != 7 {
		t.Fatalf("Add later the same day = %d, want 7", got)
	}
	w.Add("bob", 9)
	wantOrder(t, w.Current(), "bob", "ann")
	if _, ok := w.Previous(1); ok {
		t.Fatal("Previous(1) reported a closed window before any boundary was crossed")
	}
}

func TestStage3_Windowed_RotatesWhenTheClockCrossesAMidnight(t *testing.T) {
	clk := newFakeClock() // 10:00
	w := NewWindowed(clk.Now, day, 3)
	w.Add("ann", 50)
	w.Add("bob", 40)
	clk.Advance(13 * time.Hour) // 23:00, still the same day
	w.Add("ann", 1)
	clk.Advance(2 * time.Hour) // 01:00 next day
	if got := w.Add("cat", 7); got != 7 {
		t.Fatalf("first Add of the new day = %d, want 7 (fresh window)", got)
	}
	if _, ok := w.Current().Score("ann"); ok {
		t.Fatal("yesterday's player is still on today's board")
	}
	prev, ok := w.Previous(1)
	if !ok {
		t.Fatal("Previous(1) missing after a rotation")
	}
	wantOrder(t, prev, "ann", "bob")
	mustScore(t, prev, "ann", 51)
	w.Add("ann", 1000) // must land in today's window, not yesterday's
	mustScore(t, prev, "ann", 51)
	if _, ok := w.Previous(2); ok {
		t.Fatal("Previous(2) exists, but only one window has closed")
	}
}

func TestStage3_Windowed_SkippedPeriodsAreEmptyWindows(t *testing.T) {
	clk := newFakeClock()
	w := NewWindowed(clk.Now, day, 5)
	w.Add("ann", 10)
	clk.Advance(3 * day) // nobody called anything on days 2 and 3
	w.Add("bob", 1)
	for n, wantLen := range map[int]int{1: 0, 2: 0, 3: 1} {
		b, ok := w.Previous(n)
		if !ok || b.Len() != wantLen {
			t.Fatalf("Previous(%d) = len %v, ok %v; want len %d (n counts calendar days back)", n, lenOf(b), ok, wantLen)
		}
	}
	if _, ok := w.Previous(4); ok {
		t.Fatal("Previous(4) exists, but only three windows have closed")
	}
}

func lenOf(b *Board) int {
	if b == nil {
		return -1
	}
	return b.Len()
}

func TestStage3_Windowed_KeepsOnlyTheLastKClosedWindows(t *testing.T) {
	clk := newFakeClock()
	w := NewWindowed(clk.Now, day, 2)
	for d := 1; d <= 4; d++ {
		w.Add(fmt.Sprintf("day%d", d), int64(d))
		clk.Advance(day)
	}
	w.Add("day5", 5)
	if _, ok := w.Previous(3); ok {
		t.Fatal("Previous(3) exists with keep = 2")
	}
	for n, want := range map[int]string{1: "day4", 2: "day3"} {
		b, ok := w.Previous(n)
		if !ok {
			t.Fatalf("Previous(%d) missing", n)
		}
		wantOrder(t, b, want)
	}
	if _, ok := w.Previous(0); ok {
		t.Fatal("Previous(0) must not exist: 1 is the latest closed window")
	}
}

func TestStage3_Windowed_RotateClosesTheWindowNow(t *testing.T) {
	clk := newFakeClock()
	w := NewWindowed(clk.Now, day, 3)
	w.Add("season1", 100)
	w.Rotate()
	w.Add("season2", 1)
	wantOrder(t, w.Current(), "season2")
	prev, ok := w.Previous(1)
	if !ok {
		t.Fatal("Previous(1) missing after Rotate")
	}
	wantOrder(t, prev, "season1")
}

func TestStage3_Windowed_ConcurrentAddsAcrossBoundariesLoseNothing(t *testing.T) {
	clk := newFakeClock()
	w := NewWindowed(clk.Now, day, 1000)
	const workers, per = 8, 500
	var wg sync.WaitGroup
	for g := 0; g < workers; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < per; i++ {
				w.Add("p", 1)
			}
		}()
	}
	var ticker sync.WaitGroup
	var stop atomic.Bool
	ticker.Add(1)
	go func() {
		defer ticker.Done()
		for !stop.Load() {
			clk.Advance(day) // a new window every iteration
			time.Sleep(50 * time.Microsecond)
		}
	}()
	within(t, "windowed writers", testTimeout*4, wg.Wait)
	stop.Store(true)
	ticker.Wait()

	total := int64(0)
	if s, ok := w.Current().Score("p"); ok {
		total += s
	}
	for n := 1; ; n++ {
		b, ok := w.Previous(n)
		if !ok {
			break
		}
		if s, ok := b.Score("p"); ok {
			total += s
		}
	}
	if total != workers*per {
		t.Fatalf("scores across all windows add up to %d, want %d: rotating while writers run must not lose or double-count an Add", total, workers*per)
	}
}
