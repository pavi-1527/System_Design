# D4 — Mock: full senior loop simulation

| | |
|---|---|
| Track | D — Mock interviews |
| Estimated time | 20 h (2 units: 8 + 12) — CORE 20 h (includes three loops of ≈ 4.5 h each; retakes on top) |
| Prerequisites | D1, D2, D3, A8, A9, C3, C4, C7, C8, C9, C10, C11, C12 (gates passed) |
| Labs | none: the machine-coding round is written in `_mocks/<date>-loop/mc/` |
| Gate | 3 loops ≥ meets (format `loop`, distinct problems): every round ≥ meets on its own rubric, and the component oral passes |

## Why this module exists

You've passed an LLD mock, a machine-coding mock and four HLD mocks, each one on a fresh morning with nothing before
it. A real senior loop is a different test: three or more rounds in one day, a ten-minute break between them, no idea
how the last round went, and a decision taken on the whole packet. Candidates who pass every round in isolation
regularly lose a loop on the third round: the machine-coding setup skipped because it's 3 p.m., the HLD structure
that slips after ninety minutes of code, a habit (not quantifying, say) that shows up in two rounds and reads as a
trait.

D4 is the course's final exam for the interview half of the goal. Each loop is an HLD round (`hld-45`), an LLD round
(`lld-45`) and a machine-coding round (`mc-90`), each on a problem you haven't seen, run exactly to the D3, D1 and D2
formats, followed by a twenty-minute oral on the building blocks themselves: why each exists, how it works inside,
how it fails, when not to use it. Three loops at meets, plus the oral covering all thirteen building blocks, are the
evidence for items 2, 3, 4 and 5 of the [course definition of done](../../SYLLABUS.md#course-level-definition-of-done).

## Outcomes

- **Sustain** three different interview formats back to back (≈ 3 h 20 min on the clock) with the same checkpoint
  discipline in round 3 as in round 1. — CORE
- **Switch** cleanly between modes (boxes and numbers → types and seams → code and tests) with a fixed break
  routine. — CORE
- **Explain** each of the 13 building blocks from first principles under follow-up questions: why it exists, how it
  works inside, how it fails, when not to use it. — CORE
- **Read** a loop as a committee would: rounds on their own rubrics, the minimum rule, and cross-round patterns, and
  turn it into one habit to fix. — CORE
- **Track** the course definition of done with checkable evidence for every item, and close the remaining gaps. —
  CORE
- **Pass** three loops at meets on nine unseen problems. — CORE

## Unit plan

| Unit | Topic | Hours | Core/Stretch | Lab |
|---|---|---|---|---|
| 1 | [The loop day](unit-1-loop-day.md): stamina and transitions; how a loop is decided; loop 1 | 8 | CORE (8) | — |
| 2 | [The component oral and the finish line](unit-2-oral-and-finish.md): first principles under cross-examination; reading your evidence; loops 2 and 3 | 12 | CORE (12) | — |

Total: 20 h CORE. A loop is ≈ 4.5 hours (≈ 3 h 20 min of rounds and oral, three 10-minute breaks, ≈ 40 minutes of
scoring and debrief), so three loops are ≈ 13.5 h of the 20. SYLLABUS.md's planning figure for D4 was 12 h; the
module carries the honest number. A loop below meets is redone on new problems, on top of these hours.

**Unit 1 (≈ 8 h)**
- Session 1 (1.5 h): §1, §2, and your one-page loop-day plan.
- Session 2 (≈ 4.5 h, one sitting): loop 1.
- Session 3 (≈ 2 h, another day): read the log, revisit, drill one weak phase.

**Unit 2 (≈ 12 h)**
- Session 1 (1.5 h): §1 with two oral drills; §2 and your definition-of-done tracker.
- Session 2 (≈ 4.5 h): loop 2.
- Session 3 (≈ 1.5 h): loop 2's log, revisits, the one habit for loop 3.
- Session 4 (≈ 4.5 h): loop 3 (one loop in the MC-first order, if the tutor hasn't used it yet).

**The kit:**

| File | What it is |
|---|---|
| [format.md](format.md) | The loop schedule, interviewer personas, problem selection, rules during the day, recording |
| [rubric.md](rubric.md) | Round rubrics (D3, D1, D2), the oral, the loop rule, cross-round signals, DoD evidence |
| [component-oral.md](component-oral.md) | The 13 building blocks: four questions each with must-hits, follow-ups and the rotation |
| [mock-log.md](mock-log.md) | The loop log; copy it to `_mocks/<YYYY-MM-DD>-loop/log.md` |
| D1/D2/D3 kits | Each round uses its own module's format, rubric, probe bank and problem bank |

## How this connects to your banking work

- **A loop is an on-call shift for your habits.** The vendor incident at 3 a.m. gets whatever you do automatically.
  So does round 3 at hour three. The loop-day plan (setup steps, break routine) is the same idea as a runbook.
- **Money problems in every loop.** At least one round per loop is money- or banking-flavoured (ledgers, transfers,
  payment rails, partner integrations). They're where your experience gives you the edge on requirements and failure
  probes, and where interviewers expect idempotency and audit to come up unprompted.
- **The oral's building blocks are your adapter stack.** Load balancer health checks, circuit breakers on flaky
  vendors, rate limits per credit union, queues for callbacks: you've operated most of these. The oral asks you to
  explain them from the inside, not from the dashboard.

## Mastery gate checklist

- [ ] Loop 1 at ≥ meets (`sd record mock D4 --format loop --problem "HLD … · LLD … · MC …" --minutes N --level <lvl> --notes "..."`) (date: ____)
- [ ] Loop 2 at ≥ meets, three new problems (date: ____)
- [ ] Loop 3 at ≥ meets, three new problems (date: ____)
- [ ] Component orals across the three loops covered all 13 building blocks (not a gate component; DoD item 5)

Only `loop` mocks count, a repeated loop counts once, and `sd record mock` refuses D4 loops while D4 is locked.
`sd status D4` is the source of truth; this list is for your own reading.

## What's next

D4 is the last module in `recommended_order`. Passing its gate isn't the end of the course by itself: the
[definition of done](../../SYLLABUS.md#course-level-definition-of-done) also needs every core gate passed with
nothing in needs refresh, Track E complete, estimation mastered and review accuracy ≥ 85%, all at the same time
(Unit 2 §2). After that, keep `sd review` going, a few `sd estimate` problems a week, and one mock a month in the
format of your next real interview.
