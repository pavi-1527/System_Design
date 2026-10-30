# A5 Teach-back

## Prompt

A mid-level engineer on your team is about to build the scheduled-transfers and payroll-file service. Their plan, in one
breath: "We'll run two instances and take a Redis lock so only one runs the batch. Records are replicated across three
nodes, we'll write to two and read from two so it's consistent, and when two nodes disagree we keep the one with the later
timestamp. Callbacks come through a broker that says it's exactly-once. If the primary dies, the standby takes over after
ten seconds without a heartbeat."

Explain to them, in your own words, **which of those five decisions are safe for money movement, which are not, and why**:
what "consistent" actually promises (and what a quorum does not), what timestamps can and cannot order, what
"exactly-once" can honestly mean, what a lock needs besides an expiry, and why failover needs more than a timeout.
Say what you would put in each place instead, and where the original choice would be fine.

Form: written, 500–900 words, in your own words, may include one diagram (Mermaid or a sketch). No copying from the unit
text: if a sentence from the course appears verbatim, the evaluator will ask you to rephrase it. Write for that engineer,
not for an examiner: concrete beats complete, and one real number beats three adjectives.

## Evaluator checklist

Must-hit (all required for pass):
- [ ] **Quorum**: states the overlap rule (`R + W > N`, strictly greater, by pigeonhole) **and** at least two things it does
      not give: not linearizable (a read can go new-then-old while a write is in flight), a failed or timed-out write may
      still be visible (no rollback), sloppy quorums break the overlap, concurrent writes need versions or lose data.
- [ ] **Timestamps**: explains that last-write-wins on wall-clock time silently discards a causally later write when clocks
      disagree (skew, NTP steps), and says what to use instead (version vectors / siblings with a merge, a single writer or
      sequencer, or a hybrid logical timestamp), naming when LWW is acceptable.
- [ ] **Exactly-once**: states that a network or broker cannot deliver exactly once (the sender cannot tell a lost message
      from a lost ack) and that the achievable property is exactly-once *effect* = at-least-once delivery plus an
      idempotent or deduplicating consumer, with the dedupe record **atomic with the effect** (same transaction, or an
      idempotency key to the downstream plus an attempt record and reconciliation).
- [ ] **Lock/lease**: explains the pause scenario (holder stalls, lease expires, another instance takes over, the stalled
      holder resumes and writes) and the remedy: a fencing token issued with each grant and **checked at the resource**,
      atomically with the write; the TTL is for liveness, not safety.
- [ ] **Failover**: explains why two nodes with a heartbeat timeout produce split-brain (a partition looks like a dead peer)
      and that safe automatic failover needs a majority decision (consensus, three failure domains or a witness) and
      fencing of the old primary.
- [ ] **CAP / consistency framing**: states CAP correctly (during a partition choose linearizability or availability; not
      "pick two"; decided per operation) and distinguishes at least strong consistency from session guarantees such as
      read-your-writes, with one example where each is the right tool.

Should-hit (at least 5 of 8):
- [ ] Gives at least three numbers from the module and uses them to make a point (for example ≈ 70 ms against ≈ 1–2 ms
      commit latency and the hot-account consequence; ≈ 5.5% of causally later writes lost at 50 ms of skew; the duplicate
      credits with and without a same-transaction dedupe record; the odd/even cluster availability).
- [ ] Mentions PACELC: the latency-versus-consistency trade-off that exists without a partition.
- [ ] Explains majorities: any two overlap, so at most one side of a partition can commit; `2f + 1` nodes tolerate `f`
      failures; why four nodes are not better than three.
- [ ] Describes Raft's key safety rules in a sentence or two each: terms, one vote per term, vote only for an up-to-date
      log, commit only entries from the current term by counting replicas (and the no-op), randomised election timeouts.
- [ ] Shows a version token (or similar) as the way to give read-your-writes from replicas, and what it costs.
- [ ] Says where dedupe records are retained and for how long, and that identity (the event ID) must be assigned by the
      producer, not per delivery attempt.
- [ ] Proposes how to order the audit trail without trusting wall clocks (per-entity sequence, hybrid logical clock).
- [ ] Names the safety net around all of it: reconciliation against the core and vendors, and idempotent
      `(account, period)` postings for the batch.

Red flags (any one = revise):
- [ ] Says CAP means "pick two", describes a "CA" distributed system, or says CP means "down during any partition".
- [ ] Claims `R + W > N` makes the store linearizable or "strongly consistent" with no caveat, or treats `R + W = N`
      as sufficient.
- [ ] Says NTP keeps clocks close enough for last-write-wins to be safe, or orders cross-machine events by timestamp
      without qualification.
- [ ] Says the broker or the network provides exactly-once delivery, or places the dedupe record in a different store
      from the effect (or after it) without noticing the crash window.
- [ ] Says a lease or a Redis lock guarantees mutual exclusion on its own, or proposes "check the lease, then write" as the
      protection.
- [ ] Treats two-node heartbeat failover as safe, or adds a fourth node "for safety".
- [ ] Recommends one consistency level for everything (all linearizable across regions, or all eventual) without tying it
      to the cost of an error.

## Recording

`sd record teachback A5 --result <pass|revise> --notes "<missed items>"`
