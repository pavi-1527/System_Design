package ledger

import (
	"errors"
	"fmt"
	"math/rand"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// ---------- helpers (prefixed with t to avoid clashing with your code) ----------

type tClock struct {
	mu sync.Mutex
	t  time.Time
}

func tNewClock() *tClock { return &tClock{t: time.Date(2026, 6, 1, 9, 0, 0, 0, time.UTC)} }
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

// tLedger opens a settlement account (may go negative) and member accounts
// (may not), and funds each member with `fund` cents from settlement.
func tLedger(t *testing.T, clk *tClock, fund int64, members ...string) *Ledger {
	t.Helper()
	var l *Ledger
	if clk != nil {
		l = New(WithClock(clk.Now))
	} else {
		l = New()
	}
	if err := l.OpenAccount("settlement", "USD", true); err != nil {
		t.Fatalf("open settlement: %v", err)
	}
	for _, m := range members {
		if err := l.OpenAccount(m, "USD", false); err != nil {
			t.Fatalf("open %s: %v", m, err)
		}
		if fund > 0 {
			if _, _, err := l.Post(Request{Key: "fund-" + m, Entries: []Entry{
				{Account: "settlement", Side: Debit, Amount: fund},
				{Account: m, Side: Credit, Amount: fund},
			}}); err != nil {
				t.Fatalf("fund %s: %v", m, err)
			}
		}
	}
	return l
}

func tBalance(t *testing.T, l *Ledger, acct string) int64 {
	t.Helper()
	b, err := l.Balance(acct)
	if err != nil {
		t.Fatalf("Balance(%s): %v", acct, err)
	}
	return b
}

func tAvailable(t *testing.T, l *Ledger, acct string) int64 {
	t.Helper()
	b, err := l.Available(acct)
	if err != nil {
		t.Fatalf("Available(%s): %v", acct, err)
	}
	return b
}

func tTransfer(from, to string, amount int64) []Entry {
	return []Entry{{Account: from, Side: Debit, Amount: amount}, {Account: to, Side: Credit, Amount: amount}}
}

// tWait fails the test instead of hanging if wg takes longer than d.
func tWait(t *testing.T, wg *sync.WaitGroup, d time.Duration, what string) {
	t.Helper()
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(d):
		t.Fatalf("%s did not finish within %v (deadlock in the implementation?)", what, d)
	}
}

// ---------- accounts and posting ----------

func TestOpenAccount_RejectsDuplicatesAndBlanks(t *testing.T) {
	l := New()
	if err := l.OpenAccount("", "USD", false); !errors.Is(err, ErrInvalid) {
		t.Fatalf("empty id: %v, want ErrInvalid", err)
	}
	if err := l.OpenAccount("m1", "", false); !errors.Is(err, ErrInvalid) {
		t.Fatalf("empty currency: %v, want ErrInvalid", err)
	}
	if err := l.OpenAccount("m1", "USD", false); err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := l.OpenAccount("m1", "USD", false); !errors.Is(err, ErrAccountExists) {
		t.Fatalf("duplicate: %v, want ErrAccountExists", err)
	}
	if _, err := l.Balance("nope"); !errors.Is(err, ErrUnknownAccount) {
		t.Fatalf("Balance(unknown): %v, want ErrUnknownAccount", err)
	}
	if b := tBalance(t, l, "m1"); b != 0 {
		t.Fatalf("new account balance = %d, want 0", b)
	}
}

func TestPost_BalancedTransferMovesMoneyAndAppendsJournal(t *testing.T) {
	clk := tNewClock()
	l := tLedger(t, clk, 10_000, "alice", "bob")
	p, replayed, err := l.Post(Request{Key: "rent-1", Entries: tTransfer("alice", "bob", 2_500), ExternalRef: "ach-001"})
	if err != nil || replayed {
		t.Fatalf("Post = (%+v, replayed=%v, %v), want a fresh posting", p, replayed, err)
	}
	if p.ID == "" || p.Seq == 0 || !p.At.Equal(clk.Now()) || p.Amount() != 2_500 || p.ExternalRef != "ach-001" {
		t.Fatalf("posting %+v: want an ID, a Seq, At from the injected clock, Amount 2500 and the external ref", p)
	}
	if a, b := tBalance(t, l, "alice"), tBalance(t, l, "bob"); a != 7_500 || b != 12_500 {
		t.Fatalf("balances alice=%d bob=%d, want 7500 and 12500", a, b)
	}
	if s := tBalance(t, l, "settlement"); s != -20_000 {
		t.Fatalf("settlement = %d, want -20000 (it funded two members with 10000 each)", s)
	}
	hist, err := l.History("alice")
	if err != nil || len(hist) != 2 || hist[0].Key != "fund-alice" || hist[1].ID != p.ID {
		t.Fatalf("History(alice) = %+v (%v), want [fund-alice, %s] in order", hist, err, p.ID)
	}
	if hist[1].Seq <= hist[0].Seq {
		t.Fatalf("Seq must strictly increase along the journal: %d then %d", hist[0].Seq, hist[1].Seq)
	}
	got, err := l.Get(p.ID)
	if err != nil || got.ID != p.ID || len(got.Entries) != 2 {
		t.Fatalf("Get(%s) = %+v, %v", p.ID, got, err)
	}
	got.Entries[0].Amount = 1 // must not reach the ledger
	if again, _ := l.Get(p.ID); again.Entries[0].Amount != 2_500 {
		t.Fatalf("Get returned memory shared with the journal; mutation leaked into the ledger")
	}
}

func TestPost_RejectsInvalidRequestsWithoutPartialEffects(t *testing.T) {
	l := tLedger(t, nil, 1_000, "alice", "bob")
	if err := l.OpenAccount("euro", "EUR", false); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		req  Request
		want error
	}{
		{"no key", Request{Entries: tTransfer("alice", "bob", 10)}, ErrInvalid},
		{"one entry", Request{Key: "k1", Entries: []Entry{{Account: "alice", Side: Debit, Amount: 10}}}, ErrInvalid},
		{"zero amount", Request{Key: "k2", Entries: tTransfer("alice", "bob", 0)}, ErrInvalid},
		{"negative amount", Request{Key: "k3", Entries: tTransfer("alice", "bob", -5)}, ErrInvalid},
		{"bad side", Request{Key: "k4", Entries: []Entry{{Account: "alice", Amount: 10}, {Account: "bob", Side: Credit, Amount: 10}}}, ErrInvalid},
		{"unknown account", Request{Key: "k5", Entries: tTransfer("alice", "carol", 10)}, ErrUnknownAccount},
		{"currency mismatch", Request{Key: "k6", Entries: tTransfer("alice", "euro", 10)}, ErrCurrencyMismatch},
		{"unbalanced", Request{Key: "k7", Entries: []Entry{{Account: "alice", Side: Debit, Amount: 10}, {Account: "bob", Side: Credit, Amount: 9}}}, ErrUnbalanced},
		{"overdraft", Request{Key: "k8", Entries: tTransfer("alice", "bob", 1_001)}, ErrInsufficientFunds},
	}
	for _, c := range cases {
		if _, _, err := l.Post(c.req); !errors.Is(err, c.want) {
			t.Errorf("%s: err = %v, want %v", c.name, err, c.want)
		}
	}
	if a, b := tBalance(t, l, "alice"), tBalance(t, l, "bob"); a != 1_000 || b != 1_000 {
		t.Fatalf("balances changed after rejected postings: alice=%d bob=%d; a rejected posting must have no effect", a, b)
	}
	// A rejected key must not be consumed: the corrected request may reuse it.
	if _, _, err := l.Post(Request{Key: "k8", Entries: tTransfer("alice", "bob", 1_000)}); err != nil {
		t.Fatalf("reusing the key of a rejected request: %v, want success (nothing was recorded for it)", err)
	}
	if err := l.Audit(); err != nil {
		t.Fatalf("Audit: %v", err)
	}
}

func TestPost_MultiLegPostingAndDuplicateExternalRef(t *testing.T) {
	l := tLedger(t, nil, 10_000, "alice", "bob", "fees")
	// One debit split into two credits: a payment with a fee.
	p, _, err := l.Post(Request{Key: "pay-1", ExternalRef: "card-9", Entries: []Entry{
		{Account: "alice", Side: Debit, Amount: 1_030},
		{Account: "bob", Side: Credit, Amount: 1_000},
		{Account: "fees", Side: Credit, Amount: 30},
	}})
	if err != nil || p.Amount() != 1_030 {
		t.Fatalf("multi-leg Post = %+v, %v", p, err)
	}
	if a, b, f := tBalance(t, l, "alice"), tBalance(t, l, "bob"), tBalance(t, l, "fees"); a != 8_970 || b != 11_000 || f != 10_030 {
		t.Fatalf("balances alice=%d bob=%d fees=%d", a, b, f)
	}
	if _, _, err := l.Post(Request{Key: "pay-2", ExternalRef: "card-9", Entries: tTransfer("alice", "bob", 1)}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("second posting with ExternalRef card-9: %v, want ErrInvalid (refs are unique so Reconcile can match them)", err)
	}
}

func TestPost_NegativeBalanceOnlyWhereAllowed(t *testing.T) {
	l := tLedger(t, nil, 0, "alice")
	// settlement may go negative: that is how money enters the closed system.
	if _, _, err := l.Post(Request{Key: "deposit", Entries: tTransfer("settlement", "alice", 500)}); err != nil {
		t.Fatalf("deposit from settlement: %v", err)
	}
	if _, _, err := l.Post(Request{Key: "overdraw", Entries: tTransfer("alice", "settlement", 501)}); !errors.Is(err, ErrInsufficientFunds) {
		t.Fatalf("member overdraft: %v, want ErrInsufficientFunds", err)
	}
	if _, _, err := l.Post(Request{Key: "exact", Entries: tTransfer("alice", "settlement", 500)}); err != nil {
		t.Fatalf("withdrawing exactly the balance: %v, want success (available may reach zero, not below)", err)
	}
	if b := tBalance(t, l, "alice"); b != 0 {
		t.Fatalf("alice = %d, want 0", b)
	}
}

// ---------- idempotency ----------

func TestPost_SameKeySameRequestReplaysWithoutPostingAgain(t *testing.T) {
	l := tLedger(t, nil, 1_000, "alice", "bob")
	req := Request{Key: "tap-42", Entries: tTransfer("alice", "bob", 300), ExternalRef: "ref-42"}
	first, replayed, err := l.Post(req)
	if err != nil || replayed {
		t.Fatalf("first Post: %+v %v %v", first, replayed, err)
	}
	for i := 0; i < 3; i++ {
		again, replayed, err := l.Post(req)
		if err != nil || !replayed || again.ID != first.ID || again.Seq != first.Seq {
			t.Fatalf("retry %d: (%+v, replayed=%v, %v), want the original posting %s with replayed=true", i, again, replayed, err, first.ID)
		}
	}
	if a := tBalance(t, l, "alice"); a != 700 {
		t.Fatalf("alice = %d after 1 posting + 3 replays, want 700: the member would be debited %d times", a, (1_000-a)/300)
	}
	if hist, _ := l.History("alice"); len(hist) != 2 {
		t.Fatalf("History(alice) has %d postings, want 2 (fund + one transfer)", len(hist))
	}
}

func TestPost_SameKeyDifferentRequestIsRejected(t *testing.T) {
	l := tLedger(t, nil, 1_000, "alice", "bob")
	if _, _, err := l.Post(Request{Key: "k", Entries: tTransfer("alice", "bob", 300)}); err != nil {
		t.Fatal(err)
	}
	cases := map[string]Request{
		"different amount":  {Key: "k", Entries: tTransfer("alice", "bob", 301)},
		"different account": {Key: "k", Entries: tTransfer("bob", "alice", 300)},
		"different ref":     {Key: "k", Entries: tTransfer("alice", "bob", 300), ExternalRef: "x"},
	}
	for name, req := range cases {
		if _, _, err := l.Post(req); !errors.Is(err, ErrKeyReused) {
			t.Errorf("%s: err = %v, want ErrKeyReused (never replay a posting for a request it does not belong to)", name, err)
		}
	}
	if a := tBalance(t, l, "alice"); a != 700 {
		t.Fatalf("alice = %d, want 700", a)
	}
}

func TestPost_ConcurrentDuplicatesExecuteOnce(t *testing.T) {
	l := tLedger(t, nil, 10_000, "alice", "bob")
	const n = 100
	req := Request{Key: "double-tap", Entries: tTransfer("alice", "bob", 250)}
	var fresh, replays, failures atomic.Int64
	ids := make(chan string, n)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			p, replayed, err := l.Post(req)
			switch {
			case err != nil:
				failures.Add(1)
			case replayed:
				replays.Add(1)
				ids <- p.ID
			default:
				fresh.Add(1)
				ids <- p.ID
			}
		}()
	}
	close(start)
	tWait(t, &wg, 10*time.Second, "100 concurrent duplicate Posts")
	close(ids)
	if fresh.Load() != 1 || failures.Load() != 0 || replays.Load() != n-1 {
		t.Fatalf("fresh=%d replays=%d failures=%d; want exactly 1 execution, %d replays and no errors", fresh.Load(), replays.Load(), failures.Load(), n-1)
	}
	first := ""
	for id := range ids {
		if first == "" {
			first = id
		} else if id != first {
			t.Fatalf("duplicates returned different postings %s and %s", first, id)
		}
	}
	if a := tBalance(t, l, "alice"); a != 9_750 {
		t.Fatalf("alice = %d, want 9750: a double tap must debit once", a)
	}
}

func TestPost_ConcurrentTransfersConserveMoneyAndNeverOverdraw(t *testing.T) {
	members := []string{"m0", "m1", "m2", "m3", "m4", "m5", "m6", "m7"}
	l := tLedger(t, nil, 1_000, members...)
	const workers, perWorker = 16, 300
	var wg sync.WaitGroup
	var rejected atomic.Int64
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			rng := rand.New(rand.NewSource(int64(w)))
			for i := 0; i < perWorker; i++ {
				from := members[rng.Intn(len(members))]
				to := members[rng.Intn(len(members))]
				if from == to {
					continue
				}
				_, _, err := l.Post(Request{Key: fmt.Sprintf("w%d-%d", w, i), Entries: tTransfer(from, to, int64(1+rng.Intn(400)))})
				if errors.Is(err, ErrInsufficientFunds) {
					rejected.Add(1)
				} else if err != nil {
					t.Errorf("unexpected error: %v", err)
				}
			}
		}(w)
	}
	tWait(t, &wg, 30*time.Second, "concurrent transfers")
	var total int64
	for _, m := range members {
		b := tBalance(t, l, m)
		if b < 0 {
			t.Fatalf("%s = %d: a member account went negative under concurrency", m, b)
		}
		total += b
	}
	if total != 8_000 {
		t.Fatalf("members hold %d in total, want 8000: money was created or destroyed", total)
	}
	if err := l.Audit(); err != nil {
		t.Fatalf("Audit after the workload: %v", err)
	}
	t.Logf("%d transfers rejected for insufficient funds (expected to be > 0)", rejected.Load())
}

// ---------- holds ----------

func TestHold_ReducesAvailableButNotBalance(t *testing.T) {
	clk := tNewClock()
	l := tLedger(t, clk, 1_000, "alice", "merchant")
	h, err := l.Hold("auth-1", "alice", 600, time.Hour)
	if err != nil || h.ID == "" || h.State != HoldActive || !h.ExpiresAt.Equal(clk.Now().Add(time.Hour)) {
		t.Fatalf("Hold = %+v, %v", h, err)
	}
	if b, av := tBalance(t, l, "alice"), tAvailable(t, l, "alice"); b != 1_000 || av != 400 {
		t.Fatalf("balance=%d available=%d, want 1000 and 400: a hold reserves without moving", b, av)
	}
	if _, _, err := l.Post(Request{Key: "spend", Entries: tTransfer("alice", "merchant", 401)}); !errors.Is(err, ErrInsufficientFunds) {
		t.Fatalf("debit beyond available: %v, want ErrInsufficientFunds (holds count)", err)
	}
	if _, err := l.Hold("auth-2", "alice", 401, time.Hour); !errors.Is(err, ErrInsufficientFunds) {
		t.Fatalf("second hold beyond available: %v, want ErrInsufficientFunds", err)
	}
	again, err := l.Hold("auth-1", "alice", 600, time.Hour)
	if err != nil || again.ID != h.ID {
		t.Fatalf("replayed Hold = %+v, %v; want the same hold %s", again, err, h.ID)
	}
	if _, err := l.Hold("auth-1", "alice", 601, time.Hour); !errors.Is(err, ErrKeyReused) {
		t.Fatalf("hold key reused with a different amount: %v, want ErrKeyReused", err)
	}
	if _, err := l.Hold("auth-3", "nobody", 1, time.Hour); !errors.Is(err, ErrUnknownAccount) {
		t.Fatalf("hold on unknown account: %v", err)
	}
	if _, err := l.Hold("", "alice", 1, time.Hour); !errors.Is(err, ErrInvalid) {
		t.Fatalf("hold without key: %v, want ErrInvalid", err)
	}
}

func TestHold_PartialCaptureReleasesRemainderAndIsIdempotent(t *testing.T) {
	l := tLedger(t, tNewClock(), 1_000, "alice", "merchant")
	h, _ := l.Hold("auth-1", "alice", 600, time.Hour)
	if _, err := l.Capture("cap-1", h.ID, 601, "merchant"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("capture above the held amount: %v, want ErrInvalid", err)
	}
	p, err := l.Capture("cap-1", h.ID, 450, "merchant")
	if err != nil || p.Amount() != 450 {
		t.Fatalf("Capture = %+v, %v", p, err)
	}
	if b, av, m := tBalance(t, l, "alice"), tAvailable(t, l, "alice"), tBalance(t, l, "merchant"); b != 550 || av != 550 || m != 1_450 {
		t.Fatalf("alice balance=%d available=%d merchant=%d; want 550, 550 (remainder released) and 1450", b, av, m)
	}
	got, _ := l.GetHold(h.ID)
	if got.State != HoldCaptured || got.PostingID != p.ID {
		t.Fatalf("hold after capture = %+v, want captured with posting %s", got, p.ID)
	}
	again, err := l.Capture("cap-1", h.ID, 450, "merchant")
	if err != nil || again.ID != p.ID {
		t.Fatalf("replayed Capture = %+v, %v; want posting %s again, no second debit", again, err, p.ID)
	}
	if _, err := l.Capture("cap-2", h.ID, 100, "merchant"); !errors.Is(err, ErrHoldClosed) {
		t.Fatalf("second capture with a new key: %v, want ErrHoldClosed", err)
	}
	if err := l.Void(h.ID); !errors.Is(err, ErrHoldClosed) {
		t.Fatalf("void after capture: %v, want ErrHoldClosed", err)
	}
	if m := tBalance(t, l, "merchant"); m != 1_450 {
		t.Fatalf("merchant = %d after replay attempts, want 1450", m)
	}
	if err := l.Audit(); err != nil {
		t.Fatal(err)
	}
}

func TestHold_ExpiredHoldFreesFundsAndCannotBeCaptured(t *testing.T) {
	clk := tNewClock()
	l := tLedger(t, clk, 1_000, "alice", "merchant")
	h, _ := l.Hold("auth-1", "alice", 900, 7*24*time.Hour)
	clk.Advance(7*24*time.Hour - time.Second)
	if av := tAvailable(t, l, "alice"); av != 100 {
		t.Fatalf("available = %d just before expiry, want 100", av)
	}
	clk.Advance(2 * time.Second)
	if av := tAvailable(t, l, "alice"); av != 1_000 {
		t.Fatalf("available = %d after expiry, want 1000: an expired hold no longer reserves funds", av)
	}
	if _, err := l.Capture("cap-1", h.ID, 900, "merchant"); !errors.Is(err, ErrHoldExpired) {
		t.Fatalf("capture after expiry: %v, want ErrHoldExpired", err)
	}
	if _, _, err := l.Post(Request{Key: "spend", Entries: tTransfer("alice", "merchant", 1_000)}); err != nil {
		t.Fatalf("spending the freed funds: %v", err)
	}
}

func TestHold_VoidReleasesAndUnknownHoldErrors(t *testing.T) {
	l := tLedger(t, tNewClock(), 1_000, "alice", "merchant")
	h, _ := l.Hold("auth-1", "alice", 600, time.Hour)
	if err := l.Void(h.ID); err != nil {
		t.Fatalf("Void: %v", err)
	}
	if av := tAvailable(t, l, "alice"); av != 1_000 {
		t.Fatalf("available after void = %d, want 1000", av)
	}
	if err := l.Void(h.ID); !errors.Is(err, ErrHoldClosed) {
		t.Fatalf("second Void: %v, want ErrHoldClosed", err)
	}
	if _, err := l.Capture("cap", h.ID, 1, "merchant"); !errors.Is(err, ErrHoldClosed) {
		t.Fatalf("capture after void: %v, want ErrHoldClosed", err)
	}
	if err := l.Void("hold_nope"); !errors.Is(err, ErrUnknownHold) {
		t.Fatalf("Void(unknown): %v, want ErrUnknownHold", err)
	}
	if _, err := l.GetHold("hold_nope"); !errors.Is(err, ErrUnknownHold) {
		t.Fatalf("GetHold(unknown): %v, want ErrUnknownHold", err)
	}
}

func TestHold_ConcurrentHoldsNeverOverReserve(t *testing.T) {
	l := tLedger(t, tNewClock(), 1_000, "alice")
	const n = 50
	var won atomic.Int64
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			if _, err := l.Hold(fmt.Sprintf("auth-%d", i), "alice", 300, time.Hour); err == nil {
				won.Add(1)
			}
		}(i)
	}
	close(start)
	tWait(t, &wg, 10*time.Second, "50 concurrent holds")
	if won.Load() != 3 {
		t.Fatalf("%d holds of 300 succeeded against a balance of 1000; exactly 3 may", won.Load())
	}
	if av := tAvailable(t, l, "alice"); av != 100 {
		t.Fatalf("available = %d, want 100", av)
	}
}

// ---------- reversals ----------

func TestReverse_PostsMirrorEntriesOnce(t *testing.T) {
	l := tLedger(t, tNewClock(), 1_000, "alice", "bob")
	p, _, _ := l.Post(Request{Key: "k", Entries: tTransfer("alice", "bob", 400), ExternalRef: "ref-1"})
	r, err := l.Reverse("undo-k", p.ID)
	if err != nil || r.Reverses != p.ID || r.Amount() != 400 {
		t.Fatalf("Reverse = %+v, %v; want a posting of 400 with Reverses=%s", r, err, p.ID)
	}
	if r.Entries[0].Account != "alice" || r.Entries[0].Side != Credit || r.Entries[1].Account != "bob" || r.Entries[1].Side != Debit {
		t.Fatalf("reversal entries %+v: every side must be flipped, accounts and amounts kept", r.Entries)
	}
	if a, b := tBalance(t, l, "alice"), tBalance(t, l, "bob"); a != 1_000 || b != 1_000 {
		t.Fatalf("balances after reversal alice=%d bob=%d, want 1000 and 1000", a, b)
	}
	again, err := l.Reverse("undo-k", p.ID)
	if err != nil || again.ID != r.ID {
		t.Fatalf("replayed Reverse = %+v, %v; want %s", again, err, r.ID)
	}
	if _, err := l.Reverse("undo-k-2", p.ID); !errors.Is(err, ErrAlreadyReversed) {
		t.Fatalf("second reversal with a new key: %v, want ErrAlreadyReversed", err)
	}
	if _, err := l.Reverse("x", "tx_nope"); !errors.Is(err, ErrUnknownPosting) {
		t.Fatalf("Reverse(unknown): %v, want ErrUnknownPosting", err)
	}
	if hist, _ := l.History("bob"); len(hist) != 3 || hist[2].ID != r.ID {
		t.Fatalf("the reversal must be appended to the journal, not edit the original: %+v", hist)
	}
	if err := l.Audit(); err != nil {
		t.Fatal(err)
	}
}

func TestReverse_ObeysNoNegativeRule(t *testing.T) {
	l := tLedger(t, tNewClock(), 1_000, "alice", "bob")
	p, _, _ := l.Post(Request{Key: "k", Entries: tTransfer("alice", "bob", 400)})
	if _, _, err := l.Post(Request{Key: "spend", Entries: tTransfer("bob", "settlement", 1_400)}); err != nil {
		t.Fatal(err)
	}
	if _, err := l.Reverse("undo", p.ID); !errors.Is(err, ErrInsufficientFunds) {
		t.Fatalf("reversing when bob has already spent the money: %v, want ErrInsufficientFunds (a reversal is an ordinary posting)", err)
	}
}

// ---------- audit and reconciliation ----------

func TestAudit_PassesOnEmptyAndAfterWorkload(t *testing.T) {
	l := New()
	if err := l.Audit(); err != nil {
		t.Fatalf("Audit on empty ledger: %v", err)
	}
	l = tLedger(t, tNewClock(), 500, "a", "b", "c")
	for i := 0; i < 20; i++ {
		_, _, _ = l.Post(Request{Key: fmt.Sprint("k", i), Entries: tTransfer([]string{"a", "b", "c"}[i%3], []string{"b", "c", "a"}[i%3], int64(10+i))})
	}
	h, _ := l.Hold("h", "a", 50, time.Hour)
	_, _ = l.Capture("cap", h.ID, 20, "b")
	if err := l.Audit(); err != nil {
		t.Fatalf("Audit: %v", err)
	}
	var total int64
	for _, acct := range []string{"settlement", "a", "b", "c"} {
		total += tBalance(t, l, acct)
	}
	if total != 0 {
		t.Fatalf("all balances sum to %d, want 0 in a closed double-entry system", total)
	}
}

func TestReconcile_ReportsEveryKindOfDiscrepancy(t *testing.T) {
	l := tLedger(t, tNewClock(), 10_000, "alice", "bob")
	post := func(key, ref string, amt int64) Posting {
		p, _, err := l.Post(Request{Key: key, Entries: tTransfer("alice", "bob", amt), ExternalRef: ref})
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	post("k1", "ref-1", 100)
	post("k2", "ref-2", 200)
	p3 := post("k3", "ref-3", 300)
	post("k4", "", 400) // no external ref: internal transfer, never on a statement
	stmt := []StatementLine{
		{Ref: "ref-1", Amount: 100, Currency: "USD"},
		{Ref: "ref-2", Amount: 250, Currency: "USD"}, // amount mismatch
		{Ref: "ref-9", Amount: 900, Currency: "USD"}, // not in ledger
		{Ref: "ref-5", Amount: 50, Currency: "USD"},  // duplicated on the statement
		{Ref: "ref-5", Amount: 50, Currency: "USD"},
	}
	r := l.Reconcile(stmt)
	if r.Clean() {
		t.Fatalf("Clean() = true for a statement with discrepancies: %+v", r)
	}
	if len(r.Matched) != 1 || r.Matched[0] != "ref-1" {
		t.Errorf("Matched = %v, want [ref-1]", r.Matched)
	}
	if len(r.Mismatched) != 1 || r.Mismatched[0] != (Mismatch{Ref: "ref-2", LedgerAmount: 200, StatementAmount: 250}) {
		t.Errorf("Mismatched = %+v, want ref-2 ledger 200 vs statement 250", r.Mismatched)
	}
	if len(r.MissingInLedger) != 1 || r.MissingInLedger[0].Ref != "ref-9" {
		t.Errorf("MissingInLedger = %+v, want [ref-9]", r.MissingInLedger)
	}
	if len(r.MissingOnStatement) != 1 || r.MissingOnStatement[0] != p3.ID {
		t.Errorf("MissingOnStatement = %v, want [%s] (ref-3 was posted but never settled); postings without a ref are never reported", r.MissingOnStatement, p3.ID)
	}
	if len(r.DuplicateRefs) != 1 || r.DuplicateRefs[0] != "ref-5" {
		t.Errorf("DuplicateRefs = %v, want [ref-5]", r.DuplicateRefs)
	}
	clean := l.Reconcile([]StatementLine{
		{Ref: "ref-1", Amount: 100, Currency: "USD"},
		{Ref: "ref-2", Amount: 200, Currency: "USD"},
		{Ref: "ref-3", Amount: 300, Currency: "USD"},
	})
	if !clean.Clean() || len(clean.Matched) != 3 {
		t.Fatalf("a complete, correct statement must reconcile clean: %+v", clean)
	}
	if r2 := l.Reconcile(nil); len(r2.MissingOnStatement) != 3 {
		t.Fatalf("empty statement: MissingOnStatement = %v, want all three referenced postings", r2.MissingOnStatement)
	}
}

func TestReconcile_CurrencyMismatchIsReported(t *testing.T) {
	l := tLedger(t, tNewClock(), 1_000, "alice", "bob")
	if _, _, err := l.Post(Request{Key: "k", Entries: tTransfer("alice", "bob", 100), ExternalRef: "ref-1"}); err != nil {
		t.Fatal(err)
	}
	r := l.Reconcile([]StatementLine{{Ref: "ref-1", Amount: 100, Currency: "EUR"}})
	if len(r.Mismatched) != 1 || len(r.Matched) != 0 {
		t.Fatalf("a statement line in the wrong currency must be a mismatch, got %+v", r)
	}
}
