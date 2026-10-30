# E3 Teach-back

## Prompt

This teach-back is a **reflection**, written after `sd lab E3` is green and your design rationale has its observed
column. A teammate has read A6 and A7 but never built a queue. They ask: "You built one. What did the build teach you
that the units didn't, and which of the textbook ideas actually carried weight?"

Write, in your own words, how *your* pipeline delivers an alert exactly once in effect despite crashes, partitions,
slow vendors and deploys. Walk through your code's decisions, and for each one name the Track A idea it implements,
what would break without it (ideally a test you saw fail or a mutation you tried), and the cost it adds. Finish with
the two predictions from your failure table that were most wrong, and what you understand now that you didn't.

Form: written, 600–1,000 words, one diagram allowed. Quote at most two lines of your own code. No copying from the
unit files.

## Evaluator checklist

Each must-hit item ties a concrete choice in the learner's build to the Track A section it comes from. The learner
needn't cite the section; they must show the idea, its failure mode and its cost.

Must-hit (all required for pass):
- [ ] **Durable before acknowledged**: the 201 is sent after the frame is written (and synced, for enqueues); recovery
      truncates a torn tail; and why acks aren't fsynced (a lost ack is a redelivery). Ties to
      [A2 Unit 2 §4](../A2-storage/unit-2-write-paths.md#4-durability-and-the-write-ahead-log) and
      [A6 Unit 1 §4](../A6-async/unit-1-queues-logs.md#4-delivery-guarantees-and-commit-timing).
- [ ] **Leases make delivery at-least-once**: ack after the effect, visibility timeout, attempt-scoped tokens (409 for
      a stale ack), and why a lease can't stop a paused worker from acting. Ties to
      [A6 Unit 1 §4](../A6-async/unit-1-queues-logs.md#4-delivery-guarantees-and-commit-timing) and
      [A5 Unit 3 §3](../A5-consistency/unit-3-consensus-leases.md#3-leases-and-fencing-tokens).
- [ ] **Exactly-once is end to end**: the consumer key is the job id on every attempt and replay, the vendor is where
      duplicates are recognised, and the claim's limit (the vendor's key retention). Uses F10 or F11 as the evidence.
      Ties to [A5 Unit 2 §4](../A5-consistency/unit-2-time-ordering.md#4-exactly-once-is-an-end-to-end-property) and
      [A6 Unit 2 §2](../A6-async/unit-2-outbox-consumers.md#2-idempotent-consumers-and-dedupe).
- [ ] **Producer idempotency keys**: chosen by the producer from the business fact, atomic check-and-insert, a window
      longer than the producer's retry horizon, checked before the depth bound. Ties to
      [A1 Unit 3 §2](../A1-networking-api/unit-3-idempotency-auth.md#2-idempotency-keys-end-to-end).
- [ ] **Classification and backoff**: which outcomes retry and which dead-letter (with 408/429 and timeouts placed
      correctly, and a timeout recognised as ambiguous), exponential backoff with jitter and why jitter (the
      synchronised waves seen in the break-it), Retry-After as a floor. Ties to
      [A7 Unit 1 §2](../A7-reliability/unit-1-timeouts-retries.md#2-retries-backoff-jitter-and-when-retrying-is-safe).
- [ ] **Dead letters**: the three roads in (permanent, exhausted, lease-expired), what is kept, why a nacked job
      doesn't block the queue, and replay as an explicit, idempotent operation. Ties to
      [A6 Unit 2 §3](../A6-async/unit-2-outbox-consumers.md#3-poison-messages-dlqs-and-replay).
- [ ] **Backpressure**: a per-queue bound on ready + delayed + leased, sized from drain rate × useful life, answered
      with 429 + Retry-After at the door rather than shedding later. Ties to
      [A6 Unit 3 §3](../A6-async/unit-3-sagas-backpressure.md#3-backpressure-and-load-shedding).
- [ ] **Bounded concurrency, timeouts and drain**: the fixed pool as the bound on vendor load, a client timeout on
      every call (the partition lesson), and a drain that finishes calls on their own deadlines inside a bounded time.
      Ties to [A7 Unit 1 §1](../A7-reliability/unit-1-timeouts-retries.md#1-timeouts-and-deadline-budgets) and
      [B0 Unit 2 §5](../B0-go-concurrency/unit-2-cancellation.md#5-graceful-shutdown).
- [ ] **Two wrong predictions**, each with the mental model that was wrong and the corrected one.

Should-hit (at least 4 of 7):
- [ ] Queue vs log: the broker is a queue that *uses* a log, and when a log-based design (Kafka) would be the better
      choice ([A6 Unit 1 §2](../A6-async/unit-1-queues-logs.md#2-queues-versus-logs-and-partitions)).
- [ ] Retry amplification and metastable failure, and what a budget or breaker would add
      ([A7 Unit 1 §3](../A7-reliability/unit-1-timeouts-retries.md#3-retry-budgets-amplification-and-metastable-failures),
      [A7 Unit 2 §1](../A7-reliability/unit-2-breakers-rate-limits.md#1-circuit-breakers)).
- [ ] Where the producer's transactional outbox sits and which key its relay sends
      ([A6 Unit 2 §1](../A6-async/unit-2-outbox-consumers.md#1-the-dual-write-problem-and-the-transactional-outbox)).
- [ ] Lanes or separate queues so a campaign can't starve OTPs and fraud alerts, with a share of the vendor each
      ([A7 Unit 3 §1](../A7-reliability/unit-3-isolation-degradation-dr.md#1-bulkheads-cells-and-shuffle-sharding)).
- [ ] A measured number from their own build (fsync cost per enqueue, retry-storm peak, drain time) and what it implies
      at 10×.
- [ ] What the SIGKILL tests can't simulate (power loss, disk loss) and how they'd test or accept it.
- [ ] The pipeline's SLI as delivered-within-N-seconds of enqueue, and which broker counters feed it
      ([A8 Unit 2 §6](../A8-observability-slo/unit-2-slos-capacity.md#6-composing-slos-and-slos-for-asynchronous-pipelines-stretch)).

Red flags (any one = revise):
- [ ] Claims "exactly-once delivery" from the broker, or that leases prevent duplicates.
- [ ] Says a timeout means the vendor didn't send, or retries a timed-out call with a new key.
- [ ] Treats fsync and "written" as the same, or claims SIGKILL tests prove power-loss durability.
- [ ] Describes deleting jobs after max attempts, or an unbounded queue, as acceptable for member alerts.
- [ ] Explains mechanisms only in the abstract, with no reference to their own code, tests or measurements.

## Recording

`sd record teachback E3 --result <pass|revise> --notes "<missed items>"`
