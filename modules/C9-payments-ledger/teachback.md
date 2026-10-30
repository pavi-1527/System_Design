# C9 Teach-back

## Prompt

A mid-level engineer on your team has been asked to build the money side of a new wallet feature. Their plan: "a
`wallets` table with a `balance` column; a transfer endpoint that subtracts from one row and adds to the other; a
withdrawal endpoint that calls the processor and retries up to three times on errors, each with a new request id; a
webhook endpoint that marks withdrawals paid; and at month end finance can export the table." They have never been
through a quarter close or a processor incident.

Explain to them, in your own words, **how a payments ledger has to be built, and why**: what double entry buys you
and how balances relate to the journal; what must be true at all times and how each invariant is enforced (including
under concurrent requests); how holds and reversals work and why nothing is ever edited; what a processor timeout
means and how a withdrawal gets from "unknown" to "paid" or "failed" without paying twice; which key protects each
hop and where it is stored; what reconciliation is, which sources it compares and what it does with a break; and
what lets you prove to an auditor that nothing was changed.

Form: written, 600–1,000 words, one diagram (Mermaid or a sketch) allowed. Use numbers: pick a scale (theirs or the
module's) and show two or three calculations (for example postings per second against one primary, a hot account's
write rate against a row lock, unknown outcomes per day). Include the entries for at least one movement written out
as debits and credits. No copying from the unit files; if a sentence from the course appears verbatim, the evaluator
will ask you to rephrase it. Write for that engineer, so they can go and change their design, not for an examiner.

## Evaluator checklist

Must-hit (all required for pass):
- [ ] **Double entry with derived balances**: every movement is a posting of balanced debits and credits; balances
      are a projection of an append-only journal, maintained in the same transaction and recomputable; with at least
      one posting written out correctly (including a multi-leg one, such as a transfer with a fee).
- [ ] The **invariants** stated as checkable rules: postings balance, balances equal journal sums, all balances sum
      to zero (per currency), one posting per idempotency key (and per external reference), no restricted account
      below zero available.
- [ ] **No-negative under concurrency**: the available-balance check and the write are one atomic step per account
      (row lock in a fixed order, conditional update, or serializable with retry), and why a separate `SELECT` then
      `UPDATE` double-spends.
- [ ] **Corrections are reversals**, linked to the original and subject to the same rules, never edits; and
      **holds** reserve available balance without moving money, are captured (fully or partly, releasing the rest),
      voided, or expire, with expiry evaluated at read time.
- [ ] A **processor timeout is an unknown outcome**: the withdrawal goes to an unknown state with its money parked
      in an in-transit account, a resolver queries the processor by our reference, and it is never retried with a new
      key nor assumed failed.
- [ ] **Idempotency end to end**: a key per business fact at each hop (the member's request key, the withdrawal id
      as the processor's key, a step key per posting, the processor's event id for webhooks), stored and enforced in
      the same transaction as the effect, not in a cache.
- [ ] **Reconciliation** against the processor's settlement report and the bank statement (three-way), matching on
      the external reference, with break types (missing on either side, amount or currency mismatch, duplicates),
      timing differences aged, and a suspense account for unexplained money.
- [ ] **Audit evidence**: a nightly job that recomputes balances from the journal, and a tamper-evident audit stream
      (hash chain with an external anchor) plus no update/delete rights on the journal.

Should-hit (at least 5 of 9):
- [ ] Money as integer minor units with a currency, never floats; division by a deterministic allocation rule.
- [ ] A chart of accounts that includes house accounts (settlement or cash, in-transit, suspense, fee revenue) and
      says which accounts may go negative.
- [ ] Withdrawals return `202` and run in workers behind a timeout, breaker and bulkhead; P2P is one local
      transaction that no processor outage can touch.
- [ ] The outbox for events out of the ledger, and the withdrawal as a saga with the processor submission as its
      pivot and keyed compensations.
- [ ] Webhooks verified, recorded before acknowledging, deduped on the event id (not the delivery attempt), and
      applied through a state machine that rejects contradictions.
- [ ] Hot house accounts (fee revenue, settlement payable) identified with a number, and relieved by append-only legs,
      sub-accounts or aggregation.
- [ ] Which state needs strong consistency (available balance, keys, holds) and which can be eventual (displays,
      events, audit stream, reconciliation).
- [ ] Workflow state (a withdrawal's status) kept separate from postings, linked by keys derived from workflow id and
      step.
- [ ] Manual postings through the same API with maker-checker; no "set balance" tool.

Red flags (any one = revise):
- [ ] A balance that is set or updated without a corresponding posting, or corrections done by editing entries.
- [ ] Floating-point amounts.
- [ ] Retrying a processor call with a new key or reference, or treating a timeout as a failure (or as a success).
- [ ] Claims exactly-once delivery from a framework or from the processor's 200, with no reconciliation.
- [ ] Idempotency keys generated per attempt, or kept only in a cache, or checked in a separate step from the write.
- [ ] "Use a transaction" as the whole concurrency answer, with the balance check outside the write's atomic step.
- [ ] Reconciliation by amount and date, or no reconciliation at all.
- [ ] Numbers stated as facts with no scale assumption, or a ≥ 8× unit error (per day vs per second, dollars vs cents)
      that isn't caught.

## Recording

`sd record teachback C9 --result <pass|revise> --notes "<missed items>"`
