package outbox

import (
	"errors"
	"fmt"
	"math/rand"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// ---------- helpers (prefixed with t to avoid clashing with your code) ----------

var tErrCrash = errors.New("simulated crash")

type tClock struct {
	mu sync.Mutex
	t  time.Time
}

func tNewClock() *tClock { return &tClock{t: time.Date(2026, 5, 4, 12, 0, 0, 0, time.UTC)} }
func (c *tClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}
func (c *tClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// tBroker records every delivery. fail, if set, decides whether a Publish
// is rejected.
type tBroker struct {
	mu        sync.Mutex
	delivered []Message
	fail      func(Message) bool
}

func (b *tBroker) Publish(m Message) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.fail != nil && b.fail(m) {
		return errors.New("broker unavailable")
	}
	b.delivered = append(b.delivered, m)
	return nil
}

func (b *tBroker) ids() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]string, len(b.delivered))
	for i, m := range b.delivered {
		out[i] = m.ID
	}
	return out
}

func (b *tBroker) count(id string) int {
	n := 0
	for _, x := range b.ids() {
		if x == id {
			n++
		}
	}
	return n
}

func tEq(a, b []string) bool {
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

func tInt(tx *Tx, key string) int {
	v, _ := tx.Get(key)
	n, _ := strconv.Atoi(v)
	return n
}

// tTransfer moves cents between two balances AND enqueues the event, in one
// transaction. It is idempotent per opID, like an API behind an
// Idempotency-Key: running it twice for the same op changes nothing.
func tTransfer(db *DB, opID, from, to string, cents int) error {
	return db.Do("transfer", func(tx *Tx) error {
		if _, done := tx.Get("op:" + opID); done {
			return nil
		}
		tx.Put("op:"+opID, "done")
		tx.Put("bal:"+from, strconv.Itoa(tInt(tx, "bal:"+from)-cents))
		tx.Put("bal:"+to, strconv.Itoa(tInt(tx, "bal:"+to)+cents))
		tx.Enqueue("transfers", from, fmt.Sprintf("%s|%s|%d", from, to, cents))
		return nil
	})
}

// tCredit is the consumer's effect: credit the destination account.
func tCredit(tx *Tx, m Message) error {
	parts := strings.Split(m.Payload, "|")
	if len(parts) != 3 {
		return fmt.Errorf("bad payload %q", m.Payload)
	}
	cents, _ := strconv.Atoi(parts[2])
	tx.Put("credit:"+parts[1], strconv.Itoa(tInt(tx, "credit:"+parts[1])+cents))
	tx.Put("applied", strconv.Itoa(tInt(tx, "applied")+1))
	return nil
}

func tBal(db *DB, key string) int {
	v, _ := db.Get(key)
	n, _ := strconv.Atoi(v)
	return n
}

// tWait waits for wg, failing the test instead of hanging.
func tWait(t *testing.T, wg *sync.WaitGroup, d time.Duration, what string) {
	t.Helper()
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(d):
		t.Fatalf("%s did not finish within %v (deadlock or livelock in the implementation?)", what, d)
	}
}

func tMustTransfer(t *testing.T, db *DB, op, from, to string, cents int) {
	t.Helper()
	if err := tTransfer(db, op, from, to, cents); err != nil {
		t.Fatalf("transfer %s: %v", op, err)
	}
}

func tRelay(db *DB, b Broker, clk *tClock, id string, mut ...func(*RelayConfig)) *Relay {
	cfg := RelayConfig{ID: id, DB: db, Broker: b, Lease: 30 * time.Second, Batch: 10, Now: clk.Now}
	for _, m := range mut {
		m(&cfg)
	}
	return NewRelay(cfg)
}

// ---------- the transactional store ----------

func TestDB_CommitAppliesStateAndOutboxRowTogether(t *testing.T) {
	db := NewDB(nil)
	tMustTransfer(t, db, "op1", "alice", "bob", 500)
	if got := tBal(db, "bal:alice"); got != -500 {
		t.Fatalf("alice = %d, want -500", got)
	}
	rows := db.Rows()
	if len(rows) != 1 || rows[0].Msg.ID != "msg-1" || rows[0].Seq != 1 || rows[0].Sent || rows[0].Msg.Key != "alice" || rows[0].Msg.Topic != "transfers" {
		t.Fatalf("outbox rows = %+v, want one unsent row msg-1 (topic transfers, key alice) committed with the balance change", rows)
	}
	if db.Pending() != 1 {
		t.Fatalf("Pending = %d, want 1", db.Pending())
	}
}

func TestDB_ErrorRollsBackStateAndOutboxRowAndConsumesNoSequenceNumber(t *testing.T) {
	db := NewDB(nil)
	boom := errors.New("insufficient funds")
	err := db.Do("transfer", func(tx *Tx) error {
		tx.Put("bal:alice", "-999")
		tx.Enqueue("transfers", "alice", "x")
		tx.MarkSeen("m-x")
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("Do returned %v, want the function's error", err)
	}
	if _, ok := db.Get("bal:alice"); ok || len(db.Rows()) != 0 || db.Seen("m-x") {
		t.Fatalf("a failed transaction left state=%v rows=%d seen=%v; rollback must leave no trace, otherwise you publish events for changes that never happened", ok, len(db.Rows()), db.Seen("m-x"))
	}
	tMustTransfer(t, db, "op1", "alice", "bob", 1)
	if rows := db.Rows(); len(rows) != 1 || rows[0].Seq != 1 {
		t.Fatalf("rows after rollback + one commit = %+v, want a single row with Seq 1 (no gaps)", rows)
	}
}

func TestDB_TransactionSeesItsOwnWrites(t *testing.T) {
	db := NewDB(nil)
	err := db.Do("t", func(tx *Tx) error {
		tx.Put("k", "v1")
		if v, ok := tx.Get("k"); !ok || v != "v1" {
			return fmt.Errorf("Get inside the transaction = (%q, %v), want its own write v1", v, ok)
		}
		id := tx.Enqueue("t", "k", "p")
		if id != "msg-1" {
			return fmt.Errorf("Enqueue id = %q, want msg-1", id)
		}
		if id2 := tx.Enqueue("t", "k", "p2"); id2 != "msg-2" {
			return fmt.Errorf("second Enqueue id = %q, want msg-2", id2)
		}
		tx.MarkSeen("m1")
		if !tx.Seen("m1") {
			return errors.New("Seen inside the transaction does not see its own MarkSeen")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestDB_CrashBeforeCommitLeavesNothingBehind(t *testing.T) {
	crashed := false
	db := NewDB(func(point, name string) error {
		if point == PointBeforeCommit && name == "transfer" && !crashed {
			crashed = true
			return tErrCrash
		}
		return nil
	})
	if err := tTransfer(db, "op1", "alice", "bob", 100); !errors.Is(err, tErrCrash) {
		t.Fatalf("Do = %v, want the crash error", err)
	}
	if _, ok := db.Get("bal:alice"); ok || len(db.Rows()) != 0 {
		t.Fatalf("state/outbox survived a crash before commit: balance present=%v rows=%d", ok, len(db.Rows()))
	}
	// The client retries the same operation (same op id) and it now succeeds.
	tMustTransfer(t, db, "op1", "alice", "bob", 100)
	if tBal(db, "bal:alice") != -100 || len(db.Rows()) != 1 {
		t.Fatalf("retry after the pre-commit crash: balance=%d rows=%d, want -100 and 1", tBal(db, "bal:alice"), len(db.Rows()))
	}
}

func TestDB_CrashAfterCommitStillCommitsAndRetryDoesNotDuplicate(t *testing.T) {
	crashed := false
	db := NewDB(func(point, name string) error {
		if point == PointAfterCommit && name == "transfer" && !crashed {
			crashed = true
			return tErrCrash
		}
		return nil
	})
	if err := tTransfer(db, "op1", "alice", "bob", 100); !errors.Is(err, tErrCrash) {
		t.Fatalf("Do = %v, want the crash error: the caller cannot tell whether the commit happened", err)
	}
	if tBal(db, "bal:alice") != -100 || len(db.Rows()) != 1 {
		t.Fatalf("a crash after commit must keep the committed state: balance=%d rows=%d", tBal(db, "bal:alice"), len(db.Rows()))
	}
	tMustTransfer(t, db, "op1", "alice", "bob", 100) // the client retries blindly
	if tBal(db, "bal:alice") != -100 || len(db.Rows()) != 1 {
		t.Fatalf("blind retry duplicated the transfer: balance=%d rows=%d; the operation id must make the transaction idempotent", tBal(db, "bal:alice"), len(db.Rows()))
	}
}

func TestDB_ConcurrentTransactionsAreSerializableNoLostUpdates(t *testing.T) {
	db := NewDB(nil)
	var wg sync.WaitGroup
	for g := 0; g < 20; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 25; i++ {
				if err := db.Do("inc", func(tx *Tx) error {
					tx.Put("n", strconv.Itoa(tInt(tx, "n")+1))
					return nil
				}); err != nil {
					t.Errorf("Do: %v", err)
					return
				}
			}
		}()
	}
	tWait(t, &wg, 10*time.Second, "20 writers")
	if got := tBal(db, "n"); got != 500 {
		t.Fatalf("counter = %d, want 500: read-modify-write transactions must not interleave", got)
	}
}

func TestTx_MarkSentRequiresTheCurrentClaim(t *testing.T) {
	db := NewDB(nil)
	clk := tNewClock()
	tMustTransfer(t, db, "op1", "alice", "bob", 1)

	var aRows, bRows []Row
	db.Do("a-claim", func(tx *Tx) error { aRows = tx.Claim("A", clk.Now(), time.Minute, 5); return nil })
	if len(aRows) != 1 {
		t.Fatalf("A claimed %d rows, want 1", len(aRows))
	}
	db.Do("b-claim", func(tx *Tx) error { bRows = tx.Claim("B", clk.Now(), time.Minute, 5); return nil })
	if len(bRows) != 0 {
		t.Fatalf("B claimed %d rows while A's lease is live, want 0", len(bRows))
	}
	clk.Advance(time.Minute) // A's lease expires (boundary: now is not before LeaseUntil)
	db.Do("b-claim", func(tx *Tx) error { bRows = tx.Claim("B", clk.Now(), time.Minute, 5); return nil })
	if len(bRows) != 1 {
		t.Fatalf("B claimed %d rows after A's lease expired, want 1", len(bRows))
	}
	var aOK, bOK bool
	db.Do("a-mark", func(tx *Tx) error { aOK = tx.MarkSent(aRows[0].Seq, "A"); return nil })
	db.Do("b-mark", func(tx *Tx) error { bOK = tx.MarkSent(bRows[0].Seq, "B"); return nil })
	if aOK || !bOK {
		t.Fatalf("MarkSent: stale relay A = %v, current owner B = %v; want false and true. A relay that lost its lease must not be able to finish the row", aOK, bOK)
	}
	if db.Pending() != 0 {
		t.Fatalf("Pending = %d, want 0", db.Pending())
	}
}

// ---------- the relay ----------

func TestRelay_PublishesInCommitOrderAndMarksRowsSent(t *testing.T) {
	db, clk, b := NewDB(nil), tNewClock(), &tBroker{}
	for i := 1; i <= 5; i++ {
		tMustTransfer(t, db, fmt.Sprint("op", i), "alice", "bob", i)
	}
	r := tRelay(db, b, clk, "r1")
	n, err := r.RunOnce()
	if err != nil || n != 5 {
		t.Fatalf("RunOnce = (%d, %v), want (5, nil)", n, err)
	}
	if got := b.ids(); !tEq(got, []string{"msg-1", "msg-2", "msg-3", "msg-4", "msg-5"}) {
		t.Fatalf("published %v, want msg-1..msg-5 in commit order", got)
	}
	if db.Pending() != 0 {
		t.Fatalf("Pending = %d after a clean run, want 0", db.Pending())
	}
	if n, err := r.RunOnce(); err != nil || n != 0 {
		t.Fatalf("second RunOnce = (%d, %v), want (0, nil): sent rows are never published again", n, err)
	}
	if len(b.ids()) != 5 {
		t.Fatalf("broker got %d deliveries in total, want 5", len(b.ids()))
	}
}

func TestRelay_BatchSizeLimitsEachRun(t *testing.T) {
	db, clk, b := NewDB(nil), tNewClock(), &tBroker{}
	for i := 1; i <= 5; i++ {
		tMustTransfer(t, db, fmt.Sprint("op", i), "alice", "bob", 1)
	}
	r := tRelay(db, b, clk, "r1", func(c *RelayConfig) { c.Batch = 2 })
	var got []int
	for i := 0; i < 3; i++ {
		n, err := r.RunOnce()
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, n)
	}
	if fmt.Sprint(got) != "[2 2 1]" {
		t.Fatalf("published per run = %v, want [2 2 1] with Batch 2", got)
	}
	if !tEq(b.ids(), []string{"msg-1", "msg-2", "msg-3", "msg-4", "msg-5"}) {
		t.Fatalf("order across batches = %v, want msg-1..msg-5", b.ids())
	}
}

func TestRelay_BrokerFailureKeepsRowsPendingAndReleasesTheirClaims(t *testing.T) {
	db, clk := NewDB(nil), tNewClock()
	for i := 1; i <= 3; i++ {
		tMustTransfer(t, db, fmt.Sprint("op", i), "alice", "bob", 1)
	}
	down := true
	b := &tBroker{fail: func(Message) bool { return down }}
	r := tRelay(db, b, clk, "r1")
	if n, err := r.RunOnce(); err == nil || n != 0 {
		t.Fatalf("RunOnce with the broker down = (%d, %v), want (0, error)", n, err)
	}
	if db.Pending() != 3 {
		t.Fatalf("Pending = %d, want 3: a failed publish must not mark anything sent", db.Pending())
	}
	down = false
	// No clock advance: the failed run must have released its claims, so the
	// very next run (same lease window) can publish.
	if n, err := r.RunOnce(); err != nil || n != 3 {
		t.Fatalf("RunOnce after recovery = (%d, %v), want (3, nil); did the failed run release its claims?", n, err)
	}
}

func TestRelay_CrashBetweenCommitAndPublishLosesNothing(t *testing.T) {
	// The process dies right after the business transaction commits: the
	// caller sees an error, and nothing has been published yet. This is the
	// window in which a "write DB, then publish" service silently loses the
	// event.
	db := NewDB(func(point, name string) error {
		if point == PointAfterCommit && name == "transfer" {
			return tErrCrash
		}
		return nil
	})
	clk, b := tNewClock(), &tBroker{}
	if err := tTransfer(db, "op1", "alice", "bob", 700); !errors.Is(err, tErrCrash) {
		t.Fatalf("transfer = %v, want the crash", err)
	}
	if tBal(db, "bal:alice") != -700 {
		t.Fatalf("balance = %d: the state change did commit", tBal(db, "bal:alice"))
	}
	if len(b.ids()) != 0 {
		t.Fatalf("something was published before any relay ran")
	}
	// A relay that starts later (new process, no memory) finds the row.
	r := tRelay(db, b, clk, "r1", func(c *RelayConfig) { c.Hook = nil })
	if n, err := r.RunOnce(); err != nil || n != 1 {
		t.Fatalf("RunOnce = (%d, %v), want (1, nil)", n, err)
	}
	if !tEq(b.ids(), []string{"msg-1"}) {
		t.Fatalf("published %v, want [msg-1]: the event for a committed change must eventually go out", b.ids())
	}
}

func TestRelay_RolledBackTransactionPublishesNothing(t *testing.T) {
	db, clk, b := NewDB(nil), tNewClock(), &tBroker{}
	_ = db.Do("transfer", func(tx *Tx) error {
		tx.Enqueue("transfers", "alice", "ghost")
		return errors.New("insufficient funds")
	})
	r := tRelay(db, b, clk, "r1")
	if n, _ := r.RunOnce(); n != 0 || len(b.ids()) != 0 {
		t.Fatalf("relay published %d messages for a transaction that rolled back; the outbox must make 'event without state change' impossible", n)
	}
}

func TestRelay_CrashAfterClaimLeavesRowsForTakeoverOnlyAfterTheLeaseExpires(t *testing.T) {
	db, clk, b := NewDB(nil), tNewClock(), &tBroker{}
	for i := 1; i <= 3; i++ {
		tMustTransfer(t, db, fmt.Sprint("op", i), "alice", "bob", 1)
	}
	a := tRelay(db, b, clk, "A", func(c *RelayConfig) {
		c.Hook = func(point, name string) error {
			if point == PointRelayAfterClaim {
				return tErrCrash
			}
			return nil
		}
	})
	if _, err := a.RunOnce(); !errors.Is(err, tErrCrash) {
		t.Fatalf("A.RunOnce = %v, want the crash", err)
	}
	other := tRelay(db, b, clk, "B")
	clk.Advance(29 * time.Second)
	if n, err := other.RunOnce(); err != nil || n != 0 {
		t.Fatalf("B.RunOnce before A's lease expired = (%d, %v), want (0, nil): taking over early would double-publish if A were merely slow", n, err)
	}
	clk.Advance(time.Second) // now == LeaseUntil
	if n, err := other.RunOnce(); err != nil || n != 3 {
		t.Fatalf("B.RunOnce after the lease expired = (%d, %v), want (3, nil): a crashed relay must not block the outbox forever", n, err)
	}
	if !tEq(b.ids(), []string{"msg-1", "msg-2", "msg-3"}) {
		t.Fatalf("published %v", b.ids())
	}
}

func TestRelay_CrashAfterPublishBeforeMarkRepublishesTheSameMessageID(t *testing.T) {
	db, clk, b := NewDB(nil), tNewClock(), &tBroker{}
	for i := 1; i <= 3; i++ {
		tMustTransfer(t, db, fmt.Sprint("op", i), "alice", "bob", 1)
	}
	crashed := false
	a := tRelay(db, b, clk, "A", func(c *RelayConfig) {
		c.Hook = func(point, name string) error {
			if point == PointRelayAfterPublish && !crashed {
				crashed = true
				return tErrCrash
			}
			return nil
		}
	})
	if n, err := a.RunOnce(); !errors.Is(err, tErrCrash) || n != 0 {
		t.Fatalf("A.RunOnce = (%d, %v), want (0, crash)", n, err)
	}
	if !tEq(b.ids(), []string{"msg-1"}) || db.Pending() != 3 {
		t.Fatalf("after the crash: deliveries %v, pending %d; want msg-1 delivered but still pending 3", b.ids(), db.Pending())
	}
	clk.Advance(time.Minute)
	bRelay := tRelay(db, b, clk, "B")
	if n, err := bRelay.RunOnce(); err != nil || n != 3 {
		t.Fatalf("B.RunOnce = (%d, %v), want (3, nil)", n, err)
	}
	if b.count("msg-1") != 2 || b.count("msg-2") != 1 || b.count("msg-3") != 1 {
		t.Fatalf("deliveries %v: msg-1 should appear twice (same ID, so consumers can dedupe), the others once. At-least-once means duplicates, never loss", b.ids())
	}
	if db.Pending() != 0 {
		t.Fatalf("Pending = %d, want 0", db.Pending())
	}
}

func TestRelay_RelayThatLostItsLeaseStopsInsteadOfFinishingTheBatch(t *testing.T) {
	db, clk, b := NewDB(nil), tNewClock(), &tBroker{}
	for i := 1; i <= 3; i++ {
		tMustTransfer(t, db, fmt.Sprint("op", i), "alice", "bob", 1)
	}
	takeover := tRelay(db, b, clk, "B")
	var once sync.Once
	slow := tRelay(db, b, clk, "A", func(c *RelayConfig) {
		c.Hook = func(point, name string) error {
			if point == PointRelayAfterPublish {
				once.Do(func() {
					// A stalls (GC pause, network partition) after publishing
					// msg-1. Its lease expires and B publishes everything.
					clk.Advance(time.Minute)
					if n, err := takeover.RunOnce(); err != nil || n != 3 {
						t.Errorf("takeover RunOnce = (%d, %v), want (3, nil)", n, err)
					}
				})
			}
			return nil
		}
	})
	n, err := slow.RunOnce()
	if !errors.Is(err, ErrLeaseLost) || n != 0 {
		t.Fatalf("stale relay RunOnce = (%d, %v), want (0, ErrLeaseLost)", n, err)
	}
	if b.count("msg-1") != 2 || b.count("msg-2") != 1 || b.count("msg-3") != 1 {
		t.Fatalf("deliveries %v: the stale relay must publish nothing after it notices it lost the lease (msg-1 twice, rest once)", b.ids())
	}
}

func TestRelay_DoesNotJumpAheadOfAnEarlierRowForTheSameKey(t *testing.T) {
	db, clk, b := NewDB(nil), tNewClock(), &tBroker{}
	tMustTransfer(t, db, "op1", "alice", "bob", 1)  // msg-1, key alice
	tMustTransfer(t, db, "op2", "carol", "bob", 1)  // msg-2, key carol
	tMustTransfer(t, db, "op3", "alice", "dave", 1) // msg-3, key alice
	// Relay A claims msg-1 (and the rest) and then dies.
	a := tRelay(db, b, clk, "A", func(c *RelayConfig) {
		c.Batch = 1
		c.Hook = func(point, name string) error {
			if point == PointRelayAfterClaim {
				return tErrCrash
			}
			return nil
		}
	})
	if _, err := a.RunOnce(); !errors.Is(err, tErrCrash) {
		t.Fatalf("A.RunOnce = %v", err)
	}
	bRelay := tRelay(db, b, clk, "B")
	if n, err := bRelay.RunOnce(); err != nil || n != 1 {
		t.Fatalf("B.RunOnce = (%d, %v), want (1, nil): only carol's msg-2 is claimable; msg-3 (alice) must wait behind A's live claim on msg-1 or alice's events would be published out of order", n, err)
	}
	if !tEq(b.ids(), []string{"msg-2"}) {
		t.Fatalf("published %v, want only msg-2", b.ids())
	}
	clk.Advance(time.Minute)
	if n, err := bRelay.RunOnce(); err != nil || n != 2 {
		t.Fatalf("B.RunOnce after takeover = (%d, %v), want (2, nil)", n, err)
	}
	if !tEq(b.ids(), []string{"msg-2", "msg-1", "msg-3"}) {
		t.Fatalf("published %v, want msg-2 then alice's msg-1, msg-3 in order", b.ids())
	}
}

func TestRelay_ConcurrentRelaysPublishEachRowExactlyOnceAndKeepPerKeyOrder(t *testing.T) {
	const rows, relays = 200, 4
	db, clk, b := NewDB(nil), tNewClock(), &tBroker{}
	for i := 0; i < rows; i++ {
		key := fmt.Sprintf("acct-%d", i%10)
		if err := db.Do("t", func(tx *Tx) error {
			tx.Enqueue("transfers", key, strconv.Itoa(i))
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	var wg sync.WaitGroup
	var published atomic.Int64
	deadline := time.Now().Add(15 * time.Second)
	for r := 0; r < relays; r++ {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			rel := tRelay(db, b, clk, id, func(c *RelayConfig) { c.Batch = 7 })
			for db.Pending() > 0 && time.Now().Before(deadline) {
				n, err := rel.RunOnce()
				if err != nil {
					t.Errorf("%s RunOnce: %v", id, err)
					return
				}
				published.Add(int64(n))
				if n == 0 {
					time.Sleep(50 * time.Microsecond) // another relay holds the rest; yield
				}
			}
		}(fmt.Sprintf("relay-%d", r))
	}
	tWait(t, &wg, 20*time.Second, "relays")
	if db.Pending() != 0 {
		t.Fatalf("Pending = %d, relays did not drain the outbox", db.Pending())
	}
	seen := map[string]int{}
	lastPerKey := map[string]int{}
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, m := range b.delivered {
		seen[m.ID]++
		n, _ := strconv.Atoi(m.Payload)
		if prev, ok := lastPerKey[m.Key]; ok && n <= prev {
			t.Fatalf("key %s: message %d published after %d: concurrent relays reordered one key's events", m.Key, n, prev)
		}
		lastPerKey[m.Key] = n
	}
	if len(seen) != rows {
		t.Fatalf("%d distinct messages published, want %d", len(seen), rows)
	}
	for id, c := range seen {
		if c != 1 {
			t.Fatalf("%s published %d times by concurrent relays that never crashed; the claim must make each row go to exactly one relay", id, c)
		}
	}
}

// ---------- the idempotent consumer ----------

func TestConsumer_DuplicateDeliveryAppliesTheEffectOnce(t *testing.T) {
	db := NewDB(nil)
	c := NewConsumer(db, tCredit)
	m := Message{ID: "msg-7", Topic: "transfers", Key: "alice", Payload: "alice|bob|500"}
	applied, err := c.Handle(m)
	if err != nil || !applied {
		t.Fatalf("first Handle = (%v, %v), want (true, nil)", applied, err)
	}
	for i := 0; i < 3; i++ {
		applied, err = c.Handle(m)
		if err != nil || applied {
			t.Fatalf("duplicate Handle = (%v, %v), want (false, nil)", applied, err)
		}
	}
	if got := tBal(db, "credit:bob"); got != 500 {
		t.Fatalf("bob credited %d, want 500: a redelivered message credited the account again", got)
	}
	if !db.Seen("msg-7") {
		t.Fatalf("message ID not recorded in the dedupe table")
	}
}

func TestConsumer_HandlerErrorRollsBackSoTheRetryCanSucceed(t *testing.T) {
	db := NewDB(nil)
	fail := true
	c := NewConsumer(db, func(tx *Tx, m Message) error {
		if err := tCredit(tx, m); err != nil {
			return err
		}
		if fail {
			return errors.New("downstream unavailable")
		}
		return nil
	})
	m := Message{ID: "msg-1", Payload: "alice|bob|100"}
	if applied, err := c.Handle(m); err == nil || applied {
		t.Fatalf("Handle = (%v, %v), want (false, error)", applied, err)
	}
	if db.Seen("msg-1") || tBal(db, "credit:bob") != 0 {
		t.Fatalf("a failed handler left effect=%d seen=%v; a half-applied message that is also marked processed is lost forever", tBal(db, "credit:bob"), db.Seen("msg-1"))
	}
	fail = false
	if applied, err := c.Handle(m); err != nil || !applied {
		t.Fatalf("retry = (%v, %v), want (true, nil)", applied, err)
	}
	if tBal(db, "credit:bob") != 100 {
		t.Fatalf("credit = %d, want 100", tBal(db, "credit:bob"))
	}
}

func TestConsumer_CrashBeforeCommitThenRedeliveryAppliesOnce(t *testing.T) {
	crashed := false
	db := NewDB(func(point, name string) error {
		if point == PointBeforeCommit && name == "consume" && !crashed {
			crashed = true
			return tErrCrash
		}
		return nil
	})
	c := NewConsumer(db, tCredit)
	m := Message{ID: "msg-1", Payload: "alice|bob|250"}
	if _, err := c.Handle(m); !errors.Is(err, tErrCrash) {
		t.Fatalf("Handle = %v, want the crash", err)
	}
	if tBal(db, "credit:bob") != 0 || db.Seen("msg-1") {
		t.Fatalf("crash before commit left credit=%d seen=%v; effect and dedupe record must commit together or not at all", tBal(db, "credit:bob"), db.Seen("msg-1"))
	}
	if applied, err := c.Handle(m); err != nil || !applied {
		t.Fatalf("redelivery = (%v, %v), want (true, nil)", applied, err)
	}
	if tBal(db, "credit:bob") != 250 {
		t.Fatalf("credit = %d, want 250", tBal(db, "credit:bob"))
	}
}

func TestConsumer_CrashAfterCommitThenRedeliveryIsSkipped(t *testing.T) {
	crashed := false
	db := NewDB(func(point, name string) error {
		if point == PointAfterCommit && name == "consume" && !crashed {
			crashed = true
			return tErrCrash
		}
		return nil
	})
	c := NewConsumer(db, tCredit)
	m := Message{ID: "msg-1", Payload: "alice|bob|250"}
	if _, err := c.Handle(m); !errors.Is(err, tErrCrash) {
		t.Fatalf("Handle = %v, want the crash (the ack to the broker was never sent)", err)
	}
	// The broker redelivers because it never got an ack.
	if applied, err := c.Handle(m); err != nil || applied {
		t.Fatalf("redelivery = (%v, %v), want (false, nil): the first attempt had committed", applied, err)
	}
	if tBal(db, "credit:bob") != 250 {
		t.Fatalf("credit = %d, want 250 exactly once", tBal(db, "credit:bob"))
	}
}

func TestConsumer_ConcurrentDuplicatesApplyOnce(t *testing.T) {
	db := NewDB(nil)
	c := NewConsumer(db, tCredit)
	m := Message{ID: "msg-1", Payload: "alice|bob|100"}
	var applied atomic.Int64
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			ok, err := c.Handle(m)
			if err != nil {
				t.Errorf("Handle: %v", err)
				return
			}
			if ok {
				applied.Add(1)
			}
		}()
	}
	close(start)
	tWait(t, &wg, 10*time.Second, "50 duplicate deliveries")
	if applied.Load() != 1 || tBal(db, "credit:bob") != 100 {
		t.Fatalf("applied %d times, credit %d; want exactly once and 100. Two consumer instances can receive the same message at the same time", applied.Load(), tBal(db, "credit:bob"))
	}
}

// ---------- everything together ----------

// tChaos runs a whole pipeline (transfers -> outbox -> relay -> broker ->
// consumer) in one goroutine with a seeded random crash schedule, random
// broker failures and random duplicate deliveries, and checks the only thing
// that matters: no transfer is lost and none is applied twice.
func tChaos(t *testing.T, seed int64) {
	const transfers, cents = 40, 25
	rng := rand.New(rand.NewSource(seed))
	crashes := 0
	hook := func(point, name string) error {
		if rng.Float64() < 0.12 {
			crashes++
			return tErrCrash
		}
		return nil
	}
	clk := tNewClock()
	prod, cons := NewDB(hook), NewDB(hook)
	var queue []Message // the broker's undelivered backlog
	dups := 0
	broker := &tBroker{fail: func(Message) bool { return rng.Float64() < 0.1 }}
	rel := tRelay(prod, broker, clk, "relay", func(c *RelayConfig) { c.Hook = hook; c.Batch = 3; c.Lease = 10 * time.Second })
	consumer := NewConsumer(cons, tCredit)

	next, delivered := 0, 0
	for iter := 0; iter < 20000; iter++ {
		clk.Advance(time.Second)
		if next < transfers {
			if err := tTransfer(prod, fmt.Sprint("op", next), "alice", "bob", cents); err == nil {
				next++
			}
		}
		_, _ = rel.RunOnce()
		// Whatever the relay managed to publish reaches the consumer queue; the
		// broker sometimes delivers a message twice.
		broker.mu.Lock()
		for ; delivered < len(broker.delivered); delivered++ {
			m := broker.delivered[delivered]
			queue = append(queue, m)
			if rng.Float64() < 0.2 {
				queue = append(queue, m)
				dups++
			}
		}
		broker.mu.Unlock()
		if len(queue) > 0 {
			m := queue[0]
			if _, err := consumer.Handle(m); err == nil {
				queue = queue[1:] // acked
			} // else: no ack, the broker redelivers later
		}
		if next == transfers && prod.Pending() == 0 && len(queue) == 0 {
			break
		}
	}
	if next != transfers || prod.Pending() != 0 || len(queue) != 0 {
		t.Fatalf("seed %d did not converge: produced %d/%d, outbox pending %d, consumer backlog %d", seed, next, transfers, prod.Pending(), len(queue))
	}
	if crashes == 0 {
		t.Fatalf("seed %d: the chaos schedule never crashed anything; the test is not testing", seed)
	}
	if got := tBal(prod, "bal:alice"); got != -transfers*cents {
		t.Fatalf("seed %d: source balance %d, want %d: the producer side double-applied or lost a transfer", seed, got, -transfers*cents)
	}
	if got := tBal(cons, "credit:bob"); got != transfers*cents {
		t.Fatalf("seed %d: destination credited %d, want %d (applied %d times, %d crashes, %d broker duplicates): a transfer was lost or applied twice", seed, got, transfers*cents, tBal(cons, "applied"), crashes, dups)
	}
	if got := tBal(cons, "applied"); got != transfers {
		t.Fatalf("seed %d: effect ran %d times for %d transfers", seed, got, transfers)
	}
}

func TestPipeline_RandomCrashesDuplicatesAndBrokerFailuresNeverLoseOrDoubleApply(t *testing.T) {
	for seed := int64(1); seed <= 12; seed++ {
		seed := seed
		t.Run(fmt.Sprint("seed", seed), func(t *testing.T) { tChaos(t, seed) })
	}
}
