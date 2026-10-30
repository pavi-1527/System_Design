package inventory

import (
	"fmt"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func mustReserveAll(t *testing.T, inv *Inventory, ttl time.Duration, lines ...Line) Reservation {
	t.Helper()
	r, err := inv.ReserveAll(lines, ttl)
	if err != nil {
		t.Fatalf("ReserveAll(%+v) = %v, want nil", lines, err)
	}
	return r
}

func TestStage2_ReserveAllHoldsEveryLineUnderOneID(t *testing.T) {
	inv, clk := newInv(t, map[string]int{"a": 5, "b": 5})
	r := mustReserveAll(t, inv, time.Minute, Line{"b", 2}, Line{"a", 1})
	if r.ID == "" || !r.ExpiresAt.Equal(clk.Now().Add(time.Minute)) {
		t.Fatalf("Reservation = %+v, want a non-empty ID and ExpiresAt = now+ttl", r)
	}
	if want := []Line{{"a", 1}, {"b", 2}}; !reflect.DeepEqual(r.Lines, want) {
		t.Fatalf("Lines = %+v, want %+v (normalized: sorted by SKU)", r.Lines, want)
	}
	wantAvail(t, inv, "a", 4)
	wantAvail(t, inv, "b", 3)

	if err := inv.Commit(r.ID); err != nil {
		t.Fatalf("Commit = %v", err)
	}
	wantStock(t, inv, "a", Stock{Total: 5, Committed: 1, Available: 4})
	wantStock(t, inv, "b", Stock{Total: 5, Committed: 2, Available: 3})

	r2 := mustReserveAll(t, inv, time.Minute, Line{"a", 4}, Line{"b", 3})
	if err := inv.Release(r2.ID); err != nil {
		t.Fatalf("Release = %v", err)
	}
	wantAvail(t, inv, "a", 4)
	wantAvail(t, inv, "b", 3)
}

func TestStage2_ReserveAllIsAllOrNothing(t *testing.T) {
	inv, _ := newInv(t, map[string]int{"a": 5, "b": 1, "c": 5})
	_, err := inv.ReserveAll([]Line{{"a", 2}, {"b", 2}, {"c", 2}}, time.Minute)
	wantErr(t, "ReserveAll where one line is short", err, ErrInsufficient)
	for sku, want := range map[string]int{"a": 5, "b": 1, "c": 5} {
		wantAvail(t, inv, sku, want) // no partial hold may be left on a or c
	}
	wantStock(t, inv, "a", Stock{Total: 5, Available: 5})

	_, err = inv.ReserveAll([]Line{{"a", 1}, {"ghost", 1}}, time.Minute)
	wantErr(t, "ReserveAll with an unknown SKU", err, ErrUnknownSKU)
	wantAvail(t, inv, "a", 5)
}

func TestStage2_ReserveAllValidation(t *testing.T) {
	inv, _ := newInv(t, map[string]int{"a": 5})
	_, err := inv.ReserveAll(nil, time.Minute)
	wantErr(t, "ReserveAll with no lines", err, ErrNoLines)
	_, err = inv.ReserveAll([]Line{{"a", 1}, {"a", 0}}, time.Minute)
	wantErr(t, "ReserveAll with a zero-quantity line", err, ErrInvalidQty)
	_, err = inv.ReserveAll([]Line{{"a", 1}}, 0)
	wantErr(t, "ReserveAll with zero ttl", err, ErrInvalidTTL)
	wantAvail(t, inv, "a", 5)
}

func TestStage2_ReserveAllSumsDuplicateSKUs(t *testing.T) {
	inv, _ := newInv(t, map[string]int{"a": 4})
	_, err := inv.ReserveAll([]Line{{"a", 2}, {"a", 3}}, time.Minute)
	wantErr(t, "two lines of the same SKU totalling more than the stock", err, ErrInsufficient)
	wantAvail(t, inv, "a", 4)
	r := mustReserveAll(t, inv, time.Minute, Line{"a", 1}, Line{"a", 3})
	if want := []Line{{"a", 4}}; !reflect.DeepEqual(r.Lines, want) {
		t.Fatalf("Lines = %+v, want %+v (duplicate SKUs are summed)", r.Lines, want)
	}
	wantAvail(t, inv, "a", 0)
}

func TestStage2_KeyedReserveReplaysTheOriginal(t *testing.T) {
	inv, clk := newInv(t, map[string]int{"a": 5, "b": 5})
	lines := []Line{{"a", 2}, {"b", 1}}
	first, err := inv.ReserveKeyed("order-77", lines, time.Minute)
	if err != nil {
		t.Fatalf("ReserveKeyed = %v", err)
	}
	clk.Advance(10 * time.Second)
	again, err := inv.ReserveKeyed("order-77", []Line{{"b", 1}, {"a", 2}}, time.Minute) // same request, lines reordered
	if err != nil {
		t.Fatalf("retry with the same key = %v, want nil", err)
	}
	if !reflect.DeepEqual(first, again) {
		t.Fatalf("retry returned %+v, want the original %+v unchanged (ExpiresAt must not move)", again, first)
	}
	wantAvail(t, inv, "a", 3) // taken once, not twice
	wantAvail(t, inv, "b", 4)

	other, err := inv.ReserveKeyed("order-78", lines, time.Minute)
	if err != nil || other.ID == first.ID {
		t.Fatalf("a different key must create a new reservation: %+v, %v", other, err)
	}
	wantAvail(t, inv, "a", 1)

	// The replay is the recorded response, even after the reservation was used.
	if err := inv.Commit(first.ID); err != nil {
		t.Fatal(err)
	}
	replay, err := inv.ReserveKeyed("order-77", lines, time.Minute)
	if err != nil || !reflect.DeepEqual(replay, first) {
		t.Fatalf("replay after Commit = %+v, %v; want the original response", replay, err)
	}
	wantAvail(t, inv, "a", 1)
}

func TestStage2_KeyReuseWithDifferentRequestIsAConflict(t *testing.T) {
	inv, _ := newInv(t, map[string]int{"a": 9})
	if _, err := inv.ReserveKeyed("k", []Line{{"a", 2}}, time.Minute); err != nil {
		t.Fatal(err)
	}
	_, err := inv.ReserveKeyed("k", []Line{{"a", 3}}, time.Minute)
	wantErr(t, "same key, different quantity", err, ErrKeyConflict)
	_, err = inv.ReserveKeyed("k", []Line{{"a", 2}}, time.Hour)
	wantErr(t, "same key, different ttl", err, ErrKeyConflict)
	wantAvail(t, inv, "a", 7)
}

func TestStage2_FailedAttemptIsNotRemembered(t *testing.T) {
	inv, _ := newInv(t, map[string]int{"a": 1})
	_, err := inv.ReserveKeyed("k", []Line{{"a", 2}}, time.Minute)
	wantErr(t, "keyed reserve beyond stock", err, ErrInsufficient)
	if err := inv.Restock("a", 5); err != nil {
		t.Fatal(err)
	}
	r, err := inv.ReserveKeyed("k", []Line{{"a", 2}}, time.Minute)
	if err != nil || r.ID == "" {
		t.Fatalf("retry after restock = %+v, %v; a failed attempt must not poison its key", r, err)
	}
}

func TestStage2_EmptyKeyIsNotIdempotent(t *testing.T) {
	inv, _ := newInv(t, map[string]int{"a": 5})
	r1, _ := inv.ReserveKeyed("", []Line{{"a", 1}}, time.Minute)
	r2, _ := inv.ReserveKeyed("", []Line{{"a", 1}}, time.Minute)
	if r1.ID == r2.ID || r1.ID == "" {
		t.Fatalf("empty keys produced %q and %q, want two distinct reservations", r1.ID, r2.ID)
	}
	wantAvail(t, inv, "a", 3)
}

func TestStage2_Extend(t *testing.T) {
	inv, clk := newInv(t, map[string]int{"a": 3})
	r := mustReserve(t, inv, "a", 3, time.Minute)
	clk.Advance(30 * time.Second)
	ext, err := inv.Extend(r.ID, time.Minute)
	if err != nil {
		t.Fatalf("Extend = %v", err)
	}
	if want := clk.Now().Add(time.Minute); !ext.ExpiresAt.Equal(want) || ext.ID != r.ID {
		t.Fatalf("Extend returned %+v, want same ID and ExpiresAt = now+ttl = %v", ext, want)
	}
	wantAvail(t, inv, "a", 0) // extending takes no extra stock
	clk.Advance(45 * time.Second)
	wantAvail(t, inv, "a", 0) // would have expired at +60s without the extension
	clk.Advance(30 * time.Second)
	wantAvail(t, inv, "a", 3)

	_, err = inv.Extend(r.ID, time.Minute)
	wantErr(t, "Extend after expiry", err, ErrExpired)
	_, err = inv.Extend("r-nope", time.Minute)
	wantErr(t, "Extend of an unknown ID", err, ErrNotFound)
	_, err = inv.Extend(r.ID, 0)
	wantErr(t, "Extend with zero ttl", err, ErrInvalidTTL)
	r2 := mustReserve(t, inv, "a", 1, time.Minute)
	if err := inv.Commit(r2.ID); err != nil {
		t.Fatal(err)
	}
	_, err = inv.Extend(r2.ID, time.Minute)
	wantErr(t, "Extend after Commit", err, ErrAlreadyDone)
}

func TestStage2_ExtendKeepsExpiryOrderCorrect(t *testing.T) {
	// An extended reservation must expire at its NEW time, and must not hide
	// the reservations behind it that expire sooner.
	inv, clk := newInv(t, map[string]int{"a": 3})
	r1 := mustReserve(t, inv, "a", 1, 10*time.Second)
	mustReserve(t, inv, "a", 1, 20*time.Second)
	mustReserve(t, inv, "a", 1, 30*time.Second)
	if _, err := inv.Extend(r1.ID, 60*time.Second); err != nil {
		t.Fatal(err)
	}
	clk.Advance(25 * time.Second) // the 20s hold has expired; r1 (60s) and the 30s hold have not
	wantAvail(t, inv, "a", 1)
	clk.Advance(10 * time.Second) // 35s: the 30s hold is gone too
	wantAvail(t, inv, "a", 2)
	clk.Advance(30 * time.Second) // 65s: r1 gone
	wantAvail(t, inv, "a", 3)
}

func TestStage2_ExtendCanShorten(t *testing.T) {
	inv, clk := newInv(t, map[string]int{"a": 1})
	r := mustReserve(t, inv, "a", 1, time.Hour)
	if _, err := inv.Extend(r.ID, time.Minute); err != nil {
		t.Fatal(err)
	}
	clk.Advance(2 * time.Minute)
	wantAvail(t, inv, "a", 1)
}

func TestStage2_Restock(t *testing.T) {
	inv, _ := newInv(t, map[string]int{"a": 1})
	mustReserve(t, inv, "a", 1, time.Minute)
	if err := inv.Restock("a", 4); err != nil {
		t.Fatalf("Restock = %v", err)
	}
	wantStock(t, inv, "a", Stock{Total: 5, Reserved: 1, Available: 4})
	wantErr(t, "Restock of an unknown SKU", inv.Restock("nope", 1), ErrUnknownSKU)
	wantErr(t, "Restock of 0", inv.Restock("a", 0), ErrInvalidQty)
	wantErr(t, "Restock of a negative", inv.Restock("a", -1), ErrInvalidQty)
}

func TestStage2_OverlappingCartsInOppositeOrderDoNotDeadlock(t *testing.T) {
	inv, _ := newInv(t, map[string]int{"a": 100, "b": 100})
	var ok atomic.Int64
	var wg sync.WaitGroup
	for i := 0; i < 1000; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			lines := []Line{{"a", 1}, {"b", 1}}
			if i%2 == 1 {
				lines = []Line{{"b", 1}, {"a", 1}}
			}
			if _, err := inv.ReserveAll(lines, time.Hour); err == nil {
				ok.Add(1)
			} else if err != ErrInsufficient {
				t.Errorf("ReserveAll = %v, want nil or ErrInsufficient", err)
			}
		}()
	}
	within(t, "1000 overlapping carts (a,b vs b,a)", wg.Wait)
	if ok.Load() != 100 {
		t.Fatalf("%d carts succeeded, want exactly 100 (stock for 100)", ok.Load())
	}
	wantStock(t, inv, "a", Stock{Total: 100, Reserved: 100})
	wantStock(t, inv, "b", Stock{Total: 100, Reserved: 100})
}

func TestStage2_ConcurrentCartsLeaveNoPartialHolds(t *testing.T) {
	// b is the scarce line: only 60 carts can succeed. Every failed cart
	// briefly "wanted" a; none may leave a hold on it.
	inv, _ := newInv(t, map[string]int{"a": 100, "b": 60})
	var ok atomic.Int64
	var wg sync.WaitGroup
	for i := 0; i < 400; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := inv.ReserveAll([]Line{{"a", 1}, {"b", 1}}, time.Hour); err == nil {
				ok.Add(1)
			}
		}()
	}
	within(t, "400 carts on a scarce line", wg.Wait)
	if ok.Load() != 60 {
		t.Fatalf("%d carts succeeded, want exactly 60", ok.Load())
	}
	wantStock(t, inv, "a", Stock{Total: 100, Reserved: 60, Available: 40})
	wantStock(t, inv, "b", Stock{Total: 60, Reserved: 60})
}

func TestStage2_ConcurrentDuplicateKeysReserveOnce(t *testing.T) {
	inv, _ := newInv(t, map[string]int{"a": 100})
	ids := make([]string, 50)
	var wg sync.WaitGroup
	for i := range ids {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, err := inv.ReserveKeyed("callback-9", []Line{{"a", 3}}, time.Hour)
			if err != nil {
				t.Errorf("ReserveKeyed = %v", err)
				return
			}
			ids[i] = r.ID
		}()
	}
	wg.Wait()
	for i, id := range ids {
		if id == "" || id != ids[0] {
			t.Fatalf("duplicate callbacks got different reservations: ids[0]=%q ids[%d]=%q", ids[0], i, id)
		}
	}
	wantStock(t, inv, "a", Stock{Total: 100, Reserved: 3, Available: 97}) // 3 units held once, not 150
}

func TestStage2_DistinctKeysAreIndependent(t *testing.T) {
	inv, _ := newInv(t, map[string]int{"a": 10})
	for i := 0; i < 10; i++ {
		if _, err := inv.ReserveKeyed(fmt.Sprintf("k%d", i), []Line{{"a", 1}}, time.Hour); err != nil {
			t.Fatalf("key %d: %v", i, err)
		}
	}
	_, err := inv.ReserveKeyed("k10", []Line{{"a", 1}}, time.Hour)
	wantErr(t, "11th distinct key on 10 units", err, ErrInsufficient)
}
