# Labs — B1 Pragmatic OOD in Go

Two graded refactoring labs plus a set of intentionally broken programs (`breakit`). The syllabus calls the lab
"refactor-a-god-object exercises"; it is split into two independently runnable packages, one per unit, because
the two tangled components teach different things (consumer-side interfaces and constructors in Unit 1; errors,
boundaries and extension points in Unit 2).

Both packages follow the same shape:

- `legacy.go` is **given** and compiles in the stub too. It is the working-but-tangled code you are replacing.
  Do not edit it. The tests call it as an oracle ("characterisation" or parity tests) so you can prove you
  preserved the behaviour that matters and consciously fixed the behaviour that is a bug.
- `<pkg>.go` is the stub of the **target design**. Its exported API and doc comments are the specification; the
  tests are written against it, not against the legacy code. Keeping the legacy code compiling (or calling it from
  your stub) cannot pass the tests.
- `<pkg>_test.go` is byte-identical to `solutions/b1/<pkg>/<pkg>_test.go`. Passing the tests here means you match
  the reference behaviour. Don't read the solutions until your tests pass, or until you've been stuck for 30
  minutes and have written down what you're stuck on.

Run everything the gate checks with:

```sh
sd lab B1                       # runs: go test -race ./labs/b1/...   (breakit is excluded by its build tag)
go test -race ./labs/b1/...     # the same thing by hand
```

Every test that could hang has a deadline, so a wrong implementation fails with a message instead of blocking.
The stubs fail in milliseconds. Before you call a lab done, run it repeatedly to shake out schedule-dependent
bugs: `go test -race -count=20 ./labs/b1/<pkg>/`.

Requirements: Go 1.22+ (tested with 1.24) and a working race detector (`go env CGO_ENABLED` should print `1`).

## Suggested order

| # | Package | Unit § | Goal | Edit | Run |
|---|---|---|---|---|---|
| 1 | `checkout` | U1 §1–§5 | Replace the `OrderManager` god object with `Service`: three consumer-side interfaces (`Payer`, `Stock`, `Notifier`) plus a `Clock`; `New` validates and returns `ErrInvalidConfig`; functional options with copied inputs; `Place` validates before any side effect, prices exactly like the legacy code, rolls stock back on any failure, uses the order ID as the idempotency key, respects `ctx` but does not abandon a paid order, never lets a notification failure fail the order, and is safe for concurrent use | `checkout/checkout.go` | `go test -race ./labs/b1/checkout/` |
| 2 | `breakit` (Unit 1) | U1 §2–§5 | Typed nil in an interface, embedding is not inheritance, a nil map in a zero value, a fat interface, a copied mutex | see below | see below |
| 3 | `dispatch` | U2 §1–§5 | Replace the string-typed, global-state `LegacyDispatch` with a `Dispatcher` over a one-method `Sink`: typed errors (`DeliveryError`, sentinels, `Temporary`/`IsTemporary`), `%w` wrapping and `errors.Join`, injected sleep/clock/backoff, per-attempt timeouts, panic containment, concurrent delivery, a usable zero value, and no lock held while a sink runs | `dispatch/dispatch.go` | `go test -race ./labs/b1/dispatch/` |
| 4 | `breakit` (Unit 2) | U2 §1 | `%v` instead of `%w`; a wrapper type without `Unwrap` | see below | see below |

STRETCH (U1 §6): no lab package. The exercise is in the unit text: write a `Set[T comparable]` and a `Stack` as
generics and as interfaces and compare.

## Lab notes

**checkout.** Read `legacy.go` first and write down (a) every package-level variable a test of it must swap, (b)
every place a caller would have to read an error string, and (c) the three bugs the comments mention. The parity
test (`TestPlace_ParityWithLegacyOrderManager`) runs 12 pricing cases through the legacy code and through your
`Service` with fakes injected and compares subtotal, discount, tax, total, the amount charged, the stock movement
and the notification. `TestLegacy_LeaksStockWhenPaymentDeclined` pins one legacy bug so you can see it; three more tests require the
opposite behaviour (stock released on a decline, earlier reservations rolled back when a later line is out of
stock, a notification failure surfaced as `Notified=false` instead of silently logged). Do not copy the bugs. The fakes in
the test file are 5–15 lines each: that is the evidence that the interfaces are the right size. Note which tests
are about **context**: a request cancelled before charging must release stock, but a cancellation that arrives
while the charge is in flight must still return the paid order, and the notifier must then receive a context that
is not cancelled (`context.WithoutCancel`). `TestPlace_OnlyOneBuyerGetsTheLastUnit` fails for any design that
checks stock and then reserves in two steps.

**dispatch.** The retry decision is the heart of it: `IsTemporary` walks the error chain with `errors.As` and an
interface target and never reads message text (one test returns the error `"gateway timeout: 400 bad request"` and
requires exactly one attempt). The standard library's `context.DeadlineExceeded` implements `Temporary() bool`
and returns true; `Dispatch` must still not retry once the caller's own context is done. Delays come from an
injected backoff and an injected sleeper, so no test waits for real; one test uses a real 20 ms attempt timeout
against a hung sink. `TestDispatch_ReportsEveryFailureAsAJoinedError` requires `errors.Join` so that callers can
see every failed sink. `TestDispatch_SinkMayRegisterAnotherSinkWhileDelivering` fails if the registry lock is held
during `Deliver`. `TestExtension_NewSinkAndDecoratorsNeedNoDispatcherChanges` defines a new sink type and two
decorators (a tenant filter and a budget) **in the test file**: if your Dispatcher needs changing to accept them,
the design is wrong. The zero-value test means `var d Dispatcher` must work without `New`.

## Break-it exercises (`breakit/`, build tag `breakit`)

Every file under `labs/b1/breakit/` starts with `//go:build breakit`, so normal `go test`, `go vet` and
`sd lab B1` runs never compile it. Predict what the tool will report **before** each command, read the output,
then fix the code until the test passes. A panic ends the whole test binary, so run the tests one at a time with
`-run` as shown.

### 1. Typed nil in an interface (U1 §5)

```sh
go test -tags breakit -run '^TestTypedNilError$' ./labs/b1/breakit/
```

Expect:

```
--- FAIL: TestTypedNilError (0.00s)
    typednil_test.go:30: validate("alice") = <nil> (dynamic type *breakit.ValidationError); want a nil error. ...
```

`%v` prints `<nil>` yet the comparison `err != nil` is true: the interface holds `(*ValidationError, nil)`. Fix:
return a literal `nil` on the success path. `go vet` does not catch this.

### 2. Embedding is not inheritance (U1 §2)

```sh
go test -tags breakit -run '^TestEmbeddingIsNotInheritance$' ./labs/b1/breakit/
```

Expect `Describe() = "I am base", want "I am premium"`. The promoted `Describe` runs with the embedded `Base` as its
receiver. Fix: make `Describe` take a small interface (`Namer`) or define it on the outer type.

### 3. The zero value is not useful (U1 §4)

```sh
go test -tags breakit -run '^TestZeroValueRegistry$' ./labs/b1/breakit/
```

Expect `panic: assignment to entry in nil map`, with `Register` at the top of the trace. Fix: allocate lazily in
`Register` (so `var r Registry` works), or force construction through `NewRegistry`; say which and why.

### 4. The fat interface (U1 §3)

Add a sixth method to `Store` in `fat.go`:

```go
Watch(key string) <-chan string
```

then:

```sh
go test -tags breakit -run '^$' ./labs/b1/breakit/
```

(`go vet` stops at the first error; `go test` lists up to ten.) Expect **4** `does not implement Store (missing
method Watch)` errors: `MemStore` plus three test doubles that never call `Watch`. Now fix the design, not the
symptom: declare `type Getter interface{ Get(string) (string, bool) }` next to `Greeting`, change its parameter,
and make the fakes implement one method. Only `MemStore` still has to grow `Watch`. Run the same command again:
zero errors, and the new method touched one type instead of four.

### 5. Copying a lock (U1 §4)

```sh
go vet  -tags 'breakit copylocks' ./labs/b1/breakit/
go test -tags 'breakit copylocks' -run '^TestValueReceiverCounter$' ./labs/b1/breakit/
```

The extra `copylocks` tag keeps this file out of the repo-wide `go vet -tags breakit` run (which must stay clean).
Expect `Inc passes lock by value: ... contains sync.Mutex` from vet, and `Value() = 0, want 100` from the test.
Fix: pointer receivers on every method of a type that contains a lock.

### 6. `%v` is not `%w`; a wrapper without `Unwrap` (U2 §1)

```sh
go test -tags breakit -run '^TestErrorsIs_' ./labs/b1/breakit/
```

Expect two failures, both `errors.Is(..., ErrNotFound) = false`. Fix 1: `%w`. Fix 2: add
`func (e *opError) Unwrap() error { return e.err }`. After the fix, change `ErrNotFound` handling to
`errors.As` against a typed error and notice the same two rules apply.

### Vet

`go vet -tags breakit ./labs/b1/...` is clean on purpose (exercise 5 sits behind its own tag), so the repo-wide
check keeps passing. None of exercises 1–4 and 6 is something `go vet` (Go 1.24) detects.

## Break it in your own labs

Each unit section has a "Break it" exercise that mutates *your* passing solution. Good ones to run: in `checkout`,
make `Place` call `Reserve` for all lines and only then check errors (watch the rollback tests fail); change
`WithTaxRates` to keep the caller's map; swap `context.WithoutCancel(ctx)` for `ctx` in the notifier call. In
`dispatch`, hold the registry lock inside the delivery loop, make `IsTemporary` look for the substring
`"timeout"`, return `fmt.Errorf("...: %v", err)` from `deliver`, and return the first error instead of
`errors.Join`. Note which test names fail for each change; that mapping is the point.
