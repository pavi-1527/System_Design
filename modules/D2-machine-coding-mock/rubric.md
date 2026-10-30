# D2 Rubric — scoring a 90-minute machine-coding mock

Seven dimensions, each scored **below · approaching · meets · exceeds** with one line of evidence (a line of code, a
test, a timestamp, a quoted answer). Dimensions 1–6 extend the machine-coding rubric in [TUTOR.md](../../TUTOR.md)
§8 and the [B3 design exercise](../B3-machine-coding/design-exercise.md#rubric), so a B3 score and a D2 score mean the
same thing. Dimension 7 is new: in a real round the walk-through and the probes are graded, not just the code.

The scale, course-wide: **below** would fail a mid-level round; **approaching** is right direction but shallow or
needed prompting; **meets** is the senior bar (driven by you, tested, concurrency explicit, trade-offs stated);
**exceeds** is staff signal (anticipates the interviewer, reasons about evolution and cost).

## Mechanical checks (tutor, before reading the code in detail)

Run these at 1:30, in this order, and paste the output into the log. They are evidence for the dimensions in
brackets; they aren't scores on their own.

1. `gofmt -l ./_mocks/<dir>/` — anything listed is a [Code quality] note, not a fail.
2. `go vet ./_mocks/<dir>/` — `copylocks`, `lostcancel` or `loopclosure` findings are [Correctness] evidence.
3. `go test -race -count=20 ./_mocks/<dir>/` — the hard-floor check. Record pass/fail per stage (use `-run` with the
   learner's test names if needed) and any `DATA RACE` or `panic: test timed out`.
4. **Mutation.** Make one realistic change in a scratch copy (`cp -r` the directory to `_mocks/<dir>-mut/`): remove
   the lock from the hottest method, reverse a lock-acquisition order, or move a callback/send under the lock. Run
   the learner's tests with `-race -count=20`. Record whether any test failed and with what message. Delete the copy.
   [Tests]
5. **Acceptance (optional, recommended).** Write the problem's acceptance checklist as `acceptance_test.go` in a
   second scratch copy, adapted to the learner's API (don't ask them to change it), and run it with `-race`. Record
   which items fail. Failures count against [Correctness & concurrency], never against [Tests]: the learner didn't
   write those tests. Keep this to ≈ 10 minutes; skip items the learner explicitly descoped and said so.

Then: read `log.md`'s timeline, read the code top-down (types first), and check each claim from the spoken
concurrency note against the code.

## The rubric

| Dimension | below | approaching | meets (senior bar) | exceeds |
|---|---|---|---|---|
| **1. Requirements & modelling** | Starts typing without questions; one god struct; money as `float64` or raw `time.Now()` in logic; no out-of-scope list | Some questions, but misses one that changes the design (concurrency, time, what may be lost); the model needs reshuffling when Stage 2 arrives | Asks the questions that matter in ≤ 3 min and states assumptions; entities, invariants and errors named in the file by 0:10; money as integer minor units, time injected; non-goals said aloud | Asks for the likely next requirement and puts a seam exactly there; names an invariant the interviewer hadn't stated |
| **2. Interfaces & extensibility** *(gating)* | Stage 2 or 3 forces a rewrite of Stage 1; fat or producer-side interfaces; concrete dependencies (real network, real clock) everywhere | Stage 2 lands, but with edits across several existing functions; an interface exists but has 4+ methods or lives with the implementation | Small consumer-side interfaces (1–3 methods) at the axes that vary; Stage 2 lands mostly as additions (new type/option/method), and the learner predicted at 0:30 what would change; errors are sentinels or types callers match with `errors.Is/As` | Explains the alternatives rejected and their cost; the API makes misuse hard (constructor validates, zero value safe or impossible); predicts the Stage 3/4 change and where it would land |
| **3. Correctness & concurrency** *(gating)* | Races under `-race`, deadlock, wrong results under contention, or Stage 1 behaviour wrong | Correct on the happy path; check-then-act or a callback under a lock remains; the concurrency story is hand-wavy or doesn't match the code; ≥ 3 acceptance items fail | `-race -count=20` clean; the lock model was decided by 0:10 and holds (exported methods lock, helpers assume it); no user code, sends or I/O under the lock; invariants hold in a concurrent test; ≤ 2 acceptance items fail, none of them an invariant | Names the happens-before edges; shows a test that fails for each safeguard removed; discusses the next scaling step (sharding key, per-key locks) with a number and how they'd measure |
| **4. Tests** | None, or tests that sleep for real time and hope; the mutation survives | Happy path only, or tests written in a batch at the end; concurrency untested; the mutation survives | Written alongside the code (visible in the timeline); error table with `errors.Is`, an exact time boundary with an injected clock, an invariant check, one concurrent test; every wait has a timeout; **the mutation is caught** | A property-style or model-based test (random operations against a simple reference); the learner volunteers a mutation of their own that a test catches |
| **5. Code quality** | Panics for ordinary errors; dead code; unclear names; ignored errors (`_ = err`) | Mostly idiomatic; some premature abstraction, leftover debug output, or inconsistent error wrapping | Idiomatic Go; errors returned and wrapped with context; no premature abstraction; names say what things are; comments say why; `gofmt` and `vet` clean | Reads like production code; doc comments on the exported API could be the contract; a reviewer would merge it with nits |
| **6. Time management** *(gating)* | Nothing runs by 0:30; no tests by 0:55; no freeze, still adding features at 1:29 | Stage 1 green late (0:31–0:45); Stage 3 barely touched; the concurrency pass squeezed into the last two minutes | API by ≈ 0:10, Stage 1 green by ≈ 0:30, Stage 2 green by ≈ 0:55 (each within ≈ 5 min); honest partial Stage 3; freeze at 1:20 with the concurrency pass and note done; descoping said aloud | All checkpoints on time and the slack used deliberately: repeated stress runs, a refactor with tests green, or a Stage 4 sketch |
| **7. Walk-through & probe handling** | Can't explain own concurrency model; claims contradicted by the code; answers probes with "it's thread-safe" or brand names | Explains the code but the note misses parts (bounds, lifecycle, evidence); probe answers are generic or need two pushes to get a mechanism | Five-part note in ≈ 2 min, every claim true of the code; probe answers name a specific mechanism, give a number where one exists and a flip condition; admits limits precisely ("Stage 3 drain isn't bounded; I'd add…") | Anticipates probes in the note itself (10×, failure, what's next); flags an own flaw before the interviewer finds it, scopes it and fixes or tests it |

## Overall rule

- **Meets** = *meets* or better on every gating dimension (2 Interfaces & extensibility, 3 Correctness & concurrency,
  6 Time management) **and** no dimension at *below*.
- **Exceeds** = *meets* or better everywhere **and** *exceeds* on at least three dimensions, including at least two
  gating ones.
- **Otherwise** overall = the lowest gating-dimension level, and never above *approaching* if any dimension is
  *below*.
- **Hard floor:** if the Stage 1 tests aren't green under `go test -race -count=20` at 1:30, overall is at most
  *approaching*, whatever the dimensions say. A program that doesn't do the core job under the race detector isn't a
  senior result.

Worked example: Interfaces *meets*, Correctness *meets*, Time *approaching* (Stage 1 green at 0:41), Tests *exceeds*,
everything else *meets* → overall **approaching** (lowest gating dimension). The fix is about the clock, not the code.

## Calibration anchors

What the boundaries looked like in practice. Use them to keep scores consistent across mocks.

**Correctness & concurrency, approaching vs meets.** Cinema concession queue: the learner's `Submit`
takes the lock, appends the order to a station's queue and calls `notifyDisplay(order)` (which does a blocking channel
send to the pickup display) before unlocking. `-race` is clean because nothing races, but a stalled display blocks
every other kiosk's `Submit`. That's *approaching*: a send under the lock. Moving the send out of the critical
section (or making it non-blocking with a buffered signal channel) and showing a test where a stalled display doesn't
block submissions is *meets*.

**Interfaces & extensibility, approaching vs meets.** OAuth token-refresh cache: Stage 2 adds proactive refresh
(a token with less than 60 s left is returned at once while one background refresh starts). The learner adds a
`Refreshing bool` field to `Token`, a branch in `Token`'s body and a refresh started outside the lock; no signature
changes. That's *meets*. If Stage 2 instead forced them to change `Token`'s return type from `string` and rewrite
every test's assertions, it's *approaching*, even if it ends green.

**Tests, approaching vs meets.** The tutor removes the lock in `CheckIn` of a gym class roster. If the only
concurrent test calls `CheckIn` from 50 goroutines on 50 *different* classes, the mutation survives (no shared state
is touched), so Tests is *approaching* even with 20 tests. A test with 50 goroutines checking in to the *same* class
and checking the final attendance and version catches it.

**Time management, meets vs exceeds.** Both learners hit every checkpoint. One spends 1:12–1:20 adding a `Stats()`
method nobody asked for; the other runs `-count=100` and `-cpu 1,4`, finds and fixes an ordering flake, and sketches
Stage 4. The second is *exceeds*.

**Walk-through, meets vs exceeds.** Gym class roster, probe: "what breaks first at 10×?" *Meets:* "the single mutex: every operation
takes it; at ≈ 1 µs per critical section that's ≈ 10⁶ ops/s, so it's fine until a profile shows lock wait above
≈ 10–20%; then shard by class ID." *Exceeds:* that, plus "and before the lock, the unbounded waitlist per class: memory
grows with demand × class popularity, so I'd cap it and reject with a clear error; I'd watch waitlist length per
class."

## Calibration feedback to the learner

If the self-score is off by more than one level on any dimension, say so explicitly in the debrief and record it in
the log's calibration line. The common pattern is over-rating Correctness because `-race` was clean (clean `-race`
proves no data race was *observed*, not that invariants hold), and under-rating Time management after a stressful
but on-schedule mock.

## Recording

`sd record mock D2 --format mc-90 --problem "<problem name from the bank>" --minutes <actual coding minutes> --level <overall> --notes "<top gap, or top strength if exceeds>"`

Example: `sd record mock D2 --format mc-90 --problem "<problem name>" --minutes 90 --level approaching --notes "send under lock in Submit; Stage 1 green 0:38; mutation in the retry path survived"`
