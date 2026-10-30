package vending

import (
	"errors"
	"fmt"
	"math/rand"
	"runtime"
	"sync"
	"testing"
	"time"
)

// fakeDispenser records calls, can fail, and can block until released.
type fakeDispenser struct {
	mu      sync.Mutex
	calls   []string
	err     error
	gate    chan struct{} // non-nil: Dispense blocks until it is closed
	entered chan string   // non-nil: receives the slot when Dispense is entered
	yield   bool
}

func (d *fakeDispenser) Dispense(slot string) error {
	d.mu.Lock()
	d.calls = append(d.calls, slot)
	err, gate, entered := d.err, d.gate, d.entered
	d.mu.Unlock()
	if entered != nil {
		entered <- slot
	}
	if gate != nil {
		<-gate
	}
	if d.yield {
		runtime.Gosched()
	}
	return err
}

func (d *fakeDispenser) count() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.calls)
}

func (d *fakeDispenser) setErr(err error) {
	d.mu.Lock()
	d.err = err
	d.mu.Unlock()
}

// fakeGateway counts settlement calls per authorisation.
type fakeGateway struct {
	mu         sync.Mutex
	captures   map[string]int
	voids      map[string]int
	captureErr error
}

func newGateway() *fakeGateway {
	return &fakeGateway{captures: map[string]int{}, voids: map[string]int{}}
}

func (g *fakeGateway) Capture(auth string) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.captures[auth]++
	return g.captureErr
}

func (g *fakeGateway) Void(auth string) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.voids[auth]++
	return nil
}

func (g *fakeGateway) counts(auth string) (captures, voids int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.captures[auth], g.voids[auth]
}

// waitMatch waits until a notice satisfying pred has been recorded.
func (r *recorder) waitMatch(t *testing.T, what string, pred func(Notice) bool) {
	t.Helper()
	deadline := time.After(testTimeout)
	for {
		for _, n := range r.snapshot() {
			if pred(n) {
				return
			}
		}
		select {
		case <-r.wake:
		case <-deadline:
			t.Fatalf("never saw %s; notices so far: %+v", what, r.snapshot())
		}
	}
}

func becameIdle(n Notice) bool { return n.Kind == NoticeStateChanged && n.To == Idle }

func mustSale(t *testing.T, m *Machine, code string) SaleInfo {
	t.Helper()
	s, err := m.PayByCard(code)
	if err != nil {
		t.Fatalf("PayByCard(%s) = %v, want nil", code, err)
	}
	return s
}

func saleStatus(t *testing.T, m *Machine, id SaleID) SaleStatus {
	t.Helper()
	s, ok := m.Sale(id)
	if !ok {
		t.Fatalf("Sale(%s) not found", id)
	}
	return s.Status
}

// ---------------------------------------------------------------- Stage 3

// TestStage3_EveryStateEventPairMatchesTheTransitionTable drives all 5 states
// x 8 events and checks that exactly the legal pairs are accepted. This is the
// transition table as a test.
func TestStage3_EveryStateEventPairMatchesTheTransitionTable(t *testing.T) {
	type event struct {
		name    string
		run     func(m *Machine) error
		allowed map[State]bool
	}
	events := []event{
		{"InsertCoin", func(m *Machine) error { return m.InsertCoin(25) }, map[State]bool{Idle: true, HasMoney: true}},
		{"Select", func(m *Machine) error { _, err := m.Select("A1"); return err }, map[State]bool{HasMoney: true}},
		{"Cancel", func(m *Machine) error { _, err := m.Cancel(); return err }, map[State]bool{HasMoney: true}},
		{"EnterService", func(m *Machine) error { return m.EnterService() }, map[State]bool{Idle: true}},
		{"ExitService", func(m *Machine) error { return m.ExitService() }, map[State]bool{OutOfService: true}},
		{"Restock", func(m *Machine) error { return m.Restock("A1", 1) }, map[State]bool{OutOfService: true}},
		{"PayByCard", func(m *Machine) error { _, err := m.PayByCard("A1"); return err }, map[State]bool{Idle: true}},
		{"Tick", func(m *Machine) error { _, err := m.Tick(time.Now()); return err },
			map[State]bool{Idle: true, HasMoney: true, Dispensing: true, OutOfService: true, AwaitingCard: true}},
	}
	// build returns a machine in the given state and a cleanup that lets
	// anything blocked inside the machine finish.
	build := func(t *testing.T, s State) (*Machine, func()) {
		fd := &fakeDispenser{}
		m := newMachine(t, WithDispenser(fd))
		switch s {
		case Idle:
		case HasMoney:
			insert(t, m, 100, 25)
		case OutOfService:
			inService(t, m)
		case AwaitingCard:
			mustSale(t, m, "A1")
		case Dispensing:
			fd.gate, fd.entered = make(chan struct{}), make(chan string, 1)
			insert(t, m, 100, 25)
			done := make(chan struct{})
			go func() { defer close(done); _, _ = m.Select("A1") }()
			select {
			case <-fd.entered:
			case <-time.After(testTimeout):
				t.Fatal("Select never reached the dispenser")
			}
			return m, func() { close(fd.gate); <-done }
		}
		return m, func() {}
	}
	for _, s := range []State{Idle, HasMoney, Dispensing, AwaitingCard, OutOfService} {
		for _, ev := range events {
			m, cleanup := build(t, s)
			var err error
			within(t, fmt.Sprintf("%s in %v", ev.name, s), func() { err = ev.run(m) })
			switch {
			case ev.allowed[s] && err != nil:
				t.Errorf("%s in %v = %v, want nil: the event is legal there", ev.name, s, err)
			case !ev.allowed[s] && !errors.Is(err, ErrInvalidState):
				t.Errorf("%s in %v = %v, want ErrInvalidState: the event is illegal there", ev.name, s, err)
			}
			if !ev.allowed[s] {
				if got := m.State(); got != s {
					t.Errorf("rejected %s moved the machine from %v to %v", ev.name, s, got)
				}
			}
			cleanup()
		}
	}
}

func TestStage3_DispenserFailureNeverTakesTheCustomersMoney(t *testing.T) {
	fd := &fakeDispenser{err: errors.New("motor jammed")}
	m := newMachine(t, WithDispenser(fd))
	rec := newRecorder()
	m.Subscribe(rec.record)
	insert(t, m, 100, 100)
	box := m.Snapshot().Box

	_, err := m.Select("A1")
	wantErr(t, "Select with a jammed motor", err, ErrDispenseFailed)
	wantState(t, m, HasMoney)
	s := m.Snapshot()
	if s.Balance != 200 || s.Slots["A1"].Qty != 3 {
		t.Fatalf("after a failed dispense balance=%d qty=%d, want 200 and 3 (stock reservation released, money kept in escrow)", s.Balance, s.Slots["A1"].Qty)
	}
	for d, n := range box {
		if s.Box[d] != n {
			t.Fatalf("change box changed after a failed dispense: %v -> %v", box, s.Box)
		}
	}
	rec.waitMatch(t, "a DispenseFailed notice", func(n Notice) bool { return n.Kind == NoticeDispenseFailed && n.Slot == "A1" })

	fd.setErr(nil) // the technician fixed the motor; the same session can retry
	r, err := m.Select("A1")
	if err != nil || sum(r.Change) != 75 {
		t.Fatalf("retry Select = %+v, %v; want success with 75 change", r, err)
	}
	if fd.count() != 2 {
		t.Fatalf("dispenser called %d times, want 2 (one failed, one good)", fd.count())
	}
}

func TestStage3_WhileTheMotorRunsOtherEventsFailFastInsteadOfBlocking(t *testing.T) {
	fd := &fakeDispenser{gate: make(chan struct{}), entered: make(chan string, 1)}
	m := newMachine(t, WithDispenser(fd))
	insert(t, m, 100, 25)

	type result struct {
		r   Receipt
		err error
	}
	done := make(chan result, 1)
	go func() { r, err := m.Select("A1"); done <- result{r, err} }()
	select {
	case <-fd.entered:
	case <-time.After(testTimeout):
		t.Fatal("Select never reached the dispenser")
	}

	within(t, "events while the dispenser is running", func() {
		wantState(t, m, Dispensing)
		wantErr(t, "InsertCoin while dispensing", m.InsertCoin(5), ErrInvalidState)
		_, err := m.Cancel()
		wantErr(t, "Cancel while dispensing", err, ErrInvalidState)
		_, err = m.Select("A2")
		wantErr(t, "second Select while dispensing", err, ErrInvalidState)
		wantErr(t, "EnterService while dispensing", m.EnterService(), ErrInvalidState)
		if q := m.Snapshot().Slots["A1"].Qty; q != 2 {
			t.Errorf("A1 Qty = %d while its item is being dispensed, want 2 (reserved)", q)
		}
	})

	close(fd.gate)
	select {
	case res := <-done:
		if res.err != nil || res.r.Product != "Cola" {
			t.Fatalf("Select = %+v, %v", res.r, res.err)
		}
	case <-time.After(testTimeout):
		t.Fatal("Select did not finish after the motor was released")
	}
	wantState(t, m, Idle)
}

func TestStage3_CardSaleHappyPath(t *testing.T) {
	fd, gw := &fakeDispenser{}, newGateway()
	m := newMachine(t, WithDispenser(fd), WithGateway(gw))
	rec := newRecorder()
	m.Subscribe(rec.record)
	box := m.Snapshot().Box

	_, err := m.PayByCard("Z9")
	wantErr(t, "PayByCard unknown slot", err, ErrUnknownSlot)
	_, err = m.PayByCard("B2")
	wantErr(t, "PayByCard sold-out slot", err, ErrSoldOut)

	sale := mustSale(t, m, "A1")
	if sale.Price != 125 || sale.Slot != "A1" || sale.ID == "" || sale.Status != SalePending {
		t.Fatalf("PayByCard = %+v, want a pending sale for 125 on A1 with an ID", sale)
	}
	wantState(t, m, AwaitingCard)
	if fd.count() != 0 {
		t.Fatal("dispensed before the terminal approved the payment")
	}
	if err := m.CardApproved(sale.ID, "auth-1"); err != nil {
		t.Fatalf("CardApproved = %v", err)
	}
	rec.waitMatch(t, "return to Idle", becameIdle)
	wantState(t, m, Idle)
	if c, v := gw.counts("auth-1"); c != 1 || v != 0 {
		t.Fatalf("captures=%d voids=%d, want exactly one capture and no void", c, v)
	}
	if fd.count() != 1 || m.Snapshot().Slots["A1"].Qty != 2 {
		t.Fatalf("dispenser calls=%d qty=%d, want 1 and 2", fd.count(), m.Snapshot().Slots["A1"].Qty)
	}
	if got := saleStatus(t, m, sale.ID); got != SaleCompleted {
		t.Fatalf("sale status = %v, want SaleCompleted", got)
	}
	if after := m.Snapshot().Box; fmt.Sprint(after) != fmt.Sprint(box) {
		t.Fatalf("a card sale touched the coin box: %v -> %v", box, after)
	}
	rec.waitMatch(t, "a Dispensed notice", func(n Notice) bool { return n.Kind == NoticeDispensed && n.Amount == 125 })
}

func TestStage3_CardDeclinedDispensesAndChargesNothing(t *testing.T) {
	fd, gw := &fakeDispenser{}, newGateway()
	m := newMachine(t, WithDispenser(fd), WithGateway(gw))
	sale := mustSale(t, m, "A1")
	if err := m.CardDeclined(sale.ID); err != nil {
		t.Fatalf("CardDeclined = %v", err)
	}
	wantState(t, m, Idle)
	if err := m.CardDeclined(sale.ID); err != nil {
		t.Fatalf("duplicate CardDeclined = %v, want nil (idempotent)", err)
	}
	wantErr(t, "CardApproved after a decline", m.CardApproved(sale.ID, "auth-x"), ErrSaleClosed)
	wantErr(t, "CardApproved for an unknown sale", m.CardApproved("sale-nope", "a"), ErrUnknownSale)
	wantErr(t, "CardDeclined for an unknown sale", m.CardDeclined("sale-nope"), ErrUnknownSale)
	if c, v := gw.counts("auth-x"); c != 0 || v != 0 || fd.count() != 0 {
		t.Fatalf("captures=%d voids=%d dispenses=%d, want all zero after a decline", c, v, fd.count())
	}
	if got := saleStatus(t, m, sale.ID); got != SaleDeclined {
		t.Fatalf("status = %v, want SaleDeclined", got)
	}
}

func TestStage3_DuplicateAndConcurrentApprovalsCaptureExactlyOnce(t *testing.T) {
	fd, gw := &fakeDispenser{yield: true}, newGateway()
	m := newMachine(t, WithDispenser(fd), WithGateway(gw))
	rec := newRecorder()
	m.Subscribe(rec.record)
	sale := mustSale(t, m, "A1")

	const dups = 20
	start := make(chan struct{})
	errs := make([]error, dups)
	var wg sync.WaitGroup
	for i := 0; i < dups; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			errs[i] = m.CardApproved(sale.ID, "auth-1")
		}()
	}
	close(start)
	within(t, "duplicate approvals", wg.Wait)
	for i, err := range errs {
		if err != nil {
			t.Fatalf("duplicate approval %d = %v, want nil: a retried callback must look like success", i, err)
		}
	}
	rec.waitMatch(t, "return to Idle", becameIdle)
	if err := m.CardApproved(sale.ID, "auth-1"); err != nil {
		t.Fatalf("approval after completion = %v, want nil", err)
	}
	if c, v := gw.counts("auth-1"); c != 1 || v != 0 {
		t.Fatalf("captures=%d voids=%d, want exactly 1 and 0: a duplicate callback must not charge twice", c, v)
	}
	if fd.count() != 1 {
		t.Fatalf("dispensed %d times for one sale", fd.count())
	}
	if q := m.Snapshot().Slots["A1"].Qty; q != 2 {
		t.Fatalf("A1 Qty = %d, want 2", q)
	}
}

func TestStage3_CardDispenseFailureVoidsTheAuthorisationOnce(t *testing.T) {
	fd, gw := &fakeDispenser{err: errors.New("motor jammed")}, newGateway()
	m := newMachine(t, WithDispenser(fd), WithGateway(gw))
	sale := mustSale(t, m, "A1")
	wantErr(t, "CardApproved with a jammed motor", m.CardApproved(sale.ID, "auth-1"), ErrDispenseFailed)
	wantState(t, m, Idle)
	if c, v := gw.counts("auth-1"); c != 0 || v != 1 {
		t.Fatalf("captures=%d voids=%d, want 0 and 1: never charge for an item that was not delivered", c, v)
	}
	if q := m.Snapshot().Slots["A1"].Qty; q != 3 {
		t.Fatalf("A1 Qty = %d, want 3 (reservation released)", q)
	}
	wantErr(t, "duplicate approval after a failed sale", m.CardApproved(sale.ID, "auth-1"), ErrSaleClosed)
	if _, v := gw.counts("auth-1"); v != 1 {
		t.Fatalf("voids=%d after a duplicate callback, want still 1", v)
	}
	if got := saleStatus(t, m, sale.ID); got != SaleFailed {
		t.Fatalf("status = %v, want SaleFailed", got)
	}
}

func TestStage3_ExpiredCardSaleVoidsALateApprovalExactlyOnce(t *testing.T) {
	clk := newClock()
	fd, gw := &fakeDispenser{}, newGateway()
	m := newMachine(t, WithClock(clk.Now), WithCardTimeout(60*time.Second), WithDispenser(fd), WithGateway(gw))
	sale := mustSale(t, m, "A1")

	clk.Advance(59 * time.Second)
	if _, err := m.Tick(clk.Now()); err != nil {
		t.Fatal(err)
	}
	wantState(t, m, AwaitingCard)
	clk.Advance(time.Second)
	if _, err := m.Tick(clk.Now()); err != nil {
		t.Fatal(err)
	}
	wantState(t, m, Idle)
	if got := saleStatus(t, m, sale.ID); got != SaleExpired {
		t.Fatalf("status = %v, want SaleExpired", got)
	}

	// The terminal's approval arrives after the machine gave up on the sale.
	const late = 10
	errs := make([]error, late)
	var wg sync.WaitGroup
	for i := 0; i < late; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); errs[i] = m.CardApproved(sale.ID, "late-auth") }()
	}
	within(t, "late approvals", wg.Wait)
	for i, err := range errs {
		wantErr(t, fmt.Sprintf("late approval %d", i), err, ErrSaleClosed)
	}
	if c, v := gw.counts("late-auth"); c != 0 || v != 1 {
		t.Fatalf("captures=%d voids=%d, want 0 and exactly 1: release the customer's hold once, never charge", c, v)
	}
	if fd.count() != 0 {
		t.Fatal("dispensed for an expired sale")
	}
	mustSale(t, m, "A2") // the machine is usable again
}

func TestStage3_CaptureFailureIsRecordedNotLost(t *testing.T) {
	gw := newGateway()
	gw.captureErr = errors.New("gateway down")
	fd := &fakeDispenser{}
	m := newMachine(t, WithDispenser(fd), WithGateway(gw))
	sale := mustSale(t, m, "A2")
	if err := m.CardApproved(sale.ID, "auth-1"); err != nil {
		t.Fatalf("CardApproved = %v; the customer has their item, so the callback succeeded", err)
	}
	wantState(t, m, Idle)
	if got := saleStatus(t, m, sale.ID); got != SaleCaptureFailed {
		t.Fatalf("status = %v, want SaleCaptureFailed so reconciliation can find it", got)
	}
	if fd.count() != 1 {
		t.Fatalf("dispensed %d times, want 1", fd.count())
	}
}

func TestStage3_ConcurrentCustomersConserveMoneyAndStock(t *testing.T) {
	denoms := []int{5, 10, 25, 100}
	slots := []string{"A1", "A2", "B1"}
	cfg := Config{
		Denominations: denoms,
		Slots: map[string]Product{
			"A1": {Name: "Cola", Price: 125, Qty: 100000},
			"A2": {Name: "Chips", Price: 75, Qty: 100000},
			"B1": {Name: "Gum", Price: 35, Qty: 100000},
		},
		Coins: map[int]int{5: 50, 10: 50, 25: 50, 100: 20},
	}
	fd := &fakeDispenser{yield: true}
	m, err := New(cfg, WithDispenser(fd))
	if err != nil {
		t.Fatal(err)
	}

	var mu sync.Mutex
	inserted, refunded, changed := map[int]int{}, map[int]int{}, map[int]int{}
	sold := map[string]int{}
	add := func(dst, src map[int]int) {
		for d, n := range src {
			dst[d] += n
		}
	}
	allowed := func(err error) bool {
		return errors.Is(err, ErrInvalidState) || errors.Is(err, ErrInsufficientFunds) || errors.Is(err, ErrNoChange)
	}

	stop := make(chan struct{})
	var swg sync.WaitGroup
	swg.Add(1)
	go func() { // sampler: invariants that must hold at every instant
		defer swg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			s := m.Snapshot()
			if s.Balance != sum(s.Escrow) {
				t.Errorf("Balance %d != value of Escrow %v", s.Balance, s.Escrow)
				return
			}
			if s.State == Idle && s.Balance != 0 {
				t.Errorf("Idle with balance %d", s.Balance)
				return
			}
			for d, n := range s.Box {
				if n < 0 {
					t.Errorf("negative coin count %d x %d", n, d)
					return
				}
			}
			for c, p := range s.Slots {
				if p.Qty < 0 {
					t.Errorf("slot %s has negative stock %d", c, p.Qty)
					return
				}
			}
		}
	}()

	var wg sync.WaitGroup
	for g := 0; g < 12; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r := rand.New(rand.NewSource(int64(g) + 1))
			for i := 0; i < 400; i++ {
				switch op := r.Intn(10); {
				case op < 6:
					d := denoms[r.Intn(len(denoms))]
					switch err := m.InsertCoin(d); {
					case err == nil:
						mu.Lock()
						inserted[d]++
						mu.Unlock()
					case !errors.Is(err, ErrInvalidState):
						t.Errorf("InsertCoin(%d) = %v", d, err)
					}
				case op < 9:
					code := slots[r.Intn(len(slots))]
					rec, err := m.Select(code)
					switch {
					case err == nil:
						mu.Lock()
						sold[code]++
						add(changed, rec.Change)
						mu.Unlock()
					case !allowed(err):
						t.Errorf("Select(%s) = %v", code, err)
					}
				default:
					refund, err := m.Cancel()
					switch {
					case err == nil:
						mu.Lock()
						add(refunded, refund)
						mu.Unlock()
					case !errors.Is(err, ErrInvalidState):
						t.Errorf("Cancel = %v", err)
					}
				}
			}
		}()
	}
	within(t, "concurrent customers", wg.Wait)
	close(stop)
	swg.Wait()

	if m.State() == HasMoney {
		refund, err := m.Cancel()
		if err != nil {
			t.Fatalf("final Cancel = %v", err)
		}
		add(refunded, refund)
	}
	end := m.Snapshot()
	if end.State != Idle || end.Balance != 0 || len(end.Escrow) != 0 {
		t.Fatalf("machine did not settle: %+v", end)
	}

	totalSold, valueIn, valueOut, kept := 0, 0, 0, 0
	for _, d := range denoms {
		want := cfg.Coins[d] + inserted[d] - refunded[d] - changed[d]
		if end.Box[d] != want {
			t.Errorf("Box[%d] = %d, want %d (initial %d + inserted %d - refunded %d - change %d): coins were created or lost",
				d, end.Box[d], want, cfg.Coins[d], inserted[d], refunded[d], changed[d])
		}
		valueIn += d * inserted[d]
		valueOut += d * (refunded[d] + changed[d])
	}
	for _, c := range slots {
		totalSold += sold[c]
		kept += sold[c] * cfg.Slots[c].Price
		if want := cfg.Slots[c].Qty - sold[c]; end.Slots[c].Qty != want {
			t.Errorf("slot %s Qty = %d, want %d", c, end.Slots[c].Qty, want)
		}
	}
	if valueIn != valueOut+kept {
		t.Errorf("money in %d != refunds+change %d + prices kept %d", valueIn, valueOut, kept)
	}
	if fd.count() != totalSold {
		t.Errorf("dispenser ran %d times for %d successful sales: exactly one dispense per sale", fd.count(), totalSold)
	}
	if totalSold == 0 {
		t.Fatal("no sale succeeded; the workload is not exercising the machine")
	}
}

func TestStage3_CancelRacingSelectExactlyOneWins(t *testing.T) {
	for round := 0; round < 300; round++ {
		fd := &fakeDispenser{yield: true}
		m := newMachine(t, WithDispenser(fd))
		insert(t, m, 100, 25) // exactly the price of A1
		start := make(chan struct{})
		var selErr, canErr error
		var refund map[int]int
		var wg sync.WaitGroup
		wg.Add(2)
		go func() { defer wg.Done(); <-start; _, selErr = m.Select("A1") }()
		go func() { defer wg.Done(); <-start; refund, canErr = m.Cancel() }()
		close(start)
		within(t, "Select racing Cancel", wg.Wait)

		switch {
		case selErr == nil && canErr == nil:
			t.Fatalf("round %d: Select and Cancel both succeeded: the customer got the item AND a refund of %v", round, refund)
		case selErr != nil && canErr != nil:
			t.Fatalf("round %d: neither succeeded (Select: %v, Cancel: %v): the customer's money is stuck", round, selErr, canErr)
		case selErr == nil:
			wantErr(t, "Cancel that lost the race", canErr, ErrInvalidState)
			if fd.count() != 1 || len(refund) != 0 {
				t.Fatalf("round %d: Select won but dispenses=%d refund=%v", round, fd.count(), refund)
			}
		default:
			wantErr(t, "Select that lost the race", selErr, ErrInvalidState)
			if fd.count() != 0 || sum(refund) != 125 {
				t.Fatalf("round %d: Cancel won but dispenses=%d refund=%v (want 0 and 125 cents)", round, fd.count(), refund)
			}
		}
		wantState(t, m, Idle)
	}
}
