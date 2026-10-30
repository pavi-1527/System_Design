# D4 Loop log template (`loop`)

Copy this file to `_mocks/<YYYY-MM-DD>-loop/log.md` before round 1. The machine-coding round's Go package goes in
`_mocks/<YYYY-MM-DD>-loop/mc/`. The tutor fills each round's scores privately right after the round; the learner
self-scores only after the oral. Commit the folder with `progress.json`.

---

## Header

| | |
|---|---|
| Date | YYYY-MM-DD |
| Module / format | D4 / `loop` |
| Order | HLD → LLD → MC → oral (default) / MC → HLD → LLD → oral |
| Loop number | 1 / 2 / 3 (+ retakes) |
| Clock start → end | __:__ → __:__ |
| On-clock minutes (sum of rounds + oral) | |

## Schedule (actual)

| Block | Planned (loop clock) | Actual start | Actual end | Note |
|---|---|---|---|---|
| Round 1 | 0:00–0:45 | | | |
| Break | 0:45–0:55 | | | |
| Round 2 | 0:55–1:40 | | | |
| Break | 1:40–1:50 | | | |
| Round 3 | 1:50–3:20 | | | |
| Break | 3:20–3:30 | | | |
| Component oral | 3:30–3:50 | | | |

## Round summaries

For each round, keep the condensed version here; the full detail follows that format's own log template
([D3](../D3-hld-mock/mock-log.md), [D1](../D1-lld-mock/mock-log.md), [D2](../D2-machine-coding-mock/mock-log.md))
and can be appended below this file.

### HLD (`hld-45`)

| | |
|---|---|
| Problem (bank title, id) | |
| Checkpoints A / B / C | __ / __ / __ |
| Requirement change and how it landed | |
| Estimates vs reference (worst ratio) | |
| Probes (kind → verdict) | |

| Dimension | Tutor (private, right after the round) | Self (after the oral) | Evidence |
|---|---|---|---|
| Requirements & scope | | | |
| Estimation | | | |
| API & data model | | | |
| High-level design *(gating)* | | | |
| Deep dives & trade-offs *(gating)* | | | |
| Probe handling *(gating)* | | | |
| Failure, operations & cost | | | |
| Communication & time *(gating)* | | | |
| **Round overall** | | | |

### LLD (`lld-45`)

| | |
|---|---|
| Problem (bank title) | |
| Checkpoints | |
| Requirement change at 0:30: types edited, time to land | |
| Probes (kind → verdict) | |

| Dimension | Tutor | Self | Evidence |
|---|---|---|---|
| Requirements & scope | | | |
| Domain model *(gating)* | | | |
| Interfaces & extensibility *(gating)* | | | |
| Concurrency & correctness | | | |
| Code & testability | | | |
| Trade-offs & probe handling *(gating)* | | | |
| Communication & time *(gating)* | | | |
| **Round overall** | | | |

### Machine coding (`mc-90`)

| | |
|---|---|
| Problem (bank title) | |
| Green at 0:30 / 0:55 / 1:30 | |
| `gofmt` / `vet` / `-race -count=20` | |
| Mutation applied → did a test fail? | |
| Acceptance checklist (passed / total) | |
| Stage-1 hard floor triggered? | |

| Dimension | Tutor | Self | Evidence |
|---|---|---|---|
| Requirements & modelling | | | |
| Interfaces & extensibility *(gating)* | | | |
| Correctness & concurrency *(gating)* | | | |
| Tests | | | |
| Code quality | | | |
| Time management *(gating)* | | | |
| Walk-through & probe handling | | | |
| **Round overall** | | | |

## Component oral

| Block | Why | Inside | Fails | Not | Hints | Verdict | Missed must-hits |
|---|---|---|---|---|---|---|---|
| | ✓/✗ | ✓/✗ | ✓/✗ | ✓/✗ | 0/1/2 | pass / revise | |

Oral: **pass / revise**

## Loop level

| HLD | LLD | MC | Oral | Loop level (rule in [rubric.md](rubric.md)) |
|---|---|---|---|---|
| | | | | |

## Cross-round patterns

- Pattern seen in ≥ 2 rounds:
- Late-day slip (round 3 vs round 1):

## Definition-of-done evidence (tick what this loop evidences)

- [ ] 2. HLD cold: HLD round ≥ meets on every dimension
- [ ] 3. Machine coding: MC round ≥ meets on an unseen problem
- [ ] 4. Estimation: every decision-driving HLD estimate within ±25% of the reference
- [ ] 5. First principles: oral pass (blocks covered so far across loops: __ / 13)

## Debrief

Strengths:
1.
2.
3.

Gaps, each with a fix and where to revisit it:
1. Gap: … · Fix: … · Revisit: (module, unit, §)
2.
3.

Calibration (largest self-vs-tutor gap by round):

## Record lines (copy exactly what was run)

```text
sd record mock D4 --format loop --problem "HLD <title> · LLD <title> · MC <title>" --minutes <N> --level <loop level> --notes "HLD <lvl> · LLD <lvl> · MC <lvl> · oral <pass|revise> · <top gap>"
sd record note D4 --tags loop,<tags> --notes "<cross-round pattern>"
```
