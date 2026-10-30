// Package checkout is the target design for the legacy OrderManager (see
// legacy.go): one small Service whose collaborators are interfaces declared
// here, at the consumer.
//
// Lab B1 Unit 1. Your job is to implement everything marked TODO(lab) so that
// checkout_test.go passes under -race. legacy.go is given and must stay as it
// is: the tests run it side by side with your code as a parity oracle.
package checkout

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync/atomic"
	"time"
)

// Money is an amount in minor units (cents, paise). Integers only: no floats
// near money.
type Money int64

// Sentinel errors. Place wraps them with %w and context, so callers test with
// errors.Is, never with == or string matching.
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

// Line is one order line.
type Line struct {
	SKU       string
	Qty       int
	UnitPrice Money
}

// Request is what a caller asks Place to do. ID is chosen by the caller and is
// also the idempotency key sent to the payment provider.
type Request struct {
	ID        string
	Customer  string
	Region    string
	Coupon    string
	CardToken string
	Lines     []Line
}

// Order is the result of a successful Place. It is a value: Lines is a copy.
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

// The three collaborators. They are defined HERE, in the package that uses them,
// each with only the methods Place calls. Adapters (a bank gateway, a SQL
// inventory, an SMTP relay) live elsewhere and satisfy them implicitly.

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

// ClockFunc adapts a function to Clock (the http.HandlerFunc trick).
type ClockFunc func() time.Time

// Now implements Clock.
func (f ClockFunc) Now() time.Time { return f() }

type systemClock struct{}

func (systemClock) Now() time.Time { return time.Now() }

const defaultMaxLines = 20

// DefaultTaxRates returns a fresh copy of the built-in tax table (basis points
// per region). A copy, so no caller can corrupt the defaults.
func DefaultTaxRates() map[string]int {
	return map[string]int{"US-CA": 725, "US-OR": 0, "EU-DE": 1900, "IN-KA": 1800}
}

type options struct {
	notifier Notifier
	clock    Clock
	taxRates map[string]int
	maxLines int
}

// Option configures New. Options are applied in order; an option that cannot be
// honoured makes New fail with ErrInvalidConfig.
type Option func(*options) error

// WithNotifier sets the Notifier. A nil notifier is a configuration error:
// leave the option out to run without notifications.
func WithNotifier(n Notifier) Option {
	return func(o *options) error {
		if n == nil {
			return errors.New("WithNotifier(nil)")
		}
		o.notifier = n
		return nil
	}
}

// WithClock sets the Clock.
func WithClock(c Clock) Option {
	return func(o *options) error {
		if c == nil {
			return errors.New("WithClock(nil)")
		}
		o.clock = c
		return nil
	}
}

// WithTaxRates replaces the tax table. The map is copied: later changes by the
// caller must not affect the Service. Rates are basis points in [0, 10000].
func WithTaxRates(rates map[string]int) Option {
	return func(o *options) error {
		if len(rates) == 0 {
			return errors.New("WithTaxRates: empty table")
		}
		cp := make(map[string]int, len(rates))
		for region, bps := range rates {
			if region == "" || bps < 0 || bps > 10000 {
				return fmt.Errorf("WithTaxRates: bad entry %q=%d", region, bps)
			}
			cp[region] = bps
		}
		o.taxRates = cp
		return nil
	}
}

// WithMaxLines sets the maximum number of lines per order (default 20).
func WithMaxLines(n int) Option {
	return func(o *options) error {
		if n < 1 {
			return fmt.Errorf("WithMaxLines(%d): must be >= 1", n)
		}
		o.maxLines = n
		return nil
	}
}

// Service places orders. Build it with New; it is immutable afterwards and safe
// for concurrent use. It must not be copied after first use (it holds an atomic).
type Service struct {
	payer  Payer
	stock  Stock
	opts   options
	placed atomic.Int64
}

// New validates its inputs and returns a ready Service. Payer and Stock are
// required. Defaults: system clock, DefaultTaxRates, 20 lines, no notifier.
// Every failure wraps ErrInvalidConfig.
func New(p Payer, s Stock, opts ...Option) (*Service, error) {
	if p == nil {
		return nil, fmt.Errorf("%w: payer is required", ErrInvalidConfig)
	}
	if s == nil {
		return nil, fmt.Errorf("%w: stock is required", ErrInvalidConfig)
	}
	o := options{clock: systemClock{}, taxRates: DefaultTaxRates(), maxLines: defaultMaxLines}
	for _, apply := range opts {
		if apply == nil {
			return nil, fmt.Errorf("%w: nil option", ErrInvalidConfig)
		}
		if err := apply(&o); err != nil {
			return nil, fmt.Errorf("%w: %v", ErrInvalidConfig, err)
		}
	}
	return &Service{payer: p, stock: s, opts: o}, nil
}

// Placed reports how many orders this Service has completed (paid).
func (s *Service) Placed() int { return int(s.placed.Load()) }

// Place validates, prices, reserves stock, charges and notifies, in that order.
//
// Contract (all pinned by tests):
//   - Validation failures wrap ErrInvalidOrder and happen before any side effect.
//   - Pricing is identical to the legacy OrderManager.
//   - If any reservation or the charge fails, everything reserved is released
//     and the error wraps the cause (errors.Is / errors.As reach it).
//   - ctx is checked before reserving and again before charging; if it is done,
//     everything is released and the error wraps ctx.Err(). Once the charge has
//     succeeded the order is returned even if ctx is cancelled meanwhile
//     (money has moved; abandoning it would be worse).
//   - The Notifier runs after the charge, with a context that is not cancelled
//     when ctx is. Its failure sets Notified=false and does not fail the order.
//   - Request.Lines is never retained or modified; Order.Lines is a copy.
func (s *Service) Place(ctx context.Context, req Request) (Order, error) {
	if err := ctx.Err(); err != nil {
		return Order{}, fmt.Errorf("checkout: place %q: %w", req.ID, err)
	}
	if err := s.validate(req); err != nil {
		return Order{}, err
	}
	lines := append([]Line(nil), req.Lines...)
	var subtotal Money
	for _, l := range lines {
		subtotal += Money(l.Qty) * l.UnitPrice
	}
	discount := discountFor(subtotal, req.Coupon)
	taxable := subtotal - discount
	tax := Money((int64(taxable)*int64(s.opts.taxRates[req.Region]) + 5000) / 10000)
	total := taxable + tax

	reserved := make([]Line, 0, len(lines))
	release := func() {
		for i := len(reserved) - 1; i >= 0; i-- {
			s.stock.Release(reserved[i].SKU, reserved[i].Qty)
		}
	}
	for _, l := range lines {
		if err := s.stock.Reserve(l.SKU, l.Qty); err != nil {
			release()
			return Order{}, fmt.Errorf("checkout: reserve %d x %q for order %q: %w", l.Qty, l.SKU, req.ID, err)
		}
		reserved = append(reserved, l)
	}
	if err := ctx.Err(); err != nil {
		release()
		return Order{}, fmt.Errorf("checkout: place %q: %w", req.ID, err)
	}
	receipt, err := s.payer.Charge(ctx, req.ID, total, req.CardToken)
	if err != nil {
		release()
		return Order{}, fmt.Errorf("checkout: charge order %q: %w", req.ID, err)
	}
	o := Order{
		ID: req.ID, Customer: req.Customer, Region: req.Region, Lines: lines,
		Subtotal: subtotal, Discount: discount, Tax: tax, Total: total,
		Receipt: receipt, PlacedAt: s.opts.clock.Now(),
	}
	s.placed.Add(1)
	if s.opts.notifier != nil {
		snapshot := o
		snapshot.Lines = append([]Line(nil), lines...)
		if err := s.opts.notifier.OrderPlaced(context.WithoutCancel(ctx), snapshot); err == nil {
			o.Notified = true
		}
	}
	return o, nil
}

func (s *Service) validate(req Request) error {
	bad := func(format string, a ...any) error {
		return fmt.Errorf("%w: order %q: %s", ErrInvalidOrder, req.ID, fmt.Sprintf(format, a...))
	}
	switch {
	case req.ID == "":
		return bad("empty id")
	case req.Customer == "":
		return bad("empty customer")
	case req.CardToken == "":
		return bad("empty card token")
	case len(req.Lines) == 0:
		return bad("no lines")
	case len(req.Lines) > s.opts.maxLines:
		return bad("%d lines, max %d", len(req.Lines), s.opts.maxLines)
	}
	if _, ok := s.opts.taxRates[req.Region]; !ok {
		known := make([]string, 0, len(s.opts.taxRates))
		for r := range s.opts.taxRates {
			known = append(known, r)
		}
		sort.Strings(known)
		return bad("unknown region %q (known: %v)", req.Region, known)
	}
	for i, l := range req.Lines {
		switch {
		case l.SKU == "":
			return bad("line %d: empty sku", i)
		case l.Qty <= 0:
			return bad("line %d (%s): qty %d", i, l.SKU, l.Qty)
		case l.UnitPrice < 0:
			return bad("line %d (%s): negative price", i, l.SKU)
		}
	}
	return nil
}

// discountFor mirrors the legacy rules: 5% from 500.00, SAVE10 = 10%, SAVE20 =
// 20% from 200.00 (otherwise ignored), unknown coupons are ignored, the best
// single discount wins, rounding is half up.
func discountFor(subtotal Money, coupon string) Money {
	var bps int64
	if subtotal >= 50000 {
		bps = 500
	}
	switch strings.ToUpper(coupon) {
	case "SAVE10":
		if bps < 1000 {
			bps = 1000
		}
	case "SAVE20":
		if subtotal >= 20000 && bps < 2000 {
			bps = 2000
		}
	}
	return Money((int64(subtotal)*bps + 5000) / 10000)
}
