# A8 labs

Two Go packages, stdlib only, no network, no sleeps (time is always a parameter). Each stub compiles and returns
immediately (zero values or `errNotImplemented`), so every test fails fast until you implement it. Reference
solutions live in `solutions/a8/`; the `*_test.go` files there are byte-identical to the ones here — don't edit the
tests, edit the code.

Run everything the gate checks with `sd lab A8` (it runs `go test -race` over both packages). While working on one
package, run it directly as shown below.

| Order | Package | Unit | Core/Stretch |
|---|---|---|---|
| 1 | [`histogram`](histogram/) | 1 (§3–§4) | CORE |
| 2 | [`burnrate`](burnrate/) | 2 (§2–§3) | CORE |

---

## 1. `histogram` — mergeable latency histograms and fleet percentiles

**Goal.** The measuring half of observability: a concurrency-safe, mergeable, log-linear histogram (`Sketch`) whose
quantiles carry a proven relative-error bound; the Prometheus-style fixed-bucket histogram with its interpolating
`histogram_quantile`, so you can measure how wrong that can be; and three ways to answer "what is the fleet-wide
p99?" from per-instance data, two of them deliberately wrong.

**Edit** `labs/a8/histogram/histogram.go`. The package comment defines the sketch precisely (γ = (1+α)/(1−α), bucket
`i = ceil(log_γ v)`, representative `2γ^i/(γ+1)`, quantile rank `floor(q·(count−1))`, clamping to `[Min, Max]`).
Implement, in order:

1. `NewSketch`, `Record`/`RecordN` (reject NaN, ±Inf, negatives; zeros counted exactly; exact count, sum, min, max),
   `Count`, `Sum`, `Mean`, `Min`, `Max`, `BucketCount`, `RelativeError`.
2. `Quantile`, then `Merge` (snapshot the argument, then lock yourself: never hold two locks; reject a different α
   with `ErrIncompatible`).
3. `CountAtMost` (the SLI helper: a bracket `[lo, hi]` around "how many values are ≤ threshold").
4. `FixedBuckets` and `PromQuantile` (the Prometheus algorithm, including its edge cases).
5. `NaiveAverageQuantile`, `WeightedAverageQuantile`, `MergedQuantile`.

**Run:** `go test -race ./labs/a8/histogram/` (use `-run 'Sketch|Merge'`, `-run 'Count|Fixed|Prom'` or `-run Fleet` to
work on one part at a time).

**Break it:**
- `go test -tags breakit -run BreakIt_AverageOfP99s -v ./labs/a8/histogram/` — sweep the slow instance's traffic
  share and compare the true fleet p99 with the naive average, the request-weighted average and your merged sketch.
  Predict the table first.
- `go test -tags breakit -run BreakIt_BucketsStraddling -v ./labs/a8/histogram/` — an SLO threshold of 300 ms
  read from a fixed-bucket histogram without an edge near it, one with an edge on it, and the sketch.
- `go test -tags breakit -run BreakIt_FixedBucketQuantileError -v ./labs/a8/histogram/` — p50 / p99 / p99.9 from
  three bucket layouts vs exact vs the sketch.
- **Remove the mutex.** `breakit_on.go` / `breakit_off.go` define the constant `breakNoLock` (true only with
  `-tags breakit`). Route your locking through two helpers, for example
  `func (s *Sketch) lock() { if !breakNoLock { s.mu.Lock() } }` and the matching `unlock`, then run
  `go test -tags breakit -race -run Concurrent -v ./labs/a8/histogram/`. Expect the race detector to report a data
  race (and possibly the runtime's `fatal error: concurrent map writes`).

To change a scenario, copy `breakit_test.go` to a scratch file such as `breakit_scratch_test.go` (keep the
`//go:build breakit` line, rename the test functions) and edit the copy: the shared test files must stay
identical to the solution's.

Unit 1 §3–§4 has the expected observations.

---

## 2. `burnrate` — SLO tracker, error budget and multi-window burn-rate alerts

**Goal.** The promising half: a rolling-window SLO tracker with an explicit time parameter instead of a clock (so
nothing sleeps), the error-budget arithmetic (SLI over any trailing window, budget remaining, burn rate, time to
exhaustion), and an evaluator for Google-SRE-Workbook-style rules that fire only when a long **and** a short window
both burn faster than a threshold derived from the budget.

**Edit** `labs/a8/burnrate/burnrate.go`. The package comment pins the window semantics exactly (buckets of width
`Resolution` aligned to the Unix epoch; a window of `w` covers `ceil(w/Resolution)` buckets ending with the one that
contains `now`). Implement, in order:

1. `NewTracker` (validate the config; zero `Resolution` means one minute), `RecordAt` (reject `good > total`, accept
   out-of-order events, forget buckets older than `Period` without rescanning the whole map per event, drop events
   older than the retention), `Buckets`.
2. `SLI` (`ok == false` when the window has no events), `BurnRate`, `BudgetRemaining`, `TimeToExhaustion`.
3. `DefaultRules` (thresholds derived as `fraction × period ÷ long window`), `NewEvaluator` (validate and copy the
   rules), `Evaluate` (both windows must exist and be ≥ the threshold; `Short == 0` means no short window).

**Run:** `go test -race ./labs/a8/burnrate/` (use `-run 'SLI|Record|Tracker|Burn|Budget|Time'` for the tracker and
`-run 'Rules|Evaluat'` for the alerts).

**Break it:**
- `go test -tags breakit -run BreakIt -v ./labs/a8/burnrate/` — replay ten simulated days (3 days of harmless
  two-minute blips, a 20-minute outage, two days of a 0.5% slow burn, a 6-hour 10% brownout) through six alerting
  strategies, and print a timing table (outage detect and reset, slow-burn page and ticket, brownout detect) and a
  quality table (noise pages, alert episodes, precision, recall). Predict both tables first.
- Remove the short window (`Short: 0`) from a rule in a scratch copy and rerun; divide and then multiply every
  threshold by ten; set the baseline error rate to 0.3% (burn 3) and see which strategies page all the time.
- Make `SLI` return `(1, 0, true)` for an empty window and run `TestSLI_NoTrafficIsUnknownNotPerfect`; make a
  window cover one bucket too many and run `TestSLI_ExactFractionsOverTrailingWindows`.

Unit 2 §2–§3 has the expected observations.

---

## Reference

- Solutions: `go test -race ./solutions/a8/...`
- Break-it code is behind the `breakit` build tag, so default runs ignore it: `go vet -tags breakit ./labs/a8/...`
  type-checks it.
