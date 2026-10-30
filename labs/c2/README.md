# C2 labs

One Go package, stdlib only, no network. The stub compiles and returns immediately (zero values or
`errNotImplemented`), so every test fails fast until you implement it. The reference solution lives in
`solutions/c2/`; the `*_test.go` files there are byte-identical to the ones here — don't edit the tests, edit the
code.

Run everything the gate checks with `sd lab C2` (it runs `go test -race` over the package and records the result).
While working, run the package directly as shown below. Before calling it done, run
`go test -race -count=10 ./labs/c2/distlimit/` once: the atomicity and lease tests use real goroutines.

This lab is the *distributed* half of rate limiting. The single-process algorithms (token bucket, sliding windows),
the `Retry-After` and `RateLimit-*` headers and the 429 middleware are A7's lab (`labs/a7/ratelimit`); nothing here
repeats them.

| Order | Package | Unit | Core/Stretch |
|---|---|---|---|
| 1 | [`distlimit`](distlimit/) | 1 (§5–§7) | CORE |

---

## 1. `distlimit` — one limit, many gateways, one shared store

**Goal.** The component every rate-limiter design hand-waves: forty gateways enforcing one limit per key against a
shared store, with one atomic step per decision, a bounded cost for the hottest key, and a failure policy that keeps
the limiter from becoming the outage. Four pieces that compose
(`Guard{Primary: LeaseClient{Pool: Limiter{Store: MemoryStore}}}`):

1. **`MemoryStore`**: the shared counter store (Redis, DynamoDB, etcd) reduced to `Load` and an atomic
   `CompareAndSwap` with a TTL that runs on the store's clock, plus `Fail` (fault injection), `Ops` and `Len`.
2. **`Limiter`**: GCRA, one `int64` per key (the theoretical arrival time). Every decision is a `Load` and, when
   admitted, a `CompareAndSwap` against the loaded value, retried when another gateway moved first. Denials never
   write. `Refund` gives tokens back.
3. **`Guard`**: wraps any `Decider` with a timeout and a failure mode (fail open, fail closed, or a node-local
   fallback at a scaled limit), and counts degraded decisions.
4. **`LeaseClient`**: takes a batch of tokens from the shared `Limiter` in one decision, spends them locally,
   caches denials until a batch could be available, drops leases after a TTL and refunds them on `Close`.

**Edit** `labs/c2/distlimit/distlimit.go`. Provided: the errors and defaults, `Clock`/`SystemClock`, `Limit`
(`Interval`, `Valid`), `Source`, `Decision`, the `Decider`, `Store` and `Pool` interfaces, the config types,
`FailMode`, and the `checkRequest`/`badArg` helpers. Implement, in order (each step's tests run alone with `-run`):

1. `NewMemoryStore`, `Load`, `CompareAndSwap`, `Fail`, `Ops`, `Len`:
   `go test -race -run MemoryStore ./labs/c2/distlimit/`
2. `NewLimiter`, `Limiter.Decide`, `Limiter.Refund`: `go test -race -run Limiter ./labs/c2/distlimit/`
3. `NewGuard`, `Guard.Decide`, `Degraded`: `go test -race -run Guard ./labs/c2/distlimit/`
4. `NewLeaseClient`, `LeaseClient.Decide`, `Held`, `Close`: `go test -race -run LeaseClient ./labs/c2/distlimit/`
5. Everything: `go test -race ./labs/c2/distlimit/`

(Steps 3 and 4 are independent; the unit teaches leases in §6 and the Guard in §7, so either order works.)

**Documented behaviour the tests check:**
- `MemoryStore.CompareAndSwap` is atomic: of 200 concurrent swaps from `0`, exactly one wins. An expired key
  compares as `0`; `new == 0` deletes; a TTL of 0 never expires; TTLs run on the store's clock. Failed calls count
  in `Ops`.
- `Limiter.Decide` with rate 10/s, burst 5: five instant admissions with `Remaining` 4, 3, 2, 1, 0 and `ResetAfter`
  100–500 ms; the sixth is denied with `RetryAfter` exactly 100 ms. Cost `n` charges `n` tokens; `n` of 0, a negative
  `n`, `n > Burst` and an invalid limit are `ErrBadArgument`. Denials cost one `Load` and no write. Keys expire when
  their bucket is full (1,000 one-shot keys are gone 100 ms later). A TAT in the past is clamped to `now` (a key idle
  for an hour admits exactly its burst, not 36,000 requests).
- Four `Limiter`s sharing one store, 32 goroutines and 1,600 requests against a burst of 100 at a frozen instant
  admit **exactly 100**. A swap that loses is retried after a fresh `Load`; `MaxRetries` 2 makes exactly 3 attempts
  before `ErrContended`. Store errors are wrapped (`errors.Is` works) and never turned into decisions.
- `Refund` returns tokens but never overfills: refunding 100 tokens to a burst-5 key deletes it, after which
  exactly 5 are admitted. A refund on an absent key costs one `Load`.
- `Guard` passes denials through (a denial is not an outage). During an outage: fail open admits with Source
  `fail-open`; fail closed denies with the configured `ClosedRetryAfter`; fail local admits exactly 20 of 50 against
  burst 100 with `FallbackScale` 0.2. A store that never answers is cut off at the `Timeout` (16 concurrent calls
  all return, well under a second). `ErrBadArgument` and the caller's own cancellation pass through, uncounted.
- `LeaseClient` with a batch of 10 and a burst of 100: 100 admissions cost exactly **20** store ops (10 leases);
  the 101st is denied with `RetryAfter` 10 s (the wait for a batch at `T` = 1 s) for one `Load`; later denials come
  from the deny cache with the remaining wait and cost nothing. When a batch doesn't fit, the client takes what's
  left (25 of 25 with a batch of 10). Held tokens vanish at the lease TTL. Four clients × 8 goroutines conserve
  tokens exactly (`admitted + held == burst`) and hold fewer than `nodes × batch`. `Close` refunds unspent tokens
  so the next node gets them. A 5-node simulation stays within `burst + rate + nodes × batch` in every second and
  `burst + rate × 10 s` overall, costs at most two thirds of the no-lease store ops at 3× overload, and under the
  limit rejects ≤ 0.1% at ≈ 2 ops per batch.

**Acceptance:** `go test -race ./labs/c2/distlimit/` passes; then `sd lab C2`.

### Break-it exercises (Unit 1 §5–§7)

All four are behind the `breakit` build tag, so normal runs ignore them. Predict the numbers before each run.

1. **Non-atomic store** (§5).
   `go test -tags breakit -run BreakIt_NonAtomicStore -v ./labs/c2/distlimit/` runs your `Limiter` against a store
   whose "compare-and-swap" is a blind write after a yield, which is what `GET` → compute → `SET` does across a
   network. Reference run: 1,600 requests against a burst of 100, **1,598 admitted (16×)**; your count will vary and
   always be far above 100. Your `Limiter` is correct; the store primitive is not.
2. **Clock skew** (§5). `go test -tags breakit -run BreakIt_ClockSkew -v ./labs/c2/distlimit/` lets two gateways
   compute GCRA from their own clocks, B running 0 ms to 1 s ahead, limit 100 req/s with a burst of 20. Reference:
   over the limit the total stays at ≈ 102/s while A's share falls from 50/s to **0.7/s at 50 ms of skew**; under the
   limit, wrong rejections jump from 0.9% to **24%** once the skew reaches the burst tolerance (200 ms).
3. **Lease size** (§6). `go test -tags breakit -run BreakIt_LeaseSize -v ./labs/c2/distlimit/` sweeps the batch
   (none, 1, 5, 20, 50, 100) for 10 nodes sharing 1,000 req/s (burst 200): wrong rejections and store ops under the
   limit, the busiest second and store ops over it, a synchronized burst after a quiet period, and a fleet with one
   hot node. Reference: a batch of 20 cuts store ops under the limit from 1,614/s to 82/s with no wrong rejections;
   a batch of 50 already breaks the 0.1% NFR (0.19%); the synchronized burst admits `200 + 10 × (batch − 1)` (1,190
   at a batch of 100).
4. **Store outage** (§7). `go test -tags breakit -run BreakIt_StoreOutage -v ./labs/c2/distlimit/`: a tenant
   storming at 3,000 req/s against a 1,000 req/s limit and a well-behaved tenant at 500 req/s, store down for 5 s.
   Reference (admitted/s during the outage): fail open 2,994 and 495; fail closed 0 and 0; fail local (scale 2/N)
   2,074 and 495.
5. **Mutations** (one at a time, in your own code; predict which test fails and what the production symptom is):
   compare-and-swap against the clamped `tat` instead of the loaded value; write the new TAT on a denial too;
   remove `max(stored, now)`; take the store's lock twice in `CompareAndSwap` (once to compare, once to write);
   drop the `context.WithTimeout` in `Guard.Decide`; fail open on every error including `ErrBadArgument`; remove
   the deny cache; make `Close` forget held tokens instead of refunding them; hold the client-wide mutex across the
   pool call in `LeaseClient.Decide`.

Vet both build configurations: `go vet ./labs/c2/... && go vet -tags breakit ./labs/c2/...`.
