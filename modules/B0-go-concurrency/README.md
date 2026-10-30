# B0 — Go concurrency for interviews

| | |
|---|---|
| Track | B — LLD / machine coding in Go |
| Estimated time | 13.5 h (2 weekly units) — CORE 12 h, STRETCH 1.5 h |
| Prerequisites | none |
| Labs | `labs/b0/` — `batcher`, `safemap`, `group`, `pipeline`, `fanout` (+ `breakit` exercises, build tag `breakit`) |
| Gate | quiz ≥ 85% ×2 (different days) · labs pass · design ≥ meets · teach-back pass |

## Why this module exists

An FX-quote endpoint on a credit-union banking platform calls five vendors. The first version works in every test
and in staging. In production, on a Monday, one vendor slows to 25-second responses. Members on flaky mobile
connections give up and retry, but the abandoned requests keep running: the adapter never passes the request's
context to its HTTP calls. In-flight calls to the slow vendor climb from ≈ 20 to ≈ 1,100 (Little's law: rate ×
latency), the vendor starts returning `429` to *everything* from the platform, and the healthy requests fail too.
Meanwhile a "first answer wins" helper elsewhere leaks one goroutine per call; RSS grows ≈ 500 MB an hour, and over a
holiday freeze with no deploys the pods are OOM-killed on day three. The audit writer's shared map takes the SSO
adapter down at 03:00 with `fatal error: concurrent map writes`, and every deploy drops a few hundred audit events
because the process exits on SIGTERM with a half-filled batch in memory.

None of these needs exotic knowledge. Each is a missing decision about **ownership, bounds, cancellation or
lifecycle** that Go makes easy to skip because goroutines are cheap and `go f()` is two keystrokes. Senior
machine-coding rounds probe exactly these decisions ("what happens if the consumer stops reading?", "how do you know
it doesn't leak?", "what if the vendor ignores your context?"), and every later multi-process project in this course
(Track E) assumes you can answer them with code and tests, not adjectives.

## Outcomes

- **Predict** the behaviour of short concurrent Go programs: output, deadlock, panic vs fatal error, race (close
  semantics, nil channels, `select` randomness, `RWMutex` writer preference, value-receiver locks, loop variables). CORE
- **Explain** the G/M/P scheduler model well enough to predict when goroutines cost threads (blocking syscalls) and
  when they don't (network I/O, timers), and why goroutine count is bounded by what goroutines hold, not their stacks. CORE
- **Choose between** unbuffered channel, buffered channel, mutex, `RWMutex`, atomics, `sync.Map` and sharding for a
  given sharing pattern, and say when the answer flips. CORE
- **Explain** happens-before in the Go memory model and what the race detector can and cannot find (and its
  documented overhead). CORE
- **Implement** a size-or-time batcher and a sharded concurrent map that pass `go test -race` with no leaks. CORE
- **Propagate** cancellation and deadlines with `context` (including `WithCancelCause`/`Cause`), and budget
  per-call timeouts inside an overall deadline. CORE
- **Implement** bounded concurrency (an errgroup clone with `SetLimit`/`TryGo`), leak-free pipelines with explicit
  ordering, and a vendor fan-out with per-call timeouts, an overall deadline, a concurrency cap and an explicit
  partial-results policy. CORE
- **Design** a graceful shutdown sequence that fits a platform's termination grace period. CORE
- **Test** concurrent code deterministically (injected clocks, event hooks, timeouts, leak checks, `-race -count`)
  and **narrate** a component's concurrency safety in a machine-coding round. CORE
- **Benchmark** locking strategies and explain contention vs cache-coherence costs with your own numbers. STRETCH

## Unit plan

| Unit | Topic | Hours | Core/Stretch | Lab |
|---|---|---|---|---|
| 1 | [Primitives and the memory model](unit-1-primitives.md) | 6 | CORE (5) / STRETCH (1) | `labs/b0/batcher`, `labs/b0/safemap`, `labs/b0/breakit` (races) |
| 2 | [Cancellation and structured concurrency](unit-2-cancellation.md) | 7.5 | CORE (7) / STRETCH (0.5) | `labs/b0/group`, `labs/b0/pipeline`, `labs/b0/fanout`, `labs/b0/breakit` (leaks, panics, deadlocks) |

**Unit 1** (CORE 5 h + STRETCH 1 h)
- Session 1 (2 h): §1 goroutines and the scheduler, §2 channels and `select`, with every Predict box written down
  before the reveal; start the `batcher` lab.
- Session 2 (1.5 h): finish `batcher`, do its Break it (buffered hand-off), and read §3 (locks and the `sync` toolbox).
- Session 3 (1.5 h): `safemap` lab, §4 memory model and race detector with the `breakit` race exercises, §5
  choosing the tool.
- STRETCH (1 h): §6 benchmarks (`go test -run '^$' -bench . -cpu 1,4 ./labs/b0/safemap/`), with your own
  token-cache benchmark.

**Unit 2** (CORE 7 h + STRETCH 0.5 h)
- Session 1 (2 h): §1 `context`, §2 bounded concurrency; `group` lab.
- Session 2 (2.5 h): §3 pipelines and leaks with the `pipeline` lab and `breakit` leak exercise; §4 the `fanout`
  capstone.
- Session 3 (2.5 h): §5 graceful shutdown, §6 pitfall catalogue (with `breakit` deadlock/panic/loopvar), §7
  testing and narration; then the timed design exercise (60 min) and the teach-back draft.
- STRETCH (0.5 h): §7's `testing/synctest` and production goroutine forensics.

## How this connects to your banking work

- **Vendor fan-out**: your image/vendor provider adapters are the `fanout` capstone: per-vendor timeouts, an
  overall deadline from the member-facing SLO, a concurrency cap from each vendor's contract, and a decision about
  partial results (best rate available vs all checks must pass, as in KYC).
- **Client disconnects**: when a member closes the app mid-request, `r.Context()` is cancelled; if your SSO or
  imaging adapter builds requests without it, you keep paying for work nobody wants (Unit 2 §1).
- **Not hammering vendors**: month-end statement image pulls and token refreshes for 40 credit unions at the top
  of the hour are bounded-concurrency problems (Unit 2 §2), not "add more goroutines" problems.
- **Audit trails**: batching audit writes (Unit 1 §2's `batcher`) cuts round trips ≈ 100×, but only an explicit
  backpressure policy, a correct `Close` and an outbox keep it regulator-safe across failovers and deploys
  (Unit 2 §5).
- **Shared caches in adapters**: SSO token caches and vendor-limit configs are the `safemap` and
  `atomic.Pointer` patterns (Unit 1 §3–§4); `fatal error: concurrent map writes` is not recoverable.

## Mastery gate checklist
- [ ] `sd quiz B0` ≥ 85% — attempt 1 (date: ____)
- [ ] `sd quiz B0` ≥ 85% — attempt 2 on a different day (date: ____)
- [ ] `sd lab B0` — all lab tests pass (with `-race`)
- [ ] Design exercise (`design-exercise.md`): self-score recorded, tutor score ≥ "meets"
      (`sd record design B0 --self <lvl> --tutor <lvl> --notes "..."`)
- [ ] Teach-back (`teachback.md`): tutor marks pass (`sd record teachback B0 --result pass --notes "..."`)

`sd status` is the source of truth; this list is for your own reading.

## What's next

B0 is the foundation for the rest of Track B (LLD / machine-coding modules build rate limiters, caches, job queues
and pub/sub on these primitives) and for Track E's multi-process projects, which assume you can write a bounded,
cancellable, leak-free component and prove it with tests. Before moving on, run `sd review` to revisit the B0 quiz
questions you missed (weak tags typically: `leaks`, `memory-model`, `context`), and re-do one Break it exercise
from each unit without looking at the answer.
