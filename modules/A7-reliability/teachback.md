# A7 Teach-back

## Prompt

A mid-level engineer on your team has just added "retry 3 times, one second apart" to the check-image vendor adapter
after an incident, and is pleased with it. The gateway in front of the adapter already retries twice, and so does the
mobile app. They have never watched a retry storm, and their mental model of reliability is "retries make things
reliable".

Explain to them, in your own words, **how that change can make the next incident worse, and what a resilient
integration with a flaky vendor looks like**: how long to wait, when retrying is safe, how retries and a failing
dependency interact, what to put around the call (and in what order), what to do when you're overloaded, and how you'd
find out whether any of it works before the next incident.

Form: written, 400–800 words, may include one diagram (Mermaid or a sketch). No copying from the unit text: if a
sentence from the course appears verbatim, the evaluator will ask you to rephrase it. Write for that engineer, not for
an examiner: concrete beats complete, and one worked number is worth three adjectives.

## Evaluator checklist

Must-hit (all required for pass):
- [ ] Explains **retry amplification**: retries at several layers multiply ((r + 1)ᵏ; 27× for three layers retrying
      twice), retries add the most load exactly when the dependency is struggling, and the fix is to retry at **one
      layer** with a **budget** tied to traffic (a fraction of requests), not a per-request count alone.
- [ ] Explains **backoff with jitter**: exponential growth, and *why* randomness is needed (synchronised clients retry
      in lockstep and collide again), and that waits must **fit inside the caller's deadline**; honours `Retry-After`
      as a floor.
- [ ] States when a retry is **safe**: idempotent operations, a request carrying an idempotency key (reused on every
      attempt), or an error proving nothing was sent; a **timeout is ambiguous** for a non-idempotent call, so a keyless
      `POST` is not retried (or is reconciled).
- [ ] Explains **timeouts as a budget**: every call bounded; the timeout derived from the caller's remaining deadline
      (not a fixed constant); inner timeouts shorter than outer ones; deadline/cancellation propagated so work stops when
      the caller leaves; a slow dependency is worse than a dead one because it holds resources.
- [ ] Explains a **circuit breaker**: closed / open / half-open, why it exists (stop calling a failing dependency, fail
      fast, give it room to recover, probe carefully), what counts as a failure (not the caller's own cancellation, not a
      4xx), and that it needs a **fallback** or a clear failure.
- [ ] Names at least one **isolation or overload mechanism** with its reason: a bulkhead per dependency so one vendor
      cannot consume a shared pool, and/or load shedding with a bounded queue and priorities, with the mechanism of how it
      protects the rest of the system.

Should-hit (at least 4 of 7):
- [ ] Describes a **metastable failure**: a trigger that ends, and a sustaining loop (retries plus a queue of abandoned
      work) that keeps the system down; recovery by removing load or the amplifier, not by waiting.
- [ ] Rate limiting: an **outbound limiter** to respect the vendor's contract (and why a token bucket's burst matters),
      and **429 vs 503** semantics for inbound traffic with `Retry-After`.
- [ ] A concrete **degraded mode** for a banking flow ("received, pending" plus an outbox and an audit record) and why a
      fake success is unacceptable.
- [ ] **Queue sizing from degraded capacity × timeout** or "drop requests whose client has already left" as a
      server-side defence.
- [ ] **Failure-domain awareness**: multi-AZ baseline, RPO/RTO, or what a regional failover quietly needs (idempotency
      store and secrets replicated, vendor allow-lists, capacity).
- [ ] **Correlated failures / change safety**: replicas don't multiply nines when failures share a cause; canaries,
      rings, rollback.
- [ ] **How to test it**: fault injection or a game day, a steady-state hypothesis, and the metrics to watch (retry
      ratio, breaker state, budget, goodput vs throughput).

Red flags (any one = revise):
- [ ] Recommends "retry everywhere to be safe", more retries, or longer timeouts as the answer to an overloaded or
      failing dependency.
- [ ] Retries a non-idempotent call after a timeout with no key and no reconciliation, or generates a new idempotency
      key per attempt.
- [ ] Describes retries with a fixed delay or backoff without jitter as sufficient for a fleet.
- [ ] Treats the circuit breaker as a retry mechanism, or has it count the caller's cancellations, client errors or the
      system's own rejections as dependency failures; or proposes a breaker with no plan for the open state.
- [ ] Proposes an unbounded queue ("so we never drop a request") as the overload strategy, or autoscaling alone.
- [ ] Claims that more replicas or regions give arbitrarily many nines without mentioning common-cause failures.
- [ ] Treats 429 and 503 as interchangeable and tells clients to retry immediately.

## Recording

`sd record teachback A7 --result <pass|revise> --notes "<missed items>"`
