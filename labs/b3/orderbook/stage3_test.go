package orderbook

import (
	"errors"
	"fmt"
	"math/rand"
	"reflect"
	"sort"
	"sync"
	"testing"
	"time"
)

// ------------------------------------------------------------ reference model
//
// refBook is a deliberately naive matcher: one slice of resting orders, a full
// scan to find the best match. It is slow and obviously correct, which makes it
// the oracle for the differential test.

type refOrder struct {
	o   Order
	seq int
}

type refBook struct {
	symbol  string
	orders  []*refOrder
	seq     int
	tradeID uint64
}

func refCrosses(in Order, price int64) bool {
	switch {
	case in.Type == Market:
		return true
	case in.Side == Buy:
		return price <= in.Price
	default:
		return price >= in.Price
	}
}

// bestMatch is the resting order incoming should trade with next, or nil.
func (r *refBook) bestMatch(in Order) *refOrder {
	var best *refOrder
	for _, c := range r.orders {
		if c.o.Side == in.Side || !refCrosses(in, c.o.Price) {
			continue
		}
		if best == nil {
			best = c
			continue
		}
		better := false
		switch {
		case c.o.Price != best.o.Price && in.Side == Buy:
			better = c.o.Price < best.o.Price
		case c.o.Price != best.o.Price:
			better = c.o.Price > best.o.Price
		default:
			better = c.seq < best.seq
		}
		if better {
			best = c
		}
	}
	return best
}

func (r *refBook) liquidity(in Order) int64 {
	var sum int64
	for _, c := range r.orders {
		if c.o.Side != in.Side && refCrosses(in, c.o.Price) {
			sum += c.o.Qty
		}
	}
	return sum
}

func (r *refBook) remove(ro *refOrder) {
	for i, c := range r.orders {
		if c == ro {
			r.orders = append(r.orders[:i], r.orders[i+1:]...)
			return
		}
	}
}

func (r *refBook) find(id string) *refOrder {
	for _, c := range r.orders {
		if c.o.ID == id {
			return c
		}
	}
	return nil
}

func (r *refBook) submit(o Order) ([]Trade, error) {
	if o.PostOnly && r.bestMatch(o) != nil {
		return nil, ErrWouldCross
	}
	if o.TIF == FOK && r.liquidity(o) < o.Qty {
		return nil, ErrNotFillable
	}
	var trades []Trade
	for o.Qty > 0 {
		m := r.bestMatch(o)
		if m == nil {
			break
		}
		q := min(o.Qty, m.o.Qty)
		r.tradeID++
		trades = append(trades, Trade{
			ID: r.tradeID, Symbol: r.symbol, Price: m.o.Price, Qty: q,
			TakerID: o.ID, MakerID: m.o.ID, TakerSide: o.Side,
		})
		o.Qty -= q
		m.o.Qty -= q
		if m.o.Qty == 0 {
			r.remove(m)
		}
	}
	if o.Qty > 0 && o.Type == Limit && o.TIF == GTC {
		r.seq++
		r.orders = append(r.orders, &refOrder{o: o, seq: r.seq})
	}
	return trades, nil
}

func (r *refBook) cancel(id string) error {
	ro := r.find(id)
	if ro == nil {
		return ErrUnknownOrder
	}
	r.remove(ro)
	return nil
}

func (r *refBook) amend(id string, qty, price int64) ([]Trade, error) {
	ro := r.find(id)
	if ro == nil {
		return nil, ErrUnknownOrder
	}
	if qty <= 0 || price <= 0 {
		return nil, ErrInvalidOrder
	}
	switch {
	case price == ro.o.Price && qty == ro.o.Qty:
		return nil, nil
	case price == ro.o.Price && qty < ro.o.Qty:
		ro.o.Qty = qty
		return nil, nil
	case price == ro.o.Price:
		ro.o.Qty = qty
		r.seq++
		ro.seq = r.seq
		return nil, nil
	}
	o := ro.o
	o.Qty, o.Price = qty, price
	if o.PostOnly && r.bestMatch(o) != nil {
		return nil, ErrWouldCross
	}
	r.remove(ro)
	return r.submit(o)
}

func (r *refBook) depth() (bids, asks []Level) {
	agg := map[Side]map[int64]*Level{Buy: {}, Sell: {}}
	for _, c := range r.orders {
		l := agg[c.o.Side][c.o.Price]
		if l == nil {
			l = &Level{Price: c.o.Price}
			agg[c.o.Side][c.o.Price] = l
		}
		l.Qty += c.o.Qty
		l.Orders++
	}
	for _, l := range agg[Buy] {
		bids = append(bids, *l)
	}
	for _, l := range agg[Sell] {
		asks = append(asks, *l)
	}
	sort.Slice(bids, func(i, j int) bool { return bids[i].Price > bids[j].Price })
	sort.Slice(asks, func(i, j int) bool { return asks[i].Price < asks[j].Price })
	return bids, asks
}

// ------------------------------------------------------------ workload

func randomOrder(rng *rand.Rand, id string) Order {
	o := Order{ID: id, Side: Side(1 + rng.Intn(2)), Qty: 1 + rng.Int63n(15), Price: 97 + rng.Int63n(7)}
	switch x := rng.Intn(100); {
	case x < 60:
	case x < 70:
		o.TIF = IOC
	case x < 78:
		o.TIF = FOK
	case x < 86:
		o.Type, o.Price = Market, 0
		o.TIF = []TIF{GTC, IOC, FOK}[rng.Intn(3)]
	default:
		o.PostOnly = true
	}
	return o
}

// op is one step of a scripted workload.
type op struct {
	kind  string // "submit", "cancel", "amend"
	order Order
	id    string
	qty   int64
	price int64
}

func script(seed int64, prefix string, n int) []op {
	rng := rand.New(rand.NewSource(seed))
	var issued []string
	var ops []op
	for i := 0; i < n; i++ {
		switch x := rng.Intn(100); {
		case x < 72 || len(issued) == 0:
			o := randomOrder(rng, fmt.Sprintf("%s-%d", prefix, i))
			issued = append(issued, o.ID)
			ops = append(ops, op{kind: "submit", order: o})
		case x < 87:
			ops = append(ops, op{kind: "cancel", id: issued[rng.Intn(len(issued))]})
		default:
			ops = append(ops, op{kind: "amend", id: issued[rng.Intn(len(issued))], qty: 1 + rng.Int63n(15), price: 97 + rng.Int63n(7)})
		}
	}
	return ops
}

// ------------------------------------------------------------ differential test

func sameErr(got, want error) bool {
	if want == nil {
		return got == nil
	}
	return errors.Is(got, want)
}

func runDifferential(t *testing.T, seed int64, nops int) (trades, kills int) {
	t.Helper()
	rng := rand.New(rand.NewSource(seed))
	b := NewBook("DIFF")
	ref := &refBook{symbol: "DIFF"}
	var issued []string
	for i := 0; i < nops; i++ {
		var got, want []Trade
		var gotErr, wantErr error
		var id string
		desc := ""
		switch x := rng.Intn(100); {
		case x < 72 || len(issued) == 0:
			o := randomOrder(rng, fmt.Sprintf("d%d", i))
			id, desc = o.ID, fmt.Sprintf("Submit(%+v)", o)
			issued = append(issued, o.ID)
			got, gotErr = b.Submit(o)
			want, wantErr = ref.submit(o)
		case x < 87:
			id = issued[rng.Intn(len(issued))]
			desc = fmt.Sprintf("Cancel(%s)", id)
			gotErr, wantErr = b.Cancel(id), ref.cancel(id)
		default:
			id = issued[rng.Intn(len(issued))]
			q, p := 1+rng.Int63n(15), 97+rng.Int63n(7)
			desc = fmt.Sprintf("Amend(%s, %d, %d)", id, q, p)
			got, gotErr = b.Amend(id, q, p)
			want, wantErr = ref.amend(id, q, p)
		}
		if !sameErr(gotErr, wantErr) {
			t.Fatalf("seed %d op %d %s: error = %v, reference says %v", seed, i, desc, gotErr, wantErr)
		}
		if len(got) != len(want) || (len(got) > 0 && !reflect.DeepEqual(got, want)) {
			t.Fatalf("seed %d op %d %s:\n got  %+v\n want %+v", seed, i, desc, got, want)
		}
		trades += len(got)
		if errors.Is(wantErr, ErrNotFillable) || errors.Is(wantErr, ErrWouldCross) {
			kills++
		}
		gb, ga := b.Depth(0)
		wb, wa := ref.depth()
		if (len(gb) != 0 || len(wb) != 0) && !reflect.DeepEqual(gb, wb) {
			t.Fatalf("seed %d op %d %s: bids %+v, reference %+v", seed, i, desc, gb, wb)
		}
		if (len(ga) != 0 || len(wa) != 0) && !reflect.DeepEqual(ga, wa) {
			t.Fatalf("seed %d op %d %s: asks %+v, reference %+v", seed, i, desc, ga, wa)
		}
		if err := checkShape(gb, ga); err != nil {
			t.Fatalf("seed %d op %d %s: %v", seed, i, desc, err)
		}
		if err := b.CheckInvariants(); err != nil {
			t.Fatalf("seed %d op %d %s: CheckInvariants: %v", seed, i, desc, err)
		}
	}
	return trades, kills
}

func TestStage3_DifferentialAgainstNaiveReferenceMatcher(t *testing.T) {
	var trades, kills int
	for seed := int64(1); seed <= 4; seed++ {
		tr, k := runDifferential(t, seed, 2500)
		trades += tr
		kills += k
	}
	if trades < 1500 || kills < 100 {
		t.Fatalf("the workload produced only %d trades and %d rejected FOK/post-only orders; the test is not exercising the engine", trades, kills)
	}
}

// Conservation: after every operation, the change in the book's total resting
// quantity is explained exactly by (what the touched order rests now) minus
// (what it rested before) minus (the lots its trades took from makers).
func TestStage3_QuantityIsConservedOperationByOperation(t *testing.T) {
	rng := rand.New(rand.NewSource(99))
	b := NewBook("CONS")
	var issued []string
	restingQty := func(id string) int64 {
		o, _ := b.Lookup(id)
		return o.Qty
	}
	for i := 0; i < 3000; i++ {
		var id, desc string
		var ts []Trade
		var err error
		var before, total0 = int64(0), totalQty(b)
		switch x := rng.Intn(100); {
		case x < 75 || len(issued) == 0:
			o := randomOrder(rng, fmt.Sprintf("c%d", i))
			id, desc = o.ID, fmt.Sprintf("Submit(%+v)", o)
			issued = append(issued, id)
			ts, err = b.Submit(o)
			filled, rest := sumQty(ts), restingQty(id)
			switch {
			case err != nil:
				if filled != 0 || rest != 0 {
					t.Fatalf("op %d %s: rejected (%v) but traded %d / rested %d", i, desc, err, filled, rest)
				}
			case o.Type == Limit && o.TIF == GTC:
				if filled+rest != o.Qty {
					t.Fatalf("op %d %s: filled %d + resting %d != submitted %d: lots appeared or vanished", i, desc, filled, rest, o.Qty)
				}
			case o.TIF == FOK:
				if filled != o.Qty || rest != 0 {
					t.Fatalf("op %d %s: accepted FOK filled %d of %d (resting %d)", i, desc, filled, o.Qty, rest)
				}
			default:
				if rest != 0 || filled > o.Qty {
					t.Fatalf("op %d %s: IOC/market order rested %d or overfilled (%d of %d)", i, desc, rest, filled, o.Qty)
				}
			}
		case x < 88:
			id = issued[rng.Intn(len(issued))]
			desc = fmt.Sprintf("Cancel(%s)", id)
			before = restingQty(id)
			err = b.Cancel(id)
		default:
			id = issued[rng.Intn(len(issued))]
			q, p := 1+rng.Int63n(15), 97+rng.Int63n(7)
			desc = fmt.Sprintf("Amend(%s, %d, %d)", id, q, p)
			before = restingQty(id)
			ts, err = b.Amend(id, q, p)
		}
		_ = err
		want := total0 + restingQty(id) - before - sumQty(ts)
		if got := totalQty(b); got != want {
			t.Fatalf("op %d %s: resting total = %d, want %d (was %d; order rested %d before, %d after; %d lots taken from makers)",
				i, desc, got, want, total0, before, restingQty(id), sumQty(ts))
		}
		if err := b.CheckInvariants(); err != nil {
			t.Fatalf("op %d %s: %v", i, desc, err)
		}
	}
}

// ------------------------------------------------------------ exchange

func TestStage3_SymbolsAreIndependentBooks(t *testing.T) {
	x := NewExchange()
	defer closeSoon(x)
	if _, err := x.Submit("AAA", sell("same-id", 100, 5)); err != nil {
		t.Fatalf("Submit(AAA) = %v", err)
	}
	// The same order id on another symbol is a different order.
	if _, err := x.Submit("BBB", sell("same-id", 200, 5)); err != nil {
		t.Fatalf("Submit(BBB) with the same id = %v, want nil (ids are per symbol)", err)
	}
	ts, err := x.Submit("AAA", buy("b1", 100, 5))
	if err != nil || len(ts) != 1 || ts[0].Symbol != "AAA" || ts[0].Price != 100 {
		t.Fatalf("Submit(AAA buy) = %+v, %v; want one AAA trade at 100", ts, err)
	}
	_, asks, err := x.Depth("BBB", 0)
	if err != nil || len(asks) != 1 || asks[0].Price != 200 {
		t.Fatalf("BBB depth = %+v, %v; AAA's trade must not touch BBB", asks, err)
	}
	if _, _, err := x.Depth("never-used", 0); !errors.Is(err, ErrUnknownSymbol) {
		t.Fatalf("Depth of unknown symbol = %v, want ErrUnknownSymbol", err)
	}
	if err := x.Cancel("never-used", "x"); !errors.Is(err, ErrUnknownSymbol) {
		t.Fatalf("Cancel on unknown symbol = %v, want ErrUnknownSymbol", err)
	}
	if err := x.Cancel("AAA", "same-id"); !errors.Is(err, ErrUnknownOrder) {
		t.Fatalf("Cancel of a filled order = %v, want ErrUnknownOrder", err)
	}
	if err := x.Cancel("BBB", "same-id"); err != nil {
		t.Fatalf("Cancel(BBB) = %v", err)
	}
	if ts, err := x.Amend("AAA", "nope", 1, 1); err == nil || len(ts) != 0 {
		t.Fatalf("Amend of unknown order = %+v, %v; want an error", ts, err)
	}
}

// closeSoon closes x but gives up after a second: if your implementation is
// wedged (a Submit stuck on a slow subscriber still holds a lock), the test
// should report that failure instead of hanging in its own cleanup.
func closeSoon(x *Exchange) {
	done := make(chan struct{})
	go func() { x.Close(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
	}
}

func mustSub(t *testing.T, x *Exchange, symbol string, buf int) *Subscription {
	t.Helper()
	s, err := x.Subscribe(symbol, buf)
	if err != nil || s == nil {
		t.Fatalf("Subscribe(%q, %d) = %v, %v", symbol, buf, s, err)
	}
	return s
}

func recvTrade(t *testing.T, s *Subscription) (Trade, bool) {
	t.Helper()
	select {
	case tr, ok := <-s.C:
		return tr, ok
	case <-time.After(testTimeout):
		t.Fatalf("no trade (and no close) on the subscription within %v", testTimeout)
		return Trade{}, false
	}
}

func TestStage3_SubscribersSeeTradesInOrderAndCloseCleanly(t *testing.T) {
	x := NewExchange()
	defer closeSoon(x)
	s1, s2 := mustSub(t, x, "AAA", 16), mustSub(t, x, "AAA", 16)
	other := mustSub(t, x, "BBB", 16)
	for i := 1; i <= 3; i++ {
		if _, err := x.Submit("AAA", sell(fmt.Sprintf("s%d", i), 100, 1)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := x.Submit("AAA", buy("b1", 100, 3)); err != nil {
		t.Fatal(err)
	}
	for _, s := range []*Subscription{s1, s2} {
		for want := uint64(1); want <= 3; want++ {
			tr, ok := recvTrade(t, s)
			if !ok || tr.ID != want || tr.MakerID != fmt.Sprintf("s%d", want) {
				t.Fatalf("subscriber got %+v (open=%v), want trade %d against s%d", tr, ok, want, want)
			}
		}
	}
	select {
	case tr := <-other.C:
		t.Fatalf("BBB subscriber received %+v from AAA", tr)
	default:
	}

	s1.Close()
	s1.Close() // idempotent
	if _, ok := recvTrade(t, s1); ok {
		t.Fatal("C should be closed after Close")
	}
	x.Submit("AAA", sell("s9", 100, 1))
	x.Submit("AAA", buy("b9", 100, 1))
	if tr, ok := recvTrade(t, s2); !ok || tr.ID != 4 {
		t.Fatalf("remaining subscriber got %+v, %v; want trade 4", tr, ok)
	}

	x.Close()
	x.Close() // idempotent
	if _, ok := recvTrade(t, s2); ok {
		t.Fatal("Exchange.Close must close every subscription")
	}
	if _, err := x.Submit("AAA", buy("late", 1, 1)); !errors.Is(err, ErrClosed) {
		t.Fatalf("Submit after Close = %v, want ErrClosed", err)
	}
	if _, err := x.Subscribe("AAA", 1); !errors.Is(err, ErrClosed) {
		t.Fatalf("Subscribe after Close = %v, want ErrClosed", err)
	}
}

func TestStage3_ASlowSubscriberNeverBlocksMatching(t *testing.T) {
	x := NewExchange()
	defer closeSoon(x)
	slow := mustSub(t, x, "AAA", 4) // never read
	const pairs = 5000
	fast := mustSub(t, x, "AAA", pairs)
	within(t, "5,000 matching pairs with a stalled subscriber", func() {
		for i := 0; i < pairs; i++ {
			if _, err := x.Submit("AAA", sell(fmt.Sprintf("s%d", i), 100, 1)); err != nil {
				t.Errorf("Submit sell: %v", err)
				return
			}
			if ts, err := x.Submit("AAA", buy(fmt.Sprintf("b%d", i), 100, 1)); err != nil || len(ts) != 1 {
				t.Errorf("Submit buy = %+v, %v", ts, err)
				return
			}
		}
	})
	if got := slow.Dropped(); got != pairs-4 {
		t.Fatalf("slow subscriber Dropped = %d, want %d (buffer of 4, everything else dropped, never blocked)", got, pairs-4)
	}
	for want := uint64(1); want <= 4; want++ {
		if tr, ok := recvTrade(t, slow); !ok || tr.ID != want {
			t.Fatalf("slow subscriber got %+v, %v; want the first four trades (full buffer drops the NEW trade)", tr, ok)
		}
	}
	if fast.Dropped() != 0 {
		t.Fatalf("fast subscriber dropped %d trades despite a big buffer", fast.Dropped())
	}
	for want := uint64(1); want <= pairs; want++ {
		if tr, ok := recvTrade(t, fast); !ok || tr.ID != want {
			t.Fatalf("fast subscriber got trade %d, want %d: delivery must be in execution order", tr.ID, want)
		}
	}
}

func TestStage3_ConcurrentSubmitsKeepEveryBookConsistent(t *testing.T) {
	x := NewExchange()
	defer closeSoon(x)
	symbols := []string{"S0", "S1", "S2", "S3"}
	subs := map[string]*Subscription{}
	for _, s := range symbols {
		subs[s] = mustSub(t, x, s, 200000)
	}
	const workers, opsEach = 8, 600
	type rec struct {
		symbol string
		trades []Trade
	}
	results := make([][]rec, workers)
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rng := rand.New(rand.NewSource(int64(w) + 1000))
			var mine []rec
			var ids []string
			for i := 0; i < opsEach; i++ {
				sym := symbols[rng.Intn(len(symbols))]
				switch rng.Intn(10) {
				case 0:
					if len(ids) > 0 {
						_ = x.Cancel(sym, ids[rng.Intn(len(ids))]) // may be on another symbol: error is fine
					}
				default:
					o := randomOrder(rng, fmt.Sprintf("w%d-%d", w, i))
					ids = append(ids, o.ID)
					ts, err := x.Submit(sym, o)
					if err != nil && !errors.Is(err, ErrNotFillable) && !errors.Is(err, ErrWouldCross) {
						t.Errorf("Submit(%s, %+v) = %v", sym, o, err)
						return
					}
					mine = append(mine, rec{sym, ts})
				}
			}
			results[w] = mine
		}()
	}
	within(t, "concurrent workload", wg.Wait)

	perSymbol := map[string]map[uint64]Trade{}
	filled := map[string]int64{}
	for _, rs := range results {
		for _, r := range rs {
			if perSymbol[r.symbol] == nil {
				perSymbol[r.symbol] = map[uint64]Trade{}
			}
			for _, tt := range r.trades {
				if _, dup := perSymbol[r.symbol][tt.ID]; dup {
					t.Fatalf("%s: trade id %d returned twice: two writers matched at once", r.symbol, tt.ID)
				}
				perSymbol[r.symbol][tt.ID] = tt
				filled[r.symbol+"/"+tt.TakerID] += tt.Qty
				filled[r.symbol+"/"+tt.MakerID] += tt.Qty
			}
		}
	}
	total := 0
	for _, sym := range symbols {
		n := len(perSymbol[sym])
		total += n
		for id := uint64(1); id <= uint64(n); id++ {
			if _, ok := perSymbol[sym][id]; !ok {
				t.Fatalf("%s: trade ids are not exactly 1..%d (missing %d)", sym, n, id)
			}
		}
		// The subscriber saw the same trades, in order.
		for id := uint64(1); id <= uint64(n); id++ {
			tt, ok := recvTrade(t, subs[sym])
			if !ok || tt.ID != id || tt != perSymbol[sym][id] {
				t.Fatalf("%s: subscriber got %+v, want %+v", sym, tt, perSymbol[sym][id])
			}
		}
		if err := x.CheckInvariants(sym); err != nil {
			t.Fatalf("%s: CheckInvariants: %v", sym, err)
		}
	}
	if total < 300 {
		t.Fatalf("only %d trades in %d operations; the test is not exercising matching", total, workers*opsEach)
	}
}

func replay(ops []op, apply func(op) []Trade) []Trade {
	var all []Trade
	for _, o := range ops {
		all = append(all, apply(o)...)
	}
	return all
}

func TestStage3_SameInputsGiveIdenticalTradesEveryRun(t *testing.T) {
	symbols := []string{"S0", "S1", "S2", "S3"}
	scripts := map[string][]op{}
	for i, s := range symbols {
		scripts[s] = script(int64(i)+50, s, 1500)
	}

	// Oracle: each script replayed on a plain single-threaded Book.
	want := map[string][]Trade{}
	for _, s := range symbols {
		b := NewBook(s)
		want[s] = replay(scripts[s], func(o op) []Trade {
			var ts []Trade
			switch o.kind {
			case "submit":
				ts, _ = b.Submit(o.order)
			case "cancel":
				_ = b.Cancel(o.id)
			case "amend":
				ts, _ = b.Amend(o.id, o.qty, o.price)
			}
			return ts
		})
	}

	for run := 0; run < 3; run++ {
		x := NewExchange()
		got := map[string][]Trade{}
		var mu sync.Mutex
		var wg sync.WaitGroup
		for _, s := range symbols {
			wg.Add(1)
			go func() { // one goroutine per symbol, all symbols in parallel
				defer wg.Done()
				ts := replay(scripts[s], func(o op) []Trade {
					var ts []Trade
					switch o.kind {
					case "submit":
						ts, _ = x.Submit(s, o.order)
					case "cancel":
						_ = x.Cancel(s, o.id)
					case "amend":
						ts, _ = x.Amend(s, o.id, o.qty, o.price)
					}
					return ts
				})
				mu.Lock()
				got[s] = ts
				mu.Unlock()
			}()
		}
		within(t, "scripted exchange run", wg.Wait)
		x.Close()
		for _, s := range symbols {
			if len(want[s]) < 100 {
				t.Fatalf("%s: oracle produced only %d trades; the script is too tame", s, len(want[s]))
			}
			if !reflect.DeepEqual(got[s], want[s]) {
				t.Fatalf("run %d, %s: trades differ from the single-threaded replay (%d vs %d trades): matching must be deterministic, with no dependence on map order, goroutine scheduling or the clock",
					run, s, len(got[s]), len(want[s]))
			}
		}
	}
}

func TestStage3_ManyWritersOnOneSymbolAreSerialised(t *testing.T) {
	// 16 goroutines hammer ONE symbol with crossing orders. Matching must behave
	// as if the orders arrived one at a time: no lot is traded twice.
	x := NewExchange()
	defer closeSoon(x)
	const writers, each = 16, 300
	var wg sync.WaitGroup
	var mu sync.Mutex
	made := map[string]int64{} // lots traded per order id
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < each; i++ {
				side := Buy
				if (w+i)%2 == 0 {
					side = Sell
				}
				id := fmt.Sprintf("w%d-%d", w, i)
				ts, err := x.Submit("HOT", Order{ID: id, Side: side, Price: 100, Qty: 3})
				if err != nil {
					t.Errorf("Submit = %v", err)
					return
				}
				mu.Lock()
				for _, tt := range ts {
					made[tt.TakerID] += tt.Qty
					made[tt.MakerID] += tt.Qty
				}
				mu.Unlock()
			}
		}()
	}
	within(t, "hot symbol workload", wg.Wait)
	for id, q := range made {
		if q > 3 {
			t.Fatalf("order %s traded %d lots but only had 3: a resting order was matched by two writers at once", id, q)
		}
	}
	if err := x.CheckInvariants("HOT"); err != nil {
		t.Fatal(err)
	}
	bids, asks, _ := x.Depth("HOT", 0)
	var resting, traded int64
	for _, l := range append(bids, asks...) {
		resting += l.Qty
	}
	for _, q := range made {
		traded += q // counted once per side of every trade
	}
	if want := int64(writers*each) * 3; resting+traded != want {
		t.Fatalf("resting %d + traded %d (counted at both ends) != submitted %d: lots appeared or vanished", resting, traded, want)
	}
}
