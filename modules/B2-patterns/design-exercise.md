# B2 Design exercise — Outbound payments service

> Time box: 45 minutes, solo, no notes, no AI help (an LLD round, format `lld-45`). The Go standard library docs
> (`go doc`) are allowed. At the 25-minute mark open the requirement change below, and not before. Then stop at 45:00,
> self-score, and send the code, test output and design note to the tutor.

## Prompt

*As an interviewer would say it:*

"We run a digital banking platform for about forty credit unions. Members send outbound payments: bill-pay, external
transfers. Design the core of an **outbound payments service** in Go. A payment goes out over a **rail**; today we support two,
**ACH** and **wire**, each through a different vendor with a different API. Each rail has its own **fee** rule, and a credit
union can configure which vendor handles a rail. A payment has a **lifecycle**, and we must be able to answer 'what state is it
in and how did it get there' for audit. The vendors are flaky, so calls should be retried sensibly, but never in a way that can
double-pay. Support staff, the member's notification channel and the audit log all want to know when a payment changes state. You
have 45 minutes; I care about a design that works end to end, that I can extend, and that you can defend."

This is deliberately underspecified. Questions worth asking (or stating as assumptions in your note): which states exist and which
moves are legal; how fees are calculated (flat, percentage, tiered) and in what units; what 'a credit union configures the vendor'
means (config at startup, or per request?); whether duplicate vendor callbacks happen; what 'sensibly' means for retries and
what the idempotency key is; whether notifications may be lost; what concurrency to expect (many payments, one at a time per
payment?); what is out of scope (real HTTP, persistence, UI).

Suggested starting vocabulary (you may change it if you say why):

```go
type Money int64 // minor units

type Payment struct {
	ID       string
	Tenant   string
	Rail     string // "ach", "wire"
	Amount   Money
	Currency string
}

type Service struct { /* your design */ }

func (s *Service) Submit(ctx context.Context, p Payment) error
func (s *Service) HandleCallback(ctx context.Context, paymentID, vendorEvent string) error // may be delivered twice
func (s *Service) Status(paymentID string) (state string, history []string, err error)
```

## What to produce

1. **Code** in a package such as `labs/b2/_design/payments/` (directories starting with `_` are ignored by `./...`, so `sd lab B2`
   won't pick it up): the domain types, the interfaces you chose to introduce, an implementation for **two rails** with fake
   vendor clients, and `Submit` and `HandleCallback` working end to end for the happy path and at least one failure path.
2. **Tests** (`go test -race`) for at least: the fee for each rail (including a rounding or boundary case); an illegal and a
   duplicate state transition; a vendor failure that is retried and one that is not; a vendor error that must not be retried
   when the caller's context is cancelled; and a test that proves **adding a new variant needs no change to existing code**
   (a fake rail or vendor defined in the test file).
3. **Design note** (≤ 250 words, as a doc comment or a short text file), under these headings: *Requirements and assumptions*;
   *Axes of change* (what you expect to vary and why); *Pattern decisions* (for each pattern or abstraction you used: the pain it
   removes, its Go form, and the evidence that justified it); *What I deliberately did not abstract* (and what would make me);
   *Failure handling* (duplicates, vendor errors, retries and idempotency); *How the mid-way change landed*.
4. **Mid-way change** (revealed at 25:00): implement it in the time left, or if you run out of time, write down in the note exactly
   which types, functions and tests you would touch.

<details data-kind="hint">
<summary>Minute-25 requirement change (open only when your timer says 25:00)</summary>

"Compliance wants a third rail, **instant**, for same-day payments. Its fee is **tiered** (marginal bands by amount). It has **no
reversal window**: a settled instant payment cannot be reversed at all. Its vendor sends a status callback that can arrive
**twice and out of order** (a `settled` callback may reach you before `accepted`). Also, the audit team says it must **never miss a
state change**, even if the service restarts between a state change and the notification going out. Show me what you change."

</details>

## Rubric

Dimensions follow the LLD / machine-coding table in `TUTOR.md` §8, specialised for this module.

| Dimension | below | approaching | meets (senior bar) | exceeds |
|---|---|---|---|---|
| Requirements & modelling | Starts coding with no questions; entities blur (payment, rail, vendor, fee in one struct); money as `float64` | Some questions asked; model is reasonable but states, legal moves or the idempotency key are left implicit | Clarifies scope and states assumptions; crisp entities (payment, rail, fee rule, vendor port, lifecycle); money in integer minor units; names the one or two axes most likely to change (rails, vendors, fee rules) | Also identifies what must be durable (audit, outbox), what the idempotency key covers, and which assumption would change the design |
| Interfaces & extensibility | A `switch` on rail/vendor/state spread over several functions, or a wall of speculative interfaces; the mid-way change forces edits across the codebase | Some good seams (a vendor interface or a fee func) but others missing or over-built; the change lands with several edits to existing code | Small consumer-side interfaces only at real boundaries (vendor port, clock); fee rules as funcs/strategies; a lifecycle table; providers chosen by a registry or config lookup; adapters translate vendor shapes and fail closed on unknown responses; **the mid-way change adds new code (rail, fee tiers, vendor adapter, table rows) with at most a one-line wiring edit** | Names the trigger for each abstraction it did *not* add; contract tests shared across vendor adapters; shows the change as a diff of additions |
| Correctness & concurrency | State illegal moves possible; duplicates double-apply; retries not tied to an idempotency key; data races; hangs when the vendor hangs | Works sequentially; duplicates handled; some shared state unprotected or a lock held across a vendor call | Passes `-race`; transitions atomic per payment; duplicate callbacks acknowledged, illegal ones rejected distinctly; retries only with a stable key, stop on cancelled ctx or an open breaker; no lock held across calls; ownership of shared state stated | Handles out-of-order callbacks deliberately; states how the design extends to multiple pods (conditional update/outbox) with the failure windows named |
| Tests | No tests, or only a happy path | Tests exist but example-only, real sleeps, or no failure paths | Tests for fees (boundaries and rounding), duplicate and illegal transitions, retry and no-retry cases with an injected clock/sleeper, and one test that adds a variant defined only in the test file; deadlines on anything that can hang | Property or table-driven exhaustive tests (every state × event), shared contract suite across adapters, a mutation shown to fail a test |
| Code quality | Non-idiomatic (getters/setters, deep hierarchies), ignored errors, global singletons | Mostly idiomatic; some premature abstraction (one-implementation interfaces, a factory for one product) or unclear names | Idiomatic Go: func types and small interfaces where justified, errors wrapped with `%w` and classified, no premature abstraction, no stuttering names; a reader can find where the fee, the lifecycle and the vendor mapping live | Explains each deletion/omission; the code reads like the design note |
| Time management | Nothing runs by 45:00 | Core flow works late (after 30:00); the change gets only a sketch | Working core (one rail end to end with a test) by ≈ 20:00; second rail and failure paths by 25:00; the change implemented or precisely planned by 45:00; note written | Finishes early enough to run `-race -count=20` and review its own note against the code |

## Tutor critique protocol

1. **Run before reading.** `go vet` and `go test -race -count=5` on the learner's package. Then make one mutation (e.g. delete the
   duplicate check, or hold the service lock across a fake vendor call) and check that a test fails. Record whether it did.
2. **Self-score first.** Ask for a level on each dimension with a one-line justification, before you say anything evaluative.
3. **Read the note, then the code,** checking that every claim in the note is true in the code (where each pattern lives, what
   was *not* abstracted, how duplicates are handled, what the mid-way change touched).
4. **Probe before scoring.** Ask at least three of these, one at a time. Model answers are in Unit 1 (§1–§7). Score the answer,
   not the vocabulary.
   - "Walk me through a payment from `Submit` to `settled`. Where would a second pod break this?" (§3 conditional update)
   - "The vendor returns a code you've never seen. What happens?" (§5 fail closed)
   - "The vendor times out. You retry. How do you know you haven't paid twice?" (§6 idempotency below retry)
   - "Your retry wraps your circuit breaker, or the other way round. What changes, in numbers?" (§6 order)
   - "A callback is delivered twice, 5 ms apart, on two goroutines." (§3 atomic Apply, duplicate vs illegal)
   - "Why is this an interface and that a func? What would make you flip them?" (§1 func vs interface)
   - "Audit must never miss a change. Is your observer enough?" (§2 outbox)
   - "You have a registry. Where do credit unions' vendor choices get validated, and when?" (§4 startup `Build`)
   - "Delete one abstraction from your design. Which one hurts least, and why is it still there?" (§7)
5. **Score each dimension** with evidence: quote a line, test or answer. Say exactly what would have moved each dimension up one
   level.
6. **Overall.** Use the `TUTOR.md` §8 rule (the level most dimensions sit at, capped one level above the lowest). For this module
   "meets" additionally requires **meets or better on Interfaces & extensibility and Correctness & concurrency**, and the
   mid-way change landing without edits to existing logic (wiring lines excepted). Reciting pattern names with no matching
   requirement or evidence is capped at "approaching" on Interfaces & extensibility, however clean the code.
7. **Calibration feedback.** If any self-score was off by more than one level, say so (weak spot d).
8. **Below meets?** Give 2-3 concrete revision tasks and have the learner redo a variant on another day (for example: "an
   inbound-payments service with a refund flow and three card processors").

## Recording

`sd record design B2 --self <below|approaching|meets|exceeds> --tutor <level> --notes "<one-line summary>"`
