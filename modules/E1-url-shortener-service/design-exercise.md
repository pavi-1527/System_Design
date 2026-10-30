# E1 Design exercise — The design-rationale write-up for `shorty`

> Time box: version 1, 60 minutes, solo, before writing any lab code (Unit 1, session 1). Version 2, 45 minutes, after
> `TestM4` is green (Unit 3 §2). Notes allowed for v2 (it's about your own build); not for v1. Then self-score, then
> send both versions to the tutor.

Track E's design exercise is not an interview answer: it's the document a senior engineer writes before building a
service and updates after breaking it. It is graded on the reasoning and on how honestly v2 confronts v1's predictions —
a wrong prediction you explain is worth more than a right one you can't.

## Prompt

Your platform team will run the URL shortener from [spec.md](spec.md) for the statement, verification and marketing
e-mails of 40 credit unions: ≈ 120 creates/s and ≈ 12,000 redirects/s at peak (C1's estimate), a takedown requirement
from compliance ("a reported phishing link must stop redirecting within 60 seconds"), and senders that retry on
timeouts. It will run as one store process and several API processes, exactly as in the spec.

Write the design rationale for it: what each component does, why it is built that way, what it costs, how it fails,
and how you'll know.

## What to produce

**Version 1 (before code), ≈ 2–4 pages:**

1. **Context and goals**: the requirements that drive the design, with numbers; explicit non-goals.
2. **Component decisions**, one short section each — store (log format, fsync policy, recovery rules, insert-if-absent),
   idempotent create, redirect cache (what is cached, TTLs, negative caching, coalescing, stale-if-error), invalidation,
   rate limiter, deadlines. For each: the decision, the alternative you rejected, the trade-off sentence
   (claim → because → cost → when it flips, from `docs/INTERVIEW_FRAMEWORK.md`).
3. **Numbers**: production cache size for the hot set (show the arithmetic), store write throughput under
   `-sync always` (with an fsync time you state as an assumption), the takedown window your TTLs imply, the effective
   rate limit across N instances, goroutines in flight during a store outage (Little's law).
4. **Failure predictions**: for each of F1–F12 in spec §6, what *your* design does — status, time, what keeps working,
   what's lost. Fill the "predicted" column of the table below.
5. **Observability**: the five metrics you'd alert on, and which failure each catches.
6. **Open questions and risks**: what you're least sure of, and how you'd find out.

**Version 2 (after `TestM4`):**

7. **Predicted vs observed**, one row per scenario:

   | # | Scenario | Predicted (v1) | Observed (test, break-it or manual run) | Right? | Why the difference / what I changed |
   |---|---|---|---|---|---|
   | F1 | SIGKILL the store mid-burst | | | | |
   | … | | | | | |

8. **Measured numbers** replacing v1's assumptions where you can: fsync cost (BreakIt_FsyncCost), takedown window
   (BreakIt_TakedownWindow), per-instance limiting (BreakIt_LimiterIsPerInstance), and, if you did the stretch, a p99
   table from `loadgen` with the method stated.
9. **Decisions you'd change** for production and why (e.g. group commit, broadcast invalidation, a shared limiter,
   replication), each with what it would cost.

<details data-kind="hint">
<summary>Hints the tutor may give if asked</summary>

- The 60 s takedown SLA constrains `CacheTTL`, `stale-if-error` *and* `redirect-max-age`; say which ones and how.
- "Durable" needs a failure class: process crash, power loss, disk failure. Say which you cover and how you'd test the
  ones a SIGKILL can't reach.
- The interesting rows of the predicted/observed table are the ones where your v1 said "it's handled".

</details>

## Rubric

| Dimension | below | approaching | meets (senior bar) | exceeds |
|---|---|---|---|---|
| **1. Requirements and numbers** | No numbers, or numbers unrelated to decisions | Some numbers (QPS) but cache size, fsync throughput or takedown window missing | Peak rates, hot-set cache size with overhead, fsync-bound create throughput, takedown window from TTLs, effective limit × instances, outage in-flight count — each tied to the decision it drives, marked ≈ | Also cost (memory per instance, store headroom) and what would change at 10× |
| **2. Durability and recovery** | "Writes are saved to a file" | fsync mentioned; recovery rules vague or "skip bad records" | Log-first ack rule; fsync policy chosen with the failure class it covers; torn tail vs mid-file corruption rules with reasons; why SIGKILL can't test fsync and what can | Group commit with numbers; compaction design with the directory fsync; runbook for a corrupt log |
| **3. Idempotency and correctness of creates** | Retries create duplicates, or "use a unique constraint" without a mechanism | Idempotency key stored, but crash between writes or concurrent duplicates not handled | Intent claimed with insert-if-absent before the work, roll-forward on retry, 422 on mismatch, per-client scope, collision retry; concurrent-duplicate outcome stated | Retention and cleanup of intent records; what a replay returns after a delete; multi-key transaction alternative |
| **4. Cache design and invalidation** | "Cache with Redis", no TTLs or invalidation | TTLs chosen; stampede or negative caching or the fill race missing | Caches answers incl. 404/410; TTLs from the takedown SLA; negative TTL; singleflight on a detached context; the delete/fill race and its fix; cross-instance staleness bound stated in numbers; stale-if-error policy with its takedown cost | Broadcast invalidation with a TTL backstop and its failure modes; measured takedown window in production (canary) |
| **5. Rate limiting and overload** | No limiter, or limiter after the work | Per-client limiter, but placement, `Retry-After` or instance multiplication missing | Limiter first; token bucket with burst; truthful rounded-up `Retry-After`; bounded keys and the eviction trade-off; N × limit stated and what you'd do for a contractual limit | Global overload protection beyond per-client limits (concurrency limit, shedding) with numbers |
| **6. Failure behaviour and degradation** | Assumes the store is up | Some timeouts; behaviour under partition or drain not covered | Deadline budget below the client's timeout; behaviour for refused, slow and silent store; what keeps working (cached redirects) with which policy; drain on SIGTERM; bounded resources during an outage | Readiness semantics, deploy and restart runbook (don't restart APIs during a store outage), retry policy inside the budget |
| **7. Predicted vs observed (v2)** | v2 missing or copies the spec | Table filled but differences unexplained | Every scenario has a prediction made before the test and an observation; every wrong prediction is explained and either fixed or accepted with a reason | Adds a failure the spec didn't list, with a test or break-it that exercises it |
| **8. Observability and communication** | No metrics; disorganised | Metrics listed without what they catch | Five metrics tied to specific failures (e.g. `truncated_bytes`, `stale_served`, `coalesced`, `store_errors`, `rate_limited`); clear structure; trade-off sentences | Dashboards and alerts with thresholds and the on-call action for each |

## Tutor critique protocol

1. **Check v1 was written first.** v1's predictions must predate the passing tests (ask when it was written; compare
   with `sd progress` lab-attempt timestamps if in doubt). A v1 written after the tests caps dimension 7 at
   *approaching*.
2. **Recompute the numbers** from the learner's own assumptions: cache memory, fsync-bound throughput, takedown window,
   N × limit, in-flight goroutines during an outage. Arithmetic that doesn't reproduce is a finding.
3. **Read the predicted-vs-observed table** row by row. For each "right" row, ask how they know; for each "wrong" row,
   check the explanation is a mechanism, not "the test said so".
4. **Probe** (at least four, one at a time, adapting to what they wrote):
   1. "Your store acknowledged a create and the machine lost power. Prove the link exists, or tell me the case where it
      doesn't."
   2. "Your SIGKILL test passes with `-sync never`. What does that tell you about the test?"
   3. "Compliance reports a phishing link at 10:00:00. At what time, at the latest, does every instance answer 410 —
      including during a store outage? Show the formula."
   4. "A sender retries a create after your API crashed between the two store writes. Walk me through the retry."
   5. "Fifty clicks for one cold link arrive while the store is slow, and the first client hangs up after 100 ms. What
      do the other forty-nine get?"
   6. "A tenant pays for 100 creates per minute; you run six API instances. What do they actually get, and what would
      you change?"
   7. "The store is partitioned from one API instance but not the others. What does that instance's traffic see, and
      how would on-call find out?"
   8. "Your p99 is 2 ms. How was it measured?"
5. **Score each dimension** with a quote or number from the write-up, then the overall level.

**Overall-level rule.**

- **meets**: no dimension at *below*, at most two at *approaching*, and dimensions **2, 3, 6 and 7** (durability,
  create correctness, failure behaviour, predicted vs observed) each at least *meets*.
- **exceeds**: *meets* overall with at least three dimensions at *exceeds*, two of them among 2, 3, 6, 7.
- **approaching**: no more than two at *below*, none of 2, 3, 6, 7 at *below*.
- **below**: anything else, or a design that acknowledges creates before they are in the log.

## Recording

`sd record design E1 --self <below|approaching|meets|exceeds> --tutor <level> --notes "<one-line summary>"`
