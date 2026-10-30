# C3 Teach-back

## Prompt

A mid-level engineer on your team is starting a new ledger service. In the design review they say: *"For ids I'll
just call `uuid.New()`. Or, if the DBA wants `BIGINT`, I'll copy a Snowflake function from GitHub and set the
machine id from an environment variable. It's a solved problem."*

Explain, in your own words, **why unique ids in a distributed system are a design decision, and what can go wrong
with each of their two proposals**. Write 400–800 words (or talk it through with the tutor) for someone who has
built services but has never had to think about clocks or process identity. You may include one diagram. Use at
least two concrete numbers from your own estimates (a bit budget, a rate, a skew bound), and at least one example
from the banking platform (a vendor reference field, an audit export, a retried callback).

Your explanation should cover: what the requirements questions are and how their answers pick a scheme; how a
time + node + sequence id is laid out and what the bit budget decides; what happens on sequence overflow, on a
backwards clock step and on a restart; how node ids are assigned and what stops a paused process from minting
duplicates; and what ordering the ids actually promise, and what they don't.

## Evaluator checklist

Must-hit (all required for pass):
- [ ] Says that the **requirements pick the scheme**: size (64 bits vs 128), ordering strength, rates, number of
      producers and regions, and whether minting may depend on a remote call; gives the UUID answer its due (no
      coordination, but 128 bits, and random v4 keys scatter B-tree inserts) and names UUIDv7 as the fix for order.
- [ ] Lays out a **time + node + sequence id with a bit budget**: time in the high bits (why), the top bit kept 0,
      lifetime from tick and epoch (e.g. 2^41 ms ≈ 69.7 years; the Unix epoch ran out in 2039), node bits from
      processes plus churn, sequence bits from the hottest producer.
- [ ] Explains the **sequence and the lock**: one mutex per generator, and on overflow the generator waits for (or
      borrows within a bound) the next tick, never wraps.
- [ ] Explains the **clock hazard**: a backwards step makes a naive generator re-mint ids it already issued; the fix
      is to wait out small regressions and refuse large ones (returning an error, not a guessed id), with clock-offset
      monitoring.
- [ ] Explains **node-id assignment by lease**, and the paused holder: the holder stops at a deadline computed from
      when its last renewal was *sent*, the registry's token rejects stale renewals, and the next holder starts above
      a floor (the previous holder's high-water mark); says why an environment variable or IP-derived id fails.
- [ ] States the **ordering promise correctly**: k-sorted across nodes (only ids further apart than skew + one tick
      are in time order), strictly increasing within one process; not a total order, and not the order in which
      things happened.

Should-hit (at least 4 of 7):
- [ ] Ids travel as **strings** in JSON (doubles are exact only to 2^53).
- [ ] An in-process **library**, not a per-id service call, with a batch service for clients that can't embed it.
- [ ] Never shard consumers by **`id % N`**: the low bits are the sequence, usually 0.
- [ ] The lease **TTL trade-off**: long enough to ride out a registry outage, short enough that crash restarts per TTL
      don't exhaust the node-id space.
- [ ] Restarts need the **floor** (or another out-of-process memory of the last tick), because the in-memory `last`
      is gone and the monotonic clock starts afresh.
- [ ] An id is **not an idempotency key**: a retry gets a new id; the caller's key must be created once, before the
      first attempt.
- [ ] A link to the banking context: the 16-digit vendor reference and its smaller bit budget, the examiners' order
      coming from a per-account sequence rather than ids, or a retried callback with a fresh reference.

Red flags (any one = revise):
- [ ] Claims ids from different machines are strictly ordered by creation time, or that NTP guarantees no backwards
      steps.
- [ ] Proposes wrapping the sequence, or a generator with no lock, or "use the previous millisecond" on a clock
      regression without a bound.
- [ ] Assigns node ids from config, hostnames or IP bits without naming the duplicate risk.
- [ ] Treats an expired lease as safe because "the registry will give the node id to someone else", without the
      holder-side deadline.
- [ ] Puts a central sequence or a remote call per id on every write path across datacentres or regions without
      discussing latency and availability.
- [ ] States an invented throughput or vendor detail as fact.

## Recording

`sd record teachback C3 --result <pass|revise> --notes "<missed items>"`
