# A3 Teach-back

## Prompt

A mid-level engineer on your team says: *"Our account-summary endpoint is slow and the database is struggling. Let's
just add Redis. Cache the response for five minutes, and we're done."*

Explain, in your own words, **why "just add Redis" fails and how to cache safely.** Write 400–800 words (or talk it
through with the tutor) for someone who has used Redis but has never been paged because of a cache. You may include one
diagram. Use at least one concrete number from your own estimates, and at least one example from a banking platform.

Your explanation should cover: what the cache buys and what it quietly costs; how to decide what to cache and for how
long; how a cached value goes stale and how to bound that; what happens when many requests miss at once; what happens
when the cache is empty or down; and what must never be cached.

## Evaluator checklist

Must-hit (all required for pass):
- [ ] States what a cache buys in numbers: backend load drops to `Q · (1 − h)` and latency to a weighted average, and
      names the **cold factor** `1 / (1 − h)`: a cache that sheds 97% of load is a 33× multiplier when it is empty, so
      the source must survive (or be protected for) the cold case.
- [ ] Explains the **staleness problem concretely**: a cached value is a claim about the past; TTL bounds it; and gives the
      cache-aside **stale-set race** (reader reads old value, writer commits and deletes, reader then sets the old value)
      with at least one mitigation (short TTL, versioned/CAS write, lease, event-driven invalidation).
- [ ] Explains **stampedes**: many simultaneous misses on an expired or cold key; names coalescing (singleflight) and at
      least one more of jitter, stale-while-revalidate, early refresh, warming.
- [ ] Gives the **rule for what not to cache and why**: debit/authorisation decisions and flags where stale means unsafe,
      and personal data in shared layers; chooses staleness per data class from the cost of being wrong.
- [ ] Explains **tenant isolation** (key includes the tenant, built in one place) and why hit-ratio dashboards will not
      show a leak.
- [ ] Describes at least three **metrics** to watch (hit ratio by key prefix, eviction rate, p99/loader latency, key
      cardinality, a correctness counter) and what a drop in each would mean.

Should-hit (at least 4 of 7):
- [ ] The hit-ratio curve is concave (diminishing returns), with an example of sizing from skew and working set.
- [ ] Explains why 95% → 99% matters through miss ratio (5× less backend load) and fan-out (`h^k`).
- [ ] Contrasts cache-aside, write-through and write-behind, and says why write-behind to a volatile cache can lose
      acknowledged writes.
- [ ] Mentions eviction (LRU, and the scan problem) and why a batch job should not share the interactive cache.
- [ ] Negative caching / cache penetration, with the rule "never cache an error as not found".
- [ ] Hot keys and the local L1 (`instances / L1_TTL`), with the staleness cost.
- [ ] Failure modes of the cache cluster: failover to a lagging replica, timeouts turning into outages, schema changes.

Red flags (any one = revise):
- [ ] Says the cache "makes the system faster" without discussing misses, staleness or the cold case.
- [ ] Recommends updating the cache on write without discussing ordering, or claims "delete after write" is race-free.
- [ ] Recommends caching authorisation decisions or the balance used to approve transfers.
- [ ] Treats TTL as the only invalidation strategy for data that must be fresh after the member's own action.
- [ ] Proposes a longer TTL as the fix for stampedes or hot keys.
- [ ] Treats the cache as a durable store (write-behind without a log, sessions with no loss story).
- [ ] Never mentions tenant isolation in a multi-tenant system.

## Recording

`sd record teachback A3 --result <pass|revise> --notes "<missed items>"`
