# B2 — Patterns that matter

| | |
|---|---|
| Track | B — LLD / machine coding in Go |
| Estimated time | 6 h (1 weekly unit) — CORE 5 h, STRETCH 1 h |
| Prerequisites | B1 (gate passed) |
| Labs | `labs/b2/` — `strategy`, `observer`, `state`, `factory`, `adapter`, `decorator` (+ `breakit` exercises, build tag `breakit`) |
| Gate | quiz ≥ 85% ×2 (different days) · labs pass · design ≥ meets · teach-back pass |

## Why this module exists

A credit-union platform's fee engine is one `calculateFee` function with a `switch` of eleven product types, 140 lines.
Every new product edits it and four mirror copies (the disclosure text, the reversal maths, the statement line, the revenue
report). Over six months three releases forget one copy, and reversed instant-ACH transfers refund `0` instead of the fee
that was charged: ≈ $3,000 a month of silent leakage. The transfer `status` is a string compared in 11 files; a duplicate
vendor callback settles the same transfer twice. A settle event is announced to its listeners while a lock is held, so the
first subscriber that unsubscribes from inside its callback hangs the worker. The third image vendor was added by copying the
second's 300-line adapter and fixing nine `switch cfg.Vendor` sites, and the statement job was the one that got missed. Six
vendor adapters each carry their own hand-rolled retry loop; in a 40-minute vendor incident they multiply the platform's
traffic ≈ 4× and the vendor starts refusing everyone.

Nobody in that story lacks skill. They lack **names for six recurring shapes and the judgement to know when each one
pays**: a rule you can swap, a list of listeners, a lifecycle with legal moves, a map from a configured name to a
constructor, a translator for a foreign API, and a wrapper that adds behaviour. The opposite failure is just as real. A
candidate in a 60-minute LLD round who opens with a factory, a strategy, an observer and a state per slot can end the round
with nothing that runs. This module teaches the problem each pattern solves, the Go form (usually a function value or a
one-method interface, much smaller than the textbook), how each one breaks, and when leaving it out is the senior move.

## Outcomes

- **Choose** between a `switch`, a function parameter, a map of funcs and an interface for varying behaviour, and say when
  the answer flips. CORE
- **Implement** a strategy-based fee engine (flat, percentage, marginal tiers) in integer minor units with correct rounding,
  runtime replacement and a lock-free call path. CORE
- **Implement** an observer bus with sync callbacks and channel subscriptions that survives re-entrant, slow, panicking and
  racing subscribers, and **explain** when an in-process observer is the wrong tool for audit (outbox). CORE
- **Model** a transfer lifecycle as a transition table with guards, distinguish duplicate from illegal events, and test it
  exhaustively and under concurrency. CORE
- **Build** a provider registry that fails closed with actionable errors, validates every tenant at startup, and explain
  `init()` order and forgotten-import bugs (`database/sql` as the reference). CORE
- **Write** vendor adapters behind one domain port: integer money, a classified error taxonomy that keeps the cause, fail-closed
  on unknown responses, and a shared contract test suite. CORE
- **Compose** decorators (retry, circuit breaker, timeout, logging) and **predict** the effect of their order, including
  retry amplification and what a breaker counts. CORE
- **Judge** when a pattern is over-engineering, and **narrate** a minimal design plus a mid-way requirement change in an LLD
  round without reciting pattern names. CORE
- **Port** the registry to generics and **build** `net/http` middleware and `RoundTripper` chains. STRETCH

## Unit plan

| Unit | Topic | Hours | Core/Stretch | Lab |
|---|---|---|---|---|
| 1 | [Patterns that matter](unit-1-patterns.md) | 6 | CORE (5) / STRETCH (1) | `labs/b2/strategy`, `observer`, `state`, `factory`, `adapter`, `decorator`, `breakit` |

**Unit 1** (CORE 5 h + STRETCH 1 h; three sittings plus a short fourth)
- Session 1 (1.25 h): "Before you start", §1 Strategy and §2 Observer with every Predict box written down before the
  reveal; katas `strategy` and `observer`; Break it: the deadlocking bus (`breakit`).
- Session 2 (1.25 h): §3 State and §4 Factory/registry; katas `state` and `factory`; Break it: the broken transition table
  and the init-order program.
- Session 3 (1.5 h): §5 Adapter and §6 Decorator; katas `adapter` and `decorator`; Break it: decorator order (`breakit`) and
  the mutations listed in the unit.
- Session 4 (1 h): §7 over-engineering (read and do the 15-minute exercise), then the timed 45-minute design exercise.
  The teach-back (≈ 30 min of writing) goes in your next review slot, after `sd review`.
- STRETCH (1 h): the generic `Registry[T]` port in §4, and `net/http` middleware and `RoundTripper` chains in §6.

Katas are small on purpose (each solution is ≈ 80–220 lines including given types) but the tests are demanding: they check
properties (adding a variant needs no change to the engine, illegal transitions are rejected, decorators compose in the
documented order), not just examples. Budget 25–40 minutes per kata.

## How this connects to your banking work

- **Fee and interest calculation** (Unit §1): fee schedules per product and per credit union are strategies chosen by config.
  Integer minor units, half-up rounding and a version stamped on every computed fee are what auditors ask for.
- **Transfer lifecycle and duplicate callbacks** (§3): `initiated → authorised → settled → reversed` as a table with a reversal
  window guard; duplicate vendor callbacks are expected and acknowledged, illegal moves page someone, and the database-level
  conditional update is the cross-pod version of the kata's mutex.
- **Image and SSO providers per credit union** (§4–§5): your existing adapters are the strongest case for the registry,
  the port and a shared contract suite. `ErrUnknownResponse` on an unrecognised vendor code is the difference between an alert
  and 143 payments shown as approved.
- **Vendor timeouts and retries** (§6): the decorator stack replaces six copy-pasted retry loops, with the idempotency key
  below the retry so a retried attempt cannot double-charge, and a breaker that ignores members who abandon a checkout.
- **Audit trails** (§2): in-process observers announce "settled"; the durable audit record is an outbox row written in the
  same transaction. Know which is which before you promise "no lost events".

## Mastery gate checklist
- [ ] `sd quiz B2` ≥ 85% — attempt 1 (date: ____)
- [ ] `sd quiz B2` ≥ 85% — attempt 2 on a different day (date: ____)
- [ ] `sd lab B2` — all lab tests pass (with `-race`)
- [ ] Design exercise (`design-exercise.md`): self-score recorded, tutor score ≥ "meets"
      (`sd record design B2 --self <lvl> --tutor <lvl> --notes "..."`)
- [ ] Teach-back (`teachback.md`): tutor marks pass (`sd record teachback B2 --result pass --notes "..."`)

`sd status` is the source of truth; this list is for your own reading.

## What's next

B2 unlocks **B3** (the machine-coding problem bank, together with B0): its rate limiter, parking lot, in-memory KV with
transactions and pub-sub broker problems reuse strategy, state, observer and decorator under a clock. It also feeds the
**D1** LLD mocks (with B1), which add a requirement near minute 30 to see how little of your design has to change. Before
moving on, run `sd review` for the questions you missed (weak tags are typically `patterns`, `ood`, `concurrency`), and redo
one Break it exercise per pattern without looking at the answers.
