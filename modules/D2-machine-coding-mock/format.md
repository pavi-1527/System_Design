# D2 Format — the 90-minute machine-coding mock (`mc-90`)

The clock, the checkpoints and the interviewer's script for every D2 mock and for the machine-coding round of a D4
loop. The learner reads this before the first mock; the tutor follows it during every mock.

## How a D2 mock differs from B3 practice

| | B3 timed attempt / design exercise | D2 mock |
|---|---|---|
| Problem | Known problem, or one of six published variants | Unseen, picked by the tutor from [problem-bank.md](problem-bank.md) (or an equivalent the tutor writes) |
| Stages | Stage files (`stage2.go`) or variant tables you can read | Announced by the tutor at 0:30 and 0:55; no hint files, nothing to read ahead |
| Tests | Provided suite is the scoreboard | **You write all of them.** After time, the tutor may add acceptance tests of their own |
| Interviewer | None during the attempt | Present: answers questions, watches the clock, probes during and after |
| Recorded as | `sd lab B3` / `sd record design B3` | `sd record mock D2 --format mc-90 …`, counts toward the gate only at ≥ meets |

The B3 playbook ([unit 8 §3](../B3-machine-coding/unit-8-stretch-and-capstone.md#3-the-machine-coding-round-playbook-and-capstone))
gives a general budget with Stage 1 green by 0:40 and narration inside the 90 minutes. D2 uses the marks of the B3
design exercise instead (stages at 0:00, 0:30, 0:55), because the interviewer announces stages on a fixed schedule
and the walk-through happens after the 90 minutes.

## Setup (before the clock starts, ≈ 5 min)

1. Pick the directory name: `_mocks/<YYYY-MM-DD>-mc-90-<slug>/`, for example `_mocks/2026-12-03-mc-90-concessions/`.
   The leading `_` keeps it out of `./...`, so `./check.sh` and `go vet ./...` never see a half-finished mock.
2. Copy [mock-log.md](mock-log.md) to `<dir>/log.md` and fill in the header.
3. Create the package: one `.go` file with `package <slug>` and an empty `_test.go`. Check that
   `go test -race ./_mocks/<dir>/` runs (it reports "no tests" or passes trivially).
4. Close every other file. `go doc` and the standard library docs are allowed, as in a real round; notes, earlier
   solutions and AI help are not.
5. The tutor runs `date` (or asks the learner to start a visible timer) and reads the prompt. That moment is 0:00.

## The clock

| Clock | Phase | What exists at the end | Rubric dimension it feeds |
|---|---|---|---|
| 0:00–0:03 | **Questions** | Answers to the six standard questions (scope, scale, concurrency, time, failure, likely next requirement), and the out-of-scope list said aloud | Requirements & modelling |
| 0:03–0:10 | **API and model** | Types and method signatures written in the file (bodies empty), sentinel errors, the injected clock, **the concurrency decision as one comment**, the seam you expect Stage 2 to need | Requirements & modelling; Interfaces & extensibility |
| 0:10–0:30 | **Stage 1: vertical slice** | The core use case end to end with its test, then the error table; something runs by ≈ 0:20 | Correctness; Tests; Code quality |
| 0:30 | **Stage 2 announced** | The tutor reads Stage 2. You say, before typing, which types change and which are added | Interfaces & extensibility |
| 0:30–0:55 | **Stage 2** | Stage 2 behaviour and its tests green; Stage 1 still green | Interfaces & extensibility; Tests |
| 0:55 | **Stage 3 announced** | The tutor reads Stage 3 (concurrency, failure, invariants, lifecycle) | — |
| 0:55–1:20 | **Stage 3** | As much as fits, honestly partial; at least one concurrent test with an invariant | Correctness & concurrency |
| 1:20–1:30 | **Feature freeze: concurrency pass and note** | `go test -race -count=20` run by you; the five-part concurrency note spoken (≈ 2 min); a one-line status per stage | Correctness & concurrency; Time management; Walk-through |
| 1:30–1:45 | **Walk-through and probes** (outside the 90) | The tutor's mechanical checks, then 4–6 probes from the problem and [probe-bank.md](probe-bank.md) | Walk-through & probe handling |
| 1:45–1:55 | **Debrief** | Self-score first, then the tutor's scores, top 3 strengths and gaps, the `sd record` line | — |

**Checkpoints** (the tutor writes the actual time of each in the log):

1. **0:10** — API and lock model written in the file. Not in your head; in the file.
2. **0:30** — Stage 1 tests green under `-race`.
3. **0:55** — Stage 2 tests green, Stage 1 still green.
4. **1:20** — feature freeze. No new behaviour after this, only the concurrency pass, fixes and the note.

The **hard floor** of the rubric is tied to checkpoint 2: if Stage 1 isn't green under `go test -race -count=20` at
1:30, the overall level is at most *approaching*, whatever else happened.

## Behind schedule

Say it, then cut. Cut in this order:

1. Optional methods and secondary operations (`Stats`, `List`, pretty output).
2. Polish: doc comments beyond the exported API, helper extraction, naming passes.
3. Secondary error cases (keep the ones that protect an invariant).
4. Breadth of Stage 3 (do one Stage 3 property properly rather than three badly).

**Never cut:** the tests of invariants, the concurrency pass at 1:20, or the spoken note. A mock that reaches all of
Stage 3 with no concurrency pass scores lower than one that stops halfway through Stage 3 with a clean `-race` run and
an honest status.

| You're at… | and you have… | Do this |
|---|---|---|
| 0:12 | no signatures in the file | Stop talking; write the types now, bodies `return nil` / zero values, then code |
| 0:30 | Stage 1 red | Tell the tutor; take Stage 2 anyway (it's announced on schedule), but finish Stage 1 first, quickly. Stage 1 green is the hard floor |
| 0:45 | Stage 2 needs a rewrite of Stage 1 | Say so and name the seam you missed; make the smallest change that lands Stage 2; note the lesson in the log |
| 1:05 | Stage 3 not started | Pick the single Stage 3 property with the highest risk (usually the conservation/at-most-once invariant), test it under concurrency, skip the rest |
| 1:20 | a flaky concurrent test | Freeze anyway. Reproduce with `-count=50`, read the race report, say what it is. Leave the failing test in place |

## The interviewer's script (tutor)

**Before 0:00.** Pick a problem the learner hasn't seen: check `mocks` in `progress.json` (D2 and D4) and `designs`
for B3 (its six variants are seen). Prefer a skill family the last mock didn't cover (see the skill tags in
[problem-bank.md](problem-bank.md)). Don't name the problem before the clock starts.

**0:00.** Read the problem's prompt verbatim, then: "You have 90 minutes. Build it in Go with tests, in your
`_mocks/` directory. I'll add requirements as we go. At the end I'll run your tests with `-race` and ask you to walk
me through it." Answer clarifying questions from the problem's answer sheet, with concrete numbers. If they ask
something the sheet doesn't cover, answer the way the sheet's spirit suggests and write the answer in the log. Don't
volunteer Stage 2 or 3. If they ask "what's the most likely next requirement?", give the one-line hint from the
problem's answer sheet (that's what a real interviewer does with a good question).

**0:10.** Look at the file. If there are no signatures, say once: "Can you show me the API you're going for?"
Write the actual time in the log.

**0:10–0:30.** Stay quiet. Note when something first runs and when the first test is written. Interrupt only if
they're clearly stuck on something outside the problem (a toolchain issue): fix it for them, note the minutes lost,
and don't count them.

**0:30.** Read Stage 2 verbatim, whatever state Stage 1 is in. Ask: "Before you start, what in your code changes?"
Write their answer in the log; compare it with what actually changes.

**0:55.** Read Stage 3 verbatim. If they're far behind, say: "Do what you can; tell me what you'd do for the rest."

**During Stages 2–3.** At most two mid-mock probes, at natural pauses (a test just went green): one from the
problem's list, typically the concurrency one ("what happens if two goroutines do this to the same key?"). Short
answers are fine; note whether they re-anchored on the clock.

**1:15.** "About five minutes until we freeze." Once.

**1:20.** "Feature freeze. Run your tests the way you'd want me to, then walk me through the concurrency." Time the
note. Stop them at ≈ 3 minutes.

**1:30–1:45.** Run the mechanical checks from [rubric.md](rubric.md) *before* reading the code in detail. Then ask
4–6 probes, one at a time, from the problem and from [probe-bank.md](probe-bank.md), including at least one scale-up,
one failure and one "why not X". Push one rung further on any vague answer ("what number would make you change
that?"). Don't teach. Save corrections for the debrief.

**1:45.** Ask for a self-score on every dimension with one line of evidence each, before saying anything evaluative.
Then score, apply the overall rule, debrief, and record.

**Never:** show the learner the problem bank, confirm whether a design choice is "right" during the 90 minutes, write
code for them, or extend the clock. If the learner asks for more time, the answer is "no, but tell me what you'd do".
