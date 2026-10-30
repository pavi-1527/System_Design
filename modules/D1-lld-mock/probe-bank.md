# D1 Probe bank — generic LLD follow-ups

Probes an interviewer can ask about almost any LLD design, by category. Each problem in the
[problem bank](problem-bank.md) has its own specific probes; the tutor uses those first and fills in from here so that
every mock covers at least three categories (always one concurrency and one failure probe).

**Reading this is fine.** Probes are predictable in kind; what's scored is the answer for *your* design, under the
clock. Practise by picking any B3 problem you've solved and answering five probes from different categories out loud
in ≲ 2 minutes each. The shape of a good answer is always claim → why here → cost → when it flips
([framework card](../../docs/INTERVIEW_FRAMEWORK.md#stating-a-trade-off-use-this-shape-every-time)).

"Listen for" is what a *meets* answer contains. Missing the cost or the flip condition is *approaching* at best
([rubric](rubric.md), dimension 6).

## Scale-up

| # | Probe | Listen for (meets) |
|---|---|---|
| S1 | "Now there are 10× as many [objects]. What in your design breaks first?" | Names the data structure and the operation: a linear scan that becomes O(n) per call, a map that's fine, a slice that's copied on every read; gives the replacement (index map, heap, sorted structure) and its cost |
| S2 | "10× more callers per second. Where's the contention?" | Names the lock or owner goroutine every request passes through; estimates critical-section length (≈ µs) × rate to say whether it's actually a problem; sharding or per-key locks only with the condition that justifies them |
| S3 | "The history/audit list grows forever. What happens after a year?" | Unbounded memory in-process; moves history behind a store interface, keeps a bounded window or snapshot + log in memory; says what queries still need fast access |
| S4 | "What's the complexity of [the hard method]? Is that OK?" | A real big-O with the variable named (n = active reservations, not "items"); ties it to stated sizes; doesn't optimise what's already fine at the stated scale |
| S5 | "Could this run on several instances behind a load balancer?" | Identifies the in-memory state that becomes shared state; which part needs a DB transaction or a single owner per key (partition by id); idempotency for retried requests across instances |

## Failure

| # | Probe | Listen for (meets) |
|---|---|---|
| F1 | "The external dependency [card processor, insurer, shipping carrier] times out. What state are you in?" | Distinguishes *failed* from *unknown*; an unknown outcome gets its own state (pending/indeterminate), a retry with the same idempotency key, or a reconciliation path; never assumes failure when money or goods may have moved |
| F2 | "The operation fails halfway: step 2 of 3 succeeded. Now what?" | Either ordering that makes the earlier steps reversible (reserve → commit), a compensating action, or a single atomic step under one lock; says which invariant would otherwise break |
| F3 | "A callback or observer panics or blocks. What happens to the caller?" | No user code under a lock; observers called after state change with recovery or via a bounded queue; a slow observer can't stall the core path; says what's lost if the queue is full |
| F4 | "The process restarts mid-operation. What do you lose?" | In-memory state is gone; names which state must be durable (behind a repository interface) and the write ordering that keeps it consistent; out of scope is acceptable if said explicitly |
| F5 | "A duplicate request arrives (client retried)." | Idempotency key per operation; first result stored and returned; same key with different parameters is an error; key scope (per tenant, per account) stated |
| F6 | "Invalid input slips through: what stops it corrupting state?" | Validation in constructors and at the boundary; invariants enforced where state changes, not at every caller; typed values (e.g. `Money int64`) so invalid states don't compile |

## Concurrency & consistency

| # | Probe | Listen for (meets) |
|---|---|---|
| C1 | "Two callers do [the contended operation] at the same instant. Walk me through it." | A specific mechanism (mutex around the aggregate, CAS on a state field, single-writer goroutine); the critical section named; exactly one wins and the other gets a defined error |
| C2 | "Who owns this map/slice/struct?" | One owner, one sentence: "the `Registry`, under `mu`; nothing else holds a reference". Returning internal slices/maps without copying is called out |
| C3 | "Your method takes two locks. What about deadlock?" | A global lock order (by id), or a single coarser lock, or try-lock with backoff; says which and why; a test that runs the opposite order |
| C4 | "Why a mutex and not channels?" | Mutex for protecting shared state with short critical sections; a goroutine owner when events must be processed in order or the state machine is easier as a loop; cost of each (lock contention vs a goroutine to manage and shut down) |
| C5 | "A reader sees the object while a writer is halfway through updating it. Possible?" | Not if both go through the same lock, or if readers get an immutable snapshot (copy or `atomic.Pointer`); names which, and what a torn read would look like |
| C6 | "How would you prove it's race-free?" | `go test -race` with concurrent callers, `-count` > 1, an invariant asserted after the stress run; a test that fails if the lock is removed |

## Extensibility

| # | Probe | Listen for (meets) |
|---|---|---|
| E1 | "Now add [a new variant: method, rule, type]. Show me exactly what changes." | Points at the seam; one new type/function plus registration; existing types untouched; if no seam exists, the smallest edit and the new seam |
| E2 | "Product says this rule differs per customer/tenant." | Config or strategy keyed by tenant, injected at construction or looked up per request; validation of the config; the default |
| E3 | "Add audit/notifications/metrics to every state change." | A single place where transitions happen (so the hook has one call site); observer or decorator; ordering and failure semantics (audit must not be lost, notifications may be) |
| E4 | "How would you support undo / history?" | Commands or an append-only event list; inverse operations or snapshots; memory cost and a bound |
| E5 | "What change would hurt this design most?" | Honest answer naming the axis not abstracted and why it was left concrete; what it would cost to open it later |

## Testing

| # | Probe | Listen for (meets) |
|---|---|---|
| T1 | "How would you test the hard method?" | Table-driven cases including the boundary; injected clock/IDs; a fake for the external dependency; assertions on `errors.Is`, not strings |
| T2 | "How do you test the timeout / expiry path without sleeping?" | Injected clock (`func() time.Time`) or a fake ticker advanced by the test; no `time.Sleep` in tests |
| T3 | "How do you test concurrency?" | N goroutines on the contended operation, a `sync.WaitGroup`, a deadline helper so a deadlock fails instead of hanging, invariant check after, `-race` |
| T4 | "How do you know a new plug-in implementation is correct?" | A shared contract test run against every implementation of the interface |

## Cost & complexity

| # | Probe | Listen for (meets) |
|---|---|---|
| K1 | "Is this over-engineered? Which interface could you delete?" | Can name one and say it was justified (axis of change, test double) or concede and delete it; counts interfaces against stated variation |
| K2 | "A new engineer joins. What's the hardest part of this code to understand?" | Names it (the state machine, the expiry rules) and how the code makes it explicit: transition table, doc comment, invariant test |
| K3 | "What did you deliberately leave out?" | A prepared list: persistence, auth, i18n, a second state machine; each with why it's safe to defer |

## Security & multi-tenancy

| # | Probe | Listen for (meets) |
|---|---|---|
| X1 | "Two tenants share this instance. How do you stop one seeing the other's data?" | Tenant id as part of every key and every lookup; enforced in the repository/service, not by callers; a test that tries to cross the boundary |
| X2 | "Who's allowed to call this method?" | Authorization at the service boundary with a caller/principal parameter; domain types don't trust raw strings for roles |
| X3 | "This object holds a card number / PIN / PII. Anything to change?" | Don't store what you don't need; token instead of PAN; redaction in `String()`/logs; never log secrets; constant-time comparison where it matters |
| X4 | "One tenant floods the system. Effect on others?" | Per-tenant limits or quotas; separate queues/locks so one tenant's backlog doesn't block others |

## Why not X

| # | Probe | Listen for (meets) |
|---|---|---|
| W1 | "Why not an inheritance hierarchy / base class here?" | Go has embedding, not inheritance; variation by data = enum/field, by behaviour = small interface; embedding promotes methods but has no virtual dispatch |
| W2 | "Why not the [observer / state / factory] pattern here?" | When the pattern would pay (more than two implementations, a lifecycle with many legal moves) and why it doesn't yet; or why it does |
| W3 | "Why not generics?" | Generics for containers and algorithms over types; interfaces for behaviour that varies at runtime; the cost (readability, constraints) |
| W4 | "Why a state enum and a transition table instead of the State pattern (a type per state)?" | Table wins with few behaviours per state and audit needs (one place to list legal moves); type-per-state wins when each state has substantially different behaviour |
| W5 | "Why not just a big `switch`?" | A `switch` on a closed, stable set is fine; it loses when the set grows or varies per tenant, because every new case edits shared code |
| W6 | "Why return a struct and not an interface from the constructor?" | Accept interfaces, return structs: callers define their own narrow interfaces; returning an interface hides methods and makes adding one a breaking change |
