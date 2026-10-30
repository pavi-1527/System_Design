# B0 Teach-back

## Prompt

A mid-level engineer on your team is about to write their first vendor adapter that calls several providers
concurrently. They know Go syntax, goroutines and channels, but they have never debugged a leak or a race. Write
them an explanation (**500–800 words, your own words, no copying from the units, one diagram allowed**, Mermaid or
ASCII) of **how to make a concurrent Go component safe and how to prove it**, using the vendor fan-out (the
`fanout` capstone: per-vendor timeout, overall deadline, max concurrency, partial results) as the running
example. Write it as something you would actually put in the team wiki.

Then answer, in two or three sentences each, whichever two follow-up questions the tutor picks from the
"should-hit" list.

## Evaluator checklist

Must-hit (all required for pass):
- [ ] **Ownership**: says, for the example, what is shared and who owns it (e.g. each goroutine writes only its own
      `results[i]`; the caller reads after `wg.Wait()`), and states the rule "the sender/owner closes a channel".
- [ ] **Cancellation is cooperative**: ctx must be passed down to the blocking call (`http.NewRequestWithContext`),
      `defer cancel()` always, and a child deadline can only shorten the parent's.
- [ ] **Two budgets**: per-vendor timeout starting when *that call* starts, inside an overall deadline from the
      caller, and why (one slow vendor must not eat the whole budget; queued vendors must still get a fair try).
- [ ] **Bounded concurrency** with a reason grounded in resources or contracts (vendor connection/rate limits,
      Little's law: in-flight = rate × latency), and how it's implemented (semaphore / `SetLimit`).
- [ ] **Leaks**: the unbuffered-result-channel-plus-early-return leak (or an equivalent abandoned-sender leak),
      and the fix (buffer of 1 / select on ctx); that blocked goroutines are never garbage-collected.
- [ ] **Uncooperative dependencies**: how to stop *waiting* for a vendor that ignores ctx, and that this bounds
      your latency but not the vendor's resource use.
- [ ] **Evidence**: `go test -race` with real concurrent callers, what it cannot find (unexecuted paths, logical
      races, leaks, deadlocks), and a goroutine-count leak check with a settle loop.

Should-hit (at least 4 of 7):
- [ ] Explicit partial-result policy (best-effort vs fail-fast) with an example of when each is right.
- [ ] `select` picks randomly among ready cases, so re-check ctx after acquiring a slot.
- [ ] Contain panics at the adapter boundary (recover → error) so one bad adapter can't kill the process.
- [ ] Tail amplification number (1 − 0.99ᴺ) or another quantified latency argument.
- [ ] Deterministic testing techniques: injected clock, event channels instead of sleeps, timeouts on every wait.
- [ ] Ops: goroutine-count gauge, `/debug/pprof/goroutine?debug=1`, per-vendor abandoned-call/timeout metrics.
- [ ] Graceful shutdown ordering (drain, `Shutdown` with a budget, then close background work) as it applies to an
      adapter with in-flight vendor calls.

Red flags (any one = revise):
- [ ] Claims goroutines are garbage-collected when unreachable, or that ctx cancellation stops a goroutine
      "automatically".
- [ ] Recommends closing a channel from the receiving side, or closing a channel "just in case" from several places.
- [ ] Says a clean `-race` run proves the code is race-free.
- [ ] Recommends unbounded fan-out ("goroutines are cheap") for calls to rate-limited vendors with no caveat.
- [ ] Uses `time.Sleep` as a synchronisation mechanism in code or tests.
- [ ] Shares a mutable map/slice across goroutines without saying how it is synchronised.

## Recording

`sd record teachback B0 --result <pass|revise> --notes "<missed items>"`
