# B3 Design exercise — timed machine-coding round on an unseen problem

> Time box: 90 minutes of coding (stages announced at minutes 0, 30 and 55) + 10 minutes for the walk-through note,
> solo, no notes, no AI help. `go doc` is allowed, as it would be in a real round. Then self-score, then send the
> code, the test output and the note to the tutor.

This is the gated design exercise for B3. It is a dress rehearsal for the D2 machine-coding mock, on a problem you
have **not** practised: the tutor picks one variant from the bank below (or an equivalent unseen problem) without
telling you which until the timer starts. The module covers problems 1–18; the bank is intentionally built from the same
skills (caches, limiters, state, messaging, scheduling, money, contention, trees, sinks, state machines, matching,
reservations, resilience, pluggable interfaces) so that no variant needs a trick you haven't met.

## Prompt

*As an interviewer would say it.* The tutor reads out exactly one of these (variants D–F lean on the skills of
problems 10–18).

**Variant A — Meeting-room booking**
"We run an office with about 40 meeting rooms across 3 floors. Build the in-memory core of a booking service: people
book a room for a time window, and nobody should ever end up double-booked. It runs inside a multi-threaded API
server. I'll add requirements as we go."

**Variant B — In-process transfer processor**
"Build the in-memory core of a funds-transfer processor for a credit union: accounts with balances, and transfers
between them. Clients retry on timeouts, and many transfers run at once. Money must never be created or destroyed.
I'll add requirements as we go."

**Variant C — Price-alert notifier**
"Users set alerts such as 'tell me when ACME goes above 100'. A feed delivers price ticks all day. Build the
in-memory core that decides when to notify, and hands notifications to a sender. Some senders are slow. I'll add
requirements as we go."

**Variant D — Notification dispatcher with provider failover**
"We send member notifications through several providers: email, SMS and push stand-ins, each behind an interface.
Build the in-memory core of a dispatcher that sends a message through the best available provider and keeps working
when a provider is down or slow. Callers retry on timeouts. I'll add requirements as we go."

**Variant E — Limited-redemption coupon service**
"Marketing runs campaigns such as 'first 1,000 members get a bonus' with a per-member cap. Build the in-memory core of
the redemption service. It's called from many request handlers at once and must never over-redeem. I'll add
requirements as we go."

**Variant F — Versioned configuration tree with watchers**
"Services read hierarchical configuration such as `/svc/payments/timeout`. Build the in-memory core of a config
store with typed values and change notifications. Many readers, a few writers. I'll add requirements as we go."

Clarifying questions worth asking (or stating as assumptions in your note): scope and non-goals; time and money
types; what "now" is (injected clock); concurrency expectations (how many goroutines, what must be atomic); what
errors callers need to distinguish; what is persistent (nothing: in-memory only); what the tests may assume.

## Stages (the interviewer announces each at the stated minute)

| | Variant A: room booking | Variant B: transfers | Variant C: price alerts |
|---|---|---|---|
| **Stage 1** (0–30) | Rooms with capacity; `Book(room, user, start, end)`; reject overlapping windows (half-open intervals `[start, end)`); `Cancel(id)`; `Bookings(room, day)` sorted; injected clock; safe for concurrent use | Accounts in integer minor units; `Transfer(from, to, amount)`; reject overflow, same-account, negative, insufficient funds; `Balance`; the sum of all balances never changes | `SetAlert(user, symbol, op, threshold)` (`Above`/`Below`); `Tick(symbol, price)` returns alerts that fire; an alert fires once then is disarmed; `RemoveAlert`; safe for concurrent use |
| **Stage 2** (30–55) | Recurring bookings (weekly, N occurrences) that are all-or-nothing; a policy interface for rules (max 4 hours, no bookings in the past, business hours) added without editing `Book`; `Available(window)` listing free rooms | Idempotency keys: a retried `Transfer(key, …)` returns the original result and never moves money twice; the same key with different parameters is an error; an audit log entry per attempt | Notifications go to a `Sender` interface through a bounded queue and a worker pool, so `Tick` never blocks on a slow sender; a per-user rate limit (at most N notifications per window) using the injected clock; re-arming alerts (`Repeat` with a cool-down) |
| **Stage 3** (55–90) | Contention: 200 goroutines book overlapping windows on the same rooms — exactly one wins each conflict, no deadlock for multi-room bookings (book room-set atomically); per-room lock vs one global lock, with a defended choice and a stress test | Many concurrent transfers between overlapping accounts with no deadlock (lock ordering or a single-writer design, defended); a `Snapshot()` that always sums to the same total; a transfer that fails midway leaves both balances untouched | Graceful `Close(ctx)`: stop accepting ticks, drain the queue within the deadline, report what was dropped; slow or panicking `Sender` contained; no goroutine leaks; delivery is at-least-once with a dedupe key the sender can use |

| | Variant D: notification dispatcher | Variant E: coupon redemption | Variant F: config tree |
|---|---|---|---|
| **Stage 1** (0–30) | Ordered providers behind a `Provider` interface; `Send(msg)` uses the first healthy provider; errors typed retryable vs permanent; injected clock | Campaigns with a global cap and a per-member cap; `Redeem(campaign, member)` with exact errors; the sum of redemptions never exceeds the cap | `Set`, `Get`, `List(prefix)` over `/`-separated keys with typed values; path semantics (cleaned, absolute); safe for concurrent use |
| **Stage 2** (30–55) | Per-provider circuit breaker and jittered retry (injected clock and sleeper); an idempotency key so a duplicate request sends once | Two-phase `Hold` → `Confirm` / `Cancel` with expiry (injected clock); idempotent redemption keys; a held-but-expired redemption frees its slot | `Delete` / `Move` of subtrees; version numbers and compare-and-set; `Move` is atomic to readers |
| **Stage 3** (55–90) | Many concurrent callers, a global in-flight cap, graceful `Close(ctx)` that drains, exactly-once accounting under `-race` | 500 concurrent members racing for 10 uses: exactly 10 win; a FIFO waitlist for released holds; a conservation invariant | `Watch(prefix)` with a bounded buffer and an explicit slow-subscriber policy; atomic multi-key `Txn`; a consistent `Snapshot` |

Skills for D–F: `resilient`, state and bounded concurrency (D); `inventory` and idempotency (E); `memfs`, `logx` sinks
and snapshots (F).

Stage 4 (discuss only, last 5 minutes of the walk-through): the tutor asks how it would change for persistence and for
multiple instances.

## What to produce

1. **Code** in `labs/b3/_design/<variant>/`. Directories starting with `_` are ignored by `./...`, so `sd lab B3` does
   not pick it up. Include a `README.md`-style package doc comment that states the assumptions you made.
2. **Tests** written as you go (not at the end): at least one boundary test per stage, one concurrent test per
   concurrency claim (run them with `-race -count=20` and keep the output), an injected clock (no sleeps), a timeout
   helper so a bug fails instead of hanging, and one test that fails if you remove a lock or an ordering rule.
3. **Concurrency and design note** (≤ 300 words, a doc comment or text file), under these headings: *Model* (entities,
   what changes and where the seams are), *Shared state and owners* (what is shared, who may touch it, under which
   lock or channel), *Invariants* (stated as sentences, each with the test that checks it), *Failure handling*,
   *What I did not do and what I would do next*.
4. **A timeline** of what was green at minutes 30, 55 and 90 (copy-pasted test output is fine).

## Rubric

Dimensions follow the machine-coding rubric in `TUTOR.md` §8 so that your D2 mocks are scored the same way.

| Dimension | below | approaching | meets (senior bar) | exceeds |
|---|---|---|---|---|
| Requirements & modelling | Starts typing without clarifying; entities and responsibilities blurred (one god struct); money or time as `float64` / raw `time.Now()` | Clarifies some scope, but the model needs reshuffling when stage 2 arrives; types leak internals | Clarifies and states assumptions; entities and invariants named before coding; money/time modelled correctly; non-goals stated | Identifies *what will change* up front and puts a seam exactly there |
| Interfaces & extensibility | Stage 2 or 3 requires rewriting stage 1; large interfaces; concrete types everywhere | Stage 2 lands with edits in several places; some interfaces are producer-side and bloated | Small consumer-side interfaces (clock, policy, sender); stages land as additions; errors are values callers can match on | Explains the alternatives rejected and the cost of each; API shaped so misuse is hard |
| Correctness & concurrency | Races under `-race`, wrong results under contention, deadlock | Correct on the happy path; concurrency story hand-wavy; one invariant unproven | `-race -count=20` clean; invariants hold under a stress test; lock strategy chosen and defended; no user code under a lock | Names the happens-before edges, shows a failing test for each removed safeguard, and discusses the next scaling step with numbers |
| Tests | No tests, or tests that sleep and hope | Happy-path only; concurrency untested | Boundary, concurrency and invariant tests; injected clock; deadlock/hang guards; written as it went | Property or model-based test; mutation of own code shown to fail a test |
| Code quality | Non-idiomatic, panics for errors, dead code, unclear names | Mostly idiomatic; some premature abstraction or leftover hacks | Idiomatic Go; errors handled and wrapped; no premature abstraction; comments say *why* | Reads like production code; the doc comments could be turned into the API contract |
| Time management | Nothing working by minute 30 | Stage 1 late; stage 3 untouched; no note | Stage 1 by 30, stage 2 by 55, honest partial stage 3, note delivered | Finishes with time to run the stress tests repeatedly and discuss stage 4 |

## Tutor critique protocol

1. **Pick the variant** the learner has not seen (check `mocks` and `designs` in `progress.json`); don't reveal the
   other variants. Keep time with `date`; announce the stages at minutes 30 and 55.
2. **Run before reading.** `gofmt -l`, `go vet`, and `go test -race -count=20` on the learner's package. Then mutate
   one thing (remove a lock, or reverse a lock-acquisition order, or run a callback under a lock) and check whether
   a test fails. Record whether it did.
3. **Self-score first**: ask for a level and a one-line justification per dimension before you say anything
   evaluative.
4. **Read the note, then the code.** Check each claim in the note against the code: owner of each piece of shared
   state, which test checks each invariant.
5. **Ask at least four probes**, one at a time, as an interviewer would, answered from memory in ≤ 2 minutes:
   - "What does your code do if two goroutines race for the same room / account / alert? Show me the test."
   - "Walk me through the lock order. What happens if I request the resources in the opposite order?"
   - "What is the first thing that breaks at 10× the load?" (and "how would you know?")
   - "A retry arrives while the first attempt is still in flight. What happens?"
   - "What would change if the clock jumped backwards?"
   - "Which part would you shard first, and what does sharding cost you?"
   - "You said 'thread-safe'. Which invariants, and which test proves each?"
   - "What happens if a callback, subscriber or downstream provider is slow, or panics?" (variants D and F)
   - "How do you know your tests are good? Show me a bug they would catch."
6. **Score each dimension** with evidence (a line, a test, an answer). **Overall meets** requires *meets* or better on
   *Correctness & concurrency* and *Interfaces & extensibility*, and no dimension at *below*. One *approaching*
   elsewhere is acceptable if the learner fixes it when probed (note that in the record).
7. **Calibration**: if the self-score is off by more than one level on any dimension, say so.
8. **Feedback**: one thing to keep, the single most important fix, and one problem from the bank to redo. If the
   overall result is below *meets*, the learner redoes the exercise on a different day on another variant.

## Recording

`sd record design B3 --self <below|approaching|meets|exceeds> --tutor <level> --notes "<one-line summary>"`
