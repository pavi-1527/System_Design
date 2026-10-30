package parkinglot

import (
	"errors"
	"time"
)

// ---- Stage 2: pluggable allocation, pluggable pricing, lost tickets --------
// (announced at about minute 30)
//
// The point of this stage is HOW LITTLE of stage 1 you have to touch. The
// stage-1 rule for choosing a spot becomes one implementation of Allocator
// (BestFit), and "fee = 0" becomes the default Pricer. Park and Unpark only
// gain a call through each interface.
//
// Allocation contract:
//   - The lot hands the Allocator the pools that currently have a free spot
//     fitting the vehicle, ordered by (Level, Size) ascending. The slice is
//     never empty and the Allocator must not modify or keep it.
//   - Pick returns an index into that slice. Anything out of range makes Park
//     fail with ErrBadAllocation and change nothing.
//   - Inside the chosen pool the lot always hands out the LOWEST-numbered
//     free spot. Strategies choose pools, not spots.
//   - Pick runs while the lot's lock is held: it must be pure and fast, and
//     must never call back into the Lot.
//   - The default (stage 1) behaviour is BestFit.
//   - Strategies: BestFit = smallest fitting size anywhere, lowest level among
//     equals. FirstFit = pools[0] (lowest level, then smallest size).
//     SpreadLevels = the level with the most free fitting spots (lowest level
//     on ties), smallest size on that level.
//
// Pricing contract:
//   - Unpark computes Receipt.Fee with Pricer.Price(ticket, exit), where exit
//     is the clock reading, but never earlier than ticket.EnteredAt.
//   - Price is called after the spot has been freed and outside the lot's
//     lock, so a slow Pricer cannot stall other gates.
//   - Money is Cents; integer arithmetic only.
//   - Hourly: a stay of at most Grace is free (inclusive, and a zero stay is
//     free). Otherwise every STARTED hour costs PerHour[kind]; grace is a free
//     pass, not a deduction. If DailyCap[kind] > 0, each started 24-hour block
//     of the stay (counted from entry) costs at most the cap: the fee is
//     fullBlocks*cap + min(cap, startedHours(remainder)*rate).
//   - Flat: fee[kind] per entry, whatever the stay.
//   - Constructors validate (ErrInvalidPricing: a missing kind, a negative
//     amount or grace) and COPY the maps they are given.
//
// Lost tickets:
//   - UnparkLost(plate) looks the vehicle up by plate, frees its spot like
//     Unpark, and sets Receipt.Lost. Fee = max(normal fee, lost-ticket fee):
//     claiming a lost ticket never costs less than leaving honestly.
//   - The old ticket ID stops working (ErrUnknownTicket).
//   - A plate that is not parked gets ErrUnknownPlate.

// Errors added by stage 2.
var (
	ErrBadAllocation  = errors.New("parkinglot: allocator returned an invalid choice")
	ErrInvalidPricing = errors.New("parkinglot: invalid pricing configuration")
	ErrUnknownPlate   = errors.New("parkinglot: no parked vehicle with that plate")
)

// Pool is the Allocator's read-only view of the free spots of one size on one
// level.
type Pool struct {
	Level int
	Size  SpotSize
	Free  int // > 0 whenever a pool is passed to an Allocator
}

// Allocator decides which pool a vehicle is parked in. See the contract above.
type Allocator interface {
	Pick(v Vehicle, pools []Pool) int
}

// Pricer turns a finished stay into money. See the contract above.
type Pricer interface {
	Price(t Ticket, exit time.Time) Cents
}

// WithAllocator replaces the default (BestFit) allocation strategy.
func WithAllocator(a Allocator) Option {
	return func(l *Lot) {
		// TODO(lab)
		_ = a
	}
}

// WithPricer replaces the default pricer (everything is free).
func WithPricer(p Pricer) Option {
	return func(l *Lot) {
		// TODO(lab)
		_ = p
	}
}

// WithLostTicketFee sets the minimum fee charged by UnparkLost. Negative
// values are treated as zero.
func WithLostTicketFee(fee Cents) Option {
	return func(l *Lot) {
		// TODO(lab)
		_ = fee
	}
}

// UnparkLost releases the spot of the vehicle with the given plate when its
// driver has lost the ticket.
func (l *Lot) UnparkLost(plate string) (Receipt, error) {
	// TODO(lab)
	_ = plate
	return Receipt{}, errNotImplemented
}

// BestFit takes the smallest spot size that fits anywhere in the lot, lowest
// level first among equals. It keeps big spots free for big vehicles.
func BestFit() Allocator {
	// TODO(lab)
	return nil
}

// FirstFit takes the lowest level that has any fitting spot, smallest fitting
// size on that level.
func FirstFit() Allocator {
	// TODO(lab)
	return nil
}

// SpreadLevels takes the level with the most free fitting spots (lowest level
// on ties), smallest fitting size on that level.
func SpreadLevels() Allocator {
	// TODO(lab)
	return nil
}

// HourlyConfig configures NewHourlyPricer (rules in the stage-2 contract).
type HourlyConfig struct {
	Grace    time.Duration
	PerHour  map[VehicleKind]Cents // must contain every kind, all >= 0
	DailyCap map[VehicleKind]Cents // optional; missing or 0 means no cap
}

// NewHourlyPricer validates cfg (ErrInvalidPricing) and copies it, so later
// changes to the caller's maps cannot change prices.
func NewHourlyPricer(cfg HourlyConfig) (Pricer, error) {
	// TODO(lab)
	_ = cfg
	return nil, errNotImplemented
}

// NewFlatPricer charges fee[kind] per entry, whatever the stay (even zero
// minutes). fee must contain every kind, all >= 0 (ErrInvalidPricing).
func NewFlatPricer(fee map[VehicleKind]Cents) (Pricer, error) {
	// TODO(lab)
	_ = fee
	return nil, errNotImplemented
}
