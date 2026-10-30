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

import "errors"

// Money is an amount in minor units (cents).
type Money int64

var errNotImplemented = errors.New("strategy: not implemented")

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
	// TODO(lab)
	_ = fee
	return func(Txn) (Money, error) { return 0, errNotImplemented }
}

// Percent charges bps basis points (1 bp = 0.01%) of the amount, rounded half
// up to the nearest minor unit, then clamped to [min, max]. max == 0 means no
// cap. It returns ErrOverflow instead of wrapping around for huge amounts.
//
// Hint: integer math only. Round half up with (amount*bps + 5000) / 10000,
// but check for overflow before multiplying.
func Percent(bps int64, min, max Money) FeeFunc {
	// TODO(lab)
	_, _, _ = bps, min, max
	return func(Txn) (Money, error) { return 0, errNotImplemented }
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
// in"), summing the exact per-band products and rounding half up ONCE at the
// end. Tiers must be non-empty, strictly ascending in UpTo, with rates >= 0;
// only the last tier may, and must, have UpTo == 0. Violations are
// ErrInvalidStrategy. The table is copied: the caller may reuse its slice.
func Tiered(tiers []Tier) (FeeFunc, error) {
	// TODO(lab): validate, copy, return the closure.
	_ = tiers
	return nil, errNotImplemented
}

// Sum adds the fees of several strategies (for example a base fee plus a
// surcharge). The first error aborts.
func Sum(fs ...FeeFunc) FeeFunc {
	// TODO(lab)
	_ = fs
	return func(Txn) (Money, error) { return 0, errNotImplemented }
}

// Engine looks up the strategy for a fee kind. It is safe for concurrent use,
// and strategies can be registered or replaced while it is serving calls.
type Engine struct {
	// TODO(lab): a lock and a map from kind to FeeFunc.
}

// NewEngine returns an empty engine.
func NewEngine() *Engine {
	// TODO(lab)
	return &Engine{}
}

// Register installs f for kind, replacing any previous strategy (fee
// schedules change at runtime). It rejects an empty kind or a nil f with
// ErrInvalidStrategy.
func (e *Engine) Register(kind string, f FeeFunc) error {
	// TODO(lab)
	_, _ = kind, f
	return errNotImplemented
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
	// TODO(lab)
	_ = t
	return 0, errNotImplemented
}

// Kinds returns the registered kinds in sorted order.
func (e *Engine) Kinds() []string {
	// TODO(lab)
	return nil
}
