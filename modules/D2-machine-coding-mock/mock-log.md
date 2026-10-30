# D2 Mock log template (`mc-90`)

Copy this file to `_mocks/<YYYY-MM-DD>-mc-90-<slug>/log.md` before the clock starts. The learner fills in the
header, the timeline and the self-scores; the tutor fills in the checks, probes, tutor scores and the record lines.
Commit the directory (code, tests and this log) together with `progress.json` at the end of the session.

Write times as clock offsets (`0:27`), not wall-clock times. Paste test output rather than summarising it.

---

## Header

| | |
|---|---|
| Date | YYYY-MM-DD |
| Format | `mc-90` |
| Module | D2 (or D4 loop, round 3) |
| Problem | <name exactly as in the problem bank> |
| Skill family | <as tagged by the tutor after the mock> |
| Clock start (wall) | HH:MM |
| Clock end (wall) | HH:MM |
| Coding minutes | <actual, normally 90> |
| Directory | `_mocks/<YYYY-MM-DD>-mc-90-<slug>/` |

## Questions asked and assumptions stated (0:00–0:10)

| Question / assumption | Answer given, or assumption made |
|---|---|
| Scope / out of scope | |
| Scale and shape | |
| Concurrency | |
| Time | |
| Failure: what may be lost, what must not | |
| Likely next requirement | |

Concurrency decision (copy the comment from the code): `...`

Seam left for the expected change: `...`

API and lock model in the file at: `0:__`

## Stage timeline

| Checkpoint | Target | Actual | State (paste `go test -race` summary lines) |
|---|---|---|---|
| API + lock model written | 0:10 | | |
| Something runs | ≈ 0:20 | | |
| Stage 1 green | 0:30 | | |
| Stage 2 green (Stage 1 still green) | 0:55 | | |
| Feature freeze | 1:20 | | |
| Final run (`-race -count=20`) | 1:30 | | |

What was cut, and when it was said aloud:

-

## Stage announcements and how each landed

| Stage | Announced at | Learner's prediction of what changes | What actually changed (files, functions, ≈ lines) | Additive? |
|---|---|---|---|---|
| 2 | 0:30 | | | yes / mostly / no |
| 3 | 0:55 | | | yes / mostly / no |

## Mechanical checks (tutor)

| Check | Command | Result |
|---|---|---|
| gofmt | `gofmt -l ./_mocks/<dir>/` | |
| vet | `go vet ./_mocks/<dir>/` | |
| race, repeated | `go test -race -count=20 ./_mocks/<dir>/` | Stage 1: pass/fail · Stage 2: · Stage 3: |
| Mutation | <what was changed, in which function> | caught by `<TestName>` / survived |
| Acceptance | `acceptance_test.go` from the problem's checklist | <n>/<m> pass; failing items: |

Hard floor (Stage 1 green under `-race -count=20` at 1:30): **met / not met**

## Concurrency note (spoken at 1:20, ≈ 2 min)

| Part | What was said | True of the code? |
|---|---|---|
| Shared state and owners | | |
| Bounds | | |
| Lifecycle | | |
| Failure handling | | |
| Evidence | | |

Duration: `_ min`

## Probes asked

| # | When | Probe (category) | Answer summary | Verdict (weak / ok / strong) |
|---|---|---|---|---|
| 1 | mid-mock | | | |
| 2 | 1:3x | | | |
| 3 | | | | |
| 4 | | | | |
| 5 | | | | |

## Rubric

| Dimension | Self | Tutor | Evidence (line, test, timestamp or quote) |
|---|---|---|---|
| 1. Requirements & modelling | | | |
| 2. Interfaces & extensibility *(gating)* | | | |
| 3. Correctness & concurrency *(gating)* | | | |
| 4. Tests | | | |
| 5. Code quality | | | |
| 6. Time management *(gating)* | | | |
| 7. Walk-through & probe handling | | | |

## Overall

| | |
|---|---|
| Self overall | |
| Tutor overall | |
| Rule applied | e.g. "lowest gating = approaching (Time)"; "hard floor not met → capped at approaching"; "meets: all gating ≥ meets, none below" |

## Debrief

Top 3 strengths:

1.
2.
3.

Top 3 gaps (each with a concrete fix and where to revisit it):

| Gap | Fix to practise | Revisit |
|---|---|---|
| | | e.g. [B3 unit 8 §3](../../modules/B3-machine-coding/unit-8-stretch-and-capstone.md), [D2 unit 2 §2](../../modules/D2-machine-coding-mock/unit-2-defending-the-code.md) |
| | | |
| | | |

Calibration delta (dimensions where self and tutor differ by more than one level, and why):

-

Next mock should target (skill family / habit):

-

## Record lines (tutor runs these)

```text
sd record mock D2 --format mc-90 --problem "<name>" --minutes <n> --level <overall> --notes "<top gap>"
sd record note D2 --tags machine-coding,<tag> --notes "<pattern worth remembering across sessions>"
```
