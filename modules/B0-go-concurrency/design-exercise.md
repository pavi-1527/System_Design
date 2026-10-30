# B0 Design exercise — Vendor status poller

> Time box: 50 minutes of coding + 10 minutes for the concurrency note, solo, no notes, no AI help. The Go
> standard library docs (`go doc`) are allowed, as they would be in a machine-coding round. Then self-score, then
> send the code, test output and note to the tutor.

## Prompt

*As an interviewer would say it:*

"Our integration gateway talks to about 30 vendors: SSO providers, imaging, KYC, FX. On-call wants a live view of
which vendors are healthy. Build a component that periodically checks every vendor's health endpoint and lets the
rest of the service read the latest status of each vendor, and get notified when a vendor's status changes. Some
vendors are slow, some hang, and a few have strict rate limits on their health endpoint. It runs inside the
gateway process, so it must shut down cleanly on deploy. You have 50 minutes; I care about correctness under
concurrency and how you'd convince me it's correct."

This is deliberately underspecified. Clarifying questions worth asking (or stating as assumptions in your note):
check interval and per-check timeout; maximum checks in flight; what "status" contains (up/down, latency, last
error, last checked time); what happens to a check that is still running when the next tick fires; what a
subscriber gets if it is slow or stops reading; whether `Run` can be called twice; what "shut down cleanly" means
in time.

Suggested starting API (you may change it if you say why):

```go
type Checker interface {
	Name() string
	Check(ctx context.Context) error // nil = healthy
}

type Status struct {
	Vendor    string
	Healthy   bool
	Err       error
	Latency   time.Duration
	CheckedAt time.Time
}

type Options struct {
	Interval       time.Duration
	CheckTimeout   time.Duration
	MaxConcurrency int
}

func New(checkers []Checker, opts Options) *Poller
func (p *Poller) Run(ctx context.Context) error          // blocks until ctx is done and all checks have finished or been abandoned
func (p *Poller) Snapshot() map[string]Status            // safe for concurrent use; caller owns the returned map
func (p *Poller) Subscribe(buf int) (<-chan Status, func()) // status CHANGES; the func unsubscribes
```

## What to produce

1. **Code**: a package (suggested location `labs/b0/_design/poller/`. Directories starting with `_` are ignored by
   `./...`, so `sd lab B0` won't pick it up) with the implementation and tests. Run
   `go test -race -count=20 ./labs/b0/_design/poller/` and keep the output.
2. **Tests** that cover at least: a slow checker hitting `CheckTimeout`; a checker that **ignores ctx**; the
   `MaxConcurrency` bound; `Snapshot` read concurrently with updates; a subscriber that **stops reading**; `Run`
   returning promptly after cancel with **no goroutine leak** (settle-loop count check). Inject time (a ticker
   factory or a manual `tick` channel) rather than sleeping through real intervals.
3. **Concurrency-safety note** (≤ 250 words, as a doc comment or a short text file next to the code), under these
   headings: *Shared state and owners* (what is shared, who may touch it, under which lock or channel); *Bounds*
   (what limits goroutines, in-flight checks, memory); *Lifecycle* (how `Run` starts, what cancellation does, what
   `Run` waits for before returning); *Failure handling* (slow, hanging, panicking checkers; slow subscribers);
   *Evidence* (which test proves which property).

## Rubric

| Dimension | below | approaching | meets (senior bar) | exceeds |
|---|---|---|---|---|
| Correctness under `-race` | Data races, or tests not run with `-race`; status map mutated without synchronisation | Race-free on the happy path, but `Snapshot` returns the internal map (callers can race on it) or a subscriber list is mutated during iteration | `-race -count=20` clean with real concurrent readers/writers; `Snapshot` returns a copy; subscriber set guarded; no check-then-act gaps | Also argues *why* each access is safe (happens-before edges named), or uses an immutable snapshot behind `atomic.Pointer` with justification |
| Cancellation and leak freedom | `Run` doesn't return after cancel, or leaks a goroutine per tick/check | `Run` returns, but a ctx-ignoring checker leaks a goroutine (unbuffered result) or `Run` waits forever for it | Every check gets `ctx` with `CheckTimeout`; ctx-ignoring checkers are abandoned with a buffered result; `Run` returns after cancel within a bounded time; leak test proves goroutines return to baseline | Overlapping ticks handled deliberately (skip, or don't start a new check for a vendor whose previous check is still running) and tested; abandoned checks counted as a metric |
| Bounded concurrency and backpressure | One goroutine per vendor per tick with no bound; a slow subscriber blocks polling | A semaphore exists, but the semaphore wait has no ctx, or the subscriber channel blocks the poller | `MaxConcurrency` enforced (tested); semaphore acquire selects on ctx; slow/stopped subscribers cannot block the poller (buffered + drop-oldest or drop-with-counter, stated explicitly) | Per-vendor rate respect (min interval per vendor, or jittered start to avoid a thundering herd of 30 checks at t=0) with a test |
| API clarity and ownership | Exported mutable internals; unclear who closes what | Works, but contract undocumented (is `Snapshot` a copy? can `Run` be called twice? who closes subscriber channels?) | Doc comments state the concurrency contract; channels closed by their owner (poller closes subscriber channels on unsubscribe/shutdown, exactly once); `Run`-twice behaviour defined | API shaped so misuse is hard (e.g. `Run` returns an error on second call; unsubscribe is idempotent), and trade-offs of the alternative API named |
| Testability and tests | No tests, or tests that sleep and hope | Tests exist, but use real intervals/sleeps, have no timeouts, or don't run concurrent callers | Injected time; event-based waits with timeouts; fake checkers (fast, failing, slow, ctx-ignoring, panicking); concurrency tests run under `-race`; leak check | Tests are deterministic under `-count=100` on a loaded machine; a "break it" mutation is shown to fail a test |
| Explanation (note + narration) | No note, or "it's thread-safe" | Note lists mechanisms but not owners/bounds/lifecycle, or contradicts the code | All five headings answered specifically and matching the code; can narrate it in ≤ 2 minutes (Unit 2 §7) | Note includes "what I'd change for production" (metrics, per-vendor policies, persistence of last status across restarts) with costs |

## Tutor critique protocol

1. **Run before reading.** `go vet` and `go test -race -count=20` on the learner's package. Then mutate one thing
   (make the subscriber channel unbuffered, or remove the semaphore's ctx case) and check that a test fails.
   Record whether it did.
2. **Read the note, then the code**, checking that every claim in the note is true in the code (owner of each
   piece of shared state, who closes each channel, what `Run` waits for).
3. **Ask the probes below** one at a time. The learner answers from memory, out loud or in writing, in ≤ 2 minutes
   each. Model answers are in Unit 2 (§1–§7). Score the answer, not the vocabulary.
   - "What happens if a subscriber stops reading?"
   - "How do you know there are no goroutine leaks? Show me."
   - "What if a vendor's `Check` ignores ctx and hangs for 10 minutes? What does it cost you, and what does it cost
     the vendor?"
   - "The interval is 5 s, and one check takes 8 s. What happens on the next tick?"
   - "SIGTERM arrives in the middle of a poll. Walk me through until `Run` returns. How long can that take?"
   - "All 30 checks fire at the same instant on startup. Is that a problem? For whom?"
   - "Why did you pick that subscriber buffer size / drop policy?"
   - "Two goroutines call `Run` at once. What happens?"
   - "A checker panics. What happens to the gateway?"
4. **Score each dimension** using the rubric, citing a specific line, test or answer for each score.
5. **Overall "meets"** requires *meets* or better on **Correctness under `-race`** and **Cancellation and leak
   freedom**, and no dimension at *below*. One *approaching* elsewhere is acceptable if the learner corrects it
   when probed (note that in the record).
6. **Feedback**: one thing to keep, the single most important fix, and one probe to revisit with `sd review`.
   If the overall result is below *meets*, the learner redoes the exercise on a different day with a variant
   (e.g. "bounded, cancellable, rate-respecting link checker with graceful shutdown").

## Recording

`sd record design B0 --self <below|approaching|meets|exceeds> --tutor <level> --notes "<one-line summary>"`
