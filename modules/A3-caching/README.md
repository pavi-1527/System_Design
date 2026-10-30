# A3 — Caching

| | |
|---|---|
| Track | A — HLD fundamentals |
| Estimated time | 12 h (2 units) — CORE 10 h, STRETCH 2 h |
| Prerequisites | A1, A2 (gates passed) |
| Labs | `labs/a3/` — `lru`, `cacheaside` (core); `lfu` (stretch) |
| Gate | quiz ≥ 85% ×2 (different days) · labs pass · design ≥ meets · teach-back pass |

## Why this module exists

A credit union's account-summary endpoint takes 180 ms and hits the core banking system four times per call. Someone puts
Redis in front of it. The hit ratio settles at 97%, latency drops to 12 ms, the database load falls 33-fold, and the
database is downsized. Four months later a patch restarts the cache nodes on payday morning and the empty cache sends
33 times the normal load to a database that can take a fraction of it: forty minutes of outage, with retries piling on
as the cache fails to warm. The cache was never the problem; it had quietly become a load-bearing wall.

That is the first act. The second is quieter. A transfer posts at 12:00:00.000 and the member's home screen still shows
the old balance for a minute, in about one case in five hundred, because a slow reader wrote the *old* value into the
cache after the writer had already deleted it. A rate table that is read 8,000 times a second expires, and 9,600
requests rebuild it at once. A bot enumerates dead account numbers that no cache can ever hold. A key like
`summary:{accountNumber}` quietly serves one credit union's member another's balance, because both credit unions number
accounts from 100000, and no dashboard shows anything wrong. Each of these is a cache doing exactly what it was told.
This module is about being told the right things.

## Outcomes

- **Compute** effective latency and backend load from a hit ratio, and explain why moving from a 95% to a 99% hit
  ratio is a 5x cut in backend load and a very different tail. — CORE
- **Place** caches along a request path (browser, CDN, gateway, in-process, remote, database buffer pool) and **choose**
  between cache-aside, read-through, write-through, write-back, write-around and refresh-ahead, stating the failure
  semantics of each. — CORE
- **Choose** an eviction policy (LRU, LFU, CLOCK, TinyLFU admission, TTL) for a workload and **explain** when scans, skew
  or one-hit wonders flip the answer. — CORE
- **Size** a cache from working set, key skew and per-entry overhead, and **decide** whether the extra memory is worth
  the hit ratio it buys. — CORE
- **Implement** an O(1) LRU with per-entry TTL on an injected clock, cost weighting, an eviction callback and concurrent
  safety (`labs/a3/lru`). — CORE
- **Reproduce** the cache-aside stale-set race step by step, and **design** invalidation (delete after write, versioned
  keys, leases, short TTLs, event-driven invalidation) that bounds staleness. — CORE
- **Prevent** stampedes with singleflight, TTL jitter, stale-while-revalidate, probabilistic early refresh and cache
  warming, and **show** why a cold cache must never be the only thing keeping the database alive. — CORE
- **Defend** a cache against nonexistent keys (negative caching, Bloom filter) and hot keys (local L1, key replication),
  and **know** where the full hot-key treatment lives (A4). — CORE
- **Decide** the acceptable staleness for each class of banking data, **keep** tenants' entries apart, and **refuse** to
  cache authorisation decisions blindly. — CORE
- **Implement** a cache-aside loader with your own singleflight, negative and error caching, jitter, safe invalidation
  and stale-while-revalidate (`labs/a3/cacheaside`). — CORE
- **Operate** a cache: pick the metrics that matter (hit ratio, eviction rate, p99, key cardinality), and plan for
  failover, memory pressure and loss of the whole cache. — CORE
- **Implement** frequency-aware admission with a count-min sketch and show scan resistance against plain LRU
  (`labs/a3/lfu`). — STRETCH

## Unit plan

| Unit | Topic | Hours | Core/Stretch | Lab |
|---|---|---|---|---|
| 1 | [Why, where and how much: what a cache buys, placement, patterns, eviction, sizing](unit-1-why-where-how-much.md) | 6 | CORE (5) / STRETCH (1) | `labs/a3/lru`, `labs/a3/lfu` (stretch) |
| 2 | [Correctness: invalidation, stampedes, negative caching, hot keys, consistency and tenancy](unit-2-correctness.md) | 6 | CORE (5, incl. ≈ 1.25 h gate work) / STRETCH (1) | `labs/a3/cacheaside` |

**Unit 1 (6 h)**
- Session 1 (2 h): warm-up; §1 what a cache buys and where caches sit (run the cold-cache simulation); §2 caching
  patterns (run the write-behind loss demo). Predict boxes P01–P02.
- Session 2 (2 h): §3 eviction; implement `labs/a3/lru` (list primitives → Get/Set → TTL → cost → callback);
  Break-it exercises.
- Session 3 (1 h core + 1 h stretch): §4 sizing (the hit-ratio-curve scratch test against your LRU), Check-yourself
  questions, `sd review`. STRETCH (1 h, any time): the TinyLFU part of §3 and `labs/a3/lfu`.

**Unit 2 (6 h)**
- Session 1 (1.5 h): §5 invalidation (draw the t0–t4 race by hand first) and §6 stampedes; implement `Group` in
  `labs/a3/cacheaside`.
- Session 2 (1.5 h): finish `cacheaside` (`Get`, `fill`, negative/error caching, `Invalidate`, stale-while-revalidate);
  run the `breakit` experiments.
- Session 3 (2 h, gate work): §7 negative caching and hot keys, §8 consistency/tenancy/operations (≈ 50 min); first
  `sd quiz A3`; design exercise (45 min, solo); teach-back draft.
- STRETCH (1 h, any time): the XFetch derivation and simulation in §6, the lease details in §5.
- Second `sd quiz A3` attempt on a later day; polish the teach-back.

## How this connects to your banking work

- **Core-banking adapters.** Your adapters call systems that are slow, rate-limited and sometimes batch-oriented.
  Unit 1 §1 and §4 are the arithmetic behind "cache the balance inquiry": effective latency, the cold factor
  `1/(1−h)` when the cache is empty and the core is rate-limited, and the cost-weighted hit ratio when a miss is an
  800 ms vendor call. Unit 2 §6's stampede protection is what keeps ten thousand requests from becoming ten thousand
  core calls.
- **Staleness is a product decision.** Unit 2 §8's table (available balance for authorisation: don't cache; displayed
  balance: seconds; statements: immutable; rates: minutes with effective dates) is the conversation you will have with
  product and compliance, framed with numbers.
- **Multi-tenant credit unions.** The `summary:{accountNumber}` leak is a bug shape you can introduce in an afternoon:
  tenant-scoped keys from one function, tenant verified on read, per-tenant quotas, and no PII in keys. A9 returns to
  tenancy in depth.
- **SSO, sessions and entitlements.** What you cache about a session or a role, and for how long, decides how fast a
  revoked member loses access. Cache inputs, evaluate decisions, push revocation (§8).
- **Idempotency and audit.** The idempotency store from A1 Unit 3 is a *store*, not a cache: losing an entry means a
  double charge. And a cache hit is still a read of member data: audit logs must record the access even when the
  database never saw it.

## Mastery gate checklist

- [ ] `sd quiz A3` ≥ 85% — attempt 1 (date: ____)
- [ ] `sd quiz A3` ≥ 85% — attempt 2 on a different day (date: ____)
- [ ] `sd lab A3` — all lab tests pass (with `-race`)
- [ ] Design exercise ([`design-exercise.md`](design-exercise.md)): self-score recorded, tutor score ≥ "meets"
      (`sd record design A3 --self <lvl> --tutor <lvl> --notes "..."`)
- [ ] Teach-back ([`teachback.md`](teachback.md)): tutor marks pass (`sd record teachback A3 --result pass --notes "..."`)

`sd status` is the source of truth; this list is for your own reading. The `lfu` lab is STRETCH: `sd lab A3` reports it
separately and it does not block the gate.

## What's next

Passing A3 unlocks the case studies that lean on caching: C1 (URL shortener: redirect cache), C2 (distributed rate
limiter), C5 (news feed: timeline cache and the celebrity problem) and C12 (search autocomplete), and it is a
prerequisite of the E1 build project (URL shortener service with cache invalidation). The natural next Track A module is
A4 (replication and partitioning), which supplies what this module only mentions: consistent hashing for sharding the
cache tier, replication lag (the reason a cache fill from a replica goes stale) and the storage-level hot-partition
treatment. Before moving on, run `sd review` and clear any A3 questions it surfaces, especially tags `caching`,
`invalidation`, `stampede` and `estimation`; they return in almost every later design.
