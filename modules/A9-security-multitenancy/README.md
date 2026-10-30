# A9 — Security and multi-tenancy

| | |
|---|---|
| Track | A — HLD fundamentals |
| Estimated time | 12 h (2 weekly units) — CORE 10 h, STRETCH 2 h |
| Prerequisites | A1 (gate passed) |
| Labs | `labs/a9/` — `tenancy`, `auditlog` (core); `envelope` (stretch) |
| Gate | quiz ≥ 85% ×2 (different days) · labs pass · design ≥ meets · teach-back pass |

## Why this module exists

A digital-banking platform serves 40 credit unions from one codebase. In year one nothing goes wrong, because nothing
is stress-tested: every call is authenticated, every request carries a tenant, and everyone is on their best behaviour.
Then the small, ordinary failures arrive. A support-console search that "conveniently" spans all credit unions lets
a staff member at one export 1,900 documents belonging to another's members, and no alert fires because every call
was permitted. A statements endpoint with sequential ids and a forgotten ownership check leaks 38,000 statements in
three days. A marketing partner polling at 400 requests per second holds 80% of a shared connection pool, so the other
39 credit unions' p99 goes from 180 ms to 6 seconds while the global rate limit, set to 2,000, never trips.

And then the questions you can't answer: an examiner asks who approved a $48,000 wire and whether anyone has edited
the record since; a credit union asks you to delete all its data, backups included, in 30 days; a database password
that has not changed in 31 months turns out to be in an image layer in the registry. The platform has TLS and
encrypted disks, and none of it addresses the person who is actually present.

None of this needs exotic cryptography. It needs a threat model you can run in five minutes, authorisation that lives
where the data lives, tenant isolation chosen deliberately, fairness between tenants, keys arranged so that rotation and
deletion are cheap, and an audit trail that can prove its own integrity. These are the security questions a senior
system-design interview actually asks, and they are the daily substance of your credit-union work.

## Outcomes

- **Threat-model** a feature in five minutes (assets, actors, trust boundaries, STRIDE-lite, ranked abuse cases) and
  turn the top threats into testable requirements. — CORE
- **Decide** where tokens are validated and where authorisation decisions are made (gateway, service, data layer),
  **choose** between RBAC, ABAC and relationship-based models, and **state** the revocation trade-off. — CORE
- **Prevent** BOLA/IDOR and confused-deputy bugs structurally by authorising at the data layer, and **explain** 404
  versus 403. — CORE
- **Implement** a tenant-scoped data-access layer whose API shape makes cross-tenant access impossible
  (`labs/a9/tenancy`). — CORE
- **Choose** between silo, pool and bridge/cell isolation for tenants of different sizes and **enforce** the boundary
  with scoped repositories, row-level security and composite keys. — CORE
- **Implement** a per-tenant weighted-fair limiter with borrowing and a cap, size concurrency limits with Little's
  law, and **explain** why a global limit fails against a noisy neighbour (`labs/a9/tenancy`). — CORE
- **Design** secrets management with workload identity, short-lived credentials and zero-downtime rotation. — CORE
- **Explain** which attacker each encryption layer defeats, and where verification and identity checks are required.
  — CORE
- **Explain** envelope encryption: KEK/DEK hierarchy, AAD, DEK caching and KMS cost, rotation by re-wrapping, nonce
  limits, BYOK and crypto-shredding. — CORE
- **Implement** a tamper-evident hash-chained audit log, **state** which attacks it detects and which need an external
  anchor, and **design** the audit pipeline (`labs/a9/auditlog`). — CORE
- **Map** compliance frameworks onto controls and evidence, and design retention, deletion and just-in-time privileged
  access. — CORE
- **Implement** an envelope-encryption sketch with the standard library (`labs/a9/envelope`). — STRETCH
- **Explain** Zanzibar-style relationship-based access control (Unit 1 §6) and Merkle trees with transparency logs
  (Unit 2 §6). — STRETCH

## Unit plan

| Unit | Topic | Hours | Core/Stretch | Lab |
|---|---|---|---|---|
| 1 | [Who can do what, to whose data: threat modelling, authn/authz at scale, BOLA and confused deputy, tenant isolation, noisy neighbours](unit-1-authorization-tenancy.md) | 6 | CORE (5) / STRETCH (1) | `labs/a9/tenancy` |
| 2 | [Protecting data and proving what happened: secrets, encryption, envelope keys, audit trails, compliance](unit-2-data-protection-audit.md) | 6 | CORE (5) / STRETCH (1) | `labs/a9/auditlog`, `labs/a9/envelope` (stretch) |

**Unit 1 (6 h)**
- Session 1 (2 h): warm-up; §1 threat modelling (predict, then the five-minute exercise); §2 authn/authz decisions,
  PDP/PEP, RBAC vs ABAC vs ReBAC.
- Session 2 (2 h): §3 BOLA and the confused deputy; start `labs/a9/tenancy` (the `Store`: `Insert`, `Get`, `Update`,
  `Delete`, `Count`); §4 isolation models and row-level security.
- Session 3 (2 h): finish the `Store` (`List`), run the leak break-it; §5 noisy neighbours; implement the `Limiter`;
  run the noisy-neighbour break-it. STRETCH (1 h, any time): §6 Zanzibar-style relationships.

**Unit 2 (6 h, includes ≈ 1.5 h gate work)**
- Session 1 (2 h): §1 secrets (run the redaction program); §2 encryption in transit and at rest; §3 the key hierarchy
  (concept only on a first pass).
- Session 2 (2 h): §4 audit trails; implement `labs/a9/auditlog`; run the tamper matrix.
- Session 3 (1.5 h): §5 compliance and lifecycle; `sd lab A9`; first `sd quiz A9`.
- Session 4 (1.5 h, gate): design exercise (45 min + self-score), teach-back write-up. Second `sd quiz A9` on a later
  day.
- STRETCH (1 h): build `labs/a9/envelope`; §6 Merkle trees and transparency logs.

## How this connects to your banking work

- **Multi-tenant credit unions** are the running example in every section. The tenancy lab is the code version of
  the rule you already follow: the tenant comes from the authenticated principal, never from a parameter, and cross-
  tenant misses look like "not found". Unit 1 §4 gives the vocabulary (silo, pool, bridge/cells) for the placement
  conversations you have had when a large credit union asks for something special.
- **SSO adapters** (A1 Unit 3 §4): Unit 1 §2's "federated claims are input, not truth" is the security design behind per-
  tenant group mapping and metadata refresh, and the reason a credit union's IdP must never be able to mint a platform
  role.
- **Vendor adapters and secrets**: Unit 2 §1 is the secret lifecycle of your image-deposit vendor key and webhook
  secrets: dual-key rotation windows, no keys in logs or images, and tenant-specific vendor credentials.
- **Audit trails and compliance**: Unit 2 §4–§5 are the engineering behind "who did what on whose behalf" and the vendor
  due-diligence questionnaires credit unions send you. Per-tenant, anchored hash chains are an answer to "can we verify
  our own history?" that few vendors can give.
- **Money movement**: audit events in the same transaction as the transfer (outbox), fail-closed audit writes and
  step-up authentication for approvals extend A1's idempotency design.

## Mastery gate checklist

- [ ] `sd quiz A9` ≥ 85% — attempt 1 (date: ____)
- [ ] `sd quiz A9` ≥ 85% — attempt 2 on a different day (date: ____)
- [ ] `sd lab A9` — all lab tests pass (with `-race`)
- [ ] Design exercise ([`design-exercise.md`](design-exercise.md)): self-score recorded, tutor score ≥ "meets"
      (`sd record design A9 --self <lvl> --tutor <lvl> --notes "..."`)
- [ ] Teach-back ([`teachback.md`](teachback.md)): tutor marks pass (`sd record teachback A9 --result pass --notes "..."`)

`sd status` is the source of truth; this list is for your own reading. The `envelope` lab is STRETCH: `sd lab A9`
reports it separately and it does not block the gate.

## What's next

Passing A9 contributes to the prerequisites of C9 (Payments and ledger) and to the full senior loop (D4). Before moving
on, run `sd review` and clear any A9 questions it surfaces, especially tags `multitenancy`, `auth` and `audit`: tenant
scoping, per-tenant limits and audit trails reappear in almost every Track C case study (ask yourself "where is the
tenant boundary and what is audited?" in each one). Unit 1 §5 also pairs with A7 (reliability) for the algorithms
behind rate limiting and load shedding.
