# A4 — Replication, partitioning, hot keys, consistent hashing

| | |
|---|---|
| Track | A — HLD fundamentals |
| Estimated time | 18 h (3 weekly units) — CORE 15 h, STRETCH 3 h |
| Prerequisites | A2 (gate passed) |
| Labs | `labs/a4/` — `replkv`, `ring` (core); `hotkey` (stretch) |
| Gate | quiz ≥ 85% ×2 (different days) · labs pass · design ≥ meets · teach-back pass |

## Why this module exists

The transactions store of a credit-union platform runs on one Postgres primary. It handles payday (≈ 1,400 writes/s, ≈ 90,000
reads/s for the first five minutes) until the day it doesn't. A read replica is added; now a member transfers $400, taps back to the
balances screen, and sees the old numbers because the replica is 3 seconds behind. They transfer again. A month later the primary's host
dies and the promoted follower is 1.3 s behind: ≈ 1,800 acknowledged postings exist nowhere. Someone sets replication to
synchronous, and the next kernel patch on the follower freezes every write on a healthy primary.

Then the data outgrows one machine, and the team shards it with `hash(id) mod 8`. Adding the ninth shard remaps ≈ 89% of the rows.
The cache fleet autoscales and does the same to its hit rate. A payroll window sends 1,500 updates/s to one general-ledger row, and adding
partitions changes nothing, because one key lives on one partition. The member-search feature has no partition key in its query
and asks all 27 shards, 24% of which are in their slowest 1% at any moment.

None of these is exotic. They are the standard failure modes of copies, slices and skew, and they are the second half of nearly every
senior system design interview ("what happens when the replica lags?", "how do you add a shard?", "what about the celebrity account?").

## Outcomes

- **Explain** leader/follower log shipping from first principles (log positions, in-order apply, bootstrapping) and **choose** between physical, logical and
  statement-based replication. — CORE
- **Choose** between asynchronous, semi-synchronous and synchronous replication given RPO, latency and availability targets, compute the RPO of
  async from lag and write rate, and **describe** failover (detection, promotion, lost writes, split brain, fencing). — CORE
- **Implement** a replicated KV store with a replication log, in-order followers, synchronous waiting and failover that promotes the most
  up-to-date follower and reports lost writes, on a deterministic lag simulator (`labs/a4/replkv`). — CORE
- **Diagnose** read-your-writes, monotonic-reads and consistent-prefix violations from replica lag, and **implement and defend** the fixes
  (leader windows, LSN tokens, floors, sticky sessions) with their costs (`labs/a4/replkv`). — CORE
- **Explain** multi-leader and leaderless replication, why last-write-wins loses data, how CRDT-style merges and home-region
  routing avoid conflicts, and why cross-record invariants need coordination. — CORE
- **Choose** a partitioning scheme (range, hash, directory, composite) from the access pattern, and **quantify** the cost of `mod N`
  resharding versus consistent hashing. — CORE
- **Implement** a consistent-hash ring with virtual nodes, weights, N-replica lookup on distinct nodes, movement and balance
  statistics, bounded loads, and rendezvous/jump-hash alternatives (`labs/a4/ring`). — CORE
- **Plan and operate** rebalancing: fixed partitions vs dynamic splitting vs tokens, live migration with a short cutover, request routing with
  epochs and redirects, and skew monitoring. — CORE
- **Diagnose** hot keys and celebrity problems, **choose** among caching, key splitting, write coalescing and data-model changes, and **detect** hot keys with
  heavy-hitter summaries. — CORE
- **Choose** between local and global secondary indexes, **compute** scatter-gather tail latency (1 − 0.99^N), and **outline** cross-partition
  query and transaction options. — CORE
- **Size** a sharded, replicated deployment from estimates (partitions, replication factor, failure domains, rebalancing headroom) and **defend** it
  under an AZ loss at peak. — CORE
- **Implement** a Space-Saving / Count-Min hot-key detector and a key-splitting router (`labs/a4/hotkey`). — STRETCH
- **Demonstrate** last-write-wins data loss and CRDT merging, and read the leaderless mechanisms (hinted handoff, read repair, anti-entropy). — STRETCH

## Unit plan

| Unit | Topic | Hours | Core/Stretch | Lab |
|---|---|---|---|---|
| 1 | [Replication: log shipping, sync vs async, failover, lag anomalies, multi-leader and leaderless](unit-1-replication.md) | 6 | CORE (5) / STRETCH (1) | `labs/a4/replkv` |
| 2 | [Partitioning and consistent hashing: schemes, the ring, virtual nodes, rebalancing and routing](unit-2-partitioning.md) | 6 | CORE (5) / STRETCH (1) | `labs/a4/ring` |
| 3 | [Hot keys, secondary indexes and putting it together: skew, scatter-gather, sizing, failure domains](unit-3-hot-keys-indexes-sizing.md) | 6 | CORE (5, incl. ≈ 1.5 h gate work) / STRETCH (1) | `labs/a4/hotkey` (stretch) |

**Unit 1 (6 h)**
- Session 1 (2 h): warm-up; §1 log shipping (run the statement-vs-row experiment) and §2 sync/async/failover — predict boxes; start `labs/a4/replkv`
  (`node.apply`, the async write path, `Tick`, `GetAtLeast`).
- Session 2 (2 h): finish `replkv` — synchronous waiting, `Crash`/`Failover`/`Recover`; run the sync-vs-async break-it; §3 predict box.
- Session 3 (2 h): §3 lag anomalies — `Session` (read-your-writes, monotonic reads, tokens), run the anomaly-rates break-it; §4 concepts and predict box;
  Check-yourself questions; `sd review`. STRETCH (1 h, any time): the LWW-vs-CRDT experiment and the leaderless mechanisms in §4.

**Unit 2 (6 h)**
- Session 1 (2 h): warm-up; §5 partitioning schemes (run the hot-tail and modulo experiment); §6 the ring, predict box.
- Session 2 (2 h): `labs/a4/ring` parts 1–3 (`Add`/`Remove`/`Lookup`/`LookupN`, statistics); the concurrency test under `-race`.
- Session 3 (2 h): ring parts 4–5 (`AssignBounded`, `Rendezvous`, `JumpHash`), the four break-it tables; §7 rebalancing and routing, run the planner
  program. STRETCH (1 h): the papers and the Redis Cluster / Vitess migration internals named in §6–§7.

**Unit 3 (6 h)**
- Session 1 (1.5 h): warm-up; §8 skew and hot keys — predict box, remedies, detectors. STRETCH (1 h, any time): `labs/a4/hotkey` and its break-it.
- Session 2 (2 h): §9 secondary indexes and scatter-gather (run the tail-latency simulation); §10 sizing and the cheat-sheet (run the calculator);
  `sd lab A4`; first `sd quiz A4` attempt.
- Session 3 (1.5 h, gate): design exercise (45 min + self-score), teach-back write-up.
- Second `sd quiz A4` attempt on a later day.

## How this connects to your banking work

- **The balance screen after a transfer** is read-your-writes (Unit 1 §3). Your app already talks to a primary and (if you use them) replicas;
  the LSN-token router is the design you would pitch for "member sees stale balance after moving money", and the idempotency keys from A1
  are what stop the second, panicked tap from double-posting.
- **Core banking as the system of record**: your platform's transactions store is a replica of facts the core owns. A failover that loses
  1,800 postings (Unit 1 §2) is a reconciliation event against the core, which is why RPO per table matters and why adapters should be
  idempotent and re-syncable.
- **Forty uneven tenants** are the textbook directory-partitioning problem (Unit 2 §5, Unit 3 §10): a tenant → shard map, big
  credit unions on dedicated shards, `(tenant, account)` as the partition key, and tenant-scoped scatter-gather so a member search touches 2
  partitions instead of 27.
- **GL and settlement accounts** are the hot rows of banking (Unit 3 §8): the append-only ledger design (C9) exists to avoid exactly this.
- **Statements by account and date range** are the composite-key query (hash by account, order by `posted_at`); member search by name and last-4 is
  the secondary-index problem (Unit 3 §9) with a PII angle (A9).
- **Payday** is the peak you size for (Unit 3 §10): 3× writes, a lag spike on every replica, and no appetite for rebalancing; the capacity
  headroom and change freezes in Units 2–3 are about that one morning.

## Mastery gate checklist

- [ ] `sd quiz A4` ≥ 85% — attempt 1 (date: ____)
- [ ] `sd quiz A4` ≥ 85% — attempt 2 on a different day (date: ____)
- [ ] `sd lab A4` — all lab tests pass (with `-race`)
- [ ] Design exercise ([`design-exercise.md`](design-exercise.md)): self-score recorded, tutor score ≥ "meets"
      (`sd record design A4 --self <lvl> --tutor <lvl> --notes "..."`)
- [ ] Teach-back ([`teachback.md`](teachback.md)): tutor marks pass (`sd record teachback A4 --result pass --notes "..."`)

`sd status` is the source of truth; this list is for your own reading. The `hotkey` lab is STRETCH: `sd lab A4` reports it separately and
it does not block the gate.

## What's next

Passing A4 unlocks A5 (consistency and distributed basics), which gives the theory under the informal arguments here (quorums, clocks,
ordering, consensus, leases and fencing tokens), and contributes to A6 (queues, logs, outbox, sagas: the async machinery for index maintenance
and cross-partition work). The case studies that lean on this module are C3, C4, C5, C10 and C12. Before moving on, run `sd review` and clear any A4
questions it surfaces, especially tags `lag`, `failover`, `consistent-hashing` and `hot-key`; they come back in nearly every later design.
