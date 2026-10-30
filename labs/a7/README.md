# A7 labs

Four Go packages, stdlib only, no network at all (time is injected, vendors are simulated). Each stub compiles and
returns immediately (zero values or `errNotImplemented`), so every test fails fast until you implement it. Reference
solutions live in `solutions/a7/`; the `*_test.go` files there are byte-identical to the ones here — don't edit the
tests, edit the code.

Run everything the gate checks with `sd lab A7` (it runs `go test -race` over the core packages and reports `bulkhead`
separately). While working on one package, run it directly as shown below.

Every test is deterministic: clocks and randomness are injected, "waiting" is a fake sleep that moves a fake clock,
and every real wait has a timeout so a wrong implementation fails with a message instead of hanging. Concurrency tests
use real goroutines, so run them under the race detector, and before calling a lab done run
`go test -race -count=20 ./labs/a7/<pkg>/` once.

| Order | Package | Unit | Core/Stretch |
|---|---|---|---|
| 1 | [`retry`](retry/) | 1 (§2–§3) | CORE |
| 2 | [`breaker`](breaker/) | 2 (§1) | CORE |
| 3 | [`ratelimit`](ratelimit/) | 2 (§2–§3) | CORE |
| 4 | [`bulkhead`](bulkhead/) | 3 (§1–§2) | STRETCH |
| — | [`capstone`](capstone/), [`sim`](sim/) | 1–3 | break-it only (build tag `breakit`) |

The `capstone` and `sim` directories contain only `//go:build breakit` files, so normal `go test`, `go vet` and
`sd lab A7` never compile them. `go vet -tags breakit ./labs/a7/...` type-checks them.

---

## 1. `retry` — retry executor with jitter, deadlines and a retry budget

**Goal.** One function a vendor adapter could actually use: `Do` retries an operation with exponential backoff and
jitter, never beyond the caller's deadline, only when that is safe (idempotent or keyed, or provably never sent), and
only while a shared `Budget` says the dependency can afford it. Plus the arithmetic underneath (`Delay`), a
`Retry-After` parser, and the budget itself.

**Edit** `labs/a7/retry/retry.go`. Provided: `Jitter`, the sentinel errors, `GiveUpError`, the `Permanent`/`NotSent`
markers, `RetryAfterError`, `Config`, `DoErr`. Implement, in order:

1. `Delay` (no jitter, full, equal, decorrelated; overflow-safe).
2. `Do` with attempts and backoff only (defaults, pre-check of `ctx`, the loop).
3. Classification: `Permanent`, the idempotency gate (`Idempotent` / `IdempotencyKey` / `NotSent`), `IsRetryable`.
4. The deadline budget: per-attempt timeout capped by what is left, and "does the wait fit?" before sleeping.
5. `RetryAfterError` as a floor on the delay; `ParseRetryAfter`.
6. `Budget` (`NewBudget`, `Deposit`, `Withdraw`, `Tokens`) and its use in `Do` (deposit once per call, withdraw before
   every retry, after the deadline check).

**Run:** `go test -race ./labs/a7/retry/`

**Break it:**
- `go test -tags breakit -run BreakIt_Amplification -v ./labs/a7/retry/` — dependency calls per user action for
  1–4 retrying layers × 0–3 retries, then the same stacks behind one shared 10% budget. Predict the k=3, r=2 cell
  first. Unit 1 §3 has the expected table.
- `go test -tags breakit -run BreakIt_Jitter -v ./labs/a7/retry/` — 100 clients contending for one slot per 10 ms:
  total calls and completion time for each jitter strategy, using your `Delay`.
- Mutations (do one at a time in your own code, run the suite, name the production incident each one is):
  - remove the deadline check before sleeping → `RetryAfterBeyondTheDeadline…`, `ABackoffThatDoesNotFit…`,
    `TotalWaitingNeverExceeds…`, `ABudgetTokenIsNotSpent…` fail;
  - treat `Retry-After` as a ceiling instead of a floor → `RetryAfterIsAFloorNotACeiling` fails;
  - drop the idempotency gate → `NonIdempotentCallWithoutAKeyIsNotRetried` and
    `ATimeoutOnANonIdempotentCall…` fail;
  - remove the mutex from `Withdraw` → `ConcurrentWithdrawalsNeverOverspend` fails (run it with `-race` too);
  - deposit once per *attempt* instead of once per call → `ManyCallersShareOneBudgetUnderRace` fails (the budget stops
    bounding anything).
- `go run -tags breakit ./labs/a7/sim backoff` and `… sim timeouts [-propagate]` and `… sim metastable` — the
  standalone simulations behind Unit 1 §1–§3 (no lab code needed).

---

## 2. `breaker` — circuit breaker

**Goal.** Closed → open → half-open with a rolling window, a failure-ratio rule and a consecutive-failure rule,
limited half-open probes, outcome classification (a cancelled caller is not a failing vendor), and a state-change
callback — safe for concurrent use and testable with a fake clock.

**Edit** `labs/a7/breaker/breaker.go`. Provided: `State`, `Outcome`, `ErrOpen`, `Config`. Implement `New` (defaults),
`State`, `Counts`, `Allow`, `Do`. The `Breaker` doc comment lists the design the tests assume: bucketed rolling window,
lazy Open → HalfOpen, a generation number that drops results from before a state change, a cleared window on close,
callbacks outside the lock.

**Run:** `go test -race ./labs/a7/breaker/`

**Break it:**
- `go test -tags breakit -run BreakIt -v ./labs/a7/breaker/` — a 60 s vendor outage at 100 req/s with and without a
  breaker (calls into the dead vendor, when the breaker opens, recovery time), five trip rules compared by detection
  speed, and the `OpenFor` trade-off. Predict first; Unit 2 §1 has the expected numbers.
- Mutations: remove the generation check → `ResultsFromBeforeAStateChangeAreIgnored`; remove the probe limit →
  `HalfOpenAdmitsOnlyHalfOpenMax…`, `ThunderingHerdOnHalfOpen…`; count `context.Canceled` as a failure →
  `ACancelledErrorWithALiveCaller…`; stop ignoring results when the caller's `ctx` is done →
  `CallersOwnDeadlineExpiry…`; keep the window when closing → `ClosingStartsFromACleanWindow`; call the callback
  while holding the lock → `CallbackMayInspectTheBreaker…` (a deadlock caught by a timeout).

---

## 3. `ratelimit` — token bucket and sliding-window limiters

**Goal.** Per-key limiters behind one `Limiter` interface: `TokenBucket`, exact `SlidingWindowLog`, approximate
`SlidingWindowCounter`, each with an injected clock, bounded memory (`MaxKeys`, LRU eviction, `Sweep`), correct
`Retry-After`, all-or-nothing `AllowN`, and safe under concurrent callers — plus the HTTP glue (`429`, headers).

**Edit** `labs/a7/ratelimit/ratelimit.go`. Provided: `Decision`, `Limiter`, the config types and `FixedWindow` (the
baseline with the boundary flaw; it has no key cap on purpose). Implement the three limiters (including `Len` and
`Sweep`), `RetryAfterSeconds`, `Decision.SetHeaders` and `Middleware`. Write the bounded LRU key table once as a small
unexported generic type and use it three times.

**Run:** `go test -race ./labs/a7/ratelimit/`

**Break it:**
- `go test -tags breakit -run BreakIt_BoundaryBurst -v ./labs/a7/ratelimit/` — 100 requests 10 ms before a window
  boundary and 100 more 10 ms after it: how many does each algorithm admit? Predict each row.
- `go test -tags breakit -run BreakIt_CounterVersusLog -v ./labs/a7/ratelimit/` — the approximation on steady, bursty
  and adversarial traffic.
- `go test -tags breakit -run BreakIt_MemoryPerKey -v ./labs/a7/ratelimit/` — 500,000 distinct keys with and without
  `MaxKeys`; 10,000 active keys with a 100-request window for the log vs the counter vs the bucket.
- Mutations: record denied requests in the log → `DeniedRequestsAreNotCounted` and `AllowN…` fail; let the bucket
  bank idle time past `Burst` → `IdleTimeNeverBanksMoreThanBurst`; ignore the previous window in the counter →
  `WeighsThePreviousWindow…`, `RetryAfterIsTheTrueWait`; truncate instead of round up the `Retry-After` seconds →
  `RetryAfterSeconds_RoundsUp`; remove the key cap → `MemoryIsBoundedByMaxKeys`.
- `go run -tags breakit ./labs/a7/sim distlimit` — local vs shared counters across 10 nodes (Unit 2 §3).

---

## 4. `bulkhead` — bulkheads and adaptive concurrency (STRETCH, not gated)

**Goal.** A semaphore `Bulkhead` with a bounded FIFO wait queue that never leaks a slot (even when a waiter is
cancelled in the same instant a slot is handed to it), a `Group` with one bulkhead per dependency, and an `AIMD`
adaptive concurrency limit with a cooldown and a "grow only under load" rule.

**Edit** `labs/a7/bulkhead/bulkhead.go`. Provided: errors, `Config`, `Stats`, `Outcome`, `AIMDConfig`. Implement
`Bulkhead` (`New`, `Acquire`, `Do`, `Stats`), `Group`, `AIMD`.

**Run:** `go test -race ./labs/a7/bulkhead/`

**Break it:**
- `go test -tags breakit -run BreakIt_SharedVersusPerVendor -v ./labs/a7/bulkhead/` — a shared pool vs a bulkhead
  per vendor, with the other vendor slow, hung, and hung with an adapter that ignores its context. ≈ 25 s of real
  time. Predict the healthy vendor's success rate in each case.
- `go test -tags breakit -run BreakIt_AIMD -v ./labs/a7/bulkhead/` — the limit chasing a server whose capacity moves
  from 40 to 15 to 60.
- Mutations: don't pass a granted slot on when a waiter cancels → `CancellationRacesNeverLeakSlots`; serve waiters
  LIFO → `QueueIsBoundedAndServedInArrivalOrder`; remove the AIMD cooldown → `OverloadHalvesTheLimitOncePerCooldown`;
  let the limit grow while idle → `SuccessesWhileIdleDoNotInflateTheLimit`.

---

## 5. `capstone` — the resilient vendor adapter (break-it only)

`go test -tags breakit -run Capstone -v ./labs/a7/capstone/` composes **your** `retry`, `breaker`, `ratelimit` and
`bulkhead` packages into one adapter (retry with a shared budget → breaker → bulkhead → limiter → call) and runs it
against a simulated vendor — healthy, 30% errors, and down for two seconds — next to a naive "retry 3 times"
adapter. It needs all four labs passing, takes ≈ 40 s of real time, and prints the table Unit 3 §5 discusses. Predict
the "vendor calls" and "failed" columns for each row before you run it.

## Reference

- Solutions: `go test -race ./solutions/a7/...`
- Break-it code is behind the `breakit` build tag, so default runs ignore it: `go vet -tags breakit ./labs/a7/...`
  type-checks it. To run a break-it experiment against the reference solutions instead of your code, use
  `go test -tags breakit -run BreakIt -v ./solutions/a7/<pkg>/`.
