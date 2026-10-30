// Package adapter puts two foreign payment APIs behind one domain port.
//
// Lab B2 Unit 1 §5. The platform charges cards through vendors whose APIs
// disagree about everything: vendor A wants decimal strings ("12.50"),
// lower-case currency and ISO 8583-style response codes; vendor B wants integer
// minor units, upper-case currency and a state string, and reports failures
// with its own error type. The domain code must see none of that: it talks to
// Charger, in minor units, with a small error taxonomy. Each adapter is the
// only place that knows one vendor's shape, and the two are held to one shared
// behavioural contract.
package adapter

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// ---- The port: what the rest of the system sees -------------------------------

// ChargeRequest is the domain's view of a charge. Amounts are minor units.
type ChargeRequest struct {
	IdempotencyKey string // forwarded to the vendor as its reference
	AmountMinor    int64
	Currency       string // ISO 4217, upper case: "USD"
}

// Status is the outcome of a charge that reached the vendor and got an answer.
type Status string

// Charge outcomes. A decline is a normal business outcome, not an error.
const (
	Approved Status = "approved"
	Declined Status = "declined"
	Pending  Status = "pending"
)

// ChargeResult is the domain's view of the vendor's answer.
type ChargeResult struct {
	ID     string
	Status Status
}

// Charger is the port. Consumers depend on this, never on a vendor type.
type Charger interface {
	Charge(ctx context.Context, req ChargeRequest) (ChargeResult, error)
}

// The error taxonomy: what callers can branch on (retry? alert? give up?).
var (
	// ErrInvalidRequest: rejected locally; the vendor was not called.
	ErrInvalidRequest = errors.New("adapter: invalid request")
	// ErrRateLimited: the vendor throttled us; retry later with backoff.
	ErrRateLimited = errors.New("adapter: rate limited")
	// ErrUnavailable: transport failure, 5xx or "issuer unavailable"; retryable.
	ErrUnavailable = errors.New("adapter: vendor unavailable")
	// ErrRejected: the vendor said our request is malformed; retrying cannot help.
	ErrRejected = errors.New("adapter: request rejected by vendor")
	// ErrUnknownResponse: the vendor answered something we do not understand.
	// Fail closed: never guess that an unknown code means "approved".
	ErrUnknownResponse = errors.New("adapter: unknown vendor response")
)

// ---- Vendor A: decimal strings and response codes -----------------------------

// VendorAPayment is vendor A's wire format.
type VendorAPayment struct {
	Ref    string // our idempotency key
	Amount string // decimal string in major units: "12.50", "500" for yen
	Ccy    string // lower case: "usd"
}

// VendorAReply is vendor A's answer. Codes (ISO 8583 style): "00" approved,
// "05" do not honour, "51" insufficient funds, "09" in progress, "91" issuer
// unavailable, "96" system malfunction.
type VendorAReply struct {
	TxnID string
	Code  string
}

// VendorAError is what vendor A's client returns for a non-2xx HTTP response.
type VendorAError struct {
	HTTPStatus int
	Body       string
}

func (e *VendorAError) Error() string {
	return fmt.Sprintf("vendor A: HTTP %d: %s", e.HTTPStatus, e.Body)
}

// VendorAClient is vendor A's SDK surface.
type VendorAClient interface {
	CreatePayment(ctx context.Context, p VendorAPayment) (VendorAReply, error)
}

// ---- Vendor B: minor units and state strings ----------------------------------

// VendorBCharge is vendor B's wire format.
type VendorBCharge struct {
	IdempotencyKey string
	Amount         int64  // minor units
	Currency       string // upper case
}

// VendorBResult is vendor B's answer. State is "succeeded", "failed" or
// "requires_action".
type VendorBResult struct {
	ID    string
	State string
}

// VendorBError is vendor B's error type. Type is "rate_limit", "api_error"
// or "invalid_request".
type VendorBError struct {
	Type    string
	Message string
}

func (e *VendorBError) Error() string { return "vendor B: " + e.Type + ": " + e.Message }

// VendorBClient is vendor B's SDK surface.
type VendorBClient interface {
	Create(ctx context.Context, c VendorBCharge) (VendorBResult, error)
}

// ---- Adapters ----------------------------------------------------------------

// NewVendorA adapts vendor A's client to the Charger port.
//
// Contract (shared with NewVendorB, see the contract tests):
//   - invalid requests (empty key, amount <= 0, currency not three upper-case
//     letters) are ErrInvalidRequest and the vendor is not called;
//   - a context that is already done returns its error without calling the vendor;
//   - ctx is passed to the vendor; a ctx error comes back as that ctx error
//     (errors.Is(err, context.DeadlineExceeded)) and NOT as ErrUnavailable;
//   - vendor errors map to the taxonomy with BOTH the sentinel and the vendor
//     error reachable via errors.Is/As (log the cause, branch on the sentinel);
//   - unknown codes/states, or an approval without an id, are ErrUnknownResponse;
//   - on error the ChargeResult is the zero value.
func NewVendorA(c VendorAClient) Charger { return vendorA{c} }

type vendorA struct{ c VendorAClient }

func (a vendorA) Charge(ctx context.Context, req ChargeRequest) (ChargeResult, error) {
	if err := validate(ctx, req); err != nil {
		return ChargeResult{}, err
	}
	reply, err := a.c.CreatePayment(ctx, VendorAPayment{
		Ref:    req.IdempotencyKey,
		Amount: decimal(req.AmountMinor, req.Currency),
		Ccy:    strings.ToLower(req.Currency),
	})
	if err != nil {
		return ChargeResult{}, translate(ctx, "vendor A", err, classifyA)
	}
	var st Status
	switch reply.Code {
	case "00":
		st = Approved
	case "05", "51":
		st = Declined
	case "09":
		st = Pending
	case "91", "96":
		return ChargeResult{}, fmt.Errorf("vendor A: code %s: %w", reply.Code, ErrUnavailable)
	default:
		return ChargeResult{}, fmt.Errorf("vendor A: code %q: %w", reply.Code, ErrUnknownResponse)
	}
	return result(reply.TxnID, st)
}

// NewVendorB adapts vendor B's client to the Charger port. Same contract as NewVendorA.
func NewVendorB(c VendorBClient) Charger { return vendorB{c} }

type vendorB struct{ c VendorBClient }

func (b vendorB) Charge(ctx context.Context, req ChargeRequest) (ChargeResult, error) {
	if err := validate(ctx, req); err != nil {
		return ChargeResult{}, err
	}
	res, err := b.c.Create(ctx, VendorBCharge{
		IdempotencyKey: req.IdempotencyKey,
		Amount:         req.AmountMinor,
		Currency:       req.Currency,
	})
	if err != nil {
		return ChargeResult{}, translate(ctx, "vendor B", err, classifyB)
	}
	var st Status
	switch res.State {
	case "succeeded":
		st = Approved
	case "failed":
		st = Declined
	case "requires_action":
		st = Pending
	default:
		return ChargeResult{}, fmt.Errorf("vendor B: state %q: %w", res.State, ErrUnknownResponse)
	}
	return result(res.ID, st)
}

// ---- helpers -----------------------------------------------------------------

func result(id string, st Status) (ChargeResult, error) {
	if st == Approved && id == "" {
		return ChargeResult{}, fmt.Errorf("approval without a transaction id: %w", ErrUnknownResponse)
	}
	return ChargeResult{ID: id, Status: st}, nil
}

func validate(ctx context.Context, req ChargeRequest) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	switch {
	case req.IdempotencyKey == "":
		return fmt.Errorf("%w: empty idempotency key", ErrInvalidRequest)
	case req.AmountMinor <= 0:
		return fmt.Errorf("%w: amount %d", ErrInvalidRequest, req.AmountMinor)
	case len(req.Currency) != 3 || strings.Trim(req.Currency, "ABCDEFGHIJKLMNOPQRSTUVWXYZ") != "":
		return fmt.Errorf("%w: currency %q", ErrInvalidRequest, req.Currency)
	}
	return nil
}

// translate maps a vendor client error into the taxonomy, keeping the cause.
func translate(ctx context.Context, vendor string, err error, classify func(error) error) error {
	if ctx.Err() != nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("%s: %w", vendor, err)
	}
	return fmt.Errorf("%s: %w: %w", vendor, classify(err), err)
}

func classifyA(err error) error {
	var ae *VendorAError
	if !errors.As(err, &ae) {
		return ErrUnavailable // transport-level failure
	}
	switch {
	case ae.HTTPStatus == 429:
		return ErrRateLimited
	case ae.HTTPStatus >= 400 && ae.HTTPStatus < 500:
		return ErrRejected
	default:
		return ErrUnavailable
	}
}

func classifyB(err error) error {
	var be *VendorBError
	if !errors.As(err, &be) {
		return ErrUnavailable
	}
	switch be.Type {
	case "rate_limit":
		return ErrRateLimited
	case "invalid_request":
		return ErrRejected
	case "api_error":
		return ErrUnavailable
	default:
		return ErrUnknownResponse
	}
}

// exponent is the number of minor-unit digits (ISO 4217); the default is 2.
var exponent = map[string]int{"JPY": 0, "KRW": 0, "KWD": 3, "BHD": 3}

// decimal renders minor units as a major-unit decimal string: 1250 USD ->
// "12.50", 500 JPY -> "500", 1234 KWD -> "1.234". Integer math only.
func decimal(minor int64, ccy string) string {
	e, ok := exponent[ccy]
	if !ok {
		e = 2
	}
	if e == 0 {
		return strconv.FormatInt(minor, 10)
	}
	scale := int64(1)
	for i := 0; i < e; i++ {
		scale *= 10
	}
	return fmt.Sprintf("%d.%0*d", minor/scale, e, minor%scale)
}
