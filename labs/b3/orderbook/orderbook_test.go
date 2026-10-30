package orderbook

import (
	"errors"
	"fmt"
	"math/rand"
	"reflect"
	"testing"
	"time"
)

const testTimeout = 5 * time.Second

// within fails the test if fn does not return in time, so a deadlock or an
// endless loop in your implementation becomes a message instead of a hang.
func within(t *testing.T, what string, fn func()) {
	t.Helper()
	done := make(chan struct{})
	go func() { defer close(done); fn() }()
	select {
	case <-done:
	case <-time.After(testTimeout):
		t.Fatalf("%s did not finish within %v (deadlock, endless loop, or an operation that waits on a slow subscriber?)", what, testTimeout)
	}
}

func buy(id string, price, qty int64) Order { return Order{ID: id, Side: Buy, Price: price, Qty: qty} }
func sell(id string, price, qty int64) Order {
	return Order{ID: id, Side: Sell, Price: price, Qty: qty}
}

func owned(o Order, owner string) Order { o.Owner = owner; return o }

// tr is the part of a Trade the tests compare (everything except the bookkeeping fields).
type tr struct {
	price, qty   int64
	taker, maker string
}

func summarize(ts []Trade) []tr {
	out := make([]tr, 0, len(ts))
	for _, t := range ts {
		out = append(out, tr{t.Price, t.Qty, t.TakerID, t.MakerID})
	}
	return out
}

func mustSubmit(t *testing.T, b *Book, o Order) []Trade {
	t.Helper()
	ts, err := b.Submit(o)
	if err != nil {
		t.Fatalf("Submit(%+v) = %v, want nil", o, err)
	}
	return ts
}

func wantTrades(t *testing.T, got []Trade, want ...tr) {
	t.Helper()
	g := summarize(got)
	if len(g) == 0 && len(want) == 0 {
		return
	}
	if !reflect.DeepEqual(g, want) {
		t.Fatalf("trades = %+v\nwant     %+v\n(price, qty, taker, maker; trades execute at the RESTING order's price, best price first, oldest first)", g, want)
	}
}

func wantErr(t *testing.T, what string, got, want error) {
	t.Helper()
	if !errors.Is(got, want) {
		t.Fatalf("%s = %v, want an error satisfying errors.Is(err, %v)", what, got, want)
	}
}

func lv(price, qty int64, orders int) Level { return Level{Price: price, Qty: qty, Orders: orders} }

func wantDepth(t *testing.T, b *Book, bids, asks []Level) {
	t.Helper()
	gb, ga := b.Depth(0)
	if (len(gb) != 0 || len(bids) != 0) && !reflect.DeepEqual(gb, bids) {
		t.Fatalf("bids = %+v, want %+v (best price first)", gb, bids)
	}
	if (len(ga) != 0 || len(asks) != 0) && !reflect.DeepEqual(ga, asks) {
		t.Fatalf("asks = %+v, want %+v (best price first)", ga, asks)
	}
}

// checkShape verifies the public view of a book: sorted, non-empty levels and
// no crossed market. It returns a description of the first problem.
func checkShape(bids, asks []Level) error {
	for i, l := range bids {
		if l.Qty <= 0 || l.Orders <= 0 {
			return fmt.Errorf("empty bid level %+v", l)
		}
		if i > 0 && bids[i-1].Price <= l.Price {
			return fmt.Errorf("bids not strictly descending: %d then %d", bids[i-1].Price, l.Price)
		}
	}
	for i, l := range asks {
		if l.Qty <= 0 || l.Orders <= 0 {
			return fmt.Errorf("empty ask level %+v", l)
		}
		if i > 0 && asks[i-1].Price >= l.Price {
			return fmt.Errorf("asks not strictly ascending: %d then %d", asks[i-1].Price, l.Price)
		}
	}
	if len(bids) > 0 && len(asks) > 0 && bids[0].Price >= asks[0].Price {
		return fmt.Errorf("book is crossed: best bid %d >= best ask %d", bids[0].Price, asks[0].Price)
	}
	return nil
}

func totalQty(b *Book) int64 {
	bids, asks := b.Depth(0)
	var sum int64
	for _, l := range bids {
		sum += l.Qty
	}
	for _, l := range asks {
		sum += l.Qty
	}
	return sum
}

func sumQty(ts []Trade) int64 {
	var s int64
	for _, t := range ts {
		s += t.Qty
	}
	return s
}

// ---------------------------------------------------------------- Stage 1

func TestStage1_NonCrossingOrdersRestAndAggregate(t *testing.T) {
	b := NewBook("ABC")
	wantDepth(t, b, nil, nil)
	for _, o := range []Order{
		buy("b1", 99, 5), buy("b2", 99, 3), buy("b3", 98, 4),
		sell("s1", 101, 2), sell("s2", 102, 7), sell("s3", 101, 1),
	} {
		wantTrades(t, mustSubmit(t, b, o))
	}
	wantDepth(t, b,
		[]Level{lv(99, 8, 2), lv(98, 4, 1)},
		[]Level{lv(101, 3, 2), lv(102, 7, 1)})

	bids, asks := b.Depth(1)
	if len(bids) != 1 || len(asks) != 1 || bids[0].Price != 99 || asks[0].Price != 101 {
		t.Fatalf("Depth(1) = %+v / %+v, want only the best level of each side", bids, asks)
	}
	if o, ok := b.Lookup("b3"); !ok || o.Qty != 4 || o.Price != 98 || o.Side != Buy {
		t.Fatalf("Lookup(b3) = %+v, %v; want the resting order", o, ok)
	}
	if _, ok := b.Lookup("nope"); ok {
		t.Fatal("Lookup of an unknown id reported ok")
	}
}

func TestStage1_TradeExecutesAtRestingOrdersPrice(t *testing.T) {
	b := NewBook("ABC")
	mustSubmit(t, b, sell("s1", 100, 5))
	ts := mustSubmit(t, b, buy("b1", 105, 5))
	wantTrades(t, ts, tr{100, 5, "b1", "s1"})
	if ts[0].TakerSide != Buy || ts[0].Symbol != "ABC" {
		t.Fatalf("trade = %+v, want TakerSide=buy Symbol=ABC", ts[0])
	}
	wantDepth(t, b, nil, nil)

	mustSubmit(t, b, buy("b2", 100, 5))
	ts = mustSubmit(t, b, sell("s2", 95, 5))
	wantTrades(t, ts, tr{100, 5, "s2", "b2"})
	if ts[0].TakerSide != Sell {
		t.Fatalf("TakerSide = %v, want sell", ts[0].TakerSide)
	}
}

func TestStage1_PartialFillsRestTheRemainder(t *testing.T) {
	b := NewBook("ABC")
	mustSubmit(t, b, sell("s1", 100, 4))
	wantTrades(t, mustSubmit(t, b, buy("b1", 101, 10)), tr{100, 4, "b1", "s1"})
	wantDepth(t, b, []Level{lv(101, 6, 1)}, nil)
	if o, ok := b.Lookup("b1"); !ok || o.Qty != 6 || o.Price != 101 {
		t.Fatalf("Lookup(b1) = %+v, %v; want remaining qty 6 resting at its LIMIT price 101", o, ok)
	}

	// The other way round: the resting order is partly filled and keeps resting.
	mustSubmit(t, b, sell("s2", 105, 10))
	wantTrades(t, mustSubmit(t, b, buy("b2", 105, 4)), tr{105, 4, "b2", "s2"})
	wantDepth(t, b, []Level{lv(101, 6, 1)}, []Level{lv(105, 6, 1)})
	if o, _ := b.Lookup("s2"); o.Qty != 6 {
		t.Fatalf("resting s2 has qty %d, want 6", o.Qty)
	}
	if _, ok := b.Lookup("b2"); ok {
		t.Fatal("fully filled incoming order b2 must not rest")
	}
}

func TestStage1_BestPriceBeatsEarlierArrival(t *testing.T) {
	b := NewBook("ABC")
	mustSubmit(t, b, sell("s101", 101, 5)) // arrived first, worse price
	mustSubmit(t, b, sell("s100", 100, 5))
	mustSubmit(t, b, sell("s102", 102, 5))
	wantTrades(t, mustSubmit(t, b, buy("b1", 103, 7)),
		tr{100, 5, "b1", "s100"},
		tr{101, 2, "b1", "s101"})
	wantDepth(t, b, nil, []Level{lv(101, 3, 1), lv(102, 5, 1)})

	c := NewBook("ABC")
	mustSubmit(t, c, buy("b99", 99, 5)) // arrived first, worse price
	mustSubmit(t, c, buy("b100", 100, 5))
	wantTrades(t, mustSubmit(t, c, sell("s1", 98, 6)),
		tr{100, 5, "s1", "b100"},
		tr{99, 1, "s1", "b99"})
}

func TestStage1_FIFOWithinAPriceLevel(t *testing.T) {
	b := NewBook("ABC")
	mustSubmit(t, b, sell("A", 100, 3))
	mustSubmit(t, b, sell("B", 100, 3))
	mustSubmit(t, b, sell("C", 100, 3))
	wantTrades(t, mustSubmit(t, b, buy("x", 100, 5)),
		tr{100, 3, "x", "A"},
		tr{100, 2, "x", "B"})
	if o, ok := b.Lookup("B"); !ok || o.Qty != 1 {
		t.Fatalf("Lookup(B) = %+v, %v; want 1 remaining", o, ok)
	}
	wantDepth(t, b, nil, []Level{lv(100, 4, 2)})
	// B keeps its place at the FRONT after a partial fill.
	wantTrades(t, mustSubmit(t, b, buy("y", 100, 2)), tr{100, 1, "y", "B"}, tr{100, 1, "y", "C"})
}

func TestStage1_SweepsSeveralLevelsAndRespectsTheLimit(t *testing.T) {
	b := NewBook("ABC")
	mustSubmit(t, b, sell("s1", 100, 10))
	mustSubmit(t, b, sell("s2", 101, 10))
	mustSubmit(t, b, sell("s3", 102, 10))
	mustSubmit(t, b, sell("s4", 104, 10))
	wantTrades(t, mustSubmit(t, b, buy("b1", 103, 25)),
		tr{100, 10, "b1", "s1"}, tr{101, 10, "b1", "s2"}, tr{102, 5, "b1", "s3"})
	// 25 lots traded in full: nothing rests, and 104 is beyond the limit anyway.
	wantDepth(t, b, nil, []Level{lv(102, 5, 1), lv(104, 10, 1)})

	wantTrades(t, mustSubmit(t, b, buy("b2", 103, 20)), tr{102, 5, "b2", "s3"})
	wantDepth(t, b, []Level{lv(103, 15, 1)}, []Level{lv(104, 10, 1)})
}

func TestStage1_CancelRemovesOnlyThatOrder(t *testing.T) {
	b := NewBook("ABC")
	mustSubmit(t, b, sell("A", 100, 3))
	mustSubmit(t, b, sell("B", 100, 3))
	mustSubmit(t, b, sell("C", 100, 3))
	mustSubmit(t, b, sell("D", 101, 2))
	if err := b.Cancel("B"); err != nil {
		t.Fatalf("Cancel(B) = %v", err)
	}
	wantDepth(t, b, nil, []Level{lv(100, 6, 2), lv(101, 2, 1)})
	wantTrades(t, mustSubmit(t, b, buy("x", 100, 6)), tr{100, 3, "x", "A"}, tr{100, 3, "x", "C"})

	if err := b.Cancel("D"); err != nil {
		t.Fatal(err)
	}
	wantDepth(t, b, nil, nil) // the last order at a price takes its level with it
}

func TestStage1_CancelErrors(t *testing.T) {
	b := NewBook("ABC")
	wantErr(t, "Cancel of a never-seen id", b.Cancel("zzz"), ErrUnknownOrder)
	mustSubmit(t, b, sell("s1", 100, 1))
	mustSubmit(t, b, buy("b1", 100, 1)) // fills s1 completely
	wantErr(t, "Cancel of a filled order", b.Cancel("s1"), ErrUnknownOrder)
	wantErr(t, "Cancel of the order that took", b.Cancel("b1"), ErrUnknownOrder)
	mustSubmit(t, b, buy("b2", 90, 1))
	if err := b.Cancel("b2"); err != nil {
		t.Fatal(err)
	}
	wantErr(t, "second Cancel of the same order", b.Cancel("b2"), ErrUnknownOrder)
}

func TestStage1_InvalidOrdersAreRejectedAndChangeNothing(t *testing.T) {
	b := NewBook("ABC")
	mustSubmit(t, b, sell("s1", 100, 5))
	before1, before2 := b.Depth(0)
	for name, o := range map[string]Order{
		"empty id":       buy("", 100, 1),
		"zero side":      {ID: "x", Price: 100, Qty: 1},
		"bogus side":     {ID: "x", Side: Side(9), Price: 100, Qty: 1},
		"zero qty":       buy("x", 100, 0),
		"negative qty":   buy("x", 100, -3),
		"zero price":     buy("x", 0, 1),
		"negative price": sell("x", -5, 1),
	} {
		ts, err := b.Submit(o)
		if !errors.Is(err, ErrInvalidOrder) || len(ts) != 0 {
			t.Errorf("%s: Submit = %+v, %v; want no trades and ErrInvalidOrder", name, ts, err)
		}
	}
	after1, after2 := b.Depth(0)
	if !reflect.DeepEqual(before1, after1) || !reflect.DeepEqual(before2, after2) {
		t.Fatalf("rejected orders changed the book: %+v/%+v -> %+v/%+v", before1, before2, after1, after2)
	}
}

func TestStage1_DuplicateIDsOfRestingOrdersAreRejected(t *testing.T) {
	b := NewBook("ABC")
	mustSubmit(t, b, sell("dup", 100, 5))
	// Would match the resting order, but the id is taken: reject BEFORE matching.
	ts, err := b.Submit(buy("dup", 100, 5))
	wantErr(t, "Submit with a live order's id", err, ErrDuplicateID)
	if len(ts) != 0 {
		t.Fatalf("a rejected duplicate produced trades %+v", ts)
	}
	wantDepth(t, b, nil, []Level{lv(100, 5, 1)})
	// Once the order is gone, the id is free again.
	if err := b.Cancel("dup"); err != nil {
		t.Fatal(err)
	}
	mustSubmit(t, b, buy("dup", 90, 1))
}

func TestStage1_TradeIDsAreConsecutiveAndSymbolIsSet(t *testing.T) {
	b := NewBook("XYZ")
	mustSubmit(t, b, sell("s1", 100, 1))
	mustSubmit(t, b, sell("s2", 100, 1))
	mustSubmit(t, b, sell("s3", 100, 1))
	ts := mustSubmit(t, b, buy("b1", 100, 2))
	ts = append(ts, mustSubmit(t, b, buy("b2", 100, 1))...)
	for i, tt := range ts {
		if tt.ID != uint64(i+1) || tt.Symbol != "XYZ" || tt.TakerSide != Buy {
			t.Fatalf("trade %d = %+v, want ID %d, Symbol XYZ, TakerSide buy (IDs count 1,2,3… per book in execution order)", i, tt, i+1)
		}
	}
	_, _ = b.Submit(buy("", 1, 1)) // a rejected order must not consume a trade id
	mustSubmit(t, b, sell("s4", 100, 1))
	next := mustSubmit(t, b, buy("b3", 100, 1))
	if len(next) != 1 || next[0].ID != 4 {
		t.Fatalf("next trade = %+v, want ID 4", next)
	}
}

func TestStage1_RandomLimitOrdersNeverLeaveACrossedBook(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	b := NewBook("ABC")
	var resting, trades int64
	for i := 0; i < 1500; i++ {
		o := Order{ID: fmt.Sprintf("o%d", i), Side: Side(1 + rng.Intn(2)), Price: 97 + rng.Int63n(7), Qty: 1 + rng.Int63n(9)}
		before := totalQty(b)
		ts, err := b.Submit(o)
		if err != nil {
			t.Fatalf("Submit(%+v) = %v", o, err)
		}
		filled := sumQty(ts)
		trades += int64(len(ts))
		// Everything that is not filled rests (plain GTC limit order), and each
		// lot traded removes one lot from the resting side.
		wantTotal := before + (o.Qty - filled) - filled
		if got := totalQty(b); got != wantTotal {
			t.Fatalf("op %d: resting quantity = %d, want %d (before %d + rested %d - maker fills %d)", i, got, wantTotal, before, o.Qty-filled, filled)
		}
		bids, asks := b.Depth(0)
		if err := checkShape(bids, asks); err != nil {
			t.Fatalf("op %d after %+v: %v", i, o, err)
		}
		resting = totalQty(b)
	}
	if trades < 100 {
		t.Fatalf("only %d trades in 1500 orders; the test is not exercising matching (resting %d)", trades, resting)
	}
}
