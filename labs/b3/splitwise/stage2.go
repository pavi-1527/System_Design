package splitwise

import "errors"

// ---- Stage 2: split strategies, groups, settlements, removal ----------------
// (announced at about minute 30)
//
// Contract:
//   - A Splitter turns a total into per-user shares. AddExpense (stage 1) is
//     AddExpenseSplit with Equal. New strategies must not need changes to the
//     ledger: the ledger only trusts the invariant it checks itself.
//   - The ledger validates every Splitter's output (shares >= 0, one per user,
//     sum == total, all users known) before changing anything, whoever wrote
//     the Splitter. Split is called before the ledger's lock is taken, so a
//     Splitter may call back into the ledger.
//   - Percent uses basis points (1 bp = 0.01%); Percent and Weights allocate
//     with the same largest-remainder rule as the equal split: floor each
//     share, then hand the leftover cents one each to the largest fractional
//     remainders, ties to the lowest user id.
//   - Splitter constructors copy their input (a caller may reuse its map), and
//     Split returns shares sorted by user id.
//   - Settle records a payment; it is not an expense and cannot be removed.
//   - RemoveExpense reverses the stored shares of an expense exactly.
//   - Group expenses and settlements count in the group's balances AND in the
//     global balances.

// Errors added in stage 2.
var (
	ErrSplitSum       = errors.New("splitwise: shares do not add up to the expense total")
	ErrPercentSum     = errors.New("splitwise: percentages must add up to exactly 100% (10000 bp)")
	ErrInvalidShare   = errors.New("splitwise: share, weight or percentage out of range")
	ErrUnknownExpense = errors.New("splitwise: unknown expense")
	ErrSelfPayment    = errors.New("splitwise: cannot settle with yourself")
	ErrInvalidGroup   = errors.New("splitwise: group id must not be empty")
	ErrDuplicateGroup = errors.New("splitwise: group already exists")
	ErrUnknownGroup   = errors.New("splitwise: unknown group")
	ErrNotMember      = errors.New("splitwise: user is not a member of the group")
)

// Bps is a proportion in basis points: 1 bp = 0.01%, 10000 bp = 100%.
type Bps int64

// FullBps is 100%.
const FullBps Bps = 10_000

// MaxWeight bounds a Weights entry so total*weight cannot overflow int64.
const MaxWeight int64 = 1_000_000

// Splitter decides how much of an expense each participant owes.
//
// Split must return one Share per participant and the amounts must sum to
// exactly total. Implementations should be pure functions of their inputs and
// deterministic (no map-iteration order leaking into the result).
type Splitter interface {
	Split(total Money) ([]Share, error)
}

// Equal splits equally among users (the stage-1 rule: sorted ids, the first
// total%n of them owe one cent more). Repeated users: ErrDuplicateParticipant.
func Equal(users ...UserID) Splitter {
	// TODO(lab)
	_ = users
	return nil
}

// Exact says precisely what each user owes. The amounts must sum to the total
// (ErrSplitSum); a negative amount is ErrInvalidShare; zero is allowed; an
// empty map is ErrNoParticipants.
func Exact(amounts map[UserID]Money) Splitter {
	// TODO(lab)
	_ = amounts
	return nil
}

// Percent splits by basis points, which must sum to FullBps (ErrPercentSum);
// an entry outside [0, FullBps] is ErrInvalidShare. Cents that do not divide
// evenly go to the largest fractional remainders.
func Percent(shares map[UserID]Bps) Splitter {
	// TODO(lab)
	_ = shares
	return nil
}

// Weights splits in proportion to integer weights ("shares": 1 adult, 2 for a
// couple). Weights must be in [0, MaxWeight] (zero owes nothing) with a
// positive sum, otherwise ErrInvalidShare.
func Weights(weights map[UserID]int64) Splitter {
	// TODO(lab)
	_ = weights
	return nil
}

// AddExpenseSplit records that payer paid total, divided by s. A nil s is
// ErrNoParticipants.
//
// Errors are those of AddExpense plus whatever s returns; ErrSplitSum,
// ErrInvalidShare, ErrDuplicateParticipant or ErrUnknownUser if s returns
// shares the ledger cannot accept. On any error nothing has changed.
func (l *Ledger) AddExpenseSplit(payer UserID, total Money, s Splitter) (ExpenseID, error) {
	// TODO(lab)
	_, _, _ = payer, total, s
	return 0, errNotImplemented
}

// RemoveExpense reverses an expense using the shares stored when it was added
// (it never recomputes the split) and forgets it. Settlements are unaffected.
// It returns ErrUnknownExpense for an id that was never issued or was already
// removed.
func (l *Ledger) RemoveExpense(id ExpenseID) error {
	// TODO(lab)
	_ = id
	return errNotImplemented
}

// Settle records that from paid to the given amount. from's balance rises and
// to's falls by amount; paying more than is owed simply flips the debt.
// Errors: ErrInvalidAmount, ErrSelfPayment, ErrUnknownUser.
func (l *Ledger) Settle(from, to UserID, amount Money) error {
	// TODO(lab)
	_, _, _ = from, to, amount
	return errNotImplemented
}

// GroupID identifies a group (a trip, a flat). It must be non-empty.
type GroupID string

// CreateGroup creates a group from already-registered users. Errors:
// ErrInvalidGroup, ErrDuplicateGroup, ErrUnknownUser, ErrDuplicateParticipant.
// A failed call must not leave a half-built group behind.
func (l *Ledger) CreateGroup(id GroupID, members ...UserID) error {
	// TODO(lab)
	_, _ = id, members
	return errNotImplemented
}

// AddGroupMember adds a registered user to a group. Errors: ErrUnknownGroup,
// ErrUnknownUser, ErrDuplicateParticipant (already a member).
func (l *Ledger) AddGroupMember(id GroupID, user UserID) error {
	// TODO(lab)
	_, _ = id, user
	return errNotImplemented
}

// AddGroupExpense is AddExpenseSplit inside a group: the payer and every user
// in the shares must be members (ErrNotMember), and the expense is counted in
// GroupBalances as well as in the global Balances. ErrUnknownGroup for an
// unknown group.
func (l *Ledger) AddGroupExpense(id GroupID, payer UserID, total Money, s Splitter) (ExpenseID, error) {
	// TODO(lab)
	_, _, _, _ = id, payer, total, s
	return 0, errNotImplemented
}

// SettleInGroup is Settle between two members, counted in the group's
// balances as well as the global ones. Errors: those of Settle, plus
// ErrUnknownGroup and ErrNotMember.
func (l *Ledger) SettleInGroup(id GroupID, from, to UserID, amount Money) error {
	// TODO(lab)
	_, _, _, _ = id, from, to, amount
	return errNotImplemented
}

// GroupBalances returns the net balance of every member, counting only this
// group's expenses and settlements. The values sum to zero; the caller owns
// the map. It returns ErrUnknownGroup for an unknown group.
func (l *Ledger) GroupBalances(id GroupID) (map[UserID]Money, error) {
	// TODO(lab)
	_ = id
	return nil, errNotImplemented
}
