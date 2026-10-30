package kvtx

import (
	"errors"
	"fmt"
	"math/rand"
	"runtime"
	"testing"
	"time"
)

const testTimeout = 5 * time.Second

func mustNew(t *testing.T) *Store {
	t.Helper()
	s, err := New()
	if err != nil {
		t.Fatalf("New() returned error %v", err)
	}
	if s == nil {
		t.Fatal("New returned a nil *Store with a nil error")
	}
	return s
}

// within fails the test if f does not return within testTimeout, so a deadlock
// or an accidental O(n^2) fails instead of hanging the run.
func within(t *testing.T, what string, f func()) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		f()
	}()
	select {
	case <-done:
	case <-time.After(testTimeout):
		t.Fatalf("%s did not finish within %v (deadlock, or a per-operation cost that grows with the store size)", what, testTimeout)
	}
}

// wantGet fails unless key is present with exactly this value.
func wantGet(t *testing.T, s *Store, key, want string) {
	t.Helper()
	got, ok := s.Get(key)
	if !ok || got != want {
		t.Fatalf("Get(%q) = (%q, %v), want (%q, true)", key, got, ok, want)
	}
}

// wantAbsent fails if key is present, even with the empty string.
func wantAbsent(t *testing.T, s *Store, key string) {
	t.Helper()
	if got, ok := s.Get(key); ok {
		t.Fatalf("Get(%q) = (%q, true), want the key to be absent", key, got)
	}
}

func wantLen(t *testing.T, s *Store, want int) {
	t.Helper()
	if got := s.Len(); got != want {
		t.Fatalf("Len() = %d, want %d", got, want)
	}
}

func wantDepth(t *testing.T, s *Store, want int) {
	t.Helper()
	if got := s.Depth(); got != want {
		t.Fatalf("Depth() = %d, want %d", got, want)
	}
}

func mustOK(t *testing.T, what string, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("%s returned unexpected error %v", what, err)
	}
}

func TestStage1_BasicGetSetDeleteLen(t *testing.T) {
	s := mustNew(t)
	wantAbsent(t, s, "a")
	wantLen(t, s, 0)
	if s.Delete("a") {
		t.Fatal("Delete of an absent key = true, want false")
	}
	s.Set("a", "1")
	s.Set("b", "2")
	wantGet(t, s, "a", "1")
	s.Set("a", "10") // overwrite
	wantGet(t, s, "a", "10")
	wantLen(t, s, 2)
	if !s.Delete("a") {
		t.Fatal("Delete of a present key = false, want true")
	}
	if s.Delete("a") {
		t.Fatal("second Delete = true, want false")
	}
	wantAbsent(t, s, "a")
	wantLen(t, s, 1)
	// The empty string is a value, not absence.
	s.Set("e", "")
	wantGet(t, s, "e", "")
	wantLen(t, s, 2)
	if !s.Delete("e") {
		t.Fatal("Delete of a key holding \"\" = false, want true: the key was present")
	}
	wantAbsent(t, s, "e")
}

func TestStage1_CommitAndRollbackWithoutTransactionFail(t *testing.T) {
	s := mustNew(t)
	s.Set("a", "1")
	if err := s.Commit(); !errors.Is(err, ErrNoTransaction) {
		t.Fatalf("Commit with no transaction = %v, want ErrNoTransaction", err)
	}
	if err := s.Rollback(); !errors.Is(err, ErrNoTransaction) {
		t.Fatalf("Rollback with no transaction = %v, want ErrNoTransaction", err)
	}
	wantGet(t, s, "a", "1") // a failed call must not disturb state
	wantDepth(t, s, 0)
	s.Begin()
	wantDepth(t, s, 1)
	mustOK(t, "Commit", s.Commit())
	wantDepth(t, s, 0)
	if err := s.Commit(); !errors.Is(err, ErrNoTransaction) {
		t.Fatalf("second Commit = %v, want ErrNoTransaction: the transaction was already closed", err)
	}
	s.Begin()
	mustOK(t, "Rollback", s.Rollback())
	if err := s.Rollback(); !errors.Is(err, ErrNoTransaction) {
		t.Fatalf("second Rollback = %v, want ErrNoTransaction", err)
	}
}

func TestStage1_RollbackRestoresOverwrittenAndDeletedValues(t *testing.T) {
	s := mustNew(t)
	s.Set("keep", "k")
	s.Set("over", "old")
	s.Set("gone", "g")
	s.Begin()
	s.Set("over", "new")
	if !s.Delete("gone") {
		t.Fatal("Delete(gone) inside a transaction = false, want true")
	}
	wantGet(t, s, "over", "new") // a transaction reads its own writes
	wantAbsent(t, s, "gone")
	wantLen(t, s, 2)
	mustOK(t, "Rollback", s.Rollback())
	wantGet(t, s, "over", "old")
	wantGet(t, s, "gone", "g") // rollback of a Delete must bring the value back
	wantGet(t, s, "keep", "k")
	wantLen(t, s, 3)
}

func TestStage1_RollbackOfFirstWriteRemovesTheKey(t *testing.T) {
	s := mustNew(t)
	s.Set("present-empty", "")
	s.Begin()
	s.Set("fresh", "x")
	s.Set("fresh-empty", "") // absence -> "" must roll back to absence, not to ""
	s.Set("present-empty", "now-set")
	mustOK(t, "Rollback", s.Rollback())
	wantAbsent(t, s, "fresh")
	wantAbsent(t, s, "fresh-empty")
	wantGet(t, s, "present-empty", "") // "" -> "now-set" rolls back to "", not to absence
	wantLen(t, s, 1)
	// A key that never existed and is deleted inside a transaction: still nothing to restore.
	s.Begin()
	if s.Delete("never") {
		t.Fatal("Delete(never) = true")
	}
	mustOK(t, "Rollback", s.Rollback())
	wantAbsent(t, s, "never")
	wantLen(t, s, 1)
}

func TestStage1_ManyWritesToOneKeyRollBackToTheOriginal(t *testing.T) {
	s := mustNew(t)
	s.Set("k", "orig")
	s.Begin()
	s.Set("k", "v1")
	s.Set("k", "v2")
	s.Delete("k")
	s.Set("k", "v3")
	s.Set("new", "n1")
	s.Delete("new")
	s.Set("new", "n2")
	wantGet(t, s, "k", "v3")
	mustOK(t, "Rollback", s.Rollback())
	wantGet(t, s, "k", "orig") // the FIRST touch is what must be restored, not the latest previous value
	wantAbsent(t, s, "new")
	wantLen(t, s, 1)
	// The next transaction starts with a clean log: nothing from the aborted one may leak.
	s.Begin()
	s.Set("other", "o")
	mustOK(t, "Rollback", s.Rollback())
	wantGet(t, s, "k", "orig")
	wantAbsent(t, s, "other")
	wantAbsent(t, s, "new")
}

func TestStage1_RollbackUndoesOnlyTheInnermost(t *testing.T) {
	s := mustNew(t)
	s.Set("a", "0")
	s.Begin() // level 1
	s.Set("a", "1")
	s.Set("b", "1")
	s.Begin() // level 2
	s.Set("a", "2")
	s.Set("c", "2")
	s.Delete("b")
	wantDepth(t, s, 2)
	mustOK(t, "inner Rollback", s.Rollback())
	wantDepth(t, s, 1)
	wantGet(t, s, "a", "1") // level 1's write survives
	wantGet(t, s, "b", "1") // level 2's delete is undone
	wantAbsent(t, s, "c")
	mustOK(t, "outer Rollback", s.Rollback())
	wantGet(t, s, "a", "0")
	wantAbsent(t, s, "b")
	wantLen(t, s, 1)
}

func TestStage1_InnerCommitMergesIntoParent(t *testing.T) {
	s := mustNew(t)
	s.Set("a", "0")
	s.Begin() // level 1
	s.Set("x", "1")
	s.Begin()       // level 2
	s.Set("a", "2") // parent has NOT touched a: the merged record must remember "0"
	s.Set("x", "2") // parent HAS touched x: the merged record must keep the parent's older one (absent)
	s.Set("y", "2")
	mustOK(t, "inner Commit", s.Commit())
	wantDepth(t, s, 1)
	wantGet(t, s, "a", "2")
	wantGet(t, s, "x", "2")
	mustOK(t, "outer Rollback", s.Rollback())
	// An inner commit is only provisional: the outer rollback undoes it too.
	wantGet(t, s, "a", "0")
	wantAbsent(t, s, "x") // restored to the parent's first-touch state (absent), not to the child's "1"
	wantAbsent(t, s, "y")
	wantLen(t, s, 1)
}

func TestStage1_CommitInTheMiddleOfNesting(t *testing.T) {
	s := mustNew(t)
	s.Set("a", "0")
	s.Begin() // 1
	s.Set("a", "1")
	s.Begin() // 2
	s.Set("a", "2")
	s.Set("b", "2")
	s.Begin() // 3
	s.Set("a", "3")
	mustOK(t, "commit 3", s.Commit()) // merges into 2
	wantGet(t, s, "a", "3")
	s.Begin() // 3 again
	s.Set("c", "3")
	mustOK(t, "rollback 3", s.Rollback()) // only c goes
	wantAbsent(t, s, "c")
	wantGet(t, s, "a", "3")
	mustOK(t, "commit 2", s.Commit()) // merges into 1
	wantDepth(t, s, 1)
	wantGet(t, s, "b", "2")
	mustOK(t, "rollback 1", s.Rollback()) // undoes everything since the outermost Begin
	wantGet(t, s, "a", "0")
	wantAbsent(t, s, "b")
	wantDepth(t, s, 0)
	wantLen(t, s, 1)
}

func TestStage1_OutermostCommitIsFinalAndOutsideWritesAreImmediate(t *testing.T) {
	s := mustNew(t)
	s.Begin()
	s.Set("a", "1")
	s.Begin()
	s.Set("b", "2")
	s.Delete("a")
	mustOK(t, "inner Commit", s.Commit())
	mustOK(t, "outer Commit", s.Commit())
	wantDepth(t, s, 0)
	wantAbsent(t, s, "a")
	wantGet(t, s, "b", "2")
	if err := s.Rollback(); !errors.Is(err, ErrNoTransaction) {
		t.Fatalf("Rollback after the outermost Commit = %v, want ErrNoTransaction: committed data cannot be undone", err)
	}
	wantGet(t, s, "b", "2")
	// Writes with no open transaction apply at once and are not undone by a later transaction's rollback.
	s.Set("c", "3")
	s.Begin()
	s.Set("d", "4")
	mustOK(t, "Rollback", s.Rollback())
	wantGet(t, s, "c", "3")
	wantAbsent(t, s, "d")
	wantLen(t, s, 2)
}

// model is the obviously correct specification: every Begin copies the whole
// map. It is the design the undo log replaces, so it can referee it.
type model struct{ stack []map[string]string }

func newModel() *model { return &model{stack: []map[string]string{{}}} }

func (m *model) top() map[string]string { return m.stack[len(m.stack)-1] }

func (m *model) begin() {
	c := make(map[string]string, len(m.top()))
	for k, v := range m.top() {
		c[k] = v
	}
	m.stack = append(m.stack, c)
}

func (m *model) commit() bool {
	n := len(m.stack)
	if n == 1 {
		return false
	}
	m.stack[n-2] = m.stack[n-1]
	m.stack = m.stack[:n-1]
	return true
}

func (m *model) rollback() bool {
	if len(m.stack) == 1 {
		return false
	}
	m.stack = m.stack[:len(m.stack)-1]
	return true
}

func (m *model) depth() int { return len(m.stack) - 1 }

var (
	modelKeys   = []string{"a", "b", "c", "d", "e"}
	modelValues = []string{"", "x", "y"}
)

// compare checks everything observable about s against the model.
func (m *model) compare(t *testing.T, s *Store, step string) {
	t.Helper()
	for _, k := range modelKeys {
		want, wantOK := m.top()[k]
		got, ok := s.Get(k)
		if ok != wantOK || got != want {
			t.Fatalf("%s: Get(%q) = (%q, %v), model says (%q, %v)", step, k, got, ok, want, wantOK)
		}
	}
	if s.Len() != len(m.top()) {
		t.Fatalf("%s: Len() = %d, model says %d", step, s.Len(), len(m.top()))
	}
	if s.Depth() != m.depth() {
		t.Fatalf("%s: Depth() = %d, model says %d", step, s.Depth(), m.depth())
	}
}

func TestStage1_RandomOperationsMatchAFullCopyModel(t *testing.T) {
	for seed := int64(1); seed <= 300; seed++ {
		rng := rand.New(rand.NewSource(seed))
		s := mustNew(t)
		m := newModel()
		for step := 0; step < 150; step++ {
			k := modelKeys[rng.Intn(len(modelKeys))]
			v := modelValues[rng.Intn(len(modelValues))]
			var what string
			switch op := rng.Intn(10); {
			case op < 3:
				what = fmt.Sprintf("Set(%q,%q)", k, v)
				s.Set(k, v)
				m.top()[k] = v
			case op < 5:
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
			case op < 9:
				what = "Commit"
				if err := s.Commit(); (err == nil) != m.commit() {
					t.Fatalf("seed %d step %d Commit error = %v, model disagrees", seed, step, err)
				}
			default:
				what = "Rollback"
				if err := s.Rollback(); (err == nil) != m.rollback() {
					t.Fatalf("seed %d step %d Rollback error = %v, model disagrees", seed, step, err)
				}
			}
			m.compare(t, s, fmt.Sprintf("seed %d step %d after %s", seed, step, what))
		}
	}
}

func TestStage1_BeginAndRollbackCostDoesNotGrowWithStoreSize(t *testing.T) {
	// Copying the map on Begin costs ~100k entries x 20k transactions = 2 billion
	// copies and misses the cap by a wide margin; an undo log touches one key.
	const keys, txs = 100_000, 20_000
	s := mustNew(t)
	for i := 0; i < keys; i++ {
		s.Set(fmt.Sprint("k", i), "v")
	}
	within(t, fmt.Sprintf("%d Begin/Set/Rollback cycles on a %d-key store", txs, keys), func() {
		for i := 0; i < txs; i++ {
			s.Begin()
			s.Set(fmt.Sprint("k", i), "changed")
			s.Begin()
			s.Delete(fmt.Sprint("k", i+1))
			_ = s.Commit()
			_ = s.Rollback()
		}
	})
	wantLen(t, s, keys)
	wantGet(t, s, "k7", "v")
}

func TestStage1_UndoLogKeepsOneRecordPerKey(t *testing.T) {
	// A log that appends on every write holds one record per write: 500 thousand
	// records (a few tens of MB) for these two keys. First-touch-only holds two.
	const writes = 250_000
	s := mustNew(t)
	s.Set("a", "orig")
	s.Begin()
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	for i := 0; i < writes; i++ {
		s.Set("a", "same-string-constant") // no per-write allocation of our own
		s.Set("b", "same-string-constant")
	}
	runtime.GC()
	runtime.ReadMemStats(&after)
	grew := int64(after.HeapAlloc) - int64(before.HeapAlloc)
	if grew > 8<<20 {
		t.Fatalf("heap grew by %d MB across %d writes to two keys: the undo log is recording every write instead of the first touch of each key", grew>>20, 2*writes)
	}
	mustOK(t, "Rollback", s.Rollback())
	wantGet(t, s, "a", "orig")
	wantAbsent(t, s, "b")
}
