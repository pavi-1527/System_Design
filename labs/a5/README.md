# A5 labs

Three Go packages, stdlib only, no network at all: replicas, clocks and nodes are in-memory objects, and every
fault (a replica down, a write that lags, an acknowledgement that is lost, a skewed clock, a partitioned network) is a
switch you flip in the test. Nothing sleeps and nothing uses a global random source, so every run is reproducible.
Each stub compiles and returns immediately (zero values or `errNotImplemented`), so every test fails fast until you
implement it. Reference solutions live in `solutions/a5/`; the `*_test.go` files there are byte-identical to the ones
here — don't edit the tests, edit the code.

Run everything the gate checks with `sd lab A5` (it runs `go test -race` over the core packages and reports
`raftlite` separately). While working on one package, run it directly as shown below.

| Order | Package | Unit | Core/Stretch |
|---|---|---|---|
| 1 | [`quorum`](quorum/) | 1 (§3–§4) | CORE |
| 2 | [`clocks`](clocks/) | 2 (§2) | CORE |
| 3 | [`raftlite`](raftlite/) | 3 (§2) | STRETCH |

Some files in a package are **provided** (identical in the lab and the solution): `quorum/versions.go`,
`quorum/sim.go`, `raftlite/sim.go`, `raftlite/breakit_on.go` and `breakit_off.go`. They are the test bench, not the
thing you are learning. Read them, but you edit only the file named after the package.

---

## 1. `quorum` — a Dynamo-style replicated key-value store

**Goal.** Make `R + W > N` observable. You build the coordinator of a replicated KV store: a write fans out to the `N`
replicas of a key and needs `W` acknowledgements; a read consults the `R` fastest replicas and reconciles what they
return; read repair heals stale replicas; sloppy quorums borrow stand-ins and hinted handoff pays them back. The
tests then show you what the formula guarantees and, just as important, what it does not.

**Edit** `labs/a5/quorum/quorum.go`. Provided: `VClock`, `Version`, `Reconcile`, the per-replica `Node` (with its
hint list) and the `Sim` fault injector (`SetDown`, `SetLag`, `SetAckLoss`, `SetLatency`). Implement, in order:

1. `NewCluster` (validation), `PreferenceList`, `Fallbacks`, `Node`.
2. `Put` for healthy replicas, `Get` without repair. Then `Put`'s `Lag` and `AckLoss` rules and the `W` check.
3. `Get` with `ReadRepair`; `Flush`.
4. Sloppy mode in `Put`, and `DeliverHints`.

The package doc lists the exact rules the tests check. Two tests are worth reading before you code:
`TestOverlapRule_ExhaustiveForThreeReplicas` enumerates every write set and read set for `N = 3` and checks that a
stale read is possible exactly when `R + W <= N`; `TestQuorumIsNotLinearizable_ReadsCanGoBackInTime` shows a read
that returns the new value followed by one that returns the old value, with `R + W > N`.

**Run:** `go test -race ./labs/a5/quorum/`

**Break it:**
- `go test -tags breakit -run BreakIt -v ./labs/a5/quorum/` prints two tables: stale reads after acknowledged writes
  and visible effects of *rejected* writes for every `(R, W)` with `N = 3` and a 30% chance that each replica lags;
  and the availability and stale-read cost of sloppy quorums with 5 nodes and a 30% chance that each is down.
  Predict the columns first (Unit 1 §3–§4 has the numbers and the questions). To change a parameter, copy
  `breakit_test.go` to a scratch file such as `breakit_scratch_test.go` (keep the `//go:build breakit` line, rename the
  test functions) and edit the copy — the shared test files must stay identical to the solution's.
- In `Put`, count a lagging replica's *queued* write as an acknowledgement (`acks++` in the `Lag` branch) and run the
  normal suite. `TestOverlapRule_ExhaustiveForThreeReplicas` and `TestQuorumIsNotLinearizable_ReadsCanGoBackInTime`
  fail: an acknowledgement has to mean "applied", or `W` stops meaning anything.

---

## 2. `clocks` — Lamport clocks, vector clocks, siblings, and last-write-wins

**Goal.** Logical time you can reason about: a thread-safe Lamport clock, vector clocks with `Compare` / `Merge` /
`Descends`, `Reconcile` (drop what is superseded, keep what is concurrent), and two single-key registers — one that
resolves conflicts by wall-clock timestamp (last-write-wins) and one that keeps siblings by causality.

**Edit** `labs/a5/clocks/clocks.go`. Provided: `Vector.Clone`, `Vector.String`, `Ordering`. Implement `Lamport`,
`Stamp.Less`, `Vector.Tick`, `Descends`, `Compare`, `Merge`, `Reconcile`, `LWWRegister` and `SiblingRegister`.

**Run:** `go test -race ./labs/a5/clocks/`

**Break it:**
- `go test -tags breakit -run BreakIt -v ./labs/a5/clocks/` — 20,000 pairs of causally ordered writes with a
  growing clock error; how many does last-write-wins lose, and how many do siblings lose? Predict the last column
  before you run it (Unit 2 §1 has the table).
- In `Lamport.Observe` drop the `+ 1` and run the suite: which test notices, and what does the failure message say
  about why it matters? Then make `Vector.Tick` mutate its receiver and watch `TestVectorTick_ReturnsACopyAndNilIsEmpty`.
- Make `LWWRegister.Write` break timestamp ties by arrival order instead of by value and run
  `TestLWW_HighestTimestampWinsAndTiesConverge`: two replicas now disagree forever.

---

## 3. `raftlite` — leader election and log replication (STRETCH, not gated)

**Goal.** Raft's core as a deterministic state machine: terms, randomized election timeouts, one vote per term, the
"candidate's log must be at least as up to date" rule, log matching with truncation of conflicting suffixes, and the
commit rule (a leader commits only entries from its own term by counting replicas). The harness in `sim.go` is an
in-memory network that delivers every message one tick after it was sent, can crash and restart nodes, and can
partition the cluster; after every tick it checks Election Safety (at most one leader per term) and State Machine
Safety (committed entries never change).

**Edit** `labs/a5/raftlite/raftlite.go`: `NewNode` and the `Node` methods. The package doc is the specification; the
test names tell you the order to build in (`Vote*`, `Election*`, `Terms*`, `Append*`, `Leader*`, then `Cluster*`).
What it omits on purpose: persistence, membership change, snapshots, client sessions, read-only queries, pre-vote.

**Run:** `go test -race ./labs/a5/raftlite/`  (the `Cluster*` tests run 10–40 seeds each; the whole package takes a
few seconds).

**Break it:**
- `go test -tags breakit -run BreakIt -v ./labs/a5/raftlite/` — five nodes with identical election timeouts never
  elect a leader (term climbs every round); then the time to first leader and the share of split votes as the
  randomization window widens from 1 to 40 ticks. Predict the first line (Unit 3 §2 has the numbers).
- Wire `breakSkipLogCheck` into your vote handler (`if breakSkipLogCheck { upToDate = true }` before the grant
  decision), then run `go test -tags breakit -run 'StaleLog' -v ./labs/a5/raftlite/`. Expected: the unit test for
  stale-log candidates fails, and so does `TestCluster_StaleLogCandidateCannotWinElection`, where the harness reports
  that a committed entry was overwritten by a leader that never had it.
- Remove the `log[N].Term == currentTerm` condition from your commit rule and run the normal suite: watch
  `TestLeader_NeverCommitsAnEarlierTermsEntryByCountingReplicas` fail.

---

## Reference

- Solutions: `go test -race ./solutions/a5/...`
- Break-it code is behind the `breakit` build tag, so default runs ignore it: `go vet -tags breakit ./labs/a5/...`
  type-checks it.
- Everything here is also deterministic under `-count=20`: `go test -race -count=20 ./labs/a5/<pkg>/` is a good way to
  catch a data race you didn't see once.
