# D3 — Mock: 45-minute HLD

| | |
|---|---|
| Track | D — Mock interviews |
| Estimated time | 10 h (2 units) — CORE 10 h (includes four mocks with debriefs; retakes on top) |
| Prerequisites | A0, A1, A2, A3, A4, A5, A6, A7, C1, C2, C5, C6 (gates passed) |
| Labs | none: the mocks are the practice |
| Gate | 4 mocks ≥ meets (format `hld-45`, distinct problems, unseen) |

## Why this module exists

By the time D3 unlocks you've designed four case studies with a guided pass, then solo, then critiqued. That is
not the same as a design round. In a guided design the next question is printed under the last one. In a solo design
nobody interrupts you. In the real round, the interviewer answers your clarifying question with a number you didn't
expect, asks "what happens when this fails?" at 0:29 about the component you hoped they'd skip, adds "and partners
will retry on timeouts" while you're mid-diagram, and writes a score at 0:45 whether you've finished or not.

Most candidates who know enough to pass a senior HLD round fail it on exactly those moments: the 0:25 diagram that
isn't there because requirements took nine minutes, the probe answered with "it's replicated", the requirement
change that triggers a full redraw. They're weak spots (b) structuring the 45 minutes and (d) defending trade-offs
under follow-ups, and they only improve under realistic pressure, with an honest score and a log you can learn from.

D3 provides that pressure: a fixed clock with checkpoints, an interviewer who probes from a bank built for each case
study, one requirement change per mock, a senior-level rubric with gating dimensions, and a log per mock. Four mocks
at meets on problems you haven't seen is the course's evidence for "senior-bar HLD on unseen problems".

## Outcomes

- **Run** a 45-minute HLD round against the clock: numbers by 0:10, an end-to-end diagram by 0:25, closing by 0:40,
  stop at 0:45, and recover out loud when behind. — CORE
- **Answer** follow-up probes (scale-up, failure, consistency, cost, security, "why not X", "how would you know")
  from your own design in 30–90 seconds: anchor, mechanism, cost, flip. — CORE
- **Absorb** a mid-way requirement change (scale, topology, contract, constraint, product) as a delta in ≤ 3 minutes,
  naming what doesn't change and what the change costs. — CORE
- **Score** your own mock on the eight-dimension rubric with evidence, apply the caps and the overall rule, and land
  within one level of the tutor on every dimension. — CORE
- **Turn** each mock log into one concrete fix and a section to revisit, and track phase slips, probe verdicts and
  calibration across mocks. — CORE
- **Pass** four `hld-45` mocks at meets on distinct, unseen problems. — CORE

## Unit plan

| Unit | Topic | Hours | Core/Stretch | Lab |
|---|---|---|---|---|
| 1 | [The 45 minutes under a real clock](unit-1-clock-and-scoring.md): checkpoints as scoring events; scoring yourself like the interviewer; mock 1 | 5 | CORE (5) | — |
| 2 | [Under the probe ladder](unit-2-probes-and-changes.md): answering probes from your design; the mid-way requirement change; mocks 2–4 | 5 | CORE (5) | — |

Total: 10 h CORE. Each mock is ≈ 65 minutes (45 on the clock plus ≈ 20 for self-score, tutor score and debrief), so
four mocks are ≈ 4.5 h of the 10. A mock that scores below meets is redone on a different problem in a later sitting;
those retakes come on top of the estimate, as SYLLABUS.md says for all retakes.

**Unit 1 (≈ 5 h)**
- Session 1 (2 h): §1 checkpoints, the first-10 sprints.
- Session 2 (1.5 h): §2 scoring, both transcript excerpts.
- Session 3 (1.5 h): mock 1 with the tutor, debrief, log, revisit.

**Unit 2 (≈ 5 h)**
- Session 1 (1.5 h): §1 probe answers and a probe sprint on one passed case study; §2 the delta protocol and change
  drills.
- Sessions 2–4 (≈ 1.25 h each): mocks 2, 3 and 4, each with its debrief and log. Spread the four mocks over at least
  three different days.

**The kit** (read [format.md](format.md) and [rubric.md](rubric.md) before mock 1):

| File | What it is | Who reads it |
|---|---|---|
| [format.md](format.md) | The timed format: phase checklist, checkpoints, behind-schedule rules, the interviewer's script, recording | Both |
| [rubric.md](rubric.md) | Eight dimensions × four levels, gating dimensions, caps, overall rule, calibration anchors | Both |
| [probe-bank.md](probe-bank.md) | Follow-up probes per case study (C1–C16), with what a meets answer contains | Both: studying probe *kinds* is fine; the score is for answering them under your own design |
| [problem-bank.md](problem-bank.md) | Prompts, answer sheets with numbers, reference estimates and mid-way changes | **Tutor only**: reading it spoils your mocks |
| [mock-log.md](mock-log.md) | The log template; copy it to `_mocks/<YYYY-MM-DD>-hld-45-<slug>/log.md` per mock | Both |

## How this connects to your banking work

- **Vendor incidents are failure probes you've already answered for real.** "The image vendor's health endpoint is
  green but 30% of calls time out" is a Tuesday for an adapter team. In a mock, answer it the way you'd write the
  incident review: detection signal, containment (breaker, failover), recovery, what was lost.
- **Partners retrying aggressively is the most common contract change.** Your duplicate-callback and idempotent
  money-movement work is exactly the delta the interviewer wants: idempotency keys with stored responses, dedupe
  windows, and "same key, different payload" as an error.
- **Constraint changes are compliance in disguise.** Retention, audit trails, data residency and per-credit-union
  isolation are the requirements that reshaped your platform; when a mock adds one, classify it as a constraint and
  find where data hides.
- **Multi-tenancy as a security probe.** "Can one credit union see another's data?" is the security probe you
  should expect on any shared design; answer with where tenant id is enforced (token, query layer, storage key) and
  how you'd audit it.

## Mastery gate checklist

- [ ] Mock 1 at ≥ meets (`sd record mock D3 --format hld-45 --problem "..." --minutes N --level <lvl> --notes "..."`) — problem: ____ (date: ____)
- [ ] Mock 2 at ≥ meets, different problem — problem: ____ (date: ____)
- [ ] Mock 3 at ≥ meets, different problem — problem: ____ (date: ____)
- [ ] Mock 4 at ≥ meets, different problem — problem: ____ (date: ____)

Only `hld-45` mocks count, a repeated problem counts once, and `sd record mock` refuses D3 mocks while D3 is
locked. `sd status D3` is the source of truth; this list is for your own reading. Each mock has a log in `_mocks/`.

## What's next

Passing D3 is one of the prerequisites of D4, the full senior loop, together with D1, D2, A8, A9 and the remaining
core case studies (C3, C4, C7–C12). While you work towards those, keep the HLD habit alive: one probe sprint a week
on a case study you've just passed, and `sd review` for the A-track tags your mock logs flagged (often `consistency`,
`reliability` and `estimation`). Re-read your last two D3 logs before the first D4 loop.
