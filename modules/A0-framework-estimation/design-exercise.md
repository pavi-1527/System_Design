# A0 Design exercise — Framework rehearsal: photo sharing

> Time box: 30 minutes, solo, no notes. Then self-score (10 minutes), then send to the tutor for a 10–15 minute
> probe session.
>
> Have nothing open except a blank page (or whiteboard) and a clock. You may *not* use
> [docs/NUMBERS.md](../../docs/NUMBERS.md) during the 30 minutes. The numbers you need should be in your head by
> now, and if one isn't, state an assumption, just as you would in an interview.

This exercise is a compressed version of the first 25 minutes of a real interview, plus a short deep-dive
preview. It doesn't test whether your photo-sharing design is perfect. It tests whether you can run the
framework: scope, quantify, design in order, on time, and defend your choices. Those dimensions are weighted
most heavily in the rubric.

## Prompt

> "Design a photo-sharing service. Users upload photos, follow other users, and see a feed of recent photos from
> the people they follow."

That's all the interviewer says. Everything else (scale, latency, which features are in scope, what "recent"
means, how long photos are kept) is for you to clarify or state as an assumption. If you'd like answers to
clarifying questions, the tutor will answer them during the probe session, so write your questions down and state
the assumption you'd proceed with.

**Time budget for this rehearsal (30 minutes):**

| Clock | Phase |
|---|---|
| 0:00–0:05 | Requirements & scope |
| 0:05–0:09 | Estimation (decision numbers only) |
| 0:09–0:12 | API |
| 0:12–0:15 | Data model |
| 0:15–0:24 | High-level design (both happy paths traced) |
| 0:24–0:30 | Two trade-off cards + one deep dive outline |

Same rules as the full interview: numbers on the page by 0:09, a complete end-to-end diagram by 0:24, and stop at
0:30 even if you're mid-sentence.

## What to produce

1. **Timestamp log.** Write the clock time at the start of each phase. It's the evidence for "time management", so
   don't skip it or reconstruct it afterwards.
2. **Requirements card:** 3–5 functional requirements; non-functional requirements *each with a number* (scale,
   latency, availability, durability, consistency where it matters); an explicit out-of-scope list; any
   clarifying questions you'd ask, each with the assumption you proceeded with.
3. **Estimate sheet:** at most 6 numbers, each with the arithmetic shown (units carried) and **the decision it
   drives** written next to it. Peak and average where it matters; bits where you compare with a link.
4. **API sketch:** the 2–4 core calls: method, path, key request/response fields, status codes, idempotency where
   a client may retry, pagination where there's a list.
5. **Data model:** access patterns first, then entities, keys and indexes; which store holds what, and why.
6. **HLD diagram:** the write path (upload) and the read path (feed) as numbered arrows; each box with a one-line
   justification tied to a requirement or a number; at least one arrow labelled with a rate.
7. **Two trade-off cards** for your two most consequential decisions, each as claim → why here (tie to a
   number/requirement) → cost → when it flips.
8. **Deep-dive outline:** which deep dive you'd do first in a real interview, why (risk), and 3–5 bullet points of
   what you'd cover, including at least one failure mode and one metric.

## Rubric

Weights: **structure 20%, time management 20%, quantification 20%, trade-off articulation 20%**, requirements &
scope 10%, design correctness 10%. This module tests the framework more than the photo-sharing design itself.

| Dimension (weight) | below | approaching | meets (senior bar) | exceeds |
|---|---|---|---|---|
| **Structure & sequencing** (20%) | Phases missing or out of order (e.g. schema before requirements, no HLD); no visible plan | All phases present, but the order slips (boxes drawn before numbers) or transitions are unclear | Phases in dependency order; each phase visibly uses the previous one's output (estimates referenced in the data model and HLD); clear transitions | Deviates from the default order *on purpose* and says why; output of each phase is reused without being asked |
| **Time management** (20%) | No timestamps, or > 8 min over on any checkpoint; HLD incomplete at 0:30 | Finishes, but a checkpoint slips by 4–8 min; one phase overruns badly (e.g. requirements ≥ 9 min) | Numbers by ≈ 0:09 and a complete diagram by ≈ 0:24 (each within ≈ 2 min); stops at 0:30 | All checkpoints on time *and* there's a clear note of what was deliberately compressed and why |
| **Quantification** (20%) | No numbers, or numbers with unit errors ≥ 8× (bits/bytes, per-day/per-second); numbers unused | Mostly correct numbers (within ≈ 2×), but several aren't tied to a decision, or peak/replication/retention is missing | ≤ 6 decision numbers within ±25–50%; units carried; each followed by its decision (e.g. "≈ 116 Gbps peak egress → CDN mandatory") | Also sanity-checks one number against another, gives a range for a shaky input, or notes which number would flip the design if 10× off |
| **Trade-off articulation** (20%) | "Because it's scalable/fast/industry standard"; no costs | Claim + reason, but the reason is generic or the cost is missing; no flip condition | Both cards have claim → why here (a number or requirement) → cost → a *measurable* flip condition | Cards anticipate the obvious "why not Y" and the 10× question; the deep-dive outline includes failure mode, metric and migration |
| **Requirements & scope** (10%) | Features listed, no NFR numbers; no out-of-scope list | NFRs present but some without numbers; scope implicit | 3–5 core functions; every NFR has a number; explicit out-of-scope; assumptions stated for unanswered questions | Names the dominant NFR (e.g. egress/latency of the feed) and how its sensitivity changes the design |
| **Design correctness** (10%) | A happy path can't be traced; the design contradicts a requirement | Paths traceable but with gaps (where's the photo stored? who builds the feed?); some boxes unjustified | Both paths complete; every box justified; API has idempotent upload and paginated feed; data model follows access patterns; blob/metadata split | Also addresses the feed fan-out strategy choice (push vs pull vs hybrid) with numbers, or the hot-user problem |

**Overall "meets"** requires: *meets* or better on all four 20% dimensions (structure, time, quantification,
trade-offs), and no dimension at *below*. **Overall "exceeds"** requires *exceeds* on at least two of the four 20%
dimensions and *meets* everywhere else. Otherwise, overall is the lowest of the four 20% dimensions.

## Tutor critique protocol

**1. Before probing, check the artefacts (≈ 3 min):**

- The timestamp log exists and was written during the exercise. Compute the slip at 0:09, 0:24 and 0:30.
- Every NFR has a number. There's an explicit out-of-scope list.
- Every estimate has units and a decision next to it. Check the arithmetic of at least the peak write QPS, storage
  per year and peak egress, and check for the 8× bits/bytes error and missing retention/replication specifically.
- Both happy paths can be traced arrow by arrow. Every box has a justification.
- Both trade-off cards have all four parts, and the flip conditions are measurable.

**2. Ask these follow-up probes (≈ 10 min).** Ask at least six, including the ones marked (required). Push one
rung further on any vague answer ("what number would make you change that?").

1. **(required)** "What's your peak upload rate, and which decision did it drive?"
   *Meets:* a number with derivation (e.g. 50M DAU × 2/day ≈ 1.2k/s avg, ≈ 3k/s peak) and a decision (metadata
   writes fit one primary's range, but metadata *size* still forces partitioning; uploads go direct to object
   storage).
2. **(required)** "What's your peak egress in Gbps, and what does it do to the design?"
   *Meets:* the bits conversion, and the conclusion that a CDN and resized variants (thumbnails) are mandatory.
3. **(required)** "Why did you put photo metadata in that store? What changes at 10×?"
   *Meets:* tied to access patterns (lookup by photo id, feed by follower/time) and to size/write rate, with a flip
   condition and the partition key.
4. **(required)** "The object store is unavailable for 10 minutes. What happens to uploads and to feeds?"
   *Meets:* uploads fail cleanly or queue client-side with idempotent retry; feeds still render metadata and
   cached/CDN images; no corrupt partial state (metadata not committed before the blob exists).
5. "How would you know the feed is slow? Name the metric and the alert threshold."
   *Meets:* a latency SLI at a percentile (e.g. p99 feed render ≤ X ms) and an alert on error-budget burn or on
   that percentile, plus one leading indicator (cache hit ratio, fan-out queue lag).
6. "Why not store the photos in the database?"
   *Meets:* size (PB-scale), cost per GB, backup/replication load, CDN integration; flip condition: tiny objects
   with transactional needs.
7. "Roughly what does this cost per month, and which line dominates?" (STRETCH-level; don't penalise a
   missing answer below "meets", but credit it towards "exceeds".)
   *Exceeds:* identifies egress as dominant and proposes reducing bytes per read.
8. "Push or pull for the feed? What about a user with 50M followers?"
   *Exceeds:* chooses with numbers (fan-out writes = uploads × average followers), names the hybrid for
   high-follower accounts.
9. **Time-pressure probe (required, at the start of the probe session):** "Before we go on, explain in detail
   how you'd do image resizing." Then watch: does the candidate give a short answer and re-anchor, or spend
   5 minutes? Score it under *time management*.

**3. Give the critique** in this order: the single biggest gap (one sentence); per-dimension scores with one piece
of evidence each; the one habit to practise before the next exercise.

**4. Decide.** Apply the overall rule above. If overall is below "meets", the learner re-does the exercise on a
different prompt (suggested: "design a ride-hailing dispatch system" or "design a transaction-alerts service for
a digital banking platform") after at least one day, and records the new attempt.

## Recording

`sd record design A0 --self <below|approaching|meets|exceeds> --tutor <level> --notes "<one-line summary>"`

Example: `sd record design A0 --self meets --tutor approaching --notes "egress in bytes not bits (8x); HLD done at 0:27; trade-offs had no flip conditions"`
