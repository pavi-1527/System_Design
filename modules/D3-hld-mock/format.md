# D3 Format — the 45-minute HLD mock (`hld-45`)

This is the clock the tutor runs and the checklist the learner works against. It's the framework from
[A0 Unit 1](../A0-framework-estimation/unit-1-interview-framework.md) and the
[reference card](../../docs/INTERVIEW_FRAMEWORK.md), with three things a rehearsal doesn't have: an interviewer who
answers questions and probes, one requirement change released mid-way, and a score that counts towards a gate.

## Before the clock starts (≈ 5 min, not timed)

- [ ] **Unlocked.** `sd status D3` doesn't say locked. If it does, there is no mock: offer an untimed guided design
      and record nothing (TUTOR.md §8).
- [ ] **Problem picked by the tutor** from [problem-bank.md](problem-bank.md), unseen by the learner (the bank's
      selection rule), and not revealed until 0:00.
- [ ] **Log created.** The learner copies [mock-log.md](mock-log.md) to
      `_mocks/<YYYY-MM-DD>-hld-45-<slug>/log.md`. The slug can be filled in after the prompt is read.
- [ ] **Medium agreed.** Whiteboard tool, paper photographed afterwards, or text in chat with Mermaid/ASCII diagrams.
      Text is slower than speech, so write tersely (bullets, arrows, numbers); the clock doesn't change.
- [ ] **Nothing open** except the blank page and the clock: no notes, no [docs/NUMBERS.md](../../docs/NUMBERS.md),
      no case-study files. If a number isn't in your head, state an assumption out loud.
- [ ] **Clock owner.** In Claude Code the tutor runs `date` at 0:00 and whenever the learner announces a phase change.
      In a plain chat, the learner starts a timer and types the clock (`[0:07]`) at the top of every message.

## The phase checklist

| Clock | Phase | On the board when the phase ends | What the interviewer does | Rubric dimension |
|---|---|---|---|---|
| 0:00 | Prompt | — | Reads the prompt from the bank, verbatim. Nothing more. | — |
| 0:00–0:06 | **Requirements & scope** | 3–5 functional requirements; NFRs *as numbers* (scale, latency, availability, durability, consistency where it matters); an explicit out-of-scope list | Answers clarifying questions from the bank's answer sheet, with numbers. Volunteers nothing. Unasked questions get "your call — state an assumption". | Requirements & scope |
| 0:06–0:10 | **Estimation** | ≤ 6 numbers, arithmetic and units shown, each followed by the decision it drives | Silent. Checks the arithmetic against the bank's reference estimates. | Estimation |
| 0:10–0:13 | **API** | 3–6 endpoints/RPCs with key params; pagination; idempotency keys wherever a client retries; auth noted | Silent, or one clarifying answer. | API & data model |
| 0:13–0:16 | **Data model** | Entities, keys, the access patterns they serve, store choice with a one-line reason, partition key | Silent. | API & data model |
| 0:16–0:25 | **High-level design** | End-to-end diagram for the main write and read paths; every box justified; one request walked through | May ask one "why is this box here?" | High-level design |
| ≈ 0:25–0:30 | **Requirement change** | A short delta: what changes in the design, what doesn't, and why | Releases the bank's mid-way change at a natural pause, once. | Probe handling |
| 0:25–0:40 | **Deep dives (2–3)** | The riskiest parts with alternatives compared on cost and flip conditions | Probes from [probe-bank.md](probe-bank.md), one at a time, one rung further on vague answers. | Deep dives & trade-offs · Probe handling |
| 0:40–0:45 | **Bottlenecks, failures, evolution** | What breaks first at 10×, how you'd know (metric + threshold), what you'd change; open risks; cost drivers | May ask one closing probe (cost or security if neither came up). | Failure, operations & cost |
| 0:45 | **Hard stop** | — | "Let's stop there." Mid-sentence if necessary. | Communication & time |

## Checkpoints

The three checkpoints are where the interviewer writes something down. The tutor logs the actual clock at each one.

| Checkpoint | Target | On time | Slipped | Missed |
|---|---|---|---|---|
| **A. Numbers on the board** | 0:10 | ≤ 0:12 | 0:12–0:15 | none by 0:15 → *Estimation* is at most approaching |
| **B. End-to-end diagram, one request walked** | 0:25 | ≤ 0:27 | 0:27–0:30 | none by 0:30 → *High-level design* is at most approaching |
| **C. Stop drilling, start closing** | 0:40 | ≤ 0:41 | 0:41–0:43 | no closing at all → *Failure, operations & cost* is at most approaching |

Two slipped checkpoints, or one missed, put *Communication & time* at most approaching. The caps are in
[rubric.md](rubric.md); they exist because these are the failures that sink real loops, whatever the quality of the
rest.

## Behind schedule (the learner's rules)

- **Say it, then compress.** "I'm two minutes behind, so I'll keep the API to the two calls that matter." Silence
  while skipping reads as not noticing.
- **Cut in this order:** API detail → data-model detail → the third deep dive → polish of the diagram. **Never cut**
  the numbers, the end-to-end diagram, or failure modes.
- **Pulled into a deep dive early?** Follow (the steer is a hint about what's graded), then: "Let me close the loop
  on the overall design so we have the full picture." Do it by 0:30 at the latest.
- **Lost?** Restate the requirement you're serving and the number that matters, then continue. Thirty seconds of
  re-anchoring beats five minutes of drifting.

## The interviewer's script (the tutor's lines)

Stay in role from 0:00 to 0:45. No teaching, no praise, no hints beyond what a real interviewer would give.

| When | Say (or do) | Never |
|---|---|---|
| 0:00 | The bank's prompt, verbatim. "Take it from here." | Explain the problem, list requirements, mention scale |
| Clarifying questions | The answer-sheet number, short. Anything not on the sheet: a plausible value consistent with the sheet, logged as "invented" in the log | Volunteer requirements they didn't ask about |
| A phase change | Run `date`, log the clock. Say nothing | Comment on the phase |
| Hand-waving ("we'll just shard it") | "By what key? What happens to *this* query?" | Supply the answer |
| Checkpoint A missed (≈ 0:12) | "Roughly what scale are we designing for?" (once) | A second nudge |
| Checkpoint B at risk (≈ 0:24) | "We have about twenty minutes left." (once) | Tell them to draw the diagram |
| ≈ 0:25–0:30 | Release the bank's mid-way requirement change, in one sentence | Release it during estimation, or release two |
| Deep dives | 3–5 probes from the bank, one at a time; at least one on a risk the candidate *didn't* pick; push one rung further on any answer without a number or a mechanism | Ask a probe the design already answered on the board |
| 0:40 | Nothing, unless they're still drilling: "Let's zoom out for the last few minutes." (once) | Extend past 0:45 |
| 0:45 | "Let's stop there." | Let them finish the diagram "quickly" |

**Probe selection.** Use the problem's nearest case-study sections in [probe-bank.md](probe-bank.md). Cover at least
three different kinds across the session (scale-up, failure, consistency, cost, security, "why not X"). Log each
probe and a one-line summary of the answer as it happens; the rubric's *Probe handling* is scored from that list.

## After the clock (≈ 20 min)

1. **Self-score first** (≈ 5 min). The learner fills the rubric table in the log: a level and one line of evidence per
   dimension, then an overall level using the rule in [rubric.md](rubric.md). The tutor says nothing evaluative until
   this is done.
2. **Tutor scores** (≈ 5 min), each dimension with evidence (a timestamp, a quote, a number), applies the caps and the
   overall rule, and compares against the self-score.
3. **Debrief** (≈ 10 min): top 3 strengths, top 3 gaps, each gap with a concrete fix and the module/section to
   revisit; the calibration delta; whether the problem counts as unseen.
4. **Record** (the tutor runs these, in this order):

   ```text
   sd record mock D3 --format hld-45 --problem "<bank title>" --minutes <actual> --level <overall> --notes "<top gap>"
   sd record note D3 --tags mock,<weak tags> --notes "<pattern worth remembering>"
   ```

   `--minutes` is the actual elapsed time, even if it overran. Use the bank's title as the problem name so the
   distinct-problems rule recognises repeats.
5. **Commit** the log with `progress.json` (or remind the learner to).
