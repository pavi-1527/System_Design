package kvtx

import (
	"errors"
	"fmt"
	"math/rand"
	"testing"
)

func wantCount(t *testing.T, s *Store, value string, want int) {
	t.Helper()
	if got := s.Count(value); got != want {
		t.Fatalf("Count(%q) = %d, want %d", value, got, want)
	}
}

func TestStage2_CountBasics(t *testing.T) {
	s := mustNew(t)
	wantCount(t, s, "x", 0)
	s.Set("a", "x")
	s.Set("b", "x")
	s.Set("c", "y")
	wantCount(t, s, "x", 2)
	wantCount(t, s, "y", 1)
	s.Set("a", "y") // overwrite moves a from x to y
	wantCount(t, s, "x", 1)
	wantCount(t, s, "y", 2)
	s.Set("a", "y") // same value again: must not count twice
	wantCount(t, s, "y", 2)
	s.Delete("b")
	wantCount(t, s, "x", 0)
	wantCount(t, s, "never-used", 0)
	// The empty string is a value like any other; absent keys are never counted.
	wantCount(t, s, "", 0)
	s.Set("e1", "")
	s.Set("e2", "")
	wantCount(t, s, "", 2)
	s.Delete("e1")
	s.Delete("not-there")
	wantCount(t, s, "", 1)
}

func TestStage2_DeleteOfAMissingKeyDoesNotChangeCounts(t *testing.T) {
	s := mustNew(t)
	s.Set("a", "x")
	s.Delete("ghost") // outside a transaction
	wantCount(t, s, "x", 1)
	s.Begin()
	if s.Delete("ghost") {
		t.Fatal("Delete(ghost) = true")
	}
	s.Delete("a")
	s.Delete("a") // already deleted in this transaction
	wantCount(t, s, "x", 0)
	mustOK(t, "Rollback", s.Rollback())
	wantCount(t, s, "x", 1) // a decrement taken twice would leave 0 or -1 here
}

func TestStage2_CountFollowsRollback(t *testing.T) {
	s := mustNew(t)
	s.Set("a", "x")
	s.Set("b", "x")
	s.Begin()
	s.Set("a", "y") // overwrite
	s.Delete("b")   // delete
	s.Set("c", "y") // first write of a new key
	s.Set("c", "z") // overwrite inside the same transaction
	s.Set("d", "")  // empty string value
	wantCount(t, s, "x", 0)
	wantCount(t, s, "y", 1)
	wantCount(t, s, "z", 1)
	wantCount(t, s, "", 1)
	mustOK(t, "Rollback", s.Rollback())
	wantCount(t, s, "x", 2)
	wantCount(t, s, "y", 0)
	wantCount(t, s, "z", 0)
	wantCount(t, s, "", 0)
	wantLen(t, s, 2)
}

func TestStage2_CountAcrossNestedRollbackAndCommit(t *testing.T) {
	s := mustNew(t)
	s.Set("a", "x")
	s.Begin() // 1
	s.Set("a", "y")
	s.Set("b", "x")
	s.Begin() // 2
	s.Set("a", "z")
	s.Delete("b")
	s.Set("c", "x")
	wantCount(t, s, "x", 1) // only c
	mustOK(t, "rollback 2", s.Rollback())
	wantCount(t, s, "x", 1) // b is back, c is gone
	wantCount(t, s, "y", 1)
	wantCount(t, s, "z", 0)
	s.Begin() // 2 again
	s.Set("a", "x")
	mustOK(t, "commit 2", s.Commit())
	wantCount(t, s, "x", 2) // a and b
	wantCount(t, s, "y", 0)
	mustOK(t, "rollback 1", s.Rollback())
	wantCount(t, s, "x", 1) // back to the original a=x
	wantCount(t, s, "y", 0)
	wantLen(t, s, 1)
	s.Begin()
	s.Set("q", "x")
	mustOK(t, "commit", s.Commit())
	wantCount(t, s, "x", 2) // committed data keeps its counts
}

func TestStage2_CountIsConstantTime(t *testing.T) {
	// Counting by scanning (the store, or the transaction's write set) costs
	// 50k keys x 50k calls; an incrementally maintained count costs nothing.
	const keys, calls = 50_000, 50_000
	s := mustNew(t)
	for i := 0; i < keys; i++ {
		s.Set(fmt.Sprint("k", i), fmt.Sprint("v", i%7))
	}
	s.Begin() // a large open write set must not make Count slower
	for i := 0; i < keys; i += 2 {
		s.Set(fmt.Sprint("k", i), "w")
	}
	var total int
	within(t, fmt.Sprintf("%d Count calls on a %d-key store with %d uncommitted writes", calls, keys, keys/2), func() {
		for i := 0; i < calls; i++ {
			total += s.Count("w")
			if i%1000 == 0 {
				s.Set(fmt.Sprint("k", i+1), "w") // mutate between calls: the count must follow
			}
		}
	})
	if total == 0 {
		t.Fatal("Count(w) was always 0")
	}
	wantCount(t, s, "w", keys/2+calls/1000)
	mustOK(t, "Rollback", s.Rollback())
	wantCount(t, s, "w", 0)
	wantCount(t, s, "v0", (keys+6)/7)
}

func TestStage2_SavepointErrors(t *testing.T) {
	s := mustNew(t)
	if err := s.Savepoint("sp"); !errors.Is(err, ErrNoTransaction) {
		t.Fatalf("Savepoint outside a transaction = %v, want ErrNoTransaction", err)
	}
	s.Begin()
	if err := s.Savepoint(""); !errors.Is(err, ErrInvalidName) {
		t.Fatalf("Savepoint(\"\") = %v, want ErrInvalidName", err)
	}
	s.Set("a", "1")
	if err := s.RollbackTo("nope"); !errors.Is(err, ErrNoSavepoint) {
		t.Fatalf("RollbackTo(unknown) = %v, want ErrNoSavepoint", err)
	}
	if err := s.Release("nope"); !errors.Is(err, ErrNoSavepoint) {
		t.Fatalf("Release(unknown) = %v, want ErrNoSavepoint", err)
	}
	if err := s.RollbackTo(""); !errors.Is(err, ErrNoSavepoint) {
		t.Fatalf("RollbackTo(\"\") = %v, want ErrNoSavepoint: a plain Begin is not a savepoint", err)
	}
	wantGet(t, s, "a", "1") // failed calls change nothing
	wantDepth(t, s, 1)
}

func TestStage2_RollbackToUndoesSinceTheSavepointAndKeepsIt(t *testing.T) {
	s := mustNew(t)
	s.Set("a", "0")
	s.Begin()
	s.Set("a", "1")
	s.Set("keep", "k")
	mustOK(t, "Savepoint", s.Savepoint("sp"))
	s.Set("a", "2")
	s.Set("late", "l")
	s.Delete("keep")
	wantCount(t, s, "l", 1)
	mustOK(t, "RollbackTo", s.RollbackTo("sp"))
	wantGet(t, s, "a", "1")    // work done BEFORE the savepoint survives
	wantGet(t, s, "keep", "k") // a delete after the savepoint is undone
	wantAbsent(t, s, "late")
	wantCount(t, s, "l", 0)
	wantDepth(t, s, 2) // the savepoint is still open
	s.Set("a", "3")
	mustOK(t, "second RollbackTo", s.RollbackTo("sp")) // usable again
	wantGet(t, s, "a", "1")
	mustOK(t, "Release", s.Release("sp"))
	wantDepth(t, s, 1)
	mustOK(t, "Rollback", s.Rollback())
	wantGet(t, s, "a", "0")
	wantAbsent(t, s, "keep")
}

func TestStage2_ReleaseKeepsChangesInTheEnclosingTransaction(t *testing.T) {
	s := mustNew(t)
	s.Set("a", "0")
	s.Begin()
	mustOK(t, "Savepoint", s.Savepoint("sp"))
	s.Set("a", "1")
	s.Set("b", "1")
	mustOK(t, "Release", s.Release("sp"))
	wantDepth(t, s, 1)
	wantGet(t, s, "a", "1")
	if err := s.RollbackTo("sp"); !errors.Is(err, ErrNoSavepoint) {
		t.Fatalf("RollbackTo after Release = %v, want ErrNoSavepoint", err)
	}
	mustOK(t, "Rollback", s.Rollback()) // released changes are still undone by the enclosing rollback
	wantGet(t, s, "a", "0")
	wantAbsent(t, s, "b")
	wantCount(t, s, "1", 0)
}

func TestStage2_SavepointsNestAndShadow(t *testing.T) {
	s := mustNew(t)
	s.Begin()
	s.Set("k", "base")
	mustOK(t, "sp outer", s.Savepoint("sp"))
	s.Set("k", "outer")
	mustOK(t, "sp inner", s.Savepoint("sp")) // same name: shadows the first
	s.Set("k", "inner")
	mustOK(t, "RollbackTo", s.RollbackTo("sp")) // the innermost sp
	wantGet(t, s, "k", "outer")
	wantDepth(t, s, 3)
	mustOK(t, "Savepoint other", s.Savepoint("other"))
	s.Set("k", "other")
	mustOK(t, "Savepoint third", s.Savepoint("third"))
	s.Set("z", "z")
	// Rolling back to an outer savepoint closes the ones opened after it.
	mustOK(t, "RollbackTo other", s.RollbackTo("other"))
	wantGet(t, s, "k", "outer")
	wantAbsent(t, s, "z")
	wantDepth(t, s, 4) // base tx, sp, sp, other
	if err := s.RollbackTo("third"); !errors.Is(err, ErrNoSavepoint) {
		t.Fatalf("RollbackTo(third) = %v, want ErrNoSavepoint: it was closed by RollbackTo(other)", err)
	}
	// Releasing an outer savepoint merges everything inside it.
	mustOK(t, "Release sp", s.Release("sp")) // the inner sp, plus "other" above it
	wantDepth(t, s, 2)
	wantGet(t, s, "k", "outer")
	mustOK(t, "RollbackTo sp (outer)", s.RollbackTo("sp"))
	wantGet(t, s, "k", "base")
}

func TestStage2_SavepointsAreOrdinaryFramesForDepthCommitAndRollback(t *testing.T) {
	s := mustNew(t)
	s.Begin()
	mustOK(t, "Savepoint", s.Savepoint("sp"))
	wantDepth(t, s, 2)
	s.Set("a", "1")
	mustOK(t, "Commit of the innermost frame (the savepoint)", s.Commit())
	wantDepth(t, s, 1)
	wantGet(t, s, "a", "1")
	mustOK(t, "Savepoint", s.Savepoint("sp2"))
	s.Set("a", "2")
	mustOK(t, "Rollback of the innermost frame (the savepoint)", s.Rollback())
	wantGet(t, s, "a", "1")
	mustOK(t, "outermost Commit", s.Commit())
	wantGet(t, s, "a", "1")
	wantDepth(t, s, 0)
}

func TestStage2_RandomOperationsWithCountsAndSavepointsMatchTheModel(t *testing.T) {
	spNames := []string{"p", "q", "r"}
	for seed := int64(1); seed <= 300; seed++ {
		rng := rand.New(rand.NewSource(seed * 7919))
		s := mustNew(t)
		m := newModel()
		frameNames := []string{""} // name of each model frame ("" = plain Begin); index 0 is the base store
		find := func(name string) int {
			for i := len(frameNames) - 1; i >= 1; i-- {
				if frameNames[i] == name {
					return i
				}
			}
			return -1
		}
		for step := 0; step < 150; step++ {
			k := modelKeys[rng.Intn(len(modelKeys))]
			v := modelValues[rng.Intn(len(modelValues))]
			name := spNames[rng.Intn(len(spNames))]
			var what string
			switch op := rng.Intn(14); {
			case op < 4:
				what = fmt.Sprintf("Set(%q,%q)", k, v)
				s.Set(k, v)
				m.top()[k] = v
			case op < 6:
				what = fmt.Sprintf("Delete(%q)", k)
				_, existed := m.top()[k]
				if got := s.Delete(k); got != existed {
					t.Fatalf("seed %d step %d %s = %v, model says %v", seed, step, what, got, existed)
				}
				delete(m.top(), k)
			case op < 7:
				what = "Begin"
				s.Begin()
				m.begin()
				frameNames = append(frameNames, "")
			case op < 8:
				what = "Commit"
				if err := s.Commit(); (err == nil) != m.commit() {
					t.Fatalf("seed %d step %d Commit error = %v, model disagrees", seed, step, err)
				}
				if len(frameNames) > 1 {
					frameNames = frameNames[:len(frameNames)-1]
				}
			case op < 9:
				what = "Rollback"
				if err := s.Rollback(); (err == nil) != m.rollback() {
					t.Fatalf("seed %d step %d Rollback error = %v, model disagrees", seed, step, err)
				}
				if len(frameNames) > 1 {
					frameNames = frameNames[:len(frameNames)-1]
				}
			case op < 11:
				what = fmt.Sprintf("Savepoint(%q)", name)
				err := s.Savepoint(name)
				if m.depth() == 0 {
					if !errors.Is(err, ErrNoTransaction) {
						t.Fatalf("seed %d step %d %s at depth 0 = %v, want ErrNoTransaction", seed, step, what, err)
					}
				} else {
					mustOK(t, what, err)
					m.begin()
					frameNames = append(frameNames, name)
				}
			case op < 13:
				what = fmt.Sprintf("RollbackTo(%q)", name)
				err := s.RollbackTo(name)
				i := find(name)
				if i < 0 {
					if !errors.Is(err, ErrNoSavepoint) {
						t.Fatalf("seed %d step %d %s = %v, want ErrNoSavepoint", seed, step, what, err)
					}
				} else {
					mustOK(t, what, err)
					m.stack = m.stack[:i+1]
					frameNames = frameNames[:i+1]
					c := map[string]string{}
					for kk, vv := range m.stack[i-1] {
						c[kk] = vv
					}
					m.stack[i] = c
				}
			default:
				what = fmt.Sprintf("Release(%q)", name)
				err := s.Release(name)
				i := find(name)
				if i < 0 {
					if !errors.Is(err, ErrNoSavepoint) {
						t.Fatalf("seed %d step %d %s = %v, want ErrNoSavepoint", seed, step, what, err)
					}
				} else {
					mustOK(t, what, err)
					m.stack[i-1] = m.top()
					m.stack = m.stack[:i]
					frameNames = frameNames[:i]
				}
			}
			where := fmt.Sprintf("seed %d step %d after %s", seed, step, what)
			m.compare(t, s, where)
			for _, val := range modelValues {
				want := 0
				for _, mv := range m.top() {
					if mv == val {
						want++
					}
				}
				if got := s.Count(val); got != want {
					t.Fatalf("%s: Count(%q) = %d, model says %d", where, val, got, want)
				}
			}
		}
	}
}
