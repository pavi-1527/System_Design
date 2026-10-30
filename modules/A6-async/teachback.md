# A6 Teach-back

## Prompt

A mid-level engineer on your team is about to build the "pay out a credit-union payroll file" pipeline. Their plan is
"put every line on a queue, have workers post to the core, retry on any error, and publish an event after each posting".
They've never run an asynchronous pipeline that moves money.

Explain to them, in your own words, **what is wrong with that plan, and how to make an asynchronous money-moving
pipeline correct**: what a queue does and doesn't do for you, what "delivered once" can and can't mean, how the state
change and its event stay consistent, what to do with messages that can't be processed, what to do when the flow spans
several systems, and what to do when work arrives faster than it can be done.

Form: written, 400–800 words, may include one diagram (Mermaid or a sketch). No copying from the unit text: if a sentence
from the course appears verbatim, the evaluator will ask you to rephrase it. Write for that engineer, not for an
examiner — concrete beats complete.

## Evaluator checklist

Must-hit (all required for pass):
- [ ] A queue is a buffer between a producer rate and a consumer rate: backlog means waiting time (Little's law), an
      unbounded queue turns overload into silent latency, so it must be **bounded by age**, with explicit rejection or
      shedding and a visible status; the metric is the age of the oldest message, not depth alone.
- [ ] Delivery is **at-least-once** in practice: commit/ack after the effect gives duplicates on a crash, commit before gives
      loss; therefore the consumer must be **idempotent**, and "exactly-once" is achieved as an effect (dedupe or natural
      idempotence), not promised by the broker.
- [ ] The idempotency record is written **in the same transaction as the effect** (or the offset is stored with it, or a
      stable key is passed to the downstream), with a stable ID from the original request, and acking happens after the commit.
- [ ] The **dual-write problem**: committing state and then publishing loses events (publishing first invents them);
      the fix is a **transactional outbox** (event row in the same transaction, relay publishes at-least-once, event ID
      for dedupe).
- [ ] **Poison/unprocessable messages** are classified (transient versus permanent), retried a bounded number of times,
      then parked in a **DLQ** with enough context, alerting on it, and replayed safely (rate-limited, idempotent) — not
      retried forever and not dropped.
- [ ] A multi-system flow needs a **saga**: local steps with idempotent compensations, persisted progress, stable keys
      and a stuck state that alerts; a **timeout is an unknown outcome**, not a failure (so funds are not released on a
      timeout without checking).

Should-hit (at least 4 of 7):
- [ ] Per-key ordering comes from the partition key (account, not tenant alone); there is no ordering across partitions;
      hot partitions and partition count being hard to change.
- [ ] Queue versus log: replay, fan-out and retention versus per-message delay/retry; head-of-line blocking per partition.
- [ ] Rebalances replay uncommitted work and a paused consumer can act after losing its partition (generation fencing
      protects offsets, not external side effects).
- [ ] Recovery-time arithmetic: backlog ÷ (capacity − arrival), and why running at 95% utilisation makes recovery slow.
- [ ] Separate lanes / priority and per-tenant limits so a batch file cannot starve interactive transfers or other
      tenants; autoscaling past the bottleneck does not help.
- [ ] Retry storms and metastable failure: abandoned work in a deep queue plus client retries; drop expired work, use
      backoff with jitter and a budget at one layer (A7).
- [ ] Pivot and retriable steps, tombstones or lookup for late actions, holds as semantic locks, the audit trail.

Red flags (any one = revise):
- [ ] Claims the broker (or "exactly-once" settings) guarantees each effect happens once, with no mention of an idempotent
      consumer or an external side effect.
- [ ] Commits or acks **before** the effect for money movement, or marks a message as processed before the effect succeeds.
- [ ] Deduplicates on message content or a time window ("same amount to the same account in the last 5 minutes") as the
      mechanism.
- [ ] Publishes the event after the commit (or before) and treats it as safe, with no outbox or reconciliation.
- [ ] Treats a timeout as a failure and releases funds or compensates without checking the outcome.
- [ ] Retries failures forever, or sends them to a DLQ nobody watches, or drops them.
- [ ] Answers "the queue is growing" with only "add more workers/autoscale", with no bound, shedding or look at the
      bottleneck.

## Recording

`sd record teachback A6 --result <pass|revise> --notes "<missed items>"`
