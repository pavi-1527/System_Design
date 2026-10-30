# E3 Design exercise — The design rationale for your job pipeline

> Time box: two sittings. **Draft** ≈ 90 minutes, solo, *before* you write broker or worker code (sections 1–7 and
> the "predicted" column of section 8). **Revision** ≈ 60 minutes after `sd lab E3` is green (the "observed" column,
> every mismatch explained, section 9). Self-score both against the rubric, then send the final version, with the
> draft attached, to the tutor for the probe session.

A Track E design exercise differs from Track A/C ones in one way: you will *find out* whether you were right. The
rubric rewards a draft that commits to specific, testable predictions and a revision that is honest about where they
failed, more than a draft that happened to be right.

## Prompt

> "The platform sends ≈ 1.2 M member alerts a day (transfer posted, low balance, fraud alert, OTP fallback,
> statement ready) through one SMS/push vendor contracted at 100 messages/s. Producers are the transfers, cards and
> statements services. Today they call the vendor inline. Design the asynchronous pipeline you are about to build in
> `labs/e3/jobq`, as you would for a design review with the team that will run it: why each component exists, what
> each guarantee rests on, what happens in each failure, and how you'll know you were right."

Assume what you need beyond [spec.md](spec.md), and write the assumption down.

## What to produce

A Markdown document (≈ 1,500–2,500 words plus one diagram and the table), outside `modules/` (for example
`_notes/e3-rationale.md`), with these sections:

1. **Context and requirements**: lanes or alert categories you'd treat differently (at least: fraud/OTP vs
   marketing-like notices), the guarantee per category (lost vs duplicated: which is worse?), and four numbers:
   peak enqueue rate, vendor-bound drain rate, a sane `-max-depth` per queue, and log growth per day. Arithmetic shown.
2. **Architecture**: the diagram (producer, broker, workers, vendor, operator), one line per box on why it exists,
   and the single sentence that defines what a 201 from the broker promises.
3. **Durability**: the log format, which writes you fsync and which you don't (with the reason), torn-tail and
   corruption handling, what SIGKILL proves and what it can't, and your RPO for power loss and for disk loss.
4. **Leases and the worker pool**: visibility timeout and call timeout values with the reasoning between them, the
   lease token, what survives a broker restart, concurrency per worker from Little's law and the vendor contract,
   and the lease loop's behaviour when the broker is down or silent.
5. **Retry policy**: your classification table, backoff base/cap/jitter with the schedule it produces for a 10-minute
   vendor outage, Retry-After, who counts attempts, and what you'd add for a vendor-wide failure (breaker or budget).
6. **Idempotency**: the producer key (who picks it, scope, window, why the window is long enough), the consumer key,
   the exact sequence for "worker dies between effect and ack", and the honest statement of what "exactly once"
   means in your system and where it stops being true.
7. **Dead letters, backpressure, drain**: the three roads into the DLQ, what is kept, the replay procedure; the depth
   bound and the 429 contract, including duplicates; the drain sequence and the three timeouts it depends on.
8. **Failure table**: F1–F20 from [spec.md §8](spec.md#8-failure-injection-scenarios), columns *predicted* (written
   in the draft, never edited afterwards), *observed*, *match?*, *why not* (the model that was wrong).
9. **Known gaps and evolution** (revision only): claims no test proves, mutations the suite doesn't catch
   ([Unit 3 §4](unit-3-dead-letters-backpressure.md#4-the-failure-injection-run-predicted-vs-observed)), and what you'd
   change at 10× (12 M alerts/day) and at 100×.

## Rubric

| Dimension | below | approaching | meets (senior bar) | exceeds |
|---|---|---|---|---|
| Requirements & numbers | No categories or numbers | Categories without a per-category guarantee; numbers without arithmetic or decisions | Categories with "lost vs duplicated" per category; peak ≈ 3–5× the ≈ 14/s average, drain ≤ 100/s, depth from drain × useful life (e.g. 100/s × 600 s = 60k), log growth with units; each number tied to a decision | Spots that the vendor contract, not the pipeline, sets the backlog drain time and plans lanes and reserved vendor share around it |
| Durability | "It's on disk" | Log + fsync, but fsyncs everything or nothing without reasoning; torn tail not handled | Frame format with length + CRC, truncate-on-recovery explained, fsync on enqueue (and replay) only, with the redelivery argument for not syncing acks; SIGKILL vs power loss distinguished; RPO stated for both power and disk loss | Group commit with a throughput estimate; recovery time as an availability number; compaction plan |
| Leases & workers | Remove-on-receive or no timeouts | Leases without attempt-scoped tokens, or call timeout ≥ visibility | Visibility > call timeout with a reason, tokens per attempt (409 for stale), leases and attempts survive restarts, concurrency from Little's law and the contract, lease client timeout > long-poll wait, back off and never exit on broker errors | Discusses heartbeat extension vs fixed visibility with a flip condition; prefetch trade-off |
| Retries | Retry everything / never | Backoff without jitter or Retry-After; 4xx/5xx split only | Classification table including 408/429 and transport errors, full jitter with the computed schedule, Retry-After as a floor, broker-side attempt counting, and a vendor-wide answer (breaker or budget) so outages don't burn attempts | Quantifies retry amplification during a 50% error rate and sizes a budget |
| Idempotency | "Exactly-once delivery" claimed | Keys mentioned, but per attempt, or producer-side only | Producer key from the business fact, atomic check+insert, window ≥ producer retry horizon, dedupe before backpressure; consumer key = job id on every attempt and replay; the effect-then-crash sequence drawn; exactly-once stated as an end-to-end property bounded by the vendor's key retention | Covers vendors without key support (sent-log + reconciliation) and replays outside the window |
| DLQ, backpressure, drain | Failures deleted; unbounded queue; drain = exit | DLQ without history or replay; bound excludes delayed jobs; drain cancels calls | Three roads into the DLQ with history, explicit replay (same id, back of the line, bounded); per-queue depth bound over ready + delayed + leased with 429 + Retry-After; drain: stop leasing, finish calls on their own deadlines, ack, bounded by a drain timeout below the grace period, non-zero exit if abandoned | Replay rate plan for a large DLQ; per-lane bounds and priorities |
| Predictions & honesty | No predictions, or predictions edited after the fact | Predictions vague ("it recovers") or mismatches unexplained | Every F1–F20 predicted with observable quantities (codes, effects vs duplicates, counters, time bounds), every mismatch explained by naming the wrong model, known gaps listed | Adds a test or measurement of their own for a gap they found, with the result |

## Tutor critique protocol

1. Check that the draft predates the build (the learner sends the draft as written; its predicted column must not
   have changed in the final version). If the draft is missing, the "Predictions & honesty" dimension is at most
   approaching.
2. Self-score first: a level and one line of evidence per dimension.
3. Ask at least 4 of these probes, one at a time, and score the answers with the document:
   - "A 201 just went out. List every failure after which that job is never delivered, and the one you've accepted."
   - "Why don't you fsync acks? Walk me through a power cut right after an unsynced ack."
   - "The vendor takes 40 s on a bad day; your visibility is 30 s. What happens, step by step, and what would you
     change?"
   - "Your worker is SIGSTOPped (not killed) for 2 minutes while holding a lease, then resumes. What does it do, what
     does the broker say, and what does the vendor see?"
   - "The vendor returns 503 for 15 minutes. Show me the attempt timeline of one job and the vendor's load."
   - "Prove to me that a member can't get two texts for one transfer. Where exactly does your argument stop being
     true?"
   - "A template bug dead-letters 18,000 jobs. Walk me through the replay, including today's fraud alerts."
   - "Which of your failure-table predictions was wrong, and what did you believe before that you don't now?"
   - "Which claim in this document does no test prove?"
4. Run the learner's lab once yourself (`sd lab E3`) and spot-check one mismatch they reported by asking them to
   reproduce it.
5. Overall rule: **meets** = meets or better on every dimension and none at below. **exceeds** = meets everywhere and
   exceeds on at least three dimensions, including "Predictions & honesty".
6. Below meets: name 2–3 concrete revisions (for example "derive the depth bound from useful life, not memory";
   "explain F10's 2-attempts/1-effect result in terms of the lease, not the worker"), and re-assess in a later
   session.

## Recording

`sd record design E3 --self <below|approaching|meets|exceeds> --tutor <level> --notes "<one-line summary>"`
