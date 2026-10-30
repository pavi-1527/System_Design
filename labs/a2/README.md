# A2 labs

Four Go packages, stdlib only, everything in memory (no real disks, no network). Each stub compiles and returns
immediately (zero values or `errNotImplemented`), so every test fails fast until you implement it. Reference solutions
live in `solutions/a2/`; the `*_test.go` files there are byte-identical to the ones here: don't edit the tests, edit the
code.

Run everything the gate checks with `sd lab A2` (it runs `go test -race` over the core packages and reports `wal`
separately). While working on one package, run it directly as shown below.

| Order | Package | Unit | Core/Stretch |
|---|---|---|---|
| 1 | [`btree`](btree/) | 1 (§2) | CORE |
| 2 | [`lsm`](lsm/) | 2 (§5–§6) | CORE |
| 3 | [`wal`](wal/) | 2 (§4) | STRETCH |
| 4 | [`isolation`](isolation/) | 3 (§7–§8) | CORE |

The `wal` lab sits next to `lsm` in Unit 2 because §4 (durability) comes before §5 (LSM) in the text, but it is
STRETCH: do it after `lsm` if you are short on time.

---

## 1. `btree`: a B+tree with page splits and range scans

**Goal.** An index you can reason about: fixed-capacity nodes, splits that push a separator up, a root that grows the
tree one level at a time, leaves chained for range scans, and counters that show what a lookup costs in page reads.

**Edit** `labs/a2/btree/btree.go`. Provided: `New` (validation), the lock-taking public wrappers (`Put`, `Get`,
`Delete`, `Scan`), `node` and `newNode`, `touch` (call it once per node you visit), `childIndex`, `insertAt`, `Height`,
`Stats`, `LeafKeys` and **`Check`**, which verifies every structural invariant (sorted keys, capacity, separator
bounds, uniform leaf depth, leaf chain order, entry count). Implement the four unexported workers, in this order:

1. `get`: descend one node per level (touching each), binary-search the leaf.
2. `put`: insert into the leaf, split on overflow (the exact split rules are in the package comment and are pinned by
   the tests), push the separator into the parent, split internal nodes the same way, and grow a new root when the old
   root splits. Keep `size` and `splits` right.
3. `scan`: one descent to the first leaf, then follow `next` pointers.
4. `del`: remove the key from its leaf and stop there (no merging; `Check` allows under-full and empty leaves).

**Run:** `go test -race ./labs/a2/btree/` (or `-run 'Split|Height|Fill'` for the structural tests, `-run Scan` for
range scans).

**Break it:**
- `go test -tags breakit -run BreakIt -v ./labs/a2/btree/`: 50,000 inserts in ascending, descending and random key
  order behind a 100-page LRU buffer pool; compare misses per insert, leaf fill and page counts. Predict the three
  rows first (Unit 1 §2).
- In your copy of `insert`/`put`, delete the line that fixes up the leaf chain (`next` pointers) after a split, then
  run the suite and read what `Check` says; then remove `Check` from a test in a scratch copy and see how much later,
  and how much less clearly, the bug is noticed.
- Change the leaf split so that an insert that lands after the last key of the rightmost leaf splits 90/10 instead of
  50/50 (what PostgreSQL's btree does for "rightmost" inserts, TODO-VERIFY exact fraction). Watch
  `TestFillFactor_*` fail, then explain what you gained for ascending keys and what you risk (Unit 1 §2).

---

## 2. `lsm`: memtable, SSTables, bloom filters, tiered compaction

**Goal.** The write path of RocksDB/Cassandra/LevelDB in miniature: buffer writes in a memtable, flush immutable sorted
tables, avoid reading tables that cannot contain a key (key range, bloom filter), merge tables so reads stay cheap and
space is reclaimed, and delete with tombstones that are dropped only when that is safe.

**Edit** `labs/a2/lsm/lsm.go`. Provided: the public wrappers with locking, `Entry`, `Options`/`Open`, `Stats`
(with counters), `NewTable`, `Table.Range`, `hashes`, `sortedMemtable`. Implement, in order:

1. `NewBloom`, `Bloom.Add`, `Bloom.MayContain`: size from bits per key, `k = round(bitsPerKey * ln 2)`, double
   hashing over the provided `hashes`.
2. `Merge`: k-way merge, newest run wins, optional tombstone dropping.
3. `Table.Lookup`: key range, then bloom, then binary search.
4. `DB.get`, `DB.put`, `DB.flush`.
5. `DB.compact`: the tiered policy (level i reaches `Fanout` tables, merge them into one table at level i+1). The
   tests check the table counts per level, the bytes written (write amplification) and tombstone retention.
6. `DB.scan`, `DB.compactAll`.

**Run:** `go test -race ./labs/a2/lsm/` (`-run Bloom`, `-run Merge`, `-run Compaction` for slices of it).

**Break it:**
- `go test -tags breakit -run BreakIt_Amplification -v ./labs/a2/lsm/`: the same overwrite-heavy workload at fanouts
  2, 3, 4, 8, 16, with and without bloom filters; write amplification, space amplification, table count and searches
  per missing key. Predict each column's trend first (Unit 2 §5, §6).
- The files `breakit_on.go` / `breakit_off.go` define `breakAlwaysDropTombstones` (true only with `-tags breakit`).
  Your `compact` passes `!deeper || breakAlwaysDropTombstones` to `Merge` (the stub's comment says so). Then:
  `go test -tags breakit -run Tombstone -v ./labs/a2/lsm/`
  Expected: `Get("a") = "1", true; want "", false`. A deleted account is back, because the compaction forgot the
  tombstone while an older table still held the value.
- Set `BloomBitsPerKey` to 1, 4, 10, 20 in a scratch test and compare `Stats().TableLookups` after 2,000 missing-key
  reads. How many bits per key are worth it?

---

## 3. `wal`: write-ahead log with crash recovery (STRETCH, not gated)

**Goal.** The durability half of every database: records with checksums, `fsync` as the only durability boundary, a
recovery pass that tells a torn tail (normal after a crash) from mid-log corruption (an incident), and group commit so
a slow `fsync` is shared by many committers.

**Edit** `labs/a2/wal/wal.go`. Provided: `SimFile` (a file with a volatile layer, `Crash(keep)`, `Corrupt`,
`FailNextSync`, `SetSyncDelay`), the types and errors, `Append`, `Sync`, `Commit` (thin wrappers) and the counters.
Implement:

1. `EncodeRecord` (CRC-32C, length, payload; the layout is pinned by the tests).
2. `Scan`: parse bytes into records; torn tail versus corruption (rules in the package comment).
3. `Open`: recover, truncate the torn tail, position the log.
4. `appendLocked`, `waitDurable`: the leader/follower group-commit loop on a `sync.Cond`.

**Run:** `go test -race ./labs/a2/wal/`

**Break it:**
- `go test -tags breakit -run BreakIt -v ./labs/a2/wal/`: 200 commits against a 2 ms `fsync` with 1, 4 and 16
  committers. Predict wall time and sync counts, then see what group commit buys.
- `breakit_on.go` / `breakit_off.go` define `breakSkipTruncate`. Wire it into `Open` (the stub says where), then
  `go test -tags breakit -run Crash -v ./labs/a2/wal/`. Expected: `file is 106 bytes after recovery, want 105`: the torn
  bytes stay, new records are appended behind garbage, and the next recovery loses them.
- In your `waitDurable`, hold `l.mu` across `f.Sync()` (an easy "simplification") and rerun the group-commit test:
  correctness is fine, the batching is gone.

---

## 4. `isolation`: MVCC with read committed, snapshot isolation and serializable

**Goal.** A versioned key/value store where the anomalies from Unit 3 §7 are reproducible in a unit test: lost update
under read committed, prevented by first-committer-wins under snapshot isolation; write skew and phantom inserts that
snapshot isolation allows and an optimistic read-validation level catches.

**Edit** `labs/a2/isolation/isolation.go`. Provided: types, errors, `NewStore`, `Begin`, `Run` (retry loop), `Clock`,
`Versions`, `latestTs`, `finish`. Implement, in order:

1. `readAt` and `readTs`: which version a read timestamp sees, and which timestamp each level reads at.
2. `Tx.Get`, `Put`, `Delete`, `Rollback`: own writes first, no dirty reads.
3. `Tx.Commit` for read committed (install versions), then the write-write check for snapshot isolation.
4. `Tx.Scan` and the Serializable validation of point reads and scanned prefixes.
5. `Store.Vacuum`.

**Run:** `go test -race ./labs/a2/isolation/`. The tests name the anomaly they exercise (`TestLostUpdate_*`,
`TestWriteSkew_*`, `TestPhantom_*`); the concurrent ones run bank transfers and a joint-overdraft rule under load.

**Break it:**
- `go test -tags breakit -race -run BreakIt -v ./labs/a2/isolation/`: 8 workers incrementing a counter (final value and
  attempts per level), then 8 workers enforcing `checking + savings >= 0` (lowest combined balance per level). Predict
  both tables first (Unit 3 §7, §8).
- Delete the write-conflict check from `Commit` in your copy and rerun the suite. Which tests fail, and which
  anomaly is each one about?
- In `Vacuum`, ignore active snapshots (use the current clock as the horizon). Which test fails, and what would a
  real long-running report see?

---

## Reference

- Solutions: `go test -race ./solutions/a2/...`
- Break-it code is behind the `breakit` build tag, so default runs ignore it: `go vet -tags breakit ./labs/a2/...`
  type-checks it.
