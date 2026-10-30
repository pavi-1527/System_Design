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
package ledger

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
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

// Side of an entry.
type Side int

// Sides. The zero value is deliberately invalid.
const (
	Debit Side = iota + 1
	Credit
)

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

// Amount of a posting: the sum of its debits (which equals its credits).
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

// Clean reports whether the reconciliation found nothing to chase.
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

type account struct {
	id            string
	currency      string
	allowNegative bool
	balance       int64 // credits − debits, maintained on every posting
}

type keyRecord struct {
	fingerprint string
	postingID   string
}

type holdRecord struct {
	Hold
	captureKey string
}

// Ledger is the double-entry ledger. The zero value is not usable; call New.
type Ledger struct {
	mu       sync.Mutex
	now      func() time.Time
	accounts map[string]*account
	journal  []Posting
	byID     map[string]int // posting ID → index in journal
	keys     map[string]keyRecord
	refs     map[string]string // ExternalRef → posting ID
	holds    map[string]*holdRecord
	holdKeys map[string]string // hold key → hold ID
	reversed map[string]string // posting ID → reversing posting ID
	seq      uint64
	holdSeq  uint64
}

// New returns an empty ledger.
func New(opts ...Option) *Ledger {
	l := &Ledger{
		now:      time.Now,
		accounts: map[string]*account{},
		byID:     map[string]int{},
		keys:     map[string]keyRecord{},
		refs:     map[string]string{},
		holds:    map[string]*holdRecord{},
		holdKeys: map[string]string{},
		reversed: map[string]string{},
	}
	for _, o := range opts {
		o(l)
	}
	return l
}

// OpenAccount creates an account with a zero balance.
func (l *Ledger) OpenAccount(id, currency string, allowNegative bool) error {
	if id == "" || currency == "" {
		return fmt.Errorf("%w: account id and currency are required", ErrInvalid)
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if _, ok := l.accounts[id]; ok {
		return fmt.Errorf("%w: %s", ErrAccountExists, id)
	}
	l.accounts[id] = &account{id: id, currency: currency, allowNegative: allowNegative}
	return nil
}

// Balance returns credits − debits for the account.
func (l *Ledger) Balance(accountID string) (int64, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	a, ok := l.accounts[accountID]
	if !ok {
		return 0, fmt.Errorf("%w: %s", ErrUnknownAccount, accountID)
	}
	return a.balance, nil
}

// Available returns Balance minus active, unexpired holds.
func (l *Ledger) Available(accountID string) (int64, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	a, ok := l.accounts[accountID]
	if !ok {
		return 0, fmt.Errorf("%w: %s", ErrUnknownAccount, accountID)
	}
	return a.balance - l.heldLocked(accountID, l.now(), ""), nil
}

// heldLocked sums active, unexpired holds on an account, excluding one hold.
func (l *Ledger) heldLocked(accountID string, now time.Time, except string) int64 {
	var sum int64
	for id, h := range l.holds {
		if h.Account == accountID && h.State == HoldActive && id != except && now.Before(h.ExpiresAt) {
			sum += h.Amount
		}
	}
	return sum
}

func fingerprint(entries []Entry, externalRef string) string {
	var b strings.Builder
	for _, e := range entries {
		fmt.Fprintf(&b, "%s|%d|%d;", e.Account, e.Side, e.Amount)
	}
	b.WriteString("#")
	b.WriteString(externalRef)
	return b.String()
}

func cloneEntries(in []Entry) []Entry {
	return append([]Entry(nil), in...)
}

func clonePosting(p Posting) Posting {
	p.Entries = cloneEntries(p.Entries)
	return p
}

// Post records a balanced, idempotent posting. replayed is true when the key
// had already been posted with the same request.
func (l *Ledger) Post(req Request) (Posting, bool, error) {
	if req.Key == "" {
		return Posting{}, false, fmt.Errorf("%w: idempotency key is required", ErrInvalid)
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	fp := fingerprint(req.Entries, req.ExternalRef)
	if rec, ok := l.keys[req.Key]; ok {
		if rec.fingerprint != fp {
			return Posting{}, false, fmt.Errorf("%w: key %q", ErrKeyReused, req.Key)
		}
		return clonePosting(l.journal[l.byID[rec.postingID]]), true, nil
	}
	p, err := l.postLocked(req.Key, req.Entries, req.ExternalRef, "", "")
	if err != nil {
		return Posting{}, false, err
	}
	return clonePosting(p), false, nil
}

// validateLocked checks a set of entries without applying them.
// exceptHold, when set, is a hold being captured: its reservation is released
// as part of the same posting, so it does not count against the debit.
func (l *Ledger) validateLocked(entries []Entry, externalRef string, now time.Time, exceptHold string) error {
	if len(entries) < 2 {
		return fmt.Errorf("%w: a posting needs at least two entries", ErrInvalid)
	}
	if externalRef != "" {
		if _, dup := l.refs[externalRef]; dup {
			return fmt.Errorf("%w: external ref %q already posted", ErrInvalid, externalRef)
		}
	}
	var debits, credits int64
	currency := ""
	net := map[string]int64{} // per-account change in balance
	for _, e := range entries {
		if e.Amount <= 0 {
			return fmt.Errorf("%w: amount must be positive (got %d)", ErrInvalid, e.Amount)
		}
		a, ok := l.accounts[e.Account]
		if !ok {
			return fmt.Errorf("%w: %s", ErrUnknownAccount, e.Account)
		}
		if currency == "" {
			currency = a.currency
		} else if a.currency != currency {
			return fmt.Errorf("%w: %s is %s, posting is %s", ErrCurrencyMismatch, e.Account, a.currency, currency)
		}
		switch e.Side {
		case Debit:
			debits += e.Amount
			net[e.Account] -= e.Amount
		case Credit:
			credits += e.Amount
			net[e.Account] += e.Amount
		default:
			return fmt.Errorf("%w: entry side must be Debit or Credit", ErrInvalid)
		}
	}
	if debits != credits {
		return fmt.Errorf("%w: debits %d, credits %d", ErrUnbalanced, debits, credits)
	}
	for id, delta := range net {
		a := l.accounts[id]
		if a.allowNegative || delta >= 0 {
			continue
		}
		available := a.balance - l.heldLocked(id, now, exceptHold)
		if available+delta < 0 {
			return fmt.Errorf("%w: %s has %d available, needs %d", ErrInsufficientFunds, id, available, -delta)
		}
	}
	return nil
}

// postLocked validates and applies entries. Caller holds l.mu.
func (l *Ledger) postLocked(key string, entries []Entry, externalRef, reverses, exceptHold string) (Posting, error) {
	now := l.now()
	if err := l.validateLocked(entries, externalRef, now, exceptHold); err != nil {
		return Posting{}, err
	}
	l.seq++
	p := Posting{
		ID:          fmt.Sprintf("tx_%06d", l.seq),
		Key:         key,
		Seq:         l.seq,
		At:          now,
		Entries:     cloneEntries(entries),
		ExternalRef: externalRef,
		Reverses:    reverses,
	}
	for _, e := range entries {
		a := l.accounts[e.Account]
		if e.Side == Debit {
			a.balance -= e.Amount
		} else {
			a.balance += e.Amount
		}
	}
	l.journal = append(l.journal, p)
	l.byID[p.ID] = len(l.journal) - 1
	l.keys[key] = keyRecord{fingerprint: fingerprint(entries, externalRef), postingID: p.ID}
	if externalRef != "" {
		l.refs[externalRef] = p.ID
	}
	return p, nil
}

// Hold reserves amount on an account until now+ttl. Idempotent per key.
func (l *Ledger) Hold(key, accountID string, amount int64, ttl time.Duration) (Hold, error) {
	if key == "" || amount <= 0 || ttl <= 0 {
		return Hold{}, fmt.Errorf("%w: hold needs a key, a positive amount and a positive ttl", ErrInvalid)
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if id, ok := l.holdKeys[key]; ok {
		h := l.holds[id]
		if h.Account != accountID || h.Amount != amount {
			return Hold{}, fmt.Errorf("%w: hold key %q", ErrKeyReused, key)
		}
		return h.Hold, nil
	}
	a, ok := l.accounts[accountID]
	if !ok {
		return Hold{}, fmt.Errorf("%w: %s", ErrUnknownAccount, accountID)
	}
	now := l.now()
	if !a.allowNegative {
		if available := a.balance - l.heldLocked(accountID, now, ""); available < amount {
			return Hold{}, fmt.Errorf("%w: %s has %d available, hold needs %d", ErrInsufficientFunds, accountID, available, amount)
		}
	}
	l.holdSeq++
	h := &holdRecord{Hold: Hold{
		ID: fmt.Sprintf("hold_%06d", l.holdSeq), Key: key, Account: accountID, Amount: amount,
		ExpiresAt: now.Add(ttl), State: HoldActive,
	}}
	l.holds[h.ID] = h
	l.holdKeys[key] = h.ID
	return h.Hold, nil
}

// GetHold returns a snapshot of a hold.
func (l *Ledger) GetHold(holdID string) (Hold, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	h, ok := l.holds[holdID]
	if !ok {
		return Hold{}, fmt.Errorf("%w: %s", ErrUnknownHold, holdID)
	}
	return h.Hold, nil
}

// Capture moves up to the held amount from the hold's account to another
// account and closes the hold. Idempotent per key.
func (l *Ledger) Capture(key, holdID string, amount int64, to string) (Posting, error) {
	if key == "" || amount <= 0 {
		return Posting{}, fmt.Errorf("%w: capture needs a key and a positive amount", ErrInvalid)
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	h, ok := l.holds[holdID]
	if !ok {
		return Posting{}, fmt.Errorf("%w: %s", ErrUnknownHold, holdID)
	}
	if h.State == HoldCaptured && h.captureKey == key {
		return clonePosting(l.journal[l.byID[h.PostingID]]), nil
	}
	if rec, ok := l.keys[key]; ok {
		_ = rec
		return Posting{}, fmt.Errorf("%w: key %q", ErrKeyReused, key)
	}
	if h.State != HoldActive {
		return Posting{}, fmt.Errorf("%w: %s", ErrHoldClosed, holdID)
	}
	if !l.now().Before(h.ExpiresAt) {
		return Posting{}, fmt.Errorf("%w: %s", ErrHoldExpired, holdID)
	}
	if amount > h.Amount {
		return Posting{}, fmt.Errorf("%w: capture %d exceeds held %d", ErrInvalid, amount, h.Amount)
	}
	entries := []Entry{{Account: h.Account, Side: Debit, Amount: amount}, {Account: to, Side: Credit, Amount: amount}}
	p, err := l.postLocked(key, entries, "", "", holdID)
	if err != nil {
		return Posting{}, err
	}
	h.State = HoldCaptured
	h.PostingID = p.ID
	h.captureKey = key
	return clonePosting(p), nil
}

// Void closes an active hold without moving money.
func (l *Ledger) Void(holdID string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	h, ok := l.holds[holdID]
	if !ok {
		return fmt.Errorf("%w: %s", ErrUnknownHold, holdID)
	}
	if h.State != HoldActive {
		return fmt.Errorf("%w: %s", ErrHoldClosed, holdID)
	}
	h.State = HoldVoided
	return nil
}

// Reverse posts the mirror image of a posting. Idempotent per key.
func (l *Ledger) Reverse(key, postingID string) (Posting, error) {
	if key == "" {
		return Posting{}, fmt.Errorf("%w: idempotency key is required", ErrInvalid)
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	idx, ok := l.byID[postingID]
	if !ok {
		return Posting{}, fmt.Errorf("%w: %s", ErrUnknownPosting, postingID)
	}
	orig := l.journal[idx]
	mirror := make([]Entry, len(orig.Entries))
	for i, e := range orig.Entries {
		side := Credit
		if e.Side == Credit {
			side = Debit
		}
		mirror[i] = Entry{Account: e.Account, Side: side, Amount: e.Amount}
	}
	if rec, ok := l.keys[key]; ok {
		if rec.fingerprint != fingerprint(mirror, "") || l.journal[l.byID[rec.postingID]].Reverses != postingID {
			return Posting{}, fmt.Errorf("%w: key %q", ErrKeyReused, key)
		}
		return clonePosting(l.journal[l.byID[rec.postingID]]), nil
	}
	if by, done := l.reversed[postingID]; done {
		return Posting{}, fmt.Errorf("%w: %s reversed by %s", ErrAlreadyReversed, postingID, by)
	}
	p, err := l.postLocked(key, mirror, "", postingID, "")
	if err != nil {
		return Posting{}, err
	}
	l.reversed[postingID] = p.ID
	return clonePosting(p), nil
}

// Get returns a posting by ID.
func (l *Ledger) Get(postingID string) (Posting, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	idx, ok := l.byID[postingID]
	if !ok {
		return Posting{}, fmt.Errorf("%w: %s", ErrUnknownPosting, postingID)
	}
	return clonePosting(l.journal[idx]), nil
}

// History returns the postings that touch an account, oldest first.
func (l *Ledger) History(accountID string) ([]Posting, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if _, ok := l.accounts[accountID]; !ok {
		return nil, fmt.Errorf("%w: %s", ErrUnknownAccount, accountID)
	}
	var out []Posting
	for _, p := range l.journal {
		for _, e := range p.Entries {
			if e.Account == accountID {
				out = append(out, clonePosting(p))
				break
			}
		}
	}
	return out, nil
}

// Audit recomputes balances from the journal and checks the invariants.
func (l *Ledger) Audit() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	recomputed := map[string]int64{}
	var prevSeq uint64
	for _, p := range l.journal {
		if p.Seq <= prevSeq {
			return fmt.Errorf("ledger audit: posting %s has seq %d after %d", p.ID, p.Seq, prevSeq)
		}
		prevSeq = p.Seq
		var d, c int64
		for _, e := range p.Entries {
			if _, ok := l.accounts[e.Account]; !ok {
				return fmt.Errorf("ledger audit: posting %s references unknown account %s", p.ID, e.Account)
			}
			if e.Side == Debit {
				d += e.Amount
				recomputed[e.Account] -= e.Amount
			} else {
				c += e.Amount
				recomputed[e.Account] += e.Amount
			}
		}
		if d != c {
			return fmt.Errorf("ledger audit: posting %s is unbalanced (debits %d, credits %d)", p.ID, d, c)
		}
	}
	var total int64
	ids := make([]string, 0, len(l.accounts))
	for id := range l.accounts {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		a := l.accounts[id]
		if a.balance != recomputed[id] {
			return fmt.Errorf("ledger audit: account %s balance %d but journal sums to %d", id, a.balance, recomputed[id])
		}
		total += a.balance
	}
	if total != 0 {
		return fmt.Errorf("ledger audit: balances sum to %d, want 0", total)
	}
	return nil
}

// Reconcile compares a processor statement with the journal.
func (l *Ledger) Reconcile(lines []StatementLine) Report {
	l.mu.Lock()
	defer l.mu.Unlock()
	var r Report
	seen := map[string]int{}
	for _, ln := range lines {
		seen[ln.Ref]++
	}
	matchedRef := map[string]bool{}
	for _, ln := range lines {
		if seen[ln.Ref] > 1 {
			if !matchedRef[ln.Ref] {
				r.DuplicateRefs = append(r.DuplicateRefs, ln.Ref)
				matchedRef[ln.Ref] = true
			}
			continue
		}
		pid, ok := l.refs[ln.Ref]
		if !ok {
			r.MissingInLedger = append(r.MissingInLedger, ln)
			continue
		}
		p := l.journal[l.byID[pid]]
		cur := l.accounts[p.Entries[0].Account].currency
		if amt := p.Amount(); amt != ln.Amount || cur != ln.Currency {
			r.Mismatched = append(r.Mismatched, Mismatch{Ref: ln.Ref, LedgerAmount: amt, StatementAmount: ln.Amount})
		} else {
			r.Matched = append(r.Matched, ln.Ref)
		}
		matchedRef[ln.Ref] = true
	}
	for ref, pid := range l.refs {
		if !matchedRef[ref] {
			r.MissingOnStatement = append(r.MissingOnStatement, pid)
		}
	}
	sort.Strings(r.Matched)
	sort.Strings(r.MissingOnStatement)
	sort.Strings(r.DuplicateRefs)
	sort.Slice(r.MissingInLedger, func(i, j int) bool { return r.MissingInLedger[i].Ref < r.MissingInLedger[j].Ref })
	sort.Slice(r.Mismatched, func(i, j int) bool { return r.Mismatched[i].Ref < r.Mismatched[j].Ref })
	return r
}
