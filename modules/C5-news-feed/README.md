# C5 — Case study: news feed

| | |
|---|---|
| Track | C — HLD case studies |
| Estimated time | 9 h (2 weekly units) — CORE 7 h, STRETCH 2 h |
| Prerequisites | A3, A4, A6 (gates passed) |
| Labs | `labs/c5/` — `feed` (hybrid fan-out timeline service) |
| Gate | quiz ≥ 85% ×2 (different days) · labs pass · design ≥ meets · teach-back pass |

## Why this module exists

A social app launches with the obvious feed: `SELECT * FROM posts WHERE author_id IN (<everyone I follow>) ORDER BY
created_at DESC LIMIT 20`. At 10k users it returns in 8 ms. At 1 M users the `IN` list has 200 entries, the query
touches 200 index ranges on a table growing by 1 M rows a day, and the p99 is 900 ms; the team adds read replicas
and a cache in front of the query with a 30-second TTL, and the feed is now "fast" and 30 seconds stale for
everyone, including the author who just posted and can't see their own post.

So they flip it around: when someone posts, a worker pushes the post ID into a cached timeline list for each
follower, and the feed read becomes one cache lookup. Beautiful, until the first account with 10 M followers posts
a photo of a cat. That one post is 10 M cache writes. The fan-out worker pool, sized for the average author with
200 followers, needs ≈ 14 hours at 200 writes/s per worker × 1 worker, or saturates the whole cache cluster's write
capacity for minutes if you let it. While it runs, every other post queues behind it. Followers of the celebrity
see the cat at wildly different times; followers of ordinary users see nothing new for an hour; the on-call engineer
"fixes" it by scaling workers, the cache cluster falls over, and timelines for 300 M users must be rebuilt from the
database, which is now receiving the 200-range query at 100k reads/s.

Every feed-shaped system has the same shape: a many-to-many graph, writes that are cheap to accept and expensive
to deliver, reads that are frequent and must feel instant. Interviewers ask it because the right answer is not a
component but a *decision with numbers*: push or pull, for whom, how much to cache, and what consistency you
promise. This module walks the 45 minutes once with the decisions made in front of you, then makes you make them
alone.

## Outcomes

- **Scope** a news-feed prompt in six minutes: ask the clarifying questions that change the design (graph shape,
  ordering, consistency of own posts, media), state NFRs as numbers and an explicit out-of-scope list. — CORE
- **Estimate** post and feed-read QPS, fan-out write amplification, timeline-cache memory, post storage with
  replication and feed egress, and tie each number to the decision it drives. — CORE
- **Design** the feed API (idempotent post creation, cursor-paginated feed, follow/unfollow) and a data model whose
  partition keys follow the access patterns (posts by ID and by author, timelines by reader, the graph in both
  directions). — CORE
- **Draw** the post path (API → posts store + outbox → log → fan-out workers → timeline caches) and the read path
  (timeline cache → hydrate → merge celebrities), justify every box, and walk one request through each. — CORE
- **Choose** between fan-out on write, fan-out on read and the hybrid with a celebrity threshold using numbers for a
  50 M-follower account, and **explain when the answer flips**. — CORE
- **Design** the timeline cache: bounded per-user lists, memory sizing, inactive-user eviction, rebuild on miss,
  hot-key and stampede protection, and the fallback past the cached window. — CORE
- **Reason** about feed consistency: read-your-own-posts, out-of-order fan-out, dedupe across sources, propagation
  of deletes and unfollows, and what "eventual" means for a feed. — CORE
- **Implement** a hybrid fan-out timeline service in Go that passes its tests under `-race`: bounded timelines,
  celebrity merge on read, cursor paging without duplicates or skips, idempotent posts, lazy deletes, eager
  unfollows (`labs/c5/feed`). — CORE
- **Explain** how a ranked feed changes the architecture (candidate generation, feature hydration, scoring,
  re-ranking) and what it costs in latency, freshness and storage. — STRETCH

## Unit plan

| Unit | Topic | Hours | Core/Stretch | Lab |
|---|---|---|---|---|
| 1 | [Guided design: requirements, estimation, API and data model, high-level design](unit-1-guided-design.md) | 4 | CORE (3.5) / STRETCH (0.5) | — (conceptual; the estimate sheet and diagrams are the work) |
| 2 | [Deep dives: fan-out strategy, the timeline cache, ranking and consistency, common mistakes](unit-2-deep-dives.md) | 5 | CORE (3.5, incl. ≈ 1.5 h gate work) / STRETCH (1.5) | `labs/c5/feed` |

**Unit 1 (4 h)**
- Session 1 (2 h): warm-up; §1 requirements and clarifying questions (write your own list before the reveal); §2
  the estimate sheet, done by hand before reading the worked version, then `sd estimate -n 5`.
- Session 2 (2 h): §3 API and data model (commit to partition keys before the reveal); §4 the end-to-end design,
  both paths traced with rates on the arrows; the two break-it thought experiments. STRETCH (0.5 h): the media and
  multi-region notes in §4.

**Unit 2 (5 h)**
- Session 1 (2 h): warm-up; §5 fan-out on write vs read vs hybrid (do the threshold arithmetic yourself); start
  `labs/c5/feed` (graph, `Post`, fan-out).
- Session 2 (2 h): §6 the timeline cache and §7 freshness and consistency; finish `Feed`, `Delete`, `Unfollow`;
  run the fan-out amplification break-it; read "Common mistakes"; first `sd quiz C5` attempt.
- Session 3 (1 h, gate): the [design exercise](design-exercise.md) (45 min, solo, timed) and the
  [teach-back](teachback.md) write-up. Second `sd quiz C5` attempt on a later day.
- STRETCH (1.5 h): the ranking pipeline in §7 and the remaining break-it experiments.

## How this connects to your banking work

- **A member's activity feed is a news feed.** Transactions, card authorisations, alerts and messages from the credit
  union, merged newest-first per member, read every time the app opens, with a few "authors" (the credit union, the
  payments core, the fraud system) followed by every member. That is the celebrity problem with a compliance twist:
  the design exercise builds exactly this.
- **Fan-out workers are vendor adapters with a different payload.** The outbox → log → worker → retry → DLQ chain in
  Unit 1 §4 is the A6 pipeline you've built for vendor callbacks; the idempotent "push post ID into timeline" is the
  idempotent effect a retried message needs.
- **Read-your-own-writes after a transfer.** A member who just paid rent and refreshes expects to see it. Unit 2 §7's
  "route the author's reads to the source of truth" is the same decision as showing a just-posted transfer from the
  primary instead of a lagging replica.
- **Deletes and reversals.** A reversed transaction must vanish (or be marked) everywhere it was copied, including
  cached activity lists. Lazy filtering at hydration plus a tombstone is how feeds do it, and how an activity feed
  should.
- **Multi-tenant fairness.** One credit union's statement-day burst must not delay another's fraud alerts: the
  per-tenant lanes and celebrity-style isolation in Unit 2 §5 are the same shape as A6's per-tenant backpressure.

## Mastery gate checklist

- [ ] `sd quiz C5` ≥ 85% — attempt 1 (date: ____)
- [ ] `sd quiz C5` ≥ 85% — attempt 2 on a different day (date: ____)
- [ ] `sd lab C5` — all lab tests pass (with `-race`)
- [ ] Design exercise ([`design-exercise.md`](design-exercise.md)): self-score recorded, tutor score ≥ "meets"
      (`sd record design C5 --self <lvl> --tutor <lvl> --notes "..."`)
- [ ] Teach-back ([`teachback.md`](teachback.md)): tutor marks pass (`sd record teachback C5 --result pass --notes "..."`)

`sd status` is the source of truth; this list is for your own reading.

## What's next

C5 is one of the four case studies (with C1, C2 and C6) that unlock D3, the timed HLD mocks: expect "design a news
feed" or a variant (an activity feed, a notification inbox, a social timeline with ranking) to come up there with
no help. The chat case study (C6) reuses the delivery half of this design: a message to a group of 500 is a fan-out
with stricter ordering and delivery guarantees. Before moving on, run `sd review` and clear any C5 questions it
surfaces, especially tags `fan-out`, `caching` and `consistency`; and re-read the Unit 2 wrap-up table once more the
day before your first D3 mock.
