package vending

import (
	"errors"
	"math/rand"
	"reflect"
	"testing"
	"time"
)

const testTimeout = 5 * time.Second

// within fails the test if fn does not return in time, turning a deadlock or a
// lock held across a slow call into a message instead of a hang.
func within(t *testing.T, what string, fn func()) {
	t.Helper()
	done := make(chan struct{})
	go func() { defer close(done); fn() }()
	select {
	case <-done:
	case <-time.After(testTimeout):
		t.Fatalf("%s did not finish within %v (a lock held while waiting on something slow, or a deadlock?)", what, testTimeout)
	}
}

func baseConfig() Config {
	return Config{
		Denominations: []int{5, 10, 25, 100},
		Slots: map[string]Product{
			"A1": {Name: "Cola", Price: 125, Qty: 3},
			"A2": {Name: "Chips", Price: 75, Qty: 2},
			"B1": {Name: "Gum", Price: 35, Qty: 1},
			"B2": {Name: "Water", Price: 100, Qty: 0}, // sold out
		},
		Coins: map[int]int{5: 10, 10: 10, 25: 10, 100: 5},
	}
}

func newMachine(t *testing.T, opts ...Option) *Machine {
	t.Helper()
	m, err := New(baseConfig(), opts...)
	if err != nil {
		t.Fatalf("New(baseConfig) = %v, want nil", err)
	}
	if m == nil {
		t.Fatal("New returned a nil *Machine with a nil error")
	}
	return m
}

func insert(t *testing.T, m *Machine, coins ...int) {
	t.Helper()
	for _, c := range coins {
		if err := m.InsertCoin(c); err != nil {
			t.Fatalf("InsertCoin(%d) = %v, want nil", c, err)
		}
	}
}

func sum(coins map[int]int) int {
	total := 0
	for d, n := range coins {
		total += d * n
	}
	return total
}

func wantErr(t *testing.T, what string, got, want error) {
	t.Helper()
	if !errors.Is(got, want) {
		t.Fatalf("%s = %v, want an error satisfying errors.Is(err, %v)", what, got, want)
	}
}

func wantState(t *testing.T, m *Machine, want State) {
	t.Helper()
	if got := m.State(); got != want {
		t.Fatalf("State() = %v, want %v", got, want)
	}
}

// ---------------------------------------------------------------- Stage 1

func TestStage1_NewRejectsBadConfig(t *testing.T) {
	bad := map[string]func(*Config){
		"no denominations":       func(c *Config) { c.Denominations = nil },
		"zero denomination":      func(c *Config) { c.Denominations = []int{0, 5} },
		"duplicate denomination": func(c *Config) { c.Denominations = []int{5, 5} },
		"zero price":             func(c *Config) { c.Slots["A1"] = Product{Name: "x", Price: 0, Qty: 1} },
		"negative quantity":      func(c *Config) { c.Slots["A1"] = Product{Name: "x", Price: 5, Qty: -1} },
		"coins of unknown denom": func(c *Config) { c.Coins[7] = 3 },
		"negative coin stock":    func(c *Config) { c.Coins[5] = -1 },
	}
	for name, mutate := range bad {
		cfg := baseConfig()
		mutate(&cfg)
		if _, err := New(cfg); !errors.Is(err, ErrBadConfig) {
			t.Errorf("New with %s = %v, want ErrBadConfig", name, err)
		}
	}
}

func TestStage1_InitialSnapshotIsACopy(t *testing.T) {
	m := newMachine(t)
	s := m.Snapshot()
	if s.State != Idle || s.Balance != 0 || len(s.Escrow) != 0 {
		t.Fatalf("initial Snapshot = %+v, want Idle with no balance and no escrow", s)
	}
	if s.Slots["A1"] != (Product{Name: "Cola", Price: 125, Qty: 3}) {
		t.Fatalf("Snapshot.Slots[A1] = %+v", s.Slots["A1"])
	}
	if want := (map[int]int{5: 10, 10: 10, 25: 10, 100: 5}); !reflect.DeepEqual(s.Box, want) {
		t.Fatalf("Snapshot.Box = %v, want %v", s.Box, want)
	}
	s.Slots["A1"] = Product{}
	s.Box[5] = 0
	if again := m.Snapshot(); again.Slots["A1"].Qty != 3 || again.Box[5] != 10 {
		t.Fatalf("mutating a Snapshot changed the machine: %+v", again)
	}

	// New must also copy the Config it was given.
	cfg := baseConfig()
	m2, _ := New(cfg)
	cfg.Slots["A1"] = Product{Name: "Changed", Price: 1, Qty: 99}
	cfg.Coins[5] = 0
	if got := m2.Snapshot(); got.Slots["A1"].Name != "Cola" || got.Box[5] != 10 {
		t.Fatalf("New kept references into the caller's Config: %+v", got)
	}
}

func TestStage1_InsertCoinMovesIdleToHasMoney(t *testing.T) {
	m := newMachine(t)
	wantState(t, m, Idle)
	insert(t, m, 25)
	wantState(t, m, HasMoney)
	insert(t, m, 100, 5)
	if got := m.Balance(); got != 130 {
		t.Fatalf("Balance() = %d, want 130", got)
	}
	if want := (map[int]int{25: 1, 100: 1, 5: 1}); !reflect.DeepEqual(m.Snapshot().Escrow, want) {
		t.Fatalf("Escrow = %v, want %v", m.Snapshot().Escrow, want)
	}
	if m.Snapshot().Box[25] != 10 {
		t.Fatalf("inserted coins must stay in escrow (not the change box) until the sale completes; Box = %v", m.Snapshot().Box)
	}
}

func TestStage1_UnacceptedCoinsAreRejectedAndChangeNothing(t *testing.T) {
	m := newMachine(t)
	for _, c := range []int{0, -5, 7, 50, 1000} {
		wantErr(t, "InsertCoin", m.InsertCoin(c), ErrBadCoin)
	}
	wantState(t, m, Idle)
	insert(t, m, 10)
	wantErr(t, "InsertCoin(3) while HasMoney", m.InsertCoin(3), ErrBadCoin)
	if m.Balance() != 10 {
		t.Fatalf("Balance() = %d after a rejected coin, want 10", m.Balance())
	}
	wantState(t, m, HasMoney)
}

func TestStage1_ExactPaymentDispensesWithoutChange(t *testing.T) {
	m := newMachine(t)
	insert(t, m, 25, 25, 25)
	r, err := m.Select("A2")
	if err != nil {
		t.Fatalf("Select(A2) = %v, want nil", err)
	}
	if r.Slot != "A2" || r.Product != "Chips" || r.Price != 75 || sum(r.Change) != 0 {
		t.Fatalf("Receipt = %+v, want Chips for 75 with no change", r)
	}
	wantState(t, m, Idle)
	s := m.Snapshot()
	if s.Balance != 0 || len(s.Escrow) != 0 {
		t.Fatalf("session not cleared after the sale: %+v", s)
	}
	if s.Slots["A2"].Qty != 1 {
		t.Fatalf("A2 Qty = %d, want 1 after one sale", s.Slots["A2"].Qty)
	}
	if s.Box[25] != 13 {
		t.Fatalf("Box[25] = %d, want 13: the three inserted quarters now belong to the machine", s.Box[25])
	}
}

func TestStage1_ChangeIsExactAndTakenFromAvailableCoins(t *testing.T) {
	m := newMachine(t)
	before := m.Snapshot().Box
	inserted := map[int]int{100: 2}
	insert(t, m, 100, 100) // 200 for a 125 item: 75 change
	r, err := m.Select("A1")
	if err != nil {
		t.Fatalf("Select(A1) = %v, want nil", err)
	}
	if sum(r.Change) != 75 {
		t.Fatalf("change = %v (sum %d), want exactly 75", r.Change, sum(r.Change))
	}
	after := m.Snapshot().Box
	for d, n := range r.Change {
		if n < 0 || d <= 0 {
			t.Fatalf("nonsense change entry %d x %d", n, d)
		}
		if n > before[d]+inserted[d] {
			t.Fatalf("change contains %d x %d but the machine only had %d", n, d, before[d])
		}
	}
	// Conservation: box after = box before + inserted - change.
	for _, d := range []int{5, 10, 25, 100} {
		want := before[d] + inserted[d] - r.Change[d]
		if after[d] != want {
			t.Fatalf("Box[%d] = %d, want %d (before + inserted - change)", d, after[d], want)
		}
	}
}

func TestStage1_FailedSelectsLeaveTheSessionIntact(t *testing.T) {
	m := newMachine(t)
	insert(t, m, 100, 5, 5) // 110
	cases := []struct {
		code string
		want error
	}{
		{"Z9", ErrUnknownSlot},
		{"B2", ErrSoldOut},           // sold out wins over insufficient funds
		{"A1", ErrInsufficientFunds}, // 125 > 110
	}
	for _, c := range cases {
		_, err := m.Select(c.code)
		wantErr(t, "Select("+c.code+")", err, c.want)
		wantState(t, m, HasMoney)
		if m.Balance() != 110 {
			t.Fatalf("Balance() = %d after failed Select(%s), want 110", m.Balance(), c.code)
		}
	}
	if m.Snapshot().Slots["A1"].Qty != 3 || m.Snapshot().Slots["B1"].Qty != 1 {
		t.Fatalf("a failed Select changed stock: %+v", m.Snapshot().Slots)
	}
	refund, err := m.Cancel()
	if err != nil || !reflect.DeepEqual(refund, map[int]int{100: 1, 5: 2}) {
		t.Fatalf("Cancel() = %v, %v; want exactly the inserted coins {100:1 5:2}", refund, err)
	}
	if _, err := m.Select("B1"); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("Select after Cancel = %v, want ErrInvalidState (back in Idle)", err)
	}
}

func TestStage1_CancelRefundsExactlyTheInsertedCoins(t *testing.T) {
	m := newMachine(t)
	boxBefore := m.Snapshot().Box
	insert(t, m, 100, 5, 25, 5)
	refund, err := m.Cancel()
	if err != nil {
		t.Fatalf("Cancel() = %v", err)
	}
	if want := (map[int]int{100: 1, 5: 2, 25: 1}); !reflect.DeepEqual(refund, want) {
		t.Fatalf("Cancel() refunded %v, want %v: the same coins, not a re-made equivalent", refund, want)
	}
	wantState(t, m, Idle)
	if m.Balance() != 0 || !reflect.DeepEqual(m.Snapshot().Box, boxBefore) {
		t.Fatalf("after Cancel: balance %d, box %v (was %v); the change box must be untouched", m.Balance(), m.Snapshot().Box, boxBefore)
	}
}

func TestStage1_EventsInTheWrongStateAreErrorsNotNoOps(t *testing.T) {
	m := newMachine(t)
	_, err := m.Select("A1")
	wantErr(t, "Select in Idle", err, ErrInvalidState)
	_, err = m.Cancel()
	wantErr(t, "Cancel in Idle", err, ErrInvalidState)
	wantState(t, m, Idle)
	if s := m.Snapshot(); s.Balance != 0 || s.Slots["A1"].Qty != 3 {
		t.Fatalf("a rejected event changed the machine: %+v", s)
	}
}

func TestStage1_GreedyChangeFailsButExactChangeExists(t *testing.T) {
	// 30 cents of change from {one quarter, three dimes}: greedy takes the
	// quarter and is stuck needing a nickel; three dimes is the answer.
	cfg := Config{
		Denominations: []int{5, 10, 25, 100},
		Slots:         map[string]Product{"X": {Name: "Candy", Price: 70, Qty: 5}},
		Coins:         map[int]int{25: 1, 10: 3},
	}
	m, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	insert(t, m, 100)
	r, err := m.Select("X")
	if err != nil {
		t.Fatalf("Select = %v; exact change {10:3} exists, so the sale must go through (greedy is not enough)", err)
	}
	if !reflect.DeepEqual(r.Change, map[int]int{10: 3}) {
		t.Fatalf("change = %v, want {10:3}", r.Change)
	}
}

func TestStage1_NoExactChangeIsRejectedBeforeDispensing(t *testing.T) {
	cfg := Config{
		Denominations: []int{5, 10, 25, 100},
		Slots:         map[string]Product{"X": {Name: "Candy", Price: 85, Qty: 5}},
		Coins:         map[int]int{10: 3}, // no nickels: 15 cents is impossible
	}
	m, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	insert(t, m, 100)
	_, err = m.Select("X")
	wantErr(t, "Select needing impossible change", err, ErrNoChange)
	wantState(t, m, HasMoney)
	if m.Balance() != 100 || m.Snapshot().Slots["X"].Qty != 5 {
		t.Fatalf("a rejected sale must not dispense or take money: %+v", m.Snapshot())
	}

	// Adding a nickel makes 20 cents of change possible (two dimes).
	insert(t, m, 5)
	r, err := m.Select("X")
	if err != nil {
		t.Fatalf("Select after inserting a nickel = %v, want success (change 20 = 2 dimes)", err)
	}
	if !reflect.DeepEqual(r.Change, map[int]int{10: 2}) {
		t.Fatalf("change = %v, want {10:2}", r.Change)
	}

	// And the refund path: reject, then Cancel returns the original coin.
	m2, _ := New(cfg)
	insert(t, m2, 100)
	_, _ = m2.Select("X")
	refund, err := m2.Cancel()
	if err != nil || !reflect.DeepEqual(refund, map[int]int{100: 1}) {
		t.Fatalf("Cancel after ErrNoChange = %v, %v; want the 100-cent coin back", refund, err)
	}
}

func TestStage1_InsertedCoinsCanBeHandedBackAsChange(t *testing.T) {
	cfg := Config{
		Denominations: []int{5, 10, 25, 100},
		Slots:         map[string]Product{"A2": {Name: "Chips", Price: 75, Qty: 2}},
		Coins:         map[int]int{}, // an empty change box
	}
	m, _ := New(cfg)
	insert(t, m, 25, 25, 25, 25) // 100 for 75: change is one of the customer's own quarters
	r, err := m.Select("A2")
	if err != nil {
		t.Fatalf("Select = %v, want success: inserted coins are part of the change pool", err)
	}
	if !reflect.DeepEqual(r.Change, map[int]int{25: 1}) {
		t.Fatalf("change = %v, want {25:1}", r.Change)
	}
	if got := m.Snapshot().Box; !reflect.DeepEqual(got, map[int]int{25: 3}) {
		t.Fatalf("Box = %v, want {25:3}", got)
	}
}

// bruteForceFeasible reports whether amount can be formed from the pool.
func bruteForceFeasible(amount int, denoms []int, pool map[int]int) bool {
	var rec func(i, left int) bool
	rec = func(i, left int) bool {
		if i == len(denoms) {
			return left == 0
		}
		d := denoms[i]
		for k := 0; k <= pool[d] && k*d <= left; k++ {
			if rec(i+1, left-k*d) {
				return true
			}
		}
		return false
	}
	return rec(0, amount)
}

func TestStage1_ChangeMakingAgreesWithBruteForce(t *testing.T) {
	denoms := []int{5, 10, 25, 100}
	r := rand.New(rand.NewSource(42))
	feasible, infeasible := 0, 0
	for i := 0; i < 400; i++ {
		box := map[int]int{}
		for _, d := range denoms {
			box[d] = r.Intn(4)
		}
		price := 5 * (1 + r.Intn(30))
		cfg := Config{Denominations: denoms, Slots: map[string]Product{"X": {Name: "x", Price: price, Qty: 1}}, Coins: box}
		m, err := New(cfg)
		if err != nil {
			t.Fatal(err)
		}
		inserted := map[int]int{}
		for len(inserted) == 0 || sum(inserted) < price {
			c := denoms[r.Intn(len(denoms))]
			insert(t, m, c)
			inserted[c]++
		}
		pool := map[int]int{}
		for _, d := range denoms {
			pool[d] = box[d] + inserted[d]
		}
		amount := sum(inserted) - price
		want := bruteForceFeasible(amount, denoms, pool)
		rec, err := m.Select("X")
		switch {
		case want && err != nil:
			t.Fatalf("case %d: box %v inserted %v price %d: change %d is possible but Select = %v", i, box, inserted, price, amount, err)
		case !want && !errors.Is(err, ErrNoChange):
			t.Fatalf("case %d: box %v inserted %v price %d: change %d is impossible but Select = %v, want ErrNoChange", i, box, inserted, price, amount, err)
		case want:
			feasible++
			if sum(rec.Change) != amount {
				t.Fatalf("case %d: change %v sums to %d, want %d", i, rec.Change, sum(rec.Change), amount)
			}
			for d, n := range rec.Change {
				if n > pool[d] {
					t.Fatalf("case %d: change uses %d x %d but only %d exist", i, n, d, pool[d])
				}
			}
		default:
			infeasible++
		}
	}
	if feasible < 50 || infeasible < 50 {
		t.Fatalf("test generator is unbalanced (%d feasible, %d infeasible); the seed or ranges changed", feasible, infeasible)
	}
}
