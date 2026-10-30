# D1 — Mock: 45-minute LLD/OOD

| | |
|---|---|
| Track | D — Mock interviews |
| Estimated time | 6 h (1 weekly unit) — CORE 6 h, STRETCH 0 h (includes three mocks with debriefs) |
| Prerequisites | B1, B2 (gates passed) |
| Labs | none: the mocks are the work. Any Go written during a mock goes in `_mocks/<YYYY-MM-DD>-lld-45-<slug>/` |
| Gate | 3 mocks ≥ meets (format `lld-45`, distinct problems) |

**Kit:** [format](format.md) (timed phases and checkpoints) · [rubric](rubric.md) (seven dimensions, overall rule) ·
[probe bank](probe-bank.md) (generic LLD probes by category) · [mock log template](mock-log.md) ·
[problem bank](problem-bank.md) (**interviewer-only**: reading it spoils your mocks).

## Why this module exists

You passed B1 and B2. You can put a one-method interface where the consumer needs it, you know when a strategy pays
and when a function is enough, and you absorbed the B2 requirement change at minute 25 with twenty new lines. Then
the real round starts. The prompt is "design a car-rental desk". The interviewer says nothing for four minutes,
then asks why `Fleet` is a struct and not an interface. At 0:19 you're still drawing boxes because nobody told you to
stop. At 0:31 they add "now customers can return the car to a different branch" and you realise the model you
sketched ties every car to one branch for life. At 0:42 they ask what happens if the card pre-authorisation times out
*after* the keys are handed over, and you've got three minutes and no plan.

Nothing in that story is a knowledge gap B1 or B2 didn't cover. What's missing is **performance under a
clock and an observer**: knowing which artefact must be on the board by which minute, recovering when a phase
overruns, absorbing a change you didn't choose, and answering a probe in ninety seconds without losing the
thread. Self-timed design exercises can't train that, because you control the pace and nobody interrupts.
A mock can, and only a scored mock tells you whether you're at the senior bar or just comfortable.

D1 is a kit, not a lesson: one short unit on running the 45 minutes, then three or more timed mocks with the tutor
playing the interviewer. Each mock is scored on the same seven-dimension rubric, logged, and recorded with
`sd record mock`.

## Outcomes

- **Run** a 45-minute LLD round to the clock: scope by 0:05, domain model with state owners by 0:12, the API as
  Go signatures by ≈ 0:15, a coded and tested core path by 0:30, and a clean stop at 0:45. CORE
- **Scope** an underspecified prompt into 3–5 requirements, an explicit out-of-scope list and the named axes of
  change that justify each interface. CORE
- **Absorb** a requirement change released at minute 30 as an addition behind an existing seam, or name the
  smallest edit when it lands on an axis you didn't open. CORE
- **State** who owns every piece of shared mutable state and which lock or goroutine guards it, and answer
  concurrency probes with a specific mechanism and its cost. CORE
- **Answer** LLD probes (scale-up, failure, concurrency, extensibility, testing, over-engineering, security,
  why not X) with claim → why here → cost → when it flips. CORE
- **Score** your own mock on the seven-dimension [rubric](rubric.md) with one line of evidence per dimension,
  within one level of the tutor's score. CORE

## Unit plan

| Unit | Topic | Hours | Core/Stretch | Lab |
|---|---|---|---|---|
| 1 | [The 45-minute LLD round under the clock](unit-1-lld-round.md) + three timed mocks | 6 | CORE (6) | — (mocks in `_mocks/`) |

The 6 hours are ≈ 2 h of reading and one dry run, plus ≈ 3 × (45 min mock + 15 min debrief) ≈ 3 h of mocks, plus
≈ 1 h of logging and revisiting the sections the debriefs point at. If a mock lands below *meets*, add ≈ 1 h for a
re-attempt on a different problem on another day.

**Unit 1** (CORE 6 h)
- Session 1 (2 h): §1 (the clock) and §2 (the minute-30 change), every Predict written down first. Read
  [format.md](format.md) and [rubric.md](rubric.md). Then a 45-minute **dry run** on a B3 problem you've already
  solved, timed and scored by yourself with [mock-log.md](mock-log.md). Don't record it.
- Session 2 (2 h): §3 (probes and self-scoring), skim [probe-bank.md](probe-bank.md), then **mock 1** with the tutor
  (45 min + 15 min debrief). Commit the log and `progress.json`.
- Session 3 (2 h): **mock 2** and **mock 3**, on different days if you can (each 45 + 15 min). Between them, revisit
  the section the previous debrief named.

## How this connects to your banking work

- **The minute-30 change is a product manager on a Tuesday.** "Credit union #14 needs a different overdraft-notice rule by
  Friday" is exactly the requirement change interviewers release at 0:30. You've seen what happens when the seam
  isn't there: a `switch` edited in five places. The mock checks that you put the seam where the prompt hinted
  variation, and nowhere else.
- **Vendor adapters are the classic LLD seam.** Many LLD prompts have an external dependency (a card processor, an
  insurer's eligibility check, a shipping carrier) that can time out after doing the work. Your duplicate-callback and
  idempotency instincts are the "failure" probe answers interviewers look for.
- **Audit trails are a modelling decision.** "Show me how this state got here" (an insurance claim, a membership
  change) is a
  question you've answered for compliance. In a mock it's evidence that your state transitions are explicit and
  recorded, not inferred.
- **Multi-tenancy changes the model, not just the database.** Per-credit-union configuration (limits, notification
  templates, branding) is where you get to show the security and tenant-isolation probe answers from real experience.

## Mastery gate checklist

- [ ] Mock 1 (`lld-45`) ≥ meets — problem: ____ (date: ____)
      `sd record mock D1 --format lld-45 --problem "<name>" --minutes <actual> --level <overall> --notes "<top gap>"`
- [ ] Mock 2 (`lld-45`) ≥ meets on a different problem — problem: ____ (date: ____)
      `sd record mock D1 --format lld-45 --problem "<name>" --minutes <actual> --level <overall> --notes "<top gap>"`
- [ ] Mock 3 (`lld-45`) ≥ meets on a different problem — problem: ____ (date: ____)
      `sd record mock D1 --format lld-45 --problem "<name>" --minutes <actual> --level <overall> --notes "<top gap>"`

The tutor records every mock, including those below *meets*; only passing mocks on distinct problems count toward
the gate. `sd record mock` refuses a locked module or a format other than `lld-45`. `sd status` is the source of
truth; this list is for your own reading.

## What's next

Passing D1 is one of the three prerequisites of D4, the full senior loop, alongside D2 (machine coding) and D3 (HLD).
If B3 has passed, D2 is the natural next step: it reuses the same habits with working code and a 90-minute clock.
Keep the debriefs' gaps alive: each one should have become a `sd record note D1 …` that the tutor reads before your
next mock, and `sd review` keeps the B1/B2 questions you missed in rotation.
