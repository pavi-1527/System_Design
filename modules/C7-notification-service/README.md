# C7 — Case study: notification service

| | |
|---|---|
| Track | C — HLD case studies |
| Estimated time | 6 h (2 units) — CORE 5 h, STRETCH 1 h |
| Prerequisites | A6, A7 (gates passed) |
| Labs | `labs/c7/` — `dispatch` (dedupe, priority lanes, preferences and frequency caps, provider failover with breakers, bounded retries, dead letters) |
| Gate | quiz ≥ 85% ×2 (different days) · labs pass · design ≥ meets · teach-back pass |

## Why this module exists

A digital-banking platform starts with a helper: `notify(memberID, channel, text)`, called inline by the transfers
service, one HTTP call to one SMS vendor. At a few thousand members it is invisible. Three years later fourteen
services call it and it carries ≈ 20 M texts a day. On a Monday at 09:00 marketing enqueues a 2.1 M-member campaign
in twenty minutes, ahead of every one-time passcode; the vendor, contracted for 100 messages a second, starts
returning 429, which the helper retries five times, immediately. OTP wait goes from 4 s to 11 minutes and the contact
centre takes 9,000 calls before noon. Some members get the promotion three times, because the vendor had accepted it
before the 2 s client timeout fired. A member who opted out of marketing texts gets it anyway, because their opt-out
lived in the marketing tool. The invoice is 2.4× budget.

Nothing in that story needs more servers. The message leaves through a vendor you don't control, which has a rate
cap, a latency distribution and outages of its own; a timeout at that boundary is an unknown outcome, so every retry
is a choice between a duplicate and a miss, and every duplicate has a price. Most of the service's value is in *not*
sending: not twice, not after "stop", not at 3 a.m., not the ninth low-balance push in an hour, not a passcode that
arrives after it expired, and never a campaign ahead of an OTP. And when compliance asks "did we tell member 4471
about that transfer, and when?", the answer must be one query, not a two-day grep.

Interviewers like this problem because candidates relax ("a queue and some workers") and the senior signal is
specific: lanes and reserved vendor capacity, per-category retry policy at the vendor boundary, four different kinds
of "don't send this twice", preferences evaluated at send time, and an append-only record of every attempt and every
decision not to attempt.

## Outcomes

- **Scope** a notification service in six minutes: the clarifying questions whose answers change the design
  (categories, channels, triggers, preferences, delivery semantics, tenancy, retention) and non-functional requirements
  with numbers. — CORE
- **Estimate** notification volume (average, peak, a campaign spike), provider throughput limits, queue backlog and
  drain time with Little's law, and delivery-log storage with retention and replication, and tie each number to a
  decision. — CORE
- **Design** the API (idempotent create, dedupe and collapse keys, TTL, status, campaigns, preferences, provider
  receipt webhooks) and a data model whose partition keys follow the access patterns, with an append-only attempt
  log. — CORE
- **Draw** the high-level design with the urgent and campaign paths and the receipt path traced, and justify every box
  by a requirement or a number. — CORE
- **Design** provider failover: per-provider circuit breakers, timeouts as unknown outcomes, bounded retries with
  backoff and jitter, retry budgets, and a per-category decision on whether a duplicate or a miss is worse. — CORE
- **Distinguish** idempotency keys, dedupe keys, collapse keys and frequency caps; evaluate preferences and quiet hours
  at send time; isolate priority lanes and tenants so an OTP is never behind a campaign. — CORE
- **Implement** the dispatcher (`labs/c7/dispatch`): dedupe under concurrent duplicates, priority lanes, opt-out, quiet
  hours, a token-bucket frequency cap, provider failover with breakers, bounded retries and a dead-letter list,
  passing under `-race`. — CORE
- **Explain** what "delivered" means per channel, how provider receipts and webhooks update the delivery log, and which
  metrics and alerts tell you the service is failing before users do. — CORE
- **Design** templates, localisation and open/click tracking without leaking PII into logs, and reason about what
  tracking numbers actually measure. — STRETCH

## Unit plan

| Unit | Topic | Hours | Core/Stretch | Lab |
|---|---|---|---|---|
| 1 | [Guided design: requirements, estimation, API and data model, high-level design](unit-1-guided-design.md) | 3 | CORE (2.5) / STRETCH (0.5) | — (conceptual; the estimate sheet and the diagram are the work) |
| 2 | [Deep dives: provider failover and retries; dedupe, preferences, caps and lanes; common mistakes](unit-2-deep-dives.md) | 3 | CORE (2.5, incl. ≈ 1 h gate work) / STRETCH (0.5) | `labs/c7/dispatch` |

Total: 6 h, CORE 5 h, STRETCH 1 h. This is a compact module: it leans on A6 (queues, outbox, DLQs, backpressure) and
A7 (timeouts, retries, breakers, rate limits, bulkheads) and spends its time applying them at a vendor boundary.

**Unit 1 (3 h)**
- Session 1 (1.5 h, CORE): warm-up; §1 requirements (write your eight questions and the category table before the
  reveal); §2 the estimate sheet by hand before the worked version, then `sd estimate -kind qps -n 3` and
  `sd estimate -kind concurrency -n 3`.
- Session 2 (1 h CORE + 0.5 h STRETCH): §3 API and data model (commit to the keys and partition keys first); §4 the
  high-level design with the urgent, campaign and receipt paths traced and the three break-it traces. STRETCH: the
  campaign arithmetic at the end of §2 and the in-app inbox in §3.

**Unit 2 (3 h)**
- Session 3 (1.5 h, CORE): warm-up; §5 provider failover, timeouts and retries; §6 deciding not to send; lab parts 1–2
  (`New`, `Enqueue`, `Process`, the token bucket, the suppression rules) then part 3 (`call`, the breaker, the provider
  loop); `sd lab C7`.
- Session 4 (1 h, CORE incl. gate work): break-it exercises 1–4; Common mistakes; first `sd quiz C7` attempt; the
  [design exercise](design-exercise.md) (45 min, solo, timed) and the [teach-back](teachback.md) write-up, in the same
  sitting or the next day. Second `sd quiz C7` attempt on a later day.
- STRETCH (0.5 h): the templates, localisation and tracking part of §6; break-it 5 (a breaker per channel).

## How this connects to your banking work

- **You have written this adapter.** A provider adapter with a timeout, a breaker, failover to a secondary and a
  reconciliation job for unknown outcomes is the shape of every core-banking or card-processor integration you've
  built; Unit 2 §5 is that adapter with SMS and push vendors behind it, and the A7 capstone
  ([A7 Unit 3 §5](../A7-reliability/unit-3-isolation-degradation-dr.md#5-capstone-the-resilient-vendor-adapter)) is
  its general form.
- **OTP and SMS are banking's urgent lane.** Login passcodes, step-up authentication and fraud holds have a budget of
  seconds and must never queue behind a statement-ready blast; the "duplicate is harmless, miss locks the member out"
  rule for OTPs, and the opposite rule for marketing, is a per-category decision you'll recognise from payments
  (a duplicate debit versus a missed one).
- **Idempotency across a vendor boundary.** The transfers service's outbox relay with the event id as the
  `Idempotency-Key` (Unit 1 §4, [A6 Unit 2 §1](../A6-async/unit-2-outbox-consumers.md#1-the-dual-write-problem-and-the-transactional-outbox))
  is the same pattern as a payment initiation that must not be submitted twice to a processor; the dedupe key is the
  business-level version you'd key on a transfer id.
- **The audit trail is the dispute evidence.** "We alerted the member at 14:03 and the carrier confirmed delivery at
  14:03:08" is what answers a disputed card transaction; the append-only attempt log, with non-sends recorded and
  their reasons, is the alert-side equivalent of a ledger
  ([A9 Unit 2 §4](../A9-security-multitenancy/unit-2-data-protection-audit.md#4-audit-trails-and-tamper-evidence)).
- **Credit unions are tenants.** Per-tenant sender ids, provider accounts, quotas and fair shares (Unit 2 §6,
  [A9 Unit 1 §5](../A9-security-multitenancy/unit-1-authorization-tenancy.md#5-noisy-neighbours-and-fair-sharing))
  are what stops one credit union's campaign from delaying another's fraud alerts.

## Mastery gate checklist

- [ ] `sd quiz C7` ≥ 85% — attempt 1 (date: ____)
- [ ] `sd quiz C7` ≥ 85% — attempt 2 on a different day (date: ____)
- [ ] `sd lab C7` — all lab tests pass (with `-race`)
- [ ] Design exercise ([`design-exercise.md`](design-exercise.md)): self-score recorded, tutor score ≥ "meets"
      (`sd record design C7 --self <lvl> --tutor <lvl> --notes "..."`)
- [ ] Teach-back ([`teachback.md`](teachback.md)): tutor marks pass (`sd record teachback C7 --result pass --notes "..."`)

`sd status` is the source of truth; this list is for your own reading.

## What's next

C7 is one of the core case studies that D4 (the full senior loop) requires. Expect "design a notification service"
or a variant (an alerting system, an OTP service, a campaign tool, a webhook delivery platform) in the D3 and D4
mocks, where the vendor boundary and the "don't send" logic are the deep dives an interviewer picks. C9 (payments and
ledger) reuses the same idempotency-across-a-boundary and reconciliation reasoning with money instead of messages,
and E3 (the job pipeline) builds the retry, backoff and dead-letter machinery as a real service. Before moving on,
run `sd review` and clear any C7 questions it surfaces, especially tags `idempotency`, `retries`, `circuit-breaker`
and `rate-limiting`; and re-read both units' wrap-up tables the day before your next HLD mock.
