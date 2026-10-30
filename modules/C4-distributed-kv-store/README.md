# C4 — Case study: distributed key-value store

| | |
|---|---|
| Track | C — HLD case studies |
| Estimated time | 9 h (2 units: 3.5 + 5.5) — CORE 7 h, STRETCH 2 h |
| Prerequisites | A4, A5 (gates passed) |
| Labs | `labs/c4/` — `antientropy` (Merkle-tree repair, tombstones, safe purge; CORE), `membership` (gossip heartbeats, generations, phi accrual; STRETCH) |
| Gate | quiz ≥ 85% ×2 (different days) · labs pass · design ≥ meets · teach-back pass |

## Why this module exists

A team builds a key-value store for carts and sessions on a consistent-hash ring with three replicas, and it works.
Then a rolling restart overlaps a disk failure and a slice of cart writes fails, because the quorum is strict. They
make it sloppy; a node is down longer than the hint window and comes back missing hours of writes that nobody
notices until reads at `ONE` serve week-old carts. They add a weekly repair; a node returns from a twelve-day
hardware swap and members' deleted records reappear in the nightly export, because the healthy replicas had
already purged the tombstones. Somewhere in between, a GC pause convicts a healthy node on all 79 peers at once, an
automation script starts streaming half a terabyte to replace it, and a product launch puts one key at 150,000
reads a second on three machines. Every one of those incidents came from a mechanism in the Dynamo paper that the
team had implemented without knowing what it was for.

This is the case study where A4 and A5 become one system. The numbers say something surprising first: at ≈ 1M
operations a second, the ≈ 2.3M *replica* operations, not the 40 TB, decide that the cluster has ≈ 80 nodes, which
makes each node small, which makes rebuilds fast. From there the design is a chain of failure-driven decisions:
partitioning because one node can't hold it, replication because nodes die, versions because replicas disagree,
repair because some keys are never read, tombstones because deletes must survive repair, gossip because there is
no master, and consensus for the few operations that need exactly one winner. This module has you derive each one
before the reveal, and build the two pieces A5's labs left out: background repair with deletes that stick, and
membership without a coordinator.

## Outcomes

- **Scope** a distributed key-value store prompt in ≤ 6 minutes: core operations, NFRs with numbers, out-of-scope
  list, and the clarifying questions (consistency per operation, partition behaviour, concurrent-write semantics,
  scans, regions) whose answers pick between the leaderless and the consensus families. — CORE
- **Estimate** storage with replication and LSM overhead, replica operations (`reads × R + writes × N`), nodes by
  operations vs by disk, replication and cross-AZ bandwidth, hint volume and rebuild time, and **state** the decision
  each drives. — CORE
- **Design** the API (consistency level per request, opaque causal context, siblings, `Unavailable` vs `Timeout`,
  tombstone deletes) and the per-node data model (LSM sorted by token, versions, tombstones, TTL). — CORE
- **Build** the high-level design step by step: hash ring with virtual nodes, preference lists over distinct nodes
  and AZs, any-node coordination with token-aware clients, read and write paths at `QUORUM` with rates, and the
  background processes each requirement forces. — CORE
- **Choose** `N`, `R`, `W` and strict vs sloppy quorums per operation, **explain** what `R + W > N` does and does not
  guarantee, and **size** and bound hinted handoff. — CORE
- **Choose** conflict resolution per data type (last-write-wins, version vectors with siblings, CRDTs such as
  PN-counters and OR-Sets, or prevention by consensus) and **explain** how each alternative fails. — CORE
- **Implement** Merkle-tree anti-entropy that ships only the divergent leaves, deterministic merging with
  tombstones, and tombstone garbage collection with a safe cutoff, and **demonstrate** zombie resurrection
  (`labs/c4/antientropy`). — CORE
- **Explain** gossip membership and failure detection (heartbeat generations, suspicion, phi accrual), why
  conviction reroutes while removal streams, how nodes join and leave, and how to handle hot keys. — CORE
- **Decide** when the answer flips to consensus (per-key Paxos vs Raft groups per range vs a separate CP store) for
  compare-and-set, with latency and partition behaviour stated. — CORE
- **Implement** gossip membership with generation-aware heartbeats, suspect and dead states, dead-member probing
  and a phi-accrual detector (`labs/c4/membership`), and **reason** about multi-region quorums (`LOCAL_QUORUM` vs
  `EACH_QUORUM`). — STRETCH

## Unit plan

| Unit | Topic | Hours | Core/Stretch | Lab |
|---|---|---|---|---|
| 1 | [Guided design: requirements, estimation, API and data model, from one node to a ring](unit-1-guided-design.md) | 3.5 | CORE (3) / STRETCH (0.5) | — (the estimate sheet and the HLD are the work; A4/A5 labs revisited) |
| 2 | [Deep dives: quorums under failure, conflicts, anti-entropy and deletes, membership and the CP flip](unit-2-deep-dives.md) | 5.5 | CORE (4, incl. ≈ 1.5 h gate work) / STRETCH (1.5) | `labs/c4/antientropy` (CORE), `labs/c4/membership` (STRETCH) |

**Unit 1 (3.5 h)**
- Session 1 (1.5 h): warm-up; §1 requirements and the questions that pick the family (write your card before the
  reveal); §2 the estimate sheet by hand, then `sd estimate -n 5`.
- Session 2 (1.5 h): §3 API and data model (commit to the context and the tombstone before the reveal); §4 the
  five-step HLD, ending with your own one-page design with rates on the arrows. STRETCH (0.5 h): the multi-region
  notes in §1 and §4.

**Unit 2 (5.5 h)**
- Session 1 (2 h): warm-up; §5 quorums under failure (re-run A5's quorum break-it with this case study's numbers);
  §6 conflicts (re-run A5's clocks break-it; write the PN-counter).
- Session 2 (2 h): §7 anti-entropy and deletes, then `labs/c4/antientropy` in the README's order and its two
  break-its (depth sweep, zombie timeline).
- Session 3 (1.5 h, includes gate work): §8 (CORE parts) and Common mistakes; first `sd quiz C4` attempt; the
  [design exercise](design-exercise.md) (45 min, solo) and the [teach-back](teachback.md).
- STRETCH (1.5 h, any time): `labs/c4/membership` and its break-its, the phi-accrual part of §8, the multi-region
  notes in §5.
- Second `sd quiz C4` attempt on a later day.

## How this connects to your banking work

- **Idempotency keys are a uniqueness claim.** The key that stops a retried vendor callback from posting a payment
  twice is a create-if-absent with exactly one winner, which no quorum setting provides. §8's per-key consensus path
  is where it belongs, and the [design exercise](design-exercise.md) makes you argue it next to data that must stay
  writable.
- **Sessions and device bindings are not the same data.** An SSO session that a phone touches every few seconds can
  lose a concurrent touch (last-write-wins) but must not reject one during an AZ partition (a rejected write is a
  logout). A device-binding record must survive any single failure. One store, different `R`/`W` and conflict
  policies per keyspace: §5 and §6.
- **Deletes with a deadline.** Account closures and erasure requests must be gone from every copy within a bound you
  can prove. §7's tombstone rules (grace period vs outage plus repair interval, purge only after a full repair,
  rebuild-don't-rejoin) are the controls an examiner would ask about, and the zombie in the lab is the incident they
  are written to prevent.
- **Vendor timeouts, one layer down.** `Timeout` on a write means "sent, outcome unknown", exactly like a payment
  vendor that took your request and dropped the connection. The only safe retry is one the other side recognises
  as the same operation: the same value with the same context here, the same idempotency key there.
- **Multi-tenant hot keys.** One credit union's statement run or a platform-wide config key behaves like the
  launch-page key in §8: more nodes don't help a key that lives on three machines; caching and key splitting do.

## Mastery gate checklist

- [ ] `sd quiz C4` ≥ 85% — attempt 1 (date: ____)
- [ ] `sd quiz C4` ≥ 85% — attempt 2 on a different day (date: ____)
- [ ] `sd lab C4` — all lab tests pass (with `-race`)
- [ ] Design exercise ([`design-exercise.md`](design-exercise.md)): self-score recorded, tutor score ≥ "meets"
      (`sd record design C4 --self <lvl> --tutor <lvl> --notes "..."`)
- [ ] Teach-back ([`teachback.md`](teachback.md)): tutor marks pass (`sd record teachback C4 --result pass --notes "..."`)

`sd status` is the source of truth; this list is for your own reading.

## What's next

C4 is a prerequisite of the full senior loop (D4), and every later case study that stores state leans on it:
C6 (chat) uses its ordering and delivery-receipt ideas, C8 (ticket booking) and C9 (payments and ledger) live on
the consensus side of §8's line, and E2 builds a replicated key-value store for real (WAL, leader/follower, lag,
failover). Next in the recommended order are A6 (async), A7 (reliability) and C2 (distributed rate limiter, whose
shared counters are §6's CRDT question under a latency budget). Before moving on, run `sd review` and clear any C4
questions it surfaces, especially the tags `quorum`, `conflict-resolution`, `tombstones`, `anti-entropy` and
`estimation`.
