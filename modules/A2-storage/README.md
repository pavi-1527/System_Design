# A2 — Storage: SQL vs NoSQL, indexes, B-tree vs LSM, transactions, schema design

| | |
|---|---|
| Track | A — HLD fundamentals |
| Estimated time | 18 h (3 weekly units) — CORE 15 h, STRETCH 3 h |
| Prerequisites | A0 (gate passed) |
| Labs | `labs/a2/` — `btree`, `lsm`, `isolation` (core); `wal` (stretch) |
| Gate | quiz ≥ 85% ×2 (different days) · labs pass · design ≥ meets · teach-back pass |

## Why this module exists

A credit-union platform keeps its ledger in a relational database. For two years that is invisible: a few hundred writes
a second, a few thousand reads, a handful of indexes added whenever someone filed a ticket. Then four things happen in
one quarter. The transfers table's random-UUID primary key outgrows memory and inserts slow from 2 ms to 200 ms with no
code change. A payroll morning produces a few hundred double-spent overdraft limits, because two concurrent withdrawals
both read a sufficient balance and both wrote: the database said "committed" twice, honestly. An engineer adds a column
with a "10 ms" `ALTER TABLE` at 14:00; a three-minute reporting query was already running, and the platform is down for
exactly three minutes. And the event stream moved to a different store for speed, which now wears out its SSDs
three times faster than anyone budgeted.

None of these is exotic. Each one is the database doing precisely what its design says, in a situation the design was
not chosen for. This module is about *why databases are shaped the way they are*: why an index is a shallow tree of
pages, why durability costs an `fsync` and how group commit hides it, why a log-structured engine writes sequentially and
pays for it in compaction, and why "READ COMMITTED" does not mean what you hoped. You will build the index, the
LSM tree and a snapshot-isolation store, watch each anomaly happen in a test, and then prevent it.

## Outcomes

- **Choose** a data model (relational, key-value, wide-column, document) from a written list of access patterns with
  rates, latency targets and consistency needs, and defend it with numbers. — CORE
- **Explain** how a B+tree index works (pages, fan-out, splits, leaf chain) and **compute** its height, size and page
  reads from page and entry sizes. — CORE
- **Implement** a B+tree with page splits, leaf-chained range scans, lazy deletes and an invariant checker, and
  **measure** fill factor and buffer-pool behaviour for sequential versus random keys (`labs/a2/btree`). — CORE
- **Design** composite, covering and partial indexes for a query (equality, then sort, then range; leftmost prefix),
  **count** their write cost and **read** an EXPLAIN plan. — CORE
- **Explain** the write-ahead rule, the cost of `fsync`, group commit, torn pages and crash recovery, and say what each
  durability setting lets you lose. — CORE
- **Implement** an LSM tree with a memtable, immutable sorted tables, bloom filters, tiered compaction and tombstones, and
  **measure** write, read and space amplification (`labs/a2/lsm`). — CORE
- **Choose** between a B-tree and an LSM engine for a workload using amplification arithmetic and SSD-endurance
  numbers, and **explain when the answer flips**. — CORE
- **Implement** a write-ahead log with checksummed records, torn-tail truncation, mid-log corruption detection and group
  commit against a crash-simulating file (`labs/a2/wal`). — STRETCH
- **Name** the isolation anomalies (dirty read, non-repeatable read, phantom, lost update, write skew), **say which level
  and which technique** prevents each, and **explain why** READ COMMITTED double-spends. — CORE
- **Implement** an MVCC store with read committed, snapshot isolation with first-committer-wins and optimistic
  serializable validation, and **demonstrate** lost update, write skew and phantoms in tests (`labs/a2/isolation`). — CORE
- **Plan** zero-downtime schema changes with expand/migrate/contract, throttled keyset backfills, online index builds and
  lock timeouts, and **explain** why a fast `ALTER` can cause an outage. — CORE
- **Design** the storage layer for a ledger and statements system: engines, schema, indexes, transaction and isolation
  choices, durability, migration plan and capacity numbers (the design exercise). — CORE

## Unit plan

| Unit | Topic | Hours | Core/Stretch | Lab |
|---|---|---|---|---|
| 1 | [Access patterns, schema and indexes: data models, how a B-tree index works, composite/covering/partial indexes](unit-1-access-patterns-indexes.md) | 6 | CORE (5) / STRETCH (1) | `labs/a2/btree` |
| 2 | [Write paths: the write-ahead log, LSM trees, the B-tree versus LSM decision](unit-2-write-paths.md) | 6 | CORE (5) / STRETCH (1) | `labs/a2/lsm`, `labs/a2/wal` (stretch) |
| 3 | [Transactions, isolation and schema evolution: anomalies, MVCC and locks, zero-downtime migrations](unit-3-transactions-schema.md) | 6 | CORE (5, incl. ≈ 1.5 h gate work) / STRETCH (1) | `labs/a2/isolation` |

**Unit 1 (6 h)**
- Session 1 (2 h): warm-up; §1 data models: write the access-pattern table for the ledger before opening the reveal,
  then the capacity arithmetic in the break-it.
- Session 2 (2 h): §2 how a B-tree works: predict, then implement `get`, `put` (with splits) and `scan` in
  `labs/a2/btree`; run the structural tests with `Check`.
- Session 3 (2 h): finish `del`, the concurrent test and the buffer-pool break-it; §3 composite/covering/partial
  indexes and the scratch composite-key experiment. STRETCH (1 h, any time): the DynamoDB-style and wide-column modelling
  digression in §1, partial/expression indexes and the distributed secondary-index discussion in §3.

**Unit 2 (6 h)**
- Session 1 (2 h): §4 durability and the WAL: predict, the commit-path sequence, torn tail versus corruption. Read the
  `wal` lab README now and decide whether to do it this week (STRETCH).
- Session 2 (2 h): §5 LSM trees: implement `NewBloom`, `Merge`, `Table.Lookup` and the DB read/flush path in `labs/a2/lsm`.
- Session 3 (2 h): compaction and tombstones; the amplification break-it; §6 B-tree versus LSM with the SSD-wear
  arithmetic. STRETCH (1 h): `labs/a2/wal` (record format, `Scan`, `Open`, group commit) and the compaction-strategy table.

**Unit 3 (6 h)**
- Session 1 (2 h): §7 ACID and the anomalies: predict the matrix, then implement visibility, read committed and snapshot
  reads in `labs/a2/isolation`; run the lost-update demonstration.
- Session 2 (2 h): §8 MVCC and locks: first-committer-wins, `Scan`, `Serializable`, `Vacuum`; the concurrent bank-transfer
  tests under `-race`; the break-it.
- Session 3 (1 h): §9 schema design and migrations: the expand/contract plans, the lock-queue break-it.
- Session 4 (1 h, gate): design exercise (45 min + self-score), teach-back write-up. STRETCH (1 h): the SSI details in
  §8, online index-build internals in §9. Run `sd lab A2`, then your first `sd quiz A2` attempt; the second on a later day.

## How this connects to your banking work

- **Idempotency stores and duplicate vendor callbacks.** The idempotency key from A1 is only safe if the reservation is one
  atomic conditional write (a unique constraint, Unit 3 §7–§8) in the *same transaction* as the effect, and durable
  before you answer (Unit 2 §4). The "check then insert" version you may have seen in an adapter is the phantom/write-skew
  anomaly.
- **Reconciliation breaks.** The lost update in Unit 3 §7 is the textbook cause of a small number of mismatched balances
  against the core every morning. The one-statement conditional `UPDATE` and the guard-row pattern are the fixes you
  can ask for in a code review tomorrow.
- **Multi-tenant credit unions.** `tenant_id` as the leading index column, composite foreign keys that make cross-tenant
  references impossible, per-tenant time zones for statement boundaries, and the migration tax of schema-per-tenant
  (40 migrations, partial failure states) are all concrete decisions from Units 1 and 3.
- **Audit trails.** Append-only ledgers (corrections are new entries) are both the compliance answer and the
  concurrency answer: inserts do not conflict. The same property is why LSM-style stores suit event and audit streams (Unit 2).
- **Statements and reports.** The month-end statement job is a long read: run it on a replica at snapshot isolation, never
  as a multi-hour transaction on the primary (Unit 3 §8: snapshots pin old versions and bloat tables).
- **Weekly deploys.** Rolling deploys mean two code versions against one schema; every migration in Unit 3 §9 is shaped
  by that, and the lock-queue outage is the reason your DBA asks for a `lock_timeout`.

## Mastery gate checklist

- [ ] `sd quiz A2` ≥ 85% — attempt 1 (date: ____)
- [ ] `sd quiz A2` ≥ 85% — attempt 2 on a different day (date: ____)
- [ ] `sd lab A2` — all lab tests pass (with `-race`)
- [ ] Design exercise ([`design-exercise.md`](design-exercise.md)): self-score recorded, tutor score ≥ "meets"
      (`sd record design A2 --self <lvl> --tutor <lvl> --notes "..."`)
- [ ] Teach-back ([`teachback.md`](teachback.md)): tutor marks pass (`sd record teachback A2 --result pass --notes "..."`)

`sd status` is the source of truth; this list is for your own reading. The `wal` lab is STRETCH: `sd lab A2` reports it
separately and it does not block the gate.

## What's next

Passing A2 unlocks **A3 (caching)**, which builds on what you now know about the source of truth (what "committed"
means, how isolation orders concurrent writers, what an index costs), and **A4 (replication and
partitioning)**, which takes the single-node engine from this module and spreads it over machines: leader/follower logs
are Unit 2's WAL shipped over a network, sharding is the answer to "one primary is not enough", and secondary indexes across
partitions are the local-versus-global trade-off sketched in Unit 1 §3. **A6 (async)** uses transactions for the outbox
pattern. Before moving on, run `sd review` and clear any A2 questions it surfaces, especially tags `isolation`,
`indexing`, `lsm` and `wal`; they come back in almost every later design.
