# C4 labs

Two Go packages, stdlib only, no network: replicas, nodes and the network between them are in-memory objects, time
is a number you pass in (stamps, rounds), and every random choice comes from a seeded source, so every run is
reproducible. The stubs compile and return immediately (zero values or `errNotImplemented`), so every test fails
fast until you implement it. Reference solutions live in `solutions/c4/`; the `*_test.go` files there are
byte-identical to the ones here — don't edit the tests, edit the code.

Run everything the gate checks with `sd lab C4` (it runs `go test -race` over the core package and reports
`membership` separately). While working on one package, run it directly as shown below. Before calling a package
done, run it once with `-count=5`: the concurrency tests use real goroutines.

| Order | Package | Unit | Core/Stretch |
|---|---|---|---|
| 1 | [`antientropy`](antientropy/) | 2 (§7) | CORE |
| 2 | [`membership`](membership/) | 2 (§8) | STRETCH |

What these labs deliberately do **not** rebuild: the quorum coordinator, sloppy quorums, hinted handoff, read repair
and vector-clock siblings are A5's `labs/a5/quorum` and `labs/a5/clocks`; consistent hashing with virtual nodes is
A4's `labs/a4/ring`; the LSM tree under each node is A2's `labs/a2/lsm`. C4 Unit 2 points back at their break-its
with this case study's numbers. The two packages here are the pieces of a Dynamo-style store that those labs leave
out: background repair of keys nobody reads, and how nodes find out who is alive.

---

## 1. `antientropy` — Merkle-tree repair, tombstones and zombies

**Goal.** Two replicas of a key range have drifted apart by a handful of keys out of millions. Find exactly those
keys by exchanging hashes, ship only the leaves that differ, merge them deterministically, and garbage-collect
deletes without letting a replica that missed one bring the data back.

**Edit** `labs/c4/antientropy/antientropy.go`. Provided: the errors, `MaxDepth`, `Hash`, `Entry`, the `Replica` and
`Tree` structs, `NewReplica`, `Name`, `Token` (the same FNV-1a + splitmix64 hash as A4's ring) and `Stats`.
Implement, in order (each step's tests run alone with `-run`):

1. `Entry.Wins`, `clone`, `Replica.Apply`, `Put`, `Delete`, `Get`, `Lookup`, `Len`, `Compact`:
   `go test -race -run 'Wins|Replica' ./labs/c4/antientropy/`
2. `Bucket`, `BucketEntries`, `BuildTree` (with `hashLeaf` and `hashPair`), `Depth`, `Leaves`, `Root`, `Node`:
   `go test -race -run 'Bucket|BuildTree' ./labs/c4/antientropy/`
3. `Diff`: `go test -race -run Diff ./labs/c4/antientropy/`
4. `Repair`: `go test -race -run Repair ./labs/c4/antientropy/`
5. `SafeCutoff`, then the tombstone tests: `go test -race -run 'Tombstones|SafeCutoff' ./labs/c4/antientropy/`
6. Everything: `go test -race ./labs/c4/antientropy/`

**Documented behaviour the tests check:**
- `Wins`: higher `Stamp` wins; on a tie a tombstone beats a live value; on a tie between two live values the
  bytewise-larger value wins; an identical entry never wins, so `Apply` is idempotent (repair re-sends entries).
- A replica copies values in and out (a caller reusing its buffer, or editing what `Get`/`Lookup` returned, never
  changes stored data) and stores a tombstone with a nil value. `Compact(cutoff)` removes tombstones with
  `Stamp < cutoff` (strictly) and never a live value.
- `Bucket(k, d)` is the top `d` bits of `Token(k)`; 6,400 sequential keys land ≈ 100 per leaf at depth 6.
- An empty replica has a zero root; the same content inserted in any order gives the same root; a different stamp,
  a tombstone instead of a value, an extra tombstone, and `("ab","c")` instead of `("a","bc")` all change it.
- `Diff` of identical trees costs exactly **1** comparison; one differing key at depth `d` costs exactly
  `1 + 2d`; trees that differ everywhere cost `2^(d+1) − 1` (every node once). Differing leaves come back sorted.
- `Repair` of 10,000 keys with 3 divergences at depth 10 applies exactly 3 entries and ships exactly the entries
  of the 3 differing leaves on both sides (≈ 60, never the range); a second `Repair` costs one comparison and
  ships nothing. Tombstones travel like values, and repair runs in both directions.
- Six writers and three repairers on overlapping pairs `(a,b)`, `(b,c)`, `(c,a)` run concurrently without a
  deadlock or a race, and three quiesced repairs converge all three replicas.
- The zombie: three replicas hold a value, the delete misses `c`, `a` and `b` compact the tombstone, `c` returns:
  a correct `Repair` copies the value **back** onto `a`. With `SafeCutoff(now, grace, lastFullRepair)` (the older
  of `now − grace` and the last repair that covered every replica) the tombstone survives and reaches `c` instead.

**Acceptance:** `go test -race ./labs/c4/antientropy/` passes; then `sd lab C4`.

### Break-it exercises (Unit 2 §7)

Both are behind the `breakit` build tag, so normal runs ignore them. Predict the columns before each run.

1. **Tree depth.** `go test -tags breakit -run BreakIt_DepthSweep -v ./labs/c4/antientropy/` repairs two replicas
   of 100,000 keys that differ in 20, at depths 0 to 16. Reference run: depth 0 ships **200,000** entries with 1
   comparison; depth 10 ships **3,700** with 235 comparisons and a 64 KB tree; depth 16 ships **94** with 471
   comparisons and a 4 MB tree. Find the knee, then redo the arithmetic for a node holding ≈ 500 GB of ≈ 1 KB
   values (≈ 5 × 10^8 keys): what depth keeps a leaf under ≈ 10 MB, and how much memory is the tree?
2. **Zombie timeline.** `go test -tags breakit -run BreakIt_ZombieTimeline -v ./labs/c4/antientropy/` simulates 40
   days: `c` goes down on day 1 for 2–20 days, 100 keys are deleted on day 2, every replica compacts daily with a
   grace period of 3–14 days, and repair runs weekly. Reference run: with a 10-day grace, outages of 2 and 5 days
   leave **0** zombies and outages of 9, 12 and 20 days leave **100**; with `SafeCutoff` every row is **0**. Derive
   the rule (grace must exceed outage + time to the next repair) and say what an operator must do before
   returning a node that has been down longer than the grace period.
3. **Mutations** (one at a time, in your own code; predict which test fails and what the production symptom is):
   hash only the keys in `hashLeaf` (not stamps or the `Deleted` flag); make `Diff` compare only the roots and
   return every leaf when they differ; make `Wins` return `true` on a tie between live values; let `Apply` store
   the caller's slice without copying it; make `Compact` use `<=` instead of `<`; take the two replicas' locks
   nested inside `Repair` (lock `a`, then call into `b` while holding it).

Vet both build configurations: `go vet ./labs/c4/... && go vet -tags breakit ./labs/c4/...`.

---

## 2. `membership` — gossip heartbeats, suspicion, generations, phi accrual (STRETCH)

**Goal.** No coordinator, no central health checker: every node gossips a table of heartbeats with a few random
peers per round and decides for itself who is alive. Make dissemination logarithmic, make a crash visible as
suspect-then-dead on every node, make a restarted node acceptable at once, and make a healed partition rejoin.
Then replace the fixed timeout with an accrual detector that adapts to each peer's rhythm.

**Edit** `labs/c4/membership/membership.go`. Provided: the types and constants, `Status.String`, and a suggested
shape for the unexported fields. The package doc states the model rule by rule; the tests depend on every line.
Implement, in order:

1. `Heartbeat.Newer`, `NewCluster`, `View`, `Status`, `Rounds`, `Messages`:
   `go test -race -run 'Heartbeat|NewCluster' ./labs/c4/membership/`
2. `Round` (bump → snapshot → exchanges → statuses): `go test -race -run Round ./labs/c4/membership/`
3. `Stop`, `Restart`, `Partition`, `Heal`: `go test -race -run 'Stop|Restart|Partition|Concurrent' ./labs/c4/membership/`
4. `NewPhiDetector`, `Heartbeat`, `Mean`, `Phi`: `go test -race -run Phi ./labs/c4/membership/`

**Documented behaviour the tests check:**
- `Newer` compares `Generation` first: `{1, 0}` is newer than `{0, 900}`.
- After 3 rounds every node sees itself at version 3; 8 nodes with fanout 2 send exactly 48 messages.
- With fanout 1, every one of 64 nodes has heard from every other within 15 rounds (≈ 7 in the reference), for
  five seeds. A healthy 32-node cluster with fanout 3 never suspects anyone after warm-up.
- A crashed node is `Suspect` on every live node before it is `Dead`, is convicted no sooner than `DeadAfter`
  rounds and no later than `DeadAfter + 15`, nobody else is falsely suspected, and it stays in every table.
- A node restarted after being declared dead is `Alive` everywhere within 12 rounds with generation 1 (without
  the generation it would take ≈ 50 rounds to "catch up" with its old version).
- A partition makes each half convict the other while staying healthy inside; after `Heal` the cluster is whole
  within `2 × DeadProbeEvery + 15` rounds, which only happens if nodes keep probing members they consider dead.
- `Phi` is `silence / (mean · ln 10)`: with heartbeats every second, ≈ 0.43 after 1 s, ≈ 1 after 2.3 s, ≈ 8 after
  18.4 s. After 1 s of silence a peer that beats every 100 ms is at ≈ 4.3 and one that beats every 2 s at ≈ 0.22.
  Only the last `window` intervals count; out-of-order heartbeats are ignored.
- `Round` runs while 8 goroutines read views, statuses and counters, under `-race`, without deadlock.

**Acceptance:** `go test -race ./labs/c4/membership/` passes.

### Break-it exercises (Unit 2 §8)

1. **Fanout and cluster size.** `go test -tags breakit -run BreakIt_FanoutSweep -v ./labs/c4/membership/` prints
   rounds to full dissemination for 16 to 1,024 nodes and fanouts 1 to 3. Reference run: fanout 1 takes 5, 7, 9
   and **11** rounds for 16, 64, 256 and 1,024 nodes; fanout 3 takes 3 to 6; messages per node per round equal the
   fanout whatever the size. Turn rounds into seconds at one round per second and compare with the conviction
   threshold you would pick.
2. **No dead probes.** `go test -tags breakit -run BreakIt_PartitionWithoutDeadProbes -v ./labs/c4/membership/`
   partitions 16 nodes, waits for each side to convict the other, heals the network, and counts rounds until the
   cluster is whole. Reference run: whole again in **2** rounds with probes every 4 rounds; **still split after 200
   rounds** with no probes. Explain why the network being fine is not enough.
3. **Mutations:** compare only `Version` in `Newer` and run `TestRestart_NewGenerationIsAcceptedQuickly`; let a
   node copy a peer's `Status` instead of judging from its own `LastProgress` and watch a single false suspicion
   spread; merge from live tables instead of the round's snapshot and rerun the fanout sweep (news now travels
   several hops per round: the simulation lies about time).

Vet both build configurations: `go vet ./labs/c4/... && go vet -tags breakit ./labs/c4/...`.
