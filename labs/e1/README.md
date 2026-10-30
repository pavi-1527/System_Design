# E1 labs

Two Go packages, stdlib only. `shorty` is the project: one binary with two roles (`store`, `api`) that run as separate
processes on 127.0.0.1. `loadgen` is the STRETCH load generator. Reference solutions live in `solutions/e1/`; the
`*_test.go` files there are byte-identical to the ones here — don't edit the tests, edit the code. The contract the
tests check is [spec.md](../../modules/E1-url-shortener-service/spec.md).

**Platform.** The tests start real processes and send them Unix signals (SIGKILL, SIGTERM), so they need macOS or
Linux. On Windows, use WSL. The race detector needs cgo (`go env CGO_ENABLED` prints `1`).

Run everything the gate checks with `sd lab E1` (it runs `go test -race` over `shorty`; `sd lab E1 --all` adds
`loadgen`). While working, run one milestone at a time as below. The whole `shorty` suite takes ≈ 20 s under `-race`.

| Order | Package | Milestone | Unit | Core/Stretch |
|---|---|---|---|---|
| 1 | [`shorty`](shorty/) | M1: the log and the store process | 1 | CORE |
| 2 | [`shorty`](shorty/) | M2: the API and the redirect cache | 2 | CORE |
| 3 | [`shorty`](shorty/) | M3: the rate limiter | 3 §1 | CORE |
| 4 | [`shorty`](shorty/) | M4: failure injection | 3 §2 | CORE |
| 5 | [`loadgen`](loadgen/) | M5: open-loop load and the p99 report | 3 §3 | STRETCH |
| — | [`cmd/shorty`](cmd/shorty/), [`cmd/loadgen`](cmd/loadgen/) | thin `main`s for running by hand | — | — |

**How the multi-process tests work.** `shorty/harness_test.go` (shared by every Track E lab, not part of the
exercise) makes the test binary double as the service binary: started with a special environment variable, it calls
your `Run` instead of running tests. `tStart` launches such a child, waits for its `READY <addr>` line, and gives the
test a handle to `Kill` (SIGKILL), `Stop` (SIGTERM and wait), `Restart` (same port, same data directory) or inspect it.
`tProxy` sits between two processes and injects latency, a blackhole partition or refused connections. Children are
race-instrumented under `go test -race`, and a data race inside one fails the test that started it. A child exits by
itself if the test process dies, so nothing is ever left running. Stubs exit immediately with "not implemented", so
every test fails fast with a message instead of hanging.

---

## 1. `shorty` — the service

**Goal.** A store process that never loses an acknowledged write and recovers from a crash without help, and an API
process (any number of them) with idempotent creates, a redirect cache that keeps load off the store and keeps working
when the store doesn't, a per-client rate limiter, and deadlines on every remote call.

**Files to edit**, in milestone order:

| File | You implement | Provided |
|---|---|---|
| `log.go` | `ParseSyncPolicy`, `encodeRecord`, `OpenLog` (replay + recovery), `append`, `Get`, `Put`, `PutIfAbsent`, `Delete`, `Len`, `Close` | `SyncPolicy`, limits, `ErrCorrupt`, `errLogBroken`, `Recovery`, format constants, `checkKV` |
| `store.go` | `routes` (the store's HTTP API) | `runStore`, `storeServer`, `writeJSON` |
| `client.go` | `StoreClient.Get`, `Put`, `PutIfAbsent` | `ErrStoreUnavailable`, `NewStoreClient` |
| `api.go` | `newAPI`, `create`, `insertNew`, `ensureLink`, `insertIdempotent`, `redirect`, `lookup`, `answer`, `remove`, `statsHandler` | `Link`, `idemRecord`, request/response types, `cached`, `runAPI`, `routes`, `clientID`, `validToken`, `validTarget`, `fingerprint`, `apiError`, `unavailable` |
| `cache.go` | `NewCache`, `Get`, `Set`, `Token`, `SetIfToken`, `Delete`, `Len`, `Group.Do` | `CacheState`, suggested fields |
| `limiter.go` | `NewTokenBucket`, `Allow`, `Len`, `RetryAfterSeconds` | suggested fields |
| `shorty.go` | nothing | `Run`, flags, `checkLoopback`, `serve`, `NewCode`, `validCode` |

You may port your own `labs/a3/lru`, `labs/a3/cacheaside` (`Group`) and `labs/a7/ratelimit` code.

**Run, by milestone:**

```bash
go test -race -run TestM1 ./labs/e1/shorty/
```

```bash
go test -race -run TestM2 ./labs/e1/shorty/
```

```bash
go test -race -run TestM3 ./labs/e1/shorty/
```

```bash
go test -race -run TestM4 ./labs/e1/shorty/
```

Narrower runs while you work: `-run 'TestM1_Log'` (in-process log tests only), `-run 'TestM2_Cache|TestM2_Singleflight'`
(in-process cache tests), `-run TestM4_SlowStore`. Before calling the lab done, run the full package with `-count=5`
once; nothing should flake.

**Run it by hand** (two terminals, then curl): spec §8.

**Break it:**
- `go test -tags breakit -run BreakIt_FsyncCost -v ./labs/e1/shorty/` — writes/s for `-sync always` vs `never` with 1
  and 8 writers (Unit 1 §1). Predict the ratio first.
- `go test -tags breakit -run BreakIt_TakedownWindow -v ./labs/e1/shorty/` — how long a deleted link keeps redirecting
  on a second instance for three cache TTLs (Unit 2 §3).
- `go test -tags breakit -run BreakIt_LimiterIsPerInstance -v ./labs/e1/shorty/` — what "5 per client" admits with 1,
  2 and 3 instances (Unit 3 §1).
- Mutations (one at a time in your own code; run the suite; name the incident each one is):
  - update the index before appending → write the five-line test from Unit 1 §1 Break it;
  - check-then-insert under two separate locks in `PutIfAbsent` → `TestM1_LogPutIfAbsentHasExactlyOneWinner`
    (`-count=20`);
  - truncate at the first bad record anywhere → `TestM1_LogMidFileCorruptionRefusesToOpen`;
  - skip truncation of the torn tail → `TestM1_LogTornTail…` (the file-size check after recovery);
  - write the link before claiming the idempotency key → `TestM2_ConcurrentDuplicatesCreateOneLink`;
  - call `lookup` without `flight.Do` → `TestM2_StampedeOnAColdKeyIsCoalesced`;
  - use `Set` instead of `SetIfToken` in `lookup`, or invalidate before writing the tombstone → reason about it (Unit 2
    §3 Break it explains why a test rarely catches it);
  - truncate `Retry-After` → `TestM3_RetryAfterSecondsRoundsUp`; move the limiter after the store call →
    `TestM3_RateLimitIsCheckedBeforeTheStore`;
  - remove the store-call timeouts and the request budget → `TestM4_SlowStoreFailsFastNotSlow`,
    `TestM4_PartitionedStoreFailsFastAndRecovers`;
  - serve only fresh entries → `TestM4_StaleIfErrorServesExpiredEntriesWhileTheStoreIsDown`.

The `breakit_test.go` file is built only with `-tags breakit`, so normal `go test`, `go vet` and `sd lab E1` ignore
it; `go vet -tags breakit ./labs/e1/...` type-checks it.

---

## 2. `loadgen` — open-loop load and the p99 report (STRETCH)

**Goal.** A load generator that sends on a fixed schedule whether or not earlier requests have finished, measures
latency from each request's *intended* start (so the queue behind a stall counts), and reports p50/p95/p99 with
nearest-rank percentiles for both "latency" and "service time".

**Edit** `labs/e1/loadgen/loadgen.go`: `Schedule`, `Percentile`, `Generate`, `Result.Report`, `HTTPDoer`. Provided:
the types, `round`, and `Run` (the CLI, which refuses non-loopback targets).

**Run:** `go test -race ./labs/e1/loadgen/`

**Use it** against your service (spec §8):

```bash
go run ./labs/e1/cmd/loadgen -url http://127.0.0.1:8081/<code> -rate 1000 -duration 10s
```

**Break it:**
- Measure from the actual send instead of the intended start → `TestM5_OpenLoopCountsTheQueueBehindAStall` fails;
  that one line is coordinated omission.
- Run at a rate your laptop can't sustain with `-max-inflight 1` and read the "achieved" rate.
- `kill -9` the store for two seconds during a run and compare the report with a quiet run.
