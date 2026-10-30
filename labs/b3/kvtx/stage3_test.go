package kvtx

import (
	"errors"
	"fmt"
	"math/rand"
	"runtime"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
)

// newSession fails the test (instead of letting a nil *Session panic every
// later call) when NewSession is missing or broken.
func newSession(t *testing.T, s *Store) *Session {
	t.Helper()
	n := s.NewSession()
	if n == nil {
		t.Fatal("NewSession returned nil: stage 3 needs a second handle onto the same data")
	}
	return n
}

func wantConflict(t *testing.T, what string, err error) {
	t.Helper()
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("%s = %v, want an error satisfying errors.Is(err, ErrConflict)", what, err)
	}
}

func TestStage3_SessionsHaveIndependentTransactionStacks(t *testing.T) {
	a := mustNew(t)
	b := newSession(t, a)
	a.Begin()
	a.Begin()
	wantDepth(t, a, 2)
	wantDepth(t, b, 0) // b has no transaction of its own
	if err := b.Commit(); !errors.Is(err, ErrNoTransaction) {
		t.Fatalf("b.Commit = %v, want ErrNoTransaction: a's open transactions belong to a", err)
	}
	b.Begin()
	b.Set("x", "b")
	mustOK(t, "a.Rollback", a.Rollback())
	wantDepth(t, a, 1)
	wantDepth(t, b, 1)
	wantGet(t, b, "x", "b") // a's rollback did not touch b's writes
	mustOK(t, "b.Commit", b.Commit())
	mustOK(t, "a.Rollback", a.Rollback())
	wantGet(t, a, "x", "b") // and a's rollback did not undo b's commit
}

func TestStage3_UncommittedWritesAreInvisibleToOtherSessions(t *testing.T) {
	a := mustNew(t)
	b := newSession(t, a)
	a.Set("keep", "k")
	a.Set("gone", "g")
	a.Set("over", "old")
	a.Begin()
	a.Set("fresh", "f")
	a.Set("over", "new")
	a.Delete("gone")
	// a sees its own writes...
	wantGet(t, a, "fresh", "f")
	wantGet(t, a, "over", "new")
	wantAbsent(t, a, "gone")
	wantLen(t, a, 3)
	wantCount(t, a, "new", 1)
	// ...nobody else does: not Get, not Len, not Count, not MGet.
	wantAbsent(t, b, "fresh")
	wantGet(t, b, "over", "old")
	wantGet(t, b, "gone", "g")
	wantLen(t, b, 3)
	wantCount(t, b, "new", 0)
	wantCount(t, b, "old", 1)
	want := []Result{{"", false}, {"old", true}, {"g", true}}
	got := b.MGet("fresh", "over", "gone")
	if len(got) != len(want) {
		t.Fatalf("b.MGet returned %d results for 3 keys", len(got))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("b.MGet result %d = %+v, want %+v: uncommitted writes leaked to another session", i, got[i], want[i])
		}
	}
	// A rolled-back write is never visible, before or after.
	mustOK(t, "Rollback", a.Rollback())
	wantAbsent(t, b, "fresh")
	wantGet(t, b, "over", "old")
	wantGet(t, a, "gone", "g")
}

func TestStage3_CommitBecomesVisibleOnlyAtTheOutermostCommit(t *testing.T) {
	a := mustNew(t)
	b := newSession(t, a)
	a.Begin()
	a.Set("x", "1")
	a.Begin()
	a.Set("y", "2")
	mustOK(t, "inner Commit", a.Commit())
	wantAbsent(t, b, "x")
	wantAbsent(t, b, "y") // an inner commit only merges into the parent: still private
	wantGet(t, a, "y", "2")
	mustOK(t, "outer Commit", a.Commit())
	wantGet(t, b, "x", "1")
	wantGet(t, b, "y", "2")
	wantLen(t, b, 2)
	// Read committed: b's open transaction sees later commits (reads are not repeatable).
	b.Begin()
	wantGet(t, b, "x", "1")
	a.Set("x", "changed") // autocommit by another session
	wantGet(t, b, "x", "changed")
	wantCount(t, b, "changed", 1)
	mustOK(t, "b.Rollback", b.Rollback())
}

func TestStage3_FirstCommitterWinsAndTheLoserIsAbortedAtomically(t *testing.T) {
	a := mustNew(t)
	b := newSession(t, a)
	a.Set("shared", "0")
	a.Set("a-only", "0")
	a.Begin()
	b.Begin()
	a.Set("shared", "a")
	b.Set("b-only", "b") // disjoint key written first: must not be applied if the commit fails
	b.Set("shared", "b")
	b.Set("b-only2", "b2")
	mustOK(t, "a.Commit", a.Commit()) // first committer wins
	wantConflict(t, "b.Commit", b.Commit())
	wantDepth(t, b, 0) // the loser's transaction is gone
	wantGet(t, b, "shared", "a")
	wantAbsent(t, b, "b-only") // nothing of the aborted commit was applied
	wantAbsent(t, b, "b-only2")
	wantLen(t, b, 2)
	wantCount(t, b, "b", 0)
	// Retrying on fresh data succeeds.
	b.Begin()
	b.Set("shared", "b")
	mustOK(t, "retry Commit", b.Commit())
	wantGet(t, a, "shared", "b")
	// Disjoint writers never conflict.
	a.Begin()
	b.Begin()
	a.Set("a-only", "1")
	b.Set("b-only", "1")
	mustOK(t, "a.Commit", a.Commit())
	mustOK(t, "b.Commit", b.Commit())
	wantGet(t, a, "a-only", "1")
	wantGet(t, a, "b-only", "1")
}

func TestStage3_ConflictsComeFromVersionsNotValues(t *testing.T) {
	a := mustNew(t)
	b := newSession(t, a)
	a.Set("x", "orig")
	a.Set("gone", "g")

	// ABA: the key is changed and changed back, so the VALUE looks untouched.
	a.Begin()
	a.Set("x", "mine")
	b.Set("x", "other")
	b.Set("x", "orig")
	wantConflict(t, "Commit after an ABA change", a.Commit())

	// Another session's delete conflicts with a write.
	a.Begin()
	a.Set("x", "mine")
	if !b.Delete("x") {
		t.Fatal("b.Delete(x) = false")
	}
	wantConflict(t, "Commit after the key was deleted by someone else", a.Commit())
	wantAbsent(t, a, "x")

	// ...and a delete conflicts with another session's write.
	a.Begin()
	a.Delete("gone")
	b.Set("gone", "changed")
	wantConflict(t, "Delete-commit after the key was rewritten", a.Commit())
	wantGet(t, a, "gone", "changed")

	// Absent -> created and deleted again by someone else is still a change.
	a.Begin()
	a.Set("ghost", "mine")
	b.Set("ghost", "x")
	b.Delete("ghost")
	wantConflict(t, "Commit after the key was created and deleted by someone else", a.Commit())
	wantAbsent(t, a, "ghost")
}

func TestStage3_RolledBackWritesDoNotCauseConflicts(t *testing.T) {
	a := mustNew(t)
	b := newSession(t, a)
	a.Set("x", "0")
	a.Begin()
	a.Set("y", "1")
	a.Begin()
	a.Set("x", "inner")                       // a claim on x...
	mustOK(t, "inner Rollback", a.Rollback()) // ...withdrawn again
	mustOK(t, "Savepoint", a.Savepoint("sp"))
	a.Set("z", "z")
	mustOK(t, "RollbackTo", a.RollbackTo("sp")) // z is withdrawn too
	b.Set("x", "b")
	b.Set("z", "b")
	if err := a.Commit(); err != nil { // a still writes y, but neither x nor z
		t.Fatalf("Commit = %v, want success: the writes to x and z were rolled back, so they cannot conflict", err)
	}
	wantGet(t, a, "y", "1")
	wantGet(t, a, "x", "b")
	wantGet(t, a, "z", "b")
}

func TestStage3_GetForUpdatePreventsLostUpdates(t *testing.T) {
	a := mustNew(t)
	b := newSession(t, a)
	a.Set("n", "10")
	// Plain read-modify-write under read committed loses b's update; the
	// guard makes the second committer fail instead.
	a.Begin()
	v, ok := a.GetForUpdate("n")
	if !ok || v != "10" {
		t.Fatalf("GetForUpdate(n) = (%q, %v), want (10, true)", v, ok)
	}
	b.Begin()
	bv, _ := b.GetForUpdate("n")
	if bv != "10" {
		t.Fatalf("b.GetForUpdate(n) = %q, want 10", bv)
	}
	b.Set("n", "11")
	mustOK(t, "b.Commit", b.Commit())
	a.Set("n", "11") // a computed 10+1 from a stale read
	wantConflict(t, "a.Commit", a.Commit())
	wantGet(t, a, "n", "11")
	// A guard on a key that nobody else touches costs nothing.
	a.Begin()
	a.GetForUpdate("n")
	a.Set("n", "12")
	mustOK(t, "uncontended Commit", a.Commit())
	// A guard alone (no write) still fails if the key moved: the caller acted on a stale read.
	a.Begin()
	a.GetForUpdate("n")
	b.Set("n", "99")
	wantConflict(t, "Commit of a guarded-but-unwritten key", a.Commit())
	// Outside a transaction it is just a Get.
	if v, ok := a.GetForUpdate("n"); !ok || v != "99" {
		t.Fatalf("GetForUpdate outside a transaction = (%q, %v), want (99, true)", v, ok)
	}
	// An absent key can be guarded too (create-if-absent).
	a.Begin()
	if _, ok := a.GetForUpdate("fresh"); ok {
		t.Fatal("GetForUpdate(fresh) found a key that does not exist")
	}
	b.Set("fresh", "b")
	a.Set("fresh", "a")
	wantConflict(t, "create-if-absent that lost the race", a.Commit())
	wantGet(t, a, "fresh", "b")
}

func TestStage3_CountStaysCorrectWhenAnotherSessionCommitsUnderMyWrites(t *testing.T) {
	a := mustNew(t)
	b := newSession(t, a)
	a.Set("k", "old")
	a.Set("other", "y")
	a.Begin()
	a.Set("k", "mine") // a's view: k=mine
	a.Set("n", "y")
	wantCount(t, a, "mine", 1)
	wantCount(t, a, "old", 0)
	wantCount(t, a, "y", 2)
	// b rewrites the committed value underneath a's uncommitted write.
	b.Set("k", "y")
	b.Delete("other")
	// a still sees its own write on k; the committed counts moved.
	wantGet(t, a, "k", "mine")
	wantCount(t, a, "mine", 1)
	wantCount(t, a, "y", 1) // only n: other is gone, committed k=y is shadowed by a's write
	wantLen(t, a, 2)        // k, n
	mustOK(t, "Rollback", a.Rollback())
	wantCount(t, a, "y", 1) // now committed k=y shows through
	wantCount(t, a, "mine", 0)
	wantLen(t, a, 1)
}

func TestStage3_ConcurrentMultiKeyCommitsAreAtomic(t *testing.T) {
	const keyCount, writers, rounds, readers = 8, 3, 400, 4
	keys := make([]string, keyCount)
	root := mustNew(t)
	for i := range keys {
		keys[i] = fmt.Sprint("k", i)
		root.Set(keys[i], "init")
	}
	var wg sync.WaitGroup
	var stop atomic.Bool
	var torn atomic.Int64
	var example atomic.Value
	var commits atomic.Int64
	for r := 0; r < readers; r++ {
		wg.Add(1)
		go func(s *Session) {
			defer wg.Done()
			for !stop.Load() {
				res := s.MGet(keys...) // one consistent state, never half a commit
				for _, x := range res {
					if !x.OK || x.Value != res[0].Value {
						torn.Add(1)
						example.CompareAndSwap(nil, fmt.Sprintf("%+v", res))
						break
					}
				}
				runtime.Gosched() // on GOMAXPROCS=1 a spinning reader would otherwise starve the writers
			}
		}(newSession(t, root))
	}
	var ww sync.WaitGroup
	for w := 0; w < writers; w++ {
		ww.Add(1)
		go func(w int, s *Session) {
			defer ww.Done()
			for i := 0; i < rounds; i++ {
				val := fmt.Sprintf("w%d-%d", w, i)
				for {
					s.Begin()
					for _, k := range keys {
						s.Set(k, val)
					}
					err := s.Commit()
					if err == nil {
						commits.Add(1)
						break
					}
					if !errors.Is(err, ErrConflict) {
						t.Errorf("Commit = %v, want nil or ErrConflict", err)
						return
					}
				}
			}
		}(w, newSession(t, root))
	}
	within(t, "writers committing multi-key transactions", ww.Wait)
	stop.Store(true)
	within(t, "readers stopping", wg.Wait)
	if n := torn.Load(); n > 0 {
		t.Fatalf("%d MGet calls saw a half-applied commit, e.g. %v: the outermost commit must apply every key inside one critical section", n, example.Load())
	}
	if commits.Load() != writers*rounds {
		t.Fatalf("%d commits succeeded, want %d", commits.Load(), writers*rounds)
	}
	final := root.MGet(keys...)
	for _, x := range final {
		if !x.OK || x.Value != final[0].Value {
			t.Fatalf("final state is mixed: %+v", final)
		}
	}
}

func TestStage3_ConcurrentTransfersConserveMoney(t *testing.T) {
	// The ledger case: debit one account, credit another, as one unit. Under
	// read committed the read-modify-write is only safe with GetForUpdate.
	const accounts, workers, transfers, start = 6, 8, 1500, 1000
	root := mustNew(t)
	names := make([]string, accounts)
	for i := range names {
		names[i] = fmt.Sprint("acct", i)
		root.Set(names[i], strconv.Itoa(start))
	}
	var wg sync.WaitGroup
	var stop atomic.Bool
	var badSums atomic.Int64
	var auditWG sync.WaitGroup
	auditWG.Add(1)
	go func(s *Session) { // an auditor that must never see money in flight
		defer auditWG.Done()
		for !stop.Load() {
			sum := 0
			for _, r := range s.MGet(names...) {
				n, _ := strconv.Atoi(r.Value)
				sum += n
			}
			if sum != accounts*start {
				badSums.Add(1)
			}
			runtime.Gosched()
		}
	}(newSession(t, root))
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int, s *Session) {
			defer wg.Done()
			rng := rand.New(rand.NewSource(int64(w)))
			for i := 0; i < transfers; i++ {
				from, to := rng.Intn(accounts), rng.Intn(accounts)
				if from == to {
					continue
				}
				abort := i%9 == 0 // some transfers are abandoned half way and must leave no trace
				for {
					s.Begin()
					fv, _ := s.GetForUpdate(names[from])
					tv, _ := s.GetForUpdate(names[to])
					f, _ := strconv.Atoi(fv)
					g, _ := strconv.Atoi(tv)
					s.Set(names[from], strconv.Itoa(f-1))
					if abort {
						_ = s.Rollback()
						break
					}
					s.Set(names[to], strconv.Itoa(g+1))
					err := s.Commit()
					if err == nil {
						break
					}
					if !errors.Is(err, ErrConflict) {
						t.Errorf("Commit = %v, want nil or ErrConflict", err)
						return
					}
				}
			}
		}(w, newSession(t, root))
	}
	within(t, "concurrent transfers", wg.Wait)
	stop.Store(true)
	within(t, "auditor stopping", auditWG.Wait)
	if n := badSums.Load(); n > 0 {
		t.Fatalf("the auditor saw a wrong total %d times: a debit was visible without its credit (half-applied transfer)", n)
	}
	total := 0
	for _, r := range root.MGet(names...) {
		n, _ := strconv.Atoi(r.Value)
		total += n
	}
	if total != accounts*start {
		t.Fatalf("total = %d, want %d: money was created or destroyed (lost update, partial commit, or a rollback that leaked)", total, accounts*start)
	}
}

func TestStage3_ConcurrentMixedWorkloadStaysConsistent(t *testing.T) {
	const workers, steps = 8, 3_000
	keys := []string{"a", "b", "c", "d", "e", "f", "g", "h"}
	vals := []string{"", "x", "y", "z"}
	root := mustNew(t)
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int, s *Session) {
			defer wg.Done()
			rng := rand.New(rand.NewSource(int64(w) + 100))
			for i := 0; i < steps; i++ {
				k := keys[rng.Intn(len(keys))]
				switch op := rng.Intn(12); {
				case op < 4:
					v := vals[rng.Intn(len(vals))]
					s.Set(k, v)
					// Outside a transaction another session may rewrite k between the two calls.
					if got, ok := s.Get(k); s.Depth() > 0 && (!ok || got != v) {
						t.Errorf("Get(%q) = (%q, %v) right after Set(%q, %q): a session must always read its own writes", k, got, ok, k, v)
						return
					}
				case op < 6:
					deleted := s.Delete(k) // a Delete of an absent key is a no-op, so only a real delete is checked
					if _, ok := s.Get(k); ok && deleted && s.Depth() > 0 {
						t.Errorf("Get(%q) found the key right after Delete: a session must always read its own writes", k)
						return
					}
				case op < 8:
					s.Begin()
				case op < 9:
					_ = s.Commit()
				case op < 10:
					_ = s.Rollback()
				case op < 11:
					_ = s.Savepoint("sp")
				default:
					s.Count(vals[rng.Intn(len(vals))])
				}
			}
			for s.Depth() > 0 { // close everything: commits may conflict, rollbacks never do
				_ = s.Rollback()
			}
		}(w, newSession(t, root))
	}
	within(t, "concurrent mixed workload", wg.Wait)
	// With every session idle, the incremental counters must agree with the data.
	check := newSession(t, root)
	sum := 0
	for _, v := range vals {
		sum += check.Count(v)
	}
	present := 0
	for _, r := range check.MGet(keys...) {
		if r.OK {
			present++
		}
	}
	if sum != present || check.Len() != present {
		t.Fatalf("sum of Count = %d, Len = %d, keys actually present = %d: the incremental counters drifted from the data", sum, check.Len(), present)
	}
}
