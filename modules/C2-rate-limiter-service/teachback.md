# C2 Teach-back

## Prompt

A mid-level engineer on your team has been asked to add per-customer rate limits to the partner API. They say:
*"Easy. I'll put `golang.org/x/time/rate` in each gateway with the customer's limit divided by the number of
gateways. Or, if you insist on shared state, a Redis `GET`, check, `SET`. Why do we need a design review for a
counter?"*

Explain, in your own words, **why a distributed rate limiter is a real system and where its three hard parts
are**. Write 400–800 words (or talk it through with the tutor) for someone who has written a token bucket but has
never run shared state on every request of a busy API. You may include one diagram. Use at least two concrete
numbers from your own estimates, and at least one example from the banking platform (partner plans, the payroll
batch, vendor contracts, login limits).

Your explanation should cover: why a per-gateway share fails for a contractual limit and when it's still the right
tool; what the numbers say about the store (decisions per second, not memory); how one atomic step per decision
works and what `GET`/`SET` and client clocks break; what the hottest key does to the store and how leases and a deny
cache bound it, at what cost in accuracy; and what the limiter does when its own store is slow or down, and when a
rule is wrong.

## Evaluator checklist

Must-hit (all required for pass):
- [ ] Separates **protective** limits (per node, no shared state, fail open) from **contractual** limits (shared
      state, accuracy, a fallback), and explains why `limit / N` per gateway breaks for the second (sticky routing,
      uneven balancing, autoscaling changing N) with a number.
- [ ] States the **shape from the numbers**: decisions per second = requests × shared checks (e.g. 400k × 2 =
      800k/s), so the store is sized by **ops/s**, not memory (tens of MB), and the limiter is on every request's
      critical path.
- [ ] Explains **one atomic step per decision**: GCRA's single value updated by a server-side script or a
      compare-and-swap with reload-and-retry, that denials don't write, and what `GET`/`SET` does (lost updates; the
      lab's 16× over the limit).
- [ ] Explains the **hot key**: one key is one shard whatever the shard count; leases (a batch per decision) cut its
      store ops ≈ batch×; the overshoot bound `N × batch`; and a deny cache for keys over their limit.
- [ ] Explains the **failure policy**: a hard timeout (≈ 5–10 ms) and breaker on every limiter call, and a fail
      mode per limit (open for protective, closed for security, local fallback at ≈ 2× fair share for contractual),
      with the reason the limiter must never become the outage.
- [ ] Explains the **client contract**: `429` with `Retry-After` rounded up (never 0) and remaining-quota headers,
      versus `503` for global overload, and why bad headers turn denials into retry storms.

Should-hit (at least 4 of 7):
- [ ] TTL = time until the bucket is full, so idle keys vanish without a sweeper, and a past TAT is clamped to now.
- [ ] Clock skew between gateways: totals stay right but fairness collapses; use the store's clock.
- [ ] The `INCR`-then-`EXPIRE` trap, and putting the window id in a quota counter's key.
- [ ] Lease only hot keys (batch ≪ per-gateway rate × lease TTL; `N × batch` ≪ burst), lease expiry, refunds on
      drain, and the synchronized burst that reaches the bound.
- [ ] Rule changes as deploys: validation against live traffic, shadow mode, canary, versioned rollback.
- [ ] Failover of the counter store as soft-state loss (one fresh burst per key), and why replication stays
      asynchronous.
- [ ] A link to the banking context: the payroll partner as the hot key, the vendor's global contract, login limits
      that fail closed, or per-credit-union fairness.

Red flags (any one = revise):
- [ ] Proposes `GET` → compute → `SET` (or any check-then-act) on shared state, or `INCR` and `EXPIRE` as separate
      steps, without naming the atomic primitive.
- [ ] Sizes the limiter's store by memory.
- [ ] Says a single key can be spread by adding shards.
- [ ] Has no timeout on the limiter call, or one fail mode for every limit with no reason.
- [ ] Recommends leasing (batching) every key, or states leases are exact over any window.
- [ ] States an invented throughput, latency or pricing figure as fact.

## Recording

`sd record teachback C2 --result <pass|revise> --notes "<missed items>"`
