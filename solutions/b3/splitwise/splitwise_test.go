package splitwise

import (
	"errors"
	"fmt"
	"math/rand"
	"reflect"
	"testing"
	"time"
)

const testTimeout = 5 * time.Second

func mustNew(t *testing.T, users ...UserID) *Ledger {
	t.Helper()
	l, err := New(users...)
	if err != nil {
		t.Fatalf("New(%v) returned error %v", users, err)
	}
	if l == nil {
		t.Fatal("New returned a nil *Ledger with a nil error")
	}
	return l
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
		t.Fatalf("%s did not finish within %v (deadlock, or a per-operation cost that grows with the amount of history)", what, testTimeout)
	}
}

func mustAdd(t *testing.T, l *Ledger, payer UserID, amount Money, participants ...UserID) ExpenseID {
	t.Helper()
	id, err := l.AddExpense(payer, amount, participants)
	if err != nil {
		t.Fatalf("AddExpense(%s, %d, %v) = error %v", payer, amount, participants, err)
	}
	if id == 0 {
		t.Fatalf("AddExpense returned expense id 0; ids must be non-zero")
	}
	return id
}

func sum(m map[UserID]Money) Money {
	var s Money
	for _, v := range m {
		s += v
	}
	return s
}

// nonZero drops zero entries so expectations only list people with a position.
func nonZero(m map[UserID]Money) map[UserID]Money {
	out := map[UserID]Money{}
	for u, v := range m {
		if v != 0 {
			out[u] = v
		}
	}
	return out
}

func wantBalances(t *testing.T, l *Ledger, want map[UserID]Money) {
	t.Helper()
	got := l.Balances()
	if !reflect.DeepEqual(nonZero(got), nonZero(want)) {
		t.Fatalf("Balances() = %v, want %v (users with a zero balance omitted)", nonZero(got), nonZero(want))
	}
	if s := sum(got); s != 0 {
		t.Fatalf("balances sum to %d, want 0: money was created or destroyed (a lost remainder cent?)", s)
	}
}

func wantDebts(t *testing.T, l *Ledger, want ...Transfer) {
	t.Helper()
	got := l.Debts()
	if len(got) == 0 && len(want) == 0 {
		return
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Debts() = %v, want %v (netted per pair, sorted by From then To)", got, want)
	}
}

func TestStage1_UsersAreRegisteredAndValidated(t *testing.T) {
	if l, err := New("a", ""); !errors.Is(err, ErrInvalidUser) || l != nil {
		t.Errorf("New with an empty id = (%v, %v), want (nil, ErrInvalidUser)", l, err)
	}
	if l, err := New("a", "b", "a"); !errors.Is(err, ErrDuplicateUser) || l != nil {
		t.Errorf("New with a repeated id = (%v, %v), want (nil, ErrDuplicateUser)", l, err)
	}
	l := mustNew(t, "a")
	if err := l.AddUser("b"); err != nil {
		t.Fatalf("AddUser(b) = %v", err)
	}
	if err := l.AddUser("b"); !errors.Is(err, ErrDuplicateUser) {
		t.Errorf("second AddUser(b) = %v, want ErrDuplicateUser", err)
	}
	if err := l.AddUser(""); !errors.Is(err, ErrInvalidUser) {
		t.Errorf("AddUser(\"\") = %v, want ErrInvalidUser", err)
	}
	if got := len(l.Balances()); got != 2 {
		t.Errorf("Balances has %d users, want 2 (registered users appear with a zero balance)", got)
	}
	if b, err := l.Balance("a"); err != nil || b != 0 {
		t.Errorf("Balance(a) = (%d, %v), want (0, nil)", b, err)
	}
	if _, err := l.Balance("ghost"); !errors.Is(err, ErrUnknownUser) {
		t.Errorf("Balance(ghost) error = %v, want ErrUnknownUser (0 would be a lie: the user is not in the ledger)", err)
	}
}

func TestStage1_EqualSplitAndRemainderCentsGoToTheFirstParticipantsInSortedOrder(t *testing.T) {
	// No remainder: 30.00 among three.
	l := mustNew(t, "a", "b", "c")
	mustAdd(t, l, "a", 3000, "a", "b", "c")
	wantBalances(t, l, map[UserID]Money{"a": 2000, "b": -1000, "c": -1000})
	if b, _ := l.Balance("a"); b != 2000 {
		t.Errorf("Balance(a) = %d, want 2000", b)
	}

	// 1.00 among three: 34 + 33 + 33. The payer's own share is 34 because a
	// sorts first, so a is owed 100 - 34 = 66.
	l = mustNew(t, "a", "b", "c")
	mustAdd(t, l, "a", 100, "a", "b", "c")
	wantBalances(t, l, map[UserID]Money{"a": 66, "b": -33, "c": -33})

	// 0.10 among four, payer outside the group: remainder 2, so the first two
	// in sorted order owe 3 and the last two owe 2.
	l = mustNew(t, "a", "b", "c", "d", "x")
	mustAdd(t, l, "x", 10, "a", "b", "c", "d")
	wantBalances(t, l, map[UserID]Money{"x": 10, "a": -3, "b": -3, "c": -2, "d": -2})
}

func TestStage1_SplitDoesNotDependOnTheOrderParticipantsAreListed(t *testing.T) {
	orders := [][]UserID{{"a", "b", "c", "d"}, {"d", "c", "b", "a"}, {"c", "a", "d", "b"}}
	var first map[UserID]Money
	for _, order := range orders {
		l := mustNew(t, "a", "b", "c", "d", "x")
		mustAdd(t, l, "x", 10, order...)
		got := l.Balances()
		if first == nil {
			first = got
			continue
		}
		if !reflect.DeepEqual(got, first) {
			t.Fatalf("participants listed as %v gave %v, but %v gave %v: the remainder rule must use sorted ids, not caller order", order, got, orders[0], first)
		}
	}
}

func TestStage1_SharesAlwaysSumToTheAmount(t *testing.T) {
	// For every n and amount: the payer (outside the group) is owed exactly the
	// amount, each participant owes floor or ceil of amount/n, exactly
	// amount%n of them owe the ceiling, and they are the first ones in sorted order.
	for n := 1; n <= 9; n++ {
		ids := make([]UserID, n)
		users := []UserID{"z"}
		for i := range ids {
			ids[i] = UserID(fmt.Sprintf("u%d", i+1))
			users = append(users, ids[i])
		}
		for amount := Money(1); amount <= 250; amount++ {
			l := mustNew(t, users...)
			mustAdd(t, l, "z", amount, ids...)
			bal := l.Balances()
			if bal["z"] != amount || sum(bal) != 0 {
				t.Fatalf("n=%d amount=%d: payer balance %d (want %d), sum %d (want 0)", n, amount, bal["z"], amount, sum(bal))
			}
			floor, rem := amount/Money(n), int(amount%Money(n))
			for i, u := range ids {
				want := floor
				if i < rem {
					want++
				}
				if bal[u] != -want {
					t.Fatalf("n=%d amount=%d: %s owes %d, want %d (first %d sorted participants owe one extra cent)", n, amount, u, -bal[u], want, rem)
				}
			}
		}
	}
}

func TestStage1_InvalidExpensesAreRejectedAndChangeNothing(t *testing.T) {
	l := mustNew(t, "a", "b", "c")
	mustAdd(t, l, "a", 900, "a", "b", "c")
	before := l.Balances()
	debtsBefore := l.Debts()
	cases := []struct {
		name         string
		payer        UserID
		amount       Money
		participants []UserID
		want         error
	}{
		{"zero amount", "a", 0, []UserID{"a", "b"}, ErrInvalidAmount},
		{"negative amount", "a", -5, []UserID{"a", "b"}, ErrInvalidAmount},
		{"amount above MaxAmount", "a", MaxAmount + 1, []UserID{"a", "b"}, ErrInvalidAmount},
		{"unknown payer", "ghost", 100, []UserID{"a", "b"}, ErrUnknownUser},
		{"empty payer", "", 100, []UserID{"a", "b"}, ErrUnknownUser},
		{"unknown participant", "a", 100, []UserID{"a", "ghost"}, ErrUnknownUser},
		{"nil participants", "a", 100, nil, ErrNoParticipants},
		{"empty participants", "a", 100, []UserID{}, ErrNoParticipants},
		{"duplicate participant", "a", 100, []UserID{"b", "b", "c"}, ErrDuplicateParticipant},
	}
	for _, tc := range cases {
		id, err := l.AddExpense(tc.payer, tc.amount, tc.participants)
		if !errors.Is(err, tc.want) {
			t.Errorf("%s: error = %v, want %v", tc.name, err, tc.want)
		}
		if id != 0 {
			t.Errorf("%s: returned expense id %d alongside an error", tc.name, id)
		}
	}
	if got := l.Balances(); !reflect.DeepEqual(got, before) {
		t.Fatalf("rejected expenses changed balances: %v, was %v; validate everything before mutating anything", got, before)
	}
	wantDebts(t, l, debtsBefore...)

	// The boundary itself is valid, and every accepted expense gets a fresh id.
	big := mustNew(t, "a", "b", "c")
	first := mustAdd(t, big, "a", MaxAmount, "a", "b", "c")
	if bal := big.Balances(); sum(bal) != 0 || bal["a"] <= 0 {
		t.Fatalf("balances after a MaxAmount expense = %v; want a positive payer and a zero sum", bal)
	}
	if second := mustAdd(t, big, "b", 1, "a"); second == first {
		t.Fatalf("two expenses share id %d", first)
	}
}

func TestStage1_BalancesReturnsACopy(t *testing.T) {
	l := mustNew(t, "a", "b")
	mustAdd(t, l, "a", 200, "a", "b")
	snap := l.Balances()
	snap["a"] = 1_000_000
	delete(snap, "b")
	wantBalances(t, l, map[UserID]Money{"a": 100, "b": -100})
}

func TestStage1_BalancesSumToZeroAfterManyRandomExpenses(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	users := make([]UserID, 20)
	for i := range users {
		users[i] = UserID(fmt.Sprintf("user%02d", i))
	}
	l := mustNew(t, users...)
	for i := 1; i <= 5_000; i++ {
		payer := users[rng.Intn(len(users))]
		perm := rng.Perm(len(users))[:1+rng.Intn(len(users))]
		parts := make([]UserID, len(perm))
		for j, p := range perm {
			parts[j] = users[p]
		}
		mustAdd(t, l, payer, Money(1+rng.Intn(100_000)), parts...)
		if i%100 == 0 {
			if s := sum(l.Balances()); s != 0 {
				t.Fatalf("after %d expenses the balances sum to %d, want 0", i, s)
			}
		}
	}
	wantBalances(t, l, l.Balances()) // asserts the final sum
}

func TestStage1_NoDriftAfterManyTinyExpenses(t *testing.T) {
	// 0.10 split three ways, 100,000 times. In integer cents the answer is
	// exact: a owes 4 each time, b owes 3, c (the payer) is owed 7. A float64
	// ledger is off by some fraction of a cent here, and "== 0" checks fail.
	l := mustNew(t, "a", "b", "c")
	within(t, "100,000 small expenses", func() {
		for i := 0; i < 100_000; i++ {
			if _, err := l.AddExpense("c", 10, []UserID{"a", "b", "c"}); err != nil {
				t.Errorf("AddExpense = %v", err)
				return
			}
		}
	})
	wantBalances(t, l, map[UserID]Money{"a": -400_000, "b": -300_000, "c": 700_000})
}

func TestStage1_DebtsNetsPairsAndDropsSettledOnes(t *testing.T) {
	l := mustNew(t, "a", "b", "c")
	mustAdd(t, l, "a", 3000, "a", "b", "c") // b owes a 1000, c owes a 1000
	wantDebts(t, l, Transfer{"b", "a", 1000}, Transfer{"c", "a", 1000})

	mustAdd(t, l, "b", 600, "a", "b") // a owes b 300: nets against b's debt to a
	wantDebts(t, l, Transfer{"b", "a", 700}, Transfer{"c", "a", 1000})

	mustAdd(t, l, "c", 1000, "a") // a owes c 1000: exactly cancels c's debt to a
	wantDebts(t, l, Transfer{"b", "a", 700})
}

func TestStage1_DebtsAreSortedAndConsistentWithBalances(t *testing.T) {
	rng := rand.New(rand.NewSource(11))
	users := []UserID{"eve", "bob", "dan", "amy", "cat", "fay"}
	l := mustNew(t, users...)
	for i := 0; i < 300; i++ {
		perm := rng.Perm(len(users))[:2+rng.Intn(4)]
		parts := make([]UserID, len(perm))
		for j, p := range perm {
			parts[j] = users[p]
		}
		mustAdd(t, l, users[rng.Intn(len(users))], Money(1+rng.Intn(9_999)), parts...)
	}
	debts := l.Debts()
	if again := l.Debts(); !reflect.DeepEqual(again, debts) {
		t.Fatal("Debts() is not deterministic: two calls on an unchanged ledger differ (map iteration order leaking?)")
	}
	net := map[UserID]Money{}
	for i, d := range debts {
		if d.Amount <= 0 || d.From == d.To {
			t.Fatalf("Debts()[%d] = %+v: amounts must be positive and between two different users", i, d)
		}
		if i > 0 {
			p := debts[i-1]
			if p.From > d.From || (p.From == d.From && p.To >= d.To) {
				t.Fatalf("Debts() not sorted by (From, To) with unique pairs: %+v before %+v", p, d)
			}
		}
		net[d.From] -= d.Amount
		net[d.To] += d.Amount
	}
	if want := nonZero(l.Balances()); !reflect.DeepEqual(nonZero(net), want) {
		t.Fatalf("the debts imply balances %v, but Balances() = %v: the two views must agree", nonZero(net), want)
	}
	seen := map[[2]UserID]bool{}
	for _, d := range debts {
		k := [2]UserID{min(d.From, d.To), max(d.From, d.To)}
		if seen[k] {
			t.Fatalf("pair %v appears twice in Debts(): opposite debts must be netted into one entry", k)
		}
		seen[k] = true
	}
}

func TestStage1_BalanceDoesNotReplayHistory(t *testing.T) {
	// Reading a balance after every write is the normal access pattern. A
	// design that recomputes it by scanning all expenses visits ~1.8 billion
	// expenses here and misses the time cap; incremental balances take
	// milliseconds.
	const nUsers, nExpenses = 200, 60_000
	users := make([]UserID, nUsers)
	for i := range users {
		users[i] = UserID(fmt.Sprintf("u%03d", i))
	}
	l := mustNew(t, users...)
	within(t, fmt.Sprintf("%d expenses each followed by a Balance read", nExpenses), func() {
		for i := 0; i < nExpenses; i++ {
			p := users[i%nUsers]
			parts := []UserID{users[(i+1)%nUsers], users[(i+7)%nUsers], users[(i+13)%nUsers]}
			if _, err := l.AddExpense(p, Money(100+i%900), parts); err != nil {
				t.Errorf("AddExpense = %v", err)
				return
			}
			if _, err := l.Balance(p); err != nil {
				t.Errorf("Balance = %v", err)
				return
			}
		}
	})
	if s := sum(l.Balances()); s != 0 {
		t.Fatalf("balances sum to %d, want 0", s)
	}
}
