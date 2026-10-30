package splitwise

import (
	"errors"
	"fmt"
	"math/rand"
	"reflect"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
)

// applyPlan pays every transfer on a copy of balances: the payer's balance
// rises (their debt shrinks) and the payee's falls.
func applyPlan(balances map[UserID]Money, plan []Transfer) map[UserID]Money {
	out := map[UserID]Money{}
	for u, b := range balances {
		out[u] = b
	}
	for _, tr := range plan {
		out[tr.From] += tr.Amount
		out[tr.To] -= tr.Amount
	}
	return out
}

func mustSimplify(t *testing.T, balances map[UserID]Money) []Transfer {
	t.Helper()
	plan, err := Simplify(balances)
	if err != nil {
		t.Fatalf("Simplify(%v) = error %v", balances, err)
	}
	return plan
}

func mustLedgerPlan(t *testing.T, l *Ledger) []Transfer {
	t.Helper()
	plan, err := l.Simplify()
	if err != nil {
		t.Fatalf("Ledger.Simplify() = error %v", err)
	}
	return plan
}

func wantPlan(t *testing.T, got []Transfer, want ...Transfer) {
	t.Helper()
	if len(got) == 0 && len(want) == 0 {
		return
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("plan = %v, want %v (largest debtor pays largest creditor; ties by user id)", got, want)
	}
}

func TestStage3_SimplifyGreedyLargestDebtorPaysLargestCreditor(t *testing.T) {
	// A +50, B +30, C -20, D -60.
	// D(60) pays A(50): D has 10 left. C(20) pays B(30): B has 10 left. D(10) pays B(10).
	plan := mustSimplify(t, map[UserID]Money{"A": 50, "B": 30, "C": -20, "D": -60})
	wantPlan(t, plan, Transfer{"D", "A", 50}, Transfer{"C", "B", 20}, Transfer{"D", "B", 10})
}

func TestStage3_SimplifyTiesBreakByUserIDNeverByMapOrder(t *testing.T) {
	equal := map[UserID]Money{"a": 10, "b": 10, "c": 10, "x": -10, "y": -10, "z": -10}
	partial := map[UserID]Money{"a": 10, "b": 10, "x": -15, "y": -5}
	for i := 0; i < 200; i++ { // Go randomises map iteration per range loop
		wantPlan(t, mustSimplify(t, equal), Transfer{"x", "a", 10}, Transfer{"y", "b", 10}, Transfer{"z", "c", 10})
		wantPlan(t, mustSimplify(t, partial), Transfer{"x", "a", 10}, Transfer{"x", "b", 5}, Transfer{"y", "b", 5})
	}
}

func TestStage3_SimplifyEdgeCasesEmptyZeroAndUnbalanced(t *testing.T) {
	wantPlan(t, mustSimplify(t, nil))
	wantPlan(t, mustSimplify(t, map[UserID]Money{}))
	wantPlan(t, mustSimplify(t, map[UserID]Money{"a": 0, "b": 0}))
	wantPlan(t, mustSimplify(t, map[UserID]Money{"a": 0, "b": 5, "c": -5, "d": 0}), Transfer{"c", "b", 5})
	// Books that do not balance are an error, not a plan.
	for _, in := range []map[UserID]Money{{"a": 10}, {"a": 10, "b": -9}, {"a": -1, "b": -1}} {
		plan, err := Simplify(in)
		if !errors.Is(err, ErrUnbalanced) || len(plan) != 0 {
			t.Errorf("Simplify(%v) = (%v, %v), want (empty, ErrUnbalanced): a plan for unbalanced books would hide a bug", in, plan, err)
		}
	}
}

func TestStage3_SimplifyDoesNotModifyItsInput(t *testing.T) {
	in := map[UserID]Money{"a": 70, "b": 5, "c": -30, "d": -45}
	snapshot := map[UserID]Money{"a": 70, "b": 5, "c": -30, "d": -45}
	mustSimplify(t, in)
	if !reflect.DeepEqual(in, snapshot) {
		t.Fatalf("Simplify changed its input: %v, was %v; it must work on its own copy", in, snapshot)
	}
}

func TestStage3_SimplifyPlansAreCorrectBoundedAndOneDirectional(t *testing.T) {
	rng := rand.New(rand.NewSource(99))
	for trial := 0; trial < 500; trial++ {
		n := 2 + rng.Intn(30)
		bal := map[UserID]Money{}
		for i := 0; i < n; i++ {
			bal[UserID(fmt.Sprintf("u%02d", i))] = 0
		}
		// Random money moves between random pairs: always sums to zero.
		for k := 0; k < 3*n; k++ {
			a, b := rng.Intn(n), rng.Intn(n)
			amt := Money(rng.Intn(10_000))
			bal[UserID(fmt.Sprintf("u%02d", a))] -= amt
			bal[UserID(fmt.Sprintf("u%02d", b))] += amt
		}
		nonzero := len(nonZero(bal))
		plan := mustSimplify(t, bal)
		if left := nonZero(applyPlan(bal, plan)); len(left) != 0 {
			t.Fatalf("trial %d: after paying the plan these balances are not zero: %v (plan %v)", trial, left, plan)
		}
		if limit := max(nonzero-1, 0); len(plan) > limit {
			t.Fatalf("trial %d: %d transfers for %d non-zero balances, want at most %d", trial, len(plan), nonzero, limit)
		}
		sends, receives := map[UserID]bool{}, map[UserID]bool{}
		for _, tr := range plan {
			if tr.Amount <= 0 || bal[tr.From] >= 0 || bal[tr.To] <= 0 {
				t.Fatalf("trial %d: bad transfer %+v (From must be a debtor, To a creditor, amount > 0)", trial, tr)
			}
			sends[tr.From], receives[tr.To] = true, true
		}
		for u := range sends {
			if receives[u] {
				t.Fatalf("trial %d: %s both pays and receives in %v; that is a wasted transfer", trial, u, plan)
			}
		}
		if again := mustSimplify(t, bal); !reflect.DeepEqual(again, plan) {
			t.Fatalf("trial %d: Simplify is not deterministic: %v then %v", trial, plan, again)
		}
	}
}

func TestStage3_SimplifyCollapsesAChainOfDebts(t *testing.T) {
	l := mustNew(t, "a", "b", "c", "d")
	mustAdd(t, l, "a", 20, "a", "b") // b owes a 10
	mustAdd(t, l, "b", 20, "b", "c") // c owes b 10
	mustAdd(t, l, "c", 20, "c", "d") // d owes c 10
	if got := len(l.Debts()); got != 3 {
		t.Fatalf("Debts() has %d entries, want 3 (the pairwise view keeps the chain)", got)
	}
	wantPlan(t, mustLedgerPlan(t, l), Transfer{"d", "a", 10})
}

func TestStage3_ExactMinimumIsNotRequiredButTheBoundIs(t *testing.T) {
	// {A+10, B+9, C-4, D-6, E-9} splits into two zero-sum groups ({A,C,D} and
	// {B,E}), so 3 transfers suffice. The greedy plan crosses the groups and
	// uses 4 = n-1. That is the documented price of a polynomial heuristic.
	bal := map[UserID]Money{"A": 10, "B": 9, "C": -4, "D": -6, "E": -9}
	plan := mustSimplify(t, bal)
	if len(plan) < 3 || len(plan) > 4 {
		t.Fatalf("plan %v has %d transfers, want 3 (optimal) or 4 (greedy, the bound for 5 people)", plan, len(plan))
	}
	if left := nonZero(applyPlan(bal, plan)); len(left) != 0 {
		t.Fatalf("plan %v leaves %v unsettled", plan, left)
	}
}

func TestStage3_LedgerSimplifyDoesNotMutateTheLedgerAndSettlingZeroesIt(t *testing.T) {
	rng := rand.New(rand.NewSource(21))
	users := []UserID{"ann", "ben", "cy", "di", "ed", "flo", "gus"}
	l := mustNew(t, users...)
	for i := 0; i < 200; i++ {
		perm := rng.Perm(len(users))[:1+rng.Intn(len(users))]
		parts := make([]UserID, len(perm))
		for j, p := range perm {
			parts[j] = users[p]
		}
		mustAdd(t, l, users[rng.Intn(len(users))], Money(1+rng.Intn(20_000)), parts...)
	}
	balBefore, debtsBefore := l.Balances(), l.Debts()
	plan := mustLedgerPlan(t, l)
	if !reflect.DeepEqual(l.Balances(), balBefore) || !reflect.DeepEqual(l.Debts(), debtsBefore) {
		t.Fatal("Ledger.Simplify changed the ledger; computing a plan must not settle anything")
	}
	if limit := len(nonZero(balBefore)) - 1; len(plan) > limit {
		t.Fatalf("%d transfers for %d non-zero balances", len(plan), len(nonZero(balBefore)))
	}
	for _, tr := range plan {
		if err := l.Settle(tr.From, tr.To, tr.Amount); err != nil {
			t.Fatalf("Settle(%+v) = %v", tr, err)
		}
	}
	// Balances are zero; the pairwise Debts() view need not be, because the plan
	// pays people who were not the original creditor. Balances are the truth.
	wantBalances(t, l, nil)
	wantPlan(t, mustLedgerPlan(t, l))
}

func TestStage3_SimplifyAGroupFromItsOwnBalances(t *testing.T) {
	l := mustNew(t, "a", "b", "c", "d")
	if err := l.CreateGroup("g", "a", "b", "c"); err != nil {
		t.Fatal(err)
	}
	if _, err := l.AddGroupExpense("g", "a", 900, Equal("a", "b", "c")); err != nil {
		t.Fatal(err)
	}
	mustAdd(t, l, "d", 777, "a", "d") // outside the group: must not leak into the plan
	gb, err := l.GroupBalances("g")
	if err != nil {
		t.Fatal(err)
	}
	plan := mustSimplify(t, gb)
	wantPlan(t, plan, Transfer{"b", "a", 300}, Transfer{"c", "a", 300})
	for _, tr := range plan {
		if err := l.SettleInGroup("g", tr.From, tr.To, tr.Amount); err != nil {
			t.Fatalf("SettleInGroup(%+v) = %v", tr, err)
		}
	}
	gb, _ = l.GroupBalances("g")
	if len(nonZero(gb)) != 0 {
		t.Fatalf("group balances after paying the plan = %v, want all zero", gb)
	}
}

func TestStage3_SimplifyScalesToHundredsOfThousandsOfUsers(t *testing.T) {
	// 120,000 users with distinct-ish balances. Re-scanning for the largest
	// creditor/debtor every step is ~60,000 x 120,000 = 7*10^9 steps and
	// misses the time cap; two heaps take well under a second.
	const half = 60_000
	rng := rand.New(rand.NewSource(1))
	credit := make([]Money, half)
	for i := range credit {
		credit[i] = Money(1 + rng.Intn(100_000))
	}
	bal := make(map[UserID]Money, 2*half)
	for i, c := range credit {
		bal[UserID(fmt.Sprintf("c%06d", i))] = c
	}
	for i, p := range rng.Perm(half) { // same multiset of amounts, so the sum is zero
		bal[UserID(fmt.Sprintf("d%06d", i))] = -credit[p]
	}
	var plan []Transfer
	within(t, "Simplify of 120,000 balances", func() { plan, _ = Simplify(bal) })
	if len(plan) == 0 || len(plan) > 2*half-1 {
		t.Fatalf("plan has %d transfers, want between 1 and %d", len(plan), 2*half-1)
	}
	if left := nonZero(applyPlan(bal, plan)); len(left) != 0 {
		t.Fatalf("%d users left unsettled by the plan", len(left))
	}
}

// ---- concurrency ---------------------------------------------------------------

func TestStage3_ConcurrentWritersAndReadersSeeConsistentSnapshots(t *testing.T) {
	const nUsers, writers, perWriter, readers = 12, 6, 1_000, 6
	users := make([]UserID, nUsers)
	for i := range users {
		users[i] = UserID(fmt.Sprintf("u%02d", i))
	}
	expense := func(w, i int) (UserID, Money, []UserID) {
		payer := users[(w+i)%nUsers]
		parts := []UserID{users[(w*3+i)%nUsers], users[(w+i*5+1)%nUsers], users[(i+7)%nUsers]}
		if parts[0] == parts[1] || parts[0] == parts[2] || parts[1] == parts[2] {
			parts = parts[:1]
		}
		return payer, Money(1 + (i*37+w*11)%997), parts
	}

	l := mustNew(t, users...)
	var failure atomic.Value // first reader complaint
	fail := func(format string, args ...any) { failure.CompareAndSwap(nil, fmt.Sprintf(format, args...)) }

	var writing, reading sync.WaitGroup
	stop := make(chan struct{})
	for r := 0; r < readers; r++ {
		reading.Add(1)
		go func(r int) {
			defer reading.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				runtime.Gosched() // readers must interleave with writers, not starve them
				switch r % 3 {
				case 0:
					if s := sum(l.Balances()); s != 0 {
						fail("Balances() summed to %d: a reader saw part of an expense (torn snapshot)", s)
					}
				case 1:
					for _, d := range l.Debts() {
						if d.Amount <= 0 || d.From == d.To {
							fail("Debts() returned a malformed entry %+v", d)
						}
					}
				default:
					plan, err := l.Simplify()
					if err != nil {
						fail("Ledger.Simplify = %v: the snapshot it planned from did not sum to zero", err)
					}
					for _, tr := range plan {
						if tr.Amount <= 0 || tr.From == tr.To {
							fail("Simplify returned a malformed transfer %+v", tr)
						}
					}
				}
			}
		}(r)
	}
	for w := 0; w < writers; w++ {
		writing.Add(1)
		go func(w int) {
			defer writing.Done()
			for i := 0; i < perWriter; i++ {
				payer, amt, parts := expense(w, i)
				if _, err := l.AddExpense(payer, amt, parts); err != nil {
					fail("AddExpense(%s, %d, %v) = %v", payer, amt, parts, err)
					return
				}
			}
		}(w)
	}
	within(t, "concurrent writers", writing.Wait)
	close(stop)
	within(t, "concurrent readers", reading.Wait)
	if msg := failure.Load(); msg != nil {
		t.Fatal(msg)
	}

	// Addition commutes, so the final state must equal a sequential replay.
	want := mustNew(t, users...)
	for w := 0; w < writers; w++ {
		for i := 0; i < perWriter; i++ {
			payer, amt, parts := expense(w, i)
			mustAdd(t, want, payer, amt, parts...)
		}
	}
	if got := l.Balances(); !reflect.DeepEqual(got, want.Balances()) {
		t.Fatalf("concurrent result %v differs from sequential replay %v: lost or duplicated updates", got, want.Balances())
	}
	if !reflect.DeepEqual(l.Debts(), want.Debts()) {
		t.Fatal("concurrent Debts() differ from the sequential replay: the pair view and the balance view drifted apart")
	}
}

func TestStage3_ConcurrentAddRemoveSettleAndGroupsStayBalanced(t *testing.T) {
	users := []UserID{"a", "b", "c", "d", "e"}
	l := mustNew(t, users...)
	if err := l.CreateGroup("g", users...); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	var bad atomic.Value
	for w := 0; w < 8; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			var mine []ExpenseID
			for i := 0; i < 600; i++ {
				payer := users[(w+i)%len(users)]
				var id ExpenseID
				var err error
				if i%2 == 0 {
					id, err = l.AddExpenseSplit(payer, Money(100+i), Weights(map[UserID]int64{"a": 1, "b": 2, "c": 3, "d": 1}))
				} else {
					id, err = l.AddGroupExpense("g", payer, Money(100+i), Percent(map[UserID]Bps{"b": 3333, "c": 3333, "e": 3334}))
				}
				if err != nil {
					bad.CompareAndSwap(nil, fmt.Sprint("add: ", err))
					return
				}
				mine = append(mine, id)
				if i%7 == 0 {
					if err := l.SettleInGroup("g", users[(w+i+1)%len(users)], payer, Money(1+i%50)); err != nil {
						bad.CompareAndSwap(nil, fmt.Sprint("settle: ", err))
						return
					}
				}
				if i%3 == 0 { // remove an expense this worker added earlier
					if err := l.RemoveExpense(mine[0]); err != nil {
						bad.CompareAndSwap(nil, fmt.Sprint("remove: ", err))
						return
					}
					mine = mine[1:]
				}
				if gb, err := l.GroupBalances("g"); err != nil || sum(gb) != 0 {
					bad.CompareAndSwap(nil, fmt.Sprint("GroupBalances sum ", sum(gb), " err ", err))
					return
				}
				if s := sum(l.Balances()); s != 0 {
					bad.CompareAndSwap(nil, fmt.Sprint("Balances sum ", s))
					return
				}
			}
		}(w)
	}
	within(t, "mixed concurrent operations", wg.Wait)
	if msg := bad.Load(); msg != nil {
		t.Fatal(msg)
	}
	wantBalances(t, l, l.Balances())
	gb, _ := l.GroupBalances("g")
	if sum(gb) != 0 {
		t.Fatalf("final group balances sum to %d", sum(gb))
	}
}
