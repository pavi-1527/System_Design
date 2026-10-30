# C1 Design exercise — Transactional link service for the banking platform

> Time box: 45 minutes, solo, no notes. Then self-score against the rubric (≈ 10 minutes), then send it to the
> tutor for the probe session (≈ 15 minutes).
>
> This is a *different* prompt from the unit's guided design on purpose. The numbers and the dominant risks are
> not the same; a design copied from the unit will score below the bar on estimation and on the deep dives.

## Prompt

> "The platform sends transactional messages (SMS and email) to members of 40 credit unions: statement-ready
> notices, device-verification links, application-resumption links, payment reminders. Every message carries a
> link. Design the service that creates those links and serves them when members tap them."

That is all the interviewer says. Everything else (volume, how long links live, whether the destination is
personal, what a takedown must achieve, what reporting is needed, how many regions) is for you to ask or to
state as an assumption. Write your clarifying questions down with the assumption you proceed with; the tutor
answers them during the probe session.

<details data-kind="hint">
<summary>Interviewer answers to likely clarifying questions (open only after you have written your own questions and assumptions)</summary>

- **Volume.** ≈ 20M messages/day across all tenants, almost all with one link. Sends are bursty: statement runs
  at 06:00 local time create ≈ 5M links in ≈ 20 minutes; payment reminders cluster at 09:00. Members tap ≈ 2
  times per link on average (≈ 40M redirects/day); half of the taps happen within an hour of the send.
- **Links.** Device-verification links live 15 minutes and are single-use; statement links live 30 days;
  application links live 7 days. The destination is personal (a statement, a pre-filled application), so codes
  must be unguessable. No caller-chosen aliases. Tenants want their own short domains (`cu-name.link/...`).
- **Takedown.** Phishing campaigns imitating the platform happen. Any link, or every link of a tenant, must be
  dead everywhere within 5 minutes of a takedown decision, and the operator must be able to prove when it
  became dead.
- **Reporting.** Per-campaign delivered/clicked counts, exact, available by the next morning; "clicked within
  the hour" live counts are nice to have. No per-member click tracking beyond what audit requires.
- **Audit and retention.** Who created which link, from which template and message, must be kept 7 years;
  the link itself may stop resolving after its expiry.
- **NFRs.** Redirect p99 ≤ 150 ms end to end; redirects available 99.95%; creates must survive a 10-minute
  outage of any one dependency without losing or duplicating links (the sender retries). Two regions,
  active/passive today; the sender runs in the primary region.
- **Constraints.** Team of 5; the platform already runs Postgres, Redis and a Kafka-style log; moderate budget.

</details>

## What to produce

1. **Timestamp log.** The clock at the start of each phase (numbers by ≈ 0:10, complete diagram by ≈ 0:25, stop
   drilling by ≈ 0:40). Written during the exercise, not reconstructed.
2. **Requirements card.** Functional requirements, NFRs with numbers, out of scope, and your clarifying questions
   each with the assumption you proceeded with. Name the dominant risk in one sentence.
3. **Estimate sheet.** At most 6 numbers with the arithmetic shown and the decision each one drives. The burst
   create rate and the storage over the 7-year audit retention are the ones that differ most from the unit.
4. **API and data model.** The create call (idempotency across sender retries; batch or single), the redirect,
   the takedown call; entities, keys and indexes from the access patterns; where per-tenant domains and audit
   records live.
5. **HLD diagram.** Create and redirect paths traced with rates; every box justified; the takedown fan-out drawn.
6. **Three deep-dive cards** (claim → why here → cost → flip): (a) the code scheme and why it is unguessable and
   unique under the 06:00 burst; (b) the redirect path's availability and the 15-minute single-use link; (c) the
   takedown path and its proof.
7. **Failure walk-through.** The link store is unavailable for 10 minutes at 06:05; the cache cluster restarts at
   09:00; the primary region is lost. What do senders see, what do members see, what is lost, what is duplicated?
8. **Metrics and alerts.** Five signals, including the takedown-completion time and one correctness signal.
9. **Risks and open questions.** What you would validate first.

## Rubric

| Dimension | below | approaching | meets (senior bar) | exceeds |
|---|---|---|---|---|
| **1. Requirements and scope** | Features listed; NFRs as adjectives; no questions about guessability, expiry or takedown | Some NFRs with numbers; misses one of the three questions that move boxes | 3–5 core operations; every NFR numbered; explicit out-of-scope; asks about guessability, expiry/single-use, takedown SLA and reporting exactness; names the dominant risk | Also notices the burst-write shape differs from a public shortener and says what that changes before estimating |
| **2. Estimation** | No numbers, or unit errors ≥ 8×, or numbers unused | Average rates only; burst and audit retention missing | Burst create rate (≈ 4k/s), peak redirects (≈ 2k/s), storage over 7 years with replication, hot set, each tied to a decision; marked ≈ | Sanity-checks one number against another (e.g. clicks within the hour vs hot set) and states which number would flip the design at 10× |
| **3. API and data model** | Non-idempotent create; auto-increment in the URL; no takedown operation | Idempotent create but batch retries unaddressed; model without audit or tenancy | Idempotent (batch) create keyed per message; 302 with no-store; 404/410; tenant in the key; access patterns → keys and indexes; audit table separate from the link row | Designs single-use consumption atomically (compare-and-set on `used`) and explains the per-tenant domain routing |
| **4. High-level design** | A path cannot be traced; boxes unjustified | Paths traceable with gaps (where does the audit record go? who purges?) | Both paths complete with rates; takedown fan-out drawn; every box forced by a requirement or a number | Also separates the create tier from the redirect tier with a reason, or argues why not |
| **5. Code scheme (deep dive a)** | Sequential ids exposed, or random codes with check-then-insert | Picks a scheme but can't give collision or coordination numbers | Unguessable scheme with the arithmetic (collision probability, or range + permutation), the atomic primitive named, burst behaviour and crash/outage behaviour stated | Discusses per-region ranges or random codes for the passive region's activation and the seed as a secret |
| **6. Redirect path (deep dive b)** | Store on every redirect; no cache failure story | Cache present; single-use link race or cold cache unaddressed | L1/Redis/store layering with TTLs; lazy expiry; single-use consumed atomically at the store; fail-open on store outage with timeouts and a breaker; cold-cache factor computed | Quantifies the availability budget (99.95% ≈ 22 min/month) against the failure walk-through |
| **7. Takedown, expiry and tenancy (deep dive c)** | TTL-only takedown; no per-tenant isolation | Explicit purge but no proof; tenant in key by convention | Store → cache → every server → edge purge with acknowledgements; a recorded "dead everywhere at" timestamp; per-tenant bulk takedown; tenant-scoped keys from one builder | Designs the per-tenant kill switch and the audit evidence an examiner would accept |
| **8. Reporting and audit** | Counters in the link row; raw PII in events | Async pipeline but exactness vs freshness confused; retention unaddressed | Async events → log → aggregates; exact next-day per campaign; audit records kept 7 years separately from links; no raw identifiers | Sizes the audit store, picks aggregate resolution and retention with cost, and states the live count's error |
| **9. Failure modes and operations** | None, or "add replicas" | Some failures named without member-visible effect | All three scenarios walked with what is lost, duplicated and visible; detection named per scenario; rollout/migration mentioned | Game-day plan for the 06:00 burst and the takedown drill |
| **10. Communication and trade-offs** | Disorganised; no flip conditions | Trade-offs for some decisions; time slips > 5 min | Driven by the candidate; checkpoints met within ≈ 2 min; three cards with measurable flips; says what to validate first | Anticipates probes; gives a phased rollout and a cost line |

## Tutor critique protocol

Read the artifact first, then run the conversation. Do not reveal the rubric rows until the learner has
self-scored every dimension with one line of justification each.

1. **Check the numbers (≈ 3 min).** Recompute the burst create rate (5M in 20 min ≈ 4.2k/s), the peak redirect
   rate, the 7-year audit storage and the hot set from the learner's own assumptions. Check specifically for a
   design sized for the 230/s *average* create rate.
2. **Check the atomic primitives.** Where is uniqueness enforced? Where is single-use enforced? If either is a
   check-then-act, that is a finding on dimension 5 or 6.
3. **Walk the takedown.** Ask the learner to list every place a copy of the redirect can live (browser, CDN, L1,
   Redis, store, the passive region) and show how each is reached within 5 minutes, and what the proof is.
4. **Ask the follow-up probes** (at least six; adapt to what the learner wrote):
   1. "It's 06:00 and the statement run is creating 4,000 links a second. Walk me through one create end to end:
      what is synchronous, what is coordinated, what happens if the counter or the unique index is slow?"
   2. "The sender's job crashes halfway through a batch and restarts. How many links does the member end up
      with, and why?"
   3. "A member taps a 15-minute verification link twice, from two devices, within the same second. What happens?"
   4. "A phishing campaign is using one tenant's domain. Show me the takedown from the operator's click to the
      last cached copy, with times, and what you would show an examiner afterwards."
   5. "The link store is down at 06:05. What do senders see? What do members tapping yesterday's statement
      links see?"
   6. "Why is the link row not where the click count lives, and how exact is the per-campaign number at 08:00
      the next morning?"
   7. "You lose the primary region. What is the RPO for links created in the last minute, and what does the
      passive region need in order to create *new* links without duplicating codes?"
   8. "Codes must be unguessable. Prove to me that an attacker who has 10,000 of their own links cannot derive
      anyone else's."
   9. "What does this cost per month, and which line grows with tenants rather than with messages?"
5. **Assign levels** per dimension with one piece of evidence each, then the overall level with the rule below.
   Record the learner's one-line summary of the weakest dimension in the notes.

**Overall-level rule.**

- **meets**: no dimension at *below*, at most two at *approaching*, and dimensions **5, 6, 7 and 9** (code scheme,
  redirect path, takedown/tenancy, failure modes) each at least *meets*.
- **exceeds**: *meets* overall, with at least four dimensions at *exceeds* including two of 5, 6, 7 and 9.
- **approaching**: no more than two dimensions at *below*, and none of 5, 6, 7, 9 at *below*.
- **below**: anything else, or any design in which codes are enumerable, uniqueness or single-use is enforced by
  check-then-act, or takedown relies on TTL expiry alone.

**Below meets?** Give 2–3 concrete revision tasks and have the learner redo the exercise on a variant after at
least one day (suggested: "design the link service for a marketing team: public campaign links, custom aliases,
click analytics in real time, 1B redirects/day", which inverts the read/write shape again).

## Recording

`sd record design C1 --self <below|approaching|meets|exceeds> --tutor <level> --notes "<one-line summary>"`

Example: `sd record design C1 --self meets --tutor approaching --notes "sized for the average create rate; single-use link enforced in cache not store; takedown had no proof"`
