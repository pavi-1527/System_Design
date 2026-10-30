# B1 Design exercise — Pluggable payment-method checkout

> Time box: 45 minutes of design and coding (the requirement change is released at minute 30) plus 10 minutes for the
> design note, solo, no notes, no AI help. The Go standard library docs (`go doc`) are allowed, as they would be in
> a machine-coding round. Then self-score, then send the code, test output and note to the tutor.

## Prompt

*As an interviewer would say it:*

"We run a multi-tenant platform for credit unions. Members pay fees, loan instalments and merchant bills at checkout.
Each credit union enables a subset of payment methods: cards through two different vendors, an instant bank
transfer rail, and a stored-value wallet. Design the core of the checkout: given an order and a chosen method, take
the payment; refund it later, fully or partially; and let anyone ask for the status of a payment. Clients retry on
timeouts, so a retry must never charge twice. Vendors fail, time out and sometimes return something we don't
understand. We expect to add methods and vendors regularly. You have 45 minutes; I care about the shape of the types,
how a new method gets added, and how you'd convince me it works."

This is deliberately underspecified. Clarifying questions worth asking (or stating as assumptions in your note): which
operations and which failures matter most (decline versus timeout versus unknown); what "retry must not double-charge"
means for each layer (our API, our checkout, the vendor); who generates the idempotency key; whether payments are
synchronous; whether state must survive a restart (assume an in-memory store, behind an interface you choose, for the
exercise); how much concurrency to expect; how tenants enable methods; what the money type is; what is out of scope.

Suggested starting API (change it if you say why):

```go
type Money int64 // minor units

type Order struct {
	ID       string
	Tenant   string
	Amount   Money
	Method   string // "card-acme", "card-zen", "instant", "wallet"
	Instrument string // opaque token from the client
}

type Payment struct {
	ID       string
	OrderID  string
	Method   string
	Amount   Money
	Refunded Money
	Status   Status
}

func New( /* your dependencies and options */ ) (*Checkout, error)
func (c *Checkout) Pay(ctx context.Context, o Order) (Payment, error)
func (c *Checkout) Refund(ctx context.Context, paymentID string, amount Money) error
func (c *Checkout) Status(ctx context.Context, paymentID string) (Payment, error)
```

<details data-kind="hint">
<summary>Requirement change: open only at minute 30</summary>

"Product now wants **split payments**: one order can be paid with up to two methods (for example wallet balance plus
a card). It is all-or-nothing: if the second method fails, the first must be reversed. Refunds of a split payment
should go back to the methods in reverse order. And each credit union may cap the number of methods per order. Show me
how much of what you have changes."

Write down the clock time when you open this, the *diff size* (types and files touched, approximate lines) and
what you chose not to change. That goes into your note.

</details>

## What to produce

1. **Code**: a package (suggested location `labs/b1/_design/payments/`. Directories starting with `_` are ignored by
   `./...`, so `sd lab B1` won't pick it up) with the implementation and tests. Run
   `go test -race -count=20 ./labs/b1/_design/payments/` and keep the output. At least two method implementations
   (written as fakes in your tests are fine) and one in-memory store.
2. **Tests** that cover at least: a successful payment; a declined payment; a **retry with the same key does not
   charge twice** (including two concurrent calls with the same key); a vendor timeout (`ctx` deadline) leaving the
   payment in a state a later retry can resolve; a partial refund and an over-refund; **adding a new method from the test
   file only** (a method type defined in `_test.go` and registered, without editing your package); and the split
   payment with failure of the second leg (first leg voided). Inject time and IDs; no `time.Sleep` for
   synchronisation.
3. **Design note** (≤ 250 words, as a doc comment or a short text file next to the code), under these headings:
   *Entities and responsibilities* (one line each); *Extension points* (which interfaces, declared where, and what you
   deliberately did not abstract); *Errors* (the vocabulary and who maps it to transport codes); *State and
   concurrency* (who owns what, what the lock covers); *The change* (what the minute-30 requirement cost in files and
   lines); *Evidence* (which test proves which property).

## Rubric

Dimensions follow the LLD / machine-coding table in TUTOR.md §8.

| Dimension | below | approaching | meets (senior bar) | exceeds |
|---|---|---|---|---|
| Requirements and modelling | Starts coding with no clarifying questions; entities are vague (a `PaymentManager` that does everything), or a class hierarchy per vendor | Entities identified, but responsibilities overlap or idempotency/refund semantics are left implicit | Scope and assumptions stated; entities each have one clear responsibility (`Checkout` orchestrates, a method adapter talks to a vendor, a store keeps state); idempotency, partial refunds and unknown outcomes are modelled, not mentioned | Models the hard part explicitly (an "unknown/pending" status for timeouts and a reconciliation path); states what is out of scope and why; anticipates the next requirement |
| Interfaces and extensibility | Interfaces declared by the vendor adapter or one per struct; adding a method means editing a `switch` in core code; the minute-30 change is a rewrite | Interfaces are consumer-side but fat (many methods), or the new method requires touching two or three core types | One or two small consumer-side interfaces (for example a `Method` with `Charge`/`Refund`, a `Store`); a new method added from the test file only; the minute-30 change lands as new code behind existing seams with a small diff; what was *not* abstracted is justified | Seams placed where requirements vary and nowhere else; compensation (voiding the first leg) expressed as a small reusable piece rather than copied logic; names the limits (what a third axis of variation would cost) |
| Correctness and concurrency | Data races, double charge possible, or `-race` not run | Single-threaded correctness; the idempotency check is check-then-act (two concurrent calls can both charge), or a lock is held across the vendor call | `-race -count=20` clean; idempotency decided atomically (one winner per key; the loser waits for or reads the result); no lock held across vendor I/O; ctx honoured; stock/state rolled back on every failure path; shared state has a named owner | Explains the crash-between-charge-and-record window and how the key makes a retry safe; handles unknown outcomes (timeout after the vendor may have charged) with a defined state and resolution; proves the invariants with a test |
| Tests | No tests, or tests that only cover the happy path | Tests exist but depend on real time, share global state, or assert on call order of mocks | Table-driven where natural; fakes with state; time and IDs injected; deadlines on every wait; the retry, concurrent-duplicate, timeout, over-refund, extension and split-failure tests exist and are deterministic under `-count=20` | A shared contract test any `Method` implementation can run; a mutation shown to fail a test (for example, remove the idempotency check and the concurrent-duplicate test fails) |
| Code quality | Names like `Manager`/`Impl`, `util` package, errors as strings, panics for control flow | Mostly idiomatic, but constructor does not validate, exported mutable fields, or errors are flattened with `%v` | Idiomatic Go: `New` validates and returns errors; unexported state; `%w` wrapping with context; sentinel/typed errors checkable with `errors.Is/As`; handle-once error discipline; no premature abstraction | Documented behavioural contracts on the interfaces; small, readable files; the design note matches the code exactly; you can name the two things you would refactor next and why |
| Time management | Nothing compiles at the end | Compiles, but core finished late (after 0:30) or the change was not attempted | Compiling happy path with a test by ~0:20; core complete by ~0:30; the change attempted and absorbed before 0:45; note written | Everything above, plus extra edge cases and a concise "what I'd do with another hour" list that is prioritised |

## Tutor critique protocol

1. **Run before reading.** `go vet` and `go test -race -count=20` on the learner's package. Then mutate one thing:
   remove the idempotency check (or hold the lock across the vendor call), and confirm a test fails. Record whether it
   did. A suite that cannot tell is "approaching" at best on Tests.
2. **Read the design note, then the code**, checking each claim against the code: where each interface is declared,
   what the lock covers, which errors the edge maps, how large the minute-30 diff really was (compare the learner's
   version history or ask them to show the diff).
3. **Ask the probes below** one at a time, as an interviewer would. The learner answers from memory, out loud or in
   writing, in ≤ 2 minutes each. Model answers are in Unit 1 §3–§5 and Unit 2 §1–§5. Score the answer, not the
   vocabulary. Ask at least three.
   - "Where is this interface declared, and who imports whom? Why not next to the vendor adapter?"
   - "Two requests with the same idempotency key arrive at the same instant. Walk me through exactly what happens."
   - "The vendor times out after maybe charging the card. What state is the payment in? What does a retry do? How do
     you find out the truth?"
   - "Add a fourth method that needs a different shape of call (a redirect: authorize, customer approves, capture).
     What changes?"
   - "Why is that an interface and that other thing not?"
   - "Which error would the HTTP handler turn into a 402, which into a 503, and where is that mapping?"
   - "How do you know a new `Method` implementation is correct, beyond that it compiles?"
   - "What does `Refund` do when it is called twice for the same amount? Concurrently?"
   - "Show me the test that would fail if I removed the lock around the idempotency check."
4. **Score each dimension** using the rubric, citing a specific line, test or answer for each score. Use the overall
   rule from TUTOR.md §8: the overall level is the level most dimensions sit at, capped one level above the lowest
   dimension.
5. **Overall "meets"** additionally requires *meets* or better on **Interfaces and extensibility** and **Correctness
   and concurrency**, and no dimension at *below*. One *approaching* elsewhere is acceptable if the learner corrects
   it when probed (note that in the record).
6. **Calibration.** Compare the self-score with the tutor score per dimension and call out any gap of more than one
   level.
7. **Feedback**: one thing to keep, the single most important fix, and one probe to revisit with `sd review`. If the
   overall result is below *meets*, the learner redoes the exercise on a different day with a variant (for example "a
   pluggable notification router with per-tenant channels and retry budgets", with a mid-way change of "add quiet
   hours per member") and a fresh minute-30 change.

## Recording

`sd record design B1 --self <below|approaching|meets|exceeds> --tutor <level> --notes "<one-line summary>"`
