package elevator

import (
	"errors"
	"fmt"
	"math/rand"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"
)

const testTimeout = 5 * time.Second

func mustNewCar(t *testing.T, floors int, opts ...CarOption) *Car {
	t.Helper()
	c, err := NewCar(floors, opts...)
	if err != nil {
		t.Fatalf("NewCar(%d) returned error %v", floors, err)
	}
	if c == nil {
		t.Fatal("NewCar returned a nil *Car with a nil error")
	}
	return c
}

// within fails the test if f does not return within testTimeout, so a livelock
// or an accidentally O(floors)-per-tick design fails instead of hanging.
func within(t *testing.T, what string, f func()) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		f()
	}()
	select {
	case <-done:
	case <-time.After(testTimeout):
		t.Fatalf("%s did not finish within %v (deadlock, livelock, or a per-tick cost that grows with the building)", what, testTimeout)
	}
}

// waitForGoroutines fails the test if the goroutine count does not settle back
// to baseline: a leaked tick loop or ticker is a bug, not noise.
func waitForGoroutines(t *testing.T, baseline int) {
	t.Helper()
	deadline := time.Now().Add(testTimeout)
	for runtime.NumGoroutine() > baseline {
		if time.Now().After(deadline) {
			buf := make([]byte, 1<<16)
			buf = buf[:runtime.Stack(buf, true)]
			t.Fatalf("goroutine leak: %d goroutines running, baseline was %d. Live stacks:\n%s", runtime.NumGoroutine(), baseline, buf)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

var kindNames = map[EventKind]string{
	Moved: "move", Arrived: "arrive", Opened: "open", Closed: "close",
	Overloaded: "overloaded", EmergencyStopped: "estop", OutOfService: "offline",
}

var dirNames = map[Direction]string{DirNone: "none", DirUp: "up", DirDown: "down"}

// render prints an event compactly: move@3, arrive@3/up, open@3, close@3.
func render(e Event) string {
	if e.Kind == Arrived {
		return fmt.Sprintf("arrive@%d/%s", e.Floor, dirNames[e.Dir])
	}
	return fmt.Sprintf("%s@%d", kindNames[e.Kind], e.Floor)
}

// tick steps once and renders the tick: events joined by spaces, "-" if none.
func tick(c *Car) string {
	evs := c.Step()
	if len(evs) == 0 {
		return "-"
	}
	parts := make([]string, len(evs))
	for i, e := range evs {
		parts[i] = render(e)
	}
	return strings.Join(parts, " ")
}

func ticks(c *Car, n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = tick(c)
	}
	return out
}

func wantTrace(t *testing.T, got []string, want ...string) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("event trace (one entry per tick)\n got %q\nwant %q", got, want)
	}
}

// stepChecked steps once and verifies the per-tick physical invariants that
// must hold for ANY request pattern.
func stepChecked(t *testing.T, c *Car) []Event {
	t.Helper()
	before := c.Status()
	evs := c.Step()
	after := c.Status()
	moves := 0
	for _, e := range evs {
		switch e.Kind {
		case Moved:
			moves++
			if before.State == DoorsOpen {
				t.Fatalf("car moved with its doors open (state %v at floor %d)", before.State, before.Floor)
			}
		case Opened:
			if after.State != DoorsOpen {
				t.Fatalf("Opened event but the state after the tick is %v", after.State)
			}
		case Closed:
			if after.State != Idle {
				t.Fatalf("Closed event but the state after the tick is %v, want Idle (doors close, then the car may leave on a later tick)", after.State)
			}
		}
	}
	if d := after.Floor - before.Floor; moves != iabs(d) || moves > 1 {
		t.Fatalf("floor went %d -> %d with %d Moved events: a tick moves at most one floor and reports each move", before.Floor, after.Floor, moves)
	}
	if after.Floor < 0 || after.Floor >= after.Floors {
		t.Fatalf("car left the shaft: floor %d of %d", after.Floor, after.Floors)
	}
	if before.State != after.State && !CanTransition(before.State, after.State) {
		t.Fatalf("illegal transition %v -> %v in one tick", before.State, after.State)
	}
	return evs
}

// runToIdle steps (with invariant checks) until nothing is pending and the
// doors are closed, and returns every event. maxTicks guards against livelock.
func runToIdle(t *testing.T, c *Car, maxTicks int) []Event {
	t.Helper()
	var all []Event
	for i := 0; ; i++ {
		if s := c.Status(); s.Pending == 0 && s.State == Idle {
			return all
		}
		if i >= maxTicks {
			t.Fatalf("car still busy after %d ticks (status %+v): livelock or lost request", maxTicks, c.Status())
		}
		all = append(all, stepChecked(t, c)...)
	}
}

func iabs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

func arrivals(evs []Event) []Event {
	var out []Event
	for _, e := range evs {
		if e.Kind == Arrived {
			out = append(out, e)
		}
	}
	return out
}

func arrivalFloors(evs []Event) []int {
	var out []int
	for _, e := range arrivals(evs) {
		out = append(out, e.Floor)
	}
	return out
}

func TestStage1_NewCarValidatesConfig(t *testing.T) {
	cases := []struct {
		name   string
		floors int
		opts   []CarOption
		want   error
	}{
		{"one floor", 1, nil, ErrInvalidConfig},
		{"zero floors", 0, nil, ErrInvalidConfig},
		{"start below", 5, []CarOption{WithStartFloor(-1)}, ErrInvalidFloor},
		{"start above", 5, []CarOption{WithStartFloor(5)}, ErrInvalidFloor},
		{"zero door ticks", 5, []CarOption{WithDoorTicks(0)}, ErrInvalidConfig},
	}
	for _, tc := range cases {
		c, err := NewCar(tc.floors, tc.opts...)
		if !errors.Is(err, tc.want) {
			t.Errorf("%s: error = %v, want %v", tc.name, err, tc.want)
		}
		if c != nil {
			t.Errorf("%s: returned a non-nil car alongside an error", tc.name)
		}
	}
	c := mustNewCar(t, 10, WithStartFloor(4))
	if s := c.Status(); s.Floor != 4 || s.State != Idle || s.Floors != 10 || s.Pending != 0 || s.Hi != -1 || s.Lo != -1 {
		t.Fatalf("fresh car status = %+v, want floor 4, Idle, 10 floors, nothing pending (Hi/Lo = -1)", s)
	}
}

func TestStage1_StateMachineTable(t *testing.T) {
	legal := [][2]State{
		{Idle, MovingUp}, {Idle, MovingDown}, {Idle, DoorsOpen},
		{MovingUp, DoorsOpen}, {MovingDown, DoorsOpen},
		{MovingUp, Idle}, {MovingDown, Idle}, // settle at a floor when the work vanished
		{DoorsOpen, Idle},
	}
	illegal := [][2]State{
		{MovingUp, MovingDown}, // no instant reversal mid-shaft
		{MovingDown, MovingUp},
		{DoorsOpen, MovingUp}, // never move with the doors open
		{DoorsOpen, MovingDown},
		{Idle, Idle}, {MovingUp, MovingUp}, {DoorsOpen, DoorsOpen}, // staying is not a transition
		{State(0), Idle}, {Idle, State(0)}, {State(0), State(0)}, {State(99), Idle},
	}
	for _, p := range legal {
		if !CanTransition(p[0], p[1]) {
			t.Errorf("CanTransition(%v, %v) = false, want true", p[0], p[1])
		}
	}
	for _, p := range illegal {
		if CanTransition(p[0], p[1]) {
			t.Errorf("CanTransition(%v, %v) = true, want false: this edge would let the car do something physically impossible or unsafe", p[0], p[1])
		}
	}
}

func TestStage1_IdleCarWithNoRequestsDoesNothing(t *testing.T) {
	c := mustNewCar(t, 5)
	wantTrace(t, ticks(c, 4), "-", "-", "-", "-")
	if s := c.Status(); s.State != Idle || s.Floor != 0 {
		t.Fatalf("status = %+v, want Idle at floor 0", s)
	}
}

func TestStage1_CarCallFullTrip(t *testing.T) {
	c := mustNewCar(t, 10, WithDoorTicks(2))
	if err := c.RequestCar(3); err != nil {
		t.Fatalf("RequestCar(3) = %v", err)
	}
	// One tick = one action: three floors, then the doors (open on arrival,
	// one quiet tick, close). Moving off happens on a later tick.
	wantTrace(t, ticks(c, 5),
		"move@1", "move@2",
		"move@3 arrive@3/up open@3",
		"-",
		"close@3",
	)
	if s := c.Status(); s.State != Idle || s.Floor != 3 || s.Pending != 0 {
		t.Fatalf("after the trip status = %+v, want Idle at 3 with nothing pending", s)
	}
	wantTrace(t, ticks(c, 2), "-", "-")
}

func TestStage1_DoorTicksControlsHowLongDoorsStayOpen(t *testing.T) {
	c := mustNewCar(t, 4, WithStartFloor(1), WithDoorTicks(4))
	_ = c.RequestCar(1)
	wantTrace(t, ticks(c, 6), "arrive@1/none open@1", "-", "-", "-", "close@1", "-")
}

func TestStage1_RequestForCurrentFloorOpensDoors(t *testing.T) {
	c := mustNewCar(t, 6, WithStartFloor(4))
	_ = c.RequestCar(4)
	wantTrace(t, ticks(c, 1), "arrive@4/none open@4")
	if s := c.Status(); s.State != DoorsOpen || s.Pending != 0 {
		t.Fatalf("status = %+v, want DoorsOpen with the request consumed", s)
	}

	h := mustNewCar(t, 6, WithStartFloor(4))
	_ = h.RequestHall(4, DirUp)
	wantTrace(t, ticks(h, 1), "arrive@4/up open@4")
}

func TestStage1_DoorsAreAStateNotAFlag(t *testing.T) {
	// Requests arrive while the doors are open. The car must finish the door
	// cycle first and never report a move (or be in a moving state) until the
	// doors have closed.
	c := mustNewCar(t, 8, WithDoorTicks(3))
	_ = c.RequestCar(0)
	_ = c.RequestCar(6)
	var sawOpen, movedAfterClose bool
	for i := 0; i < 20; i++ {
		before := c.Status().State
		evs := stepChecked(t, c)
		if before == DoorsOpen {
			sawOpen = true
			if s := c.Status().State; s == MovingUp || s == MovingDown {
				t.Fatalf("state went DoorsOpen -> %v in one tick: the doors must close (Idle) first", s)
			}
		}
		for _, e := range evs {
			if e.Kind == Moved && sawOpen {
				movedAfterClose = true
			}
		}
	}
	if !sawOpen || !movedAfterClose {
		t.Fatalf("scenario did not exercise open-then-depart (sawOpen=%v, movedAfterClose=%v)", sawOpen, movedAfterClose)
	}
}

func TestStage1_HallCallInOpposingDirectionIsServedOnTheWayBack(t *testing.T) {
	// Trap: stopping at 4 on the way UP because somebody there wants to go
	// DOWN. They would board a car that is about to take them further from
	// where they want to go. A car heading up passes a down-call unless that
	// call is the highest request (the turnaround).
	c := mustNewCar(t, 10)
	_ = c.RequestCar(8)
	_ = c.RequestHall(4, DirDown)
	got := arrivals(runToIdle(t, c, 100))
	want := []Event{{Arrived, 8, DirDown}, {Arrived, 4, DirDown}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("arrivals = %v, want %v: pass 4 going up, turn at 8 (lantern: down), serve 4 going down", got, want)
	}
}

func TestStage1_HallCallInTheSameDirectionIsServedOnTheWay(t *testing.T) {
	c := mustNewCar(t, 10)
	_ = c.RequestCar(8)
	_ = c.RequestHall(4, DirUp)
	got := arrivals(runToIdle(t, c, 100))
	want := []Event{{Arrived, 4, DirUp}, {Arrived, 8, DirUp}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("arrivals = %v, want %v: an up-call below the destination is on the way", got, want)
	}
}

func TestStage1_TurnaroundAtAHallCallTurnsTheLantern(t *testing.T) {
	// The only request is a DOWN call at 6. The car must go up to it (it is
	// the highest request), stop, and leave facing down: direction and state
	// are separate things, and the heading flips at the turnaround while the
	// doors are open.
	c := mustNewCar(t, 10)
	_ = c.RequestHall(6, DirDown)
	var evs []Event
	for c.Status().State != DoorsOpen {
		evs = append(evs, stepChecked(t, c)...)
		if len(evs) > 50 {
			t.Fatal("car never opened at 6")
		}
	}
	if last := evs[len(evs)-2]; last != (Event{Arrived, 6, DirDown}) {
		t.Fatalf("arrival event = %v, want arrive@6 with lantern down", last)
	}
	if s := c.Status(); s.Heading != DirDown || s.Floor != 6 {
		t.Fatalf("status = %+v, want heading Down at floor 6 while the doors are open", s)
	}
	// The passenger chooses floor 2: the car now travels down, not up.
	_ = c.RequestCar(2)
	rest := runToIdle(t, c, 100)
	if got := arrivalFloors(rest); !reflect.DeepEqual(got, []int{2}) {
		t.Fatalf("arrivals after the turnaround = %v, want [2]", got)
	}
	for _, e := range rest {
		if e.Kind == Moved && e.Floor > 6 {
			t.Fatalf("car moved to floor %d after turning at 6", e.Floor)
		}
	}
}

func TestStage1_LookReversesAtTheLastRequestNotTheEndOfTheShaft(t *testing.T) {
	c := mustNewCar(t, 10, WithStartFloor(5))
	_ = c.RequestCar(7)
	_ = c.RequestCar(2)
	evs := runToIdle(t, c, 100)
	if got := arrivalFloors(evs); !reflect.DeepEqual(got, []int{7, 2}) {
		t.Fatalf("arrivals = %v, want [7 2]: 7 is nearer (2 away vs 3), then reverse", got)
	}
	for _, e := range evs {
		if e.Kind == Moved && (e.Floor > 7 || e.Floor < 2) {
			t.Fatalf("car visited floor %d: LOOK turns at the farthest request (2..7), SCAN's trip to the end of the shaft wastes ticks", e.Floor)
		}
	}
}

func TestStage1_KeepsGoingInItsDirectionBeforeServingNearerRequestsBehind(t *testing.T) {
	// Nearest-first would serve 4 (2 floors back from 6) before 9 (3 ahead);
	// the elevator policy keeps its direction, and the passengers already
	// riding up are not made to wait for a detour.
	c := mustNewCar(t, 12)
	_ = c.RequestCar(6)
	for c.Status().Floor < 5 {
		stepChecked(t, c)
	}
	_ = c.RequestCar(4) // behind the car
	_ = c.RequestCar(9) // ahead of the car
	got := arrivalFloors(runToIdle(t, c, 100))
	if !reflect.DeepEqual(got, []int{6, 9, 4}) {
		t.Fatalf("arrivals = %v, want [6 9 4]: finish the upward sweep, then come back for 4", got)
	}
}

func TestStage1_RequestForTheFloorJustPassedWaitsForTheReturnTrip(t *testing.T) {
	// A car never stops mid-shaft. While it is passing floor 2 a passenger
	// presses 2: that is behind the car, so it is served after the turnaround.
	c := mustNewCar(t, 10)
	_ = c.RequestCar(5)
	for c.Status().Floor < 2 {
		stepChecked(t, c)
	}
	_ = c.RequestCar(2)
	got := arrivalFloors(runToIdle(t, c, 100))
	if !reflect.DeepEqual(got, []int{5, 2}) {
		t.Fatalf("arrivals = %v, want [5 2]", got)
	}
}

func TestStage1_DuplicateRequestsAreCoalesced(t *testing.T) {
	c := mustNewCar(t, 10)
	for i := 0; i < 5; i++ {
		_ = c.RequestCar(3)
	}
	for i := 0; i < 3; i++ {
		_ = c.RequestHall(3, DirUp)
	}
	if s := c.Status(); s.Pending != 1 {
		t.Fatalf("Pending = %d after 8 requests for the same floor, want 1", s.Pending)
	}
	if !c.HasCarCall(3) || !c.HasHallCall(3, DirUp) || c.HasHallCall(3, DirDown) || c.HasCarCall(4) {
		t.Fatal("HasCarCall/HasHallCall disagree with what was requested")
	}
	evs := runToIdle(t, c, 50)
	if n := len(arrivals(evs)); n != 1 {
		t.Fatalf("%d stops at floor 3, want exactly 1: the car call and the up-call are served together", n)
	}
	if c.HasCarCall(3) || c.HasHallCall(3, DirUp) {
		t.Fatal("served requests are still reported as pending")
	}
}

func TestStage1_InvalidRequestsAreRejectedAndNotRecorded(t *testing.T) {
	c := mustNewCar(t, 5)
	for _, f := range []int{-1, 5, 100} {
		if err := c.RequestCar(f); !errors.Is(err, ErrInvalidFloor) {
			t.Errorf("RequestCar(%d) = %v, want ErrInvalidFloor", f, err)
		}
	}
	bad := []struct {
		floor int
		dir   Direction
		want  error
	}{
		{0, DirDown, ErrInvalidDirection}, // nothing below the ground floor
		{4, DirUp, ErrInvalidDirection},   // nothing above the top floor
		{2, DirNone, ErrInvalidDirection},
		{2, Direction(9), ErrInvalidDirection},
		{-1, DirUp, ErrInvalidFloor},
		{5, DirDown, ErrInvalidFloor},
	}
	for _, b := range bad {
		if err := c.RequestHall(b.floor, b.dir); !errors.Is(err, b.want) {
			t.Errorf("RequestHall(%d, %v) = %v, want %v", b.floor, b.dir, err, b.want)
		}
	}
	if s := c.Status(); s.Pending != 0 {
		t.Fatalf("Pending = %d: rejected requests must leave no trace", s.Pending)
	}
	wantTrace(t, ticks(c, 2), "-", "-")
}

func TestStage1_RequestsForTheOpenFloorAreAbsorbedOrDeferredButNeverLost(t *testing.T) {
	c := mustNewCar(t, 6, WithStartFloor(2), WithDoorTicks(2))
	_ = c.RequestHall(2, DirUp)
	wantTrace(t, ticks(c, 2), "arrive@2/up open@2", "-")

	_ = c.RequestCar(2)         // already here, doors open: absorbed
	_ = c.RequestHall(2, DirUp) // same direction the car is facing: absorbed
	if p := c.Status().Pending; p != 0 {
		t.Fatalf("Pending = %d, want 0: requests for the open floor in the lantern direction ride along with this stop", p)
	}
	_ = c.RequestHall(2, DirDown) // the other button: not served by this stop
	if p := c.Status().Pending; p != 1 {
		t.Fatalf("Pending = %d, want 1: the opposite call must be remembered", p)
	}
	// The doors keep their schedule (absorbing is not "extend the timer": an
	// extendable timer lets one hammered button hold the car forever), and
	// the deferred call gets its own stop right after.
	wantTrace(t, ticks(c, 2), "close@2", "arrive@2/down open@2")
}

func TestStage1_RandomWorkloadKeepsEveryInvariant(t *testing.T) {
	rng := rand.New(rand.NewSource(8))
	const floors = 15
	c := mustNewCar(t, floors, WithDoorTicks(2))
	for i := 0; i < 600; i++ {
		if rng.Intn(3) == 0 {
			if rng.Intn(2) == 0 {
				_ = c.RequestCar(rng.Intn(floors))
			} else {
				f := rng.Intn(floors)
				d := DirUp
				if f == floors-1 || f > 0 && rng.Intn(2) == 0 {
					d = DirDown
				}
				_ = c.RequestHall(f, d)
			}
		}
		for _, e := range stepChecked(t, c) {
			if e.Kind == Arrived && (c.HasCarCall(e.Floor) || e.Dir != DirNone && c.HasHallCall(e.Floor, e.Dir)) {
				t.Fatalf("arrived at %d (lantern %v) but the request this stop serves is still pending", e.Floor, e.Dir)
			}
		}
	}
	runToIdle(t, c, 2_000) // everything still outstanding must drain: no request is ever lost
}

func TestStage1_TickCostDoesNotGrowWithTheNumberOfFloors(t *testing.T) {
	// 200,000 floors, one request at the top. A design that rescans the floors
	// (or sorts all pending requests) on every tick does ~2*10^10 steps here
	// and misses the time cap by a wide margin; O(1) per tick finishes in
	// milliseconds.
	const floors = 200_000
	c := mustNewCar(t, floors)
	_ = c.RequestCar(floors - 1)
	var moves int
	within(t, fmt.Sprintf("a %d-floor trip", floors), func() {
		for c.Status().State != DoorsOpen {
			for _, e := range c.Step() {
				if e.Kind == Moved {
					moves++
				}
			}
		}
	})
	if moves != floors-1 || c.Status().Floor != floors-1 {
		t.Fatalf("moves = %d, floor = %d; want %d moves and arrival at the top", moves, c.Status().Floor, floors-1)
	}
}
