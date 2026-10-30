# C12 labs

One Go package, stdlib only, no network. The stub compiles and returns zero values, so every test fails fast until
you implement it. The reference solution lives in `solutions/c12/suggest/`; its `*_test.go` files are byte-identical
to the ones here, so don't edit the tests, edit the code. Look at the solution only after a real attempt (or after
30 minutes stuck with a written-down description of what you're stuck on).

Run everything the gate checks with `sd lab C12` (it runs `go test -race` over the package). While working, run the
package directly as shown below.

| Order | Package | Unit | Core/Stretch |
|---|---|---|---|
| 1 | [`suggest`](suggest/) | 2 (§5–§6) | CORE |

---

## 1. `suggest` — prefix index, serving snapshot, decaying counter

**Goal.** The three components a senior candidate is expected to be able to sketch *and* reason about for an
autocomplete service: an index that answers "best K completions of this prefix" in O(len(prefix)) because every
node already holds its top-K; a serving replica that publishes a new index snapshot with one atomic pointer swap
while readers keep reading; and an aggregation counter whose counts decay with a half-life so the suggestions stay
fresh and the counter's memory stays bounded.

**Edit** `labs/c12/suggest/suggest.go`. `Options`, `Suggestion`, `Less` (the total order: count descending, then
query ascending) and `Normalize` (lower case, trim, collapse whitespace) are provided. Implement, in order:

1. `Build(counts, opts)` and `(*Index).Suggest`, `Nodes`, `Len` — apply the defaults (`K` = 5, `MaxPrefix` = 20),
   normalise and merge queries, drop non-positive counts, apply `MinCount` and the whole-word/phrase `Blocklist`,
   then push every surviving query into the top-K of each of its prefixes down to `MaxPrefix` runes. `Suggest`
   walks to the prefix node, filters by the full prefix when the prefix was truncated, and returns a *copy* of up
   to `limit` entries (`limit ≤ 0` or `> K` means `K`). The empty prefix returns nil. Write the build cost, lookup
   cost and bytes per node of your structure in the `Index` doc comment.
2. `NewService`, `Current`, `Swap`, `(*Service).Suggest` — the current `*Index` behind `sync/atomic` (an
   `atomic.Pointer[Index]` is the natural fit). No lock on the read path; a reader sees the whole old snapshot or
   the whole new one.
3. `NewCounter`, `Record`, `Snapshot`, `(*Counter).Len` — a mutex-protected map of query → (float count, last
   update); decay lazily with `count *= 2^(-elapsed/halfLife)` whenever an entry is touched; `Snapshot` rounds to
   integers, omits and forgets queries that round to zero, and returns exactly what `Build` takes.

**Run:** `go test -race ./labs/c12/suggest/`. Run it a few times (`-count=5`) once it's green: the swap test is
schedule-dependent.

**Acceptance tests:** `TestBuild_TopKMatchesBruteForce` (every prefix of a 3,000-query corpus, K ∈ {1, 3, 5},
compared with a brute-force ranking), `TestSuggest_TiesBreakByQueryAscending`, `TestSuggest_LimitClampsToK`,
`TestSuggest_EmptyPrefixAndMissesReturnNothing`, `TestSuggest_NormalisesPrefixAndHandlesUnicode`,
`TestBuild_MergesQueriesThatNormaliseEqual`, `TestBuild_DropsBelowMinCountAndNonPositive`,
`TestBuild_BlocklistMatchesWholeWordsAndPhrases`, `TestSuggest_PrefixLongerThanMaxPrefixIsTruncatedThenFiltered`,
`TestSuggest_ResultIsACopy`, `TestIndex_NodesBoundedByPrefixCount`, `TestService_NilIndexServesNothing`,
`TestService_SwapIsAtomicUnderConcurrentReaders` (8 readers, 20,000 swaps, `-race`),
`TestCounter_RecordNormalisesAndCounts`, `TestCounter_HalfLifeDecay` (8 → 4 → 2 → 1 with an injected clock, then
forgotten), `TestCounter_ConcurrentRecordsAreNotLost` (50 goroutines, `-race`), `TestCounter_SnapshotFeedsBuildAndSwap`
(the whole pipeline: record → snapshot → build → swap → suggest, with yesterday's news decaying away).

**Benchmark** (not part of the gate): `go test -run '^$' -bench Suggest -benchmem ./labs/c12/suggest/` builds a
50,000-query index and times lookups. The reference solution answers in ≈ 90 ns with 2 allocations (the result
copy) on a laptop; yours should be within a small factor. If it's microseconds, you're walking a subtree at query
time, which is the first break-it exercise.

### Break it

All break-it code lives in `breakit_test.go` behind the `breakit` build tag, so `sd lab C12`, `go vet` and plain
`go test` never compile it.

**1. Top-K at query time (Unit 2 §5).** Predict the ratio first, then:

```sh
go test -tags breakit -run '^$' -bench BreakIt -benchmem ./labs/c12/suggest/
```

It builds a 200,000-query corpus over a 6-letter alphabet and compares your index with `scanIndex`, the "sorted
list + binary search + walk the prefix range" design, for the prefixes `a`, `ab` and `abc`.

<details data-kind="answer">
<summary>What you should see, and why</summary>

Reference run (laptop, `-race` off): precomputed ≈ 65–80 ns for every prefix; scan ≈ 1.4 ms for `a` (≈ 33,000
matching queries), ≈ 170 µs for `ab` (≈ 5,600), ≈ 24 µs for `abc` (≈ 880). The scan's cost is proportional to the
number of queries sharing the prefix, and the shortest prefixes are both the most common requests (every search
starts with one letter) and the most expensive. At 350k lookups/s peak, 1.4 ms of CPU per one-letter lookup is
≈ 500 cores just for those; 70 ns is a rounding error. That is the whole reason the index precomputes the top-K per
node and pays for it in memory and build time.

</details>

**2. The swap without the atomic (Unit 2 §6).** `racyService` is `Service` with the atomic pointer replaced by a
plain field. Predict what the race detector reports, then:

```sh
go test -race -tags breakit -run TestBreakIt ./labs/c12/suggest/
```

<details data-kind="answer">
<summary>What you should see, and why</summary>

`WARNING: DATA RACE` between `racyService.Swap` (write of `s.cur`) and `racyService.Suggest` (read of `s.cur`),
and the test is marked failed because a race was detected. Without `-race` the test passes every time, and on
amd64 a plain pointer write even happens to be atomic in practice. The bug is still real: the Go memory model
gives no guarantee that a reader sees a consistent pointer, and more importantly a compiler is free to reorder the
*construction* of the new index relative to the publication of the pointer. `atomic.Pointer.Store` is a release
store and `Load` an acquire load: everything `Build` wrote is visible to a reader that loads the new pointer. A
`sync.RWMutex` is also correct, at the cost of a lock on every lookup (and of writers waiting for readers to drain).

</details>

**3. Decay switched off (Unit 2 §6).** In a scratch test, run the `TestCounter_SnapshotFeedsBuildAndSwap` scenario
with `HalfLife: 0` and look at the second snapshot's suggestions. Then try `HalfLife: time.Minute` with the same
timestamps. What does each do to the product?

<details data-kind="answer">
<summary>What you should see, and why</summary>

With no decay, "world cup final" (100 observations three days ago) still beats "world weather" (50 today): the
suggestions are a hall of fame, not a reflection of what people search now. With a one-minute half-life, three
days later every old count has decayed to zero, the snapshot contains only the queries of the last few minutes,
and a quiet night empties it: suggestions flicker with noise. The half-life is a product knob between stability
and freshness; hours to a day is the usual range, and trending surfaces use a second, much shorter one.

</details>

### Notes

- `Normalize` trims trailing whitespace, so `"pizza "` and `"pizza"` are the same prefix. Real systems keep the
  trailing space as a signal ("the word is complete, suggest the next word"); it's left out here to keep the
  contract small.
- Map iteration order is random in Go. Because `Less` is a total order (no two queries compare equal after
  normalisation), "keep the best K under `Less`" gives the same result in any insertion order, and the brute-force
  test depends on that.
- The trie uses one `map[rune]*node` per node in the reference solution, which is convenient and memory-hungry.
  Unit 2 §5 covers the compact alternatives (sorted child arrays, radix/Patricia compression, FSTs); none is
  required for the tests.
