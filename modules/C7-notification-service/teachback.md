# C7 Teach-back

## Prompt

A mid-level engineer on your team has been asked to replace the in-process `notify()` helper with a shared
notification service. Their plan: "an endpoint that writes a row with a `sent` boolean and pushes the message onto
a queue; a pool of workers that call the SMS, push and email vendors; on any error, retry three times; marketing
can use the same endpoint for campaigns." They have never been on call for a vendor outage.

Explain to them, in your own words, **how a notification service has to be built, and why**: what makes this problem
hard (hint: it isn't your servers), how messages differ and why that decides almost everything, what a timeout at
the vendor means and what to do about it per kind of message, how the service survives a slow or dead vendor, the
different meanings of "don't send this twice" and where each is enforced, why preferences are checked at send time,
how an OTP stays fast while a campaign runs, and what you record so that "did we tell this member, and when?" has an
answer.

Form: written, 500–900 words, one diagram (Mermaid or a sketch) allowed. Use numbers: pick a scale (theirs or the
module's) and show two or three calculations. No copying from the unit files; if a sentence from the course
appears verbatim, the evaluator will ask you to rephrase it. Write for that engineer, so they can go and change
their design, not for an examiner.

## Evaluator checklist

Must-hit (all required for pass):
- [ ] Identifies the **vendor as the bottleneck**: a contracted rate cap and a latency your pool's throughput
      depends on (Little's law, with numbers), so adding workers past the cap produces 429s, not throughput.
- [ ] Introduces **categories with a policy each** (lane, latency budget, TTL, whether opt-out and quiet hours apply,
      whether a duplicate or a miss is worse), and explains why a priority flag or field is not enough.
- [ ] Treats a **vendor timeout as an unknown outcome** and gives a **per-category** retry rule: retry an OTP on
      another provider (with the same code), never retry marketing across a timeout, resolve transactional alerts
      by lookup or receipts where possible.
- [ ] Describes surviving a bad vendor: **timeouts sized from the budget**, a **circuit breaker per provider**
      (counting provider failures, not message rejections), **failover**, **bounded retries with backoff and
      jitter**, and **expiry** so stale messages are dropped rather than delivered late.
- [ ] Distinguishes at least three of the four keys with their scope and where they're enforced: **idempotency key**
      (one request, at the API), **dedupe key** (one business event, at the API, atomically), **collapse key**
      (newest wins, at send time), **frequency cap** (per member per period, at send time); and that keys come from
      the business fact, not from the request.
- [ ] **Preferences and quiet hours are evaluated at send time** (because they change while messages wait), with a
      failure policy that differs between service messages and marketing.
- [ ] **Lane isolation** that keeps an OTP ahead of a campaign: separate topics and worker pools *and* a reserved
      share of each provider's capacity, with campaigns throttled.
- [ ] An **append-only attempt log** (sends, non-sends with reasons, receipts) instead of a `sent` boolean, and the
      difference between **accepted** and **delivered**.

Should-hit (at least 5 of 9):
- [ ] The API returns `202` with an id and never calls a vendor in the request path; callers integrate via an outbox
      with the event id as the idempotency key.
- [ ] Provider receipts arrive by webhook and are correlated by the provider's message id, with a plan for receipts
      that arrive before the attempt row and for vendors that stop sending receipts.
- [ ] A dedupe hit returns success with the existing id, and the dedupe reservation is released or completed when
      the enqueue that follows fails.
- [ ] Frequency caps as a token bucket (or sliding log) with the burst trade-off, held per user partition.
- [ ] Recovery after an outage drains at the vendor's rate (a limiter below the cap, a retry budget, `Retry-After`).
- [ ] Tenants: per-tenant quotas and fair shares, tenant id from the principal, breakers per tenant × provider when
      tenants bring their own accounts.
- [ ] Cost as a design input: SMS price × volume, the dollar value of a duplicate rate, push-first policies.
- [ ] Templates by reference with typed params, and no rendered bodies, params or OTPs in logs.
- [ ] Metrics that catch failure before users do: queue age per lane, delivered rate and receipt lag per provider,
      breaker state, non-send outcomes by reason.

Red flags (any one = revise):
- [ ] Claims exactly-once delivery, or that a vendor's 2xx means the user received the message.
- [ ] Keeps a single queue (with or without a priority field) for OTPs and campaigns.
- [ ] Retries every error the same way regardless of category, or retries immediately with no backoff or bound.
- [ ] Checks preferences only when the notification is created, or treats opt-out as optional for marketing.
- [ ] Calls the vendor synchronously inside the caller's request, or overwrites a status column as the only record.
- [ ] "Deduplicate" with no key named, or a key generated per request (a UUID per call).
- [ ] Numbers stated as facts with no scale assumption, or a ≥ 8× unit error (per day vs per second, messages vs
      segments) that isn't caught.

## Recording

`sd record teachback C7 --result <pass|revise> --notes "<missed items>"`
