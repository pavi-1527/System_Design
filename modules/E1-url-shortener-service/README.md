# E1 — Build: URL shortener service

| | |
|---|---|
| Track | E — Build real systems |
| Estimated time | 18 h (3 weekly units) — CORE 15 h, STRETCH 3 h |
| Prerequisites | A1, A2, A3, A7, B0 (gates passed) |
| Labs | `labs/e1/` — `shorty` (core: the store and api processes, four milestones); `loadgen` (stretch: open-loop load and the p99 report) |
| Gate | quiz ≥ 85% ×2 (different days) · labs pass · design ≥ meets · teach-back pass |

## Why this module exists

You designed a URL shortener in C1 and it held up on the whiteboard: random codes with insert-if-absent, a redirect
cache, 302 not 301, a takedown path. Now run it. Start a store process and two API processes on your laptop and
`kill -9` the store in the middle of a burst of creates. Which of the links you told clients you created still work?
Put a 2-second delay on the store's socket: does a create fail in 300 ms, or does every request hang for 2 seconds and
take a goroutine with it? Delete a phishing link through one API instance and keep clicking it through the other: for
how long does it still redirect? Point fifty concurrent clicks at a link that just went viral on an instance with a
cold cache: how many reads does the store see — one, or fifty?

On a whiteboard all of those answers are "it's handled". In a running system each one is a specific line of code, and
every one of them is wrong the first time. The append-only log you write in Unit 1 loses acknowledged links or refuses
to start after a crash until you get recovery right. The cache from A3 serves a deleted link forever until you close the
race between a fill and an invalidation. The rate limiter from A7 admits twice the configured rate as soon as a second
instance exists. The point of Track E is to find that out yourself, with tests that start real processes and break them
on purpose, and then to explain in writing why each choice was the right one and what it costs.

## Outcomes

- **Write** a design-rationale document for a multi-process service before building it, with per-component failure
  predictions, and **revise** it against what failure injection actually showed. — CORE
- **Implement** an append-only, checksummed log with an in-memory index, an fsync policy and crash recovery that
  truncates a torn tail but refuses to open on mid-file corruption, and **explain** why SIGKILL cannot test fsync. — CORE
- **Expose** the log as a store process with insert-if-absent, and **explain** why that one atomic primitive lets
  several API instances allocate random codes and claim idempotency keys without coordinating. — CORE
- **Implement** an idempotent create with a stored intent record that any retry can roll forward, with the right status
  for replay (201 + `Idempotent-Replayed`), reuse with a different body (422) and concurrent duplicates. — CORE
- **Design** the redirect hot path: 302 with explicit `Cache-Control`, 404 vs 410 with tombstones, an LRU+TTL cache with
  negative entries, singleflight on a detached context, and serve-time expiry checks. — CORE
- **Close** the invalidation race between a delete and an in-flight cache fill, and **state** (and measure) the
  staleness bound for other instances when invalidation is local. — CORE
- **Implement** a per-client token-bucket limiter with bounded memory and truthful `Retry-After`, **place** it before any
  expensive work, and **compute** what a per-instance limit really admits behind a load balancer. — CORE
- **Apply** deadline budgets, stale-if-error and fast 503 + `Retry-After` so the service degrades predictably when the
  store is down, slow or partitioned, and **drain** in-flight requests on SIGTERM. — CORE
- **Inject** failures across processes (SIGKILL, SIGTERM under load, latency, blackhole, refused connections, torn and
  corrupt files) and **turn** each prediction into a deterministic test. — CORE
- **Build** an open-loop load generator that measures from the intended start, **report** p50/p95/p99 with nearest-rank
  percentiles, and **explain** how coordinated omission hides a stall. — STRETCH

## Unit plan

| Unit | Topic | Hours | Core/Stretch | Lab |
|---|---|---|---|---|
| 1 | [The durable store: an append-only log, crash recovery and a store process](unit-1-durable-store.md) | 5.5 | CORE (5, incl. ≈ 1 h design write-up) / STRETCH (0.5) | `labs/e1/shorty` milestone 1 |
| 2 | [The API and the redirect cache: idempotent creates, negative caching, stampedes, invalidation](unit-2-api-and-cache.md) | 5.5 | CORE (5) / STRETCH (0.5) | `labs/e1/shorty` milestone 2 |
| 3 | [Limits, failure injection and the p99 report](unit-3-limits-failure-load.md) | 7 | CORE (5, incl. ≈ 1.5 h gate work) / STRETCH (2) | `labs/e1/shorty` milestones 3–4; `labs/e1/loadgen` (stretch) |

Read [spec.md](spec.md) first: it is the contract (processes, flags, endpoints, status codes, on-disk format, the
failure scenarios with predicted behaviour, and the milestone → test map). The tests in `labs/e1/shorty` are its
executable form.

**Unit 1 (5.5 h)**
- Session 1 (2 h): read [spec.md](spec.md); write version 1 of the [design write-up](design-exercise.md) (≈ 1 h,
  before any code: it is graded partly on predictions you could not have copied from a passing test).
- Session 2 (2 h): §1 the log (predict, then `TestM1_Log…`), §2 recovery (`TestM1_LogTornTail…`,
  `TestM1_LogMidFile…`).
- Session 3 (1 h core + 0.5 h stretch): §3 the store process (`go test -race -run TestM1 ./labs/e1/shorty/` green);
  Break-it: the fsync experiment. STRETCH: group commit in §1.

**Unit 2 (5.5 h)**
- Session 1 (2 h): §1 the API (`TestM2_Create…`, `TestM2_Idempotent…`, `TestM2_ConcurrentDuplicates…`).
- Session 2 (2 h): §2 the redirect cache (`TestM2_Cache…`, `TestM2_Singleflight…`, `TestM2_NegativeCaching…`,
  `TestM2_Stampede…`).
- Session 3 (1 h core + 0.5 h stretch): §3 invalidation across instances (`TestM2_DeleteIs…`) and the takedown-window
  break-it; update the write-up's predictions. STRETCH: the cross-instance invalidation sketch in §3.

**Unit 3 (7 h)**
- Session 1 (1.5 h): §1 the rate limiter (`go test -race -run TestM3 …`); the per-instance break-it.
- Session 2 (2 h): §2 failure injection (`go test -race -run TestM4 …`), then `sd lab E1`; version 2 of the design
  write-up with the predicted-vs-observed table.
- Session 3 (1.5 h, gate work): first `sd quiz E1`; send the write-up to the tutor; the [teach-back](teachback.md)
  (reflection checklist).
- STRETCH (2 h): §3 `labs/e1/loadgen` and a p99 report of your own service for the write-up's appendix.
- Second `sd quiz E1` attempt on a later day.

## How this connects to your banking work

- **Notification links are transactional.** Statement-ready e-mails, device-verification links and loan-application
  resume links are short links with expiry and takedown requirements. The 410 tombstone, the expiry check at serve
  time and the takedown window you measure in Unit 2 §3 are the numbers a compliance reviewer will ask for.
- **Retrying senders.** The notification pipeline that calls `POST /v1/links` retries on timeouts, exactly like the
  vendor callbacks you have deduplicated. Unit 2 §1's intent record is the same idempotency-key design as
  [A1 Unit 3 §2](../A1-networking-api/unit-3-idempotency-auth.md#2-idempotency-keys-end-to-end), now with the failure
  you have actually seen: a crash between "claimed the key" and "did the work".
- **A dependency that is down is normal.** The store going away in Unit 3 §2 is your core-banking adapter's Monday
  morning: a timeout that comes from a budget, a 503 with `Retry-After` instead of a hung thread, and a hot path
  (redirects, balance display) that keeps working on cached data with an honest staleness bound.
- **Per-tenant limits behind a load balancer.** A "100 requests per minute per credit union" limit enforced in each of
  six pods admits ≈ 600. Unit 3 §1 makes you measure it before you promise it to a tenant.
- **Audit and durability.** "We returned 201" is an audit claim. Unit 1's rule — acknowledge only after the record is
  in the log, and refuse to start on corruption rather than silently truncating — is the same rule as for an audit
  trail ([A9 Unit 2 §4](../A9-security-multitenancy/unit-2-data-protection-audit.md#4-audit-trails-and-tamper-evidence)).

## Mastery gate checklist

- [ ] `sd quiz E1` ≥ 85% — attempt 1 (date: ____)
- [ ] `sd quiz E1` ≥ 85% — attempt 2 on a different day (date: ____)
- [ ] `sd lab E1` — all lab tests pass (with `-race`): `labs/e1/shorty`, milestones 1–4, including the
      failure-injection tests
- [ ] Design write-up ([`design-exercise.md`](design-exercise.md)): versions 1 and 2, self-score recorded, tutor score
      ≥ "meets" (`sd record design E1 --self <lvl> --tutor <lvl> --notes "..."`)
- [ ] Teach-back / reflection checklist ([`teachback.md`](teachback.md)): tutor marks pass
      (`sd record teachback E1 --result pass --notes "..."`)

`sd status` is the source of truth; this list is for your own reading. `loadgen` is STRETCH: `sd lab E1 --all`
reports it, and it does not block the gate.

## What's next

Passing E1 doesn't unlock another module by itself (Track E modules are leaves), but it is part of the course
definition of done, and the code is reusable: E3's workers call a dependency with the same budgets, retries and
`Retry-After` handling, and E2 replaces the single store with a replicated one — the first thing you will notice is
that your insert-if-absent is only atomic on one machine. Keep your design write-up; D3 and D4 probe exactly the
failure modes you measured here. Before moving on, run `sd review` and clear the E1 cards, especially tags
`durability`, `caching`, `idempotency` and `rate-limiting`.
