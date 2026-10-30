# A6 Design exercise — Payroll files and the transfer pipeline

> Time box: 60 minutes, solo, no notes. Then self-score against the rubric (≈ 15 min), then send your write-up to the
> tutor. Use the structure from [`docs/INTERVIEW_FRAMEWORK.md`](../../docs/INTERVIEW_FRAMEWORK.md) and the numbers from
> [`docs/NUMBERS.md`](../../docs/NUMBERS.md).

## Prompt

> "We're a digital-banking platform serving about 40 credit unions. Employers, through their credit union, upload payroll
> files: up to 50,000 credits each, and on the 1st and 15th a few dozen of them arrive between 08:00 and 09:00. The
> credits have to be posted to members' accounts through the core banking system before a 10:00 cut-off, and some lines
> go to accounts at other institutions through a payment-network vendor. Members also make interactive transfers all
> day. Every posted credit must produce events for notifications, fraud and the audit trail. Design the asynchronous
> pipeline behind this."

That's all the interviewer says up front. Treat it like the real interview: ask the clarifying questions you need (write
them down, then write the answers you'd assume if the interviewer said "you decide"), and drive.

Things an interviewer would answer if asked (don't read until you've written your questions):

<details data-kind="hint">
<summary>Interviewer's answers to common clarifying questions</summary>

- Interactive transfers: ≈ 60/s at peak, ≈ 2 M a day. They must post within a few seconds and the app times out after 10 s.
- Payroll: peak morning ≈ 30 files × up to 50,000 lines = up to ≈ 1.5 M lines in an hour, but the typical day is a
  tenth of that. A file belongs to one credit union (one tenant). A line is a (member account, amount, reference).
- The core can post ≈ 200 postings/s in total (interactive and batch together) and is unavailable for maintenance
  02:00–04:00. Posting a line takes ≈ 100 ms against the core; the core accepts a client reference and deduplicates by it.
- ≈ 5% of lines go to external accounts through the vendor. Its call takes ≈ 300 ms, it times out (10 s) on ≈ 0.2% of calls,
  and about 40% of those turn out to have succeeded. It accepts a client reference as an idempotency key and has a
  lookup-by-reference call. A transfer it accepted can still be returned by the receiving bank up to a few days later.
- Employers sometimes upload the same file twice. A malformed line should not stop the file.
- The audit trail must be complete, ordered per account and retained for years; fraud and notifications may lag by
  minutes; the audit store must never miss an event.
- Credit unions are billed per tenant and must not be able to slow each other down.
- The cut-off matters: lines not posted by 10:00 roll to the next business day, which is a contractual penalty.

</details>

## What to produce

1. **Requirements**: functional (bullets) and non-functional (latency for interactive, completion deadline for files,
   consistency, durability, audit), with your clarifying questions and assumptions.
2. **Estimate sheet**: arrival rates (interactive, file lines at peak), the core's capacity and the leftover for batch,
   completion time of one 50,000-line file and of the whole morning, backlog and recovery after a core outage, event
   rates and storage with retention, and an in-flight limit and queue bound derived with Little's law. Show the
   arithmetic and tie each number to a decision.
3. **Architecture diagram**: ingest, durable file storage, the queue or log (say which and why), lanes, workers, the
   core adapter, the outbox and relay, the broker and subscribers, the saga orchestrator for external transfers, DLQs.
   Mark the idempotency keys on every hop and where each pipeline stage commits.
4. **The hard parts, explicitly**:
   - queue versus log, partition key and count (or lane design), ordering guarantees per account and what you give up;
   - delivery semantics and idempotency at each stage: file upload (duplicate files), line to posting (key =
     file ID + line number), posting to core, event publication, each subscriber;
   - the outbox (what is in the row, how ordering and duplicates are handled, relay failure) and the subscribers'
     dedupe;
   - failure handling: malformed lines, poison messages, DLQ contents, retry policy, replay;
   - the external-transfer saga: steps, which is the pivot, timeouts, compensations, stuck handling, returns;
   - backpressure and shedding: where the limits are, what is refused first, per-tenant fairness, how the cut-off
     is protected, how you drain after a core outage;
   - observability: the five metrics and three alerts you'd set.
5. **Trade-off table**: at least three decisions with "wins when / loses when / when the answer flips".
6. **Failure walkthrough**: the core is down 02:00–04:00 and 200,000 postings (scheduled files and interactive
   transfers) are waiting when it comes back; a relay crashes after publishing 100 events and before marking them; the
   vendor times out on a line that actually succeeded.

## Rubric

| Dimension | below | approaching | meets (senior bar) | exceeds |
|---|---|---|---|---|
| Requirements & scope | Starts drawing immediately; no deadline or tenancy thinking | Lists features; NFRs vague ("fast", "reliable") | Clarifies lanes, cut-off, tenancy, audit and duplicates; quantified NFRs; states assumptions | Turns the cut-off into a scheduling requirement and prioritises scope for 60 minutes explicitly |
| Estimation | None, or off by orders of magnitude | Average rates only | Peak line rate, core headroom, completion times, backlog and recovery, event storage, Little's-law limits; arithmetic shown | Uses the numbers to make decisions (lane shares, queue bounds, whether the morning fits before 10:00, retention) |
| Queue/log choice & partitioning | Queue everywhere, or Kafka by reflex, without reasons | Picks one with generic reasons | Queue or log chosen per stage with reasons; key follows the ordering requirement (account, not tenant alone); partition count or lane count derived from rates; states what is not ordered | Plans for hot tenants and re-partitioning; separates the work queue from the event log deliberately |
| Delivery semantics & idempotency | "Exactly-once" asserted; or no thought about duplicates | At-least-once mentioned; idempotency hand-waved | Names the guarantee at each stage; stable keys end to end (file+line to core reference); dedupe atomic with the effect; ack after commit; duplicate file handled | Dedupe retention sized against replay; offset-in-DB or downstream-key choices argued; crash windows enumerated |
| Events and consistency (outbox) | Publishes after commit and calls it done | Mentions an outbox without mechanics | Outbox in the same transaction, at-least-once relay with leases, event ID as dedupe key, per-account ordering handled, oldest-unsent alert | CDC versus polling argued from the rate; relay lag and replay procedures; audit subscriber that stops rather than drops at its limit |
| Failure handling (poison, DLQ, replay) | Retries forever, or drops failures | Has a DLQ but no classification or ownership | Transient versus permanent classification, bounded retries, DLQ with context and alert, ordering consequence stated, rate-limited audited replay | Per-tenant handling of bad files, quarantine workflow with support tooling, replay dry-runs |
| Sagas & compensation | Sequential calls with try/catch | Saga named, steps listed | Steps classified (compensatable, pivot, retriable), persisted state, stable keys, timeout treated as unknown (lookup or same-key retry), compensation retry then stuck, returns modelled as a new saga | Tombstone or lookup design justified for this vendor; reconciliation job; semantic locks for the hold |
| Backpressure & shedding | Unbounded queue; autoscale as the only answer | Mentions rate limiting generally | Admission control at the edge, lanes with priority and a batch minimum, per-tenant caps, bounds from Little's law, drain plan after the outage (controlled rate), sheds counted by class | Adaptive limits, deadline-aware dropping, cut-off-aware scheduling, explicit retry guidance for clients |
| Operability & communication | No metrics or alerts | Generic dashboards | Age of oldest message, per-partition or per-lane lag, DLQ depth, stuck sagas, shed rate by tenant; runbooks; drives the 60 minutes; trade-off table with flip conditions | Anticipates probes; quantifies cost and migration path; notes what to defer |

## Tutor critique protocol

1. Read the write-up once fully before scoring. Score each dimension independently using the rubric's words; quote the
   learner's own text as evidence for each score.
2. Check the arithmetic in the estimate sheet. An error of more than 10x in a number the design depends on (peak line
   rate, batch completion time, backlog after the outage, in-flight limit) caps *Estimation* at "approaching". Use these
   reference values: 1.5 M lines in an hour ≈ 417 lines/s average at the peak hour against a core with ≈ 140/s left
   after interactive traffic (200 − 60), so the peak morning cannot finish by 10:00 from a standing start (≈ 3 h), which
   the design must acknowledge (more core capacity, per-tenant scheduling, earlier file cut-offs, or negotiated
   limits); one 50,000-line file alone: 50,000 / 140 ≈ 6 minutes. A 2-hour core outage at the peak interactive rate of
   60/s would leave ≈ 430,000 postings queued (60 × 7,200) plus the files, so the drain plan matters (at night the real
   number is lower; the learner should say which rate they assume).
3. Ask these follow-up probes (all of them; the learner answers in writing or live, without notes). Score the answers
   into the relevant dimensions:
   - "The relay crashes after publishing 100 events and before marking them sent. Trace every consumer."
   - "Employer uploads the same 50,000-line file twice, 30 seconds apart. What happens, line by line?"
   - "A line's vendor call times out after 10 s. The vendor actually posted it. Walk me through the saga. What if it
     hadn't?"
   - "One credit union's file contains a line that makes your parser throw. What happens to the other 49,999 lines, to
     that tenant's other files, and to other tenants?"
   - "The core comes back at 04:00 after the outage with 200,000 postings queued. How do you drain, and how do
     interactive transfers at 04:05 fare?"
   - "Your autoscaler has tripled the posting workers and the core's latency is going up. What did you do wrong?"
   - "Why a log and not a queue (or the reverse) for the line stage? What do you give up?"
   - One probe of the tutor's choice on the learner's weakest dimension.
4. Overall level:
   - **meets** requires "meets" or better on *Delivery semantics & idempotency* and *Backpressure & shedding* (the two
     dimensions where a mistake moves money twice or takes the platform down), and no dimension at "below".
   - **exceeds** requires "meets" on every dimension and "exceeds" on at least three.
   - Otherwise **approaching** (or **below** if two or more dimensions are "below").
5. Feedback: three bullets — the strongest part, the one change that would most raise the score, and one concrete thing
   to re-read (link the section, e.g. [`unit-2-outbox-consumers.md`](unit-2-outbox-consumers.md) §2). If the result is
   below "meets", the learner redoes the exercise on a different day (variation: "design the pipeline behind monthly
   statement generation and delivery for all 40 credit unions", same rubric).

## Recording

`sd record design A6 --self <below|approaching|meets|exceeds> --tutor <level> --notes "<one-line summary>"`
