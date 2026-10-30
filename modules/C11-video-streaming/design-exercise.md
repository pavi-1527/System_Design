# C11 Design exercise — A short-form video app

> Time box: 45 minutes, solo, no notes, timed like a real interview (numbers by 0:10, end-to-end diagram by 0:25,
> stop drilling at 0:40). Then self-score against the rubric (≈ 10 min), then send the write-up to the tutor for the
> probe session. Use the structure from [`docs/INTERVIEW_FRAMEWORK.md`](../../docs/INTERVIEW_FRAMEWORK.md) and the
> numbers from [`docs/NUMBERS.md`](../../docs/NUMBERS.md) — from memory.

This is a *variant* of the module's case study on purpose. The mechanisms (the three paths, a processing DAG with
idempotent artifacts, a ladder and ABR, CDN economics, storage tiers, counts from an event log) all apply, but the
constraints are different: clips are seconds long, not minutes; the feed decides what plays next, so the next clip
can be fetched before the viewer asks for it; creators expect a clip to be live within seconds; and almost every
view happens on a phone. If you reproduce Unit 1's design box for box you will score "approaching": the rubric
rewards *transferring* the decisions, with numbers, to this shape, including the ones that flip (segment-level
parallelism matters much less here; startup and prefetch matter much more).

## Prompt

> "Design the video side of a short-form video app: people record clips of 15 to 90 seconds on their phones and post
> them; viewers swipe through a vertical feed that plays clip after clip with no visible loading. We have about
> 150 million daily users in 20 countries. Creators want their clip to be watchable within seconds of posting."

That is all the interviewer says up front. Ask the clarifying questions you need (write them down with the answer
you'd assume if told "you decide"), and drive.

Things an interviewer would answer if asked (don't read until you've written your questions):

<details data-kind="hint">
<summary>Interviewer's answers to common clarifying questions</summary>

- Scale: ≈ 150 M daily active viewers, ≈ 50 minutes a day each; average clip ≈ 30 s; ≈ 20 M clips posted a day.
- Peak concurrency ≈ 2× the daily average (evenings, staggered across countries; assume the two largest overlap).
- Devices: ≈ 95% phones, mostly on mobile data; screens are vertical (1080×1920 at most). Average *delivered* bitrate
  ≈ 1.5 Mbps.
- Source uploads ≈ 8 Mbps from the phone camera; uploads are often on weak networks.
- Ladder: four rungs is fine (e.g. ≈ 0.5, 1.0, 1.8 and 3.5 Mbps); H.264 everywhere, a newer codec optional.
- The feed service (out of scope) decides the next clips and can tell the player 3–5 clips ahead.
- Startup: swiping to the next clip should show the first frame in well under half a second (`TODO-VERIFY` any
  published target; treat ≈ 200–300 ms as the goal).
- Time to watchable: ≈ 10–30 s after the upload finishes. Every clip must pass an automated moderation check before
  it can be shown to anyone other than its creator.
- Views: a view counts after ≈ 1 s of playback (assumption for this exercise); counts are shown on every clip and
  drive creator payouts in some countries.
- Popularity: extremely skewed. The feed pushes a small set of clips to millions within hours; most clips are
  watched fewer than 100 times, ever.
- Retention: clips are kept indefinitely unless deleted.
- Out of scope unless you choose otherwise: the recommendation algorithm, comments and likes, live streaming, ads.

</details>

## What to produce

1. **Timestamp log**: the clock time at the start of each phase (evidence for time management).
2. **Requirements card**: functional requirements (3–5), non-functional requirements *with numbers* (startup,
   rebuffering, time to watchable, availability, durability), explicit out-of-scope, and your clarifying questions
   with the assumptions you proceeded with.
3. **Estimate sheet**: at most 7 numbers with arithmetic and units, each with the decision it drives: concurrent
   viewers and peak egress, egress per day, plays per second, upload ingress, storage growth per year, transcode
   compute, and the cost of prefetching.
4. **API sketch**: resumable upload (and what changes for a 30 s clip), publish, playback (what the player receives
   for the next several clips), playback events, view counts.
5. **Data model**: access patterns first; clip metadata, upload sessions, processing artifacts, moderation state,
   object key layout (versioned, immutable), counters; partition keys.
6. **HLD diagram**: upload, processing (with the moderation gate) and playback paths, with rates on at least three
   arrows and a one-line justification per box.
7. **Deep dives (2–3)**, each with alternatives, a cost and a flip condition:
   - time to watchable: the processing DAG for a 30 s clip (what's parallel, what's the fast path, where moderation
     sits), retries, poison inputs, idempotent resume;
   - instant playback in the feed: ladder, segment or file layout, startup rung, prefetching the next clips, and what
     prefetching costs in egress;
   - delivery economics: CDN hit ratio for an extremely skewed head and a vast tail, pre-positioning trending clips,
     storage tiers, and cost per hour watched.
8. **Failure walkthrough**: a moderation-service outage for 20 minutes at peak; the transcode fleet loses a third of
   its capacity for an hour; a CDN region fails during the evening peak in the largest country.

## Rubric

| Dimension | below | approaching | meets (senior bar) | exceeds |
|---|---|---|---|---|
| Requirements & scope | Draws immediately; "like YouTube but short" | Lists features; NFRs without numbers | Startup (≈ 200–300 ms on swipe), rebuffer, time to watchable (≈ 10–30 s), durability and availability with numbers; moderation gate as a requirement; out-of-scope stated; the questions that flip decisions (clip length, device mix, popularity skew) asked | Notes early that startup and prefetch, not long-video parallelism, are this design's hard parts, and that the feed knowing the next clips is the key enabler |
| Estimation | Missing, or ≥ 10× off on a number the design depends on | Daily totals only; no peak; bits/bytes confusion | ≤ 7 numbers, units carried: ≈ 5 M concurrent average, ≈ 10 M peak, ≈ 15 Tbps peak egress, ≈ 85 PB/day, ≈ 15 B plays/day ≈ 175k/s average, ≈ 600 TB/day ingest, ≈ 400 PB/year storage, ≈ 28k transcode cores; each tied to a decision | Quantifies prefetch waste (e.g. bytes fetched for clips swiped past) and shows it's a double-digit percentage of egress, then designs to bound it |
| API & data model | Uploads through app servers; no idempotency; status column only | Resumable upload and signed URLs mentioned; keys not deterministic | Idempotent upload create and complete (single PUT or a few parts for a 30 s clip, resumable on weak networks); playback returns signed URLs for the next N clips with startup hints; deterministic, versioned, immutable object keys; moderation state and processing artifacts as records; counts from events; partition keys follow access patterns | Small-object effects reasoned: per-object request overhead, packing a clip's rungs or first segments for fewer requests |
| High-level design | One service does everything; vendors or encoders in the request path | Paths drawn but processing and playback coupled; no moderation gate | Upload, processing and playback paths sharing only blob store and metadata; outbox → orchestrator; moderation as a DAG node that gates *distribution* (creator can preview before it passes); CDN with shield; playback API off the byte path; rates on arrows; every box justified | Per-country or per-region cells; degraded modes drawn (e.g. moderation backlog → creator-only visibility) |
| Processing pipeline | Serial cron job; retries forever; no idempotency | DAG drawn but no failure handling, or segment parallelism copied without reasoning | Recognises that a 30 s clip needs little segment parallelism (per-rung tasks or a few chunks suffice) and that the fast path is the lowest rung plus moderation; bounded retries with backoff, per-attempt timeouts sized from p99, poison clips to dead letters with a creator message, resume from artifacts, cancellation on delete; fairness so one burst of uploads can't starve others | Considers encoding the lowest rung on the device or at ingest to hit ≈ 10 s, and costs it |
| Delivery: ladder, startup, prefetch | One rendition; no ABR; no prefetch | ABR mentioned; no startup or prefetch design | ≈ 3–4 vertical rungs; short first segment (or a small initial file); startup rung chosen from the throughput measured on the *previous* clip (no cold start after the first); prefetch the first ≈ 1–2 s of the next 2–3 clips at a modest rung, full clip only once it starts; immediate down-switch, buffer-gated up-switch | Bounds prefetch by network type and battery, and uses swipe-away statistics to tune how much to prefetch |
| CDN economics & storage | "Add a CDN"; no cost | CDN with defaults; storage in one tier | Byte hit ratio as the KPI; immutable versioned URLs with long TTLs; tokens validated at the edge and excluded from the key; shield with collapsing for viral clips; pre-positioning trending clips the feed is about to push; multi-CDN by region; storage by last access (tail clips' renditions to infrequent access, sources to archive, maybe fewer rungs for clips under N views); cost per hour watched | Uses the feed's knowledge of what it will push as a cache-fill signal, and quantifies the head/tail split's effect on hit ratio |
| Views & events | `UPDATE … + 1` per play | Event log mentioned; no hot-key or fraud handling | Batched player events → log → windowed aggregation → batched counter writes; hot-key handling for viral clips; dedupe and fraud filters; fast approximate display vs exact batch recount for payouts | Ties QoE events (startup, rebuffer, swipe-before-first-frame) to CDN steering and ABR tuning |
| Failure modes, ops & cost | None, or "add retries" | Generic metrics | The three failure walkthroughs answered with what creators and viewers experience; dashboards: time to watchable, queue age per step, dead letters, startup time, rebuffer ratio and error rate per CDN/region, byte hit ratio, egress per hour watched | Cost per hour watched broken into egress, storage and compute, with the top policy lever for each |
| Communication & time | Checkpoints missed by > 8 min; no trade-off table | Finishes but a phase overruns; trade-offs lack flip conditions | Numbers by ≈ 0:10, diagram by ≈ 0:25, stops at 0:40; drives; a trade-off table with ≥ 3 rows and flip conditions | Anticipates probes; says what was cut and why |

**Overall "meets"** requires *meets* or better on **Processing pipeline** and on **CDN economics & storage** (the two
places a video design either works or doesn't), *meets* on **Delivery: ladder, startup, prefetch** or
**Estimation**, and no dimension at *below*. **Overall "exceeds"** requires *meets* everywhere and *exceeds* on at
least three dimensions. Otherwise overall is *approaching* (or *below* with two or more dimensions at *below*).

## Tutor critique protocol

1. **Check the artefacts before probing (≈ 3 min).** Timestamp log present; slips at 0:10 / 0:25 / 0:40 computed.
   Check the estimate sheet's arithmetic: viewer-minutes ≈ 150 M × 50 = 7.5 × 10^9 a day ÷ 1,440 ≈ 5.2 M concurrent
   average, ≈ 10 M at peak; peak egress ≈ 10 M × 1.5 Mbps ≈ 15 Tbps; egress per day ≈ 7.5 × 10^9 min × 60 s ×
   1.5 Mbps ÷ 8 ≈ 85 PB (before prefetch waste); plays ≈ 7.5 × 10^9 min × 2 per minute ≈ 15 B a day ≈ 175k/s average,
   ≈ 350k/s peak; ingest ≈ 20 M × 30 s × 8 Mbps ÷ 8 ≈ 600 TB/day ≈ 55 Gbps average; storage ≈ (ladder ≈ 6.8 Mbps +
   source 8 Mbps) ≈ 110 MB per minute × 10 M minutes ≈ 1.1 PB/day ≈ 400 PB/year; transcode ≈ 6 × 10^8 s × 4 rungs ×
   ≈ 1 core-s ÷ 86,400 ≈ 28k cores. An error ≥ 10× on peak egress, storage growth or plays per second caps
   *Estimation* at "approaching".
2. **Self-score first.** Ask for a level per dimension with a one-line justification before saying anything.
3. **Probe (≈ 10 min).** Ask at least six, including the ones marked (required); one at a time; push one rung
   further on vague answers.
   1. **(required)** "A creator posts a 30-second clip. Walk me from 'upload finished' to 'a stranger can watch it',
      with times."
      *Meets:* upload complete → event via outbox → orchestrator → probe (validate) → lowest rung transcode (≈ 30 s
      of video at ≈ 1 core-s per s, split into a few chunks or run on a fast instance: seconds) and moderation in
      parallel → package + manifest for the low rung → publish v1 *visible to the creator immediately, to others once
      moderation passes* → full ladder as v2. Idempotent keys, bounded retries, poison clip → creator message. Says
      why 4 s segment parallelism buys little for a 30 s clip.
   2. **(required)** "The viewer swipes. How does the next clip show its first frame in 200 ms?"
      *Meets:* the player already has signed URLs for the next N clips from the playback response; it prefetched
      their init segment and first ≈ 1–2 s at a rung chosen from the current measured throughput; on swipe it plays
      from memory and starts fetching the rest; ABR continues per segment. Names the cost: bytes for clips swiped
      past before playing.
   3. **(required)** "How much of your egress is prefetch for clips nobody watched, and what do you do about it?"
      *Meets:* an estimate with stated assumptions (e.g. prefetch 3 clips × 1.5 s × 1.5 Mbps ≈ 0.85 MB per swipe;
      with ≈ 30% of prefetched clips skipped before they play, that's a double-digit percentage of bytes for short
      watch sessions) and levers: prefetch fewer seconds, at a lower rung, fewer clips ahead on cellular, more on
      Wi-Fi; measure swipe-away rates per position.
   4. **(required)** "A clip goes viral: 5 M views in an hour, and the feed knew it would push it. What does the CDN
      see, and what would you do differently because the feed knew?"
      *Meets:* shield + collapsing turn the initial burst into a handful of origin fetches per object; long TTLs;
      byte hit ratio near 100% for that clip after the first minute; because the feed knows, pre-position (warm the
      shield and big edges) before the push; counts via aggregation, not per-play writes, with hot-key handling.
   5. "Most clips get fewer than 100 views. What do you store, in which tier, and for how long?"
      *Meets:* tier by last access; tail renditions to infrequent access after ≈ 30 days without reads; sources to
      instant-retrieval archive (needed for re-encodes, never streamed); maybe only the lowest 2 rungs kept for clips
      under N views, regenerated from source if they revive; never playable content in hour-latency archive;
      ≈ 400 PB/year makes this worth millions per year.
   6. "The moderation service is down for 20 minutes at peak. What happens?"
      *Meets:* the DAG's moderation step retries with backoff; clips are processed and visible to their creators,
      not distributed; the backlog drains when it recovers; alert on moderation queue age; never fail open to public
      distribution.
   7. "The transcode fleet loses a third of its capacity for an hour."
      *Meets:* queue age grows; fast-path tasks (lowest rung + moderation) prioritised so time to watchable degrades
      gracefully while the full ladder lags; spot capacity replaced by an on-demand floor; nothing lost because
      tasks are idempotent and retried.
   8. "Roughly what does an hour of watching cost you, and which policy would you change first?" (credit toward
      *exceeds* only).
4. **Score each dimension with evidence** (quote the write-up or the probe answer). Say what would have moved each
   dimension up one level.
5. **Calibration feedback**: name any dimension where the self-score was off by more than one level.
6. **Decide** with the overall rule above. Below "meets": give 2–3 concrete revision tasks and redo on a different
   day with a variation (suggested: "design the video platform for a bank's internal training: 40 credit unions as
   tenants, compliance courses that must be watched to the end and proven, DRM so videos can't leave the platform,
   viewers on branch networks behind proxies, a few hundred uploads a week").

## Recording

`sd record design C11 --self <below|approaching|meets|exceeds> --tutor <level> --notes "<one-line summary>"`

Example: `sd record design C11 --self meets --tutor approaching --notes "estimates and CDN solid; copied 4 s segment DAG without reasoning; no prefetch cost"`
