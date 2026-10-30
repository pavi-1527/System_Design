# D4 Component oral — first principles under cross-examination

Item 5 of the [course definition of done](../../SYLLABUS.md#course-level-definition-of-done) asks that for each major
building block you can explain **why it exists, how it works inside, how it fails, and when not to use it**. The
teach-backs in Track A covered these one at a time, with time to write. The oral asks them out loud, in 20 minutes,
at the end of a long day, with follow-ups.

## How the tutor runs it (20 minutes)

- Ask **4–5 building blocks** per loop, ≈ 4 minutes each. Rotate so that three loops cover all 13: suggested sets are
  below, and a block that got *revise* in the previous loop is asked first.
- For each block, ask the four questions in order, one at a time. A follow-up is allowed when an answer is vague
  ("how does the leader know it's still the leader?"); it isn't a hint. A **hint** is anything that supplies part of
  the answer ("think about what happens to in-flight writes").
- No whole-system diagrams. A small sketch of the component's internals is fine.
- Score each block immediately: **pass** = all four questions correct with at most one hint across the four;
  otherwise **revise**, with the missed must-hits noted. The oral passes only if every block asked passes (see
  [rubric.md](rubric.md)).

**Rotation (one per loop):**

| Loop | Blocks |
|---|---|
| 1 | load balancer · cache · log · consensus/coordination service · circuit breaker |
| 2 | DNS · relational database · object store · rate limiter |
| 3 | CDN · LSM/KV store · search index · queue |

Change the order if the HLD round of that loop already went deep on one of these; ask a different block instead
and move that one to the next loop.

## The 13 building blocks

Each block lists the four questions with the must-hits for a pass, and one follow-up to use when the answer is
correct but thin. Numbers are approximate (≈) and should agree with [docs/NUMBERS.md](../../docs/NUMBERS.md).

### 1. DNS

| Question | Must-hit |
|---|---|
| Why does it exist? | Map stable names to changing addresses; delegation so no single party holds the whole namespace; a cheap first layer of traffic steering |
| How does it work inside? | Stub → recursive resolver → root → TLD → authoritative; caching at each level by TTL; record types that matter (A/AAAA, CNAME, NS) |
| How does it fail? | Stale caches after a change (TTL, and resolvers that don't honour it); authoritative provider outage takes everything down; slow failover because clients cache |
| When not to use it? | Not for fast failover (seconds) or per-request load balancing; not for anything needing instant consistency of the mapping |

Follow-up: "You set the TTL to 60 s for failover. Why do some clients still hit the dead IP ten minutes later?"
(Resolvers and clients that cache beyond the TTL, connection reuse, OS and browser caches.)

### 2. Load balancer

| Question | Must-hit |
|---|---|
| Why? | Spread load across replicas, hide replica churn, remove unhealthy backends |
| Inside? | L4 (connections, by 5-tuple) vs L7 (requests, can route on path/header, terminates TLS); algorithms (round robin, least-outstanding, power of two choices, consistent hashing for affinity); active health checks and passive outlier ejection |
| Fails? | The LB itself as a single point (hence pairs/anycast); health checks that pass while requests fail; least-connections sending a burst to a just-recovered, cold backend; long-lived connections that never rebalance |
| Not? | Client-side balancing with service discovery inside a mesh can remove a hop; a single backend; stateful protocols where affinity matters more than spread (then consistent hashing, not round robin) |

Follow-up: "Why can round robin overload one backend when all backends are identical?" (Requests vary in cost;
round robin ignores in-flight work; least-outstanding or P2C fixes it.)

### 3. CDN

| Question | Must-hit |
|---|---|
| Why? | Serve bytes close to users: latency (fewer RTTs over long paths) and origin offload; often the biggest cost lever for egress |
| Inside? | Edge PoPs with caches; cache key; TTL and revalidation (`Cache-Control`, ETags); origin shield / tiered caching; purge |
| Fails? | Low hit rate (long tail, cache-key mistakes like varying on cookies); thundering herd on the origin after a purge; stale or private content cached publicly |
| Not? | Highly personalised or rapidly changing responses with no shared cacheable part; tiny audiences where the CDN costs more than it saves |

Follow-up: "You purge a popular object globally. What happens to the origin?" (Every PoP misses at once; shield
tier, request coalescing, staggered purge or soft purge with revalidation.)

### 4. Cache

| Question | Must-hit |
|---|---|
| Why? | Trade memory for latency and backend load on a skewed (hot-set) workload |
| Inside? | Placement (client, service, shared tier); cache-aside vs read/write-through vs write-back; eviction (LRU/LFU) and TTL; sizing from hit-rate math |
| Fails? | Stampede on expiry of a hot key; node loss shifts its share of misses to the database; stale data after a write (invalidation ordering); hot keys saturating one node |
| Not? | Uniform access with no hot set (hit rate too low to pay for itself); data that must be read-your-writes everywhere without a correct invalidation story; write-heavy data read once |

Follow-up: "Delete-then-write or write-then-delete for invalidation? What race does each have?"

### 5. Relational database

| Question | Must-hit |
|---|---|
| Why? | Transactions (atomic multi-row changes), constraints, flexible queries and joins over well-understood data |
| Inside? | B-tree indexes; WAL for durability; MVCC and isolation levels; buffer pool; replication by shipping the log |
| Fails? | Write ceiling on one primary (≈ 1k–10k small transactions/s); lock contention and long transactions; replica lag making reads stale; anomalies at weaker isolation (lost update, write skew) |
| Not? | Write rates or data sizes that need partitioning the database can't do natively, when the access pattern is a single key; append-only time series at very high rates; blobs |

Follow-up: "Two transactions each check a doctor is on call, then each takes one doctor off. Which isolation level
prevents it?" (Write skew: serializable, or an explicit lock/constraint.)

### 6. LSM / KV store

| Question | Must-hit |
|---|---|
| Why? | High write throughput and horizontal partitioning for key-based access patterns |
| Inside? | Memtable + WAL → immutable SSTables → compaction; bloom filters to skip files; partitioning by hash or range; replication (leader-based or leaderless quorums) |
| Fails? | Compaction falling behind (write stalls, read amplification); tombstone build-up; hot partitions; quorum settings that don't give the consistency people assume |
| Not? | Ad-hoc queries, joins or multi-key transactions; small datasets where one relational database is simpler |

Follow-up: "Why are reads slower than writes in an LSM, and what keeps them acceptable?" (Many files to check;
bloom filters, compaction, block cache.)

### 7. Search index

| Question | Must-hit |
|---|---|
| Why? | Find documents by content (terms, prefixes, relevance), which primary-key stores can't do |
| Inside? | Inverted index (term → posting list); analysis (tokenising, stemming); segments merged in the background; relevance scoring; sharded and replicated |
| Fails? | Indexing lag (near-real-time, not real-time); split brain or a red cluster; mapping explosions; deep pagination cost |
| Not? | As the source of truth; for transactional reads-after-writes; for exact key lookups a database already serves |

Follow-up: "A user edits a document and searches immediately. Why might they not find it, and what do you tell
the product team?" (Refresh interval; either accept seconds of lag or read the edited document from the primary.)

### 8. Object store

| Question | Must-hit |
|---|---|
| Why? | Cheap, durable storage for large immutable blobs, decoupled from compute |
| Inside? | Flat key space with metadata; data replicated or erasure-coded across devices and zones; multipart upload; storage tiers |
| Fails? | Per-prefix request-rate limits or hot keys; high per-request latency compared with a local disk; cost surprises from requests and egress rather than storage |
| Not? | Small, frequently updated records; low-latency random reads; anything needing in-place updates or transactions across objects |

Follow-up: "Your service stores photo metadata in the database and bytes in the object store. In what order do you
write them, and why?" (Blob first, then commit metadata; orphans are garbage-collected, dangling metadata is a bug.)

### 9. Queue

| Question | Must-hit |
|---|---|
| Why? | Decouple producers from consumers in time and rate; absorb bursts; distribute work |
| Inside? | Messages delivered to one consumer each; acknowledgements and visibility timeouts; redelivery; dead-letter queue; at-least-once delivery |
| Fails? | Backlog grows without bound (a hidden capacity problem); poison messages; duplicates on redelivery; lost ordering with parallel consumers |
| Not? | Synchronous request/response that needs an answer now; when you need replay or multiple independent readers (that's a log); when it only hides a consumer that is too slow |

Follow-up: "Your queue depth grows for an hour. Is that a problem?" (Only relative to the SLO: compute drain time;
alert on age of the oldest message, not depth.)

### 10. Log

| Question | Must-hit |
|---|---|
| Why? | A durable, ordered, replayable record that many independent consumers read at their own pace |
| Inside? | Append-only partitions; offsets tracked by consumers; consumer groups; retention by time or size; replication of partitions |
| Fails? | Ordering only within a partition; hot partitions from a skewed key; consumer lag; retention expiring data a slow consumer hasn't read |
| Not? | Per-message routing, priorities or individual acks with delays (a queue fits better); low volume where a database outbox is simpler |

Follow-up: "Why does adding partitions to a topic break per-key ordering for a while?" (Key → partition mapping
changes; in-flight messages for a key are in the old partition.)

### 11. Consensus / coordination service

| Question | Must-hit |
|---|---|
| Why? | Agree on one value (a leader, a config, a lock owner) despite failures, so the rest of the system doesn't have to |
| Inside? | Replicated log with a leader (Raft/Paxos family); majority quorums; terms/epochs; leases and watches for clients |
| Fails? | Loses availability without a majority; slow under high write rates (every write is a quorum round trip); a client holding an expired lease still acts — hence fencing tokens |
| Not? | High-throughput data paths; as a general database; when a single leader with a lease in an existing database is enough |

Follow-up: "A node holds a lock lease, pauses for 30 s in garbage collection, then writes. What stops the damage?"
(Fencing token checked by the storage being written to.)

### 12. Rate limiter

| Question | Must-hit |
|---|---|
| Why? | Protect capacity and fairness: stop one client from consuming what others need; enforce contracts and abuse limits |
| Inside? | Token bucket (burst + rate) vs sliding window; per-key state; local vs shared counters (accuracy vs latency); what is returned (`429` with `Retry-After`) |
| Fails? | Shared counter store becomes the bottleneck or a single point of failure; fail-open vs fail-closed when the store is down; clock skew between nodes; limits that punish legitimate bursts |
| Not? | As the only overload protection (you still need load shedding for traffic within the limits); for cost control when the real problem is an unbounded query |

Follow-up: "The limiter's Redis is down. Fail open or closed?" (Depends on what it protects: fail open with local
approximate limits for user traffic, fail closed for expensive or abusive endpoints; say which and why.)

### 13. Circuit breaker

| Question | Must-hit |
|---|---|
| Why? | Stop calling a failing dependency so callers fail fast, free up threads/connections, and give it room to recover |
| Inside? | Closed → open on an error or latency threshold over a window → half-open trial requests → closed; per-dependency (and sometimes per-endpoint) state |
| Fails? | Thresholds too sensitive (flapping) or too slow (damage done before it opens); one breaker for a dependency with mixed healthy and unhealthy endpoints; no fallback, so open = outage |
| Not? | Failures that are per-request (bad input) rather than dependency health; when retries with backoff and a timeout already bound the damage at low volume |

Follow-up: "Your vendor's health endpoint is green but 30% of calls time out. What should open the breaker?"
(Observed call outcomes and latency, not the health endpoint.)

## Recording

The oral is part of the loop record: `oral pass` or `oral revise (<blocks>)` goes in the `--notes` of
`sd record mock D4 --format loop …` (see [format.md](format.md)). Blocks at revise also get a note so the next loop
starts with them:

```text
sd record note D4 --tags oral,<block> --notes "oral revise: <block> — missed <must-hit>"
```
