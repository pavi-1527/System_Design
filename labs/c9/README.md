# C9 labs — payments and ledger

One Go package, stdlib only. The stub compiles and returns immediately (`errNotImplemented` or zero values), so
every test fails fast until you implement it. The reference solution lives in `solutions/c9/`; its `*_test.go`
is byte-identical to the one here, so edit the code, never the tests.

Run what the gate checks with `sd lab C9` (`go test -race ./labs/c9/...`). While working, run the package
directly as shown below; before you call it done, `go test -race -count=20 ./labs/c9/ledger/`.

| Order | Package | Unit | Core/Stretch |
|---|---|---|---|
| 1 | [`ledger`](ledger/) | 2 (§5–§7) | CORE |

---

## 1. `ledger` — double-entry ledger with idempotent postings, holds, reversals, audit and reconciliation

**Goal.** The component every payments design is judged on. Money only moves as a balanced posting (debits =
credits, all or nothing); a posting is idempotent per key, so a retried transfer posts once; balances are
derived from an append-only journal and never edited; member accounts can't go below their *available*
balance (balance minus holds); authorisation holds reserve funds without moving them and expire; corrections
are reversals, appended, never edits; `Audit` proves the invariants from the journal; `Reconcile` compares the
journal with a processor's settlement statement and names every discrepancy.

**Edit** `labs/c9/ledger/ledger.go`. Provided: errors, `Side`, `Entry`, `Request`, `Posting` (with `Amount`),
`Hold`, `StatementLine`, `Mismatch`, `Report` (with `Clean`), `WithClock`. Implement, in order:

1. `OpenAccount`, `Balance`, `Post` (validation, the no-negative rule, balances, the journal), `Get`, `History`
   (Unit 2 §5).
   Tests: `TestOpenAccount_*`, `TestPost_Balanced*`, `TestPost_Rejects*`, `TestPost_MultiLeg*`,
   `TestPost_NegativeBalance*`.
2. Idempotency: the key check (same request → replay, different request → `ErrKeyReused`) under the same lock
   as the write (Unit 2 §6). Tests: `TestPost_SameKey*`, `TestPost_Concurrent*`.
3. Holds: `Hold`, `Available`, `GetHold`, `Capture`, `Void`, lazy expiry (Unit 2 §5). Tests: `TestHold_*`.
4. `Reverse` (Unit 2 §5), `Audit`, `Reconcile` (Unit 2 §7). Tests: `TestReverse_*`, `TestAudit_*`, `TestReconcile_*`.

**Run:** `go test -race ./labs/c9/ledger/`

**Break it** (edit your passing solution, watch which test fails, then revert):

- Move the available-funds check outside the critical section (read the balance, unlock, lock again, write).
  Run `go test -race -count=5 -run 'Concurrent|NeverOverdraw' ./labs/c9/ledger/`. Expected:
  `TestHold_ConcurrentHoldsNeverOverReserve` grants more than 3 holds of 300 against 1,000 (reliably), and
  `TestPost_ConcurrentTransfersConserveMoneyAndNeverOverdraw` sometimes reports a member account below zero (it
  checks final balances, so an overdraft refilled later in the run goes unseen). That is the
  READ COMMITTED double-spend from [A2 Unit 3 §7](../../modules/A2-storage/unit-3-transactions-schema.md#7-acid-in-practice-and-the-isolation-anomalies),
  in Go.
- Record the idempotency key *after* applying the posting (or outside the lock). Run
  `go test -race -run ConcurrentDuplicates ./labs/c9/ledger/`. Expected: "fresh=2 (or more) ... want exactly 1
  execution": a double tap debits the member twice. Compare with the atomic `Reserve` of
  [`labs/a1/idempotency`](../a1/idempotency/).
- Add `+1` to an account's balance somewhere without a journal entry (simulate a "quick fix" that edits a balance
  directly). Run `-run Audit`. Expected: `ledger audit: account ... balance ... but journal sums to ...`. This is
  why balances are derived, and why the nightly audit job exists.
- In `Reconcile`, stop reporting `DuplicateRefs` and treat the second copy as a match. Run `-run Reconcile`.
  Then think about what a processor that sends the settlement file twice does to a ledger that trusts it.
- Make `Reverse` skip the no-negative rule. Run `-run Reverse_ObeysNoNegativeRule`. Then decide which accounts in
  a real refund flow are allowed to go negative (hint: the merchant's settlement account, not the member's).

Unit 2 §5–§7 has the expected observations and the production mapping (database transaction + unique constraint
instead of the mutex, outbox for the events, hash-chained audit stream).

---

## Reference

- Solution: `go test -race ./solutions/c9/...`
- Related earlier labs: [`labs/a1/idempotency`](../a1/idempotency/) (the HTTP layer of the same idempotency
  story), [`labs/a6/outbox`](../a6/outbox/) and [`labs/a6/saga`](../a6/saga/) (the pipeline around the ledger),
  [`labs/a9/auditlog`](../a9/auditlog/) (the tamper-evident audit stream the ledger feeds).
