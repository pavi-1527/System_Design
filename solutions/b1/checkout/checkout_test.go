package checkout

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

// ---- fakes -----------------------------------------------------------------
//
// Every collaborator is a few lines of in-memory code. That is the payoff of
// small consumer-side interfaces: no mocking library, no network, no clock.

type chargeCall struct {
	Key    string
	Amount Money
	Token  string
}

type fakePayer struct {
	mu       sync.Mutex
	calls    []chargeCall
	err      error                     // returned for every call when set
	onCharge func(ctx context.Context) // runs inside Charge (e.g. cancel the context)
}

func (p *fakePayer) Charge(ctx context.Context, key string, amount Money, token string) (string, error) {
	p.mu.Lock()
	p.calls = append(p.calls, chargeCall{key, amount, token})
	n := len(p.calls)
	hook, err := p.onCharge, p.err
	p.mu.Unlock()
	if hook != nil {
		hook(ctx)
	}
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("rcpt-%s-%d", key, n), nil
}

func (p *fakePayer) charges() []chargeCall {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]chargeCall(nil), p.calls...)
}

type fakeStock struct {
	mu        sync.Mutex
	onHand    map[string]int
	reserveOK func(sku string) error // optional extra failure injection
	releases  []Line
	touched   int
}

func newStock(onHand map[string]int) *fakeStock {
	cp := map[string]int{}
	for k, v := range onHand {
		cp[k] = v
	}
	return &fakeStock{onHand: cp}
}

func (s *fakeStock) Reserve(sku string, qty int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.touched++
	if s.reserveOK != nil {
		if err := s.reserveOK(sku); err != nil {
			return err
		}
	}
	if s.onHand[sku] < qty {
		return ErrOutOfStock
	}
	s.onHand[sku] -= qty
	return nil
}

func (s *fakeStock) Release(sku string, qty int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.touched++
	s.onHand[sku] += qty
	s.releases = append(s.releases, Line{SKU: sku, Qty: qty})
}

func (s *fakeStock) level(sku string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.onHand[sku]
}

type fakeNotifier struct {
	mu       sync.Mutex
	orders   []Order
	ctxErrs  []error
	err      error
	mutateIt bool
}

func (n *fakeNotifier) OrderPlaced(ctx context.Context, o Order) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.orders = append(n.orders, o)
	n.ctxErrs = append(n.ctxErrs, ctx.Err())
	if n.mutateIt && len(o.Lines) > 0 {
		o.Lines[0].Qty = 999 // a rude notifier: must not corrupt the caller's order
	}
	return n.err
}

type fixedClock struct{ t time.Time }

func (c fixedClock) Now() time.Time { return c.t }

var t0 = time.Date(2026, 3, 1, 9, 30, 0, 0, time.UTC)

func ctxT(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func newService(t *testing.T, p Payer, s Stock, opts ...Option) *Service {
	t.Helper()
	svc, err := New(p, s, opts...)
	if err != nil {
		t.Fatalf("New returned %v; want a ready Service (is New still returning errNotImplemented?)", err)
	}
	return svc
}

func validRequest() Request {
	return Request{
		ID: "ord-1", Customer: "member-42", Region: "US-CA", CardToken: "tok_1",
		Lines: []Line{{SKU: "A", Qty: 2, UnitPrice: 1000}, {SKU: "B", Qty: 1, UnitPrice: 550}},
	}
}

// ---- construction ------------------------------------------------------------

func TestNew_RejectsMissingDependencies(t *testing.T) {
	p, s := &fakePayer{}, newStock(nil)
	if _, err := New(nil, s); !errors.Is(err, ErrInvalidConfig) {
		t.Errorf("New(nil payer) error = %v; want ErrInvalidConfig (a Service that panics on first use is worse than one that refuses to exist)", err)
	}
	if _, err := New(p, nil); !errors.Is(err, ErrInvalidConfig) {
		t.Errorf("New(nil stock) error = %v; want ErrInvalidConfig", err)
	}
	svc, err := New(p, s)
	if err != nil || svc == nil {
		t.Fatalf("New(valid) = %v, %v; want a Service and nil error", svc, err)
	}
}

func TestNew_RejectsBadOptions(t *testing.T) {
	p, s := &fakePayer{}, newStock(nil)
	cases := map[string]Option{
		"WithMaxLines(0)":        WithMaxLines(0),
		"WithMaxLines(-3)":       WithMaxLines(-3),
		"WithClock(nil)":         WithClock(nil),
		"WithNotifier(nil)":      WithNotifier(nil),
		"WithTaxRates(nil)":      WithTaxRates(nil),
		"WithTaxRates(negative)": WithTaxRates(map[string]int{"X": -1}),
		"WithTaxRates(>100%)":    WithTaxRates(map[string]int{"X": 10001}),
		"nil option":             nil,
	}
	for name, opt := range cases {
		if _, err := New(p, s, opt); !errors.Is(err, ErrInvalidConfig) {
			t.Errorf("New(%s) error = %v; want ErrInvalidConfig", name, err)
		}
	}
}

func TestNew_CopiesTaxRatesFromCaller(t *testing.T) {
	rates := map[string]int{"XX": 1000}
	svc := newService(t, &fakePayer{}, newStock(map[string]int{"A": 10}), WithTaxRates(rates))
	rates["XX"] = 9999 // the caller keeps using its map
	delete(rates, "XX")

	req := validRequest()
	req.Region = "XX"
	req.Lines = []Line{{SKU: "A", Qty: 1, UnitPrice: 1000}}
	o, err := svc.Place(ctxT(t), req)
	if err != nil {
		t.Fatalf("Place: %v (the Service must keep its own copy of the tax table)", err)
	}
	if o.Tax != 100 {
		t.Fatalf("tax = %d, want 100 (10%% of 1000); the Service shares the caller's map, so later edits leak in", o.Tax)
	}
}

func TestDefaultTaxRates_ReturnsACopy(t *testing.T) {
	a := DefaultTaxRates()
	a["US-CA"] = 1
	delete(a, "EU-DE")
	b := DefaultTaxRates()
	if b["US-CA"] != 725 || b["EU-DE"] != 1900 {
		t.Fatalf("DefaultTaxRates() = %v after the caller edited a previous result; it must hand out a fresh copy every time", b)
	}
}

// ---- parity with the legacy god object -----------------------------------------

type parityCase struct {
	name   string
	region string
	coupon string
	lines  []Line
}

var parityCases = []parityCase{
	{"small order, no discount", "US-OR", "", []Line{{"A", 1, 1999}}},
	{"california rounding", "US-CA", "", []Line{{"A", 3, 3333}}},
	{"SAVE10", "EU-DE", "SAVE10", []Line{{"A", 2, 4500}, {"B", 1, 1}}},
	{"coupon is case-insensitive", "IN-KA", "save10", []Line{{"A", 1, 12345}}},
	{"SAVE20 below its minimum is ignored", "US-OR", "SAVE20", []Line{{"A", 1, 19999}}},
	{"SAVE20 at its minimum", "US-OR", "SAVE20", []Line{{"A", 1, 20000}}},
	{"volume discount only", "EU-DE", "", []Line{{"A", 5, 10000}}},
	{"volume 5% loses to SAVE10", "US-CA", "SAVE10", []Line{{"A", 1, 60000}}},
	{"volume 5% loses to SAVE20", "US-CA", "SAVE20", []Line{{"A", 1, 60000}}},
	{"unknown coupon ignored", "US-OR", "FREEMONEY", []Line{{"A", 1, 7777}}},
	{"half-up rounding of discount", "US-OR", "SAVE10", []Line{{"A", 1, 12345}}},
	{"many lines", "IN-KA", "SAVE20", []Line{{"A", 2, 9999}, {"B", 3, 5000}, {"C", 7, 123}}},
}

func TestPlace_ParityWithLegacyOrderManager(t *testing.T) {
	for _, tc := range parityCases {
		t.Run(tc.name, func(t *testing.T) {
			// Legacy side: swap the globals, run, restore.
			oldCharge, oldStock, oldMail := LegacyCharge, LegacyStock, LegacySendEmail
			defer func() { LegacyCharge, LegacyStock, LegacySendEmail = oldCharge, oldStock, oldMail }()
			var legacyCharged int64
			LegacyCharge = func(token string, amount int64) (string, error) { legacyCharged = amount; return "L-1", nil }
			LegacyStock = map[string]int{"A": 100, "B": 100, "C": 100}
			mails := 0
			LegacySendEmail = func(to, subject, body string) error { mails++; return nil }

			skus, qtys, prices := make([]string, 0), make([]int, 0), make([]int64, 0)
			for _, l := range tc.lines {
				skus, qtys, prices = append(skus, l.SKU), append(qtys, l.Qty), append(prices, int64(l.UnitPrice))
			}
			var mgr OrderManager
			lo, err := mgr.PlaceOrder("member-42", tc.region, tc.coupon, "tok_1", skus, qtys, prices)
			if err != nil {
				t.Fatalf("legacy oracle failed: %v", err)
			}

			// New side: same inputs, fakes injected.
			payer := &fakePayer{}
			stock := newStock(map[string]int{"A": 100, "B": 100, "C": 100})
			note := &fakeNotifier{}
			svc := newService(t, payer, stock, WithNotifier(note), WithClock(fixedClock{t0}))
			req := Request{ID: "ord-1", Customer: "member-42", Region: tc.region, Coupon: tc.coupon, CardToken: "tok_1", Lines: tc.lines}
			no, err := svc.Place(ctxT(t), req)
			if err != nil {
				t.Fatalf("Place: %v", err)
			}

			got := [4]int64{int64(no.Subtotal), int64(no.Discount), int64(no.Tax), int64(no.Total)}
			want := [4]int64{lo.Subtotal, lo.Discount, lo.Tax, lo.Total}
			if got != want {
				t.Errorf("[subtotal discount tax total] = %v, legacy says %v: behaviour changed in the refactor", got, want)
			}
			calls := payer.charges()
			if len(calls) != 1 || int64(calls[0].Amount) != legacyCharged || calls[0].Token != "tok_1" {
				t.Errorf("charges = %+v; legacy charged %d once with token tok_1", calls, legacyCharged)
			}
			for _, sku := range []string{"A", "B", "C"} {
				if stock.level(sku) != LegacyStock[sku] {
					t.Errorf("stock[%s] = %d, legacy left %d", sku, stock.level(sku), LegacyStock[sku])
				}
			}
			if mails != 1 || len(note.orders) != 1 || !no.Notified {
				t.Errorf("legacy sent %d mail; new notifier saw %d orders, Notified=%v; want one of each", mails, len(note.orders), no.Notified)
			}
		})
	}
}

func TestPlace_PricingHardCodedExample(t *testing.T) {
	// 2 x 45.00 + 0.01 in Germany with SAVE10: subtotal 9001, discount 900
	// (900.1 rounds down), taxable 8101, tax 1539 (1539.19), total 9640.
	svc := newService(t, &fakePayer{}, newStock(map[string]int{"A": 9, "B": 9}))
	req := validRequest()
	req.Region, req.Coupon = "EU-DE", "SAVE10"
	req.Lines = []Line{{"A", 2, 4500}, {"B", 1, 1}}
	o, err := svc.Place(ctxT(t), req)
	if err != nil {
		t.Fatal(err)
	}
	if o.Subtotal != 9001 || o.Discount != 900 || o.Tax != 1539 || o.Total != 9640 {
		t.Fatalf("got subtotal=%d discount=%d tax=%d total=%d; want 9001 900 1539 9640", o.Subtotal, o.Discount, o.Tax, o.Total)
	}
}

// The legacy code has bugs we deliberately do NOT preserve. This test pins the
// legacy behaviour so the next two tests are a conscious departure, not an accident.
func TestLegacy_LeaksStockWhenPaymentDeclined(t *testing.T) {
	oldCharge, oldStock := LegacyCharge, LegacyStock
	defer func() { LegacyCharge, LegacyStock = oldCharge, oldStock }()
	LegacyCharge = func(string, int64) (string, error) { return "", errors.New("declined") }
	LegacyStock = map[string]int{"A": 5}
	var m OrderManager
	if _, err := m.PlaceOrder("c", "US-OR", "", "tok", []string{"A"}, []int{2}, []int64{100}); err == nil {
		t.Fatal("legacy accepted a declined payment")
	}
	if LegacyStock["A"] != 3 {
		t.Fatalf("legacy stock = %d; the oracle changed: it is supposed to leak 2 units on a decline", LegacyStock["A"])
	}
}

// ---- behaviour the refactor must add ----------------------------------------------

func TestPlace_ValidationFailuresTouchNoDependency(t *testing.T) {
	mut := func(f func(*Request)) Request { r := validRequest(); f(&r); return r }
	cases := map[string]Request{
		"empty id":       mut(func(r *Request) { r.ID = "" }),
		"empty customer": mut(func(r *Request) { r.Customer = "" }),
		"empty token":    mut(func(r *Request) { r.CardToken = "" }),
		"no lines":       mut(func(r *Request) { r.Lines = nil }),
		"unknown region": mut(func(r *Request) { r.Region = "MARS" }),
		"zero qty":       mut(func(r *Request) { r.Lines[0].Qty = 0 }),
		"negative qty":   mut(func(r *Request) { r.Lines[0].Qty = -2 }),
		"negative price": mut(func(r *Request) { r.Lines[1].UnitPrice = -1 }),
		"empty sku":      mut(func(r *Request) { r.Lines[1].SKU = "" }),
		"too many lines": mut(func(r *Request) { r.Lines = append(r.Lines, Line{"C", 1, 1}) }),
	}
	for name, req := range cases {
		t.Run(name, func(t *testing.T) {
			payer, stock, note := &fakePayer{}, newStock(map[string]int{"A": 9, "B": 9, "C": 9}), &fakeNotifier{}
			svc := newService(t, payer, stock, WithNotifier(note), WithMaxLines(2))
			_, err := svc.Place(ctxT(t), req)
			if !errors.Is(err, ErrInvalidOrder) {
				t.Fatalf("error = %v; want it to wrap ErrInvalidOrder", err)
			}
			if len(payer.charges()) != 0 || stock.touched != 0 || len(note.orders) != 0 {
				t.Fatalf("a rejected order touched a dependency (charges=%d stock calls=%d notifications=%d): validate before any side effect",
					len(payer.charges()), stock.touched, len(note.orders))
			}
		})
	}
}

func TestPlace_OutOfStockRollsBackEarlierReservations(t *testing.T) {
	payer, stock := &fakePayer{}, newStock(map[string]int{"A": 5, "B": 0})
	svc := newService(t, payer, stock)
	_, err := svc.Place(ctxT(t), validRequest())
	if !errors.Is(err, ErrOutOfStock) {
		t.Fatalf("error = %v; want it to wrap ErrOutOfStock (use %%w so the cause survives)", err)
	}
	if !strings.Contains(err.Error(), `"B"`) || !strings.Contains(err.Error(), "ord-1") {
		t.Errorf("error %q should name the failing sku and the order for whoever is on call", err)
	}
	if got := stock.level("A"); got != 5 {
		t.Errorf("stock[A] = %d after a failed order, want 5: line A was reserved and never released (the legacy bug)", got)
	}
	if n := len(payer.charges()); n != 0 {
		t.Errorf("charged %d times for an order that could not be fulfilled", n)
	}
}

// declineError is the kind of rich error a real gateway adapter returns.
type declineError struct{ Code string }

func (e *declineError) Error() string { return "card declined: " + e.Code }
func (e *declineError) Unwrap() error { return ErrPaymentDeclined }

func TestPlace_PaymentDeclinedReleasesAllStockAndKeepsTheCause(t *testing.T) {
	payer := &fakePayer{err: &declineError{Code: "insufficient_funds"}}
	stock := newStock(map[string]int{"A": 5, "B": 5})
	svc := newService(t, payer, stock)
	_, err := svc.Place(ctxT(t), validRequest())
	if !errors.Is(err, ErrPaymentDeclined) {
		t.Fatalf("error = %v; want errors.Is(err, ErrPaymentDeclined)", err)
	}
	var de *declineError
	if !errors.As(err, &de) || de.Code != "insufficient_funds" {
		t.Fatalf("errors.As(*declineError) failed on %v: the adapter's detail must survive wrapping", err)
	}
	if stock.level("A") != 5 || stock.level("B") != 5 {
		t.Errorf("stock A=%d B=%d after a decline, want 5 and 5: reservations leaked", stock.level("A"), stock.level("B"))
	}
	if svc.Placed() != 0 {
		t.Errorf("Placed() = %d, want 0 after a decline", svc.Placed())
	}
}

func TestPlace_ChargeUsesOrderIDAsIdempotencyKey(t *testing.T) {
	payer := &fakePayer{}
	svc := newService(t, payer, newStock(map[string]int{"A": 9, "B": 9}))
	req := validRequest()
	req.ID = "ord-stable-77"
	for i := 0; i < 2; i++ { // the caller retried after a timeout
		if _, err := svc.Place(ctxT(t), req); err != nil {
			t.Fatal(err)
		}
	}
	calls := payer.charges()
	if len(calls) != 2 || calls[0].Key != "ord-stable-77" || calls[1].Key != "ord-stable-77" {
		t.Fatalf("charge keys = %+v; every attempt for one order must carry the same key so the provider can deduplicate", calls)
	}
}

func TestPlace_UsesInjectedClockAndReturnsReceipt(t *testing.T) {
	svc := newService(t, &fakePayer{}, newStock(map[string]int{"A": 9, "B": 9}), WithClock(ClockFunc(func() time.Time { return t0 })))
	o, err := svc.Place(ctxT(t), validRequest())
	if err != nil {
		t.Fatal(err)
	}
	if !o.PlacedAt.Equal(t0) {
		t.Errorf("PlacedAt = %v, want the injected clock's %v", o.PlacedAt, t0)
	}
	if o.Receipt != "rcpt-ord-1-1" || o.ID != "ord-1" || o.Customer != "member-42" || o.Region != "US-CA" {
		t.Errorf("order = %+v; receipt and identity fields must come from the payer and the request", o)
	}
}

func TestPlace_NotifierFailureDoesNotFailAPaidOrder(t *testing.T) {
	note := &fakeNotifier{err: errors.New("smtp: connection refused")}
	svc := newService(t, &fakePayer{}, newStock(map[string]int{"A": 9, "B": 9}), WithNotifier(note))
	o, err := svc.Place(ctxT(t), validRequest())
	if err != nil {
		t.Fatalf("Place returned %v: the customer has been charged, so a mail outage must not look like a failed order", err)
	}
	if o.Notified {
		t.Error("Notified = true although the notifier failed")
	}
	if svc.Placed() != 1 {
		t.Errorf("Placed() = %d, want 1", svc.Placed())
	}
}

func TestPlace_NotifierIsOptional(t *testing.T) {
	svc := newService(t, &fakePayer{}, newStock(map[string]int{"A": 9, "B": 9}))
	o, err := svc.Place(ctxT(t), validRequest())
	if err != nil || o.Notified {
		t.Fatalf("Place = %+v, %v; want a paid order with Notified=false and no error", o, err)
	}
}

func TestPlace_CancelledContextDoesNothing(t *testing.T) {
	payer, stock := &fakePayer{}, newStock(map[string]int{"A": 9, "B": 9})
	svc := newService(t, payer, stock)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := svc.Place(ctx, validRequest())
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v; want it to wrap context.Canceled", err)
	}
	if stock.touched != 0 || len(payer.charges()) != 0 {
		t.Fatalf("a cancelled request reserved stock or charged: stock calls=%d charges=%d", stock.touched, len(payer.charges()))
	}
}

func TestPlace_CancelledBetweenReserveAndChargeReleasesAndSkipsCharge(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	payer, stock := &fakePayer{}, newStock(map[string]int{"A": 5, "B": 5})
	stock.reserveOK = func(sku string) error {
		if sku == "B" {
			cancel() // the client disconnects while we are reserving the last line
		}
		return nil
	}
	svc := newService(t, payer, stock)
	_, err := svc.Place(ctx, validRequest())
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v; want it to wrap context.Canceled", err)
	}
	if len(payer.charges()) != 0 {
		t.Error("charged a customer whose request was already cancelled")
	}
	if stock.level("A") != 5 || stock.level("B") != 5 {
		t.Errorf("stock A=%d B=%d, want 5 and 5: reservations must be released when the context ends", stock.level("A"), stock.level("B"))
	}
}

func TestPlace_CancelDuringChargeStillReturnsThePaidOrder(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	payer := &fakePayer{onCharge: func(context.Context) { cancel() }} // money moves, then the client goes away
	note := &fakeNotifier{}
	stock := newStock(map[string]int{"A": 9, "B": 9})
	svc := newService(t, payer, stock, WithNotifier(note))
	o, err := svc.Place(ctx, validRequest())
	if err != nil {
		t.Fatalf("Place = %v; once the charge succeeded the order must be returned even though ctx is cancelled", err)
	}
	if o.Receipt == "" || stock.level("A") != 7 {
		t.Errorf("order %+v, stock[A]=%d; want the paid order with stock still reserved", o, stock.level("A"))
	}
	if len(note.ctxErrs) != 1 || note.ctxErrs[0] != nil {
		t.Errorf("notifier saw ctx error %v; the confirmation of a paid order must not be cancelled with the request (context.WithoutCancel)", note.ctxErrs)
	}
}

func TestPlace_DoesNotAliasCallersLines(t *testing.T) {
	note := &fakeNotifier{mutateIt: true}
	svc := newService(t, &fakePayer{}, newStock(map[string]int{"A": 99, "B": 99}), WithNotifier(note))
	req := validRequest()
	o, err := svc.Place(ctxT(t), req)
	if err != nil {
		t.Fatal(err)
	}
	req.Lines[0].Qty = 50 // caller reuses its slice
	if o.Lines[0].Qty != 2 {
		t.Errorf("Order.Lines[0].Qty = %d after the caller edited Request.Lines; Order must hold a copy", o.Lines[0].Qty)
	}
	if o.Lines[0].Qty == 999 {
		t.Error("the notifier's edit of its Order leaked into the returned Order; hand the notifier its own copy")
	}
	if req.Lines[0].SKU != "A" || len(req.Lines) != 2 {
		t.Errorf("Place modified the caller's request: %+v", req.Lines)
	}
}

// ---- concurrency ----------------------------------------------------------------

func TestPlace_ConcurrentOrdersAreIndependent(t *testing.T) {
	const n = 64
	payer, stock := &fakePayer{}, newStock(map[string]int{"A": 1000, "B": 1000})
	svc := newService(t, payer, stock, WithNotifier(&fakeNotifier{}))
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req := validRequest()
			req.ID = fmt.Sprintf("ord-%d", i)
			_, err := svc.Place(ctxT(t), req)
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent Place failed: %v", err)
		}
	}
	if svc.Placed() != n {
		t.Errorf("Placed() = %d, want %d: the counter loses updates (or the Service shares mutable state)", svc.Placed(), n)
	}
	if got := len(payer.charges()); got != n {
		t.Errorf("charges = %d, want %d", got, n)
	}
	if stock.level("A") != 1000-2*n || stock.level("B") != 1000-n {
		t.Errorf("stock A=%d B=%d, want %d and %d", stock.level("A"), stock.level("B"), 1000-2*n, 1000-n)
	}
}

func TestPlace_OnlyOneBuyerGetsTheLastUnit(t *testing.T) {
	const buyers = 20
	payer, stock := &fakePayer{}, newStock(map[string]int{"LAST": 1})
	svc := newService(t, payer, stock)
	var wg sync.WaitGroup
	var mu sync.Mutex
	won, outOfStock := 0, 0
	for i := 0; i < buyers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := svc.Place(ctxT(t), Request{
				ID: fmt.Sprintf("o%d", i), Customer: "c", Region: "US-OR", CardToken: "t",
				Lines: []Line{{SKU: "LAST", Qty: 1, UnitPrice: 500}},
			})
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				won++
			case errors.Is(err, ErrOutOfStock):
				outOfStock++
			default:
				t.Errorf("unexpected error: %v", err)
			}
		}()
	}
	wg.Wait()
	if won != 1 || outOfStock != buyers-1 {
		t.Fatalf("won=%d outOfStock=%d; want exactly 1 winner and %d ErrOutOfStock", won, outOfStock, buyers-1)
	}
	if got := len(payer.charges()); got != 1 {
		t.Fatalf("charges = %d, want 1: customers who could not get the item were charged", got)
	}
}

// ---- extension: new behaviour is new code, not edits to Service ---------------------

// auditedPayer is a decorator defined in the TEST file. It satisfies Payer
// structurally, so Service needs no change to accept it: that is the whole
// point of defining the interface at the consumer with one method.
type auditedPayer struct {
	inner Payer
	log   *[]string
}

func (a auditedPayer) Charge(ctx context.Context, key string, amount Money, token string) (string, error) {
	*a.log = append(*a.log, fmt.Sprintf("charge %s %d", key, amount))
	r, err := a.inner.Charge(ctx, key, amount, token)
	*a.log = append(*a.log, fmt.Sprintf("result %q %v", r, err))
	return r, err
}

func TestExtension_PayerDecoratorPlugsInWithoutChangingService(t *testing.T) {
	var log []string
	inner := &fakePayer{}
	svc := newService(t, auditedPayer{inner: inner, log: &log}, newStock(map[string]int{"A": 9, "B": 9}))
	if _, err := svc.Place(ctxT(t), validRequest()); err != nil {
		t.Fatal(err)
	}
	want := []string{"charge ord-1 2735", `result "rcpt-ord-1-1" <nil>`}
	if !reflect.DeepEqual(log, want) {
		t.Fatalf("audit log = %q, want %q", log, want)
	}
}
