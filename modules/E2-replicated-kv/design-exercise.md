# E2 Design exercise — the design rationale for your replicated KV store

> Time box: part A 60 minutes, solo, no notes, **before** you write any lab code (Unit 1, session 1). Part B ≈ 60
> minutes after M4 and M5 are green, with your test and break-it output in front of you. Then self-score, then send
> both parts to the tutor.

This is the Track E version of a design exercise: instead of designing a system you'll never run, you write the
rationale for the one you're about to build, commit to predictions about how it fails, and then grade those predictions
against reality. The point is calibration. A design doc whose predictions all came true was either very good or very
vague; the rubric can tell which.

## Prompt

Part A (before coding):

> A credit union's ledger service needs a replicated key-value store for posting balances and transfer states. One
> region, three machines. Writes ≈ 300/s at peak, reads ≈ 3,000/s. "A transfer we said was posted must never
> disappear" and "a member always sees their own change straight after making it". Ops wants failover without a human
> for machine failures. You will build it as described in [spec.md](spec.md), but the spec doesn't say *why*. Write
> the why, and predict how it fails.

Part B (after M4 and M5): fill in what actually happened, explain every difference, and revise the design where reality
proved you wrong.

## What to produce

A Markdown document (keep it outside `modules/`, e.g. in your notes or `_mocks/`, or paste it to the tutor) with these
sections:

1. **Requirements and numbers.** Functional and non-functional requirements with numbers: write and read rates, the
   value size you assume, storage per year, the RPO and RTO you're promising *per failure type* (process crash, machine
   loss, network partition between two machines, coordinator loss).
2. **What an acknowledgement means.** For async and semi-sync: on which disks the entry is when the client gets `200`,
   and which failures that survives. Which mode do you choose for the ledger and why? What's the latency cost (use
   `docs/NUMBERS.md` round trips and a stated fsync assumption)?
3. **On-disk format and recovery.** The WAL record, what recovery does with each crash state, how `meta.json` is
   replaced, what survives a restart.
4. **Failover and fencing.** How failure is detected (numbers: interval, timeout, threshold), how long the coordinator
   waits and why, how the successor is chosen, what the epoch fences and where each fence is checked, what happens to
   a returning old leader's divergent entries. Name every single point of failure.
5. **Predictions table.** For each scenario F1–F12 in [spec.md §8](spec.md#8-failure-injection-scenarios-and-the-predicted-behaviour),
   your predicted outcome *with a number where one exists*: failover time, acknowledged writes lost, lag in records
   and seconds after 400 ms of partition, the semi-sync response code and time, how long a token read waits.
6. **Part B — predicted vs observed.** The same table with an "observed" column filled from your test logs and the three
   break-it runs (`WriteCostByMode`, `FailoverTimeVsLease`, `LagUnderLoad`), and a "why different" column. At least one
   row must explain a difference you didn't expect.
7. **Part B — revisions.** What you'd change for production, ranked: at minimum the coordinator SPOF, group commit, the
   `(epoch, index)` token, and one thing of your own. For each: the trade-off sentence (claim → because → cost → flip,
   `docs/INTERVIEW_FRAMEWORK.md`).

## Rubric

| Dimension | below | approaching | meets (senior bar) | exceeds |
|---|---|---|---|---|
| Requirements & numbers | no numbers, or RPO/RTO as one number for everything | rates and sizes present; RPO/RTO not split by failure type | rates, storage, RPO and RTO stated per failure type, each tied to a design decision | also derives what breaks first at 10× (fsync-bound writes, fetch RTT-bound lag) with numbers |
| Acknowledgement semantics | "replicated" without saying where the bytes are | correct for one mode, vague for the other | exact disks-at-ack for both modes, the failures each survives, the latency cost, and a justified choice for the ledger | discusses the fallback-to-async question and when "refuse" vs "downgrade" flips, with monitoring |
| Durability & recovery | ack before fsync, or no torn-tail handling | fsync before ack; recovery hand-wavy | every crash state named and handled; fsync of truncation and directory; atomic `meta.json`; restart semantics (meta wins) | quantifies recovery time vs log size and proposes snapshots/segments with the follower catch-up consequence |
| Failover & fencing | promotes on first missed heartbeat, no epochs | epochs present; lease wait or delayed-renewal problem missing | detection math, wait for latest *sent* lease, (last_epoch, last_index) selection, every fence and where it's checked, divergent-suffix truncation, SPOFs named | proposes the consensus-backed fix and states its cost; handles cross-machine clock bounds explicitly |
| Prediction quality (part B) | predictions missing or not compared | compared, but differences unexplained | every row has a number, a comparison and an explanation; at least one honest miss analysed | uses a miss to find a real bug or a missing test, and adds it (scratch test) |
| Trade-offs & revisions | none | listed without costs | ranked revisions, each with claim → because → cost → flip | ties each revision to the incident it prevents and its operational cost |

**Overall rule:** "meets" requires meets or better on every dimension, none at below. "Exceeds" requires meets everywhere
and exceeds on at least three dimensions, two of them among *Acknowledgement semantics*, *Failover & fencing* and
*Prediction quality*.

## Tutor critique protocol

1. Read part A first, alone, and note every prediction. Then read part B. Check the observed numbers against the
   learner's own `sd lab E2` and break-it output (ask them to paste it), not against the reference.
2. Ask at least three of these probes, one at a time, before scoring:
   - "Your leader acknowledged a write and died. Walk me through exactly which disks have it, in each mode, and who gets
     promoted."
   - "The coordinator can't reach the leader but clients can. What happens to writes in the next two seconds, and is
     anything lost?"
   - "A renewal timed out on the coordinator's side. Why does the coordinator still count it as granted?"
   - "Your old leader comes back with two entries the new leader doesn't have. Where do they go, and how would the
     compliance team find out?"
   - "A semi-sync write returned 503 and the client retried. How many transfers exist?"
   - "The follower says its lag is zero. Why don't you believe it?"
   - "What's the first thing to break at 10× the write rate, and what's the fix?"
   - "List every single point of failure. Which one is a correctness risk and which an availability risk?"
3. Score each dimension with evidence (quote the document or the answer to a probe). For *Prediction quality*, a
   learner who predicted "≈ 1 s failover" and observed 1.3 s with a correct explanation (lease dominates) scores higher
   than one who predicted "fast" and observed anything.
4. Name what would move each dimension up one level. Below meets: two or three revision tasks, and the learner revises
   the document in a later sitting (not the same one).

## Recording

`sd record design E2 --self <below|approaching|meets|exceeds> --tutor <level> --notes "<one-line summary>"`
