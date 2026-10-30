package orderbook

import (
	"errors"
	"reflect"
	"testing"
)

func mkt(id string, side Side, qty int64) Order {
	return Order{ID: id, Side: side, Type: Market, Qty: qty}
}

func withTIF(o Order, tif TIF) Order { o.TIF = tif; return o }

func postOnly(o Order) Order { o.PostOnly = true; return o }

func depthOf(b *Book) ([]Level, []Level) { return b.Depth(0) }

func TestStage2_MarketOrderSweepsAndNeverRests(t *testing.T) {
	b := NewBook("ABC")
	mustSubmit(t, b, sell("s1", 100, 3))
	mustSubmit(t, b, sell("s2", 105, 3))
	mustSubmit(t, b, sell("s3", 250, 3))
	wantTrades(t, mustSubmit(t, b, mkt("m1", Buy, 8)),
		tr{100, 3, "m1", "s1"}, tr{105, 3, "m1", "s2"}, tr{250, 2, "m1", "s3"})
	wantDepth(t, b, nil, []Level{lv(250, 1, 1)})

	// More than the book holds: the unfilled remainder is DROPPED, not rested.
	wantTrades(t, mustSubmit(t, b, mkt("m2", Buy, 10)), tr{250, 1, "m2", "s3"})
	wantDepth(t, b, nil, nil)
	if _, ok := b.Lookup("m2"); ok {
		t.Fatal("a market order's remainder must never rest in the book")
	}
	// Empty opposite side: no trades, no error, nothing rests.
	wantTrades(t, mustSubmit(t, b, mkt("m3", Sell, 5)))
	wantDepth(t, b, nil, nil)
}

func TestStage2_MarketOrderValidation(t *testing.T) {
	b := NewBook("ABC")
	mustSubmit(t, b, sell("s1", 100, 3))
	m := mkt("m1", Buy, 1)
	m.Price = 100
	_, err := b.Submit(m)
	wantErr(t, "market order with a price", err, ErrInvalidOrder)
	p := postOnly(mkt("m2", Buy, 1))
	_, err = b.Submit(p)
	wantErr(t, "post-only market order", err, ErrInvalidOrder)
	_, err = b.Submit(postOnly(withTIF(buy("x", 99, 1), IOC)))
	wantErr(t, "post-only IOC order", err, ErrInvalidOrder)
	_, err = b.Submit(withTIF(buy("y", 99, 1), TIF(9)))
	wantErr(t, "unknown time in force", err, ErrInvalidOrder)
	wantDepth(t, b, nil, []Level{lv(100, 3, 1)})
}

func TestStage2_IOCTradesWhatItCanAndDropsTheRest(t *testing.T) {
	b := NewBook("ABC")
	mustSubmit(t, b, sell("s1", 100, 4))
	wantTrades(t, mustSubmit(t, b, withTIF(buy("i1", 100, 10), IOC)), tr{100, 4, "i1", "s1"})
	wantDepth(t, b, nil, nil)
	if _, ok := b.Lookup("i1"); ok {
		t.Fatal("IOC remainder must not rest")
	}
	// Not marketable at all: nothing happens, nothing rests.
	mustSubmit(t, b, sell("s2", 105, 4))
	wantTrades(t, mustSubmit(t, b, withTIF(buy("i2", 100, 3), IOC)))
	wantDepth(t, b, nil, []Level{lv(105, 4, 1)})
}

func TestStage2_FOKFillsCompletelyOrDoesNothing(t *testing.T) {
	b := NewBook("ABC")
	mustSubmit(t, b, sell("s1", 100, 5))
	mustSubmit(t, b, sell("s2", 101, 5))
	mustSubmit(t, b, sell("s3", 103, 5)) // beyond the limit used below

	bidsBefore, asksBefore := depthOf(b)
	ts, err := b.Submit(withTIF(buy("f1", 102, 11), FOK)) // only 10 lots at <= 102
	wantErr(t, "FOK that cannot be filled", err, ErrNotFillable)
	if len(ts) != 0 {
		t.Fatalf("a killed FOK produced trades %+v", ts)
	}
	bidsAfter, asksAfter := depthOf(b)
	if !reflect.DeepEqual(bidsBefore, bidsAfter) || !reflect.DeepEqual(asksBefore, asksAfter) {
		t.Fatalf("a killed FOK changed the book: %+v -> %+v (check feasibility BEFORE mutating)", asksBefore, asksAfter)
	}
	if o, _ := b.Lookup("s1"); o.Qty != 5 {
		t.Fatalf("resting s1 was partially consumed by a killed FOK: qty %d", o.Qty)
	}

	wantTrades(t, mustSubmit(t, b, withTIF(buy("f2", 102, 10), FOK)),
		tr{100, 5, "f2", "s1"}, tr{101, 5, "f2", "s2"})
	wantDepth(t, b, nil, []Level{lv(103, 5, 1)})

	// A market FOK needs liquidity at any price.
	wantTrades(t, mustSubmit(t, b, withTIF(mkt("f3", Buy, 5), FOK)), tr{103, 5, "f3", "s3"})
	_, err = b.Submit(withTIF(mkt("f4", Buy, 1), FOK))
	wantErr(t, "market FOK on an empty side", err, ErrNotFillable)
}

func TestStage2_PostOnlyIsRejectedIfItWouldTrade(t *testing.T) {
	b := NewBook("ABC")
	mustSubmit(t, b, sell("s1", 100, 5))
	mustSubmit(t, b, buy("b1", 98, 5))

	ts, err := b.Submit(postOnly(buy("p1", 100, 1))) // equal to the best ask: would trade
	wantErr(t, "post-only at the best ask", err, ErrWouldCross)
	if len(ts) != 0 {
		t.Fatalf("rejected post-only produced trades %+v", ts)
	}
	_, err = b.Submit(postOnly(buy("p2", 105, 1)))
	wantErr(t, "post-only through the best ask", err, ErrWouldCross)
	_, err = b.Submit(postOnly(sell("p3", 97, 1)))
	wantErr(t, "post-only sell through the best bid", err, ErrWouldCross)
	if _, ok := b.Lookup("p1"); ok {
		t.Fatal("rejected post-only order is resting")
	}

	wantTrades(t, mustSubmit(t, b, postOnly(buy("p4", 99, 2))))
	wantDepth(t, b, []Level{lv(99, 2, 1), lv(98, 5, 1)}, []Level{lv(100, 5, 1)})
}

func TestStage2_AmendDownKeepsTimePriority(t *testing.T) {
	b := NewBook("ABC")
	mustSubmit(t, b, sell("A", 100, 5))
	mustSubmit(t, b, sell("B", 100, 5))
	mustSubmit(t, b, sell("C", 100, 5))
	ts, err := b.Amend("A", 2, 100)
	if err != nil || len(ts) != 0 {
		t.Fatalf("Amend(A, 2, 100) = %+v, %v", ts, err)
	}
	if o, _ := b.Lookup("A"); o.Qty != 2 {
		t.Fatalf("A has qty %d after amending down to 2", o.Qty)
	}
	wantDepth(t, b, nil, []Level{lv(100, 12, 3)})
	wantTrades(t, mustSubmit(t, b, buy("x", 100, 4)), tr{100, 2, "x", "A"}, tr{100, 2, "x", "B"})
}

func TestStage2_AmendUpOrToANewPriceLosesTimePriority(t *testing.T) {
	b := NewBook("ABC")
	mustSubmit(t, b, sell("A", 100, 2))
	mustSubmit(t, b, sell("B", 100, 2))
	mustSubmit(t, b, sell("C", 100, 2))
	if _, err := b.Amend("A", 3, 100); err != nil { // bigger: goes to the back
		t.Fatal(err)
	}
	wantDepth(t, b, nil, []Level{lv(100, 7, 3)})
	wantTrades(t, mustSubmit(t, b, buy("x", 100, 4)), tr{100, 2, "x", "B"}, tr{100, 2, "x", "C"})
	wantTrades(t, mustSubmit(t, b, buy("y", 100, 3)), tr{100, 3, "y", "A"})

	// A price change re-enters the order behind everything at the new level.
	mustSubmit(t, b, sell("D", 101, 2))
	mustSubmit(t, b, sell("E", 102, 2))
	if _, err := b.Amend("E", 2, 101); err != nil {
		t.Fatal(err)
	}
	wantDepth(t, b, nil, []Level{lv(101, 4, 2)})
	wantTrades(t, mustSubmit(t, b, buy("z", 101, 3)), tr{101, 2, "z", "D"}, tr{101, 1, "z", "E"})
}

func TestStage2_AmendToACrossingPriceTradesImmediately(t *testing.T) {
	b := NewBook("ABC")
	mustSubmit(t, b, sell("s1", 100, 3))
	mustSubmit(t, b, buy("b1", 95, 5))
	ts, err := b.Amend("b1", 5, 101)
	if err != nil {
		t.Fatalf("Amend = %v", err)
	}
	wantTrades(t, ts, tr{100, 3, "b1", "s1"})
	wantDepth(t, b, []Level{lv(101, 2, 1)}, nil)
	if o, _ := b.Lookup("b1"); o.Qty != 2 || o.Price != 101 {
		t.Fatalf("b1 = %+v, want 2 lots resting at 101", o)
	}
}

func TestStage2_AmendNoOpErrorsAndPostOnly(t *testing.T) {
	b := NewBook("ABC")
	mustSubmit(t, b, sell("A", 100, 2))
	mustSubmit(t, b, sell("B", 100, 2))
	if ts, err := b.Amend("A", 2, 100); err != nil || len(ts) != 0 {
		t.Fatalf("no-op Amend = %+v, %v", ts, err)
	}
	wantTrades(t, mustSubmit(t, b, buy("x", 100, 1)), tr{100, 1, "x", "A"}) // A kept its place
	if _, err := b.Amend("nope", 1, 100); !isErr(err, ErrUnknownOrder) {
		t.Fatalf("Amend of unknown id = %v, want ErrUnknownOrder", err)
	}
	if _, err := b.Amend("A", 0, 100); !isErr(err, ErrInvalidOrder) {
		t.Fatalf("Amend to qty 0 = %v, want ErrInvalidOrder (use Cancel)", err)
	}
	if _, err := b.Amend("A", 1, 0); !isErr(err, ErrInvalidOrder) {
		t.Fatalf("Amend to price 0 = %v, want ErrInvalidOrder", err)
	}
	// Amend's quantity is the new REMAINING quantity of a partly filled order.
	if o, _ := b.Lookup("A"); o.Qty != 1 {
		t.Fatalf("A has %d left, want 1", o.Qty)
	}
	if _, err := b.Amend("A", 3, 100); err != nil {
		t.Fatal(err)
	}
	if o, _ := b.Lookup("A"); o.Qty != 3 {
		t.Fatalf("A has %d after amending the remainder to 3", o.Qty)
	}

	// A post-only order must not be amended into a trade; it stays untouched.
	mustSubmit(t, b, postOnly(buy("po", 90, 4)))
	_, err := b.Amend("po", 4, 100)
	wantErr(t, "Amend of a post-only order to a crossing price", err, ErrWouldCross)
	if o, ok := b.Lookup("po"); !ok || o.Price != 90 || o.Qty != 4 {
		t.Fatalf("po = %+v, %v; a rejected amend must leave the order as it was", o, ok)
	}
}

func isErr(err, target error) bool { return errors.Is(err, target) }

func TestStage2_SelfTradePreventionCancelIncoming(t *testing.T) {
	b := NewBook("ABC")
	b.SetSelfTradePolicy(STPCancelIncoming)
	mustSubmit(t, b, owned(sell("o1", 100, 2), "bob"))    // other owner, best priority
	mustSubmit(t, b, owned(sell("own", 100, 5), "alice")) // alice's own, behind bob's
	mustSubmit(t, b, owned(sell("o2", 101, 5), "bob"))
	ts := mustSubmit(t, b, owned(buy("a1", 102, 9), "alice"))
	wantTrades(t, ts, tr{100, 2, "a1", "o1"}) // trades before the self-match stand
	if _, ok := b.Lookup("a1"); ok {
		t.Fatal("the remainder of the incoming order must be cancelled, not rested")
	}
	wantDepth(t, b, nil, []Level{lv(100, 5, 1), lv(101, 5, 1)}) // resting orders untouched
	if o, ok := b.Lookup("own"); !ok || o.Qty != 5 {
		t.Fatalf("resting own order = %+v, %v; it must be untouched", o, ok)
	}
}

func TestStage2_SelfTradePreventionCancelResting(t *testing.T) {
	b := NewBook("ABC")
	b.SetSelfTradePolicy(STPCancelResting)
	mustSubmit(t, b, owned(sell("own", 100, 5), "alice"))
	mustSubmit(t, b, owned(sell("o1", 100, 2), "bob"))
	mustSubmit(t, b, owned(sell("o2", 101, 5), "bob"))
	ts := mustSubmit(t, b, owned(buy("a1", 101, 6), "alice"))
	wantTrades(t, ts, tr{100, 2, "a1", "o1"}, tr{101, 4, "a1", "o2"})
	if _, ok := b.Lookup("own"); ok {
		t.Fatal("the resting self-order should have been cancelled")
	}
	wantDepth(t, b, nil, []Level{lv(101, 1, 1)})
}

func TestStage2_SelfTradesAreAllowedByDefaultAndForAnonymousOrders(t *testing.T) {
	b := NewBook("ABC")
	mustSubmit(t, b, owned(sell("s1", 100, 2), "alice"))
	wantTrades(t, mustSubmit(t, b, owned(buy("b1", 100, 2), "alice")), tr{100, 2, "b1", "s1"})

	b.SetSelfTradePolicy(STPCancelIncoming)
	mustSubmit(t, b, sell("s2", 100, 2)) // empty owner means "no owner": never a self-trade
	wantTrades(t, mustSubmit(t, b, buy("b2", 100, 2)), tr{100, 2, "b2", "s2"})
}

func TestStage2_FOKRespectsSelfTradePrevention(t *testing.T) {
	b := NewBook("ABC")
	b.SetSelfTradePolicy(STPCancelIncoming)
	mustSubmit(t, b, owned(sell("own", 100, 5), "alice"))
	mustSubmit(t, b, owned(sell("o1", 101, 5), "bob"))
	// Matching would stop at alice's own order, so 5 lots can never be filled.
	_, err := b.Submit(withTIF(owned(buy("f1", 101, 5), "alice"), FOK))
	wantErr(t, "FOK blocked by own resting order", err, ErrNotFillable)
	wantDepth(t, b, nil, []Level{lv(100, 5, 1), lv(101, 5, 1)})
}
