# A0 — Interview framework & estimation

| | |
|---|---|
| Track | A — HLD fundamentals |
| Estimated time | 12 h (2 weekly units) — CORE 10 h, STRETCH 2 h |
| Prerequisites | none |
| Labs | `labs/a0/` — `capacity` (capacity-modelling package + break-it estimator) |
| Gate | quiz ≥ 85% ×2 (different days) · labs pass · design ≥ meets · teach-back pass |

## Why this module exists

You're 22 minutes into a system design interview for a senior role. You know what a cache is, what a queue is,
roughly how sharding works. You've spent the first 15 minutes on a careful schema and an API with every field
typed. Then the interviewer asks, "How many writes per second is this?" You don't know. You never decided. They
ask, "So why did you put Kafka there?" You say it's scalable. They write something down. At 0:38 they ask what
happens when the database primary fails, and you start drawing a load balancer for the first time. Time's up.

None of that was a knowledge failure. It was three skill failures, and they're the ones this module trains:
no **structure** for the 45 minutes, so time went where it was comfortable rather than where the signal was; no
**numbers**, so no box on the board could be justified or ruled out ("35 writes/s" ends the sharding discussion
before it starts; "93k writes/s" makes it the first deep dive); and no **technique for defending a choice**,
so a reasonable decision sounded like a guess.

Every later module in this course assumes you can do these three things under time pressure: run the 45 minutes
on purpose, produce decision-driving numbers in four minutes, and answer "why X, what does it cost, when would
you choose Y?" without improvising. That's why A0 has no prerequisites and gates everything else.

## Outcomes

- **Derive** the 45-minute framework (phases, time budget, three checkpoints) from what the interviewer must
  assess and from the dependency chain between design decisions, and adapt it when the interviewer changes the
  rules. — CORE
- **Scope** a design prompt in ≤ 6 minutes: core functional requirements, non-functional requirements each with a
  number, and an explicit out-of-scope list. — CORE
- **Estimate** average/peak QPS with a read/write split, storage with retention and replication, ingress/egress
  bandwidth, cache size for a hot set, server count with utilisation and spares, Little's-law concurrency, and
  serial/parallel availability, within ±25%, without a calculator. — CORE
- **Choose** whether a design needs partitioning, caching, a CDN, async buffering or redundancy by comparing each
  estimate with a known threshold, and say when the answer flips. — CORE
- **Defend** a design decision with claim → why here → cost → when it flips, and answer the six-rung probe ladder
  (failure, 10×, why not Y, how would you know, cost, migration). — CORE
- **Drive** the session (plan, signposts, parking lot), follow interviewer hints, and recover at the 0:10 / 0:25 /
  0:40 checkpoints when behind. — CORE
- **Implement** a tested capacity-modelling package in Go, and quantify how several small estimation shortcuts
  compound into a wrong design (break-it). — CORE
- **Estimate** a design's monthly cost and identify the dominant cost line. — STRETCH
- **Adapt** the framework to LLD and machine-coding rounds. — STRETCH

## Unit plan

| Unit | Topic | Hours | Core/Stretch | Lab |
|---|---|---|---|---|
| 1 | [The 45-minute framework](unit-1-interview-framework.md) | 6 | CORE (5) / STRETCH (1) | — (conceptual; rehearsals instead) |
| 2 | [Back-of-envelope estimation](unit-2-estimation.md) | 6 | CORE (5) / STRETCH (1) | `labs/a0/capacity` |

Total: 12 h, CORE 10 h, STRETCH 2 h. On top of this, the `sd estimate` mastery drill (§7 of Unit 2) is best spread
over several short daily sittings; budget ≈ 15 minutes a day for a week.

**Unit 1 — The 45-minute framework (3 × 2 h)**
- Session 1 (2 h): §1 (what is scored, the dependency chain, the time budget), §2 (requirements), §3 (estimation's
  role, API, data model). Do every Predict in writing.
- Session 2 (2 h): §4 (HLD), §5 (deep dives, the trade-off statement, the probe ladder, practice set), §6 (driving,
  following, recovering).
- Session 3 (2 h): §7 (the annotated pastebin walkthrough and the bad-vs-good contrast), then one timed 30-minute
  dry run of the [design exercise](design-exercise.md) (don't record it). §8 (LLD and machine coding) is STRETCH.

**Unit 2 — Back-of-envelope estimation (3 × 2 h)**
- Session 1 (2 h): §1–§3 (arithmetic and units, QPS, storage and bandwidth). Lab: `ParseQuantity`, `HumanBytes` /
  `HumanCount` / `HumanBits`, `BitsPerSec`, `AvgQPS`, `PeakQPS`, `StorageBytes`, `BandwidthBytesPerSec`.
- Session 2 (2 h): §4–§6 (cache, servers and Little's law, latency and availability). Lab: `ServersNeeded`,
  `Concurrency`, `SerialAvailability`, `ParallelAvailability`, `DowntimePerYear`.
- Session 3 (2 h): §7 (the estimate sheet, which numbers matter, compound errors). Lab: `Spec.Validate`,
  `Estimate`, then the break-it exercise. First `sd estimate` runs per kind. §8 (cost) is STRETCH.

After both units: `sd quiz A0` (first attempt), the design exercise for real, the teach-back, and a second quiz
attempt on a different day.

## How this connects to your banking work

- **Vendor adapters are Little's law in production.** Your image-vendor and SSO adapters each hold a pool of
  in-flight calls. When a vendor goes from 300 ms to 5 s, in-flight demand goes up ≈ 17×, and the pool size,
  timeouts and bulkheads decide whether one slow vendor takes down the whole adapter service (Unit 2 §5).
- **Vendor SLAs cap your availability.** A login that synchronously needs the identity provider can never be more
  available than the IdP. Serial availability math turns "the IdP is flaky" into a number and a product decision:
  degrade (honour existing sessions) or accept the ceiling (Unit 2 §6).
- **Idempotency on retried creates.** You already know a retried transfer must not post twice. The same instinct
  drives `Idempotency-Key` on every create in an interview API (Unit 1 §3).
- **Banking traffic has scheduled peaks.** Paydays, month-end and batch-file windows make "consumer 2–3× peak"
  wrong. Deriving the peak from traffic concentration (Unit 2 §2) is something you can check against your own
  platform's access logs.
- **NFRs you already lived with:** vendor rate limits, timeouts, audit retention and PII handling were the
  requirements that shaped your adapters, more than the API fields did. In interviews you do the same scoping on
  a product you've never seen (Unit 1 §2).

## Mastery gate checklist

- [ ] `sd quiz A0` ≥ 85% — attempt 1 (date: ____)
- [ ] `sd quiz A0` ≥ 85% — attempt 2 on a different day (date: ____)
- [ ] `sd lab A0` — all lab tests pass (with `-race`)
- [ ] Design exercise (`design-exercise.md`): self-score recorded, tutor score ≥ "meets"
      (`sd record design A0 --self <lvl> --tutor <lvl> --notes "..."`)
- [ ] Teach-back (`teachback.md`): tutor marks pass (`sd record teachback A0 --result pass --notes "..."`)

`sd status` is the source of truth; this list is for your own reading.

Also do this before you move on (it isn't a recorded gate item, but the design exercise assumes it):
`sd estimate` ≥ 80% within ±25% over 20 consecutive problems that include every kind (`qps`, `storage`,
`bandwidth`, `servers`, `cache`, `concurrency`, `availability`).

## What's next

Passing A0 unlocks the rest of Track A, starting with A1 (networking and API design). Every later module's design
exercise is scored with the same rubric dimensions for structure, quantification and trade-offs, so A0's habits
are re-tested everywhere.

Before you move on, run `sd review` to clear any A0 questions due for spaced repetition, and keep the reference
cards within reach: [docs/INTERVIEW_FRAMEWORK.md](../../docs/INTERVIEW_FRAMEWORK.md) for the 45 minutes and
[docs/NUMBERS.md](../../docs/NUMBERS.md) for the numbers. A short `sd estimate -n 5` before each later design
exercise keeps the arithmetic fast. `sd progress` shows which quiz tags (e.g. `estimation`, `availability`,
`trade-offs`) are your weakest.
