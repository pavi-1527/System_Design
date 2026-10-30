# D3 Probe bank — interviewer follow-ups per case study

The probes a senior interviewer actually asks after the end-to-end diagram is on the board. They're grouped by
case study (C1–C16, the names and signature deep dives from [SYLLABUS.md](../../SYLLABUS.md#track-c--hld-case-studies)).
The tutor uses them in every `hld-45` mock and in the HLD round of a D4 loop. The answers count under the rubric
dimension **Probe handling** (see [rubric.md](rubric.md)). A probe that exposes a hole in the design also counts
against the dimension where the hole is, for example **Deep dives & trade-offs** or **Failure, operations & cost**.

**You may study this file.** Knowing that "what happens when the cache cluster dies?" is coming doesn't answer it:
the answer has to come from *your* design, with *your* numbers, at minute 32 with the clock running. Reading these
probes trains you to anticipate what an interviewer will ask, which is part of the senior bar. What spoils a mock is
knowing the problem and its numbers, and those are in [problem-bank.md](problem-bank.md). Don't open that one.

## How the tutor uses this bank

1. **Pick 3–5 probes per mock**, mostly along the risk the candidate chose to deep-dive, **plus at least one on a risk
   they avoided.** If they never mentioned the hot key, the hot-key probe is the one to ask.
2. **Map the problem to case studies.** Unseen problems list their nearest case studies in
   [problem-bank.md](problem-bank.md). Draw from those sections and adapt the numbers to the candidate's own
   estimates ("you said 40k writes/s, so at 10× that's 400k…").
3. **One probe at a time.** Wait for the full answer. Don't stack questions.
4. **Push one rung further on any vague answer.** For example: "Which metric?", "What number would make you change
   that?", "By what key?" or "What does the user see during those 30 seconds?" An answer that holds up only because
   nobody followed up gets scored at the level of the follow-up.
5. **Release the curveball** (the mid-interview requirement change) at ≈ 0:25–0:30 if the problem bank doesn't
   specify one. A good adaptation states what changes and what stays, then re-checks the numbers.
6. **Note the timestamp of each probe and a one-line verdict** in the mock log ([mock-log.md](mock-log.md)). The
   debrief quotes them.

Each "Listen for" cell describes a **meets** answer: the mechanism, a number, and the trade-off or flip condition. An
answer that names the right component with no mechanism and no number is **approaching**. An answer that doesn't
change course when the premise breaks is **below**.

---

## The probe ladder: categories and answer shapes

These eight probe kinds cover almost everything you'll hear. Each has a shape. Answers that follow the shape sound
senior, and answers that don't sound improvised.

| Kind | Typical wording | Answer shape | Approaching | Meets | Exceeds |
|---|---|---|---|---|---|
| **Scale-up** | "What breaks first at 10×? At 100×?" | Name the resource that saturates first, *with the number* → what you change → what that costs | "Add more servers" or "shard it", with no resource, key or number | Picks the first bottleneck from their own estimates (e.g. "the 10k-writes/s primary at 3×"), names the fix with its key/fan-out, and states the new cost or failure mode the fix introduces | Orders the next two bottlenecks too, and says which number would be the earliest warning |
| **Failure** | "This node / dependency / region dies. Now what?" | Blast radius → detection (how fast) → containment → recovery → what the user sees, and data loss in RPO terms | "It's replicated", with no detection, no user impact, and no answer to "what about in-flight requests?" | Says what fails, what keeps working (degraded mode), the RPO/RTO, which requests are retried and why that's safe (idempotency) | Also covers the correlated failure (bad deploy, poison message, thundering herd on recovery) and how to prevent the retry storm |
| **Consistency** | "Two users do X at the same moment. What does each see?" | The guarantee you give (by name) → the mechanism that enforces it → the anomaly you accept and why it's OK for this product | Names a guarantee ("strongly consistent") without the mechanism, or the mechanism without the anomaly | Names the guarantee, the mechanism (conditional write, quorum, single-writer, fencing token), and the window and anomaly they accept, tied to a requirement | Says where the boundary sits (strong for money, eventual for counts) and what it costs in latency/availability (PACELC) |
| **Cost** | "What does this cost per month? What dominates?" | The dominant line item (usually egress, storage tier or instances) with a rough number → one lever to cut it → what the lever costs | Lists services with no magnitudes | Identifies the dominant line from their own estimates with a ≈ figure and one concrete lever (CDN offload, tiering, compression, batching) | Compares the lever's engineering cost with the saving, or reasons about cost per user/request |
| **Security** | "How do you stop abuse? Who can see this data?" | Threat → control at the right layer (edge, service, data) → residual risk | "Use HTTPS and auth" | Names the specific threat for this product (enumeration, replay, SSRF, tenant leak, fraud), the control and where it runs, and its cost (latency, false positives) | Adds the audit/detection story and the abuse economics (what an attacker gains vs what it costs them) |
| **Why not X** | "Why not use Postgres / Kafka / a CDN / WebSockets here?" | Steelman X → the requirement or number that rules it out *here* → the condition under which you'd switch to X | Attacks X ("X doesn't scale"), or "we could use either" | Gives X's real advantage, the specific number or requirement that tips it, and a measurable flip condition | Points out a hybrid where X wins one path, or what X would look like if added later |
| **How would you know** | "How would you know this is breaking before users tell you?" | SLI (with percentile) → threshold → alert type (burn-rate, saturation, lag) → the leading indicator | "Monitoring and dashboards" | One user-facing SLI with a number (p99 < X ms, success ≥ 99.9%), one saturation/lag metric with a threshold, and what the on-call does when it fires | Multi-window burn-rate alerting, or a synthetic probe for a silent failure mode (e.g. "no events at all") |
| **Requirement change** | "Now it must work in two regions / for partners / with a regulator's retention rule." | What changes, what stays → the new dominant constraint → re-check the one number it moves | Starts redesigning from scratch, or bolts a box on without checking what it breaks | Names the 1–2 components that change and the new constraint; re-estimates the number it moves; keeps the rest | Says what they'd have done differently at minute 5 had they known, and how to migrate live |

**The universal red flags** (each caps **Probe handling** at *approaching*, whatever the topic): reaching for a
technology by brand ("Kafka, because it's scalable"); claiming a component cannot fail; answering a "why not X" by
attacking X; quoting an invented precise figure for a real product with confidence; abandoning the design under the
first push instead of defending or deliberately revising it.

<details data-kind="answer">
<summary>Worked example: one probe, three levels</summary>

Probe (C1): "Your redirect cache cluster goes down. What happens?"

- **Approaching:** "Requests fall back to the database, and Redis is replicated anyway." No numbers, and no check on
  whether the database survives the fallback.
- **Meets:** "Redirects drop from ≈ 50k/s served at ≈ 95% hit rate to all 50k/s on the database tier. Our replicas
  handle ≈ 20k indexed reads/s each with 3 of them, so ≈ 60k/s of headroom: we survive, barely, with p99 rising from
  ≈ 5 ms to ≈ 20–30 ms. I'd protect the database with request coalescing on misses and a concurrency limit, shed
  analytics writes first, and page on the cache-hit-ratio SLI dropping below 80%."
- **Exceeds:** adds the cold-restart problem (a recovered empty cache sends the miss storm back to the database, so
  warm it from the top-N hot keys or ramp traffic over gradually), and says CDN caching of 301s for permanent links
  would have absorbed most of the load in the first place, at the cost of losing per-click analytics for those hits.

</details>

---

## C1 — URL shortener

**Risk map:** ID generation (collisions, enumeration, coordination), the redirect read path (≈ 100:1 read/write,
cache hit rate, 301 vs 302), the analytics pipeline (click events must not slow redirects), abuse (phishing, spam).

| Kind | Probe | Listen for (meets) |
|---|---|---|
| Scale-up | "At 10× — say 50k redirects/s per region — which tier saturates first?" | Uses own hit-rate math: at 95% hit rate the DB sees ≈ 2.5k/s; cache nodes ≈ 100k+ ops/s each, so the stateless redirect tier (≈ 1k–10k req/s per server) or the analytics write path goes first; scale horizontally, batch analytics |
| Scale-up | "Writes go from 100/s to 10k/s (a bulk-API customer). What changes?" | ID generation must not be a single counter round-trip per write → range/block allocation per server; DB writes at 10k/s are near one primary's limit → partition by short code (hash), or batch inserts |
| Failure | "The ID allocator (counter service / ticket DB) is down for 10 minutes." | Servers hold pre-allocated ranges (e.g. 10k IDs each) so writes continue until ranges run out; a range lost at a crash is just a gap; redirects are unaffected |
| Failure | "The redirect cache cluster fails." | Miss storm to DB: quantify against replica capacity; request coalescing; serve stale/hot set from local in-process LRU; alert on hit-ratio drop; warm on recovery |
| Consistency | "A user creates a link and clicks it 50 ms later from another region. 404?" | Async cross-region replication can 404 a fresh link; fixes: read-through to the home region on miss, write the cache on create, or embed the region in the code; accept rare first-click latency instead of a 404 |
| Consistency | "Two users shorten the same long URL at once. Same code or different?" | Product decision: dedupe needs a long-URL index (unique constraint, costly at scale, leaks that someone else shortened it); most choose different codes per request; a custom alias needs a conditional insert |
| Cost | "Where does the money go?" | Mostly analytics storage and compute (billions of click rows), not redirects; keep raw clicks short-lived, roll them up, keep aggregates; a CDN for 301s cuts compute but loses click visibility |
| Security | "How do you stop people enumerating every link?" | Sequential base62 IDs are enumerable; use random or encrypted/shuffled IDs (e.g. a keyed permutation of the counter), 7+ chars ≈ 62^7 ≈ 3.5 × 10^12 space; rate-limit lookups that miss |
| Security | "Phishers use you to hide malicious URLs." | Check against threat lists on create (async + sync for high-risk), interstitial warning page, takedown flag checked on redirect; cost: create latency and false positives |
| Why not X | "Why not just hash the long URL (MD5, take 7 chars)?" | Collisions at scale (birthday bound: with 62^7 space, ≈ 50% collision chance by ≈ 2 M links) need check-and-retry anyway; deterministic codes also leak dedupe; flip: when idempotent "same URL → same code" is a requirement |
| Why not X | "Why 302 and not 301?" (or the reverse) | 301 is cached by browsers/CDN, which cuts load but loses analytics and can't be retargeted; 302 keeps every click visible; flip: no analytics requirement → 301 |
| Why not X | "Why not Kafka for click events? / Why Kafka?" | A log decouples redirects from analytics and allows replay; at a few hundred clicks/s a batched async write to a table is enough; the redirect must never block on it either way |
| How would you know | "How do you know redirects are healthy?" | Redirect p99 latency SLI (e.g. < 20 ms server-side) and 404-rate SLI (a spike means replication lag or a bad deploy); cache hit ratio as leading indicator; analytics consumer lag |

**Curveball:** "Links now expire, and enterprise customers can edit the destination of an existing link." A good
adaptation: 302 only (or short CDN TTLs) for editable links; cache invalidation on edit (delete + short TTL as a
backstop); expiry as a TTL in the cache plus a lazy check on read and a background sweeper; codes of expired links are
not reused (or only after a quarantine period) to avoid redirecting old printed links to a new owner.

**Red flags:** single auto-increment primary key with no plan for the write path or enumeration; analytics written
synchronously in the redirect path; "Redis is in-memory so it's fast enough" with no hit-rate or failure math.

---

## C2 — Distributed rate limiter

**Risk map:** algorithm choice (fixed window, sliding log/window, token bucket), where the shared counter lives and
what a call costs, accuracy vs latency, failure policy (fail open vs closed), per-tenant fairness.

| Kind | Probe | Listen for (meets) |
|---|---|---|
| Scale-up | "We go from 50k to 500k checks/s across 200 gateway nodes. Does central Redis still work?" | ≈ 100k+ ops/s per Redis shard, so shard by key (consistent hashing); or local token buckets with periodic sync (accuracy loss bounded by sync interval × nodes); one round-trip ≈ 0.5 ms added to every request |
| Scale-up | "One tenant's key gets 80% of traffic." | Hot key on one shard: local pre-allocation of tokens (lease N tokens per node), or a two-tier limit (local coarse + global fine); never shard a single bucket naively |
| Failure | "The Redis shard holding limits is unreachable." | Explicit policy per limit: fail open for abuse protection of internal APIs (availability first), fail closed (or local conservative fallback) for limits that protect a vendor contract or cost; short timeout (≈ 5–10 ms) so the limiter never becomes the outage |
| Failure | "A gateway node's clock drifts by 2 s." | Fixed/sliding windows keyed on local time get skewed; use the store's time (Redis `TIME` or server-side script) or token bucket with refill computed in one place |
| Consistency | "Two gateway nodes check the same key at the same instant with 1 token left. Both allowed?" | Read-then-write races allow both; atomic check-and-decrement (Lua script / single command) or accept bounded over-admission with local buckets and say the bound |
| Consistency | "How accurate does it need to be?" | Ties accuracy to purpose: billing quota needs near-exact (central, atomic); abuse protection tolerates ≈ 5–10% over-admission for zero added latency (local + async sync) |
| Cost | "What does the limiter cost us per request?" | One network hop ≈ 0.5–1 ms plus Redis capacity; local buckets cost memory per active key (≈ 100 B × active keys); compare with the cost of the thing being protected |
| Security | "An attacker rotates IPs and API keys." | Limit on several dimensions (account, IP prefix, device), cost-based limits for expensive endpoints, and a separate abuse system; the limiter is one control, not the fraud model |
| Security | "A credit union's partner retries aggressively on 429." | Return `Retry-After`, document backoff + jitter; per-client retry budgets; this is the outbound vendor-adapter problem from the other side |
| Why not X | "Why not a fixed window counter? It's one INCR." | Burst of 2× the limit at the window boundary; sliding window counter (weighted previous window) fixes it at ≈ same cost; flip: coarse daily quotas where boundary bursts don't matter |
| Why not X | "Why not do it in each service instead of at the gateway?" | Gateway gives one place for per-client limits; services still need their own concurrency limits/load shedding because the gateway can't see downstream saturation; both layers, different jobs |
| Why not X | "Why not a sliding log (sorted set of timestamps)?" | Exact, but O(limit) memory per key (1,000 req/min ≈ 1,000 entries ≈ tens of KB per key); fine for low limits, wasteful at millions of keys |
| How would you know | "How do you know the limiter is hurting legitimate users?" | 429 rate per client tier and per endpoint, limiter latency p99 (< ≈ 2 ms), limiter-store error rate (fail-open events), and complaints correlated to limit changes; shadow mode before enforcing a new limit |

**Curveball:** "Limits are now per tenant and change at runtime; a credit union upgrades its plan and expects the new
limit within a minute." A good adaptation: limit config separate from counters, pushed or cached with ≈ 30–60 s TTL;
token bucket parameters read at refill time so a change applies without resetting state; audit log of limit changes.

**Red flags:** read-then-write in two calls with no atomicity; no failure policy; one global Redis for everything with
no capacity math.

---

## C3 — Unique ID generator

**Risk map:** Snowflake bit layout (timestamp / worker / sequence), clock skew and clocks moving backwards, worker-ID
assignment, coordination-free generation, ordering guarantees.

| Kind | Probe | Listen for (meets) |
|---|---|---|
| Scale-up | "One worker needs 10 M IDs/s." | With 12 sequence bits, 4,096 IDs per ms per worker ≈ 4 M/s; beyond that, wait for the next ms (throughput cap), steal bits from worker ID, or run more workers; state the layout trade |
| Scale-up | "We go from 50 to 5,000 workers." | 10 worker bits = 1,024 workers; re-split bits (fewer sequence bits) or add a datacenter field; the layout is a long-term contract, so reserve bits early |
| Failure | "The system clock jumps back 500 ms after an NTP correction." | Detect (last timestamp > now): refuse/ wait until caught up for small skews, or fail loudly for large ones; never emit possibly duplicate IDs; alert on it |
| Failure | "The coordinator that assigns worker IDs (ZooKeeper/etcd/DB) is down." | Workers keep their leased IDs while running; new workers can't start; leases with TTL so a dead worker's ID is reclaimed only after a safe delay > max clock skew |
| Consistency | "Two workers end up with the same worker ID after a network partition." | Duplicate IDs: prevent with leases + fencing (worker stops issuing when it can't renew), or derive worker ID from something unique (pod ordinal) with a uniqueness check |
| Consistency | "Are your IDs sorted? Globally?" | Roughly time-ordered (k-sorted) across workers within clock skew, strictly increasing per worker only; if the product needs strict global order that's a sequencer, a different design with a throughput ceiling |
| Cost | "What does coordination cost?" | Snowflake: zero per-ID coordination; only worker registration. Central ticket DB: one round trip per ID or per batch, and a SPOF; batching amortizes |
| Security | "IDs leak information. Problem?" | Snowflake IDs expose creation time and volume (competitors can estimate growth); for public-facing IDs use random (UUIDv4) or encrypt the ID; internal IDs can stay sortable |
| Why not X | "Why not UUIDv4?" | 128 bits, no coordination, but random inserts fragment B-tree indexes and they aren't sortable; flip: when IDs are generated on clients/offline, or when time leakage matters; UUIDv7 gives time order in 128 bits |
| Why not X | "Why not a DB auto-increment?" | Simple and dense but single-writer bottleneck (≈ 1k–10k TPS) and SPOF; multi-master with offset/step works but is rigid; flip: low volume, one database |
| Why not X | "Why not a central sequencer with ranges (ticket server handing out blocks)?" | Valid: blocks of 1k IDs make the ticket server cheap; you lose time order across servers and lose a block on crash; good when dense-ish numeric IDs matter more than time order |
| How would you know | "How would you detect duplicates or skew in production?" | Per-worker metrics: clock-backwards events, sequence exhaustion per ms (throttle), lease renewal failures; a sampled uniqueness check downstream (unique index violations) as the last line |

**Curveball:** "IDs must be generated on mobile clients while offline and merged later." A good adaptation: switch to
128-bit IDs (UUIDv7 or random), accept no global ordering, order by server-assigned sequence on sync if order matters.

**Red flags:** ignoring clock-backwards; worker IDs assigned by hand with no reuse safety; claiming global strict order
from Snowflake.

---

## C4 — Distributed key-value store

**Risk map:** partitioning (consistent hashing, vnodes), replication with N/R/W quorums, repair (read repair,
hinted handoff, anti-entropy with Merkle trees), membership and failure detection, conflict resolution.

| Kind | Probe | Listen for (meets) |
|---|---|---|
| Scale-up | "Data grows from 10 TB to 1 PB." | ≈ 1–4 TB usable per node (TODO-VERIFY per hardware), so hundreds of nodes; vnodes spread rebalancing so a new node pulls from many peers; rebalancing throughput must be throttled to protect foreground latency |
| Scale-up | "One key gets 200k reads/s." | One replica set can't serve it; client-side or proxy cache for hot keys, read from all N replicas with R=1, or key splitting for writes; detect hot keys by sampling |
| Failure | "A node dies for 2 hours, then comes back." | Sloppy quorum + hinted handoff covers writes during the outage; hints may overflow → anti-entropy (Merkle tree diff) repairs on return; reads meanwhile served by remaining replicas |
| Failure | "A whole AZ goes down with N=3, W=2." | Replica placement must span AZs (one replica per AZ); with one AZ lost, W=2 and R=2 still succeed; if placement ignored AZs, some keys lose quorum |
| Consistency | "With N=3, R=2, W=2, can a client read a stale value?" | R+W>N gives overlap in the happy path, but sloppy quorums, concurrent writes, and failed partial writes can still return stale/conflicting data; it's not linearizability |
| Consistency | "Two clients write the same key concurrently on different replicas." | Last-write-wins by timestamp (simple, silently drops a write, clock-dependent) vs vector clocks/siblings returned to the client to merge; choose per use case (cart: merge; profile: LWW) |
| Cost | "What does N=3 cost vs N=2?" | 1.5× storage and write bandwidth; repair traffic; N=3 needed to keep a quorum through one failure plus one maintenance; erasure coding for cold data cuts overhead to ≈ 1.4× (TODO-VERIFY scheme) |
| Security | "Tenants share the cluster. How do you isolate them?" | Key-prefix namespaces with authz at the coordinator, per-tenant quotas (storage, ops/s) for noisy neighbours, encryption at rest with per-tenant keys if required |
| Why not X | "Why not just shard Postgres?" | Postgres gives transactions and secondary indexes; KV gives availability under partitions and easy horizontal growth; flip: needs multi-key transactions or ad-hoc queries |
| Why not X | "Why consistent hashing and not range partitioning?" | Hash spreads load and avoids hot ranges on sequential keys; range enables scans and splits hot ranges dynamically; flip: when range scans are a core access pattern |
| Why not X | "Why leaderless instead of leader-per-partition (Raft)?" | Leaderless keeps writes available during node failure with tunable consistency; Raft gives linearizable ops per partition at the cost of a failover pause (≈ seconds) and leader hotspots |
| How would you know | "How do you know replicas are diverging?" | Read-repair rate, hinted-handoff queue size, anti-entropy diff volume per run, and p99 latency per coordinator; alert when hints back up or diffs grow run over run |

**Curveball:** "One product needs compare-and-set on a key (e.g. an inventory count)." A good adaptation: leaderless
quorums can't give CAS safely; route those keys to a per-partition consensus group (Paxos/Raft, "lightweight
transactions") and accept higher latency for that path only.

**Red flags:** claiming R+W>N means strong consistency; no repair mechanism; replica placement ignoring failure
domains.

---

## C5 — News feed

**Risk map:** fan-out on write vs read, celebrities (accounts with millions of followers), the timeline cache,
ranking, pagination of a changing feed.

| Kind | Probe | Listen for (meets) |
|---|---|---|
| Scale-up | "A celebrity with 50 M followers posts. Walk me through it." | Fan-out on write = 50 M cache writes per post; at ≈ 1 M writes/s that's ≈ 50 s of fan-out capacity for one post; hybrid: pull celebrity posts at read time and merge with the pushed timeline |
| Scale-up | "DAU goes from 50 M to 500 M." | Timeline cache grows ≈ 10× (e.g. 800 post IDs × 8 B ≈ 6.4 KB/user → ≈ 3.2 TB at 500 M); keep only active users' timelines warm; fan-out workers scale with posts × avg followers |
| Failure | "The fan-out queue backs up for 20 minutes." | Feeds go stale, not broken; read path falls back to pull for recently active followees; queue lag alert; prioritize fan-out to active users; posts themselves are durable |
| Failure | "The timeline cache cluster loses a shard." | Those users' feeds rebuild on read from the follow graph + recent posts (expensive): rate-limit rebuilds, serve a degraded "recent from top followees" feed meanwhile |
| Consistency | "I post and immediately refresh. Is my post there?" | Read-your-writes: inject the author's own recent posts at read time regardless of fan-out progress; followers may see it seconds later, which is fine |
| Consistency | "A user deletes a post that was already fanned out to 2 M timelines." | Don't chase deletes into every timeline synchronously; filter at read time against a deleted/visibility check (hydration step), clean timelines lazily |
| Cost | "What's the big cost line?" | Timeline cache memory (TBs of RAM) and fan-out compute; storing IDs not bodies, capping timeline length, and skipping inactive users are the levers |
| Security | "Private accounts and blocks." | Visibility enforced at read/hydration time, not only at fan-out (follow relationships change after fan-out); blocks filtered at hydration; never trust the cached timeline for authz |
| Why not X | "Why not pure fan-out on read?" | Read = query N followees' recent posts and merge: with 300 followees that's 300 lookups per feed load at ≈ 100k+ feed loads/s; fine for low read volume or for celebrity accounts only |
| Why not X | "Why not compute the feed in a relational DB with a JOIN?" | Works at small scale; at 10^4+ feed reads/s across sharded data the scatter-gather JOIN is the bottleneck; flip: < ≈ 1k feed reads/s |
| Why not X | "Why a cursor and not page numbers?" | New posts shift offsets (duplicates/skips); cursor = (score, post_id) is stable and index-friendly |
| How would you know | "How do you know the feed is fresh?" | Fan-out lag (time from post to last timeline write, p99), feed load p99, cache hit ratio, % feeds served from fallback; alert on lag > ≈ 1 min |
| Requirement change | "Now the feed is ranked, not chronological." | Timeline cache holds candidates (≈ last N), a ranking service scores at read time with a latency budget (≈ 50–100 ms); cursor becomes a snapshot/session token |

**Curveball:** "Ads must be inserted every 5 posts, and advertisers are billed per impression." A good adaptation: ads
merged at read time from a separate service (never fanned out), impressions logged from the client on actual view,
deduped by impression ID before billing.

**Red flags:** fan-out on write with no celebrity handling; feed bodies duplicated into every timeline; authz decided
only at fan-out time.

---

## C6 — Chat / messaging

**Risk map:** millions of persistent connections, message ordering per conversation, delivery receipts and
exactly-once *display*, offline sync across devices, group chats.

| Kind | Probe | Listen for (meets) |
|---|---|---|
| Scale-up | "50 M concurrent connections. How many gateway servers?" | ≈ 100k–1M idle connections per tuned server is possible (TODO-VERIFY per stack); with heartbeats and TLS memory, plan ≈ 50k–100k per box → ≈ 500–1,000 gateways; connection registry maps user → gateway |
| Scale-up | "A group of 100k members (a broadcast channel) gets a message." | Per-member fan-out at send is too heavy; treat large groups as pull/fan-out-on-read with per-member read cursors, or fan out to gateways (not users) |
| Failure | "A gateway with 80k connections crashes." | Clients reconnect with backoff + jitter to avoid a thundering herd; resume from last acked message sequence; registry entries expire via TTL/heartbeat |
| Failure | "The message store is slow (p99 2 s) for 5 minutes." | Ack to sender only after durable write, so sender sees "sending"; client retries with the same client message ID; don't fake delivery; shed typing indicators/presence first |
| Consistency | "Two people send at the same moment. Do both see the same order?" | Per-conversation sequence assigned by one owner (conversation's partition leader); clients order by server sequence, not device clock |
| Consistency | "The sender retries after a timeout. Duplicate message?" | Client-generated message ID; server dedupes per conversation; receivers dedupe by ID too; at-least-once delivery + idempotent display |
| Cost | "Where's the cost?" | Persistent-connection servers (memory per connection), message storage growth (e.g. 100 B × 50 B msgs/day ≈ 5 TB/day before replication), media storage and egress dominate if attachments exist |
| Security | "End-to-end encryption: what changes?" | Server can't read content: no server-side search or moderation of content; key management per device (sessions per device pair), multi-device sync needs per-device encryption; metadata still visible |
| Why not X | "Why WebSockets and not long polling / push notifications?" | Bidirectional, low overhead per message at high frequency; long polling is simpler through proxies but costs a request per message batch; mobile backgrounded apps need OS push anyway |
| Why not X | "Why not Kafka as the message store?" | A log gives ordering and fan-out, but chat needs per-conversation random reads (history, sync from cursor) and long retention → a wide-column/KV store keyed by (conversation, seq); Kafka can still be the pipeline |
| Why not X | "Why not one global sequence for all messages?" | Global sequencer is a throughput ceiling and a SPOF; ordering only matters within a conversation |
| How would you know | "How do you know messages are being delivered?" | End-to-end delivery latency SLI (send → recipient ack, p99 < ≈ 500 ms for online users), undelivered-backlog per gateway, reconnect rate spikes, synthetic chat bots between regions |

**Curveball:** "Users want message edit and delete-for-everyone, synced across their 4 devices." A good adaptation:
edits/deletes are new events in the conversation sequence (tombstones), devices apply them on sync, history reads
return the latest version; never mutate in place without an event, or offline devices diverge.

**Red flags:** ordering by client timestamps; acking before persistence; no reconnect/resume protocol.

---

## C7 — Notification service

**Risk map:** provider failover (email/SMS/push vendors), dedupe of duplicate triggers, user preferences and quiet
hours, rate limits (per user and per provider), priority (OTP vs marketing).

| Kind | Probe | Listen for (meets) |
|---|---|---|
| Scale-up | "Marketing sends a campaign to 20 M members at 9:00." | Don't enqueue 20 M at once into the same queue as OTPs: separate priority queues; ramp sending to provider rate limits (e.g. at 2k SMS/s ≈ 2.8 h for 20 M); precompute audience in batches |
| Scale-up | "Transactional volume goes 10×: 5k → 50k notifications/s." | Workers scale horizontally; the limit is provider quotas → add providers, negotiate limits; preference lookups need caching (≈ 50k reads/s) |
| Failure | "The SMS vendor starts timing out at 30 s." (the learner has lived this) | Short per-call timeout (≈ 2–5 s), circuit breaker per provider, fail over to secondary provider for OTPs; idempotency key per message to the vendor if supported, else accept rare duplicates for OTP and record which provider sent |
| Failure | "A bug triggers the same 'transfer received' event 5 times." | Dedupe key = (event ID, channel, user) with a TTL'd store; notification service must be idempotent at intake because upstream is at-least-once |
| Consistency | "The user turns off SMS at 10:00:00; an SMS goes out at 10:00:01." | Preferences read at send time (not at enqueue), cached with a short TTL (≈ 30–60 s) and an explicit stated window; legal opt-outs need a stronger path (invalidate cache on change) |
| Consistency | "Delivery receipts arrive out of order or twice from the vendor webhook." | Status as a monotonic state machine (queued → sent → delivered/failed), ignore regressions, dedupe by vendor message ID |
| Cost | "SMS costs money per message. How do you control it?" | SMS is the dominant cost (≈ cents each, TODO-VERIFY by country); route by priority (push first, SMS for OTP/critical), collapse duplicates, per-user caps, cheapest-healthy-provider routing |
| Security | "Someone triggers OTP SMS to premium numbers (SMS pumping)." | Per-number/per-IP/per-account OTP rate limits, block premium/high-risk prefixes, CAPTCHA/risk score before sending, alert on cost anomalies per country |
| Security | "Notifications contain account balances." | Minimize sensitive data in SMS/push (lock-screen visible); send "you have a new alert, open the app"; templates reviewed; PII in logs masked |
| Why not X | "Why not have each product service call the vendors directly?" | Duplicated failover/retry/preference logic in every team, no global rate limiting or dedupe, no audit trail of what was sent; flip: a single sender with a single channel |
| Why not X | "Why a queue in front? Why not send synchronously from the API?" | Vendors are slow and flaky; queue absorbs bursts and enables retries; OTP path may still be synchronous-ish with a tight timeout because the user is waiting |
| Why not X | "Why not exactly-once delivery?" | Not achievable end to end with external vendors that don't support idempotency; at-least-once + dedupe where possible; for OTP a duplicate is harmless, a missing one isn't |
| How would you know | "How do you know OTPs are arriving?" | Send-to-delivered latency per provider and country (p95 < ≈ 10 s), delivery success rate per provider (alert < 95% over 5 min), queue age for the OTP queue, OTP verification completion rate as an end-to-end proxy |

**Curveball:** "Regulators require that every notification about a money movement is auditable for 7 years: what was
sent, to whom, through which provider, and the content." A good adaptation: append-only audit store (write before
send, update with provider response), content stored or a template ID + parameters, tamper evidence (hash chain),
retention tiering to cheap storage after ≈ 90 days.

**Red flags:** marketing and OTP sharing one FIFO queue; no per-provider breaker; preferences checked only at enqueue.

---

## C8 — Ticket booking

**Risk map:** seat holds with expiry, oversell prevention under contention, flash-sale traffic (waiting rooms and
queues), payment integration between hold and confirm.

| Kind | Probe | Listen for (meets) |
|---|---|---|
| Scale-up | "A stadium show (60k seats) gets 2 M users at 10:00:00." | Waiting room / virtual queue admits users at the rate the booking core can serve (e.g. ≈ 2k holds/s); 2 M users, 60k seats: most get a fair "sold out" fast; static/CDN pages for the queue |
| Scale-up | "100 popular events go on sale at the same minute." | Partition booking state by event (each event's seats on one shard), so contention stays within an event; waiting rooms per event |
| Failure | "Payment provider is down while users hold seats." | Holds expire (≈ 5–10 min) and release seats; extend holds when payment is in progress; don't confirm without payment; idempotent payment intent so a retry doesn't double-charge |
| Failure | "The booking DB primary fails over mid-sale (≈ 30 s)." | Holds/confirmations in flight fail and are retried idempotently; waiting room pauses admission; no seat state lives only in a cache |
| Consistency | "Two users click the same seat at the same millisecond." | Conditional update (`UPDATE seats SET held_by=? WHERE id=? AND status='free'`) or a unique constraint on (event, seat) for holds; exactly one wins; the other gets a clear error |
| Consistency | "A hold expires at the exact moment the payment succeeds." | Confirm must check the hold is still owned (conditional on hold ID/version); if lost, refund automatically; or extend the hold when payment starts |
| Cost | "Do we keep this capacity all year for 10-minute spikes?" | Autoscale stateless tiers and the waiting room; the seat-state store is small (60k rows per event) but needs write headroom; pre-scale for known on-sale times |
| Security | "Bots buy the best seats in 2 seconds." | Waiting room randomization (not first-click wins), per-account/payment-instrument limits, bot detection/CAPTCHA at queue entry, verified accounts for high-demand events |
| Why not X | "Why not just a Redis counter for available seats?" | Works for general admission (atomic DECR, never below 0), but assigned seats need per-seat state; Redis as source of truth risks loss on failover; DB remains authoritative |
| Why not X | "Why not optimistic 'book then check'?" | Overselling then cancelling is a terrible user experience and a refund cost; flip: airlines-style overbooking policy as a deliberate business choice |
| Why not X | "Why not a distributed lock per seat?" | Locks add a round trip and lease/fencing problems; a conditional write on the row is the lock; flip: multi-resource holds across stores |
| How would you know | "How do you know the sale is going well?" | Holds/s vs admission rate, hold→confirm conversion, hold expiry rate (payment trouble), oversell invariant check (confirmed ≤ capacity per event) as an alert, queue wait time |

**Curveball:** "Users can now hold up to 6 adjacent seats as a group, all or nothing." A good adaptation: one
transaction over the seat rows of one event (same partition, so local), lock in a deterministic order to avoid
deadlocks, or a "block" abstraction; suggest alternatives when exact adjacency fails.

**Red flags:** read-check-then-write without atomicity; holds without expiry; seat state only in a cache.

---

## C9 — Payments & ledger

**Risk map:** double-entry ledger invariants, idempotency end to end (client → API → ledger → payment provider),
reconciliation against bank/processor files, sagas and compensation, audit.

| Kind | Probe | Listen for (meets) |
|---|---|---|
| Scale-up | "From 100 to 10k transfers/s." | Single ledger DB at ≈ 1k–10k TPS is the ceiling; partition accounts (by account ID), keep each transfer's postings in one partition where possible, cross-partition transfers via a two-step saga with a suspense/clearing account |
| Scale-up | "One merchant account receives 30% of all postings." | Hot account row lock serializes writes: split into sub-accounts (sharded balances summed on read) or batch postings; balance reads become aggregates |
| Failure | "The processor times out on a charge. Did it go through?" (the learner has lived this) | Unknown outcome: never blindly retry with a new request; retry with the same idempotency key, or query status; hold funds in a pending state; reconciliation resolves the remainder |
| Failure | "The ledger write succeeds but the event to downstream (notifications, risk) is lost." | Transactional outbox in the same DB transaction as the postings; relay to the log; consumers dedupe |
| Consistency | "Two withdrawals from the same account at once with balance for only one." | Serialize per account: `SELECT … FOR UPDATE`, or conditional update `balance >= amount`, or single-writer per account partition; invariant: no negative balance unless overdraft allowed |
| Consistency | "Can a balance ever be wrong for a moment?" | Ledger (postings) is the truth; balances are derived (materialized in the same transaction, or computed); reads of a projection may lag, money decisions read the authoritative balance |
| Cost | "What costs the most here?" | Not infrastructure: chargebacks, fraud losses, and processor fees dominate; infra: the audit/retention storage (7+ years), and operations (reconciliation staff) |
| Security | "An insider changes a ledger row to cover a theft." | Append-only postings (no UPDATE/DELETE), corrections as reversing entries, hash-chained or signed entries for tamper evidence, separation of duties, audit log of access |
| Security | "A replayed API request moves money twice." | Idempotency key stored with request hash and the result, scoped per client, kept ≥ the client's retry window (≈ 24 h+); same key + different payload → error |
| Why not X | "Why double-entry? Just store balances." | Balances alone can't explain themselves: no audit trail, no invariant that money is conserved (sum of postings per transfer = 0), hard reconciliation |
| Why not X | "Why not a distributed transaction (2PC) across the ledger and the processor?" | External processors don't participate in 2PC; saga with pending states, compensations and reconciliation is the industry pattern |
| Why not X | "Why not floating point for amounts?" | Binary floats can't represent 0.10 exactly; integer minor units (or decimal) with explicit currency and rounding rules |
| How would you know | "How would you know money is missing?" | Continuous invariants: sum of all postings = 0 per transfer and globally; daily reconciliation breaks (count and value) against processor/bank files with alert thresholds; aging of pending transfers |
| Requirement change | "Now multi-currency." | Postings per currency, FX as a transfer between currency accounts at a recorded rate, never mix currencies in one balance, rounding policy explicit |

**Curveball:** "A partner bank sends a settlement file at 02:00 that disagrees with our ledger by 14 transactions."
A good adaptation: reconciliation job matches by external reference + amount + date, classifies breaks (missing on our
side, missing on theirs, amount mismatch), auto-resolves known patterns (late settlement), routes the rest to an ops
queue with SLAs; never edits postings, only adds adjustments.

**Red flags:** mutable balance as the only record; retry-with-new-request on timeouts; "exactly-once" claimed without
idempotency keys.

---

## C10 — File storage & sync

**Risk map:** chunking (fixed vs content-defined) and dedupe, metadata service vs blob store, upload resume, sync
protocol across devices, conflicts.

| Kind | Probe | Listen for (meets) |
|---|---|---|
| Scale-up | "From 10 M to 500 M users with 15 GB average quota." | Logical up to ≈ 7.5 EB at full quota; actual usage is a fraction plus dedupe; blob store scales (object storage), the hard part is metadata: billions of file/chunk rows → partition by user/namespace |
| Scale-up | "A 50 GB file uploads over a flaky mobile link." | Chunked, resumable upload (≈ 4–8 MB chunks), client tracks acked chunks, server commits the file version only when all chunks exist; parallel chunk uploads |
| Failure | "The metadata DB is down; the blob store is up." | Uploads can stage chunks but can't commit; clients queue changes locally; reads of existing files fail unless metadata is cached; this is why metadata needs the higher availability target |
| Failure | "A chunk upload completes but the commit never happens (client crash)." | Orphaned chunks: garbage-collect unreferenced chunks after a grace period (≈ 1–7 days), with reference counting or mark-and-sweep that tolerates in-progress uploads |
| Consistency | "Two devices edit the same file offline and both sync." | Detect via version vector/base version on commit (conditional write); keep both ("conflicted copy"), don't silently overwrite; text-merge only for specific types |
| Consistency | "Device sees file version 5's metadata but its chunks aren't readable yet." | Commit order: chunks durable first, then metadata points to them; readers never see metadata for missing chunks |
| Cost | "Storage is the bill. How do you cut it?" | Dedupe by content hash (cross-user dedupe has privacy issues), compression, tiering cold data to infrequent-access storage (≈ $0.023/GB-month standard vs cheaper tiers), delete old versions after a retention window |
| Security | "Cross-user dedupe: what's the risk?" | Confirmation-of-file attack (upload a hash to learn whether someone else has a file); mitigate with per-user or per-tenant dedupe, or convergent encryption with its own leaks |
| Why not X | "Why chunk at all? Upload whole files." | No resume, no delta sync (1-byte edit re-uploads the whole file), no dedupe; flip: small files (< ≈ 4 MB) are one chunk anyway |
| Why not X | "Why content-defined chunking instead of fixed 4 MB blocks?" | An insert at the start shifts every fixed block; CDC (rolling hash) keeps boundaries stable, so delta sync uploads only changed chunks; costs CPU and variable chunk sizes |
| Why not X | "Why not store metadata in the object store too?" | Need transactional updates, listing by folder, and fast queries; object stores are bad at those; flip: write-once archives |
| How would you know | "How do you know sync is working?" | Sync lag (change on device A → visible on device B, p95), conflict rate, upload failure/resume rate, orphan-chunk volume, metadata DB latency |

**Curveball:** "Enterprise tenants need shared folders with 10k members and per-folder permissions." A good adaptation:
namespace/folder as the unit of sharing with an ACL; permission checks on metadata ops; change feed per namespace so
members sync shared folders without per-member copies.

**Red flags:** metadata and blobs in one store with no rationale; last-write-wins on conflicts without telling users;
no garbage collection for orphans.

---

## C11 — Video streaming

**Risk map:** transcoding pipeline (DAG of jobs, many renditions), adaptive bitrate (ABR: HLS/DASH segments), CDN
economics (egress dominates), upload path for huge files.

| Kind | Probe | Listen for (meets) |
|---|---|---|
| Scale-up | "Uploads go from 500 to 50k hours of video per day." | Transcoding compute scales linearly: e.g. ≈ 6 renditions, each rendering at some multiple of real-time (TODO-VERIFY codec/hardware); split videos into segments to parallelize; queue + autoscaled workers, spot instances |
| Scale-up | "A live event with 10 M concurrent viewers." | At ≈ 5 Mbps per 1080p viewer ≈ 50 Tbps egress: multi-CDN, origin shield, segment caching (every viewer requests the same segments), lower default rendition under load |
| Failure | "A transcoding worker dies at 80% of a 2-hour movie." | Segment-level jobs: only the failed segments rerun; idempotent outputs keyed by (video, rendition, segment); job state in a durable queue/workflow engine |
| Failure | "One CDN has an outage in a region." | DNS/steering to a second CDN or to the origin shield (sized for it?); clients' ABR drops quality on slow segments; per-CDN health metrics drive steering |
| Consistency | "A creator updates a video's thumbnail and title. When do viewers see it?" | Metadata cache/CDN TTL (≈ minutes) or explicit purge; video segments are immutable (new versions get new URLs), so no invalidation problem there |
| Consistency | "View counts: exact or approximate?" | Approximate in real time (batched counters, stream aggregation), exact later for creator payouts (deduped event log); different consumers, different guarantees |
| Cost | "Egress is the bill. What do you do?" | Egress ≈ $0.05–0.09/GB list (TODO-VERIFY): CDN contracts/peering, better codecs (same quality at fewer bits), per-title encoding ladders, don't serve 4K to a phone, cache-hit ratio at the edge |
| Security | "Premium content gets ripped and re-hosted." | Signed URLs/tokens with short expiry for segments, DRM for premium (license server), watermarking for leak tracing; accept that determined pirates exist, raise the cost |
| Why not X | "Why not one MP4 per resolution, no segments?" | Segments enable ABR switching mid-stream, CDN caching of small objects, and fast start; progressive MP4 is fine for short clips |
| Why not X | "Why transcode up front? Transcode on demand." | On-demand saves compute for never-watched renditions (long tail) but adds first-view latency; common hybrid: transcode popular ladders eagerly, rare renditions lazily |
| Why not X | "Why not serve from our own origin servers?" | 50 Tbps doesn't fit any origin; CDNs exist for edge proximity and egress pricing; flip: small private video platform with low concurrency |
| How would you know | "How do you know playback is good?" | Client-side QoE metrics: startup time (p90 < ≈ 2 s), rebuffer ratio (< ≈ 1%), average bitrate, playback failures per CDN/ISP; transcoding queue age for upload freshness |

**Curveball:** "Creators want videos available within 1 minute of upload finishing." A good adaptation: publish the
lowest rendition first (fast), add higher renditions as they complete, parallel segment transcoding; manifest updated
as renditions land.

**Red flags:** egress cost never mentioned; monolithic transcode job per video; ignoring ABR.

---

## C12 — Search autocomplete

**Risk map:** tries vs inverted index, top-k aggregation of query frequencies, freshness (trending queries),
latency budget (per keystroke), personalization and filtering.

| Kind | Probe | Listen for (meets) |
|---|---|---|
| Scale-up | "10 B searches/day; every keystroke queries suggestions." | ≈ 10^10 / 86,400 ≈ 116k searches/s; × ≈ 5 prefix requests each ≈ 580k req/s avg, peak ≈ 1M+; client-side debouncing and caching cut it; edge/CDN cache for short popular prefixes |
| Scale-up | "Support 30 languages and per-country suggestions." | Separate tries/indices per locale; memory grows ≈ linearly; shard by (locale, prefix range); country-specific top-k from regional query logs |
| Failure | "The aggregation pipeline that rebuilds the trie fails for 2 days." | Serve the last good snapshot (suggestions get stale, not broken); versioned snapshots, health check before swapping in a new one; alert on snapshot age |
| Failure | "A bad trie build ships garbage suggestions." | Validate before promote (size within ±X%, top queries present, offensive filter applied), canary to 1% traffic, instant rollback to previous snapshot |
| Consistency | "A breaking news term appears. How soon is it suggested?" | Batch pipeline (hours) + a real-time trending layer (streaming counts over a sliding window, ≈ minutes) merged at query time; freshness vs cost |
| Consistency | "Different servers return different suggestions for the same prefix during a rollout." | Acceptable briefly; pin a user to a snapshot version via a version header if it matters; atomic swap per server |
| Cost | "Is precomputing top-k per prefix too expensive?" | Precomputing top-k (k≈10) at each trie node trades memory (k IDs per node) for O(prefix length) lookups; the alternative (walk subtree at query time) blows the latency budget for short prefixes |
| Security | "Offensive or defamatory suggestions; someone games the counts with bots." | Blocklist and classifier filtering at build time, manual takedown path that updates live quickly; dedupe counts per user/IP, ignore bot traffic, threshold minimum unique users |
| Why not X | "Why not query Elasticsearch with a prefix query on every keystroke?" | Possible at moderate scale; at ≈ 1M req/s with < 50 ms p99 a precomputed in-memory trie/top-k table is cheaper and more predictable; flip: need fuzzy matching/rich ranking on a smaller corpus |
| Why not X | "Why a trie and not a hash map of prefix → top-k?" | A prefix map is the trie flattened: simple, fast, easy to shard and cache, but duplicates storage; the trie is compact; both valid, choose by memory vs simplicity |
| Why not X | "Why not compute top-k exactly over all queries?" | Exact counts over billions of distinct queries is heavy; count-min sketch / heavy hitters in streaming, exact in batch for the head |
| How would you know | "How do you know suggestions are good?" | Latency p99 per keystroke (< ≈ 50 ms server), suggestion click-through rate, % searches using a suggestion, snapshot age; CTR drop after a deploy = bad build |

**Curveball:** "Suggestions must be personalized: your own recent searches first." A good adaptation: per-user recent
history (small, per-user KV, last ≈ 50 queries) merged with global top-k at query time; privacy controls (delete
history); latency budget re-split.

**Red flags:** querying the raw logs at request time; no snapshot/versioning; ignoring the per-keystroke QPS
multiplier.

---

## C13 — Web crawler (STRETCH)

**Risk map:** URL frontier (prioritization and politeness), dedupe (URL and content), DNS and fetch throughput,
traps, recrawl scheduling.

| Kind | Probe | Listen for (meets) |
|---|---|---|
| Scale-up | "Crawl 1 B pages a month." | ≈ 10^9 / 2.6 × 10^6 s ≈ 385 pages/s avg; at ≈ 500 KB per page ≈ 190 MB/s ≈ 1.5 Gbps; storage ≈ 500 TB/month raw before compression/dedupe |
| Scale-up | "Now 10 B pages a month." | Frontier and seen-URL set no longer fit one box: partition by host hash (keeps politeness per host local); bloom filter for seen URLs (≈ 10 bits per URL ≈ 12.5 GB for 10 B at ≈ 1% false positives) |
| Failure | "A crawler node dies with 50k URLs in its in-memory queue." | Frontier must be durable (persisted queues per partition); in-flight URLs leased and re-enqueued after timeout; idempotent fetch/store |
| Failure | "A site serves an infinite calendar of links (crawler trap)." | Per-host page budgets, URL depth limits, URL normalization, pattern detection; prioritization so traps can't starve the frontier |
| Consistency | "Two workers fetch the same URL." | Single owner per host partition prevents it mostly; content dedupe by hash catches the rest; harmless duplicate work, not corruption |
| Cost | "Bandwidth or storage: which dominates?" | Storage of raw pages over time and bandwidth; levers: compress, store only changed content, adaptive recrawl (static pages less often) |
| Security | "The crawler fetches internal IPs from a malicious link (SSRF) / gets blocked by sites." | Block private IP ranges after DNS resolution, respect robots.txt and crawl-delay, identify the user agent, back off on 429/503 |
| Why not X | "Why not BFS over a single queue?" | No politeness (hammers one host), no priority; per-host queues with a scheduler that enforces delay per host plus a priority layer |
| Why not X | "Why not a database table for the frontier?" | Billions of rows with constant pop/push churn; append-only queues per partition are cheaper; DB fine for small focused crawls |
| How would you know | "How do you know the crawl is healthy?" | Pages fetched/s, error rate by class (DNS, 4xx, 5xx, timeout), frontier size and age per priority, duplicate-content rate, per-host politeness violations (should be 0) |

**Curveball:** "News sites must be recrawled within 5 minutes of publishing." A good adaptation: a high-priority
frontier lane for a small set of hosts, sitemaps/RSS/ping-based discovery instead of blind recrawl, separate fetch
capacity reserved for the fresh lane.

**Red flags:** no politeness; in-memory-only frontier; no trap defence.

---

## C14 — Proximity search (STRETCH)

**Risk map:** geospatial indexing (geohash, quadtree, S2-style cells), read-heavy static data vs moving objects,
radius queries across cell boundaries, hot areas.

| Kind | Probe | Listen for (meets) |
|---|---|---|
| Scale-up | "200 M places, 100k searches/s." | Places are read-mostly: replicate the whole index (≈ 200 M × ≈ 100 B ≈ 20 GB in memory, fits one box) to many read replicas; search QPS scales by replicas |
| Scale-up | "Now drivers' locations update every 4 s from 2 M drivers." | ≈ 500k writes/s: in-memory, sharded by cell; TTL'd entries; don't write every update to a durable DB synchronously |
| Failure | "The in-memory location shard for downtown loses its data." | Driver locations are ephemeral: they repopulate within one update interval (≈ 4 s); meanwhile fewer matches in that area; replicate if 4 s blindness matters |
| Failure | "A cell boundary splits a dense area; queries near the edge miss results." | Query the target cell plus neighbours (8 for geohash grid), then filter by true distance; choose cell size from the radius |
| Consistency | "A restaurant closes permanently. How quickly does it disappear?" | Static data via a nightly or streaming index rebuild; for urgent removals, a deny-list overlay checked at query time |
| Cost | "Do we need a specialized geo database?" | Not necessarily: geohash prefix in a regular index or PostGIS for moderate scale; in-memory grid for high write rates; cost vs operational familiarity |
| Security | "Location privacy for drivers/users." | Store precise locations briefly (TTL), coarsen for analytics, restrict who can query raw locations, audit access |
| Why not X | "Why not compute distance to every place in SQL?" | Full scan of 200 M rows per query; spatial index needed; fine for thousands of places |
| Why not X | "Geohash vs quadtree?" | Geohash: simple prefix strings, fixed cells, works in any KV index, uneven density; quadtree: adapts to density (dense cities split deeper), in-memory structure; pick by density skew and write rate |
| How would you know | "How do you know results are good?" | Query latency p99, empty-result rate per area (indexing gaps), location staleness (age of last update), match acceptance downstream |

**Curveball:** "Results must be ranked by travel time, not straight-line distance." A good adaptation: geo index
produces a candidate set (k ≈ 50–100), a routing/ETA service ranks it within the latency budget, cache ETAs by cell
pair.

**Red flags:** no neighbour-cell handling; durable DB write per location ping; one global grid with no density
handling.

---

## C15 — Metrics & monitoring system (STRETCH)

**Risk map:** high-volume ingest of time series, storage (compression, downsampling, retention tiers), cardinality
explosions, query and alerting paths, monitoring the monitoring.

| Kind | Probe | Listen for (meets) |
|---|---|---|
| Scale-up | "10k hosts × 1,000 series each, every 10 s." | 10^7 series / 10 s = 10^6 samples/s; at ≈ 1.5–2 bytes/sample compressed (Gorilla-style, TODO-VERIFY) ≈ 1.5–2 MB/s ≈ 130–170 GB/day |
| Scale-up | "A team adds a `user_id` label." | Cardinality explosion (millions of series): per-tenant series limits enforced at ingest, reject/aggregate high-cardinality labels, alert on series count growth |
| Failure | "The time-series DB is down for 15 minutes." | Agents buffer locally (bounded) and replay; alerting must not depend on the same path silently: alert on absent data; separate alerting evaluation path if possible |
| Failure | "Ingest is fine but alert evaluation stops." | Silent failure: watchdog/dead-man's-switch alert that must fire continuously, external heartbeat monitoring |
| Consistency | "Late samples arrive 5 minutes late. Does the alert fire?" | Alert evaluation windows tolerate some lateness (evaluate with a delay); out-of-order ingestion policy; trade detection speed vs false negatives |
| Cost | "Retention: 13 months of 10 s data?" | Downsample: raw 10 s for ≈ 2 weeks, 1 min for ≈ 3 months, 1 h for 13 months; cold data to object storage; cuts storage ≈ 10–100× |
| Security | "Metrics labels leak customer data." | Label allowlists, PII scrubbing at ingest, tenant isolation of query access |
| Why not X | "Why not store metrics in Postgres?" | 10^6 inserts/s and range scans over time per series; a TSDB's columnar compression and append-only blocks are orders of magnitude cheaper; Postgres fine at low volume |
| Why not X | "Push (agents send) vs pull (server scrapes)?" | Pull: server controls rate and knows when a target is down; push: works behind NAT/for short-lived jobs; many systems support both |
| How would you know | "How do you monitor the monitoring?" | A second minimal system (or external service) checks ingest rate, rule evaluation lag, and alert delivery end to end with synthetic alerts |

**Curveball:** "Percentile latencies across 500 hosts, per minute." A good adaptation: you can't average p99s; ship
histograms (fixed buckets or sketches) and merge them, compute percentiles at query time.

**Red flags:** averaging percentiles; no cardinality control; alerting that fails silently with the database.

---

## C16 — Collaborative editing (STRETCH)

**Risk map:** concurrent edits (OT vs CRDTs), the document session server, presence/cursors, offline edits, history
and snapshots.

| Kind | Probe | Listen for (meets) |
|---|---|---|
| Scale-up | "One document with 200 simultaneous editors." | Ops broadcast is O(editors) per op; batch ops (≈ 50–100 ms), throttle presence/cursor updates harder than content, cap active editors and degrade others to viewers |
| Scale-up | "10 M documents open at peak." | Sessions sharded by document ID: each open document lives on one session server; registry maps doc → server; idle documents unloaded |
| Failure | "The session server holding a document crashes." | Ops persisted (op log) before ack; clients reconnect to a new owner, which loads snapshot + log tail; clients resend unacked ops with IDs (dedupe) |
| Failure | "A client is offline for 3 hours, then reconnects with 500 ops." | Rebase/transform against the server's ops since the client's base version (OT), or merge (CRDT); large divergence may need a snapshot reload with the local changes reapplied |
| Consistency | "Two users type at the same position at the same time. Final text?" | Both clients converge to the same text: OT with a central server ordering ops, or CRDT with tie-breaking by replica ID; intention may differ, convergence must not |
| Cost | "Storing every keystroke forever?" | Op log compacted into periodic snapshots (e.g. every N ops); keep fine-grained history for a window, coarser versions after |
| Security | "Share links and permission changes mid-session." | Permission checked on connect and re-checked on change (server pushes revocation, closes session); viewer vs commenter vs editor enforced server-side on each op |
| Why not X | "Why not lock the paragraph being edited?" | Simple, but bad UX (lock contention, stale locks when clients vanish); OT/CRDT allow simultaneous editing; flip: structured forms or code review where locking is acceptable |
| Why not X | "OT or CRDT?" | OT: central server, smaller metadata, mature in server-centric editors; CRDT: peer-to-peer/offline-first friendly, larger metadata per character, no central ordering needed |
| How would you know | "How do you know documents aren't diverging?" | Periodic checksum of document state between server and clients, divergence-report rate, op ack latency p99, reconnect/resync rate |

**Curveball:** "Add comments anchored to text ranges that survive edits." A good adaptation: anchor comments to
positions that transform with ops (or CRDT element IDs), handle anchors whose text is deleted (orphaned comments
listed separately).

**Red flags:** last-write-wins on the whole document; acking ops before persistence; no plan for reconnect/offline.
