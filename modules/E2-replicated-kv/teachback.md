# E2 Teach-back — reflection checklist

## Prompt

Write a reflection on the store you built, for a mid-level engineer on your team who is about to be on call for it and
has read the Track A modules but never seen this code. 600–1,000 words, your own words, one diagram allowed (Mermaid or
ASCII).

For every major choice in your build, say **what you chose, which Track A idea it implements, what the tests or
break-it runs showed, and when you'd choose differently.** Cover at least: the WAL and when you ack; pull replication
and the fetch-as-ack; async vs semi-sync; how lag is measured; failure detection and the lease wait; epochs and every
place they're checked; divergent-log truncation; read-your-writes. End with the two things you would tell them to watch
on the first night on call.

Don't paste unit text or code. Link the Track A sections you're tying back to.

## Evaluator checklist

Must-hit (all required for pass):

- [ ] **Ack after fsync**, and *why* a process-kill test can't prove durability (the page cache survives a `SIGKILL`),
      tied to [A2 Unit 2 §4](../A2-storage/unit-2-write-paths.md#4-durability-and-the-write-ahead-log).
- [ ] **Recovery** keeps the longest valid, contiguous prefix and cuts (durably) at the first torn, corrupt or
      out-of-sequence record, with the reason ordering matters more than individual valid records.
- [ ] **The fetch position is the acknowledgement**, valid only after a log-matching check, and the follower fsyncs
      before asking for more, tied to [A4 Unit 1 §1](../A4-replication-partitioning/unit-1-replication.md#1-leaderfollower-replication-and-log-shipping).
- [ ] **Async vs semi-sync** stated as disks-at-ack and RPO, including the observed acknowledged-write loss in async and
      the 503 "outcome unknown" in semi-sync, tied to [A4 Unit 1 §2](../A4-replication-partitioning/unit-1-replication.md#2-synchronous-asynchronous-and-semi-synchronous-replication-failover).
- [ ] **Lag measured on the leader** in records and seconds (from entry timestamps, one clock), and why the follower's
      own view reads zero during a partition.
- [ ] **Failover waits for the latest lease *sent***, not acknowledged, and why (buffered renewals, the zombie test),
      tied to [A5 Unit 3 §3](../A5-consistency/unit-3-consensus-leases.md#3-leases-and-fencing-tokens).
- [ ] **Epochs as fencing tokens**, with each check named: lease epoch at the leader, admin requests at every node,
      fetches at a stale leader, and persisted in `meta.json` across restarts.
- [ ] **Divergent suffixes are truncated on the follower** by `(epoch, index)` matching, and those writes are the data
      lost in the incident.
- [ ] **Read-your-writes** with an index token, a bounded wait and a redirect to the advertised leader, tied to
      [A4 Unit 1 §3](../A4-replication-partitioning/unit-1-replication.md#3-replication-lag-anomalies-and-how-to-fix-them),
      and that it is a session guarantee, not linearizability ([A5 Unit 1 §1](../A5-consistency/unit-1-consistency-quorums.md#1-consistency-models-what-a-read-may-return)).

Should-hit (at least 5 of 8):

- [ ] Failure detection is a timeout and therefore a guess; detection time = `fail-after × (interval + timeout)`, with
      the observed failover time ([A7 Unit 1 §1](../A7-reliability/unit-1-timeouts-retries.md#1-timeouts-and-deadline-budgets)).
- [ ] The successor is chosen by `(last_epoch, last_index)`, with an example of why index alone is wrong.
- [ ] The coordinator is a single point of failure for failover (and, with leases, for writes), and a majority-based
      design removes it ([A5 Unit 3 §1](../A5-consistency/unit-3-consensus-leases.md#1-why-consensus-needs-a-majority)).
- [ ] Leases depend on bounded clock drift; on one host it's trivially true, across machines it needs a margin
      ([A5 Unit 2 §3](../A5-consistency/unit-2-time-ordering.md#3-hybrid-and-bounded-uncertainty-clocks)).
- [ ] A semi-sync 503 or a connection reset is an ambiguous outcome that needs an idempotency key, not a blind retry
      ([A1 Unit 3 §2](../A1-networking-api/unit-3-idempotency-auth.md#2-idempotency-keys-end-to-end)).
- [ ] Throughput is fsync-bound without group commit, with the measured number from `WriteCostByMode`.
- [ ] Replication rate is bounded by batch / RTT with one fetch in flight, with the `LagUnderLoad` observation.
- [ ] The `(epoch, index)` token gap: an index-only token can be falsely satisfied after a failover.

Red flags (any one = revise):

- [ ] Says semi-sync "guarantees no data loss" without the "while the acked follower survives and is the one promoted"
      condition.
- [ ] Says the lease alone prevents split brain (no mention of the check-then-act race and the follower-side fence).
- [ ] Treats a 503 or timeout as "the write didn't happen".
- [ ] Claims `kill -9` tests prove durability against power loss.
- [ ] Describes read-your-writes as linearizable or "strongly consistent".
- [ ] Copies text from the units or the reference solution instead of explaining in their own words.

## Recording

`sd record teachback E2 --result <pass|revise> --notes "<missed items>"`
