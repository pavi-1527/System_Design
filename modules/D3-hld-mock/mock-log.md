# D3 Mock log template (`hld-45`)

Copy this file to `_mocks/<YYYY-MM-DD>-hld-45-<slug>/log.md` before the clock starts (for example
`_mocks/2027-03-14-hld-45-transaction-alerts/log.md`). The learner fills the phase timestamps and the self-score;
the tutor fills the clarifications, probes, scores and the record lines. Commit the log with `progress.json`.

The leading underscore in `_mocks/` keeps the folder out of `./...`, so nothing in it can break `./check.sh`.

---

## Header

| | |
|---|---|
| Date | YYYY-MM-DD |
| Module / format | D3 / `hld-45` |
| Problem (bank title) | |
| Bank id and nearest case studies | P__ · C__, C__ |
| Unseen? (why) | |
| Medium | whiteboard / paper / text |
| Clock start → stop | __:__ → __:__ |
| Minutes (actual) | |

## Phase timestamps

| Phase | Planned | Actual start | Slip | Note |
|---|---|---|---|---|
| Requirements & scope | 0:00 | | | |
| Estimation | 0:06 | | | |
| API | 0:10 | | | |
| Data model | 0:13 | | | |
| High-level design | 0:16 | | | |
| Requirement change released | ≈ 0:25–0:30 | | — | |
| Deep dives | 0:25 | | | |
| Bottlenecks, failures, evolution | 0:40 | | | |
| Stop | 0:45 | | | |

| Checkpoint | Target | Actual | On time / slipped / missed |
|---|---|---|---|
| A. Numbers on the board | 0:10 | | |
| B. End-to-end diagram, one request walked | 0:25 | | |
| C. Closing started | 0:40 | | |

## Clarifications given

| Clock | Question asked | Answer given | From the sheet or invented? |
|---|---|---|---|
| | | | |

## Requirements and assumptions (as the candidate stated them)

- Functional:
- Non-functional (with numbers):
- Out of scope:
- Assumptions made without asking:

## Estimates (candidate vs reference)

| Quantity | Candidate | Reference (bank) | Ratio | Decision it drove |
|---|---|---|---|---|
| Peak write QPS | | | | |
| Peak read QPS | | | | |
| Storage / year | | | | |
| Peak egress | | | | |

## Design summary

- API (calls):
- Data model (entities, keys, store, partition key):
- Diagram: link or paste (Mermaid/ASCII) the final state at 0:45.
- Deep dives chosen, and why:

## Requirement change

- Change released (clock, wording):
- How it landed (what changed, what didn't, cost named?):

## Probes

| Clock | Kind | Probe | Answer summary | Verdict (weak / ok / strong) |
|---|---|---|---|---|
| | scale-up / failure / consistency / cost / security / why-not-X | | | |

## Rubric

| # | Dimension | Self | Tutor | Evidence (timestamp, quote or number) |
|---|---|---|---|---|
| 1 | Requirements & scope | | | |
| 2 | Estimation | | | |
| 3 | API & data model | | | |
| 4 | High-level design *(gating)* | | | |
| 5 | Deep dives & trade-offs *(gating)* | | | |
| 6 | Probe handling *(gating)* | | | |
| 7 | Failure, operations & cost | | | |
| 8 | Communication & time *(gating)* | | | |

Caps applied (from [rubric.md](rubric.md)):

| | Self | Tutor |
|---|---|---|
| **Overall** (rule applied) | | |
| Counts towards the gate? (≥ meets, distinct, unseen) | — | |

## Debrief

Strengths (specific):
1.
2.
3.

Gaps, each with a fix and where to revisit it:
1. Gap: … · Fix: … · Revisit: (module, unit, §)
2.
3.

Calibration: largest self-vs-tutor gap and why:

## Record lines (copy exactly what was run)

```text
sd record mock D3 --format hld-45 --problem "<bank title>" --minutes <N> --level <overall> --notes "<top gap>"
sd record note D3 --tags mock,<tags> --notes "<pattern>"
```
