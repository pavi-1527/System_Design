# B1 — Pragmatic OOD in Go

| | |
|---|---|
| Track | B — LLD / machine coding in Go |
| Estimated time | 12 h (2 weekly units) — CORE 10 h, STRETCH 2 h |
| Prerequisites | B0 (gate passed) |
| Labs | `labs/b1/` — `checkout`, `dispatch` (+ `breakit` exercises, build tag `breakit`) |
| Gate | quiz ≥ 85% ×2 (different days) · labs pass · design ≥ meets · teach-back pass |

## Why this module exists

The payments service of a credit-union platform has one type at its centre, `PaymentService`: ≈ 1,100 lines, 41
methods, a package-level database handle, `time.Now()` in 17 places. Product asks for a fourth payment provider and
estimates two days. It takes nine: the change touches six files, breaks two tests nobody wrote, and the suite that
should have caught it takes ≈ 11 minutes because 70% of the tests need a real Postgres and the vendors' sandbox.
A week later a nil-map panic takes the service down at 02:10, because an `init()` in a renamed file now runs after the
code that uses its map. Meanwhile one failed vendor call logs seven lines at four layers, duplicate-key detection
matches an English error message that changes when the database locale does, and a helper returning a nil pointer as
`error` makes every payment fail for 22 minutes after a deploy.

None of this needs exotic knowledge. Each incident is a missing design decision: where an interface lives, who
validates an invariant, how an error travels, which package may import which. Go removes inheritance and most
ceremony, so the decisions are fewer, but nothing in the language makes you take them, and the code that results from
*not* taking them compiles and passes review. Senior LLD rounds probe exactly these decisions ("where does that
interface go?", "what happens when the requirement changes now?", "how would you test that?"), and every later module
in Track B (patterns, the machine-coding problem bank) and Track D's LLD mocks assume you make them without prompting.

## Outcomes

- **Diagnose** a god object by its reasons to change and its ambient state, and **refactor** it behind a parity test
  one seam at a time. CORE
- **Explain** Go's object model without inheritance: method sets, embedding as composition (promotion, no virtual
  dispatch), embed versus named field. CORE
- **Define** small interfaces at the consumer, accept interfaces and return structs, and **say when not to** define
  an interface. CORE
- **Design** constructors that enforce invariants: required dependencies, options versus config struct, defensive
  copies, immutability, a deliberate zero-value story. CORE
- **Choose** pointer or value receivers, avoid copying locks, and **explain and avoid** the typed-nil trap. CORE
- **Use** errors as values: sentinel, typed and behaviour errors, `%w` wrapping with `errors.Is/As/Join`, handle once,
  map domain errors to transport codes at the edge. CORE
- **Structure** packages by domain with dependencies pointing at the core, break import cycles, avoid
  `util`/`common`. CORE
- **Translate** SOLID into idiomatic Go and write a contract test for a plug-in interface. CORE
- **Design for tests**: seams for time, IDs, network and filesystem; fakes versus mocks; table-driven tests, golden
  files, `httptest`. CORE
- **Run** a 45-minute LLD round and absorb a mid-way requirement change by adding code behind an existing seam. CORE
- **Choose** between generics and interfaces for containers and plug-in points. STRETCH
- **Recognise and unwind** premature abstraction with a checklist. STRETCH

## Unit plan

| Unit | Topic | Hours | Core/Stretch | Lab |
|---|---|---|---|---|
| 1 | [Shaping types](unit-1-shaping-types.md) | 6 | CORE (5) / STRETCH (1) | `labs/b1/checkout`, `labs/b1/breakit` (typed nil, embedding, zero value, fat interface, copied lock) |
| 2 | [Boundaries and errors](unit-2-boundaries-errors.md) | 6 | CORE (5) / STRETCH (1) | `labs/b1/dispatch`, `labs/b1/breakit` (error wrapping) |

**Unit 1** (CORE 5 h + STRETCH 1 h)
- Session 1 (2 h): §1 the god object and §2 Go's object model, every Predict box written down before the reveal; read
  `labs/b1/checkout/legacy.go` with a pencil and start `checkout` (`DefaultTaxRates`, options, `New`, validation and
  pricing until the parity test passes).
- Session 2 (1.5 h): §3 consumer-side interfaces and §4 constructors and invariants; finish `Place` (reserve, rollback,
  charge, idempotency key, notification); the fat-interface `breakit` exercise.
- Session 3 (1.5 h): §5 pointer or value and the typed-nil trap; context handling and concurrency tests in `checkout`
  (`go test -race -count=20`); the other `breakit` exercises (typed nil, embedding, zero value, copylocks).
- STRETCH (1 h): §6 generics versus interfaces, with your own `Set[T]` in a scratch directory.

**Unit 2** (CORE 5 h + STRETCH 1 h)
- Session 1 (1.5 h): §1 errors as values and §2 package boundaries; `breakit` error-wrapping exercises; start `dispatch`
  (`Match`, `Temporary`/`IsTemporary`, options, `Register`, `Stats`).
- Session 2 (1.5 h): §3 SOLID in Go and §4 designing for tests; finish `dispatch` (`Dispatch`: retries, panics,
  concurrency, joined errors, extension test); write a sink contract test in a scratch directory.
- Session 3 (2 h): §5 the LLD interview, then the timed design exercise (45 min plus the mid-way change) and the
  teach-back draft.
- STRETCH (1 h): §6 premature abstraction: inventory the interfaces in both labs and justify each.

## How this connects to your banking work

- **Vendor adapters.** `checkout.Payer` and `dispatch.Sink` are your payment-provider and notification adapters.
  The interface lives in the service; the vendor SDK lives behind an adapter package that `main` wires; a fourth
  provider is a new type and a line in `main`, not a change to six files (Unit 1 §3, Unit 2 §2).
- **Idempotent money movement.** `Place` sends the order ID as the idempotency key and releases stock on every failure
  path; the lab's rollback and "cancelled after the charge" tests are the failure cases your payment code must handle
  (Unit 1 §3, §5).
- **Errors across the boundary.** SSO and vendor failures must reach the HTTP edge as typed, wrapped errors that map
  to 4xx/5xx and `Retry-After` in one place, never as strings matched against a vendor message (Unit 2 §1).
- **Audit hooks as decorators.** Wrapping a `Payer` or `Sink` to log every attempt with a correlation ID is the
  open/closed principle in Go: a decorator, not an edit to the service (Unit 2 §3, §5).
- **Tenant configuration.** A config struct missing a timeout, or a tax table shared with a hot-reloader, are the
  constructor-and-invariant failures of Unit 1 §4: validate each tenant at load time and copy what you keep.

## Mastery gate checklist
- [ ] `sd quiz B1` ≥ 85% — attempt 1 (date: ____)
- [ ] `sd quiz B1` ≥ 85% — attempt 2 on a different day (date: ____)
- [ ] `sd lab B1` — all lab tests pass (with `-race`)
- [ ] Design exercise (`design-exercise.md`): self-score recorded, tutor score ≥ "meets"
      (`sd record design B1 --self <lvl> --tutor <lvl> --notes "..."`)
- [ ] Teach-back (`teachback.md`): tutor marks pass (`sd record teachback B1 --result pass --notes "..."`)

`sd status` is the source of truth; this list is for your own reading.

## What's next

B1 unlocks B2 (patterns that matter): Strategy, Observer, State, Factory, Adapter and Decorator are the idiomatic Go
forms of the seams you just used, and B2 asks *when each is over-engineering*. B1 is also a prerequisite for the LLD
mocks in Track D (D1). Before moving on, run `sd review` to revisit the B1 questions you missed (weak tags are
typically `errors`, `interfaces` and `design`), and re-do one `breakit` exercise from each unit without looking at the
answer.
