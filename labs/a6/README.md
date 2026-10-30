# A6 labs

Four Go packages, stdlib only, no network. Each stub compiles and returns immediately (zero values or
`errNotImplemented`), so every test fails fast until you implement it. Reference solutions live in `solutions/a6/`; the
`*_test.go` files there are byte-identical to the ones here — don't edit the tests, edit the code. Every stub's package
comment is the specification: read it before you write a line.

Run everything the gate checks with `sd lab A6` (it runs `go test -race` over the core packages and reports
`backpressure` separately). While working on one package, run it directly as shown below. Tests that wait have
deadlines, and the concurrent ones need `-race`; before you call a lab done, also run it a few times:
`go test -race -count=20 ./labs/a6/<pkg>/`.

| Order | Package | Unit | Core/Stretch |
|---|---|---|---|
| 1 | [`tinylog`](tinylog/) | 1 (§2–§4) | CORE |
| 2 | [`outbox`](outbox/) | 2 (§1–§2) | CORE |
| 3 | [`saga`](saga/) | 3 (§1–§2) | CORE |
| 4 | [`backpressure`](backpressure/) | 3 (§3) | STRETCH |

---

## 1. `tinylog` — partitioned log with consumer groups

**Goal.** An in-memory Kafka-shaped log you can reason about in an interview: keyed partitioning, dense per-partition
offsets, retention, consumer groups with committed offsets, stop-the-world rebalancing with generation fencing, and
at-least-once redelivery after a crash before commit.

**Edit** `labs/a6/tinylog/tinylog.go`. Provided: the message and config types, the error values. Implement, in order:

1. `New`, `Partitions`, `PartitionFor`, `Append`, `AppendTo`, `Read`, `StartOffset`, `EndOffset`, `ApplyRetention`
   (empty key → round robin; non-empty key → `Hash(key) % Partitions`; retention by count and by age on the injected
   clock; offsets never change when old messages are truncated).
2. `Log.Group`, `Group.Join`, `Group.Evict`, `Consumer.Close`, `Consumer.Rejoin` (deterministic assignment
   `members[p % len(members)]` over sorted IDs; the generation increases on every membership change; rejoining rewinds
   to the committed offset).
3. `Consumer.Poll` (round robin across assigned partitions, one message at a time; stale → `ErrRebalance`;
   retention jumps counted in `Skipped`), `Consumer.Commit` (stale → `ErrStaleGeneration`, gone → `ErrFenced`,
   unassigned → `ErrNotAssigned`, never backwards), `Committed`, `Lag`, `TotalLag`, `ResetOffset`.

**Run:** `go test -race ./labs/a6/tinylog/` (or `-run 'Append|Read|Retention|PerKey'` for just the log).

**Break it:**
- `go test -tags breakit -run CommitTiming -v ./labs/a6/tinylog/` — commit before processing, after each message, and once
  per batch, with a crash after the 6th of 10 side effects: how many messages are lost and how many repeated?
  Predict first. Then copy `breakit_test.go` to a scratch file (keep the `//go:build breakit` line, rename the test
  functions) and change the crash point — the shared test files must stay identical to the solution's.
- `go test -tags breakit -run PartitionCount -v ./labs/a6/tinylog/` — how many keys change partition when you add one
  (3→4 up to 48→49)?
- `go test -tags breakit -run HotKey -v ./labs/a6/tinylog/` — ten consumers, one hot key: ticks to drain at 0%, 30% and
  60% skew.
- In your `Rejoin`, stop resetting positions to the committed offset and run the normal suite: which tests fail?

Unit 1 §2–§4 has the expected observations.

---

## 2. `outbox` — transactional outbox, relay and idempotent consumer

**Goal.** A toy transactional store with injectable crash points, a relay that publishes from its outbox with
at-least-once semantics (claims, leases, ownership-guarded mark-sent, per-key ordering), and a consumer whose dedupe
record commits atomically with its effect.

**Edit** `labs/a6/outbox/outbox.go`. Provided: types, hook points, `ErrLeaseLost`. Implement, in order:

1. `NewDB`, `DB.Do` (serializable, sees own writes, rollback on error, hook at `PointBeforeCommit` and
   `PointAfterCommit`), `Get`, `Rows`, `Pending`, `Seen`; `Tx.Get/Put/Enqueue/Seen/MarkSeen`.
2. `Tx.Claim`, `Tx.MarkSent`, `Tx.Release`; `NewRelay`, `Relay.RunOnce` (claim → hook → publish → hook → mark → hook).
3. `NewConsumer`, `Consumer.Handle` (handler + dedupe record in one `consume` transaction).

**Run:** `go test -race ./labs/a6/outbox/`. The last test, `TestPipeline_RandomCrashesDuplicatesAndBrokerFailuresNeverLoseOrDoubleApply`,
crashes the whole pipeline at random points for 12 seeds; if it fails, the message says which invariant broke.

**Break it:**
- `go test -tags breakit -run DualWrite -v ./labs/a6/outbox/` — commit-then-publish, publish-then-commit and the outbox
  under a 10% crash rate: lost, phantom and duplicate counts. Predict first.
- `go test -tags breakit -run SplitDedupe -v ./labs/a6/outbox/` — the tag also switches `Consumer.Handle` to the broken
  variant described in the package comment (effect and dedupe record in two transactions). How much does it
  double-credit when every message crashes once between them? Then run the whole suite with the tag
  (`go test -tags breakit ./labs/a6/outbox/`) and explain each failure. Your `Handle` must contain the broken branch
  behind `breakSplitDedupe` for this to work, as the stub's comment says.
- Remove the ownership check from `MarkSent` and run `TestTx_MarkSentRequiresTheCurrentClaim`.

Unit 2 §1–§2 has the expected observations.

---

## 3. `saga` — orchestrator with compensations

**Goal.** A saga orchestrator over steps with compensations: persisted, versioned state; idempotency keys that are
stable across retries and restarts; reverse compensation on failure; per-step timeouts that treat a timeout as an
unknown outcome; retried compensations that end in a resumable `stuck` state; pivot and retriable steps; resumption
after a crash at any point.

**Edit** `labs/a6/saga/saga.go`. Provided: types, statuses, errors, `Key`. Implement, in order:

1. `MemoryStore` (`Load`, `Save` with the version rule and copies), `New` (validation and defaults).
2. `Run`: forward execution and persistence, then compensation in reverse.
3. Timeouts through `Config.NewTimer` (abandon a hung action), `Hook` points, retries with `Backoff`/`Sleep`/`MaxAttempts`,
   `Stuck`, `Resume`, pivot and retriable behaviour, context cancellation, panic recovery.

**Run:** `go test -race ./labs/a6/saga/`. The two crash-sweep tests crash the orchestrator at every hook point in turn;
`-count=20` is worth running.

**Break it:**
- `go test -tags breakit -run NonIdempotent -v ./labs/a6/saga/` — crash at each point of a 3-step run with naive services:
  which crash points double-apply a step? Predict first.
- `go test -tags breakit -run CompensationThatCannot -v ./labs/a6/saga/` — a step times out, the saga rolls back and the
  hung call lands late: net effect with a tombstone, without one, and with a naive refund.
- Delete the rule that compensates the timed-out step too and run `go test -race -run Timeout ./labs/a6/saga/`.

Unit 3 §1–§2 has the expected observations.

---

## 4. `backpressure` — bounded queues and load shedding (STRETCH, not gated)

**Goal.** The toolbox of Unit 3 §3: a bounded queue with block, drop-newest and drop-oldest policies; a priority queue
that sheds low priorities first by admission thresholds; a credit gate (credit-based flow control); and a deterministic
queueing simulator whose time-averaged number in system lets you check Little's law.

**Edit** `labs/a6/backpressure/backpressure.go`: `Queue[T]`, `PriorityQueue[T]`, `CreditGate`, `Simulate`.

**Run:** `go test -race ./labs/a6/backpressure/`

**Break it:**
- `go test -tags breakit -run QueueGrowthVsLatency -v ./labs/a6/backpressure/` — one server at 100 req/s fed at 80–300 req/s
  with and without a bound of 50: max queue, mean and max wait, drops, and whether L = λW holds. Predict the worst wait
  at 110/s and at 300/s first.
- `go test -tags breakit -run WhatDoesTheConsumerGet -v ./labs/a6/backpressure/` — the age of the item the consumer takes
  under an unbounded queue, drop-newest and drop-oldest.

Unit 3 §3 has the expected observations.

---

## Reference

- Solutions: `go test -race ./solutions/a6/...`
- Break-it code is behind the `breakit` build tag, so default runs ignore it: `go vet -tags breakit ./labs/a6/...`
  type-checks it.
