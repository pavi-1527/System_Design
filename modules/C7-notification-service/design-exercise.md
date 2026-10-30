# C7 Design exercise — Order and courier notifications for a delivery marketplace

> Time box: 45 minutes, solo, no notes, timed like a real interview (numbers by 0:10, end-to-end diagram by 0:25,
> stop drilling at 0:40). Then self-score against the rubric (≈ 10 min), then send the write-up to the tutor for the
> probe session. Use the structure from [`docs/INTERVIEW_FRAMEWORK.md`](../../docs/INTERVIEW_FRAMEWORK.md) and the
> numbers from [`docs/NUMBERS.md`](../../docs/NUMBERS.md) — from memory.

This is a *variant* of the module's case study on purpose. The mechanisms (categories with a policy each, lanes,
the vendor boundary, the four keys, send-time preferences, the attempt log) all apply, but the constraints are
different: most traffic is transactional and short-lived, the recipients include businesses and workers who must
act within seconds or minutes, state changes arrive out of order, and "delivered" for the most important message has
to mean "a human acknowledged it". If you reproduce Unit 1's design box for box you will score "approaching": the
rubric rewards *transferring* the decisions, with numbers, to this shape.

## Prompt

> "We run a food-delivery marketplace in 12 countries. Design the notification system. Customers get order updates
> (confirmed, courier assigned, arriving, delivered) and our marketing pushes. Restaurants get new-order alerts on a
> tablet app; an order they don't accept within 3 minutes is cancelled. Couriers get delivery offers on their phone;
> an offer is withdrawn after 30 seconds. Everyone gets login codes. Growth wants the lunch campaign to land at 11:00
> local time."

That is all the interviewer says up front. Ask the clarifying questions you need (write them down with the answer
you'd assume if told "you decide"), and drive.

Things an interviewer would answer if asked (don't read until you've written your questions):

<details data-kind="hint">
<summary>Interviewer's answers to common clarifying questions</summary>

- Scale: ≈ 25 M monthly customers, ≈ 8 M orders a day, ≈ 400k restaurants, ≈ 600k couriers. Each country is one
  time zone for this exercise.
- Peaks: ≈ 15% of a country's daily orders fall in its busiest hour (lunch or dinner); countries peak at different
  UTC times, but assume the two biggest overlap.
- Per order: ≈ 5 customer updates, 1 restaurant alert, ≈ 3 courier offers (offered in turn until one courier accepts).
  The courier's "arriving in N minutes" update is recomputed every ≈ 30 s from GPS.
- Order events come from the order service on an at-least-once stream with an event id and a per-order sequence
  number; duplicates and out-of-order delivery happen (a "delivered" event can arrive before "courier assigned").
- Channels: push for customers and couriers; the restaurant tablet app holds a persistent connection and
  acknowledges alerts; SMS for login codes and as a fallback; an automated voice call is available as a last resort
  for restaurants.
- SMS: ≈ 1.5 M login codes a day; vendors differ per country (two countries have a single local vendor contracted
  at ≈ 50 messages/s; others ≈ 200/s); prices vary ≈ 5× between countries (`TODO-VERIFY` for real figures).
- Marketing: two campaigns a day per country, ≈ 10 M opted-in customers in total per campaign, push only. Customers
  can opt out per category; marketing respects quiet hours and a cap of 2 per day. Order updates and offers ignore
  both.
- Latency: courier offers on the phone within ≈ 2 s; restaurant alerts within ≈ 5 s; customer updates within ≈ 10 s;
  login codes within ≈ 5 s.
- Audit: restaurants dispute cancellations ("we never got the order"); you must show what was sent, when, and what
  was acknowledged, for 1 year.
- Out of scope unless you choose otherwise: the dispatch algorithm that picks couriers, in-app chat, the campaign
  authoring UI.

</details>

## What to produce

1. **Timestamp log**: the clock time at the start of each phase (evidence for time management).
2. **Requirements card**: categories with a policy each (lane, latency, TTL, opt-out/quiet hours/cap, duplicate vs
   miss), functional requirements (3–5), non-functional requirements *with numbers*, explicit out-of-scope, and your
   clarifying questions with the assumptions you proceeded with.
3. **Estimate sheet**: at most 6 numbers with arithmetic and units, each with the decision it drives: notifications
   per day and in the peak hour by category, the campaign's send rate if unthrottled versus throttled, provider calls
   in flight, SMS volume and cost per day, and attempt-log storage for a year with replication.
4. **API and event sketch**: how order events become notifications (and which keys come from where), the
   restaurant acknowledgement, the courier offer with its expiry, preferences, and provider receipts.
5. **Data model**: access patterns first; notifications, attempts (append-only), acknowledgements, per-order latest
   sequence, device tokens and tablet sessions, preferences; partition keys and tenant/country on every key.
6. **HLD diagram**: the offer path, the restaurant-alert path with its escalation, the customer-update path and the
   campaign path, with rates on at least three arrows and a one-line justification per box.
7. **Deep dives (2–3)**, each with alternatives, a cost and a flip condition:
   - the restaurant alert: what "delivered" means, acknowledgement, escalation push → SMS → voice, and what you show
     in a cancellation dispute;
   - stale and duplicate updates: ordering by sequence number, collapse, TTLs, what happens to a courier offer that
     is late or sent twice;
   - the vendor boundary per country: failover where there is one vendor, timeouts, retries per category, and
     keeping the lunch campaign from delaying a single offer or login code.
8. **Failure walkthrough**: the push provider's latency goes to 8 s at 12:30 in the biggest country; the order
   service replays 2 hours of events; the only SMS vendor in one country is down for 40 minutes.

## Rubric

| Dimension | below | approaching | meets (senior bar) | exceeds |
|---|---|---|---|---|
| Requirements & categories | Draws immediately; one undifferentiated "notification" | Lists message types but no per-category policy | A category table: lane, latency, TTL (30 s for offers, ≈ 3 min for restaurant alerts, the order's lifetime for updates), opt-out/quiet hours/cap only for marketing, and duplicate-vs-miss per category; out-of-scope stated | Spots that the offer and the restaurant alert are the two messages whose failure costs money (a lost order, an idle courier) and designs around them first |
| Estimation | Missing, or ≥ 10× off on a number the design depends on | Daily totals only; no peak-hour or campaign number | ≤ 6 numbers, units carried: ≈ 72 M order notifications/day, ≈ 3k/s in the peak hour, campaign ≈ 11k/s if sent in 15 min vs ≈ 2.8k/s over an hour, ≈ 600 calls in flight, SMS ≈ 1.5–2 M/day and its cost; each tied to a decision | Shows the 50/s single-vendor countries can't absorb any SMS fallback surge and plans for it (voice, push-first, a second vendor) |
| API, keys & data model | No idempotency; status column overwritten; no sequence handling | Keys mentioned but conflated; ack not modelled | Notifications created from events with `Idempotency-Key = event id` and dedupe keys from the business fact; collapse key per order and per offer; sequence number per order; append-only attempts and acknowledgements; partition keys follow access patterns; country/tenant on every key | Device-side dedupe by offer id designed in; the ack stream modelled as the source of truth for restaurant delivery with retention for disputes |
| High-level design & lanes | One queue; vendors called from the order service's request path | Lanes drawn, but sharing one worker pool or one provider limiter | Separate lanes (offers, restaurant alerts, login codes, customer updates, campaigns) with their own topics and pools and a reserved share of each provider; the order service publishes events and never waits on a vendor; every box justified; rates on arrows | Per-country cells or provider shares, so one country's vendor outage or campaign can't touch another's; degraded modes drawn |
| Vendor boundary | Retries every error; no timeouts; no failover thinking | Timeouts and retries mentioned without numbers or per category | Timeouts from each lane's budget (≈ 1 s for offers), a breaker per provider (per country × provider), failover where a second provider exists, a per-category retry rule after a timeout (offers and login codes yes within TTL, marketing never), bounded retries with jitter, expiry before every attempt | Plans the single-vendor countries explicitly (voice or push fallback, a second contract, a reserve for login codes) and costs it |
| Don't-send logic | Late offers and stale updates delivered; marketing ignores preferences | Collapse or TTL mentioned, not both; ordering not handled | Strict TTLs (a 31 s-old offer is never sent); never send a state older than the last sent for that order (sequence check at send time); collapse "arriving in N minutes" to the latest; preferences, quiet hours in the member's zone and the 2-per-day cap evaluated at send time for marketing only; campaigns throttled per country at local 11:00 | Edge cases reasoned: an offer withdrawn while queued, a delivered order whose "arriving" update is still queued, a campaign that can't finish in its window |
| Delivery confirmation & escalation | "Sent" means delivered | Receipts mentioned; restaurant escalation vague | Restaurant "delivered" = the app's acknowledgement; escalation timers (push → SMS at ≈ 60 s → voice at ≈ 120 s, within the 3-minute window) with each step recorded; vendor receipts correlated by provider message id; dispute answered from attempts + acks | An end-to-end SLI ("alert acknowledged within 60 s") per country with burn-rate alerts; ack latency used to detect a silently failing push provider |
| Failure modes, ops & cost | None, or "add retries" | Generic metrics | Queue age per lane, accept and delivered rates per provider, breaker state, ack latency, non-send outcomes by reason; kill switch per campaign; the three failure walkthroughs answered with what each user experiences; SMS and voice cost per day | Cost per order of notifications, and the policies that move it (push-first, collapse, fallback thresholds) |
| Communication & time | Checkpoints missed by > 8 min; no trade-off table | Finishes but a phase overruns; trade-offs lack flip conditions | Numbers by ≈ 0:10, diagram by ≈ 0:25, stops at 0:40; drives; a trade-off table with ≥ 3 rows and flip conditions | Anticipates probes; says what was cut and why |

**Overall "meets"** requires *meets* or better on **Vendor boundary** and **Don't-send logic** (the two places this
design either works or doesn't), and no dimension at *below*. **Overall "exceeds"** requires *meets* everywhere and
*exceeds* on at least three dimensions. Otherwise overall is *approaching* (or *below* with two or more dimensions
at *below*).

## Tutor critique protocol

1. **Check the artefacts before probing (≈ 3 min).** Timestamp log present; slips at 0:10 / 0:25 / 0:40 computed.
   Every category has a TTL and a duplicate-vs-miss answer. The estimate sheet's arithmetic: order notifications
   ≈ 8 M × 9 ≈ 72 M/day; peak hour ≈ 15% × 8 M ≈ 1.2 M orders ≈ 330 orders/s × 9 ≈ 3k/s (two overlapping countries
   don't change the order of magnitude); campaign ≈ 10 M in 15 min ≈ 11k/s versus ≈ 2.8k/s spread over an hour, so
   "lands at 11:00" must mean "starts at 11:00, throttled", per country; in flight ≈ 3k/s × ≈ 0.2 s ≈ 600 calls;
   SMS ≈ 1.5 M login codes plus a few hundred thousand fallbacks ≈ 2 M/day, at a blended ≈ $0.03 (`TODO-VERIFY`)
   ≈ $60k/day; attempt log ≈ (72 M + 20 M + 2 M) × 1.3 × 300 B ≈ 37 GB/day ≈ 13 TB/year, ≈ 40 TB with 3 replicas.
   An error ≥ 10× on the peak rate, the campaign rate or SMS cost caps *Estimation* at "approaching".
2. **Self-score first.** Ask for a level per dimension with a one-line justification before saying anything.
3. **Probe (≈ 10 min).** Ask at least six, including the ones marked (required); one at a time; push one rung
   further on vague answers.
   1. **(required)** "A restaurant says it never got order 8812 and it was cancelled. Show me what you'd look up and
      what it would say."
      *Meets:* the attempt log for that notification (push accepted at T, no ack; SMS at T+60 s accepted, carrier
      receipt delivered; voice call at T+120 s, answered or not), the tablet session state, and the ack table; the
      escalation timers are policy, recorded per step; retention ≥ the dispute window.
   2. **(required)** "The order service replays two hours of events at 13:00. What do customers and couriers see?"
      *Meets:* dedupe by event id and business key (no duplicate updates); sequence check at send time (no stale
      states: nobody is told "courier assigned" for an order delivered an hour ago); every replayed offer is past its
      30 s TTL and recorded `expired`; the replay is shaped by the API's quota so it can't crowd live offers.
   3. **(required)** "The push provider's latency goes to 8 s at 12:30. Walk me through the offer lane."
      *Meets:* Little's law (600 in flight at 0.2 s becomes ≈ 24,000 needed at 8 s; a fixed pool's throughput falls
      ≈ 40×); a timeout of ≈ 1 s from the 2 s budget; the breaker opens; for offers there is often no second push
      provider, so the honest answer is expiry and re-offer to another courier, plus SMS for couriers only if policy
      and cost allow; the offer lane's queue age alert fires.
   4. **(required)** "Growth wants the lunch campaign at exactly 11:00 to all 10 M. What do you say, with numbers?"
      *Meets:* ≈ 11k/s in 15 minutes on top of a lunch peak versus ≈ 2.8k/s over an hour; its own lane and pool,
      a throttle per country and per provider share, never a token taken from offers or login codes; quiet hours and
      the 2-per-day cap evaluated at send time; the scheduler tells growth when the campaign will finish.
   5. "An offer is sent twice because the push call timed out and you retried. What happens on the courier's phone?"
      *Meets:* the app dedupes by offer id (designed in), the server accepts only one acceptance per offer
      (idempotent accept), and the retry was legitimate because a missed offer costs more than a duplicate inside the
      TTL.
   6. "Country X's only SMS vendor is down for 40 minutes. What breaks?"
      *Meets:* login codes in that country: push-based approval or voice codes where available, an honest login page
      message; restaurant escalation skips SMS and goes to voice; the breaker per country × provider keeps other
      countries unaffected; the case for a second contract, costed.
   7. "Which metrics page, and which only go on a dashboard?"
      *Meets:* page on offer and restaurant-alert queue age, ack latency, and the provider breaker opening; dashboard
      for campaign progress, opens and clicks, cost.
   8. "Roughly what do notifications cost per order, and which policy would you change first?" (credit toward
      *exceeds* only).
4. **Score each dimension with evidence** (quote the write-up or the probe answer). Say what would have moved each
   dimension up one level.
5. **Calibration feedback**: name any dimension where the self-score was off by more than one level.
6. **Decide** with the overall rule above. Below "meets": give 2–3 concrete revision tasks and redo on a different
   day with a variation (suggested: "design the alerting service for a brokerage app: price alerts that users set,
   margin calls that must be acknowledged, trade confirmations with a regulatory retention, market-open campaigns").

## Recording

`sd record design C7 --self <below|approaching|meets|exceeds> --tutor <level> --notes "<one-line summary>"`

Example: `sd record design C7 --self meets --tutor approaching --notes "lanes and failover solid; stale offers and out-of-order updates not handled; no restaurant ack"`
