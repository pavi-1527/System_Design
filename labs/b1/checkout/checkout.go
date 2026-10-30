// Package checkout is the target design for the legacy OrderManager (see
// legacy.go): one small Service whose collaborators are interfaces declared
// here, at the consumer.
//
// Lab B1 Unit 1. Your job is to implement everything marked TODO(lab) so that
// checkout_test.go passes under -race. legacy.go is given and must stay as it
// is: the tests run it side by side with your code as a parity oracle.
//
// Suggested order: New + options, then Place's validation, pricing, the
// reserve/charge/rollback path, then context handling, then notification.
// Run one test at a time while you work:
//
//	go test -race -run 'TestNew' ./labs/b1/checkout/
package checkout

import (
	"context"
	"errors"
	"time"
)

var errNotImplemented = errors.New("checkout: not implemented")

// Money is an amount in minor units (cents, paise). Integers only: no floats
// near money.
type Money int64

// Sentinel errors. Place wraps them with %w and context, so callers test with
// errors.Is, never with == or string matching. (GIVEN)
var (
	// ErrInvalidConfig is returned by New for missing dependencies or bad options.
	ErrInvalidConfig = errors.New("checkout: invalid configuration")
	// ErrInvalidOrder is returned by Place for a request that can never succeed.
	ErrInvalidOrder = errors.New("checkout: invalid order")
	// ErrOutOfStock is what a Stock implementation returns when it cannot reserve.
	ErrOutOfStock = errors.New("checkout: out of stock")
	// ErrPaymentDeclined is what a Payer implementation returns when the charge is refused.
	ErrPaymentDeclined = errors.New("checkout: payment declined")
)

// Line is one order line. (GIVEN)
type Line struct {
	SKU       string
	Qty       int
	UnitPrice Money
}

// Request is what a caller asks Place to do. ID is chosen by the caller and is
// also the idempotency key sent to the payment provider. (GIVEN)
type Request struct {
	ID        string
	Customer  string
	Region    string
	Coupon    string
	CardToken string
	Lines     []Line
}

// Order is the result of a successful Place. It is a value: Lines is a copy. (GIVEN)
type Order struct {
	ID       string
	Customer string
	Region   string
	Lines    []Line
	Subtotal Money
	Discount Money
	Tax      Money
	Total    Money
	Receipt  string
	PlacedAt time.Time
	// Notified is false when no Notifier is configured or the notification failed.
	// A failed notification never fails an order that has already been paid.
	Notified bool
}

// The three collaborators plus the clock. They are defined HERE, in the package
// that uses them, each with only the methods Place calls. Adapters (a bank
// gateway, a SQL inventory, an SMTP relay) live elsewhere and satisfy them
// implicitly. (GIVEN)

// Payer moves money. key is an idempotency key: charging twice with the same key
// must not move money twice (that is the provider's job; Place only has to send
// a stable key).
type Payer interface {
	Charge(ctx context.Context, key string, amount Money, cardToken string) (receipt string, err error)
}

// Stock reserves and releases units.
type Stock interface {
	Reserve(sku string, qty int) error
	Release(sku string, qty int)
}

// Notifier tells the customer. Optional.
type Notifier interface {
	OrderPlaced(ctx context.Context, o Order) error
}

// Clock supplies the current time.
type Clock interface {
	Now() time.Time
}

// ClockFunc adapts a function to Clock (the http.HandlerFunc trick). (GIVEN)
type ClockFunc func() time.Time

// Now implements Clock.
func (f ClockFunc) Now() time.Time { return f() }

// DefaultTaxRates returns a fresh copy of the built-in tax table (basis points
// per region): US-CA 725, US-OR 0, EU-DE 1900, IN-KA 1800 (the legacy table).
// It must be a copy, so no caller can corrupt the defaults.
func DefaultTaxRates() map[string]int {
	// TODO(lab)
	return map[string]int{}
}

// options is the validated configuration New builds. TODO(lab): add the fields
// you need (notifier, clock, tax table, max lines).
type options struct{}

// Option configures New. Options are applied in order; an option that cannot be
// honoured makes New fail with an error wrapping ErrInvalidConfig.
type Option func(*options) error

// WithNotifier sets the Notifier. A nil notifier is a configuration error:
// leave the option out to run without notifications.
func WithNotifier(n Notifier) Option {
	// TODO(lab)
	_ = n
	return func(*options) error { return errNotImplemented }
}

// WithClock sets the Clock. nil is a configuration error.
func WithClock(c Clock) Option {
	// TODO(lab)
	_ = c
	return func(*options) error { return errNotImplemented }
}

// WithTaxRates replaces the tax table. The map must be COPIED: later changes
// by the caller must not affect the Service. Rates are basis points in
// [0, 10000]; an empty table, an empty region or an out-of-range rate is an error.
func WithTaxRates(rates map[string]int) Option {
	// TODO(lab)
	_ = rates
	return func(*options) error { return errNotImplemented }
}

// WithMaxLines sets the maximum number of lines per order (default 20, minimum 1).
func WithMaxLines(n int) Option {
	// TODO(lab)
	_ = n
	return func(*options) error { return errNotImplemented }
}

// Service places orders. Build it with New; it is immutable afterwards and safe
// for concurrent use. It must not be copied after first use.
// TODO(lab): add the fields you need (payer, stock, options, a counter).
type Service struct{}

// New validates its inputs and returns a ready Service. Payer and Stock are
// required (nil is an error). Defaults: system clock, DefaultTaxRates, 20
// lines, no notifier. Every failure wraps ErrInvalidConfig.
func New(p Payer, s Stock, opts ...Option) (*Service, error) {
	// TODO(lab)
	_, _, _ = p, s, opts
	return nil, errNotImplemented
}

// Placed reports how many orders this Service has completed (paid). It must be
// safe to call while other goroutines are inside Place.
func (s *Service) Placed() int {
	// TODO(lab)
	return 0
}

// Place validates, prices, reserves stock, charges and notifies, in that order.
//
// Contract (all pinned by tests):
//   - Validation failures wrap ErrInvalidOrder and happen before any side
//     effect: empty ID/customer/card token, no lines, more than the maximum
//     lines, unknown region, empty SKU, Qty <= 0, negative UnitPrice.
//   - Pricing is identical to legacy.go: subtotal; discount = the best single
//     one of volume 5% (subtotal >= 50000), SAVE10 10%, SAVE20 20% (only from
//     20000; coupons are case-insensitive, unknown coupons ignored); discount
//     and tax round half up on integer minor units; tax = taxable * region bps.
//   - Reserve every line in order. If a reservation or the charge fails,
//     release everything reserved and return an error that wraps the cause
//     (errors.Is and errors.As must reach what the adapter returned) and names
//     the order, and for stock failures the SKU.
//   - ctx is checked before reserving and again before charging; if it is done,
//     release everything and return an error wrapping ctx.Err(). Once the charge
//     has succeeded, return the order even if ctx was cancelled meanwhile
//     (money has moved; abandoning it would be worse).
//   - Charge(ctx, req.ID, total, req.CardToken): the order ID is the
//     idempotency key.
//   - The Notifier runs after the charge, with a context that is NOT cancelled
//     when ctx is (context.WithoutCancel). Its failure sets Notified=false and
//     does not fail the order. Give it its own copy of the Lines.
//   - Request.Lines is never retained or modified; Order.Lines is a copy.
//   - Placed() counts only paid orders.
func (s *Service) Place(ctx context.Context, req Request) (Order, error) {
	// TODO(lab)
	_, _ = ctx, req
	return Order{}, errNotImplemented
}
