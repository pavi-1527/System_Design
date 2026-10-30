package isolation

import (
	"errors"
	"fmt"
	"math/rand/v2"
	"reflect"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// ---------- helpers (prefixed with t to avoid clashing with your code) ----------

func tMustCommit(t *testing.T, tx *Tx) {
	t.Helper()
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}
}

func tPut(t *testing.T, tx *Tx, k, v string) {
	t.Helper()
	if err := tx.Put(k, v); err != nil {
		t.Fatalf("Put(%q): %v", k, err)
	}
}

// tSeed commits the given key/value pairs in one transaction.
func tSeed(t *testing.T, s *Store, kv map[string]string) {
	t.Helper()
	tx := s.Begin(SnapshotIsolation)
	for k, v := range kv {
		tPut(t, tx, k, v)
	}
	tMustCommit(t, tx)
}

func tExpect(t *testing.T, tx *Tx, k, want string, wantOK bool) {
	t.Helper()
	got, ok := tx.Get(k)
	if got != want || ok != wantOK {
		t.Fatalf("[%v] Get(%q) = %q, %v; want %q, %v", tx.Level(), k, got, ok, want, wantOK)
	}
}

func tInt(t *testing.T, tx *Tx, k string) int {
	t.Helper()
	v, ok := tx.Get(k)
	if !ok {
		return 0
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		t.Fatalf("Get(%q) = %q, not an int", k, v)
	}
	return n
}

func tSetInt(t *testing.T, tx *Tx, k string, n int) {
	t.Helper()
	tPut(t, tx, k, strconv.Itoa(n))
}

// tCommittedInt reads k in a fresh transaction.
func tCommittedInt(t *testing.T, s *Store, k string) int {
	t.Helper()
	tx := s.Begin(SnapshotIsolation)
	defer tx.Rollback()
	return tInt(t, tx, k)
}

var tAllLevels = []Level{ReadCommitted, SnapshotIsolation, Serializable}
var tSnapshotLevels = []Level{SnapshotIsolation, Serializable}

// ---------- visibility ----------

func TestTx_ReadsItsOwnWritesAndRollbackDiscardsThem(t *testing.T) {
	for _, lvl := range tAllLevels {
		s := NewStore()
		tx := s.Begin(lvl)
		tExpect(t, tx, "a", "", false)
		tPut(t, tx, "a", "1")
		tExpect(t, tx, "a", "1", true)
		tPut(t, tx, "a", "2")
		tExpect(t, tx, "a", "2", true)
		if err := tx.Delete("a"); err != nil {
			t.Fatal(err)
		}
		tExpect(t, tx, "a", "", false)
		tPut(t, tx, "b", "x")
		tx.Rollback()
		other := s.Begin(lvl)
		tExpect(t, other, "b", "", false)
		if s.Clock() != 0 {
			t.Errorf("[%v] clock = %d after a rollback; only commits that wrote something advance it", lvl, s.Clock())
		}
	}
}

func TestNoDirtyReads_UncommittedWritesAreInvisibleToOthers(t *testing.T) {
	for _, lvl := range tAllLevels {
		s := NewStore()
		tSeed(t, s, map[string]string{"bal": "100"})
		writer, reader := s.Begin(lvl), s.Begin(lvl)
		tSetInt(t, writer, "bal", 0)
		tExpect(t, reader, "bal", "100", true)
		tMustCommit(t, writer)
		if lvl == ReadCommitted {
			tExpect(t, reader, "bal", "0", true) // committed now, visible at this level
		} else {
			tExpect(t, reader, "bal", "100", true)
		}
	}
}

func TestSnapshotIsolation_RepeatableReadsFromTheSnapshotTakenAtBegin(t *testing.T) {
	for _, lvl := range tSnapshotLevels {
		s := NewStore()
		tSeed(t, s, map[string]string{"x": "1"})
		long := s.Begin(lvl)
		tExpect(t, long, "x", "1", true)
		upd := s.Begin(lvl)
		tPut(t, upd, "x", "2")
		tMustCommit(t, upd)
		tExpect(t, long, "x", "1", true) // same answer as before: repeatable read
		tExpect(t, s.Begin(lvl), "x", "2", true)
	}
}

func TestSnapshotIsolation_TheSnapshotIsTakenAtBeginNotAtFirstRead(t *testing.T) {
	for _, lvl := range tSnapshotLevels {
		s := NewStore()
		tSeed(t, s, map[string]string{"x": "old"})
		tx := s.Begin(lvl) // has not read anything yet
		upd := s.Begin(lvl)
		tPut(t, upd, "x", "new")
		tPut(t, upd, "y", "created")
		tMustCommit(t, upd)
		tExpect(t, tx, "x", "old", true)
		tExpect(t, tx, "y", "", false)
		if tx.Snapshot() != 1 {
			t.Errorf("[%v] snapshot = %d, want 1 (the clock when it began)", lvl, tx.Snapshot())
		}
	}
}

func TestReadCommitted_NonRepeatableReadsAreAllowed(t *testing.T) {
	s := NewStore()
	tSeed(t, s, map[string]string{"x": "1"})
	rc := s.Begin(ReadCommitted)
	tExpect(t, rc, "x", "1", true)
	upd := s.Begin(ReadCommitted)
	tPut(t, upd, "x", "2")
	tMustCommit(t, upd)
	tExpect(t, rc, "x", "2", true) // every read sees the latest commit: the anomaly
}

func TestDeletedKeysAreInvisibleToLaterSnapshotsButNotToEarlierOnes(t *testing.T) {
	s := NewStore()
	tSeed(t, s, map[string]string{"k": "v"})
	before := s.Begin(SnapshotIsolation)
	del := s.Begin(SnapshotIsolation)
	if err := del.Delete("k"); err != nil {
		t.Fatal(err)
	}
	tMustCommit(t, del)
	after := s.Begin(SnapshotIsolation)
	tExpect(t, before, "k", "v", true)
	tExpect(t, after, "k", "", false)
	// And it can come back.
	re := s.Begin(SnapshotIsolation)
	tPut(t, re, "k", "again")
	tMustCommit(t, re)
	tExpect(t, s.Begin(SnapshotIsolation), "k", "again", true)
	tExpect(t, after, "k", "", false)
}

// ---------- lost update ----------

// tWithdraw reads the balance, then writes balance-amt: the classic
// read-modify-write that an application does in two statements.
func tWithdrawBoth(t *testing.T, lvl Level) (first, second error, final int) {
	s := NewStore()
	tSeed(t, s, map[string]string{"bal": "100"})
	a, b := s.Begin(lvl), s.Begin(lvl)
	balA, balB := tInt(t, a, "bal"), tInt(t, b, "bal") // both read 100
	tSetInt(t, a, "bal", balA-30)
	tSetInt(t, b, "bal", balB-50)
	first = a.Commit()
	second = b.Commit()
	return first, second, tCommittedInt(t, s, "bal")
}

func TestLostUpdate_HappensUnderReadCommitted(t *testing.T) {
	first, second, final := tWithdrawBoth(t, ReadCommitted)
	if first != nil || second != nil {
		t.Fatalf("read committed must let both commit, got %v and %v", first, second)
	}
	if final != 50 {
		t.Fatalf("final balance = %d, want 50: the second writer overwrote the first (100-30-50 should have been 20, the $30 withdrawal is lost)", final)
	}
}

func TestLostUpdate_PreventedBySnapshotIsolationFirstCommitterWins(t *testing.T) {
	for _, lvl := range tSnapshotLevels {
		first, second, final := tWithdrawBoth(t, lvl)
		if first != nil {
			t.Fatalf("[%v] first committer failed: %v", lvl, first)
		}
		if !errors.Is(second, ErrWriteConflict) {
			t.Fatalf("[%v] second committer got %v, want ErrWriteConflict (it updated a row that changed after its snapshot)", lvl, second)
		}
		if final != 70 {
			t.Fatalf("[%v] final balance = %d, want 70 (only the first withdrawal applied)", lvl, final)
		}
	}
}

func TestFirstCommitterWins_AlsoCoversBlindWritesAndDeletes(t *testing.T) {
	for _, lvl := range tSnapshotLevels {
		s := NewStore()
		tSeed(t, s, map[string]string{"k": "v0"})
		a, b := s.Begin(lvl), s.Begin(lvl)
		tPut(t, a, "k", "a") // neither transaction reads k
		tPut(t, b, "k", "b")
		tMustCommit(t, a)
		if err := b.Commit(); !errors.Is(err, ErrWriteConflict) {
			t.Errorf("[%v] blind write-write: got %v, want ErrWriteConflict", lvl, err)
		}
		c, d := s.Begin(lvl), s.Begin(lvl)
		_ = c.Delete("k")
		tPut(t, d, "k", "resurrect")
		tMustCommit(t, c)
		if err := d.Commit(); !errors.Is(err, ErrWriteConflict) {
			t.Errorf("[%v] put vs committed delete: got %v, want ErrWriteConflict", lvl, err)
		}
	}
}

func TestFirstCommitterWins_NoConflictWithoutOverlap(t *testing.T) {
	for _, lvl := range tSnapshotLevels {
		s := NewStore()
		tSeed(t, s, map[string]string{"x": "0", "y": "0"})
		// Disjoint write sets overlap in time: both commit.
		a, b := s.Begin(lvl), s.Begin(lvl)
		tPut(t, a, "x", "a")
		tPut(t, b, "y", "b")
		tMustCommit(t, a)
		if err := b.Commit(); err != nil {
			t.Errorf("[%v] disjoint writes: %v", lvl, err)
		}
		// Same key, but the second transaction started after the first committed.
		c := s.Begin(lvl)
		tPut(t, c, "x", "c")
		tMustCommit(t, c)
		d := s.Begin(lvl)
		tPut(t, d, "x", "d")
		if err := d.Commit(); err != nil {
			t.Errorf("[%v] sequential writes to one key: %v", lvl, err)
		}
		final := s.Begin(lvl)
		tExpect(t, final, "y", "b", true)
		tExpect(t, final, "x", "d", true)
	}
}

func TestFailedCommitInstallsNothing(t *testing.T) {
	for _, lvl := range tSnapshotLevels {
		s := NewStore()
		tSeed(t, s, map[string]string{"x": "0"})
		a, b := s.Begin(lvl), s.Begin(lvl)
		tPut(t, a, "x", "a")
		tMustCommit(t, a)
		tPut(t, b, "y", "should-not-appear")
		tPut(t, b, "x", "b")
		if err := b.Commit(); !errors.Is(err, ErrWriteConflict) {
			t.Fatalf("[%v] got %v", lvl, err)
		}
		check := s.Begin(lvl)
		tExpect(t, check, "y", "", false) // the transaction is atomic: all or nothing
		tExpect(t, check, "x", "a", true)
		if s.Versions("y") != 0 {
			t.Errorf("[%v] a version of y was stored by a failed commit", lvl)
		}
	}
}

func TestTx_FinishedTransactionsRejectFurtherUse(t *testing.T) {
	s := NewStore()
	tx := s.Begin(SnapshotIsolation)
	tPut(t, tx, "a", "1")
	tMustCommit(t, tx)
	if err := tx.Commit(); !errors.Is(err, ErrTxDone) {
		t.Errorf("second Commit = %v, want ErrTxDone", err)
	}
	if err := tx.Put("a", "2"); !errors.Is(err, ErrTxDone) {
		t.Errorf("Put after Commit = %v, want ErrTxDone", err)
	}
	if err := tx.Delete("a"); !errors.Is(err, ErrTxDone) {
		t.Errorf("Delete after Commit = %v, want ErrTxDone", err)
	}
	tx.Rollback() // harmless
	rb := s.Begin(SnapshotIsolation)
	rb.Rollback()
	if err := rb.Commit(); !errors.Is(err, ErrTxDone) {
		t.Errorf("Commit after Rollback = %v, want ErrTxDone", err)
	}
	if got := tCommittedInt(t, s, "a"); got != 1 {
		t.Errorf("a = %d, want 1", got)
	}
}

// ---------- write skew ----------

// tJointOverdraft: a member's checking and savings are covered by one rule,
// checking + savings >= 0. Each withdrawal checks the rule against what it
// read, then debits only one of the two accounts.
func tSkewSetup(t *testing.T, lvl Level) (s *Store, w1, w2 *Tx) {
	s = NewStore()
	tSeed(t, s, map[string]string{"checking": "100", "savings": "100"})
	w1, w2 = s.Begin(lvl), s.Begin(lvl)
	for _, w := range []struct {
		tx      *Tx
		account string
	}{{w1, "checking"}, {w2, "savings"}} {
		sum := tInt(t, w.tx, "checking") + tInt(t, w.tx, "savings")
		if sum < 150 {
			t.Fatalf("setup: %d", sum)
		}
		tSetInt(t, w.tx, w.account, tInt(t, w.tx, w.account)-150) // each sees 200 >= 150, so allowed
	}
	return s, w1, w2
}

func TestWriteSkew_IsPossibleUnderSnapshotIsolation(t *testing.T) {
	s, w1, w2 := tSkewSetup(t, SnapshotIsolation)
	if err := w1.Commit(); err != nil {
		t.Fatalf("first: %v", err)
	}
	if err := w2.Commit(); err != nil {
		t.Fatalf("second: %v (snapshot isolation only detects write-write conflicts; these write sets are disjoint, so both must commit)", err)
	}
	sum := tCommittedInt(t, s, "checking") + tCommittedInt(t, s, "savings")
	if sum != -100 {
		t.Fatalf("checking+savings = %d, want -100: each transaction checked the rule on its own snapshot and both withdrew $150 (write skew)", sum)
	}
}

func TestWriteSkew_PreventedBySerializable(t *testing.T) {
	s, w1, w2 := tSkewSetup(t, Serializable)
	if err := w1.Commit(); err != nil {
		t.Fatalf("first: %v", err)
	}
	if err := w2.Commit(); !errors.Is(err, ErrSerialization) {
		t.Fatalf("second: %v, want ErrSerialization (it read checking, which the first transaction changed after its snapshot)", err)
	}
	sum := tCommittedInt(t, s, "checking") + tCommittedInt(t, s, "savings")
	if sum != 50 {
		t.Fatalf("checking+savings = %d, want 50", sum)
	}
	// A retry sees the first withdrawal and correctly refuses.
	retry := s.Begin(Serializable)
	if tInt(t, retry, "checking")+tInt(t, retry, "savings") >= 150 {
		t.Fatal("retry should now see only $50 across both accounts")
	}
}

func TestSerializable_DoesNotAbortTransactionsThatReadNothingThatChanged(t *testing.T) {
	s := NewStore()
	tSeed(t, s, map[string]string{"a": "1", "b": "1", "c": "1"})
	t1, t2 := s.Begin(Serializable), s.Begin(Serializable)
	tSetInt(t, t1, "a", tInt(t, t1, "b")+1) // reads b, writes a
	tSetInt(t, t2, "c", tInt(t, t2, "b")+1) // reads b, writes c
	tMustCommit(t, t1)
	if err := t2.Commit(); err != nil {
		t.Fatalf("t2 only read b, which nobody wrote: %v (a validator that aborts on any concurrent commit is useless)", err)
	}
}

func TestSerializable_ReadOnlyTransactionsNeverFail(t *testing.T) {
	s := NewStore()
	tSeed(t, s, map[string]string{"x": "1"})
	ro := s.Begin(Serializable)
	tExpect(t, ro, "x", "1", true)
	ro.Scan("")
	upd := s.Begin(Serializable)
	tPut(t, upd, "x", "2")
	tPut(t, upd, "y", "new")
	tMustCommit(t, upd)
	if err := ro.Commit(); err != nil {
		t.Fatalf("read-only transaction failed to commit: %v (it read a consistent snapshot; nothing to validate)", err)
	}
}

func TestSerializable_ValidatesReadsThatFoundNothing(t *testing.T) {
	s := NewStore()
	a, b := s.Begin(Serializable), s.Begin(Serializable)
	tExpect(t, a, "lock/payee-7", "", false) // "nobody holds the lock"
	tExpect(t, b, "lock/payee-7", "", false)
	tPut(t, a, "lock/payee-7", "a")
	tPut(t, b, "lock/payee-7", "b")
	tMustCommit(t, a)
	if err := b.Commit(); err == nil {
		t.Fatal("both transactions acquired the same 'lock'")
	}
}

// ---------- phantoms ----------

func TestScan_SnapshotIsolationScansAreRepeatable(t *testing.T) {
	s := NewStore()
	tSeed(t, s, map[string]string{"acct/m1/1": "open", "acct/m1/2": "open", "acct/m2/1": "open"})
	tx := s.Begin(SnapshotIsolation)
	want := []KV{{"acct/m1/1", "open"}, {"acct/m1/2", "open"}}
	if got := tx.Scan("acct/m1/"); !reflect.DeepEqual(got, want) {
		t.Fatalf("scan = %v, want %v (sorted, only the prefix)", got, want)
	}
	ins := s.Begin(SnapshotIsolation)
	tPut(t, ins, "acct/m1/3", "open")
	_ = ins.Delete("acct/m1/1")
	tMustCommit(t, ins)
	if got := tx.Scan("acct/m1/"); !reflect.DeepEqual(got, want) {
		t.Fatalf("second scan = %v, want the same %v: a snapshot has no phantoms", got, want)
	}
	rc := s.Begin(ReadCommitted)
	if got := rc.Scan("acct/m1/"); len(got) != 2 || got[0].Key != "acct/m1/2" || got[1].Key != "acct/m1/3" {
		t.Fatalf("read committed scan = %v, want the latest committed rows", got)
	}
}

func TestScan_IncludesOwnBufferedWritesAndDeletes(t *testing.T) {
	s := NewStore()
	tSeed(t, s, map[string]string{"p/1": "a", "p/2": "b", "q/1": "z"})
	tx := s.Begin(SnapshotIsolation)
	tPut(t, tx, "p/3", "c")
	tPut(t, tx, "p/1", "A")
	_ = tx.Delete("p/2")
	tPut(t, tx, "q/9", "other")
	want := []KV{{"p/1", "A"}, {"p/3", "c"}}
	if got := tx.Scan("p/"); !reflect.DeepEqual(got, want) {
		t.Fatalf("scan = %v, want %v", got, want)
	}
	if got := tx.Scan("none/"); got == nil || len(got) != 0 {
		t.Fatalf("scan of an empty prefix = %#v, want a non-nil empty slice", got)
	}
	if got := tx.Scan(""); len(got) != 4 {
		t.Fatalf("scan of everything = %v, want 4 rows", got)
	}
}

// tPhantomSkew: a member may have at most 2 accounts. Two requests each count
// the member's accounts (1), see room, and insert a different new account.
func tPhantomSkew(t *testing.T, lvl Level) (err2 error, accounts int) {
	s := NewStore()
	tSeed(t, s, map[string]string{"acct/m1/checking": "open"})
	a, b := s.Begin(lvl), s.Begin(lvl)
	for _, x := range []struct {
		tx   *Tx
		name string
	}{{a, "savings"}, {b, "moneymarket"}} {
		if n := len(x.tx.Scan("acct/m1/")); n >= 2 {
			t.Fatalf("setup: %d accounts", n)
		}
		tPut(t, x.tx, "acct/m1/"+x.name, "open")
	}
	tMustCommit(t, a)
	err2 = b.Commit()
	check := s.Begin(SnapshotIsolation)
	return err2, len(check.Scan("acct/m1/"))
}

func TestPhantom_InvariantBreaksUnderSnapshotIsolation(t *testing.T) {
	err2, accounts := tPhantomSkew(t, SnapshotIsolation)
	if err2 != nil {
		t.Fatalf("second insert failed: %v; the two transactions insert different keys, so snapshot isolation lets both commit", err2)
	}
	if accounts != 3 {
		t.Fatalf("member has %d accounts, want 3 (limit of 2 broken by phantom inserts)", accounts)
	}
}

func TestPhantom_PreventedBySerializablePredicateValidation(t *testing.T) {
	err2, accounts := tPhantomSkew(t, Serializable)
	if !errors.Is(err2, ErrSerialization) {
		t.Fatalf("second insert: %v, want ErrSerialization (a key under the scanned prefix appeared after its snapshot)", err2)
	}
	if accounts != 2 {
		t.Fatalf("member has %d accounts, want 2", accounts)
	}
}

func TestPhantom_DeletionsUnderAScannedPrefixAlsoInvalidate(t *testing.T) {
	s := NewStore()
	tSeed(t, s, map[string]string{"q/1": "x", "q/2": "x"})
	reader := s.Begin(Serializable)
	if n := len(reader.Scan("q/")); n != 2 {
		t.Fatalf("scan = %d rows", n)
	}
	del := s.Begin(Serializable)
	_ = del.Delete("q/1")
	tMustCommit(t, del)
	tPut(t, reader, "summary", "2 rows")
	if err := reader.Commit(); !errors.Is(err, ErrSerialization) {
		t.Fatalf("got %v, want ErrSerialization: the summary was computed from rows that no longer exist", err)
	}
}

// ---------- vacuum ----------

func TestVacuum_RemovesVersionsNoSnapshotCanSee(t *testing.T) {
	s := NewStore()
	for i := 1; i <= 5; i++ {
		tSeed(t, s, map[string]string{"x": strconv.Itoa(i)})
	}
	if s.Versions("x") != 5 {
		t.Fatalf("versions = %d, want 5", s.Versions("x"))
	}
	if n := s.Vacuum(); n != 4 {
		t.Errorf("Vacuum removed %d versions, want 4", n)
	}
	if s.Versions("x") != 1 {
		t.Errorf("versions after vacuum = %d, want 1 (the newest)", s.Versions("x"))
	}
	if got := tCommittedInt(t, s, "x"); got != 5 {
		t.Errorf("x = %d after vacuum, want 5", got)
	}
}

func TestVacuum_ALongRunningTransactionHoldsBackTheHorizon(t *testing.T) {
	s := NewStore()
	tSeed(t, s, map[string]string{"x": "1"})
	tSeed(t, s, map[string]string{"x": "2"})
	long := s.Begin(SnapshotIsolation) // sees x=2
	for i := 3; i <= 6; i++ {
		tSeed(t, s, map[string]string{"x": strconv.Itoa(i)})
	}
	if n := s.Vacuum(); n != 1 {
		t.Errorf("Vacuum removed %d versions with a snapshot at x=2 open, want 1 (only x=1)", n)
	}
	if s.Versions("x") != 5 {
		t.Errorf("versions = %d, want 5 (x=2..6 must stay: the old snapshot can still read x=2)", s.Versions("x"))
	}
	tExpect(t, long, "x", "2", true)
	long.Rollback()
	if n := s.Vacuum(); n != 4 {
		t.Errorf("after the long transaction ended, Vacuum removed %d, want 4", n)
	}
	if s.Versions("x") != 1 {
		t.Errorf("versions = %d, want 1", s.Versions("x"))
	}
}

func TestVacuum_ForgetsDeletedKeysAndIgnoresReadCommittedTransactions(t *testing.T) {
	s := NewStore()
	tSeed(t, s, map[string]string{"gone": "1", "kept": "1"})
	del := s.Begin(SnapshotIsolation)
	_ = del.Delete("gone")
	tMustCommit(t, del)
	rc := s.Begin(ReadCommitted) // reads the latest commit, needs no old versions
	if n := s.Vacuum(); n != 2 {
		t.Errorf("Vacuum removed %d versions, want 2 (the value and the deletion marker of 'gone')", n)
	}
	if s.Versions("gone") != 0 {
		t.Errorf("deleted key still has %d versions", s.Versions("gone"))
	}
	tExpect(t, rc, "kept", "1", true)
}

// ---------- concurrency ----------

func tWaitOrFail(t *testing.T, wg *sync.WaitGroup, d time.Duration) {
	t.Helper()
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(d):
		t.Fatal("timed out: transactions are stuck (deadlock?)")
	}
}

func TestConcurrent_CounterIncrementsAreNeverLost(t *testing.T) {
	for _, lvl := range tSnapshotLevels {
		s := NewStore()
		tSeed(t, s, map[string]string{"counter": "0"})
		const workers, each = 8, 150
		var wg sync.WaitGroup
		var failed atomic.Value
		var attempts atomic.Int64
		for w := 0; w < workers; w++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for i := 0; i < each; i++ {
					n, err := s.Run(lvl, 10_000, func(tx *Tx) error {
						v, _ := tx.Get("counter")
						c, _ := strconv.Atoi(v)
						return tx.Put("counter", strconv.Itoa(c+1))
					})
					attempts.Add(int64(n))
					if err != nil {
						failed.Store(err)
						return
					}
				}
			}()
		}
		tWaitOrFail(t, &wg, 30*time.Second)
		if err := failed.Load(); err != nil {
			t.Fatalf("[%v] Run failed: %v", lvl, err)
		}
		if got := tCommittedInt(t, s, "counter"); got != workers*each {
			t.Fatalf("[%v] counter = %d, want %d: increments were lost", lvl, got, workers*each)
		}
		if attempts.Load() <= workers*each {
			t.Logf("[%v] no conflicts were observed (%d attempts); unusual but possible", lvl, attempts.Load())
		}
	}
}

func TestConcurrent_BankTransfersConserveMoneyAndNeverOverdraw(t *testing.T) {
	const accounts, initial, workers, transfers = 6, 1000, 8, 250
	for _, lvl := range tSnapshotLevels {
		s := NewStore()
		seed := map[string]string{}
		for i := 0; i < accounts; i++ {
			seed[fmt.Sprintf("acct/%d", i)] = strconv.Itoa(initial)
		}
		tSeed(t, s, seed)
		errInsufficient := errors.New("insufficient funds")
		var wg sync.WaitGroup
		var bad atomic.Value
		var done, refused atomic.Int64
		for w := 0; w < workers; w++ {
			wg.Add(1)
			go func(w int) {
				defer wg.Done()
				r := rand.New(rand.NewPCG(uint64(w), 77))
				for i := 0; i < transfers; i++ {
					from, to := r.IntN(accounts), r.IntN(accounts)
					if from == to {
						continue
					}
					amt := 1 + r.IntN(400)
					_, err := s.Run(lvl, 10_000, func(tx *Tx) error {
						f, _ := tx.Get(fmt.Sprintf("acct/%d", from))
						g, _ := tx.Get(fmt.Sprintf("acct/%d", to))
						fb, _ := strconv.Atoi(f)
						gb, _ := strconv.Atoi(g)
						if fb < amt {
							return errInsufficient
						}
						_ = tx.Put(fmt.Sprintf("acct/%d", from), strconv.Itoa(fb-amt))
						return tx.Put(fmt.Sprintf("acct/%d", to), strconv.Itoa(gb+amt))
					})
					switch {
					case err == nil:
						done.Add(1)
					case errors.Is(err, errInsufficient):
						refused.Add(1)
					default:
						bad.Store(err)
						return
					}
				}
			}(w)
		}
		tWaitOrFail(t, &wg, 30*time.Second)
		if err := bad.Load(); err != nil {
			t.Fatalf("[%v] %v", lvl, err)
		}
		tx := s.Begin(SnapshotIsolation)
		total := 0
		for _, kv := range tx.Scan("acct/") {
			n, _ := strconv.Atoi(kv.Value)
			if n < 0 {
				t.Errorf("[%v] %s overdrawn: %d", lvl, kv.Key, n)
			}
			total += n
		}
		if total != accounts*initial {
			t.Fatalf("[%v] money in the system = %d, want %d: transfers were lost or duplicated", lvl, total, accounts*initial)
		}
		if done.Load() == 0 {
			t.Fatalf("[%v] no transfer ever succeeded", lvl)
		}
		t.Logf("[%v] %d transfers committed, %d refused for insufficient funds", lvl, done.Load(), refused.Load())
	}
}

func TestConcurrent_SerializableKeepsTheJointOverdraftRuleUnderLoad(t *testing.T) {
	s := NewStore()
	tSeed(t, s, map[string]string{"checking": "500", "savings": "500"})
	var wg sync.WaitGroup
	var bad atomic.Value
	for w := 0; w < 8; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			r := rand.New(rand.NewPCG(uint64(w), 3))
			for i := 0; i < 100; i++ {
				acct := []string{"checking", "savings"}[r.IntN(2)]
				amt := 1 + r.IntN(60)
				_, err := s.Run(Serializable, 10_000, func(tx *Tx) error {
					c, _ := tx.Get("checking")
					v, _ := tx.Get("savings")
					ci, _ := strconv.Atoi(c)
					vi, _ := strconv.Atoi(v)
					if ci+vi-amt < 0 { // the rule: combined balance must stay >= 0
						return nil
					}
					cur := ci
					if acct == "savings" {
						cur = vi
					}
					return tx.Put(acct, strconv.Itoa(cur-amt))
				})
				if err != nil {
					bad.Store(err)
					return
				}
			}
		}(w)
	}
	tWaitOrFail(t, &wg, 30*time.Second)
	if err := bad.Load(); err != nil {
		t.Fatal(err)
	}
	sum := tCommittedInt(t, s, "checking") + tCommittedInt(t, s, "savings")
	if sum < 0 {
		t.Fatalf("checking+savings = %d: the joint overdraft rule was violated under serializable isolation", sum)
	}
}
