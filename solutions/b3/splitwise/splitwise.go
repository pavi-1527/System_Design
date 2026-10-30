// Package splitwise is an expense-sharing ledger ("who owes whom") with debt
// simplification.
//
// Lab B3 Problem 7 (75-minute box). Stage 1 is this file: money as integer
// minor units, users, equal splits, balances and the pairwise "who owes whom"
// view. Stage 2 (split strategies, groups, settlements, expense removal) lives
// in stage2.go and stage 3 (debt simplification, concurrency contract) in
// stage3.go.
//
// Reference design, in one paragraph: money is an int64 count of minor units
// (cents), never a float. A Ledger keeps, incrementally, a net balance per
// user and a net amount per user pair, so Balance is O(1) and no read ever
// replays history. Every write is validated first and applied second, under
// one mutex, so a rejected call changes nothing and a reader can never observe
// half of an expense. The invariant that makes it a ledger: the balances of all
// users always sum to exactly zero, because every expense moves money from
// debtors to the creditor and nothing is ever created or rounded away.
package splitwise

import (
	"cmp"
	"errors"
	"maps"
	"slices"
	"sync"
)

// UserID identifies a person in the ledger. It must be non-empty.
type UserID string

// Money is an amount in minor units (cents). 1234 means 12.34. It is an
// integer on purpose: float64 cannot represent 0.10 exactly, and a ledger that
// drifts by a fraction of a cent per operation is wrong after a million of them.
type Money int64

// ExpenseID identifies a recorded expense. IDs are unique and non-zero.
type ExpenseID int64

// MaxAmount is the largest amount (and share) the ledger accepts: 10^12 minor
// units. It keeps every intermediate product in the split arithmetic far
// below the int64 limit.
const MaxAmount Money = 1_000_000_000_000

// Errors returned by Ledger methods. Compare with errors.Is.
var (
	ErrInvalidUser          = errors.New("splitwise: user id must not be empty")
	ErrDuplicateUser        = errors.New("splitwise: user already exists")
	ErrUnknownUser          = errors.New("splitwise: unknown user")
	ErrInvalidAmount        = errors.New("splitwise: amount must be in [1, MaxAmount]")
	ErrNoParticipants       = errors.New("splitwise: an expense needs at least one participant")
	ErrDuplicateParticipant = errors.New("splitwise: user listed more than once")
)

// Share is one user's portion of an expense, in minor units.
type Share struct {
	User   UserID
	Amount Money
}

// Transfer is a directed amount: From owes To (in Debts) or From should pay
// To (in a Simplify plan). Amount is always positive.
type Transfer struct {
	From, To UserID
	Amount   Money
}

// ---- the ledger -------------------------------------------------------------

// pairKey names an unordered pair of users; lo < hi.
type pairKey struct{ lo, hi UserID }

func keyOf(a, b UserID) pairKey {
	if a < b {
		return pairKey{a, b}
	}
	return pairKey{b, a}
}

// book is the incrementally maintained state of one scope (the whole ledger,
// or one group): the net balance per user and the net amount per pair. It does
// no locking and no validation; the Ledger owns both.
type book struct {
	bal  map[UserID]Money  // positive: is owed money; negative: owes money
	pair map[pairKey]Money // positive: lo owes hi; negative: hi owes lo; never 0
}

func newBook() *book {
	return &book{bal: map[UserID]Money{}, pair: map[pairKey]Money{}}
}

// owe records that debtor owes creditor amt more (amt may be negative, which
// reverses an earlier debt). Both views change together, so they cannot drift.
func (b *book) owe(debtor, creditor UserID, amt Money) {
	b.bal[debtor] -= amt
	b.bal[creditor] += amt
	k := keyOf(debtor, creditor)
	if debtor == k.lo {
		b.pair[k] += amt
	} else {
		b.pair[k] -= amt
	}
	if b.pair[k] == 0 {
		delete(b.pair, k)
	}
}

// applyExpense books an expense, or (sign == -1) reverses it. The payer's own
// share nets against what the payer paid, so only the other participants'
// shares become debts.
func (b *book) applyExpense(payer UserID, shares []Share, sign Money) {
	for _, s := range shares {
		if s.User != payer && s.Amount != 0 {
			b.owe(s.User, payer, sign*s.Amount)
		}
	}
}

// debts lists the non-zero pairs as "From owes To", sorted by (From, To).
func (b *book) debts() []Transfer {
	out := make([]Transfer, 0, len(b.pair))
	for k, v := range b.pair {
		if v > 0 {
			out = append(out, Transfer{From: k.lo, To: k.hi, Amount: v})
		} else {
			out = append(out, Transfer{From: k.hi, To: k.lo, Amount: -v})
		}
	}
	slices.SortFunc(out, func(a, b Transfer) int {
		if c := cmp.Compare(a.From, b.From); c != 0 {
			return c
		}
		return cmp.Compare(a.To, b.To)
	})
	return out
}

// expense is the stored record of one expense: the shares exactly as they were
// computed, so a reversal never has to recompute (and possibly disagree).
type expense struct {
	group  GroupID // "" for an expense outside any group (stage 2)
	payer  UserID
	total  Money
	shares []Share
}

// Ledger records shared expenses and answers "what does everyone owe". Create
// one with New. All methods are safe for concurrent use (stage 3 contract; the
// reference takes one mutex from the start because it costs one line).
type Ledger struct {
	mu       sync.RWMutex // guards everything below
	all      *book
	expenses map[ExpenseID]*expense
	nextID   ExpenseID
	groups   map[GroupID]*group // stage 2
}

// New returns an empty ledger with the given users registered. It returns
// ErrInvalidUser for an empty id and ErrDuplicateUser for a repeated one.
func New(users ...UserID) (*Ledger, error) {
	l := &Ledger{
		all:      newBook(),
		expenses: map[ExpenseID]*expense{},
		groups:   map[GroupID]*group{},
	}
	for _, u := range users {
		if err := l.AddUser(u); err != nil {
			return nil, err
		}
	}
	return l, nil
}

// AddUser registers a user with a zero balance.
func (l *Ledger) AddUser(id UserID) error {
	if id == "" {
		return ErrInvalidUser
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if _, ok := l.all.bal[id]; ok {
		return ErrDuplicateUser
	}
	l.all.bal[id] = 0
	return nil
}

// AddExpense records that payer paid amount (minor units) for the
// participants, split equally, and returns the expense's id.
//
// The split rule: sort the participants by id; each owes amount/n rounded
// down, and the first amount%n of them in that order owe one cent more. The
// shares therefore always sum to exactly amount, and the result does not
// depend on the order the caller listed the participants in. The payer may or
// may not be a participant; a payer who is one simply owes themselves their
// own share.
//
// It returns ErrInvalidAmount (amount < 1 or > MaxAmount), ErrNoParticipants,
// ErrDuplicateParticipant or ErrUnknownUser (payer or participant) and, when
// it returns an error, has changed nothing.
func (l *Ledger) AddExpense(payer UserID, amount Money, participants []UserID) (ExpenseID, error) {
	shares, err := equalShares(amount, participants)
	if err != nil {
		return 0, err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := l.validateLocked(payer, amount, shares); err != nil {
		return 0, err
	}
	return l.recordLocked("", payer, amount, shares), nil
}

// Balance returns the user's net position: positive means the user is owed
// that much overall, negative means the user owes it. It returns
// ErrUnknownUser for an unregistered user.
func (l *Ledger) Balance(user UserID) (Money, error) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	b, ok := l.all.bal[user]
	if !ok {
		return 0, ErrUnknownUser
	}
	return b, nil
}

// Balances returns every registered user's balance (zeros included) as one
// consistent snapshot: the values always sum to zero. The caller owns the map.
func (l *Ledger) Balances() map[UserID]Money {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return maps.Clone(l.all.bal)
}

// Debts is the pairwise "who owes whom" view: for every pair of users with a
// non-zero net amount between them, one Transfer{From, To, Amount} meaning
// From owes To. Debts in opposite directions are netted. The result is sorted
// by (From, To); the caller owns the slice.
func (l *Ledger) Debts() []Transfer {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.all.debts()
}

// ---- internals (require l.mu) ------------------------------------------------

func checkAmount(a Money) error {
	if a < 1 || a > MaxAmount {
		return ErrInvalidAmount
	}
	return nil
}

// validateLocked checks everything that can be checked before anything is
// changed. It is the one place the "shares sum to the total" invariant is
// enforced, so a buggy Splitter (stage 2) cannot corrupt the ledger.
func (l *Ledger) validateLocked(payer UserID, total Money, shares []Share) error {
	if err := checkAmount(total); err != nil {
		return err
	}
	if _, ok := l.all.bal[payer]; !ok {
		return ErrUnknownUser
	}
	if len(shares) == 0 {
		return ErrNoParticipants
	}
	seen := make(map[UserID]struct{}, len(shares))
	var sum Money
	for _, s := range shares {
		if _, ok := l.all.bal[s.User]; !ok {
			return ErrUnknownUser
		}
		if _, dup := seen[s.User]; dup {
			return ErrDuplicateParticipant
		}
		seen[s.User] = struct{}{}
		if s.Amount < 0 || s.Amount > MaxAmount {
			return ErrInvalidShare
		}
		sum += s.Amount
	}
	if sum != total {
		return ErrSplitSum
	}
	return nil
}

// recordLocked books a validated expense and stores it for later reversal.
func (l *Ledger) recordLocked(g GroupID, payer UserID, total Money, shares []Share) ExpenseID {
	l.nextID++
	e := &expense{group: g, payer: payer, total: total, shares: slices.Clone(shares)}
	l.expenses[l.nextID] = e
	l.all.applyExpense(payer, e.shares, 1)
	return l.nextID
}

// equalShares implements the stage-1 rule: equal weights through the general
// largest-remainder allocator, participants in sorted-id order.
func equalShares(total Money, participants []UserID) ([]Share, error) {
	if err := checkAmount(total); err != nil {
		return nil, err
	}
	if len(participants) == 0 {
		return nil, ErrNoParticipants
	}
	ids := slices.Clone(participants)
	slices.Sort(ids)
	for i := 1; i < len(ids); i++ {
		if ids[i] == ids[i-1] {
			return nil, ErrDuplicateParticipant
		}
	}
	weights := make([]int64, len(ids))
	for i := range weights {
		weights[i] = 1
	}
	return sharesFor(ids, allocate(total, weights)), nil
}

func sharesFor(ids []UserID, amounts []Money) []Share {
	out := make([]Share, len(ids))
	for i := range ids {
		out[i] = Share{User: ids[i], Amount: amounts[i]}
	}
	return out
}

// allocate splits total in proportion to weights using the largest-remainder
// method: every entry gets floor(total*w/sum); the cents left over (always
// fewer than len(weights)) go one each to the entries with the largest
// fractional remainders, ties to the lowest index. The result sums to exactly
// total. Requires total in [1, MaxAmount], weights >= 0 with a positive sum and
// each weight <= MaxWeight, so total*w cannot overflow.
func allocate(total Money, weights []int64) []Money {
	var sum int64
	for _, w := range weights {
		sum += w
	}
	out := make([]Money, len(weights))
	rem := make([]int64, len(weights))
	var given Money
	for i, w := range weights {
		p := int64(total) * w
		out[i] = Money(p / sum)
		rem[i] = p % sum
		given += out[i]
	}
	order := make([]int, len(weights))
	for i := range order {
		order[i] = i
	}
	slices.SortFunc(order, func(a, b int) int {
		if rem[a] != rem[b] {
			return cmp.Compare(rem[b], rem[a]) // larger remainder first
		}
		return cmp.Compare(a, b) // then lower index (= lower user id)
	})
	for _, i := range order[:int(total-given)] {
		out[i]++
	}
	return out
}
