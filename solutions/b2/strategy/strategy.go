// Package strategy is a fee engine built on the strategy pattern.
//
// Lab B2 Unit 1 §1. A retail bank charges fees on wires, card top-ups,
// overdrafts and so on. Each fee type has its own rule: flat, percentage with
// a floor and a cap, tiered by band. The anti-pattern is one giant
// `switch kind` that every new product edits. The pattern is: a rule is a
// value (a FeeFunc), the engine looks the rule up, and adding a fee type means
// registering new code, never editing the engine.
//
// All money is integer minor units (cents). Never float for money.
package strategy

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"sync"
)

// Money is an amount in minor units (cents).
type Money int64

var (
	// ErrUnknownKind is returned (wrapped, with the kind) for an unregistered fee kind.
	ErrUnknownKind = errors.New("strategy: unknown fee kind")
	// ErrInvalidAmount is returned for a negative transaction amount.
	ErrInvalidAmount = errors.New("strategy: invalid amount")
	// ErrInvalidStrategy is returned for an empty kind, a nil FeeFunc or a malformed tier table.
	ErrInvalidStrategy = errors.New("strategy: invalid strategy")
	// ErrInvalidFee is returned when a strategy produces a negative fee.
	ErrInvalidFee = errors.New("strategy: strategy returned a negative fee")
	// ErrOverflow is returned when a computation would overflow int64.
	ErrOverflow = errors.New("strategy: amount too large")
)

// Txn is the input every strategy receives.
type Txn struct {
	Kind   string
	Amount Money
}

// FeeFunc is one fee rule: the strategy is just a function value.
type FeeFunc func(t Txn) (Money, error)

// Flat charges the same fee for every transaction.
func Flat(fee Money) FeeFunc {
	return func(Txn) (Money, error) { return fee, nil }
}

// Percent charges bps basis points (1 bp = 0.01%) of the amount, rounded half
// up to the nearest minor unit, then clamped to [min, max]. max == 0 means no
// cap. It returns ErrOverflow instead of wrapping around for huge amounts.
func Percent(bps int64, min, max Money) FeeFunc {
	return func(t Txn) (Money, error) {
		if bps > 0 && int64(t.Amount) > (math.MaxInt64-5000)/bps {
			return 0, ErrOverflow
		}
		fee := Money((int64(t.Amount)*bps + 5000) / 10000)
		if fee < min {
			fee = min
		}
		if max > 0 && fee > max {
			fee = max
		}
		return fee, nil
	}
}

// Tier is one band of a marginal fee schedule. BPS applies to the part of the
// amount that falls in this band, which ends at UpTo (bands are contiguous,
// each starting where the previous one ended). UpTo == 0 on the last tier
// means "no limit".
type Tier struct {
	UpTo Money
	BPS  int64
}

// Tiered charges each band of the amount at that band's rate, like income-tax
// brackets (marginal, NOT "the whole amount at the rate of the band it lands
// in"), summing the per-band products and rounding half up once at the end.
// Tiers must be non-empty, strictly ascending in UpTo, with rates >= 0; only
// the last tier may, and must, have UpTo == 0.
func Tiered(tiers []Tier) (FeeFunc, error) {
	if len(tiers) == 0 {
		return nil, fmt.Errorf("%w: no tiers", ErrInvalidStrategy)
	}
	var prev Money
	for i, tr := range tiers {
		last := i == len(tiers)-1
		switch {
		case last && tr.UpTo != 0:
			return nil, fmt.Errorf("%w: last tier must have UpTo 0 (unbounded)", ErrInvalidStrategy)
		case !last && tr.UpTo <= prev:
			return nil, fmt.Errorf("%w: tier %d is not above the previous bound", ErrInvalidStrategy, i)
		case tr.BPS < 0:
			return nil, fmt.Errorf("%w: tier %d has a negative rate", ErrInvalidStrategy, i)
		}
		prev = tr.UpTo
	}
	tiers = append([]Tier(nil), tiers...) // the caller may reuse its slice
	return func(t Txn) (Money, error) {
		amount := int64(t.Amount)
		var num, lower int64
		for _, tr := range tiers {
			if amount <= lower {
				break
			}
			upper := amount
			if tr.UpTo != 0 && int64(tr.UpTo) < amount {
				upper = int64(tr.UpTo)
			}
			portion := upper - lower
			if tr.BPS > 0 && portion > (math.MaxInt64-num)/tr.BPS {
				return 0, ErrOverflow
			}
			num += portion * tr.BPS
			lower = upper
		}
		if num > math.MaxInt64-5000 {
			return 0, ErrOverflow
		}
		return Money((num + 5000) / 10000), nil
	}, nil
}

// Sum adds the fees of several strategies (for example a base fee plus a
// surcharge). The first error aborts.
func Sum(fs ...FeeFunc) FeeFunc {
	return func(t Txn) (Money, error) {
		var total Money
		for _, f := range fs {
			fee, err := f(t)
			if err != nil {
				return 0, err
			}
			total += fee
		}
		return total, nil
	}
}

// Engine looks up the strategy for a fee kind. It is safe for concurrent use,
// and strategies can be registered or replaced while it is serving calls.
type Engine struct {
	mu   sync.RWMutex
	fees map[string]FeeFunc
}

// NewEngine returns an empty engine.
func NewEngine() *Engine { return &Engine{fees: map[string]FeeFunc{}} }

// Register installs f for kind, replacing any previous strategy (fee
// schedules change at runtime). It rejects an empty kind or a nil f with
// ErrInvalidStrategy.
func (e *Engine) Register(kind string, f FeeFunc) error {
	if kind == "" || f == nil {
		return fmt.Errorf("%w: kind %q, nil func %v", ErrInvalidStrategy, kind, f == nil)
	}
	e.mu.Lock()
	e.fees[kind] = f
	e.mu.Unlock()
	return nil
}

// Fee computes the fee for t using the strategy registered for t.Kind.
//
// Contract: a negative amount is ErrInvalidAmount; an unknown kind is
// ErrUnknownKind wrapped with the kind; a strategy error is returned wrapped
// with the kind (errors.Is still works); a negative fee from a strategy is
// ErrInvalidFee. The strategy runs WITHOUT the engine's lock held, so a
// strategy may call back into the engine (for example "international wire" =
// "domestic wire" + surcharge) even while another goroutine registers.
func (e *Engine) Fee(t Txn) (Money, error) {
	if t.Amount < 0 {
		return 0, fmt.Errorf("%w: %d", ErrInvalidAmount, t.Amount)
	}
	e.mu.RLock()
	f, ok := e.fees[t.Kind]
	e.mu.RUnlock()
	if !ok {
		return 0, fmt.Errorf("%w %q", ErrUnknownKind, t.Kind)
	}
	fee, err := f(t)
	if err != nil {
		return 0, fmt.Errorf("fee %q: %w", t.Kind, err)
	}
	if fee < 0 {
		return 0, fmt.Errorf("fee %q: %w (%d)", t.Kind, ErrInvalidFee, fee)
	}
	return fee, nil
}

// Kinds returns the registered kinds in sorted order.
func (e *Engine) Kinds() []string {
	e.mu.RLock()
	defer e.mu.RUnlock()
	out := make([]string, 0, len(e.fees))
	for k := range e.fees {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
