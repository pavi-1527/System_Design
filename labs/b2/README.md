# Labs — B2 Patterns that matter

Six graded kata packages, one per pattern, plus a set of intentionally broken programs (`breakit`). Every graded package
has a stub here (compiles, returns zero values or `errNotImplemented`, never panics or blocks) and a reference solution under
`solutions/b2/<pkg>/`. Test files are byte-identical between the two, so passing the tests here means you match the reference
behaviour. Don't read the solutions until your tests pass, or until you've been stuck for 30 minutes and have written down what
you're stuck on.

Run everything the gate checks with:

```sh
sd lab B2                       # runs: go test -race ./labs/b2/...   (breakit is excluded by its build tag)
go test -race ./labs/b2/...     # the same thing by hand
```

Every test that waits has a timeout (5 s), so a wrong implementation fails with a message instead of hanging. The stubs fail in
milliseconds. Before you call a kata done, run it a few times to shake out schedule-dependent bugs:
`go test -race -count=20 ./labs/b2/<pkg>/`.

Requirements: Go 1.22+ (tested with 1.24) and a working race detector (cgo enabled and a C compiler on Linux; `go env
CGO_ENABLED` should print `1`). Standard library only.

Each kata is small (a solution is ≈ 80-220 lines including the types the stub gives you) but the tests check **properties of
the pattern**, not only examples: a new fee type / provider / vendor / variant defined *in the test file* must work without
changes to your code; illegal transitions must be rejected and leave no trace; decorators must compose in the documented order.
Read the test names first. They are the specification.

## Suggested order

| # | Package | Unit § | Goal | Edit | Run |
|---|---|---|---|---|---|
| 1 | `strategy` | §1 | Fee engine: `Flat`, `Percent` (half-up in integer maths, floor/cap, overflow), marginal `Tiered`, `Sum`; `Engine` with runtime `Register`, strategy called without the lock held, errors that keep kind and cause | `strategy/strategy.go` | `go test -race ./labs/b2/strategy/` |
| 2 | `observer` | §2 | Event bus: ordered sync callbacks with no lock held, re-entrancy, idempotent unsubscribe, panic containment; channel subscription that drops (and counts) instead of blocking and closes safely | `observer/observer.go` | `go test -race ./labs/b2/observer/` |
| 3 | `state` | §3 | Transfer lifecycle table with a reversal-window guard, duplicate vs illegal events, atomic `Apply`, history copy, `Allowed`, `IsTerminal` | `state/state.go` | `go test -race ./labs/b2/state/` |
| 4 | `factory` | §4 | Provider registry: fail-closed `New` with helpful errors, duplicate rejection, constructor outside the lock with copied config and contained panics, `Build` that reports every bad tenant | `factory/factory.go` | `go test -race ./labs/b2/factory/` |
| 5 | `adapter` | §5 | Two vendor APIs behind one `Charger` port: integer money rendering, error taxonomy with `%w` twice, fail-closed on unknown codes, ctx handling; both adapters pass one contract suite | `adapter/adapter.go` | `go test -race ./labs/b2/adapter/` |
| 6 | `decorator` | §6 | `Chain` (first = outermost), `WithRetry`, `WithBreaker` (single half-open probe), `WithTimeout`, `WithLogging`; order tests with counted vendor calls | `decorator/decorator.go` | `go test -race ./labs/b2/decorator/` |
| 7 | `breakit` | §2, §3, §4, §6 | Read a deadlock profile, a broken transition table, an init-order panic and a decorator-order failure | `breakit/...` | see below |

## Lab notes

**strategy.** The `FeeFunc` type, error values and `Txn` are given. The two tests that carry the lesson are
`TestEngine_StrategyRunsWithoutTheEngineLock` (a strategy is parked while a writer registers) and
`TestTiered_FeeNeverDecreasesAsAmountGrows` (a "whole amount at the tier's rate" implementation passes every hand-picked example
and fails this one). Rounding: half up, in integer maths, once.

**observer.** The tests use channels and `within(...)` guards instead of sleeps. Copy-on-write is the simplest way to let a
`Publish` iterate a snapshot while callbacks subscribe and unsubscribe. `SubscribeChan`'s hard part is closing: delivery and
close must be serialised or `-race -count` will eventually find `send on closed channel` (the test runs 200 rounds).

**state.** The transition table is yours to design. The test walks all 20 `(state, event)` cells against an independently
written matrix, so a missing or extra edge shows up as a named cell. A refused move (illegal, duplicate or guard) must leave
state and history untouched. The reversal boundary is inclusive: exactly 24 h is allowed, +1 ns is not. The clock is injected
through `New`.

**factory.** `Provider`, `Config`, `Constructor` and the error values are given. The test providers (`acme`, `pixelcorp`, a
`failover` that composes two others through the registry) exist only in the test file. That is the extension property.
Constructors run outside the registry lock, receive a copy of the config, and may panic or return `(nil, nil)`.

**adapter.** Both vendor wire formats, both client interfaces and the port are given. The test file contains a scripted fake for
each vendor client and one scenario table that is run against **both** adapters. If one adapter is right and the other is wrong
for a scenario, the failing subtest name says which. `fmt.Errorf` supports two `%w` verbs since Go 1.20.

**decorator.** `ClientFunc`, `Decorator`, `ErrOpen` and the config types are given. No test sleeps: retry takes an injected
`Sleep`, the breaker and logger take injected clocks. The concurrency test fires 32 goroutines at a half-open breaker and
requires exactly one probe. Two order tests document the numbers from Unit §6 (3 vendor calls versus 9).

## Break-it exercises (`breakit/`, build tag `breakit`)

Every file under `labs/b2/breakit/` starts with `//go:build breakit`, so normal `go test`, `go vet` and `sd lab B2` runs never
compile it. Predict what the test or program will report **before** each command, then read the output, then fix the code
until it passes. Line numbers below are from the unmodified files.

### 1. Observer: callback under a lock (Unit §2)

```sh
go test -tags breakit -run '^TestNaiveBus' ./labs/b2/breakit/
```

Expect, after 2 seconds:

```
--- FAIL: TestNaiveBus_CallbackThatSubscribesDeadlocks (2.00s)
    breakit_test.go:62: Publish did not return within 2s: a subscriber called Subscribe while Publish held the lock.
        Read the profile below: find the goroutine parked in sync.(*Mutex).Lock and the callback above it.
        goroutine profile: total 3
        ...
        1 @ ... sync.(*Mutex).Lock ... breakit.(*naiveBus).Subscribe ... TestNaiveBus_...func1.1 ... (*naiveBus).Publish
```

There is no data race and `go vet` is clean: it is a lock-order bug on a single goroutine. Fix: copy the subscriber slice under
the lock, release it, then call the subscribers.

### 2. State: a transition table with holes (Unit §3)

```sh
go test -tags breakit -run '^TestBrokenTable' ./labs/b2/breakit/
```

Expect:

```
--- FAIL: TestBrokenTable_MatchesTheBusinessLifecycle (0.00s)
    missing edge: authorised --fail--> (a transfer here can never take this step)
    missing edge: settled --reverse--> (a transfer here can never take this step)
    unexpected edge: settled --reverce--> (not part of the lifecycle; likely a typo)
--- FAIL: TestBrokenTable_EveryStateIsReachableAndEveryNonTerminalStateHasAWayOut (0.00s)
    state "settled" is a dead end but is not terminal: transfers pile up here forever
    state "reversed" is unreachable from initiated: no sequence of the table's events leads there
```

Fix the table (it is a plain `map[string][]string` at the top of the file) until both tests pass. Which test would have caught
each bug on its own?

### 3. Factory: init-order surprise (Unit §4)

```sh
go run -tags breakit ./labs/b2/breakit/initorder
```

Expect (the exact addresses differ):

```
panic: unknown provider "acme" (registered: 0)

goroutine 1 [running]:
main.mustGet({0x..., 0x4})
	.../labs/b2/breakit/initorder/main.go:34 +0x...
main.init()
	.../labs/b2/breakit/initorder/main.go:39 +0x...
exit status 2
```

It dies before `main` runs: package-level variables are initialised before any `init()` function. Fix it twice (lazy lookup;
explicit registration from `main`) and decide which you would ship.

### 4. Decorator: where the retry sits (Unit §6)

```sh
go test -tags breakit -run '^TestDecoratorOrder' ./labs/b2/breakit/
```

Expect:

```
--- FAIL: TestDecoratorOrder_BreakerMustBoundVendorCalls (0.00s)
    breakit_test.go:209: the vendor was called 9 times before the circuit opened; the contract allows 3. Which wrapper sees
    each attempt, and which sees only the logical call?
```

Fix: swap the two wrappers in the `chain := ...` line, then explain what the breaker counts now and what that costs.

### Vet

`go vet -tags breakit ./labs/b2/...` is clean on purpose: none of these bugs is one `go vet` finds.

## Break it in your own katas

Each unit section's "Break it" also mutates *your* passing solution. Do those, watch which tests fail and why, then revert:

| Kata | Mutation | Test that should notice |
|---|---|---|
| `strategy` | hold the engine's read lock while calling the strategy | `TestEngine_StrategyRunsWithoutTheEngineLock` |
| `strategy` | charge the whole amount at the landing tier's rate | `TestTiered_FeeNeverDecreasesAsAmountGrows` |
| `observer` | send and close without the subscription mutex | `TestSubscribeChan_CancelRacingPublishNeverPanics` |
| `observer` | remove a subscriber by slice index | `TestUnsubscribe_StopsDeliveryAndIsIdempotent` |
| `state` | drop the mutex in `Apply` | `TestApply_ConcurrentDuplicateSettleSucceedsExactlyOnce` (with `-race`) |
| `factory` | hold the registry lock while constructing | `TestNew_ConstructorRunsWithoutTheRegistryLockAndMayReenter` |
| `adapter` | map an unknown code to `Approved` | `TestVendorA_ResponseCodeTable`, the contract suite |
| `adapter` | wrap the vendor error with `%v` instead of `%w` | `TestContract_VendorErrorStaysReachableForLogging` |
| `decorator` | `time.Sleep` for the backoff | `TestRetry_StopsWaitingWhenTheCallerGivesUp` |
| `decorator` | let any success close the breaker | `TestBreaker_LateSuccessFromBeforeTheTripDoesNotCloseTheCircuit` |
