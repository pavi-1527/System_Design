# Labs — B3 machine-coding problem bank (problems 1–18)

Eighteen lab packages, one per problem (sixteen core, `leaderboard` and `hashcache` stretch). Each has a stub here (compiles, returns zero values or `errNotImplemented`,
never panics or blocks) and a reference solution under `solutions/b3/<pkg>/`. Test files are byte-identical between
the two, so passing the tests here means you match the reference behaviour. Don't read the solutions until you have
done the timed attempt (and written down what you got stuck on).

```sh
sd lab B3                              # runs: go test -race ./labs/b3/...   (what the gate checks)
go test -race ./labs/b3/<pkg>/         # one problem
go test -race -run Stage1 ./labs/b3/<pkg>/    # one stage
go test -race -count=20 ./labs/b3/<pkg>/      # shake out schedule-dependent bugs
```

Requirements: Go 1.22+ (tested with 1.24) and a working race detector (cgo enabled and a C compiler on Linux;
`go env CGO_ENABLED` should print `1`).

## How the labs model "requirements that grow"

Every package has **three stages**, mirroring a real round where the interviewer adds requirements at minutes 20–30
and 40–55:

| File | What it holds |
|---|---|
| `<pkg>.go` | Package doc with the stage 1 contract, and the stage 1 API stub |
| `stage2.go` | Stage 2 API stubs (**sealed**: open when the interviewer "announces" stage 2) |
| `stage3.go` | Stage 3 API stubs (**sealed** until stage 3) |
| `<pkg>_test.go`, `stage2_test.go`, `stage3_test.go` | Tests named `TestStage1_…`, `TestStage2_…`, `TestStage3_…`, plus shared helpers (fake clocks, timeout guards) |

The later stages are additive on purpose: your stage 1 design has to absorb them without rewriting its public API.
That is the extensibility skill the rubric scores, and a stage that forces you to rewrite stage 1 tells you what the
design missed. Test files are one package, so the whole package compiles from the start; run `-run Stage1` while
you are in stage 1.

Rules every test file follows, so you can trust a failure:

- Anything that can block has a timeout guard (5 s), so a wrong implementation fails with a message instead of
  hanging the run.
- Time is injected (a fake clock or ticker): no test sleeps through a real interval.
- Concurrency tests use many goroutines and check invariants, so `-race` and the invariant checks both have something
  to catch.
- A *scale test* fails an accidentally O(n) or O(n²) design on a time cap where complexity is the point.

## Suggested order

| # | Package | Unit § | Time box | Goal |
|---|---|---|---|---|
| 1 | `lrucache` | U1 §1 | 60 min | O(1) LRU, TTL with a fake clock, expiry heap, callbacks outside the lock, stats |
| 2 | `ratelimit` | U1 §2 | 60 min | Token bucket and sliding window behind one interface, per-key state, bounded memory |
| 3 | `parkinglot` | U2 §1 | 75 min | Modelling, pluggable pricing and allocation, no double allocation under contention |
| 4 | `kvtx` | U2 §2 | 75 min | Nested transactions via an undo log, `Count` index, concurrent sessions |
| 5 | `pubsub` | U3 §1 | 90 min | Bounded subscribers, acks, redelivery, dead letters, consumer groups, graceful close |
| 6 | `scheduler` | U3 §2 | 90 min | Heap + one dispatcher, recurring jobs, cancellation, shutdown |
| 7 | `splitwise` | U4 §1 | 75 min | Integer money, split strategies, debt simplification, ledger invariants |
| 8 | `elevator` | U4 §2 | 90 min | Deterministic state machine, dispatch strategies, concurrent controller |
| 9 | `ticketing` | U4 §3 | 90 min | All-or-nothing seat holds, expiry, contention without deadlock, idempotent confirm |
| 10 | `memfs` | U5 §1 | 75 min | In-memory file system in three stages: paths/dirs/files → Stat, Remove, Rename, Walk → quota, Usage, atomicity under concurrency |
| 11 | `logx` | U5 §2 | 60 min | Structured logging library in three stages: Logger + decorators (`LevelFilter`, `Multi`, `WriterSink`) → `AsyncSink` over a `BatchSink` (bounded queue, size-or-linger batching, Block/DropNewest/DropOldest, Flush, Close, Stats) → retries, panic containment, a bounded `Close` and the conservation invariant |
| 12 | `vending` | U6 §1 | 60 min | Vending machine with the State pattern: coins and exact change from a finite stock → maintenance mode, idle timeout, notices → failing hardware, card payments, concurrency |
| 13 | `orderbook` | U6 §2 | 90 min | Limit order book and matching engine: price-time priority, partial fills → market/IOC/FOK/post-only, amend, self-trade prevention → multi-symbol Exchange, trade streams, executable invariants |
| 14 | `inventory` | U7 §1 | 75 min | Quantity stock with reservations: Reserve/Commit/Release with lazy expiry → all-or-nothing carts, idempotency keys, Extend, Restock → library Borrow/Return, member limits, FIFO waitlist, Prune; hammered by 1,000 goroutines |
| 15 | `resilient` | U7 §2 | 60 min | Resilient HTTP client as `http.RoundTripper` decorators in three stages: safe retry with backoff and `Retry-After` → circuit breaker (half-open probes, per-host) → retry budget, per-attempt timeouts, bulkhead, stats |
| 16 | `shortener` | U7 §3 | 60 min | In-process URL shortener behind three small interfaces (Store, IDGenerator, Encoder): shorten/resolve + validation → aliases, TTL, stats, delete, random IDs with collision retry → concurrency guarantees and a ShardedStore that passes the shared contract suite |
| 17 | `leaderboard` | U8 §1 | 60 min | STRETCH. Leaderboard with top-k, rank and neighbours: correct-first → O(log n) order-statistic structure → consistent snapshots, then sharded and windowed boards |
| 18 | `hashcache` | U8 §2 | 75 min | STRETCH. Consistent-hashing cache client in three stages: ring with virtual nodes and replicas → client over backends with replication, quorum and health → live membership change with lazy migration, plus bounded-load placement |

STRETCH packages (`leaderboard`, `hashcache`) never gate; `sd lab B3 --all` runs them too.

## How to run a timed attempt

1. Read the problem statement in the unit file, answer the Predict blocks on paper, then **stop reading**.
2. Start a timer for the problem's time box. Open only `<pkg>.go` and `<pkg>_test.go`.
3. Work to stage 1 green; at the announced minute open `stage2.go`, and so on. Move on at each checkpoint even if a
   test is red.
4. At the box, stop, run `go test -race -count=5 ./labs/b3/<pkg>/`, and write down the stage you reached and which
   tests were red.
5. Read the debrief, score yourself on the rubric, then refactor and finish the rest. `sd lab B3` records the gate
   run.

## Break-it exercises

Each problem's *Break it* section lists mutations to apply to your solution (or to the reference in `solutions/b3`),
with the failing tests to predict first. They are plain edits: no build tags needed.

## Lab notes for problems 10–18

**memfs.** The tests are grouped by stage (`TestStage1_*`, `TestStage2_*`, `TestStage3_*`), so you can run one stage at
a time and open `stage2.go` / `stage3.go` only when you get there. `Walk` and `Rename` carry the design decisions:
the tests call back into the file system from a `Walk` callback (a lock held across the callback deadlocks, and the
test reports it after 5 s), compare a string-prefix sibling (`/a` → `/ab`), and watch `Rename` from four concurrent
`Walk` observers (a non-atomic rename or a non-snapshot `Walk` shows a file twice or not at all).
Run the whole thing several times before you call it done: `go test -race -count=20 ./labs/b3/memfs/`.

Break-it: mutate your own solution (see unit 5 §1) — string-prefix "inside" check, `Walk` holding the read lock while
calling `fn`, no defensive copy in `WriteFile`, a gap between the quota check and the write, no lock in `ReadFile`.

**logx.** The tests are grouped by stage (`TestStage1_*`, `TestStage2_*`, `TestStage3_*`); open `stage2.go` and
`stage3.go` only when you reach them. Stage 2 and 3 tests inject a linger-timer factory (`AsyncConfig.NewTimer`) and a
retry sleeper (`FailurePolicy.Sleep`), so nothing waits for real time, and the destination is a fake that can block until
released, fail, or panic. `TestStage2_AStaleLingerTimerDoesNotFlushTheNextBatchEarly` fires a timer that belongs to a
batch that is already gone. `TestStage3_ManyLoggersConserveEveryEntry` runs 16 writers through `With` children into an
overflowing queue with a concurrent `Flush` loop and checks `Received == Written + Dropped + Failed`, per-writer order and
a goroutine-leak baseline. One caution: an unrecovered panic in a goroutine you start kills the whole test binary, so if a
run ends with `panic: boom` and no test names, the panic escaped your worker (that is Stage 3's point).
Run the whole thing several times before you call it done: `go test -race -count=20 ./labs/b3/logx/`.

Break-it: mutate your own solution (see unit 5 §2): `With` appending into the parent's slice, storing the caller's
variadic slice, reusing one batch buffer, leaving the linger timer running, not waking `Block` writers in `Close`, no
`recover` around the destination, not cancelling the retry-sleep context, writing a line in two `Write` calls.

**vending.** Tests are grouped by stage (`TestStage1_*`, `TestStage2_*`, `TestStage3_*`); open `stage2.go` / `stage3.go`
only when you get there. The design decisions the tests probe: change must be exact and come from the change box
*plus* the customer's inserted coins (one test compares `Select` with a brute-force search over 400 random pools, and
one hand-built case defeats a greedy algorithm); a failed `Select` leaves the session untouched; every wrong-state
event is `ErrInvalidState`, and Stage 3 enumerates all 5 states x 8 events; the machine must never hold its lock
while the `Dispenser` or `Gateway` runs, so a gated fake dispenser checks that other events fail fast instead of
blocking; card callbacks are idempotent per sale (20 concurrent duplicates must dispense once and capture once; a late
approval of an expired sale is voided once); and `Select` racing `Cancel` is repeated 300 times (exactly one wins).
Listeners are called after unlocking, in order, with panics contained. Run it several times before you call it done:
`go test -race -count=20 ./labs/b3/vending/`.

Break-it: mutate your own solution (see unit 6 §1) — greedy change, the lock held across the dispenser, `Cancel`
accepted in `Dispensing`, duplicate approvals not recognised, listeners called under the machine lock.

**orderbook.** The tests are grouped by stage (`TestStage1_*`, `TestStage2_*`, `TestStage3_*`); open `stage2.go` and
`stage3.go` only when you get there. Prices and quantities are `int64`, and time priority is a logical sequence number
your book assigns (no `time.Now`). `Book` is a single-threaded core with no locks; `Exchange` (Stage 3) adds one
mutex per symbol. Stage 3 asks you to write `CheckInvariants`, and the tests call it after every operation of a
randomised workload that is also checked, trade by trade and level by level, against a naive reference matcher
that lives in `stage3_test.go` (read it after your attempt: it is the "oracle" pattern from the unit). A stalled
subscriber test (5,000 trades, buffer of 4) and a replay test (same inputs, three runs, identical trades) cover the
policies the unit asks you to name. If a concurrent test fails with a data race, read the two stacks: they name the
unlocked access. Run `go test -race -count=10 ./labs/b3/orderbook/` before you call it done.

Break-it: mutate your own solution (see unit 6 §2) — execute trades at the taker's limit, make FOK match before
checking feasibility, re-queue an order on a quantity reduction, stop decrementing the level aggregate, drop the
`Exchange` mutex, and block on a full subscriber channel.

**inventory.** The tests are grouped by stage (`TestStage1_*` 12, `TestStage2_*` 16, `TestStage3_*` 18), so you can
run one stage at a time and open `stage2.go` / `stage3.go` only when you get there. Time is injected (`New(clock)`)
and every expiry test moves a fake clock, so there are no sleeps. The invariant *Total = Committed + Reserved +
Available, Available ≥ 0* is asserted by observer goroutines while 1,000 goroutines hammer the inventory, so a
check-then-act bug shows up as an "impossible Stock" message even though `-race` stays silent. There is no `Close`:
`TestStage1_NoBackgroundGoroutines` fails any design that starts a janitor goroutine. Opposite-order carts and the
waitlist tests run under a 5 s `within` guard, so a deadlock is reported instead of hanging. One design decision is
left to you and is not tested: one mutex or per-SKU locks with ordered acquisition. Write your choice and what
profile would change your mind in the doc comment, and run
`go test -run '^$' -bench . -cpu 1,4,8 ./labs/b3/inventory/` to put a number next to it.

Run the whole thing several times before you call it done: `go test -race -count=20 ./labs/b3/inventory/`.

Break-it: mutate your own solution (see unit 7 §1) — release the lock between the stock check and the hold,
apply cart lines one by one without rollback, flip the expiry boundary, drop `heap.Fix` from `Extend`, ignore the
idempotency key, drop `serve` from `Return`, remove the "refuse while others are waiting" check.

**resilient.** No test touches the network: the "vendor" is a scripted `http.RoundTripper`, and the sleeper, jitter source
and clock are injected, so the suite takes about a second and is stable under `-count=20`. Stage 1 is the largest
(21 tests); if Retry-After is still red at minute 25, park it and come back. `Chain` is given. Watch three
design decisions that the tests probe from different angles: the order of the gates in your retry loop (a budget
token must only be spent on a retry that will happen), the lifetime of the per-attempt context (it must outlive
`RoundTrip` until the body is closed), and what the breaker does with results from requests admitted before the
state last changed. `TestStage3_ConcurrentChainHonoursEveryLimit` pushes 480 requests through
Retry → Breaker → Bulkhead from 16 goroutines; run it with `go test -race -count=20 ./labs/b3/resilient/` before
calling the lab done.

Break-it: mutate your own solution (see unit 7 §2): retry unkeyed `POST`s, shift-based backoff, `defer cancel()` after
`RoundTrip`, no generation check in the breaker, release the bulkhead slot before the body is closed, withdraw a
budget token too early, retry `ErrCircuitOpen`, and hold a lock across `next.RoundTrip`.

**shortener.** The tests bring their own fakes: a `fakeStore` (wraps its errors, counts calls, can fail on demand, can
yield the processor inside `Put`/`Get` to widen race windows), a `scriptedIDs` generator that collides on purpose, a
`prefixEncoder` (`id-7`) and a fake clock. Read them first: they are the contract. `RunStoreContract` is a shared
suite every `Store` must pass; it is run against `MemoryStore` (Stage 1), `ShardedStore` (Stage 3) and the test fake.
Three design decisions are checked by behaviour, not by name: the dedupe index is only a *hint* (verify a hit against
the store: `TestStage2_StaleIndexIsCheckedAgainstTheStoreNotTrusted`), options create *distinct* links, and an
expired alias can be reclaimed with exactly one winner. The `Service` must not hold a lock across a store call that
every request needs, and a failed store call must leave no trace.
Run the whole thing several times before you call it done: `go test -race -count=20 ./labs/b3/shortener/`.

Break-it: on your passing solution — trust the dedupe index without checking the store, drop the per-URL lock,
compare store errors with `==`, report every `Get` error as `ErrNotFound`, drop the alias lock, increment the click
counter without a lock, make `ShardedStore.Put` check-then-act (see unit 7 §3).

**leaderboard.** Stretch lab, 60 minutes. The ordering is a strict total order (higher score first; ties go to whoever
*reached* the score first, tracked by a logical clock that ticks on every score change), so no two players share a
rank; read the type comment on `Board` before writing the comparison. Stage 1's differential test compares your board
with a map-plus-sort model over 20,000 random operations, so a sorted slice is a fine first answer. The Stage 2 scale
tests are the real gate: 100,000 players, three workloads (random scores, strictly ascending scores, everyone tied),
generous wall-clock budgets under `-race`, aborting as soon as the budget is blown. They fail anything whose
per-operation cost grows linearly with the number of players, and the ascending workload fails an unbalanced search
tree. Expect them to take 5–15 s with `-race` on a correct solution. Stage 3 splits in two: the 15-minute part
(`-run 'Stage3_(Concurrent|Snapshot)'`) is concurrency and `Snapshot`; the follow-ups (`-run 'Stage3_(Sharded|Windowed)'`)
are for when you finish early. The sharded test compares `Sharded` with a plain `Board` operation by operation, so
tie-breaking must agree across shards (one shared logical clock). The windowed tests use a fake clock; nothing sleeps
on the real one.

Run the whole thing a few times before you call it done: `go test -race -count=3 ./labs/b3/leaderboard/` (≈ 40 s).

Break-it: mutate your own solution (see unit 8 §1): rank by scanning instead of counting, constant treap priorities,
ties broken by name, `Set` to an unchanged score refreshing the tiebreak clock, `Add` under a read lock, a `Snapshot`
that shares the live map, per-shard logical clocks, too few entries taken from each shard, skipped windows not
materialised.

**hashcache** (STRETCH). The tests are grouped by stage (`TestStage1_*`, `TestStage2_*`, `TestStage3_*`), so you can run one stage at a time and open `stage2.go` / `stage3.go` only when you get there. The ring tests measure *properties*, not exact placements, so any correct hash passes: balance over many keys and node-name sets, "adding a node moves ≈ 1/(N+1) of the keys, all to the new node", "removing a node moves only its keys, to the next node clockwise", and a one-virtual-node wraparound test. Plain `hash/fnv` on short, similar strings fails the balance tests: that is intended, so look at the numbers in the failure message and mix the hash. `stage2_test.go` has the fake backends (failure injection, call counters, a fake clock): read it before writing the client, because the Stage 2 contract is stated in terms of what the fakes observe (which node was called how often). Placement must equal `NewRing(cfg.Vnodes)` plus `Add(node, 1)` for each backend, so the tests can predict where a key lives. Stage 3 includes a deterministic hook that stores a newer value between a migration's read and its copy-forward (use `Backend.Add`), a hit-ratio comparison against a `hash % N` client defined in the test file, and `SetMembership` racing with eight goroutines of traffic under `-race`. Suite time under `-race` is a few seconds per run; use `-count=5` rather than `-count=20` while iterating.

Break-it: mutate your own solution (see unit 8 §2) — make vnode positions depend on ring size, drop the distinct-node check in `Replicas`, clamp instead of wrapping, remove the hash finaliser, copy forward with `Set`, skip deleting from previous owners, swap the state pointer without an atomic, report "all failed" as a miss, and drop the same-owner check in the migration read.
