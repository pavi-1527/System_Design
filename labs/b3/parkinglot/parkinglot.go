// Package parkinglot models a multi-level parking lot: spots of three sizes,
// vehicles of three kinds, tickets, receipts and live availability.
//
// Lab B3 Problem 3 (75-minute box). Stage 1 is this file; stage 2 (pluggable
// allocation, pricing and lost tickets) lives in stage2.go and stage 3
// (concurrent gates, a snapshot of who is parked) in stage3.go. Treat the
// later files as sealed until the interviewer "announces" them (see
// unit-2-modelling-and-state.md §1).
//
// The types below are the API sheet the interviewer hands you: names, fields
// and signatures are fixed so the tests can compile against any solution. How
// you store things, and where you put the lock, is yours.
//
// Stage 1 contract (single goroutine at a time; concurrency is stage 3):
//   - Park(v) validates the vehicle (ErrInvalidVehicle: empty plate or unknown
//     kind), then rejects a plate that is already inside (ErrAlreadyParked),
//     then picks a spot (ErrLotFull when no FREE spot FITS this kind). That
//     order of precedence is part of the contract. A failed Park changes
//     nothing.
//   - Fit rule: a motorcycle fits any spot, a car fits Compact and Large, a
//     truck fits only Large.
//   - Default allocation ("best fit"): of all free spots that fit, take the
//     smallest size; among equals the lowest level, then the lowest spot
//     number. Spot numbers within a level start at 0 and are assigned Small
//     first, then Compact, then Large.
//   - Park stamps EnteredAt from the injected Clock and issues a ticket whose
//     ID is unique within the lot and never reused, even when spots and plates
//     are.
//   - Unpark(id) frees the spot and returns a Receipt: Stay = clock now minus
//     EnteredAt, never negative (if the clock stepped back, Stay is 0 and
//     ExitedAt = EnteredAt). Fee is 0 until stage 2. A ticket works exactly
//     once; afterwards, and for unknown IDs, the error is ErrUnknownTicket.
//   - A freed spot is reused by the normal rule: lowest number first within
//     its pool, whatever order spots were freed in.
//   - Availability returns a snapshot that belongs to the caller with
//     Free + Occupied == Total, FreeBySize holding all three sizes, and
//     len(FreeByLevel) == number of levels.
package parkinglot

import (
	"errors"
	"fmt"
	"time"
)

// Errors are sentinel values: compare with errors.Is. Wrapping them with
// detail (a plate, a ticket ID) is fine.
var (
	ErrInvalidConfig  = errors.New("parkinglot: invalid lot configuration")
	ErrInvalidVehicle = errors.New("parkinglot: invalid vehicle")
	// ErrLotFull means no FREE spot that FITS this vehicle exists. Spots for
	// smaller vehicles may still be free.
	ErrLotFull       = errors.New("parkinglot: no free spot fits this vehicle")
	ErrAlreadyParked = errors.New("parkinglot: vehicle is already parked")
	ErrUnknownTicket = errors.New("parkinglot: unknown or already used ticket")
)

var errNotImplemented = errors.New("parkinglot: not implemented")

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

// Fits reports whether a vehicle of kind k may park in a spot of size s (see
// the fit rule above). Unknown kinds fit nowhere.
func Fits(k VehicleKind, s SpotSize) bool {
	// TODO(lab)
	_, _ = k, s
	return false
}

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

// Option customises a Lot at construction time. Stage 2 adds options
// (WithAllocator, WithPricer, WithLostTicketFee); your New must apply them in
// order. Nil arguments to the With... constructors are ignored.
type Option func(*Lot)

// WithClock sets the clock used for entry and exit times. The default is the
// system clock.
func WithClock(c Clock) Option {
	return func(l *Lot) {
		// TODO(lab)
		_ = c
	}
}

// Lot is a parking lot. The zero value is not usable; call New.
type Lot struct {
	// TODO(lab): fields. Think about: how you find "a free spot that fits" fast,
	// how you find a ticket (and a plate) fast, what is immutable after New,
	// and (later stages) the allocator, the pricer and the lock.
}

// New builds a lot. It returns ErrInvalidConfig for no levels, a negative
// count, or a lot with no spots at all. Options are applied in order.
func New(cfg Config, opts ...Option) (*Lot, error) {
	// TODO(lab)
	_, _ = cfg, opts
	return nil, errNotImplemented
}

// Park admits a vehicle: it picks a spot, issues a ticket and records the
// entry time.
func (l *Lot) Park(v Vehicle) (Ticket, error) {
	// TODO(lab)
	_ = v
	return Ticket{}, errNotImplemented
}

// Unpark releases the spot held by ticketID and returns the receipt.
func (l *Lot) Unpark(ticketID string) (Receipt, error) {
	// TODO(lab)
	_ = ticketID
	return Receipt{}, errNotImplemented
}

// Availability returns a consistent snapshot of free and occupied spots.
func (l *Lot) Availability() Availability {
	// TODO(lab)
	return Availability{}
}
