package splitwise

import (
	"errors"
	"fmt"
	"math/rand"
	"reflect"
	"testing"
)

func mustSplit(t *testing.T, s Splitter, total Money) []Share {
	t.Helper()
	if s == nil {
		t.Fatal("splitter constructor returned nil")
	}
	shares, err := s.Split(total)
	if err != nil {
		t.Fatalf("Split(%d) = error %v", total, err)
	}
	return shares
}

func amounts(shares []Share) []Money {
	out := make([]Money, len(shares))
	for i, s := range shares {
		out[i] = s.Amount
	}
	return out
}

func wantAmounts(t *testing.T, got []Share, users []UserID, want ...Money) {
	t.Helper()
	if len(got) != len(users) {
		t.Fatalf("got %d shares %v, want %d for %v", len(got), got, len(users), users)
	}
	for i, s := range got {
		if s.User != users[i] || s.Amount != want[i] {
			t.Fatalf("shares = %v, want users %v with amounts %v (sorted by user id)", got, users, want)
		}
	}
}

func TestStage2_ExactSplit(t *testing.T) {
	l := mustNew(t, "a", "b", "c")
	id, err := l.AddExpenseSplit("a", 1000, Exact(map[UserID]Money{"a": 100, "b": 250, "c": 650}))
	if err != nil || id == 0 {
		t.Fatalf("AddExpenseSplit(Exact) = (%d, %v)", id, err)
	}
	wantBalances(t, l, map[UserID]Money{"a": 900, "b": -250, "c": -650})

	// A participant with an explicit zero share is allowed and owes nothing.
	l = mustNew(t, "a", "b", "c")
	if _, err := l.AddExpenseSplit("a", 500, Exact(map[UserID]Money{"b": 500, "c": 0})); err != nil {
		t.Fatalf("Exact with a zero share = %v, want nil", err)
	}
	wantBalances(t, l, map[UserID]Money{"a": 500, "b": -500})

	// Invalid input is rejected and changes nothing.
	l = mustNew(t, "a", "b")
	cases := []struct {
		name  string
		total Money
		s     Splitter
		want  error
	}{
		{"short by one cent", 1000, Exact(map[UserID]Money{"a": 500, "b": 499}), ErrSplitSum},
		{"over by one cent", 1000, Exact(map[UserID]Money{"a": 500, "b": 501}), ErrSplitSum},
		{"negative share that still sums", 1000, Exact(map[UserID]Money{"a": 1500, "b": -500}), ErrInvalidShare},
		{"no participants", 1000, Exact(nil), ErrNoParticipants},
		{"zero total", 0, Exact(map[UserID]Money{"a": 0}), ErrInvalidAmount},
		{"nil splitter", 1000, nil, ErrNoParticipants},
	}
	for _, tc := range cases {
		if id, err := l.AddExpenseSplit("a", tc.total, tc.s); !errors.Is(err, tc.want) || id != 0 {
			t.Errorf("%s: = (%d, %v), want (0, %v)", tc.name, id, err, tc.want)
		}
	}
	wantBalances(t, l, nil)

	// The splitter copied the caller's map: reusing the map afterwards is harmless.
	m := map[UserID]Money{"a": 600, "b": 400}
	sp := Exact(m)
	m["a"], m["b"] = 1, 1
	wantAmounts(t, mustSplit(t, sp, 1000), []UserID{"a", "b"}, 600, 400)
}

func TestStage2_PercentSplitUsesBasisPointsAndLargestRemainder(t *testing.T) {
	third := map[UserID]Bps{"a": 3333, "b": 3333, "c": 3334}
	ids := []UserID{"a", "b", "c"}
	// 10.00 at 33.33/33.33/33.34: exact 333.3, 333.3, 333.4 -> floors sum to 999, the spare cent
	// goes to c (largest fractional part).
	wantAmounts(t, mustSplit(t, Percent(third), 1000), ids, 333, 333, 334)
	wantAmounts(t, mustSplit(t, Percent(third), 100), ids, 33, 33, 34)
	// Exact ties go to the lowest id: 1.01 at 50/50.
	wantAmounts(t, mustSplit(t, Percent(map[UserID]Bps{"a": 5000, "b": 5000}), 101), []UserID{"a", "b"}, 51, 50)
	// 0.10 at 25% x 4: every remainder ties, two spare cents go to the first two ids.
	quarters := map[UserID]Bps{"a": 2500, "b": 2500, "c": 2500, "d": 2500}
	wantAmounts(t, mustSplit(t, Percent(quarters), 10), []UserID{"a", "b", "c", "d"}, 3, 3, 2, 2)

	// Validation: the percentages must be exactly 100%.
	l := mustNew(t, "a", "b")
	cases := []struct {
		name string
		s    Splitter
		want error
	}{
		{"99.99%", Percent(map[UserID]Bps{"a": 5000, "b": 4999}), ErrPercentSum},
		{"100.01%", Percent(map[UserID]Bps{"a": 5000, "b": 5001}), ErrPercentSum},
		{"negative entry that sums to 100%", Percent(map[UserID]Bps{"a": 11000, "b": -1000}), ErrInvalidShare},
		{"no participants", Percent(nil), ErrNoParticipants},
	}
	for _, tc := range cases {
		if _, err := l.AddExpenseSplit("a", 1000, tc.s); !errors.Is(err, tc.want) {
			t.Errorf("%s: error = %v, want %v", tc.name, err, tc.want)
		}
	}
	wantBalances(t, l, nil)
}

func TestStage2_PercentSharesAlwaysSumAndStayWithinOneCent(t *testing.T) {
	rng := rand.New(rand.NewSource(3))
	for trial := 0; trial < 2_000; trial++ {
		n := 1 + rng.Intn(8)
		bps := map[UserID]Bps{}
		left := FullBps
		for i := 0; i < n; i++ {
			v := left
			if i < n-1 {
				v = Bps(rng.Int63n(int64(left) + 1))
			}
			bps[UserID(fmt.Sprintf("u%d", i))] = v
			left -= v
		}
		total := Money(1 + rng.Int63n(5_000_000))
		shares := mustSplit(t, Percent(bps), total)
		var got Money
		for _, s := range shares {
			got += s.Amount
			exact := float64(total) * float64(bps[s.User]) / float64(FullBps)
			if d := float64(s.Amount) - exact; d <= -1 || d >= 1 {
				t.Fatalf("total %d, %s at %d bp got %d, exact %.3f: more than a cent off", total, s.User, bps[s.User], s.Amount, exact)
			}
		}
		if got != total {
			t.Fatalf("total %d at %v: shares sum to %d; rounding lost or invented money", total, bps, got)
		}
		if again := mustSplit(t, Percent(bps), total); !reflect.DeepEqual(again, shares) {
			t.Fatalf("Percent split is not deterministic: %v then %v", shares, again)
		}
	}
}

func TestStage2_WeightsSplit(t *testing.T) {
	ids := []UserID{"a", "b", "c"}
	w := map[UserID]int64{"a": 1, "b": 2, "c": 3}
	wantAmounts(t, mustSplit(t, Weights(w), 600), ids, 100, 200, 300)
	// 1.00 in 1:2:3: exact 16.67, 33.33, 50.00 -> the spare cent goes to a (largest remainder).
	wantAmounts(t, mustSplit(t, Weights(w), 100), ids, 17, 33, 50)
	// A zero weight owes nothing, and never receives a spare cent.
	wantAmounts(t, mustSplit(t, Weights(map[UserID]int64{"a": 1, "b": 0, "c": 1}), 101), ids, 51, 0, 50)

	for _, bad := range []map[UserID]int64{{"a": 0, "b": 0}, {"a": -1, "b": 2}, {"a": MaxWeight + 1}} {
		if _, err := Weights(bad).Split(100); !errors.Is(err, ErrInvalidShare) {
			t.Errorf("Weights(%v) error = %v, want ErrInvalidShare", bad, err)
		}
	}
	if _, err := Weights(nil).Split(100); !errors.Is(err, ErrNoParticipants) {
		t.Errorf("Weights(nil) error = %v, want ErrNoParticipants", err)
	}
}

func TestStage2_EqualSplitterMatchesAddExpenseAndRejectsUnknownUsersAtomically(t *testing.T) {
	l1, l2 := mustNew(t, "a", "b", "c", "x"), mustNew(t, "a", "b", "c", "x")
	mustAdd(t, l1, "x", 1001, "c", "a", "b")
	if _, err := l2.AddExpenseSplit("x", 1001, Equal("b", "c", "a")); err != nil {
		t.Fatalf("AddExpenseSplit(Equal) = %v", err)
	}
	if !reflect.DeepEqual(l1.Balances(), l2.Balances()) {
		t.Fatalf("AddExpense gave %v, AddExpenseSplit(Equal) gave %v; they must be the same operation", l1.Balances(), l2.Balances())
	}
	if _, err := l2.AddExpenseSplit("x", 100, Equal("a", "a")); !errors.Is(err, ErrDuplicateParticipant) {
		t.Errorf("Equal with a repeated user: error = %v, want ErrDuplicateParticipant", err)
	}

	// Unknown users are rejected atomically: nothing is half-applied.
	l := mustNew(t, "a", "b")
	mustAdd(t, l, "a", 100, "a", "b")
	before := l.Balances()
	if _, err := l.AddExpenseSplit("a", 1000, Exact(map[UserID]Money{"b": 500, "ghost": 500})); !errors.Is(err, ErrUnknownUser) {
		t.Fatalf("unknown user in Exact: error = %v, want ErrUnknownUser", err)
	}
	if _, err := l.AddExpenseSplit("ghost", 1000, Equal("a", "b")); !errors.Is(err, ErrUnknownUser) {
		t.Fatalf("unknown payer: error = %v, want ErrUnknownUser", err)
	}
	if !reflect.DeepEqual(l.Balances(), before) {
		t.Fatalf("a rejected expense changed the balances: %v, was %v (half-applied?)", l.Balances(), before)
	}
}

// customSplitter is a strategy the ledger has never heard of: everyone else
// pays floor(total/(n+1)) and the payer absorbs the remainder. That it works
// without touching the ledger is the extensibility point of stage 2.
type customSplitter struct {
	payer  UserID
	others []UserID
}

func (c customSplitter) Split(total Money) ([]Share, error) {
	each := total / Money(len(c.others)+1)
	out := []Share{{User: c.payer, Amount: total - each*Money(len(c.others))}}
	for _, u := range c.others {
		out = append(out, Share{User: u, Amount: each})
	}
	return out, nil
}

// brokenSplitter returns whatever shares it was built with, valid or not.
type brokenSplitter []Share

func (b brokenSplitter) Split(Money) ([]Share, error) { return b, nil }

func TestStage2_CustomSplittersPlugInButTheLedgerDoesNotTrustThem(t *testing.T) {
	cl := mustNew(t, "a", "b", "c")
	if _, err := cl.AddExpenseSplit("a", 100, customSplitter{payer: "a", others: []UserID{"b", "c"}}); err != nil {
		t.Fatalf("custom splitter: %v", err)
	}
	wantBalances(t, cl, map[UserID]Money{"a": 66, "b": -33, "c": -33})

	l := mustNew(t, "a", "b")
	cases := []struct {
		name string
		s    Splitter
		want error
	}{
		{"shares sum to total-1", brokenSplitter{{"a", 50}, {"b", 49}}, ErrSplitSum},
		{"shares sum to total+1", brokenSplitter{{"a", 50}, {"b", 51}}, ErrSplitSum},
		{"negative share", brokenSplitter{{"a", 150}, {"b", -50}}, ErrInvalidShare},
		{"same user twice", brokenSplitter{{"a", 50}, {"a", 50}}, ErrDuplicateParticipant},
		{"unknown user", brokenSplitter{{"a", 50}, {"zed", 50}}, ErrUnknownUser},
		{"no shares", brokenSplitter{}, ErrNoParticipants},
	}
	for _, tc := range cases {
		if _, err := l.AddExpenseSplit("a", 100, tc.s); !errors.Is(err, tc.want) {
			t.Errorf("%s: error = %v, want %v: the ledger must check the invariant itself", tc.name, err, tc.want)
		}
	}
	wantBalances(t, l, nil)
}

// reentrantSplitter reads the ledger from inside Split.
type reentrantSplitter struct{ l *Ledger }

func (r reentrantSplitter) Split(total Money) ([]Share, error) {
	_ = r.l.Balances()
	return []Share{{User: "a", Amount: total}}, nil
}

func TestStage2_SplitterMayCallBackIntoTheLedger(t *testing.T) {
	l := mustNew(t, "a", "b")
	within(t, "AddExpenseSplit with a splitter that calls Balances", func() {
		if _, err := l.AddExpenseSplit("b", 100, reentrantSplitter{l}); err != nil {
			t.Errorf("AddExpenseSplit = %v", err)
		}
	})
	wantBalances(t, l, map[UserID]Money{"a": -100, "b": 100})
}

func TestStage2_SettleMovesBalancesNetsDebtsAndValidates(t *testing.T) {
	l := mustNew(t, "a", "b", "c")
	mustAdd(t, l, "a", 3000, "a", "b", "c")

	if err := l.Settle("b", "a", 1000); err != nil {
		t.Fatalf("Settle(b, a, 1000) = %v", err)
	}
	wantBalances(t, l, map[UserID]Money{"a": 1000, "c": -1000})
	wantDebts(t, l, Transfer{"c", "a", 1000})

	// Paying more than is owed flips the direction: now a owes b 300.
	if err := l.Settle("b", "a", 300); err != nil {
		t.Fatalf("Settle(b, a, 300) = %v", err)
	}
	wantBalances(t, l, map[UserID]Money{"a": 700, "b": 300, "c": -1000})
	wantDebts(t, l, Transfer{"a", "b", 300}, Transfer{"c", "a", 1000})

	// Validation.
	l = mustNew(t, "a", "b")
	mustAdd(t, l, "a", 200, "a", "b")
	cases := []struct {
		name     string
		from, to UserID
		amount   Money
		want     error
	}{
		{"zero", "b", "a", 0, ErrInvalidAmount},
		{"negative", "b", "a", -100, ErrInvalidAmount},
		{"above MaxAmount", "b", "a", MaxAmount + 1, ErrInvalidAmount},
		{"self payment", "a", "a", 100, ErrSelfPayment},
		{"unknown payer", "ghost", "a", 100, ErrUnknownUser},
		{"unknown payee", "a", "ghost", 100, ErrUnknownUser},
	}
	for _, tc := range cases {
		if err := l.Settle(tc.from, tc.to, tc.amount); !errors.Is(err, tc.want) {
			t.Errorf("%s: error = %v, want %v", tc.name, err, tc.want)
		}
	}
	wantBalances(t, l, map[UserID]Money{"a": 100, "b": -100})
}

func TestStage2_RemoveExpenseRestoresBalancesAndDebtsExactlyAndLeavesSettlementsAlone(t *testing.T) {
	l := mustNew(t, "a", "b", "c")
	mustAdd(t, l, "a", 100, "a", "b", "c")
	balBefore, debtsBefore := l.Balances(), l.Debts()

	id, err := l.AddExpenseSplit("b", 1001, Percent(map[UserID]Bps{"a": 3333, "b": 3333, "c": 3334}))
	if err != nil {
		t.Fatalf("AddExpenseSplit = %v", err)
	}
	if reflect.DeepEqual(l.Balances(), balBefore) {
		t.Fatal("the second expense changed nothing; test setup is wrong")
	}
	if err := l.RemoveExpense(id); err != nil {
		t.Fatalf("RemoveExpense = %v", err)
	}
	if !reflect.DeepEqual(l.Balances(), balBefore) {
		t.Fatalf("after removal Balances() = %v, want %v", l.Balances(), balBefore)
	}
	wantDebts(t, l, debtsBefore...)
	if err := l.RemoveExpense(id); !errors.Is(err, ErrUnknownExpense) {
		t.Errorf("second RemoveExpense = %v, want ErrUnknownExpense (removal is not idempotent: say so)", err)
	}
	if err := l.RemoveExpense(9999); !errors.Is(err, ErrUnknownExpense) {
		t.Errorf("RemoveExpense(9999) = %v, want ErrUnknownExpense", err)
	}

	// Settlements are independent of expenses.
	l = mustNew(t, "a", "b")
	id = mustAdd(t, l, "a", 1000, "a", "b") // b owes a 500
	if err := l.Settle("b", "a", 500); err != nil {
		t.Fatal(err)
	}
	wantBalances(t, l, nil)
	if err := l.RemoveExpense(id); err != nil {
		t.Fatal(err)
	}
	// The payment happened; without the expense it is now an overpayment.
	wantBalances(t, l, map[UserID]Money{"a": -500, "b": 500})
	wantDebts(t, l, Transfer{"a", "b", 500})
}

func TestStage2_RandomAddRemoveSettleKeepsTheInvariant(t *testing.T) {
	rng := rand.New(rand.NewSource(5))
	users := []UserID{"a", "b", "c", "d", "e", "f"}
	l, only := mustNew(t, users...), mustNew(t, users...) // only: settlements alone
	var live []ExpenseID
	for i := 0; i < 3_000; i++ {
		switch op := rng.Intn(10); {
		case op < 5:
			perm := rng.Perm(len(users))[:1+rng.Intn(len(users))]
			w := map[UserID]int64{}
			for _, p := range perm {
				w[users[p]] = int64(rng.Intn(4))
			}
			w[users[perm[0]]]++ // keep the sum positive
			id, err := l.AddExpenseSplit(users[rng.Intn(len(users))], Money(1+rng.Intn(50_000)), Weights(w))
			if err != nil {
				t.Fatalf("AddExpenseSplit = %v", err)
			}
			live = append(live, id)
		case op < 8 && len(live) > 0:
			k := rng.Intn(len(live))
			if err := l.RemoveExpense(live[k]); err != nil {
				t.Fatalf("RemoveExpense = %v", err)
			}
			live = append(live[:k], live[k+1:]...)
		default:
			from, to := users[rng.Intn(len(users))], users[rng.Intn(len(users))]
			amt := Money(1 + rng.Intn(5_000))
			if from == to {
				continue
			}
			if err := l.Settle(from, to, amt); err != nil {
				t.Fatalf("Settle = %v", err)
			}
			if err := only.Settle(from, to, amt); err != nil {
				t.Fatalf("Settle = %v", err)
			}
		}
		if s := sum(l.Balances()); s != 0 {
			t.Fatalf("step %d: balances sum to %d, want 0", i, s)
		}
	}
	for _, id := range live {
		if err := l.RemoveExpense(id); err != nil {
			t.Fatalf("RemoveExpense = %v", err)
		}
	}
	if !reflect.DeepEqual(nonZero(l.Balances()), nonZero(only.Balances())) {
		t.Fatalf("with every expense removed, balances = %v, want just the settlements: %v", nonZero(l.Balances()), nonZero(only.Balances()))
	}
	if !reflect.DeepEqual(l.Debts(), only.Debts()) {
		t.Fatalf("with every expense removed, Debts() = %v, want %v", l.Debts(), only.Debts())
	}
}

func TestStage2_GroupsScopeExpensesAndMembership(t *testing.T) {
	l := mustNew(t, "a", "b", "c", "d")
	if err := l.CreateGroup("trip", "a", "b", "c"); err != nil {
		t.Fatalf("CreateGroup = %v", err)
	}
	if err := l.CreateGroup("trip", "a"); !errors.Is(err, ErrDuplicateGroup) {
		t.Errorf("duplicate CreateGroup = %v, want ErrDuplicateGroup", err)
	}
	if err := l.CreateGroup("", "a"); !errors.Is(err, ErrInvalidGroup) {
		t.Errorf("CreateGroup(\"\") = %v, want ErrInvalidGroup", err)
	}
	if err := l.CreateGroup("bad", "a", "ghost"); !errors.Is(err, ErrUnknownUser) {
		t.Errorf("CreateGroup with an unknown member = %v, want ErrUnknownUser", err)
	}
	if err := l.CreateGroup("bad", "a", "a"); !errors.Is(err, ErrDuplicateParticipant) {
		t.Errorf("CreateGroup with a repeated member = %v, want ErrDuplicateParticipant", err)
	}
	if _, err := l.GroupBalances("bad"); !errors.Is(err, ErrUnknownGroup) {
		t.Errorf("a failed CreateGroup must not leave the group behind: GroupBalances error = %v", err)
	}

	if _, err := l.AddGroupExpense("trip", "a", 900, Equal("a", "b", "c")); err != nil {
		t.Fatalf("AddGroupExpense = %v", err)
	}
	mustAdd(t, l, "d", 400, "a", "d") // outside the group

	gb, err := l.GroupBalances("trip")
	if err != nil {
		t.Fatal(err)
	}
	if want := map[UserID]Money{"a": 600, "b": -300, "c": -300}; !reflect.DeepEqual(gb, want) {
		t.Fatalf("GroupBalances(trip) = %v, want %v: only the group's expenses count", gb, want)
	}
	wantBalances(t, l, map[UserID]Money{"a": 400, "b": -300, "c": -300, "d": 200})

	// Membership is enforced for the payer and for every share.
	if _, err := l.AddGroupExpense("trip", "d", 100, Equal("a", "b")); !errors.Is(err, ErrNotMember) {
		t.Errorf("non-member payer: error = %v, want ErrNotMember", err)
	}
	if _, err := l.AddGroupExpense("trip", "a", 100, Equal("a", "d")); !errors.Is(err, ErrNotMember) {
		t.Errorf("non-member participant: error = %v, want ErrNotMember", err)
	}
	if _, err := l.AddGroupExpense("nope", "a", 100, Equal("a")); !errors.Is(err, ErrUnknownGroup) {
		t.Errorf("unknown group: error = %v, want ErrUnknownGroup", err)
	}
	if err := l.AddGroupMember("trip", "d"); err != nil {
		t.Fatalf("AddGroupMember = %v", err)
	}
	if err := l.AddGroupMember("trip", "d"); !errors.Is(err, ErrDuplicateParticipant) {
		t.Errorf("AddGroupMember twice = %v, want ErrDuplicateParticipant", err)
	}
	if err := l.AddGroupMember("trip", "ghost"); !errors.Is(err, ErrUnknownUser) {
		t.Errorf("AddGroupMember(ghost) = %v, want ErrUnknownUser", err)
	}
	if _, err := l.AddGroupExpense("trip", "d", 100, Equal("a", "b")); err != nil {
		t.Errorf("new member as payer: %v", err)
	}
	// None of the rejected calls changed anything.
	gb, _ = l.GroupBalances("trip")
	if s := sum(gb); s != 0 {
		t.Fatalf("group balances sum to %d, want 0", s)
	}
}

func TestStage2_GroupSettleAndRemoveKeepGroupAndGlobalViewsInStep(t *testing.T) {
	l := mustNew(t, "a", "b", "c")
	if err := l.CreateGroup("flat", "a", "b"); err != nil {
		t.Fatal(err)
	}
	id, err := l.AddGroupExpense("flat", "a", 1000, Equal("a", "b"))
	if err != nil {
		t.Fatal(err)
	}
	if err := l.SettleInGroup("flat", "b", "a", 200); err != nil {
		t.Fatalf("SettleInGroup = %v", err)
	}
	gb, _ := l.GroupBalances("flat")
	if want := map[UserID]Money{"a": 300, "b": -300}; !reflect.DeepEqual(gb, want) {
		t.Fatalf("GroupBalances after SettleInGroup = %v, want %v", gb, want)
	}
	wantBalances(t, l, map[UserID]Money{"a": 300, "b": -300})

	if err := l.SettleInGroup("flat", "c", "a", 10); !errors.Is(err, ErrNotMember) {
		t.Errorf("SettleInGroup with a non-member = %v, want ErrNotMember", err)
	}
	if err := l.SettleInGroup("zzz", "b", "a", 10); !errors.Is(err, ErrUnknownGroup) {
		t.Errorf("SettleInGroup in an unknown group = %v, want ErrUnknownGroup", err)
	}
	if err := l.SettleInGroup("flat", "a", "a", 10); !errors.Is(err, ErrSelfPayment) {
		t.Errorf("SettleInGroup with yourself = %v, want ErrSelfPayment", err)
	}

	if err := l.RemoveExpense(id); err != nil {
		t.Fatal(err)
	}
	gb, _ = l.GroupBalances("flat")
	if want := map[UserID]Money{"a": -200, "b": 200}; !reflect.DeepEqual(gb, want) {
		t.Fatalf("GroupBalances after removing the group expense = %v, want %v (only the settlement is left)", gb, want)
	}
	wantBalances(t, l, map[UserID]Money{"a": -200, "b": 200})
}
