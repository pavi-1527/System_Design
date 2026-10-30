# A5 Design exercise — Two-region payments platform: what must be consistent?

> Time box: 45 minutes, solo, no notes. Then self-score against the rubric (≈ 15 min), then send your write-up to the
> tutor. Use the structure from [`docs/INTERVIEW_FRAMEWORK.md`](../../docs/INTERVIEW_FRAMEWORK.md) and the numbers from
> [`docs/NUMBERS.md`](../../docs/NUMBERS.md) and this module's units.

## Prompt

> "We run a digital-banking platform for about 40 credit unions. Members move money between their own accounts and to
> external accounts, partners call our API to do the same, and a payroll provider submits large files. Today everything
> runs in one region against the credit unions' core banking systems. Leadership wants it to survive the loss of a region
> and to stay correct while doing it. Walk me through which parts of this system need strong consistency, which don't,
> and what you'd build to get each."

That is all the interviewer says up front. Treat it like the real interview: ask the clarifying questions you need
(write them down, then write the answers you'd assume if the interviewer said "you decide"), state the invariants,
and drive. Midway through, the interviewer will add: "now both regions must accept traffic."

Things an interviewer would answer if asked (don't read until you've written your questions):

<details data-kind="hint">
<summary>Interviewer's answers to common clarifying questions</summary>

- ≈ 2 M transfers/day at launch, plan for 10× in two years; peak ≈ 3× the average. Balance and activity reads ≈ 20× writes.
- Each transfer posts to the credit union's core banking system through an adapter. Core latency: p50 ≈ 300 ms,
  p99 ≈ 9 s; the core has a nightly batch window of ≈ 30 minutes in which it only accepts a limited set of calls.
- Two regions, us-east (primary) and us-west, ≈ 70 ms round trip. Within a region, three availability zones, ≈ 1–2 ms.
- Target: lose at most seconds of acknowledged data on a regional failure (state your RPO and how you'd measure it),
  recover in ≤ 15 minutes (RTO).
- Partners retry with idempotency keys for up to 24 hours. Vendors (check-deposit, card processor) deliver callbacks
  at-least-once, sometimes late and out of order, up to ~3 deliveries each.
- A payroll provider submits a file of 50,000 credits to 50,000 member accounts debited from one employer account at
  09:00 on paydays; the file must be fully posted within ≈ 5 minutes.
- A nightly job accrues interest for ≈ 1.5 M accounts and must run exactly once per account per period.
- Every posting needs an audit record: who, what, on whose behalf, in which order. Auditors look at the order.
- Members expect to see their own transfer reflected within about a second; other members' views may lag.
- Clock sync is NTP; assume skew between services is usually tens of milliseconds, occasionally seconds.

</details>

## What to produce

1. **Requirements and invariants**: functional bullets; non-functional (latency, availability, RPO/RTO) as numbers; and the
   invariants that must never break ("balance never negative through our path", "a posting happens at most once",
   "no acknowledged posting is silently lost", "the audit order is explainable").
2. **Estimate sheet**: average and peak transfers per second, read rate, the payroll file's effect on one hot account
   (state the commit latency you assume and the resulting time), the RPO your replication choice implies (replication lag
   × peak rate), idempotency/dedupe storage, and the commit latency of any quorum or consensus step.
3. **Consistency table**: for at least eight pieces of state (ledger postings, balance display, idempotency keys, SSO
   session/profile, audit trail, tenant configuration/feature flags, batch-job leadership, activity feed/preferences, and
   any you add), give the model (linearizable / session / causal / eventual / mergeable), the mechanism (single leader,
   quorum N/R/W, consensus group, CRDT/siblings, cache with "as of"), and the behaviour during a partition.
4. **The hard parts, explicitly**:
   - replication and placement (where the leader of an account lives, synchronous vs asynchronous replication, quorum or
     group sizes and failure domains, what happens in a cut between regions);
   - time and ordering (which components may use wall-clock time, what orders the audit trail, how out-of-order vendor
     callbacks are handled);
   - exactly-once effects (event IDs, where the dedupe record lives for a credit in the core, retention, reconciliation);
   - coordination (who runs the nightly job and the payroll file, lease, fencing token, what the stale holder is stopped
     by).
5. **Architecture diagram**: regions, AZs, the core adapter, the stores with their consistency level, the coordination
   service, and where idempotency keys and fencing tokens flow.
6. **Trade-off table**: at least three decisions with "wins when / loses when / when the answer flips".
7. **Failure walkthroughs**: (a) the link between regions is cut for 5 minutes; (b) the batch runner freezes for 20 s
   after taking its lease; (c) a vendor callback arrives three times, once two hours late and after the `settled` event;
   (d) one service's clock is 40 s slow; (e) the primary region is lost at peak. For each: what members and partners see,
   what is detected, how it recovers, what could be wrong afterwards.

## Rubric

| Dimension | below | approaching | meets (senior bar) | exceeds |
|---|---|---|---|---|
| Requirements & invariants | Starts drawing immediately; "strongly consistent" with no definition | Lists features; invariants vague ("money must be correct") | States concrete invariants and RPO/RTO as numbers; separates what must hold across copies from what is display; states assumptions | Spots that per-account ordering, not global ordering, is the real requirement and says so early; ranks invariants by blast radius |
| Estimation & hot-spot math | None, or off by orders of magnitude | Average QPS only | Average and peak TPS; hot-account time for the payroll file from commit latency; RPO = lag × rate; quorum/consensus commit latency from RTTs; arithmetic shown | Uses the numbers to decide (per-account single writer because 70 ms × 50,000 is an hour; sub-accounts or batching for the hot account) |
| Consistency model per data class | One model for everything ("use a strongly consistent DB" or "eventual for scale") | Some distinction, mechanisms vague or wrong (LWW for balances) | Table of ≥ 8 states with model, mechanism and partition behaviour; strong where a decision reads the value, weak where it is display; no CAP slogans without an operation attached | Chooses per-entity home regions, escrow or CRDTs where they fit, and states what each member sees during a failover |
| Replication, quorum and consensus | "Replicate it"; sizes and placement unexplained | Mentions quorums or Raft without numbers or failure domains | Explains N/R/W or group size and failure domains (three domains, odd size, witness), sync vs async with the RPO it gives, what a cut between regions does to each side, split-brain prevention | Quantifies availability of the chosen group size, discusses read paths (`ReadIndex` vs follower reads) and membership/rolling-restart safety |
| Time & ordering | Wall-clock timestamps everywhere | Mentions "logical clocks" without saying where | Identifies which components may use wall time (display, TTL with slack) and which need versions, per-entity sequence or hybrid timestamps (audit ordering); handles out-of-order vendor events with versions | Quantifies the cost of skew (e.g. ≈ 5.5% mis-ordered at 50 ms), sets a clock-offset alert, and argues HLC or a sequencer for the audit trail |
| Exactly-once effects | "The queue is exactly-once" or "the client shouldn't retry" | Idempotency keys named without mechanics | Producer-assigned event IDs; dedupe record in the same transaction as the effect (or idempotency key + attempt record + reconciliation for the core); retention reasoned; reconciliation as backstop | Handles the crash between the core call and the record, ordering via entity versions, and measures duplicate-hit rate as a health signal |
| Coordination: leases & fencing | A lock "in Redis" with no failure story | Lease named; stale-holder problem not addressed | Lease for liveness plus a fencing token checked atomically at the resource, plus idempotent `(account, period)` postings; explains the pause scenario in the failure walkthrough | Fences the old primary during failover (STONITH or token), monitors takeovers, rehearses a frozen holder |
| Failure walkthroughs | One or none | Plausible, not worked through | All five walked end to end with what members see, detection, recovery, and what could still be wrong; the mid-way "both regions" change absorbed without a rewrite | Anticipates follow-ups and names residual risks and their reconciliation |
| Trade-offs & communication | Single answer per decision | Trade-offs only when prompted | Trade-off table with "when the answer flips"; drives the 45 minutes; checks in with the interviewer; finishes on time | Quantifies costs (latency, cross-region traffic, operations), says what they would defer |

## Tutor critique protocol

1. Read the write-up once fully before scoring. Score each dimension independently using the rubric's words; quote the
   learner's own text as evidence for each score.
2. Check the arithmetic. An estimate that is off by > 10× on a number the design depends on (the payroll hot-account
   time, the RPO) caps *Estimation & hot-spot math* at "approaching". Check that the learner distinguishes "acknowledged"
   from "applied" wherever they claim a guarantee.
3. Ask these follow-up probes (all of them; the learner answers in writing or live, without notes). Score the answers into
   the relevant dimensions:
   - "The link between regions is cut for five minutes. Walk me through ledger postings, the balance on the home screen
     and a card authorisation in each region. Who is unavailable, and what do they see?"
   - "Your nightly accrual runner takes a lease and then freezes for 20 seconds. What stops it from posting interest a
     second time? Show me the line or the constraint."
   - "The vendor sends `settled` before `accepted`, then `accepted` three times, one of them two hours late. Trace each
     delivery through your system."
   - "Skew between your services is 80 ms. Which decisions in your design depend on wall-clock time, and what breaks?"
   - "You chose asynchronous replication to the DR region. Quantify what a failover at peak loses, how you detect it and
     how you repair it."
   - "Why not make every write linearizable across both regions? Show me what that does to the payroll file."
   - "Add a requirement: a member edits alert preferences on two devices while offline. What is the merge?"
   - One probe of the tutor's choice on the learner's weakest dimension.
4. Overall level:
   - **meets** requires "meets" or better on *Consistency model per data class*, *Exactly-once effects* and
     *Coordination: leases & fencing* (the three dimensions where a mistake moves money twice or loses it), and no
     dimension at "below".
   - **exceeds** requires "meets" on every dimension and "exceeds" on at least three.
   - Otherwise **approaching** (or **below** if two or more dimensions are "below").
5. Feedback: three bullets: the strongest part, the one change that would most raise the score, and one concrete thing to
   re-read (link the section, e.g. [`unit-2-time-ordering.md`](unit-2-time-ordering.md) §4 or
   [`unit-3-consensus-leases.md`](unit-3-consensus-leases.md) §3). If the result is below "meets", the learner redoes the
   exercise on a different day (variation: "design the same for a ticket-booking platform with seat holds", same rubric).

## Recording

`sd record design A5 --self <below|approaching|meets|exceeds> --tutor <level> --notes "<one-line summary>"`
