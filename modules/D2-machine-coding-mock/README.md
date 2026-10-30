# D2 — Mock: 90-minute machine coding

| | |
|---|---|
| Track | D — Mock interviews |
| Estimated time | 10 h (2 units, mocks included) — CORE 10 h |
| Prerequisites | B3 (gate passed) |
| Labs | none (the mock's own package in `_mocks/` is the lab) |
| Gate | 3 mocks ≥ meets (format `mc-90`, distinct problems) |

**The kit:** [format.md](format.md) (clock, checkpoints, interviewer script) · [rubric.md](rubric.md) (scoring and
the overall rule) · [probe-bank.md](probe-bank.md) (follow-up probes by category) · [mock-log.md](mock-log.md) (the
log you copy for every mock) · [problem-bank.md](problem-bank.md) (**tutor only**: reading it spoils your mocks).

## Why this module exists

You passed B3. You solved eighteen problems, each with a spec you could read, stage files you opened when you chose,
and tests that told you exactly what "done" meant. Now picture the real round. The interviewer reads out four
sentences about a cinema concession order queue you have never seen. There is no test file. At minute 30 they say
"combo orders are called only when every item in them is ready", and at minute 55 "a prep station can break down
mid-rush; don't lose orders and don't stall the other stations". At minute 90 they run `go test -race -count=20` on
*your* tests, then delete one line of your locking and run them again. Then they ask why you used a mutex and not a
goroutine per order, and what breaks first at 10× the orders.

Every one of those moments was in B3, but never all at once, never unseen, and never with someone watching the clock
and writing down what you said. Candidates who are strong in practice lose this round in three predictable ways: the
first ten minutes produce no written API, so Stage 2 lands as a rewrite; the tests only cover what the candidate
already believed worked, so the interviewer's mutation survives; and the walk-through claims a concurrency story the
code doesn't implement. D2 makes those failures visible, scores them the same way every time, and makes you pass
three times on three different problems before Track D's final loop (D4) opens.

## Outcomes

- **Run** an unseen 90-minute problem against the clock: API and lock model written by 0:10, Stage 1 green by 0:30,
  Stage 2 green by 0:55, feature freeze at 1:20, tests written alongside the code. CORE
- **Land** interviewer-announced Stage 2 and Stage 3 requirements as additions behind seams chosen in the first ten
  minutes, and report how much stage-1 code each one changed. CORE
- **Survive** the tutor's mechanical checks: `gofmt` and `go vet` clean, `go test -race -count=20` green, a deliberate
  mutation caught by a test, the acceptance checklist mostly passing. CORE
- **Deliver** the five-part concurrency note in ≈ 2 minutes and make every claim in it match the code. CORE
- **Answer** machine-coding probes (10× load, slow or panicking dependency, lock order, next requirement, test quality,
  "why not channels / `sync.Map`") with a mechanism, a number where one exists, and a flip condition. CORE
- **Score** your own mock on the seven-dimension rubric within one level of the tutor, applying the overall rule and
  the Stage 1 hard floor. CORE
- **Convert** each below-meets mock into a targeted re-attempt on a different problem. CORE

## Unit plan

| Unit | Topic | Hours | Core/Stretch | Lab |
|---|---|---|---|---|
| 1 | [Running the round under observation](unit-1-running-the-round.md) | 5 | CORE (5) | — (mock 1 in `_mocks/`) |
| 2 | [Defending the code: walk-through, probes and re-attempts](unit-2-defending-the-code.md) | 5 | CORE (5) | — (mocks 2 and 3 in `_mocks/`) |

Total: 10 h CORE. About 5.5 h of that is the mocks themselves: three mocks × (90 min coding + ≈ 20 min of mechanical
checks, probes and debrief). Unit 1 holds one mock, unit 2 holds two. A mock below *meets* adds one more mock
(≈ 2 h) on a different problem; budget for at least one.

**Unit 1 — Running the round under observation (CORE 5 h)**
- Session 1 (1.5 h): §1 (what changes when someone is watching), §2 (the first ten minutes). Write every Predict
  down. Then a 20-minute dry run of the first ten minutes on a B3 problem you have *already* solved, out loud.
- Session 2 (2 h): **mock 1** with the tutor (90 min + checks and debrief). Copy [mock-log.md](mock-log.md) first.
- Session 3 (1.5 h): §3 (stage seams and scoring yourself). Score mock 1 against [rubric.md](rubric.md) yourself
  *before* re-reading the tutor's scores, then compare.

**Unit 2 — Defending the code (CORE 5 h)**
- Session 1 (1 h): §1 (the walk-through and the concurrency note), §2 (probes), skim [probe-bank.md](probe-bank.md)
  and rehearse three probes aloud on your mock-1 code.
- Session 2 (2 h): **mock 2** (different problem, ideally a different skill family).
- Session 3 (2 h): **mock 3**; §3 (the re-attempt loop) if any mock scored below *meets*.

Don't run two mocks on the same day: the second one measures fatigue, not skill, and the debrief of the first should
change how you run the next.

## How this connects to your banking work

- **Your incident reviews are the probes.** Several bank problems are drawn from platform work like yours, so the
  follow-ups will sound familiar: a retry that must not apply twice, a dependency that's down for an hour, a deploy
  that lands in the middle of in-flight work. You've answered these after the fact; D2 asks for the answer in the
  code, before the incident.
- **The walk-through is a design review.** "Show me which lock protects that map, and which test proves the balance
  never goes negative" is the question a senior reviewer asks on a pull request to the payments service. D2 trains
  you to answer it in two minutes without hand-waving.
- **Mutation testing is the audit question.** "How do you know this control works?" is what an auditor asks about a
  reconciliation job. The tutor's mutation check asks the same of your tests.
- **Descoping out loud is change management.** Saying "I'm cutting `Stats`, finishing combo orders properly" at minute 60
  is the same call as cutting scope before a vendor go-live instead of shipping something half-tested.

## Mastery gate checklist

- [ ] Mock 1 at ≥ meets (problem: ____, date: ____)
      `sd record mock D2 --format mc-90 --problem "<name>" --minutes <actual> --level <overall> --notes "<top gap>"`
- [ ] Mock 2 at ≥ meets, on a different problem (problem: ____, date: ____)
      `sd record mock D2 --format mc-90 --problem "<name>" --minutes <actual> --level <overall> --notes "<top gap>"`
- [ ] Mock 3 at ≥ meets, on a third problem (problem: ____, date: ____)
      `sd record mock D2 --format mc-90 --problem "<name>" --minutes <actual> --level <overall> --notes "<top gap>"`

Only the tutor records mocks, and only after scoring them against [rubric.md](rubric.md). Mocks below *meets* are
recorded too (they're your history), but only one passing mock per distinct problem counts, and only the `mc-90`
format counts for D2. `sd record mock` refuses a mock while D2 is locked. `sd status` is the source of truth; this
list is for your own reading.

## What's next

Passing D2 is one of the three prerequisites of D4, the full senior loop (HLD + LLD + machine coding back to back),
along with D1, D3, A8, A9 and the remaining core case studies. The machine-coding round of each D4 loop uses this
module's format and rubric unchanged, on a problem from the bank you haven't seen.

Before moving on: commit your `_mocks/` directories and `progress.json`; run `sd review` for any B3 questions due;
and redo Stage 1 of your weakest mock's problem from scratch two weeks later, timed (retrieval, not recall). If
`sd progress` shows `concurrency` or `testing` among your weakest tags, the [probe bank](probe-bank.md) sections on
those categories are the place to start.
