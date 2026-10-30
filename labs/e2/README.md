# E2 labs

Two Go packages, stdlib only, that together are a replicated key-value store running as **real processes on
127.0.0.1**. The tests start your code as child processes, talk to it over HTTP, and inject real failures: `SIGKILL`,
`SIGTERM`, `SIGSTOP`/`SIGCONT`, network partitions and added latency through an in-test TCP proxy, and torn bytes
appended to your WAL. The contract is [modules/E2-replicated-kv/spec.md](../../modules/E2-replicated-kv/spec.md).

Each stub compiles and returns immediately (`errNotImplemented`; a child process that exits before printing `READY`),
so every test fails fast with the child's stderr until you implement it. Reference solutions live in `solutions/e2/`;
the `*_test.go` files there are byte-identical to the ones here: don't edit the tests, edit the code.

**Platform:** the tests use Unix signals (`SIGSTOP`, `SIGTERM`), so they need macOS or Linux. On Windows, use WSL.
Everything binds to loopback only; nothing is reachable from your network.

Run everything the gate checks with `sd lab E2` (`go test -race` over both packages). While working, run one
milestone at a time as shown below. Before calling the lab done, run `go test -race -count=3 ./labs/e2/...` once.

| Order | Package | Unit | Milestones | Core/Stretch |
|---|---|---|---|---|
| 1 | [`wal`](wal/) | 1 (§1) | M1 | CORE |
| 2 | [`rkv`](rkv/) | 1–4 | M1–M5 | CORE |
| — | [`cmd/rkv`](cmd/rkv/) | — | run the store by hand | no tests |

`rkv` imports your `wal` package, so finish `wal` first.

## How the multi-process tests work

`rkv/harness_test.go` (identical in every Track E lab, not part of the exercise) makes the test binary double as the
service binary: with the environment variable `SD_TRACK_E_CHILD_ARGS` set, `TestMain` calls your `Run` instead of the
tests, exactly as `cmd/rkv/main.go` does. `tStart` launches a child, waits for its `READY <host:port>` line, and gives
the test a handle to kill, stop, pause, restart (on the same port) or inspect it. Children are race-instrumented under
`go test -race`, and a data race in any child fails the test. A child exits by itself if the test process dies, so no
server is ever left running. `tProxy` sits between two processes and can delay, blackhole (a partition: connections
stay open, bytes vanish) or refuse connections. `rkv/helpers_test.go` adds node/coordinator starters and
`Pause`/`Resume`.

When a test fails, it prints the last lines of each child's output. Write to stderr freely in your code: it's your
log.

---

## 1. `wal` — write-ahead log with crash recovery

**Goal.** An append-only file of CRC-32C-framed records with contiguous indexes, fsynced before `Append` returns,
recovered on `Open` by keeping the longest valid prefix and durably cutting everything after it.

**Edit** `labs/e2/wal/wal.go`. Provided: `Entry`, `Op`, the errors, `HeaderSize`, `MaxPayload`, `SyncPolicy`,
`Recovery`, `syncDir`. Implement in order: `Encode`, `Decode`, `Open` (with recovery), `LastIndex`, `Size`, `Append`,
`TruncateAfter`, `Close`.

**Run:** `go test -race ./labs/e2/wal/` (8 tests).

**Break it** (Unit 1 §1 has the answers):
- In `Open`, skip a corrupt record instead of stopping: `TestM1_CorruptRecordCutsTheLogThere` fails.
- Remove the fsync after the recovery truncation: no test fails. Write down the power-loss sequence that makes it a
  real bug.
- Write each entry of a batch with its own `Write` call; run `-count=50 -run Concurrent`. Why is it worse even though
  nothing fails?

---

## 2. `rkv` — node and coordinator

**Goal.** `Run(ctx, args, stdout, stderr)` with two roles. `node`: KV server over your WAL; leader or follower; pull
replication with long-poll fetches whose position is the ack; async and semi-sync; lag on `/status`; read-your-writes
with `?min_index=`; promote/follow/lease admin API with epochs. `coord`: renews the leader's lease, fails over after
consecutive failed renewals (after waiting out the latest lease *sent*), promotes the most up-to-date follower at a new
epoch, and demotes anything stale.

**Edit** `labs/e2/rkv/node.go` and `labs/e2/rkv/coord.go`. Provided in `rkv.go`: `Run`, flag parsing, the loopback
check, `serve` (listen, `READY`, graceful shutdown), every wire type, `writeJSON`/`writeErr`. Provided in `node.go`:
the `node` struct with the suggested design in its doc comment, and `routes`. The doc comment on each TODO says exactly
what the tests assume.

**Run, milestone by milestone:**

| Milestone | Unit | Command | Tests |
|---|---|---|---|
| M1 single durable node | 1 | `go test -race -run TestM1 ./labs/e2/rkv/` | 5 |
| M2 replication, async + semi-sync | 2 | `go test -race -run TestM2 ./labs/e2/rkv/` | 5 |
| M3 lag and follower faults | 2 | `go test -race -run TestM3 ./labs/e2/rkv/` | 3 |
| M4 failover and fencing | 3 | `go test -race -run TestM4 ./labs/e2/rkv/` | 5 |
| M5 read-your-writes | 4 | `go test -race -run TestM5 ./labs/e2/rkv/` | 4 |
| all | — | `go test -race ./labs/e2/rkv/` (≈ 20 s on the reference) | 22 |

Suggested order inside `node.go`: `writeMeta`/`readMeta` → `runNode` → `handleWrite` (async) → `handleGet` →
`handleStatus` (M1) → `handleFetch` match path + `replicate` (M2) → semi-sync wait (M2) → follower lag fields (M3) →
`handlePromote`/`handleFollow`/`handleLease` and the lease check, then `coord.go` (M4) → the truncation path of
`handleFetch` (M4) → `min_index` (M5).

**Run it by hand:**

```bash
go run ./labs/e2/cmd/rkv node -id n1 -data /tmp/rkv/n1 -addr 127.0.0.1:7001 -require-lease
go run ./labs/e2/cmd/rkv node -id n2 -data /tmp/rkv/n2 -addr 127.0.0.1:7002 -role follower -leader http://127.0.0.1:7001 -require-lease
go run ./labs/e2/cmd/rkv coord -addr 127.0.0.1:7000 -nodes n1=http://127.0.0.1:7001,n2=http://127.0.0.1:7002
curl -X PUT --data 250 http://127.0.0.1:7001/kv/acct-1
curl http://127.0.0.1:7002/status
```

(one command per terminal; then kill the first one and watch `curl http://127.0.0.1:7000/status`).

**Break it — measurements** (build tag `breakit`; they print tables, compare them with your design write-up):
- `go test -tags breakit -run BreakIt_WriteCostByMode -v ./labs/e2/rkv/` — writes/s and p50/p99 for fsync never vs
  always, async vs semi-sync (Unit 1 §3, Unit 2 §2).
- `go test -tags breakit -run BreakIt_FailoverTimeVsLease -v ./labs/e2/rkv/` — time from leader SIGKILL to the first
  acknowledged write on the new leader, for five lease/fail-after settings (Unit 3 §1).
- `go test -tags breakit -run BreakIt_LagUnderLoad -v ./labs/e2/rkv/` — max lag in records and seconds with 0, 5 and
  20 ms of link delay (Unit 2 §3).

To change a break-it's parameters, copy `breakit_test.go` to a scratch file such as `breakit_scratch_test.go` (keep the
`//go:build breakit` line, rename the functions); the shared test files must stay identical to the solution's.

**Break it — mutations** (one at a time in your own code; each was checked against the reference):
- Coordinator records `lastExpiry` only on a *successful* renewal → `TestM4_PausedZombieLeaderCannotAcceptWritesAfterFailover`
  fails (timing-dependent; use `-count=5`). Incident: split brain from a GC pause.
- Coordinator promotes without waiting for the lease → the zombie test fails.
- Leader skips the lease check → the zombie test and the "returning old leader" subtest fail. Incident: two primaries.
- Semi-sync acks without waiting for a follower → `TestM4_SemiSyncLosesNoAcknowledgedWrite` fails. Incident: RPO > 0
  under a "RPO = 0" promise.
- Leader skips log matching (always serves from `from`) → the "returning old leader is fenced and truncates its
  divergent suffix" subtest fails (after its 15 s timeout). Incident: replicas that disagree forever.
- `applyLocked` before `log.Append` with a failing append → a value that's readable and then un-happens (Unit 1 §2).
