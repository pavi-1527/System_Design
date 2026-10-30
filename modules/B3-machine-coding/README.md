# B3 — Machine-coding problem bank (timed)

| | |
|---|---|
| Track | B — LLD / machine coding in Go |
| Estimated time | 53.5 h (8 weekly units) — CORE 46 h, STRETCH 7.5 h |
| Prerequisites | B0, B2 (gates passed) |
| Labs | `labs/b3/` — `lrucache`, `ratelimit`, `parkinglot`, `kvtx`, `pubsub`, `scheduler`, `splitwise`, `elevator`, `ticketing`, `memfs`, `logx`, `vending`, `orderbook`, `inventory`, `resilient`, `shortener` (core); `leaderboard`, `hashcache` (stretch) |
| Gate | labs pass · design ≥ meets (no quiz or teach-back; `sd quiz B3` is an optional practice deck that feeds `sd review`) |

> This module covers all 18 problems of [SYLLABUS.md](../../SYLLABUS.md#b3--machine-coding-problem-bank): units 1–4 are
> problems 1–9, units 5–8 are problems 10–18 (file system, async logger, vending machine, order book, inventory
> reservations, resilient HTTP client, URL shortener, the two stretch problems and the round playbook). Hours above
> are the sum of the unit hours in `module.json`; the SYLLABUS figure (48 h) was the planning estimate.

## Why this module exists

A candidate sits a 90-minute machine-coding round: "build an in-memory cache with a TTL and make it thread-safe".
At minute 70 there is clean, single-threaded code, no tests, and a `map` guarded by a `sync.RWMutex` whose `Get`
moves an entry in a linked list. The interviewer says "good, now add expiry". The expiry check lands in three
methods, there is no clock to inject, so the new behaviour can only be tested by sleeping, and `go test -race`, run
for the first time at minute 80, prints nine data races. Nothing the candidate wrote is exotic. Each failure is a
decision made too late: state ownership, what time is, what changes next, what the tests will be.

Machine-coding rounds reward a specific, trainable skill: turn an underspecified problem into a staged, tested,
race-free design under a clock, and narrate the choices while doing it. It is also the code you write every day
(a retrying vendor client, a de-duplicating consumer, a seat hold that must not oversell), only with a timer
running and someone reading over your shoulder. You will practise it on eighteen problems, each in the same loop: a
spec with a time box, requirements that grow in three stages as they do in a real round, tests you must make pass
under `-race`, a rubric, and a debrief that shows what the senior-level choices were and why.

## Outcomes

- **Run** a timed machine-coding attempt: clarify and pin down requirements, state invariants out loud, hit stage
  checkpoints, and finish with a concurrency walk-through. CORE
- **Choose** data structures that give the required complexity (map + intrusive list, heaps with back-indices, undo
  logs, tick-driven state machines) and show the scale test that proves it. CORE
- **Make** a component safe for concurrent use under `go test -race`: state ownership, lock granularity and lock
  ordering, no user code under a lock, and invariants that a stress test can check. CORE
- **Inject** time (clocks, timers, ticks) so every stage is testable without sleeping, including exact boundaries. CORE
- **Land** mid-interview requirement changes as additions (options, strategy interfaces, new methods) rather than
  rewrites of stage-1 code. CORE
- **Design** for lifecycle: bounded queues and memory, backpressure policies, graceful shutdown, panic containment and
  goroutine-leak freedom. CORE
- **Model** money, retries and duplicates correctly: integer minor units, deterministic rounding, idempotency keys,
  at-least-once delivery with dedupe. CORE
- **Score** your own code and a peer's against the machine-coding rubric (extensibility, concurrency safety, clean
  interfaces, tests, time management) and name the change that moves each dimension up a level. CORE
- **Choose and defend a data model** before coding (tree vs flat map, state pattern vs enum, sorted levels vs heap),
  and implement atomic tree operations (rename, remove, walk) with correct path handling. CORE
- **Compose decorators and sinks** with an explicit backpressure policy (block, drop newest, drop oldest), failure
  isolation and a graceful, bounded shutdown (async batching logger). CORE
- **Implement the State pattern** with an explicit transition table, reject invalid events, make change correctly with
  finite coin stock, and extend payment methods without editing existing states. CORE
- **Implement a price-time-priority matching engine** with integer money, executable invariants, deterministic
  replay and a differential test against a reference model. CORE
- **Design reservations**: reserve → commit/release with lazy expiry, all-or-nothing multi-item holds without
  deadlock, idempotency keys and a fair FIFO waitlist. CORE
- **Build a resilient HTTP client**: retry only what is safe, jittered backoff, `Retry-After`, retry budgets,
  circuit breaker, bulkhead, per-attempt timeouts inside an overall deadline. CORE
- **Design pluggable components** with small consumer-side interfaces and shared contract tests (URL shortener with
  pluggable ID generation and stores). CORE
- **Run the whole 90-minute round**: time budget with checkpoints, a deliberate concurrency pass, a spoken five-part
  concurrency note, and descoping out loud. CORE
- **Choose an order-statistic structure** (treap/skip list) for leaderboard top-k, rank and neighbours, and shard
  and merge top-k. STRETCH
- **Implement consistent hashing** with virtual nodes, replication, bounded loads and lazy migration on membership
  change. STRETCH

## How a problem works

Every problem section in the unit files follows the same shape. It deviates from the concept units of other modules
in one way: the *problem* and your *timed attempt* come before the reference design, because reading the design
first destroys the exercise.

1. **The failure story** and **Predict**. Write down answers to the design traps before looking at the reveal.
2. **The problem**, stated as an interviewer would, with a checkpoint table and three stages of requirements.
3. **Build it.** Start a timer, work in `labs/b3/<pkg>/`, and stop at the time box. `go test -race` is the
   scoreboard; the stage files (`stage2.go`, `stage3.go`) are opened only when the interviewer "announces" them.
4. **Then** read **How it actually works**, **Rubric**, **Common mistakes** and the **Debrief**; score yourself;
   refactor; re-attempt if you are below *meets*.
5. **Break it**, **In the wild**, **Trade-offs**, **Senior bar** (with follow-up probes) and **Check yourself**.

The tutor reviews your timed attempts against the rubric in each section. The five rubric dimensions
(extensibility, concurrency safety, clean interfaces, tests, time management) are the same every time, with
problem-specific descriptors.

## How a timed attempt works

1. **Set up.** Work tree clean; open only the problem's Stage 1 file and test file. The `stage2.go` / `stage3.go`
   stubs and the hint blocks stay closed until you reach them.
2. **Start the clock** (use the time box in the problem header). Minutes 0–10: questions, assumptions, API and state
   sketch, the concurrency decision. Write them down.
3. **Work in stages.** Run the stage's tests (`go test -race -run 'Stage1' ./labs/b3/<pkg>/`) as you go. When Stage 1
   is green or the stage's minute mark arrives, open the next hint block and stub file, like an interviewer adding a
   requirement.
4. **Stop at the time box** even if tests are red. Note which stage you reached and where the minutes went.
5. **Then** run `go test -race -count=20 ./labs/b3/<pkg>/`, read the reference design, score yourself on the
   problem's rubric (one line of evidence per row), and answer the debrief's narration out loud once.
6. **Refactor and finish**: untimed, make everything green with the debrief in hand; do the Break-it exercises.
7. **Send the tutor** the code, the test output and your self-score. Below *meets* on a problem means: refactor with
   the debrief, then redo it (or a variant the tutor sets) on a different day.
8. **Spaced re-attempt.** Two weeks after a problem passes, redo Stage 1 and Stage 2 from scratch, timed. The point
   is retrieval, not recall of the solution.

`sd lab B3` runs every core lab package under `-race` and records the result for the gate.

## Unit plan

| Unit | Topic | Hours | Core/Stretch | Lab |
|---|---|---|---|---|
| 1 | [Caches and limiters](unit-1-caches-and-limiters.md): LRU cache with TTL, rate limiter library | 6 | CORE (5) / STRETCH (1) | `labs/b3/lrucache`, `labs/b3/ratelimit` |
| 2 | [Modelling and state](unit-2-modelling-and-state.md): parking lot, in-memory KV with nested transactions | 6 | CORE (5.5) / STRETCH (0.5) | `labs/b3/parkinglot`, `labs/b3/kvtx` |
| 3 | [Messaging and scheduling](unit-3-messaging-and-scheduling.md): pub-sub with acks and redelivery, job scheduler | 7.5 | CORE (6.5) / STRETCH (1) | `labs/b3/pubsub`, `labs/b3/scheduler` |
| 4 | [Algorithms, state machines and contention](unit-4-algorithms-and-contention.md): Splitwise, elevator, ticket booking | 9 | CORE (8) / STRETCH (1) | `labs/b3/splitwise`, `labs/b3/elevator`, `labs/b3/ticketing` |
| 5 | [Trees and sinks](unit-5-trees-and-sinks.md): in-memory file system, async logging | 6 | CORE (6) | `labs/b3/memfs`, `labs/b3/logx` |
| 6 | [State and matching](unit-6-state-and-matching.md): vending machine, order book | 6 | CORE (6) | `labs/b3/vending`, `labs/b3/orderbook` |
| 7 | [Reservations and resilience](unit-7-reservations-and-resilience.md): inventory, HTTP client, URL shortener | 6 | CORE (6) | `labs/b3/inventory`, `labs/b3/resilient`, `labs/b3/shortener` |
| 8 | [Stretch problems and the round playbook](unit-8-stretch-and-capstone.md): leaderboard, consistent-hashing client, the 90-minute round | 7 | CORE (3) / STRETCH (4) | `labs/b3/leaderboard`, `labs/b3/hashcache` (stretch) |

Per-problem time: the box (60–90 min) is only the timed attempt. Budget ≈ 15 min to predict and plan before it,
and ≈ 60–90 min after it for the debrief, refactor and Break it. STRETCH time is the "Stage 4: discuss" prompts and
extra Break it mutations.

**Unit 1** (CORE 5 h + STRETCH 1 h)
- Session 1 (2.5 h): §1 LRU cache with TTL: predict, timed attempt (60 min), debrief.
- Session 2 (2.5 h): §2 rate limiter: predict, timed attempt (60 min), debrief.
- STRETCH (1 h): Stage 4 discussions for both problems; extra Break it mutations.

**Unit 2** (CORE 5.5 h + STRETCH 0.5 h)
- Session 1 (2.75 h): §1 parking lot (75 min attempt) and its debrief.
- Session 2 (2.75 h): §2 KV store with nested transactions (75 min attempt) and its debrief.
- STRETCH (0.5 h): Stage 4 discussions.

**Unit 3** (CORE 6.5 h + STRETCH 1 h)
- Session 1 (3.25 h): §1 pub-sub (90 min attempt), then the debrief.
- Session 2 (3.25 h): §2 job scheduler (90 min attempt), then the debrief.
- STRETCH (1 h): Stage 4 discussions; redo one attempt with a different slow-consumer or overlap policy.

**Unit 4** (CORE 8 h + STRETCH 1 h)
- Session 1 (2.5 h): §1 Splitwise (75 min attempt) and its debrief.
- Session 2 (2.75 h): §2 elevator controller (90 min attempt) and its debrief.
- Session 3 (2.75 h): §3 ticket booking (90 min attempt), its debrief and the design exercise draft.
- STRETCH (1 h): Stage 4 discussions.

**Unit 5** (CORE 6 h)
- Session 1 (2 h): §1 failure story, problem and Predict blocks; **timed attempt** of `memfs` (75 min).
- Session 2 (2 h): read the reference design, score yourself on the rubric, Break it; refactor; §2 failure story,
  problem and Predicts.
- Session 3 (2 h): **timed attempt** of `logx` (60 min); debrief, Break it, Check yourself, `sd review`.

**Unit 6** (CORE 6 h)
- Session 1 (2 h): §1 `vending` timed attempt (60 min) and self-score; the state-pattern debrief.
- Session 2 (2.5 h): §2 `orderbook` timed attempt (90 min), stage by stage.
- Session 3 (1.5 h): order book debrief, differential test, Break it; re-attempt any problem scored below *meets*.

**Unit 7** (CORE 6 h)
- Session 1 (2 h): §1 `inventory` timed attempt (75 min), debrief.
- Session 2 (2 h): §2 `resilient` timed attempt (60 min), debrief, Break it.
- Session 3 (2 h): §3 `shortener` timed attempt (60 min), contract-test debrief, Check yourself, `sd review`.

**Unit 8** (CORE 3 h + STRETCH 4 h)
- STRETCH (2 × 2 h): §1 `leaderboard` (60 min attempt) and §2 `hashcache` (75 min attempt), each with its debrief.
- CORE (1 h + 90 min + debrief): §3 the playbook, then the gated 90-minute **design exercise** with the tutor.

After unit 8 §3 (or earlier, once the core problems score *meets*), schedule the gated [design exercise](design-exercise.md)
(90 min + 10 min note) on a day you have not just practised, and ask the tutor to pick the variant.

## How this connects to your banking work

- **Retries and duplicates**: the pub-sub redelivery (U3 §1) and ticket-booking idempotent `Confirm` (U4 §3) are your
  duplicate vendor callbacks and idempotent money movement in miniature: at-least-once delivery plus a dedupe key, and
  "same key, different payload" as an error.
- **Vendor and member throttles**: the rate limiter (U1 §2) is the per-credit-union and per-vendor outbound limit; the
  scheduler (U3 §2) is month-end statement generation and token refresh, with the same cancellation and shutdown
  questions your deploys raise.
- **Money**: Splitwise (U4 §1) is allocation arithmetic: integer minor units, deterministic remainders and
  "balances sum to zero", which is the double-entry invariant of a ledger.
- **State machines and audit**: the elevator (U4 §2) and the KV transactions (U2 §2) are the same discipline as loan or
  ACH state transitions that must be explicit, auditable and undoable as a unit.
- **Multi-tenant caches**: the LRU cache (U1 §1) is your SSO profile and vendor-config cache; a per-tenant limiter or
  capacity split is the noisy-neighbour question.
- **`resilient`** is your vendor adapter life: image, KYC and SSO providers with 30-second timeouts, duplicate
  callbacks and retries that double-charge when the operation was not idempotent. The retry rules (safe methods or an
  idempotency key), the budget and the breaker are the answers to incidents you have lived.
- **`inventory`** is the reserve/commit/release pattern behind card authorisation and capture, ledger holds and
  limited-quantity promotions; idempotency keys are the same ones you use for money movement.
- **`logx`** forces the distinction every audit trail needs: a debug log may drop entries under pressure, an audit
  event may not (that is an outbox, not an async logger). Per-tenant fields via `With` mirror the credit-union tenant
  ID on every line.
- **`orderbook`** is an append-only sequence of validated state transitions with integer money and executable
  invariants: the discipline of a ledger, in a smaller box.
- **`memfs`** and **`vending`** are the staged-statement packaging area and the card-terminal state machine in
  disguise: a tree with atomic moves, and a state machine that must handle a duplicate callback in the wrong state
  without double-capturing.

## Mastery gate checklist
- [ ] `sd lab B3` — all lab tests pass (with `-race`)
- [ ] Design exercise (`design-exercise.md`): self-score recorded, tutor score ≥ "meets"
      (`sd record design B3 --self <lvl> --tutor <lvl> --notes "..."`)

`sd status` is the source of truth; this list is for your own reading. B3's gate has no quiz or teach-back
component, so the question bank in `quiz.json` is practice only: `sd quiz B3` runs a draw from it, and `sd review`
brings the questions you missed back on a schedule.

## What's next

B3 unlocks D2 (the 90-minute machine-coding mock) once the whole gate passes. Before moving on, run `sd review` for the
B3 questions you missed (weak tags are typically `concurrency`, `testing`, `idempotency` and `time-management`), and
redo one timed attempt from each unit without the stage hints, scoring yourself against the rubric first. Then redo your
two weakest problems under the full 90-minute round clock with a problem you have not seen.
