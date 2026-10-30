package vending

import (
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"
)

// manualClock is a clock the test advances by hand.
type manualClock struct {
	mu sync.Mutex
	t  time.Time
}

func newClock() *manualClock {
	return &manualClock{t: time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)}
}

func (c *manualClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *manualClock) Advance(d time.Duration) time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
	return c.t
}

// recorder collects notices and lets a test wait for a given number of them.
type recorder struct {
	mu   sync.Mutex
	got  []Notice
	wake chan struct{}
}

func newRecorder() *recorder { return &recorder{wake: make(chan struct{}, 1000)} }

func (r *recorder) record(n Notice) {
	r.mu.Lock()
	r.got = append(r.got, n)
	r.mu.Unlock()
	r.wake <- struct{}{}
}

func (r *recorder) snapshot() []Notice {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]Notice(nil), r.got...)
}

func (r *recorder) waitFor(t *testing.T, n int) []Notice {
	t.Helper()
	deadline := time.After(testTimeout)
	for {
		if got := r.snapshot(); len(got) >= n {
			return got
		}
		select {
		case <-r.wake:
		case <-deadline:
			t.Fatalf("received %d notices %+v, want %d within %v", len(r.snapshot()), r.snapshot(), n, testTimeout)
		}
	}
}

func inService(t *testing.T, m *Machine) {
	t.Helper()
	if err := m.EnterService(); err != nil {
		t.Fatalf("EnterService() = %v, want nil", err)
	}
}

// ---------------------------------------------------------------- Stage 2

func TestStage2_EnterAndExitServiceOnlyFromTheRightStates(t *testing.T) {
	m := newMachine(t)
	wantErr(t, "ExitService in Idle", m.ExitService(), ErrInvalidState)
	insert(t, m, 25)
	wantErr(t, "EnterService with a customer's money inside", m.EnterService(), ErrInvalidState)
	wantState(t, m, HasMoney)
	if _, err := m.Cancel(); err != nil {
		t.Fatal(err)
	}
	inService(t, m)
	wantState(t, m, OutOfService)
	wantErr(t, "second EnterService", m.EnterService(), ErrInvalidState)
	if err := m.ExitService(); err != nil {
		t.Fatalf("ExitService() = %v", err)
	}
	wantState(t, m, Idle)
}

func TestStage2_OutOfServiceRejectsCustomerEvents(t *testing.T) {
	m := newMachine(t)
	inService(t, m)
	wantErr(t, "InsertCoin out of service", m.InsertCoin(25), ErrInvalidState)
	_, err := m.Select("A1")
	wantErr(t, "Select out of service", err, ErrInvalidState)
	_, err = m.Cancel()
	wantErr(t, "Cancel out of service", err, ErrInvalidState)
	if s := m.Snapshot(); s.State != OutOfService || s.Balance != 0 {
		t.Fatalf("Snapshot (a query) must work in every state; got %+v", s)
	}
}

func TestStage2_AdminOperations(t *testing.T) {
	m := newMachine(t)
	inService(t, m)

	if err := m.Restock("A2", 5); err != nil {
		t.Fatalf("Restock = %v", err)
	}
	if err := m.SetPrice("A2", 50); err != nil {
		t.Fatalf("SetPrice = %v", err)
	}
	if err := m.LoadCoins(25, 4); err != nil {
		t.Fatalf("LoadCoins = %v", err)
	}
	s := m.Snapshot()
	if s.Slots["A2"].Qty != 7 || s.Slots["A2"].Price != 50 || s.Box[25] != 14 {
		t.Fatalf("after admin ops Snapshot = %+v, want A2 qty 7 price 50 and 14 quarters", s)
	}

	// CollectCash leaves a float of `keep` coins per denomination.
	taken, err := m.CollectCash(4)
	if err != nil {
		t.Fatalf("CollectCash = %v", err)
	}
	if want := (map[int]int{5: 6, 10: 6, 25: 10, 100: 1}); !reflect.DeepEqual(taken, want) {
		t.Fatalf("CollectCash(4) returned %v, want %v", taken, want)
	}
	if want := (map[int]int{5: 4, 10: 4, 25: 4, 100: 4}); !reflect.DeepEqual(m.Snapshot().Box, want) {
		t.Fatalf("Box after CollectCash = %v, want %v", m.Snapshot().Box, want)
	}

	// Validation.
	wantErr(t, "Restock unknown slot", m.Restock("Z9", 1), ErrUnknownSlot)
	wantErr(t, "Restock zero", m.Restock("A1", 0), ErrInvalidArgument)
	wantErr(t, "SetPrice negative", m.SetPrice("A1", -1), ErrInvalidArgument)
	wantErr(t, "SetPrice unknown slot", m.SetPrice("Z9", 5), ErrUnknownSlot)
	wantErr(t, "LoadCoins bad denomination", m.LoadCoins(7, 1), ErrBadCoin)
	wantErr(t, "LoadCoins zero", m.LoadCoins(5, 0), ErrInvalidArgument)
	_, err = m.CollectCash(-1)
	wantErr(t, "CollectCash negative float", err, ErrInvalidArgument)

	// The new price is live after returning to service.
	if err := m.ExitService(); err != nil {
		t.Fatal(err)
	}
	insert(t, m, 25, 25)
	if _, err := m.Select("A2"); err != nil {
		t.Fatalf("Select(A2) at the new price 50 = %v", err)
	}
}

func TestStage2_AdminOperationsOutsideMaintenanceAreInvalid(t *testing.T) {
	m := newMachine(t)
	for _, state := range []string{"Idle", "HasMoney"} {
		if state == "HasMoney" {
			insert(t, m, 25)
		}
		wantErr(t, state+": Restock", m.Restock("A1", 1), ErrInvalidState)
		wantErr(t, state+": SetPrice", m.SetPrice("A1", 5), ErrInvalidState)
		wantErr(t, state+": LoadCoins", m.LoadCoins(5, 1), ErrInvalidState)
		_, err := m.CollectCash(0)
		wantErr(t, state+": CollectCash", err, ErrInvalidState)
	}
	if s := m.Snapshot(); s.Slots["A1"].Qty != 3 || s.Box[5] != 10 {
		t.Fatalf("a rejected admin operation changed the machine: %+v", s)
	}
}

func TestStage2_IdleTimeoutRefundsTheSession(t *testing.T) {
	clk := newClock()
	m := newMachine(t, WithClock(clk.Now), WithIdleTimeout(30*time.Second))
	insert(t, m, 25, 100)

	clk.Advance(29 * time.Second)
	if refund, err := m.Tick(clk.Now()); err != nil || refund != nil {
		t.Fatalf("Tick at 29s = %v, %v; want nothing yet", refund, err)
	}
	wantState(t, m, HasMoney)

	clk.Advance(time.Second)
	refund, err := m.Tick(clk.Now())
	if err != nil || !reflect.DeepEqual(refund, map[int]int{25: 1, 100: 1}) {
		t.Fatalf("Tick at 30s = %v, %v; want the inserted coins {25:1 100:1} back", refund, err)
	}
	wantState(t, m, Idle)
	if m.Balance() != 0 {
		t.Fatalf("Balance() = %d after the timeout refund", m.Balance())
	}
	if refund, err := m.Tick(clk.Now()); err != nil || refund != nil {
		t.Fatalf("Tick in Idle = %v, %v; want (nil, nil): Tick is valid in every state", refund, err)
	}
}

func TestStage2_EachCoinRestartsTheIdleTimer(t *testing.T) {
	clk := newClock()
	m := newMachine(t, WithClock(clk.Now), WithIdleTimeout(30*time.Second))
	insert(t, m, 25)
	clk.Advance(20 * time.Second)
	insert(t, m, 25)              // activity at t=20s
	clk.Advance(20 * time.Second) // t=40s: 40s since the first coin, 20s since the last
	if refund, _ := m.Tick(clk.Now()); refund != nil {
		t.Fatalf("Tick refunded %v 20s after the last coin; the timeout is measured from the latest activity", refund)
	}
	clk.Advance(10 * time.Second) // 30s since the last coin
	if refund, _ := m.Tick(clk.Now()); sum(refund) != 50 {
		t.Fatalf("Tick refunded %v, want both quarters", refund)
	}
}

func TestStage2_NoTimeoutConfiguredNeverRefunds(t *testing.T) {
	clk := newClock()
	m := newMachine(t, WithClock(clk.Now))
	insert(t, m, 25)
	clk.Advance(100 * time.Hour)
	if refund, err := m.Tick(clk.Now()); err != nil || refund != nil {
		t.Fatalf("Tick = %v, %v; a zero idle timeout means disabled", refund, err)
	}
	inService := newMachine(t)
	if err := inService.EnterService(); err != nil {
		t.Fatal(err)
	}
	if _, err := inService.Tick(time.Now()); err != nil {
		t.Fatalf("Tick OutOfService = %v, want nil", err)
	}
}

func TestStage2_NoticesForAPurchase(t *testing.T) {
	m := newMachine(t, WithLowStockAlert(1))
	rec := newRecorder()
	m.Subscribe(rec.record)
	insert(t, m, 25, 25, 25)
	if _, err := m.Select("A2"); err != nil { // leaves 1 in stock: low-stock alert
		t.Fatal(err)
	}
	got := rec.waitFor(t, 5)
	want := []Notice{
		{Kind: NoticeStateChanged, From: Idle, To: HasMoney},
		{Kind: NoticeStateChanged, From: HasMoney, To: Dispensing},
		{Kind: NoticeDispensed, Slot: "A2", Amount: 75},
		{Kind: NoticeLowStock, Slot: "A2", Qty: 1},
		{Kind: NoticeStateChanged, From: Dispensing, To: Idle},
	}
	if !reflect.DeepEqual(got[:5], want) {
		t.Fatalf("notices =\n  %+v\nwant\n  %+v\n(effects are announced in the order they happen; "+
			"a coin while already HasMoney is not a state change)", got, want)
	}
}

func TestStage2_NoticesForCancelAndTimeout(t *testing.T) {
	clk := newClock()
	m := newMachine(t, WithClock(clk.Now), WithIdleTimeout(time.Minute))
	rec := newRecorder()
	m.Subscribe(rec.record)
	insert(t, m, 25)
	if _, err := m.Cancel(); err != nil {
		t.Fatal(err)
	}
	insert(t, m, 10)
	clk.Advance(time.Minute)
	if _, err := m.Tick(clk.Now()); err != nil {
		t.Fatal(err)
	}
	got := rec.waitFor(t, 6)
	want := []Notice{
		{Kind: NoticeStateChanged, From: Idle, To: HasMoney},
		{Kind: NoticeRefunded, Amount: 25, Reason: "cancel"},
		{Kind: NoticeStateChanged, From: HasMoney, To: Idle},
		{Kind: NoticeStateChanged, From: Idle, To: HasMoney},
		{Kind: NoticeRefunded, Amount: 10, Reason: "timeout"},
		{Kind: NoticeStateChanged, From: HasMoney, To: Idle},
	}
	if !reflect.DeepEqual(got[:6], want) {
		t.Fatalf("notices =\n  %+v\nwant\n  %+v", got, want)
	}
}

func TestStage2_APanickingListenerDoesNotBreakTheMachineOrOtherListeners(t *testing.T) {
	m := newMachine(t)
	m.Subscribe(func(Notice) { panic("listener bug") })
	rec := newRecorder()
	m.Subscribe(rec.record)
	within(t, "operations with a panicking listener", func() {
		if err := m.InsertCoin(25); err != nil {
			t.Errorf("InsertCoin = %v", err)
		}
		if _, err := m.Cancel(); err != nil {
			t.Errorf("Cancel = %v", err)
		}
	})
	rec.waitFor(t, 3)
	wantState(t, m, Idle)
	insert(t, m, 10) // still fully functional
	if m.Balance() != 10 {
		t.Fatalf("Balance() = %d, want 10", m.Balance())
	}
}

func TestStage2_ListenersMayCallBackIntoTheMachine(t *testing.T) {
	m := newMachine(t)
	seen := make(chan State, 10)
	m.Subscribe(func(n Notice) {
		if n.Kind == NoticeStateChanged {
			seen <- m.State() // deadlocks if notices are delivered under the machine's lock
			_ = m.Balance()
			_ = m.Snapshot()
		}
	})
	within(t, "a listener that queries the machine", func() { insert(t, m, 25) })
	select {
	case <-seen:
	case <-time.After(testTimeout):
		t.Fatal("listener never ran")
	}
}

func TestStage2_ASlowListenerDoesNotBlockTheMachine(t *testing.T) {
	m := newMachine(t)
	gate := make(chan struct{})
	entered := make(chan struct{}, 1)
	rec := newRecorder()
	var once sync.Once
	m.Subscribe(func(n Notice) {
		once.Do(func() {
			entered <- struct{}{}
			<-gate // the first notice is delivered slowly
		})
		rec.record(n)
	})

	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); _ = m.InsertCoin(25) }()
	select {
	case <-entered:
	case <-time.After(testTimeout):
		t.Fatal("listener never ran")
	}
	// The listener is stuck. The machine must still serve other callers.
	within(t, "Balance while a listener is stuck", func() {
		if m.Balance() != 25 {
			t.Errorf("Balance() = %d, want 25", m.Balance())
		}
	})
	within(t, "Cancel while a listener is stuck", func() {
		if _, err := m.Cancel(); err != nil {
			t.Errorf("Cancel = %v", err)
		}
	})
	close(gate)
	wg.Wait()
	got := rec.waitFor(t, 3)
	wantKinds := []NoticeKind{NoticeStateChanged, NoticeRefunded, NoticeStateChanged}
	for i, k := range wantKinds {
		if got[i].Kind != k {
			t.Fatalf("notice %d = %+v, want kind %v: delivery must preserve order", i, got[i], k)
		}
	}
}

func TestStage2_NoticesArriveInOrderAndUnsubscribeStopsThem(t *testing.T) {
	m := newMachine(t)
	rec := newRecorder()
	cancel := m.Subscribe(rec.record)
	for i := 0; i < 40; i++ {
		insert(t, m, 25)
		if _, err := m.Cancel(); err != nil {
			t.Fatal(err)
		}
	}
	got := rec.waitFor(t, 120)
	for i := 0; i < 120; i += 3 {
		if got[i].Kind != NoticeStateChanged || got[i].To != HasMoney ||
			got[i+1].Kind != NoticeRefunded || got[i+2].Kind != NoticeStateChanged || got[i+2].To != Idle {
			t.Fatalf("notices %d..%d = %+v: out of order", i, i+2, got[i:i+3])
		}
	}
	cancel()
	insert(t, m, 10)
	time.Sleep(20 * time.Millisecond) // negative assertion: give a stray delivery a chance to appear
	if n := len(rec.snapshot()); n != 120 {
		t.Fatalf("%d notices after unsubscribing, want the original 120", n)
	}
	if !errors.Is(m.InsertCoin(3), ErrBadCoin) {
		t.Fatal("machine broken after unsubscribe")
	}
}
