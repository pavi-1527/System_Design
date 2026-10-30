# A1 — Networking, API design, DNS/LB/CDN

| | |
|---|---|
| Track | A — HLD fundamentals |
| Estimated time | 20 h (3 weekly units) — CORE 18 h, STRETCH 2 h |
| Prerequisites | A0 (gate passed) |
| Labs | `labs/a1/` — `lb`, `pagination`, `idempotency` (core); `token` (stretch) |
| Gate | quiz ≥ 85% ×2 (different days) · labs pass · design ≥ meets · teach-back pass |

## Why this module exists

A credit union's members open the mobile app on payday morning. Each tap goes through a DNS lookup, a TLS handshake,
a CDN edge, a load balancer, an API gateway, a transfers service, a connection pool, an adapter and finally the core
banking system. At 50 requests per second every one of those hops is invisible. At 5,000 they each fail in their own
way: DNS failover takes 15 minutes instead of 5 because clients are pinned to a degraded region; a vendor adapter
that builds a new HTTP transport per call runs the box out of file descriptors; round robin keeps sending a quarter of
the traffic to one sick server; a CDN rule caches `/api/accounts/summary` and shows one member another member's
balance.

Then the API itself. The transfers endpoint returns `200 {"success": false}` for errors, so the dashboards show 0%
errors during a 40-minute outage. Offset pagination shows members the same $1,200 payment twice while new
transactions post. And the classic: the core takes 11 seconds, the app gives up at 10, retries, and the member's rent
goes out twice — ≈ 500 times a day at 1 M transfers/day.

None of this is exotic. It is the everyday substance of senior system design interviews ("what happens when the client
retries?", "how does your pagination behave under concurrent inserts?", "why is DNS failover slow?") and of building
anything real.

## Outcomes

- **Estimate** the latency cost of the request path in round trips (TCP, TLS 1.2/1.3, QUIC, keep-alive vs new
  connections) and explain why DNS failover is slow. — CORE
- **Diagnose** connection-reuse bugs in Go HTTP clients and **size** connection pools with Little's law. — CORE
- **Choose** between L4 and L7 load balancing and between round robin, least-connections and power-of-two-choices for
  a workload, and **explain when the answer flips**. — CORE
- **Implement** a load balancer with pluggable pickers, passive outlier ejection with probation, and connection
  draining (`labs/a1/lb`). — CORE
- **Decide** what a CDN may cache in an authenticated banking app and how to deploy behind it safely. — CORE
- **Design** resource-oriented APIs with correct method semantics, status codes, an error model and evolution rules;
  **choose** between REST, gRPC and GraphQL for a given client population. — CORE
- **Implement** keyset pagination with HMAC-signed opaque cursors and **explain** why offset pagination duplicates and
  skips rows under concurrent writes (`labs/a1/pagination`). — CORE
- **Design** long-running operations and webhooks that survive at-least-once, unordered delivery (signing, dedup,
  outbox, backoff with jitter). — CORE
- **Implement** idempotency-key middleware that executes a handler exactly once under 50 concurrent duplicates, and
  **defend** its choices (409 vs wait, 5xx caching, leases, scope) (`labs/a1/idempotency`). — CORE
- **Choose** authentication and authorisation mechanisms (sessions vs JWT, OAuth code + PKCE, client credentials,
  OIDC, SAML, mTLS, API keys) and where tokens are validated. — CORE
- **Implement** a JWT-compatible HMAC token verifier with `kid` rotation, leeway and strict algorithm checks
  (`labs/a1/token`). — STRETCH

## Unit plan

| Unit | Topic | Hours | Core/Stretch | Lab |
|---|---|---|---|---|
| 1 | [The request path: DNS, connections, HTTP versions, pooling, load balancing, health, CDNs](unit-1-request-path.md) | 6 | CORE (5.5) / STRETCH (0.5) | `labs/a1/lb` |
| 2 | [API design: resources & errors, pagination, versioning, REST/gRPC/GraphQL, async & webhooks](unit-2-api-design.md) | 6 | CORE (5.5) / STRETCH (0.5) | `labs/a1/pagination` |
| 3 | [Idempotency and auth: retries, idempotency keys, tokens, OAuth/OIDC/SAML/mTLS](unit-3-idempotency-auth.md) | 8 | CORE (7, incl. ≈ 1.5 h gate work) / STRETCH (1) | `labs/a1/idempotency`, `labs/a1/token` (stretch) |

**Unit 1 (6 h)**
- Session 1 (2 h): warm-up; §1 DNS and §2 connection setup — predict boxes, run the connection-pinning and keep-alive
  experiments.
- Session 2 (2 h): §3 connection pooling (run the connection-count experiment); §4 load balancing; implement the three
  pickers in `labs/a1/lb`.
- Session 3 (2 h): §5 health checks and draining — implement `Pool` and `NewHandler`, run both break-it experiments;
  §6 CDNs and the cache-leak experiment. STRETCH (0.5 h, any time): QUIC internals in §2, the P2C math in §4.

**Unit 2 (6 h)**
- Session 1 (2 h): §1 resources, methods, status codes; §2 pagination reading and predict; start `labs/a1/pagination`
  (`Offset`, `After`).
- Session 2 (2 h): finish the pagination lab (cursor codec, `List`, handler); run the offset-vs-keyset break-it.
- Session 3 (1.5 h): §3 versioning (strict-decoder experiment), §4 API styles (N+1 experiment), §5 async operations and
  webhooks (write and test the signature verifier). STRETCH (0.5 h): GraphQL at scale in §4.

**Unit 3 (8 h)**
- Session 1 (2 h): §1 retries and duplicates (jitter experiment); §2 idempotency keys — predict and reveal.
- Session 2 (2 h): implement `labs/a1/idempotency`; run the `breakit` build to watch the duplicate-charge test fail.
- Session 3 (1.5 h): §3 tokens and §4 OAuth/OIDC/SAML/mTLS — predicts and the "find the flaw" break-it; `sd lab A1`;
  first `sd quiz A1` attempt.
- Session 4 (1.5 h, gate): design exercise (45 min + self-score), teach-back write-up.
- STRETCH (1 h): `labs/a1/token` and the SAML deep dive in §4.
- Second `sd quiz A1` attempt on a later day.

## How this connects to your banking work

- **Vendor adapters** (image/RDC vendors, SSO providers, core banking): the "new transport per call because of the mTLS
  client certificate" bug in Unit 1 §3, timeouts ordered across hops and retry only at one layer (Unit 3 §1), and
  bulkheads per vendor are adapter design decisions you've already made — now you'll know the numbers behind them
  (Little's law for pool size, ≈ 470 new connections/s per destination before TIME_WAIT bites).
- **Duplicate vendor callbacks**: the webhook receiver checklist in Unit 2 §5 (signature over the raw body, timestamp
  tolerance, dedup by event ID, out-of-order handling) is the generalisation of what you did when a vendor posted
  "deposit accepted" twice.
- **Money movement**: Unit 3 §2 is the design behind every "was I charged twice?" ticket. Idempotency keys scoped per
  (tenant, partner), atomic reservation, and passing a key to the core end to end — the interview version of a problem
  you've seen in production.
- **SSO adapters**: Unit 3 §4's SAML validation list (audience, recipient, `InResponseTo`, signature over the exact
  assertion, metadata refresh for certificate rotation across 40 IdPs) and the OIDC ID-token-vs-access-token
  distinction are your SSO work, framed for an interview.
- **Multi-tenant credit unions**: tenant scoping shows up everywhere — 404 for another tenant's object (BOLA), cursors
  that are signed but never the security boundary, idempotency keys scoped per tenant, partner tokens bound to the
  credit unions they may act for, audit trails of "who did what on whose behalf".

## Mastery gate checklist

- [ ] `sd quiz A1` ≥ 85% — attempt 1 (date: ____)
- [ ] `sd quiz A1` ≥ 85% — attempt 2 on a different day (date: ____)
- [ ] `sd lab A1` — all lab tests pass (with `-race`)
- [ ] Design exercise ([`design-exercise.md`](design-exercise.md)): self-score recorded, tutor score ≥ "meets"
      (`sd record design A1 --self <lvl> --tutor <lvl> --notes "..."`)
- [ ] Teach-back ([`teachback.md`](teachback.md)): tutor marks pass (`sd record teachback A1 --result pass --notes "..."`)

`sd status` is the source of truth; this list is for your own reading. The `token` lab is STRETCH: `sd lab A1` reports
it separately and it does not block the gate.

## What's next

Passing A1 unlocks the next Track A modules (see [`SYLLABUS.md`](../../SYLLABUS.md) and `sd status`). Two threads
continue directly: caching (this module's CDN section is the edge case of a general caching problem — TTLs vs
invalidation, stampedes, hit-ratio math) and partitioning (consistent hashing, teased in Unit 1 §4). Before moving on,
run `sd review` and clear any A1 questions it surfaces, especially tags `idempotency`, `pagination` and
`load-balancing`; they come back in almost every later design.
