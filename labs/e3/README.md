# E3 labs

One Go package, stdlib only, run as **three kinds of process on 127.0.0.1**: a broker, workers and a fake
notification vendor. The contract is [`modules/E3-job-pipeline/spec.md`](../../modules/E3-job-pipeline/spec.md);
the units walk through it milestone by milestone. Reference solutions live in `solutions/e3/`; the `*_test.go` files
there are byte-identical to the ones here — don't edit the tests, edit the code.

**Platform:** Track E labs need macOS or Linux (on Windows, use WSL). The tests send Unix signals (SIGKILL, SIGTERM)
to child processes.

Run everything the gate checks with `sd lab E3` (it runs `go test -race` over the package). While working, run one
milestone at a time as shown below. Before calling the lab done, run `go test -race -count=5 ./labs/e3/jobq/` once:
the tests use real processes, real sockets and real time, with generous bounds, and a flaky pass is a bug worth
finding.

| Order | Package | Units | Core/Stretch |
|---|---|---|---|
| 1 | [`jobq`](jobq/) | 1–3 (milestones M1–M5) | CORE |
| — | [`cmd/jobq`](cmd/jobq/) | — | the binary for running the roles by hand (no tests) |

## How the tests run your code

`harness_test.go` (provided, identical in every Track E lab) makes the test binary double as the service binary:
when it is started with a special environment variable, `TestMain` calls your `Run(ctx, args, …)` instead of the
tests. Each multi-process test starts the roles it needs as child processes, waits for the `READY <host:port>` line,
and then drives them over HTTP, exactly as a producer or an operator would. It can:

- `Kill()` a process (SIGKILL: a crash), `Stop(timeout)` it (SIGTERM: a deploy), `Restart()` it on the same port;
- put a `tProxy` between two processes and add latency, `Partition()` the link (a blackhole) or `Refuse()`
  connections;
- read each process's output, which is printed when a test fails. A data race inside any child fails the test that
  started it, because the children are race-instrumented whenever you run with `-race`.

Children exit by themselves if the test process dies, so an interrupted run never leaves servers behind.

---

## `jobq` — broker, worker and fake vendor

**Goal.** A durable job queue on a checksummed log with leases, nack delays, producer idempotency keys, dead letters
and replay, and a depth bound; a worker pool that classifies vendor outcomes, backs off with jitter, sends a stable
idempotency key and drains on SIGTERM. Then prove all of it under injected failures.

**Provided** (read, don't edit): `jobq.go` (`Run`, role dispatch, flags, the loopback check, `serveOn`, wire types),
`provider.go` (the fake vendor and its `/control` fault injection), the `Jitter`/`Outcome` types and `sleepCtx` in
`backoff.go`.

**Edit:**

| File | What | Milestone |
|---|---|---|
| `frame.go` | `AppendFrame`, `ScanFrames` | M1 |
| `broker.go` | `runBroker`: recovery, enqueue, lease (long-poll), ack, nack, DLQ, replay, stats, depth bound, producer keys | M1–M5 |
| `worker.go` | `runWorker`: status endpoint, pool, lease loop, send, ack/nack, drain | M1–M3, M5 |
| `backoff.go` | `Backoff`, `RetryDelay`, `Classify`, `ParseRetryAfter` | M2 |

The design comments at the top of `broker.go` and `worker.go` list what the tests assume. You may port code from
your own `labs/a7/retry` and `labs/a6` work.

**Run, milestone by milestone:**

```bash
go test -race -run TestM1 ./labs/e3/jobq/   # queue + workers, crash recovery, torn tails
go test -race -run TestM2 ./labs/e3/jobq/   # classification, backoff + jitter, Retry-After, partition
go test -race -run TestM3 ./labs/e3/jobq/   # producer keys, consumer key, kill between effect and ack
go test -race -run TestM4 ./labs/e3/jobq/   # dead letters, poison jobs, replay
go test -race -run TestM5 ./labs/e3/jobq/   # backpressure (429), SIGTERM drain
go test -race ./labs/e3/jobq/               # everything (what sd lab E3 runs)
```

**Run it by hand** (three shells):

```bash
go run ./labs/e3/cmd/jobq provider -addr 127.0.0.1:7400
go run ./labs/e3/cmd/jobq broker -addr 127.0.0.1:7300 -data /tmp/e3-broker -visibility 5s
go run ./labs/e3/cmd/jobq worker -broker http://127.0.0.1:7300 -provider http://127.0.0.1:7400
```

```bash
curl -s -XPOST localhost:7300/queues/default/jobs -H 'Idempotency-Key: transfer-991' -d '{"payload":{"to":"member-4471","class":"transfer-posted"}}'
curl -s localhost:7300/queues/default/stats
curl -s -XPOST localhost:7400/control -d '{"fail_next":3}'
curl -s localhost:7400/stats
```

**Break it:**

- `go test -tags breakit -run BreakIt_RetryStorm -v ./labs/e3/jobq/` — 200 jobs, a vendor that fails the next 400
  sends, arrival histogram per 100 ms with `-jitter none` and `-jitter full`. Predict both histograms first (Unit 2
  §2).
- `go test -tags breakit -run BreakIt_FsyncCost -v ./labs/e3/jobq/` — 300 enqueues with and without `-fsync`.
  Predict the ratio first (Unit 1 §2).
- Mutations (one at a time in your own code; run the suite; name the incident each one is):
  - skip `Truncate` on recovery → `ATornLogTailIsTruncatedOnRecovery`, `ACorruptFrameEndsRecoveryThere`;
  - send the lease token instead of the job id as `Idempotency-Key` → almost every effect-counting test, above all
    `AWorkerKilledBetweenEffectAndAck…`;
  - use the SIGTERM context for the vendor call → `SIGTERMDrainsAWorkerWithoutLosingOrDuplicating` (duplicates),
    `ADrainThatCannotFinish…`;
  - ignore `Retry-After` → `RetryAfterFromTheVendorIsAFloor`;
  - check the depth bound before the producer key → `AProducerRetryIsADuplicateEvenWhenTheQueueIsFull`;
  - ignore not-before in the lease scan → `ANackDelaysRedelivery`, `APoisonJobDoesNotBlockTheQueue`, …;
  - move the producer-key check out of the critical section → `ConcurrentDuplicateEnqueuesCreateOneJob` (run with
    `-count=20`; `-race` stays silent: it's a logic race).
- The full failure run with the predicted-vs-observed table: Unit 3 §4.

The `breakit_test.go` file is behind `//go:build breakit`, so normal runs and `sd lab E3` never compile it;
`go vet -tags breakit ./labs/e3/...` type-checks it.
