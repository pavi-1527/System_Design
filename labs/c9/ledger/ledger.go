// Package ledger is an in-memory double-entry ledger: the toy version of the
// hardest component in a payments system. Every movement of money is a
// Posting whose debits equal its credits, postings are idempotent per key,
// balances are derived from the journal and never edited directly, holds
// (authorisations) reserve funds without moving them, corrections are
// reversals rather than edits, Audit checks the invariants, and Reconcile
// compares the journal with a processor's statement.
//
// It is the lab for C9 Unit 2 §5–§7. Suggested order: accounts and Post
// (validation, balances, the no-negative rule), then idempotency (replay,
// key reuse, concurrent duplicates), then holds, reversals, Audit and
// Reconcile. Run: go test -race ./labs/c9/ledger/
//
// Conventions (the tests depend on them):
//   - Amounts are int64 minor units (cents). Never floats.
//   - Balance(account) = sum(credits) − sum(debits) over the account's
//     journal entries: "credit-normal", the way a bank sees a member's
//     deposit account (a liability). An account opened with allowNegative =
//     false may never have Available < 0; settlement, suspense and external
//     accounts are opened with allowNegative = true and absorb the other side.
//   - Available(account) = Balance − the sum of active, unexpired holds.
//   - Post is all-or-nothing: a request that fails validation (fewer than two
//     entries, a non-positive amount, an unknown account, a currency mismatch,
//     debits ≠ credits, or a debit that would take Available below zero on an
//     account that may not go negative) changes nothing.
//   - Post is idempotent per Request.Key: the same key with the same entries
//     (same accounts, sides, amounts, in order) and the same ExternalRef
//     returns the original posting and replayed = true; the same key with
//     anything different is ErrKeyReused. Of N concurrent Posts with one key,
//     exactly one executes.
//   - ExternalRef, when set, is unique across postings (ErrInvalid otherwise):
//     it is what Reconcile matches on.
//   - Hold reserves amount on an account until now+ttl without moving money.
//     It is idempotent per key. Capture(key, holdID, amount, to) posts
//     Debit(hold.Account, amount) / Credit(to, amount) for amount ≤ hold.Amount
//     and closes the hold, releasing any remainder; it is idempotent per key.
//     Void closes an active hold. A hold past ExpiresAt is ErrHoldExpired for
//     Capture, and no longer reduces Available. Capture or Void on a closed
//     hold is ErrHoldClosed.
//   - Reverse(key, postingID) posts the mirror image of a posting (every Debit
//     becomes a Credit and vice versa), linked through Reverses. A posting can
//     be reversed once (ErrAlreadyReversed). A reversal is an ordinary posting:
//     it obeys the no-negative rule like any other.
//   - The journal is append-only. Postings carry a strictly increasing Seq.
//     History(account) returns that account's postings in Seq order. Returned
//     values never share memory with the ledger.
//   - Audit recomputes every balance from the journal and checks that every
//     posting balances and that the sum of all balances is zero. It returns a
//     descriptive error on the first violation.
//   - Reconcile(lines) matches statement lines to postings by ExternalRef and
//     reports what matched, what the statement has that the ledger lacks, what
//     the ledger has (with an ExternalRef) that the statement lacks, amount
//     mismatches, and refs that appear more than once on the statement. The
//     amount of a posting is the sum of its debits.
//   - All exported methods are safe for concurrent use. Time comes from the
//     injected clock.
//
// Hints:
//   - One mutex around the ledger is the right first design: the atomic
//     reservation of an idempotency key and the balance check must happen
//     under the same lock as the write, or two concurrent Posts both pass.
//     (In production the same role is played by the database transaction
//     plus a UNIQUE (tenant, key) constraint.)
//   - Keep: accounts (balance maintained on every posting), the journal
//     slice, an index from posting ID to journal position, a map from key to
//     (fingerprint, posting ID), a map from ExternalRef to posting ID, the
//     holds, a map from hold key to hold ID, and the set of reversed postings.
//   - Validate everything before mutating anything (two phases), so a
//     rejected request leaves no trace and its key stays free.
//   - When capturing a hold, the hold's own reservation must not count
//     against the debit it is being captured for.
package ledger

import (
	"errors"
	"time"
)

// Errors are values: compare with errors.Is.
var (
	ErrInvalid           = errors.New("ledger: invalid request")
	ErrAccountExists     = errors.New("ledger: account already exists")
	ErrUnknownAccount    = errors.New("ledger: unknown account")
	ErrCurrencyMismatch  = errors.New("ledger: currency mismatch")
	ErrUnbalanced        = errors.New("ledger: debits do not equal credits")
	ErrInsufficientFunds = errors.New("ledger: insufficient available funds")
	ErrKeyReused         = errors.New("ledger: idempotency key reused with a different request")
	ErrUnknownHold       = errors.New("ledger: unknown hold")
	ErrHoldExpired       = errors.New("ledger: hold expired")
	ErrHoldClosed        = errors.New("ledger: hold already captured or voided")
	ErrUnknownPosting    = errors.New("ledger: unknown posting")
	ErrAlreadyReversed   = errors.New("ledger: posting already reversed")
)

var errNotImplemented = errors.New("ledger: not implemented")

// Side of an entry.
type Side int

// Sides. The zero value is deliberately invalid.
const (
	Debit Side = iota + 1
	Credit
)

// String is GIVEN.
func (s Side) String() string {
	switch s {
	case Debit:
		return "debit"
	case Credit:
		return "credit"
	}
	return "invalid"
}

// Entry is one leg of a posting.
type Entry struct {
	Account string
	Side    Side
	Amount  int64 // minor units, > 0
}

// Request is what a caller asks the ledger to post.
type Request struct {
	Key         string  // idempotency key, required
	Entries     []Entry // at least two, debits = credits
	ExternalRef string  // optional processor reference, unique when set
}

// Posting is one immutable journal record.
type Posting struct {
	ID          string
	Key         string
	Seq         uint64
	At          time.Time
	Entries     []Entry
	ExternalRef string
	Reverses    string // ID of the posting this one reverses, if any
}

// Amount of a posting: the sum of its debits (which equals its credits). GIVEN.
func (p Posting) Amount() int64 {
	var sum int64
	for _, e := range p.Entries {
		if e.Side == Debit {
			sum += e.Amount
		}
	}
	return sum
}

// HoldState is where a hold is in its life.
type HoldState int

// Hold states.
const (
	HoldActive HoldState = iota + 1
	HoldCaptured
	HoldVoided
)

// Hold is a reservation of funds on one account.
type Hold struct {
	ID        string
	Key       string
	Account   string
	Amount    int64
	ExpiresAt time.Time
	State     HoldState
	PostingID string // the capture posting, once captured
}

// StatementLine is one settlement line from an external processor.
type StatementLine struct {
	Ref      string
	Amount   int64
	Currency string
}

// Mismatch is a ref present on both sides with different amounts.
type Mismatch struct {
	Ref             string
	LedgerAmount    int64
	StatementAmount int64
}

// Report is the result of Reconcile. Every slice is sorted (by ref or by
// posting ID) so reports are deterministic.
type Report struct {
	Matched            []string        // refs present on both sides with equal amounts
	MissingInLedger    []StatementLine // on the statement, no posting with that ref
	MissingOnStatement []string        // posting IDs with an ExternalRef the statement lacks
	Mismatched         []Mismatch
	DuplicateRefs      []string // refs that appear more than once on the statement
}

// Clean reports whether the reconciliation found nothing to chase. GIVEN.
func (r Report) Clean() bool {
	return len(r.MissingInLedger) == 0 && len(r.MissingOnStatement) == 0 && len(r.Mismatched) == 0 && len(r.DuplicateRefs) == 0
}

// Option configures a Ledger.
type Option func(*Ledger)

// WithClock injects the clock. Default: time.Now.
func WithClock(now func() time.Time) Option {
	return func(l *Ledger) {
		if now != nil {
			l.now = now
		}
	}
}

// Ledger is the double-entry ledger. The zero value is not usable; call New.
type Ledger struct {
	now func() time.Time
	// TODO(lab): the mutex and the maps listed in the package hints.
}

// New returns an empty ledger.
func New(opts ...Option) *Ledger {
	l := &Ledger{now: time.Now}
	for _, o := range opts {
		o(l)
	}
	// TODO(lab): initialise your maps.
	return l
}

// OpenAccount creates an account with a zero balance.
func (l *Ledger) OpenAccount(id, currency string, allowNegative bool) error {
	// TODO(lab)
	_, _, _ = id, currency, allowNegative
	return errNotImplemented
}

// Balance returns credits − debits for the account.
func (l *Ledger) Balance(accountID string) (int64, error) {
	// TODO(lab)
	_ = accountID
	return 0, errNotImplemented
}

// Available returns Balance minus active, unexpired holds.
func (l *Ledger) Available(accountID string) (int64, error) {
	// TODO(lab)
	_ = accountID
	return 0, errNotImplemented
}

// Post records a balanced, idempotent posting. replayed is true when the key
// had already been posted with the same request.
func (l *Ledger) Post(req Request) (Posting, bool, error) {
	// TODO(lab): key check (replay / reuse), validate every entry, check
	// available funds, then apply: balances, journal, indexes.
	_ = req
	return Posting{}, false, errNotImplemented
}

// Hold reserves amount on an account until now+ttl. Idempotent per key.
func (l *Ledger) Hold(key, accountID string, amount int64, ttl time.Duration) (Hold, error) {
	// TODO(lab)
	_, _, _, _ = key, accountID, amount, ttl
	return Hold{}, errNotImplemented
}

// GetHold returns a snapshot of a hold.
func (l *Ledger) GetHold(holdID string) (Hold, error) {
	// TODO(lab)
	_ = holdID
	return Hold{}, errNotImplemented
}

// Capture moves up to the held amount from the hold's account to another
// account and closes the hold. Idempotent per key.
func (l *Ledger) Capture(key, holdID string, amount int64, to string) (Posting, error) {
	// TODO(lab)
	_, _, _, _ = key, holdID, amount, to
	return Posting{}, errNotImplemented
}

// Void closes an active hold without moving money.
func (l *Ledger) Void(holdID string) error {
	// TODO(lab)
	_ = holdID
	return errNotImplemented
}

// Reverse posts the mirror image of a posting. Idempotent per key.
func (l *Ledger) Reverse(key, postingID string) (Posting, error) {
	// TODO(lab)
	_, _ = key, postingID
	return Posting{}, errNotImplemented
}

// Get returns a posting by ID.
func (l *Ledger) Get(postingID string) (Posting, error) {
	// TODO(lab)
	_ = postingID
	return Posting{}, errNotImplemented
}

// History returns the postings that touch an account, oldest first.
func (l *Ledger) History(accountID string) ([]Posting, error) {
	// TODO(lab)
	_ = accountID
	return nil, errNotImplemented
}

// Audit recomputes balances from the journal and checks the invariants.
func (l *Ledger) Audit() error {
	// TODO(lab)
	return errNotImplemented
}

// Reconcile compares a processor statement with the journal.
func (l *Ledger) Reconcile(lines []StatementLine) Report {
	// TODO(lab)
	_ = lines
	return Report{}
}
