package adapter

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

const testTimeout = 5 * time.Second

// ---- scripted vendor clients --------------------------------------------------

type fakeA struct {
	mu      sync.Mutex
	calls   []VendorAPayment
	reply   VendorAReply
	err     error
	hang    bool
	release chan struct{}
}

func (f *fakeA) CreatePayment(ctx context.Context, p VendorAPayment) (VendorAReply, error) {
	f.mu.Lock()
	f.calls = append(f.calls, p)
	f.mu.Unlock()
	if f.hang {
		select {
		case <-ctx.Done():
			return VendorAReply{}, ctx.Err()
		case <-f.release:
			return VendorAReply{}, errors.New("released")
		}
	}
	return f.reply, f.err
}

func (f *fakeA) first(t *testing.T) VendorAPayment {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.calls) == 0 {
		t.Fatal("vendor A was never called")
	}
	return f.calls[0]
}

func (f *fakeA) count() int { f.mu.Lock(); defer f.mu.Unlock(); return len(f.calls) }

type fakeB struct {
	mu      sync.Mutex
	calls   []VendorBCharge
	res     VendorBResult
	err     error
	hang    bool
	release chan struct{}
}

func (f *fakeB) Create(ctx context.Context, c VendorBCharge) (VendorBResult, error) {
	f.mu.Lock()
	f.calls = append(f.calls, c)
	f.mu.Unlock()
	if f.hang {
		select {
		case <-ctx.Done():
			return VendorBResult{}, ctx.Err()
		case <-f.release:
			return VendorBResult{}, errors.New("released")
		}
	}
	return f.res, f.err
}

func (f *fakeB) first(t *testing.T) VendorBCharge {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.calls) == 0 {
		t.Fatal("vendor B was never called")
	}
	return f.calls[0]
}

func (f *fakeB) count() int { f.mu.Lock(); defer f.mu.Unlock(); return len(f.calls) }

// ---- the shared contract ------------------------------------------------------

// outcome describes one thing a vendor can do, in each vendor's own words.
type outcome struct {
	name       string
	a          func() (VendorAReply, error)
	b          func() (VendorBResult, error)
	wantStatus Status
	wantErr    error
	cause      error // if non-nil, the original vendor error must stay reachable
}

var (
	a429 = &VendorAError{HTTPStatus: 429, Body: "slow down"}
	b429 = &VendorBError{Type: "rate_limit", Message: "slow down"}
	a503 = &VendorAError{HTTPStatus: 503, Body: "maintenance"}
	bAPI = &VendorBError{Type: "api_error", Message: "boom"}
	a422 = &VendorAError{HTTPStatus: 422, Body: "bad amount"}
	bBad = &VendorBError{Type: "invalid_request", Message: "bad amount"}
	conn = errors.New("connection reset by peer")
)

var outcomes = []outcome{
	{name: "approved",
		a: func() (VendorAReply, error) { return VendorAReply{TxnID: "tx-1", Code: "00"}, nil },
		b: func() (VendorBResult, error) { return VendorBResult{ID: "tx-1", State: "succeeded"}, nil }, wantStatus: Approved},
	{name: "declined",
		a: func() (VendorAReply, error) { return VendorAReply{TxnID: "tx-2", Code: "05"}, nil },
		b: func() (VendorBResult, error) { return VendorBResult{ID: "tx-2", State: "failed"}, nil }, wantStatus: Declined},
	{name: "pending",
		a: func() (VendorAReply, error) { return VendorAReply{TxnID: "tx-3", Code: "09"}, nil },
		b: func() (VendorBResult, error) { return VendorBResult{ID: "tx-3", State: "requires_action"}, nil }, wantStatus: Pending},
	{name: "rate limited",
		a:       func() (VendorAReply, error) { return VendorAReply{}, a429 },
		b:       func() (VendorBResult, error) { return VendorBResult{}, b429 },
		wantErr: ErrRateLimited, cause: nil},
	{name: "server error",
		a:       func() (VendorAReply, error) { return VendorAReply{}, a503 },
		b:       func() (VendorBResult, error) { return VendorBResult{}, bAPI },
		wantErr: ErrUnavailable},
	{name: "vendor rejects the request",
		a:       func() (VendorAReply, error) { return VendorAReply{}, a422 },
		b:       func() (VendorBResult, error) { return VendorBResult{}, bBad },
		wantErr: ErrRejected},
	{name: "transport failure",
		a:       func() (VendorAReply, error) { return VendorAReply{}, conn },
		b:       func() (VendorBResult, error) { return VendorBResult{}, conn },
		wantErr: ErrUnavailable, cause: conn},
	{name: "wrapped vendor error from the SDK",
		a:       func() (VendorAReply, error) { return VendorAReply{}, fmt.Errorf("sdk: %w", a429) },
		b:       func() (VendorBResult, error) { return VendorBResult{}, fmt.Errorf("sdk: %w", b429) },
		wantErr: ErrRateLimited},
	{name: "unknown code or state",
		a:       func() (VendorAReply, error) { return VendorAReply{TxnID: "x", Code: "ZZ"}, nil },
		b:       func() (VendorBResult, error) { return VendorBResult{ID: "x", State: "teleported"}, nil },
		wantErr: ErrUnknownResponse},
	{name: "approval without a transaction id",
		a:       func() (VendorAReply, error) { return VendorAReply{Code: "00"}, nil },
		b:       func() (VendorBResult, error) { return VendorBResult{State: "succeeded"}, nil },
		wantErr: ErrUnknownResponse},
}

// harness builds a Charger for one vendor from a scripted outcome and exposes
// how many times the vendor was called.
type harness struct {
	name  string
	build func(o outcome) (Charger, func() int)
	hang  func(release chan struct{}) (Charger, func() int)
}

var harnesses = []harness{
	{
		name: "vendorA",
		build: func(o outcome) (Charger, func() int) {
			f := &fakeA{}
			f.reply, f.err = o.a()
			return NewVendorA(f), f.count
		},
		hang: func(rel chan struct{}) (Charger, func() int) {
			f := &fakeA{hang: true, release: rel}
			return NewVendorA(f), f.count
		},
	},
	{
		name: "vendorB",
		build: func(o outcome) (Charger, func() int) {
			f := &fakeB{}
			f.res, f.err = o.b()
			return NewVendorB(f), f.count
		},
		hang: func(rel chan struct{}) (Charger, func() int) {
			f := &fakeB{hang: true, release: rel}
			return NewVendorB(f), f.count
		},
	},
}

var goodReq = ChargeRequest{IdempotencyKey: "idem-42", AmountMinor: 1250, Currency: "USD"}

func TestContract_VendorOutcomesMapToTheDomainTaxonomy(t *testing.T) {
	for _, h := range harnesses {
		for _, o := range outcomes {
			t.Run(h.name+"/"+o.name, func(t *testing.T) {
				c, _ := h.build(o)
				got, err := c.Charge(context.Background(), goodReq)
				if o.wantErr != nil {
					if !errors.Is(err, o.wantErr) {
						t.Fatalf("err = %v, want errors.Is %v", err, o.wantErr)
					}
					if o.cause != nil && !errors.Is(err, o.cause) {
						t.Errorf("the vendor error %v was lost: errors.Is(%v, cause) is false", o.cause, err)
					}
					if got != (ChargeResult{}) {
						t.Errorf("result = %+v alongside an error, want the zero value", got)
					}
					return
				}
				if err != nil {
					t.Fatalf("err = %v, want nil", err)
				}
				if got.Status != o.wantStatus || got.ID == "" {
					t.Errorf("result = %+v, want status %q and a non-empty id", got, o.wantStatus)
				}
			})
		}
	}
}

func TestContract_VendorErrorStaysReachableForLogging(t *testing.T) {
	c, _ := harnesses[0].build(outcomes[3])
	_, err := c.Charge(context.Background(), goodReq)
	var ae *VendorAError
	if !errors.As(err, &ae) || ae.HTTPStatus != 429 {
		t.Errorf("vendor A: errors.As(%v, *VendorAError) failed; keep the cause with a second %%w so it can be logged", err)
	}
	c, _ = harnesses[1].build(outcomes[3])
	_, err = c.Charge(context.Background(), goodReq)
	var be *VendorBError
	if !errors.As(err, &be) || be.Type != "rate_limit" {
		t.Errorf("vendor B: errors.As(%v, *VendorBError) failed", err)
	}
	c, _ = harnesses[0].build(outcomes[6])
	if _, err = c.Charge(context.Background(), goodReq); !errors.Is(err, conn) {
		t.Errorf("transport error cause lost: errors.Is(%v, conn) is false", err)
	}
}

func TestContract_InvalidRequestsNeverReachTheVendor(t *testing.T) {
	bad := map[string]ChargeRequest{
		"empty key":          {IdempotencyKey: "", AmountMinor: 100, Currency: "USD"},
		"zero amount":        {IdempotencyKey: "k", AmountMinor: 0, Currency: "USD"},
		"negative amount":    {IdempotencyKey: "k", AmountMinor: -100, Currency: "USD"},
		"lower-case ccy":     {IdempotencyKey: "k", AmountMinor: 100, Currency: "usd"},
		"two-letter ccy":     {IdempotencyKey: "k", AmountMinor: 100, Currency: "US"},
		"four-letter ccy":    {IdempotencyKey: "k", AmountMinor: 100, Currency: "USDD"},
		"digits in ccy":      {IdempotencyKey: "k", AmountMinor: 100, Currency: "U5D"},
		"empty ccy":          {IdempotencyKey: "k", AmountMinor: 100, Currency: ""},
		"ccy with a newline": {IdempotencyKey: "k", AmountMinor: 100, Currency: "US\n"},
	}
	for _, h := range harnesses {
		for name, req := range bad {
			t.Run(h.name+"/"+name, func(t *testing.T) {
				c, calls := h.build(outcomes[0])
				_, err := c.Charge(context.Background(), req)
				if !errors.Is(err, ErrInvalidRequest) {
					t.Errorf("err = %v, want ErrInvalidRequest", err)
				}
				if n := calls(); n != 0 {
					t.Errorf("vendor called %d times for an invalid request; validate before spending a vendor call", n)
				}
			})
		}
	}
}

func TestContract_AlreadyCancelledContextDoesNotCallTheVendor(t *testing.T) {
	for _, h := range harnesses {
		c, calls := h.build(outcomes[0])
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := c.Charge(ctx, goodReq)
		if !errors.Is(err, context.Canceled) {
			t.Errorf("%s: err = %v, want context.Canceled", h.name, err)
		}
		if errors.Is(err, ErrUnavailable) || errors.Is(err, ErrInvalidRequest) {
			t.Errorf("%s: a cancelled caller is not a vendor outage or a bad request: %v", h.name, err)
		}
		if n := calls(); n != 0 {
			t.Errorf("%s: vendor called %d times with a dead context", h.name, n)
		}
	}
}

func TestContract_ContextDeadlineReachesTheVendorCall(t *testing.T) {
	for _, h := range harnesses {
		t.Run(h.name, func(t *testing.T) {
			release := make(chan struct{})
			defer close(release)
			c, calls := h.hang(release)
			ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
			defer cancel()

			errc := make(chan error, 1)
			go func() { _, err := c.Charge(ctx, goodReq); errc <- err }()
			select {
			case err := <-errc:
				if !errors.Is(err, context.DeadlineExceeded) {
					t.Errorf("err = %v, want context.DeadlineExceeded", err)
				}
				if errors.Is(err, ErrUnavailable) {
					t.Errorf("a caller-side timeout was reported as ErrUnavailable (%v): it would trip breakers and page the vendor team", err)
				}
				if calls() != 1 {
					t.Errorf("vendor calls = %d, want 1", calls())
				}
			case <-time.After(testTimeout):
				t.Fatal("Charge did not return after its context expired: the adapter must pass ctx to the vendor client")
			}
		})
	}
}

func TestContract_IdempotencyKeyIsForwardedAsTheVendorReference(t *testing.T) {
	fa := &fakeA{reply: VendorAReply{TxnID: "t", Code: "00"}}
	if _, err := NewVendorA(fa).Charge(context.Background(), goodReq); err != nil {
		t.Fatal(err)
	}
	if fa.first(t).Ref != "idem-42" {
		t.Errorf("vendor A Ref = %q, want the idempotency key idem-42 (a retried charge would double-charge)", fa.first(t).Ref)
	}
	fb := &fakeB{res: VendorBResult{ID: "t", State: "succeeded"}}
	if _, err := NewVendorB(fb).Charge(context.Background(), goodReq); err != nil {
		t.Fatal(err)
	}
	if fb.first(t).IdempotencyKey != "idem-42" {
		t.Errorf("vendor B IdempotencyKey = %q, want idem-42", fb.first(t).IdempotencyKey)
	}
}

func TestContract_AdaptersAreInterchangeableBehindThePort(t *testing.T) {
	// Domain code written against Charger only. It must work with either vendor.
	chargeTwice := func(c Charger) (ChargeResult, error) {
		if _, err := c.Charge(context.Background(), goodReq); err != nil {
			return ChargeResult{}, err
		}
		return c.Charge(context.Background(), ChargeRequest{IdempotencyKey: "idem-43", AmountMinor: 99, Currency: "EUR"})
	}
	for _, h := range harnesses {
		c, calls := h.build(outcomes[0])
		res, err := chargeTwice(c)
		if err != nil || res.Status != Approved || calls() != 2 {
			t.Errorf("%s: chargeTwice = %+v, %v after %d vendor calls", h.name, res, err, calls())
		}
	}
}

// ---- vendor A specifics -------------------------------------------------------

func TestVendorA_FormatsMinorUnitsAsDecimalStringsWithoutFloats(t *testing.T) {
	cases := []struct {
		minor int64
		ccy   string
		want  string
	}{
		{1250, "USD", "12.50"},
		{5, "USD", "0.05"},
		{100, "EUR", "1.00"},
		{99, "GBP", "0.99"},
		{500, "JPY", "500"},
		{1234, "KWD", "1.234"},
		{7, "KWD", "0.007"},
		{9007199254740993, "USD", "90071992547409.93"}, // 2^53 + 1: a float64 cannot hold this
	}
	for _, c := range cases {
		f := &fakeA{reply: VendorAReply{TxnID: "t", Code: "00"}}
		_, err := NewVendorA(f).Charge(context.Background(), ChargeRequest{IdempotencyKey: "k", AmountMinor: c.minor, Currency: c.ccy})
		if err != nil {
			t.Fatalf("%d %s: %v", c.minor, c.ccy, err)
		}
		if got := f.first(t).Amount; got != c.want {
			t.Errorf("%d minor units of %s sent as %q, want %q", c.minor, c.ccy, got, c.want)
		}
	}
}

func TestVendorA_SendsLowerCaseCurrency(t *testing.T) {
	f := &fakeA{reply: VendorAReply{TxnID: "t", Code: "00"}}
	_, _ = NewVendorA(f).Charge(context.Background(), goodReq)
	if f.first(t).Ccy != "usd" {
		t.Errorf("Ccy = %q, want usd (vendor A rejects upper case)", f.first(t).Ccy)
	}
}

func TestVendorA_ResponseCodeTable(t *testing.T) {
	cases := []struct {
		code       string
		wantStatus Status
		wantErr    error
	}{
		{"00", Approved, nil},
		{"05", Declined, nil},
		{"51", Declined, nil},
		{"09", Pending, nil},
		{"91", "", ErrUnavailable},
		{"96", "", ErrUnavailable},
		{"", "", ErrUnknownResponse},
		{"0", "", ErrUnknownResponse},
		{"99", "", ErrUnknownResponse},
	}
	for _, c := range cases {
		f := &fakeA{reply: VendorAReply{TxnID: "t", Code: c.code}}
		got, err := NewVendorA(f).Charge(context.Background(), goodReq)
		if c.wantErr != nil {
			if !errors.Is(err, c.wantErr) {
				t.Errorf("code %q: err = %v, want %v", c.code, err, c.wantErr)
			}
			continue
		}
		if err != nil || got.Status != c.wantStatus {
			t.Errorf("code %q: %+v, %v; want status %q", c.code, got, err, c.wantStatus)
		}
	}
}

func TestVendorA_HTTPStatusTable(t *testing.T) {
	cases := map[int]error{
		400: ErrRejected, 404: ErrRejected, 409: ErrRejected, 422: ErrRejected,
		429: ErrRateLimited,
		500: ErrUnavailable, 502: ErrUnavailable, 503: ErrUnavailable, 504: ErrUnavailable,
		301: ErrUnavailable,
	}
	for status, want := range cases {
		f := &fakeA{err: &VendorAError{HTTPStatus: status}}
		_, err := NewVendorA(f).Charge(context.Background(), goodReq)
		if !errors.Is(err, want) {
			t.Errorf("HTTP %d: err = %v, want %v", status, err, want)
		}
	}
}

// ---- vendor B specifics -------------------------------------------------------

func TestVendorB_PassesMinorUnitsAndUpperCaseCurrencyThrough(t *testing.T) {
	f := &fakeB{res: VendorBResult{ID: "t", State: "succeeded"}}
	_, err := NewVendorB(f).Charge(context.Background(), ChargeRequest{IdempotencyKey: "k", AmountMinor: 1250, Currency: "USD"})
	if err != nil {
		t.Fatal(err)
	}
	if got := f.first(t); got.Amount != 1250 || got.Currency != "USD" {
		t.Errorf("sent %+v, want Amount 1250 and Currency USD unchanged", got)
	}
}

func TestVendorB_UnknownErrorTypeIsAnUnknownResponse(t *testing.T) {
	f := &fakeB{err: &VendorBError{Type: "quantum_flux"}}
	_, err := NewVendorB(f).Charge(context.Background(), goodReq)
	if !errors.Is(err, ErrUnknownResponse) {
		t.Errorf("err = %v, want ErrUnknownResponse", err)
	}
}

func TestAdapters_AreSafeForConcurrentUse(t *testing.T) {
	fa := &fakeA{reply: VendorAReply{TxnID: "t", Code: "00"}}
	fb := &fakeB{res: VendorBResult{ID: "t", State: "succeeded"}}
	var wg sync.WaitGroup
	for _, c := range []Charger{NewVendorA(fa), NewVendorB(fb)} {
		for g := 0; g < 8; g++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for i := 0; i < 100; i++ {
					if res, err := c.Charge(context.Background(), goodReq); err != nil || res.Status != Approved {
						t.Errorf("Charge = %+v, %v", res, err)
						return
					}
				}
			}()
		}
	}
	wg.Wait()
	if fa.count() != 800 || fb.count() != 800 {
		t.Errorf("vendor calls = %d and %d, want 800 each", fa.count(), fb.count())
	}
}
