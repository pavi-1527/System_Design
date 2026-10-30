# C4 Design exercise — A shared state store for the banking platform, across two data centres

> Time box: 45 minutes, solo, no notes. Then self-score against the rubric (≈ 10 minutes), then send it to the
> tutor for the probe session (≈ 15 minutes).
>
> This is a *different* prompt from the units' guided design on purpose. Its clients need different consistency
> per operation, it spans two data centres, and one of its operations cannot be built from quorums at all. A design
> copied from the units will score below the bar on requirements, on the consistency deep dive and on deletes.

## Prompt

> "Three platform teams each run their own small key-value cluster, and all three have had incidents. We want one
> shared key-value service, run by your team, in both of our data centres. The first three clients are the API
> gateway's idempotency keys for money movement, the web and mobile session store, and member notification
> preferences. Design it."

That is all the interviewer says. Volumes, latency targets, what each client must read after a write, what happens
when the link between the data centres fails, how long data lives, and what deletion must guarantee are for you to
ask or to state as assumptions. Write your clarifying questions down with the assumption you proceed with; the
tutor answers them during the probe session.

<details data-kind="hint">
<summary>Interviewer answers to likely clarifying questions (open only after you have written your own questions and assumptions)</summary>

- **Data centres.** Two, ≈ 25 ms round trip apart, both serving members (active-active). Each has three
  availability zones. The inter-DC link fails a few times a year for minutes to an hour.
- **Idempotency keys.** The gateway claims a key before executing a money movement (`claim(key) → mine | already
  claimed(result)`), then stores the result against it. ≈ 2,000 claims/s at peak across both DCs, ≈ 10,000
  lookups/s. Keys live 72 hours. **Two claims for the same key must never both succeed, even across DCs**; a claim
  may fail (the gateway then returns 503 and the member retries) but must not lie. p99 ≤ 50 ms for a claim.
- **Sessions.** ≈ 3M active sessions, ≈ 2 KB each; ≈ 40,000 reads/s and ≈ 8,000 writes/s ("touch" updates of the
  last-seen time) at peak. TTL 30 minutes of inactivity. A read right after login must see the session. Writes
  must keep working during an inter-DC link failure (a member must not be logged out because of it). p99 ≤ 10 ms.
- **Notification preferences.** ≈ 20M members × ≈ 1 KB; edited by members on web and mobile and by branch staff, a
  few hundred writes/s; read by the notification service (≈ 5,000 reads/s). Two edits of *different* fields at the
  same time from different channels must both survive. Members may request deletion of their preferences (an
  erasure request): gone from every copy within 30 days, provably.
- **Constraints.** Team of 6; the platform already runs a Raft-based coordination service (etcd-like) for leases
  and config, and a Cassandra-style leaderless store is the team's preferred engine. Moderate budget; cross-DC
  bandwidth is billed.

</details>

## What to produce

1. **Timestamp log.** The clock at the start of each phase (numbers by ≈ 0:10, complete diagram by ≈ 0:25, stop
   drilling by ≈ 0:40). Written during the exercise, not reconstructed.
2. **Requirements card.** Functional requirements per client, NFRs with numbers per client, out of scope, and your
   clarifying questions each with the assumption you proceeded with. Name the operation that cannot be built from
   quorums.
3. **Estimate sheet.** At most 6 numbers with the arithmetic shown and the decision each drives: per-client replica
   operations, storage with replication in both DCs, cross-DC replication bandwidth, and node count. Say which
   client sizes the cluster.
4. **API and data model.** Operations per client (including `claim`), the consistency level each uses and why,
   the record layout (versions, tombstones, TTL), and the keyspace settings (replication per DC, defaults).
5. **HLD diagram.** Both DCs, the replica placement, the coordinator path for a session read, a preferences write
   and an idempotency claim, with rates; the background processes.
6. **Three deep-dive cards** (claim → why here → cost → flip): (a) the idempotency claim across two DCs; (b)
   sessions during an inter-DC link failure and the read-after-login guarantee; (c) concurrent preference edits and
   the erasure deadline.
7. **Failure walk-through.** The inter-DC link fails for 40 minutes at peak; one node in DC-A is down for 9 hours;
   a node in DC-B returns after 35 days in repair. What does each client see, what is lost, what is duplicated,
   and what does the operator do?
8. **Metrics and alerts.** Five signals, including one for repair coverage against the tombstone grace period and
   one correctness signal for claims.
9. **Risks and open questions.** What you would validate first.

## Rubric

| Dimension | below | approaching | meets (senior bar) | exceeds |
|---|---|---|---|---|
| **1. Requirements and scope** | One consistency answer for all three clients; no questions about the inter-DC failure or deletion | Per-client needs partly captured; misses that claims need exactly one winner across DCs | Per-client NFRs with numbers; asks what each read must return after a write, what happens when the DC link fails, concurrent-edit semantics and the deletion deadline; names the claim as the non-quorum operation | Also notices the claim and the stored result are two writes and asks what happens if the gateway dies between them |
| **2. Estimation** | No numbers, or unit errors ≥ 8×, or numbers unused | Client rates only; replica multipliers or the second DC missing | Replica operations per client with `R`/`N` per DC, storage with replication in both DCs, cross-DC bandwidth, a node count; says which client sizes it; marked ≈ | Sanity-checks per-node data vs rebuild time and prices the cross-DC bandwidth |
| **3. API and data model** | Plain `get`/`put` for everything; deletes remove rows | Consistency levels named but not justified per client; no context for preferences | `claim` with explicit outcomes; per-client levels (`LOCAL_QUORUM` for sessions, a consensus path for claims); per-field versions or a CRDT map for preferences; tombstones and TTL; `N` per DC | Specifies `Timeout` semantics per operation and how the gateway retries a claim safely |
| **4. High-level design** | A path cannot be traced; boxes unjustified | Paths traceable but the second DC or the claim path is hand-waved | Ring per DC with replicas across AZs, coordinator paths for all three clients with rates, the consensus path for claims drawn, background processes listed; every box forced by a requirement | Separates keyspaces (or clusters) per client with a blast-radius argument, or argues convincingly why not |
| **5. Idempotency claims (deep dive a)** | Quorum or LWW "claims"; check-then-write | Consensus named but within one DC only, or the cross-DC conflict unaddressed | A single serialisation point per key across DCs (consensus spanning DCs, or a home DC per key with forwarding), ≈ latency per claim from the 25 ms RTT, behaviour during a DC link failure (claims fail safely), result stored with the claim | Discusses the claim-then-execute gap (a claimed key with no result after a crash) and its recovery |
| **6. Sessions under failure (deep dive b)** | Sessions on the consensus path, or strict cross-DC quorums | `LOCAL_QUORUM` named, but read-after-login or TTL not addressed | Writes at `LOCAL_QUORUM` (sloppy within the DC acceptable), read-after-login at `LOCAL_QUORUM` in the same DC, async cross-DC replication, LWW per session, TTL as expiring writes; the link-failure behaviour stated | Handles a member switching DCs mid-session (sticky routing or a cross-DC read on miss) with numbers |
| **7. Preferences and deletion (deep dive c)** | LWW on the whole record; deletes as row removal | Per-field merge but deletion deadline or tombstone grace unaddressed | Per-field LWW or a CRDT map so concurrent field edits both survive; erasure as tombstones with a grace period and repair schedule that complete within 30 days; rebuild-don't-rejoin for long-absent nodes | Proves the 30-day deletion (repair completion per range, backup handling) in terms an examiner would accept |
| **8. Failure modes and operations** | None, or "add replicas" | Some failures named without client-visible effect | All three scenarios walked with what each client sees, loss/duplication, detection and operator action (including hints vs repair and the 35-day node) | Game-day plan: DC-link failure drill and a deletion audit |
| **9. Communication and trade-offs** | Disorganised; no flip conditions | Trade-offs for some decisions; time slips > 5 min | Driven by the candidate; checkpoints met within ≈ 2 min; three cards with measurable flips; says what to validate first | Anticipates probes; gives a migration plan from the three existing clusters |

## Tutor critique protocol

Read the artifact first, then run the conversation. Do not reveal the rubric rows until the learner has
self-scored every dimension with one line of justification each.

1. **Check the numbers (≈ 3 min).** Recompute the replica operations per client from the learner's own `R`/`W`/`N`
   (sessions dominate: ≈ 40k reads/s × `R` 2 ≈ 80k, plus ≈ 8k writes/s × 3 replicas × 2 DCs ≈ 48k, so ≈ 130k
   replica ops/s across both DCs), the storage (sessions ≈ 6 GB, preferences ≈ 20 GB logical, idempotency keys
   ≈ 2,000/s × 72 h ≈ 5 × 10^8 keys), and the cross-DC bandwidth. The punchline the learner should reach: this is a
   small cluster sized by failure domains and operations, not by data. Check specifically for a design that puts
   sessions on the consensus path, or claims on a quorum.
2. **Check the one non-quorum operation.** Where is "exactly one winner" enforced for a claim? If the answer is a
   quorum, LWW, or a consensus group inside one DC with both DCs accepting claims, that is a finding on dimension 5.
3. **Walk the link failure.** Ask the learner to say, client by client, what happens in each DC during a 40-minute
   inter-DC link failure, and what converges afterwards and how.
4. **Ask the follow-up probes** (at least six; adapt to what the learner wrote):
   1. "Two gateways in different DCs claim the same idempotency key 5 ms apart. Walk me through both requests.
      Which one wins, how long did each take, and what does the loser return?"
   2. "The DC link is down. What happens to claims, to session writes, to preference edits?"
   3. "A member logs in on mobile in DC-A, and the next request lands in DC-B 10 ms later. Do they get logged out?"
   4. "A member changes their email channel on the web while branch staff change their SMS opt-out. Show me the
      record after both writes, in both DCs."
   5. "An erasure request arrives. Prove to me that the preferences are gone from every replica in both DCs within
      30 days. What could bring them back?"
   6. "A node in DC-B comes back after 35 days in repair. What does it do at startup, and what do you do?"
   7. "The gateway claims a key, then crashes before storing the result. The member retries. What happens?"
   8. "Why one shared service rather than three? What is the blast radius of a bad config push?"
   9. "What does the cross-DC replication cost per month, and which client drives it?"
5. **Assign levels** per dimension with one piece of evidence each, then the overall level with the rule below.
   Record the learner's one-line summary of the weakest dimension in the notes.

**Overall-level rule.**

- **meets**: no dimension at *below*, at most two at *approaching*, and dimensions **5, 6, 7 and 8** (claims,
  sessions under failure, preferences and deletion, failure modes) each at least *meets*.
- **exceeds**: *meets* overall, with at least four dimensions at *exceeds* including two of 5, 6, 7 and 8.
- **approaching**: no more than two dimensions at *below*, and none of 5, 6, 7, 8 at *below*.
- **below**: anything else, or any design in which idempotency claims can have two winners, deletes are row
  removals, or `R + W > N` is presented as making claims safe.

**Below meets?** Give 2–3 concrete revision tasks and have the learner redo the exercise on a variant after at
least one day (suggested: "design the device-binding store for the mobile app: each member may bind at most three
devices, unbinding must take effect everywhere within a minute, and the store must keep accepting bindings during
an AZ outage", which puts a counting constraint, a revocation deadline and availability in tension).

## Recording

`sd record design C4 --self <below|approaching|meets|exceeds> --tutor <level> --notes "<one-line summary>"`
