# A7 — Reliability: timeouts, retries, circuit breakers, rate limiting, bulkheads, multi-region

| | |
|---|---|
| Track | A — HLD fundamentals |
| Estimated time | 18 h (3 weekly units) — CORE 15 h, STRETCH 3 h |
| Prerequisites | A1 (gate passed), A6 (gate passed) |
| Labs | `labs/a7/` — `retry`, `breaker`, `ratelimit` (core); `bulkhead` (stretch); `capstone` and `sim` (break-it only) |
| Gate | quiz ≥ 85% ×2 (different days) · labs pass · design ≥ meets · teach-back pass |

## Why this module exists

At 09:05 on payday the check-image vendor starts answering in 2 seconds instead of 200 milliseconds. Nothing is
"down": every health check is green. The platform sends it ≈ 100 calls a second, so the adapter now holds 200
connections instead of 20 (Little's law), against a shared pool of 150. The statements service, which uses the same
HTTP client, starts timing out. Every layer has "sensible" retry settings, the app, the gateway and the adapter each
retry twice, so every member tap becomes up to 27 calls to a vendor that is already struggling. The vendor's gateway
starts returning 429s, which the adapter retries immediately. Ten minutes later the vendor has recovered, but the
platform hasn't: its queues hold thousands of requests whose members gave up minutes ago, each of which triggers
another retry. Goodput is zero at full utilisation, and the incident ends when someone restarts the pods.

None of the mechanisms was wrong in isolation. The timeouts were there, the retries were reasonable, the vendor was
the one at fault. What was missing was the *system*: a budget that links the timeouts to the member's deadline, a
retry policy that is paid for out of traffic and not out of the vendor's goodwill, a breaker that remembers the last
four hundred failures, a bulkhead that keeps one vendor from eating a shared pool, a server that sheds instead of
queueing, and a second region that someone has actually failed over to. This module builds those pieces, in Go, until
you can say what each protects, what it costs, and when it makes things worse.

## Outcomes

- **Derive** per-hop timeouts from a propagated deadline budget, order them across hops, and **explain** why a timeout
  is a capacity control as much as a correctness control. — CORE
- **Implement** a retry executor with exponential backoff and full / equal / decorrelated jitter, `Retry-After`
  handling, a deadline budget, an idempotency gate and error classification (`labs/a7/retry`). — CORE
- **Compute** retry amplification ((r + 1)ᵏ), **size** a shared retry budget, and **explain** metastable failures and
  the defences against them. — CORE
- **Implement** a circuit breaker with a rolling window, limited half-open probing and correct outcome classification
  (a cancelled caller is not a failing vendor), and **design** its fallback (`labs/a7/breaker`). — CORE
- **Choose** between fixed-window, sliding-window (log and counter), token-bucket, leaky-bucket and GCRA limiters for a
  requirement, and **implement** token-bucket and sliding-window limiters with bounded memory and correct
  `Retry-After` (`labs/a7/ratelimit`). — CORE
- **Design** rate limiting for a multi-node, multi-tenant API: local vs shared counters vs leases, fairness, fail-open
  vs fail-closed, 429 vs 503, and client behaviour. — CORE
- **Apply** bulkheads, cells and shuffle sharding to contain failure, and **size** pools and queues with Little's law.
  — CORE
- **Design** load shedding and graceful degradation for an overloaded service: priority classes, queue bounds,
  deadline-aware servers, brownouts. — CORE
- **Choose** a multi-AZ / multi-region topology and DR tier from RPO, RTO and cost, and **walk through** a failover
  including fencing, split brain and the dependencies that quietly don't replicate. — CORE
- **Reason about** availability with correlated failures (`A = (1 − c)(1 − uᵏ)`), and **design** deploy-safety and
  chaos / game-day practices that exercise the failure paths. — CORE
- **Compose** retry, breaker, bulkhead and limiter into one vendor adapter with an explicit error taxonomy, and
  **predict** how mis-ordering them fails. — CORE
- **Implement** semaphore bulkheads per dependency and an AIMD adaptive concurrency limit (`labs/a7/bulkhead`),
  and **explain** hedged requests, GCRA and the leaky bucket as a shaper. — STRETCH

## Unit plan

| Unit | Topic | Hours | Core/Stretch | Lab |
|---|---|---|---|---|
| 1 | [Timeouts, retries and retry budgets: deadlines, backoff and jitter, amplification, metastable failures, hedging](unit-1-timeouts-retries.md) | 5 | CORE (4.5) / STRETCH (0.5) | `labs/a7/retry` |
| 2 | [Circuit breakers and rate limiting: breakers, algorithms, shared vs local counters, fairness, 429 vs 503](unit-2-breakers-rate-limits.md) | 5.5 | CORE (5) / STRETCH (0.5) | `labs/a7/breaker`, `labs/a7/ratelimit` |
| 3 | [Isolation, degradation and surviving failure domains: bulkheads and cells, shedding, multi-region and DR, correlated failures and chaos, the capstone](unit-3-isolation-degradation-dr.md) | 7.5 | CORE (5.5, incl. ≈ 1.5 h gate work) / STRETCH (2) | `labs/a7/bulkhead` (stretch), `labs/a7/capstone` (break-it) |

**Unit 1 (5 h)**
- Session 1 (1.5 h): warm-up; §1 timeouts and deadline budgets, predict boxes, the `sim timeouts` experiment.
- Session 2 (2 h): §2 retries, backoff and jitter; implement `Delay`, `Do`, classification, the deadline budget and
  `ParseRetryAfter` in `labs/a7/retry`; the jitter break-it.
- Session 3 (1 h): §3 retry budgets and metastable failures; implement `Budget`; run the amplification table and
  `sim metastable`. STRETCH (0.5 h, any time): §4 hedged requests.

**Unit 2 (5.5 h)**
- Session 1 (2 h): §1 circuit breakers; implement `labs/a7/breaker`; run the outage and detection-speed experiments.
- Session 2 (2 h): §2 rate-limiting algorithms; implement the three limiters in `labs/a7/ratelimit`; run the boundary
  burst, counter-vs-log and memory experiments.
- Session 3 (1 h): §3 limiting in a fleet; implement `RetryAfterSeconds`, `SetHeaders` and `Middleware`; run
  `sim distlimit`. STRETCH (0.5 h): GCRA and the leaky bucket as a shaper, in §2.

**Unit 3 (7.5 h)**
- Session 1 (1.5 h): §1 bulkheads, cells and shuffle sharding; §2 load shedding and degradation (read, predict, and
  sweep the queue limit in `sim metastable`).
- Session 2 (1.5 h): §3 multi-AZ, multi-region and DR (including the tabletop exercise); §4 correlated failures,
  deploy safety and chaos.
- Session 3 (1 h): §5 capstone: run `labs/a7/capstone` against your own code; `sd lab A7`; first `sd quiz A7` attempt.
- Session 4 (1.5 h, gate): design exercise (60 min + self-score), teach-back write-up.
- STRETCH (2 h): `labs/a7/bulkhead` (the `Bulkhead`, `Group` and `AIMD` implementations) and its break-it
  experiments.
- Second `sd quiz A7` attempt on a later day.

## How this connects to your banking work

- **Every vendor adapter you wrote is this module's subject.** The check-image vendor, the SSO provider, the card
  processor and the core all need the same stack: a timeout derived from the member's deadline (Unit 1 §1), retries
  classified into safe / ambiguous / permanent (Unit 1 §2), a breaker with an honest fallback such as "received,
  pending" (Unit 2 §1), an outbound limiter that respects the vendor's contract (Unit 2 §2–§3) and a bulkhead per
  vendor (Unit 3 §1). Unit 3 §5 assembles them and shows what each ordering mistake costs.
- **Vendor timeouts and duplicate callbacks are the ambiguity at the heart of retries.** A timed-out `POST` may have
  been processed, and the vendor may call back later, twice. Unit 1 §2 ties retries to the idempotency keys of A1, and
  Unit 3 §3 shows why the idempotency store and the outbox must replicate to the standby region or a failover turns
  retries into double postings.
- **Audit trails and compliance shape the degraded behaviours.** Shedding and brownouts are business decisions made in
  advance (Unit 3 §2): partner batch before interactive reads before money movement, and never the audit write. A
  breaker opening is an auditable event, and "accepted while the vendor was down" needs a record.
- **Multi-tenancy is a reliability problem.** Forty credit unions share your platform: per-tenant limits and fairness
  (Unit 2 §3), cells and shuffle sharding to keep one tenant's incident from becoming everyone's (Unit 3 §1), and
  ring-by-credit-union rollouts (Unit 3 §4).
- **SSO and DR.** An identity provider is a dependency with its own SLA and failure modes; its availability caps
  yours unless existing sessions keep working (Unit 3 §4). Your DR region needs the same certificates, vendor
  allow-lists and capacity, which is where real RTO is usually decided (Unit 3 §3).

## Mastery gate checklist

- [ ] `sd quiz A7` ≥ 85% — attempt 1 (date: ____)
- [ ] `sd quiz A7` ≥ 85% — attempt 2 on a different day (date: ____)
- [ ] `sd lab A7` — all lab tests pass (with `-race`)
- [ ] Design exercise ([`design-exercise.md`](design-exercise.md)): self-score recorded, tutor score ≥ "meets"
      (`sd record design A7 --self <lvl> --tutor <lvl> --notes "..."`)
- [ ] Teach-back ([`teachback.md`](teachback.md)): tutor marks pass (`sd record teachback A7 --result pass --notes "..."`)

`sd status` is the source of truth; this list is for your own reading. The `bulkhead` lab is STRETCH: `sd lab A7`
reports it separately and it does not block the gate. The `capstone` and `sim` programs are break-it material behind
the `breakit` build tag and are not graded.

## What's next

Passing A7 unlocks A8 (observability, SLOs and capacity planning: the metrics and alerts that tell you a breaker is
open, a budget is empty or a canary is failing), and, with their other prerequisites, the Track C case studies that
lean on this module: the distributed rate limiter (C2), the notification service with provider failover (C7) and
payments (C9), plus the HLD mocks (D3) and the build projects that need retries, backoff and rate limiting (E1, E3).
Before moving on, run `sd review` and clear any A7 questions it surfaces, especially tags `retries`, `timeouts`,
`circuit-breaker` and `rate-limiting`; they come back in almost every later design.
