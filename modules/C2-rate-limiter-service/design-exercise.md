# C2 Design exercise — Vendor-call budget service for the banking platform

> Time box: 45 minutes, solo, no notes. Then self-score against the rubric (≈ 10 minutes), then send it to the
> tutor for the probe session (≈ 15 minutes).
>
> This is a *different* prompt from the unit's guided design on purpose. The traffic is outbound, the store load is
> tiny, demand sometimes exceeds the limit, and some calls are slow. A design copied from the unit (inbound 429s,
> leases, 18 shards) will score below the bar on estimation, on allocation and on the deep dives.

## Prompt

> "Our integration adapters call external vendors on behalf of 40 credit unions: the core-banking processor, two
> credit bureaus, a card processor and a KYC provider. Every vendor contract limits how fast and how much we may
> call them. Design the service that keeps every adapter, in every region, within those limits."

That is all the interviewer says. Everything else (how many adapters, which limits, what happens when demand
exceeds the contract, who goes first, what to do when your own service is down, how many regions) is for you to
ask or to state as an assumption. Write your clarifying questions down with the assumption you proceed with; the
tutor answers them during the probe session.

<details data-kind="hint">
<summary>Interviewer answers to likely clarifying questions (open only after you have written your own questions and assumptions)</summary>

- **Callers.** ≈ 60 adapter instances in two regions (active/active), autoscaling between ≈ 30 and ≈ 90 during the
  day. Each adapter serves every credit union.
- **Core-banking processor.** Contract: **300 req/s across all of our callers**, and at most **50 concurrent
  connections**. Above that it returns `429` with `Retry-After`, and repeated violations in a month carry a penalty
  fee. Demand: ≈ 150 req/s of member-facing calls (balance at login, transfer initiation) all day; payroll partners'
  batches add ≈ 250 req/s for ≈ 20 minutes at 09:00 on paydays. Demand exceeds the contract at peak.
- **Credit bureau A.** **50,000 pulls per credit union per day** (billed; overage costs ≈ 3× the unit price) and
  **20 req/s** total. A pull must never be made without quota.
- **KYC provider.** **10 req/s** total; each call takes **2–8 s**. Nightly re-verification jobs want to run as fast
  as possible.
- **Priorities.** Member-facing calls have a 1 s budget and must go first; batch work (payroll, statement runs,
  nightly KYC) can wait minutes. Inside a priority class, no credit union may take more than its share while others
  are waiting; shares are weighted by contract tier (large credit unions have 4× the weight of small ones).
- **Behaviour on contention.** Callers prefer *waiting* (up to ≈ 500 ms interactive, minutes for batch) to being
  rejected.
- **Failure.** If the budget service is unavailable, adapters must keep serving members. Briefly exceeding a vendor
  rate is a penalty risk; exceeding the bureau quota is a direct cost; failing member logins is worse than both.
- **Evidence.** Finance and the vendor-management team want monthly evidence of usage per vendor and per credit
  union, and alerts before a quota runs out.
- **Constraints.** Team of 4; the platform already runs Postgres, Redis and a Kafka-style log; moderate budget.

</details>

## What to produce

1. **Timestamp log.** The clock at the start of each phase (numbers by ≈ 0:10, complete diagram by ≈ 0:25, stop
   drilling by ≈ 0:40). Written during the exercise, not reconstructed.
2. **Requirements card.** Functional requirements, NFRs with numbers, out of scope, and your clarifying questions
   each with the assumption you proceeded with. Name the dominant risk in one sentence.
3. **Estimate sheet.** At most 6 numbers with the arithmetic shown and the decision each one drives. The ones that
   differ most from the unit: the decision rate against the store (tiny), the payday shortfall (demand vs
   contract), the KYC concurrency (Little's law), and the per-adapter share under autoscaling.
4. **API and data model.** The call an adapter makes before calling a vendor (with priority, tenant, cost, and a
   maximum wait); what it returns; the rule and quota records; the counter keys and their TTLs; where usage evidence
   lives.
5. **HLD diagram.** Both regions, the adapters, your service or library, the shared state, the vendors, the evidence
   path; rates on the arrows; every box justified.
6. **Three deep-dive cards** (claim → why here → cost → flip): (a) enforcing the global rate and the quota
   atomically across 60 adapters and two regions; (b) allocation when demand exceeds the contract: priorities,
   weighted fairness and waiting instead of rejecting; (c) the slow vendor: concurrency limits, the vendor's own 429s,
   and what happens when your service is down.
7. **Failure walk-through.** The budget service's Redis is unavailable for 10 minutes at 09:05 on payday; one region
   is lost; the KYC provider slows to 8 s per call. What do members see, what does each vendor see, what is
   overspent?
8. **Metrics and alerts.** Five signals, including one per vendor contract and one for quota burn-down.
9. **Risks and open questions.** What you would validate first.

## Rubric

| Dimension | below | approaching | meets (senior bar) | exceeds |
|---|---|---|---|---|
| **1. Requirements and scope** | "Rate limit the vendors"; NFRs as adjectives; no questions about priority, waiting or failure | Some numbers; treats it as the inbound limiter of the unit | Separates rate, concurrency and quota limits; asks about priorities, fairness, wait vs reject, multi-region and the fail mode; out-of-scope list; names the dominant risk (demand > contract at peak) | Notices up front that store throughput is a non-issue and that the design is an allocation problem, and says what that changes |
| **2. Estimation** | No numbers, or unit errors ≥ 8× | Contract rates listed, nothing derived | Store load (≈ hundreds of decisions/s), the payday shortfall (≈ 400 demand vs 300), KYC in-flight (10/s × 8 s = 80), per-adapter share at 30 vs 90 adapters, quota burn per credit union; each tied to a decision; marked ≈ | Sanity-checks one number against another (e.g. the 50-connection cap vs Little's law on core banking) and states which number would flip the design at 10× |
| **3. API and data model** | Boolean "allowed?" call; no tenant or priority | Tenant and priority present; no wait semantics or quota atomicity | `Acquire(vendor, tenant, priority, cost, max_wait)` → granted / wait / denied with a reason; a release for concurrency slots; rules per vendor and per tenant with versions; one counter per (vendor, limit) and per (vendor, tenant, day) with TTLs; usage events for evidence | Defines the quota reservation (reserve, then confirm or release) so a failed vendor call doesn't burn quota, and the idempotency of retries |
| **4. High-level design** | A path cannot be traced; limits per adapter only | Shared state drawn, regions or evidence path missing | Adapters → library/service → shared state → vendors traced in both regions with rates; evidence path (events → log → aggregates); every box justified | Argues placement (library in adapters vs a central dispatcher that owns the queues) with a flip condition |
| **5. Shared state and atomicity (deep dive a)** | `limit / N` per adapter, or `GET`/`SET` | Shared counters, but the quota or the concurrency count is check-then-act | One atomic step per decision (script or compare-and-swap); the quota decremented atomically (never below zero); concurrency slots as expiring leases released on completion; store's clock; regions split the contract with rebalancing | Explains why leases/batching are *not* needed here (≈ 5 req/s per adapter) and what would change that |
| **6. Allocation (deep dive b)** | First come, first served | Priorities named, no mechanism; or fairness without weights | Strict priority for interactive within a reserved floor; weighted max-min fairness across credit unions for batch; waiting with a bound (a queue per class, or GCRA's retry-after as the wait) instead of rejecting; what each caller sees at 09:00 | Quantifies the payday plan (who waits how long) and proposes moving batch to off-peak with the business |
| **7. Slow vendor and vendor feedback (deep dive c)** | Rate limit only | Concurrency mentioned, no numbers | Concurrency limit per vendor from Little's law, with slots that expire if an adapter dies; adaptive response to vendor 429s (honour `Retry-After`, lower the local rate, breaker); interaction with the rate limit stated | Adaptive concurrency (AIMD) against vendor latency, with the A7 reasoning |
| **8. Quotas, evidence and audit** | No quota enforcement, or quota counted after the fact only | Real-time counter, no reconciliation or alerting | Atomic per-day quota per credit union, reconciled against the vendor's invoice/log; usage events to a log and monthly aggregates; burn-down alerts (e.g. 80% by mid-day) | Designs overage handling (block, borrow from the tenant's next-day quota, or approve with a record) as a business choice with an owner |
| **9. Failure modes and operations** | None, or "add replicas" | Some failures named without member-visible effect | All three scenarios walked: per-limit fail mode (interactive core banking falls back to a local `limit / adapters`; bureau pulls fail closed without quota; KYC batch pauses); region loss re-splits the contract; slow vendor caps concurrency; detection per scenario; timeouts on every call | Game-day plan for payday and the evidence an auditor would accept |
| **10. Communication and trade-offs** | Disorganised; no flip conditions | Trade-offs for some decisions; time slips > 5 min | Driven by the candidate; checkpoints met within ≈ 2 min; three cards with measurable flips; says what to validate first | Anticipates probes; gives a phased rollout and a cost line |

## Tutor critique protocol

Read the artifact first, then run the conversation. Do not reveal the rubric rows until the learner has
self-scored every dimension with one line of justification each.

1. **Check the numbers (≈ 3 min).** Recompute the store load (a few hundred decisions/s: one small Redis), the
   payday shortfall (≈ 150 + 250 = 400 demand against 300), KYC concurrency (10 req/s × up to 8 s ≈ 80 in flight,
   against what limit?) and the per-adapter share (300 / 30 = 10 vs 300 / 90 ≈ 3.3 req/s). Check specifically for a
   design sized like the unit's (shards, leases) for a problem with ≈ 1/1000 of its decision rate.
2. **Check the atomic primitives.** Where is the global rate enforced? The quota? The concurrency count? If any is
   check-then-act or per adapter, that is a finding on dimension 5. If concurrency slots don't expire, a crashed
   adapter leaks them: finding.
3. **Walk 09:00 on payday.** Ask the learner to say, second by second, which calls go out, which wait, for how long,
   and what a member logging in sees.
4. **Ask the follow-up probes** (at least six; adapt to what the learner wrote):
   1. "It's 09:00 on payday: payroll wants 250 req/s to core banking, members want 150, the contract is 300. Who
      waits, how long, and what implements it?"
   2. "Adapters autoscale from 30 to 90 between 08:30 and 09:00. What happens to your limit, and to the vendor?"
   3. "Your Redis is down for 10 minutes at 09:05. What do adapters do for core banking, for bureau pulls and for
      KYC? What does each vendor see?"
   4. "The vendor starts returning 429s at 250 req/s although the contract says 300. What does your system do in the
      next minute, and what do you do next week?"
   5. "Credit union 17 has 1 bureau pull left today. Two adapters request one at the same instant. What happens? And
      if the granted call then fails with a timeout?"
   6. "The KYC provider slows from 2 s to 8 s per call. What happens to your 10 req/s limit, and which limit actually
      protects you?"
   7. "Two regions share one 300 req/s contract. How do they split it, and what happens when one region is lost?"
   8. "Why not a token bucket in each adapter at `limit / N`?"
   9. "Prove to vendor management that we stayed within contract last month, per vendor and per credit union."
5. **Assign levels** per dimension with one piece of evidence each, then the overall level with the rule below.
   Record the learner's one-line summary of the weakest dimension in the notes.

**Overall-level rule.**

- **meets**: no dimension at *below*, at most two at *approaching*, and dimensions **5, 6, 7 and 9** (shared state,
  allocation, slow vendor, failure modes) each at least *meets*.
- **exceeds**: *meets* overall, with at least four dimensions at *exceeds* including two of 5, 6, 7 and 9.
- **approaching**: no more than two dimensions at *below*, and none of 5, 6, 7, 9 at *below*.
- **below**: anything else, or any design in which the global contract is enforced per adapter as `limit / N`
  without a shared state, a quota can go negative under concurrency, or concurrency slots never expire.

**Below meets?** Give 2–3 concrete revision tasks and have the learner redo the exercise on a variant after at
least one day (suggested: "design the inbound limiter for the partner API: 200 fintech partners, per-partner plans,
per-credit-union fairness, 5k req/s, one partner at 2,000 req/s on paydays", which brings back the hot key and the
429 contract).

## Recording

`sd record design C2 --self <below|approaching|meets|exceeds> --tutor <level> --notes "<one-line summary>"`

Example: `sd record design C2 --self meets --tutor approaching --notes "quota check-then-act; no concurrency limit for KYC; payday allocation was FIFO"`
