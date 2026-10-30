# A6 — Async: queues, logs, pub/sub, outbox, sagas, backpressure

| | |
|---|---|
| Track | A — HLD fundamentals |
| Estimated time | 18 h (3 weekly units) — CORE 15 h, STRETCH 3 h |
| Prerequisites | A2, A4 (gates passed) |
| Labs | `labs/a6/` — `tinylog`, `outbox`, `saga` (core); `backpressure` (stretch) |
| Gate | quiz ≥ 85% ×2 (different days) · labs pass · design ≥ meets · teach-back pass |

## Why this module exists

On payday morning a credit-union platform decides to make transfers "resilient": the API drops each transfer on a
queue, returns `202 Accepted` in 15 ms, and workers post to the core banking system in the background. At 60 transfers a
second it's a triumph. Then the first big payday arrives: 150 transfers a second for 30 minutes against a core that can
post 100. The backlog reaches 90,000, the last transfer waits 15 minutes, and it takes another 37 minutes to drain
after the surge ends. Every dashboard is green, because the API returned 202s the whole time. Members, whose app gave up
after 10 seconds, tapped "Send" again and made it worse.

That is only the first of the ways asynchrony bites. The service that commits a transfer and *then* publishes
"TransferPosted" loses about 500 events a day at a million transfers, because a deploy kills it in between. The consumer
that credits an account on every delivery double-credits whenever a rebalance replays a batch. One malformed vendor
callback blocks a partition for an hour while the other eleven look healthy. The four-step "send money to an external
account" flow strands a member's funds whenever a process dies between the vendor accepting the transfer and the ledger
being posted, and an autoscaler that triples the workers makes the overloaded database slower, not faster.

None of this is exotic. It's the everyday substance of senior system design interviews ("what happens if the consumer
crashes after the side effect?", "how do you keep the event and the database in sync?", "what do you do when the queue
fills?") and of building anything that talks to a bank, a vendor or each other without a shared transaction.

## Outcomes

- **Decide** when a queue helps and when it hides a problem: size a queue by latency budget and capacity, compute backlog
  and recovery time with Little's law, and recognise a metastable failure. — CORE
- **Choose** between a queue and a log for a workload, pick a partition key and count, and state precisely what ordering
  a partitioned log does and doesn't give. — CORE
- **Implement** a partitioned log with keyed placement, offsets, retention, consumer groups, commits and rebalancing
  (`labs/a6/tinylog`). — CORE
- **Explain** consumer-group rebalancing, generation fencing, hot partitions and how to commit safely when messages are
  processed in parallel. — CORE
- **Choose** a delivery guarantee (at-most-once, at-least-once, effectively-once) from ack and commit timing, and explain
  what Kafka-style transactions do and do not cover. — CORE
- **Explain** the dual-write problem and **implement** a transactional outbox with a leased, at-least-once relay
  (`labs/a6/outbox`). — CORE
- **Design** idempotent consumers (dedupe table, offset-in-database, natural idempotence, downstream idempotency key) and
  size dedupe retention against replay (`labs/a6/outbox`). — CORE
- **Handle** poison messages: classify failures, bound retries, use DLQs with context, reason about ordering and replay
  safely. — CORE
- **Design** pub/sub fan-out with isolated subscriptions, choose log versus queue-per-subscriber and push versus pull,
  and evolve event schemas safely. — CORE
- **Design and implement** a saga orchestrator: orchestration versus choreography, compensations, pivot and retriable
  steps, timeouts as unknown outcomes, isolation countermeasures (`labs/a6/saga`). — CORE
- **Apply** backpressure and load shedding: bound queues by age, size admission limits with Little's law, shed by priority
  and tenant, and avoid autoscaling past the bottleneck. — CORE
- **Implement** bounded queues with block, drop-newest and drop-oldest policies, priority shedding, credit-based flow
  control and a queueing simulator (`labs/a6/backpressure`). — STRETCH

## Unit plan

| Unit | Topic | Hours | Core/Stretch | Lab |
|---|---|---|---|---|
| 1 | [Queues, logs and delivery guarantees](unit-1-queues-logs.md): queue as buffer or debt, queue versus log, partitions, consumer groups, rebalancing, ack and commit timing | 6 | CORE (5) / STRETCH (1) | `labs/a6/tinylog` |
| 2 | [Reliable events](unit-2-outbox-consumers.md): the dual-write problem and the outbox, idempotent consumers, poison messages and DLQs, pub/sub fan-out | 6 | CORE (5) / STRETCH (1) | `labs/a6/outbox` |
| 3 | [Sagas, backpressure and load shedding](unit-3-sagas-backpressure.md): orchestration, compensations and pivots, bounded queues, admission control | 6 | CORE (5, incl. ≈ 1.5 h gate work) / STRETCH (1) | `labs/a6/saga`, `labs/a6/backpressure` (stretch) |

**Unit 1 (6 h)**
- Session 1 (2 h): warm-up; §1 queue as buffer or debt (backlog and recovery arithmetic); §2 queue versus log and
  partitions. Predict boxes, then implement the `Log` half of `labs/a6/tinylog` (append, read, retention).
- Session 2 (2 h): §3 consumer groups and rebalancing; implement `Group` and `Consumer` until
  `go test -race ./labs/a6/tinylog/` is green; run the hot-key and partition-count break-it measurements.
- Session 3 (1 h): §4 delivery guarantees; the commit-timing experiment; check-yourself questions and `sd review`.
  STRETCH (1 h, any time): the metastable-failure program in §1 and Kafka transactions in §4.

**Unit 2 (6 h)**
- Session 1 (2 h): warm-up; §1 dual write and the transactional outbox; implement `DB.Do` and the relay in
  `labs/a6/outbox`, run the dual-write break-it.
- Session 2 (2 h): §2 idempotent consumers; implement `Consumer.Handle`, get the chaos pipeline test green under
  `-race`, then switch on the broken consumer with `-tags breakit` and watch it double-credit.
- Session 3 (1 h): §3 poison messages and DLQs (the head-of-line experiment); §4 pub/sub fan-out; check-yourself and
  `sd review`. STRETCH (1 h, any time): CDC (log tailing) in §1 and DLQ replay tooling in §3.

**Unit 3 (6 h)**
- Session 1 (1.5 h): warm-up; §1 sagas; implement `MemoryStore`, `New` and the forward and compensation paths of
  `labs/a6/saga`; run the non-idempotent-services break-it.
- Session 2 (2 h): §2 compensations, pivots and isolation; add timeouts, retries, stuck/resume and the crash sweeps;
  §3 backpressure and load shedding (predict and reveal, the admission-control program). First `sd lab A6` and
  `sd quiz A6` attempt.
- Session 3 (1.5 h, gate): [design exercise](design-exercise.md) (60 min + self-score) and
  [teach-back](teachback.md) write-up. Second `sd quiz A6` attempt on a later day.
- STRETCH (1 h, any time): `labs/a6/backpressure` (bounded queues, priority shedding, credit gate, Little's-law simulator).

## How this connects to your banking work

- **Duplicate vendor callbacks** are the consumer side of Unit 1 §4 and Unit 2 §2. "Deposit accepted" arriving twice is
  window 3 of the commit-timing diagram with a vendor playing the broker; the fix is a stable external ID, stored with the
  platform's event ID, and a dedupe record in the same transaction as the credit.
- **Money movement** is the textbook saga (Unit 3 §1–§2). The hold on the member's funds is a semantic lock, the vendor's
  timeout is the ambiguous-outcome problem, reversals are appended ledger entries rather than deletions, and the saga's
  persisted trail is the audit trail an auditor reads.
- **Vendor adapters** are where the outbox and idempotency keys meet reality: "after the transfer posts, send a webhook"
  is an outbox (Unit 2 §1), and each vendor's client-reference field is the downstream idempotency key that makes a
  retried step safe. A vendor with no such field makes the integration "at-most-once with reconciliation", and you should
  say so in an interview.
- **Multi-tenant credit unions** show up as the noisy-neighbour problem: one tenant's payroll file must not starve
  another's notifications. That drives the partition key (tenant plus account, not tenant alone), per-tenant caps and
  priority lanes in Unit 3 §3, and tenant ID on every outbox row and DLQ entry.
- **Audit trails and regulators**: an append-only, per-account-ordered log with retention is the natural substrate for
  statements, fraud features and reconciliation; the outbox makes the audit event commit atomically with the ledger
  change; DLQs keep the raw vendor payload for support and for compliance.

## Mastery gate checklist

- [ ] `sd quiz A6` ≥ 85% — attempt 1 (date: ____)
- [ ] `sd quiz A6` ≥ 85% — attempt 2 on a different day (date: ____)
- [ ] `sd lab A6` — all lab tests pass (with `-race`)
- [ ] Design exercise ([`design-exercise.md`](design-exercise.md)): self-score recorded, tutor score ≥ "meets"
      (`sd record design A6 --self <lvl> --tutor <lvl> --notes "..."`)
- [ ] Teach-back ([`teachback.md`](teachback.md)): tutor marks pass (`sd record teachback A6 --result pass --notes "..."`)

`sd status` is the source of truth; this list is for your own reading. The `backpressure` lab is STRETCH: `sd lab A6`
reports it separately and it does not block the gate.

## What's next

Passing A6 unlocks A7 (reliability: timeouts, retries with backoff and jitter, circuit breakers, rate limiting, which
this module deliberately only names) and, with their other prerequisites, the case studies that lean hardest on
asynchrony: news feed, chat, notification service, payments and ledger, and file storage and sync. Track E's job
pipeline project builds exactly this module's pieces as running services. Before moving on, run `sd review` and clear any
A6 questions it surfaces, especially tags `outbox`, `idempotency`, `saga` and `backpressure`; they come back in almost
every later design.
