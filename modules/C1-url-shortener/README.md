# C1 — Case study: URL shortener

| | |
|---|---|
| Track | C — HLD case studies |
| Estimated time | 6 h (1 weekly unit) — CORE 5 h, STRETCH 1 h |
| Prerequisites | A1, A2, A3 (gates passed) |
| Labs | `labs/c1/` — `keygen` (unique short codes: base 62, keyed permutation, counter ranges with compare-and-swap, random codes with insert-if-absent) |
| Gate | quiz ≥ 85% ×2 (different days) · labs pass · design ≥ meets · teach-back pass |

## Why this module exists

The URL shortener is the interview problem everyone has read about, which is exactly why it separates candidates.
A mid-level answer is a table and two endpoints. Then the interviewer asks: two servers create links at the same
moment, how do they avoid the same code? A celebrity posts one link and it gets 50,000 clicks a second: what
melts first? A phishing campaign is using your short domain: how long until every cached copy of that redirect is
dead, and how do you know? Click counts by day and country for six billion links: where do they live, and why
isn't it the link row? Each question is a system of its own, and none of them is visible from the two endpoints.

The numbers say it first: ≈ 120 creates/s against ≈ 12k redirects/s, ≈ 9–11 TB of links over five years, one key
that can take half a cache node by itself, and an analytics store that ends up larger than the links. That makes
the shortener a read-heavy key-value system with three genuinely hard parts: unique identifiers without a
serialising point, a redirect path that must be fast and almost never down, and an event pipeline and abuse
process that must never touch the hot path. This module runs the 45-minute framework on it end to end, has you
commit to each decision before the reveal, and makes you build the one component that interviewers probe hardest
and candidates hand-wave most: the key generator.

## Outcomes

- **Scope** a shortener prompt in ≤ 6 minutes: core operations, NFRs with numbers, out-of-scope list, and the four
  clarifying questions (guessability, analytics, aliases, takedown SLA) whose answers change the architecture. — CORE
- **Estimate** peak creates, peak redirects, storage with replication, the base-62 code length and its headroom,
  cache memory for the hot set and egress, and **state** which decision each number drives. — CORE
- **Design** the API (idempotent create, a browser-followable 302 redirect with explicit caching semantics, 404 vs
  410) and a data model whose partition key follows the dominant access pattern, and **defend** the store choice
  with a flip condition. — CORE
- **Draw** a minimal, justified high-level design with both paths traced and rates on the arrows, and **say** what
  each box does when it fails. — CORE
- **Choose** an ID scheme (random codes with insert-if-absent vs counter ranges with a keyed permutation vs a
  global counter vs URL hashing vs a key pool) using collision arithmetic, coordination cost, crash behaviour and
  guessability. — CORE
- **Implement** base-62 codes, a bijective mixer, compare-and-swap range allocation with a per-node generator,
  and random codes with atomic insert-if-absent, and **show** with a break-it what a non-atomic counter does
  (`labs/c1/keygen`). — CORE
- **Design** the redirect hot path: in-process L1 with request coalescing for a viral key, negative caching
  against enumeration, lazy expiry, a measurable takedown fan-out, and fail-open behaviour on a store outage. — CORE
- **Design** the click-analytics pipeline (async events, durable log, stream aggregates, nightly exact recompute,
  HyperLogLog for uniques) and the abuse controls (reputation scanning off the create path, open-redirect
  validation, rate limits, takedown), and **explain** what breaks at 10× and 100×. — CORE
- **Estimate** the monthly cost and name the lines that scale with clicks rather than links, and **sketch** the
  multi-region evolution. — STRETCH

## Unit plan

| Unit | Topic | Hours | Core/Stretch | Lab |
|---|---|---|---|---|
| 1 | [Guided design: requirements → estimation → API and model → HLD → three deep dives](unit-1-url-shortener.md) | 6 | CORE (5, incl. ≈ 1.5 h gate work) / STRETCH (1) | `labs/c1/keygen` |

**Unit 1 (3 × 2 h)**
- Session 1 (2 h): warm-up; §1 requirements (write the card before the reveal), §2 estimation (compute the five
  numbers), §3 API and data model, §4 the HLD on paper with both paths traced.
- Session 2 (2 h): §5 unique codes: predict, then `labs/c1/keygen` in the README's order (codes → mixer → counter
  and allocator → generator → random codes), then the three break-it runs.
- Session 3 (2 h): §6 the redirect hot path and §7 the analytics and abuse pipeline (≈ 50 min), then the gate work:
  first `sd quiz C1`, the [design exercise](design-exercise.md) (45 min, solo), the [teach-back](teachback.md).
- STRETCH (1 h, any time): the second half of §7 (approximate counting, the cost model, multi-region evolution).
- Second `sd quiz C1` attempt on a later day.

## How this connects to your banking work

- **Transactional links are personal.** Statement-ready notices, device-verification links and application
  resumption links point at member data, so codes must be unguessable, links must expire (minutes for a
  verification, weeks for a statement) and every link must be traceable to the message, template and tenant that
  created it. §1 and §5 are where those requirements become design decisions; the design exercise is this exact
  service.
- **Phishing against your brand.** A short domain is an open redirector by construction. The takedown fan-out in §6
  (store → cache → every server → edge, with a completion metric) is the control the compliance team will ask you
  to demonstrate, and the reputation scan in §7 is the one you keep off the create path's latency budget, the
  same way you keep a slow vendor off a login.
- **Idempotent creates from retrying pipelines.** Notification senders retry on timeouts. A1's idempotency key on
  `POST /v1/links` is what stops a retried send from minting a second link, which is the same instinct as a retried
  transfer not posting twice.
- **Multi-tenant domains and quotas.** Forty credit unions with branded short domains share one service: tenant in
  the key, per-tenant rate limits on create, and per-tenant takedown and reporting. A9 deepens this; the design
  exercise asks for it now.

## Mastery gate checklist

- [ ] `sd quiz C1` ≥ 85% — attempt 1 (date: ____)
- [ ] `sd quiz C1` ≥ 85% — attempt 2 on a different day (date: ____)
- [ ] `sd lab C1` — all lab tests pass (with `-race`)
- [ ] Design exercise ([`design-exercise.md`](design-exercise.md)): self-score recorded, tutor score ≥ "meets"
      (`sd record design C1 --self <lvl> --tutor <lvl> --notes "..."`)
- [ ] Teach-back ([`teachback.md`](teachback.md)): tutor marks pass (`sd record teachback C1 --result pass --notes "..."`)

`sd status` is the source of truth; this list is for your own reading.

## What's next

C1 is a prerequisite of the HLD mock kit (D3) and of E1, where you build this service for real: an HTTP API with a
file-backed store, a cache with invalidation, a rate limiter and a load test with a p99 report. The next case
studies in the recommended order are C3 (unique ID generator, which takes §5's counter and removes it entirely)
and C4 (distributed key-value store, which is the link store's internals). Before moving on, run `sd review` and
clear any C1 questions it surfaces, especially the tags `id-generation`, `caching`, `hot-keys` and `estimation`.
