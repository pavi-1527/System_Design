# D1 Problem bank (interviewer-only)

> **Spoiler warning.** This file is for the tutor playing the interviewer. If you're the learner, stop here: each
> problem you read is one fewer unseen problem for your mocks, and the gate counts each problem once. The
> [format](format.md), [rubric](rubric.md) and [probe bank](probe-bank.md) are the parts meant for you.

## How to use it (tutor)

1. Pick a problem the learner hasn't done in a mock (check `mocks` in `progress.json` and the `_mocks/` logs). Prefer
   one that exercises the weakest tags in `sd progress --json`. Rotate: no more than two banking-flavoured (🏦)
   problems in a row.
2. Read the **Prompt** verbatim at 0:00. Answer clarifying questions from the **Answer sheet**; don't volunteer the
   rest.
3. The **Expected shape** is for you, to recognise a sound model quickly. It isn't the only acceptable answer: a
   different model that meets the rubric meets the rubric.
4. Release the **Requirement change** at 0:30 verbatim.
5. Ask probes from **Probes** first (2–4 of them), then fill categories from the [probe bank](probe-bank.md).
6. Record with the problem's **Record as** name so the distinct-problem rule sees repeats.

These problems deliberately avoid the B3 problem bank (LRU, rate limiter, parking lot, nested-transaction KV, pub-sub,
job scheduler, Splitwise, elevator, ticket booking, in-memory file system, logging library, vending machine, order
book, library/inventory, resilient HTTP client, URL shortener, leaderboard, consistent-hash client), the B3
design-exercise variants, the B1 checkout and the B2 outbound-payments service. All of them are seen problems by now.

| # | Problem | Record as | Flavour | Main skills |
|---|---|---|---|---|
| 1 | ATM with cash cassettes and a bank host | `atm` | 🏦 | state machine, partial failure, adapter |
| 2 | Loan-eligibility rule engine per credit union | `loan rules` | 🏦 | composite/strategy, explainability, tenancy |
| 3 | Food-delivery order lifecycle | `food delivery` | | state, strategy, observer |
| 4 | Chess with variant extensibility | `chess` | | modelling, validation seams |
| 5 | Configurable issue-tracker workflows | `issue workflows` | | transition tables, permissions, audit |
| 6 | Ride booking with pricing and surge | `ride booking` | | strategy, matching, concurrency |
| 7 | Promotions and pricing rule stacking | `promotions` | | rule ordering, money, determinism |
| 8 | API gateway middleware pipeline | `api gateway` | | decorator/chain, ordering, failure |
| 9 | Text editor with undo/redo | `text editor` | | command pattern, history bounds |
| 10 | Spreadsheet cells with formulas | `spreadsheet` | | dependency graph, recalculation, cycles |
| 11 | Multi-tenant feature-flag evaluator | `feature flags` | 🏦 (tenancy) | targeting rules, consistent bucketing, snapshots |

---

## 1. ATM with cash cassettes and a bank host 🏦

**Prompt.** "Design the software for an ATM. Customers insert a card, enter a PIN, and withdraw cash. The ATM talks to
the bank to authorise transactions. Design the classes; I'd like to see some code for the core."

**Answer sheet.**
- Operations in scope: balance inquiry and withdrawal. Deposits: "not for now".
- Cash: four cassettes with fixed denominations (e.g. 100, 50, 20, 10), each with a count. Dispense the fewest notes
  possible from what's available.
- The bank host is a remote service: authorise (card, PIN block, amount) → approved/declined; it can time out (≈ 1%
  of calls at peak). One customer at a time per ATM; a maintenance operator can refill cassettes while the ATM is
  idle.
- Three wrong PINs retain the card. PIN verification happens at the bank, not locally.
- Audit: every state change and every dispense must be journaled (regulatory).

**Expected shape.** `ATM` (owns session state and cassettes) with an explicit state enum (Idle → CardInserted →
Authenticated → Dispensing → Idle, plus OutOfService); `CashDispenser`/`Cassettes` computing a note combination
(greedy works for these denominations but isn't always optimal for arbitrary sets: a good candidate says so);
`BankHost` consumer-side interface (`Authorise`, `Reverse`); `Journal` interface. Key invariant: reserve notes before
asking the bank, dispense only after approval; a timeout after approval is the hard case.

**Requirement change (0:30).** "Now add cash deposits: the customer inserts notes, the machine counts them, and the
bank credits the account. Deposits can be rejected by the bank after the notes are already in the machine."

**Probes.**
- "The bank approves, you dispense, and then the confirmation to the bank times out. What happens?" — *Listen for:*
  journal first; the transaction is *unknown* not failed; a reversal or completion message sent with the same
  transaction id on retry; reconciliation at end of day; never re-dispense.
- "Can the operator refill while a customer is mid-withdrawal?" — *Listen for:* cassettes have one owner (the ATM's
  lock or state machine); refill only in Idle/OutOfService, or reservations accounted for.
- "The cassettes have 3 × 20s and 1 × 50, and the customer asks for 60. What does your algorithm do?" — *Listen for:*
  greedy picks the 50 and then fails; a fallback (try combinations/DP over a tiny denomination set) or reject with a
  clear error; tested.
- "Where does the PIN live in your objects?" — *Listen for:* never stored or logged in clear; an encrypted PIN block
  passed through; `String()` redaction.
- "How do you test the timeout path?" — *Listen for:* a fake `BankHost` returning a timeout error; asserting journal
  entries and state, not log text.

**Red flags.** `ATM` inherits from `Machine`; PIN checked locally; dispense before authorisation; the deposit change
needs a rewrite of the withdrawal flow (no `Transaction` abstraction or state per transaction type).

**Draws on.** B2 state and adapter; B1 errors as values (declined vs timeout vs unknown); B3 `vending` state machine
and `resilient` unknown outcomes.

---

## 2. Loan-eligibility rule engine per credit union 🏦

**Prompt.** "Each credit union on our platform has its own rules for who can get a personal loan: minimum credit
score, debt-to-income ratio, membership age, that kind of thing. Design the engine that evaluates an application
against a credit union's rules."

**Answer sheet.**
- ≈ 40 credit unions, each with 5–20 rules. Rules change monthly; changes are made by credit-union staff through an
  admin tool (out of scope), stored as configuration.
- Inputs: applicant data (score, income, debts, membership start date, existing loans), loan amount and term.
- Output: approve / decline / refer-to-human, **with reasons** ("DTI 47% exceeds 43% maximum"). Reasons are shown to
  the member (regulatory adverse-action notices).
- The credit score comes from an external bureau call (slow, ≈ 1–3 s, costs money per call).
- Evaluation volume: a few thousand applications a day. Correctness and explainability matter more than speed.

**Expected shape.** `Rule` interface (`Evaluate(Application) Result` with outcome + reason); concrete rules
parameterised by config (`MinScore{Min: 680}`, `MaxDTI{Max: 0.43}`); a `RuleSet` per tenant (composite) with a
combining policy (any decline → decline, any refer → refer); a factory/registry from config (`{"type": "max_dti",
"max": 0.43}`) to `Rule`; a bureau adapter behind an interface, called lazily and at most once per application.
Money as integer minor units; ratios computed deterministically.

**Requirement change (0:30).** "Some credit unions want rules that combine others: 'decline if score < 650 unless
the applicant has been a member for more than 5 years'. And every decision must record which rule set version was
used."

**Probes.**
- "Why is `Rule` an interface and not a struct with a type field and a switch?" — *Listen for:* rules vary by
  behaviour and new kinds arrive; tests per rule; the flip: a closed set of three comparisons could be a table.
- "A credit union edits its rules while applications are being evaluated." — *Listen for:* immutable rule-set
  snapshot per evaluation (version number, swap via `atomic.Pointer` or a lock), so one application never sees half
  old and half new rules.
- "The bureau is down. What does the engine return?" — *Listen for:* refer-to-human (not decline) with a reason;
  short timeout; no caching of a failure as a score.
- "How do you avoid calling the bureau when a cheap rule already declines?" — *Listen for:* ordering by cost /
  lazy evaluation; but explainability may require evaluating all rules: say which and why.
- "How does a credit union's config get validated?" — *Listen for:* factory returns errors for unknown types and
  bad params at load time, not at evaluation; tests per config error.
- "Tenant A's rules must never apply to tenant B." — *Listen for:* rule sets keyed by tenant id; lookup in one place.

**Red flags.** Floating-point money; `if creditUnion == "X"` in code; reasons as free-text log lines; the combinator
change requires rewriting every rule.

**Draws on.** B2 strategy and factory; B1 constructors that validate; composite (B2 decorator family); A9 tenancy.

---

## 3. Food-delivery order lifecycle

**Prompt.** "Design the order management part of a food-delivery app: customers place orders with restaurants,
restaurants accept and prepare them, and couriers pick them up and deliver them."

**Answer sheet.**
- In scope: order lifecycle, assigning a courier, notifying parties of status changes. Out: payments (assume paid),
  menus, search.
- States the business cares about: placed, accepted, rejected, preparing, ready, picked up, delivered, cancelled.
  Customers can cancel before the restaurant accepts; after that only support can.
- Courier assignment: today, nearest available courier. "We'll experiment with other strategies."
- Notifications: customer push, restaurant tablet, courier app. A notification failing must not block the order.
- Load: thousands of active orders per city instance; several couriers may be offered the same order.

**Expected shape.** `Order` with a status enum and a transition table (legal moves + who may make them); an
`OrderService` owning orders under a lock; `AssignmentStrategy` interface (`Choose(order, couriers) (CourierID,
error)`); observers for status changes called outside the lock or via a queue; a `Clock`.

**Requirement change (0:30).** "Restaurants want to batch: one courier picks up two orders from the same restaurant
if their delivery addresses are close."

**Probes.**
- "Two couriers accept the same order at the same moment." — *Listen for:* compare-and-set on the order's courier
  under the service lock; one wins, the other gets `ErrAlreadyAssigned`; tested with concurrent goroutines.
- "Where do the legal transitions live, and how would support see why an order was cancelled?" — *Listen for:* one
  transition table/method; an append-only history of transitions with actor and reason.
- "The push provider is down for ten minutes." — *Listen for:* notifications are fire-and-forget with a bounded
  queue and retry; the order state is the source of truth; customers see status on refresh.
- "How does the batching change touch your design?" — *Listen for:* a new strategy or a new `Delivery` grouping
  orders; whether `Order` → courier becomes `Delivery` → courier, and the smallest edit if the model was one courier
  per order.
- "Why not one goroutine per order?" — *Listen for:* thousands of goroutines are cheap but lifecycle/shutdown and
  testing get harder; events are rare per order; a lock is simpler; flips if each order has timers (prep timeouts).

**Red flags.** Status as string compared in many places; subclasses per state with duplicated logic; notifications
called under the lock; assignment hard-coded in the service.

**Draws on.** B2 state, strategy, observer; B3 `pubsub` delivery semantics.

---

## 4. Chess with variant extensibility

**Prompt.** "Design a chess game that two players can play against each other. Focus on the model and move
validation."

**Answer sheet.**
- Standard rules including castling, en passant and promotion; check and checkmate detection. Stalemate: yes. Draw
  by repetition / 50-move rule: "nice to have".
- No AI opponent, no UI; a method to make a move and query the board/state.
- Games are independent; many games run in one process (an online server), but each game has two players who move
  in turn.
- Moves arrive as from/to squares (plus promotion piece).

**Expected shape.** `Board` (8×8 array of `Piece` values, or a map) as value type with copy for "would this leave the
king in check"; `Piece` = kind + colour (data, not subclasses), move generation per kind via a function table or a
small interface; `Game` owns board, turn, history (for en passant, castling rights, repetition) and status. Validation
= pseudo-legal moves filtered by "own king not in check after move".

**Requirement change (0:30).** "We want to support Chess960 (randomised back-rank starting positions) and a 'king of
the hill' variant where moving your king to a centre square wins."

**Probes.**
- "Why aren't `Rook`, `Bishop` etc. types with a `Moves()` method?" — *Listen for:* either is defensible; data +
  movement table keeps `Board` copying cheap and serialisation simple; types per piece win if pieces get rich
  behaviour (fairy pieces). Cost and flip stated.
- "How do you detect check efficiently?" — *Listen for:* from the king, scan rays and knight/pawn squares for attackers
  rather than generating all opponent moves; complexity stated; correctness before optimisation.
- "Two requests for the same game arrive concurrently (a double-click)." — *Listen for:* per-game lock or owner; turn
  check inside the critical section; a move number/version so a stale move is rejected.
- "How do you test castling rules?" — *Listen for:* table-driven positions from a string notation (e.g. FEN-like),
  expected legal/illegal; a fixture builder.
- "Where does the variant land?" — *Listen for:* starting-position generator and win-condition checks as injected
  rules (`Variant` interface with `Setup` and `Outcome`), with standard chess as one implementation; no `if variant ==`
  scattered in move validation.

**Red flags.** Class hierarchy `Piece → Rook` with board logic inside pieces mutating global state; check detection
missing; no way to undo a hypothetical move.

**Draws on.** B1 values vs entities, copying; B2 strategy; B3 `elevator`-style explicit state.

---

## 5. Configurable issue-tracker workflows

**Prompt.** "Design the workflow part of an issue tracker. Teams define the statuses an issue can be in and how it
moves between them."

**Answer sheet.**
- Each project defines its own workflow: statuses (e.g. Open, In Progress, In Review, Done) and allowed transitions.
- Transitions can have conditions: "only the assignee can move to In Review", "can't close without a resolution".
- Every transition is recorded: who, when, from, to, comment. Auditors ask "how did this get to Done?".
- Admins can change a workflow while issues are in flight. Issues in a status that no longer exists must stay
  valid.
- Scale: thousands of projects, millions of issues overall, but this is the in-memory domain core; persistence is
  behind a repository.

**Expected shape.** `Workflow` (immutable value: statuses, transitions, version); `Transition` with `[]Condition`
(interface or func type: `Check(issue, actor) error`); `Issue` referencing workflow id + version; `IssueService.Move
(issueID, to, actor)` that loads, validates, applies and appends a `HistoryEntry`; `Repository` interface.

**Requirement change (0:30).** "Some transitions should trigger actions: moving to Done notifies watchers and closes
linked sub-tasks; moving to In Review assigns a reviewer automatically."

**Probes.**
- "An admin removes the In Review status while 300 issues are in it." — *Listen for:* versioned workflows; issues keep
  the version they're on or a migration mapping is required by the admin action; never silently invalid.
- "Two people move the same issue at the same time." — *Listen for:* optimistic concurrency (issue version, compare
  and swap in the repository) or a per-issue lock; second gets a conflict error.
- "Closing linked sub-tasks fails for one of them. Is the parent Done?" — *Listen for:* the explicit choice: the
  primary transition commits; post-actions are best-effort with retry and recorded failures, or the whole thing is
  one unit and fails; say which and why.
- "How do you make conditions testable?" — *Listen for:* conditions are pure functions of issue + actor; table tests
  per condition; the service tested with a fake repository.
- "Could a malicious user craft a request to skip a transition?" — *Listen for:* the server validates from→to against
  the workflow and conditions; the client's idea of current status isn't trusted.

**Red flags.** Hard-coded status enum; history reconstructed from current state; conditions as `switch` on project
name; actions run under a lock that also covers other issues.

**Draws on.** B2 state (transition tables), observer, strategy; B1 repository seams; A9 audit trails.

---

## 6. Ride booking with pricing and surge

**Prompt.** "Design the core of a ride-booking service: a rider requests a ride, we find a driver, price the trip,
and track it until drop-off."

**Answer sheet.**
- In scope: request, match, price estimate and final fare, trip states, cancellation. Out: maps/routing (assume an
  ETA/distance service), payments (assume charged later), ratings.
- Pricing: base + per-km + per-minute, by vehicle class (economy, premium). Surge multiplier per zone when demand
  exceeds supply. The price shown at request time is honoured unless the route changes a lot.
- Matching: nearest available driver by ETA; the driver can decline (offer times out after 15 s), then the next one.
- Cancellation fee if the rider cancels > 2 minutes after a driver accepts.

**Expected shape.** `Trip` with states (Requested, Offered, Accepted, Arrived, InProgress, Completed, Cancelled);
`Matcher` with a `DriverLocator` interface; `Pricer` interface with `Quote` and `Fare`; surge as a separate
`SurgeProvider` (or a decorator over the base pricer); a quote id/lock-in that the final fare references; injected
clock for offer timeouts and cancellation windows; money in minor units.

**Requirement change (0:30).** "Add ride pooling: two riders going the same way share a car, each pays a discounted
fare, and either can cancel without cancelling the other."

**Probes.**
- "Two riders are matched to the same driver at the same instant." — *Listen for:* driver availability claimed
  atomically (CAS on driver state under the locator's lock or a per-driver lock); losing request tries the next
  driver.
- "Surge changes between quote and booking." — *Listen for:* quotes are immutable with an expiry; booking references
  the quote id; expired quote → re-quote; tested with an injected clock.
- "Why is surge a decorator/separate provider and not inside the pricer?" — *Listen for:* independent variation and
  testing; the flip if surge depends on vehicle-class pricing internals.
- "The ETA service is slow." — *Listen for:* timeout with fallback to straight-line distance for matching; no
  blocking under the matching lock.
- "How would you test the 15-second offer timeout?" — *Listen for:* fake clock or timer interface; no sleeping.

**Red flags.** `float64` money; pricing logic duplicated across vehicle classes via subclasses; pooling requires
rewriting `Trip` (one rider per trip hard-wired everywhere) without naming the smallest edit (`Trip` → many
`RideRequest`s).

**Draws on.** B2 strategy and decorator; B3 `ticketing` holds and expiry; B1 injected clock.

---

## 7. Promotions and pricing rule stacking

**Prompt.** "We sell products online. Marketing wants to run promotions: percentage discounts, buy-one-get-one,
discounts on categories. Design the part that computes the final price of a cart."

**Answer sheet.**
- Promotion types today: percent off an item or category, fixed amount off the cart above a threshold, buy X get Y
  free. More will come.
- Stacking: by default at most one item-level promotion per item plus one cart-level promotion; some promotions are
  marked "stackable".
- Price shown must equal price charged; receipts list each applied promotion and its saving.
- Money in a single currency; rounding: half-up to the cent per line.
- Thousands of carts priced per second during sales; promotions change a few times a day.

**Expected shape.** `Promotion` interface (`Apply(cart) []Adjustment` or `Eligible` + `Discount`); a `PricingEngine`
with an explicit, deterministic order (item-level by priority, then cart-level); `Adjustment` value objects
(promotion id, line, amount) so receipts and totals come from the same data; immutable promotion set snapshot.

**Requirement change (0:30).** "Add coupon codes: a customer enters a code that activates a promotion just for them,
and each code can be used once per customer."

**Probes.**
- "Two promotions both apply to an item. Which wins?" — *Listen for:* an explicit policy (best for customer, or
  priority) implemented in one place and tested; determinism (same cart → same price every time).
- "Why is the order of application important?" — *Listen for:* percent-then-fixed differs from fixed-then-percent;
  rounding per line vs per cart; a worked example with numbers.
- "Promotions change while carts are being priced." — *Listen for:* snapshot per pricing call; checkout re-prices
  and shows a diff if it changed.
- "How does 'once per customer' work if the customer double-submits checkout?" — *Listen for:* redemption recorded
  atomically with an idempotency key per checkout; the second submit returns the first result.
- "Is this a strategy pattern or a rules engine?" — *Listen for:* small interface per promotion type is enough;
  a general rules DSL is over-engineering until marketing needs to author rules without deploys.

**Red flags.** `float64`; discounts computed twice (once for display, once for charge); a `switch` on promotion type
in the engine; order of application undefined.

**Draws on.** B2 strategy, factory; B3 `splitwise` deterministic rounding; B3 `inventory` idempotency.

---

## 8. API gateway middleware pipeline

**Prompt.** "Design the request-processing pipeline of an API gateway: requests come in, go through a series of
steps like authentication and rate limiting, and get forwarded to a backend service."

**Answer sheet.**
- Steps today: request id, authentication (API key or JWT), per-client rate limit, request logging, routing by
  path prefix to a backend, response logging. Order matters.
- Each route can enable a different set of steps (public health check: no auth).
- Backends can be slow or down; the gateway must not hang. Timeouts are per route.
- Config is loaded at startup and reloaded every few minutes without restart.
- Thousands of requests per second per instance.

**Expected shape.** A middleware type (`func(Handler) Handler` or a `Step` interface returning continue/stop), a
per-route chain built from config by a factory; context carries request-scoped values (principal, request id);
routing table as an immutable snapshot swapped on reload (`atomic.Pointer`); backend client interface with deadline
propagation.

**Requirement change (0:30).** "Some clients need request transformation: rename headers and rewrite paths per
route, and we need a circuit breaker per backend."

**Probes.**
- "Where does the rate limiter's state live, and is it safe?" — *Listen for:* per-client buckets in a sharded map or
  with a lock; memory bound (evict idle clients); per-instance vs global limit trade-off stated.
- "Config reloads while 1,000 requests are in flight." — *Listen for:* each request uses the chain it started with
  (snapshot); new requests get the new one; no lock on the hot path.
- "Why `func(Handler) Handler` and not an interface with `Before`/`After`?" — *Listen for:* the decorator closure
  sees both request and response and can short-circuit naturally; interface wins if steps need metadata (names,
  ordering constraints) for config.
- "The auth step must run before rate limiting by client. How do you stop misconfiguration?" — *Listen for:* chain
  built by the factory with declared dependencies/ordering, validated at load; tested.
- "A backend hangs." — *Listen for:* per-route timeout via context; breaker opens after failures; bulkhead/limit
  on concurrent calls per backend so one backend can't exhaust the gateway.

**Red flags.** A single `Handle` method with a long `if route.authEnabled` chain; global mutable config; logging the
`Authorization` header.

**Draws on.** B2 decorator and factory; B3 `resilient` breaker and deadlines; A7 bulkheads; A1 auth.

---

## 9. Text editor with undo/redo

**Prompt.** "Design the core of a text editor: inserting and deleting text, moving the cursor, and undo/redo."

**Answer sheet.**
- Single document, single user (for now). Operations: insert text at cursor, delete range, move cursor, select,
  cut/copy/paste.
- Undo/redo unlimited "within reason"; typing a word should undo as one step, not per character.
- Documents up to ≈ 10 MB; typical edits are small.
- No rendering/UI; the core exposes text and cursor position.

**Expected shape.** `Document` holding the text (a gap buffer, rope or piece table; a plain string is acceptable at
first if the candidate states the O(n) cost per edit); `Command` interface (`Apply(doc)`, `Undo(doc)`) with insert /
delete commands storing what they need to invert; `History` with undo and redo stacks, redo cleared on a new command;
coalescing of consecutive typing into one command; bounded history.

**Requirement change (0:30).** "Two users can now edit the same document at once (each with their own cursor), and
undo must only undo *your own* last change."

**Probes.**
- "What's the cost of inserting a character in the middle of a 10 MB string?" — *Listen for:* O(n) copy ≈ 10 MB per
  keystroke is too slow at typing speed; gap buffer amortised O(1) near the cursor, rope/piece table O(log n); chosen
  with a reason.
- "How does 'undo the whole word' work?" — *Listen for:* coalescing rule (same kind, adjacent position, within a
  time window via injected clock) merges commands; tests for boundaries.
- "Memory after 100,000 edits?" — *Listen for:* commands store only deltas; cap history length or bytes; drop oldest.
- "How does the multi-user change break your undo?" — *Listen for:* positions in a user's old commands shift when the
  other user edits; need to transform positions against later operations (operational transformation idea) or anchor
  positions; per-user history; the smallest step: per-user stacks + position adjustment, with the limit named.
- "How do you test undo correctness?" — *Listen for:* property: apply random command sequence then undo all → original
  text; redo all → final text.

**Red flags.** Undo by storing full document copies per keystroke without stating cost; commands that can't be
inverted; redo stack never cleared.

**Draws on.** B2 command/strategy; B3 `kvtx` undo logs; C16 territory for the change (discussion only).

---

## 10. Spreadsheet cells with formulas

**Prompt.** "Design the core of a spreadsheet: cells hold values or formulas that reference other cells, and when a
cell changes, everything that depends on it updates."

**Answer sheet.**
- Formulas: arithmetic on cell references and numbers, plus `SUM(A1:A10)`. No string functions for now.
- Sheet up to ≈ 1M cells, mostly empty; a few thousand formulas.
- Changing a cell must leave every dependent cell correct before the call returns. Cycles must be detected and
  reported (the cell shows an error), not hang.
- Single user, single goroutine for edits; reads can come from other goroutines (a renderer).

**Expected shape.** Sparse map `CellRef → Cell` (raw input, parsed expression, cached value or error); a parser to an
AST (`Expr` interface: `Num`, `Ref`, `Range`, `BinOp`, `Func`); dependency graph both ways (precedents and
dependents); on update: re-parse, update edges, detect cycle (DFS), recompute dependents in topological order.

**Requirement change (0:30).** "Add functions that are volatile, like `NOW()` and `RAND()`, which change on every
recalculation, and let users define named ranges (`Revenue = B2:B13`)."

**Probes.**
- "A1 = B1 + 1, B1 = A1 * 2. What happens when the user enters the second formula?" — *Listen for:* cycle detected
  at edit time via DFS from the edited cell over precedents; both cells show an error; graph left consistent.
- "C1 depends on A1 twice through different paths. How many times is it recomputed?" — *Listen for:* topological
  order over the affected subgraph so each cell is computed once; naive recursive recompute is exponential in the
  worst case.
- "A renderer goroutine reads cells while an edit is recalculating." — *Listen for:* RWMutex around the sheet, or
  publish an immutable snapshot after recalculation; never let readers see half-updated values.
- "`SUM(A1:A100000)` over mostly-empty cells." — *Listen for:* iterate stored cells in the range or keep per-column
  indexes, not 100k empty lookups; range dependency stored as a range, not 100k edges.
- "Why an AST and not re-parse the string every time?" — *Listen for:* parse once on edit; evaluation fast and
  testable; references extracted from the AST for the graph.

**Red flags.** Dense 2D array for 1M cells; recompute everything on every edit without saying the cost; cycle
detection by recursion depth limit.

**Draws on.** B3 `memfs` tree/graph handling; B2 interpreter-ish interfaces; B0 snapshots for readers.

---

## 11. Multi-tenant feature-flag evaluator 🏦 (tenancy)

**Prompt.** "We want feature flags across our platform: turn features on for some customers and not others, and roll
them out gradually. Design the evaluation library our services call."

**Answer sheet.**
- Platform: ≈ 40 credit unions (tenants), each with up to ≈ 500k members. Services call `IsEnabled(flag, context)`
  on every request: must be in-process and fast (≈ µs), no network call per evaluation.
- Targeting: per tenant on/off; allow/deny lists of member ids; percentage rollouts ("10% of members of tenant X");
  attribute rules ("members in state = CA").
- A member must see a **stable** result: the same member stays in the 10% when it grows to 20%.
- Flag definitions are fetched from a config service every 30 s; if the config service is down, keep evaluating.
- Every flag change is audited (who changed what), but that's in the config service; the library logs evaluation
  counts for metrics.

**Expected shape.** `Flag` definition (immutable) with ordered `Rule`s (interface: `Match(ctx) (bool, matched)`);
`Evaluator` holding a snapshot map behind `atomic.Pointer`, refreshed by a background loader through a `Source`
interface; consistent bucketing: `hash(flagKey + memberID) mod 10000 < pct*100`; default value on unknown flag;
evaluation context struct with tenant id required.

**Requirement change (0:30).** "Some flags need variants, not just on/off: e.g. 'checkout-layout' returns A, B or C
with weights 50/30/20, and we need to know which variant each member saw for analysis."

**Probes.**
- "Why hash flag key + member id instead of just member id?" — *Listen for:* otherwise the same 10% of members get
  every experiment (correlated rollouts); stability when the percentage grows because the threshold rises over the
  same bucket.
- "The config service has been down for an hour." — *Listen for:* keep last good snapshot (possibly persisted to
  disk at startup); expose snapshot age as a metric; defaults only when nothing was ever loaded.
- "10,000 evaluations per second per service. Any locks?" — *Listen for:* reads load an immutable snapshot pointer;
  no lock on the hot path; refresh swaps the pointer; counters via atomics or sharded.
- "Could tenant A's rule ever apply to tenant B's member?" — *Listen for:* tenant id is part of the flag lookup key
  or the first rule; context without tenant is rejected; tested.
- "How do you test the 10% rollout?" — *Listen for:* deterministic hash; over 100k synthetic ids the share is within
  a small tolerance; a specific id's result pinned in a test.
- "Why not evaluate on the config service with an RPC?" — *Listen for:* latency and availability on every request;
  wins only for flags needing server-side secrets or very large lists.

**Red flags.** `rand.Float64() < pct` per evaluation (unstable); a mutex taken on every evaluation for reading a map
that changes every 30 s without stating the cost; no default on unknown flags.

**Draws on.** B2 strategy; B3 `hashcache` immutable snapshots; A9 tenancy; A8 metrics.
