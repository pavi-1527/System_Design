# A4 Teach-back

## Prompt

A mid-level engineer on your team added a read replica last week to take load off the primary. Since then, support has seen members
transfer money, tap back to the balances screen and see the old balance. The engineer is sure the replica is "just a copy, it must be the
same". Next quarter the team will add a fifth database shard, and they have heard that "consistent hashing" means you don't have to reshuffle all the
data, but they cannot say why `hash(id) % shards` is a problem or what consistent hashing does differently.

Explain to them, in your own words, **why adding a read replica made the balance screen show stale data, how to fix it without sending every
read to the primary, and how consistent hashing avoids reshuffling everything when a shard is added**.

Form: written, 400–800 words, may include one diagram (Mermaid or a sketch). No copying from the unit text: if a sentence from the course
appears verbatim, the evaluator will ask you to rephrase it. Write for that engineer, not for an examiner: concrete beats complete.

## Evaluator checklist

Must-hit (all required for pass):
- [ ] Explains that the replica applies the primary's change log **asynchronously**, so it is always some amount **behind** (replication lag), and gives the
      timeline: write acknowledged on the primary → read hits a replica that has not applied it yet → old balance.
- [ ] Names the anomaly as **read-your-writes** (read-after-write) and explains that the replica is not "broken": lag is normal and spikes under load (for example payroll).
- [ ] Gives **at least two fixes** with their costs: return the new state in the write response; read from the primary for a window after a write or for data the user
      just changed; an **LSN/commit token** carried with the session so a read goes only to a replica that has applied it (otherwise wait briefly or use the primary).
      States the cost (extra primary load, token plumbing) and that the fix degrades toward "all reads to the primary" when lag is large.
- [ ] Explains why `hash(key) % N` is a problem: **N is in the formula**, so changing N changes the home of almost every key (≈ N/(N+1) of keys, e.g. ≈ 80% going from 4 to 5 shards), so
      a resize is a near-total data migration and, for a cache, a cold cache.
- [ ] Explains the consistent-hashing idea: nodes and keys are hashed onto the same circle; a key belongs to the first node clockwise; **adding a node only takes over the arc just
      before it**, so only ≈ 1/(N+1) of keys move, all of them **to the new node**.
- [ ] Explains **virtual nodes** (many points per physical node) and why they are needed: with one point per node the arcs are very unequal; many points even out load and spread the cost of
      adding/removing a node over all nodes.

Should-hit (at least 4 of 7):
- [ ] Mentions **monotonic reads** (the balance appearing then disappearing across refreshes) and a fix (a floor on the highest LSN seen, or sticky replica with its limits).
- [ ] Mentions that a replica does not add write capacity (every replica applies every write) and that sharding is what scales writes and size.
- [ ] Mentions monitoring: replica lag (seconds and bytes), the share of reads served by the primary, and removing lagging replicas from the read pool.
- [ ] Mentions semi-synchronous replication or `remote_apply`-style commits as an option and its cost (latency, availability), or the durability angle (failover can lose the lag's worth of writes).
- [ ] Gives a worked number (for example lag × write rate = writes at risk, or 8 → 9 shards moves ≈ 89% with mod vs ≈ 11% with a ring).
- [ ] Mentions replica placement: a key's replicas are the next distinct physical nodes clockwise, not adjacent virtual nodes, and ideally in different AZs.
- [ ] Mentions the fixed-partitions/hash-slots alternative (many logical partitions assigned to nodes by a table) as the other way to avoid reshuffling.

Red flags (any one = revise):
- [ ] Says the replica is "eventually consistent so the data is wrong" or that async replication is a bug, or that a replica is always up to date.
- [ ] Proposes "just cache it" or "just retry the read" as the fix for read-your-writes without addressing the lag.
- [ ] Proposes synchronous replication to every replica as the fix without naming its latency and availability cost.
- [ ] Says consistent hashing means data never moves, or that it moves "nothing" when a node is added, or that it moves keys between existing nodes.
- [ ] Confuses virtual nodes with replicas (copies of the data).
- [ ] Claims a ring with one point per node is balanced, or that sticky sessions alone fix read-your-writes.

## Recording

`sd record teachback A4 --result <pass|revise> --notes "<missed items>"`
