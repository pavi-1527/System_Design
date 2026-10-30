package splitwise

import "errors"

// ---- Stage 3: debt simplification and the concurrency contract -------------
// (announced at about minute 50)
//
// Contract:
//   - Simplify takes net balances and returns a list of transfers that, when
//     every one is paid, brings every balance to zero. It needs at most
//     (number of non-zero balances - 1) transfers. Finding the true minimum is
//     NP-hard (it is the problem of partitioning the users into as many
//     zero-sum groups as possible), so use the greedy heuristic: repeatedly
//     make the largest debtor pay the largest creditor.
//   - It is a pure function: it never modifies its input, and equal input
//     always gives equal output (ties broken by user id, never by map order).
//   - Every transfer has a positive amount, a debtor as From and a creditor as
//     To, and no user both pays and receives.
//   - It must stay fast for hundreds of thousands of users.
//   - Ledger.Simplify is Simplify of one consistent snapshot of the balances;
//     it changes nothing in the ledger. Its error is always nil unless the
//     ledger itself is corrupt.
//   - Every Ledger method is safe for concurrent use. A write is atomic: a
//     concurrent Balances never shows half of an expense, so its values always
//     sum to zero.

// ErrUnbalanced is returned by Simplify when the balances do not sum to zero.
var ErrUnbalanced = errors.New("splitwise: balances do not sum to zero")

// Simplify returns the transfers that settle the given balances (positive:
// is owed, negative: owes). See the stage 3 contract above. Balances that do
// not sum to zero are ErrUnbalanced.
func Simplify(balances map[UserID]Money) ([]Transfer, error) {
	// TODO(lab)
	_ = balances
	return nil, errNotImplemented
}

// Simplify returns the plan for one consistent snapshot of the ledger's
// balances. It does not modify the ledger; apply the plan with Settle. The
// error is ErrUnbalanced and can only mean the ledger's invariant is broken.
func (l *Ledger) Simplify() ([]Transfer, error) {
	// TODO(lab)
	return nil, errNotImplemented
}
