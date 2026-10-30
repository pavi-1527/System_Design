# A4 labs

Three Go packages, stdlib only, no network and no sleeps in the core logic (`replkv` runs on a deterministic, tick-driven
network simulator; `ring` and `hotkey` are pure data structures). Each stub compiles and returns immediately (zero values or
`errNotImplemented`), so every test fails fast until you implement it. Reference solutions live in `solutions/a4/`; the
`*_test.go` files there are byte-identical to the ones here: don't edit the tests, edit the code.

Run everything the gate checks with `sd lab A4` (it runs `go test -race` over the core packages and reports `hotkey`
separately). While working on one package, run it directly as shown below.

| Order | Package | Unit | Core/Stretch |
|---|---|---|---|
| 1 | [`replkv`](replkv/) | 1 (§1–§3) | CORE |
| 2 | [`ring`](ring/) | 2 (§6) | CORE |
| 3 | [`hotkey`](hotkey/) | 3 (§8) | STRETCH |

---

## 1. `replkv`: replicated KV with a lag simulator

**Goal.** A single-leader replicated key-value store you can reason about: a replication log with LSNs, followers that
apply entries strictly in order (buffering gaps, ignoring duplicates), synchronous and asynchronous commit, client
sessions that provide read-your-writes and monotonic reads with LSN tokens, and failover that promotes the most
up-to-date follower and reports the writes it lost. The network is a simulator: lag is a number of ticks, partitions hold
messages, a later message can overtake an earlier one, and nothing moves until `Tick`.

**Edit** `labs/a4/replkv/replkv.go`. Provided: `Entry`, the error values, `Options`, `node` helpers (`newNode`, `applyOne`,
`truncate`, `commonPrefix`), `NewCluster`, the read-only accessors (`Leader`, `Followers`, `Applied`, `Log`, `Snapshot`,
`Network`), `Flush`, the `Put`/`Delete`/`PutVia` wrappers, `Get`, `Tick` and `NewSession`; and all of `sim.go`
(the `Network`). Implement, in this order:

1. `(*node).apply`: the follower rule (in order, ignore duplicates, buffer the future, drain the buffer).
2. `Cluster.write` (asynchronous part), `Cluster.tickLocked`, `Cluster.GetAtLeast`.
3. `Cluster.write` (synchronous part: `SyncAcks`, `SyncTimeoutTicks`, `ErrSyncTimeout` with the LSN still returned).
4. `Session.Put`, `Session.Token`, `Session.Get` (floors, `Pick`, fallbacks).
5. `Cluster.Crash`, `Recover`, `catchUpLocked`, `Failover` (promote, drop in-flight messages and buffers, fence, catch up, report lost writes).

**Run:** `go test -race ./labs/a4/replkv/` (or `-run 'NodeApply|Put_|Replication|Lag|Partition'` for part 1–2, `-run Sync` for
part 3, `-run Session` for part 4, `-run 'Failover|Recover'` for part 5).

**Break it:**
- `go test -tags breakit -run BreakIt_ReadAnomalyRates -v ./labs/a4/replkv/`: how often a member misses their own write or
  sees time go backwards under each session mode, and what share of reads the leader serves. Predict first.
- `go test -tags breakit -run BreakIt_SyncVersusAsync -v ./labs/a4/replkv/`: acknowledged writes lost on failover and write wait
  time for `SyncAcks` = 0/1/2, then the same with one follower partitioned. Copy `breakit_test.go` to a scratch file such as
  `breakit_scratch_test.go` (keep the `//go:build breakit` line, rename the test functions) to change delays; the shared test
  files must stay identical to the solution's.
- Remove the `pending` buffer from `apply` (apply any future entry immediately) and watch
  `TestLag_LaterEntryOvertakesEarlierOneButIsNotAppliedFirst` and `TestFailover_DiscardsBufferedOutOfOrderEntries` fail.
- Skip `DropAll` in `Failover` and watch `TestFailover_PromotesTheMostUpToDateFollowerAndReportsLostWrites` fail.
- Remove the `old.down = true` fence and run `TestFailover_FencesAnOldLeaderThatIsStillAlive`.

Unit 1 §1–§3 has the expected observations.

---

## 2. `ring`: consistent-hash ring with virtual nodes

**Goal.** The placement function behind Dynamo-style stores and distributed caches: a ring with weighted virtual nodes,
successor lookup, an N-replica walk over distinct physical nodes, statistics for key movement and load balance, a
bounded-load assignment, and the two alternatives (rendezvous and jump hash).

**Edit** `labs/a4/ring/ring.go`. Provided: `Hash64`, `VNodeLabel`, `ModOwner` (the modulo baseline), `Config`, `New`, the error
values and `Stats`. Implement:

1. `Add`, `Remove`, `Lookup`, `Nodes`, `Len`, `Clone` (virtual node `i` at `Hash(VNodeLabel(node, i))`, points sorted by
   position then node name, successor with wrap-around, safe for concurrent use).
2. `LookupN` (distinct physical nodes, clockwise).
3. `Distribution`, `MovedFraction`, `Summarize`.
4. `AssignBounded` (capacity `ceil((1+ε) × keys / nodes)`, spill clockwise).
5. `Rendezvous` and `JumpHash`.

**Run:** `go test -race ./labs/a4/ring/`

**Break it:**
- `go test -tags breakit -run BreakIt -v ./labs/a4/ring/`: four tables: modulo vs ring key movement, vnodes vs skew, who donates
  keys when a node joins, and bounded loads. Predict first.
- In your `Add`, include the current node count in the label you hash (so every point moves when a node joins) and run
  `go test -race -run TestAdd_KeysOnlyMoveToTheNewNode ./labs/a4/ring/`.
- Break the tie-break (sort by position only) and run `TestRing_PositionTieIsBrokenByNodeNameNotInsertionOrder`.
- Drop the lock around `Lookup` and run `go test -race -run Concurrent ./labs/a4/ring/`.

Unit 2 §6 has the expected observations.

---

## 3. `hotkey`: hot-key detection and key splitting (STRETCH, not gated)

**Goal.** Find the heavy hitters of a stream in fixed memory and spread them: a Space-Saving summary, a Count-Min sketch, a
windowed `Detector` that judges hotness by the guaranteed count (`Count − Err`), and a `Router` that splits hot keys on write
and fans in on read.

**Edit** `labs/a4/hotkey/hotkey.go`. Provided: `Item`, `counter` and `counterHeap`, the hash helpers, `PartitionOf`, `SplitKey`, the
constructors and defaults. Implement `SpaceSaving` (`ObserveN`, `Estimate`, `Top`, `Total`, `Reset`), `CountMin` (`Add`, `Estimate`,
`Total`), `Detector` (`Observe`, `IsHot`, `HotKeys`, `Rotate`) and `Router` (`WriteKey`, `ReadKeys`).

**Run:** `go test -race ./labs/a4/hotkey/`

**Break it:**
- `go test -tags breakit -run BreakIt -v ./labs/a4/hotkey/`: fanout vs busiest-partition share and read cost; summary size vs
  top-10 recall. Predict first.
- In `Detector.hotInWindow` use `Count` instead of `Count − Err` and run `TestDetector_UsesGuaranteedCountSoCrowdedSummariesDoNotInventHotKeys`.
- Evict the largest counter instead of the smallest in `ObserveN` and run the Space-Saving tests.
- Make every Count-Min row use the same column and run `TestCountMin_DepthMattersAndAddAccumulates`.

Unit 3 §8 has the expected observations.

---

## Reference

- Solutions: `go test -race ./solutions/a4/...`
- Break-it code is behind the `breakit` build tag, so default runs ignore it: `go vet -tags breakit ./labs/a4/...`
  type-checks it. The break-it files are identical in `labs/` and `solutions/`, and use the helpers in the regular test
  files, so they run against whichever package you point them at.
