# C9 — Case study: payments and ledger

| | |
|---|---|
| Track | C — HLD case studies |
| Estimated time | 9 h (2 units) — CORE 7 h, STRETCH 2 h |
| Prerequisites | A2, A5, A6, A7, A9 (gates passed) |
| Labs | `labs/c9/` — `ledger` (balanced idempotent postings, no-negative available balance under concurrency, holds with capture, void and expiry, reversals, audit, reconciliation) |
| Gate | quiz ≥ 85% ×2 (different days) · labs pass · design ≥ meets · teach-back pass |

## Why this module exists

A digital-banking platform launches a wallet with a `balance` column per member, a transfer endpoint that runs two
`UPDATE` statements, and a payouts endpoint that calls the processor synchronously and retries twice on timeout. At
4 M members, finance closes the quarter and the books are out by $312,418.27. Six weeks of forensics find duplicate
payouts (the processor had accepted the request that timed out), money created by a pod killed between a debit and
its credit, overdrafts from double taps that both passed the balance check, 1,140 balance "fixes" from a support
tool with no record of the old values, and $9k of processor settlements that matched nothing and were written off.
The auditors' first question, "how did this member's balance come to be $41.07 on 30 June?", has no answer, because
the balance is a number that has been overwritten 600 times.

None of that is a scale problem. At ≈ 140 postings a second one database primary has headroom to spare. The hard
parts are correctness under retries and concurrency, an external processor whose timeouts are unknown outcomes, and
the admission that every mechanism has a failure window, so you reconcile against what the processor and the bank
say actually happened, every day, and you can prove nothing was edited.

Interviewers like this problem because the senior signal is precise and easy to miss: a double-entry journal with
derived balances, an idempotency key per business fact at every hop, an `unknown` state instead of a guess, a
suspense account instead of a write-off, reconciliation as a designed component, and a clear answer to which state
needs strong consistency and which doesn't.

## Outcomes

- **Scope** a payments and ledger system in six minutes: the clarifying questions whose answers change the design
  (money movements, system of record, external parties and settlement, overdraft rules, authorisation budget, volume,
  currencies, audit and tenancy), non-functional requirements with numbers, and the invariants a nightly job can
  check. — CORE
- **Estimate** postings per second (average and peak), the write rate on a hot house account against the per-row lock
  bound, unknown outcomes per day, journal storage with retention and replication, and reconciliation volume, and tie
  each number to a decision. — CORE
- **Design** the API (an idempotency key per member intent, integer minor units with a currency, 201 for internal
  transfers and 202 for processor-backed ones, idempotent inbound processor calls and webhooks) and a data model with
  an append-only journal, a balance projection maintained in the same transaction, holds, and workflow state kept
  separate from postings. — CORE
- **Draw** the high-level design with the card authorisation, clearing, settlement and withdrawal paths traced,
  justify every box, and say which state must be strongly consistent and which may be eventual. — CORE
- **Explain** double-entry postings, holds and available balance, reversals instead of edits, and the concurrency
  control that enforces the no-negative rule (row locks in sorted order, conditional updates, serializable, single
  writer), and relieve hot accounts with append-only legs, sub-accounts or aggregation. — CORE
- **Design** idempotency end to end: a key per business fact at every hop (request, workflow, posting step, processor
  call, webhook event, file line), timeouts as unknown outcomes resolved by status query and reconciliation, the
  outbox, and the withdrawal saga with its pivot. — CORE
- **Design** reconciliation (three-way against the processor report and the bank statement, matching on references,
  break types, timing differences, suspense with aging) and audit (balances recomputed from the journal, a
  hash-chained and anchored audit stream, maker-checker for manual postings). — CORE
- **Implement** the ledger (`labs/c9/ledger`): balanced multi-leg postings, idempotency with replay and key-reuse
  rejection under concurrent duplicates, no-negative available balance, holds with partial capture, void and lazy
  expiry, reversals, `Audit` and `Reconcile`, passing under `-race`. — CORE
- **Represent** money correctly (minor units, deterministic allocation, one rounding step), post currency conversions
  through FX position accounts, keep card data out of most systems with tokenisation, and move money across ledger
  shards with a clearing-account saga. — STRETCH

## Unit plan

| Unit | Topic | Hours | Core/Stretch | Lab |
|---|---|---|---|---|
| 1 | [Guided design: requirements, estimation, API and data model, high-level design](unit-1-guided-design.md) | 4 | CORE (3.5) / STRETCH (0.5) | — (conceptual; the estimate sheet, the chart of accounts and the diagram are the work) |
| 2 | [Deep dives: the ledger core and concurrency; idempotency across the processor boundary; reconciliation and audit; common mistakes](unit-2-deep-dives.md) | 5 | CORE (3.5, incl. ≈ 1 h gate work) / STRETCH (1.5) | `labs/c9/ledger` |

Total: 9 h, CORE 7 h, STRETCH 2 h. The module leans on A2 (isolation and locks), A5 (consistency per piece of state),
A6 (outbox, idempotent consumers, sagas), A7 (timeouts, retries, breakers) and A9 (audit trails), and spends its time
applying them to money.

**Unit 1 (4 h)**
- Session 1 (1.5 h, CORE): warm-up; §1 requirements (write your eight questions, the chart of accounts and the
  invariants before the reveal); §2 the estimate sheet by hand before the worked version, then
  `sd estimate -kind qps -n 3` and `sd estimate -kind concurrency -n 3`.
- Session 2 (2 h CORE + 0.5 h STRETCH): §3 API and data model (commit to the keys, the amount type and the unique
  constraints first); §4 the high-level design with the card and withdrawal paths traced and the three break-it
  traces. STRETCH: the payday-file arithmetic at the end of §2 and the card lifecycle detail (clearing that differs
  from the authorisation, refunds, chargebacks) in §3.

**Unit 2 (5 h)**
- Session 3 (1.25 h, CORE): warm-up; §5 the ledger core; lab part 1 (`OpenAccount`, `Balance`, `Post` with
  validation and the no-negative rule, `Get`, `History`).
- Session 4 (1.25 h, CORE): §6 idempotency end to end; lab part 2 (the key check under the lock), then part 3 (holds)
  and `Reverse`; §5 break-it 1–2 and §6 break-it 1.
- Session 5 (1 h, CORE incl. gate work): §7 reconciliation and audit; `Audit` and `Reconcile`; §7 break-it 1–2;
  Common mistakes; `go test -race -count=20 ./labs/c9/ledger/` and `sd lab C9`; first `sd quiz C9` attempt. Then the
  [design exercise](design-exercise.md) (45 min, solo, timed) and the [teach-back](teachback.md) write-up, in the
  same sitting or the next day. Second `sd quiz C9` attempt on a later day.
- STRETCH (1.5 h): §8 money representation, multi-currency, PCI scope and sharding, with its break-it questions; the
  optional `Split` extension in §8.

## How this connects to your banking work

- **You have lived the unknown outcome.** A core-banking or card-processor call that times out after the far side
  committed is the central problem of this module. The `unknown` state, the status query by your own reference, the
  in-transit account where the money waits, and reconciliation as the final word (Unit 2 §6) are the formal version of
  the incident playbooks you've written; the A7 capstone
  ([A7 Unit 3 §5](../A7-reliability/unit-3-isolation-degradation-dr.md#5-capstone-the-resilient-vendor-adapter)) is
  the adapter that sits in front of it.
- **Idempotency across every boundary you integrate.** Your adapters already send idempotency keys or client
  references to processors and dedupe their callbacks. Unit 2 §6 makes the chain explicit: the member's request key,
  the workflow id as the processor's key, step keys for each posting, the processor's event id for webhooks, and file
  id plus line for settlement imports, each derived from the business fact and enforced in the same transaction as the
  effect ([A1 Unit 3 §2](../A1-networking-api/unit-3-idempotency-auth.md#2-idempotency-keys-end-to-end),
  [A6 Unit 2 §2](../A6-async/unit-2-outbox-consumers.md#2-idempotent-consumers-and-dedupe)).
- **Authorisations, holds and clearing are card-processor work.** The hold placed at authorisation, the clearing that
  captures less or more than the hold, the reversal that voids it, the hold that lapses, and the daily settlement with
  the network (Unit 1 §3–§4, Unit 2 §5) map one-to-one onto the card-processor messages and files you've handled. The
  lab's `Hold`, `Capture`, `Void` and lazy expiry are that lifecycle as code.
- **The core is often the system of record.** For credit unions, your platform's ledger is frequently a sub-ledger of
  the core banking system. Unit 1 §1's "who is the system of record?" question flips the design to sagas against the
  core with idempotent calls, and adds the core's end-of-day file as a third reconciliation source next to the
  processor's report and the bank statement (Unit 2 §7).
- **Audit trails and multi-tenant credit unions.** Reversals instead of edits, maker-checker for manual postings, a
  hash-chained audit stream per tenant
  ([A9 Unit 2 §4](../A9-security-multitenancy/unit-2-data-protection-audit.md#4-audit-trails-and-tamper-evidence),
  [`labs/a9/auditlog`](../../labs/a9/auditlog/)), and per-tenant house accounts, processor credentials and
  reconciliation (Unit 1 §4) are what make each credit union's books balance on their own and stand up to its
  examiners.

## Mastery gate checklist

- [ ] `sd quiz C9` ≥ 85% — attempt 1 (date: ____)
- [ ] `sd quiz C9` ≥ 85% — attempt 2 on a different day (date: ____)
- [ ] `sd lab C9` — all lab tests pass (with `-race`)
- [ ] Design exercise ([`design-exercise.md`](design-exercise.md)): self-score recorded, tutor score ≥ "meets"
      (`sd record design C9 --self <lvl> --tutor <lvl> --notes "..."`)
- [ ] Teach-back ([`teachback.md`](teachback.md)): tutor marks pass (`sd record teachback C9 --result pass --notes "..."`)

`sd status` is the source of truth; this list is for your own reading.

## What's next

C9 is one of the core case studies that D4 (the full senior loop) requires. Expect "design a payment system" or a
variant (a digital wallet, a marketplace's pay-ins and seller payouts, a card-issuing platform, a billing and
invoicing system, a loyalty-points ledger) in the D3 and D4 mocks, where the ledger's concurrency, the processor
boundary and reconciliation are the deep dives an interviewer picks. C8 (ticket booking) reuses holds and oversell
prevention with seats instead of money, and E3 (the job pipeline) builds the retry, idempotency and dead-letter
machinery as a real service. Before moving on, run `sd review` and clear any C9 questions it surfaces, especially tags
`idempotency`, `transactions`, `concurrency`, `reconciliation` and `audit`; and re-read both units' wrap-up tables
the day before your next HLD mock.
