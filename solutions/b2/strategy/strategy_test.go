package strategy

import (
	"errors"
	"fmt"
	"math"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

const testTimeout = 5 * time.Second

func mustFee(t *testing.T, f FeeFunc, amount Money) Money {
	t.Helper()
	got, err := f(Txn{Amount: amount})
	if err != nil {
		t.Fatalf("fee for amount %d returned error %v", amount, err)
	}
	return got
}

func TestFlat_IgnoresAmount(t *testing.T) {
	f := Flat(1500)
	for _, amt := range []Money{0, 1, 99999, 1 << 40} {
		if got := mustFee(t, f, amt); got != 1500 {
			t.Errorf("Flat(1500) on amount %d = %d, want 1500", amt, got)
		}
	}
}

func TestPercent_RoundsHalfUpInMinorUnits(t *testing.T) {
	cases := []struct {
		name     string
		bps      int64
		amount   Money
		want     Money
		min, max Money
	}{
		{"1.5% of 123.45 rounds down", 150, 12345, 185, 0, 0},
		{"exact half rounds up (0.5 -> 1)", 1000, 5, 1, 0, 0},
		{"below half rounds down (0.4 -> 0)", 1000, 4, 0, 0, 0},
		{"1.5 rounds up to 2", 1000, 15, 2, 0, 0},
		{"exact result", 25, 100000, 250, 0, 0},
		{"floor applies", 100, 1000, 50, 50, 0},
		{"floor not applied when fee is above it", 100, 10000, 100, 50, 0},
		{"cap applies", 100, 100000, 200, 0, 200},
		{"cap not applied when fee is below it", 100, 10000, 100, 0, 200},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := mustFee(t, Percent(c.bps, c.min, c.max), c.amount); got != c.want {
				t.Errorf("Percent(%d bps, min %d, max %d) of %d = %d, want %d", c.bps, c.min, c.max, c.amount, got, c.want)
			}
		})
	}
}

func TestPercent_OverflowIsAnErrorNotAWrapAround(t *testing.T) {
	_, err := Percent(10000, 0, 0)(Txn{Amount: Money(math.MaxInt64)})
	if !errors.Is(err, ErrOverflow) {
		t.Fatalf("100%% of MaxInt64 returned err=%v, want ErrOverflow (a wrapped-around negative fee would pay the customer)", err)
	}
}

func TestTiered_IsMarginalNotWholeAmount(t *testing.T) {
	f, err := Tiered([]Tier{{UpTo: 100000, BPS: 100}, {UpTo: 500000, BPS: 50}, {UpTo: 0, BPS: 25}})
	if err != nil {
		t.Fatalf("Tiered returned %v for a valid table", err)
	}
	cases := []struct{ amount, want Money }{
		{0, 0},
		{50000, 500},
		{100000, 1000},
		{100001, 1000}, // one cent into band 2 is worth 0.005 cents
		{500000, 3000},
		{600000, 3250}, // 1000 + 2000 + 250
	}
	for _, c := range cases {
		if got := mustFee(t, f, c.amount); got != c.want {
			t.Errorf("Tiered fee for %d = %d, want %d (each band charged at its own rate)", c.amount, got, c.want)
		}
	}
}

func TestTiered_FeeNeverDecreasesAsAmountGrows(t *testing.T) {
	// A "whole amount at the band's rate" implementation has a cliff where the
	// rate drops: 1,000.00 costs more than 1,000.01. Marginal tiers cannot.
	f, err := Tiered([]Tier{{UpTo: 100000, BPS: 100}, {UpTo: 500000, BPS: 50}, {UpTo: 0, BPS: 25}})
	if err != nil {
		t.Fatal(err)
	}
	var prev Money
	for a := Money(0); a <= 700000; a += 997 {
		got := mustFee(t, f, a)
		if got < prev {
			t.Fatalf("fee fell from %d to %d when the amount rose to %d: tiers must be marginal", prev, got, a)
		}
		prev = got
	}
}

func TestTiered_RoundsOnceAtTheEnd(t *testing.T) {
	f, err := Tiered([]Tier{{UpTo: 150, BPS: 30}, {UpTo: 0, BPS: 30}})
	if err != nil {
		t.Fatal(err)
	}
	// 300 * 30bps = 0.9 -> 1. Rounding each 150-cent band separately gives 0.45 -> 0 twice.
	if got := mustFee(t, f, 300); got != 1 {
		t.Errorf("fee = %d, want 1: sum the exact band products and round half up once", got)
	}
}

func TestTiered_RejectsMalformedTables(t *testing.T) {
	bad := map[string][]Tier{
		"empty":                 nil,
		"not ascending":         {{UpTo: 500, BPS: 1}, {UpTo: 100, BPS: 1}, {UpTo: 0, BPS: 1}},
		"duplicate bound":       {{UpTo: 100, BPS: 1}, {UpTo: 100, BPS: 1}, {UpTo: 0, BPS: 1}},
		"last tier is bounded":  {{UpTo: 100, BPS: 1}, {UpTo: 500, BPS: 1}},
		"negative rate":         {{UpTo: 100, BPS: -1}, {UpTo: 0, BPS: 1}},
		"unbounded tier first":  {{UpTo: 0, BPS: 1}, {UpTo: 500, BPS: 1}},
		"non-positive UpTo mid": {{UpTo: -5, BPS: 1}, {UpTo: 0, BPS: 1}},
	}
	for name, tiers := range bad {
		if _, err := Tiered(tiers); !errors.Is(err, ErrInvalidStrategy) {
			t.Errorf("%s: Tiered returned err=%v, want ErrInvalidStrategy", name, err)
		}
	}
	if _, err := Tiered([]Tier{{UpTo: 0, BPS: 50}}); err != nil {
		t.Errorf("a single unbounded tier is valid, got %v", err)
	}
}

func TestTiered_CopiesTheTable(t *testing.T) {
	tiers := []Tier{{UpTo: 1000, BPS: 100}, {UpTo: 0, BPS: 50}}
	f, err := Tiered(tiers)
	if err != nil {
		t.Fatal(err)
	}
	before := mustFee(t, f, 5000)
	tiers[0].BPS = 9999 // the caller edits its slice later
	if after := mustFee(t, f, 5000); after != before {
		t.Errorf("fee changed from %d to %d after the caller mutated its slice: copy the table in Tiered", before, after)
	}
}

func TestTiered_OverflowIsAnError(t *testing.T) {
	f, err := Tiered([]Tier{{UpTo: 0, BPS: 10000}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f(Txn{Amount: Money(math.MaxInt64)}); !errors.Is(err, ErrOverflow) {
		t.Fatalf("err=%v, want ErrOverflow", err)
	}
}

func TestSum_AddsAndStopsOnFirstError(t *testing.T) {
	if got := mustFee(t, Sum(Flat(100), Percent(100, 0, 0)), 10000); got != 200 {
		t.Errorf("Sum(Flat(100), 1%% of 10000) = %d, want 200", got)
	}
	boom := errors.New("boom")
	called := false
	_, err := Sum(Flat(1), func(Txn) (Money, error) { return 0, boom }, func(Txn) (Money, error) {
		called = true
		return 0, nil
	})(Txn{})
	if !errors.Is(err, boom) {
		t.Errorf("Sum err=%v, want boom", err)
	}
	if called {
		t.Error("Sum kept evaluating strategies after an error")
	}
}

// overdraft is a brand-new fee type that exists ONLY in this test file. The
// engine must be able to serve it without any change: that is the point of
// the pattern. Its method value is the strategy.
type overdraft struct {
	freeBelow Money
	perDay    Money
}

func (o overdraft) fee(t Txn) (Money, error) {
	if t.Amount <= o.freeBelow {
		return 0, nil
	}
	return o.perDay, nil
}

func TestEngine_NewFeeTypeNeedsOnlyNewCode(t *testing.T) {
	e := NewEngine()
	od := overdraft{freeBelow: 5000, perDay: 3500}
	if err := e.Register("overdraft", od.fee); err != nil {
		t.Fatal(err)
	}
	if err := e.Register("wire", Percent(25, 1500, 4500)); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		kind   string
		amount Money
		want   Money
	}{
		{"overdraft", 4000, 0},
		{"overdraft", 5001, 3500},
		{"wire", 100000, 1500}, // 0.25% = 250, floored to 1500
		{"wire", 10000000, 4500},
	} {
		got, err := e.Fee(Txn{Kind: c.kind, Amount: c.amount})
		if err != nil || got != c.want {
			t.Errorf("Fee(%s, %d) = %d, %v; want %d", c.kind, c.amount, got, err, c.want)
		}
	}
	if got, want := e.Kinds(), []string{"overdraft", "wire"}; !reflect.DeepEqual(got, want) {
		t.Errorf("Kinds() = %v, want sorted %v", got, want)
	}
}

func TestEngine_ErrorsCarryTheKindAndKeepTheCause(t *testing.T) {
	e := NewEngine()
	_, err := e.Fee(Txn{Kind: "crypto_swap", Amount: 10})
	if !errors.Is(err, ErrUnknownKind) {
		t.Fatalf("unknown kind: err=%v, want ErrUnknownKind", err)
	}
	if msg := err.Error(); !strings.Contains(msg, "crypto_swap") {
		t.Errorf("error %q does not name the unknown kind; on-call needs it", msg)
	}

	cause := errors.New("rate table unavailable")
	_ = e.Register("fx", func(Txn) (Money, error) { return 0, cause })
	_, err = e.Fee(Txn{Kind: "fx", Amount: 10})
	if !errors.Is(err, cause) {
		t.Errorf("strategy error was not wrapped with %%w: errors.Is(%v, cause) is false", err)
	}
	if !strings.Contains(err.Error(), "fx") {
		t.Errorf("error %q does not name the kind", err)
	}

	if _, err = e.Fee(Txn{Kind: "fx", Amount: -1}); !errors.Is(err, ErrInvalidAmount) {
		t.Errorf("negative amount: err=%v, want ErrInvalidAmount", err)
	}
}

func TestEngine_RejectsNegativeFeesFromStrategies(t *testing.T) {
	e := NewEngine()
	_ = e.Register("buggy", func(Txn) (Money, error) { return -500, nil })
	if _, err := e.Fee(Txn{Kind: "buggy", Amount: 10}); !errors.Is(err, ErrInvalidFee) {
		t.Fatalf("err=%v, want ErrInvalidFee: a strategy bug must not credit the customer", err)
	}
}

func TestEngine_RegisterValidationAndReplace(t *testing.T) {
	e := NewEngine()
	if err := e.Register("", Flat(1)); !errors.Is(err, ErrInvalidStrategy) {
		t.Errorf("empty kind: err=%v, want ErrInvalidStrategy", err)
	}
	if err := e.Register("x", nil); !errors.Is(err, ErrInvalidStrategy) {
		t.Errorf("nil func: err=%v, want ErrInvalidStrategy", err)
	}
	_ = e.Register("x", Flat(1))
	_ = e.Register("x", Flat(2)) // a new fee schedule replaces the old one
	if got, _ := e.Fee(Txn{Kind: "x", Amount: 5}); got != 2 {
		t.Errorf("after re-registering, fee = %d, want 2", got)
	}
}

func TestEngine_StrategyRunsWithoutTheEngineLock(t *testing.T) {
	e := NewEngine()
	_ = e.Register("domestic", Flat(1500))
	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	releaseAll := func() { once.Do(func() { close(release) }) }
	defer releaseAll()
	_ = e.Register("international", func(t Txn) (Money, error) {
		close(entered)
		<-release
		base, err := e.Fee(Txn{Kind: "domestic", Amount: t.Amount}) // re-enters the engine
		return base + 2500, err
	})

	type result struct {
		fee Money
		err error
	}
	res := make(chan result, 1)
	go func() {
		fee, err := e.Fee(Txn{Kind: "international", Amount: 100})
		res <- result{fee, err}
	}()
	select {
	case <-entered:
	case r := <-res:
		t.Fatalf("Fee returned (%d, %v) without ever running the registered strategy", r.fee, r.err)
	case <-time.After(testTimeout):
		t.Fatal("strategy never started")
	}

	registered := make(chan struct{})
	go func() {
		_ = e.Register("another", Flat(1)) // a writer arrives while a strategy is running
		close(registered)
	}()
	select {
	case <-registered:
	case <-time.After(testTimeout):
		t.Fatal("Register blocked while a strategy was running: Fee must not hold the lock while it calls the strategy")
	}

	releaseAll()
	select {
	case r := <-res:
		if r.err != nil || r.fee != 4000 {
			t.Errorf("international fee = %d, %v; want 4000, nil", r.fee, r.err)
		}
	case <-time.After(testTimeout):
		t.Fatal("re-entrant Fee deadlocked: a waiting writer blocks a second RLock if the first one is still held")
	}
}

func TestEngine_ConcurrentSwapAndLookup(t *testing.T) {
	e := NewEngine()
	_ = e.Register("k", Flat(1))
	var wg sync.WaitGroup
	errc := make(chan error, 8)
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 2000; i++ {
				got, err := e.Fee(Txn{Kind: "k", Amount: 10})
				if err != nil || (got != 1 && got != 2) {
					errc <- fmt.Errorf("Fee = %d, %v; want 1 or 2 and no error", got, err)
					return
				}
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 2000; i++ {
			_ = e.Register("k", Flat(Money(1+i%2)))
			_ = e.Kinds()
		}
	}()
	wg.Wait()
	close(errc)
	for err := range errc {
		t.Error(err)
	}
}
