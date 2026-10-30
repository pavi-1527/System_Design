# C12 Design exercise — Marketplace typeahead across twelve countries

> Time box: 45 minutes, solo, no notes, timed like a real interview (numbers by 0:10, end-to-end diagram by 0:25,
> stop drilling at 0:40). Then self-score against the rubric (≈ 10 min), then send the write-up to the tutor for the
> probe session. Use the structure from [`docs/INTERVIEW_FRAMEWORK.md`](../../docs/INTERVIEW_FRAMEWORK.md) and the
> numbers from [`docs/NUMBERS.md`](../../docs/NUMBERS.md) — from memory.

This is a *variant* of the module's case study on purpose. The mechanisms (keystroke-rate estimation, a precomputed
top-K index in RAM, a cacheable `GET`, decayed counts, immutable snapshots, an override set) all apply, but three
constraints change the answer: the corpus no longer fits on one node, a flash sale needs minutes-level freshness,
and some of the people generating your signal are trying to game it. If you reproduce Unit 1's design box for box
you will score "approaching": the rubric rewards *transferring* the decisions, with numbers, to this shape.

## Prompt

> "We run a marketplace in 12 countries, in 9 languages. Design the search-box typeahead in our mobile apps and on
> the web: as the shopper types, suggest completions of what other shoppers searched for. Marketing runs flash
> sales where a brand-new product name has to show up in suggestions almost immediately. Legal sometimes orders a
> term removed in one country, fast. And we've caught sellers running bots that search for their own brand names to
> get them suggested."

That is all the interviewer says up front. Ask the clarifying questions you need (write them down with the answer
you'd assume if told "you decide"), and drive.

Things an interviewer would answer if asked (don't read until you've written your questions):

<details data-kind="hint">
<summary>Interviewer's answers to common clarifying questions</summary>

- Shoppers: ≈ 60 M daily active across 12 countries; ≈ 300 M submitted searches a day. ≈ 80% of traffic is
  mobile. Assume ≈ 15 keystrokes per search. The largest country is ≈ 30% of traffic.
- Corpus: ≈ 150 M distinct queries kept after filtering, across 9 languages; the largest locale has ≈ 40 M, the
  smallest ≈ 1 M. Some languages (Japanese, Thai) don't separate words with spaces.
- Results: up to 8 suggestions; global per locale (no personalisation in v1, but don't make it impossible).
- Latency: perceived p99 ≤ 150 ms on mobile networks; server p99 ≤ 20 ms.
- Freshness: ranking changes within ≈ 1 h; a flash-sale product name typed by many shoppers must be suggested
  within ≈ 5 min of the sale starting.
- Flash sales: one country at a time, up to ≈ 10× that country's average traffic for ≈ 15 minutes, announced a day
  ahead.
- Takedowns: a legal removal in one country must be gone from that country's suggestions within ≈ 5 min, with an
  audit trail; other countries are unaffected.
- Manipulation: bot farms of a few thousand freshly created accounts searching the same branded phrase. Shoppers can
  search logged out.
- Availability: 99.9% for suggest; search must never depend on it.
- Out of scope unless you choose otherwise: spelling correction, ads in suggestions, the search engine itself.

</details>

## What to produce

1. **Timestamp log**: the clock time at the start of each phase (evidence for time management).
2. **Requirements card**: functional (3–5), non-functional *with numbers* (scale, latency, freshness per tier,
   takedown SLO per country, availability, fail-open behaviour), explicit out-of-scope, and your clarifying
   questions with the assumptions you proceeded with.
3. **Estimate sheet**: at most 6 numbers with arithmetic and units, each with the decision it drives: lookups/s
   (average, peak, after client mitigation, and during a flash sale in the largest country), index memory for all
   locales and for the largest one (nodes × bytes, with swap headroom), serving nodes, egress, and the trending
   tier's state.
4. **API sketch**: the suggest endpoint (method, parameters including locale, response, cache headers, behaviour
   for empty and over-long prefixes and for errors), the client's debounce, cache and sequencing, and the
   takedown/override admin API.
5. **Data model**: raw events, aggregated counts (with what makes them abuse-resistant), the serving snapshot per
   locale, and the override set per country; keys, stores, partitioned or replicated.
6. **HLD diagram**: the read path (client → edge → routing by locale → replicas) and the write path (submitted
   searches → log → batch and streaming aggregation → build → validate → snapshot → rollout), with rates on at least
   three arrows and a one-line justification per box.
7. **Deep dives (2–3)**, each with alternatives, a cost and a flip condition:
   - the index and its partitioning (layout and bytes per node; why the whole corpus doesn't fit; locale routing;
     what you do for the largest locale and for the twenty smallest);
   - freshness and publishing (main tier vs a trending tier, half-lives, sketches vs exact counts, the merge, the
     5-minute path for a flash-sale name, snapshot rollout per locale);
   - safety and manipulation (what counts as support, how a bot farm is discounted, how a per-country takedown
     reaches every tier and the edge within 5 minutes).
8. **Failure walkthrough**: the Japanese build comes out with half its prefixes; the CDN is purged at the start of
   a flash sale; the streaming aggregation restarts and loses its state 5 minutes into a sale.

## Rubric

| Dimension | below | approaching | meets (senior bar) | exceeds |
|---|---|---|---|---|
| Requirements & scope | Draws immediately; no freshness or takedown numbers; manipulation ignored | Lists features; NFRs partly numeric; locales or fail-open missing | Freshness per tier (≈ 1 h main, ≈ 5 min trending), takedown SLO per country, latency split perceived vs server, fail open, manipulation named as a requirement; explicit out-of-scope; assumptions written | Identifies "corpus size per node", "5-minute trends" and "adversarial signal" as the three requirements that move this design away from the default, and negotiates scope accordingly |
| Estimation | Missing, or counts searches not keystrokes (≥ 10× off) | Averages only; index sized as strings; no flash-sale number | Keystroke multiplier and peak applied (≈ 52k/s avg, ≈ 156k/s peak client-sent, ≈ ½ after debounce and cache); flash-sale load for the largest country; index ≈ 750 M nodes ≈ 45–75 GB compact for all locales vs ≈ 12–20 GB for the largest; 2× swap headroom; each number tied to a decision | Shows that traffic and corpus scale independently (replicas vs partitions), sizes per-locale replica pools from each locale's traffic share, and estimates the flash sale's effect on the CDN hit rate (new long-tail prefixes) |
| API & client | `POST`, per-user data in the cache key, or no locale in the key | `GET` with a cache header, but no echo, no empty-prefix rule or no client behaviour | `GET /v1/suggest?q=&locale=&limit=` with locale in the cache key, normalised `q` echoed, empty list for empty `q`, capped `limit`, `Cache-Control: public` with `stale-*`; client debounce, session prefix cache, sequence numbers, short timeout and fail open; edge rate limits | Per-locale normalisation (case, width, accents; segmentation for scripts without spaces) applied identically on client, edge key and build; IME composition handled on the client |
| High-level design | A database on the read path, or a path that can't be traced | Both paths present but boxes unjustified, no validation, or replicas written in place | Read path: client → CDN → gateway routing by locale → per-locale replica pool with the index in RAM; write path: log → batch aggregation + streaming counter → builder (support, blocklist, decay, top-K) → validation → versioned snapshot → canaried rollout; every box justified; one keystroke walked through with latencies | Small locales packed onto shared replicas with a routing table; regional serving for mobile latency; the write path's region-outage behaviour (freshness pauses, serving continues) |
| Index & partitioning | Trie with query-time top-K, or strings in every node; "shard by hash" with no reason | Precomputed top-K named, but bytes per node or the fit question skipped | Trie (or prefix map) with precomputed top-K ids and an id → text table, bytes per node stated; the whole corpus doesn't fit with headroom, so partition by locale (requests carry it), each locale replicated whole, the largest still fits; hot short prefixes served from the edge | Hybrid skip for small subtrees and a pointer-free, memory-mappable layout; a plan for when the largest locale stops fitting (prefix-range split with a routing table, hot ranges replicated more) |
| Freshness & publishing | Counts never decay or reset; updates pushed into live indexes | Decay named without a knob; no trending tier, or a trending tier without its 5-minute path traced | Main tier hourly with a half-life of days; trending tier per locale from a streaming counter (sketch + heavy hitters, half-life ≈ minutes), rebuilt every minute and merged at lookup (`main + β × recent`); immutable snapshots, validation, atomic swap, canary and waves per locale; the flash-sale name's path traced end to end within ≈ 5 min | Uses the day-ahead announcement (pre-seed the product name into the trending tier or pre-warm the flash locale's pool and CDN); streaming state checkpointed so a restart doesn't lose the sale |
| Safety & manipulation | Raw counts as support; no takedown path | Blocklist and takedowns mentioned; bots handled by "rate limiting" only | Support on distinct users with a cardinality sketch per candidate, weighted against new or low-trust accounts and logged-out sessions; per-account and per-IP limits at the edge; the per-country override set applied at lookup over both tiers, with an audit trail and the CDN's TTL inside the 5-minute SLO | Anomaly signals on candidates (share of new accounts, burstiness, one ASN), a review queue before a suspicious query can surface, and measuring the false-positive cost on real flash-sale spikes |
| Failure modes & ops | None, or "add replicas" | Generic metrics | Snapshot age and version per locale, empty-result rate by prefix length, validation failures, trending-tier lag; origin sized for the pre-CDN load; health check means "locale snapshots loaded"; runbooks for rollback and emergency override | Cost drivers named (RAM per locale pool, egress, the streaming tier's engineering); flash-sale game day; migration from a single-locale design without downtime |
| Communication & time | Checkpoints missed by > 8 min; no trade-off table | Finishes but a phase overruns; trade-offs lack flip conditions | Numbers by ≈ 0:10, diagram by ≈ 0:25, stops at 0:40; drives; a trade-off table with ≥ 3 rows and flip conditions | Anticipates probes; notes what was deliberately cut and why |

**Overall "meets"** requires *meets* or better on **Index & partitioning** and **Freshness & publishing** (the two
places where this variant departs from the guided design), and no dimension at *below*. **Overall "exceeds"**
requires *meets* everywhere and *exceeds* on at least three dimensions. Otherwise overall is *approaching* (or
*below* with two or more dimensions at *below*).

## Tutor critique protocol

1. **Check the artefacts before probing (≈ 3 min).** Timestamp log present; slips at 0:10 / 0:25 / 0:40 computed.
   Every NFR has a number. The estimate sheet's arithmetic: 300 M searches/day ≈ 3.5k/s; × 15 keystrokes ≈ 52k
   lookups/s average; × 3 ≈ 156k/s peak client-sent, ≈ 78k/s after a debounce and session cache that halve it; a
   flash sale in the largest country: ≈ 30% × 52k ≈ 15.6k/s average there, × 10 ≈ 156k/s client-sent from that one
   country (≈ 78k/s after mitigation), on top of the rest of the world. Index: 150 M queries × ≈ 5 nodes ≈ 750 M
   nodes × 60–100 B ≈ 45–75 GB compact, ≈ 90–150 GB with swap headroom, so the whole corpus per node is out; the
   largest locale: 40 M × 5 ≈ 200 M nodes ≈ 12–20 GB, ≈ 24–40 GB with headroom, fits a 64 GB node. Egress:
   300 B × 156k/s ≈ 47 MB/s ≈ 0.37 Gbps peak. Trending state: a count-min sketch at ε = 10⁻⁴, δ = 10⁻³ ≈ 0.8 MB per
   locale, plus a heavy-hitters list of a few thousand candidates each. Raw logs ≈ 300 M × 100 B ≈ 30 GB/day. An
   error ≥ 10× on lookups or index memory (searches instead of keystrokes, strings instead of nodes) caps
   *Estimation* at "approaching".
2. **Self-score first.** Ask for a level per dimension with a one-line justification before saying anything.
3. **Probe (≈ 10 min).** Ask at least six, including the ones marked (required); one at a time; push one rung
   further on vague answers.
   1. **(required)** "Walk me through the bytes. Does every replica hold every country's index? What decides it?"
      *Meets:* nodes × bytes per node with the assumption stated, the 2× swap headroom, the conclusion that ≈ 90–150
      GB doesn't fit comfortably while the largest locale does, so partition by locale (free, since requests carry
      it) and replicate each locale whole; what happens to the twenty-odd small locale/country combinations
      (packed together on a shared pool).
   2. **(required)** "The sale for 'AcmePhone X9' starts at 12:00. At 12:03 a shopper in that country types
      `acme`. Show me every hop that decides whether they see it."
      *Meets:* submitted searches → log → streaming counter for that locale → heavy-hitters candidate → support and
      safety checks → recent snapshot rebuilt within ≈ 1 min → swapped on that locale's replicas → merged at lookup
      with `β` high enough to outrank evergreen `acme…` queries for the sale's duration; the CDN's TTL for `acme…`
      prefixes counted against the 5 minutes (short TTL for the trending-affected tier, or accept ≈ 1–2 min of
      staleness at the edge).
   3. **(required)** "Five thousand accounts created yesterday all search `brandx cheap` this morning. What stops
      it from being suggested, and what does it cost you on a real flash sale?"
      *Meets:* support on distinct users weighted by trust (account age, purchase history, logged-in vs not), per-
      account and per-IP limits at the edge, heavy-hitters candidates checked for an abnormal share of new accounts
      before publication; names the false-positive risk (a genuine sale spike also brings new accounts) and how
      the day-ahead announcement lets marketing whitelist the sale's terms.
   4. **(required)** "A German court orders `brandx replica watch` removed in Germany within five minutes. Walk me
      through it, including the trending tier and the CDN."
      *Meets:* an override entry scoped to the country/locale, published on the fast path and polled by replicas
      every ≈ 1 min, applied at lookup over the merged main + trending result; the CDN entries for affected
      prefixes purged (or the edge TTL kept short enough to fit inside the SLO); the next main build also excludes
      it; an audit record of who, what, when, and when it propagated; other countries unaffected.
   5. "The Japanese build comes out with half its usual prefixes. What happens?"
      *Meets:* validation against the manifest and that locale's golden prefixes fails the publish; Japanese stays
      on the previous snapshot; other locales' rollouts are independent; alert on validation failure and on
      snapshot age for that locale.
   6. "The CDN is purged by mistake at 12:00, just as the sale starts. What does your origin see?"
      *Meets:* the pre-CDN load (≈ 78k/s from the sale country after client mitigation, plus everyone else's); the
      origin, or at least that locale's pool, was sized for it, ideally pre-scaled a day ahead; edge rate limits
      and `stale-if-error` cover the rest.
   7. "What is a 'prefix' in Japanese, and where does normalisation happen?"
      *Meets:* one normalisation function per locale (width, case, accents; segmentation or character-level
      prefixes for scripts without spaces), applied identically in the client, the edge cache key and the build;
      the client sends composed text, not intermediate input-method states.
   8. "The streaming aggregation crashes five minutes into the sale and restarts empty. What breaks?"
      *Exceeds:* checkpointed state (or replay from the log, whose retention covers it); the trending tier keeps
      serving its last snapshot; lag metric on the trending tier; the sale's terms pre-seeded so a restart doesn't
      erase them.
   9. "Roughly what does this cost per month, and which line dominates?" (credit toward *exceeds* only).
4. **Score each dimension with evidence** (quote the write-up or the probe answer). Say what would have moved
   each dimension up one level.
5. **Calibration feedback**: name any dimension where the self-score was off by more than one level.
6. **Decide** with the overall rule above. Below "meets": give 2–3 concrete revision tasks and redo on a different
   day with a variation (suggested: "design symbol typeahead for an internal code-search tool: ≈ 10k engineers,
   ≈ 50k repositories, suggestions filtered by the repositories the engineer can read, new symbols visible within
   ≈ 2 minutes of a merge").

## Recording

`sd record design C12 --self <below|approaching|meets|exceeds> --tutor <level> --notes "<one-line summary>"`

Example: `sd record design C12 --self meets --tutor approaching --notes "locale partitioning solid; flash-sale path ignored CDN TTL; bot farm handled by rate limits only"`
