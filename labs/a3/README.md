# A3 labs

Three Go packages, stdlib only, no network. Each stub compiles and returns immediately (zero values or
`errNotImplemented`), so every test fails fast until you implement it. Reference solutions live in `solutions/a3/`;
the `*_test.go` files there are byte-identical to the ones here — don't edit the tests, edit the code.

Run everything the gate checks with `sd lab A3` (it runs `go test -race` over the core packages and reports `lfu`
separately). While working on one package, run it directly as shown below.

| Order | Package | Unit | Core/Stretch |
|---|---|---|---|
| 1 | [`lru`](lru/) | 1 (§3) | CORE |
| 2 | [`cacheaside`](cacheaside/) | 2 (§5–§7) | CORE |
| 3 | [`lfu`](lfu/) | 1 (§3, stretch) | STRETCH |

---

## 1. `lru` — LRU cache with TTL and cost weighting

**Goal.** The cache you would put in front of a slow lookup: O(1) `Get`/`Set`/evict, per-entry TTLs on an injected
clock, optional byte-cost weighting, hit/miss/eviction statistics, an eviction callback that may call back into the
cache, and safety under concurrent use.

**Edit** `labs/a3/lru/lru.go`. Provided: `Config`, `Stats`, `entry`, `New`, `Set`, `notify`, `overLimit`. Implement,
in order:

1. `unlink`, `pushFront`, `remove` (the intrusive doubly linked list; sentinels are already wired in `New`).
2. `Get`, `SetWithCost`, `Delete`, `Len`, `Cost`, `Keys`, `Stats` — eviction by entry count first.
3. TTL: expiry in `Get`/`Peek`, `PurgeExpired`.
4. Cost weighting in `SetWithCost` (several evictions per insert, oversized entries rejected).
5. `OnEvict`: collect victims under the lock, call the callback after unlocking.

The package doc lists the exact behaviour the tests check. Why a plain `sync.Mutex` and not an `RWMutex`: `Get`
promotes, so it writes.

**Run:** `go test -race ./labs/a3/lru/` (or `-run 'Eviction'`, `-run 'TTL'`, `-run 'Cost'`, `-run 'Concurrent'`).

**Break it:**
- Make `Get` skip the promotion and run `-run Eviction`: the cache silently degrades to FIFO. Read the failure
  message, then reason about which real workload would notice (Unit 1 §3).
- Wrap `Get` in `RLock`/`RUnlock` instead of `Lock` and run `go test -race -run Concurrent ./labs/a3/lru/`. Watch the
  race detector point at the list pointers.
- Call `OnEvict` while holding the lock and run `-run RunsOutsideTheLock`: the deadlock is caught by a timeout, not
  by a hang.
- Replace the map lookup with a scan of the list (keep everything else) and run `-run Scale`: the O(n) tax is
  invisible at 100 entries and fatal at 100k.

---

## 2. `cacheaside` — cache-aside with stampede protection

**Goal.** A loader-backed cache that survives the three things that kill naive cache-aside: a stampede on a cold or
expired key, a flood of lookups for keys that do not exist, and an invalidation that loses a race to a slow load. You
write your own singleflight (`Group`), then the `Cache` on top: TTL with jitter, negative caching with its own TTL,
short error caching, `Invalidate` that also cancels the store of an in-flight load, and stale-while-revalidate.

**Edit** `labs/a3/cacheaside/cacheaside.go`. Provided: types, `Config`, `Stats`, `New`, `jittered`, `InFlight`,
`Stats`. Implement, in order:

1. `Group.Do`, `Group.run`, `Group.Waiters` — the `TestGroup_*` tests. Rules that matter: the shared load is detached
   from every caller's cancellation, cancelled only when all callers have left, and a panic releases the waiters.
2. `Cache.Get` (hit/miss path), `Cache.fill` (store policy: value, `ErrNotFound`, errors, never context errors).
3. `Cache.Invalidate` and the `loadState` hand-shake with `fill`.
4. `Cache.refresh` and the stale branch of `Get`.

**Run:** `go test -race ./labs/a3/cacheaside/` (or `-run TestGroup`, `-run TestCache_Cold`, `-run Stale`).

**Break it:**
- `breakit_off.go` / `breakit_on.go` define `breakNoSingleflight` and `breakNoInvalidateGuard` (true only with
  `-tags breakit`). Wire them into your `Get` and `fill` as the stub comments say, then:
  `go test -tags breakit -race -run 'Cache_ColdKey|Cache_Invalidate' -v ./labs/a3/cacheaside/`
  Expected: `loader called 50 times for 50 concurrent Gets of one cold key` and the stale value surviving
  an invalidation.
- `go test -tags breakit -run BreakIt -v ./labs/a3/cacheaside/` prints three measurements; predict each before
  running (with the break-it consts unwired, the third shows your `Cache` handling the race correctly):
  - `StampedeCount`: 400 requests, a 30 ms "database" that can serve 20 queries at once — naive vs `Group.Do`.
  - `SynchronizedExpiry`: 2,000 keys warmed together; database loads per second over five minutes with jitter 0, 0.1,
    0.2, 0.5.
  - `StaleSetRace`: a hand-driven interleaving of the classic cache-aside race, naive vs your `Cache`.
- Make `fill` cache `context.Canceled` errors with `ErrorTTL` set; run `-run ContextErrors`, then describe the
  outage this causes when one impatient client times out.

Unit 2 §5–§7 has the expected observations.

---

## 3. `lfu` — frequency-aware admission (STRETCH, not gated)

**Goal.** A TinyLFU-style cache: a 4-row count-min sketch with 4-bit-style saturating counters and periodic
halving estimates how popular each key is; a full cache admits a newcomer only if it is more popular than the entry
it would evict. A one-pass scan then cannot flush the hot set — which the tests demonstrate against a plain LRU.

**Edit** `labs/a3/lfu/lfu.go`. Provided: `Hashes`, `NewSketch`, `New`, `Frequency`, `Len`, `Keys`, `Stats`.
Implement `Sketch.Increment`, `Sketch.Estimate`, `Cache.Get`, `Cache.Set`.

**Run:** `go test -race ./labs/a3/lfu/`

**Break it:**
- Remove the halving (`sample`) and run `-run Aging`: the old hot set blocks the new one forever.
- Change the admission test from `<=` to `<` (admit on ties) and rerun the suite; then explain what happens to a
  cache whose victims and candidates usually have equal, low counts.
- Grow the scan in `TestCache_ScanResistanceBeatsLRU` from 800 keys to 5,000 in a scratch copy of the test and watch
  the hot set decay anyway: every scanned key bumps the sketch, so aging fires several times mid-scan. Real TinyLFU
  puts a "doorkeeper" Bloom filter in front of the sketch so one-hit wonders never reach it. Sketch how you would add
  one (Unit 1 §3, stretch).

---

## Reference

- Solutions: `go test -race ./solutions/a3/...`
- Break-it code is behind the `breakit` build tag, so default runs ignore it: `go vet -tags breakit ./labs/a3/...`
  type-checks it.
