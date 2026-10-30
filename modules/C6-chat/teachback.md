# C6 Teach-back

## Prompt

A mid-level engineer on your team is building chat into a product that has just passed a million users. Their
plan: "the app calls `POST /messages` and polls `GET /messages?since=<last timestamp>` every few seconds; messages
are sorted by `created_at`; if a send times out, the app retries; later we'll swap the polling for WebSockets and
the problem is solved". They have never run a system with persistent connections.

Explain to them, in your own words, **how a chat system gets every message to every member's devices in the same
order, without loss and without visible duplicates, and why each piece is there**: what changes when you hold
millions of open sockets (and why WebSockets alone solve none of the guarantees), where the order of a
conversation comes from, what makes a retried send safe and what the ack means, how a device that was offline or
missed a push catches up, and what receipts, big groups and presence cost if you do them naively.

Form: written, 500–900 words, one diagram (Mermaid or a sketch) allowed. Use numbers: pick a scale (theirs or the
module's) and show two or three calculations. No copying from the unit files; if a sentence from the course
appears verbatim, the evaluator will ask you to rephrase it. Write for that engineer, so they can go and change
their plan, not for an examiner.

## Evaluator checklist

Must-hit (all required for pass):
- [ ] Sizes the system by **concurrent connections** (and the **reconnect wave**), not only messages per second,
      with arithmetic at a stated scale, and concludes that persistent connections need a **dedicated stateful
      gateway tier plus a registry** of who is connected where, kept apart from the business logic that deploys
      often.
- [ ] Order comes from **one writer per conversation assigning a dense sequence number in the same atomic step as
      the durable write**, and explains why timestamps (client or server) can't provide it: skew, ties, and the
      deeper point that simultaneous sends need *someone* to decide.
- [ ] A retried send is safe because the client generates a **message id before the first attempt** and the server
      **dedupes on (conversation, sender, client id) atomically with the insert**, returning the original seq; the
      **ack is sent after the durable write and means "stored and ordered", not "delivered"**.
- [ ] **The store is the truth and the push is a hint**: clients catch up with **`after_seq` from their own
      cursor**, detect gaps from dense seqs, **dedupe deliveries by (conversation, seq)**, and **connect before
      they sync**; a lost push costs latency, never a message.
- [ ] **Per-connection queues are bounded**: a slow or dead client's frames are dropped and it is told to resync;
      the fan-out never blocks on one socket and never buffers without limit.
- [ ] States the honest version of "exactly once": **at-least-once on every hop plus the two dedupe points**, so a
      message is *shown* once; WebSockets/TCP guarantee a byte stream while the connection lives and nothing more.

Should-hit (at least 4 of 7):
- [ ] Deploys and reconnect storms: rolling drains with reconnect hints, **exponential backoff with full jitter**,
      and the arithmetic of a fast restart versus a slow one (e.g. ≈ 55k/s versus ≈ 4k/s at 10 M connections).
- [ ] Heartbeats keep NAT mappings alive and find dead sockets; the interval is set by **NAT/proxy idle timeouts
      and battery**, not bandwidth; registry entries expire with a TTL tied to the heartbeat.
- [ ] Receipts as **monotonic cursors**, **delivered per device and read per user**, coalesced, shown as **counts in
      groups**, with the quadratic cost of per-message receipts in a group computed.
- [ ] Reconnect after a long absence is **one inbox-cursor call** that says which conversations changed, then
      per-conversation `after_seq` only where needed.
- [ ] Group size sets the fan-out: **inbox copies below a threshold (≈ 1k members), pull on read above it**, with
      digests for pushes, justified by the burst a single post creates.
- [ ] Presence and typing are **soft state with TTLs, published only to subscribers** (what is on screen), never to
      every contact.
- [ ] Data model: messages keyed **(conversation_id, seq)**, partitioned by conversation; the dedupe key kept at
      least as long as the client's retry window.

Red flags (any one = revise):
- [ ] Timestamps (client or server) used as the order or as the sync cursor, or `since=<timestamp>` kept "because
      clocks are synced with NTP".
- [ ] A claim that WebSockets, TCP or a message broker deliver each message exactly once, or that switching to
      WebSockets fixes ordering or duplicates.
- [ ] The ack sent before the durable write, or only after the recipient's delivery.
- [ ] Dedupe by message body or by a time window, or retries described without a client-generated id.
- [ ] An unbounded per-connection buffer, or a fan-out that blocks on a slow socket, described as the design.
- [ ] Sockets held in the stateless business-logic tier with no drain or reconnect story, or a fixed-interval
      reconnect policy.
- [ ] Numbers stated as facts with no scale assumption, or a ≥ 8× unit error (bytes vs bits, per day vs per
      second) that isn't caught.

## Recording

`sd record teachback C6 --result <pass|revise> --notes "<missed items>"`
