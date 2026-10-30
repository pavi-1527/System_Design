# C8 — Case study: ticket booking

| | |
|---|---|
| Track | C — HLD case studies |
| Estimated time | 6 h (2 units) — CORE 5 h, STRETCH 1 h |
| Prerequisites | A2, A5 (gates passed) |
| Labs | `labs/c8/` — `waitroom` (virtual waiting room: FIFO queue with position and ETA, token-bucket admission, HMAC-signed passes with a TTL, idempotent joins, a bounded queue, expiry) |
| Gate | quiz ≥ 85% ×2 (different days) · labs pass · design ≥ meets · teach-back pass |

## Why this module exists

A regional ticketing company has sold millions of tickets from one database: a `seats` table, a checkout page, a
few orders a second. Then it wins a stadium tour: 18,000 seats a night, on sale Saturday at 10:00. At 09:59 half a
million people are refreshing the event page. At 10:00 the seat map, an 18,000-row query against the primary, is
requested ≈ 250,000 times a second; the database is at 100% CPU within four seconds. The hold code reads a seat's
status and then writes it, so 212 seats are sold twice. The site is down for 40 minutes, the tickets go mostly to
scripts that hammered the hold endpoint, and support spends the following week on two kinds of call: "you charged me
for a seat someone else has" and, ≈ 3,000 times, "I paid and got nothing", because the payment provider's webhook
arrived after the seat's 5-minute hold had expired and the seat had been resold.

None of that is a data-size problem: the whole tour's inventory fits in a few megabytes. It is a **contention**
problem (a few thousand rows that everyone wants in the same second), a **time** problem (a hold is a lease with a
clock, and the payment's outcome arrives on someone else's clock), and a **front-door** problem (28 people per seat
arrive at once, and most of them must be told "no" quickly and fairly).

Interviewers like this problem because the obvious design is a CRUD app, and the senior signal is specific: the exact
statement that makes oversell impossible and the isolation level it needs; holds with lazy expiry; general admission
as a hot counter; a payment-pending state with "cancel, then release"; idempotent confirmation and reconciliation; and
a waiting room that admits people at the rate the inventory can sell, with passes the booking API verifies without a
database read.

## Outcomes

- **Scope** a ticket-booking system in six minutes: the clarifying questions whose answers change the design (assigned
  seats vs general admission, the on-sale peak, holds and their TTL, how payment confirms, per-customer limits,
  fairness, other sellers) and the two hard invariants (never sell a seat twice; never take money without a ticket or
  a refund). — CORE
- **Estimate** an on-sale (arrivals, demand ratio, seat-map reads and bandwidth, an admission rate from Little's law,
  queue wait and sell-out time, hold transactions per second, the single-row ceiling, storage), and tie each number to
  a decision. — CORE
- **Design** the API (idempotent hold returning 409 with the seat, checkout that extends the hold once, signed
  webhook, server-side price) and a data model partitioned by event with seat rows, self-routing hold ids, limit rows
  and the unique constraints that make oversell and double processing impossible. — CORE
- **Draw** the high-level design with the on-sale path through the waiting room, the booking path and the payment path
  traced, and justify every box by a requirement or a number. — CORE
- **Choose** an oversell-prevention mechanism (conditional UPDATE, `SELECT … FOR UPDATE` in sorted order, optimistic
  versions, insert-to-claim, Redis holds, an in-memory owner per event), say which isolation level each needs and where
  each breaks, and treat general admission as a hot counter (buckets, block allocation). — CORE
- **Design** the payment-in-flight path: a selection hold and a payment window, lazy expiry, cancel-then-release,
  idempotent confirmation keyed on the provider's event id, authorise-then-capture, the late-payment path and
  reconciliation. — CORE
- **Implement** the waiting room (`labs/c8/waitroom`): idempotent FIFO joins with position and ETA, token-bucket
  admission capped at a burst, HMAC-signed passes verified in constant time, expiry and a bounded queue, passing under
  `-race`. — CORE
- **Handle** the hot event past the room: rows every transaction touches, a CDN-served availability snapshot, position
  by sequence number and watermark, an adaptive admission rate and an honest "sold out". — CORE
- **Evaluate** bot and fairness controls (randomised pre-queue, registration lotteries, account and card limits, passes
  bound to accounts, rate limits at join) and what each costs. — STRETCH

## Unit plan

| Unit | Topic | Hours | Core/Stretch | Lab |
|---|---|---|---|---|
| 1 | [Guided design: requirements, estimation, API and data model, high-level design](unit-1-guided-design.md) | 3 | CORE (2.5) / STRETCH (0.5) | — (conceptual; the estimate sheet and the diagram are the work) |
| 2 | [Deep dives: oversell prevention and holds; payment in flight; the flash sale and the waiting room; common mistakes](unit-2-deep-dives.md) | 3 | CORE (2.5, incl. ≈ 1 h gate work) / STRETCH (0.5) | `labs/c8/waitroom` |

Total: 6 h, CORE 5 h, STRETCH 1 h. This is a compact module: it leans on A2 (isolation anomalies, MVCC and locks) and
A5 (leases and fencing tokens) and spends its time applying them to one contended inventory. The seat-hold path in
code is B3 problem 9 ([`labs/b3/ticketing`](../../labs/b3/ticketing/)); this module links to it rather than
rebuilding it, and builds the component that sits in front of it.

**Unit 1 (3 h)**
- Session 1 (1.5 h, CORE): warm-up; §1 requirements (write your eight questions and the two invariants before the
  reveal); §2 the on-sale estimate sheet by hand before the worked version, then `sd estimate -kind qps -n 3` and
  `sd estimate -kind concurrency -n 3`.
- Session 2 (1 h CORE + 0.5 h STRETCH): §3 API and data model (commit to the constraints and the limit row first); §4
  the high-level design with the on-sale, booking and payment paths and the three break-it traces. STRETCH: the tour
  on-sale arithmetic at the end of §2 and "best available" seat picking in §3.

**Unit 2 (3 h)**
- Session 3 (1.5 h, CORE): warm-up; §5 oversell prevention (the five implementations, the isolation table, GA
  counters) and its break-it; §6 payment in flight and the four timelines; read §7 and do lab parts 1–2 (`New`,
  `Join`, `Status`, `Leave`, `Len`, then `Tick`).
- Session 4 (1 h, CORE incl. gate work): lab parts 3–4 (`Verify`, expiry), `sd lab C8`; break-it exercises 1–6;
  Common mistakes; first `sd quiz C8` attempt; the [design exercise](design-exercise.md) (45 min, solo, timed) and the
  [teach-back](teachback.md) write-up, in the same sitting or the next day. Second `sd quiz C8` attempt on a later day.
- STRETCH (0.5 h): bots, fairness and presale lotteries at the end of §7; the "one owner per event" and Redis-holds
  discussions in §5 in depth.

## How this connects to your banking work

- **A hold is an authorisation.** A card authorisation reduces the member's available balance without touching the
  ledger balance, and either a capture or the authorisation's expiry settles it. Seat holds are the same state
  machine, and Unit 2 §6's authorise-then-capture with a void on a lost seat is literally the card rail you integrate
  with; "cancel the payment, then release the seats" is "void before you release the funds hold".
- **Oversell is overdraft.** The conditional `UPDATE … WHERE status = 'available'` in Unit 2 §5 is the same statement
  as A2's overdraft fix (`… WHERE balance + overdraft >= amount`), and the per-account ticket limit is the shared
  overdraft limit across accounts: a write skew that needs a row to conflict on
  ([A2 Unit 3 §7](../A2-storage/unit-3-transactions-schema.md#7-acid-in-practice-and-the-isolation-anomalies)).
- **Webhooks from a vendor you don't control.** The payment provider's webhook arrives twice, late, or not at all, and
  a timeout on your side is an unknown outcome. Deduping on the provider's event id, binding the key before doing the
  work, and an hourly reconciliation against the provider's report are what your payment and core-banking adapters
  already do; Unit 2 §6 applies them to inventory.
- **The waiting room is a rate limiter for a fragile downstream.** Admitting at the rate the inventory and the payment
  provider can serve is the same decision as throttling a bulk payroll file into a core-banking vendor that is
  contracted for N requests a second ([A7 Unit 2 §2](../A7-reliability/unit-2-breakers-rate-limits.md#2-rate-limiting-algorithms)).
- **One hot tenant.** A big on-sale is one event monopolising a partition, the same shape as one credit union's payroll
  morning hammering a single settlement GL row
  ([A4 Unit 3 §8](../A4-replication-partitioning/unit-3-hot-keys-indexes-sizing.md#8-skew-hot-keys-and-celebrity-problems)):
  isolate it, and never let one row be touched by every transaction. The append-only inventory log is the same kind
  of audit trail you keep for every money movement.

## Mastery gate checklist

- [ ] `sd quiz C8` ≥ 85% — attempt 1 (date: ____)
- [ ] `sd quiz C8` ≥ 85% — attempt 2 on a different day (date: ____)
- [ ] `sd lab C8` — all lab tests pass (with `-race`)
- [ ] Design exercise ([`design-exercise.md`](design-exercise.md)): self-score recorded, tutor score ≥ "meets"
      (`sd record design C8 --self <lvl> --tutor <lvl> --notes "..."`)
- [ ] Teach-back ([`teachback.md`](teachback.md)): tutor marks pass (`sd record teachback C8 --result pass --notes "..."`)

`sd status` is the source of truth; this list is for your own reading.

## What's next

C8 is one of the core case studies that D4 (the full senior loop) requires. Expect "design Ticketmaster" or a variant
(movie or concert booking, a flash sale, hotel or airline seat inventory, restaurant slots, vaccine or exam
appointments) in the D3 and D4 mocks, where oversell prevention, payment in flight and the waiting room are the deep
dives an interviewer picks. C9 (payments and ledger) takes the payment half of this module further: double-entry,
idempotency end to end and reconciliation with money as the inventory. Before moving on, run `sd review` and clear any
C8 questions it surfaces, especially tags `concurrency`, `isolation`, `idempotency`, `rate-limiting` and `hot-key`;
and re-read both units' wrap-up tables the day before your next HLD mock.
