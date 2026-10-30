# C7 labs

One Go package, stdlib only, no network. The stub compiles and returns immediately (zero values or
`errNotImplemented`), so every test fails fast until you implement it. The reference solution lives in
`solutions/c7/dispatch/`; the `*_test.go` files there are byte-identical to the ones here — don't edit the tests,
edit the code. Look at the solution only after a real attempt.

Run everything the gate checks with `sd lab C7` (it runs `go test -race` over the package). While working, run the
package directly as shown below. Before you call it done, shake out schedule-dependent bugs with
`go test -race -count=20 ./labs/c7/dispatch/`.

| Order | Package | Unit | Core/Stretch |
|---|---|---|---|
| 1 | [`dispatch`](dispatch/) | 2 (§5–§6) | CORE |

---

## 1. `dispatch` — the notification worker's core

**Goal.** The hardest component of the notification service as a single, testable type: the dispatcher that sits
between "a notification was requested" and "a provider accepted it". It deduplicates by notification ID, keeps an
urgent lane ahead of a normal lane, applies opt-out, quiet hours and a per-user frequency cap to normal
notifications only, fails over across an ordered provider chain with one circuit breaker per provider, bounds every
provider call with a timeout, retries with backoff across passes, dead-letters what can't be delivered, and records
every outcome. There are no background goroutines: `Process` does one pass over what is due at the injected clock's
time, which is what makes the tests deterministic.

**Edit** `labs/c7/dispatch/dispatch.go`. Provided: every type, the heaps (`itemHeap`), `push`, `requeue`, `record`,
`deadLetter`, `rateKey`, `copyRecords`. Implement, in order:

1. **Part 1 — accept and order** (Unit 2 §6): `New` (validation, defaults, one breaker per provider name, the
   limiter), `Enqueue` (atomic check-and-insert into the dedupe set, push onto the lane), `popDue`, `Process`,
   `Pending`, `NextDue`, `Log`, `DeadLetters`, `Counts`, and the `limiter.allow` token bucket. With `handle` still
   empty, `TestEnqueue_*`, `TestDefaultBackoff_*` and `TestNew_*` are your first targets.
2. **Part 2 — suppression rules** (Unit 2 §6): the first half of `handle` — opt-out, quiet hours (defer, don't drop),
   rate limit — for `Normal` only. Targets: `TestProcess_OptedOutChannelIsRecordedNotSent`,
   `TestProcess_QuietHoursDeferNormalAndPassUrgent`, `TestProcess_RateLimitSuppressesBeyondBurstAndRefills`,
   `TestProcess_UrgentBeforeNormalThenFIFO`.
3. **Part 3 — providers** (Unit 2 §5): `call` (timeout + contained panic), the breaker (`allow`, `availableAt`,
   `success`, `failure`; evaluated lazily from the clock, no goroutines), and the provider loop in `handle`:
   failover in the same pass, bounded attempts, backoff between passes, dead-letter with history. Targets:
   `TestProcess_FailsOverToNextProvider`, `TestProcess_SendTimeoutCountsAsFailureAndFailsOver`,
   `TestProcess_ProviderPanicIsContainedAsFailure`, `TestBreaker_OpensAfterThresholdAndHalfOpensAfterCooldown`,
   `TestProcess_ExhaustedAttemptsGoToDeadLettersWithHistory`, `TestProcess_NoProviderForChannelDeadLettersImmediately`,
   `TestProcess_RetryWaitsForBreakerCooldownWhenAllProvidersAreOpen`.
4. **Concurrency**: `TestProcess_ConcurrentEnqueueAndProcessDeliverEachOnce` runs 8 producers against 2 `Process`
   loops under `-race`; `TestEnqueue_ConcurrentDuplicatesAreAcceptedOnce` sends the same retry 50 times at once.
   Never hold `d.mu` across a provider call.

**Run:** `go test -race ./labs/c7/dispatch/` (or `-run 'Enqueue|Backoff|New'` while on part 1).

**Contract details the tests pin down** (all in the doc comments):

- `Urgent` bypasses opt-out, quiet hours and the rate limit, and is dequeued before any due `Normal` notification.
  The lab's simplification of "security and regulatory messages are not subject to marketing preferences".
- Quiet hours **defer** (the record says `deferred` with `NextAt`); opt-out and the rate limit **drop** (recorded as
  `opted_out` / `suppressed`, never retried). Preferences are evaluated at send time, not at enqueue time: the
  opt-out test flips the preference *after* `Enqueue`.
- One failed provider call is one attempt; failover to the next provider happens in the same pass; when the pass ends
  without delivery and attempts remain, the notification is requeued at `now + Backoff(retries)`, or at the earliest
  time an open breaker may admit a probe if no provider could be called at all. `MaxAttempts` provider calls in
  total, then the dead-letter list, with every attempt (time, provider, error) attached.
- The breaker admits exactly one half-open probe at a time; a failed probe re-opens it for another cooldown; a
  successful probe closes it. `FailureThreshold: 0` disables breakers (used by the first break-it).
- A provider that panics is a failed attempt. A provider that ignores `ctx` is cut off by `SendTimeout`.

**Break it** (build tag `breakit`; the file is `labs/c7/dispatch/breakit_test.go`, it never fails, it prints):

1. *A dead provider with and without a breaker.* Predict both counts first, then run
   `go test -tags breakit -run '^TestBreakIt_DeadProviderWithAndWithoutBreaker$' -v ./labs/c7/dispatch/`.
   1,000 urgent notifications, provider chain `[dead, backup]`, once with `FailureThreshold: 0` and once with `3`.
   Multiply the calls to the dead provider by a 2 s timeout: that is the worker time an outage burns without a
   breaker (Unit 2 §5 does the Little's-law version of this).
2. *Zero backoff.* `go test -tags breakit -run '^TestBreakIt_ZeroBackoffRetryStorm$' -v ./labs/c7/dispatch/`:
   100 notifications, a provider that always fails, `MaxAttempts: 5`, `Backoff: 0`. Every attempt lands in one
   pass at one timestamp — the retry wave that hits a vendor the moment it recovers. Then set `Backoff` to
   `DefaultBackoff` and watch the attempts spread out across passes (you'll have to advance the clock).
3. *Preferences at the wrong time.* Move the opt-out check from `handle` into `Enqueue` and run
   `go test -race -run OptedOut ./labs/c7/dispatch/`: the member who opts out while the campaign is queued gets
   the message anyway.
4. *Lose the lock.* Comment out the `d.mu.Lock()`/`Unlock()` pair in `Enqueue` and run
   `go test -race -run Concurrent ./labs/c7/dispatch/`. Read the race report, then count how many times the
   passcode was delivered.
5. *A breaker per channel instead of per provider.* Key `d.breakers` by channel+name and give the same provider
   to `SMS` and `Push`. Nothing in the tests breaks; explain what changes during a vendor outage (hint: how many
   probes per cooldown hit the vendor, and how long the push channel keeps trying a vendor that SMS already knows
   is down).
