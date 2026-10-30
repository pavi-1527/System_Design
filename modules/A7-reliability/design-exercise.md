# A7 Design exercise — Resilient check deposits

> Time box: 60 minutes, solo, no notes. Then self-score against the rubric (≈ 15 min), then send your write-up to
> the tutor. Use the structure from [`docs/INTERVIEW_FRAMEWORK.md`](../../docs/INTERVIEW_FRAMEWORK.md) and the
> numbers from [`docs/NUMBERS.md`](../../docs/NUMBERS.md). The first ten minutes are requirements and estimation;
> don't skip them.

## Prompt

> "We run digital banking for about 40 credit unions. Members deposit checks by photographing them in the mobile app.
> We send each image to a third-party check-analysis vendor, then post the deposit to the credit union's core banking
> system. Last quarter the vendor was slow for ten minutes on a payday morning, and our whole platform fell over,
> not just deposits. Design how this flow should behave when the vendor, the core, our own service or a whole region
> misbehaves."

That is all the interviewer says up front. Treat it like the real interview: ask the clarifying questions you need
(write them down, then write the answers you'd assume if the interviewer said "you decide"), and drive.

Things an interviewer would answer if asked (don't read until you've written your questions):

<details data-kind="hint">
<summary>Interviewer's answers to common clarifying questions</summary>

- ≈ 400,000 deposits a day across all credit unions on average; payday peak ≈ 5× the average rate for ≈ 2 hours.
  Read traffic (status checks) ≈ 10× writes.
- The vendor: p50 ≈ 1.2 s, p99 ≈ 8 s per image. Its contract allows 150 requests/s and 40 concurrent requests for the
  whole platform. It had three outages of 10–60 minutes last year and one 2-hour brown-out. It exposes a client reference
  field you may set; it also offers a callback with the result, which it sometimes delivers twice.
- The core: per credit union, p50 ≈ 300 ms, p99 ≈ 9 s, occasionally unavailable during maintenance windows. Posting a
  deposit is not idempotent unless you pass a unique reference, which the core deduplicates for 24 hours.
- Members expect an immediate "we received your deposit"; availability of *submission* must be ≥ 99.95%; a deposit
  may take minutes to be *accepted* but must never be silently lost or posted twice.
- Compliance requires an audit trail of every state change of a deposit, including those made while the vendor was
  unavailable.
- Primary region plus a DR region (cold today). The business says: no more than 5 minutes of data loss and no more than
  30 minutes of downtime for deposit submission after a regional outage; money-movement data (the ledger of deposits)
  must not be lost.
- One credit union occasionally runs a promotion that produces 10× its normal deposit volume for an hour.
- Teams: one platform team, one team per vendor adapter.

</details>

## What to produce

1. **Requirements and SLOs**: functional (bullets) and non-functional with numbers (submission availability, time to
   accept, RPO/RTO, what "degraded" means to a member), your clarifying questions and assumptions.
2. **Estimate sheet**: average and peak deposit rate; vendor calls/s and concurrency (Little's law) in the normal case
   and when the vendor is 5× slower; retry amplification with your retry policy; the sizes you choose for the vendor
   bulkhead, outbound rate limiter and queue; the core's concurrency; DR numbers (RPO in transfers lost at a given
   replication lag, RTO as a sum of steps).
3. **Architecture diagram**: member → edge → deposit service → (queue/outbox?) → vendor adapter → vendor; → core
   adapter → core; callback receiver; DR region. Mark where deadlines are set and propagated, where retries happen
   (and where they don't), where the breaker, limiter and bulkhead sit, and where the idempotency keys live.
4. **A timeout and deadline budget table** for the synchronous parts (app, gateway, service, adapter, vendor/core
   attempt), with how it's derived, plus the retry policy (what, how many, backoff and jitter, budget, who retries).
5. **A failure table**: for the vendor, the core, your own service, the database, and a region, give the failure modes
   (down, slow, flaky, duplicate callbacks, partial), how you detect each, what the system does, what the member sees,
   and what's written to the audit trail.
6. **Overload and degradation plan**: priority classes and shedding order; queue bounds with the arithmetic; per-credit-
   union fairness (the promotion case); what the 503/429 responses look like; brownout levels.
7. **DR plan**: tier chosen from the RPO/RTO with the reasoning; failover steps and who declares; how you prevent
   split brain; what must replicate (including the things that are easy to forget); how you'd drill it.
8. **Trade-off table**: at least three decisions with "wins when / loses when / when the answer flips".
9. **A test plan**: three fault-injection experiments you would run (staging or production at small blast radius) with
   the steady-state hypothesis and the abort condition for each.

## Rubric

| Dimension | below | approaching | meets (senior bar) | exceeds |
|---|---|---|---|---|
| Requirements & SLOs | Draws immediately; "highly available" | Lists features; NFRs vague | Quantified submission SLO, accept-latency, RPO/RTO, explicit definition of degraded vs failed; clarifies idempotency/callbacks and audit | Derives the error budget and shows which dependency consumes it; separates *submit* from *accept* as different SLOs and says why |
| Estimation | None, or off by orders of magnitude | Average rate only | Peak rate; vendor concurrency by Little's law normal vs 5× slow; amplification; bulkhead/limiter/queue sizes from numbers; DR arithmetic | Uses the numbers to decide (async acceptance because concurrency at p99 exceeds the contract; queue limit from degraded capacity × timeout) |
| Timeouts & deadline budget | No timeouts, or one number everywhere | Timeouts per hop but not derived; inner can exceed outer | A derived, ordered budget with propagation; attempts × timeout + backoff fits the deadline; knows what happens to work after the caller leaves | Per-hop reserve and "skip when too little left"; separate budgets for the sync and async halves; cancellation reaches the vendor adapter |
| Retries & idempotency | "Retry on failure" | Retries with backoff; idempotency mentioned vaguely | One retrying layer; safe-to-resend rule (keyed / idempotent / never sent); full jitter + `Retry-After`; shared budget; client reference to the vendor and a unique reference to the core; ambiguous outcomes reconciled; duplicate callbacks deduped | Crash between "vendor accepted" and "recorded" handled; budget sized from the vendor's base error rate; retry ratio as an alert |
| Breakers, limits & isolation | Not mentioned | Breaker or rate limit named without parameters | Breaker per vendor with rule, window, probes and what counts as failure; outbound limiter at the contract; bulkhead per vendor; per-credit-union limits; fallback defined | Error taxonomy across the stack (non-retryable local rejections, ignored by the breaker); cell or shuffle-shard placement for tenants; probe jitter across the fleet |
| Overload & degradation | "Autoscale" or "add a queue" | Some shedding idea | Priority classes with the audit write never shed; bounded queue sized from capacity and deadline; 503/429 with `Retry-After`; deadline-aware workers; per-tenant fairness (max-min) for the promotion; brownout levels | Shows the metastable loop and where each control cuts it; adaptive concurrency; client-side adaptation |
| Failure domains & DR | "Multi-region" with no detail | Names a DR tier without tying to RPO/RTO | Tier chosen from RPO/RTO with cost; failover chain with a time budget; fencing / split-brain prevention; what must replicate (idempotency store, outbox, secrets, vendor allow-lists, capacity); zone baseline at ≤ 66% | Home-region writes for the ledger; failback plan; dependency map including the vendor's and core's own DR; rehearsed drill with a harder-than-reality scenario |
| Operability & testing | No metrics | Generic dashboards | Per-layer metrics (retry ratio, budget, breaker state, bulkhead use, shed by class, queue delay, goodput); alerts on symptoms; runbook for "vendor slow"; three concrete fault-injection experiments with hypotheses and abort conditions | Deploy safety (rings by credit union, flags, auto-rollback), correlated-failure budget `c`, game-day cadence, audit of the degraded path |
| Trade-offs & communication | Single answer per decision | Trade-offs only when prompted | Trade-off table with "when the answer flips"; drives the 60 minutes; checks in with the interviewer | Anticipates follow-ups; quantifies the cost of each protection; says what they'd defer and why |

## Tutor critique protocol

1. Read the write-up once fully before scoring. Score each dimension independently using the rubric's words; quote the
   learner's own text as evidence for each score.
2. Check the arithmetic in the estimate sheet. An estimate that is off by > 10× on a number the design depends on
   (vendor concurrency, the queue bound, retry amplification, the DR RPO) caps *Estimation* at "approaching".
3. Ask these follow-up probes (all of them, one at a time; the learner answers in writing or live, without notes).
   Score the answers into the relevant dimensions:
   - "At 09:05 on payday the vendor starts answering in 20 seconds instead of 1.2. Walk me through the first 60
     seconds, then minutes 1 to 10. What do members see, and what is written to the audit trail?"
   - "The member's app timed out after 10 seconds. The vendor's callback arrives at 14 seconds saying 'accepted', and
     then arrives again at 16 seconds. Trace every component, including the core."
   - "The app, the gateway and your adapter all retry. Where exactly do you cap the amplification, and what is the
     number for your design?"
   - "One credit union's promotion sends 5,000 deposits in a minute. What happens to the other 39? Show me the line of
     your design that decides."
   - "The breaker is open and deposits are being accepted as pending. The vendor recovers. How do the queued deposits
     get processed without a thundering herd, without duplicates, and in what order?"
   - "The primary region is gone. Walk me through the first 30 minutes: who declares, what do you lose, what do you
     tell members, and what is the first thing that breaks?"
   - "How do you know your fallback works? When did you last run it?"
   - "What is the availability of *submit* with your design? Show the arithmetic including the vendor's 99.9% and a
     bad-deploy budget."
   - One probe of the tutor's choice on the learner's weakest dimension.
4. Overall level:
   - **meets** requires "meets" or better on *Retries & idempotency* and *Failure domains & DR* (the two dimensions
     where a mistake posts money twice or loses a ledger), and no dimension at "below".
   - **exceeds** requires "meets" on every dimension and "exceeds" on at least three.
   - Otherwise **approaching** (or **below** if two or more dimensions are "below").
5. Feedback: three bullets: the strongest part, the one change that would most raise the score, and one concrete thing
   to re-read (link the section, e.g. [`unit-1-timeouts-retries.md`](unit-1-timeouts-retries.md) §3 or
   [`unit-3-isolation-degradation-dr.md`](unit-3-isolation-degradation-dr.md) §3). If the result is below "meets", the
   learner redoes the exercise on a different day (variation: "design the SSO integration for 40 identity providers
   when one of them is slow", same rubric).

## Recording

`sd record design A7 --self <below|approaching|meets|exceeds> --tutor <level> --notes "<one-line summary>"`
