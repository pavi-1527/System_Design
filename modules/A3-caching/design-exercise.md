# A3 Design exercise — Caching strategy for a credit-union mobile app

> Time box: 45 minutes, solo, no notes. Then self-score against the rubric, then send to the tutor.

## Prompt

You run the platform behind the mobile and web apps of 40 credit unions. The app's busiest screens are the **home
screen** (account summary: names, balances, last few transactions), **transaction history**, the **product and rate
catalogue** (savings rates, loan and mortgage rates, promotions) and everything that depends on the member's **session
and permissions**. Reads dominate, the core banking systems behind the platform are slow and rate-limited, and the
database is under pressure at the payday peak. The team has been told to "add Redis".

Design the caching strategy. Say what you would cache, where, with which pattern, for how long, how it is
invalidated, how it survives the cache being cold or down, and how you would stop one credit union's data ever being
shown to another.

## What to produce

On paper or in a doc, in about 45 minutes:

1. **Requirements and staleness budget**: the clarifying questions you asked and the assumptions you made, then a table
   of data classes (at least account summary/balance, transaction history, product/rate catalogue, session/entitlement
   data, and one more of your choosing) with the maximum staleness you will accept for each and *why*.
2. **Estimate sheet**: request rates (average and peak), working-set size, memory (with per-entry overhead and
   headroom), target hit ratio, the resulting database and core-banking load, and the **cold-cache load** with your
   mitigation. Mark approximations with `≈`.
3. **Placement diagram**: client, CDN, gateway, in-process L1, remote L2, database; which data lives where, and the
   total worst-case staleness a member can see on each path.
4. **Decision table** per data class: placement, pattern (aside / through / behind / around / refresh-ahead), TTL and
   jitter, invalidation mechanism (including how you avoid the stale-set race), stampede and hot-key handling, negative
   caching, tenant isolation.
5. **Failure walk-through**: the cache cluster restarts at the payday peak; a node fails over to a lagging replica; the
   core banking system is down for five minutes. What does the member see, what does the database see, and what do you
   do?
6. **Metrics and alerts**: five signals you would put on a dashboard, including at least one *correctness* signal, with
   what each would have told you in the scenarios above.
7. **Risks and open questions**: what you would validate first (a trace replay for the hit-ratio curve? a load test that
   flushes the cache at peak?).

<details data-kind="hint">
<summary>Interviewer answers to likely clarifying questions (open only after you have written your own questions)</summary>

- **Scale**: 40 credit unions, ≈ 2.1 M members, ≈ 3.5 M accounts; ≈ 25% daily actives; home-screen peak ≈ 6,000
  requests/s (payday morning), ≈ 2,000/s on an ordinary day. Transaction history ≈ 1,500/s at peak. Rate catalogue
  is read on most screens (≈ 8,000/s at peak across the fleet) but has only ≈ 5 tenants' tables that change in a given
  month.
- **Sources**: Postgres primary, comfortable at ≈ 20k QPS; the core banking system is reached through per-tenant
  adapters and is contractually limited to ≈ 300 calls/s per tenant, with an overnight batch window after which
  balances are "official". Intraday balance changes from the platform's own transfers are applied to the core
  immediately.
- **Staleness**: the displayed balance may be up to ≈ 10 s old, but a member who has just made a transfer must see its
  effect immediately. Transfers and debits are authorised against the core/ledger and never against any cache. Posted
  transactions never change; pending ones do. Rate tables carry an effective date; the product team wants new rates
  to appear within a minute of the effective moment. Entitlement revocations (lost device, closed account) must take
  effect within ≈ 60 s.
- **Sessions**: OIDC; access tokens live 15 minutes, refresh tokens longer; tokens are validated locally and
  entitlements are fetched per request.
- **Infrastructure**: one region with a warm DR region; Redis available as a managed cluster with replicas (asynchronous
  replication); an API gateway and a CDN are available for public content.
- **Compliance**: balances and names are personal data; right-to-erasure requests exist; every read of member data must
  be auditable; a cross-tenant exposure is a reportable incident.
- **Constraints**: team of 6; no appetite for a second data store beyond Redis; budget is moderate.

</details>

## Rubric

| Dimension | below | approaching | meets (senior bar) | exceeds |
|---|---|---|---|---|
| **1. Requirements and staleness budget** | No staleness stated; "everything is eventually consistent" or "cache everything" | Staleness for some data; reasons weak; no read-your-writes | Per-class staleness derived from business/safety rules; asymmetric costs noticed; read-your-writes for the member's own writes; sums TTLs across layers | Also shows `asOf` in the API/UI, separates display from authorisation reads, and ties budgets to a named product/compliance owner |
| **2. Estimation and sizing** | No numbers, or numbers inconsistent with the prompt | Peak QPS only; memory without overhead; hit ratio assumed | Peak QPS, working set, bytes per entry with overhead and headroom, hit-ratio target from skew, resulting DB/core load, marked with `≈` | Uses the miss-ratio framing and fan-out (page-level miss probability), marginal cost of memory, and a plan to validate with a trace replay |
| **3. Placement and layering** | One Redis for everything, or caches on personal data at shared layers | Several layers, but no reasoning on staleness or invalidation per layer | Right data in the right layer (public rates at CDN/gateway, hot keys in L1, personal data in L2 only); worst-case staleness per path | Explains what each layer does when the layer above it is cold, and removes a layer where it does not pay |
| **4. Pattern and invalidation correctness** | Update cache on write or delete-then-write without noticing the race | Delete after write, TTL backstop; race not identified | Names the stale-set race and closes it (versions/leases/short TTL) or justifies the residual risk; reliable invalidation path (outbox/CDC) and TTL backstop; replica-fill hazard noted | Designs tombstones/generation keys for bulk invalidation, effective-dated rates, and the invalidation consumer's failure handling |
| **5. Stampede and cold start** | No mention, or "Redis is fast" | Mentions singleflight or jitter but cold-cache load not computed | Computes the cold factor against DB and core limits; coalescing, jitter, stale-while-revalidate, warming/staggering and load shedding chosen with reasons | Sizes DB/core for an agreed cold share, specifies restart procedures, and tests (flush at peak) as part of the plan |
| **6. Negative caching and hot keys** | Not considered | Names them without numbers or mechanisms | Short negative TTL invalidated on create, key validation, Bloom filter where justified; hot-key detection and an L1/CDN/split plan with numbers (`instances / L1_TTL`) | Bounds negative-entry memory against pollution, and routes public hot data to the CDN with correct headers |
| **7. Consistency and authorisation safety** | Caches authorisation decisions or balances for transfers | Notices the risk but no mechanism | Debits authorised against the source; caches authorisation inputs with short TTL plus revocation push; fails closed for high-risk actions; monotonic reads considered | Quantifies revocation lag per layer and designs step-up re-checks and audit of cached reads |
| **8. Tenant isolation and data protection** | Keys without tenant; no discussion | Tenant in key by convention only | One key-builder, tenant first, injective encoding, tenant verified on read with an alarm; no PII in keys; quotas for noisy neighbours; erasure considered | Per-tenant credentials/ACLs or instances where risk justifies, and a runbook for a suspected leak |
| **9. Failure behaviour and operations** | Cache is assumed always up | Fail-open without protection; no metrics | Behaviour for restart, failover to a lagging replica (treated as partial flush) and core outage; short cache timeouts with a breaker; metrics include hit ratio by prefix, eviction age, p99, key cardinality **and** a correctness counter | Game-day plan, flush-by-family tool with audit, and alerts tied to specific failure modes |
| **10. Communication and trade-offs** | Disorganised; no trade-offs | Trade-offs stated for some decisions | Driven by the candidate, structured, states what flips each decision, and says what they would validate first | Anticipates the interviewer's probes, gives a phased rollout, and quantifies cost and risk |

## Tutor critique protocol

The tutor reads the artifact first, then runs the conversation. Do not reveal the rubric rows until the learner has
self-scored.

1. **Check the numbers.** Recompute the peak QPS, the working-set memory and the cold-cache load from the learner's own
   assumptions. Any arithmetic that does not reproduce is a finding.
2. **Check the staleness table against the safety rule.** Is any authorisation or debit path reading a cache? Is any
   asymmetric flag cached in the unsafe direction?
3. **Walk the race.** Ask the learner to draw the interleaving that leaves a stale balance in the cache for a full TTL
   under their own design, and to show where their mitigation breaks it.
4. **Ask the follow-up probes** (at least six; adapt to what the learner wrote):
   1. "The cache cluster restarts at 08:55 on payday. Walk me through the first five minutes: what does the database
      see, what does the core see, what does the member see?"
   2. "Your rate table expires every 60 s and is read 8,000 times a second. How many loads happen at expiry, and what
      happens if you run 40 instances?"
   3. "A member transfers $500 and reloads. Show me the exact sequence that could display the old balance. How does your
      design prevent it, and what residual risk remains?"
   4. "Your entitlement cache has a 60 s TTL and a revocation event. The event is lost. What does the exposure look like and
      how do you notice?"
   5. "A bot probes 5,000 nonexistent account numbers per second. What happens in your design, and what does it cost you?"
   6. "Two credit unions both have account 100234. Prove to me their entries can never collide, including through
      separators in tenant names. What alerts if your proof is wrong?"
   7. "Redis fails over to a replica that is two seconds behind. What can the member see that is wrong, and how do you
      bound it?"
   8. "The core banking system is down for five minutes. Which screens keep working, on what data, and what does the UI say?"
   9. "Finance asks you to halve the cache memory. What do you measure first, and what is your decision rule?"
5. **Assign levels** per dimension, then the overall level using the rule below. Record the learner's one-line
   summary of the weakest dimension in the notes.

**Overall-level rule.**

- **meets**: no dimension at *below*, at most two at *approaching*, and dimensions **1, 4, 5 and 7** (staleness budget,
  invalidation correctness, cold start, consistency/authorisation safety) are each at least *meets*.
- **exceeds**: *meets* overall, with at least four dimensions at *exceeds* including two of 1, 4, 5 and 7.
- **approaching**: no more than two dimensions at *below* and none of 1, 4, 5, 7 at *below*.
- **below**: anything else, or any answer that caches authorisation or debit decisions without an authoritative check.

## Recording

`sd record design A3 --self <below|approaching|meets|exceeds> --tutor <level> --notes "<one-line summary>"`
