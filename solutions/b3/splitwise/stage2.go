package splitwise

import (
	"errors"
	"maps"
	"slices"
)

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
//     with the same largest-remainder rule as the equal split.
//   - Settle records a payment; it is not an expense and cannot be removed.
//   - RemoveExpense reverses the stored shares of an expense exactly.

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
// total%n of them owe one cent more).
func Equal(users ...UserID) Splitter { return equalSplit(slices.Clone(users)) }

// Exact says precisely what each user owes. The amounts must sum to the total
// (ErrSplitSum); a negative amount is ErrInvalidShare; zero is allowed.
func Exact(amounts map[UserID]Money) Splitter { return exactSplit(maps.Clone(amounts)) }

// Percent splits by basis points, which must sum to FullBps (ErrPercentSum).
// Cents that do not divide evenly go to the largest fractional remainders.
func Percent(shares map[UserID]Bps) Splitter { return percentSplit(maps.Clone(shares)) }

// Weights splits in proportion to integer weights ("shares": 1 adult, 2 for a
// couple). Weights must be >= 0 (zero owes nothing) with a positive sum.
func Weights(weights map[UserID]int64) Splitter { return weightSplit(maps.Clone(weights)) }

type equalSplit []UserID

func (s equalSplit) Split(total Money) ([]Share, error) { return equalShares(total, s) }

type exactSplit map[UserID]Money

func (s exactSplit) Split(total Money) ([]Share, error) {
	if err := checkAmount(total); err != nil {
		return nil, err
	}
	if len(s) == 0 {
		return nil, ErrNoParticipants
	}
	var sum Money
	for _, u := range sortedKeys(s) {
		if s[u] < 0 || s[u] > MaxAmount {
			return nil, ErrInvalidShare
		}
		sum += s[u]
	}
	if sum != total {
		return nil, ErrSplitSum
	}
	ids := sortedKeys(s)
	amounts := make([]Money, len(ids))
	for i, u := range ids {
		amounts[i] = s[u]
	}
	return sharesFor(ids, amounts), nil
}

type percentSplit map[UserID]Bps

func (s percentSplit) Split(total Money) ([]Share, error) {
	if len(s) == 0 {
		return nil, ErrNoParticipants
	}
	ids := sortedKeys(s)
	w := make([]int64, len(ids))
	var sum Bps
	for i, u := range ids {
		if s[u] < 0 || s[u] > FullBps {
			return nil, ErrInvalidShare
		}
		w[i] = int64(s[u])
		sum += s[u]
	}
	if sum != FullBps {
		return nil, ErrPercentSum
	}
	if err := checkAmount(total); err != nil {
		return nil, err
	}
	return sharesFor(ids, allocate(total, w)), nil
}

type weightSplit map[UserID]int64

func (s weightSplit) Split(total Money) ([]Share, error) {
	if len(s) == 0 {
		return nil, ErrNoParticipants
	}
	ids := sortedKeys(s)
	w := make([]int64, len(ids))
	var sum int64
	for i, u := range ids {
		if s[u] < 0 || s[u] > MaxWeight {
			return nil, ErrInvalidShare
		}
		w[i] = s[u]
		sum += s[u]
	}
	if sum == 0 {
		return nil, ErrInvalidShare
	}
	if err := checkAmount(total); err != nil {
		return nil, err
	}
	return sharesFor(ids, allocate(total, w)), nil
}

func sortedKeys[V any](m map[UserID]V) []UserID {
	ids := make([]UserID, 0, len(m))
	for u := range m {
		ids = append(ids, u)
	}
	slices.Sort(ids)
	return ids
}

// AddExpenseSplit records that payer paid total, divided by s.
//
// Errors are those of AddExpense plus whatever s returns; ErrSplitSum,
// ErrInvalidShare, ErrDuplicateParticipant or ErrUnknownUser if s returns
// shares the ledger cannot accept. On any error nothing has changed.
func (l *Ledger) AddExpenseSplit(payer UserID, total Money, s Splitter) (ExpenseID, error) {
	if s == nil {
		return 0, ErrNoParticipants
	}
	shares, err := s.Split(total) // outside the lock: s may call back into l
	if err != nil {
		return 0, err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := l.validateLocked(payer, total, shares); err != nil {
		return 0, err
	}
	return l.recordLocked("", payer, total, shares), nil
}

// RemoveExpense reverses an expense using the shares stored when it was added
// (it never recomputes the split) and forgets it. Settlements are unaffected.
// It returns ErrUnknownExpense for an id that was never issued or was already
// removed.
func (l *Ledger) RemoveExpense(id ExpenseID) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	e, ok := l.expenses[id]
	if !ok {
		return ErrUnknownExpense
	}
	l.all.applyExpense(e.payer, e.shares, -1)
	if e.group != "" {
		l.groups[e.group].book.applyExpense(e.payer, e.shares, -1)
	}
	delete(l.expenses, id)
	return nil
}

// Settle records that from paid to the given amount. from's balance rises and
// to's falls by amount; paying more than is owed simply flips the debt.
// Errors: ErrInvalidAmount, ErrSelfPayment, ErrUnknownUser.
func (l *Ledger) Settle(from, to UserID, amount Money) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := l.checkSettleLocked(from, to, amount); err != nil {
		return err
	}
	l.all.owe(to, from, amount)
	return nil
}

func (l *Ledger) checkSettleLocked(from, to UserID, amount Money) error {
	if err := checkAmount(amount); err != nil {
		return err
	}
	if from == to {
		if _, ok := l.all.bal[from]; !ok {
			return ErrUnknownUser
		}
		return ErrSelfPayment
	}
	if _, ok := l.all.bal[from]; !ok {
		return ErrUnknownUser
	}
	if _, ok := l.all.bal[to]; !ok {
		return ErrUnknownUser
	}
	return nil
}

// ---- groups -------------------------------------------------------------------

// GroupID identifies a group (a trip, a flat). It must be non-empty.
type GroupID string

// group is a membership set plus its own book. Group expenses and settlements
// are booked in the group's book AND in the ledger's global book, so the
// global Balances are always the sum over every scope.
type group struct {
	members map[UserID]struct{}
	book    *book
}

// CreateGroup creates a group from already-registered users. Errors:
// ErrInvalidGroup, ErrDuplicateGroup, ErrUnknownUser, ErrDuplicateParticipant.
func (l *Ledger) CreateGroup(id GroupID, members ...UserID) error {
	if id == "" {
		return ErrInvalidGroup
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if _, ok := l.groups[id]; ok {
		return ErrDuplicateGroup
	}
	g := &group{members: map[UserID]struct{}{}, book: newBook()}
	for _, u := range members {
		if _, ok := l.all.bal[u]; !ok {
			return ErrUnknownUser
		}
		if _, dup := g.members[u]; dup {
			return ErrDuplicateParticipant
		}
		g.members[u] = struct{}{}
		g.book.bal[u] = 0
	}
	l.groups[id] = g
	return nil
}

// AddGroupMember adds a registered user to a group. Errors: ErrUnknownGroup,
// ErrUnknownUser, ErrDuplicateParticipant (already a member).
func (l *Ledger) AddGroupMember(id GroupID, user UserID) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	g, ok := l.groups[id]
	if !ok {
		return ErrUnknownGroup
	}
	if _, ok := l.all.bal[user]; !ok {
		return ErrUnknownUser
	}
	if _, dup := g.members[user]; dup {
		return ErrDuplicateParticipant
	}
	g.members[user] = struct{}{}
	g.book.bal[user] = 0
	return nil
}

// AddGroupExpense is AddExpenseSplit inside a group: the payer and every user
// in the shares must be members (ErrNotMember), and the expense is counted in
// GroupBalances as well as in the global Balances.
func (l *Ledger) AddGroupExpense(id GroupID, payer UserID, total Money, s Splitter) (ExpenseID, error) {
	if s == nil {
		return 0, ErrNoParticipants
	}
	shares, err := s.Split(total)
	if err != nil {
		return 0, err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	g, ok := l.groups[id]
	if !ok {
		return 0, ErrUnknownGroup
	}
	if err := l.validateLocked(payer, total, shares); err != nil {
		return 0, err
	}
	if _, ok := g.members[payer]; !ok {
		return 0, ErrNotMember
	}
	for _, sh := range shares {
		if _, ok := g.members[sh.User]; !ok {
			return 0, ErrNotMember
		}
	}
	eid := l.recordLocked(id, payer, total, shares)
	g.book.applyExpense(payer, shares, 1)
	return eid, nil
}

// SettleInGroup is Settle between two members, counted in the group's
// balances as well as the global ones. Errors: those of Settle, plus
// ErrUnknownGroup and ErrNotMember.
func (l *Ledger) SettleInGroup(id GroupID, from, to UserID, amount Money) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	g, ok := l.groups[id]
	if !ok {
		return ErrUnknownGroup
	}
	if err := l.checkSettleLocked(from, to, amount); err != nil {
		return err
	}
	if _, ok := g.members[from]; !ok {
		return ErrNotMember
	}
	if _, ok := g.members[to]; !ok {
		return ErrNotMember
	}
	l.all.owe(to, from, amount)
	g.book.owe(to, from, amount)
	return nil
}

// GroupBalances returns the net balance of every member, counting only this
// group's expenses and settlements. The values sum to zero; the caller owns
// the map. It returns ErrUnknownGroup for an unknown group.
func (l *Ledger) GroupBalances(id GroupID) (map[UserID]Money, error) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	g, ok := l.groups[id]
	if !ok {
		return nil, ErrUnknownGroup
	}
	return maps.Clone(g.book.bal), nil
}
