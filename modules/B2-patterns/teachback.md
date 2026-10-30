# B2 Teach-back

## Prompt

A mid-level engineer on your team has just read a design-patterns book and wants to add an `AbstractVendorFactory`, a
`PaymentStrategy` hierarchy and an observer framework to the payments service "to make it extensible". They write good Go
but have never watched a pattern hurt a production system, and they have not yet seen one of the Go forms of these ideas.
Write them an explanation (**500-800 words, your own words, no copying from the unit, one diagram allowed**, Mermaid or
ASCII) of **how to decide whether, and in what Go form, to use strategy, observer, state, factory/registry, adapter and
decorator**, using the outbound-payments service (fees per rail, a payment lifecycle, vendors per credit union, flaky vendor
calls, audit and notifications) as the running example. Write it as something you would actually put in the team wiki.

Then answer, in two or three sentences each, whichever two follow-up questions the tutor picks from the "should-hit" list.

## Evaluator checklist

Must-hit (all required for pass):
- [ ] **Pain first.** For at least four of the six patterns, states the concrete pain it removes in terms of the example
      (edits per new variant, a `switch` mirrored in several files, a status compared everywhere, call sites that must know
      every listener, vendor types leaking, retry loops copy-pasted), not a definition or a class diagram.
- [ ] **Go forms are small.** Says that a func type or a one-method, consumer-side interface usually replaces the textbook
      class structure (strategy as `FeeFunc`, factory as a map of constructors, decorator as `func(Client) Client`), and
      gives the rule for flipping a func into an interface (several related behaviours or identity).
- [ ] **Observer hazards.** Callbacks must not run while the bus holds its lock (re-entrancy deadlock); one slow or panicking
      subscriber must not stall or kill the publisher; sync callbacks versus a channel per subscriber with an explicit
      full-buffer policy; and that an in-process observer cannot guarantee audit delivery (outbox in the same transaction).
- [ ] **State as data.** One transition table is the source of truth; **duplicate** events are distinguished from **illegal**
      ones (and why); transitions are atomic (a mutex in process, a conditional `UPDATE ... WHERE status = expected` across pods).
- [ ] **Adapter discipline.** The port lives with the consumer; money is integer minor units; unknown vendor responses **fail
      closed**; errors are classified into a small taxonomy while keeping the cause (`errors.Is`/`errors.As`); a caller's
      cancelled or expired context is not counted as a vendor failure.
- [ ] **Decorator order is behaviour.** Gives the retry-versus-breaker example with numbers (for example 3 attempts and a
      threshold of 3 give 3 vendor calls to trip when retry is outside, and 9 when the breaker is outside) or an equivalent
      worked case, and says retries need a stable idempotency key sitting below the retry.
- [ ] **When *not* to.** States the evidence rule (a real second variant, a second `switch`, a test seam, a team boundary),
      names at least two over-engineering smells (single-implementation interface, factory of one product, observer with one
      subscriber, forwarding layers), and says patterns are named after the code asks for them, not before.

Should-hit (at least 4 of 7):
- [ ] Registry specifics: explicit registration versus `init()`, the `init()`/variable-initialisation order trap or the
      forgotten blank import (`database/sql` as the reference), and a startup `Build` that reports every misconfigured tenant.
- [ ] Contract tests: one scenario table run against every adapter, so a new vendor is a checklist.
- [ ] Copy the function or slice out under the lock and call it outside (including why a re-entrant `RLock` can deadlock behind
      a waiting writer).
- [ ] Retries: only for idempotent calls or with a key, backoff with jitter, retry at one layer only (amplification), a
      breaker that admits exactly one half-open probe.
- [ ] Interview use: requirements, then entities and flows, then the axes of change, then a minimal design with tests, then
      walking the mid-way requirement change, naming patterns as a consequence; and the failure mode of reciting GoF names.
- [ ] Testing techniques: exhaustive state × event matrix, a property such as "tiered fees never decrease", injected clock and
      sleeper, a variant defined only in the test file.
- [ ] Ops: metrics for dropped events, subscriber count, oldest transfer per non-terminal state, unknown-response count per
      vendor, breaker state transitions.

Red flags (any one = revise):
- [ ] Explains patterns through inheritance or recommends base classes, abstract factories or "Manager" hierarchies in Go.
- [ ] Says holding the bus lock while calling subscribers is fine, or fans out an unbounded goroutine per event with no caveat.
- [ ] Treats a decline or a duplicate callback as the same kind of thing as an outage, or maps an unknown vendor code to approved
      (or silently to declined).
- [ ] Uses `float64` for money.
- [ ] Claims decorator order does not matter, retries at several layers without noting amplification, or retries a charge with
      no idempotency key.
- [ ] Recommends adding every pattern up front "for extensibility" with no stated trigger or cost.
- [ ] Says an in-process observer is enough to guarantee audit delivery.

## Recording

`sd record teachback B2 --result <pass|revise> --notes "<missed items>"`
