# A4 Design exercise — Storage topology for a multi-tenant transactions store

> Time box: 45 minutes, solo, no notes. Then self-score against the rubric (≈ 15 min), then send your write-up to the
> tutor. Use the structure from [`docs/INTERVIEW_FRAMEWORK.md`](../../docs/INTERVIEW_FRAMEWORK.md) and the numbers from
> [`docs/NUMBERS.md`](../../docs/NUMBERS.md). This exercise is about the *data layer*: how the data is split, copied, found and
> kept healthy. You do not need to design the API or the app tier.

## Prompt

> "We run a digital-banking platform for 40 credit unions of very uneven size: a couple are huge, most are small. We keep a
> transactions store (posted and pending transactions, balances) that the mobile and web apps read all day, that credit-union staff search,
> and that partners read through an API. It has outgrown a single database server. Design the storage topology: how the data is
> partitioned and replicated, how reads stay correct after a member moves money, what happens when a node or a zone dies, and how we grow it."

That is all the interviewer says up front. Treat it like the real interview: ask the clarifying questions you need (write them down,
then write the answers you'd assume if the interviewer said "you decide"), and drive.

Things an interviewer would answer if asked (don't read until you've written your questions):

<details data-kind="hint">
<summary>Interviewer's answers to common clarifying questions</summary>

- Members: ≈ 3 M across 40 credit unions; the two largest hold ≈ 800 k and ≈ 500 k members; the other 38 average ≈ 45 k. ≈ 6 M accounts.
- Volume: ≈ 12 M new transactions per day on average (≈ 140 writes/s), with payday and month-end peaks of ≈ 10× (≈ 1,400 writes/s).
  Five years of history stays online: ≈ 22 B rows, ≈ 9 TB with indexes today; assume ≈ 40% growth per year.
- Reads: balance and recent-transactions screens ≈ 20,000 reads/s at peak (≈ 15 reads per write). Statements are by account and
  date range (up to 12 months). Partner API reads ≈ 2,000/s; partners tolerate ≈ 5 s of staleness; they are rate-limited per credit union.
- Member search for credit-union staff (by name, by last four of the card number): ≈ 50 searches/s at peak; a newly created member must be
  findable within ≈ 10 s. Searches are always scoped to one credit union.
- The core banking system is the system of record for most data and can re-send history, so core-synced rows are re-derivable
  (acceptable RPO ≈ 30 s). Transfers initiated on our platform (before the core confirms) are not re-derivable: RPO ≈ 0 for them.
- Availability SLO 99.95%; one region with three availability zones; a second region for disaster recovery (RTO 1 h, RPO 5 min).
- A transfer moves money between two accounts, usually within one credit union. A member who has just moved money must see the new balance immediately on the
  next screen and on their other devices.
- Each credit union has ≈ 10 general-ledger accounts. The ACH-settlement GL account of the largest credit union is updated by every ACH posting,
  ≈ 900/s during its payroll window.
- The biggest credit union's contract requires that its data not share database servers with other credit unions.
- The team is six engineers with moderate database-operations experience; payday weeks are a change freeze.

</details>

## What to produce

1. **Requirements**: functional (bullets) and non-functional (latency, availability, durability/RPO per kind of write, freshness per kind of read), with
   your clarifying questions and assumptions.
2. **Estimate sheet**: average and peak write TPS and read QPS, storage with growth and replication, number of partitions with the constraint that decides it
   (storage, writes or reads), replica counts, machine count, replication bandwidth, and what you hold back as headroom. Arithmetic shown.
3. **Topology**: partition key and scheme (and why not the alternatives), how tenants are placed (including the contractual one), the replication
   group per partition (leader, sync policy, followers), failure-domain placement across the three AZs, the partition map / directory and how clients and
   services find a partition's leader.
4. **Replication and lag plan**: which writes are synchronous and which asynchronous; what the app does so a member sees their own transfer on the next screen
   and on other devices; monotonic reads; how replicas that lag are handled; failover (detection, promotion rule, fencing, lost-writes handling) and
   RPO/RTO claims with numbers.
5. **Skew plan**: hot rows and keys you expect (GL accounts, large business members, tenant promotions), how you detect them and what you do about each (read-hot vs write-hot).
6. **Query plan**: how statements, member search (name, last four), and partner reads are served; local vs global index choices; fan-out and its tail-latency cost;
   cross-partition transfers.
7. **Growth and operations**: how you add capacity, move a tenant, rebalance, and with what limits and monitoring; what you do about payday; what you would alert on.
8. **Trade-off table**: at least three decisions with "wins when / loses when / when the answer flips".
9. **Failure walkthrough**: an AZ is lost at 09:05 on payday; one GL account melts one partition; a replica set's lag jumps to 30 s; the largest credit union's
   shard reaches 80% disk.

## Rubric

| Dimension | below | approaching | meets (senior bar) | exceeds |
|---|---|---|---|---|
| Requirements & scope | Starts drawing immediately; no per-operation durability or freshness needs | Lists features; "consistent and fast" | Separates re-derivable from non-re-derivable data, states RPO/RTO and freshness per operation, tenants and peak patterns clarified | Spots payday, GL hot rows and the contractual-isolation tenant as design drivers up front; prioritises scope explicitly |
| Estimation & sizing | None, or orders of magnitude off | Average numbers only | Peak and average, storage × growth × RF, partitions = max(storage, writes, reads) with the deciding constraint named, headroom and AZ-loss check | Uses numbers to decide (partition for operability not TPS; logical partitions ≫ machines; N+AZ capacity at peak) |
| Partitioning scheme & key | `mod N` hashing or a timestamp key, no justification | Hash on an id, queries not considered | Composite key `(tenant, account)` hash + `posted_at` order, justified from the statement query, avoids hot tail, explains alternatives and what it gives up | Plans key evolution, logical-partition count and a migration path; shows the cost of the other candidate keys with numbers |
| Tenancy & placement | One shared pile, or a database per tenant with no reasoning | Mentions a tenant mapping | Directory (tenant → cell/shard) with dedicated cells for the big/contractual tenant, pooled cells for small ones, cached directory, move procedure | Cell architecture with blast-radius reasoning, per-tenant limits and a story for a tenant outgrowing its cell |
| Replication & durability | "Replicate it" / all async or all sync | Names sync vs async without mapping to data | Semi-sync to another AZ for non-re-derivable writes, async for re-derivable; RPO computed from lag × write rate; failure behaviour when no follower acks stated | Per-transaction commit levels, cross-region DR stance (RPO 5 min) with lag guard, and what is lost on a region failover |
| Lag & read correctness | Reads from replicas with no mention of staleness | Says "read from primary after write" | Names the anomalies; LSN/commit token or leader window for needs-fresh reads, floors for monotonic reads across devices, lag-aware routing and a leader-read-share metric | Stores the token with the member for cross-device, bounds waiting, shows how the fix degrades under a lag spike and the capacity impact on leaders |
| Failover & failure domains | One diagram, no failure handling | Mentions failover without mechanics | Promotion rule (highest LSN), fencing, lost-writes report, client redirection with epochs, one replica per AZ, leaders spread, simultaneous failovers on AZ loss, no split brain | Quantifies RTO, rehearsal/game-day plan, lag guard on promotion, reconciliation against the core for lost writes |
| Hot keys & skew | Not mentioned or "add shards" | Mentions caching | Distinguishes read-hot/write-hot; GL rows solved by append-only ledger or key splitting with fan-in; detection (heavy hitters) and per-key limits; knows adding shards does not help | Quantifies single-row ceiling (≈ 1/lock-hold), splitting factor, and the read cost; plans the pre-split data for newly hot keys |
| Secondary indexes & cross-partition queries | Scatter-gather everywhere | Names local vs global | Tenant-scoped fan-out bounded, local index for staff search with rate limit or global/CDC index with staleness SLO and re-check on read; fan-out tail math (1 − 0.99^N); transfers across partitions addressed (co-location, ledger, saga/2PC named) | Hedging, index repair/rebuild, partner reads on dedicated replicas, pagination across partitions |
| Rebalancing & operations | "We'll add nodes" | Mentions a rebalancer | Fixed logical partitions with a table, throttled one-at-a-time moves off-peak, copy + catch-up + fenced cutover + verify, skew and lag monitoring, payday freeze, disk headroom ≥ 30% | Dry-run planning with movement estimates, rollback, rebalance-storm protection, capacity review cadence |
| Trade-offs & communication | Single answer per decision | Trade-offs only when prompted | Trade-off table with "when the answer flips"; drives the 45 minutes; checks in; states what was deferred | Anticipates follow-ups; quantifies cost, latency and operational load; proposes an evolution path (managed service vs self-run) |

## Tutor critique protocol

1. Read the write-up once fully before scoring. Score each dimension independently using the rubric's words; quote the learner's own text as
   evidence for each score.
2. Check the arithmetic in the estimate sheet. An estimate that is off by > 10× on a number the design depends on (partition count, raw storage, peak writes) caps
   *Estimation & sizing* at "approaching". Check that the deciding constraint is named.
3. Ask these follow-up probes (all of them; the learner answers in writing or live, without notes). Score the answers into the relevant dimensions:
   - "A member transfers $400 and opens the balance screen 300 ms later, but the replicas are 3 s behind. Walk me through exactly what the app and the
     router do. What happens to your leader's load during a payroll lag spike?"
   - "The primary of one partition dies; its async follower was 1.3 s behind. What is lost, how do you find out, who reconciles, and what changes if
     the partition used semi-sync?"
   - "You add a ninth shard group. Show me which rows move, how many, and what happens to a write for a moving row during the copy and at cutover."
   - "The ACH-settlement GL row of the biggest credit union takes 900 updates/s and the partition is on fire. Adding partitions did not help. Why, and
     what do you change?"
   - "Staff search by last four digits across the whole platform: why is that a problem, and how do you serve the tenant-scoped version at p99 < 300 ms?"
   - "An AZ disappears at 09:05 on payday. How many leaders fail over, what capacity do the survivors have, and what must not happen in the next hour?"
   - "A transfer between two members of the same credit union lands on two partitions. What are your options and what do you pick?"
   - One probe of the tutor's choice on the learner's weakest dimension.
4. Overall level:
   - **meets** requires "meets" or better on *Partitioning scheme & key*, *Replication & durability* and *Lag & read correctness* (the three dimensions where a
     mistake loses money or shows a member a wrong balance), and no dimension at "below".
   - **exceeds** requires "meets" on every dimension and "exceeds" on at least three.
   - Otherwise **approaching** (or **below** if two or more dimensions are "below").
5. Feedback: three bullets — the strongest part, the one change that would most raise the score, and one concrete thing to re-read (link the
   section, e.g. [`unit-1-replication.md`](unit-1-replication.md) §3 or [`unit-3-hot-keys-indexes-sizing.md`](unit-3-hot-keys-indexes-sizing.md) §10). If
   the result is below "meets", the learner redoes the exercise on a different day (variation: "design the storage topology for the audit-trail store of the
   same platform: append-only, write-heavy, queried by actor and time", same rubric).

## Recording

`sd record design A4 --self <below|approaching|meets|exceeds> --tutor <level> --notes "<one-line summary>"`
