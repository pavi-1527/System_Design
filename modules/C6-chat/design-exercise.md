# C6 Design exercise — The credit-union secure message centre

> Time box: 45 minutes, solo, no notes, timed like a real interview (numbers by 0:10, end-to-end diagram by 0:25,
> stop drilling at 0:40). Then self-score against the rubric (≈ 10 min), then send the write-up to the tutor for the
> probe session. Use the structure from [`docs/INTERVIEW_FRAMEWORK.md`](../../docs/INTERVIEW_FRAMEWORK.md) and the
> numbers from [`docs/NUMBERS.md`](../../docs/NUMBERS.md), from memory.

This is a *variant* of the module's case study on purpose. Every mechanism from the units applies (gateways and a
registry, one writer per conversation, idempotent sends, sync from a cursor, receipts as cursors, a fan-out
threshold), but the scale is ≈ 100× smaller and the hard requirements have moved: conversations change hands
between agents, receipts are compliance evidence, tenants must be isolated, and one kind of message goes to two
million people at once. If you reproduce Unit 1's design box for box, with 42 gateways and a wide-column store
sized for 70k writes/s, you will score "approaching": the rubric rewards re-deriving the decisions from *these*
numbers.

## Prompt

> "We run the digital-banking platform for ≈ 40 credit unions, ≈ 12 M members in total. Design the in-app
> **secure message centre**: members start conversations with their credit union from the mobile app or the web,
> agents answer from a web console, and conversations can be transferred between agents. Credit unions can also
> send secure notices, such as a fee-schedule disclosure, to all their members. Members can attach documents.
> Everything must be kept for seven years, and the credit union must be able to prove that a member received and
> opened a given message."

That is all the interviewer says up front. Ask the clarifying questions you need (write them down with the answer
you'd assume if told "you decide"), and drive.

Things an interviewer would answer if asked (don't read until you've written your questions):

<details data-kind="hint">
<summary>Interviewer's answers to common clarifying questions</summary>

- Members: ≈ 12 M across ≈ 40 credit unions; the largest has ≈ 2 M members, the smallest ≈ 20k. Many members use
  two devices (phone and web).
- Conversations: ≈ 250k started per day platform-wide, ≈ 12 messages each including system messages ("transferred
  to Priya", "conversation closed") → ≈ 3 M messages/day. Peaks: Monday mornings and incident days (a card-network
  outage) run ≈ 5× the average for an hour or two.
- Online at once: at the busiest minute ≈ 400k members have the app open; ≈ 6,000 agents are logged in across all
  credit unions, each handling up to 6 conversations at a time.
- Agents: one queue per credit union and topic; a conversation can be transferred to another agent or escalated to
  a supervisor; some credit unions put a bot first. Agents and supervisors see presence and typing for the member
  they're talking to; members see "agent is typing".
- Latency: member ↔ agent delivery p95 ≤ 1 s when both are online; send acknowledged in p99 ≤ 300 ms server-side.
  Offline members get a push that says only "You have a new secure message" (never the content) and see the
  message when they open the app.
- Secure notices: ≈ 20 per month platform-wide, to all of a credit union's members (up to ≈ 2 M) or to a segment
  (≈ 10k–200k). For each recipient the credit union must be able to show when it reached a device and when the
  member opened it. Notices can't be replied to.
- Attachments: ≈ 3% of messages, ≈ 400 KB on average (PDF statements, photos of documents); malware-scanned before
  the other side can open them.
- Compliance: everything retained 7 years; legal hold per member or per conversation overrides retention deletes;
  every agent view of a conversation is audit-logged; a member can hide a message from their own view but the
  record stays.
- Tenancy: data isolated per credit union, with per-tenant encryption keys at rest; one credit union's notice or
  incident burst must not slow another's agent conversations. The platform bills per tenant.
- Availability: 99.95% for messaging; an acknowledged message is never lost. End-to-end encryption is not wanted
  (agents, supervisors and compliance must read conversations).
- Out of scope unless you choose otherwise: bot NLP, voice and co-browsing, the push dispatcher's internals (C7),
  full-text search beyond "find the conversations for member X".

</details>

## What to produce

1. **Timestamp log**: the clock time at the start of each phase (evidence for time management).
2. **Requirements card**: functional (3–5), non-functional *with numbers* (scale, delivery latency, ack latency,
   availability, retention and legal hold, isolation), explicit out-of-scope, and your clarifying questions with
   the assumptions you proceeded with.
3. **Estimate sheet**: at most 6 numbers with arithmetic and units, each with the decision it drives: messages/s
   (average and incident peak), concurrent connections and gateway count, the reconnect wave of a gateway deploy,
   text and attachment storage over 7 years with replication, and the write and push cost of a 2 M-member notice.
4. **Protocol and API sketch**: the send frame and its ack (idempotent), catch-up after offline, the agent
   console's view of a queue, transfer of a conversation, sending and withdrawing a notice, attachment upload.
5. **Data model**: access patterns first; entities, keys and which store holds what: the per-conversation log,
   participants over time (who could see what, when), receipt cursors and the receipt audit trail, notices and
   their audiences, attachments, legal holds; the tenant on every key.
6. **HLD diagram**: the member ↔ agent send path, the reconnect/sync path, the notice path, and the attachment
   path, with rates on at least three arrows and a one-line justification per box.
7. **Deep dives (2–3)**, each with alternatives, a cost and a flip condition:
   - ordering and delivery across a transfer: what the new agent sees, what the member's two devices see;
   - receipts as compliance evidence: what is written, by whom, when, and how you answer the auditor;
   - the notice fan-out: write or read, the number that decides it, pushes, and per-member receipts at 2 M;
   - the connection tier at this scale: how many gateways and why, deploys during business hours.
8. **Failure walkthrough**: a gateway holding 1,500 agents dies at 10:00 on a Monday; a member on a train retries a
   "please stop that payment" message three times; a credit union sends a 2 M-member notice during another credit
   union's incident peak.

## Rubric

| Dimension | below | approaching | meets (senior bar) | exceeds |
|---|---|---|---|---|
| Requirements & scope | Draws immediately; receipts and retention treated as features | Lists features; NFRs vague; compliance mentioned but not specified | Latency, ack, availability and retention with numbers; "prove received and opened" restated as a precise requirement (per device delivered, per member opened, with timestamps, 7 years); legal hold, audit of agent views and tenant isolation named; explicit out-of-scope; assumptions written | Names the two NFRs that shape this design (receipt evidence and tenant isolation, not scale) and negotiates the rest; asks whether the app holds a socket all the time or only on the messaging screen |
| Estimation | Missing, or ≥ 10× off on a number the design depends on | Copies the guided design's numbers; no incident peak; attachments ignored | ≤ 6 numbers with units: ≈ 35 messages/s average, ≈ 175/s peak; ≈ 0.4 M connections ≈ 12 GB; gateways sized by blast radius and AZs, not capacity; text ≈ 11.5 TB vs attachments ≈ 90 TB over 7 years; a 2 M notice ≈ 2 M writes ≈ two-thirds of a day's messages in one burst; each tied to a decision | Shows the scale makes a partitioned relational store comfortable and spends the saved complexity on isolation and audit; sizes the "socket only on the messaging screen" option and what it does to connections |
| Protocol, API & data model | Non-idempotent send; timestamps as cursors; tenant missing from keys | Client id and cursor mentioned; participants or receipts modelled loosely | Send frame with a client message id deduped on (tenant, conversation, sender, client id) atomically with the insert; ack carries seq; `after_seq` catch-up; messages keyed (tenant, conversation, seq); participants as time-ranged rows (agent joined at seq N, left at seq M); receipt cursors plus an append-only receipt event log; notices keyed by audience; attachment as a reference to scanned object storage | Transfers and closes are system messages in the same log, so membership changes are ordered with the conversation; hide-from-my-view modelled as a per-member flag, never a delete; legal hold as a flag the retention job must check |
| High-level design | A path can't be traced; no durable write before the ack | Paths present but boxes unjustified, or the guided design copied at full size | Gateways (a few, across AZs) + registry, stateless chat service, store as truth, best-effort bus, push via C7 for offline members, agent console on the same protocol, attachment upload + malware scan out of the message path; ack after the durable write; connect then sync; one message walked end to end | Per-tenant lanes or shards drawn into the path; the notice path drawn as its own lane that cannot touch agent-chat capacity |
| Connections & deploys | Polling at scale, or sockets in the business-logic tier with no drain plan | Socket tier present; deploys and reconnects hand-waved | Heartbeats with a registry TTL; bounded per-connection queues with drop-and-resync; rolling drains with reconnect hints and jittered backoff, sized with numbers (e.g. ≈ 100k per box over ≈ 60 s ≈ 1.7k/s); agent consoles resume and resync without losing their place; token refresh in-band | Uses the small scale to choose resumable sessions and business-hours deploys deliberately; considers long polling for locked-down agent desktops and says when it flips |
| Ordering, idempotency & handoff | Order by timestamps; transfers on a side channel; retries duplicate | Per-conversation seq named, but the transfer or multi-device case not walked through | One writer per conversation assigns dense seqs with the durable write; the transfer is a seq'd system message, so the new agent syncs from the conversation's start (or from the transfer) and the member's devices render the same order; client dedupe by seq; the train retry returns the original seq; at-least-once + two dedupe points stated as the honest "exactly once" | Agent permissions follow the participants' time ranges (an agent sees what was in the conversation while they could see it, by policy); a supervisor's silent view is an audit event, not a participant change |
| Receipts, audit & compliance | "Read receipts" as a boolean per message, or "the push was sent" as proof | Cursors described, but nothing durable an auditor could query; preview vs open not distinguished | Delivered cursor per device, read cursor per member, monotonic; every cursor advance appended to a receipt log with timestamps and retained 7 years, so "did member X open notice Y, when" is a lookup; opening the conversation, not a notification, is "opened"; agent views audit-logged; legal hold and retention described | Separates *evidence* (append-only, immutable, per-tenant keys) from *UI state* (cursors, counts); reasons about clock source for the evidence timestamps and what is shown to an examiner |
| Notice fan-out & sync | Writes 2 M inbox rows and sends 2 M pushes synchronously | Pull or hybrid named without the number, or receipts at 2 M ignored | Notices fan out on read (a per-tenant or per-audience notice list merged into the member's inbox on open), with the number that decides it; pushes via C7 rate-limited per tenant and spread over time; per-member receipt rows written when each member's device fetches and opens, not at send; reconnect is one inbox-cursor call then per-conversation `after_seq` | Segment audiences evaluated at send and frozen (the audit needs "who was meant to get it"); notice withdrawal as a tombstone that still keeps the receipts |
| Tenancy, failure modes & ops | None, or "add replicas" | Generic metrics; isolation only as a column | Tenant on every key and a shard or lane strategy so one tenant's burst can't slow another's chats; per-tenant rate limits on notices; per-tenant keys at rest; dashboards for ack p99, delivery lag, reconnects/s, resync rate and receipt-log lag per tenant; the three failure walkthroughs answered with numbers | Cost per tenant (attachments dominate storage); migration from an existing REST + polling inbox without losing receipts |
| Communication & time | Checkpoints missed by > 8 min; no trade-off table | Finishes but a phase overruns; trade-offs lack flip conditions | Numbers by ≈ 0:10, diagram by ≈ 0:25, stops at 0:40; drives; a trade-off table with ≥ 3 rows and flip conditions | Anticipates probes; notes what was deliberately cut and why |

**Overall "meets"** requires *meets* or better on **Ordering, idempotency & handoff** and **Receipts, audit &
compliance** (the two places where this design is either defensible to an examiner or not), and no dimension at
*below*. **Overall "exceeds"** requires *meets* everywhere and *exceeds* on at least three dimensions. Otherwise
overall is *approaching* (or *below* with two or more dimensions at *below*).

## Tutor critique protocol

1. **Check the artefacts before probing (≈ 3 min).** Timestamp log present; slips at 0:10 / 0:25 / 0:40 computed.
   Every NFR has a number. The estimate sheet's arithmetic, all ≈:
   - Messages: 3 M/day ÷ 86,400 ≈ 35/s average; × 5 ≈ 175/s at an incident peak; deliveries ≈ × 3 (member's
     devices + agent + supervisor) ≈ 500/s. A partitioned relational store handles this comfortably (≈ 1k–10k
     small TPS per primary), so the learner should *not* reach for the guided design's wide-column store sized for
     70k/s.
   - Connections: ≈ 400k members + ≈ 6k agents ≈ 0.4 M, ≈ 30 KB each ≈ 12 GB in total. At ≈ 500k per box one
     gateway would do; the count (≈ 4–6 across ≥ 2 AZs, ≈ 100k each) comes from blast radius and AZ failure, not
     capacity. If the app opens a socket only on the messaging screen, concurrency drops by an order of magnitude
     (`TODO-VERIFY: share of app-open time spent on the messaging screen; the design should treat it as an
     assumption to measure`).
   - Reconnect wave: one ≈ 100k gateway drained over ≈ 60 s ≈ 1.7k/s; the whole fleet in 3 minutes ≈ 2.2k/s. Not a
     storm at this scale, so the deploy question is about agents mid-conversation, not auth capacity.
   - Storage: text ≈ 3 M × ≈ 0.5 KB (body, metadata, audit fields) ≈ 1.5 GB/day ≈ 550 GB/year ≈ 3.8 TB raw over 7
     years, ≈ 11.5 TB at 3×. Attachments ≈ 3% × 3 M × 400 KB ≈ 36 GB/day ≈ 13 TB/year ≈ 90 TB over 7 years in
     object storage: ≈ 8× the replicated text, so tiering and per-tenant cost attribution are about attachments.
   - Notice: a 2 M-member notice written to every inbox is ≈ 2 M writes in a burst, about two-thirds of a whole
     day's 3 M message writes in one event, which is the number that decides fan-out on read. Pushes for it, rate
     limited to ≈ 500/s per tenant, take ≈ 70 minutes, which is acceptable for a disclosure and must be said.
   An error ≥ 10× on messages/s, connections or the notice write cost caps *Estimation* at "approaching".
2. **Self-score first.** Ask for a level per dimension with a one-line justification before saying anything.
3. **Probe (≈ 10 min).** Ask at least six, including the ones marked (required); one at a time; push one rung
   further on vague answers.
   1. **(required)** "Priya transfers the conversation to Sam while the member is typing on their phone and has the
      web app open. Show me the exact sequence every party sees."
      *Meets:* the transfer is a system message with its own seq from the conversation's single writer, so it is
      ordered against the member's messages; Sam's console joins and syncs `after_seq` from the transfer point (or
      the start, per policy); both member devices receive the same seqs and dedupe by seq; nothing travels on a
      side channel; a message the member sends during the transfer lands before or after it, never both.
   2. **(required)** "An examiner asks: prove that member X received and opened the fee disclosure sent 14 months
      ago, and tell me when."
      *Meets:* the notice is a message with a seq in the member's notice view; device delivered cursor and member
      read cursor advances are appended to a receipt log (tenant, member, conversation or notice, seq, device,
      kind, server timestamp) retained 7 years; "opened" means the member viewed it, not that a push or
      lock-screen preview rendered; the answer is one query on that log. A cursor alone answers "up to which seq",
      not "when".
   3. **(required)** "The largest credit union sends a notice to all 2 M members at 09:00 on a Monday. Walk me
      through the writes, the pushes and the receipts, with numbers."
      *Meets:* no 2 M-row burst: one notice row plus a per-audience list members merge on open; pushes through C7,
      rate-limited per tenant and spread (≈ an hour at ≈ 500/s), content-free; receipt rows written as each
      member's device fetches and opens over the following days; agent chats of every tenant unaffected because
      the notice path is its own lane.
   4. **(required)** "A member on a train sends 'please stop that payment', it times out, and the app retries
      three times. What does the agent see, and what does the member see?"
      *Meets:* one client message id across all attempts; dedupe on (tenant, conversation, sender, client id)
      atomically with the insert; every retry gets the original seq in its ack; the agent sees it once; the member's
      pending bubble turns into "sent" once; the dedupe key lives at least as long as the app's retry window.
   5. "A gateway holding 1,500 agents and 60k members dies at 10:00 on Monday. What do agents experience, and what
      did you build so they don't lose their place?"
      *Meets:* heartbeat detection, jittered reconnect to the other gateways, session resume or fresh auth, sync
      from each conversation's cursor; messages acked before the failure are all in the store; the registry entries
      expire with the TTL; the reconnect arithmetic stated (small at this scale).
   6. "Why not keep the existing REST inbox and poll? It's only 35 messages a second."
      *Meets:* the cost is the polls, not the messages: ≈ 400k open apps polling every 5 s ≈ 80k requests/s to
      return ≈ 35 messages/s, and the latency floor is the poll interval; the honest flip (socket only on the
      messaging screen, long polling where agent desktops block WebSockets, polling for the notice list is fine).
   7. "One credit union's agents are mid-incident at 5× traffic. Prove another credit union's members can't be
      slowed down."
      *Meets:* tenant in every key; conversations sharded so a tenant's burst lands on its shards or lanes;
      per-tenant rate limits and queues on the chat service and the push path; per-tenant ack and delivery-lag
      SLOs with alerts; a load test that floods one tenant and measures another.
   8. "A member's conversations are placed on legal hold while the 7-year retention job is deleting old data. What
      stops the job from deleting them, and what does the member see if they hid a message?"
      *Meets:* the hold is a flag (per member or conversation) the retention job must check transactionally before
      deleting; hide-from-my-view is a per-member flag, never a delete, so the record and its receipts survive.
   9. "Roughly what does this cost per month, and which line dominates?" (credit toward *exceeds* only; attachments
      in object storage dominate storage, the gateway fleet is a handful of boxes).
4. **Score each dimension with evidence** (quote the write-up or the probe answer). Say what would have moved each
   dimension up one level.
5. **Calibration feedback**: name any dimension where the self-score was off by more than one level.
6. **Decide** with the overall rule above. Below "meets": give 2–3 concrete revision tasks and redo on a different
   day with a variation (suggested: "design the in-game chat for a multiplayer game: lobbies of 8, guilds of 500,
   one world channel per server with 50k players online, profanity filtering, and a 'report this message' flow
   that must preserve evidence").

## Recording

`sd record design C6 --self <below|approaching|meets|exceeds> --tutor <level> --notes "<one-line summary>"`

Example: `sd record design C6 --self meets --tutor approaching --notes "transfer as side channel, not a seq'd system message; receipts were cursors only, no evidence log; notice fan-out on read solid"`
