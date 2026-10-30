# D3 Problem bank — interviewer only

> **Spoiler warning.** This is the tutor's file. It holds the prompts, the numbers the interviewer gives when you
> ask, the mid-interview changes and the reference estimates. If you read it, every mock drawn from it stops being
> unseen and no longer counts towards the D3 gate. Don't open it before D3 has passed. The
> [probe bank](probe-bank.md), the [format](format.md) and the [rubric](rubric.md) are yours to study; this file
> isn't.

## What "unseen" means

D3's gate is **4 mocks at ≥ meets on distinct, unseen problems** (format `hld-45`). A problem is *unseen* when the
learner hasn't designed it before: not as a Track C guided or solo design, and not in an earlier mock. A **variant**
of a case study counts as unseen only if its design differs from the case study's in **at least two of these
three**:

1. the **dominant non-functional requirement** (e.g. read throughput → security/one-time semantics; availability →
   strict consistency; single region → data residency);
2. the **data model or partition key**;
3. the **top deep dive** a strong candidate would pick.

Each variant below states which two (or three) differ. If you invent a new variant, write that check down in the
mock log before the mock starts. Re-skinning a problem ("URL shortener, but for images") doesn't make it unseen.

## How to run a problem from this bank

- Read the prompt aloud exactly as written. Don't add requirements.
- Give answer-sheet numbers **only when asked**. If the candidate assumes something reasonable instead, let them
  continue with their own numbers, and check their arithmetic against *their* assumptions, using the reference
  estimates as the check.
- Release the **mid-way change** at ≈ 0:25–0:30, once the end-to-end diagram exists. If there's no diagram by 0:30,
  note it under *Communication & time* and release the change anyway.
- Draw probes from the listed [probe-bank](probe-bank.md) sections and adapt their numbers to the candidate's
  estimates. Ask at least one probe on a risk the candidate didn't choose to deep-dive.
- Record with `sd record mock D3 --format hld-45 --problem "<Pxx title>" --minutes <actual> --level <overall> --notes "<top gap>"`.
  Use the problem title from the table below, exactly as written, so `sd` can recognise repeats.

## Selection rule

1. Read `mocks` for D3 (and D4) in `progress.json` (`sd progress --json`). Never reuse a problem for credit.
   Re-running a problem as practice is fine, but it's recorded as a note, not as a mock.
2. Rotate categories across the four gating mocks: one each of **read-heavy**, **write-heavy**,
   **consistency-critical** and **real-time**, where possible.
3. At least one of the four gating mocks is a **banking/money** problem (marked 💳).
4. Prefer problems whose nearest case studies the learner passed longest ago (retrieval), and avoid problems built
   on a case study that's currently *needs refresh*: clear the refresh first.
5. Start with ★★ problems. Use ★★★ problems for the third and fourth gating mocks or for D4. ★ problems are for
   warm-ups or when the learner is returning after a gap.

| ID | Problem | Nearest case studies | Category | Difficulty |
|---|---|---|---|---|
| P01 | Credit-union transaction alerts 💳 | C7, C2 | real-time | ★★ |
| P02 | Webhook delivery platform for partner banks 💳 | C7, C2, C9 | write-heavy | ★★ |
| P03 | Card-authorization fraud check 💳 | C9, C2, C15 | consistency-critical / real-time | ★★★ |
| P04 | Digital wallet with P2P transfers 💳 | C9, C3 | consistency-critical | ★★ |
| P05 | Hotel reservations with overbooking | C8, C12 | consistency-critical / read-heavy | ★★ |
| P06 | Ride-hailing dispatch | C14, C6 | real-time / write-heavy | ★★★ |
| P07 | Ad-click aggregation with exact billing counts | C15, C1 | write-heavy | ★★★ |
| P08 | Real-time game leaderboard | C12, C4 | read-heavy / real-time | ★★ |
| P09 | Distributed job scheduler (cron at scale) | C7, C3, C4 | write-heavy | ★★ |
| P10 | Distributed message queue | C4, C6 | write-heavy | ★★★ |
| P11 | Online code judge | C11, C8, C7 | write-heavy / security | ★★ |
| P12 | Feature-flag and config distribution | C4, C6 | read-heavy | ★★ |
| P13 | Email inbox service | C10, C12, C6 | write-heavy / read-heavy | ★★★ |
| P14 | Live comments on live video | C6, C5, C11 | real-time | ★★ |
| P15 | Stock-exchange order gateway and matching 💳 | C9, C3, C6 | consistency-critical | ★★★ |
| P16 | Secure short links for bank SMS (variant of C1) 💳 | C1, C7, C9 | consistency-critical / security | ★★ |
| P17 | Multi-region chat with data residency (variant of C6) | C6, C4 | real-time | ★★★ |
| P18 | Regulated document vault (variant of C10) 💳 | C10, C9 | consistency-critical / security | ★★ |
| P19 | Typeahead warm-up: contact search | C12 | read-heavy | ★ |

---

## P01 — Credit-union transaction alerts 💳

**Nearest:** C7, C2 · **Probe sections:** C7 (all), C2 (per-tenant limits), C9 (idempotency) · **Category:** real-time

> "We run a digital banking platform for credit unions. Members want alerts when things happen on their accounts:
> a big card purchase, a low balance, a deposit landing. Design the alerts service."

**Answer sheet (give only when asked):**
- 40 credit unions, 3 M members; 1.2 M members have alerts enabled, ≈ 3 rules each (amount over $X, balance under
  $Y, card-not-present purchase, deposit received).
- The core banking system emits a posting event for every transaction: ≈ 6 M/day, delivered at least once, possibly
  out of order within an account. Each event carries the available balance after posting.
- Peak: payday and month-end mornings are ≈ 5× the average.
- Latency: alert delivered within 30 s of posting at p99 (≈ 5 s at p50).
- Channels: push 80%, SMS 15%, email 5%. Members set quiet hours (for non-urgent alerts) per channel.
- SMS duplicates generate complaints and cost money, so avoid them; a duplicate push is tolerable. A missed alert
  is worse than a late one.
- Availability 99.9%. Each sent alert is kept for 7 years for audit.
- Two SMS vendors under contract, and one of them is known to be flaky.

**Reference estimates:**
- Postings ≈ 6 M / 86,400 ≈ 69/s average, ≈ 350/s peak (5×).
- Alert-enabled share ≈ 1.2 M / 3 M = 40% → ≈ 2.4 M evaluated postings/day; at ≈ 1 in 3 matching a rule, ≈ 0.8 M
  alerts/day ≈ 9/s average, ≈ 46/s peak.
- SMS ≈ 15% × 0.8 M ≈ 120k/day. Audit ≈ 0.8 M × 1 KB ≈ 0.8 GB/day ≈ 0.3 TB/year, ≈ 2 TB over 7 years before
  replication.
- **So what:** throughput is tiny. The design is about correctness: dedupe, ordering, preference timing, vendor
  failover. A candidate who reaches for a large Kafka cluster sized "for scale" without these numbers is
  overbuilding, so probe with "why not X" (C7).

**Strong deep dives:** dedupe and idempotency from the at-least-once event stream through to the SMS vendor;
low-balance flapping (the balance drops below the threshold, recovers, drops again: alert once per crossing, with a
cool-down); vendor failover with breakers and the OTP-vs-marketing priority split.

**Mid-way change (≈ 0:25):** "We're selling this to 400 more credit unions. Each wants its own SMS sender ID and its
own monthly SMS cap." → per-tenant configuration and routing, noisy-neighbour isolation, cap enforcement as a
rate-limiter problem (C2), and ≈ 10× volume, still only ≈ 3.5k postings/s at peak.

**Meets looks like:**
- Quantifies early, and says out loud that scale isn't the hard part.
- Dedupe key (posting ID, rule ID, member) at intake, plus the provider's idempotency key or a send ledger.
- Rule evaluation reads the balance from the event, not from a racing balance query. Out-of-order events are
  handled per account (sequence or event time).
- Per-provider breaker with a short timeout and failover; preferences read at send time.
- Audit store written before the send and updated with the provider's result.

---

## P02 — Webhook delivery platform for partner banks 💳

**Nearest:** C7, C2, C9 · **Probe sections:** C7 (failure, consistency), C2 (per-client limits), C9 (idempotency,
security) · **Category:** write-heavy

> "Our platform emits events — payments settled, accounts opened, cards frozen — and partner fintechs and banks
> want them pushed to their HTTPS endpoints. Design the webhook delivery system."

**Answer sheet:**
- 2,000 partner endpoints. ≈ 500 M events/day, peak 3× average. Payload ≈ 2 KB.
- One large partner accounts for 20% of the volume.
- At any moment ≈ 5% of endpoints are slow (p99 > 5 s) or down. Some go down for a whole weekend.
- Healthy endpoints should get events within 10 s at p99. Failed deliveries are retried for up to 72 h, then sent to
  a dead-letter queue that partners can replay through an API.
- At-least-once delivery. Ordering isn't required (yet).
- Partners must be able to verify that an event came from us and isn't a replay.
- Delivery attempts are kept for 30 days and are queryable by partners.

**Reference estimates:**
- 500 M / 86,400 ≈ 5.8k/s average, ≈ 17k/s peak.
- Egress at peak ≈ 17k × 2 KB ≈ 35 MB/s ≈ 280 Mbps, plus retries.
- In flight (Little's law): healthy ≈ 17k/s × 0.3 s ≈ 5k concurrent requests. If the 20% partner degrades to a
  10 s timeout: 3.4k/s × 10 s ≈ 34k in flight for that partner alone, which is why per-endpoint isolation matters.
- Attempt log ≈ 500 M × ≈ 2.5 KB (payload + attempt metadata) ≈ 1.25 TB/day; 30 days ≈ 37 TB, ≈ 110 TB with 3×
  replication.

**Strong deep dives:** per-endpoint isolation (separate queues or bulkheads, per-endpoint concurrency caps, so one
dead endpoint can't starve the others); retry scheduling (backoff + jitter, delay queues or time-bucketed storage,
72 h of backlog for a dead endpoint, e.g. 5% of traffic × 72 h); signing (HMAC over timestamp + body, rotating
secrets, replay window).

**Mid-way change (≈ 0:30):** "Partners now want events delivered in order per account." → per-(endpoint, account)
ordering keys, head-of-line blocking (one failing event blocks that account's later events, so decide whether to
park and skip or to block), partitioning by account within an endpoint, and the throughput loss.

**Meets looks like:**
- Isolation is quantified with Little's law, not just asserted.
- A durable intake log/outbox, so an event accepted from internal producers is never lost.
- Concrete retry policy and DLQ with replay; idempotency guidance to partners (event ID), because at-least-once
  delivery means duplicates.
- HMAC signing with a timestamp and secret rotation; SSRF protection on partner-supplied URLs (no private IP
  ranges).
- SLIs: delivery latency per endpoint tier and backlog age per endpoint.

---

## P03 — Card-authorization fraud check 💳

**Nearest:** C9, C2, C15 · **Probe sections:** C9 (consistency, failure), C2 (sliding windows, hot keys), C15
(late data, monitoring) · **Category:** consistency-critical / real-time · ★★★

> "We process card authorizations for a group of issuers. Before approving, we want a fraud decision. Design the
> fraud-check service that sits in the authorization path."

**Answer sheet:**
- ≈ 5k authorizations/s average, 20k/s peak (holiday weekends).
- The network gives the issuer ≈ 100 ms end to end (TODO-VERIFY: real network and issuer timeouts vary). Our fraud
  decision gets **50 ms at p99**.
- Inputs: card, amount, merchant/MCC, country, device or terminal data. Features include velocity counters per card
  (count and amount over 1 min / 1 h / 24 h), merchant risk score, and distance from the last transaction.
- 100 M cards issued, ≈ 30 M used on a given day.
- A model scoring service exists (≈ 10–20 ms p99, TODO-VERIFY for any specific model); you design around it.
- Availability 99.99% for the authorization path. If fraud is unavailable, authorizations must still be decided.
- Chargebacks arrive weeks later and are the training labels.
- Every decision is stored with its inputs for 5 years.

**Reference estimates:**
- 5k/s × 86,400 ≈ 432 M decisions/day. Decision log ≈ 432 M × 1 KB ≈ 432 GB/day ≈ 158 TB/year before
  replication.
- Velocity state ≈ 100 M cards × 6 counters × 16 B ≈ 9.6 GB: fits in an in-memory store with replicas.
- In flight at peak ≈ 20k/s × 30 ms ≈ 600 concurrent requests.
- Latency budget: ≈ 1–2 ms network, feature reads in parallel ≈ 5 ms, model ≈ 20 ms, rules ≈ 2 ms. That leaves
  ≈ 20 ms headroom, so there's no room for a synchronous database call on a cold path.

**Strong deep dives:** the feature store under a 50 ms budget (in-memory sliding-window counters updated on the
authorization itself, consistency between "read velocity" and "update velocity" for concurrent authorizations on
the same card); the fail-open/fail-closed policy by amount and merchant risk (stand-in rules); the decision log and
label feedback loop.

**Mid-way change (≈ 0:28):** "A regulator now requires a human-readable reason for every decline, and we must be able
to reproduce exactly why a decision was made, 5 years later." → version and store model/rule versions plus the
exact feature values with each decision, reason codes from rules or model explanations, and immutable storage.

**Meets looks like:**
- Latency budget split explicitly, and everything slow taken off the synchronous path.
- A fallback decision with a defended policy (e.g. approve under $50 at low-risk merchants, otherwise decline or
  step-up) and the business cost of each choice.
- Velocity counters: an atomic increment-and-read per card, and a stated accuracy window.
- Async decision logging (outbox/log) so storage hiccups don't block authorizations.
- SLIs: p99 decision latency, fallback rate, decline rate per issuer (a sudden change means a bad model or rule
  deploy).

---

## P04 — Digital wallet with P2P transfers 💳

**Nearest:** C9, C3 · **Probe sections:** C9 (all), C3 (IDs for transfers), C7 (notifications) · **Category:**
consistency-critical

> "Design a digital wallet: users top up from their bank account, send money to friends instantly, and cash out."

**Answer sheet:**
- 30 M registered users, 10 M monthly active. ≈ 5 M P2P transfers/day. Peak ≈ 10× the average on holidays and the
  1st of the month (rent).
- Balance views ≈ 50 M/day.
- A P2P transfer is confirmed to both users within 1 s at p99. Money is never lost or duplicated. Availability
  99.95%.
- Top-ups and cash-outs go through ACH via a partner bank, settle in 1–3 business days (TODO-VERIFY: same-day ACH
  windows change this) and can be returned (failed) days later.
- Fees: 1% on instant cash-out, otherwise free.
- Records kept 7 years. Single region to start.

**Reference estimates:**
- Transfers ≈ 5 M / 86,400 ≈ 58/s average, ≈ 600/s peak.
- Postings per transfer: 2, or ≈ 4 with fees and clearing → ≈ 2.4k postings/s peak. That's inside one primary's
  ≈ 1k–10k TPS range, but at its edge, so the candidate should plan account-partitioning as the 10× step, not build
  it on day one.
- Ledger storage ≈ 5 M × 4 × 200 B ≈ 4 GB/day ≈ 1.5 TB/year, ≈ 10 TB for 7 years before replication.
- Balance reads ≈ 580/s average, ≈ 3k/s peak: display reads come from replicas/cache, spend decisions from the
  authoritative balance.

**Strong deep dives:** the double-entry ledger and how a P2P transfer posts atomically (idempotency key → postings
in one transaction → outbox); top-ups with pending funds and ACH returns (provisional credit policy, what happens
when someone spends funds that are later returned, i.e. negative-balance risk); hot accounts and concurrency on the
same wallet.

**Mid-way change (≈ 0:25):** "Now cross-border: send USD, receiver gets EUR. The FX quote is valid for 30 seconds."
→ quote as a stored object with expiry, FX via currency-specific accounts, a quote-ID-bound transfer, rounding
rules.

**Meets looks like:**
- Postings sum to zero per transfer, balances are derived, and corrections are reversals, never edits.
- Idempotency key on every money-moving API, with request-hash checking.
- Pending state machine for ACH, with reconciliation against partner-bank files.
- Per-account serialization mechanism named (row lock or conditional update), with its throughput cost.
- Fraud and limits mentioned (new-account velocity limits) without derailing the session.

---

## P05 — Hotel reservations with overbooking

**Nearest:** C8, C12 · **Probe sections:** C8 (all), C12 (search caching), C9 (payment holds) · **Category:**
consistency-critical / read-heavy

> "Design the reservation system for a hotel chain: guests search for rooms and book stays."

**Answer sheet:**
- 5,000 hotels, ≈ 200 rooms each on average, ≈ 10 room types per hotel; bookable up to 365 days ahead.
- Searches ≈ 50 M/day, peak 3×; bookings ≈ 500k/day, with flash promotions up to 50 bookings/s.
- Average stay 3 nights. Cancellation rate ≈ 10%.
- Search results may be slightly stale (a minute is fine); a confirmed booking must be honoured.
- The business **overbooks** standard rooms by up to 5% (based on no-shows), never suites. Guests who are walked
  are rebooked at a partner hotel, at a cost.
- Booking confirmation within 2 s at p99; payment is a card authorization at booking, capture at check-in.

**Reference estimates:**
- Inventory rows ≈ 5,000 × 10 × 365 ≈ 18 M rows (hotel, room type, night) × ≈ 100 B ≈ 1.8 GB: fits one database
  comfortably.
- Searches ≈ 50 M / 86,400 ≈ 580/s average, ≈ 1.7k/s peak; bookings ≈ 5.8/s average, 50/s peak.
- Look-to-book ≈ 100:1 → cache search heavily, check availability exactly at booking.
- Each booking decrements ≈ 3 rows atomically.

**Strong deep dives:** the inventory model (counts per (hotel, room type, night), not per physical room, until
check-in) and the atomic multi-night decrement; overbooking as a capacity of `rooms × 1.05` per type, with the walk
policy; search caching versus the booking-time truth.

**Mid-way change (≈ 0:28):** "The same rooms are also sold on three external booking sites through a channel
manager that syncs every few minutes." → our inventory is no longer the only writer, so allocate per-channel
allotments or accept overbooking risk, confirm external bookings with reconciliation, and absorb any oversell within
the overbooking margin.

**Meets looks like:**
- Data model shaped by the access pattern (date ranges per room type); the multi-night update is atomic (one
  transaction, rows in a fixed order).
- Overbooking modelled explicitly, not as an accidental race.
- Search path cached with a stated staleness, and a re-check at booking with a clear error when sold out.
- Payment authorization tied to the hold, with expiry.
- Not over-engineered: a single partitioned relational store is defended with the 1.8 GB / 50 writes/s numbers.

---

## P06 — Ride-hailing dispatch

**Nearest:** C14, C6 · **Probe sections:** C14 (all), C6 (connections, failure), C8 (contention: a driver
accepts two trips) · **Category:** real-time / write-heavy · ★★★

> "Design the dispatch system for a ride-hailing app: a rider requests a ride and we find them a driver."

**Answer sheet:**
- 300 cities. ≈ 1 M drivers online at global peak, the largest city ≈ 100k. Drivers send a location every 4 s.
- ≈ 20 M rides/day, peak 3× the average.
- Match a driver within 5 s at p95. A driver has 10 s to accept an offer.
- A driver must never be assigned two trips at once; a rider never gets two drivers.
- Riders see their driver's car move on the map (updates every ≈ 2–4 s).
- Availability 99.95%; a region-level outage may degrade service.

**Reference estimates:**
- Location writes ≈ 1 M / 4 s ≈ 250k/s; at ≈ 50 B each ≈ 12.5 MB/s ingest.
- Live index memory ≈ 1 M × 100 B ≈ 100 MB: tiny. The problem is the write rate and per-city locality, not size.
- Rides ≈ 20 M / 86,400 ≈ 230/s average, ≈ 700/s peak. Trip records ≈ 20 M × 2 KB ≈ 40 GB/day.

**Strong deep dives:** the location index (in-memory geo cells sharded by city/cell, TTL'd entries, no durable write
per ping); the matching protocol (offer to one driver at a time with a 10 s timeout vs a batched assignment per
cell every ≈ 2 s; the atomic state change of driver available → offered → on trip); the trip state machine and its
consistency across rider app, driver app and payment.

**Mid-way change (≈ 0:28):** "Add pooled rides: two riders heading the same way share a car." → matching becomes a
batched optimization over pending requests within a time window, the trip model gains multiple riders, the ETA
promise loosens, and the state machine gets new transitions.

**Meets looks like:**
- 250k writes/s handled in memory, sharded by geography, with a stated loss tolerance (one update interval).
- Neighbour cells queried; candidates ranked by ETA, not raw distance (or says why not yet).
- Driver assignment uses a conditional state transition (CAS) or a single owner per driver; no double assignment
  under concurrency.
- The persistent-connection path for driver/rider apps (C6) and reconnect behaviour.
- SLIs: match time p95, offer acceptance rate, location staleness.

---

## P07 — Ad-click aggregation with exact billing counts

**Nearest:** C15, C1 · **Probe sections:** C15 (ingest, late data, cardinality), C1 (analytics pipeline), C9
(reconciliation) · **Category:** write-heavy · ★★★

> "Advertisers pay per click. Design the system that counts ad clicks, shows advertisers near-real-time stats, and
> produces the numbers we bill on."

**Answer sheet:**
- ≈ 1 B clicks/day, peak 5× the average during big live events.
- Click event ≈ 200 B: click ID, ad ID, campaign, timestamp, user/device hash, IP.
- 2 M active ads. Dashboards: clicks per ad per minute, at most 1 min stale.
- Billing: daily per campaign, **exact**: no double-counting, no lost clicks.
- Clients retry, so ≈ 1–2% of events are duplicates. 99% of events arrive within 1 min, 0.9% within 1 h, and
  events older than 24 h are dropped.
- Raw events kept 90 days, aggregates 3 years.

**Reference estimates:**
- ≈ 1 B / 86,400 ≈ 11.6k/s average, ≈ 58k/s peak.
- Raw ≈ 1 B × 200 B ≈ 200 GB/day; 90 days ≈ 18 TB before replication.
- Minute aggregates: at most 2 M × 1,440 ≈ 2.9 B ad-minutes/day, in practice sparse (most ads get few clicks).
- Dedupe state: click IDs for the 24 h lateness window ≈ 1 B × 16 B ≈ 16 GB (or a Bloom filter at ≈ 10 bits per ID
  ≈ 1.25 GB with ≈ 1% false positives, which is unacceptable for exact billing on its own).

**Strong deep dives:** the streaming aggregation (event-time windows, watermarks, late data handled as corrections);
exact billing by deduping on click ID with a batch recomputation from the raw log as the source of truth (stream for
speed, batch for money); hot ads during a live event (one key gets ≈ 5% of 58k/s, so pre-aggregate per partition and
merge).

**Mid-way change (≈ 0:28):** "Fraud detection flags clicks as invalid up to an hour later; billed counts must exclude
them, and advertisers' dashboards should reflect it." → retractions as negative events keyed by click ID,
aggregates that support subtraction, billing finalized after the fraud window closes, and dashboards showing
"provisional" vs "final".

**Meets looks like:**
- Separates the freshness path (approximate, minute-level) from the billing path (exact, deduped, recomputable).
- Dedupe key and window sized from the lateness numbers.
- A durable log as the system of record, with replay as the recovery story.
- Cost answer: raw retention and the stream-processing cluster dominate; the lever is compaction into aggregates.
- SLIs: end-to-end lag, late-event rate, stream-versus-batch reconciliation difference (alert above ≈ 0.1%).

---

## P08 — Real-time game leaderboard

**Nearest:** C12, C4 · **Probe sections:** C12 (top-k, caching), C4 (partitioning, hot keys), C5 (fan-out to
friends) · **Category:** read-heavy / real-time

> "Design the leaderboard for a mobile game: players see the global top 100 and their own rank."

**Answer sheet:**
- 50 M DAU, 5 M concurrent at peak; 80 M players with a score this season. Seasons last one month, then reset.
- Score updates ≈ 100k/s at peak (end of each match); leaderboard reads ≈ 200k/s at peak.
- The top 100 must be exact and at most ≈ 1 s stale. "Your rank" should be exact in the top 10,000; beyond that,
  "top 12%" is acceptable.
- Scores only go up within a season. Cheating exists.

**Reference estimates:**
- Sorted-set memory ≈ 80 M × ≈ 100 B (TODO-VERIFY: per-entry overhead depends on the store) ≈ 8 GB, which fits one
  node's memory, but 100k writes/s + 200k reads/s is ≈ 3× a single node's ≈ 100k+ ops/s.
- The top-100 view cached for 1 s means ≈ 1 read/s per region against the store, instead of 200k/s.
- Approximate ranks from a score histogram: e.g. 10,000 buckets × 8 B = 80 KB, recomputed every few seconds.

**Strong deep dives:** exact top-k plus exact rank for the head (a dedicated sorted set holding only the top ≈ 10k,
fed by threshold filtering) and an approximate percentile for the tail; sharding the full set by player (rank =
scatter-gather of "count above score" across shards, so cost grows with shard count) versus bucketing by score
range; anti-cheat (server-authoritative scores, plausibility limits).

**Mid-way change (≈ 0:28):** "Add a friends leaderboard: rank among your ≈ 200 friends." → that's a fan-out problem
(C5): compute on read from friends' scores (200 point lookups, cheap) rather than maintaining per-user sorted sets.

**Meets looks like:**
- Realises the exact global rank for 80 M players isn't needed, and negotiates it with the numbers.
- Caching of the top 100 with the 1 s staleness requirement.
- Write path: score update → durable store → leaderboard store asynchronously; a rebuild path if the in-memory store
  is lost.
- Season reset without downtime (new key per season).
- SLIs: read p99, update-to-visible lag.

---

## P09 — Distributed job scheduler (cron at scale)

**Nearest:** C7, C3, C4 · **Probe sections:** C7 (retries, dedupe), C4 (partitioning, membership), C3 (job/run
IDs) · **Category:** write-heavy

> "Teams across the company want to schedule jobs — every hour, every Monday, once next Tuesday at 9. Design a
> scheduler service that triggers them."

**Answer sheet:**
- 10 M job definitions; ≈ 240 M firings/day in total.
- 30% of jobs are hourly at minute :00, so ≈ 3 M firings are due at the top of every hour.
- A firing must be dispatched within 5 s of its scheduled time at p99. We only *trigger* the job (an HTTP call or a
  message onto the owning team's queue); teams run their own workers.
- At-least-once triggering, with a run ID teams can dedupe on.
- Missed-fire policy per job: "run once on recovery" or "skip".
- Run history kept 30 days.

**Reference estimates:**
- Average ≈ 240 M / 86,400 ≈ 2.8k firings/s.
- Top-of-hour burst ≈ 3 M within 5 s ≈ 600k dispatches/s. That burst, not the average, sizes the system.
- History ≈ 240 M × 500 B ≈ 120 GB/day ≈ 3.6 TB for 30 days.

**Strong deep dives:** storage of next-fire times (time-bucketed, partitioned by job ID hash, pollers that own
partitions via leases, so no double-firing from two pollers); the :00 spike (pre-load the next minute's due jobs,
spread jitter where jobs allow it, dispatch queue with backpressure to teams' endpoints); failure and missed-fire
handling (a poller dies, so its lease expires and another takes over; the run ID makes overlap safe).

**Mid-way change (≈ 0:28):** "Jobs can now depend on other jobs: run B after A succeeds." → a workflow/DAG layer
with run states, completion callbacks from teams' workers, timeouts, and the scheduler only triggering roots.

**Meets looks like:**
- Sizes for the 600k/s burst and explains how (pre-fetch, parallel partitions, jitter).
- Partition ownership with leases plus fencing, or an idempotent run ID, to avoid double triggers.
- Durable schedule store; computes the next fire time after each run (cron parsing, time zones, DST mentioned).
- Isolation: one team's slow endpoint doesn't delay others' dispatch.
- SLIs: schedule lag (actual − scheduled) p99, missed-fire count.

---

## P10 — Distributed message queue

**Nearest:** C4, C6 · **Probe sections:** C4 (replication, membership, repair), C6 (ordering, dedupe) ·
**Category:** write-heavy · ★★★

> "Design a durable, distributed message queue that services use to publish and consume events."

**Answer sheet:**
- Ingest ≈ 400 MB/s average, 1 GB/s peak; messages ≈ 1 KB on average.
- On average 3 consumer groups read each topic.
- Retention 7 days. Replication factor 3; an acknowledged message must survive the loss of any one machine.
- Ordering within a key (e.g. per account) is required, global ordering isn't.
- Publish latency p99 < 20 ms within a region.
- Consumers may be slow or down for hours and must resume from where they left off.

**Reference estimates:**
- Peak ≈ 1 GB/s / 1 KB ≈ 1 M msgs/s.
- Storage ≈ 400 MB/s × 86,400 ≈ 34.6 TB/day; × 7 days ≈ 242 TB; × 3 replicas ≈ 726 TB.
- Network at peak: 1 GB/s in + 2 GB/s replication + 3 GB/s to consumers ≈ 6 GB/s ≈ 48 Gbps across the cluster.
- Partitions: at an assumed ≈ 10 MB/s write per partition (TODO-VERIFY: depends on hardware and settings), ≥ 100
  partitions at peak; more for consumer parallelism.

**Strong deep dives:** the partitioned append-only log with leader/follower replication (in-sync replica set or
quorum; ack after the replicas have the write; leader election via a coordination service); consumer offsets and
consumer groups (offset commits, rebalancing, at-least-once vs at-most-once depending on when offsets are committed);
retention and storage (segments, tiering old segments to object storage to cut the 726 TB).

**Mid-way change (≈ 0:30):** "Some teams need exactly-once processing: consume, transform, produce to another
topic." → producer IDs + sequence numbers for idempotent produce, transactional writes of output plus consumer
offsets, and the honest caveat that exactly-once ends at the queue boundary: external side effects still need
idempotency.

**Meets looks like:**
- Partitioning by key for ordering; the trade of more partitions vs per-key ordering and rebalancing cost.
- Ack semantics tied to the durability requirement (acks after replication, min in-sync replicas = 2).
- Failure walk-through: leader dies → election → what acknowledged data is safe and what unacked data the producer
  retries (idempotent producer).
- Consumer lag as the key SLI; slow consumers don't affect producers (pull model).
- Storage cost addressed (tiering), with the 726 TB number.

---

## P11 — Online code judge

**Nearest:** C11, C8, C7 · **Probe sections:** C11 (job pipeline, workers), C8 (flash crowds), C7 (queues,
retries), plus security probes · **Category:** write-heavy / security

> "Design an online judge: users submit code for programming problems and get a verdict. We also host timed
> contests."

**Answer sheet:**
- ≈ 2 M submissions/day normally. Contests: 30k participants, ≈ 300k submissions in the final 10 minutes.
- 10 supported languages. Each submission runs ≈ 20 test cases, ≈ 2 CPU-seconds in total.
- Verdict within 10 s at p95 normally, within 60 s during contests.
- Submitted code is untrusted: it must not reach the network, other submissions or the host.
- A live contest scoreboard (see the mid-way change).
- Source ≈ 10 KB; test data per problem up to 1 GB.

**Reference estimates:**
- Normal ≈ 2 M / 86,400 ≈ 23/s. Contest burst ≈ 300k / 600 s = 500/s.
- CPU at burst ≈ 500/s × 2 CPU-s ≈ 1,000 cores busy; with 32-core workers ≈ 32 machines at 100%, ≈ 45–50 with
  headroom; normal load needs ≈ 46 cores.
- Test data: cache per problem on workers (1 GB × active problems), not fetched per run.

**Strong deep dives:** the sandbox (process isolation with containers plus syscall filtering, or microVMs;
CPU/memory/time/output limits; no network; read-only file system; TODO-VERIFY isolation strength claims for any
specific technology); queueing and fairness (contest submissions prioritized, per-user rate limits, pre-scaling
before scheduled contests); judging correctness (deterministic time measurement, re-judge on infrastructure error,
idempotent verdict writes).

**Mid-way change (≈ 0:28):** "The contest needs a live scoreboard for 30k participants and 200k spectators, updated
every few seconds." → scoreboard computed from verdict events, cached and served as a static snapshot every ≈ 5 s
(CDN), and a scoreboard "freeze" in the last hour.

**Meets looks like:**
- Treats the sandbox as the top risk and is concrete about limits and escape blast radius.
- Sizes worker CPU from the contest burst; autoscaling plus pre-scaling.
- A queue between submission and judging; at-least-once judging with idempotent verdicts.
- Separates the verdict store from the scoreboard view.
- SLIs: queue wait time per priority, infrastructure-error re-judge rate.

---

## P12 — Feature-flag and config distribution

**Nearest:** C4, C6 · **Probe sections:** C4 (replication, consistency), C6 (push connections, reconnects), C2
(per-tenant rollout limits) · **Category:** read-heavy

> "Design a feature-flag service: engineers toggle features and roll them out gradually, and every service and app
> picks up changes quickly."

**Answer sheet:**
- 2,000 services, 200k server instances, 5 M mobile app sessions.
- 50k flags; ≈ 1k flag changes/day. A service subscribes to its ≈ 500 relevant flags (≈ 500 KB of rules).
- Changes reach every server within 10 s at p99 (mobile within ≈ 1 min).
- Flag evaluation happens inside the app (SDK), < 1 ms, on every request. No network call per evaluation.
- Percentage rollouts must be sticky per user (the same user always gets the same variant).
- If the flag service is down, apps keep working with the last known values.
- Changes are audited, and some flags (payments) require approval.

**Reference estimates:**
- Total rule data ≈ 50k × 1 KB ≈ 50 MB: tiny.
- Naive polling: 200k instances every 10 s ≈ 20k req/s, mostly "not modified". Fine with conditional requests, but
  streaming is better for latency.
- Naive full-payload push on every change: 200k × 500 KB ≈ 100 GB per change, × 1k changes/day ≈ 100 TB/day. Hence
  deltas or versioned per-flag updates.

**Strong deep dives:** distribution (streaming connections from SDKs to a relay tier, version numbers per flag set,
deltas, fallback to polling, reconnect storms); resilience (SDK caches last-known-good on local disk, defaults on cold
start with no cache, so the flag service is never on the critical path); sticky bucketing (hash(flag key, user ID) mod
100, stable across servers, with no state).

**Mid-way change (≈ 0:28):** "Last month a flag change broke payments for 20 minutes. Flag changes now need staged
rollout and automatic rollback." → treat config changes as deploys: progressive exposure (1% → 10% → 100%) gated
on health metrics, an automatic revert on SLO burn, and an approval workflow for tagged flags.

**Meets looks like:**
- The flag service is off the request path; local evaluation and last-known-good caching are explicit.
- Propagation designed for 10 s with the bandwidth numbers (deltas, relays).
- Consistent hashing for rollouts; explains why random-per-request is wrong.
- Audit log and kill switch.
- SLIs: propagation lag (version seen by SDKs vs latest), SDK staleness, connection count per relay.

---

## P13 — Email inbox service

**Nearest:** C10, C12, C6 · **Probe sections:** C10 (metadata vs blobs, dedupe), C12 (search index), C6 (sync,
offline) · **Category:** write-heavy / read-heavy · ★★★

> "Design the backend of an email service: receiving mail, storing it, and letting users read and search their
> inbox."

**Answer sheet:**
- 50 M users, 20 M DAU; 15 GB quota per user.
- ≈ 1 B inbound messages/day, peak 2× the average; ≈ 40% rejected as spam at intake.
- Average stored message ≈ 50 KB; 5% of messages carry attachments averaging ≈ 1 MB.
- Reads: ≈ 50 list/read operations per DAU per day. Search must return within 500 ms at p95.
- Once we accept a message (SMTP 250), it must never be lost. Read availability 99.9%.
- Mailing lists: one message can go to thousands of our users.

**Reference estimates:**
- Inbound ≈ 1 B / 86,400 ≈ 11.6k/s average, ≈ 23k/s peak; stored ≈ 600 M/day.
- Bodies ≈ 600 M × 50 KB ≈ 30 TB/day; attachments ≈ 600 M × 5% × 1 MB ≈ 30 TB/day → ≈ 60 TB/day ≈ 22 PB/year
  before replication and dedupe.
- Reads ≈ 20 M × 50 ≈ 1 B/day ≈ 11.6k/s average.
- **So what:** storage dominates cost, so dedupe identical attachments/bodies across recipients and tier cold mail.

**Strong deep dives:** storage split (per-user mailbox metadata partitioned by user ID; bodies and attachments in a
blob store, content-addressed so a mailing-list message is stored once); the intake pipeline (SMTP → durable queue
→ spam/virus checks → delivery to mailboxes, with the ack only after a durable write); per-user search index
(inverted index sharded by user, built asynchronously, with freshness of seconds).

**Mid-way change (≈ 0:28):** "A mailing list sends one message to 50k of our users at once, and 20 such lists fire at
9:00." → fan-out of metadata entries (1 M mailbox inserts), body stored once, delivery queue with backpressure;
explicitly C5's fan-out problem.

**Meets looks like:**
- Durability of accepted mail is explicit (replicated queue before the 250 OK).
- Partition by user, with the reasoning (all access is per mailbox).
- Storage numbers drive dedupe and tiering decisions.
- Search scoped per user, which keeps index shards small.
- SLIs: intake queue age, delivery latency (accept → visible), search p95.

---

## P14 — Live comments on live video

**Nearest:** C6, C5, C11 · **Probe sections:** C6 (connections, fan-out), C5 (celebrity fan-out, ranking), C11
(live events, egress) · **Category:** real-time

> "Design live comments for a live-streaming platform: viewers post comments and everyone watching sees them
> scroll by."

**Answer sheet:**
- 50k concurrent live streams, 20 M concurrent viewers in total; the biggest stream has 2 M viewers.
- Posting: the biggest stream receives ≈ 5k comments/s; the platform ≈ 100k comments/s at peak.
- A viewer sees a comment within 2 s of it being posted, at p95.
- A human can read ≈ 10–20 comments/s; nobody needs all 5k/s.
- Moderation: banned words are blocked immediately; ML moderation can take ≤ 1 s.
- Comments are replayable alongside the VOD afterwards.

**Reference estimates:**
- Delivery per viewer capped at ≈ 20 comments/s × 200 B ≈ 4 KB/s.
- Biggest stream: 2 M × 4 KB/s ≈ 8 GB/s ≈ 64 Gbps of fan-out egress. Platform: 20 M × 4 KB/s ≈ 80 GB/s ≈ 640 Gbps
  if every stream were that busy (most aren't, so the real figure is much lower).
- Without sampling, the biggest stream would be 2 M × 5k × 200 B ≈ 2 TB/s, which is impossible. Sampling is
  mandatory, not an optimization.
- Connections: 20 M at ≈ 50k–100k per gateway ≈ 200–400 gateways.

**Strong deep dives:** fan-out architecture (pub/sub per stream to the gateways holding viewers, not per viewer;
gateways sample or rank locally); sampling/ranking per viewer (always show own comments, comments from friends and
the creator, and a sample of the rest); hot-stream handling (shard a stream's channel across multiple pub/sub
nodes; tiered fan-out).

**Mid-way change (≈ 0:28):** "Paid 'super comments' must be shown to every viewer, pinned for 30 s, and billed." →
a separate reliable channel bypassing sampling, payment captured before broadcast (idempotent), and accounting for
delivery per viewer as best effort but billing per purchase.

**Meets looks like:**
- Quantifies the impossibility of full fan-out and makes sampling the core decision.
- Gateway-level fan-out with a pub/sub tier; reconnect behaviour.
- Read-your-writes for the poster's own comment.
- Moderation inline (cheap blocklist) vs async (ML) with retraction.
- SLIs: post → visible latency, dropped-comment ratio at gateways, connection churn.

---

## P15 — Stock-exchange order gateway and matching 💳

**Nearest:** C9, C3, C6 · **Probe sections:** C9 (consistency, audit), C3 (sequencing, ordering), C6 (ordering,
fan-out) · **Category:** consistency-critical · ★★★

> "Design the core of a small stock exchange: brokers submit and cancel orders, orders get matched, and everyone
> receives market data."

**Answer sheet:**
- 5,000 symbols; ≈ 100k order messages/s at peak (new, cancel, modify), the busiest symbol ≈ 10% of that.
- Order acknowledgement within 5 ms at p99 inside the data centre (brokers are co-located).
- Matching is price-time priority per symbol, and must be deterministic and auditable.
- Zero loss of acknowledged orders. A matching-engine failure must be recovered within seconds.
- ≈ 10k market-data subscribers; trading day 6.5 h, average ≈ 30k messages/s.
- Pre-trade risk checks per broker (credit limit).

**Reference estimates:**
- Daily ≈ 6.5 h × 3,600 s × 30k/s ≈ 23,400 × 30k ≈ 702 M messages; at ≈ 200 B ≈ 140 GB/day of event log.
- Busiest symbol ≈ 10k orders/s: a single-threaded engine per symbol handles that (in-memory book operations take
  ≈ microseconds, TODO-VERIFY for any specific implementation).
- Market data: ≈ 2 updates per order ≈ 200k/s × 100 B ≈ 20 MB/s per full-feed subscriber; × 10k unicast ≈ 200 GB/s,
  hence multicast, tiered distribution or conflated feeds.
- Same-DC round trip ≈ 0.1–0.5 ms: synchronous replication to a standby fits inside the 5 ms budget.

**Strong deep dives:** sequencing and determinism (a sequencer assigns a global or per-symbol sequence, events are
written to a replicated log before the ack, and the engine is a deterministic state machine over the log, so replay
reconstructs the state exactly); partitioning by symbol and hot symbols; the market-data fan-out and fairness (all
subscribers see the same sequence).

**Mid-way change (≈ 0:28):** "If a matching engine dies, its standby must take over within 2 s with zero acknowledged
orders lost." → a hot standby replays the same sequenced log in lockstep, failover gated by the sequence number,
fencing of the old primary, and in-flight unacked orders resubmitted idempotently by brokers (client order IDs).

**Meets looks like:**
- The log-before-ack plus deterministic replay mental model, stated clearly.
- Single writer per symbol; argues against locks in the matching path.
- Risk check placement (before sequencing) and its latency cost.
- Market-data fan-out quantified.
- Audit: every event is sequenced and immutable, and the regulator replay is the same as the recovery replay.

---

## P16 — Secure short links for bank SMS (variant of C1) 💳

**Nearest:** C1, C7, C9 · **Probe sections:** C1 (ID generation, security, expiry curveball), C7 (SMS, abuse), C9
(audit, idempotency) · **Category:** consistency-critical / security

**Why this is unseen relative to C1:** the dominant NFR changes (read throughput → security and one-time
semantics), the data model changes (a permanent code → URL mapping becomes a short-lived token bound to a member
and a purpose), and the top deep dive changes (cache/ID generation → one-time use under link-preview bots, and
phishing resistance). All three differ.

> "Our credit unions send members SMS messages with links — 'your statement is ready', 'approve this payment'. We
> want short links on our own domain. Design that."

**Answer sheet:**
- ≈ 5 M links created per day. The monthly statement run creates 2 M of them between 06:00 and 07:00.
- Statement links expire after 72 h. Payment-approval links are **one-time**: they expire after 15 min or on first
  use.
- Human clicks: ≈ 30% of links within 24 h.
- Many messaging apps and phones fetch links automatically to render previews: ≈ 60% of links get an automated GET
  within seconds of delivery (TODO-VERIFY: the behaviour varies by platform). A GET doesn't mean a human clicked.
- A link must reveal nothing without authentication: no balances in the URL or on the landing page before login.
- Every creation and click is audited for 7 years. Phishers will copy our SMS style.

**Reference estimates:**
- Statement burst ≈ 2 M / 3,600 s ≈ 560 creates/s; otherwise ≈ 3 M / 82,800 s ≈ 36/s.
- Human clicks ≈ 1.5 M/day ≈ 17/s average.
- Live tokens ≈ 5 M/day × 3 days ≈ 15 M × ≈ 300 B ≈ 4.5 GB: tiny.
- Audit ≈ (5 M creates + ≈ 4.5 M fetches) × 300 B ≈ 2.9 GB/day ≈ 1 TB/year.
- **So what:** scale is trivial. A candidate who spends 10 minutes on cache tiers and sharding has missed the
  dominant requirement.

**Strong deep dives:** token design (≥ 128 bits of randomness, or a signed token with purpose, member and expiry;
stored server-side for revocation; never sequential); one-time semantics vs preview bots (the GET renders a landing
page and the state change requires an explicit POST after authentication, so a prefetch can't consume or approve
anything; atomic consume-on-POST); anti-phishing (one consistent bank-owned domain, never ask for credentials via
link-landing flows, member education, monitoring for lookalike domains).

**Mid-way change (≈ 0:28):** "The SMS vendor bills per message segment, and links longer than 20 characters push many
messages into two segments." → shorter tokens trade entropy for cost: keep ≈ 10–12 base62 chars (≈ 60–71 bits) plus
server-side rate-limiting of misses and short expiry, and argue whether the reduced entropy is acceptable.

**Meets looks like:**
- Names the preview-bot problem (or reasons to it when asked what a GET implies) and fixes it with GET-safe pages
  and POST-to-act.
- Token entropy and enumeration resistance quantified; misses rate-limited.
- Expiry and one-time consumption done atomically (conditional update), audited.
- Says scale isn't the challenge, and keeps the storage design simple.
- SLIs: click-to-landing latency, expired-link hit rate (UX issue), miss rate per IP (enumeration attempts).

---

## P17 — Multi-region chat with data residency (variant of C6)

**Nearest:** C6, C4 · **Probe sections:** C6 (all), C4 (replication, partitions, multi-AZ) · **Category:**
real-time · ★★★

**Why this is unseen relative to C6:** the dominant NFR changes (single-region delivery latency → residency plus
survival of a region outage), the data model changes (a conversation gets a home region, and its sequence ownership
must move on failover), and the top deep dive changes (connection scale → cross-region ordering and failover).

> "We have a chat app with users in the US, Europe and India. Design it so it stays fast for everyone and keeps
> working if a whole region goes down."

**Answer sheet:**
- 200 M MAU, 100 M DAU, 30 M concurrent at peak; ≈ 40 messages per DAU per day.
- 20% of conversations have participants in more than one region.
- Same-region delivery within 300 ms at p99; cross-region within 1 s.
- EU users' message content must be stored in the EU (residency); metadata may be replicated elsewhere.
- If one region is lost, users there reconnect elsewhere within ≈ 1 min, and conversations continue; at most ≈ 5 s
  of acknowledged messages may be lost in a regional disaster (RPO ≈ 5 s).

**Reference estimates:**
- ≈ 100 M × 40 ≈ 4 B messages/day ≈ 46k/s average, ≈ 140k/s peak (3×).
- Storage ≈ 4 B × 200 B ≈ 800 GB/day ≈ 290 TB/year, ≈ 0.9 PB with 3 replicas.
- Cross-region traffic ≈ 20% × 46k/s × 200 B ≈ 1.8 MB/s: bandwidth is trivial; the problem is latency (≈ 80–150 ms
  US↔EU round trip) and ownership.
- Gateways ≈ 30 M / 50k–100k ≈ 300–600 servers, split across regions.

**Strong deep dives:** conversation home region (the sequencer for a conversation lives in one region, usually the
creator's or the majority's; other regions forward writes, so cross-region messages pay one RTT, which fits the 1 s
budget); failover of a home region (async replication with ≈ 5 s RPO, promote a replica, handle sequence gaps and
reordering; fencing so two regions don't both sequence the same conversation); residency (where content is stored
vs where it's cached in transit, and what an EU–India conversation does).

**Mid-way change (≈ 0:28):** "India now requires India–India messages to never leave India, not even replicas." →
the failover target for India-only conversations must be another India region (or accept a lower availability for
them), conversations are classified by participants, and a conversation becomes restricted when membership changes.

**Meets looks like:**
- A single-writer per conversation with a home region, plus the trade-off against multi-writer conflict resolution.
- RPO/RTO tied to the replication mode, honestly stated.
- Residency handled explicitly in the data placement, not hand-waved.
- Reconnect storm on regional failover addressed (backoff + jitter, capacity headroom in the surviving regions).
- SLIs: cross-region delivery p99, replication lag per region pair.

---

## P18 — Regulated document vault (variant of C10) 💳

**Nearest:** C10, C9 · **Probe sections:** C10 (metadata vs blobs, cost), C9 (audit, tamper evidence), plus
security probes · **Category:** consistency-critical / security

**Why this is unseen relative to C10:** the dominant NFR changes (sync latency and dedupe → retention, legal hold and
tenant key control), the data model changes (per-tenant key hierarchy and retention policy per document class
instead of chunk graphs for delta sync), and the top deep dive changes (chunking/conflicts → deletion semantics
under retention plus legal hold, and envelope encryption).

> "We store documents for 400 credit unions — statements, loan files, signed disclosures. Design the document vault."

**Answer sheet:**
- 20 M members across 400 tenants; 300 M documents today, ≈ 60 M new per year; average ≈ 500 KB.
- On the 1st of each month, ≈ 20 M statements are generated within 6 hours.
- ≈ 2 M document views/day, 10× on statement days.
- Retention: 7 years minimum for most classes (TODO-VERIFY: actual US record-retention rules differ by document
  type; treat 7 years as the given requirement), after which documents must be deleted.
- **Legal hold:** a tenant can freeze specific members' documents indefinitely; held documents must never be
  deleted.
- Each tenant has its own encryption key and can revoke our access to it (the "kill switch" in their contract).
- Every access is audited; documents are immutable once stored (no edits, only new versions).

**Reference estimates:**
- Existing ≈ 300 M × 500 KB ≈ 150 TB; growth ≈ 60 M × 500 KB ≈ 30 TB/year.
- Statement burst ≈ 20 M / 21,600 s ≈ 926 writes/s for 6 h, against ≈ 1.9/s average (60 M / 3.15 × 10^7 s).
- Views ≈ 23/s average, ≈ 230/s on statement days.
- Storage cost at ≈ $0.023/GB-month ≈ 150,000 GB × 0.023 ≈ $3.5k/month for the current set in a standard tier
  (TODO-VERIFY prices); older documents move to colder tiers.

**Strong deep dives:** retention and legal-hold policy (policy evaluated per document class and per hold; deletion by
a sweeper that re-checks holds atomically; WORM/object-lock style immutability, TODO-VERIFY provider features);
envelope encryption with per-tenant KEKs in a KMS/HSM and per-document DEKs (revocation makes a tenant's documents
unreadable; key rotation re-wraps DEKs without re-encrypting the blobs); tamper-evident audit (append-only,
hash-chained access log).

**Mid-way change (≈ 0:28):** "A credit union is leaving the platform: export everything within 30 days, then prove
destruction — except documents under legal hold." → a bulk export job (manifest + checksums), crypto-shredding by
destroying the tenant's KEK for non-held data versus the conflict with held documents (held documents need their own
key scope, so plan key granularity accordingly), and a destruction certificate from the audit trail.

**Meets looks like:**
- Separates metadata (indexed, transactional) from blobs (object storage), justified by the numbers.
- The deletion pipeline respects holds atomically, with the race (hold placed during deletion) addressed.
- Key hierarchy explained, and what revocation and rotation cost.
- Statement burst handled with queueing/batching and idempotent writes.
- Cost tiering with a ≈ figure and the lever.

---

## P19 — Typeahead warm-up: contact search

**Nearest:** C12 · **Probe sections:** C12 · **Category:** read-heavy · ★ (a warm-up after a gap or before the first
gating mock. It counts as unseen relative to C12, because the data model (small per-user lists) and the top deep
dive (client-side search) both differ. Being ★, it may be at most one of the four gating mocks.)

> "In our messaging app, as you type a name in the 'new chat' box, show matching contacts. Design it."

**Answer sheet:**
- 100 M users, each with ≈ 300 contacts (some up to 5,000). 20 M DAU, ≈ 5 searches per DAU per day, ≈ 6 keystrokes
  each.
- Results within 100 ms of a keystroke at p95, ranked by recent interaction.
- Contacts change rarely; a new contact should be findable within a minute.

**Reference estimates:**
- Requests ≈ 20 M × 5 × 6 ≈ 600 M/day ≈ 6.9k/s average.
- Per-user index ≈ 300 contacts × ≈ 100 B ≈ 30 KB, so the whole contact list can go to the client (100 M × 30 KB ≈
  3 TB server-side in total).
- **So what:** the strongest answer moves search to the client (download the contact list, filter locally) and keeps
  a server path only for large lists and the "people you may know" directory.

**Strong deep dives:** client-side vs server-side search; ranking signals; sync of contact changes.

**Mid-way change (≈ 0:25):** "Also search the company directory: 200k employees for enterprise tenants." → a
per-tenant server-side prefix index (C12 style) merged with local results.

**Meets looks like:**
- Notices the per-user data is small and argues for client-side search.
- Still quantifies, and handles the outliers (5,000-contact users).
- Clear sync/freshness story.
