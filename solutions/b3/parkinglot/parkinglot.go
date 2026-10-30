// Package parkinglot models a multi-level parking lot: spots of three sizes,
// vehicles of three kinds, tickets, receipts and live availability.
//
// Lab B3 Problem 3 (75-minute box). Stage 1 is this file; stage 2 (pluggable
// allocation, pricing and lost tickets) lives in stage2.go and stage 3
// (concurrent gates, a snapshot of who is parked) in stage3.go.
//
// Reference design, in one paragraph: the lot is a set of "pools", one per
// (level, spot size), each holding the numbers of its free spots in a min-heap
// so "lowest-numbered free spot" is O(log n). Two maps index the live
// tickets: by ticket ID (Unpark) and by plate (duplicate detection, lost
// tickets). ONE mutex guards pools and maps. What varies between deployments
// (which pool a vehicle goes to, what a stay costs, what time it is) sits
// behind three small interfaces: Allocator, Pricer and Clock. The two that are
// user code that can be slow (Pricer, Clock) are called outside the lock; the
// Allocator must be called inside it, so its contract says "pure and fast".
package parkinglot

import (
	"container/heap"
	"errors"
	"fmt"
	"sync"
	"time"
)

// Errors are sentinel values: compare with errors.Is. The lot may wrap them
// with detail (a plate, a ticket ID).
var (
	ErrInvalidConfig  = errors.New("parkinglot: invalid lot configuration")
	ErrInvalidVehicle = errors.New("parkinglot: invalid vehicle")
	// ErrLotFull means no FREE spot that FITS this vehicle exists. Spots for
	// smaller vehicles may still be free.
	ErrLotFull       = errors.New("parkinglot: no free spot fits this vehicle")
	ErrAlreadyParked = errors.New("parkinglot: vehicle is already parked")
	ErrUnknownTicket = errors.New("parkinglot: unknown or already used ticket")
)

// VehicleKind is what drives in. The zero value is invalid.
type VehicleKind int

// Vehicle kinds.
const (
	Motorcycle VehicleKind = iota + 1
	Car
	Truck
)

func (k VehicleKind) String() string {
	switch k {
	case Motorcycle:
		return "motorcycle"
	case Car:
		return "car"
	case Truck:
		return "truck"
	}
	return fmt.Sprintf("VehicleKind(%d)", int(k))
}

// SpotSize is how big a spot is. The zero value is invalid.
type SpotSize int

// Spot sizes, smallest first.
const (
	Small SpotSize = iota + 1
	Compact
	Large
)

func (s SpotSize) String() string {
	switch s {
	case Small:
		return "small"
	case Compact:
		return "compact"
	case Large:
		return "large"
	}
	return fmt.Sprintf("SpotSize(%d)", int(s))
}

const numSizes = 3

// Fits reports whether a vehicle of kind k may park in a spot of size s: a
// motorcycle fits any spot, a car fits Compact and Large, a truck fits only
// Large. Unknown kinds fit nowhere.
func Fits(k VehicleKind, s SpotSize) bool {
	var need SpotSize
	switch k {
	case Motorcycle:
		need = Small
	case Car:
		need = Compact
	case Truck:
		need = Large
	default:
		return false
	}
	return s >= need && s <= Large
}

func validKind(k VehicleKind) bool { return k >= Motorcycle && k <= Truck }

// Cents is money in minor units. Integer arithmetic only: no floats near money.
type Cents int64

// Vehicle is what a gate sees. Plate is an opaque, case-sensitive identity.
type Vehicle struct {
	Plate string
	Kind  VehicleKind
}

// Spot identifies one physical spot. Within a level, numbers start at 0 and
// are assigned Small spots first, then Compact, then Large.
type Spot struct {
	Level  int
	Number int
	Size   SpotSize
}

// Ticket is issued by Park. It is a value: the lot keeps its own copy.
type Ticket struct {
	ID        string // unique within a Lot, opaque
	Plate     string
	Kind      VehicleKind
	Spot      Spot
	EnteredAt time.Time
}

// Receipt is returned when a vehicle leaves.
type Receipt struct {
	Ticket   Ticket
	ExitedAt time.Time
	Stay     time.Duration // ExitedAt - EnteredAt, never negative
	Fee      Cents         // 0 unless a Pricer is configured (stage 2)
	Lost     bool          // set by UnparkLost (stage 2)
}

// LevelConfig is how many spots of each size one level has.
type LevelConfig struct {
	Small, Compact, Large int
}

// Config describes the lot. Level 0 is first in the slice.
type Config struct {
	Levels []LevelConfig
}

// Availability is a consistent point-in-time view of the whole lot.
// Invariants: Free + Occupied == Total; the FreeBySize values and the
// FreeByLevel values each sum to Free; FreeBySize has an entry for every size.
type Availability struct {
	Total, Free, Occupied int
	FreeBySize            map[SpotSize]int
	FreeByLevel           []int
}

// Clock is the only thing the lot needs from time.
type Clock interface {
	Now() time.Time
}

type systemClock struct{}

func (systemClock) Now() time.Time { return time.Now() }

// Option customises a Lot at construction time. Nil arguments to the With…
// constructors are ignored (the default stays).
type Option func(*Lot)

// WithClock sets the clock used for entry and exit times.
func WithClock(c Clock) Option {
	return func(l *Lot) {
		if c != nil {
			l.clock = c
		}
	}
}

// pool is every spot of one size on one level. free holds the numbers of the
// free spots as a min-heap.
type pool struct {
	level int
	size  SpotSize
	free  intHeap
}

// Lot is a parking lot. The zero value is not usable; call New.
//
// Concurrency: every method is safe for concurrent use. All mutable state sits
// behind one mutex (see the package comment for why). The Clock and the Pricer
// are called outside it; the Allocator is called inside it.
type Lot struct {
	// Immutable after New, so readable without the lock.
	clock   Clock
	alloc   Allocator
	pricer  Pricer
	lostFee Cents
	levels  int
	total   int
	pools   []*pool // index = level*numSizes + int(size)-1: ordered by (level, size)

	mu       sync.Mutex // guards everything below
	byTicket map[string]Ticket
	byPlate  map[string]string // plate -> ticket ID
	nextID   uint64
	// Scratch space for candidates, reused by every Park (hence under mu).
	// This is why an Allocator must not keep the slice it is shown.
	scratchPools []*pool
	scratchView  []Pool
}

// New builds a lot. It returns ErrInvalidConfig for no levels, a negative
// count, or a lot with no spots at all.
func New(cfg Config, opts ...Option) (*Lot, error) {
	if len(cfg.Levels) == 0 {
		return nil, fmt.Errorf("%w: no levels", ErrInvalidConfig)
	}
	l := &Lot{
		clock:    systemClock{},
		alloc:    BestFit(),
		pricer:   freeParking{},
		levels:   len(cfg.Levels),
		byTicket: make(map[string]Ticket),
		byPlate:  make(map[string]string),
	}
	for lvl, lc := range cfg.Levels {
		if lc.Small < 0 || lc.Compact < 0 || lc.Large < 0 {
			return nil, fmt.Errorf("%w: level %d has a negative spot count", ErrInvalidConfig, lvl)
		}
		first := 0
		for i, n := range []int{lc.Small, lc.Compact, lc.Large} {
			p := &pool{level: lvl, size: SpotSize(i + 1), free: make(intHeap, n)}
			for j := range p.free {
				p.free[j] = first + j // ascending slice is already a valid min-heap
			}
			first += n
			l.pools = append(l.pools, p)
		}
		l.total += first
	}
	if l.total == 0 {
		return nil, fmt.Errorf("%w: no spots", ErrInvalidConfig)
	}
	for _, opt := range opts {
		opt(l)
	}
	return l, nil
}

// Park admits a vehicle: it picks a spot, issues a ticket and records the
// entry time. It returns ErrInvalidVehicle (empty plate or unknown kind),
// ErrAlreadyParked (that plate is inside), ErrLotFull (no free spot fits), in
// that order of precedence. A failed Park changes nothing.
func (l *Lot) Park(v Vehicle) (Ticket, error) {
	if v.Plate == "" || !validKind(v.Kind) {
		return Ticket{}, fmt.Errorf("%w: plate %q kind %v", ErrInvalidVehicle, v.Plate, v.Kind)
	}
	now := l.clock.Now() // user code: outside the lock

	l.mu.Lock()
	defer l.mu.Unlock()
	if id, dup := l.byPlate[v.Plate]; dup {
		return Ticket{}, fmt.Errorf("%w: %q holds ticket %s", ErrAlreadyParked, v.Plate, id)
	}
	cands, view := l.candidates(v.Kind)
	if len(cands) == 0 {
		return Ticket{}, fmt.Errorf("%w: %v", ErrLotFull, v.Kind)
	}
	i := l.alloc.Pick(v, view)
	if i < 0 || i >= len(cands) {
		return Ticket{}, fmt.Errorf("%w: Pick returned %d for %d pools", ErrBadAllocation, i, len(cands))
	}
	p := cands[i]
	l.nextID++
	t := Ticket{
		ID:        fmt.Sprintf("T%06d", l.nextID),
		Plate:     v.Plate,
		Kind:      v.Kind,
		Spot:      Spot{Level: p.level, Number: heap.Pop(&p.free).(int), Size: p.size},
		EnteredAt: now,
	}
	l.byTicket[t.ID] = t
	l.byPlate[t.Plate] = t.ID
	return t, nil
}

// Unpark releases the spot held by ticketID and returns the receipt. A ticket
// works exactly once: the second call gets ErrUnknownTicket.
func (l *Lot) Unpark(ticketID string) (Receipt, error) {
	now := l.clock.Now()
	l.mu.Lock()
	t, ok := l.byTicket[ticketID]
	if !ok {
		l.mu.Unlock()
		return Receipt{}, fmt.Errorf("%w: %q", ErrUnknownTicket, ticketID)
	}
	l.release(t)
	l.mu.Unlock()
	return l.receipt(t, now, false), nil
}

// Availability returns a consistent snapshot of free and occupied spots.
func (l *Lot) Availability() Availability {
	l.mu.Lock()
	defer l.mu.Unlock()
	a := Availability{
		Total:       l.total,
		Occupied:    len(l.byTicket),
		FreeBySize:  map[SpotSize]int{Small: 0, Compact: 0, Large: 0},
		FreeByLevel: make([]int, l.levels),
	}
	for _, p := range l.pools {
		n := len(p.free)
		a.Free += n
		a.FreeBySize[p.size] += n
		a.FreeByLevel[p.level] += n
	}
	return a
}

// ---- internals ---------------------------------------------------------------

// candidates returns, in (level, size) order, the pools that have a free spot
// fitting kind: the real pools (to pop from) and their read-only views (for the
// Allocator). Requires l.mu.
func (l *Lot) candidates(kind VehicleKind) ([]*pool, []Pool) {
	ps, view := l.scratchPools[:0], l.scratchView[:0]
	for _, p := range l.pools {
		if len(p.free) > 0 && Fits(kind, p.size) {
			ps = append(ps, p)
			view = append(view, Pool{Level: p.level, Size: p.size, Free: len(p.free)})
		}
	}
	l.scratchPools, l.scratchView = ps, view
	return ps, view
}

// release frees t's spot and forgets the ticket. The ticket must be live
// (present in byTicket), which is what makes a double free impossible: callers
// look it up under the same critical section. Requires l.mu.
func (l *Lot) release(t Ticket) {
	delete(l.byTicket, t.ID)
	delete(l.byPlate, t.Plate)
	p := l.pools[t.Spot.Level*numSizes+int(t.Spot.Size)-1]
	heap.Push(&p.free, t.Spot.Number)
}

// receipt prices a departure. It runs after the lock is released: the vehicle
// has already left, and a slow or broken Pricer must neither stall the other
// gates nor keep a spot occupied.
func (l *Lot) receipt(t Ticket, now time.Time, lost bool) Receipt {
	exit := now
	if exit.Before(t.EnteredAt) {
		exit = t.EnteredAt // the clock stepped back; never bill a negative stay
	}
	fee := l.pricer.Price(t, exit)
	if lost && fee < l.lostFee {
		fee = l.lostFee
	}
	return Receipt{Ticket: t, ExitedAt: exit, Stay: exit.Sub(t.EnteredAt), Fee: fee, Lost: lost}
}

// freeParking is the stage-1 pricer: everyone parks for nothing.
type freeParking struct{}

func (freeParking) Price(Ticket, time.Time) Cents { return 0 }

// intHeap is a min-heap of spot numbers.
type intHeap []int

func (h intHeap) Len() int           { return len(h) }
func (h intHeap) Less(i, j int) bool { return h[i] < h[j] }
func (h intHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }
func (h *intHeap) Push(x any)        { *h = append(*h, x.(int)) }
func (h *intHeap) Pop() any {
	old := *h
	n := len(old)
	x := old[n-1]
	*h = old[:n-1]
	return x
}
