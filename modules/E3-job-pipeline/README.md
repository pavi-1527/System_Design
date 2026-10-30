# E3 — Build: async job / notification pipeline

| | |
|---|---|
| Track | E — Build real systems |
| Estimated time | 18 h (3 weekly units) — CORE 15 h, STRETCH 3 h |
| Prerequisites | A6, A7, B0 (gates passed) |
| Labs | `labs/e3/` — `jobq` (broker, worker and fake vendor as separate processes on 127.0.0.1; milestone and failure-injection tests) |
| Gate | quiz ≥ 85% ×2 (different days) · labs pass · design rationale ≥ meets · reflection (teach-back) pass |

## Why this module exists

The first version of member alerts on most platforms is a function call: the transfers service posts the transfer,
then calls the SMS vendor inline with a 10 s timeout. It works for a year. Then the vendor has a bad Tuesday: p99
goes from 300 ms to 9 s, transfers p99 follows it, and the transfers team adds "retry three times". Now each slow
send costs up to 40 s of a request thread. Someone moves sending to a goroutine, and alerts quietly disappear on
every deploy, because the goroutines die with the pod. Someone adds a Redis list and a worker. Alerts survive deploys
now, but a worker that crashes mid-send loses the job, and one that is merely slow gets the same job twice from a
"helpful" requeue script. Members get two "your transfer of $2,000 posted" texts, and compliance asks how many
fraud alerts were never sent last quarter. Nobody can answer, because nothing records an attempt.

Each fix was reasonable, and each one moved the problem somewhere less visible. A6 and A7 gave you the vocabulary:
at-least-once delivery, idempotent consumers, visibility timeouts, poison messages, backoff with jitter,
Retry-After, backpressure. This module makes you *build* the pipeline as three real processes (a durable broker, a
pool of workers and a deliberately unreliable vendor), then kill them, partition them, stall them and corrupt
their disks, and check that the alert reaches the member **exactly once in effect**, never zero times and never
twice. Every design choice gets argued in a write-up before you code it, and every failure gets predicted before
you inject it.

## Outcomes

- **Implement** a durable job broker on an append-only, checksummed log that survives SIGKILL and a torn tail, and
  **explain** which writes must be fsynced before acknowledging and which need not be. — CORE
- **Implement** leases with visibility timeouts and per-attempt lease tokens, and **explain** why that makes delivery
  at-least-once and what a lease expiry means for a job that is still running. — CORE
- **Build** a worker pool with bounded concurrency and long-polling that survives a broker outage or partition
  without exiting, spinning or overloading the vendor. — CORE
- **Classify** vendor responses (timeout, 429, 5xx, other 4xx) into retryable and permanent, and **implement**
  exponential backoff with full or equal jitter and Retry-After as a floor, without overflow. — CORE
- **Make** the pipeline effectively exactly-once at the vendor with a producer key at the broker and a stable
  consumer key per job, and **show** with a SIGKILL between effect and ack that it holds. — CORE
- **Dead-letter** permanent failures, exhausted retries and jobs that keep killing workers, with their history;
  **keep** a poison job from blocking the queue; **replay** safely after a fix. — CORE
- **Apply** backpressure with a depth bound that answers 429 + Retry-After (and still recognises a producer's
  retry), and **size** the bound from drain time and memory. — CORE
- **Drain** a worker on SIGTERM without losing or duplicating work, within a bounded time. — CORE
- **Predict** every failure-injection scenario in a design rationale, then **compare** it with what happened and
  explain every difference. — CORE
- **Compact** the log, **add** a shared retry budget across workers, and **place** the producer's outbox and a
  partitioned broker in the design. — STRETCH

## Unit plan

| Unit | Topic | Hours | Core/Stretch | Lab |
|---|---|---|---|---|
| 1 | [A durable queue and a pool of workers](unit-1-durable-queue.md) (milestone M1) | 6 | CORE (5) / STRETCH (1: §5 log compaction) | `labs/e3/jobq` |
| 2 | [Retries and idempotency at the vendor boundary](unit-2-retries-idempotency.md) (M2, M3) | 6 | CORE (5) / STRETCH (1: §4 retry budget) | `labs/e3/jobq` |
| 3 | [Dead letters, backpressure, drain and the failure run](unit-3-dead-letters-backpressure.md) (M4, M5) | 6 | CORE (5) / STRETCH (1: §5 outbox and partitions) | `labs/e3/jobq` |

Read [spec.md](spec.md) first (20 minutes); it is the contract and the failure table you will predict against.

**Unit 1 (3 × 2 h)**
- Session 1 (2 h): warm-up, [spec.md](spec.md), §1. Write the first draft of the [design rationale](design-exercise.md)
  sections 1–4 (why a broker, the log, leases, the worker) *before* any code. That is part of the gate.
- Session 2 (2 h): §2–§3; implement `AppendFrame`/`ScanFrames`, then the broker's enqueue, lease, ack and recovery.
  Frames first (`go test -race -run 'TestM1_(Frames|AppendFrame|Scan)' ./labs/e3/jobq/`), then the broker-only
  tests (`EnqueueLeaseAck`, `JobsAreLeasedInEnqueueOrder`, `LongPoll…`, `AnExpiredLease…`, `AckedJobs…`,
  `ALeaseSurvives…`, `ATornLogTail…`, `ACorruptFrame…`, `BrokerShutsDown…`).
- Session 3 (2 h): §4; the worker loop. All of `-run TestM1` green, then the break-it.
- STRETCH (1 h): §5, log compaction.

**Unit 2 (3 × 2 h)**
- Session 1 (2 h): warm-up, §1–§2; `Classify`, `Backoff`, `RetryDelay`, `ParseRetryAfter`, the broker's nack and the
  worker's retry path. `-run TestM2`.
- Session 2 (2 h): §3; producer keys and the consumer key. `-run TestM3`. Run the retry-storm break-it.
- Session 3 (2 h): update the design rationale (retry policy, the two keys); first `sd quiz E3` attempt.
- STRETCH (1 h): §4, a retry budget shared by the fleet.

**Unit 3 (3 × 2 h)**
- Session 1 (2 h): warm-up, §1; dead letters and replay. `-run TestM4`.
- Session 2 (2 h): §2–§3; depth bound and drain. `-run TestM5`, then `sd lab E3` (all of it, with `-race`).
- Session 3 (2 h): §4, the failure run: fill in the predicted-vs-observed table of the design rationale, then the
  [reflection checklist](teachback.md); send both to the tutor.
- STRETCH (1 h): §5, where the producer's outbox sits and how the broker would scale out.
- Second `sd quiz E3` attempt on a later day.

## How this connects to your banking work

- **Duplicate vendor callbacks, from the other side.** You have written adapters that had to survive a vendor
  calling *you* twice. Here you are the caller, and the vendor's idempotency key is the only thing between a timeout
  and a member getting two "transfer posted" texts. F10 and F11 in the spec are exactly the incidents you have seen
  from the receiving end.
- **The SMS vendor's bad Tuesday.** 503 bursts, 429 with Retry-After at the contracted rate, and a vendor that accepts
  a message and then stalls past your timeout. The worker's classification table (Unit 2 §1) is the error taxonomy
  your adapters already had implicitly; now it decides between a retry, a dead letter and a page.
- **Fraud and OTP alerts can't wait behind a broken template.** A marketing template that the vendor rejects with 400
  must not sit at the head of the queue in front of a fraud alert. That is the poison-job test, and the reason
  dead-letter queues exist.
- **Compliance asks "did we notify the member, and when?"** The attempt history on every job, the DLQ with its last
  error, and an explicit replay are the audit trail. A `sent` boolean can't answer that question.
- **Deploys.** Every rolling deploy SIGTERMs your workers. The drain milestone is the difference between "deploys are
  boring" and "every deploy duplicates a few alerts".

## Mastery gate checklist
- [ ] `sd quiz E3` ≥ 85% — attempt 1 (date: ____)
- [ ] `sd quiz E3` ≥ 85% — attempt 2 on a different day (date: ____)
- [ ] `sd lab E3` — all lab tests pass (with `-race`), including the failure-injection tests
- [ ] Design rationale (`design-exercise.md`): written before M1, revised after the failure run with the
      predicted-vs-observed table, self-score recorded, tutor score ≥ "meets"
      (`sd record design E3 --self <lvl> --tutor <lvl> --notes "..."`)
- [ ] Reflection checklist (`teachback.md`): tutor marks pass (`sd record teachback E3 --result pass --notes "..."`)

`sd status` is the source of truth; this list is for your own reading.

## What's next

E3 is the last build in the recommended order before the stretch case studies and D4. Passing it means you have
built, broken and defended the async half of almost every C-track design: C7's notification service, C9's payment
pipeline and C11's transcoding jobs are this pipeline with different payloads. Before D4, run `sd review` until the
E3 cards are in box 2 or later, and re-read the wrap-up tables of all three units: the D4 component oral asks you to
explain a queue "from the inside", and you now have one.
