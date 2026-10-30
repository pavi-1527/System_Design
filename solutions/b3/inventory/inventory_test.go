package inventory

import (
	"errors"
	"reflect"
	"runtime"
	"sync"
	"testing"
	"time"
)

const testTimeout = 5 * time.Second

// fakeClock is a manually advanced clock, safe for concurrent use.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func newClock() *fakeClock { return &fakeClock{t: time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC)} }

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// within fails the test if fn does not return in time: a deadlock in your
// implementation becomes a message instead of a hung test run.
func within(t *testing.T, what string, fn func()) {
	t.Helper()
	done := make(chan struct{})
	go func() { defer close(done); fn() }()
	select {
	case <-done:
	case <-time.After(testTimeout):
		t.Fatalf("%s did not finish within %v (deadlock, or a lock held across a blocking call?)", what, testTimeout)
	}
}

func newInv(t *testing.T, stock map[string]int) (*Inventory, *fakeClock) {
	t.Helper()
	clk := newClock()
	inv := New(clk.Now)
	if inv == nil {
		t.Fatal("New returned nil")
	}
	for sku, n := range stock {
		if err := inv.AddSKU(sku, n); err != nil {
			t.Fatalf("AddSKU(%q, %d) = %v, want nil", sku, n, err)
		}
	}
	return inv, clk
}

func wantErr(t *testing.T, what string, got, want error) {
	t.Helper()
	if !errors.Is(got, want) {
		t.Fatalf("%s = %v, want an error satisfying errors.Is(err, %v)", what, got, want)
	}
}

func mustReserve(t *testing.T, inv *Inventory, sku string, qty int, ttl time.Duration) Reservation {
	t.Helper()
	r, err := inv.Reserve(sku, qty, ttl)
	if err != nil {
		t.Fatalf("Reserve(%q, %d, %v) = %v, want nil", sku, qty, ttl, err)
	}
	return r
}

func wantStock(t *testing.T, inv *Inventory, sku string, want Stock) {
	t.Helper()
	got, err := inv.Stock(sku)
	if err != nil {
		t.Fatalf("Stock(%q) = %v", sku, err)
	}
	if got != want {
		t.Fatalf("Stock(%q) = %+v, want %+v", sku, got, want)
	}
}

func wantAvail(t *testing.T, inv *Inventory, sku string, want int) {
	t.Helper()
	got, err := inv.Available(sku)
	if err != nil {
		t.Fatalf("Available(%q) = %v", sku, err)
	}
	if got != want {
		t.Fatalf("Available(%q) = %d, want %d", sku, got, want)
	}
}

// ---------------------------------------------------------------- Stage 1

func TestStage1_AddSKUAndAvailable(t *testing.T) {
	inv, _ := newInv(t, map[string]int{"book": 5})
	wantAvail(t, inv, "book", 5)
	wantStock(t, inv, "book", Stock{Total: 5, Available: 5})
	wantErr(t, "AddSKU of an existing SKU", inv.AddSKU("book", 1), ErrSKUExists)
	wantErr(t, "AddSKU with negative total", inv.AddSKU("neg", -1), ErrInvalidQty)
	if err := inv.AddSKU("empty", 0); err != nil {
		t.Fatalf("AddSKU(empty, 0) = %v, want nil (a SKU may start with no stock)", err)
	}
	_, err := inv.Available("nope")
	wantErr(t, "Available of an unknown SKU", err, ErrUnknownSKU)
	_, err = inv.Stock("nope")
	wantErr(t, "Stock of an unknown SKU", err, ErrUnknownSKU)
}

func TestStage1_ReserveHoldsUnits(t *testing.T) {
	inv, clk := newInv(t, map[string]int{"book": 5})
	r := mustReserve(t, inv, "book", 2, time.Minute)
	if r.ID == "" {
		t.Fatal("Reservation.ID is empty")
	}
	if want := []Line{{SKU: "book", Qty: 2}}; !reflect.DeepEqual(r.Lines, want) {
		t.Fatalf("Reservation.Lines = %+v, want %+v", r.Lines, want)
	}
	if want := clk.Now().Add(time.Minute); !r.ExpiresAt.Equal(want) {
		t.Fatalf("ExpiresAt = %v, want now+ttl = %v", r.ExpiresAt, want)
	}
	wantAvail(t, inv, "book", 3)
	wantStock(t, inv, "book", Stock{Total: 5, Reserved: 2, Available: 3})
	r2 := mustReserve(t, inv, "book", 1, time.Minute)
	if r2.ID == r.ID {
		t.Fatalf("two reservations share ID %q", r.ID)
	}
}

func TestStage1_ReserveRejectsWhatDoesNotFit(t *testing.T) {
	inv, _ := newInv(t, map[string]int{"book": 3})
	mustReserve(t, inv, "book", 3, time.Minute) // exactly fits
	_, err := inv.Reserve("book", 1, time.Minute)
	wantErr(t, "Reserve past the stock", err, ErrInsufficient)
	wantStock(t, inv, "book", Stock{Total: 3, Reserved: 3})

	_, err = inv.Reserve("nope", 1, time.Minute)
	wantErr(t, "Reserve of an unknown SKU", err, ErrUnknownSKU)
	_, err = inv.Reserve("book", 0, time.Minute)
	wantErr(t, "Reserve of 0 units", err, ErrInvalidQty)
	_, err = inv.Reserve("book", -2, time.Minute)
	wantErr(t, "Reserve of negative units", err, ErrInvalidQty)
	_, err = inv.Reserve("book", 1, 0)
	wantErr(t, "Reserve with a zero ttl", err, ErrInvalidTTL)
	_, err = inv.Reserve("book", 1, -time.Second)
	wantErr(t, "Reserve with a negative ttl", err, ErrInvalidTTL)
	wantStock(t, inv, "book", Stock{Total: 3, Reserved: 3}) // failed calls changed nothing
}

func TestStage1_ReleaseReturnsUnitsOnce(t *testing.T) {
	inv, _ := newInv(t, map[string]int{"book": 3})
	r := mustReserve(t, inv, "book", 2, time.Minute)
	if err := inv.Release(r.ID); err != nil {
		t.Fatalf("Release = %v", err)
	}
	wantAvail(t, inv, "book", 3)
	wantErr(t, "second Release", inv.Release(r.ID), ErrAlreadyDone)
	wantAvail(t, inv, "book", 3) // a repeated Release must not return the units twice
	wantErr(t, "Release of an unknown ID", inv.Release("r-nope"), ErrNotFound)
	wantErr(t, "Commit after Release", inv.Commit(r.ID), ErrAlreadyDone)
}

func TestStage1_CommitRemovesStockForGood(t *testing.T) {
	inv, clk := newInv(t, map[string]int{"book": 3})
	r := mustReserve(t, inv, "book", 2, time.Minute)
	if err := inv.Commit(r.ID); err != nil {
		t.Fatalf("Commit = %v", err)
	}
	wantStock(t, inv, "book", Stock{Total: 3, Committed: 2, Available: 1})
	clk.Advance(time.Hour) // the TTL passing must not give committed units back
	wantStock(t, inv, "book", Stock{Total: 3, Committed: 2, Available: 1})
	wantErr(t, "second Commit", inv.Commit(r.ID), ErrAlreadyDone)
	wantErr(t, "Release after Commit", inv.Release(r.ID), ErrAlreadyDone)
	wantStock(t, inv, "book", Stock{Total: 3, Committed: 2, Available: 1})
	wantErr(t, "Commit of an unknown ID", inv.Commit("r-nope"), ErrNotFound)
}

func TestStage1_ExpiryIsLazyAndExact(t *testing.T) {
	inv, clk := newInv(t, map[string]int{"book": 2})
	r := mustReserve(t, inv, "book", 2, time.Minute)
	wantAvail(t, inv, "book", 0)

	clk.Advance(time.Minute - time.Nanosecond)
	wantAvail(t, inv, "book", 0) // still active one nanosecond before ExpiresAt
	clk.Advance(time.Nanosecond)
	wantAvail(t, inv, "book", 2) // at ExpiresAt exactly: expired (active means now < ExpiresAt)
	wantStock(t, inv, "book", Stock{Total: 2, Available: 2})

	wantErr(t, "Commit of an expired reservation", inv.Commit(r.ID), ErrExpired)
	wantErr(t, "Release of an expired reservation", inv.Release(r.ID), ErrExpired)
	wantAvail(t, inv, "book", 2)
}

func TestStage1_CommitJustBeforeExpiryWins(t *testing.T) {
	inv, clk := newInv(t, map[string]int{"book": 1})
	r := mustReserve(t, inv, "book", 1, time.Minute)
	clk.Advance(time.Minute - time.Nanosecond)
	if err := inv.Commit(r.ID); err != nil {
		t.Fatalf("Commit one nanosecond before expiry = %v, want nil", err)
	}
	clk.Advance(time.Second)
	wantStock(t, inv, "book", Stock{Total: 1, Committed: 1})
}

func TestStage1_ExpiredUnitsCanBeReservedAgain(t *testing.T) {
	inv, clk := newInv(t, map[string]int{"book": 2})
	mustReserve(t, inv, "book", 2, time.Minute)
	_, err := inv.Reserve("book", 1, time.Minute)
	wantErr(t, "Reserve while all units are held", err, ErrInsufficient)
	clk.Advance(2 * time.Minute)
	mustReserve(t, inv, "book", 2, time.Minute) // no ghost holds left behind
	wantAvail(t, inv, "book", 0)
}

func TestStage1_ManyReservationsExpireInTimeOrder(t *testing.T) {
	inv, clk := newInv(t, map[string]int{"book": 10})
	for i := 1; i <= 10; i++ {
		mustReserve(t, inv, "book", 1, time.Duration(i)*time.Minute)
	}
	wantAvail(t, inv, "book", 0)
	for i := 1; i <= 10; i++ {
		clk.Advance(time.Minute)
		wantAvail(t, inv, "book", i)
	}
}

func TestStage1_ReturnedReservationIsACopy(t *testing.T) {
	inv, _ := newInv(t, map[string]int{"book": 5})
	r := mustReserve(t, inv, "book", 2, time.Minute)
	r.Lines[0].Qty = 5 // the caller scribbles on its copy
	r.Lines[0].SKU = "other"
	if err := inv.Commit(r.ID); err != nil {
		t.Fatalf("Commit = %v", err)
	}
	wantStock(t, inv, "book", Stock{Total: 5, Committed: 2, Available: 3})
}

func TestStage1_NoBackgroundGoroutines(t *testing.T) {
	// There is no Close method: an implementation that starts a janitor
	// goroutine per Inventory leaks it forever.
	before := runtime.NumGoroutine()
	for i := 0; i < 50; i++ {
		inv, _ := newInv(t, map[string]int{"book": 3})
		mustReserve(t, inv, "book", 1, time.Minute)
	}
	deadline := time.Now().Add(testTimeout)
	for runtime.NumGoroutine() > before && time.Now().Before(deadline) {
		runtime.Gosched()
		time.Sleep(5 * time.Millisecond)
	}
	if n := runtime.NumGoroutine(); n > before {
		t.Fatalf("%d goroutines now vs %d before: expiry must be lazy (no goroutine or timer per Inventory)", n, before)
	}
}

func TestStage1_StockInvariantHoldsAfterMixedOperations(t *testing.T) {
	inv, clk := newInv(t, map[string]int{"a": 10})
	r1 := mustReserve(t, inv, "a", 3, time.Minute)
	r2 := mustReserve(t, inv, "a", 2, 2*time.Minute)
	mustReserve(t, inv, "a", 1, 3*time.Minute)
	if err := inv.Commit(r1.ID); err != nil {
		t.Fatal(err)
	}
	if err := inv.Release(r2.ID); err != nil {
		t.Fatal(err)
	}
	clk.Advance(90 * time.Second)
	st, _ := inv.Stock("a")
	if st.Total != st.Committed+st.Reserved+st.Available || st.Available < 0 {
		t.Fatalf("Stock = %+v violates Total = Committed + Reserved + Available, Available >= 0", st)
	}
	if want := (Stock{Total: 10, Committed: 3, Reserved: 1, Available: 6}); st != want {
		t.Fatalf("Stock = %+v, want %+v", st, want)
	}
}
