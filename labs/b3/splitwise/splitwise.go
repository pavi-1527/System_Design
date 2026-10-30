// Package splitwise is an expense-sharing ledger ("who owes whom") with debt
// simplification.
//
// Lab B3 Problem 7 (75-minute box). Stage 1 is this file; stage 2 (split
// strategies, groups, settlements, removal) lives in stage2.go and stage 3
// (debt simplification, concurrency) in stage3.go. Treat the later files as
// sealed until the interviewer "announces" them (see
// unit-4-algorithms-and-contention.md §1).
//
// Stage 1 contract:
//   - Money is an integer count of minor units (cents). Never float64: 0.10 has
//     no exact binary representation and the error accumulates.
//   - Users must be registered (New or AddUser) before they appear in an expense.
//   - AddExpense(payer, amount, participants) splits amount EQUALLY. The rule
//     for cents that do not divide evenly: sort the participants by id; each
//     owes amount/n rounded down and the first amount%n of them owe one cent
//     more. The shares must sum to exactly amount, and the result must not
//     depend on the order the caller listed the participants in.
//   - Balance is the net position of a user: positive = is owed, negative =
//     owes. THE INVARIANT: the balances of all users sum to exactly zero, always.
//   - Debts is the pairwise view ("From owes To"), netted per pair, sorted by
//     (From, To).
//   - A rejected call changes nothing. Balance must not replay history: a test
//     reads a balance after every one of 60,000 writes.
//   - Concurrency: stage 1 may be written single-threaded, but stage 3 requires
//     every method to be safe for concurrent use. A mutex now costs one line.
package splitwise

import "errors"

// UserID identifies a person in the ledger. It must be non-empty.
type UserID string

// Money is an amount in minor units (cents). 1234 means 12.34.
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

var errNotImplemented = errors.New("splitwise: not implemented")

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

// Ledger records shared expenses and answers "what does everyone owe". Create
// one with New.
type Ledger struct {
	// TODO(lab): fields. Think about: the lock, the balance per user, the net
	// amount per pair of users (so Debts needs no history), the stored
	// expenses (stage 2 reverses them), and groups (stage 2).
}

// New returns an empty ledger with the given users registered. It returns
// ErrInvalidUser for an empty id and ErrDuplicateUser for a repeated one.
func New(users ...UserID) (*Ledger, error) {
	// TODO(lab)
	_ = users
	return nil, errNotImplemented
}

// AddUser registers a user with a zero balance.
func (l *Ledger) AddUser(id UserID) error {
	// TODO(lab)
	_ = id
	return errNotImplemented
}

// AddExpense records that payer paid amount (minor units) for the
// participants, split equally, and returns the expense's id.
//
// It returns ErrInvalidAmount (amount < 1 or > MaxAmount), ErrNoParticipants,
// ErrDuplicateParticipant or ErrUnknownUser (payer or participant) and, when
// it returns an error, has changed nothing. The payer may or may not be a
// participant.
func (l *Ledger) AddExpense(payer UserID, amount Money, participants []UserID) (ExpenseID, error) {
	// TODO(lab)
	_, _, _ = payer, amount, participants
	return 0, errNotImplemented
}

// Balance returns the user's net position: positive means the user is owed
// that much overall, negative means the user owes it. It returns
// ErrUnknownUser for an unregistered user.
func (l *Ledger) Balance(user UserID) (Money, error) {
	// TODO(lab)
	_ = user
	return 0, errNotImplemented
}

// Balances returns every registered user's balance (zeros included) as one
// consistent snapshot: the values always sum to zero. The caller owns the map.
func (l *Ledger) Balances() map[UserID]Money {
	// TODO(lab)
	return nil
}

// Debts is the pairwise "who owes whom" view: for every pair of users with a
// non-zero net amount between them, one Transfer{From, To, Amount} meaning
// From owes To. Debts in opposite directions are netted. The result is sorted
// by (From, To); the caller owns the slice.
func (l *Ledger) Debts() []Transfer {
	// TODO(lab)
	return nil
}
