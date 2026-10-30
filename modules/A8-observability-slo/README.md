# A8 — Observability, SLOs, capacity planning

| | |
|---|---|
| Track | A — HLD fundamentals |
| Estimated time | 12 h (2 weekly units) — CORE 10 h, STRETCH 2 h |
| Prerequisites | A7 (gate passed) |
| Labs | `labs/a8/` — `histogram`, `burnrate` (both core) |
| Gate | quiz ≥ 85% ×2 (different days) · labs pass · design ≥ meets · teach-back pass |

## Why this module exists

A credit-union platform serves 40 institutions at ≈ 3,000 req/s at peak. On a Tuesday at 02:10 the pager says
"transfers slow". The dashboard shows average latency 147 ms and is green. The logs are 500 GB a day and say nothing
about which hop is slow. The one trace anyone can find belongs to a fast request, because tracing samples 1% at random.
Ninety minutes later someone discovers that a single credit union's core connector is slow: one fortieth of the
traffic, invisible in every aggregate. The follow-up, "put tenant and member IDs on all metrics", takes Prometheus
down the next morning, because the metric that had 80,000 series now has millions.

Meanwhile the alerts are wrong in both directions. "CPU above 80%" pages 41 times a month for a nightly batch, and
doesn't fire when the core vendor returns 504 on 12% of balance lookups for three hours (CPU 25%: the threads are
waiting, not working). The contract promises "99.99% uptime", measured by pinging `/healthz`, which keeps answering
200 through a four-hour vendor maintenance window during which every transfer fails. Then the 1st of the month
arrives: traffic is 6× baseline, the autoscaler needs four minutes, the load test that "passed" was a closed loop
that can never offer more load than the system completes, and the vendor's per-credit-union connection limit was the
real ceiling all along.

None of this is exotic. It is what "how will you know it's working, what will you promise, and what happens at 10×?"
means in a senior interview, and what on-call actually is.

## Outcomes

- **Choose** between metrics, logs and traces for a debugging question; **estimate** series cardinality and log
  volume; **design** correlation (trace IDs, canonical log lines) across synchronous and asynchronous hops. — CORE
- **Apply** the golden signals, RED and USE, and **page on symptoms** (SLO burn, synthetic probes) while causes
  stay on dashboards and tickets. — CORE
- **Explain** why averages hide the tail and why percentiles cannot be averaged; **compute** fleet-wide percentiles
  by merging counts. — CORE
- **Compare** histogram designs (fixed buckets with interpolation, log-linear with a relative-error bound, HDR,
  t-digest), **place bucket edges** on SLO thresholds, and **derive** a sketch's error bound and bucket count. — CORE
- **Implement** a concurrency-safe, mergeable latency histogram with a guaranteed relative error, plus a
  Prometheus-style quantile estimator (`labs/a8/histogram`). — CORE
- **Measure** latency correctly: coordinated omission, open- vs closed-loop load, where to take the SLI. — CORE
- **Define** SLIs, SLOs and SLAs for a money-movement API (valid vs good events, request- vs time-based,
  per-tenant fairness) and **compute** error budgets. — CORE
- **Compute** burn rates and time to exhaustion, **derive** multi-window multi-burn-rate alert thresholds, and
  **evaluate** alert quality (precision, recall, detection and reset time). — CORE
- **Implement** a rolling SLO tracker and a multi-window burn-rate evaluator without sleeps
  (`labs/a8/burnrate`). — CORE
- **Plan** capacity: the queueing knee, Little's law for pools, headroom for AZ loss and deploys, calendar-driven
  forecasts, and open-loop load tests. — CORE
- **Design** telemetry pipelines that drop rather than block, sample sensibly, and keep sensitive data and audit
  records out of debug telemetry. — STRETCH
- **Compose** SLOs across serial dependencies and **define** freshness SLOs for asynchronous pipelines. — STRETCH

## Unit plan

| Unit | Topic | Hours | Core/Stretch | Lab |
|---|---|---|---|---|
| 1 | [Seeing the system: signals, golden signals, percentiles, histograms, measuring latency](unit-1-seeing-the-system.md) | 6 | CORE (5) / STRETCH (1) | `labs/a8/histogram` |
| 2 | [Promising and planning: SLIs/SLOs, error budgets, burn-rate alerts, alert quality, capacity](unit-2-slos-capacity.md) | 6 | CORE (5) / STRETCH (1) | `labs/a8/burnrate` |

**Unit 1 (6 h)**
- Session 1 (2 h): warm-up; §1 signals and cardinality (predict, the correlation snippet, the cardinality toy);
  §2 golden signals and symptom alerts (predict, the RED middleware and its break-it).
- Session 2 (2 h): §3 percentiles (predict, then lab part 1: `Sketch`, `Merge`, the fleet helpers); the
  "average the p99s" and "remove the mutex" break-its.
- Session 3 (1 h core + 1 h stretch): §4 histogram designs (lab part 2: `CountAtMost`, `FixedBuckets`,
  `PromQuantile`; the bucket-straddling break-it); §5 coordinated omission (run the simulation).
  STRETCH (1 h, any time): §6 telemetry pipelines.

**Unit 2 (6 h)**
- Session 1 (2 h): warm-up; §1 SLIs, SLOs, SLAs (predict, the classification snippet, the request- vs time-based
  program); §2 error budgets and burn rate (predict; lab part 1: `Tracker`).
- Session 2 (2 h): §3 multi-window burn-rate alerts (predict; lab part 2: `DefaultRules`, `NewEvaluator`,
  `Evaluate`); run the alert-strategy harness and read both tables.
- Session 3 (1 h core + 1 h stretch): §4 alert quality (scoring program); §5 capacity (queueing and open/closed
  loop simulations). STRETCH (1 h, any time): §6 composing SLOs and pipeline freshness.
- Then `sd lab A8`, the design exercise (45 min + self-score) and the teach-back write-up; first `sd quiz A8`
  attempt, and the second on a later day.

## How this connects to your banking work

- **Vendor adapters**: every adapter deserves RED per vendor (calls by outcome class, duration histogram for
  successful calls, in-flight against the bulkhead limit, breaker state). Unit 1 §2–§4 explains why the vendor's p99
  must come from merged histograms with a bucket edge at your timeout, not from averaged per-pod percentiles.
- **Vendor latency and timeouts**: Little's law (Unit 2 §5) turns "core call = 300 ms at 2,400 req/s" into 720
  connections in flight and a pool ceiling of ≈ 167 req/s per 50 connections, whatever your pod count.
- **Duplicate callbacks**: a rising duplicate-callback ratio is a leading indicator (slow acknowledgements, vendor
  retries), not a budget event. Unit 1 §2 and Unit 2 §3 show where it belongs: a dashboard panel and a ticket
  threshold, not a page. Trace context travels through the outbox so a callback links back to its request.
- **Money movement**: Unit 2 §1–§2 separate *acceptance* availability from *settlement* freshness, count unknown
  outcomes as bad, and treat duplicate postings and ledger mismatches as invariants (target zero, alert on any),
  not as budget items. The core's maintenance windows are budget events you can forecast.
- **Multi-tenant credit unions**: a global SLI hides a tiny credit union that is entirely down; per-tenant SLIs,
  per-tenant probes and a cardinality plan (tenants yes, member IDs never) come straight from Unit 1 §1 and
  Unit 2 §1.
- **Audit trails**: audit records are a regulated, complete, tamper-evident stream; debug telemetry is sampled and
  droppable. Unit 1 §6 keeps them in separate pipelines.

## Mastery gate checklist

- [ ] `sd quiz A8` ≥ 85% — attempt 1 (date: ____)
- [ ] `sd quiz A8` ≥ 85% — attempt 2 on a different day (date: ____)
- [ ] `sd lab A8` — all lab tests pass (with `-race`)
- [ ] Design exercise ([`design-exercise.md`](design-exercise.md)): self-score recorded, tutor score ≥ "meets"
      (`sd record design A8 --self <lvl> --tutor <lvl> --notes "..."`)
- [ ] Teach-back ([`teachback.md`](teachback.md)): tutor marks pass (`sd record teachback A8 --result pass --notes "..."`)

`sd status` is the source of truth; this list is for your own reading.

## What's next

Passing A8 unlocks C15 (design a metrics and monitoring system, where this module's histograms, cardinality and
burn-rate material become the case study) and counts towards D4, the full senior loop, which expects you to state
SLOs and a capacity plan unprompted. Before moving on, run `sd review` and clear any A8 questions it
surfaces, especially tags `percentiles`, `histograms` and `burn-rate`; they reappear whenever a design says "p99
latency". Then run a few `sd estimate -kind servers` and `-kind concurrency` drills, which are this module's capacity
arithmetic under time pressure.
