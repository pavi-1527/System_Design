# C3 labs

One Go package, stdlib only, no network, and no real waiting: every test that involves time uses a fake clock
whose `Sleep` moves time forward instantly, so a 2-second clock regression costs microseconds to test. The stub
compiles and returns immediately (zero values or `errNotImplemented`), so every test fails fast until you implement
it. The reference solution lives in `solutions/c3/`; the `*_test.go` files there are byte-identical to the ones
here — don't edit the tests, edit the code.

Run everything the gate checks with `sd lab C3` (it runs `go test -race` over the package and records the result).
While working, run the package directly as shown below. Before calling it done, run
`go test -race -count=10 ./labs/c3/snowflake/` once: the uniqueness tests use real goroutines.

| Order | Package | Unit | Core/Stretch |
|---|---|---|---|
| 1 | [`snowflake`](snowflake/) | 1 (§2, §5–§7) | CORE |

---

## 1. `snowflake` — 64-bit time-ordered ids with no coordinator on the hot path

**Goal.** The component the whole case study is about: an in-process id generator that every service embeds, that
mints millions of ids per second per node with nothing but a mutex and a clock read, and that stays unique when
the three things it depends on misbehave: the **sequence** fills up inside one millisecond, the **clock** moves
backwards, and the **node id** is handed to a new process while the old one is paused. Four pieces:

1. **Layout.** The bit budget: `Validate`, `Nodes`, `PerTick`, `Lifetime`, `NodeRate`, `Compose`, `Decompose`,
   `TickOf`, `TimeOf`. This is §2's estimate as code: a layout is a lifetime, a node count and a per-node rate.
2. **Generator.** `NewGenerator`, `Next`, `Extend`, `Last`, `Node`: one node's minting loop, with the five rules in
   the package doc (sequence overflow waits or runs ahead; small regressions are waited out; large ones are refused;
   a frozen clock is an error; Floor and Limit bound the ticks).
3. **Registry.** `NewRegistry`, `Acquire`, `Renew`, `Release`: node ids as leases with a TTL, a fencing token per
   grant, and a high-water mark per node that the next holder inherits as its floor.
4. **Worker.** `StartWorker`, `Next`, `Renew`, `Stop`: a generator on a leased node id that stops minting before its
   lease can expire, so a paused old holder and its replacement can never mint the same id.

**Edit** `labs/c3/snowflake/snowflake.go`. Provided: the errors, `DefaultEpoch`, `ID.String`, the `Layout`, `Parts`,
`Config`, `Lease` and `WorkerConfig` types, `DefaultLayout`, the `Clock` interface and `SystemClock`. Implement, in
order (each step's tests run alone with `-run`):

1. The layout: `go test -race -run Layout ./labs/c3/snowflake/`
2. `NewGenerator`, `Next`, `Extend`, `Last`, `Node`. Start with rule 1 and the same-tick case
   (`-run 'Generator_(Validates|IDsCarry|StrictlyIncreasing)'`), then overflow (`-run Overflow`), then concurrency
   (`-run 'Concurrent|DistinctNodes'`), then the clock rules (`-run 'Backward|Frozen|RunAhead|Floor|Lifetime'`),
   then `-run SystemClock`.
3. The registry: `go test -race -run Registry ./labs/c3/snowflake/`
4. The worker: `go test -race -run Worker ./labs/c3/snowflake/`
5. Everything: `go test -race ./labs/c3/snowflake/`

**Documented behaviour the tests check:**
- `DefaultLayout()` is 41/10/12 with 1 ms ticks: 1,024 nodes, 4,096 ids per tick, ≈ 69.7 years, 4,096,000 ids/s
  per node. A 39/16/8 layout with 10 ms ticks lasts ≈ 174 years at 25,600 ids/s per node. A 48-bit millisecond
  layout's `Lifetime` saturates near 292 years instead of overflowing.
- `Validate` rejects 64-bit layouts (the sign bit stays 0), zero time bits, a non-positive tick and a zero epoch.
  `Compose(1, 0, 0)` is `1<<22` in the default layout; a field that does not fit is `ErrOverflow`. Numeric order is
  (tick, node, seq) order.
- `Next` on one node is strictly increasing for 50,000 ids; the 4,097th id in one millisecond is `(tick+1, seq 0)`
  after exactly one sleep of at most one tick; 16 goroutines × 4,000 ids with 16 ids per tick never duplicate, and
  each goroutine sees its own ids increase.
- A 3 ms step back with `MaxBackwards` 10 ms is waited out (≈ 3 ms of sleep) and minting resumes in the same tick at
  the next sequence number; a 2 s step back is `ErrClockBackwards` with no sleep and no state change; once the
  clock recovers, ids continue above the last one.
- A frozen clock (sleep returns, time never moves) gives `ErrClockStalled` after at most `MaxBackwards + MaxAhead +
  2 ticks` of requested sleep, never a hang. With `MaxAhead` 5 ms and 4 ids per tick, 24 ids need no sleep (ticks
  k..k+5) and the 25th sleeps exactly 1 ms; a 3 ms step back is borrowed without sleeping.
- `Floor` = k+5 makes the first id `(k+6, 0)` after a 6 ms wait; a floor 1 s ahead is refused. `Limit` = k+2 with 4
  per tick allows 12 ids, then `ErrLeaseExpired`; `Extend` raises a limit but never lowers it or adds one.
- `Acquire` from 64 goroutines hands out 64 distinct nodes and tokens; the 65th is `ErrNoFreeNode`. An expired lease
  (clock at or past its expiry) is re-granted to the next caller with a larger token and the old high-water mark as
  `Floor`; the old holder's `Renew` and `Release` then fail with `ErrLeaseLost`. An expired lease can't be renewed
  even if nobody took it. `Release(l, used)` sets the node's high-water mark to `used`, so the next holder starts
  right away.
- A worker with TTL 10 s and margin 1 s mints at +8.9 s and gets `ErrLeaseExpired` at +9.1 s; a renewal at +5 s moves
  that to +14 s. In the paused-holder scenario (a stops at +8.9 s, b takes over at +15.5 s with a clock 7 s behind)
  b waits for its floor, a's `Next` is `ErrLeaseExpired` and its `Renew` is `ErrLeaseLost`, and no id repeats. With
  `MaxBackwards` 10 ms, b refuses with `ErrClockBackwards` instead.

**Acceptance:** `go test -race ./labs/c3/snowflake/` passes; then `sd lab C3`.

### Break-it exercises (Unit 1 §5–§7)

All five are behind the `breakit` build tag, so normal runs ignore them. Predict the numbers before each run. To
change a parameter, copy `breakit_test.go` to a scratch file such as `breakit_scratch_test.go` (keep the
`//go:build breakit` line, rename the test functions) and edit the copy: the shared test files must stay identical
to the solution's.

1. **The naive generator and a clock step** (§6).
   `go test -tags breakit -run BreakIt_ClockStepBack -v ./labs/c3/snowflake/` mints 30,000 ids over 3 s (10 per ms),
   steps the clock back, and mints 30,000 more, with the textbook generator (`same tick → seq++, else seq = 0`) and
   with yours (`MaxBackwards` 10 ms). Reference run: naive duplicates **20 / 100 / 500 / 20,000** for steps of
   2 ms / 10 ms / 50 ms / 2 s; yours **0** every time, by waiting (1 ms, 9 ms) or by refusing (36 and 1,809
   refusals, a caller retrying every millisecond). Notice that the naive one's damage is "rate × step": a quiet
   service survives the same step that corrupts a busy one.
2. **Two processes, one node id** (§7).
   `go test -tags breakit -run BreakIt_SharedNodeID -v ./labs/c3/snowflake/` runs two correct generators that were
   given the same node id (a copy-pasted config, a hostname hash that collided), each serving a Poisson stream.
   Reference run: **565, 5,275, 44,785 and 237,785 duplicates per million ids** at 1, 10, 100 and 1,000 ids/s per
   process. At 1 id/s the bug is one id in ≈ 1,800: rare enough to pass every test, common enough to corrupt a
   ledger by lunchtime.
3. **Layouts** (§2). `go test -tags breakit -run BreakIt_Layouts -v ./labs/c3/snowflake/` prints lifetime, end year,
   node count, per-node and fleet rate for six layouts, computed with your methods. Before running, predict the end
   year of 41 bits of milliseconds from a 1970 epoch (reference: **2039**) and from a 2026 epoch (**2095**), and
   which column moving one bit from sequence to node changes (only the split, never the fleet total).
4. **Sharding by `id % N`** (§5). `go test -tags breakit -run BreakIt_ShardByModulo -v ./labs/c3/snowflake/` mints
   10,000 ids from one node at one id every 3 ms and shards them three ways. Reference run: `id % 16` puts **all
   10,000 on shard 0**; `id % 10` uses only the five even shards (2,000 each); `fnv(id) % 16` gives ≈ 625 per shard.
   The low bits of a lightly loaded generator are the sequence, which is almost always 0.
5. **The paused holder** (§7). `go test -tags breakit -run BreakIt_PausedHolder -v ./labs/c3/snowflake/` replays two
   takeovers with plain generators and with your workers. Reference run: a zombie old holder minting beside its
   replacement duplicates **1,000 of 2,000** ids (every pair), against **0** with workers (the old holder's 1,000
   attempts are refused by its deadline); a replacement whose clock is 7 s behind duplicates **400 of 1,000** of
   its ids, against **0** with workers (it waits ≈ 501 ms for its floor).
6. **Mutations** (one at a time, in your own code; predict which test fails and what the production symptom is):
   - Remove the mutex from `Next` and run `go test -race -run Concurrent ./labs/c3/snowflake/`: the race detector
     reports the data race, and without `-race` the duplicate count is non-zero on most runs.
   - Wrap the sequence (`seq = (seq + 1) & mask`) instead of waiting: `TestGenerator_SequenceOverflowWaitsForTheNextTick`.
   - Release the mutex while sleeping, and after re-acquiring it use the `(want, seq)` you computed before the sleep
     instead of going round the loop again: `TestGenerator_ConcurrentCallersNeverDuplicate` and
     `TestGenerator_DistinctNodesNeverCollide` (several callers waited for the same next tick and all hand out
     `(want, 0)`). Releasing the lock during the sleep is fine on its own; acting on a decision made before it is not.
   - Treat any tick other than `last` as a new tick (the naive `!=`): `TestGenerator_SmallBackwardStepIsWaitedOut`.
   - In `Acquire`, keep the old token on a re-grant: `TestRegistry_ExpiredLeaseIsReassignedWithItsHighWaterAsTheFloor`
     (the old holder's renewal now succeeds and steals the node back).
   - In `StartWorker`, read the clock *after* `Acquire` instead of before: no test can see a zero-latency fake
     registry, so explain instead what a 2 s `Acquire` round trip does to the worker's limit, and why A5 Unit 3 §3
     measures leases from the send time.

Vet both build configurations: `go vet ./labs/c3/... && go vet -tags breakit ./labs/c3/...`.
