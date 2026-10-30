# D4 Rubric — scoring a full loop

A loop is scored in three layers: each round on its own format's rubric, the component oral as pass/revise, then
one loop level from a fixed rule. Nothing in D4 changes the round rubrics; a round in a loop is held to exactly the
bar it was held to in D1–D3.

## Layer 1 — the rounds

| Round | Rubric | Gating dimensions | Mechanical checks before scoring |
|---|---|---|---|
| HLD (`hld-45`) | [D3 rubric](../D3-hld-mock/rubric.md) | High-level design · Deep dives & trade-offs · Probe handling · Communication & time | Checkpoint clocks (0:10, 0:25, 0:40); estimates vs the bank's reference (±25%) |
| LLD (`lld-45`) | [D1 rubric](../D1-lld-mock/rubric.md) | Domain model · Interfaces & extensibility · Trade-offs & probe handling · Communication & time | Checkpoint clocks; types edited by the requirement change |
| Machine coding (`mc-90`) | [D2 rubric](../D2-machine-coding-mock/rubric.md) | Interfaces & extensibility · Correctness & concurrency · Time management | `gofmt -l`, `go vet`, `go test -race -count=20` on `_mocks/<date>-loop/mc/`; one mutation; acceptance checklist; the Stage-1 hard floor |

Each round gets its overall level from its own rubric's overall rule and caps. The tutor scores each round
**immediately after it ends**, before the break, so a strong or weak later round can't colour the earlier score.

## Layer 2 — the component oral

Scored per building block with the checklist in [component-oral.md](component-oral.md): a block passes when all four
questions (why it exists, how it works inside, how it fails, when not to use it) are answered correctly with at most
one hint across the four. **Oral pass** = every block asked passes. One block at revise makes the oral *revise*, and
that block is asked again, first, in the next loop.

## Layer 3 — the loop level

| Loop level | Rule |
|---|---|
| **exceeds** | All three rounds ≥ meets, at least two rounds at exceeds, oral pass |
| **meets** | All three rounds ≥ meets, oral pass |
| **approaching** | Any round at approaching (and none below), or all rounds ≥ meets with the oral at revise |
| **below** | Any round at below |

Worked examples:

| HLD · LLD · MC · oral | Loop | Why |
|---|---|---|
| meets · meets · meets · pass | meets | the senior bar in every round |
| exceeds · meets · exceeds · pass | exceeds | two rounds at exceeds |
| exceeds · exceeds · approaching · pass | approaching | one round under the bar caps the loop |
| meets · meets · meets · revise | approaching | first principles are part of the definition of done |
| meets · below · exceeds · pass | below | a round that would fail a mid-level loop |

**Why stricter than many real loops.** Hiring committees often weigh a strong round against a weak one, and a single
"lean no" doesn't always sink a candidate (TODO-VERIFY: practice differs by company and level). The course's rule
is deliberately stricter: it's measuring mastery, not predicting an offer, and a loop with one round under the bar is
a clear signal of what to fix before the next. Three loops at meets under this rule leave margin for a real loop on
a bad day.

## Cross-round signals (debrief, not scoring)

The debrief starts with patterns across rounds, because a hiring committee reads the whole packet and a habit that
appears in two rounds reads as a trait, not an accident:

| Signal | Where it shows | What it usually means |
|---|---|---|
| Doesn't quantify | HLD estimation; MC "how many goroutines / how big is the queue" | Weak spot (c) not automatic under fatigue; more `sd estimate` reps |
| Requirement change lands as a rewrite | HLD delta; LLD minute-30 change; MC stage 2 | Seams chosen late; revisit the D1/D3 delta protocols |
| Time slips late in the day | Round 3 checkpoints worse than round 1's | Stamina; practise one loop in the MC-first order |
| Probe answers without an anchor | Any round | Revisit D3 Unit 2 §1; drill probe sprints |
| Concurrency hand-waving | LLD concurrency dimension; MC correctness | Revisit B0 and B3 Unit 8 §3's concurrency note |

## Definition-of-done evidence

Each passing loop is evidence for items of the [course definition of done](../../SYLLABUS.md#course-level-definition-of-done).
The tutor ticks them in the log:

| DoD item | Evidenced by a loop when |
|---|---|
| 2. HLD cold on an unseen problem | the HLD round is ≥ meets on every D3 dimension (not just overall) |
| 3. Machine coding on unseen problems | the MC round is ≥ meets (D2 + D4 together need two unseen problems at ≥ meets) |
| 4. Estimation in mocks within tolerance | every decision-driving HLD estimate is within ±25% of the bank's reference |
| 5. First principles | the oral passes; over three loops every one of the 13 building blocks has been asked and passed |

Items 1, 6 and 7 (all gates passed, Track E complete, review accuracy ≥ 85%) come from `sd status` and
`sd progress`, not from the loop.

## Calibration

Self-score each round after the loop, before the reveal. A two-level gap on any round's overall is noted with
`sd record note D4 --tags calibration`. By the last loop, self and tutor should agree on every round's overall level.
