# C8 Teach-back

## Prompt

A mid-level engineer on your team has been asked to get the booking service ready for a stadium on-sale: 18,000
seats, about half a million fans at 10:00. Their plan: "when someone picks seats we `SELECT` them, and if they're all
available we `UPDATE` them to `held` with a 10-minute timeout; a cron job frees expired holds every minute; the
payment page charges the card and marks the seats `sold` when it gets the success redirect; we'll cache the seat map in
Redis and autoscale the API to 200 pods for the sale." They have never seen an on-sale from the inside.

Explain to them, in your own words, **how a ticket-booking system has to be built for a sale like this, and why**:
what actually limits it (hint: not the pods), how to make it impossible to sell a seat twice and which isolation
level that needs, how holds should expire, what happens when a payment finishes after the hold, how confirmation
stays correct when the payment provider retries, what to put in front of the inventory so it isn't flattened at 10:00,
and how the seat map should be served.

Form: written, 500–900 words, one diagram (Mermaid or a sketch) allowed. Use numbers: pick a scale (theirs or the
module's) and show two or three calculations. No copying from the unit files; if a sentence from the course
appears verbatim, the evaluator will ask you to rephrase it. Write for that engineer, so they can go and change their
plan, not for an examiner.

## Evaluator checklist

Must-hit (all required for pass):
- [ ] Identifies **contention on a small inventory** as the problem, with numbers (≈ 28 people per seat, thousands
      of arrivals a second), and explains why more pods add conflicting writers, not sales.
- [ ] Replaces select-then-update with an **atomic claim**: a conditional `UPDATE … WHERE status = 'available'` (expired
      holds included) with the affected-row count checked, or an equivalent (insert-to-claim, sorted
      `SELECT … FOR UPDATE`), and says it is safe at READ COMMITTED while select-then-update is not; plus a **unique
      constraint** on sold tickets as the backstop.
- [ ] Makes **multi-seat holds all-or-nothing** and avoids deadlock (one statement or a sorted lock order).
- [ ] **Lazy expiry**: an expired hold is free at the next claim, whatever the sweeper is doing; the sweeper only tidies
      and updates the map; one clock (the database's) defines "expired".
- [ ] A **payment-pending state** with its own window (a short selection hold, extended once at checkout), and the rule
      at the deadline: **cancel the payment first, then release**; a late payment re-claims the same seats if free or
      is voided or refunded, never "sold anyway".
- [ ] **Idempotent confirmation from the provider's webhook**, deduplicated on the provider's event id and bound before
      the work, with the browser redirect treated as a hint, not proof.
- [ ] A **waiting room** that admits users at the rate the inventory (and the payment provider) can sell, derived
      with Little's law or equivalent, with **signed passes** the booking API verifies without a database read.
- [ ] The **seat map as a periodically published snapshot** (layout cached separately), served from a CDN or edge
      cache, explicitly a hint, with the hold's 409 as the truth.

Should-hit (at least 5 of 9):
- [ ] General admission as a hot counter (≈ 500 updates/s on one row) and a bucketed or block-allocation fix with the
      end-game problem named.
- [ ] Per-account limits enforced with a row that conflicts, not by counting existing tickets (write skew).
- [ ] Authorise-then-capture, so a lost seat becomes a void rather than a refund.
- [ ] Reconciliation against the payment provider (payments without orders, stuck authorisations) during the sale.
- [ ] A token bucket capped at a burst, so an idle period can't release the whole crowd at once.
- [ ] Position computed cheaply (sequence number minus a published watermark) and an honest ETA, with "sold out"
      broadcast early and a bounded queue.
- [ ] Partitioning by event for contention isolation (the hot event on its own primary) rather than for data size.
- [ ] Holds or confirmations in Redis treated as a gate, with the database still deciding sales (asynchronous
      replication loses writes on failover).
- [ ] A fairness or bot measure with its cost (randomised pre-queue, registration lottery, account and card limits,
      passes bound to the account).

Red flags (any one = revise):
- [ ] Keeps select-then-update, or says a transaction alone (at the default isolation level) prevents oversell.
- [ ] Puts the payment-provider call inside the transaction that holds seat locks.
- [ ] Relies on the waiting room, the cache or Redis to prevent oversell.
- [ ] Confirms on the browser's success redirect, or confirms whenever payment succeeds regardless of the hold's state.
- [ ] Releases seats at the deadline and then cancels the payment (or never cancels it).
- [ ] Claims autoscaling the API solves the on-sale.
- [ ] Numbers stated as facts with no scale assumption, or a ≥ 8× unit error (bits vs bytes, per minute vs per second)
      that isn't caught.

## Recording

`sd record teachback C8 --result <pass|revise> --notes "<missed items>"`
