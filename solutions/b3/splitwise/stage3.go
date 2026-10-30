package splitwise

import (
	"container/heap"
	"errors"
)

// ---- Stage 3: debt simplification and the concurrency contract -------------
// (announced at about minute 50)
//
// Contract:
//   - Simplify takes net balances and returns a list of transfers that, when
//     every one is paid, brings every balance to zero. It needs at most
//     (number of non-zero balances - 1) transfers. Finding the true minimum is
//     NP-hard (it is the problem of partitioning the users into as many
//     zero-sum groups as possible), so the algorithm is the greedy heuristic:
//     repeatedly make the largest debtor pay the largest creditor.
//   - It is a pure function: it never modifies its input, and equal input
//     always gives equal output (ties broken by user id, never by map order).
//   - Every transfer has a positive amount, a debtor as From and a creditor as
//     To, and no user both pays and receives.
//   - Ledger.Simplify is Simplify of one consistent snapshot of the balances;
//     it changes nothing in the ledger. Its error is always nil unless the
//     ledger itself is corrupt.
//   - Every Ledger method is safe for concurrent use. A write is atomic: a
//     concurrent Balances never shows half of an expense, so its values always
//     sum to zero.

// ErrUnbalanced is returned by Simplify when the balances do not sum to zero.
var ErrUnbalanced = errors.New("splitwise: balances do not sum to zero")

// Simplify returns the transfers that settle the given balances (positive:
// is owed, negative: owes). See the stage 3 contract above.
func Simplify(balances map[UserID]Money) ([]Transfer, error) {
	var sum Money
	creditors, debtors := &holderHeap{}, &holderHeap{}
	for u, b := range balances { // map order is fine here: the heaps re-order totally
		sum += b
		switch {
		case b > 0:
			*creditors = append(*creditors, holder{u, b})
		case b < 0:
			*debtors = append(*debtors, holder{u, -b})
		}
	}
	if sum != 0 {
		return nil, ErrUnbalanced
	}
	heap.Init(creditors)
	heap.Init(debtors)

	var plan []Transfer
	for creditors.Len() > 0 { // sum == 0, so debtors empties at the same time
		c := heap.Pop(creditors).(holder)
		d := heap.Pop(debtors).(holder)
		pay := min(c.amt, d.amt)
		plan = append(plan, Transfer{From: d.id, To: c.id, Amount: pay})
		// At least one of them is now settled; the other goes back with
		// what is left. That is what bounds the plan at n-1 transfers.
		if c.amt > pay {
			heap.Push(creditors, holder{c.id, c.amt - pay})
		}
		if d.amt > pay {
			heap.Push(debtors, holder{d.id, d.amt - pay})
		}
	}
	return plan, nil
}

// Simplify returns the plan for one consistent snapshot of the ledger's
// balances. It does not modify the ledger; apply the plan with Settle. The
// error is ErrUnbalanced and can only mean the ledger's invariant is broken.
func (l *Ledger) Simplify() ([]Transfer, error) {
	return Simplify(l.Balances())
}

// holder is a user with the magnitude of what they are owed or owe.
type holder struct {
	id  UserID
	amt Money // always > 0
}

// holderHeap is a max-heap on amt; equal amounts order by id so the output
// never depends on insertion (map) order.
type holderHeap []holder

func (h holderHeap) Len() int { return len(h) }
func (h holderHeap) Less(i, j int) bool {
	if h[i].amt != h[j].amt {
		return h[i].amt > h[j].amt
	}
	return h[i].id < h[j].id
}
func (h holderHeap) Swap(i, j int) { h[i], h[j] = h[j], h[i] }
func (h *holderHeap) Push(x any)   { *h = append(*h, x.(holder)) }
func (h *holderHeap) Pop() any {
	old := *h
	n := len(old)
	x := old[n-1]
	*h = old[:n-1]
	return x
}
