# C10 labs — file storage and sync

Two Go packages, stdlib only. The stubs compile and return immediately (`errNotImplemented` or zero values), so
every test fails fast until you implement it. Reference solutions live in `solutions/c10/`; the `*_test.go`
files there are byte-identical to the ones here, so edit the code, never the tests.

Run what the gate checks with `sd lab C10` (`go test -race` over the core package; `syncvv` is reported
separately). Before you call a package done, `go test -race -count=20 ./labs/c10/<pkg>/`.

| Order | Package | Unit | Core/Stretch |
|---|---|---|---|
| 1 | [`chunker`](chunker/) | 2 (§5–§6) | CORE |
| 2 | [`syncvv`](syncvv/) | 2 (§7) | STRETCH |

---

## 1. `chunker` — content-defined chunking, dedupe store, manifests, resumable uploads

**Goal.** The storage core of a sync service: split files into chunks whose boundaries depend on content (so an
edit changes a few chunks, not every chunk after it), address chunks by their hash so identical content is stored
once and reference-counted, describe a file as a manifest of chunk hashes, verify integrity on the way out, and
run upload sessions that ask the client only for the chunks the service does not already hold, accept them in
any order from parallel connections, resume after a disconnect, commit exactly once, expire, and get garbage
collected.

**Edit** `labs/c10/chunker/chunker.go`. Provided: errors, the `gear` table and `DefaultCDC`, `HashChunk`, the
types. Implement, in order:

1. `FixedChunks`, `CDCChunks`, `BuildManifest`. Tests: `TestFixedChunks_*`, `TestCDCChunks_*`, `TestHashAndManifest`.
   The insertion test is the point of the exercise: a 100-byte insertion must leave ≥ 90% of CDC chunks shared
   and < 30% of fixed-size chunks shared.
2. `Store`: `PutChunk`, `Has`, `Get` (with verification), `Commit`, `Release`, `Assemble`, `Stats`, `Corrupt`.
   Tests: `TestStore_*`.
3. `Uploader`: `Begin`, `Missing`, `PutChunk`, `Commit`, `Abort`, `GC`, session expiry. Tests: `TestUpload_*`.

**Run:** `go test -race ./labs/c10/chunker/`

**Break it** (edit your passing solution, watch which test fails, then revert):

- Temporarily change `DefaultCDC` in `chunker.go` to `{Min: 4096, Avg: 4096, Max: 4096}` (which is fixed-size
  chunking in disguise) and rerun the insertion test (`-run Insertion -v`). Expected: the CDC share collapses towards
  the fixed-size figure. Then try `{Min: 16384, Avg: 65536, Max: 262144}` and watch the chunk count and the shared
  fraction; decide what average chunk size you'd pick for documents vs videos, and why big chunks waste upload
  bandwidth after small edits. Revert afterwards (Unit 2 §5).
- In `Get`, return the stored bytes without re-hashing them. Run `-run Assemble`. Expected: `Get of a bit-rotted
  chunk` returns no error and the test fails. `Assemble` would still notice through the whole-file hash, but any
  path that serves single chunks hands out silently wrong bytes. This is the verify-on-read argument in Unit 2 §6.
- In `GC`, forget the "keep chunks of live sessions" rule. Run `-run SessionsExpire`. Expected: a live upload's
  chunks are deleted (`GC deleted 1 chunks of a live session`), and the client's next `Commit` would reference
  bytes that no longer exist.
- Hold the uploader's mutex while calling `HashChunk` and `store.PutChunk` in `PutChunk`. Nothing fails, but
  time `TestUpload_ConcurrentChunkUploadsFromManyConnections` with `-v` before and after, and explain what 16
  parallel connections are now waiting on.
- Make `Commit` increment references every time it is called. Run `-run AnyOrder`. Expected: `LogicalBytes = 2×`;
  a retried commit (the client timed out waiting for the first) has double-counted the file, and `Release` will
  leave garbage behind forever.

## 2. `syncvv` — version vectors and conflict resolution (STRETCH, not gated)

**Goal.** The decision a sync client makes when the server shows it a version of a file it also has: take the
remote version, push its own, or keep both as a conflicted copy. Version vectors (one counter per device) answer
"did this version see that one?" without clocks, as in
[A5 Unit 2 §2](../../modules/A5-consistency/unit-2-time-ordering.md#2-logical-time-lamport-and-vector-clocks).

**Edit** `labs/c10/syncvv/syncvv.go`: `Increment`, `Compare`, `Merge`, `Resolve` (with the conflicted-copy
naming rule in the package doc).

**Run:** `go test -race ./labs/c10/syncvv/`

**Break it:** replace `Compare` with a last-writer-wins comparison on a wall-clock timestamp you add to `Version`,
and rerun `TestResolve_TakeKeepOrConflict`. The concurrent case now silently picks a winner and drops the other
edit, which is exactly the "lost update" that conflicted copies exist to avoid.

---

## Reference

- Solutions: `go test -race ./solutions/c10/...`
- Related earlier labs: [`labs/a4/ring`](../a4/ring/) (placing chunks on storage nodes),
  [`labs/a6/outbox`](../a6/outbox/) (the metadata-then-notify pattern the sync fan-out uses),
  [`labs/a5/clocks`](../a5/clocks/) (the vector clocks `syncvv` applies).
