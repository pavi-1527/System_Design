# A1 Design exercise — Partner Transfers API

> Time box: 45 minutes, solo, no notes. Then self-score against the rubric (≈ 15 min), then send your write-up to
> the tutor. Use the structure from [`docs/INTERVIEW_FRAMEWORK.md`](../../docs/INTERVIEW_FRAMEWORK.md) and the
> numbers from [`docs/NUMBERS.md`](../../docs/NUMBERS.md).

## Prompt

> "We're a digital-banking platform serving about 40 credit unions. We want to let third-party fintech apps — budgeting
> apps, payroll providers, a crypto on-ramp — move money on behalf of credit-union members: transfers between a
> member's own accounts and outbound transfers to external accounts. Design the public API those partners will use."

That is all the interviewer says up front. Treat it like the real interview: ask the clarifying questions you need
(write them down, then write the answers you'd assume if the interviewer said "you decide"), and drive.

Things an interviewer would answer if asked (don't read until you've written your questions):

<details data-kind="hint">
<summary>Interviewer's answers to common clarifying questions</summary>

- ≈ 200 partners at launch, a handful large (one payroll provider runs batch files of 50k transfers twice a month).
- ≈ 2 M transfers/day across all tenants at launch; plan for 10x in two years. Read traffic (status checks, lists)
  ≈ 20x writes.
- Transfers settle through the credit union's core banking system, via your existing adapters. Core latency: p50
  ≈ 300 ms, p99 ≈ 9 s, occasionally unavailable for maintenance windows.
- Partners are server-side (no partner code runs on members' devices). Members authorise each partner once, through
  their credit union's login.
- Partners must learn about status changes (pending → completed / failed / returned) within seconds.
- Regulators and credit unions require a complete audit trail of who initiated what, on whose behalf.
- One partner may serve members of several credit unions; a partner's access to credit union A must never grant
  access to credit union B.

</details>

## What to produce

1. **Requirements**: functional (bullets) and non-functional (latency, availability, consistency, security,
   compliance), with your clarifying questions and assumptions.
2. **Estimate sheet**: write QPS (average and peak, including the batch-file pattern), read QPS, webhook deliveries
   per second, idempotency-store size, and the concurrency you need toward core banking (Little's law).
3. **API surface**: resources and endpoints (method, path, request/response sketch, status codes), including at least:
   create transfer, get transfer, list transfers, cancel, and how partners receive status changes.
4. **The hard parts, explicitly**:
   - idempotency (header, scope, fingerprint, in-flight behaviour, retention, what happens when core banking times out);
   - pagination (ordering, cursor format, guarantees under concurrent inserts);
   - authentication and authorisation (how partners authenticate, how member consent and tenant scoping work, where
     tokens are validated);
   - errors and rate limits (error shape, which codes, `429` and its headers, `Retry-After`);
   - versioning and evolution rules you'd publish on day one;
   - webhooks (signing, retries, ordering, replay/catch-up).
5. **Architecture diagram**: edge (DNS/CDN?), load balancer layer, API gateway, transfers service, idempotency store,
   database, adapter to core banking, webhook dispatcher. Mark where TLS terminates and where tokens are validated.
6. **Trade-off table**: at least three decisions with "wins when / loses when / when the answer flips".
7. **Failure walkthrough**: core banking times out on a transfer; a partner's webhook endpoint is down for 6 hours;
   one API instance is slow but passing health checks.

## Rubric

| Dimension | below | approaching | meets (senior bar) | exceeds |
|---|---|---|---|---|
| Requirements & scope | Starts drawing immediately; no NFRs | Lists features; NFRs vague ("fast", "secure") | Clarifies tenants, partner types, consistency and audit needs; quantified NFRs; states assumptions | Spots the batch-file pattern and consent model as design drivers early; prioritises scope for 45 min explicitly |
| Estimation | None, or off by orders of magnitude | Average QPS only | Avg and peak write/read QPS, webhook rate, idempotency storage, concurrency to core via Little's law; arithmetic shown | Uses the numbers to make decisions (async acceptance because core p99 ≈ 9 s; pool and bulkhead sizes; storage retention) |
| Resource model & HTTP semantics | RPC-style verbs; 200 for everything | Resources but inconsistent methods/codes | Clean resources, correct methods, 201 + Location, 202 where async, 409/412/422/429 used precisely, one error shape with stable codes and request IDs | State machine for transfers drives the API (actions vs PATCH), ETag/If-Match where edits exist, consistent across endpoints |
| Correctness under retries (idempotency) | Not mentioned, or "the client shouldn't retry" | Idempotency key mentioned without mechanics | Client-generated key per intent, scoped per (partner, tenant), fingerprint → 422, in-flight → 409 or wait, replay, retention ≥ retry window, atomic reservation, downstream key to core | Crash-between-effect-and-record handled (leases + downstream idempotency or reconciliation); 5xx caching decision defended; batch files idempotent per line |
| Pagination & listing | Offset with no thought | Cursor mentioned, details hand-waved | Keyset on (created_at, id) with index, opaque signed cursor, limit + max, `has_more`/next cursor, stated guarantee under concurrent inserts | Filters bound into the cursor; exports moved to async jobs; reconciliation feed for partners |
| Auth & tenancy | API keys with full access, or "use OAuth" with no detail | OAuth named; tenant isolation implied | Client credentials (or equivalent) for partners, member consent via authorization code at the CU's login, tokens scoped to partner + tenant + member grant, gateway coarse checks + service object-level checks, audit of principal and on-behalf-of | Sender-constrained tokens (mTLS/DPoP) for money movement, key rotation, revocation story for consent withdrawal, BOLA explicitly prevented in data access |
| Errors, rate limits & versioning | Not addressed | Some status codes; no rate-limit design | 429 + `Retry-After` (and remaining-quota headers), per-partner and per-tenant limits, tolerant-reader rules published, a versioning strategy chosen with reasons | Separate limits for batch vs interactive, date-pinned versions with transformers vs URL majors argued from the partner population |
| Webhooks & async | Polling only, or webhooks without reliability | Webhooks mentioned with retries | Outbox, at-least-once with backoff + jitter, per-endpoint queues, HMAC signature with timestamp, event IDs for dedup, ordering guidance, event feed for catch-up | SSRF protection for registered URLs, thin vs fat events argued, partner-visible delivery logs and replay tooling |
| Architecture & failure modes | Boxes without responsibilities | Plausible diagram; failures not discussed | Each failure walkthrough answered concretely (timeouts → pending + webhook; partner down → backoff/disable/replay; slow instance → outlier ejection/least-request); timeouts ordered across hops; bulkhead to core | Capacity headroom and N+1 explicit; deploy/drain story; multi-region trade-offs for the audit/idempotency stores |
| Trade-offs & communication | Single answer per decision | Trade-offs only when prompted | Trade-off table with "when the answer flips"; drives the 45 minutes; checks in with the interviewer | Anticipates follow-ups; quantifies trade-offs (cost, latency, operational load); notes what they'd defer |

## Tutor critique protocol

1. Read the write-up once fully before scoring. Score each dimension independently using the rubric's words; quote
   the learner's own text as evidence for each score.
2. Check the arithmetic in the estimate sheet. An estimate that is off by > 10x on a number the design depends on
   (e.g. peak write QPS during a batch file, or connections needed to core) caps *Estimation* at "approaching".
3. Ask these follow-up probes (all of them; the learner answers in writing or live, without notes). Score the answers
   into the relevant dimensions:
   - "The payroll partner uploads 50k transfers at 09:00 on payday. Walk me through what happens to your QPS, the core
     adapter and the webhook dispatcher. Where does the backlog live?"
   - "A partner's `POST /transfers` times out after 10 s. Core banking actually posted it at 11 s. The partner retries
     with the same key 2 s later. Trace every component."
   - "Same scenario, but your API instance crashed between the core's success and writing the idempotency record.
     What does the retry do?"
   - "A partner authorised by members of credit union A sends a valid token and asks for
     `/v1/transfers/tr_from_cu_B`. Show me the line of code or query that stops it."
   - "You need to add an `on_hold` status for compliance review. Which partners break, and what should you have
     published on day one?"
   - "The partner's webhook endpoint returns 500 for 6 hours. How many delivery attempts do you make, and how does the
     partner catch up afterwards?"
   - "Why did you choose 409 (or wait-and-replay) for concurrent duplicates? When would you flip?"
   - One probe of the tutor's choice on the learner's weakest dimension.
4. Overall level:
   - **meets** requires "meets" or better on *Correctness under retries* and *Auth & tenancy* (the two dimensions where
     a mistake moves money or leaks data), and no dimension at "below".
   - **exceeds** requires "meets" on every dimension and "exceeds" on at least three.
   - Otherwise **approaching** (or **below** if two or more dimensions are "below").
5. Feedback: three bullets — the strongest part, the one change that would most raise the score, and one concrete
   thing to re-read (link the section, e.g. [`unit-3-idempotency-auth.md`](unit-3-idempotency-auth.md) §2). If the result
   is below "meets", the learner redoes the exercise on a different day (variation: "design the API gateway in front
   of your vendor adapters", same rubric).

## Recording

`sd record design A1 --self <below|approaching|meets|exceeds> --tutor <level> --notes "<one-line summary>"`
