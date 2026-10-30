# C12 — Case study: search autocomplete

| | |
|---|---|
| Track | C — HLD case studies |
| Estimated time | 6 h (2 units) — CORE 5 h, STRETCH 1 h |
| Prerequisites | A3, A4 (gates passed) |
| Labs | `labs/c12/` — `suggest` (prefix index with precomputed top-K, atomic snapshot swap, decaying counter) |
| Gate | quiz ≥ 85% ×2 (different days) · labs pass · design ≥ meets · teach-back pass |

## Why this module exists

A shopping app adds typeahead to its search box in an afternoon: on every keystroke, `SELECT query FROM searches
WHERE query LIKE 'sh%' ORDER BY count DESC LIMIT 5`. In the beta it is instant. At launch, ≈ 20 M searches a day
turn into ≈ 300 M lookups a day, because nobody multiplied by the ≈ 15 keystrokes in each search. The prefix `s`
matches ≈ 4 M rows and each request sorts them; the database that also serves search results is at 100% CPU within
the hour, and search slows down with it. The responses take a second, longer than the gap between keystrokes, so
the user who has typed `shoes` sees completions of `sh` that arrived late.

The fixes come in a familiar order, and each one breaks something new. An in-memory trie takes the database off the
read path, but computing the top 5 at query time means the hottest prefix (`s`, requested by nearly every search)
is the slowest one. Precomputing the top 5 per node fixes that, and the first hourly rebuild loads the new index
next to the old one on every replica at the same minute and the kernel kills them all. Counts that never decay keep
last season's sale at the top forever; counts reset at midnight make 00:05 suggestions pure noise. And one Tuesday a
journalist notices that a private person's name completes to an accusation that exactly one user ever typed.

Autocomplete is the smallest system in Track C, which is why interviewers like it: one endpoint, one data
structure, and nowhere to hide. The whole 45 minutes is numbers at keystroke rate, a data structure you have to
justify byte by byte, a pipeline that decides what millions of people see, and the operations of replacing an
in-memory index on a running fleet. This module walks it once with the decisions made in front of you, then makes
you make them alone.

## Outcomes

- **Scope** an autocomplete prompt in six minutes: ask the questions that change the design (volume, what is
  completed, personalisation, freshness, latency, K and prefix length, locales, what must never be suggested, what
  happens when suggest is down), state NFRs as numbers and an explicit out-of-scope list. — CORE
- **Estimate** keystroke-rate lookups before and after client mitigation, index memory from distinct prefixes and
  bytes per node, serving nodes, egress, log volume and snapshot distribution, and tie each number to a decision,
  above all *replicate the whole index vs partition it*. — CORE
- **Design** a cacheable suggest API (`GET`, normalised `q` echoed, empty list for an empty prefix,
  `Cache-Control` with `stale-*`, capped `limit`, edge rate limits), the client's debounce, prefix cache and
  sequencing, and the three data sets (raw events, aggregated counts, serving snapshot) with their keys and stores.
  — CORE
- **Draw** the read path (client → CDN → load balancer → replicas holding the index in RAM) and the write path
  (submitted searches → log → aggregation → build → validate → versioned snapshot → canaried rollout), justify every
  box, and walk one keystroke through the latency budget. — CORE
- **Choose** an index layout (trie with precomputed top-K ids, hash map of prefixes, sorted array + scan, FST) with
  lookup cost and bytes per node, **explain** why top-K at query time is backwards for a hot path, and how a
  pointer-free layout avoids garbage-collector pauses. — CORE
- **Design** freshness: exponential decay with a half-life instead of resets or hard windows, exact batch counts for
  the main tier, a count-min sketch plus heavy hitters for a per-minute trending tier, merged at lookup. — CORE
- **Publish** a new index with no lock on the read path (immutable versioned snapshots, validation, atomic pointer
  swap, canary and waves within the 2× memory headroom, rollback), and **handle** hot prefixes and partitioning when
  the index stops fitting. — CORE
- **Keep suggestions safe**: minimum support on *distinct users*, a whole-word blocklist at build time, and a fast
  override set for takedowns applied at lookup over every tier, with an audit trail and a propagation SLO. — CORE
- **Implement** in Go, passing under `-race`, a prefix index with a precomputed top-K per node, a serving snapshot
  swapped through an atomic pointer, and a lazily decaying counter that feeds the next build (`labs/c12/suggest`).
  — CORE
- **Extend** the design without touching the global path: personalisation by client-side merge or a parallel
  endpoint, bounded fuzzy prefix matching, and multi-region serving from replicated snapshots. — STRETCH

## Unit plan

| Unit | Topic | Hours | Core/Stretch | Lab |
|---|---|---|---|---|
| 1 | [Guided design: requirements, estimation, API and data model, high-level design](unit-1-guided-design.md) | 3 | CORE (2.5) / STRETCH (0.5) | — (conceptual; the estimate sheet and both paths are the work) |
| 2 | [Deep dives: the prefix index, freshness and snapshot publishing, personalisation and fuzziness, common mistakes](unit-2-deep-dives.md) | 3 | CORE (2.5, incl. ≈ 1 h gate work) / STRETCH (0.5) | `labs/c12/suggest` |

**Unit 1 (3 h)**
- Session 1 (1.5 h): warm-up; §1 requirements (write your own question list, with what each answer changes,
  before the reveal); §2 the estimate sheet by hand before reading the worked version, then a few
  `sd estimate -kind qps` and `-kind cache` drills.
- Session 2 (1 h): §3 the API and the three data sets (commit to method, cache headers and stores before the
  reveal; run the scratch handler); §4 both paths end to end, with the publish sequence and the four break-it faults
  on paper.
- STRETCH (0.5 h): the multi-region part of §4, and redo §1's break-it (personalised, 60-second trends, infix) as a
  timed five-minute drill.

**Unit 2 (3 h)**
- Session 1 (1.5 h): warm-up; §5 index layouts (fill in the five-layout table before the reveal); `labs/c12/suggest`
  part 1 (`Build`, `Suggest`); §6 decay, sketches and publishing; lab parts 2 and 3 (`Service`, `Counter`); the
  top-K-at-query-time and racy-swap break-its. Read "Common mistakes"; first `sd quiz C12` attempt.
- Session 2 (1 h, gate): the [design exercise](design-exercise.md) (45 min, solo, timed) and the
  [teach-back](teachback.md) write-up. Second `sd quiz C12` attempt on a later day.
- STRETCH (0.5 h): §7 personalisation and spelling correction; the decay-off break-it and the bytes-per-node
  measurement from §5.

## How this connects to your banking work

- **Payee and merchant typeahead is this design at a smaller scale.** A bill-pay screen that completes payee names,
  or a transaction search that completes merchant names, is a per-tenant prefix index. At one credit union's volume a
  `LIKE 'prefix%'` on an indexed column may be fine (the flip condition in §1's probe); across 40 tenants at
  keystroke rate it is the Unit 1 failure story, and the corpus must be partitioned by tenant exactly as this module
  partitions by locale ([A9 tenancy](../A9-security-multitenancy/unit-1-authorization-tenancy.md#4-tenant-isolation-models)).
- **Snapshots are the reference-data files you already deploy.** Routing-number directories, fee schedules and
  vendor code tables arrive as versioned files, get validated, and are swapped in whole, with the previous version
  kept for rollback. The serving index in §4 and §6 is the same discipline, at hourly cadence: validate before
  publish, canary, atomic swap, never edit in place.
- **Minimum support and takedowns are PII controls.** "Never surface a string only one person typed" is the same
  rule as not showing one member's memo text in another member's search. The override set with an audit trail of
  who removed what, and how fast it propagated, is what a compliance review will ask you for.
- **Fail open is a vendor-adapter decision.** A suggest outage must leave the search box usable, the way a slow
  credit-bureau or address-validation vendor must not block an application form: short client timeout, empty
  result, no shared dependency with the critical path.
- **Decayed counts beat calendar resets.** Ranking merchants by "this month" resets on the 1st and looks wrong for
  a week; a half-life gives the same "recent matters more" without the cliff, which is §6's argument.

## Mastery gate checklist

- [ ] `sd quiz C12` ≥ 85% — attempt 1 (date: ____)
- [ ] `sd quiz C12` ≥ 85% — attempt 2 on a different day (date: ____)
- [ ] `sd lab C12` — all lab tests pass (with `-race`)
- [ ] Design exercise ([`design-exercise.md`](design-exercise.md)): self-score recorded, tutor score ≥ "meets"
      (`sd record design C12 --self <lvl> --tutor <lvl> --notes "..."`)
- [ ] Teach-back ([`teachback.md`](teachback.md)): tutor marks pass (`sd record teachback C12 --result pass --notes "..."`)

`sd status` is the source of truth; this list is for your own reading.

## What's next

C12 is one of the core case studies that D4, the full senior-loop simulation, requires, and "design typeahead" (or
a variant: contact search in a mail client, product search in a marketplace, a command palette) is a common
unseen problem in the D3 and D4 HLD rounds. The concepts it builds on are worth one more pass if any quiz tag
below is weak: hit-rate math and stampedes from [A3](../A3-caching/README.md)
([Unit 2 §6](../A3-caching/unit-2-correctness.md#6-stampedes-thundering-herds-dogpiles-and-synchronized-expiry)),
range vs hash partitioning and hot keys from [A4](../A4-replication-partitioning/README.md)
([Unit 2 §5](../A4-replication-partitioning/unit-2-partitioning.md#5-why-partition-and-the-partitioning-schemes),
[Unit 3 §8](../A4-replication-partitioning/unit-3-hot-keys-indexes-sizing.md#8-skew-hot-keys-and-celebrity-problems)),
the log behind the pipeline from [A6 Unit 1 §2](../A6-async/unit-1-queues-logs.md#2-queues-versus-logs-and-partitions),
and CDN cache keys from [A1 Unit 1 §6](../A1-networking-api/unit-1-request-path.md#6-cdns). Two STRETCH case studies
reuse pieces directly: C14 (proximity search, where geohash prefixes are a prefix index) and C15 (metrics, where
windowed aggregation and cardinality are the core problems). Before moving on, run `sd review` and clear any C12 questions
it surfaces, especially tags `estimation`, `trie`, `freshness` and `caching`.
