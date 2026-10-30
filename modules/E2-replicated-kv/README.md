# E2 — Build: replicated KV store

| | |
|---|---|
| Track | E — Build real systems |
| Estimated time | 24 h (4 weekly units) — CORE 20 h, STRETCH 4 h |
| Prerequisites | A4, A5, B0 (gates passed) |
| Labs | `labs/e2/` — `wal` (checksummed write-ahead log with crash recovery), `rkv` (node and coordinator as real processes: replication, lag, failover, fencing, read-your-writes) |
| Gate | quiz ≥ 85% ×2 (different days) · labs pass · design write-up ≥ meets · reflection (teach-back) pass |

## Why this module exists

In A4 you replicated a log through a simulator where partitions were a boolean and a crash was a method call. In A5 you
argued about leases and fencing tokens on paper. Both were the right way to learn the ideas, and both hid the part
that hurts: real processes don't die politely. A `SIGKILL` lands between `write` and `fsync`. A follower behind a
dead link doesn't get an error, it gets silence for as long as its timeout says. A leader frozen for four seconds by a
GC pause or a suspended VM wakes up, finds a lease-renewal request sitting in its socket buffer, and believes it is
still in charge, while another node has been accepting writes for two seconds.

The numbers make it concrete. On a laptop an fsync costs ≈ 4 ms, and with eight writers queued behind it each write
takes ≈ 30 ms, so a store that acks before fsyncing is ≈ 50× faster and lies about every write in the window. An async follower 2 s behind a leader taking 200 writes/s means a
leader crash loses ≈ 400 acknowledged writes, which for a ledger means 400 members told "transfer complete" for
transfers that no longer exist. Fail over in 1 s instead of waiting for the old lease and you have two leaders for the
difference.

This module has you build the thing for real: separate OS processes on `127.0.0.1`, a WAL on disk, HTTP between
them, and a test suite that kills, partitions and freezes those processes and checks what your design promised. You
write the design rationale first, predicting what each failure will do, and then you find out.

## Outcomes

- **Implement** a write-ahead log with checksummed, length-prefixed records and **explain** the crash states recovery
  must handle (torn tail, corrupt record, missing directory entry) and why an ack must come after fsync. — CORE
- **Build** pull-based log shipping in which the follower's fetch position is its acknowledgement, and **explain** why
  pull beats push for catch-up, backpressure and failure handling. — CORE
- **Choose** between async and semi-sync replication by stating the RPO, the write latency and the availability cost
  of each, and **demonstrate** async's acknowledged-write loss on a real crash. — CORE
- **Measure** replication lag in records and seconds from the leader's side, and **explain** why a follower's
  self-reported lag lies during a partition. — CORE
- **Design** lease-based failure detection and failover: detection time, the wait for the old lease, choosing the most
  up-to-date follower, and the clock assumption leases rest on. — CORE
- **Use** epochs as fencing tokens so a returning or paused old leader can neither acknowledge writes nor corrupt
  followers, and **implement** epoch-based log matching that truncates a divergent suffix. — CORE
- **Provide** read-your-writes on followers with a version token and a bounded wait-then-redirect, and **say** what
  it does not give you (monotonic reads across replicas, linearizability). — CORE
- **Write** a design rationale that predicts each failure-injection outcome, then **compare** the predictions with
  what the tests and break-it runs observed. — CORE
- **Explain** why a single sentinel is a single point of failure for failover and how a majority-based design removes
  it; **estimate** the gain from group commit and pipelined fetches. — STRETCH

## Unit plan

| Unit | Topic | Hours | Core/Stretch | Lab |
|---|---|---|---|---|
| 1 | [A durable single node: the write-ahead log](unit-1-durable-node.md) | 5 | CORE (4.5) / STRETCH (0.5) | `labs/e2/wal`, `labs/e2/rkv` (M1) |
| 2 | [Leader/follower replication and lag](unit-2-replication-lag.md) | 6 | CORE (5.5) / STRETCH (0.5) | `labs/e2/rkv` (M2, M3) |
| 3 | [Failure injection, failover and fencing](unit-3-failover-fencing.md) | 7 | CORE (6) / STRETCH (1) | `labs/e2/rkv` (M4) |
| 4 | [Read-your-writes and the design review](unit-4-read-your-writes.md) | 6 | CORE (4) / STRETCH (2) | `labs/e2/rkv` (M5) |

Read [spec.md](spec.md) before Unit 1 and keep it open: it is the contract the tests check.

**Unit 1 (≈ 5 h)**
- Session 1 (2 h): read the spec; write sections 1–4 of the [design write-up](design-exercise.md) (≈ 45 min, before any
  code); Unit 1 §1 with predictions; `labs/e2/wal` up to `Decode`.
- Session 2 (2 h): finish `wal` (`Open`, `Append`, `TruncateAfter`); Unit 1 §2; start `rkv` M1 (`runNode`,
  `meta.json`, PUT/GET/DELETE, status).
- Session 3 (1 h): M1 green under `-race`; the break-it mutations; STRETCH §3 (fsync cost, group commit).

**Unit 2 (≈ 6 h)**
- Session 1 (2 h): §1 pull replication; implement `handleFetch` and `replicate`; `-run TestM2_Followers`.
- Session 2 (2 h): §2 async vs semi-sync; the semi-sync wait; rest of M2.
- Session 3 (2 h): §3 lag; status and M3; break-it `LagUnderLoad`; update the write-up's predictions table.

**Unit 3 (≈ 7 h)**
- Session 1 (2 h): §1 failure detection; the coordinator's tick, renewal and failover; `TestM4_Coordinator…`.
- Session 2 (2 h): §2 leases and fencing; promote/follow/lease handlers; the async-loss and zombie tests.
- Session 3 (2 h): §3 divergent logs; log matching and truncation; all of M4 green; break-it `FailoverTimeVsLease`.
- STRETCH (1 h): §4 the sentinel as a single point of failure.

**Unit 4 (≈ 6 h)**
- Session 1 (2 h): §1 read-your-writes; M5; `sd lab E2`.
- Session 2 (2 h): finish the design write-up's "predicted vs observed" section; first `sd quiz E2`; the reflection
  ([teachback.md](teachback.md)).
- STRETCH (2 h): §2 monotonic and linearizable reads, epochs in the token.
- Second `sd quiz E2` attempt on a later day.

## How this connects to your banking work

- **An ack is a promise about money.** When the core-banking adapter returns "posted", the member app shows the
  transfer as done. Unit 2 §2 is the exact question of *what has to be true on which disks* before you may say that,
  and Unit 3 §1 shows what async replication does to that promise on a crash.
- **Ambiguous outcomes need idempotency keys.** A semi-sync `503 not replicated, outcome unknown` is the same shape as a
  vendor read timeout on `POST /transfers`: the write may or may not survive. The answer is the one you already use
  for vendor callbacks: a client-supplied key and a status lookup, never a blind retry (A1 Unit 3 §2).
- **Two "primaries" is the incident you never want to explain to an auditor.** The zombie-leader test (Unit 3 §2) is
  the replicated-database version of two adapter instances both believing they hold the job lock for the nightly ACH
  file and both submitting it. Fencing tokens are the fix in both places.
- **Read-your-writes is the "I just changed my address and it shows the old one" ticket.** Unit 4's version token is
  how the member app can read from a replica and still never show a member their own stale data.

## Mastery gate checklist

- [ ] `sd quiz E2` ≥ 85% — attempt 1 (date: ____)
- [ ] `sd quiz E2` ≥ 85% — attempt 2 on a different day (date: ____)
- [ ] `sd lab E2` — `wal` and `rkv` pass (with `-race`)
- [ ] Design write-up (`design-exercise.md`): self-score recorded, tutor score ≥ "meets"
      (`sd record design E2 --self <lvl> --tutor <lvl> --notes "..."`)
- [ ] Reflection (`teachback.md`): tutor marks pass (`sd record teachback E2 --result pass --notes "..."`)

`sd status` is the source of truth; this list is for your own reading.

## What's next

E2 is in phase 6 with E3 (the async job pipeline) and D4. Nothing depends on E2's gate, but the course's definition of
done needs all three Track E builds. Before moving on, `sd review` until no E2 cards are due, and re-read the A5 Unit 3
§3 wrap-up: you have now built the lease and the fencing token it describes, so check which of its claims you could
demonstrate and which you only read. If you take on the STRETCH, C4 (distributed KV store) is the place where this
single-leader design meets partitioning and quorums.
