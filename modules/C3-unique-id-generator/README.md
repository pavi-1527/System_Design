# C3 — Case study: unique ID generator

| | |
|---|---|
| Track | C — HLD case studies |
| Estimated time | 6 h (1 weekly unit) — CORE 5 h, STRETCH 1 h |
| Prerequisites | A4, A5 (gates passed) |
| Labs | `labs/c3/` — `snowflake` (bit layout, a concurrent generator with overflow and clock-regression handling, node-id leases with tokens, deadlines and floors) |
| Gate | quiz ≥ 85% ×2 (different days) · labs pass · design ≥ meets · teach-back pass |

## Why this module exists

Every system you will design needs ids, and the first answer everyone gives is wrong at some scale. Random UUIDs
work until the orders table is 400 GB and every insert lands on a random B-tree leaf that isn't in memory, and
until someone wants "newest first" by id. A database sequence works until the second region pays 70 ms per id and
the primary's failover stops every create on the platform. A Snowflake implementation copied from a blog works
until the clock steps back 1.2 s after a VM migration and re-mints a second of payment ids, or until two pods
derive the same machine number from their IP addresses and quietly overwrite one event in two thousand.

The numbers are small (≈ 200k ids/s against an in-process ceiling of ≈ 10^7 per core; no reads; no storage), so
the case study isn't about scale. It is about the three things every distributed system eventually has to get
right, here in their smallest form: what you may assume about **clocks**, how you hand out **identity** to processes
without a single point of failure, and what **ordering** you actually promise. This module runs the 45-minute
framework on the problem end to end, has you commit to each decision before the reveal, and makes you build the
generator and its node-id leases so that the failure modes (a wrapped sequence, a backwards clock, a paused process
that wakes up still minting) are things you have measured, not things you've read about.

## Outcomes

- **Scope** an id-generator prompt in ≤ 6 minutes: uniqueness scope, size (64 bits, and 2^53 for JavaScript), the
  strength of ordering (none, k-sorted, per-producer, total), rates and bursts, processes and regions, behaviour
  during coordination outages, information leakage and gaps, and **say** which box each answer moves. — CORE
- **Estimate** the bit budget: time bits from the tick and lifetime (and the end date for a given epoch), node
  bits from processes plus churn, sequence bits from the hottest producer, and key bytes saved against 128-bit ids;
  **tie** each number to a decision. — CORE
- **Design** the API and data model: an in-process library with errors instead of guesses, a batch service for
  clients that can't embed it, ids as strings in JSON, and the node-lease record (token, expiry, high-water). — CORE
- **Compare** UUIDv4, UUIDv7/ULID, database sequences with interleaved offsets, ticket servers, leased ranges and
  time + node + sequence on size, ordering, coordination per id, dependency failure and index locality, and
  **defend** a choice with a flip condition. — CORE
- **Explain** sequence overflow policies (wait, borrow within a bound, error; never wrap), why k-sorted is not
  linearizable, and why `id % N` sharding concentrates load on one shard. — CORE
- **Choose** a clock-regression policy (wait out small steps, refuse large ones, bounded run-ahead, a floor at
  startup) for slews, steps, leap seconds, resumed VMs and restarts, and **say** what each costs. — CORE
- **Assign** node ids safely with leases: a fencing token per grant, a holder-side deadline measured from the send
  time, a floor for the next holder, and a TTL sized against registry outages and churn. — CORE
- **Implement** the layout, a concurrent generator with overflow and regression handling, a lease registry and a
  leased worker, and **show** with break-its what a naive generator, a shared node id and a paused holder do
  (`labs/c3/snowflake`). — CORE
- **Explain** index locality of random vs time-ordered keys, why time-ordered ids must not lead a range-partition
  key, and what ordering ids from several regions really have. — STRETCH

## Unit plan

| Unit | Topic | Hours | Core/Stretch | Lab |
|---|---|---|---|---|
| 1 | [Guided design: requirements → bit budget → API and model → candidates and HLD → sequence, clock and node-id deep dives](unit-1-unique-id-generator.md) | 6 | CORE (5, incl. ≈ 1.5 h gate work) / STRETCH (1) | `labs/c3/snowflake` |

**Unit 1 (3 × 2 h)**
- Session 1 (2 h): warm-up; §1 requirements (write the card before the reveal), §2 the bit budget (compute it), §3
  API and data model, §4 the candidates side by side and your HLD on paper.
- Session 2 (2 h): §5 the sequence and §6 the clock, with `labs/c3/snowflake` in the README's order (layout →
  generator → the clock rules) and the step-back, modulo and layout break-its.
- Session 3 (2 h): §7 node ids and leases with the registry and worker parts of the lab and the shared-node and
  paused-holder break-its (≈ 50 min), then the gate work: first `sd quiz C3`, the
  [design exercise](design-exercise.md) (45 min, solo), the [teach-back](teachback.md).
- STRETCH (1 h, any time): the second half of §7 (index locality, range-partitioned stores, multi-region ordering).
- Second `sd quiz C3` attempt on a later day.

## How this connects to your banking work

- **Transaction references with constraints you don't control.** Core-banking and payment-network vendors often
  accept only a fixed-length numeric reference. Fitting uniqueness, rough time order and per-tenant opacity into
  15 or 16 decimal digits is §2's bit budget with a much smaller budget; the design exercise is exactly that.
- **Idempotency is not identity.** A vendor callback retried with a freshly minted reference becomes a second
  transfer. §3 separates the caller's idempotency key (created once, before the first attempt) from the record's
  generated id, which is the A1 lesson applied to the id generator.
- **Audit order is a ledger property.** Examiners ask for entries "in the order they were applied". k-sorted ids give
  you "roughly by time"; the order of application is a per-ledger sequence assigned inside the posting
  transaction (§1, and C9 later).
- **Vendor and VM clocks.** Your adapters already compare timestamps from systems whose clocks you don't own. §6's
  rule (refuse rather than guess when your own clock goes backwards, and monitor the offset) is the same discipline
  you apply to a vendor's callback timestamps.

## Mastery gate checklist

- [ ] `sd quiz C3` ≥ 85% — attempt 1 (date: ____)
- [ ] `sd quiz C3` ≥ 85% — attempt 2 on a different day (date: ____)
- [ ] `sd lab C3` — all lab tests pass (with `-race`)
- [ ] Design exercise ([`design-exercise.md`](design-exercise.md)): self-score recorded, tutor score ≥ "meets"
      (`sd record design C3 --self <lvl> --tutor <lvl> --notes "..."`)
- [ ] Teach-back ([`teachback.md`](teachback.md)): tutor marks pass (`sd record teachback C3 --result pass --notes "..."`)

`sd status` is the source of truth; this list is for your own reading.

## What's next

C3 sits between the two halves of Track A's distributed-systems material, and the next case study in the
recommended order, C4 (distributed key-value store), uses all of it: versioned values, clocks and conflict
resolution, and leases for membership. C6 (chat) leans on §5's ordering semantics for message order, and C9
(payments and ledger) on the separation between ids and the ledger's own sequence. Before moving on, run
`sd review` and clear any C3 questions it surfaces, especially the tags `id-generation`, `clocks`, `leases` and
`ordering`.
