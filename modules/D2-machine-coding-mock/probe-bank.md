# D2 Probe bank — follow-up probes for machine-coding rounds

Generic probes that apply to any problem in the bank, grouped by category. Each problem in
[problem-bank.md](problem-bank.md) adds 4–6 problem-specific probes on top.

**Reading this is fine, and useful.** Probes are predictable in kind; what's scored is whether your answer is true of
*your* code, names a mechanism, carries a number where one exists, and says when the answer flips. Rehearse a few
aloud against code you've written. Don't memorise model answers: the tutor pushes one rung further on anything that
sounds recited.

**How the tutor uses it.** Ask 4–6 probes at 1:30–1:45 (and at most two mid-mock), one at a time, including at least
one *scale-up*, one *failure* and one *why not X*. Answers count toward rubric dimension 7 (Walk-through & probe
handling) and can move dimension 3 (Correctness & concurrency) if they expose a flaw. "Listen for" is what a *meets*
answer contains; an *exceeds* answer usually adds how they'd measure it and what they'd do next.

## Scale-up (10× / 100×)

| # | Probe | Listen for (meets) |
|---|---|---|
| S1 | "What's the first thing that breaks at 10× the load?" | Names the specific resource in *their* code (the single mutex, an unbounded slice, a goroutine per item, an O(n) scan on the hot path), with an estimate (≈ 1 µs critical section → ≈ 10⁶ ops/s per lock) and how they'd confirm it (mutex/block profile, `pprof`, a benchmark) |
| S2 | "One key gets 90% of the traffic. What happens?" | Sharding doesn't help a single hot key; per-key serialisation is now the bottleneck; options: batching/coalescing, splitting the key's state (striped counters), or accepting it with a number |
| S3 | "Your single lock shows 30% wait time in a profile. What do you change?" | Shard by an independent key (each shard's operations stay linearizable) before considering lock-free; names the cross-shard operations that get harder (snapshots, totals) and how they'd handle them (lock all shards in index order, or accept a non-atomic total) |
| S4 | "Memory grows without bound during a long test. Where?" | Points to the unbounded structure (retry queue, history, dedupe set, per-key map that never shrinks) and a bound with a policy: cap + reject, cap + drop oldest, TTL eviction; says which is acceptable for *this* data |
| S5 | "What's the complexity of your hot operation, and does it matter at 1M entries?" | States big-O for the hot path and the data structure that gives it (map + heap with back-index, sorted slice + binary search); one sentence on when the constant factor matters more than big-O |
| S6 | "This has to run across three processes now. What breaks?" | Which in-memory invariants now need coordination (uniqueness, ordering, at-most-once), what moves to a shared store or a single owner per key (partition by key), and which parts stay local; hands off to HLD terms (leases, idempotency keys) without hand-waving "use Redis" |

## Failure

| # | Probe | Listen for (meets) |
|---|---|---|
| F1 | "The dependency behind your interface takes 30 seconds to answer. What happens to callers?" | Whether the slow call is under a lock (it mustn't be); per-call timeout via `context`; bounded concurrency so slow calls can't pile up goroutines; what the caller sees (error type) |
| F2 | "A callback / sender / handler you call panics. What happens?" | Whether a panic kills a worker goroutine or the process; `recover` at the goroutine boundary, converted to an error and counted; state left consistent because no lock is held across the call |
| F3 | "The process crashes in the middle of this operation. What state is left?" | In-memory: everything is lost, so say what a caller must retry; which operations are idempotent so the retry is safe; for persistence, where the write-ahead record would go (before the state change) |
| F4 | "`Close` is called while operations are in flight. Walk me through it." | Stop accepting (returns a sentinel), drain within the `ctx` deadline, report what was dropped, `WaitGroup` for workers, no goroutine leak; second `Close` is safe |
| F5 | "A retry arrives while the first attempt is still running." | Idempotency key recorded *before* executing (in-flight state), the duplicate waits or gets "in progress", same key + different payload is an error; no double effect |
| F6 | "The clock jumps backwards by a minute." | Their injected clock makes it testable; use monotonic time for durations (`time.Since` uses it), wall time only for display; what breaks if they compare wall times (early/late expiry) |

## Concurrency and consistency

| # | Probe | Listen for (meets) |
|---|---|---|
| C1 | "Two goroutines do this to the same key at the same moment. Show me the test." | Points to an actual concurrent test on the *same* key with an end-state invariant, run with `-race -count=N`; if there isn't one, says so and describes it precisely |
| C2 | "Walk me through your lock order. What if I request the resources in the opposite order?" | A global order (sorted IDs, index order) for multi-resource operations, or a design that never holds two locks; why reversing it can deadlock (circular wait) |
| C3 | "Where's your check-then-act?" | Finds it (`if available { take }`, quota check, existence check before insert) and shows it's inside one critical section or a CAS |
| C4 | "You hold the lock while calling X. Why is that safe?" (only if they do) | Either it isn't (and they say so, and how to snapshot-then-call), or X is a pure, bounded, non-blocking function they control |
| C5 | "Is your read path consistent? Can a reader see a half-applied update?" | Multi-field updates happen in one critical section, or readers get an immutable snapshot (`atomic.Pointer` swap); names what a reader could observe otherwise |
| C6 | "What happens-before edge makes this safe?" | Unlock → subsequent lock of the same mutex; channel send → receive completes; `WaitGroup.Done` → `Wait` returns; `atomic` store → load that observes it. Names the one in their code |
| C7 | "`-race` is clean. Does that prove it's correct?" | No: the detector reports races on executions it observed; it says nothing about logical races (check-then-act across two critical sections) or invariants. Evidence comes from invariant tests under contention, `-count`, `-cpu 1,4` |

## Extensibility

| # | Probe | Listen for (meets) |
|---|---|---|
| E1 | "What's the next requirement you'd expect, and where does it go?" | A specific requirement and the exact seam (interface, option, new type) where it lands; which existing code doesn't change |
| E2 | "Add persistence. What changes?" | A store interface at the consumer (1–3 methods), write-ahead before state change, snapshot + replay for recovery; which in-memory structures become caches |
| E3 | "We need a second implementation of <the strategy/policy>. How?" | The existing interface or function type; contract tests shared by both implementations; no `if kind == …` branches in the core |
| E4 | "Metrics: product wants counts of X per minute. Where do you hook it in?" | An observer/hook interface or counters behind an interface, called outside the lock, with no effect on the core logic's tests |
| E5 | "How did Stage 2 change your Stage 1 code? What would you change in the seam now?" | Honest count (files, functions, ≈ lines); the seam they'd move with hindsight and why it wasn't obvious at 0:10 |

## Testing

| # | Probe | Listen for (meets) |
|---|---|---|
| T1 | "How do you know your tests are good? Show me a bug they'd catch." | Names a realistic mutation (drop the lock, off-by-one at the boundary, skip the dedupe check) and the test that fails with a message naming the property |
| T2 | "Which of your tests could be flaky, and why?" | Any test that depends on scheduling (sleeps, "eventually" without a deadline) or real time; how they removed it (injected clock, event channels, timeouts on every wait) |
| T3 | "How do you test the expiry/backoff without waiting?" | Injected clock (`func() time.Time`) and, for timers, an injected sleeper or tick; exact boundary tests at `t` and `t-1ns` |
| T4 | "What would a model-based test look like here?" | Random sequence of operations applied to their implementation and to a trivial reference (a map, a slice), compare after each step; seeds logged for reproduction |
| T5 | "Your concurrent test hangs if the code is wrong. What stops CI from hanging?" | Every wait has a deadline (`select` with `time.After`, `context.WithTimeout`), plus `go test -timeout`; a helper that fails the test instead of blocking |

## Cost and complexity

| # | Probe | Listen for (meets) |
|---|---|---|
| K1 | "What did you deliberately not build, and why?" | A named list (sharding, persistence, metrics, a second strategy) with the reason it wasn't needed for the stated requirements; says when they'd add each |
| K2 | "This has three goroutines per item. What does that cost?" (if relevant) | ≈ 2 KB of initial stack each (it grows on demand), scheduler overhead, and lifecycle complexity (who stops them); a pool or a single dispatcher instead, with the trade-off |
| K3 | "Is there an abstraction in here you'd remove?" | Identifies one honestly (an interface with one implementation and no test fake, a factory nobody calls), or defends each with the requirement that forced it |
| K4 | "How much memory per entry, roughly?" | A rough per-entry estimate (map overhead + struct + strings, ≈ 100–200 B for small entries) and what that means at 1M entries; what they'd trim first |

## Security and multi-tenancy

| # | Probe | Listen for (meets) |
|---|---|---|
| M1 | "One tenant sends 100× the traffic of the others. What happens to everyone else?" | Whether a shared queue/lock/worker pool lets one tenant starve others; per-tenant bounds (queue cap, concurrency cap, fair scheduling such as round-robin across tenants) |
| M2 | "The input comes from an untrusted client. What would you validate?" | Sizes and counts bounded (no unbounded allocation from input), IDs and paths validated, numbers range-checked (overflow on minor units), errors that don't leak internals |
| M3 | "Could one tenant read or affect another tenant's data through your API?" | Tenant as part of every key, not a filter applied later; no global mutable state keyed by user-supplied strings alone |
| M4 | "Where would a secret (an API key, a signing key) live, and how would you rotate it?" | Injected at construction (never a global), key ID carried with the signature, accept old + new during rotation, constant-time comparison (`hmac.Equal`) |

## "Why not X"

| # | Probe | Listen for (meets) |
|---|---|---|
| W1 | "Why a mutex and not channels / a single owner goroutine?" | Mutex for short critical sections on shared state (simplest, ≈ tens of ns uncontended); a single owner goroutine when ordered event processing or determinism matters; cost of the goroutine (lifecycle, back-pressure, harder errors); flip condition |
| W2 | "Why not `sync.Map`?" | `sync.Map` fits append-mostly keys or disjoint key sets per goroutine; it doesn't make multi-step operations atomic, loses type safety, and can't protect invariants across fields; a plain map under a mutex is simpler here |
| W3 | "Why not generics for this container?" | Generics when the container is genuinely type-agnostic and used with several types; here one concrete type keeps the code shorter; flip when a second element type appears |
| W4 | "Why not spawn a goroutine per item/request?" | Unbounded concurrency on slow dependencies (Little's law: rate × latency goroutines in flight); a bounded worker pool or semaphore gives back-pressure and a place to stop; fine when work is CPU-light and bounded |
| W5 | "Why not a `time.Timer` per entry for expiry?" | Thousands of timers are OK in Go but create lifecycle and stop/reset subtleties; a lazy check on access plus a heap-driven sweeper is deterministic and testable with an injected clock; flip when callbacks must fire on time without access |
| W6 | "Why not just use `atomic` everywhere?" | Atomics protect single words; invariants across fields need a lock or an immutable snapshot swap; CAS loops are harder to read and test; fine for counters and flags |
| W7 | "Why an interface here and not a function type?" | A one-method dependency is often clearer as `func(...)` (clock, sleeper); an interface when there are 2–3 related methods or implementations carry state; both are consumer-side |
