# C3 Design exercise — Transaction identifiers for a two-datacentre banking platform

> Time box: 45 minutes, solo, no notes. Then self-score against the rubric (≈ 10 minutes), then send it to the
> tutor for the probe session (≈ 15 minutes).
>
> This is a *different* prompt from the unit's guided design on purpose. One of the identifiers has a far smaller
> bit budget than the unit's, the ordering requirement belongs somewhere else, and the network partition is real.
> A design copied from the unit will score below the bar on estimation and on two of the deep dives.

## Prompt

> "The platform moves money for 40 credit unions from two datacentres, both active. Every transfer, every ledger
> posting and every vendor callback needs an identifier, and members see a reference number for each transfer.
> Design how those identifiers are issued."

That is all the interviewer says. Everything else (volumes, what the identifiers are used for, how long they must
stay unique, what the core-banking vendor accepts, what auditors ask for, what happens when the datacentres can't
talk) is for you to ask or to state as an assumption. Write your clarifying questions down with the assumption you
proceed with; the tutor answers them during the probe session.

<details data-kind="hint">
<summary>Interviewer answers to likely clarifying questions (open only after you have written your own questions and assumptions)</summary>

- **Volume.** ≈ 15M transfers/day, ≈ 90M ledger postings/day (≈ 6 per transfer: debit, credit, fees, holds and their
  releases) and ≈ 30M vendor callbacks/day. Payroll mornings run at ≈ 10× the daily average for about two hours.
  Bulk payroll files arrive as single uploads of up to 200,000 transfers.
- **Topology.** Two datacentres (DC1, DC2), both accepting transfers for any tenant; each tenant's ledger is homed in
  one of them. ≈ 12 services and ≈ 150 processes per datacentre, up to ≈ 200 per datacentre during deploys. The
  inter-datacentre link has ≈ 5 ms RTT and partitions a few times a year for up to an hour; during a partition both
  datacentres must keep accepting transfers.
- **Internal identifiers.** Transfer, posting and callback ids are primary keys in existing `BIGINT` columns, used for
  cursor pagination and for "all postings for this account after id X" scans. They must never repeat.
- **The member-visible reference.** Shown in the app and on statements, read out over the phone to support, and
  passed to the core-banking vendor, whose reference field is **numeric, exactly 16 digits**. The last digit should
  be a check digit so that support catches a mistyped reference. References must not repeat for **20 years** (the
  vendor's retention), and a member must not be able to work out a credit union's transfer volume from two
  references a day apart.
- **Audit.** Examiners ask for each account's postings "in the order they were applied", and want evidence that no
  transfer is missing. Gaps in reference numbers are acceptable if you can explain them.
- **Clocks.** Each datacentre runs its own NTP sources; measured host offsets are normally ≤ 5 ms. Last year a VM
  migration stepped one host's clock back by ≈ 800 ms.
- **Constraints.** Team of 6. Each datacentre runs PostgreSQL and a 3-node etcd cluster; Kafka replicates events
  between datacentres. Moderate budget.

</details>

## What to produce

1. **Timestamp log.** The clock at the start of each phase (numbers by ≈ 0:10, complete diagram by ≈ 0:25, stop
   drilling by ≈ 0:40). Written during the exercise, not reconstructed.
2. **Requirements card.** Functional requirements, NFRs with numbers, out of scope, and your clarifying questions
   each with the assumption you proceeded with. Name the dominant risk in one sentence.
3. **Two bit budgets.** One for the internal 64-bit ids and one for the 16-digit reference, each with the arithmetic
   shown (lifetime, producers, per-producer rate) and the decision each number drives. The second one is where the
   unit's numbers stop working.
4. **API and data model.** How services obtain each kind of identifier; how the reference is formatted and
   validated; where node-id leases (or counters) live in each datacentre; how the audit order is recorded.
5. **HLD diagram.** Both datacentres; where each identifier is minted; what crosses the inter-datacentre link and
   what never does; rates on the arrows.
6. **Three deep-dive cards** (claim → why here → cost → flip): (a) the internal id scheme and its uniqueness across
   two active datacentres; (b) the 16-digit reference: uniqueness for 20 years, the check digit, and why it reveals
   no volume; (c) clocks and node ids: the 800 ms step, a paused process, a deploy surge.
7. **Ordering and idempotency.** How the examiners get "the order they were applied", and how a payroll file
   uploaded twice, or a vendor callback delivered twice, avoids creating duplicates.
8. **Failure walk-through.** The datacentres partition for an hour at 08:00 on a payroll morning; the etcd cluster in
   DC2 is down for 20 minutes; a host steps its clock back 800 ms. What do callers see, what is minted, what could be
   duplicated, what is reconciled afterwards?
9. **Metrics and alerts.** Five signals, including one that would catch a duplicate id before a customer does.
10. **Risks and open questions.** What you would validate first.

## Rubric

| Dimension | below | approaching | meets (senior bar) | exceeds |
|---|---|---|---|---|
| **1. Requirements and scope** | Features listed; no questions about size, ordering or the vendor field | Asks about volume and size but not the reference's constraints or the partition | Asks about 64-bit columns, ordering strength, the 16-digit field and check digit, retention, volume leakage, active/active and partitions; out-of-scope list; names the dominant risk | Notices early that "the order they were applied" is not an id property and says where it will come from |
| **2. Estimation** | No numbers, or a reference that doesn't fit 16 digits | Internal layout computed; the 16-digit budget hand-waved | Peak rates (≈ 15k ids/s on payroll mornings, ≈ 1.7k transfers/s); internal layout with lifetime and node bits for ≈ 400 processes; 10^15 ≈ 2^49.8 for 15 payload digits, with a layout or counter space that lasts 20 years; marked ≈ | Checks the reference space against 20 years of transfers (≈ 1.1 × 10^11 against ≈ 5.6 × 10^14) and states the margin |
| **3. API and data model** | One "get id" call for everything; references as raw counters | Library for internal ids but the reference path or the audit order is missing | Library for internal ids; a reference generator (or mapping) with format, Luhn check digit and validation; node-lease or counter records per datacentre; a per-account posting sequence; ids as strings in JSON | Specifies how a reference is looked up by support (reference → transfer id) and its retention |
| **4. High-level design** | Ids minted in one datacentre for both; a path can't be traced | Both datacentres drawn but cross-DC dependencies unclear | Each datacentre mints independently (DC bit or disjoint spaces); nothing on the minting path crosses the link; registries per datacentre; rates on arrows | Shows what Kafka replicates and how the two datacentres' ids interleave in a merged view |
| **5. Internal ids (deep dive a)** | UUIDv4 in `BIGINT`, or a central sequence across the link | Snowflake-style with node ids from config, or no datacentre separation | 64-bit time + node + sequence with a DC bit; node ids leased per datacentre; overflow waits; k-sorted within skew, and says what that promises | Sizes the layout for growth (a third datacentre, 10× processes) and the migration from existing ids |
| **6. The reference (deep dive b)** | Doesn't fit 16 digits, or is a visible counter per tenant | Fits, but uniqueness over 20 years or the volume leak is unaddressed | 15-digit payload from a ≤ 49-bit space (time-based or leased ranges per datacentre), permuted with a keyed bijection so it reveals no volume, plus a Luhn digit; uniqueness argued for 20 years and across both datacentres during a partition | Discusses key rotation for the permutation (it can't change without a new space) and support's lookup path |
| **7. Clocks and node ids (deep dive c)** | Trusts NTP; node ids from IPs or hostnames | Regression policy or lease mechanics, but not both | Waits out small regressions, refuses the 800 ms step; leases with token, holder deadline from the send time, and a floor; TTL sized against an etcd outage and deploy churn | Explains why a reference scheme based on leased ranges avoids the clock entirely, and chooses accordingly |
| **8. Ordering, audit and idempotency** | "Sort by id" for the examiners; retries mint new transfers | Mentions idempotency keys but confuses them with generated ids | Per-account sequence assigned in the posting transaction; completeness by reconciliation, gaps explained; idempotency keys from the caller (file id + line, callback's vendor reference) distinct from generated ids | Shows the examiners' export and the reconciliation report, and what each proves |
| **9. Failure modes and operations** | None, or "add replicas" | Some failures named without their effect | All three scenarios walked: minting continues in both datacentres during the partition with no duplicates; DC2's etcd outage is absorbed by the lease TTL; the stepped host refuses; detection named for each | Game-day plan for the partition and the clock step; a duplicate-id detector (unique constraints plus a sampled cross-DC check) |
| **10. Communication and trade-offs** | Disorganised; no flip conditions | Trade-offs for some decisions; time slips > 5 min | Driven by the candidate; checkpoints met within ≈ 2 min; three cards with measurable flips; says what to validate first | Anticipates probes; gives a phased rollout and the cost of each identifier scheme |

## Tutor critique protocol

Read the artifact first, then run the conversation. Do not reveal the rubric rows until the learner has
self-scored every dimension with one line of justification each.

1. **Check both budgets (≈ 3 min).** Recompute the payroll-morning peak (≈ 135M ids/day ≈ 1.6k/s average, ≈ 16k/s at
   10×), the internal layout's lifetime and node count, and the reference space: 15 payload digits hold < 10^15 ≈
   2^49.8 values; does the learner's layout or counter space fit, and does it last 20 years at ≈ 15M transfers a day
   (≈ 1.1 × 10^11 references)? A reference that needs 17 digits, or a 64-bit id squeezed into 16 decimal digits,
   is a finding on dimension 2 and 6.
2. **Find every place uniqueness rests on something.** Node-id assignment, the clock policy, the datacentre
   separation, the reference space split. If any rests on "NTP keeps clocks in sync", static config, or a central
   counter across the link, that is a finding on dimension 5, 6 or 7.
3. **Ask where the order comes from.** If the learner says the examiners' order is "sort by posting id", ask what
   happens to two postings on the same account minted 2 ms apart in two datacentres.
4. **Ask the follow-up probes** (at least six; adapt to what the learner wrote):
   1. "It's 08:15 on payroll morning and the datacentres have been partitioned for 15 minutes. Both are accepting
      transfers. Prove to me that no internal id and no reference is issued twice."
   2. "A host's clock steps back 800 ms in the middle of a payroll file. What does your generator do, what does the
      caller see, and how many transfers are affected?"
   3. "A process is paused for 45 seconds and wakes up. Its node-id lease expired 30 seconds ago and another process
      has it. Walk me through the next millisecond of the old process."
   4. "DC2's etcd is down for 20 minutes during a deploy. What happens to running processes and to the ones being
      deployed?"
   5. "A member reads their reference to support and swaps two digits. What happens? And how does support find the
      transfer from a correct reference?"
   6. "The same payroll file is uploaded twice by mistake. Where is the duplicate stopped, and why isn't it the id
      generator's job?"
   7. "The examiners want the account's postings in application order and proof that none is missing. Show me
      what you hand them."
   8. "We're opening a third datacentre next year. What changes in each of your two schemes?"
   9. "Why not use UUIDs for the internal ids and avoid all of this?"
5. **Assign levels** per dimension with one piece of evidence each, then the overall level with the rule below.
   Record the learner's one-line summary of the weakest dimension in the notes.

**Overall-level rule.**

- **meets**: no dimension at *below*, at most two at *approaching*, and dimensions **5, 6, 7 and 9** (internal ids,
  the reference, clocks and node ids, failure modes) each at least *meets*.
- **exceeds**: *meets* overall, with at least four dimensions at *exceeds* including two of 5, 6, 7 and 9.
- **approaching**: no more than two dimensions at *below*, and none of 5, 6, 7, 9 at *below*.
- **below**: anything else, or any design in which two live processes can share a node id without a lease and a
  deadline, a sequence can wrap, a backwards clock can re-mint ids, minting depends on the inter-datacentre link,
  or the reference doesn't fit 16 digits.

**Below meets?** Give 2–3 concrete revision tasks and have the learner redo the exercise on a variant after at
least one day (suggested: "ids for a mobile banking app that must create payees and draft transfers offline and sync
later, plus 64-bit server ids", which moves the problem onto devices whose clocks and identities you don't control).

## Recording

`sd record design C3 --self <below|approaching|meets|exceeds> --tutor <level> --notes "<one-line summary>"`
