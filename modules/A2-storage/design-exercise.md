# A2 Design exercise — Storage layer for a credit-union ledger and statements system

> Time box: 45 minutes, solo, no notes. Then self-score against the rubric (≈ 15 min), then send your write-up to
> the tutor. Use the structure from [`docs/INTERVIEW_FRAMEWORK.md`](../../docs/INTERVIEW_FRAMEWORK.md) and the
> numbers from [`docs/NUMBERS.md`](../../docs/NUMBERS.md). The focus is the storage layer, not the whole product: an
> API sketch is optional.

## Prompt

> "We run a digital-banking platform for about 40 credit unions. Members move money between their own accounts and to
> other institutions, see their balance and recent transactions in the mobile app, and receive monthly statements.
> Design the storage layer for the ledger and the statements: what databases you would use, the schema, the indexes, how a
> transfer is posted safely when many things happen at once, how it survives crashes, and how you would change the schema
> without taking the platform down."

That is all the interviewer says up front. Treat it like the real interview: ask the clarifying questions you need (write
them down, then write the answers you'd assume if the interviewer said "you decide"), and drive.

<!-- predict id="A2-U0-P01" -->
**Predict (before you start).** In two sentences each, without notes: (1) which single property of your ledger database would
you refuse to compromise on, and which would you trade away first if you had to? (2) What is the first number you would
want to know, and why does it change the design? Write them down, then compare with your final write-up.
<!-- /predict -->

<details data-kind="reveal" data-for="A2-U0-P01">
<summary>Reveal</summary>

There is no single right answer; strong candidates usually (1) refuse to compromise on *acknowledged means durable and
atomic* (a transfer either fully posts, once, or not at all) and trade away read freshness on derived views (statements,
analytics, search) first; and (2) ask for the write rate and the shape of the hottest access pattern (writes per second at
peak, and per-account contention such as payroll), because those decide whether one relational primary is enough and
whether any single row becomes a bottleneck. If your list started with "scale" rather than "correctness under concurrency",
re-read Unit 1 §1 and Unit 3 §7.

</details>

Things an interviewer would answer if asked (don't read until you've written your questions):

<details data-kind="hint">
<summary>Interviewer's answers to common clarifying questions</summary>

- ≈ 1.2 M members, ≈ 2.5 M accounts across 40 credit unions; ≈ 5 M ledger entries a day on average (transfers post two
  entries each, plus card, ACH and fee postings); peak ≈ 3× average; payroll mornings spike ≈ 10× for ≈ 30 minutes.
- Reads at peak: ≈ 600 balance lookups/s, ≈ 400 "last 50 transactions" requests/s, ≈ 5 audit lookups/s by transfer ID.
- Month-end: a statement for every account within a 6-hour window. Fraud/AML run hourly ad-hoc queries across all credit
  unions over the last 24 hours (and occasionally 90 days).
- Retention: 7 years online (regulatory); entries are never modified or deleted, corrections are new entries.
- Money rules: no negative balance except within an overdraft limit; some members have a *shared* overdraft limit across
  checking and savings; a transfer between two of a member's accounts is atomic; all transfers are idempotent (a client
  retry must never post twice).
- Durability: an acknowledged transfer must never be lost (RPO 0 within the region); RTO ≈ 15 minutes for a primary
  failure; a cross-region copy for disasters may lag by seconds.
- The platform's ledger is authoritative for digital transfers and is reconciled with the credit union's core banking system
  nightly; the core's real-time API is slow (p99 ≈ 9 s) and is not on the posting path.
- Deploys are weekly and rolling; the schema changes in most releases; downtime is not acceptable for schema changes.
- Hot spots: payroll accounts with tens of thousands of entries a month, and a few fee/clearing accounts touched by many
  transfers.
- Tenancy: credit unions must never see each other's data; per-tenant time zones; some regulators ask for per-tenant exports.

</details>

## What to produce

1. **Requirements**: functional bullets and non-functional (latency, durability, consistency, availability, retention,
   tenancy), with your clarifying questions and assumptions.
2. **Access-pattern table**: each query with peak rate, latency target and the consistency it needs (as in Unit 1 §1).
3. **Estimate sheet**: rows/day and per year, bytes/year including indexes, index sizes of the key indexes (use
   rows × entry size ÷ fill), memory you want for the cache, commits/s versus fsync latency, hot-row ceiling for the
   worst account. Show the arithmetic.
4. **Engine choices**: which store holds which data (ledger, balances, idempotency keys, statements, fraud/analytics,
   audit exports) and why, with the amplification or consistency argument (B-tree versus LSM, relational versus key-value).
5. **Schema**: tables with keys, types (money, time), constraints, tenant handling, ID strategy, and partitioning/retention.
6. **Indexes**: each one tied to a named query and rate; which are composite/covering/partial; the write cost you accept.
7. **Transaction design for "post a transfer"**: what is in one transaction, isolation level, how you prevent lost update
   and the shared-overdraft write skew, idempotency, lock ordering, retries.
8. **Durability and recovery**: commit settings, replication and failover, what is lost and what is not, backup and
   point-in-time recovery, the crash-recovery story.
9. **Migration plan**: add a `currency` column and a new covering index to the live ledger (hundreds of millions of rows)
   with weekly rolling deploys: the steps, the locks, the backfill, the rollback.
10. **Trade-off table** (at least three decisions, with "wins when / loses when / when the answer flips") and a
    **failure walkthrough**: payroll morning on one hot account; a month-end statement job running for hours; the primary's
    storage stalls for 30 seconds.

## Rubric

| Dimension | below | approaching | meets (senior bar) | exceeds |
|---|---|---|---|---|
| Requirements & access patterns | Starts with product names; no query list | Lists entities and a few queries; no rates | Query table with peak rates, latency and consistency per query; separates OLTP from reporting; states assumptions | Finds the two or three patterns that decide the design (payroll hot accounts, month-end burst) early and says what would change if they were 10× |
| Estimation | None, or off by orders of magnitude | Rows/day only | Rows, bytes (with indexes), index sizes, memory, commits/s vs fsync, hot-row ceiling; arithmetic shown; checks against single-node limits | Uses the numbers to make decisions (partition by month, when to shard, which index to drop); notes WAL volume and replica lag |
| Data model & engine choice | One store for everything, or NoSQL "for scale" with no argument | Relational ledger; others chosen by habit | Relational ledger justified by multi-row atomicity and ad-hoc queries; derived data (statements, analytics) separated with the sync mechanism named; B-tree vs LSM argued with amplification | Explains where the choice flips (rate, retention, tenancy) and what would move to an LSM or analytics store first; names the cost of each extra store |
| Schema & integrity | Floats for money; no constraints | Sensible tables; weak keys/constraints | Integer minor units + currency; append-only ledger; constraints (NOT NULL, CHECK, UNIQUE idempotency, FKs incl. tenant); ID strategy with index-locality reasoning; business date vs timestamp; partitioning/retention | Double-entry invariant enforced and reconciled; composite tenant FKs; partial unique constraints; per-tenant statement boundaries |
| Indexing | "Index everything" or none | Single-column indexes per column | Each index tied to a query; composite order by equality-sort-range; covering/partial where it pays; write cost and size stated; redundant prefixes removed | Reads EXPLAIN-style reasoning (rows examined vs returned); index-only scan caveats; plans index maintenance (concurrent builds, bloat) |
| Transactions & isolation | "Use transactions" with no level | Names a level; doesn't connect to anomalies | Identifies lost update and shared-limit write skew; picks mechanisms (conditional UPDATE, ordered FOR UPDATE, guard row or SERIALIZABLE) with their costs; idempotency in the same transaction; retries; lock ordering | Quantifies contention (hot-row ceiling, retry rates); designs the hot-account mitigation (append-only entries + rollup, sharded counter); distinguishes defaults across engines |
| Durability & recovery | Not addressed, or "it's in the database" | fsync/replication mentioned without trade-offs | Synchronous local flush with group commit; failover and RPO/RTO stated; what async replication can lose; backup + PITR; crash-recovery story (WAL redo, torn tail) | Quantifies commit latency and throughput; handles fsync failure policy; tests recovery (game days); cross-region lag vs RPO |
| Schema evolution | "Run ALTER in a maintenance window" | Mentions expand/contract abstractly | Concrete steps for the column and the index: lock levels, `lock_timeout`, concurrent index build, throttled keyset backfill, verification, separate contract step, rollback; rolling-deploy compatibility | Estimates backfill time and WAL/replica impact; per-tenant rollout and flags; linting/dry runs on prod-size copy |
| Failure modes & operations | Boxes without failure thinking | Plausible failures, no mitigations | The three walkthroughs answered concretely (hot row, long snapshot/bloat, storage stall) with metrics and mitigations; alerts for deadlocks, serialization failures, lock waits, bloat, replica lag | Capacity headroom and N+1, restore drills, index/vacuum maintenance plan, cost per option |
| Trade-offs & communication | Single answer per decision | Trade-offs only when prompted | Trade-off table with "when the answer flips"; drives the 45 minutes; checks in; states what is deferred | Anticipates follow-ups; quantifies cost/latency/ops trade-offs; names what you would measure before committing |

## Tutor critique protocol

1. Read the write-up once fully before scoring. Score each dimension independently using the rubric's words; quote the
   learner's own text as evidence for each score.
2. Check the arithmetic in the estimate sheet. An estimate that is off by > 10× on a number the design depends on (rows/year,
   index size, hot-row ceiling, backfill duration) caps *Estimation* at "approaching".
3. Ask these follow-up probes (all of them; the learner answers in writing or live, without notes). Score the answers
   into the relevant dimensions:
   - "Payroll morning: 50,000 credits land on one employer clearing account in 10 minutes, each transaction holding that
     row's lock for ≈ 3 ms. What is the ceiling, what happens to everything else, and what do you change?"
   - "Two $700 withdrawals hit an account with $300 and a $500 overdraft limit at the same instant, under READ COMMITTED.
     Walk me through exactly what each statement sees. Then show me the fix that needs no retry."
   - "A member has a shared overdraft limit across checking and savings. Two withdrawals hit different accounts at the
     same time. Why doesn't snapshot isolation stop it, and what do you do?"
   - "The primary loses power after a transfer returned success. What do you recover, from where, and in what order? What
     if the storage lies about fsync?"
   - "The month-end statement job opens a transaction at 00:05 and runs until 05:30. What does it do to the primary,
     why, and how do you run it instead?"
   - "You need to add an index to the 2-billion-row ledger and widen a column from int to bigint. Walk me through the lock
     each step takes and what an `ALTER` waiting behind a long query does to the application."
   - "Ingest grows 10×. What breaks first (WAL, index maintenance, replica lag, disk), and at what point do you move the audit
     stream to an LSM-based store or partition the ledger? What would you measure?"
   - One probe of the tutor's choice on the learner's weakest dimension.
4. Overall level:
   - **meets** requires "meets" or better on *Transactions & isolation* and *Durability & recovery* (the two dimensions where
     a mistake loses or duplicates money) and *Data model & engine choice*, and no dimension at "below".
   - **exceeds** requires "meets" on every dimension and "exceeds" on at least three.
   - Otherwise **approaching** (or **below** if two or more dimensions are "below").
5. Feedback: three bullets — the strongest part, the one change that would most raise the score, and one concrete thing to
   re-read (link the section, e.g. [`unit-3-transactions-schema.md`](unit-3-transactions-schema.md) §8). If the result is
   below "meets", the learner redoes the exercise on a different day (variation: "design the storage layer for the
   idempotency-key store and audit log of the partner transfers API", same rubric).

## Recording

`sd record design A2 --self <below|approaching|meets|exceeds> --tutor <level> --notes "<one-line summary>"`
