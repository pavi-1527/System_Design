# A8 Teach-back

## Prompt

A mid-level engineer on your team owns the dashboards and alerts for the Payments API. Their current setup is a
"p99 latency" panel built by averaging each pod's p99, a page on "CPU above 80%", and a contract line that promises
"five nines because we're a bank". They have heard the words SLO and error budget but have never used them.

Explain to them, in your own words, **how to know the Payments API is healthy, what it can sensibly promise, and
how to be paged only when it matters**. Cover: why their p99 panel is wrong and how to compute fleet-wide percentiles
correctly; what an SLI, SLO, SLA and error budget are for a money-movement endpoint; how burn-rate alerts with a long
and a short window replace the CPU page and how you would derive their thresholds; and how you would check that the
platform can survive the 1st-of-the-month traffic spike.

Form: written, 500–800 words, own words, may include one diagram (Mermaid or a sketch). No copying from the units:
if a sentence from the course appears verbatim, the evaluator will ask you to rephrase it. Write it as something you
would actually put in the team wiki, with at least one worked number of your own.

## Evaluator checklist

Must-hit (all required for pass):
- [ ] **Averages and percentiles**: the mean hides the tail; **a percentile of the union is not a function of the
      parts' percentiles**, so averaging per-pod p99s (even weighted) is wrong; the fix is to **merge counts**
      (histogram buckets or mergeable sketches) across pods and time and compute the quantile once at the end.
      Includes a concrete numeric illustration (for example 990 fast + 10 slow vs 980 fast + 20 slow).
- [ ] **SLI/SLO/SLA/budget**: the SLI is `good ÷ valid` events measured at the edge for a user journey (not a ping);
      the SLO is a target over a window; the SLA is looser than the SLO; the error budget is `1 − SLO`, with a
      worked number (99.9% over 30 days ≈ 43 minutes or a request count).
- [ ] **Burn rate**: defined as observed error rate ÷ allowed error rate (or the speed of budget consumption);
      time to exhaustion = period ÷ burn; and at least one threshold **derived** from budget fraction × period ÷
      window (for example 14.4 = 2% of a 30-day budget in one hour), not picked by taste.
- [ ] **Two windows**: the long window gives significance and the short window gives recency and fast reset; both
      must exceed the threshold; and a slow burn is a ticket while a fast burn is a page.
- [ ] **Symptoms over causes**: page on user-visible SLO burn plus a synthetic probe, not on CPU; says what
      happens to causes (dashboards, tickets); notes that burn alerts cannot see a failure that removes all traffic,
      hence the probe and an absent-data alert.
- [ ] **Capacity**: latency climbs sharply with utilisation (queueing), so plan for a target utilisation well below
      100% of the capacity measured at the SLO knee; headroom for losing an AZ; and a load test must be
      **open-loop** (arrival rate fixed), otherwise it can never show overload.

Should-hit (at least 4 of 7):
- [ ] Histogram design: a bucket edge on the SLO threshold (so the SLI is exact) or a log-linear sketch with a stated
      relative error; mentions the interpolation error of fixed buckets.
- [ ] Cardinality discipline: bounded labels (no member or request IDs on metrics), with a series-count
      calculation; high-cardinality questions go to logs and traces.
- [ ] Correlation: trace ID in logs and propagated across the outbox/webhook hop; error-biased (tail) sampling.
- [ ] An error-budget policy (what changes when the budget runs low or is spent) and the idea that unused budget is
      a resource to spend.
- [ ] Money-movement specifics: acceptance availability vs settlement freshness; unknown outcomes count as bad;
      duplicate postings as an invariant rather than a budget item; core maintenance windows in the contract.
- [ ] Per-tenant visibility: a global SLI hides a small credit union that is down.
- [ ] Alert quality: precision, recall, detection time and reset time, or the share of incidents found by customers,
      as the way to judge the alerting.

Red flags (any one = revise):
- [ ] Averages the per-instance p99s (or the p99s over time) to get a fleet or daily p99, or recommends the mean as
      the headline latency.
- [ ] Treats "no traffic" or "no data" as healthy, or uses a ping of `/healthz` as the availability SLI.
- [ ] Proposes five nines (or any target) without relating it to the budget, the dependencies or the cost.
- [ ] Chooses burn-rate thresholds or windows as round numbers with no derivation, or uses a single window for
      paging and does not notice the reset problem.
- [ ] Recommends a closed-loop load test as proof of capacity, or sizes capacity at 100% utilisation or without
      failure-domain headroom.
- [ ] Puts member or request identifiers in metric labels.

## Recording

`sd record teachback A8 --result <pass|revise> --notes "<missed items>"`
