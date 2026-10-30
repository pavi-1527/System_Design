# C9 Design exercise — Marketplace pay-ins, seller balances and payouts

> Time box: 45 minutes, solo, no notes, timed like a real interview (numbers by 0:10, end-to-end diagram by 0:25,
> stop drilling at 0:40). Then self-score against the rubric (≈ 10 min), then send the write-up to the tutor for the
> probe session. Use the structure from [`docs/INTERVIEW_FRAMEWORK.md`](../../docs/INTERVIEW_FRAMEWORK.md) and the
> numbers from [`docs/NUMBERS.md`](../../docs/NUMBERS.md) — from memory.

This is a *variant* of the module's case study on purpose. The mechanisms (a double-entry journal with derived
balances, idempotency keys per business fact at every hop, unknown outcomes resolved rather than guessed, holds and
reversals, hot house accounts, three-way reconciliation, an audit trail) all apply, but the shape is different: the
platform is the *merchant* side of card payments, not the issuer; money belongs to sellers who are paid later, so the
platform holds funds in its books; refunds and chargebacks arrive after the seller may already have been paid; and
payouts are a weekly burst to many countries. If you reproduce Unit 1's wallet design box for box you will score
"approaching": the rubric rewards *transferring* the decisions, with numbers, to this shape.

## Prompt

> "We run an online marketplace for handmade goods. Buyers pay by card at checkout; an order can contain items from
> several sellers. We keep a 10% commission. Sellers are paid out to their bank accounts every week. Buyers can get
> refunds, and sometimes they dispute charges with their bank. Design the payments and ledger system."

That is all the interviewer says up front. Ask the clarifying questions you need (write them down with the answer
you'd assume if told "you decide"), and drive.

Things an interviewer would answer if asked (don't read until you've written your questions):

<details data-kind="hint">
<summary>Interviewer's answers to common clarifying questions</summary>

- Scale: ≈ 30 M buyers, ≈ 2 M active sellers in 15 countries, ≈ 4 M orders a day, average order ≈ $40. The busiest
  hour (a holiday sale) runs at ≈ 5× the daily average rate.
- Card payments go through one payment service provider (PSP). The card is authorised at checkout and captured per
  seller when that seller ships, normally within 3 days; an uncaptured authorisation lapses after ≈ 7 days
  (`TODO-VERIFY` for real network rules). ≈ 15% of orders have items from more than one seller.
- The PSP charges ≈ 2.9% + $0.30 per captured payment (`TODO-VERIFY`), which the marketplace pays. It sends webhooks
  for captures, refunds, disputes and payouts, and a daily settlement report; it pays the marketplace's bank account
  the net amount two business days later.
- Sellers have a balance on the marketplace. Weekly payouts go through the PSP's payout API, all on Monday morning;
  new sellers' funds are held for 14 days before they can be paid out. Sellers are paid in their local currency;
  buyers pay in one of 5 currencies.
- Refunds: ≈ 3% of orders, up to 30 days after delivery; partial refunds exist. Chargebacks: ≈ 0.3% of orders
  (`TODO-VERIFY` typical rates), decided weeks later; the marketplace recovers the amount from the seller's balance,
  which may go negative.
- The PSP's APIs accept idempotency keys, retained for ≈ 24 hours (`TODO-VERIFY`). The payout API times out or
  returns ambiguous errors for ≈ 0.2% of calls, more on Monday mornings.
- Finance needs to answer, any day: how much do we owe each seller, how much commission have we earned, and does our
  bank balance agree with all of it.
- Out of scope unless you choose otherwise: fraud scoring, sales tax and VAT calculation, seller KYC, the checkout UI.

</details>

## What to produce

1. **Timestamp log**: the clock time at the start of each phase (evidence for time management).
2. **Requirements card**: functional requirements (3–6), non-functional requirements *with numbers*, the invariants
   (as statements a nightly job could check), explicit out-of-scope, and your clarifying questions with the
   assumptions you proceeded with.
3. **Estimate sheet**: at most 6 numbers with arithmetic and units, each with the decision it drives: captures and
   postings per second (average and peak hour), the write rate on the busiest house account, Monday's payout burst
   rate and the unknown outcomes it produces, journal storage per year, daily reconciliation volume.
4. **Chart of accounts and posting templates**: the accounts (buyer side, seller balances, commission, PSP fees, PSP
   clearing or receivable, bank cash, refunds, chargebacks, suspense, FX if you handle it) and the entries for: a
   two-seller order captured in two parts, a partial refund, a payout, a chargeback after the seller was paid.
5. **API and data model**: checkout and capture, refunds, payouts, PSP webhooks, seller balance reads; keys and where
   each is enforced; access patterns, tables, unique constraints, which tables are append-only.
6. **HLD diagram**: the pay-in path, the capture-per-seller path, the payout path and the reconciliation path, with
   rates on at least three arrows and a one-line justification per box.
7. **Deep dives (2–3)**, each with alternatives, a cost and a flip condition:
   - the ledger: seller balances with holdbacks, concurrency on a seller's balance, hot house accounts on a sale day;
   - the PSP boundary: the chain of idempotency keys, Monday's payout burst, unknown outcomes and their resolution,
     out-of-order webhooks;
   - reconciliation: the PSP settlement report, the bank statement, net settlement with fees deducted, timing,
     refunds and disputes that cross days.
8. **Failure walkthrough**: the PSP's payout API is degraded for two hours on Monday morning; the PSP re-sends a
   day of webhooks; Tuesday's settlement report contains 1,200 captures you have no record of.

## Rubric

| Dimension | below | approaching | meets (senior bar) | exceeds |
|---|---|---|---|---|
| Requirements & invariants | Draws immediately; "store payments" | Lists flows, no invariants or no numbers | Flows (pay-in, capture per seller, refund, chargeback, payout, holdback), NFRs with numbers, and checkable invariants: postings balance, balances derived, Σ = 0 per currency, one posting per key and per PSP reference, seller payouts never exceed available; out-of-scope stated | Asks who bears PSP fees and chargeback losses and shows how the answers change the posting templates |
| Estimation | Missing, or ≥ 10× off on a number the design depends on | Daily totals only; no peak, burst or unknown-outcome number | ≤ 6 numbers, units carried: ≈ 46 orders/s average and ≈ 230/s peak hour, ≈ 12 M postings/day (≈ 140/s, ≈ 700/s peak), commission account ≈ 230–270 writes/s at peak, Monday payouts ≈ 2 M in a few hours (≈ 200–300/s) with ≈ 4,000 unknowns, ≈ 3 TB/year of journal; each tied to a decision | Sees that one primary carries it, so the design effort goes to the hot accounts and the payout burst, and says what would make them shard |
| Ledger design | Seller balance as a column updated in place; no double entry | Double entry mentioned; templates wrong or incomplete (commission or fees missing, refund as an edit) | Correct chart of accounts and multi-leg templates (capture: debit PSP receivable, credit seller payable 90%, credit commission 10%; fees as their own leg or posting; refunds and chargebacks as new postings that may take a seller negative by policy); holdback as an available-balance rule; payout reserves by posting to in-transit; concurrency on the seller balance at payout; hot house accounts handled (append-only legs, sub-accounts or aggregation) | Distinguishes the buyer's card authorisation (a hold at the issuer, not in our ledger) from the seller holdback (our rule), and models seller debt and its recovery |
| PSP boundary & idempotency | Retries with new keys; timeouts treated as failures; webhooks trusted | Keys on the API only; unknown outcomes hand-waved | Key per business fact at every hop (checkout intent, capture per order-seller, refund id, payout id as the PSP key, webhook event id, settlement file id + line); timeouts → `unknown`, money in `payout_in_transit`, resolver queries by reference within the PSP's key retention, never a new key; webhooks verified, recorded then acknowledged, out-of-order handled by a state machine; payout burst throttled under the PSP's limits behind a breaker | Explains what happens when a retry would fall outside the PSP's key retention and designs the pre-submit record and query-first rule for it |
| Reconciliation & audit | None, or "check totals" | Matching by amount and date; no bank leg | Three-way: ledger ↔ PSP settlement report (per transaction, fees, refunds, disputes) ↔ bank statement (net deposit); match on PSP reference; break types with actions; timing differences aged; suspense with owners; file ids and control totals; nightly balances-vs-journal audit; reversals not edits; manual postings maker-checker | Reconciles the PSP's net settlement as captures − refunds − fees − dispute debits, explaining each component, and designs the daily finance sign-off |
| HLD & consistency | PSP called in the checkout request with no timeout story; one box for everything | Boxes drawn; paths not traced; consistency unspecified | Checkout → PSP authorisation with a timeout and an idempotency key; capture on shipment via workflow; payouts via outbox → workers → PSP; webhook receiver; resolver; reconciliation on a replica; strong consistency for seller available balance, keys and refs; eventual for dashboards and seller statements; rates on arrows | Per-currency and per-country concerns drawn (FX position accounts, local payout rails) without derailing the core |
| Failure modes, ops & cost | None, or "add retries" | Generic metrics | The three failure walkthroughs answered with what buyers, sellers and finance see; dashboards for in-transit and suspense by age, unknowns by age, webhook lag, break counts by type; payout kill switch; cost of duplicates versus engineering | Quantifies the cost of a 0.1% duplicate payout rate on Monday's volume and uses it to justify the resolver and the throttle |
| Communication & time | Checkpoints missed by > 8 min; no trade-off table | Finishes but a phase overruns; trade-offs lack flip conditions | Numbers by ≈ 0:10, diagram by ≈ 0:25, stops at 0:40; drives; a trade-off table with ≥ 3 rows and flip conditions | Anticipates probes; says what was cut and why |

**Overall "meets"** requires *meets* or better on **Ledger design** and **PSP boundary & idempotency** (the two places
this design either works or doesn't), and no dimension at *below*. **Overall "exceeds"** requires *meets* everywhere
and *exceeds* on at least three dimensions. Otherwise overall is *approaching* (or *below* with two or more dimensions
at *below*).

## Tutor critique protocol

1. **Check the artefacts before probing (≈ 3 min).** Timestamp log present; slips at 0:10 / 0:25 / 0:40 computed.
   The estimate sheet's arithmetic: 4 M orders / 86,400 s ≈ 46/s, × 5 ≈ 230/s in the peak hour; ≈ 15% multi-seller
   adds captures (≈ 4.7 M captures/day); postings ≈ 3 per order (capture, fees, a share of refunds and payouts) ≈ 12 M
   a day ≈ 140/s, ≈ 700/s peak, one primary; the commission and PSP-receivable accounts take a leg of every capture,
   ≈ 230–270 writes/s at peak against ≈ 250–500/s per locked row; Monday ≈ 2 M payouts in ≈ 2–3 hours ≈ 200–280/s
   (check against any PSP rate limit the candidate assumed) with ≈ 0.2% ≈ 4,000 unknowns; journal ≈ 12 M × 700 B ≈
   8.4 GB/day ≈ 3 TB/year before replication; GMV ≈ 4 M × $40 ≈ $160 M/day, so the marketplace holds hundreds of
   millions of sellers' money at any time. An error ≥ 10× on postings per second, the payout burst or the unknown
   count caps *Estimation* at "approaching".
2. **Self-score first.** Ask for a level per dimension with a one-line justification before saying anything.
3. **Probe (≈ 10 min).** Ask at least six, including the ones marked (required); one at a time; push one rung
   further on vague answers.
   1. **(required)** "Write the entries for a $100 order: $60 from seller S1, $40 from S2, each captured when they
      ship, a 10% commission, and a PSP fee of $3.20. Then S2's item is fully refunded after S2 was paid out."
      *Meets:* two capture postings (S1: debit `psp_receivable` 6,000 / credit `seller:S1` 5,400 / credit
      `commission_revenue` 600; S2 likewise with 4,000 / 3,600 / 400); fees as a posting or leg
      (`debit psp_fees_expense 320 / credit psp_receivable 320`, allocated per capture by a deterministic rule); the
      refund as new postings (`debit seller:S2 3,600`, `debit commission_revenue 400` or a policy to keep the
      commission, `credit psp_receivable 4,000`) that may take S2 negative by policy, recovered from future sales;
      nothing edited.
   2. **(required)** "Monday at 06:00, 3% of payout calls time out for two hours. Walk me through it."
      *Meets:* payouts already reserved into `payout_in_transit`; timeouts → `unknown`, not failed and not retried with
      a new key; the breaker opens and the throttle drops the rate; the resolver queries by payout id within the
      PSP's key retention; ≈ 2 M × 3% × (2 h / burst duration) ≈ tens of thousands of unknowns, so the resolver's
      query rate is bounded too; sellers see "processing"; reconciliation tomorrow confirms.
   3. **(required)** "Tuesday's settlement report has 1,200 captures you have no record of. What happened, and what
      do you do?"
      *Meets:* `MissingInLedger` breaks; likely missed webhooks or a capture path that bypassed the ledger, or another
      environment sharing the PSP account; post known flows from the report or park in suspense so the books match
      cash, query the PSP, and fix the cause; check whether those captures were also refunded or disputed; never
      write off silently.
   4. **(required)** "On the holiday sale, which row is hot and what do you do?"
      *Meets:* commission revenue and the PSP receivable take a leg of every capture; both may go negative, so append
      their legs without updating a balance row (async balances) or split into sub-accounts; seller balances are cold
      except for a few huge sellers, whose credits are safe to append and whose debits (payouts, refunds) serialise.
   5. "The PSP re-sends a day of webhooks. What changes in your books?"
      *Meets:* nothing: dedupe on the PSP event id (recorded before acknowledging), and step keys on every posting, so
      even an event with a new id for the same fact replays.
   6. "A seller with a negative balance (after a chargeback) makes a sale. What happens to the money?"
      *Meets:* the capture posting credits the seller's balance, which nets against the debt; payouts only when
      available (balance − holdback − reserve) is positive; policy for collections after N days.
   7. "Why not hold the seller's funds with an expiring hold, like a card authorisation?"
      *Meets:* the 14-day holdback is a policy on available balance, not an external reservation; an expiring hold
      would free money on a timer even if a dispute is open; holds model reservations whose lifetime an external
      party defines.
   8. "Sellers are paid in 15 currencies. Where does FX live in the ledger?" (credit toward *exceeds* only).
      *Meets/exceeds:* one currency per account; conversion as linked postings through `fx_position` accounts per
      currency; the rate and spread recorded; revaluation as reporting.
4. **Score each dimension with evidence** (quote the write-up or the probe answer). Say what would have moved each
   dimension up one level.
5. **Calibration feedback**: name any dimension where the self-score was off by more than one level.
6. **Decide** with the overall rule above. Below "meets": give 2–3 concrete revision tasks and redo on a different
   day with a variation (suggested: "design the ledger for a prepaid card programme run on an issuer-processor:
   real-time authorisations against a program balance, clearing files, network settlement, load and unload by ACH,
   and a sponsor bank's statement").

## Recording

`sd record design C9 --self <below|approaching|meets|exceeds> --tutor <level> --notes "<one-line summary>"`

Example: `sd record design C9 --self meets --tutor approaching --notes "ledger templates solid; payout timeouts retried with new keys; no bank leg in recon"`
