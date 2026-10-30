# D1 Format — the 45-minute LLD round (`lld-45`)

The timed format for every D1 mock. [Unit 1](unit-1-lld-round.md) explains why it looks like this; this page is the
card to keep open (on the learner's side, closed during the mock) and the script the tutor follows.

Format id for recording: `lld-45`. Scored with [rubric.md](rubric.md). Logged with [mock-log.md](mock-log.md).

## Phases and checkpoints

| Clock | Phase | On the board / in the editor | Rubric dimension it feeds |
|---|---|---|---|
| 0:00–0:05 | **Clarify & scope** | 3–5 functional requirements; explicit out-of-scope; the **axes of change** you expect ("late-fee rules will vary", "rules differ per branch"); one concurrency question ("how many callers at once?") | 1 Requirements & scope |
| 0:05–0:12 | **Domain model** | Entities with one-sentence responsibilities; which are values and which have identity and changing state; **who owns each piece of mutable state**; the invariants in words | 2 Domain model |
| 0:12–0:20 | **Interfaces & main flow** | The public API as **Go signatures** (no bodies); 1–3 consumer-side interfaces, each tied to a named axis of change; one sequence (bullets or Mermaid) for the main use case; error values callers can match | 3 Interfaces & extensibility |
| 0:20–0:30 | **Core path in code** | A compile-able skeleton plus the **one hard method** (the state transition, the allocation, the rule evaluation) and **one test** for it | 5 Code & testability, 4 Concurrency & correctness |
| **0:30** | *Interviewer releases the requirement change* | — | — |
| 0:30–0:37 | **Absorb the change** | Say which seam it lands behind (or that it lands on an unopened axis, and the smallest edit); write the new type/function; adjust or add one test | 3 Interfaces & extensibility |
| 0:37–0:45 | **Concurrency, edge cases, probes, next** | The ownership story said out loud; one edge case handled; probe answers; "what I'd do with another hour" and what the design does **not** cover | 4, 6 Trade-offs & probe handling, 7 Communication & time |

**Checkpoints** (the tutor notes the actual clock at each):

1. **API as Go signatures by 0:12–0:15.** Later than 0:15 means the model phase overran; later than 0:20 is a
   *below* signal on time.
2. **Core path coded by 0:30.** "Coded" means the hard method is written and its test exists, even if untested
   against a compiler. Nothing coded at 0:30 caps *Code & testability* at approaching.
3. **Requirement change landed by 0:37.** Landed = the candidate has said where it goes and written it.
4. **Stop at 0:45.** The tutor stops the clock. Anything said after it doesn't count.

### How this differs from the B1 shape

[B1 Unit 2 §5](../B1-ood-go/unit-2-boundaries-errors.md#5-the-lld-interview-extension-without-a-rewrite) gives a
code-weighted 45 minutes: model *and* signatures by 0:10, then 20 minutes of code, change at 0:30, concurrency from
0:35. D1 keeps the same skeleton (scope 5 min, change at 0:30, concurrency last) but splits model and interfaces
into two phases (0:05–0:12 and 0:12–0:20) and gives code 10 minutes instead of 20. That's because many LLD rounds
are whiteboard-first with partial code, and the interviewer scores the model and the seams more than a running
program (TODO-VERIFY: the code/whiteboard split varies by company and interviewer; ask the recruiter which it is).
If the interviewer says "I'd like working code", switch to the B1 shape: merge the model and interface phases into
0:05–0:10 and code from 0:10. If they say "no code at all", replace 0:20–0:30 with a second sequence (the failure
path) and pseudo-code for the hard method.

## Behind schedule

Say it, then compress in this order:

1. **Shrink the sequence diagram** to three bullets. Never skip the signatures.
2. **Code only the hard method**, not the skeleton around it; name the test you'd write instead of writing it.
3. **Defer secondary error cases** to the "what's next" list.

Never cut: the out-of-scope list (it takes 30 seconds), the state-ownership sentence, or the response to the
requirement change. If the interviewer pulls you into a deep question early ("what does `Checkout` do if the card pre-authorisation times out?"),
answer in under two minutes, put the rest in a parking-lot list, and say "let me get the interfaces down first".

## The interviewer's script (tutor)

**Before 0:00.** Check `sd status D1` is unlocked. Pick a problem from [problem-bank.md](problem-bank.md) the learner
hasn't done (check `mocks` in `progress.json` and the `_mocks/` logs). Tell the learner the format and checkpoints,
not the problem. Ask them to create `_mocks/<YYYY-MM-DD>-lld-45-<slug>/` and copy [mock-log.md](mock-log.md) into it
as `log.md`. Note the start time with `date`.

**0:00.** Read the prompt exactly as written in the bank: short, underspecified. Answer clarifying questions from the
bank's answer sheet, with numbers. Don't volunteer requirements. If asked something the sheet doesn't cover, answer
plausibly and write the answer in the log so the score uses the same facts.

**0:05–0:30. Mostly silent.** One interjection is realistic around 0:15–0:20: a single "why" question about a choice
on the board (a probe from the problem's list). Note the clock if a checkpoint slips. If there are no signatures by
0:15, nudge once as an interviewer would: "Can you show me the API you'd expose?" If nothing is coded by 0:25: "We
have about twenty minutes; I'd like to see the core method."

**0:30. Release the requirement change** exactly as written in the bank, even if the candidate is mid-method (that's
part of the test). Don't hint at where it should land.

**0:37–0:45. Probes.** Ask 2–4 probes, one at a time, from the problem's list first, then the
[probe bank](probe-bank.md), aiming for at least three different categories across the round (always one concurrency
probe and one failure probe). Push one rung on any vague answer: "which lock?", "what number would change your mind?".

**0:45. Stop.** Then: the learner self-scores every dimension (one line each) *before* any feedback; the tutor
scores with evidence, applies the overall rule, debriefs (≈ 15 min), fills the log and records:

```text
sd record mock D1 --format lld-45 --problem "<name>" --minutes <actual> --level <overall> --notes "<top gap>"
sd record note D1 --tags lld,<tags> --notes "<pattern worth remembering>"
```

Don't teach during the mock. A mock that was interrupted for more than a few minutes (a call, a crash) is not
recorded; rerun it on another problem.
