# C11 labs — video streaming

Two Go packages, stdlib only. The stubs compile and return immediately (`errNotImplemented` or zero values), so
every test fails fast until you implement it. Reference solutions live in `solutions/c11/`; the `*_test.go`
files there are byte-identical to the ones here, so edit the code, never the tests.

Run what the gate checks with `sd lab C11` (`go test -race` over the core package; `abr` is reported
separately). Before you call a package done, `go test -race -count=20 ./labs/c11/<pkg>/`.

| Order | Package | Unit | Core/Stretch |
|---|---|---|---|
| 1 | [`transcode`](transcode/) | 2 (§5–§6) | CORE |
| 2 | [`abr`](abr/) | 2 (§7) | STRETCH |

---

## 1. `transcode` — the transcoding pipeline as a dependency graph

**Goal.** The job runner behind "upload once, play anywhere": every segment of a source is transcoded into every
rendition (independent work, run in parallel), each segment is packaged once all its renditions exist, and the
manifest is written once every segment is packaged. A bounded worker pool, retries with backoff, a per-attempt
timeout, panic containment, idempotent resume from a store of finished artifacts (a crashed job restarts and
redoes only what is missing), progress reporting, and cancellation that leaves no goroutine behind.

**Edit** `labs/c11/transcode/transcode.go`. Provided: errors, the types, `Step.Key`, `MemStore`. Implement:

1. `Steps` (the DAG's nodes) and a serial `Run` that respects dependencies and passes inputs in order. Tests:
   `TestSteps_*`, `TestRun_RespectsDependencies*`.
2. The worker pool: `MaxWorkers` as a bound that is actually used. Test: `TestRun_BoundsConcurrency*`.
3. Retries, backoff, timeouts, panics, failure. Tests: `TestRun_Retries*`, `TestRun_FailsTheJob*`,
   `TestRun_PerAttemptTimeout*`, `TestRun_ContainsWorkerPanics`.
4. Resume from the store, cancellation without leaks, progress. Tests: `TestRun_Resumes*`,
   `TestRun_Cancellation*`, `TestRun_ProgressCallback*`.

**Run:** `go test -race ./labs/c11/transcode/`

**Break it** (edit your passing solution, watch which test fails, then revert):

- In the worker goroutine, send the result with a plain `results <- r` instead of a `select` that also watches
  `ctx.Done()`. Run `-run Cancellation`. Expected: `Run did not return within 5 s of cancellation`, the classic
  leak from [B0 Unit 2 §3](../../modules/B0-go-concurrency/unit-2-cancellation.md).
- Ignore `MaxAttempts` (retry forever). Run `-run FailsTheJob`. Expected: the 20 s guard fires; in production the
  job runs until someone notices, and the poison input blocks a worker forever
  ([A6 Unit 2 §3](../../modules/A6-async/unit-2-outbox-consumers.md#3-poison-messages-dlqs-and-replay)).
- Drop the per-attempt timeout. Run `-run PerAttemptTimeout`. Expected: the hung worker never returns, the job
  hangs. Decide what the timeout should be for a 4-second segment at 1080p (hint: measure the p99 and add
  headroom, not "30 minutes to be safe").
- Call `Store.Put` only at the end of `Run` instead of as each step finishes. Run `-run Resumes`. Expected: "only 0
  artifacts persisted before the failure"; a crash loses all the work, and a 2-hour movie starts over.
- Schedule steps in list order without dependency counts. Run `-run RespectsDependencies`. Expected: a package step
  runs with empty inputs, and the manifest references packages that don't exist yet.

[Unit 2 §5–§6](../../modules/C11-video-streaming/unit-2-deep-dives.md#5-the-transcoding-pipeline-as-a-dependency-graph)
has the expected observations and the production mapping (queues per step type, idempotent object keys, the
artifacts table, why segments are the unit of parallelism), plus three more break-it exercises for the DAG and
the pool in §5.

## 2. `abr` — adaptive bitrate selection (STRETCH, not gated)

**Goal.** The client-side algorithm that makes segmented streaming work: pick the highest rendition the measured
throughput affords (with a safety factor), switch down immediately when throughput drops, switch up only when
enough video is buffered, and fall to the lowest rendition when the buffer is about to run dry. Pure functions,
table tests.

**Edit** `labs/c11/abr/abr.go`: `Startup`, `Next`.

**Run:** `go test -race ./labs/c11/abr/`

**Break it:** [Unit 2 §7](../../modules/C11-video-streaming/unit-2-deep-dives.md#7-packaging-the-bitrate-ladder-and-adaptive-bitrate)
has a scratch test (`scratch_test.go`, delete it afterwards) that simulates 20 segments with a buffer model and
counts switches and stall time. Compare a naive policy (`Safety: 1.0` and tiny positive `UpSwitchBufferSec` and
`PanicBufferSec`, since zero means "use the default") with the default policy, on a throughput that oscillates
around one rung's bitrate and on a short dip. Then widen the oscillation past the safety margin and watch the
default policy flap too, which is why production players also smooth their throughput estimate. §7 explains why
a player that flaps between 720p and 1080p looks worse than one that stays at 720p.

---

## Reference

- Solutions: `go test -race ./solutions/c11/...`
- Related earlier labs: [`labs/b0/pipeline`](../b0/pipeline/) and [`labs/b0/group`](../b0/group/) (bounded
  concurrency and leak-free pipelines), [`labs/a6/saga`](../a6/saga/) (resumable multi-step work with persisted
  state), [`labs/c10/chunker`](../c10/chunker/) (the upload side the pipeline consumes).
