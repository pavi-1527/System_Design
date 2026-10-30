# D4 Format — the full senior loop (`loop`)

One loop is three interview rounds and a short component oral, back to back on the same day, each round on a problem
the learner hasn't seen. It reuses the D1–D3 formats exactly. What D4 adds is the day itself: stamina, transitions,
no feedback between rounds, and a loop-level decision.

## The schedule

| Clock (loop) | Block | Format and kit | Interviewer persona |
|---|---|---|---|
| 0:00–0:45 | **Round 1 — HLD** | `hld-45`: [D3 format](../D3-hld-mock/format.md), [D3 rubric](../D3-hld-mock/rubric.md), [D3 probe bank](../D3-hld-mock/probe-bank.md) | The skeptical staff engineer: probes failure and cost, asks "why not X" twice |
| 0:45–0:55 | Break (10 min) | — | — |
| 0:55–1:40 | **Round 2 — LLD** | `lld-45`: [D1 format](../D1-lld-mock/format.md), [D1 rubric](../D1-lld-mock/rubric.md), [D1 probe bank](../D1-lld-mock/probe-bank.md) | The product-minded engineering manager: cares about the requirement change and how the model maps to the product |
| 1:40–1:50 | Break (10 min) | — | — |
| 1:50–3:20 | **Round 3 — Machine coding** | `mc-90`: [D2 format](../D2-machine-coding-mock/format.md), [D2 rubric](../D2-machine-coding-mock/rubric.md), [D2 probe bank](../D2-machine-coding-mock/probe-bank.md) | The quiet pair: answers questions, announces stages, says little else until the walk-through |
| 3:20–3:30 | Break (10 min) | — | — |
| 3:30–3:50 | **Component oral** (20 min) | [component-oral.md](component-oral.md) | The bar raiser: first principles, no diagrams of a whole system |
| 3:50–4:30 | Scoring and debrief | [rubric.md](rubric.md), [mock-log.md](mock-log.md) | Tutor (out of role) |

≈ 4.5 hours in all, ≈ 3 h 20 min of it on the clock. Personas change the interviewer's emphasis, never the rubric:
each round is scored on its own format's rubric.

**Order.** The default is HLD → LLD → MC → oral. For one of the three loops the tutor may put machine coding first
(MC → HLD → LLD → oral), so that the HLD round is done tired, which is when structure tends to slip. Say which order
at the start of the day.

## Before the day

- [ ] **Unlocked.** `sd status D4` doesn't say locked (D1, D2, D3, A8, A9 and C3, C4, C7–C12 passed). If it's locked,
      no loop: name the missing gates.
- [ ] **Three unseen problems** picked by the tutor, one from each bank: [D3 problem bank](../D3-hld-mock/problem-bank.md),
      [D1 problem bank](../D1-lld-mock/problem-bank.md), [D2 problem bank](../D2-machine-coding-mock/problem-bank.md).
      Unseen = not in any earlier mock, design exercise or case study the learner did (check `mocks` and `designs` in
      `progress.json` and the `_mocks/` logs). If a bank is exhausted, the tutor writes a new problem in the bank's
      format before the day and adds it to the bank.
- [ ] **Domain spread.** No two rounds in the same domain (an HLD payments design and an LLD payments model is one domain twice).
      At least one round is money- or banking-flavoured.
- [ ] **Oral blocks chosen** (4–5 building blocks, per the rotation in [component-oral.md](component-oral.md)).
- [ ] **Log and folder.** The learner copies [mock-log.md](mock-log.md) to
      `_mocks/<YYYY-MM-DD>-loop/log.md`; the machine-coding round works in `_mocks/<YYYY-MM-DD>-loop/mc/` (a Go
      package; the `_` prefix keeps it out of `./...` and `./check.sh`).
- [ ] **Half a day free.** A loop interrupted for more than 15 minutes outside the breaks doesn't count; stop, note it
      (`sd record note D4 --tags loop --notes "interrupted after round 2"`) and redo the whole loop another day.

## During the loop

- **Each round runs exactly as its module's format.md says**: same phases, checkpoints, interviewer script, number of
  probes, requirement change (HLD at ≈ 0:25–0:30, LLD at 0:30) and stage announcements (MC at 0:30 and 0:55).
- **No feedback between rounds.** The tutor scores each round privately in the log right after it ends, before the
  break, and says nothing about it until the debrief. The learner doesn't self-score between rounds either; they
  write one line ("what I'd change") and then let it go.
- **Breaks are for resetting, not reviewing:** water, stand up, clear the page, read the *next* format's checkpoints
  once. Don't re-open the last round's work.
- **The clock is strict.** A round that overruns takes the time out of the following break, never out of the next
  round. If a break goes to zero, the next round still starts on time.
- **Timekeeping.** As in D1–D3: the tutor runs `date` at each phase change in Claude Code; in plain chat the learner
  types the loop clock and the round clock (`[L 1:12 · R 0:17]`).

## After the loop (≈ 40 min)

1. **Self-score each round** on its rubric, from memory and the artefacts (≈ 10 min), then the overall per round.
2. **Tutor reveals** the private round scores, the oral verdict, and applies the loop rule in [rubric.md](rubric.md).
3. **Debrief** (≈ 20 min): the pattern across rounds first (a habit that showed up in two rounds matters more than a
   one-off), then top 3 strengths, top 3 gaps with fixes, calibration per round, and which definition-of-done items
   this loop evidences.
4. **Record** (the tutor runs these):

   ```text
   sd record mock D4 --format loop --problem "HLD <title> · LLD <title> · MC <title>" --minutes <on-clock minutes> --level <loop level> --notes "HLD <lvl> · LLD <lvl> · MC <lvl> · oral <pass|revise> · <top gap>"
   sd record note D4 --tags loop,<weak tags> --notes "<cross-round pattern>"
   ```

   `--minutes` is the on-clock total actually used (≈ 200 when on time: 45 + 45 + 90 + 20). The problem string names
   all three problems with their bank titles, so a repeated loop is recognised. Rounds aren't recorded separately
   against D1–D3: the loop record carries the round levels in its notes.
5. **Commit** `_mocks/<date>-loop/` with `progress.json`.
