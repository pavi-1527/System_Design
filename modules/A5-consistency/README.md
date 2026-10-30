# A5 — Consistency and distributed-systems basics

| | |
|---|---|
| Track | A — HLD fundamentals |
| Estimated time | 18 h (3 weekly units) — CORE 14 h, STRETCH 4 h |
| Prerequisites | A4 (gate passed) |
| Labs | `labs/a5/` — `quorum`, `clocks` (core); `raftlite` (stretch) |
| Gate | quiz ≥ 85% ×2 (different days) · labs pass · design ≥ meets · teach-back pass |

## Why this module exists

A credit union's mobile app has a "Send" button, a balance, and a list of recent activity. Behind them, the platform
keeps copies: read replicas, a cache, a session store, a vendor's record of the same deposit. On a normal day the copies
agree to within a few hundred milliseconds and nobody notices. Then a member moves $2,000 and refreshes 200 ms later:
about one refresh in ten comes from a replica that has not caught up, shows the old balance, and produces ≈ 2,500
"where did my money go?" contacts a day from a system in which nothing has failed.

Turn up the pressure. A fibre cut isolates two data centres for 40 seconds and both keep accepting withdrawals:
221 of 400 accounts end up overdrawn, $22,100 to reverse. A reconciliation job whose host clock is 40 s slow writes
`reconciled` over `settled` and every replica silently keeps the older one, ≈ 6,400 times a day. A consumer credits a
deposit, crashes before acknowledging, and the "exactly-once" broker redelivers it: +$7,570 per 10,000 callbacks. A batch
runner pauses for 15 seconds, loses its lease, wakes up and posts interest a second time: ≈ $134,400. Every one of these is a
failure of *agreement* between machines, with no component misbehaving.

This module gives you the vocabulary to say exactly what a system promises (and what it doesn't), the mechanisms behind
the promises (quorums, logical clocks, majorities and leases), and the habit of asking, for each piece of state,
"what has to be true here, and what is the cheapest mechanism that guarantees it?" It is the module that turns
"eventual consistency" and "strong consistency" from slogans into design decisions, and it is the foundation for the
key-value store, chat, ticketing and payments case studies.

## Outcomes

- **State** the CAP theorem correctly (what C, A and P mean, and what it does not say), apply PACELC, and **assign** a
  consistency-versus-availability choice to each operation of a banking platform. — CORE
- **Distinguish** linearizability, serializability, sequential, causal and eventual consistency and the session
  guarantees, and **identify** which anomaly a given system or recorded history permits. — CORE
- **Choose** N, R and W for a workload, **prove** the overlap rule, and **explain** what quorums do not guarantee: sloppy
  quorums, partial writes, concurrent writes and non-linearizable reads. — CORE
- **Implement** a Dynamo-style quorum key-value coordinator with versioned values, read repair, sloppy quorums and hinted
  handoff (`labs/a5/quorum`). — CORE
- **Explain** why wall-clock last-write-wins loses data under clock skew, **quantify** the loss, and say what monotonic,
  hybrid logical and bounded-uncertainty clocks do and do not give. — CORE
- **Implement** Lamport and vector clocks, detect concurrent writes, and merge siblings (`labs/a5/clocks`). — CORE
- **Argue** that exactly-once is an end-to-end property (at-least-once delivery plus idempotent, deduplicating
  processing) and **place** the dedupe record correctly for a given kind of effect. — CORE
- **Explain** why consensus needs a majority and how Raft's terms, elections, log matching and commit rule keep committed
  entries safe. — CORE
- **Design** lease-based mutual exclusion with fencing tokens and explain why a lease alone is unsafe. — CORE
- **Choose** a consistency model and mechanism for each piece of state in a money-movement system, with numbers, and
  defend it under follow-up probes. — CORE
- **Implement** leader election and log replication as a Raft-style state machine (`labs/a5/raftlite`). — STRETCH
- **Explain** hybrid logical clocks and TrueTime-style commit wait, including what happens when one clock in an HLC
  cluster is far ahead. — STRETCH

## Unit plan

| Unit | Topic | Hours | Core/Stretch | Lab |
|---|---|---|---|---|
| 1 | [Consistency models, CAP/PACELC and quorums](unit-1-consistency-quorums.md) | 5.5 | CORE (4.5) / STRETCH (1) | `labs/a5/quorum` |
| 2 | [Time, clocks and ordering; exactly-once as an end-to-end property](unit-2-time-ordering.md) | 5.5 | CORE (4.5) / STRETCH (1) | `labs/a5/clocks` |
| 3 | [Consensus, leases, fencing and choosing a model](unit-3-consensus-leases.md) | 7 | CORE (5, incl. ≈ 1.5 h gate work) / STRETCH (2) | `labs/a5/raftlite` (stretch) |

**Unit 1 (5.5 h)**
- Session 1 (2 h): warm-up; §1 consistency models (predict, then type in and run the linearizability checker and the
  session-guarantee simulation); §2 CAP and PACELC (predict, run the partition program).
- Session 2 (2 h): §3 quorums: predict, then implement `NewCluster`, `Put`, `Get` and `Flush` in `labs/a5/quorum`; run
  the `R + W` break-it table.
- Session 3 (0.5 h): §4 reading; finish the lab (read repair, sloppy quorums, hinted handoff) and run the sloppy-quorum
  table. STRETCH (1 h, any time): anti-entropy with Merkle trees and the Aurora quorum in §4.

**Unit 2 (5.5 h)**
- Session 1 (1.5 h): warm-up; §1 physical clocks (predict, implement `LWWRegister`, run the skew break-it).
- Session 2 (2 h): §2 Lamport and vector clocks: implement the rest of `labs/a5/clocks`; run the LWW-versus-siblings
  comparison.
- Session 3 (1 h): §4 exactly-once (predict, type in and run the dedupe program, work through the "find the flaw" list).
  STRETCH (1 h, any time): §3 hybrid logical clocks and TrueTime-style commit wait.

**Unit 3 (7 h)**
- Session 1 (2 h): warm-up; §1 majorities (predict, run the majority program); §2 Raft concepts (predict, read the
  Figure 8 walk-through).
- Session 2 (1.5 h): §3 leases and fencing (predict, run the fencing program); §4 choosing a model for money movement
  (fill in the table before opening the reveal); `sd lab A5`; first `sd quiz A5` attempt.
- Session 3 (1.5 h, gate): design exercise (45 min + self-score), teach-back write-up.
- STRETCH (2 h): the `raftlite` lab, its break-its, and the Unit 2 §3 programs if you skipped them.
- Second `sd quiz A5` attempt on a later day.

## How this connects to your banking work

- **Duplicate vendor callbacks and idempotency.** Unit 2 §4 is the pipeline-scale version of what you did when a vendor
  posted "deposit accepted" twice: a stable event ID, a dedupe record committed in the *same transaction* as the credit
  (the +$2,190 vs $0 simulation), an idempotency key passed to the core, and a nightly reconciliation against the vendor's
  settlement file. The quorum lab's lost-acknowledgement test is the same idea one layer down.
- **Balances that lag.** Unit 1 §1 names the anomaly behind "I transferred money and the balance didn't change" (a
  read-your-writes violation on a replica) and the cheap fix (a version token), with the cost measured: ≈ 6.8% of reads go
  back to the primary.
- **Core banking outages and batch windows.** The platform's behaviour when the core is down or closing the day is a
  PACELC decision made per operation (Unit 1 §2): postings refuse or queue, balances serve "as of", card authorisations
  fall back to stand-in limits. Interviewers want you to say which, and what the loser sees.
- **SSO and sessions.** A profile edit followed by an assertion that still carries the old e-mail address is
  read-your-writes across two services; the 40-second-slow verifier from A1's JWT story is Unit 2 §1.
- **Audit trails.** Ordering audit events by each service's wall clock mis-orders ≈ 5.5% of causally related pairs at 50 ms
  of skew (Unit 2 §2). The senior answer is a per-entity sequence or a hybrid logical timestamp, and the compliance
  answer is that you record both deliveries when a duplicate is skipped.
- **Batch jobs, scheduled transfers and statements** that must run once: lease plus fencing token plus idempotent
  `(account, period)` postings (Unit 3 §3), and the A1 idempotency lab's owner token is already a fence.
- **Multi-tenancy.** The home region, the shard and the batch lease belong to the *tenant* (a credit union), so a
  tenant's data and its jobs move together (Unit 3 §4).

## Mastery gate checklist

- [ ] `sd quiz A5` ≥ 85% — attempt 1 (date: ____)
- [ ] `sd quiz A5` ≥ 85% — attempt 2 on a different day (date: ____)
- [ ] `sd lab A5` — all lab tests pass (with `-race`)
- [ ] Design exercise ([`design-exercise.md`](design-exercise.md)): self-score recorded, tutor score ≥ "meets"
      (`sd record design A5 --self <lvl> --tutor <lvl> --notes "..."`)
- [ ] Teach-back ([`teachback.md`](teachback.md)): tutor marks pass (`sd record teachback A5 --result pass --notes "..."`)

`sd status` is the source of truth; this list is for your own reading. The `raftlite` lab is STRETCH: `sd lab A5`
reports it separately and it does not block the gate.

## What's next

Passing A5 unlocks the case studies that lean on it hardest: the unique ID generator (clock skew and coordination-free
IDs), the distributed key-value store (partitioning, quorums, repair, membership), chat and messaging (ordering and
delivery), ticket booking (holds and oversell prevention) and payments and ledger (idempotency end to end,
reconciliation). The first build project that uses it is the replicated key-value store in Track E. Before moving on, run
`sd review` and clear any A5 questions it surfaces, especially the tags `consistency`, `quorum`, `clocks`, `consensus`
and `idempotency`; they come back in almost every later design. A6 (queues, logs, outbox, sagas) picks up exactly where
Unit 2 §4 stops: the delivery mechanics behind "at-least-once".
