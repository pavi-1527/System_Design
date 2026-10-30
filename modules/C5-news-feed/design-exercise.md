# C5 Design exercise — The member activity feed

> Time box: 45 minutes, solo, no notes, timed like a real interview (numbers by 0:10, end-to-end diagram by 0:25,
> stop drilling at 0:40). Then self-score against the rubric (≈ 10 min), then send the write-up to the tutor for the
> probe session. Use the structure from [`docs/INTERVIEW_FRAMEWORK.md`](../../docs/INTERVIEW_FRAMEWORK.md) and the
> numbers from [`docs/NUMBERS.md`](../../docs/NUMBERS.md) — from memory.

This is a *variant* of the module's case study on purpose. The news-feed mechanisms (fan-out, timeline cache,
hydration, tombstones, read-your-writes) all apply, but the graph has a different shape: every member "follows" a
handful of sources, and a few sources are followed by millions. If you reproduce Unit 1's design box for box you
will score "approaching": the rubric rewards *transferring* the decisions, with numbers, to this shape.

## Prompt

> "We run the digital-banking platform for ≈ 40 credit unions, ≈ 12 M members in total. Design the in-app
> **activity feed**: when a member opens the app they see, newest first, their account transactions and card
> authorisations, alerts (fraud, low balance, payment due) and announcements from their credit union. Some
> announcements go to every member of a credit union, some to a segment. A transfer the member just made must show
> up immediately. A reversed transaction must be marked or removed, and a withdrawn announcement must disappear
> within a minute. The underlying records are kept for seven years; the feed itself only needs 90 days."

That is all the interviewer says up front. Ask the clarifying questions you need (write them down with the answer
you'd assume if told "you decide"), and drive.

Things an interviewer would answer if asked (don't read until you've written your questions):

<details data-kind="hint">
<summary>Interviewer's answers to common clarifying questions</summary>

- Members: ≈ 12 M across ≈ 40 credit unions; the largest credit union has ≈ 2 M members, the smallest ≈ 20k.
- Events per day: ≈ 60 M (≈ 40 M transactions and authorisations, ≈ 15 M alerts, the rest status changes).
  ≈ 5 events per member per day on average; a business account can have ≈ 2,000 a day.
- Announcements: ≈ 200 per day platform-wide; a credit-union-wide one reaches up to ≈ 2 M members; segment ones
  reach ≈ 10k–200k. Announcements can be withdrawn.
- Feed opens: ≈ 60 M per day (≈ 5 per member); banking peaks are sharp: payday mornings ≈ 5× the average for an
  hour. 20 items per page.
- Latency: feed read p99 ≤ 300 ms. Freshness: a member's own transfer visible on the next read (immediately);
  fraud alerts visible within ≈ 5 s; other events within ≈ 30 s; announcements within ≈ 1 min.
- Sources: the core banking system and the card processor publish events on an at-least-once stream with a unique
  event ID; duplicates and out-of-order delivery happen. Reversals arrive as new events referencing the original.
- Tenancy: one credit union's statement-day burst must not delay another's fraud alerts; each credit union's data
  must be isolated (A9); the platform is billed per tenant.
- Availability: 99.9% for reads; a slightly stale feed beats an error page; an acknowledged event must never be
  lost from the underlying store (7-year retention, audit).
- Out of scope unless you choose otherwise: push notifications (C7), search, ranking, media.

</details>

## What to produce

1. **Timestamp log**: the clock time at the start of each phase (evidence for time management).
2. **Requirements card**: functional (3–5), non-functional *with numbers* (scale, latency, freshness per event
   class, availability, retention, isolation), explicit out-of-scope, and your clarifying questions with the
   assumptions you proceeded with.
3. **Estimate sheet**: at most 6 numbers with arithmetic and units, each with the decision it drives: event ingest
   rate (avg/peak), feed-read rate (avg/peak, payday factor), fan-out deliveries per day *by source class*
   (per-member events vs announcements), timeline memory for 90 days or a bounded window, hydration rate, and
   storage of the underlying events with retention and replication.
4. **API sketch**: the feed read (cursor, page size), event ingest from the core (idempotent by event ID), announce
   and withdraw an announcement, and whatever the mobile client needs to show a just-made transfer immediately.
5. **Data model**: access patterns first; entities, partition keys and which store holds what: events (7 years),
   per-member timelines (90 days or N entries), announcements and their audiences, tombstones/reversals, tenant
   scoping on every key.
6. **HLD diagram**: the ingest path (core event → durable store → timeline) and the read path (timeline →
   merge announcements → hydrate → filter), with rates on at least three arrows and a one-line justification per
   box.
7. **Deep dives (2–3)**, each with alternatives, a cost and a flip condition:
   - the fan-out strategy *per source class* (why per-member events are trivially pushed, why announcements are
     pulled or pushed, and the number that decides it for a 2 M-member announcement);
   - the timeline store or cache (bounded how? 90 days vs 800 entries; eviction; misses; what a lost node costs);
   - consistency and propagation: read-your-own-transfer, duplicate and out-of-order core events, reversals,
     withdrawn announcements within a minute, tenant isolation of lanes.
8. **Failure walkthrough**: the core replays 6 hours of events after an outage (duplicates, out of order); a
   timeline cache node is lost at 09:00 on payday; a credit union withdraws an announcement sent to 2 M members.

## Rubric

| Dimension | below | approaching | meets (senior bar) | exceeds |
|---|---|---|---|---|
| Requirements & scope | Draws immediately; no freshness classes; tenancy ignored | Lists features; NFRs vague | Freshness stated per event class with numbers; retention split (7 y store vs 90 d feed); isolation and audit named; explicit out-of-scope; assumptions written | Identifies that "own transfer immediately" and "fraud in 5 s" are the two NFRs that shape the design, and negotiates the rest |
| Estimation | Missing, or ≥ 10× off on a number the design depends on | Average rates only; no fan-out by class; no payday factor | ≤ 6 numbers, units carried, payday peak applied, deliveries split by class (per-member ≈ 60 M/day at fan-out 1; announcements ≈ 200/day × up to 2 M), timeline memory bounded, hydration rate; each tied to a decision | Shows that per-member fan-out is cheap *because* the fan-out factor is 1, and that the whole fan-out problem is the announcements; sizes both ways |
| API & data model | Offset paging; no idempotent ingest; tenant missing from keys | Cursor and idempotency mentioned, keys generic | Keyset cursor; ingest idempotent by event ID with dedupe atomic with the effect; partition keys follow access patterns (timeline by member, events by ID with per-account projection, announcements by audience); tenant ID on every key; tombstone/reversal modelled | Reversal modelled as a new event that references the original and is rendered as a state, not a deletion; audience evaluation designed (segment membership at publish vs at read) |
| High-level design | A path can't be traced; no durable store before the feed | Both paths present but boxes unjustified or rates missing | Ingest: durable event store (outbox or the stream itself) → idempotent timeline writer; read: timeline → announcement merge → batched hydration → filter; every box justified; rates on arrows; one event and one read walked through | Lanes per event class and per tenant drawn into the ingest path; degraded modes on the read path (thin page, stale page) explicit |
| Fan-out strategy | Pushes announcements to 2 M timelines synchronously, or pulls everything | Hybrid named without numbers or without the per-class reasoning | Per-member events pushed (fan-out 1); announcements pulled at read from per-audience lists (or pushed only when the audience is small), with the number that decides (2 M deliveries vs ≈ 1 read per member per open), and the merge width bounded (≈ ≤ 5 audience lists per member) | Treats "segment" audiences correctly (membership can change; evaluate at read or materialise with invalidation), plus a lane for mass announcements that can never delay fraud alerts |
| Timeline store & misses | Unbounded; cache as the only copy; no miss path | Bounded but no eviction/miss thinking | Bounded by entries or days with the memory arithmetic; durable events are the truth; misses rebuild from the per-account projection with coalescing and a budget; a lost node's cost quantified | Argues persistent timelines (per-member wide rows, 90-day TTL) vs RAM cache with cost, given fan-out 1 makes writes cheap; warm failover plan |
| Consistency & propagation | "Strongly consistent feed"; duplicates possible; reversals not handled | Idempotency mentioned; reversals or withdrawals hand-waved | Read-your-own-transfer via synchronous write to own timeline or merge from the ledger; dedupe by event ID; sorted idempotent inserts for out-of-order replay; reversals rendered from the current event state at hydration; withdrawn announcements tombstoned at hydration within the 1-minute SLO | Replay of 6 hours reasoned step by step; monotonic reads per session; what the audit trail records about what was shown |
| Failure modes, ops & tenancy | None, or "add replicas" | Generic metrics | Fan-out lag per class and per tenant; tombstone and rebuild metrics; per-tenant lanes and caps; DLQ with tenant and event ID; payday capacity plan; rollout of the cache tier | Cost drivers named (RAM vs SSD timelines; per-tenant billing); migration from a query-time feed to this design without downtime |
| Communication & time | Checkpoints missed by > 8 min; no trade-off table | Finishes but a phase overruns; trade-offs lack flip conditions | Numbers by ≈ 0:10, diagram by ≈ 0:25, stops at 0:40; drives; a trade-off table with ≥ 3 rows and flip conditions | Anticipates probes; notes what was deliberately cut and why |

**Overall "meets"** requires *meets* or better on **Fan-out strategy** and **Consistency & propagation** (the two
places where this design either works or doesn't), and no dimension at *below*. **Overall "exceeds"** requires
*meets* everywhere and *exceeds* on at least three dimensions. Otherwise overall is *approaching* (or *below* with
two or more dimensions at *below*).

## Tutor critique protocol

1. **Check the artefacts before probing (≈ 3 min).** Timestamp log present; slips at 0:10 / 0:25 / 0:40 computed.
   Every NFR has a number and freshness is per class. The estimate sheet's arithmetic: ingest ≈ 60 M/day ≈ 700/s
   average, payday peak ≈ 5× ≈ 3.5k/s; feed reads ≈ 60 M/day ≈ 700/s average, ≈ 3.5k/s peak, ≈ 70k hydrations/s
   peak; per-member deliveries ≈ 60 M/day (fan-out factor 1); announcement deliveries if pushed: ≈ 200/day with up
   to ≈ 2 M each — a single credit-union-wide announcement is ≈ 2 M writes, about **33 days** of a member's normal
   events in one event, which is the number that decides pull for announcements; timeline memory if cached: 12 M ×
   (5 events/day × 90 days ≈ 450 entries, or a cap of 800) × 8 B ≈ 43–77 GB raw (small: this feed *can* be
   persisted per member cheaply, and the learner should notice); events storage ≈ 60 M × ≈ 0.5 KB × 365 × 7 × 3
   replicas ≈ 230 TB. An error ≥ 10× on ingest rate, announcement deliveries or timeline memory caps *Estimation*
   at "approaching".
2. **Self-score first.** Ask for a level per dimension with a one-line justification before saying anything.
3. **Probe (≈ 10 min).** Ask at least six, including the ones marked (required); one at a time; push one rung
   further on vague answers.
   1. **(required)** "A credit union publishes an announcement to its 2 M members at 09:00 on payday. Walk me
      through what happens, with numbers, and what you chose not to do."
      *Meets:* no 2 M-write burst; the announcement lands in a per-audience list; readers merge it at read; the
      number (2 M writes vs ≈ 2 M reads anyway) stated; the withdrawal path follows from the same choice.
   2. **(required)** "The core replays six hours of events after an outage: duplicates, out of order. What does a
      member's feed look like during and after?"
      *Meets:* dedupe by event ID atomic with the timeline write; sorted inserts by event time; reversals and
      state changes applied by hydration from the current event state; the feed never shows duplicates; lag during
      the replay is visible in the fan-out-lag metric; fraud lane protected from the replay's bulk.
   3. **(required)** "A member sends a transfer and the app refreshes 300 ms later. Show me the exact path that
      makes it appear."
      *Meets:* either the transfer service writes to the member's own timeline synchronously (fan-out factor 1
      makes it cheap) before responding, or the read merges the ledger's last few seconds for that member; plus
      the idempotency key that prevents a double transfer on retry.
   4. **(required)** "Why not just query the events table for the member at read time? It's ≈ 5 events a day."
      *Meets:* the query is cheap for one member but the merge with announcements, segments, tombstones and
      multi-source ordering is the work; a per-member timeline (persisted or cached) precomputes it; show the
      read-rate arithmetic (3.5k/s peak × multi-source) and the latency budget; the flip condition (very low read
      rate → query-time is fine).
   5. "A timeline cache node dies at 09:05 on payday. What breaks, and what did you build so it doesn't?"
      *Meets:* replicas or persisted timelines; coalesced, budgeted rebuilds from the per-account projection; a
      thin page; the number of affected members and their read rate.
   6. "A business member has 2,000 events a day. What happens to their timeline and their feed?"
      *Meets:* the per-member bound (entries or days) and the past-the-window pull path; the hot-member case
      doesn't affect others.
   7. "Which lane does a fraud alert ride, and how do you prove it can't be delayed by another tenant's statement
      run?"
      *Meets:* separate consumer group or partition set for alerts; per-tenant caps on the bulk lane; a per-class,
      per-tenant lag SLO with an alert; a load test that floods one tenant and measures another's alert latency.
   8. "What does the audit trail record, and how would you answer 'did member X see alert Y, and when'?"
      *Exceeds:* read receipts or impression logs are a separate stream; the feed service logs page composition
      (IDs served) with retention; privacy and cost noted.
   9. "Roughly what does this cost per month, and which line dominates?" (credit toward *exceeds* only).
4. **Score each dimension with evidence** (quote the write-up or the probe answer). Say what would have moved
   each dimension up one level.
5. **Calibration feedback**: name any dimension where the self-score was off by more than one level.
6. **Decide** with the overall rule above. Below "meets": give 2–3 concrete revision tasks and redo on a different
   day with a variation (suggested: "design the notification inbox for a marketplace app: order updates per buyer,
   seller broadcasts to all their past buyers, platform-wide announcements").

## Recording

`sd record design C5 --self <below|approaching|meets|exceeds> --tutor <level> --notes "<one-line summary>"`

Example: `sd record design C5 --self meets --tutor approaching --notes "pushed announcements to 2M timelines; no dedupe on core replay; read-your-writes path solid"`
