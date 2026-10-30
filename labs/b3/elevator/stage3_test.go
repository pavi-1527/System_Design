package elevator

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"reflect"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func mustNewController(t *testing.T, f *Fleet, opts ...ControllerOption) *Controller {
	t.Helper()
	c, err := NewController(f, opts...)
	if err != nil {
		t.Fatalf("NewController returned error %v", err)
	}
	if c == nil {
		t.Fatal("NewController returned a nil *Controller with a nil error")
	}
	return c
}

// ---- Car: capacity and emergency stop --------------------------------------

func TestStage3_OverloadKeepsTheDoorsOpenAndSaysSoOncePerStop(t *testing.T) {
	c := mustNewCar(t, 6, WithCapacity(4), WithDoorTicks(1))
	_ = c.RequestCar(0)
	wantTrace(t, ticks(c, 1), "arrive@0/none open@0")
	if err := c.SetLoad(5); err != nil {
		t.Fatalf("SetLoad(5) = %v", err)
	}
	if !c.Overloaded() || !c.Full() || c.Load() != 5 {
		t.Fatalf("Overloaded/Full/Load = %v/%v/%d, want true/true/5 with capacity 4", c.Overloaded(), c.Full(), c.Load())
	}
	wantTrace(t, ticks(c, 1), "overloaded@0") // the moment the doors would have closed
	wantTrace(t, ticks(c, 4), "-", "-", "-", "-")
	if s := c.Status().State; s != DoorsOpen {
		t.Fatalf("state = %v while overloaded, want DoorsOpen: a car must never close and leave overloaded", s)
	}
	_ = c.SetLoad(4) // exactly at capacity is allowed: full, not overloaded
	if c.Overloaded() || !c.Full() {
		t.Fatalf("at load 4 of 4: Overloaded=%v Full=%v, want false/true", c.Overloaded(), c.Full())
	}
	wantTrace(t, ticks(c, 1), "close@0")

	// The next stop is a new stop: the warning comes again, once.
	_ = c.RequestCar(2)
	_ = c.SetLoad(6)
	got := ticks(c, 8)
	n := 0
	for _, g := range got {
		if g == "overloaded@2" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("trace %q has %d overloaded@2 events, want exactly 1", got, n)
	}
}

func TestStage3_UnlimitedCapacityAndLoadValidation(t *testing.T) {
	c := mustNewCar(t, 4, WithDoorTicks(1))
	if err := c.SetLoad(-1); !errors.Is(err, ErrInvalidLoad) {
		t.Fatalf("SetLoad(-1) = %v, want ErrInvalidLoad", err)
	}
	_ = c.SetLoad(10_000)
	if c.Overloaded() || c.Full() {
		t.Fatal("a car without WithCapacity is never full or overloaded")
	}
	_ = c.RequestCar(0)
	wantTrace(t, ticks(c, 2), "arrive@0/none open@0", "close@0")
	if _, err := NewCar(4, WithCapacity(-1)); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("NewCar(WithCapacity(-1)) error = %v, want ErrInvalidConfig", err)
	}
}

func TestStage3_HaltedStateTransitions(t *testing.T) {
	for _, from := range []State{Idle, MovingUp, MovingDown, DoorsOpen} {
		if !CanTransition(from, Halted) {
			t.Errorf("CanTransition(%v, Halted) = false: an emergency stop must be possible from every state", from)
		}
	}
	if !CanTransition(Halted, Idle) {
		t.Error("CanTransition(Halted, Idle) = false: Resume must be possible")
	}
	for _, to := range []State{MovingUp, MovingDown, DoorsOpen, Halted} {
		if CanTransition(Halted, to) {
			t.Errorf("CanTransition(Halted, %v) = true: a halted car must pass through Idle after Resume", to)
		}
	}
	// Stage 1's table must survive the extension.
	if CanTransition(MovingUp, MovingDown) || CanTransition(DoorsOpen, MovingUp) {
		t.Error("the stage 3 edges must not open up reversal mid-shaft or moving with the doors open")
	}
}

func TestStage3_EmergencyStopFreezesTheCarAndResumeCarriesOn(t *testing.T) {
	c := mustNewCar(t, 10)
	_ = c.RequestCar(6)
	wantTrace(t, ticks(c, 2), "move@1", "move@2")
	c.EmergencyStop()
	c.EmergencyStop() // idempotent: one stop, one event
	wantTrace(t, ticks(c, 1), "estop@2")
	for i := 0; i < 20; i++ {
		if g := tick(c); g != "-" {
			t.Fatalf("halted car produced %q on tick %d, want nothing", g, i)
		}
	}
	if s := c.Status(); s.State != Halted || s.Floor != 2 {
		t.Fatalf("status = %+v, want Halted at floor 2", s)
	}
	if err := c.RequestCar(1); err != nil { // people can still press buttons; the request is kept
		t.Fatalf("RequestCar while halted = %v", err)
	}
	c.Resume()
	if s := c.Status(); s.State != Idle || s.Pending != 2 {
		t.Fatalf("after Resume: %+v, want Idle with both requests still pending", s)
	}
	if got := arrivalFloors(runToIdle(t, c, 100)); !reflect.DeepEqual(got, []int{6, 1}) {
		t.Fatalf("arrivals after Resume = %v, want [6 1]: nothing requested before or during the stop may be forgotten", got)
	}
	c.Resume() // not halted: no effect
	if c.Status().State != Idle {
		t.Fatal("Resume on a running car changed its state")
	}
}

func TestStage3_EmergencyStopWithTheDoorsOpenStillClosesThemAfterResume(t *testing.T) {
	// The audit trail must balance: every Opened is followed by a Closed.
	c := mustNewCar(t, 5, WithStartFloor(3))
	_ = c.RequestCar(3)
	wantTrace(t, ticks(c, 1), "arrive@3/none open@3")
	c.EmergencyStop()
	wantTrace(t, ticks(c, 2), "estop@3", "-")
	c.Resume()
	wantTrace(t, ticks(c, 2), "close@3", "-")
	if s := c.Status().State; s != Idle {
		t.Fatalf("state = %v after resume, want Idle", s)
	}
}

// ---- Fleet: full cars, maintenance, emergency stop --------------------------

func TestStage3_FullCarGetsNoNewHallCalls(t *testing.T) {
	f := mustNewFleet(t, 12, 2, WithCarOptions(WithCapacity(4)))
	if err := f.SetLoad(0, 4); err != nil {
		t.Fatalf("SetLoad = %v", err)
	}
	// Two idle cars at the same floor tie, and ties go to car 0. Car 0 is
	// full, so the call must go to car 1.
	if car := hall(t, f, 5, DirUp); car != 1 {
		t.Fatalf("call went to car %d, want car 1: car 0 is full", car)
	}
	_ = f.SetLoad(0, 3)
	// Same distance for both again (car 1 has one stop at 5); car 0 can take work again.
	if car := hall(t, f, 6, DirUp); car != 0 {
		t.Fatalf("call went to car %d, want car 0 once it has room again", car)
	}
	if err := f.SetLoad(5, 1); !errors.Is(err, ErrInvalidCar) {
		t.Fatalf("SetLoad(5, 1) = %v, want ErrInvalidCar", err)
	}
	if err := f.SetLoad(0, -2); !errors.Is(err, ErrInvalidLoad) {
		t.Fatalf("SetLoad(0, -2) = %v, want ErrInvalidLoad", err)
	}
}

func TestStage3_CallsWaitWhenEveryCarIsFullAndAreAssignedTheMomentOneHasRoom(t *testing.T) {
	f := mustNewFleet(t, 12, 2, WithCarOptions(WithCapacity(2)))
	_ = f.SetLoad(0, 2)
	_ = f.SetLoad(1, 2)
	if car := hall(t, f, 3, DirUp); car != -1 {
		t.Fatalf("call returned car %d, want -1: every car is full, the call must wait, not be dropped", car)
	}
	if owner, ok := f.Owner(HallCall{3, DirUp}); !ok || owner != -1 || f.Outstanding() != 1 {
		t.Fatalf("Owner = (%d, %v), Outstanding = %d; want (-1, true) and 1", owner, ok, f.Outstanding())
	}
	_ = f.SetLoad(1, 0) // everyone got out of car 1
	if owner, ok := f.Owner(HallCall{3, DirUp}); !ok || owner != 1 {
		t.Fatalf("Owner = (%d, %v) right after car 1 emptied, want (1, true)", owner, ok)
	}
	got := hallArrivals(fleetRunToIdle(t, f, 100))
	if len(got) != 1 || got[0].Car != 1 || got[0].Floor != 3 {
		t.Fatalf("arrivals = %+v, want one stop at 3 by car 1", got)
	}
}

func TestStage3_TakeOutOfServiceDrainsThenStopsAndReassignsHallCalls(t *testing.T) {
	f := mustNewFleet(t, 12, 2)
	park(t, f, 1, 11)
	if car := hall(t, f, 5, DirUp); car != 0 {
		t.Fatalf("setup: call at 5 went to car %d, want car 0 (5 floors away vs 6)", car)
	}
	_ = f.RequestCar(0, 8) // a passenger already riding car 0
	f.Step()
	f.Step()
	if err := f.TakeOutOfService(0); err != nil {
		t.Fatalf("TakeOutOfService = %v", err)
	}
	if owner, ok := f.Owner(HallCall{5, DirUp}); !ok || owner != 1 {
		t.Fatalf("Owner = (%d, %v) right after maintenance began, want (1, true): hall calls move to a working car at once", owner, ok)
	}
	evs := fleetRunToIdle(t, f, 200)
	evs = append(evs, f.Step()...) // the drained car reports OutOfService on the tick after its last Closed

	var car0 []Event
	for _, e := range evs {
		if e.Car == 0 {
			car0 = append(car0, e.Event)
		}
	}
	if got := arrivalFloors(car0); !reflect.DeepEqual(got, []int{8}) {
		t.Fatalf("car 0 stopped at %v, want only [8]: it delivers its passenger (drain) and takes nothing else", got)
	}
	if last := car0[len(car0)-1]; last.Kind != OutOfService {
		t.Fatalf("car 0's last event = %v, want OutOfService after its final Closed", last)
	}
	var served bool
	for _, e := range hallArrivals(evs) {
		if e.Car == 1 && e.Floor == 5 && e.Dir == DirUp {
			served = true
		}
	}
	if !served {
		t.Fatal("the reassigned call at 5 was never served by car 1")
	}
	// An offline car takes nothing, even when it is the nearest.
	if car := hall(t, f, 8, DirDown); car != 1 {
		t.Fatalf("call went to car %d, want car 1: car 0 is offline", car)
	}
	if err := f.RequestCar(0, 3); !errors.Is(err, ErrOutOfService) {
		t.Fatalf("RequestCar on an offline car = %v, want ErrOutOfService", err)
	}
}

func TestStage3_LastCarLeavingServiceQueuesCallsInsteadOfDroppingThem(t *testing.T) {
	f := mustNewFleet(t, 12, 1)
	hall(t, f, 5, DirUp)
	if err := f.TakeOutOfService(0); err != nil {
		t.Fatal(err)
	}
	if owner, ok := f.Owner(HallCall{5, DirUp}); !ok || owner != -1 {
		t.Fatalf("Owner = (%d, %v), want (-1, true): no car is left, the call must wait", owner, ok)
	}
	if car := hall(t, f, 7, DirDown); car != -1 {
		t.Fatalf("a call during maintenance returned car %d, want -1 (queued)", car)
	}
	var offline bool
	for i := 0; i < 5; i++ {
		for _, e := range f.Step() {
			offline = offline || e.Kind == OutOfService
			if e.Kind == Arrived || e.Kind == Moved {
				t.Fatalf("the offline car did %+v", e)
			}
		}
	}
	if !offline {
		t.Fatal("the car never reported OutOfService")
	}
	if err := f.Restore(0); err != nil {
		t.Fatal(err)
	}
	for _, c := range []HallCall{{5, DirUp}, {7, DirDown}} {
		if owner, ok := f.Owner(c); !ok || owner != 0 {
			t.Fatalf("Owner(%+v) = (%d, %v) after Restore, want (0, true)", c, owner, ok)
		}
	}
	got := hallArrivals(fleetRunToIdle(t, f, 200))
	if len(got) != 2 || got[0].Floor != 5 || got[1].Floor != 7 {
		t.Fatalf("arrivals = %+v, want stops at 5 then 7", got)
	}
}

func TestStage3_EmergencyStopReassignsHallCallsButNotThePassengers(t *testing.T) {
	f := mustNewFleet(t, 12, 2)
	park(t, f, 1, 11)
	hall(t, f, 5, DirUp) // car 0
	_ = f.RequestCar(0, 7)
	f.Step()
	if err := f.EmergencyStop(0); err != nil {
		t.Fatal(err)
	}
	if owner, ok := f.Owner(HallCall{5, DirUp}); !ok || owner != 1 {
		t.Fatalf("Owner = (%d, %v) after the stop, want (1, true)", owner, ok)
	}
	if car := hall(t, f, 1, DirUp); car != 1 {
		t.Fatalf("call at 1 went to car %d, want car 1: car 0 is halted (although it is standing at floor 1)", car)
	}
	var stopped bool
	for i := 0; i < 100 && f.Outstanding() > 0; i++ {
		for _, e := range f.Step() {
			if e.Car == 0 && e.Kind == EmergencyStopped {
				stopped = true
			}
			if e.Car == 0 && e.Kind != EmergencyStopped {
				t.Fatalf("halted car 0 produced %+v", e)
			}
		}
	}
	if !stopped || f.Outstanding() != 0 {
		t.Fatalf("stopped=%v outstanding=%d: car 1 should have served both hall calls while car 0 was halted", stopped, f.Outstanding())
	}
	if err := f.Resume(0); err != nil {
		t.Fatal(err)
	}
	if got := arrivalFloors(eventsOf(0, fleetRunToIdle(t, f, 200))); !reflect.DeepEqual(got, []int{7}) {
		t.Fatalf("car 0 arrivals after Resume = %v, want [7]: the passenger's request survives the stop", got)
	}
}

func eventsOf(car int, evs []FleetEvent) []Event {
	var out []Event
	for _, e := range evs {
		if e.Car == car {
			out = append(out, e.Event)
		}
	}
	return out
}

func TestStage3_FleetOperationsRejectUnknownCars(t *testing.T) {
	f := mustNewFleet(t, 6, 2)
	ops := map[string]func(int) error{
		"SetLoad":          func(i int) error { return f.SetLoad(i, 1) },
		"TakeOutOfService": f.TakeOutOfService,
		"Restore":          f.Restore,
		"EmergencyStop":    f.EmergencyStop,
		"Resume":           f.Resume,
	}
	for name, op := range ops {
		for _, i := range []int{-1, 2} {
			if err := op(i); !errors.Is(err, ErrInvalidCar) {
				t.Errorf("%s(%d) = %v, want ErrInvalidCar", name, i, err)
			}
		}
	}
}

func TestStage3_MaintenanceAndStopsNeverLoseARequest(t *testing.T) {
	// Seeded chaos: calls keep arriving while cars go out of service, halt,
	// resume and come back. Every accepted hall call must still be served.
	const floors = 30
	rng := rand.New(rand.NewSource(7))
	f := mustNewFleet(t, floors, 3)
	accepted := map[HallCall]bool{}
	var all []FleetEvent
	for i := 0; i < 400; i++ {
		if rng.Intn(2) == 0 {
			fl := rng.Intn(floors)
			d := DirUp
			if fl == floors-1 || fl > 0 && rng.Intn(2) == 0 {
				d = DirDown
			}
			hall(t, f, fl, d)
			accepted[HallCall{fl, d}] = true
		}
		switch i {
		case 40:
			_ = f.TakeOutOfService(1)
		case 90:
			_ = f.EmergencyStop(2)
		case 140:
			_ = f.TakeOutOfService(0) // now every car is unavailable: calls must queue
		case 180:
			_ = f.Resume(2)
		case 250:
			_ = f.Restore(1)
		case 300:
			_ = f.Restore(0)
		}
		all = append(all, f.Step()...)
	}
	all = append(all, fleetRunToIdle(t, f, 20_000)...)
	served := map[HallCall]bool{}
	for _, e := range hallArrivals(all) {
		served[HallCall{e.Floor, e.Dir}] = true
	}
	for call := range accepted {
		if !served[call] {
			t.Fatalf("hall call %+v was accepted but never served: it was lost during maintenance or an emergency stop", call)
		}
	}
}

// ---- Controller: the concurrent shell ---------------------------------------

func TestStage3_NewControllerRejectsNilFleet(t *testing.T) {
	if c, err := NewController(nil); !errors.Is(err, ErrInvalidConfig) || c != nil {
		t.Fatalf("NewController(nil) = (%v, %v), want (nil, ErrInvalidConfig)", c, err)
	}
}

func TestStage3_RequestsAreAcceptedWithoutAnyTickRunning(t *testing.T) {
	// Submitting must not wait for the tick loop: nobody is ticking here.
	ctl := mustNewController(t, mustNewFleet(t, 10, 2))
	within(t, "submitting requests with no tick loop running", func() {
		for i := 0; i < 100; i++ {
			if _, err := ctl.RequestHall(1+i%8, DirUp); err != nil {
				t.Errorf("RequestHall = %v", err)
			}
			if err := ctl.RequestCar(i%2, i%10); err != nil {
				t.Errorf("RequestCar = %v", err)
			}
		}
	})
	if n := ctl.Outstanding(); n != 8 {
		t.Fatalf("Outstanding = %d, want 8 distinct up-calls (floors 1..8)", n)
	}
}

func TestStage3_ConcurrentRequestsTicksAndMaintenanceLoseNothing(t *testing.T) {
	const floors, cars = 40, 3
	var calls []HallCall
	for fl := 0; fl < floors-1; fl++ {
		calls = append(calls, HallCall{fl, DirUp})
	}
	for fl := 1; fl < floors; fl++ {
		calls = append(calls, HallCall{fl, DirDown})
	}
	var mu sync.Mutex
	served := map[HallCall]bool{}
	ctl := mustNewController(t, mustNewFleet(t, floors, cars), WithObserver(func(_ uint64, evs []FleetEvent) {
		mu.Lock()
		defer mu.Unlock()
		for _, e := range evs {
			if e.Kind == Arrived && e.Dir != DirNone {
				served[HallCall{e.Floor, e.Dir}] = true
			}
		}
	}))

	stop := make(chan struct{})
	var background sync.WaitGroup
	background.Add(2)
	go func() { // the clock: ticks as fast as it can
		defer background.Done()
		for {
			select {
			case <-stop:
				return
			default:
				ctl.Tick()
				runtime.Gosched()
			}
		}
	}()
	go func() { // an operator: maintenance and emergency stops while traffic flows
		defer background.Done()
		for i := 0; i < 30; i++ {
			_ = ctl.TakeOutOfService(1)
			_ = ctl.EmergencyStop(2)
			runtime.Gosched()
			_ = ctl.Resume(2)
			_ = ctl.Restore(1)
		}
	}()

	var wg sync.WaitGroup
	for i, call := range calls {
		wg.Add(1)
		go func(i int, call HallCall) {
			defer wg.Done()
			if car, err := ctl.RequestHall(call.Floor, call.Dir); err != nil || car < -1 || car >= cars {
				t.Errorf("RequestHall(%+v) = (%d, %v)", call, car, err)
			}
			// An offline car legitimately refuses car calls while the operator
			// has it in maintenance; any other error is a bug.
			if err := ctl.RequestCar(i%cars, (i*7)%floors); err != nil && !errors.Is(err, ErrOutOfService) {
				t.Errorf("RequestCar: %v", err)
			}
			_ = ctl.SetLoad(i%cars, i%3)
		}(i, call)
	}
	within(t, "concurrent requests", wg.Wait)
	close(stop)
	within(t, "background goroutines", background.Wait)

	// Everything is back in service; drain with the test goroutine as the clock.
	within(t, "draining the building", func() {
		for i := 0; i < 200_000; i++ {
			idle := ctl.Outstanding() == 0
			for _, s := range ctl.Statuses() {
				idle = idle && s.Pending == 0 && s.State == Idle
			}
			if idle {
				return
			}
			ctl.Tick()
		}
		t.Error("the building never went idle after 200,000 ticks")
	})
	mu.Lock()
	defer mu.Unlock()
	for _, call := range calls {
		if !served[call] {
			t.Fatalf("hall call %+v was accepted but never served: lost between a submit and a tick, or during a reassignment", call)
		}
	}
}

func TestStage3_TicksAreSerialisedAndObserversMayCallBack(t *testing.T) {
	var ctl *Controller
	var seen []uint64 // deliberately unsynchronised: ticks and observer calls must be serialised
	ctl = mustNewController(t, mustNewFleet(t, 6, 1), WithObserver(func(n uint64, _ []FleetEvent) {
		runtime.Gosched() // widen the window in which an unserialised Tick would interleave
		seen = append(seen, n)
		if n%50 == 0 {
			_, _ = ctl.RequestHall(2, DirUp) // re-entering the controller must not deadlock
		}
	}))
	var wg sync.WaitGroup
	for g := 0; g < 4; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 100; i++ {
				ctl.Tick()
			}
		}()
	}
	within(t, "400 concurrent ticks with a re-entrant observer", wg.Wait)
	if len(seen) != 400 {
		t.Fatalf("observer called %d times, want 400", len(seen))
	}
	for i, n := range seen {
		if n != uint64(i+1) {
			t.Fatalf("observer call %d saw tick %d, want %d: ticks must be reported once each, in order", i, n, i+1)
		}
	}
}

func TestStage3_RunAdvancesOnlyOnTicksAndStopsOnCancel(t *testing.T) {
	baseline := runtime.NumGoroutine()
	var count atomic.Int64
	ctl := mustNewController(t, mustNewFleet(t, 8, 1), WithObserver(func(uint64, []FleetEvent) { count.Add(1) }))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ticks := make(chan struct{})
	done := make(chan error, 1)
	go func() { done <- ctl.Run(ctx, ticks) }()

	time.Sleep(25 * time.Millisecond) // a polling loop would have ticked thousands of times by now
	if n := count.Load(); n != 0 {
		t.Fatalf("%d ticks happened with nobody sending on the tick channel: Run must block, not poll", n)
	}
	if _, err := ctl.RequestHall(3, DirUp); err != nil {
		t.Fatal(err)
	}
	const sent = 12
	for i := 0; i < sent; i++ {
		select {
		case ticks <- struct{}{}:
		case <-time.After(testTimeout):
			t.Fatalf("Run stopped receiving ticks after %d", i)
		}
	}
	// The Run loop is now running (it received a tick): a second Run must refuse.
	if err := ctl.Run(context.Background(), make(chan struct{})); !errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("second Run = %v, want ErrAlreadyRunning", err)
	}
	deadline := time.Now().Add(testTimeout)
	for count.Load() < sent {
		if time.Now().After(deadline) {
			t.Fatalf("observer saw %d ticks, want %d", count.Load(), sent)
		}
		time.Sleep(time.Millisecond)
	}
	if n := count.Load(); n != sent {
		t.Fatalf("%d ticks for %d signals", n, sent)
	}
	if ctl.Outstanding() != 0 {
		t.Fatal("twelve ticks should be enough for the car to reach floor 3 and open")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Run returned %v, want context.Canceled", err)
		}
	case <-time.After(testTimeout):
		t.Fatal("Run did not return after the context was cancelled")
	}
	waitForGoroutines(t, baseline)
}

func TestStage3_RunReturnsWhenTicksCloseAndDoesNotTickAfterCancel(t *testing.T) {
	var count atomic.Int64
	ctl := mustNewController(t, mustNewFleet(t, 8, 1), WithObserver(func(uint64, []FleetEvent) { count.Add(1) }))

	closed := make(chan struct{})
	close(closed)
	within(t, "Run on a closed tick channel", func() {
		if err := ctl.Run(context.Background(), closed); err != nil {
			t.Errorf("Run on a closed tick channel = %v, want nil", err)
		}
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	ready := make(chan struct{}, 1)
	ready <- struct{}{}
	within(t, "Run with a cancelled context", func() {
		if err := ctl.Run(ctx, ready); !errors.Is(err, context.Canceled) {
			t.Errorf("Run with a cancelled context = %v, want context.Canceled", err)
		}
	})
	if n := count.Load(); n != 0 {
		t.Fatalf("%d ticks happened although the context was already cancelled", n)
	}
	// Run can be started again after it returned.
	if err := ctl.Run(context.Background(), closed); err != nil {
		t.Fatalf("second Run after the first returned = %v, want nil", err)
	}
}

func TestStage3_TickerDropsInsteadOfQueuingAndStopsWithItsContext(t *testing.T) {
	baseline := runtime.NumGoroutine()
	if ch, err := Ticker(context.Background(), 0); !errors.Is(err, ErrInvalidConfig) || ch != nil {
		t.Fatalf("Ticker(0) = (%v, %v), want (nil, ErrInvalidConfig)", ch, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch, err := Ticker(ctx, time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		select {
		case <-ch:
		case <-time.After(testTimeout):
			t.Fatalf("no tick %d within %v", i, testTimeout)
		}
	}
	time.Sleep(25 * time.Millisecond) // a stalled consumer: about 25 tick intervals pass
	if n := len(ch); n > 1 {
		t.Fatalf("%d ticks are queued after a stall: a backlog makes the simulation fast-forward when the consumer wakes up; drop ticks instead", n)
	}
	cancel()
	deadline := time.After(testTimeout)
	for open := true; open; {
		select {
		case _, ok := <-ch:
			open = ok
		case <-deadline:
			t.Fatal("the tick channel was not closed after the context was cancelled")
		}
	}
	waitForGoroutines(t, baseline)
}

func TestStage3_ControllerIsDrivenEndToEndByATickChannel(t *testing.T) {
	// The whole production wiring, with the clock replaced by a channel the
	// test owns: requests from outside, ticks from the "clock", events out.
	var mu sync.Mutex
	var trace []string
	ctl := mustNewController(t, mustNewFleet(t, 10, 1, WithCarOptions(WithDoorTicks(1))), WithObserver(func(_ uint64, evs []FleetEvent) {
		mu.Lock()
		defer mu.Unlock()
		for _, e := range evs {
			trace = append(trace, fmt.Sprintf("car%d:%s", e.Car, render(e.Event)))
		}
	}))
	ticks := make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- ctl.Run(ctx, ticks) }()
	_, _ = ctl.RequestHall(2, DirUp)
	for i := 0; i < 5; i++ {
		ticks <- struct{}{}
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("Run = %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	want := []string{"car0:move@1", "car0:move@2", "car0:arrive@2/up", "car0:open@2", "car0:close@2"}
	if !reflect.DeepEqual(trace, want) {
		t.Fatalf("trace = %q\nwant  %q", trace, want)
	}
}
