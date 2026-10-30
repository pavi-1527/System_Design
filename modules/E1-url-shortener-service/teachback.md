# E1 Teach-back — The reflection checklist

## Prompt

You've built and broken `shorty`. A mid-level engineer on your team is about to build a similar service — a
notification-link or document-link service for your platform — and asks: *"What did you learn that the design docs
don't tell you?"*

Write a reflection (600–1,000 words, your own words, one diagram allowed) that walks through **five decisions in your
build** — one each from the store, the create path, the cache, the limiter and failure handling — and for each one:

1. what you built (a specific line of behaviour, not a component name),
2. the **Track A concept** it applies, named precisely (which module and section),
3. what broke or would break without it — ideally something you saw in a test, a break-it or a mutation,
4. what it costs, and when you'd choose differently.

Then end with one prediction from your design write-up v1 that turned out wrong, and what it taught you.

Link each concept to the section that teaches it, e.g.
[A2 Unit 2 §4](../A2-storage/unit-2-write-paths.md#4-durability-and-the-write-ahead-log). The evaluator checks the
mapping, not your prose.

## Evaluator checklist

Must-hit (all required for pass):
- [ ] **Durability / WAL** ([A2 Unit 2 §4](../A2-storage/unit-2-write-paths.md#4-durability-and-the-write-ahead-log)):
      the store acknowledges only after the record is in the log (and fsynced under `-sync always`), applies to the
      index after the append, and the learner states that SIGKILL survives the page cache so it can't test fsync.
- [ ] **Crash recovery**: torn tail truncated vs mid-file corruption refused, with the reason (the last record was never
      acknowledged; a bad record followed by good ones was), and the consequence of getting it wrong in each direction.
- [ ] **Idempotency keys end to end**
      ([A1 Unit 3 §2](../A1-networking-api/unit-3-idempotency-auth.md#2-idempotency-keys-end-to-end)): the intent is
      claimed with insert-if-absent *before* the link is written, a retry rolls it forward, reuse with a different body
      is 422, and the key is scoped per client.
- [ ] **Stampedes and coalescing**
      ([A3 Unit 2 §6](../A3-caching/unit-2-correctness.md#6-stampedes-thundering-herds-dogpiles-and-synchronized-expiry)):
      one store read per cold key per instance, and why the shared lookup must run on a detached context.
- [ ] **Invalidation and the fill race**
      ([A3 Unit 2 §5](../A3-caching/unit-2-correctness.md#5-invalidation-and-ttl-strategy)): tombstone first, then
      invalidate; token-checked fills; the cross-instance staleness bound stated as a number derived from the TTL.
- [ ] **Rate limiting in a fleet**
      ([A7 Unit 2 §3](../A7-reliability/unit-2-breakers-rate-limits.md#3-rate-limiting-in-a-fleet-shared-vs-local-counters-fairness-and-what-to-tell-the-client)):
      the limiter runs before any work, `Retry-After` rounds up, and a per-instance limit admits ≈ N × the limit.
- [ ] **Deadline budgets and degradation**
      ([A7 Unit 1 §1](../A7-reliability/unit-1-timeouts-retries.md#1-timeouts-and-deadline-budgets),
      [A7 Unit 3 §2](../A7-reliability/unit-3-isolation-degradation-dr.md#2-load-shedding-adaptive-concurrency-and-graceful-degradation)):
      per-call timeouts derived from a request budget shorter than the client's timeout; a partition is silent so only
      a timeout ends it; what keeps working when the store is down and why.

Should-hit (at least 4 of 7):
- [ ] **Negative caching** ([A3 Unit 2 §7](../A3-caching/unit-2-correctness.md#7-negative-caching-cache-penetration-and-hot-keys)):
      short TTL, never cache errors as "not found", validation before lookup.
- [ ] **Stale-if-error as a policy choice** with its takedown cost, and data for which it would be wrong.
- [ ] **Insert-if-absent as the coordination primitive**: why it's atomic here (one process serialises it), and what
      breaks that in a replicated store (E2, [A5 Unit 3 §3](../A5-consistency/unit-3-consensus-leases.md#3-leases-and-fencing-tokens)).
- [ ] **Graceful shutdown** (B0 Unit 2): drain on SIGTERM, why `os.Exit` or `Close` loses accepted requests.
- [ ] **302 vs 301 and 404 vs 410** ([A1 Unit 2 §1](../A1-networking-api/unit-2-api-design.md#1-resources-methods-status-codes-and-errors)):
      explicit `Cache-Control`, tombstones, and why a non-owner's delete is 404.
- [ ] **Measurement honesty** ([A8 Unit 1 §5](../A8-observability-slo/unit-1-seeing-the-system.md#5-measuring-latency-correctly-coordinated-omission-and-where-to-measure)):
      open loop, latency from the intended start, percentiles not averaged (stretch-dependent; credit it if they did §3).
- [ ] **Group commit or compaction** described with the cost it removes.

Red flags (any one = revise):
- [ ] Claims a SIGKILL test proves fsync durability.
- [ ] Recovery that skips bad records or truncates at the first bad record anywhere, presented as robust.
- [ ] Idempotency described as "check whether the link exists before creating it" (check-then-act across processes).
- [ ] Says a TTL "guarantees" freshness, or that local invalidation makes all instances fresh.
- [ ] Rate limiter described as overload protection for the store, or a per-instance limit quoted as the fleet limit.
- [ ] Retries inside the API with no budget, or timeouts longer than the client's.
- [ ] Concepts named without a link to a section, or linked to the wrong one.

## Recording

`sd record teachback E1 --result <pass|revise> --notes "<missed items>"`
