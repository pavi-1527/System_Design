package inventory

import (
	"fmt"
	"math/rand"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const loanPeriod = 14 * 24 * time.Hour

func mustBorrow(t *testing.T, inv *Inventory, clk *fakeClock, member, sku string) Loan {
	t.Helper()
	l, err := inv.Borrow(member, sku, clk.Now().Add(loanPeriod))
	if err != nil {
		t.Fatalf("Borrow(%q, %q) = %v, want nil", member, sku, err)
	}
	return l
}

func mustEnqueue(t *testing.T, inv *Inventory, member, sku string, qty int, ttl time.Duration) Ticket {
	t.Helper()
	tk, err := inv.Enqueue(member, sku, qty, ttl)
	if err != nil {
		t.Fatalf("Enqueue(%q, %q, %d) = %v, want nil", member, sku, qty, err)
	}
	return tk
}

func wantStatus(t *testing.T, inv *Inventory, tk Ticket, state TicketState, pos int) TicketStatus {
	t.Helper()
	st, err := inv.Status(tk.ID)
	if err != nil {
		t.Fatalf("Status(%s) = %v", tk.ID, err)
	}
	if st.State != state || st.Position != pos {
		t.Fatalf("ticket of %s: state=%q position=%d, want state=%q position=%d", tk.Member, st.State, st.Position, state, pos)
	}
	return st
}

func TestStage3_BorrowAndReturn(t *testing.T) {
	inv, clk := newInv(t, map[string]int{"dune": 2})
	due := clk.Now().Add(loanPeriod)
	l, err := inv.Borrow("ann", "dune", due)
	if err != nil {
		t.Fatalf("Borrow = %v", err)
	}
	if l.ID == "" || l.SKU != "dune" || l.Member != "ann" || !l.DueAt.Equal(due) {
		t.Fatalf("Loan = %+v, want ID set, SKU=dune Member=ann DueAt=%v", l, due)
	}
	wantStock(t, inv, "dune", Stock{Total: 2, Committed: 1, Available: 1})
	if got := inv.Loans("ann"); len(got) != 1 || got[0] != l {
		t.Fatalf("Loans(ann) = %+v, want [%+v]", got, l)
	}
	if got := inv.Loans("bob"); len(got) != 0 {
		t.Fatalf("Loans(bob) = %+v, want none", got)
	}
	if err := inv.Return(l.ID); err != nil {
		t.Fatalf("Return = %v", err)
	}
	wantStock(t, inv, "dune", Stock{Total: 2, Available: 2})
	wantErr(t, "second Return", inv.Return(l.ID), ErrAlreadyDone)
	wantStock(t, inv, "dune", Stock{Total: 2, Available: 2}) // not returned twice
	wantErr(t, "Return of an unknown loan", inv.Return("l-nope"), ErrNotFound)
	if got := inv.Loans("ann"); len(got) != 0 {
		t.Fatalf("Loans(ann) after Return = %+v, want none", got)
	}
}

func TestStage3_BorrowErrors(t *testing.T) {
	inv, clk := newInv(t, map[string]int{"dune": 1})
	due := clk.Now().Add(loanPeriod)
	_, err := inv.Borrow("ann", "nope", due)
	wantErr(t, "Borrow of an unknown SKU", err, ErrUnknownSKU)
	_, err = inv.Borrow("", "dune", due)
	wantErr(t, "Borrow with no member", err, ErrInvalidWho)
	_, err = inv.Borrow("ann", "dune", clk.Now())
	wantErr(t, "Borrow due now", err, ErrInvalidDue)
	mustBorrow(t, inv, clk, "ann", "dune")
	_, err = inv.Borrow("bob", "dune", due)
	wantErr(t, "Borrow with no copies left", err, ErrInsufficient)
}

func TestStage3_BorrowCountsAgainstReservations(t *testing.T) {
	inv, clk := newInv(t, map[string]int{"dune": 1})
	mustReserve(t, inv, "dune", 1, time.Minute)
	_, err := inv.Borrow("ann", "dune", clk.Now().Add(loanPeriod))
	wantErr(t, "Borrow while the only copy is on hold", err, ErrInsufficient)
	clk.Advance(2 * time.Minute)
	mustBorrow(t, inv, clk, "ann", "dune") // the hold lapsed
}

func TestStage3_MemberLimit(t *testing.T) {
	inv, clk := newInv(t, map[string]int{"a": 5, "b": 5})
	inv.SetMemberLimit(2)
	l1 := mustBorrow(t, inv, clk, "ann", "a")
	mustBorrow(t, inv, clk, "ann", "b")
	_, err := inv.Borrow("ann", "a", clk.Now().Add(loanPeriod))
	wantErr(t, "third loan against a limit of 2 (across SKUs)", err, ErrLimitReached)
	wantAvail(t, inv, "a", 4)           // the refused loan took nothing
	mustBorrow(t, inv, clk, "bob", "a") // limits are per member
	if err := inv.Return(l1.ID); err != nil {
		t.Fatal(err)
	}
	mustBorrow(t, inv, clk, "ann", "a") // a returned loan frees a slot
	inv.SetMemberLimit(0)
	mustBorrow(t, inv, clk, "ann", "a") // 0 = unlimited
}

func TestStage3_WaitlistServesOldestFirst(t *testing.T) {
	inv, clk := newInv(t, map[string]int{"dune": 1})
	l := mustBorrow(t, inv, clk, "zed", "dune")
	t1 := mustEnqueue(t, inv, "ann", "dune", 1, time.Hour)
	t2 := mustEnqueue(t, inv, "bob", "dune", 1, time.Hour)
	t3 := mustEnqueue(t, inv, "cy", "dune", 1, time.Hour)
	wantStatus(t, inv, t1, Waiting, 1)
	wantStatus(t, inv, t2, Waiting, 2)
	wantStatus(t, inv, t3, Waiting, 3)

	if err := inv.Return(l.ID); err != nil {
		t.Fatal(err)
	}
	st := wantStatus(t, inv, t1, Ready, 0)
	if len(st.Reservation.Lines) != 1 || st.Reservation.Lines[0] != (Line{"dune", 1}) || st.Reservation.ID == "" {
		t.Fatalf("Ready ticket's Reservation = %+v, want a one-unit hold on dune", st.Reservation)
	}
	wantStatus(t, inv, t2, Waiting, 1) // positions move up
	wantStatus(t, inv, t3, Waiting, 2)
	wantAvail(t, inv, "dune", 0) // the unit is held for ann, not "available"

	loan, err := inv.BorrowHeld(st.Reservation.ID, "ann", clk.Now().Add(loanPeriod))
	if err != nil {
		t.Fatalf("BorrowHeld = %v", err)
	}
	wantStatus(t, inv, t1, Claimed, 0)
	wantStock(t, inv, "dune", Stock{Total: 1, Committed: 1})
	if err := inv.Return(loan.ID); err != nil {
		t.Fatal(err)
	}
	wantStatus(t, inv, t2, Ready, 0)
	wantStatus(t, inv, t3, Waiting, 1)
}

func TestStage3_EnqueueWithStockAvailableIsReadyImmediately(t *testing.T) {
	inv, _ := newInv(t, map[string]int{"dune": 2})
	tk := mustEnqueue(t, inv, "ann", "dune", 2, time.Minute)
	st := wantStatus(t, inv, tk, Ready, 0)
	if st.Reservation.ID == "" {
		t.Fatal("a Ready ticket must carry its Reservation")
	}
	wantAvail(t, inv, "dune", 0)
}

func TestStage3_NoBargingPastTheQueue(t *testing.T) {
	inv, clk := newInv(t, map[string]int{"dune": 1})
	l := mustBorrow(t, inv, clk, "zed", "dune")
	head := mustEnqueue(t, inv, "ann", "dune", 1, time.Hour)
	if err := inv.Return(l.ID); err != nil {
		t.Fatal(err)
	}
	// The returned copy is held for ann: a newcomer must not get it.
	_, err := inv.Reserve("dune", 1, time.Minute)
	wantErr(t, "Reserve of a copy held for the head of the queue", err, ErrInsufficient)
	_, err = inv.Borrow("eve", "dune", clk.Now().Add(loanPeriod))
	wantErr(t, "Borrow of a copy held for the head of the queue", err, ErrInsufficient)
	wantStatus(t, inv, head, Ready, 0)
}

func TestStage3_HeadOfLineBlocksSmallerRequestsBehindIt(t *testing.T) {
	// Strict FIFO: the head wants 3, only 1 is free. Nobody behind it, and no
	// newcomer, may take the free unit; it is not "idle", it is queued for.
	inv, _ := newInv(t, map[string]int{"a": 1})
	head := mustEnqueue(t, inv, "ann", "a", 3, time.Hour)
	next := mustEnqueue(t, inv, "bob", "a", 1, time.Hour)
	wantStatus(t, inv, head, Waiting, 1)
	wantStatus(t, inv, next, Waiting, 2)
	_, err := inv.Reserve("a", 1, time.Minute)
	wantErr(t, "Reserve of a free unit while a larger request heads the queue", err, ErrInsufficient)

	if err := inv.Restock("a", 2); err != nil {
		t.Fatal(err)
	}
	wantStatus(t, inv, head, Ready, 0)
	wantStatus(t, inv, next, Waiting, 1)
	if err := inv.Restock("a", 1); err != nil {
		t.Fatal(err)
	}
	wantStatus(t, inv, next, Ready, 0)
}

func TestStage3_UnclaimedHoldExpiresAndPassesOn(t *testing.T) {
	inv, clk := newInv(t, map[string]int{"dune": 1})
	l := mustBorrow(t, inv, clk, "zed", "dune")
	t1 := mustEnqueue(t, inv, "ann", "dune", 1, 10*time.Minute)
	t2 := mustEnqueue(t, inv, "bob", "dune", 1, 10*time.Minute)
	if err := inv.Return(l.ID); err != nil {
		t.Fatal(err)
	}
	st := wantStatus(t, inv, t1, Ready, 0)
	clk.Advance(9 * time.Minute)
	wantStatus(t, inv, t2, Waiting, 1) // ann still has her hold
	clk.Advance(2 * time.Minute)
	wantStatus(t, inv, t1, Expired, 0)
	wantStatus(t, inv, t2, Ready, 0) // the unit moved to bob without anyone calling Return
	_, err := inv.BorrowHeld(st.Reservation.ID, "ann", clk.Now().Add(loanPeriod))
	wantErr(t, "BorrowHeld on a lapsed hold", err, ErrExpired)
}

func TestStage3_CancelWait(t *testing.T) {
	inv, clk := newInv(t, map[string]int{"dune": 1})
	l := mustBorrow(t, inv, clk, "zed", "dune")
	t1 := mustEnqueue(t, inv, "ann", "dune", 1, time.Hour)
	t2 := mustEnqueue(t, inv, "bob", "dune", 1, time.Hour)
	t3 := mustEnqueue(t, inv, "cy", "dune", 1, time.Hour)

	if err := inv.CancelWait(t2.ID); err != nil {
		t.Fatalf("CancelWait(waiting) = %v", err)
	}
	wantStatus(t, inv, t2, Cancelled, 0)
	wantStatus(t, inv, t3, Waiting, 2)
	wantErr(t, "second CancelWait", inv.CancelWait(t2.ID), ErrAlreadyDone)

	if err := inv.Return(l.ID); err != nil {
		t.Fatal(err)
	}
	wantStatus(t, inv, t1, Ready, 0)
	if err := inv.CancelWait(t1.ID); err != nil { // giving up a ready hold passes it on
		t.Fatalf("CancelWait(ready) = %v", err)
	}
	wantStatus(t, inv, t1, Cancelled, 0)
	wantStatus(t, inv, t3, Ready, 0)
	wantErr(t, "CancelWait of an unknown ticket", inv.CancelWait("t-nope"), ErrNotFound)
	_, err := inv.Status("t-nope")
	wantErr(t, "Status of an unknown ticket", err, ErrNotFound)
}

func TestStage3_CancellingTheHeadUnblocksTheQueue(t *testing.T) {
	inv, _ := newInv(t, map[string]int{"a": 1})
	head := mustEnqueue(t, inv, "ann", "a", 5, time.Hour)
	next := mustEnqueue(t, inv, "bob", "a", 1, time.Hour)
	wantStatus(t, inv, next, Waiting, 2)
	if err := inv.CancelWait(head.ID); err != nil {
		t.Fatal(err)
	}
	wantStatus(t, inv, next, Ready, 0) // the free unit now fits the new head
}

func TestStage3_BorrowHeldChecksWhoOwnsTheHold(t *testing.T) {
	inv, clk := newInv(t, map[string]int{"dune": 1})
	tk := mustEnqueue(t, inv, "ann", "dune", 1, time.Hour)
	st := wantStatus(t, inv, tk, Ready, 0)
	due := clk.Now().Add(loanPeriod)
	_, err := inv.BorrowHeld(st.Reservation.ID, "bob", due)
	wantErr(t, "BorrowHeld by someone else", err, ErrNotYours)
	_, err = inv.BorrowHeld("r-nope", "ann", due)
	wantErr(t, "BorrowHeld of an unknown hold", err, ErrNotFound)
	if _, err := inv.BorrowHeld(st.Reservation.ID, "ann", due); err != nil {
		t.Fatalf("BorrowHeld by the owner = %v", err)
	}
	_, err = inv.BorrowHeld(st.Reservation.ID, "ann", due)
	wantErr(t, "BorrowHeld twice", err, ErrAlreadyDone)
	wantStock(t, inv, "dune", Stock{Total: 1, Committed: 1}) // one unit lent, counted once
}

func TestStage3_BorrowHeldHonoursMemberLimit(t *testing.T) {
	inv, clk := newInv(t, map[string]int{"a": 3})
	inv.SetMemberLimit(1)
	mustBorrow(t, inv, clk, "ann", "a")
	tk := mustEnqueue(t, inv, "ann", "a", 1, time.Hour)
	st := wantStatus(t, inv, tk, Ready, 0)
	_, err := inv.BorrowHeld(st.Reservation.ID, "ann", clk.Now().Add(loanPeriod))
	wantErr(t, "BorrowHeld over the member limit", err, ErrLimitReached)
	wantStatus(t, inv, tk, Ready, 0) // the hold survives the refusal
}

func TestStage3_PruneForgetsOldRecordsOnly(t *testing.T) {
	inv, clk := newInv(t, map[string]int{"a": 10})
	done := mustReserve(t, inv, "a", 1, time.Minute)
	if err := inv.Commit(done.ID); err != nil {
		t.Fatal(err)
	}
	lapsed, err := inv.ReserveKeyed("order-1", []Line{{"a", 1}}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	clk.Advance(48 * time.Hour)
	fresh := mustReserve(t, inv, "a", 1, time.Hour)

	if n := inv.Prune(24 * time.Hour); n != 2 {
		t.Fatalf("Prune removed %d records, want 2 (the committed and the lapsed reservation)", n)
	}
	wantErr(t, "Commit of a pruned reservation", inv.Commit(done.ID), ErrNotFound)
	wantErr(t, "Release of a pruned reservation", inv.Release(lapsed.ID), ErrNotFound)
	if err := inv.Commit(fresh.ID); err != nil {
		t.Fatalf("Commit of an active reservation after Prune = %v: Prune must never touch active holds", err)
	}
	wantStock(t, inv, "a", Stock{Total: 10, Committed: 2, Available: 8}) // counters unaffected
	again, err := inv.ReserveKeyed("order-1", []Line{{"a", 1}}, time.Minute)
	if err != nil || again.ID == lapsed.ID {
		t.Fatalf("after Prune the key is forgotten: got %+v, %v; want a NEW reservation", again, err)
	}
	if n := inv.Prune(24 * time.Hour); n != 0 {
		t.Fatalf("second Prune removed %d, want 0", n)
	}
}

func TestStage3_ConcurrentReservesNeverOversell(t *testing.T) {
	skus := []string{"s0", "s1", "s2", "s3"}
	inv, _ := newInv(t, map[string]int{"s0": 10, "s1": 10, "s2": 10, "s3": 10})
	var ok [4]atomic.Int64
	var wg sync.WaitGroup
	for i := 0; i < 1000; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := inv.Reserve(skus[i%4], 1, time.Hour); err == nil {
				ok[i%4].Add(1)
			} else if err != ErrInsufficient {
				t.Errorf("Reserve = %v, want nil or ErrInsufficient", err)
			}
		}()
	}
	stop := make(chan struct{})
	var rwg sync.WaitGroup
	rwg.Add(1)
	go func() { // observer: Available is never negative, never above Total
		defer rwg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			for _, s := range skus {
				st, err := inv.Stock(s)
				if err != nil || st.Available < 0 || st.Available > st.Total || st.Total != st.Committed+st.Reserved+st.Available {
					t.Errorf("observed impossible Stock(%s) = %+v, %v", s, st, err)
					return
				}
			}
		}
	}()
	within(t, "1000 concurrent reservations", wg.Wait)
	close(stop)
	rwg.Wait()
	for i, s := range skus {
		if ok[i].Load() != 10 {
			t.Fatalf("%s: %d reservations succeeded for 10 units; the check and the hold must be one atomic step", s, ok[i].Load())
		}
		wantStock(t, inv, s, Stock{Total: 10, Reserved: 10})
	}
}

func TestStage3_ConcurrentWaitlistServesExactlyTheFreedUnits(t *testing.T) {
	inv, _ := newInv(t, map[string]int{"dune": 5})
	var held []Reservation
	for i := 0; i < 5; i++ {
		held = append(held, mustReserve(t, inv, "dune", 1, time.Hour))
	}
	tickets := make([]Ticket, 40)
	var wg sync.WaitGroup
	for i := range tickets {
		wg.Add(1)
		go func() {
			defer wg.Done()
			tk, err := inv.Enqueue(fmt.Sprintf("m%02d", i), "dune", 1, time.Hour)
			if err != nil {
				t.Errorf("Enqueue = %v", err)
			}
			tickets[i] = tk
		}()
	}
	within(t, "40 concurrent Enqueue calls", wg.Wait)

	for _, r := range held { // free the 5 units concurrently
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := inv.Release(r.ID); err != nil {
				t.Errorf("Release = %v", err)
			}
		}()
	}
	within(t, "5 concurrent releases", wg.Wait)

	var ready int
	var positions []int
	for _, tk := range tickets {
		st, err := inv.Status(tk.ID)
		if err != nil {
			t.Fatal(err)
		}
		switch st.State {
		case Ready:
			ready++
		case Waiting:
			positions = append(positions, st.Position)
		default:
			t.Fatalf("ticket in state %q", st.State)
		}
	}
	if ready != 5 {
		t.Fatalf("%d tickets are Ready, want exactly 5 (one per freed unit)", ready)
	}
	sort.Ints(positions)
	for i, p := range positions {
		if p != i+1 {
			t.Fatalf("waiting positions %v, want 1..35 without gaps or duplicates", positions)
		}
	}
	wantStock(t, inv, "dune", Stock{Total: 5, Reserved: 5})
}

func TestStage3_ServedInArrivalOrderUnderSequentialEnqueue(t *testing.T) {
	inv, _ := newInv(t, map[string]int{"a": 3})
	held := mustReserve(t, inv, "a", 3, time.Hour)
	var tickets []Ticket
	for i := 0; i < 6; i++ {
		tickets = append(tickets, mustEnqueue(t, inv, fmt.Sprintf("m%d", i), "a", 1, time.Hour))
	}
	if err := inv.Release(held.ID); err != nil {
		t.Fatal(err)
	}
	for i, tk := range tickets {
		want, pos := Ready, 0
		if i >= 3 {
			want, pos = Waiting, i-2
		}
		wantStatus(t, inv, tk, want, pos)
	}
}

func TestStage3_MixedWorkloadKeepsAccountingExact(t *testing.T) {
	skus := []string{"a", "b", "c"}
	inv, clk := newInv(t, map[string]int{"a": 20, "b": 20, "c": 20})
	inv.SetMemberLimit(5)
	var committed, borrowed, returned, restocked atomic.Int64
	var tmu sync.Mutex
	var allTickets []Ticket
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r := rand.New(rand.NewSource(int64(g) + 7))
			var loans []Loan
			member := fmt.Sprintf("m%d", g)
			for i := 0; i < 400; i++ {
				sku := skus[r.Intn(3)]
				switch r.Intn(9) {
				case 0, 1:
					if res, err := inv.Reserve(sku, 1+r.Intn(3), time.Duration(1+r.Intn(5))*time.Minute); err == nil {
						switch r.Intn(3) {
						case 0:
							if inv.Commit(res.ID) == nil {
								committed.Add(int64(res.Lines[0].Qty))
							}
						case 1:
							_ = inv.Release(res.ID)
						}
					}
				case 2:
					other := skus[r.Intn(3)]
					if res, err := inv.ReserveAll([]Line{{sku, 1}, {other, 1}}, time.Minute); err == nil && r.Intn(2) == 0 {
						n := int64(0)
						for _, l := range res.Lines {
							n += int64(l.Qty)
						}
						if inv.Commit(res.ID) == nil {
							committed.Add(n)
						}
					}
				case 3:
					if l, err := inv.Borrow(member, sku, clk.Now().Add(loanPeriod)); err == nil {
						loans = append(loans, l)
						borrowed.Add(1)
					}
				case 4:
					if len(loans) > 0 {
						if inv.Return(loans[0].ID) == nil {
							returned.Add(1)
						}
						loans = loans[1:]
					}
				case 5:
					if tk, err := inv.Enqueue(member, sku, 1, time.Minute); err == nil {
						tmu.Lock()
						allTickets = append(allTickets, tk)
						tmu.Unlock()
						if r.Intn(2) == 0 {
							_ = inv.CancelWait(tk.ID)
						}
					}
				case 6:
					if inv.Restock(sku, 1) == nil {
						restocked.Add(1)
					}
				case 7:
					clk.Advance(time.Duration(r.Intn(40)) * time.Second)
				case 8:
					st, err := inv.Stock(sku)
					if err != nil || st.Available < 0 || st.Total != st.Committed+st.Reserved+st.Available {
						t.Errorf("observed impossible Stock(%s) = %+v, %v", sku, st, err)
					}
				}
			}
		}()
	}
	within(t, "mixed workload", wg.Wait)

	// Withdraw every ticket still queued or holding (cancelling a Ready hold can
	// promote the next waiter, so repeat until none is left), then let time pass.
	for pass := 0; pass < 100; pass++ {
		left := 0
		for _, tk := range allTickets {
			if st, _ := inv.Status(tk.ID); st.State == Waiting || st.State == Ready {
				left++
				_ = inv.CancelWait(tk.ID)
			}
		}
		if left == 0 {
			break
		}
	}
	clk.Advance(24 * time.Hour) // every remaining hold lapses
	var total, comm int
	for _, s := range skus {
		st, _ := inv.Stock(s)
		total += st.Total
		comm += st.Committed
		if st.Reserved != 0 {
			t.Fatalf("Stock(%s) = %+v a day later: Reserved must be 0 once every hold has expired", s, st)
		}
		if st.Available != st.Total-st.Committed {
			t.Fatalf("Stock(%s) = %+v: Available != Total - Committed", s, st)
		}
	}
	if want := 60 + int(restocked.Load()); total != want {
		t.Fatalf("sum of Totals = %d, want 60 + %d restocks", total, restocked.Load())
	}
	if want := int(committed.Load() + borrowed.Load() - returned.Load()); comm != want {
		t.Fatalf("sum of Committed = %d, want commits %d + borrows %d - returns %d = %d: units were lost or double counted",
			comm, committed.Load(), borrowed.Load(), returned.Load(), want)
	}
}

func BenchmarkReserveRelease_Parallel(b *testing.B) {
	for _, nSKU := range []int{1, 16} {
		b.Run(fmt.Sprintf("skus=%d", nSKU), func(b *testing.B) {
			clk := newClock()
			inv := New(clk.Now)
			names := make([]string, nSKU)
			for i := range names {
				names[i] = fmt.Sprintf("s%d", i)
				if err := inv.AddSKU(names[i], 1<<30); err != nil {
					b.Fatal(err)
				}
			}
			var n atomic.Int64
			b.RunParallel(func(pb *testing.PB) {
				sku := names[int(n.Add(1))%nSKU]
				for pb.Next() {
					r, err := inv.Reserve(sku, 1, time.Hour)
					if err != nil {
						b.Error(err)
						return
					}
					_ = inv.Release(r.ID)
				}
			})
		})
	}
}
