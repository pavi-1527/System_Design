# A9 Design exercise — Member Document Vault

> Time box: 45 minutes, solo, no notes. Then self-score against the rubric (≈ 15 min), then send your write-up to
> the tutor. Use the structure from [`docs/INTERVIEW_FRAMEWORK.md`](../../docs/INTERVIEW_FRAMEWORK.md) and the
> numbers from [`docs/NUMBERS.md`](../../docs/NUMBERS.md). This exercise is about the security-critical parts: don't
> spend your 45 minutes on upload chunking or PDF rendering.

## Prompt

> "We run a digital-banking platform for about 40 credit unions, roughly 3 million members in total. We're adding a
> **Member Document Vault**. Members upload ID scans and signed loan documents from the mobile app. Credit-union staff
> view them from an admin portal, and support engineers need to look at a member's documents to troubleshoot. A few
> loan-origination partners should be able to fetch a document with the member's consent. Design it, and be
> specific about how you keep one credit union's data away from the others and how you'd satisfy an examiner."

That is all the interviewer says up front. Ask the clarifying questions you need (write them down, then write the
answers you'd assume if the interviewer said "you decide"), and drive.

Things an interviewer would answer if asked (don't read until you've written your questions):

<details data-kind="hint">
<summary>Interviewer's answers to common clarifying questions</summary>

- ≈ 20,000 uploads/day on average, ≈ 5× at peak (month end, loan campaigns); average file ≈ 1.5 MB, maximum 25 MB.
  Reads ≈ 10× writes. One credit union (≈ 12% of members) does bulk loan imports of up to 5,000 documents at a time.
- Retention: loan documents 7 years after the loan closes; KYC documents 5 years after the account closes. After that
  they must be deleted. Credit unions may also request deletion of a member's non-regulated documents.
- One large credit union contractually requires dedicated infrastructure and holds its own encryption key in its own
  cloud KMS; another has asked for the same "next year".
- Staff roles differ per credit union (teller, loan officer, branch manager, compliance); a person works for exactly
  one credit union. Support engineers belong to the platform company.
- ≈ 25 partners at launch. A partner may act only for members who have granted consent, and only for the credit unions
  it is contracted with.
- Examiners and credit unions expect a record of every view, download, upload, deletion and permission change, with who,
  on whose behalf, and when, retained for 7 years, and want to be able to check that it hasn't been altered.
- Documents are scanned for malware and OCR'd by asynchronous workers.
- The company's other products already authenticate members (OIDC), staff (SAML/OIDC through each credit union's IdP)
  and partners (OAuth client credentials).

</details>

## What to produce

1. **Requirements**: functional bullets and *security* NFRs with numbers (isolation, audit completeness, revocation
   latency, retention, availability), your clarifying questions and assumptions.
2. **Estimate sheet**: uploads and reads per second (average/peak), storage growth per year and over retention, audit
   events per day and 7-year audit volume, key/KMS call rates with and without DEK caching, and one capacity number that
   drives a limit (for example upload concurrency per tenant using Little's law).
3. **Threat model**: assets, actors (include insiders and partners), a diagram with trust boundaries (mark the tenant
   boundary), STRIDE-lite on the boundary crossings, and **at least six abuse cases ranked by blast radius**, each
   with the mitigation and where it lives.
4. **Authentication and authorisation**: where tokens are validated, what identity services see (internal token?
   mTLS?), the policy model for members, staff (role per tenant), support (just-in-time) and partners (consent), where
   the PEPs are, and the **object-level authorisation at the data layer** that makes a forgotten check structurally
   impossible. State your 404/403 position.
5. **Tenant isolation**: model per tenant tier (silo, pool, bridge/cells), what enforces the boundary (scoped
   repository, row-level security, composite keys, per-tenant credentials), how the tenant context reaches the
   asynchronous scan/OCR workers and the object store, and how you move a tenant between tiers.
6. **Noisy neighbours**: the limits for uploads and bulk imports (rate, concurrency, size), guarantees versus caps, what
   is rejected versus queued, and the response clients see.
7. **Data protection**: classification, encryption in transit and at rest, the key hierarchy (root, KEK, DEK; per
   tenant or per object), AAD, DEK caching, rotation, BYOK for the large credit union, crypto-shredding at retention end,
   and how secrets (partner credentials, tenant IdP certificates) are stored and rotated.
8. **Audit**: event schema (who/what/when/where/outcome/why), what is logged and what is not, the pipeline
   (same-transaction outbox? fail-closed?), tamper-evidence design and **what it cannot detect without an anchor**,
   who verifies and how often, per-tenant exports, PII handling and retention.
9. **Compliance evidence**: a table of five controls (for example access reviews, JIT support access, encryption, log
   integrity, deletion) with the evidence each produces.
10. **Trade-off table**: at least three decisions with "wins when / loses when / when the answer flips".
11. **Failure and abuse walkthroughs**: a staff member at credit union A tries to open credit union B's document;
    the KMS is unavailable for 10 minutes; the audit store is down; a partner's token leaks.

## Rubric

| Dimension | below | approaching | meets (senior bar) | exceeds |
|---|---|---|---|---|
| Requirements & security NFRs | Starts drawing immediately; "secure" with no numbers | Features listed; NFRs vague ("encrypted", "audited") | Clarifies tenant tiers, retention, consent, BYOK; testable security NFRs with numbers (zero cross-tenant reads, 100% of views audited, revocation ≤ 15 min, deletion ≤ 30 days) | Spots the support-console and bulk-import patterns as design drivers early; scopes the 45 minutes explicitly |
| Estimation | None, or off by orders of magnitude | Average upload rate only | Peak and average rates, storage over retention, audit volume, KMS call rates with/without DEK cache, per-tenant concurrency from Little's law; arithmetic shown | Uses the numbers to decide (DEK per tenant, outbox sizing, concurrency caps, anchor cadence) |
| Threat model | Lists controls, no threats; only external attackers | Some threats; no ranking; tenant boundary not drawn | Assets, actors incl. insiders and partners, trust boundaries with the tenant boundary, STRIDE-lite, ≥ 6 abuse cases ranked by blast radius with mitigations | Assumes breach: blast radius per compromised component, tests/alerts for each top threat |
| Authn/authz and object-level access | "Use OAuth/RBAC" with no detail; gateway-only checks | Tokens and roles named; object checks in handlers | Token validation at the edge plus an internal signed identity or mTLS; role-per-tenant RBAC with ABAC conditions; consent for partners; JIT support; **data-layer scoping from context**; 404 for out-of-scope objects; fail-closed policy | Confused deputy in workers and presigned URLs handled; step-up for sensitive actions; decision caching with tenant in the key; shadow-mode policy rollout |
| Tenant isolation and context | Tenant read from a request parameter; one flat table | `tenant_id` column and "filter by it" | Model per tier with reasons; scoped repository + row-level security (per-transaction variable) + composite keys; tenant context in async messages validated; object-store prefixes and scoped credentials; tenants movable between tiers | Cell/placement design, migration canaries, a continuous cross-tenant prober, authz matrix generated from the route table |
| Noisy neighbours and fairness | None | Global rate limit only | Per-tenant rate and concurrency limits sized from numbers; guarantee/cap/borrowing idea; separate bulk lane; 429 + `Retry-After`; per-tenant observability | Weights tied to contract tiers, fair queues for async work, shuffle-sharded workers, per-tenant overrides with expiry |
| Data protection and keys | "Encrypted at rest and TLS" | Volume encryption and TLS; one key | TLS/mTLS; field/object encryption with envelope hierarchy; per-tenant DEKs; AAD; DEK cache with limits; rotation by re-wrap; BYOK handling and its availability coupling; crypto-shredding with key-table backup retention; secrets via workload identity | Nonce/usage limits, KMS quota and cost analysis, key-policy separation of duties, rehearsed rotation and shred runbooks |
| Audit and tamper-evidence | Application logs | Audit table with some fields | Complete schema; same-transaction outbox; append-only/WORM sink in a separate account; hash chain per tenant with sequence numbers; **states that truncation/rewrite need an external anchor** and names one; scheduled verification; PII kept out; retention | Per-tenant anchors held by the credit union, Merkle batches or inclusion proofs where useful, audit-of-auditors, fail-closed policy per event class |
| Compliance and operations | Not addressed | Mentions SOC 2/PCI by name | Control-to-evidence table; JIT and break-glass; access reviews; environment separation; retention and deletion schedule; incident and key-compromise runbook | Shared-responsibility matrix with tenants, policy-as-code evidence, scope reduction (tokenisation) argued |
| Trade-offs and communication | One answer per decision | Trade-offs only when prompted | Trade-off table with "when the answer flips"; drives the 45 minutes; checks in with the interviewer | Anticipates follow-ups; quantifies cost/latency/ops trade-offs; says what they'd defer and why |

## Tutor critique protocol

1. Read the write-up once fully before scoring. Score each dimension independently using the rubric's words; quote the
   learner's own text as evidence for each score.
2. Check the arithmetic in the estimate sheet. An estimate that is off by > 10× on a number the design depends on (for
   example the KMS call rate without a DEK cache, or audit volume) caps *Estimation* at "approaching".
3. Ask these follow-up probes (at least three, ideally all; the learner answers in writing or live, without notes).
   Score the answers into the relevant dimensions:
   - "A staff member at credit union A pastes the URL of a document that belongs to credit union B into their browser,
     with a valid session. Walk me through every layer that stops them and what they see."
   - "The OCR worker reads a job message containing `tenant_id` and a document key. How does it know the message is
     honest, and what could a bug in the producer expose?"
   - "You said per-tenant DEKs. The KMS key policy says `kms:Decrypt` for the whole platform account. What did you
     actually achieve, and what would you change?"
   - "The audit store operator deletes the last two hours of events. How do you find out? What if they also rewrite
     the anchors you published?"
   - "A tenant leaves. Show me, step by step, how their documents become unreadable in backups within 30 days, and what
     conflicts with the 7-year retention rule."
   - "The big credit union's partner imports 5,000 documents in 60 seconds on month end. What happens to everyone else?"
   - "A support engineer needs to see a member's document. What exactly do they get, for how long, and what's recorded?"
   - "How would you prove, continuously, that no endpoint leaks across tenants?"
   - One probe of the tutor's choice on the learner's weakest dimension.
4. Overall level:
   - **meets** requires "meets" or better on *Threat model*, *Authn/authz and object-level access* and *Tenant isolation
     and context* (the dimensions where a mistake leaks data across tenants), and no dimension at "below", and at most
     two dimensions at "approaching".
   - **exceeds** requires "meets" on every dimension and "exceeds" on at least three.
   - Otherwise **approaching** (or **below** if two or more dimensions are "below").
5. Feedback: three bullets — the strongest part, the one change that would most raise the score, and one concrete
   thing to re-read (link the section, for example [`unit-1-authorization-tenancy.md`](unit-1-authorization-tenancy.md)
   §3 or [`unit-2-data-protection-audit.md`](unit-2-data-protection-audit.md) §4). If the result is below "meets", the
   learner redoes the exercise on a different day (variation: "design the partner consent and data-sharing service
   for the platform", same rubric).

## Recording

`sd record design A9 --self <below|approaching|meets|exceeds> --tutor <level> --notes "<one-line summary>"`
