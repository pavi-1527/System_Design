# A8 Design exercise — SLOs, alerting and capacity for the Payments API

> Time box: 60 minutes, solo, no notes. Then self-score against the rubric (≈ 15 min), then send your write-up to
> the tutor. Use the structure from [`docs/INTERVIEW_FRAMEWORK.md`](../../docs/INTERVIEW_FRAMEWORK.md) and the
> numbers from [`docs/NUMBERS.md`](../../docs/NUMBERS.md).

## Prompt

> "We run a digital-banking platform for about 40 credit unions. Our Payments API lets members and third-party apps
> create transfers, check their status and receive webhooks when the state changes. Today we have a CPU alert, a
> latency dashboard that shows the average, and a contract that says '99.9% uptime, measured by pinging /healthz'.
> Last month the core-banking vendor's maintenance window broke every transfer for four hours while we reported 100%
> uptime, and on the 1st of the month traffic jumped 6× and we browned out for forty minutes. Design how we should
> observe the Payments API, what we should promise, when we should page people, and how we plan capacity."

Treat it like the real interview: ask the clarifying questions you need (write them down, then write the answers
you would assume if the interviewer said "you decide"), and drive.

Things an interviewer would answer if asked (don't read until you've written your questions):

<details data-kind="hint">
<summary>Interviewer's answers to common clarifying questions</summary>

- Baseline ≈ 400 req/s across the platform, payday and month-end peaks ≈ 2,400 req/s for ≈ 90 minutes; writes
  (`POST /v1/transfers`) ≈ 10% of traffic. ≈ 1.2 million members; the largest credit union is ≈ 15% of traffic, the
  smallest ≈ 0.01%.
- Transfers settle through the credit union's core banking system via our adapters. Core: p50 ≈ 300 ms, p99 ≈ 9 s,
  99.5% available including ≈ 4 hours of scheduled maintenance per month; contractual limit ≈ 300 req/s and 50
  connections per credit union.
- Stateless tier on Kubernetes across 3 availability zones; one Postgres primary with replicas; a queue and a
  webhook dispatcher via an outbox. Autoscaling today: CPU 60% target, ≈ 4 minutes to add capacity.
- A transfer is "accepted" (we have durably recorded it) within a second, and "settled" when the core confirms:
  usually seconds, sometimes minutes or hours during core slowness. Partners learn of state changes by webhook.
- Credit unions have contractual expectations; two of the large ones ask for per-tenant reporting.
- On-call is one rotation of 6 engineers; the current pager load is ≈ 60 pages a week and most are noise.
- Duplicate vendor callbacks happen (≈ 0.5% of events); the dedup layer handles them. Regulators require a complete
  audit trail of money-movement actions.

</details>

## What to produce

1. **Requirements and assumptions**: the user journeys that matter, the NFRs with numbers, your clarifying questions
   and assumed answers.
2. **SLIs and SLOs** for at least three journeys (create transfer, read status, webhook delivery or settlement):
   each SLI as `good ÷ valid` with specification **and** implementation (what is valid, what is good, where it is
   measured), the SLO target and window, and what you would put in the SLA and why it differs. Say how unknown
   outcomes, shed requests, 4xx declines, synthetic traffic and core-banking maintenance windows are treated.
3. **Error budget** arithmetic: minutes and request counts per 30 days for your targets, a one-paragraph budget
   policy (what happens at 50%, 25%, 0% remaining; who may override).
4. **Alert design**: the burn-rate rules for the money-movement SLO as a table (long window, short window, derived
   threshold, severity) with the derivation of at least one threshold; the non-SLO alerts you keep (synthetic
   canary, absent traffic, invariants such as duplicate postings, a ticket-level duplicate-callback ratio); what
   you delete (the CPU page) and how you will measure alert quality.
5. **Telemetry plan**: which signals answer which questions (metrics / logs / traces), the label set for the main
   request metric with a cardinality estimate, correlation across the outbox and webhook hops, sampling policy,
   what must never appear in telemetry, and how audit records are kept separate.
6. **Latency measurement**: where the SLI is measured, the histogram design (type, bucket edges or accuracy, and
   why), and how you will compute a fleet-wide p99 per vendor correctly.
7. **Capacity plan**: a worked estimate for payday (average and peak, per-pod capacity assumption and how you would
   measure it, target utilisation, AZ-loss headroom, number of pods), the dependency ceilings (core connection pool,
   vendor rate limit, database) computed with Little's law, the autoscaling and pre-scaling approach, and a load
   test plan (open or closed loop, which tests, what pass looks like).
8. **Failure walkthroughs**: (a) the core is slow (p99 9 s) for an hour; (b) a 200-member credit union is down for
   three hours; (c) a 0.5% error rate persists for two days; (d) the metrics system is out of memory after someone
   adds a label.

## Rubric

| Dimension | below | approaching | meets (senior bar) | exceeds |
|---|---|---|---|---|
| SLIs and SLOs | "99.99% uptime"; SLI is a ping or a CPU number; no valid/good definition | SLIs named without exact definitions; one SLO for the whole API | Per-journey SLIs as good ÷ valid with specification and implementation, measured at the edge; SLA looser than SLO; declines, shed requests, canary traffic and unknown outcomes classified deliberately | Separates acceptance from settlement (freshness), proposes per-tenant fairness, ties targets to what the core can support, and treats invariants (duplicates, ledger mismatches) separately from budgets |
| Error budget and policy | No budget concept | Budget stated in minutes only | Minutes and request counts for each target, rolling window, a policy with thresholds and exceptions, explains why request-based budgets make night incidents cheap | Allocates budget across dependencies, uses it to justify spending (migrations, canaries) and to push back on unrealistic promises |
| Alert design | Alerts on CPU, queue depth or fixed error thresholds | Burn rate mentioned; one window; thresholds are round numbers | Multi-window multi-burn-rate pairs with thresholds derived from budget fraction × period ÷ window, page vs ticket split, canary and absent-data alerts, runbook links, minimum-event guard for low traffic | Quantifies precision, recall, detection and reset time of the design (or replays incidents), inhibition and grouping, per-tenant ticket rules, and alert tests |
| Signals, cardinality and correlation | "Log everything" or "put member_id on the metric" | Knows the three signals; cardinality unexamined | Right signal per question, bounded labels with the series count computed, trace IDs in logs and across the outbox, canonical log line, tail or error-biased sampling, PII rules, audit separated | Telemetry cost model (bytes/day), cardinality budget per team, drop-not-block policy, break-glass debug mode scoped by tenant |
| Percentiles and histograms | Average latency, or averages of per-pod p99 | Knows p99 but aggregates incorrectly or hand-waves buckets | Merges counts or sketches before taking quantiles; SLI as a ratio; bucket edge on the SLO threshold and the timeout (or log-linear sketch with stated error); explains interpolation error | Chooses and justifies the sketch parameters, explains coordinated omission and server-side under-reporting, per-vendor merged histograms |
| Capacity planning | "Add more servers" | Peak QPS divided by a guess | Demand estimate with peak factor and calendar, per-instance capacity at the SLO knee, 50–70% target utilisation, AZ-loss factor, Little's law pool ceilings, minimum over dependency limits, open-loop load tests with a step test and failure injection | Pre-scaling and load-shedding plan for the 6× cliff, cost of headroom quantified, forecast error tracking, retrograde-scaling awareness |
| Failure reasoning and trade-offs | Single answer per decision, ignores failures | Some failure cases when prompted | All four walkthroughs answered concretely (what alert fires, how long, what the on-call does), trade-off table with "when the answer flips" for at least three decisions | Anticipates follow-ups, quantifies trade-offs, says what to defer and what to measure first |

## Tutor critique protocol

1. Read the write-up once fully before scoring. Score each dimension independently using the rubric's words; quote
   the learner's own text as evidence for each score.
2. Check the arithmetic: budget minutes, burn-rate thresholds and the capacity estimate. A threshold or a capacity
   number off by 10× on something the design depends on caps the relevant dimension at "approaching".
3. Ask these probes, one at a time, live or in writing, without notes. Score the answers into the relevant
   dimensions:
   - "The core is down for its four-hour maintenance window. What happens to your SLI, your error budget, your
     alerts and your contract?"
   - "Show me the exact threshold for your one-hour page and derive it. What does it correspond to as an error rate?"
   - "A dashboard shows the average of the per-pod p99 at 280 ms and members are complaining. What is wrong and how
     do you fix it?"
   - "A 200-member credit union is completely down for three hours. Which alert fires, when, and what does the
     on-call see?"
   - "Payday is 6× baseline within ten minutes and your autoscaler needs four. Walk me through the first fifteen
     minutes, and what you did last week to prepare."
   - "Your load test passed at 500 virtual users and production browned out at half the tested load. What do you
     suspect, and how do you re-test?"
   - "Prometheus is OOM-killed after a deploy. How do you find the cause, and how do you stop it happening again?"
   - One probe of the tutor's choice on the learner's weakest dimension.
4. Overall level:
   - **meets** requires "meets" or better on *SLIs and SLOs*, *Alert design* and *Percentiles and histograms*
     (the three dimensions where a mistake stays silent and the dashboard looks fine) and no dimension at "below".
   - **exceeds** requires "meets" on every dimension and "exceeds" on at least three.
   - Otherwise **approaching** (or **below** if two or more dimensions are "below").
5. Feedback: three bullets — the strongest part, the one change that would most raise the score, and one concrete
   thing to re-read (link the section, for example [`unit-2-slos-capacity.md`](unit-2-slos-capacity.md) §3). If the
   result is below "meets", the learner redoes the exercise on a different day (variation: "the same questions for the
   webhook dispatcher and its partners"), same rubric.

## Recording

`sd record design A8 --self <below|approaching|meets|exceeds> --tutor <level> --notes "<one-line summary>"`
