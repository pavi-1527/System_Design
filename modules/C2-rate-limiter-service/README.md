# C2 — Case study: distributed rate limiter

| | |
|---|---|
| Track | C — HLD case studies |
| Estimated time | 6 h (1 weekly unit) — CORE 5 h, STRETCH 1 h |
| Prerequisites | A3, A7 (gates passed) |
| Labs | `labs/c2/` — `distlimit` (GCRA over a compare-and-swap store, a fail-policy guard with a timeout and local fallback, batched token leases with a deny cache) |
| Gate | quiz ≥ 85% ×2 (different days) · labs pass · design ≥ meets · teach-back pass |

## Why this module exists

A rate limiter looks like the smallest system in the case-study list: one counter per customer and an `if`. A7 taught
you the algorithms and why a per-node share of a limit falls apart in a fleet. The interview question starts where A7
stops. Forty gateways must agree, within a millisecond, on how many requests a customer has made, on every one of
400,000 requests a second, which means the limiter makes more calls than the API serves. Done as `GET` → compute →
`SET`, it admits 16× the limit with four gateways. Its biggest customer is a single key that no amount of sharding can
spread, and a retry storm on that key saturates one shard and drags 11,000 unrelated customers into degraded mode.
When its store hangs, a limiter with no timeout takes the whole API down. A one-character rule change locks 3,000
customers out in ten seconds.

None of that is about token buckets. It's about shared state on the hot path: one atomic step per decision, the cost
and accuracy of the hottest key, and what the component that protects everything does when it is the thing that
fails. This module runs the 45-minute framework on the problem end to end, has you commit to each decision before
the reveal, and makes you build the distributed half that A7's lab deliberately left out: the store-side decision, the
lease client and the failure guard.

## Outcomes

- **Scope** a rate-limiter prompt in ≤ 6 minutes: what each limit is for (protective, contractual, security), what
  is limited and how many checks per request, rate vs quota, the fail mode per limit, global vs per region, and the
  client contract. — CORE
- **Estimate** shared decisions per second, store shards from ops/s, state memory per algorithm, the hot key's
  share of one shard and per-gateway rate, and the round-trip latency against the budget, and **state** which
  decision each number drives. — CORE
- **Design** the decision call (all descriptors in one call, a cost, remaining/retry/reset, the violated rule, its
  version and the decision source), the client contract (429 vs 503, `Retry-After` rounded up, `RateLimit-*`
  headers), the versioned rules API with shadow mode and expiring overrides, and the data model (rules apart from
  counters; one TAT per key with TTL = time to full; co-located account limits). — CORE
- **Draw** the HLD with both the data path and the rule-distribution path, rates on the arrows, a justified placement
  (gateway filter vs library vs sidecar vs decision service), and **say** what each box does when it fails. — CORE
- **Choose** the algorithm and the atomic primitive for shared state (GCRA in a script or with compare-and-swap;
  fixed windows keyed by period for quotas; a log only for small exact limits), and **explain** what `GET`/`SET`,
  `INCR`-then-`EXPIRE` and client clocks each break. — CORE
- **Implement** GCRA over a compare-and-swap store with bounded retries and refunds, a guard with a timeout and
  per-rule fail modes including a scaled local fallback, and a lease client with a deny cache, lease expiry and
  refunds on drain, and **show** with break-its what a non-atomic store, clock skew, the batch size and each fail mode
  do (`labs/c2/distlimit`). — CORE
- **Handle** the hot key: lease sizing from per-gateway rate, lease TTL and burst; the `N × batch` overshoot bound
  and the synchronized burst that reaches it; stranded tokens; the deny cache; local pre-filters, routing by key and
  key splitting as alternatives. — CORE
- **Design** the limiter's failure behaviour and operations: timeouts and breakers, fail open/closed/local per rule,
  asynchronous failover as soft-state loss, rule rollout as a deploy (validate, shadow, canary, versioned rollback),
  the signals to watch, and what changes at 10× and 100×. — CORE
- **Compare** multi-region options (home region, static split, rebalanced shares, replicated counters) with their
  latency and overshoot, and **estimate** the limiter's monthly cost including cross-AZ traffic. — STRETCH

## Unit plan

| Unit | Topic | Hours | Core/Stretch | Lab |
|---|---|---|---|---|
| 1 | [Guided design: requirements → estimation → API and model → HLD → three deep dives](unit-1-rate-limiter-service.md) | 6 | CORE (5, incl. ≈ 1.5 h gate work) / STRETCH (1) | `labs/c2/distlimit` |

**Unit 1 (3 × 2 h)**
- Session 1 (2 h): warm-up; §1 requirements (write the card before the reveal), §2 estimation (the decision rate,
  shards, memory, the hot key, round trips), §3 decision API and data model, §4 the HLD on paper with both paths
  traced.
- Session 2 (2 h): §5 the atomic decision and §6 the hot key: predict, then `labs/c2/distlimit` parts 1, 2 and 4
  (store → limiter → leases), then the non-atomic-store, clock-skew and lease-size break-its.
- Session 3 (2 h): §7 failure and rollout with the lab's part 3 (`Guard`) and the outage break-it, Common mistakes
  (≈ 50 min in all), then the gate work: first `sd quiz C2`, the [design exercise](design-exercise.md) (45 min,
  solo), the [teach-back](teachback.md).
- STRETCH (1 h, any time): the end of §7 (multi-region limits and the cost model).
- Second `sd quiz C2` attempt on a later day.

## How this connects to your banking work

- **Vendor contracts are global limits.** The core-banking processor allows ≈ 300 req/s *across all of your adapter
  instances*, and the credit bureau bills per pull. A per-instance `limit / N` breaks the day you autoscale; a shared
  limiter with leases is how eight adapters in two regions stay under one contract. The
  [design exercise](design-exercise.md) is that service.
- **Noisy neighbours among credit unions.** One credit union's payroll partner running 50,000 transfers at 09:00 must
  not starve another's members at the branch. Per-tenant contractual limits under a global protective limit, with the
  payroll key leased, are the mechanism (A9 Unit 1 §5).
- **Fail open vs fail closed is a decision you've already made.** When the fraud-scoring vendor times out, you
  approve, decline or fall back to local rules by transaction type. A limiter's fail mode is the same choice, per
  rule: plan limits fall back locally, login and OTP limits fail closed.
- **Lost updates on shared state.** "Read the balance, then write the debit" without a conditional update is the
  same bug as `GET` → compute → `SET` on a rate-limit counter, with the same fix: one atomic conditional write and a
  retry on conflict.

## Mastery gate checklist

- [ ] `sd quiz C2` ≥ 85% — attempt 1 (date: ____)
- [ ] `sd quiz C2` ≥ 85% — attempt 2 on a different day (date: ____)
- [ ] `sd lab C2` — all lab tests pass (with `-race`)
- [ ] Design exercise ([`design-exercise.md`](design-exercise.md)): self-score recorded, tutor score ≥ "meets"
      (`sd record design C2 --self <lvl> --tutor <lvl> --notes "..."`)
- [ ] Teach-back ([`teachback.md`](teachback.md)): tutor marks pass (`sd record teachback C2 --result pass --notes "..."`)

`sd status` is the source of truth; this list is for your own reading.

## What's next

C2 is a prerequisite of the HLD mock kit (D3), and E1 builds a limiter into a real service (its third milestone).
The next case studies in the recommended order are C5 (news feed) and C6 (chat), which reuse this module's hot-key
reasoning for celebrity accounts and its fail-mode reasoning for delivery; C7 (notification service) reuses its
outbound vendor limits almost verbatim. Before moving on, run `sd review` and clear any C2 questions it surfaces,
especially the tags `rate-limiting`, `concurrency`, `hot-key` and `availability`.
