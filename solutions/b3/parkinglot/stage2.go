package parkinglot

import (
	"errors"
	"fmt"
	"time"
)

// ---- Stage 2: pluggable allocation, pluggable pricing, lost tickets --------
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
//
// Pricing contract:
//   - Unpark computes Receipt.Fee with Pricer.Price(ticket, exit), where exit
//     is the clock reading, but never earlier than ticket.EnteredAt.
//   - Price is called after the spot has been freed and outside the lot's
//     lock, so a slow Pricer cannot stall other gates.
//   - Money is Cents; integer arithmetic only.
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
		if a != nil {
			l.alloc = a
		}
	}
}

// WithPricer replaces the default pricer (everything is free).
func WithPricer(p Pricer) Option {
	return func(l *Lot) {
		if p != nil {
			l.pricer = p
		}
	}
}

// WithLostTicketFee sets the minimum fee charged by UnparkLost. Negative
// values are treated as zero.
func WithLostTicketFee(fee Cents) Option {
	return func(l *Lot) { l.lostFee = max(fee, 0) }
}

// UnparkLost releases the spot of the vehicle with the given plate when its
// driver has lost the ticket.
func (l *Lot) UnparkLost(plate string) (Receipt, error) {
	now := l.clock.Now()
	l.mu.Lock()
	id, ok := l.byPlate[plate]
	if !ok {
		l.mu.Unlock()
		return Receipt{}, fmt.Errorf("%w: %q", ErrUnknownPlate, plate)
	}
	t := l.byTicket[id]
	l.release(t)
	l.mu.Unlock()
	return l.receipt(t, now, true), nil
}

// ---- allocation strategies ---------------------------------------------------

type bestFit struct{}

// BestFit takes the smallest spot size that fits anywhere in the lot, lowest
// level first among equals. It keeps big spots free for big vehicles.
func BestFit() Allocator { return bestFit{} }

func (bestFit) Pick(_ Vehicle, pools []Pool) int {
	best := 0
	for i, p := range pools {
		if p.Size < pools[best].Size { // strict: ties keep the lower level
			best = i
		}
	}
	return best
}

type firstFit struct{}

// FirstFit takes the lowest level that has any fitting spot, smallest fitting
// size on that level. When level numbers grow with the walk from the entrance
// this is also "nearest to the entrance".
func FirstFit() Allocator { return firstFit{} }

func (firstFit) Pick(Vehicle, []Pool) int { return 0 }

type spreadLevels struct{}

// SpreadLevels takes the level with the most free fitting spots (lowest level
// on ties), smallest fitting size on that level. It keeps floors evenly loaded.
func SpreadLevels() Allocator { return spreadLevels{} }

func (spreadLevels) Pick(_ Vehicle, pools []Pool) int {
	freeAt := make(map[int]int)
	for _, p := range pools {
		freeAt[p.Level] += p.Free
	}
	best := 0
	for i, p := range pools {
		if freeAt[p.Level] > freeAt[pools[best].Level] { // strict: ties keep the lower level
			best = i
		}
	}
	return best // pools are sorted, so this is the smallest size on that level
}

// ---- pricers -------------------------------------------------------------------

// HourlyConfig configures NewHourlyPricer.
//
// Rules: a stay of at most Grace is free (inclusive; grace is a free pass, not
// a deduction). Otherwise every STARTED hour costs PerHour[kind]. If DailyCap
// has a positive entry for the kind, each started 24-hour block of the stay
// (counted from entry, not calendar midnight) costs at most that cap.
type HourlyConfig struct {
	Grace    time.Duration
	PerHour  map[VehicleKind]Cents // must contain every kind, all >= 0
	DailyCap map[VehicleKind]Cents // optional; missing or 0 means no cap
}

type hourly struct {
	grace   time.Duration
	perHour map[VehicleKind]Cents
	cap     map[VehicleKind]Cents
}

// NewHourlyPricer validates cfg (ErrInvalidPricing) and copies it, so later
// changes to the caller's maps cannot change prices.
func NewHourlyPricer(cfg HourlyConfig) (Pricer, error) {
	if cfg.Grace < 0 {
		return nil, fmt.Errorf("%w: negative grace", ErrInvalidPricing)
	}
	perHour, err := copyRates(cfg.PerHour, true)
	if err != nil {
		return nil, err
	}
	caps, err := copyRates(cfg.DailyCap, false)
	if err != nil {
		return nil, err
	}
	return hourly{grace: cfg.Grace, perHour: perHour, cap: caps}, nil
}

func (h hourly) Price(t Ticket, exit time.Time) Cents {
	d := max(exit.Sub(t.EnteredAt), 0)
	if d <= h.grace {
		return 0
	}
	rate := h.perHour[t.Kind]
	limit := h.cap[t.Kind]
	if limit <= 0 {
		return Cents(ceilDiv(d, time.Hour)) * rate
	}
	const day = 24 * time.Hour
	days := d / day
	rem := d - days*day
	return Cents(days)*limit + min(Cents(ceilDiv(rem, time.Hour))*rate, limit)
}

type flat struct{ fee map[VehicleKind]Cents }

// NewFlatPricer charges fee[kind] per entry, whatever the stay (even zero
// minutes). fee must contain every kind, all >= 0 (ErrInvalidPricing).
func NewFlatPricer(fee map[VehicleKind]Cents) (Pricer, error) {
	m, err := copyRates(fee, true)
	if err != nil {
		return nil, err
	}
	return flat{fee: m}, nil
}

func (f flat) Price(t Ticket, _ time.Time) Cents { return f.fee[t.Kind] }

func copyRates(in map[VehicleKind]Cents, requireAll bool) (map[VehicleKind]Cents, error) {
	out := make(map[VehicleKind]Cents, len(in))
	for k, v := range in {
		if !validKind(k) || v < 0 {
			return nil, fmt.Errorf("%w: bad entry %v=%d", ErrInvalidPricing, k, v)
		}
		out[k] = v
	}
	if requireAll {
		for _, k := range []VehicleKind{Motorcycle, Car, Truck} {
			if _, ok := out[k]; !ok {
				return nil, fmt.Errorf("%w: no price for %v", ErrInvalidPricing, k)
			}
		}
	}
	return out, nil
}

// ceilDiv returns ceil(d/unit) for d >= 0.
func ceilDiv(d, unit time.Duration) int64 {
	return int64((d + unit - 1) / unit)
}
