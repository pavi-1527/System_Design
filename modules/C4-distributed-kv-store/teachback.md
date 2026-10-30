# C4 Teach-back

## Prompt

A mid-level engineer on your team proposes running the platform's new session and idempotency-key store as "a
few Redis-style nodes behind consistent hashing, each key on three of them, read and write two out of three". They
say: *"That's what Dynamo does. Two out of three means we're always consistent and always available, right?"*

Explain, in your own words, **how a Dynamo-style key-value store stays writable when machines fail, what it gives
up to do so, and which of the platform's data it is wrong for**. Write 400–800 words (or talk it through with the
tutor) for someone who has used a key-value store but never operated a replicated one. You may include one diagram.
Use at least two concrete numbers from your own estimates, and at least one example from the banking platform
(idempotency keys, sessions, device bindings, erasure requests).

Your explanation should cover: how a key finds its replicas without a master; what `N`, `R` and `W` buy and what
`R + W > N` does *not* guarantee; what happens when a replica is down, and when it is down for a long time; what
the store does with concurrent writes to one key; how replicas that drifted apart converge and why deletes need
special care; how nodes know who is alive; and the operation the design cannot do.

## Evaluator checklist

Must-hit (all required for pass):
- [ ] Explains **placement without a master**: keys hashed onto a ring with virtual nodes, every node (and smart
      clients) holding the ring, a preference list of `N` distinct nodes in distinct failure domains, any node able
      to coordinate.
- [ ] States the **quorum trade** correctly: writes tolerate `N − W` failed replicas and reads `N − R`;
      `R + W > N` gives overlap with the latest *completed* write on the home replicas, **not** linearizability (in-flight
      or timed-out writes, sloppy quorums, repair races).
- [ ] Explains **sloppy quorums and hinted handoff** as the mechanism behind "always writable", with the price
      (stale reads during handoff) and the bound (a hint window, after which repair is needed).
- [ ] Gives a **conflict policy chosen by data type**: last-write-wins for sessions (and why it is safe there),
      version vectors with siblings or a CRDT for mergeable data, and why LWW loses writes under concurrency or
      clock skew.
- [ ] Explains **anti-entropy repair** (Merkle trees comparing ranges and shipping only the differences) and why
      read repair alone is not enough (cold keys).
- [ ] Explains **tombstones and zombies**: a delete is a write; tombstones must outlive the longest outage plus the
      repair interval; a node down longer than the grace period is rebuilt, not rejoined.
- [ ] Names the **operation the design cannot do** (create-if-absent / compare-and-set with exactly one winner,
      e.g. an idempotency-key claim) and the alternative (consensus per key or per range, with its latency and
      partition cost).

Should-hit (at least 4 of 7):
- [ ] Estimates with the **replica-operation multiplier** (`reads × R + writes × N`) and notes that operations, not
      bytes, often size the cluster.
- [ ] Distinguishes **`Unavailable` from `Timeout`** for writes, and what a client must do after a timeout.
- [ ] Explains **gossip membership and local failure detection** (heartbeats, generations, phi accrual or a suspect
      stage) and why conviction reroutes but does not move data.
- [ ] Describes **rebuild time** (data per node ÷ streaming rate) as the window of reduced redundancy, with a number.
- [ ] Mentions **hot keys** (a key's replicas are `N` machines whatever the cluster size) and a fix.
- [ ] Discusses **multi-DC** levels (`LOCAL_QUORUM` vs `EACH_QUORUM`) and the RPO each implies.
- [ ] A link to the banking context: sessions vs idempotency claims vs erasure deadlines needing different paths.

Red flags (any one = revise):
- [ ] Says `R + W > N` makes the store strongly consistent or linearizable.
- [ ] Says the store is "always consistent and always available" without naming what is given up during a
      partition.
- [ ] Proposes quorums, LWW or sibling resolution for idempotency-key claims or other uniqueness constraints.
- [ ] Describes delete as removing the record, or never mentions tombstones.
- [ ] Treats the failure detector's "dead" as the trigger for moving data.
- [ ] States an invented throughput, default or pricing figure as fact.

## Recording

`sd record teachback C4 --result <pass|revise> --notes "<missed items>"`
