# A0 Teach-back

## Prompt

A mid-level engineer on your team has their first senior system design interview in two weeks. They know the
building blocks (caches, queues, databases) but have never structured a 45-minute design session, and they freeze
on estimation. Write them an explanation, in your own words, of **how to run the 45 minutes and how to do the
numbers**.

- Form: written, **600–900 words**, plus **one** diagram (Mermaid or a hand sketch) if you want.
- No copying from the unit files or [docs/INTERVIEW_FRAMEWORK.md](../../docs/INTERVIEW_FRAMEWORK.md). If you
  can't explain it without looking, you're not ready.
- Include **one worked estimate** with real arithmetic (pick any simple system), showing how one of its numbers
  changes a design decision.
- Include **one trade-off** stated the way you'd want them to state it.
- Write it for the reader: they should be able to use it in their interview, not just agree with it.

Send it to the tutor. The tutor evaluates it against the checklist below and may ask up to three follow-up
questions. Your answers to those count too.

## Evaluator checklist

Must-hit (all required for pass):
- [ ] Explains the phase order as a **dependency chain** (requirements → estimation → API → data model → HLD →
      deep dives → trade-offs), saying *why* each phase needs the previous one, not just listing the phases.
- [ ] Gives the **time budget** (approximately: ≈ 6 / 4 / 3 / 3 / 9 / 15 / 5 minutes) and the **three
      checkpoints** (numbers by 0:10, complete end-to-end diagram by 0:25, stop drilling at 0:40), with what goes
      wrong if each is missed.
- [ ] States that non-functional requirements must carry **numbers**, and that out-of-scope must be said out loud.
- [ ] Says that estimates exist **to drive decisions**: compute only numbers near a threshold, and say the
      decision right after the number.
- [ ] The **worked estimate is arithmetically correct** (within ±25%), carries units, uses peak (not only
      average) where it sizes something, and ends in a design decision.
- [ ] Presents the **trade-off statement** with all four parts: claim → why here (tied to a requirement or
      number) → cost → when it flips.

Should-hit (at least 4 of 7):
- [ ] The probe ladder (what if it fails, 10×, why not Y, how would you know, cost, migration) as preparation for
      deep dives.
- [ ] How to choose deep dives by risk (biggest number, subtlest correctness property, interviewer hints).
- [ ] Driving vs following: short answers to tangents plus a parking lot; hints become deep dives.
- [ ] A recovery tactic when behind (e.g. thin skeleton by 0:25; cut API detail first; never cut failure modes).
- [ ] At least one classic estimation trap explained with its magnitude (bits vs bytes 8×; retention or
      replication forgotten; average vs peak; 1e5 s/day ≈ 14% low).
- [ ] Little's law or serial availability used correctly, with a number.
- [ ] A link to real work (e.g. a vendor adapter's pool sized by Little's law, or an IdP's SLA capping login
      availability).

Red flags (any one = revise):
- [ ] Presents the framework as a script to memorise or recite ("first say X, then say Y") with no reasoning about
      why the order holds or when to deviate.
- [ ] A unit error of ≥ 8× anywhere (bits/bytes, per-day/per-second, KB/MB) that isn't caught.
- [ ] Recommends a technology choice justified only by popularity or "scalability".
- [ ] Advises spending most of the 45 minutes on one phase (e.g. "requirements can take 15 minutes") or skipping
      the end-to-end design to go deep.
- [ ] States an invented fact or number with confidence (e.g. a precise throughput figure for a product without
      saying it's an assumption).

## Recording

`sd record teachback A0 --result <pass|revise> --notes "<missed items>"`
