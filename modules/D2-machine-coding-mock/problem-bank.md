# D2 Problem bank — interviewer only

> **Learner: stop here.** This file is the tutor's. Reading it turns an unseen-problem mock into a rehearsed one, and
> the D2 gate (three passing mocks on distinct problems) only means something if the problems are new to you. If you
> have read a problem, tell the tutor; it's then "seen" and won't be used for a gated mock.

**Tutor: how to use this bank.**

- Pick a problem the learner hasn't seen in a D2 or D4 mock (`mocks` in `progress.json`) and preferably from a skill
  family the previous mock didn't use. None of these overlap B3's eighteen problems or its six design-exercise
  variants; if the learner says they've built one of these at work, pick another.
- Read the **prompt** verbatim at 0:00. Answer clarifying questions from the **answer sheet**; give the "likely next
  requirement" hint only if asked. Read **Stage 2** at 0:30 and **Stage 3** at 0:55, verbatim.
- After 1:30, turn the **acceptance checklist** into `acceptance_test.go` against the learner's API (see
  [rubric.md](rubric.md), mechanical check 5). Then ask 2–3 **problem probes** plus 2–3 from
  [probe-bank.md](probe-bank.md).
- **Red flags** are things to look for while reading the code; the number in brackets is the
  [rubric](rubric.md) dimension it counts against (a letter-number such as K2 points at a
  [probe-bank](probe-bank.md) probe worth asking).
- You can write an equivalent problem of your own. Keep the shape: a core model with error semantics (Stage 1), a new
  dimension that stresses the model (Stage 2), concurrency, failure and invariants (Stage 3), sized so a strong
  candidate reaches part of Stage 3 in 90 minutes (≈ 250–400 lines of code plus ≈ 150–250 lines of tests).

| # | Problem | Skill family | Banking flavour |
|---|---|---|---|
| 1 | [Lease manager with fencing tokens](#1-lease-manager-with-fencing-tokens) | time, expiry, monotonic tokens | batch-job ownership |
| 2 | [Event-sourced account store](#2-event-sourced-account-store) | append-only state, optimistic concurrency, projections | yes |
| 3 | [Webhook delivery queue](#3-webhook-delivery-queue) | per-key ordering, retries, DLQ, worker pools | yes (vendor callbacks) |
| 4 | [Real-time auction with anti-sniping](#4-real-time-auction-with-anti-sniping) | contention, deadlines that move, observers | — |
| 5 | [Metrics aggregator with windowed rollups](#5-metrics-aggregator-with-windowed-rollups) | aggregation, time windows, top-k | — |
| 6 | [DAG workflow runner](#6-dag-workflow-runner) | graphs, bounded parallelism, cancellation, retries | statement run |
| 7 | [Write-behind cache in front of a slow store](#7-write-behind-cache-in-front-of-a-slow-store) | coalescing, flush, crash semantics | core-banking adapter |
| 8 | [Batch payment-file processor](#8-batch-payment-file-processor) | parsing, validation, idempotent posting, partial failure | yes |
| 9 | [Polite fetch scheduler](#9-polite-fetch-scheduler) | per-host rate limits, dedupe, bounded concurrency, fairness | vendor rate limits |

---

## 1. Lease manager with fencing tokens

**Prompt (verbatim).** "Several worker processes run the same scheduled jobs, like the nightly interest accrual, and
exactly one of them may run a given job at a time. Build the in-memory core of a lease manager: a worker asks for a
lease on a named resource for a duration, renews it while it works, and releases it when done. If a worker dies, its
lease must expire so another can take over. I'll add requirements as we go."

**Answer sheet.**
- Scale: ≈ 1,000 resources, ≈ 50 workers; renewals every few seconds. Read and write rates are tiny; correctness is
  everything.
- Concurrency: called from many goroutines (stand-ins for processes). Yes, `Acquire` races are the point.
- Time: leases are durations from "now"; inject the clock. Expiry is exact: a lease granted at `t` for `d` is held
  for `[t, t+d)`.
- Errors callers distinguish: held by someone else, not the holder, expired, unknown resource.
- Out of scope: real networking, persistence, clock sync between processes.
- Likely next requirement (if asked): "protecting the resource itself from a worker that *thinks* it still holds the
  lease".

| | Requirements |
|---|---|
| **Stage 1** (0:00–0:30) | `Acquire(resource, owner, ttl) (Lease, error)`; `Renew(lease, ttl) error` only by the current holder and only before expiry; `Release(lease) error` idempotent for the holder; an expired lease can be acquired by anyone; `Holder(resource)` returns the current owner or none; injected clock; safe for concurrent use |
| **Stage 2** (0:30–0:55) | **Fencing tokens:** every successful `Acquire` returns a token strictly greater than any token ever issued for that resource (also across expiry and release). Add a `Guard` that the protected resource uses: `Guard.Check(resource, token) error` rejects any token lower than the highest token it has seen for that resource. Plus `Acquire` with wait: `AcquireWait(ctx, resource, owner, ttl)` blocks until the lease is free or `ctx` is done |
| **Stage 3** (0:55–1:20) | **Watchers and contention:** `Watch(resource) (<-chan Event, cancel func())` emitting acquired/released/expired events with a bounded buffer and an explicit slow-watcher policy; 100 goroutines contend for one resource and at any instant at most one holds an unexpired lease (an invariant checked during the test); `AcquireWait` waiters are woken in FIFO order; no goroutine leak after `cancel` or `ctx` cancellation |

**Acceptance checklist.**
1. Second `Acquire` on a held, unexpired resource fails with the "held" error.
2. At exactly `t+ttl` the lease is expired and another owner can acquire; at `t+ttl-1ns` it can't.
3. `Renew` by a non-holder fails; `Renew` after expiry fails with the "expired" error even if nobody re-acquired.
4. `Release` twice by the holder succeeds both times (idempotent); `Release` by a non-holder fails and doesn't free it.
5. Tokens for one resource strictly increase across acquire → release → acquire and acquire → expire → acquire.
6. `Guard.Check` rejects a stale token after a newer one was checked; accepts equal-or-higher.
7. `AcquireWait` returns promptly when the holder releases; returns `ctx.Err()` when cancelled, without leaking.
8. 100 goroutines × 100 acquire/release cycles on one resource: never two unexpired holders at once (sampled
   invariant); `-race` clean.
9. Watch events arrive in order for one resource; a watcher that never reads doesn't block `Acquire`.
10. Tokens of different resources are independent (or globally increasing: either is fine if stated).

**Problem probes.**
- "Your worker pauses for a 40-second GC while holding a 30-second lease. What happens, and what stops it from
  corrupting the resource?" — *Listen for:* the lease expires and another worker acquires; the paused worker still
  believes it holds it; only the fencing token checked *at the resource* protects it. Leases alone are never enough.
- "Why must tokens survive release and expiry?" — *Listen for:* a reused or reset token lets a stale holder pass the
  guard; monotonic per resource, never reset.
- "How do you wake waiters without a goroutine per lease timer?" — *Listen for:* a condition variable or per-resource
  waiter queue of channels signalled on release; expiry handled lazily on access plus a timer for the earliest
  expiry, or polling the injected clock in tests; trade-off with `time.AfterFunc` per lease.
- "This is now three real processes on three machines. What changes?" — *Listen for:* the lease state needs a
  consensus-backed store (single linearizable owner per resource); clocks differ, so TTLs need safety margins; tokens
  come from that store's revision; it's what etcd/ZooKeeper-style leases do (TODO-VERIFY exact semantics before
  naming a specific product's API).

**Red flags.** Expiry checked with `time.Now()` [1, 4]; `Renew` that extends an already-expired lease [3]; a token
counter that resets on release [3]; `Watch` sending under the lock to an unbuffered channel [3]; tests that sleep
for the TTL [4].

**Draws on.** B0 (context cancellation, `sync.Cond` or channel signalling, leak-free goroutines); B3 `lrucache` (TTL
with injected clock), `scheduler` (timers, heaps), `inventory` (holds and expiry).

---

## 2. Event-sourced account store

**Prompt (verbatim).** "We're moving member account balances to an event-sourced model. Build the in-memory core:
accounts change only by appending events (deposited, withdrawn, held, hold released), and the current balance is
derived from the events. Several request handlers write to the same account at once, and we must never lose an event
or let a balance go negative. I'll add requirements as we go."

**Answer sheet.**
- Money: integer minor units, one currency per account. Overdrafts not allowed: available balance ≥ 0 always.
- Scale: ≈ 100k accounts, most with < 1,000 events; a few business accounts with ≈ 10⁶ events (matters in Stage 2).
- Concurrency: yes, many handlers per account. Writers must not silently overwrite each other.
- Events are immutable once appended; nothing is ever updated in place.
- Out of scope: persistence, currency conversion, interest.
- Likely next requirement: "reading the balance of a busy account fast, and building other views from the events".

| | Requirements |
|---|---|
| **Stage 1** (0:00–0:30) | `Append(accountID, expectedVersion, events...) (newVersion, error)` with **optimistic concurrency**: fails with a conflict error if the account's version isn't `expectedVersion`; events validated against current state (no withdrawal below zero available; hold release only for an existing hold); `Load(accountID) (State, version)` folds events into balance, available, holds; account versions start at 0 |
| **Stage 2** (0:30–0:55) | **Snapshots and projections:** `Load` uses a snapshot taken every N events so it doesn't replay 10⁶ events; snapshots never change the result (`Load` with and without snapshots agree). A `Projection` interface (`Apply(Event)`) registered at construction receives every appended event in global append order; ship one projection: total deposits per day across all accounts. **Idempotent commands:** `Append` takes a command ID; retrying the same command ID returns the original version and appends nothing |
| **Stage 3** (0:55–1:20) | **Contention and transfers:** a `Transfer(from, to, amount, commandID)` that appends to both accounts atomically (both or neither) without deadlock under concurrent opposite-direction transfers; 50 goroutines retrying `Append` on conflict against the same account end with every successful command applied exactly once and the balance equal to the fold of the events; a slow projection doesn't block appends (bounded queue, stated policy when full); `Replay()` rebuilds all projections from the log |

**Acceptance checklist.**
1. `Append` with the wrong expected version returns the conflict error and appends nothing.
2. A withdrawal exceeding available balance is rejected; available = balance − holds.
3. Releasing an unknown hold is rejected; releasing it twice is rejected the second time.
4. `Load` after N appends equals a fold of all events from version 0 (with snapshots enabled at N=10, test 25
   events).
5. Same command ID retried returns the same version and doesn't double-apply; same ID with a different payload
   errors.
6. Projection receives events in append order; daily totals match a recomputation from the log.
7. 50 goroutines × 20 deposits on one account with conflict-retry loops: final balance = sum; version = 1000; `-race`
   clean.
8. 100 concurrent transfers A→B and B→A: no deadlock (test has a timeout), sum of balances conserved.
9. A projection that blocks forever doesn't stop `Append` from returning.
10. `Replay` produces the same projection state as live application.

**Problem probes.**
- "Why optimistic concurrency and not a lock held across load-decide-append?" — *Listen for:* callers decide outside
  the store (business logic), so a lock would be held across user code; version check makes conflicts explicit and
  retryable; flips when contention per account is high (a hot business account) → serialize per account with a
  single writer or queue.
- "How do you pick N for snapshots?" — *Listen for:* trade replay cost (events × fold cost) against snapshot cost and
  memory; ≈ hundreds to thousands; only accounts that need it; snapshots are derived data, safe to drop.
- "A projection crashed halfway. How does it recover?" — *Listen for:* projections track the last applied global
  position; resume from there; projection updates must be idempotent or position-checked.
- "The bank changes the overdraft rule. Do old events need to change?" — *Listen for:* no; events record facts;
  validation applies at append time; the fold must still interpret old events (versioned event types / upcasting).

**Red flags.** Balance stored and mutated alongside events (two sources of truth) [1]; `float64` money [1]; projection
called under the store lock [3]; transfer locking accounts in argument order [3]; snapshot path tested only by
comparing against itself [4].

**Draws on.** B3 `kvtx` (undo logs, atomic multi-key changes), `orderbook` (append-only transitions, invariants,
replay), `splitwise` (integer money, conservation); A2 MVCC intuition.

---

## 3. Webhook delivery queue

**Prompt (verbatim).** "Our platform notifies partner systems by calling their webhook endpoints when things happen
to a member's account. Build the in-memory core of the delivery service: we enqueue a payload for an endpoint, and
the service delivers it with an HTTP-like sender you get as an interface. Endpoints are slow or down sometimes, and
some partners have told us they process events in the order they receive them. I'll add requirements as we go."

**Answer sheet.**
- Scale: ≈ 2,000 endpoints, ≈ 200 events/s overall, heavily skewed (a few endpoints get most traffic).
- Sender: `Send(ctx, endpoint, payload) error`; errors are retryable (timeout, 5xx-like) or permanent (4xx-like). You
  define the error types.
- Delivery semantics: at-least-once; partners dedupe by event ID.
- Concurrency: `Enqueue` from many goroutines; deliveries in parallel across endpoints.
- Time: backoff uses an injected clock and sleeper; no real sleeps in tests.
- Out of scope: real HTTP, persistence, partner registration UI.
- Likely next requirement: "per-endpoint ordering, and what happens when an endpoint is down for an hour".

| | Requirements |
|---|---|
| **Stage 1** (0:00–0:30) | `Enqueue(endpoint, eventID, payload) error`; a worker pool of size W delivers through the `Sender`; retryable errors retried with exponential backoff (base, cap, max attempts) via the injected clock/sleeper; permanent errors and exhausted retries go to a dead-letter list with the last error; `Stats()` (delivered, retried, dead) |
| **Stage 2** (0:30–0:55) | **Per-endpoint ordering:** deliveries to one endpoint happen strictly in enqueue order, one at a time (a failing head-of-line event blocks only *its* endpoint until it succeeds or is dead-lettered); other endpoints proceed. **Signing:** each delivery carries an HMAC-SHA256 signature over timestamp + payload with a per-endpoint secret, via a `Signer` interface; `Verify` helper for tests |
| **Stage 3** (0:55–1:20) | **Isolation and lifecycle:** one endpoint down for an hour must not consume all W workers or grow memory without bound (per-endpoint queue cap with a stated overflow policy); `Pause(endpoint)` / `Resume`; `Close(ctx)` stops intake, finishes in-flight deliveries within the deadline, reports undelivered counts; a `Sender` that panics is contained and counted as a retryable failure; dead letters can be replayed in order |

**Acceptance checklist.**
1. A successful send is delivered once; `Stats` shows it.
2. Retryable error twice then success: delivered, retried = 2, backoff delays follow the configured sequence (fake
   sleeper records them), capped at the max.
3. Permanent error: no retry, dead-lettered with the error.
4. Max attempts exhausted: dead-lettered after exactly that many attempts.
5. Ordering: 100 events to one endpoint whose sender randomly fails retryably are observed in enqueue order.
6. A stuck endpoint (sender blocks until test releases it) doesn't stop deliveries to 10 other endpoints.
7. Signature verifies with the right secret; fails with a wrong secret or modified payload; uses `hmac.Equal`.
8. Per-endpoint cap: the (cap+1)th enqueue to a stuck endpoint follows the stated policy (error or dead-letter).
9. `Close` with in-flight deliveries returns within the deadline; enqueue after close errors; no goroutine leak.
10. A panicking sender doesn't kill the worker; the event is retried.

**Problem probes.**
- "Why not a goroutine per endpoint?" — *Listen for:* 2,000 mostly idle goroutines are cheap (≈ 2 KB stacks), and a
  goroutine per endpoint gives ordering for free; the cost is lifecycle (start/stop, idle reaping) and no global
  concurrency cap; a shared pool with per-endpoint "in-flight" flags caps concurrency. Either defended is fine.
- "A partner's endpoint is down for six hours. What happens to memory and to their events?" — *Listen for:* bounded
  queue per endpoint, spill/dead-letter beyond it, alert on queue age; at ≈ 1 event/s that endpoint accumulates
  ≈ 21,600 events: decide whether that fits.
- "The partner says they received the same event twice. Bug?" — *Listen for:* expected under at-least-once (timeout
  after the partner processed it); event ID for dedupe; documented contract.
- "Why sign timestamp + payload, not just payload?" — *Listen for:* replay protection with a tolerance window;
  key rotation with key IDs.
- "Where's the head-of-line blocking trade-off?" — *Listen for:* ordering costs throughput per endpoint; ordered by
  key (account ID) instead of endpoint would allow parallelism within an endpoint.

**Red flags.** Calling `Send` under the queue lock [3]; ordering implemented by a single global worker [2/6]; backoff
with `time.Sleep` [4]; `==` to compare signatures [5/M2]; unbounded per-endpoint slices [3].

**Draws on.** B3 `resilient` (retry classes, backoff, jitter), `pubsub` (acks, redelivery), `logx` (bounded queues,
shutdown); B0 worker pools, graceful shutdown.

---

## 4. Real-time auction with anti-sniping

**Prompt (verbatim).** "We run timed online auctions. Build the in-memory core: sellers open an auction with a
reserve price and an end time; bidders place bids; when the auction ends, the highest valid bid wins if it meets the
reserve. Bids arrive from many request handlers at once, especially in the last seconds. I'll add requirements as we
go."

**Answer sheet.**
- Money: integer minor units. A bid must exceed the current highest by at least a minimum increment.
- Scale: ≈ 10k concurrent auctions; a popular one gets ≈ 1,000 bids in its final minute.
- Time: injected clock; an auction is open for `[start, end)`; bids at or after `end` are rejected.
- Ties: the earlier accepted bid wins (equal amounts are rejected anyway by the increment rule).
- Out of scope: payment, user accounts, persistence.
- Likely next requirement: "the end time can move".

| | Requirements |
|---|---|
| **Stage 1** (0:00–0:30) | `Open(item, seller, reserve, increment, end) (auctionID, error)`; `Bid(auctionID, bidder, amount) error` with errors for closed, too low, seller bidding on own auction; `Close(auctionID)` (or lazy close when the clock passes `end`) produces a result: winner and price, or "no sale" if below reserve; `Highest(auctionID)` |
| **Stage 2** (0:30–0:55) | **Anti-sniping:** a bid accepted within the last E seconds extends `end` to `bidTime + E` (capped at a maximum total extension). **Proxy bidding:** a bidder can set a maximum; the system bids on their behalf the minimum needed to stay highest, up to the maximum; resolve proxy-vs-proxy correctly (higher max wins at the lower max + increment, capped at the higher max) |
| **Stage 3** (0:55–1:20) | **Contention and observers:** 500 goroutines bidding on one auction in its final seconds: accepted bids form a strictly increasing sequence, the final result is consistent with that sequence, and no bid is accepted after the (possibly extended) end; a `Subscribe(auctionID)` stream of outbid/extended/closed events with a bounded buffer that never blocks bidding; closing many auctions is driven by a sweeper using the injected clock, not a timer per auction |

**Acceptance checklist.**
1. Bid below current + increment rejected with "too low"; seller's own bid rejected.
2. Bid at exactly `end` rejected; at `end-1ns` accepted.
3. Highest bid below reserve → "no sale"; at reserve → sold at that bid.
4. Anti-sniping: a bid at `end-2s` with E=30s moves end to `bidTime+30s`; total extension capped.
5. A bid in the extension window extends again; a bid after the extended end is rejected.
6. Proxy: A max 100, B bids 50 → A highest at 50+increment; B max 120 → B highest at 100+increment.
7. Two proxies with equal max: the earlier one wins.
8. 500 concurrent bidders: accepted amounts strictly increase; winner = last accepted; `-race` clean.
9. A subscriber that never reads doesn't block `Bid`.
10. Sweeper closes all auctions whose end has passed when the fake clock advances; no timer per auction.

**Problem probes.**
- "Where exactly is the critical section for `Bid`?" — *Listen for:* read current highest + end, validate, maybe
  extend, record — all under the auction's lock; a per-auction lock so different auctions don't contend.
- "1,000 bids in the last minute on one auction: is a lock fine?" — *Listen for:* ≈ 17 bids/s against a µs critical
  section is nothing; the real risk is fan-out to subscribers; push events outside the lock.
- "Why is the extension capped?" — *Listen for:* otherwise two bots can extend forever; product rule; the cap bounds
  the sweeper's worst case.
- "The auction must run on two servers for availability. What breaks?" — *Listen for:* bid ordering needs a single
  owner per auction (partition by auction ID) or consensus; clocks differ, so `end` is decided by the owner's clock.

**Red flags.** Global lock across all auctions without a stated reason [3]; extension computed from wall-clock
`time.Now()` [1]; proxy resolution as recursive bidding loops that can spin [3]; subscriber send under lock [3].

**Draws on.** B3 `orderbook` (price-time priority, invariants), `ticketing` (contention), `scheduler` (heap-driven
sweeps); B2 observer.

---

## 5. Metrics aggregator with windowed rollups

**Prompt (verbatim).** "Services in our platform push metric samples: a counter increment or a latency value, tagged
with a service name and an endpoint. Build the in-memory core of an aggregator that answers 'how many requests and
what latency did endpoint X see in the last 5 minutes'. Lots of goroutines push samples concurrently. I'll add
requirements as we go."

**Answer sheet.**
- Scale: ≈ 50k samples/s; ≈ 2,000 distinct (service, endpoint) series.
- Windows: 1-second buckets; queries over the last 1, 5 or 60 minutes; older data can be dropped after 60 minutes.
- Latency: report count, sum, max, and approximate percentiles (Stage 2). Exact percentiles aren't required; say how
  approximate yours are.
- Time: injected clock; samples carry their own timestamp; samples older than the retention or more than 10 s in the
  future are rejected.
- Out of scope: persistence, a query language, network transport.
- Likely next requirement: "percentiles and the busiest endpoints".

| | Requirements |
|---|---|
| **Stage 1** (0:00–0:30) | `Record(series, ts, value)` for latency and `Inc(series, ts, n)` for counters; per-series ring of 1-second buckets (count, sum, max) covering 60 minutes; `Query(series, window) Summary` aggregates the buckets in `(now-window, now]`; late samples within retention go into their bucket; out-of-range samples rejected |
| **Stage 2** (0:30–0:55) | **Percentiles and rollups:** fixed-boundary histogram buckets per second (e.g. log-spaced latency boundaries) so p50/p95/p99 over a window are computed by merging histograms, with the error bound stated; 1-minute rollups computed from seconds so 60-minute queries touch 60 buckets, not 3,600; `TopK(window, k)` series by request count |
| **Stage 3** (0:55–1:20) | **Throughput and memory:** 64 goroutines recording 10⁵ samples each with totals exactly equal to what was recorded (no lost increments); recording doesn't take a global lock on the hot path (state the design: per-series locks, sharding, or per-goroutine buffers merged on read); a series with no samples for 60 minutes is evicted; a cap on the number of series with a stated policy for new series beyond it (cardinality explosion) |

**Acceptance checklist.**
1. Five increments in the current second → `Query(1m)` count 5.
2. A sample at `now-61m` rejected; at `now-59m` accepted and counted in a 60m query but not a 5m query.
3. Window boundary: a sample exactly at `now-5m` is excluded from the 5m window (half-open as stated).
4. Advancing the clock 61 minutes drops old buckets (ring reuse doesn't leak old counts into new seconds).
5. Max is correct across buckets.
6. p99 of 1,000 known samples within the stated relative error of the exact value.
7. 60m query result equals the sum of second-level data (rollups agree).
8. `TopK` returns series ordered by count; ties broken deterministically.
9. 64 goroutines × 10⁵ increments: total exact; `-race` clean.
10. Series cap: the (cap+1)th new series follows the stated policy; existing series unaffected.

**Problem probes.**
- "Why can't you average p99s across buckets or servers?" — *Listen for:* percentiles don't compose; merge histograms
  (or sketches) instead; averaging p99s understates tails.
- "What's your error bound on p99, and what does it cost?" — *Listen for:* bucket-boundary relative error (e.g. ≈ 10%
  with log-spaced buckets at ratio 1.1); more buckets = more memory per series per second; numbers for 2,000 series.
- "How much memory does this use?" — *Listen for:* series × seconds × bucket size, e.g. 2,000 × 3,600 × (≈ 24 B +
  histogram) — and that histograms dominate, hence rollups and coarser retention.
- "A buggy service sends a unique endpoint per request ID. What happens?" — *Listen for:* cardinality explosion;
  the series cap protects the aggregator; reject/aggregate into an "other" series; alert on it.

**Red flags.** A global mutex around the whole map on every `Record` with no plan [3]; a ring that doesn't clear stale
buckets on wrap-around [3]; percentiles computed by sorting all raw samples [1/3]; tests that assume `time.Now()` [4].

**Draws on.** A8 histograms and why percentiles don't average; B3 `leaderboard` (top-k), `ratelimit` (sliding
windows); B0 sharded state.

---

## 6. DAG workflow runner

**Prompt (verbatim).** "Our month-end statement run is a set of tasks with dependencies: fetch balances, compute
interest, render statements, send them, and so on. Build the in-memory core of a workflow runner: given tasks and
their dependencies, run each task after its dependencies succeed, in parallel where possible. Tasks are Go functions
that can fail. I'll add requirements as we go."

**Answer sheet.**
- Scale: ≈ 10–500 tasks per workflow; tasks take ms to minutes.
- Task signature: `func(ctx context.Context) error`; you may change it if you say why.
- Concurrency: run independent tasks in parallel, at most P at a time.
- Failure: a failing task means its dependents don't run; independent branches continue (Stage 1).
- Out of scope: persistence, distributed workers, a scheduler UI.
- Likely next requirement: "retries, and stopping the whole run".

| | Requirements |
|---|---|
| **Stage 1** (0:00–0:30) | `Add(name, fn, deps...) error` rejects unknown deps, duplicates and **cycles** (with the cycle in the error); `Run(ctx) Report` executes in dependency order with at most P concurrent tasks; failed tasks' dependents are marked skipped; the report lists each task's status (succeeded, failed, skipped) and error |
| **Stage 2** (0:30–0:55) | **Retries and outputs:** per-task retry policy (max attempts, backoff via injected sleeper) applied only to errors marked retryable; tasks can publish an output value consumed by dependents (`func(ctx, inputs map[string]any) (any, error)` or a typed equivalent); a per-task timeout derived from the run's context |
| **Stage 3** (0:55–1:20) | **Cancellation and resume:** cancelling the run's `ctx` stops scheduling new tasks, cancels running ones, and `Run` returns within a bounded time with accurate statuses (cancelled vs failed); a "fail-fast" mode where the first failure cancels everything; `Resume(report)` re-runs only tasks that didn't succeed, reusing outputs of succeeded ones; a panicking task is reported as failed, not a crashed runner; never more than P tasks in flight (checked by the test) |

**Acceptance checklist.**
1. A→B→C runs in order; D independent of them may overlap.
2. Cycle A→B→A rejected at `Add` (or at `Run` before any task starts) with both names in the error.
3. Unknown dependency rejected.
4. B fails → C skipped, independent D succeeds; report correct.
5. Never more than P concurrent tasks (tasks increment/decrement a shared atomic and record the max).
6. Retryable error twice then success with max 3 → succeeded, 3 attempts; permanent error → 1 attempt.
7. Output of A visible to B.
8. Cancel mid-run: `Run` returns within the test's deadline; running tasks see `ctx.Done()`; not-started tasks
   marked cancelled.
9. Fail-fast: first failure cancels the rest.
10. `Resume` runs only non-succeeded tasks; succeeded tasks' functions are not called again.
11. A panicking task is reported failed; others complete.

**Problem probes.**
- "How do you detect a cycle and how expensive is it?" — *Listen for:* DFS with colours or Kahn's algorithm; O(V+E)
  at `Add` or once before `Run`; incremental check on `Add` is O(V+E) per add, fine at 500 tasks.
- "How do you schedule without busy-waiting?" — *Listen for:* in-degree counts, a ready channel/queue fed when a
  task's last dependency completes, P workers or a semaphore; one coordinator goroutine owns the counts.
- "A task ignores `ctx`. What happens when you cancel?" — *Listen for:* Go can't kill a goroutine; the runner stops
  waiting after a grace period and reports it as abandoned; the goroutine leaks until it returns; document the task
  contract.
- "Resume after a crash: is it safe to re-run a task that sent half the statements?" — *Listen for:* tasks must be
  idempotent or checkpoint their own progress; the runner can't guarantee exactly-once side effects.

**Red flags.** A goroutine per task that busy-loops checking deps [3]; shared status map written by tasks without a
lock [3]; `Run` that can block forever on a channel when a task panics [3]; retry inside the task function [2].

**Draws on.** B0 `group` (errgroup), `pipeline`, context cancellation; B3 `scheduler`, `resilient` (retry classes).

---

## 7. Write-behind cache in front of a slow store

**Prompt (verbatim).** "Our core-banking vendor's API is slow: writes take ≈ 200 ms and it rate-limits us. Member
preference updates (notification settings, nicknames) come in bursts. Build the in-memory core of a write-behind
cache: reads are served from memory, writes are acknowledged immediately and flushed to the store in the background.
The store is an interface you get. I'll add requirements as we go."

**Answer sheet.**
- Scale: ≈ 200k members, ≈ 500 updates/s in bursts; the store accepts ≈ 50 writes/s, but supports a batch write of up
  to 100 keys per call.
- Consistency: a member reading their own preferences right after writing must see the new value.
- Loss: preferences may lose the last few seconds on a crash (stated product decision), but must never be applied
  out of order (an older value must never overwrite a newer one in the store).
- Time: flush every F (injected clock/ticker) or when B dirty keys accumulate.
- Out of scope: real network, multi-instance cache coherence (until Stage 3 discussion).
- Likely next requirement: "the store fails for a while".

| | Requirements |
|---|---|
| **Stage 1** (0:00–0:30) | `Get(key)`: memory first, else load from the store (read-through) and cache; `Put(key, value)`: update memory, mark dirty, return; `Flush(ctx)` writes all dirty keys to the store and clears their dirty flag only if the value didn't change during the flush; read-your-writes |
| **Stage 2** (0:30–0:55) | **Coalescing and batching:** ten `Put`s to one key between flushes produce one store write with the latest value; flushes use the batch API (≤ 100 keys per call); background flusher triggered by interval or dirty-count threshold; concurrent `Get` misses for the same key cause one store load (singleflight) |
| **Stage 3** (0:55–1:20) | **Failure and lifecycle:** store errors keep keys dirty and retry with backoff, without blocking `Put`/`Get`; a bound on dirty keys with a stated policy when exceeded (block with timeout, or reject); versioned writes so a slow, retried batch can never overwrite a newer value in the store (the store exposes `PutIfNewer(key, value, version)` or you design the check); `Close(ctx)` performs a final flush within the deadline and reports unflushed keys; eviction of clean keys under a memory cap (never evict dirty ones) |

**Acceptance checklist.**
1. `Put` then `Get` returns the new value without touching the store.
2. Miss → one store load; subsequent `Get` from memory.
3. Ten puts to one key, then `Flush` → exactly one store write with the last value.
4. A `Put` during an in-flight flush of the same key leaves it dirty with the newer value; next flush writes it.
5. 250 dirty keys → 3 batch calls of ≤ 100.
6. 50 concurrent `Get` misses on one key → 1 store load.
7. Store failing: `Put` still returns promptly; keys stay dirty; after recovery they're flushed.
8. A delayed retry of an old batch doesn't overwrite a newer value in the store (fake store checks versions).
9. Dirty cap reached → stated policy observed.
10. `Close` flushes; returns within deadline when the store hangs, reporting unflushed keys.
11. Eviction never drops a dirty key.

**Problem probes.**
- "Write-behind vs write-through: when is write-behind wrong?" — *Listen for:* when the write must be durable before
  ack (money, consents); here product accepted seconds of loss; flip to write-through or an outbox for anything
  regulatory.
- "The process crashes with 3,000 dirty keys. What's lost and how would you shrink it?" — *Listen for:* all of them;
  a local append-only log of dirty writes (WAL) or a durable queue; shorter flush interval costs store quota.
- "Two cache instances now serve the same members. What breaks?" — *Listen for:* stale reads across instances and
  conflicting writes; route each member to one instance (consistent hashing) or invalidate via pub/sub; versions in
  the store keep the newest value.
- "How did you avoid clearing a dirty flag for a value written during the flush?" — *Listen for:* per-key version
  captured at flush start; clear only if unchanged (compare-and-clear).

**Red flags.** Store calls under the cache lock [3]; flush that clears all dirty flags after the batch regardless of
interleaved puts [3]; eviction that can drop dirty entries [3]; a real `time.Ticker` in tests [4].

**Draws on.** A3 (cache-aside, write-back, stampedes); B3 `lrucache`, `logx` (batching, shutdown), `resilient`;
B0 singleflight-style coalescing.

---

## 8. Batch payment-file processor

**Prompt (verbatim).** "Credit unions upload payment files: one payment per line, a header and a trailer with a count
and a total. Build the in-memory core of the processor: parse the file, validate it, and post each valid payment to a
ledger interface you get. The ledger is a remote system and calls can fail or time out. The same file sometimes gets
uploaded twice. I'll add requirements as we go."

**Answer sheet.**
- Format (simple, you may adjust it): `H|<fileID>|<tenant>|<date>`, then `P|<lineNo>|<fromAcct>|<toAcct>|<amount
  minor units>|<reference>`, then `T|<count>|<total>`. Pipes as separators, no escaping.
- Scale: files of up to ≈ 100k lines; ≈ 40 tenants.
- Validation: header/trailer present; trailer count and total match the payment lines; amounts positive and fit in
  int64; accounts non-empty; line numbers unique and increasing.
- Ledger: `Post(ctx, Posting) error`, where `Posting` carries an idempotency key you choose; the ledger guarantees
  that the same key posts at most once.
- Policy: a header/trailer error rejects the whole file; a bad payment line rejects only that line (Stage 1).
- Out of scope: real file I/O beyond an `io.Reader`, money movement rules inside the ledger.
- Likely next requirement: "posting in parallel and surviving a crash halfway through".

| | Requirements |
|---|---|
| **Stage 1** (0:00–0:30) | `Process(ctx, r io.Reader) (Report, error)`: streaming parse (don't load the whole file into a slice of strings first if you can avoid it); file-level validation (header, trailer count and total) rejects the file before any posting; line-level errors collected with line numbers; valid lines posted in order; the report lists posted, rejected (with reason) and failed counts plus per-line outcomes |
| **Stage 2** (0:30–0:55) | **Idempotency and duplicates:** the posting idempotency key is derived from tenant + fileID + lineNo, so re-uploading the same file posts nothing twice and the report says "already posted" per line; a file whose fileID was processed with *different* content is rejected (content hash); ledger errors classified retryable vs permanent with bounded retries (injected sleeper) |
| **Stage 3** (0:55–1:20) | **Parallelism and partial failure:** post with at most W concurrent ledger calls while keeping the report in line order; per-tenant fairness when several files are processed concurrently (one tenant's 100k-line file doesn't starve another's 50-line file); `ctx` cancellation mid-file stops new postings and the report says exactly which lines are posted, failed or not attempted; re-processing after a crash halfway (simulated by cancelling) completes the remainder without double posting |

**Acceptance checklist.**
1. A valid 3-line file posts 3 payments in order; report correct.
2. Trailer count mismatch → whole file rejected, zero postings.
3. Trailer total mismatch → whole file rejected.
4. One line with a negative amount → that line rejected with its line number, others posted.
5. Amount overflowing int64 → line rejected, no panic.
6. Same file twice → second run posts nothing; lines reported "already posted".
7. Same fileID, different content → rejected.
8. Ledger fails retryably twice then succeeds → posted once.
9. W=4: never more than 4 concurrent ledger calls; report still in line order.
10. Cancel after ≈ half the lines; re-run → remaining lines posted, total postings = line count exactly.
11. Two tenants' files concurrently: the small file finishes without waiting for the large one to complete.

**Problem probes.**
- "Why derive the idempotency key from the file and line rather than generating a UUID?" — *Listen for:* a random
  key changes on re-upload, so dedupe fails; the key must be a function of the business identity of the payment.
- "The ledger timed out. Did the payment post?" — *Listen for:* unknown; retry with the same key is safe *because*
  the ledger dedupes; never retry with a new key; reconciliation catches the rest.
- "Validate everything first, or post as you go?" — *Listen for:* file-level checks need the trailer, so stream once
  to validate (or buffer parsed postings) before posting; memory trade-off at 100k lines (≈ 100k × ≈ 100 B ≈ 10 MB:
  fine) vs two passes over the reader.
- "A tenant's file has 30% bad lines. Still post the rest?" — *Listen for:* that's a policy decision to raise with
  the business; a threshold that rejects the whole file is common; make it configurable.

**Red flags.** `strconv.ParseFloat` for amounts [1]; postings issued before trailer validation [3]; idempotency key
including a timestamp [3]; unbounded goroutine per line [3]; report order depending on goroutine completion [3].

**Draws on.** B3 `splitwise` (integer money), `inventory` and `ticketing` (idempotency keys), `resilient` (retry
classes); B0 bounded parallelism and ordered fan-in; your own ACH/batch-file work.

---

## 9. Polite fetch scheduler

**Prompt (verbatim).** "We call dozens of partner APIs to refresh data, and each partner allows a limited request
rate. Build the in-memory core of a fetch scheduler: callers submit URLs, the scheduler fetches them through a fetcher
interface, and it never exceeds any host's allowed rate. Some hosts are slow. I'll add requirements as we go."

**Answer sheet.**
- Scale: ≈ 100 hosts, ≈ 10k URLs queued at peak; per-host limits like 2 requests/s with at most 1 in flight.
- Rate: per-host minimum interval between request starts, plus a max-in-flight per host; injected clock.
- Duplicates: the same URL submitted twice while queued or in flight is fetched once; both callers get the result.
- Fetcher: `Fetch(ctx, url) (Result, error)`.
- Out of scope: robots.txt parsing, HTML parsing, persistence.
- Likely next requirement: "priorities, and backing off a host that starts failing".

| | Requirements |
|---|---|
| **Stage 1** (0:00–0:30) | `Submit(url) (<-chan Outcome, error)` (or a callback); URLs grouped by host; per-host minimum interval and max-in-flight enforced; a global worker limit G; duplicate URLs coalesced (one fetch, every submitter gets the outcome); invalid URLs rejected |
| **Stage 2** (0:30–0:55) | **Priorities and fairness:** two priority classes (interactive, background); interactive URLs for a host go before background ones for that host; across hosts, a free worker picks the host whose next allowed start time is earliest among hosts with work (no host starves); per-host limits can be updated at runtime (`SetLimit(host, interval, inFlight)`) |
| **Stage 3** (0:55–1:20) | **Adaptive backoff and lifecycle:** a host returning rate-limit errors (with an optional retry-after) is backed off and its queue paused until then, without blocking other hosts; consecutive failures open a per-host breaker for a cool-down; `Close(ctx)` stops intake, cancels queued work with a "closed" outcome, waits for in-flight fetches within the deadline; no goroutine leak; under 100 goroutines submitting, the per-host rate limit is never exceeded (test records start times via the fake fetcher) |

**Acceptance checklist.**
1. Two URLs on one host with interval 500 ms: second start ≥ first start + 500 ms (fake clock).
2. Max-in-flight 1: never two concurrent fetches on one host.
3. Global limit G respected across hosts.
4. Duplicate URL submitted while in flight → one fetch, both receivers get the same outcome.
5. Interactive before background on the same host.
6. A slow host (fetch blocks) doesn't delay fetches to other hosts.
7. `SetLimit` takes effect for subsequent starts.
8. Rate-limit error with retry-after 10 s → no further fetch to that host until 10 s later; other hosts unaffected.
9. Breaker opens after N consecutive failures; half-open probe after the cool-down.
10. `Close` returns within deadline; queued URLs get "closed"; no leaked goroutines.
11. 100 concurrent submitters × 50 URLs over 5 hosts: per-host start intervals never below the limit; `-race` clean.

**Problem probes.**
- "How do you avoid busy-polling hosts that aren't ready yet?" — *Listen for:* a min-heap of hosts keyed by next
  allowed start; one dispatcher sleeps (injected) until the earliest; wakes on submit; per-host queues.
- "How many goroutines does this use at 10k queued URLs?" — *Listen for:* G workers + one dispatcher, not one per
  URL; queued URLs are data, not goroutines.
- "Why coalesce duplicates instead of just dedupe-dropping them?" — *Listen for:* every submitter needs an answer;
  a waiters list per in-flight URL (singleflight shape).
- "A partner says we exceeded their limit even though our scheduler obeyed it. How?" — *Listen for:* several
  instances of our service each with their own limiter; the limit must be divided or shared (central limiter, quota
  per instance); clock differences; retries counted by the partner.

**Red flags.** A goroutine per queued URL sleeping until its turn [3/K2]; rate checks based on completion time
instead of start time without stating it [3]; holding the scheduler lock across `Fetch` [3]; tests with real sleeps
[4].

**Draws on.** B3 `ratelimit` (token bucket, intervals), `resilient` (retry-after, breaker), `scheduler` (heaps,
timers); B0 worker pools, `fanout`.
