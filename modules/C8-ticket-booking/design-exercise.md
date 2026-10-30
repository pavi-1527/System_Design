# C8 Design exercise — Movie tickets across cinema chains on release day

> Time box: 45 minutes, solo, no notes, timed like a real interview (numbers by 0:10, end-to-end diagram by 0:25,
> stop drilling at 0:40). Then self-score against the rubric (≈ 10 min), then send the write-up to the tutor for the
> probe session. Use the structure from [`docs/INTERVIEW_FRAMEWORK.md`](../../docs/INTERVIEW_FRAMEWORK.md) and the
> numbers from [`docs/NUMBERS.md`](../../docs/NUMBERS.md) — from memory.

This is a *variant* of the module's case study on purpose. The mechanisms (holds with expiry, conditional claims and
unique constraints, a payment-pending state with cancel-then-release, idempotent confirmation, a waiting room with
signed passes, snapshots for the seat map) all apply, but the constraints are different: the inventory is tens of
thousands of small shows instead of one arena, most of it is owned by **someone else's system** that you reach through
a slow API with its own hold timer, and most payments confirm **asynchronously** minutes later. If you reproduce the
units' design box for box you will score "approaching": the rubric rewards *transferring* the decisions, with numbers,
to this shape.

## Prompt

> "We're a movie-ticket marketplace. We sell seats for about 8,000 screens across several cinema chains. A huge
> release opens advance booking for its first weekend on Monday at 12:00. Design the booking system. Most of the big
> chains run their own box-office software, and the seats we sell are their seats. Our customers mostly pay by UPI."

That is all the interviewer says up front. Ask the clarifying questions you need (write them down with the answer
you'd assume if told "you decide"), and drive.

Things an interviewer would answer if asked (don't read until you've written your questions):

<details data-kind="hint">
<summary>Interviewer's answers to common clarifying questions</summary>

- Scale: ≈ 8,000 screens, ≈ 5 shows a day, ≈ 250 seats per show on average (multiplex screens 150–300, a few single
  screens up to 1,000). About 4,000 screens play the release; its first weekend (Friday to Sunday) is ≈ 60,000 shows,
  ≈ 15 M seats. Normal day: ≈ 1.5 M tickets, ≈ 2.2 seats per booking.
- Demand: ≈ 2 M users try in the first 10 minutes after 12:00, ≈ 6 M in the first hour. The hottest inventory is the
  "first day, first show" (FDFS) in ≈ 300 big-city screens: ≈ 1,500 shows, ≈ 375,000 seats, demand ≈ 10:1 on average
  and ≈ 30:1 for the best halls.
- Inventory ownership: ≈ 60% of screens belong to chains whose box-office system (POS) is the system of record. We
  hold, confirm and release seats through each chain's API: p50 ≈ 300 ms, p99 ≈ 3 s, occasional 5xx, ≈ 20 requests
  per second per cinema. A POS hold expires on their side after 7 minutes and cannot be extended. Confirm is idempotent
  on our booking reference. The other ≈ 40% of screens use our software as their box office: we own that inventory.
  (`TODO-VERIFY`: these partner figures are invented for the exercise.)
- Payments: ≈ 70% UPI (the user approves in their UPI app; the result arrives by callback, usually within 10–60 s,
  sometimes several minutes later; a collect request has an expiry you set within limits), ≈ 30% cards with OTP-based
  3-D Secure and wallets. (`TODO-VERIFY` real UPI timeouts and callback behaviour.)
- Seats: assigned seating with a seat map per screen layout; some single screens sell an unreserved balcony as general
  admission, both online and at the counter.
- Limits: at most 10 seats per booking; for FDFS shows, at most 4 seats per account.
- Fairness: the distributor wants "no bots in the FDFS sale"; the business wants "the app must not go down at 12:00".
- Latency: seat map ≤ ≈ 2 s stale; hold p99 ≤ 1 s for our own inventory, ≤ 4 s for chain inventory.
- Audit: chains reconcile daily against our booking records; disputes are "the customer has a ticket from you, our POS
  says the seat was sold at the counter".
- Out of scope unless you choose otherwise: food and beverage orders, offers and coupons, recommendations.

</details>

## What to produce

1. **Timestamp log**: the clock time at the start of each phase (evidence for time management).
2. **Requirements card**: functional requirements (3–5), the hard invariants stated per inventory owner (ours vs a
   chain's), non-functional requirements *with numbers*, explicit out-of-scope, and your clarifying questions with the
   assumptions you proceeded with.
3. **Estimate sheet**: at most 6 numbers with arithmetic and units, each with the decision it drives: arrivals at 12:00
   and the demand ratio for FDFS, seat-map reads and the size of one show's snapshot, hold and confirm rates against
   our database and against one chain's POS (how long a hot multiplex takes to sell out through 20 requests a second),
   calls in flight to a POS (Little's law), and the payment window versus the POS hold.
4. **API sketch**: hold, checkout, payment callback, booking status, the waiting room and its pass; which calls are
   idempotent and on which key; what the client sees while a chain's POS is slow.
5. **Data model**: access patterns first; shows, seats (or a local shadow of chain seats), holds with both our state and
   the POS state, bookings, payment events, limit rows, an append-only inventory log; partition keys and the unique
   constraints that are your backstop.
6. **HLD diagram**: the path for our own inventory, the path for chain inventory through a POS adapter, the payment
   path, the waiting room, the seat-map snapshots, with rates on at least three arrows and a one-line justification per
   box.
7. **Deep dives (2–3)**, each with alternatives, a cost and a flip condition:
   - claiming seats you don't own: the POS adapter, timeouts as unknown outcomes, idempotent references, a local shadow
     of the POS state, what "held" means to the user;
   - payment in flight against a 7-minute POS hold: the payment window, the UPI collect expiry, cancel-then-release,
     late payments, reconciliation with both the payment provider and the chain;
   - the 12:00 crowd: what you queue (the release, a show, a cinema), the admission rate per POS, the FDFS limit,
     bots, the seat map.
8. **Failure walkthrough**: one chain's POS API goes to p99 = 15 s at 12:03; UPI callbacks are delayed by 6 minutes for
   a quarter of payments between 12:05 and 12:20; your inventory database's primary fails over at 12:08.

## Rubric

| Dimension | below | approaching | meets (senior bar) | exceeds |
|---|---|---|---|---|
| Requirements & invariants | Draws immediately; "book a seat" with no notion of holds or owners | Holds and payments listed; ownership by the chains not reflected in the invariants | Two invariants (no seat sold twice; no money without a ticket or refund) stated separately for our inventory and chain inventory; the POS as authority for chain seats; hold TTLs and the payment confirmation mode asked about; out-of-scope stated | Notices up front that the 7-minute POS hold and asynchronous UPI clash, and that chain APIs, not our servers, cap the sale rate |
| Estimation | Missing, or ≥ 10× off on a number the design depends on | Daily totals only; no 12:00 number or no POS number | ≤ 6 numbers, units carried: ≈ 3,300 arrivals/s at 12:00; FDFS demand ≈ 10–30:1; one show's snapshot ≈ 250 × 2 bits ≈ 63 B (request rate, not bandwidth, is the issue); a hot multiplex (≈ 30 FDFS shows × 250 seats ≈ 3,400 bookings × ≈ 2.5 POS calls) takes ≈ 7 minutes through 20 calls/s; ≈ 60 POS calls in flight per cinema at p99; each tied to a decision | Shows that admission must be set per cinema (≈ 8 bookings/s for a 20 calls/s POS), not per release, and sizes the queue per hot cinema |
| API & data model | No idempotency; seat state duplicated with no authority; price from the client | Hold and booking modelled; POS state and our state conflated; keys mentioned but not scoped | Idempotent hold (our reference sent to the POS), checkout extending once, signed payment callback deduped by provider event id; holds record both our state and the POS hold id and expiry; unique constraints on tickets per (show, seat) for our inventory and on bookings per hold; FDFS limit as a row; partition by show (or cinema) | A local shadow of chain seat state, fed by our own holds and the POS's responses, used to fail fast on known conflicts without spending POS calls |
| High-level design | One service calling the POS inside the user's request with no timeouts; seat map from the database | Adapter drawn, but no waiting room or no snapshot path | Waiting room with signed passes; booking API; inventory DB partitioned by show for our screens; a POS adapter per chain with timeouts, breaker, per-cinema rate limiter and idempotent references; payment provider and callback handler with dedupe and outbox; sweeper; snapshotter + CDN; every box justified; rates on arrows | Per-chain bulkheads so one chain's slow POS can't exhaust shared workers or delay other chains; degraded modes drawn (a chain's shows marked "booking paused") |
| Oversell & holds | Read-then-write; holds without expiry; confirms on our side only | Conditional writes for our inventory, but chain inventory "held" in our DB without the POS, or POS timeouts treated as failures | Our inventory: one conditional claim, sorted, expiry in the predicate, unique constraint backstop; chain inventory: the POS decides, a timeout is an unknown outcome resolved by retrying with the same reference or querying, the user sees "held" only after the POS confirms, and our record never says "sold" without a POS confirm; balcony GA handled as a counter or an allocated block | An online allocation block for single-screen GA balconies returned to the counter before showtime; reconciliation with each chain daily with a defined resolution for each mismatch |
| Payment in flight | Releases at the hold deadline and ignores late payments | A payment-pending state, but its window ignores the POS's 7-minute hold | Payment window = min(our window, POS hold remaining − margin); UPI collect expiry set to fit inside it; checkout refused or re-held when too little POS time remains; cancel-then-release (or expire the collect, then release at the POS); confirm at the POS before telling the customer; late payment → re-hold if possible, else automatic refund; callbacks deduped by provider event id; reconciliation with the payment provider | Authorise-then-capture for cards so lost seats become voids; a measured "confirm after POS expiry" rate with an alert; a policy for UPI payments that complete after the collect expired |
| The 12:00 crowd | "Autoscale" | A waiting room, but admitting at one global rate sized by our servers | A waiting room for the release (or per hot cinema) admitting at the rate the inventory sells: our database for our screens, ≈ 20 calls/s per POS for chain screens; position by sequence and watermark; signed passes bound to the account; seat-map snapshots per show on a CDN; FDFS 4-per-account limit as a row; an honest sold-out message | Bot controls with costs (randomised pre-queue, account and payment-instrument limits, a registration lottery for the top FDFS shows) and how you would measure them |
| Failure modes, ops & cost | None, or "add retries" | Generic metrics | The three failure walkthroughs answered with what users see; metrics per chain (POS p99, error rate, breaker state, calls/s vs limit), per sale (queue length, admission rate, conflict rate, payment-pending count, confirm-after-expiry), reconciliation mismatches; runbook levers (pause a chain, change admission, extend windows) | Cost of refunds and failed bookings per chain; the case for asking chains for higher POS limits or extendable holds, with numbers |
| Communication & time | Checkpoints missed by > 8 min; no trade-off table | Finishes but a phase overruns; trade-offs lack flip conditions | Numbers by ≈ 0:10, diagram by ≈ 0:25, stops at 0:40; drives; a trade-off table with ≥ 3 rows and flip conditions | Anticipates probes; says what was cut and why |

**Overall "meets"** requires *meets* or better on **Oversell & holds** and **Payment in flight** (the two places
this design either sells seats correctly or doesn't), and no dimension at *below*. **Overall "exceeds"** requires
*meets* everywhere and *exceeds* on at least three dimensions. Otherwise overall is *approaching* (or *below* with two
or more dimensions at *below*).

## Tutor critique protocol

1. **Check the artefacts before probing (≈ 3 min).** Timestamp log present; slips at 0:10 / 0:25 / 0:40 computed.
   Both invariants stated, with the POS as the authority for chain seats. The estimate sheet's arithmetic: arrivals
   ≈ 2 M / 600 s ≈ 3,300/s; FDFS ≈ 375,000 seats at ≈ 10:1, so ≈ 3.75 M people competing for ≈ 170,000 bookings;
   a show's snapshot ≈ 250 × 2 bits ≈ 63 B (so the seat map is a request-rate problem, solved by caching per show, not
   a bandwidth problem); a hot multiplex ≈ 30 FDFS shows × 250 ≈ 7,500 seats ≈ 3,400 bookings × ≈ 2.5 POS calls
   (hold, confirm, some releases) ≈ 8,500 calls ÷ 20/s ≈ 7 minutes at best; POS calls in flight ≈ 20/s × 3 s ≈ 60
   per cinema at p99 (and ≈ 300 at a 15 s p99, which is why a breaker and a per-cinema limiter exist); payment window
   ≤ 7 min − time already spent − margin. An error ≥ 10× on arrivals, the POS sell-out time or calls in flight caps
   *Estimation* at "approaching".
2. **Self-score first.** Ask for a level per dimension with a one-line justification before saying anything.
3. **Probe (≈ 10 min).** Ask at least six, including the ones marked (required); one at a time; push one rung
   further on vague answers.
   1. **(required)** "A customer's UPI payment succeeds 9 minutes after they started checkout. The chain's POS hold
      expired at 7 minutes. Walk me through it, and then tell me how you stop it happening."
      *Meets:* the callback is deduped and finds the hold no longer payable (our window was bounded by the POS hold);
      the adapter tries to re-hold the same seats at the POS; if they are gone, an automatic refund, an order marked
      accordingly, a message to the customer, and a `confirm_after_expiry` metric. Prevention: the payment window and
      the UPI collect expiry are set from the POS hold's remaining life minus a margin; checkout is refused (re-hold
      first) when too little remains; at the deadline, expire or cancel the payment request, then release at the POS.
   2. **(required)** "The POS hold call times out after 4 s. Do we have the seats?"
      *Meets:* unknown; the hold carried our idempotent booking reference, so the adapter retries with the same
      reference or queries the POS for it; the user sees "holding…" and only "held" after a POS confirmation; if it
      can't be resolved within the budget, tell the user to try again and let a reconciliation job release any orphan
      POS hold; the breaker per chain opens on repeated timeouts and the chain's shows show "booking paused".
   3. **(required)** "12:00 Monday, 2 M people. What protects the chains' APIs and your database?"
      *Meets:* a waiting room with signed passes; admission per hot cinema at ≈ the POS's rate (≈ 8 bookings/s at
      20 calls/s and 2.5 calls per booking) and per our own partitions for our screens; position by sequence and
      watermark; seat-map snapshots per show on a CDN; a local shadow of seat state to fail fast on known conflicts;
      the FDFS limit as a row.
   4. **(required)** "A customer on our app and a customer at the box office buy F12 for the same chain show in the
      same second."
      *Meets:* the POS is the authority, so one of our hold calls fails and our customer gets a 409 naming F12; we
      never record a sale without a POS confirm; the daily reconciliation with the chain compares bookings and
      resolves disputes from the inventory log and the POS references.
   5. "For FDFS, a user opens three tabs and books 4 seats in each."
      *Meets:* a limit row per (show or release, account) updated conditionally in the hold transaction (not a count of
      existing bookings, which is a write skew); the pass is bound to the account.
   6. "A single-screen balcony is general admission, sold at the counter and online. How do you avoid overselling it?"
      *Meets:* an online allocation (a block of N seats) agreed with the cinema and decremented as a counter, or the
      cinema's POS as the authority with a counter there; unsold online allocation returned before showtime.
   7. "What's on your release-day dashboard, and what pages someone?"
      *Meets:* page on a chain's POS error rate or breaker opening, confirm-after-expiry above a threshold, callback
      verification failures, reconciliation mismatches; dashboard for queue length, admission rate, conflict rate,
      payment-pending count and age.
   8. "What does a bot-free FDFS sale cost, and would you do it?" (credit toward *exceeds* only).
4. **Score each dimension with evidence** (quote the write-up or the probe answer). Say what would have moved each
   dimension up one level.
5. **Calibration feedback**: name any dimension where the self-score was off by more than one level.
6. **Decide** with the overall rule above. Below "meets": give 2–3 concrete revision tasks and redo on a different
   day with a variation (suggested: "design a limited sneaker drop: 5,000 pairs across 12 sizes, 1 M people at 10:00,
   card payments, an optional raffle, and a resale problem the brand wants to prevent").

## Recording

`sd record design C8 --self <below|approaching|meets|exceeds> --tutor <level> --notes "<one-line summary>"`

Example: `sd record design C8 --self meets --tutor approaching --notes "own inventory solid; POS timeout treated as failure; payment window not bounded by POS hold"`
