# D1 Mock log template (`lld-45`)

Copy this file to `_mocks/<YYYY-MM-DD>-lld-45-<slug>/log.md` before the mock starts (for example
`_mocks/2026-11-14-lld-45-car-rental/log.md`). The `_` prefix keeps the folder out of `go vet ./...`, `go test ./...` and
`./check.sh`; any Go you write during the mock goes in the same folder and is checked explicitly with
`go vet ./_mocks/<dir>/` and `go test -race ./_mocks/<dir>/`. Fill the timestamp table **during** the mock. Commit the
log together with `progress.json` after the debrief.

---

## Header

| | |
|---|---|
| Date | YYYY-MM-DD |
| Format | `lld-45` |
| Module | D1 |
| Problem | <name from the problem bank, exactly as recorded> |
| Interviewer | tutor (<session>) |
| Clock start / end | hh:mm / hh:mm |
| Minutes (actual) | NN |

## Phase timestamps

| Phase | Planned | Actual start | Slip (min) | Note |
|---|---|---|---|---|
| Clarify & scope | 0:00 | | | |
| Domain model | 0:05 | | | |
| Interfaces & main flow | 0:12 | | | |
| Signatures on the board (checkpoint) | ≤ 0:15 | | | |
| Core path in code | 0:20 | | | |
| Core path coded (checkpoint) | 0:30 | | | |
| Requirement change released | 0:30 | | | |
| Change landed (checkpoint) | ≤ 0:37 | | | |
| Concurrency, edge cases, probes | 0:37 | | | |
| Stop | 0:45 | | | |

## Requirements and assumptions

- Functional requirements (as agreed):
- Out of scope:
- Axes of change named:
- Clarifying questions asked → answers given (or assumptions stated):

## Model and interfaces (summary)

- Entities and owners of mutable state:
- Invariants stated:
- Interfaces (name, consumer, axis of change):
- Concurrency model:

## The requirement change

- Change as released (verbatim):
- Where it landed (seam / new type / edits to existing types, with count):
- Lines changed (≈):
- Verdict (addition / small edit / rewrite):

## Probes

| # | Category | Probe | Answer summary | Verdict (below…exceeds) |
|---|---|---|---|---|
| 1 | | | | |
| 2 | | | | |
| 3 | | | | |
| 4 | | | | |

## Rubric

| Dimension | Self | Tutor | Evidence (quote, timestamp, code line, probe #) |
|---|---|---|---|
| 1 Requirements & scope | | | |
| 2 Domain model ★ | | | |
| 3 Interfaces & extensibility ★ | | | |
| 4 Concurrency & correctness | | | |
| 5 Code & testability | | | |
| 6 Trade-offs & probe handling ★ | | | |
| 7 Communication & time ★ | | | |

## Overall

| | Level |
|---|---|
| Self | |
| Tutor | |
| Rule applied | e.g. "gating all meets, no below → meets" or "lowest gating = approaching (dim 6)" |

## Debrief

Top 3 strengths (specific):
1.
2.
3.

Top 3 gaps, each with a concrete fix and where to revisit it:
1. Gap: … → Fix: … → Revisit: <module/section link>
2. Gap: … → Fix: … → Revisit: …
3. Gap: … → Fix: … → Revisit: …

Calibration delta (dimensions where self and tutor differ by > 1 level, and why):

## Recorded

```text
sd record mock D1 --format lld-45 --problem "<name>" --minutes <actual> --level <overall> --notes "<top gap>"
sd record note D1 --tags lld,<tags> --notes "<pattern worth remembering>"
```
