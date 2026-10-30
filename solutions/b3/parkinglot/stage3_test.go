package parkinglot

import (
	"bytes"
	"errors"
	"fmt"
	"math/rand"
	"reflect"
	"runtime"
	"runtime/pprof"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// ---- invariant checkers (pure: usable from any goroutine) ------------------------

// checkAvailability verifies the arithmetic every snapshot must satisfy.
func checkAvailability(a Availability) error {
	if a.Free < 0 || a.Free > a.Total {
		return fmt.Errorf("Free = %d outside [0, Total=%d]", a.Free, a.Total)
	}
	if a.Occupied+a.Free != a.Total {
		return fmt.Errorf("Occupied %d + Free %d != Total %d", a.Occupied, a.Free, a.Total)
	}
	if len(a.FreeBySize) != 3 {
		return fmt.Errorf("FreeBySize has %d entries, want one per size: %v", len(a.FreeBySize), a.FreeBySize)
	}
	sizes, lvls := 0, 0
	for _, n := range a.FreeBySize {
		sizes += n
	}
	for _, n := range a.FreeByLevel {
		lvls += n
	}
	if sizes != a.Free || lvls != a.Free {
		return fmt.Errorf("FreeBySize sums to %d and FreeByLevel to %d, Free is %d", sizes, lvls, a.Free)
	}
	return nil
}

// checkParked verifies a Parked() snapshot on its own: unique IDs, plates and
// spots, every vehicle in a spot it fits, ordered by spot.
func checkParked(ts []Ticket) error {
	ids, plates, spots := map[string]bool{}, map[string]bool{}, map[Spot]bool{}
	for i, tk := range ts {
		switch {
		case ids[tk.ID]:
			return fmt.Errorf("ticket ID %s appears twice", tk.ID)
		case plates[tk.Plate]:
			return fmt.Errorf("plate %s is parked twice", tk.Plate)
		case spots[tk.Spot]:
			return fmt.Errorf("spot %+v holds two vehicles", tk.Spot)
		case !Fits(tk.Kind, tk.Spot.Size):
			return fmt.Errorf("%v parked in a %v spot", tk.Kind, tk.Spot.Size)
		}
		ids[tk.ID], plates[tk.Plate], spots[tk.Spot] = true, true, true
		if i > 0 {
			p := ts[i-1].Spot
			if p.Level > tk.Spot.Level || (p.Level == tk.Spot.Level && p.Number >= tk.Spot.Number) {
				return fmt.Errorf("Parked() not ordered by spot: %+v before %+v", p, tk.Spot)
			}
		}
	}
	return nil
}

// runTogether starts n goroutines that all begin at the same moment.
func runTogether(t *testing.T, what string, n int, f func(i int)) {
	t.Helper()
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			f(i)
		}(i)
	}
	close(start)
	within(t, what, wg.Wait)
}

func waitForGoroutines(t *testing.T, baseline int) {
	t.Helper()
	deadline := time.Now().Add(testTimeout)
	for runtime.NumGoroutine() > baseline {
		if time.Now().After(deadline) {
			var buf bytes.Buffer
			_ = pprof.Lookup("goroutine").WriteTo(&buf, 1)
			t.Fatalf("goroutine leak: %d running, baseline %d. Live stacks:\n%s", runtime.NumGoroutine(), baseline, buf.String())
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// ---- tests -------------------------------------------------------------------

func TestStage3_ParkedSnapshotIsOrderedAndOwnedByTheCaller(t *testing.T) {
	l := mustNew(t, levels(2, LevelConfig{Small: 1, Compact: 2, Large: 1}))
	if got := l.Parked(); len(got) != 0 {
		t.Fatalf("Parked() on an empty lot = %v, want none", got)
	}
	var issued []Ticket
	for i, k := range []VehicleKind{Truck, Car, Motorcycle, Car, Truck, Car, Motorcycle} {
		issued = append(issued, park(t, l, fmt.Sprintf("v%d", i), k))
	}
	got := l.Parked()
	if err := checkParked(got); err != nil {
		t.Fatalf("Parked() = %v", err)
	}
	if len(got) != len(issued) {
		t.Fatalf("Parked() has %d tickets, want %d", len(got), len(issued))
	}
	byID := map[string]Ticket{}
	for _, tk := range got {
		byID[tk.ID] = tk
	}
	for _, tk := range issued {
		if byID[tk.ID] != tk {
			t.Errorf("Parked() entry for %s = %+v, want the issued ticket %+v", tk.ID, byID[tk.ID], tk)
		}
	}
	got[0].Plate = "scribbled"
	again := l.Parked()
	if len(again) != len(issued) || again[0].Plate == "scribbled" {
		t.Fatal("changing the slice returned by Parked() changed the lot: the caller must own the snapshot")
	}
	unpark(t, l, issued[0].ID)
	if n := len(l.Parked()); n != len(issued)-1 {
		t.Fatalf("Parked() has %d tickets after one Unpark, want %d", n, len(issued)-1)
	}
}

func TestStage3_ConcurrentParksFillExactlyToCapacity(t *testing.T) {
	// 40 spots (16 compact + 24 large; cars may use both), 200 gates racing:
	// exactly 40 may succeed, all in distinct spots, and 160 must be told "full".
	l := mustNew(t, levels(4, LevelConfig{Small: 0, Compact: 4, Large: 6}))
	const vehicles = 200
	var ok, full atomic.Int64
	var mu sync.Mutex
	spots := map[Spot]string{}
	runTogether(t, "concurrent Parks", vehicles, func(i int) {
		plate := fmt.Sprintf("car-%d", i)
		tk, err := l.Park(Vehicle{Plate: plate, Kind: Car})
		switch {
		case err == nil:
			ok.Add(1)
			mu.Lock()
			if other, dup := spots[tk.Spot]; dup {
				t.Errorf("spot %+v given to both %s and %s", tk.Spot, other, plate)
			}
			spots[tk.Spot] = plate
			mu.Unlock()
		case errors.Is(err, ErrLotFull):
			full.Add(1)
		default:
			t.Errorf("Park(%s) = %v, want success or ErrLotFull", plate, err)
		}
	})
	if ok.Load() != 40 || full.Load() != vehicles-40 {
		t.Fatalf("%d parked and %d rejected, want 40 and %d: capacity must hold exactly under contention", ok.Load(), full.Load(), vehicles-40)
	}
	wantAvail(t, l, 40, 0)
	if err := checkParked(l.Parked()); err != nil {
		t.Fatalf("Parked(): %v", err)
	}
}

func TestStage3_ConcurrentParksOfOnePlateAdmitExactlyOne(t *testing.T) {
	// Two gates scan the same plate at the same instant (a camera double-read,
	// a retried request). Check-then-insert without one critical section lets
	// both through.
	l := mustNew(t, oneLevel(0, 8, 0))
	const rounds, gates = 300, 8
	for r := 0; r < rounds; r++ {
		plate := fmt.Sprintf("DUP-%d", r)
		var ok, dup atomic.Int64
		var winner atomic.Value
		runTogether(t, "concurrent Parks of one plate", gates, func(int) {
			tk, err := l.Park(Vehicle{Plate: plate, Kind: Car})
			switch {
			case err == nil:
				ok.Add(1)
				winner.Store(tk)
			case errors.Is(err, ErrAlreadyParked):
				dup.Add(1)
			default:
				t.Errorf("Park = %v, want success or ErrAlreadyParked", err)
			}
		})
		if ok.Load() != 1 || dup.Load() != gates-1 {
			t.Fatalf("round %d: %d successes and %d ErrAlreadyParked, want 1 and %d: the same car must never be inside twice",
				r, ok.Load(), dup.Load(), gates-1)
		}
		if n := len(l.Parked()); n != 1 {
			t.Fatalf("round %d: %d vehicles parked, want 1", r, n)
		}
		unpark(t, l, winner.Load().(Ticket).ID)
	}
	wantAvail(t, l, 8, 8)
}

func TestStage3_ConcurrentRedemptionsOfOneTicketSucceedOnce(t *testing.T) {
	// A customer double-taps "exit", or two exit gates read the same ticket.
	// Every extra redemption would free the spot again (a double free).
	l := mustNew(t, oneLevel(0, 6, 0))
	const rounds, gates = 200, 8
	for r := 0; r < rounds; r++ {
		plate := fmt.Sprintf("P%d", r)
		tk := park(t, l, plate, Car)
		var ok, unknown atomic.Int64
		runTogether(t, "concurrent redemptions", gates, func(i int) {
			var err error
			switch i % 4 { // mix the two ways of leaving
			case 0:
				_, err = l.UnparkLost(plate)
				if errors.Is(err, ErrUnknownPlate) {
					err = ErrUnknownTicket
				}
			default:
				_, err = l.Unpark(tk.ID)
			}
			switch {
			case err == nil:
				ok.Add(1)
			case errors.Is(err, ErrUnknownTicket):
				unknown.Add(1)
			default:
				t.Errorf("redemption = %v, want success or an unknown-ticket/plate error", err)
			}
		})
		if ok.Load() != 1 || unknown.Load() != gates-1 {
			t.Fatalf("round %d: %d redemptions succeeded, want exactly 1", r, ok.Load())
		}
		wantAvail(t, l, 6, 6)
	}
	// Prove no spot was freed twice: the lot holds exactly 6 cars in 6 different spots.
	seen := map[Spot]bool{}
	for i := 0; i < 6; i++ {
		tk := park(t, l, fmt.Sprintf("fill-%d", i), Car)
		if seen[tk.Spot] {
			t.Fatalf("spot %+v handed out twice: a spot was freed twice", tk.Spot)
		}
		seen[tk.Spot] = true
	}
	if _, err := l.Park(Vehicle{Plate: "extra", Kind: Car}); !errors.Is(err, ErrLotFull) {
		t.Fatalf("7th car in a 6-spot lot: err = %v, want ErrLotFull", err)
	}
}

// tracker is the test's own record of who is parked where. It is the oracle the
// stress test compares the lot against.
type tracker struct {
	mu     sync.Mutex
	active map[string]Ticket // plate -> ticket
	owner  map[Spot]string   // spot -> plate
}

func newTracker() *tracker {
	return &tracker{active: map[string]Ticket{}, owner: map[Spot]string{}}
}

// admit records a successful Park. Because the lot said "yes", a spot or plate
// that is already recorded means the lot broke an invariant.
func (tr *tracker) admit(tk Ticket) error {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	if other, taken := tr.owner[tk.Spot]; taken {
		return fmt.Errorf("spot %+v given to %s while %s still holds it", tk.Spot, tk.Plate, other)
	}
	if prev, dup := tr.active[tk.Plate]; dup {
		return fmt.Errorf("plate %s admitted again (ticket %s) while ticket %s is live", tk.Plate, tk.ID, prev.ID)
	}
	tr.active[tk.Plate] = tk
	tr.owner[tk.Spot] = tk.Plate
	return nil
}

// takeAny removes an arbitrary parked vehicle from the oracle, BEFORE the
// caller asks the lot to release it (so a new owner of the spot can register).
func (tr *tracker) takeAny() (Ticket, bool) {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	for plate, tk := range tr.active {
		delete(tr.active, plate)
		delete(tr.owner, tk.Spot)
		return tk, true
	}
	return Ticket{}, false
}

func TestStage3_StressManyGatesKeepsEveryInvariant(t *testing.T) {
	allocators := map[string]Allocator{"BestFit": BestFit(), "FirstFit": FirstFit(), "SpreadLevels": SpreadLevels()}
	for name, alloc := range allocators {
		t.Run(name, func(t *testing.T) {
			baseline := runtime.NumGoroutine()
			clk := newFakeClock()
			pricer := mustHourly(t, HourlyConfig{PerHour: rates(100, 300, 600), DailyCap: rates(700, 2000, 4000)})
			cfg := levels(3, LevelConfig{Small: 3, Compact: 8, Large: 4}) // 45 spots
			l := mustNew(t, cfg, WithAllocator(alloc), WithPricer(pricer), WithClock(clk), WithLostTicketFee(1000))

			// 90 plates compete for 45 spots; 25 of them are trucks for 12 Large spots,
			// so ErrLotFull and ErrAlreadyParked both happen all the time.
			plates := make([]Vehicle, 90)
			for i := range plates {
				kind := Car
				switch {
				case i < 25:
					kind = Truck
				case i < 45:
					kind = Motorcycle
				}
				plates[i] = Vehicle{Plate: fmt.Sprintf("PL%03d", i), Kind: kind}
			}

			tr := newTracker()
			var parks, leaves, full, dups, failures atomic.Int64
			fail := func(format string, args ...any) {
				if failures.Add(1) <= 5 {
					t.Errorf(format, args...)
				}
			}

			// An auditor hammers the read paths while the gates run.
			stop := make(chan struct{})
			var auditor sync.WaitGroup
			auditor.Add(1)
			go func() {
				defer auditor.Done()
				for {
					select {
					case <-stop:
						return
					default:
					}
					if err := checkAvailability(l.Availability()); err != nil {
						fail("Availability snapshot inconsistent while gates run: %v", err)
					}
					if err := checkParked(l.Parked()); err != nil {
						fail("Parked snapshot inconsistent while gates run: %v", err)
					}
					time.Sleep(100 * time.Microsecond)
				}
			}()

			const gates, opsPerGate = 12, 2500
			var wg sync.WaitGroup
			for g := 0; g < gates; g++ {
				wg.Add(1)
				go func(seed int64) {
					defer wg.Done()
					rng := rand.New(rand.NewSource(seed))
					for i := 0; i < opsPerGate; i++ {
						if rng.Intn(100) < 58 {
							v := plates[rng.Intn(len(plates))]
							tk, err := l.Park(v)
							switch {
							case err == nil:
								parks.Add(1)
								if e := tr.admit(tk); e != nil {
									fail("lot broke an admission invariant: %v", e)
								}
							case errors.Is(err, ErrLotFull):
								full.Add(1)
							case errors.Is(err, ErrAlreadyParked):
								dups.Add(1)
							default:
								fail("Park(%v) = %v", v, err)
							}
							continue
						}
						tk, ok := tr.takeAny()
						if !ok {
							continue
						}
						clk.Advance(time.Duration(rng.Intn(90)) * time.Minute)
						var err error
						if rng.Intn(10) == 0 {
							_, err = l.UnparkLost(tk.Plate)
						} else {
							_, err = l.Unpark(tk.ID)
						}
						if err != nil {
							fail("releasing live ticket %s (%s): %v", tk.ID, tk.Plate, err)
						} else {
							leaves.Add(1)
						}
					}
				}(int64(g) + 1)
			}
			within(t, "the gates' workload", wg.Wait)
			close(stop)
			within(t, "the auditor", auditor.Wait)

			if failures.Load() > 0 {
				t.FailNow()
			}
			if full.Load() == 0 || dups.Load() == 0 || parks.Load() < 1000 {
				t.Fatalf("workload too gentle to prove anything: %d parks, %d lot-full, %d already-parked", parks.Load(), full.Load(), dups.Load())
			}

			// Quiescent: the lot and the oracle must agree exactly.
			if err := checkAvailability(l.Availability()); err != nil {
				t.Fatalf("final Availability: %v", err)
			}
			got := map[string]Ticket{}
			for _, tk := range l.Parked() {
				got[tk.Plate] = tk
			}
			if !reflect.DeepEqual(got, tr.active) {
				t.Fatalf("lot says %d vehicles are parked, the oracle says %d; they must be identical", len(got), len(tr.active))
			}
			if a := l.Availability(); a.Occupied != len(tr.active) {
				t.Fatalf("Occupied = %d, oracle has %d", a.Occupied, len(tr.active))
			}
			for {
				tk, ok := tr.takeAny()
				if !ok {
					break
				}
				unpark(t, l, tk.ID)
				leaves.Add(1)
			}
			a := l.Availability()
			if a.Free != a.Total || a.Occupied != 0 || len(l.Parked()) != 0 {
				t.Fatalf("after draining %d parks / %d leaves the lot is not empty: %+v", parks.Load(), leaves.Load(), a)
			}
			if parks.Load() != leaves.Load() {
				t.Fatalf("%d successful Parks but %d successful exits", parks.Load(), leaves.Load())
			}
			waitForGoroutines(t, baseline) // the lot must not start background goroutines
		})
	}
}

func TestStage3_SlowPricerDoesNotStallOtherGates(t *testing.T) {
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	var once sync.Once
	t.Cleanup(func() { once.Do(func() { close(release) }) })
	slow := pickPricer(func(Ticket, time.Time) Cents {
		entered <- struct{}{}
		<-release
		return 123
	})
	l := mustNew(t, oneLevel(0, 4, 0), WithPricer(slow))
	a := park(t, l, "a", Car)
	park(t, l, "b", Car)

	type result struct {
		r   Receipt
		err error
	}
	done := make(chan result, 1)
	go func() {
		r, err := l.Unpark(a.ID)
		done <- result{r, err}
	}()
	select {
	case <-entered:
	case <-time.After(testTimeout):
		t.Fatal("the pricer was never called")
	}

	// The exit gate is now stuck inside the pricer. Everyone else must carry on.
	within(t, "Park, Availability and Parked while another gate sits in the pricer", func() {
		park(t, l, "c", Car)
		a := l.Availability()
		if a.Occupied != 2 { // b and c: a has already left, its spot is free again
			t.Errorf("Occupied = %d, want 2: the departing car's spot is freed before it is priced", a.Occupied)
		}
		if n := len(l.Parked()); n != 2 {
			t.Errorf("Parked() has %d tickets, want 2", n)
		}
	})
	once.Do(func() { close(release) })
	select {
	case res := <-done:
		if res.err != nil || res.r.Fee != 123 {
			t.Fatalf("Unpark = (%+v, %v), want a receipt with the pricer's fee 123", res.r, res.err)
		}
	case <-time.After(testTimeout):
		t.Fatal("Unpark did not return after the pricer was released")
	}
}

type pickPricer func(Ticket, time.Time) Cents

func (f pickPricer) Price(t Ticket, exit time.Time) Cents { return f(t, exit) }

func TestStage3_CostDoesNotGrowWithLotSize(t *testing.T) {
	// A 50,000-space airport lot. An implementation that scans spots (or
	// tickets) per call does ~1.25 billion steps to fill it.
	const lvls, perLevel = 10, 5000
	l := mustNew(t, levels(lvls, LevelConfig{Compact: perLevel}), WithClock(newFakeClock()))
	total := lvls * perLevel
	tickets := make([]Ticket, 0, total)
	within(t, fmt.Sprintf("filling and draining a %d-spot lot", total), func() {
		for i := 0; i < total; i++ {
			tk, err := l.Park(Vehicle{Plate: fmt.Sprintf("P%d", i), Kind: Car})
			if err != nil {
				t.Errorf("Park #%d = %v", i, err)
				return
			}
			tickets = append(tickets, tk)
			if i%3 == 0 {
				l.Availability() // the entrance display polls constantly
			}
		}
		if _, err := l.Park(Vehicle{Plate: "overflow", Kind: Car}); !errors.Is(err, ErrLotFull) {
			t.Errorf("Park into a full lot = %v, want ErrLotFull", err)
		}
		for i := len(tickets) - 1; i >= 0; i-- {
			if _, err := l.Unpark(tickets[i].ID); err != nil {
				t.Errorf("Unpark #%d = %v", i, err)
				return
			}
		}
	})
	wantAvail(t, l, total, total)
}
