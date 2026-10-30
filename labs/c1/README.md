# C1 labs

One Go package, stdlib only, no network. The stub compiles and returns immediately (zero values or
`errNotImplemented`), so every test fails fast until you implement it. The reference solution lives in
`solutions/c1/`; the `*_test.go` files there are byte-identical to the ones here — don't edit the tests, edit the
code.

Run everything the gate checks with `sd lab C1` (it runs `go test -race` over the package and records the result).
While working, run the package directly as shown below. Before calling it done, run
`go test -race -count=10 ./labs/c1/keygen/` once: the uniqueness tests use real goroutines.

| Order | Package | Unit | Core/Stretch |
|---|---|---|---|
| 1 | [`keygen`](keygen/) | 1 (§5) | CORE |

---

## 1. `keygen` — unique short codes without a serialising point

**Goal.** The component every URL-shortener design hand-waves: a source of short codes that are unique across
every API server forever, cheap on the hot path, safe across crashes, and not enumerable. Three strategies
side by side, so that you can defend the choice with measurements instead of adjectives:

1. **Codes.** `Encode`/`Decode` (base 62, fixed length, zero-padded), `Capacity`, `CollisionProbability`, and a
   `Mixer`: a bijection on `[0, 2^bits)` that turns consecutive counter ids into scattered codes.
2. **Range allocation.** An `Allocator` reserves disjoint ranges from a durable `Counter` with compare-and-swap
   (`Load` + `CompareAndSwap`, retried when another node moved first); a per-node `Generator` consumes its range
   locally and touches the store once per range. A crash wastes the rest of the range and nothing else.
3. **Random codes.** `RandomCode` (unbiased draw from the alphabet) and a `RandomGenerator` that claims codes
   through an atomic insert-if-absent (`Inserter`), retrying a bounded number of times.

**Edit** `labs/c1/keygen/keygen.go`. Provided: the errors, `Alphabet`, `DefaultLimit`, the `Counter` and
`Inserter` interfaces, the config types. Implement, in order (each step's tests run alone with `-run`):

1. `Capacity`, `Encode`, `Decode`, `CollisionProbability`:
   `go test -race -run 'Encode|Decode|Capacity' ./labs/c1/keygen/`
2. `NewMixer`, `Mix`, `Unmix`: `go test -race -run Mixer ./labs/c1/keygen/`
3. `MemoryCounter` (`Load`, `CompareAndSwap`, `Fail`, `Ops`), `Range.Len`, `NewAllocator`, `Size`, `Reserve`:
   `go test -race -run 'Counter|Allocator' ./labs/c1/keygen/`
4. `NewGenerator`, `Next`, `Remaining`: `go test -race -run Generator ./labs/c1/keygen/`
5. `MemoryInserter`, `RandomCode`, `NewRandomGenerator`, `RandomGenerator.Next`:
   `go test -race -run 'Random|Inserter' ./labs/c1/keygen/`
6. Everything: `go test -race ./labs/c1/keygen/`

**Documented behaviour the tests check:**
- `Encode(62, 4)` is `"0010"`; `Encode(3844, 2)` is `ErrBadArgument` (two digits hold 0..3843). `Decode` accepts
  leading zeros and rejects the empty string, characters outside the alphabet and overflow (12 `Z`s).
- `CollisionProbability(7, 6e9)` ≈ 0.0017 and `CollisionProbability(6, 6e9)` ≈ 0.106; it is capped at 1.
- `Mixer` is a bijection on the whole domain (the 12-bit test enumerates all 4,096 inputs), deterministic for a
  given `(bits, seed)`, and consecutive ids never produce adjacent codes.
- `MemoryCounter.CompareAndSwap` is atomic: of 200 concurrent `CompareAndSwap(0, 1000)` calls exactly one wins.
- `Allocator.Reserve` retries a lost swap (another node moved first), returns `ErrContended` after `MaxRetries`
  losses, `ErrExhausted` at the limit, the store's error (wrapped) when the counter fails, and `ctx.Err()` when
  cancelled. A failed reservation never advances the counter.
- `Generator.Next` is unique across 4 generators × 8 goroutines sharing one counter, costs one `Load` and one
  `CompareAndSwap` per 1,000 ids with a range of 1,000, hands out no id during a counter outage, and a replacement
  node after a crash starts at the next range (ids 3..99 of a range of 100 are wasted, never reused).
- `RandomCode` uses rejection sampling (bytes ≥ 248 are discarded), so the fixed-source test sees every
  character exactly 160 times. `RandomGenerator.Next` reports the number of inserts, returns
  `ErrTooManyCollisions` after `MaxAttempts`, and returns a store error at once rather than retrying it.
- With 2-character codes (3,844 possibilities) 16 goroutines claim 1,600 codes with no duplicates, and the
  inserts outnumber the codes (collisions happened and were retried).

**Acceptance:** `go test -race ./labs/c1/keygen/` passes; then `sd lab C1`.

### Break-it exercises (Unit 1 §5)

All three are behind the `breakit` build tag, so normal runs ignore them. Predict the numbers before each run.

1. **Check-then-write counter.**
   `go test -tags breakit -run BreakIt_RacyCounter -v ./labs/c1/keygen/` runs your `Allocator` against a counter
   whose "compare-and-swap" is a blind write after a yield, which is what `SELECT value; UPDATE counter SET value =
   $new` does without `WHERE value = $old`. Reference run: 800 reservations, **711 ranges handed out twice
   (88.9%)**, each one 10 duplicate short codes. Your `Allocator` is correct; the store primitive is not, and no
   retry loop can fix that.
2. **Range size.** `go test -tags breakit -run BreakIt_StoreOps -v ./labs/c1/keygen/` prints counter operations
   per 1,000 ids and the ids one crash can waste, for range sizes 1 to 10,000 (2,000 ops per 1,000 ids at size 1;
   2 at size 1,000). Pick the size for 120 creates/s at peak with a counter that does ≈ 10k ops/s and a keyspace of
   3.5 × 10^12, and say what a deploy of 20 nodes wastes.
3. **Random collision rate.** `go test -tags breakit -run BreakIt_RandomCollision -v ./labs/c1/keygen/` fills a
   3-character keyspace to 10%, 30%, 50%, 70% and 90% and prints inserts per claimed code against the `1/(1 − p)`
   prediction (≈ 1.05, 1.25, 1.67, 2.5, 5.0) and the worst single code (75 attempts at 90%). Decide where you would
   stop using random codes and switch to a counter, and what `MaxAttempts` should be at that occupancy.
4. **Mutations** (one at a time, in your own code; predict which test fails and what the production symptom is):
   remove the `id != 0` check at the end of `Encode`; make `Reserve` reload only once (no loop); let `Next` keep
   the id it was about to hand out when `Reserve` fails; use `b % 62` without rejecting bytes ≥ 248 in
   `RandomCode`; make `MemoryInserter.Insert` check and then insert under two separate lock acquisitions.

Vet both build configurations: `go vet ./labs/c1/... && go vet -tags breakit ./labs/c1/...`.
