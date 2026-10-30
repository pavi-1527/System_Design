# C6 — Case study: chat / messaging

| | |
|---|---|
| Track | C — HLD case studies |
| Estimated time | 9 h (2 weekly units) — CORE 7 h, STRETCH 2 h |
| Prerequisites | A1, A5, A6 (gates passed) |
| Labs | `labs/c6/` — `chat` (conversation core: sequencing, idempotent sends, sync, bounded subscriptions, read cursors) |
| Gate | quiz ≥ 85% ×2 (different days) · labs pass · design ≥ meets · teach-back pass |

## Why this module exists

A banking app ships "secure messages" as `POST /messages` plus a poll of `GET /messages?since=<timestamp>` every
5 seconds. The 2,000-agent pilot is fine. At 3 M members the polls alone are ≈ 600k requests/s against a real
message rate of ≈ 200/s, so ≈ 99.9% of the read replicas' work returns nothing; the fix is "poll every 30 s", and
replies now take up to half a minute to appear. Two agents answer within the same second and a member's phone and
laptop show them in different orders, because each device sorted by whichever timestamp arrived first. A send
times out on a train, the app retries, and "please close my account" is stored twice.

So the team moves to WebSockets, sizes the new tier by messages per second, and puts ≈ 1.2 M idle sockets on each
of eight boxes. The first one runs out of memory, its clients reconnect to the other seven, and they fall over in
turn. The next morning a routine three-minute deploy disconnects ten million clients at once; they all retry
every second, and the token service, sized for ≈ 20k logins/s, is asked for ≈ 55k validations per second for forty
minutes. Nothing about the traffic was unusual. The *rate of change* of connections was.

Chat is the case study where the guarantees are easy to say and hard to deliver: every member sees the same
order, nothing acknowledged is lost, nothing is shown twice, across several devices, reconnects and deploys, while
the fleet holds millions of open sockets. Interviewers like it because a box diagram is worthless here; the marks
are in *where* order is decided, *what* the ack means, and *how* a device that missed a push gets the message
anyway. This module walks the 45 minutes once with those decisions made in front of you, has you build the core
in Go, and then makes you make them alone on a banking variant.

## Outcomes

- **Scope** a messaging prompt in six minutes: the clarifying questions that change the design (group size, online
  and offline delivery, ordering and duplicates, receipts and presence, retention and E2E), the dominant NFR (open
  connections), the hardest guarantee, and an explicit out-of-scope list. — CORE
- **Estimate** concurrent connections and their memory, gateway count, peak sends and deliveries, storage with
  replication and retention, heartbeat rate and the reconnect wave of a deploy, and tie each number to the
  decision it drives. — CORE
- **Design** the client protocol and data model: transport per client type, an idempotent `send` keyed by
  (conversation, sender, client message id) with an `ack` carrying `seq`, `after_seq` catch-up, and messages keyed
  `(conversation_id, seq)` partitioned by conversation. — CORE
- **Draw** the send and sync paths (L4 balancer, stateful gateways, session registry, stateless chat service,
  partitioned log store, best-effort per-gateway bus, push for offline members), justify every box, ack after the
  durable write, and connect before syncing. — CORE
- **Operate** the connection tier: rolling drains sized to the auth and sync budget, backoff with full jitter,
  heartbeats with a registry TTL, bounded per-connection queues that drop and ask for a resync, resumable sessions,
  and token expiry on long-lived sockets. — CORE
- **Guarantee** order and delivery: one writer per conversation assigning dense seqs with the durable write, gap
  detection, idempotent sends with dedupe atomic with the insert, dedupe by seq on delivery, exactly-once as an
  end-to-end property, and receipts as monotonic cursors (delivered per device, read per user). — CORE
- **Design** reconnect sync and group fan-out: one inbox-cursor call then per-conversation `after_seq`, fan-out on
  write below a member threshold derived from burst arithmetic and on read above it, digests and counts for
  channels, and presence published only to subscribers. — CORE
- **Implement** the conversation core in Go so it passes its tests under `-race`: dense seqs under concurrent
  senders, idempotent sends under concurrent retries, sync from a cursor, bounded subscriptions that never block
  senders, leak-free close and cancellation, monotonic read cursors (`labs/c6/chat`). — CORE
- **Extend** the design to two regions (a home region per conversation, what crosses, what you refuse) and to
  end-to-end encryption (what stays as metadata, what the server loses, how sync and new devices change). — STRETCH

## Unit plan

| Unit | Topic | Hours | Core/Stretch | Lab |
|---|---|---|---|---|
| 1 | [Guided design: requirements, estimation, API and data model, the send and sync paths](unit-1-guided-design.md) | 4 | CORE (3.5) / STRETCH (0.5) | — (conceptual; the estimate sheet and the two path diagrams are the work) |
| 2 | [Deep dives: connections at scale, ordering and delivery, offline sync, fan-out and presence, common mistakes](unit-2-deep-dives.md) | 5 | CORE (3.5, incl. ≈ 1 h gate work) / STRETCH (1.5) | `labs/c6/chat` |

**Unit 1 (4 h)**
- Session 1 (2 h): warm-up; §1 requirements (write your five clarifying questions and the dominant NFR before the
  reveal) and the requirement-change break-it; §2 the estimate sheet by hand, then the worked version, then
  `sd estimate -kind concurrency` and `-kind storage` a few times.
- Session 2 (1.5 h): §3 transport, frames and data model (commit to the dedupe key and the primary key before the
  reveal) and the "remove `client_msg_id`" thought experiment; §4 both paths drawn end to end with times on the
  arrows, the failure table, and the connect-then-sync rule.
- STRETCH (0.5 h): the gRPC-streams alternative in §3; the "requirement change at 0:30" rehearsal in §4, out loud
  with a timer.

**Unit 2 (5 h)**
- Session 1 (1.5 h): warm-up; §5 connections at scale (do the drain arithmetic yourself); §6 predict and reveal;
  lab steps 1–3 in [`labs/c6/README.md`](../../labs/c6/README.md): `Join`/`Members`/`Latest`, `Send`, `Sync`.
- Session 2 (1 h): lab steps 4–5 (`Connect` and `Subscription`, `MarkRead`/`Unread`); `go test -race -count=20
  ./labs/c6/chat/`; break-its 1–3 from the lab README; §7 core (inbox sync, fan-out threshold, presence); "Common
  mistakes"; `sd lab C6`; first `sd quiz C6` attempt.
- Session 3 (1 h, gate): the [design exercise](design-exercise.md) (45 min, solo, timed) and the self-score; the
  [teach-back](teachback.md) write-up can go the same day or with the second `sd quiz C6` attempt on a later day.
- STRETCH (1.5 h): multi-region and end-to-end encryption in §7, the presence fan-out arithmetic, and lab
  break-its 4–6 (unbounded fan-out, the goroutine leak, backwards receipts).

## How this connects to your banking work

- **The secure-message centre is this design at one-hundredth the scale.** Member-to-agent conversations inside the
  app, agent handoff, statements as attachments, seven-year retention with legal hold. The connection tier shrinks
  to a handful of boxes and the hard parts move to ordering across handoffs, receipts as compliance records and
  tenant isolation. The [design exercise](design-exercise.md) is exactly this.
- **A `send` frame is a transfer request.** The client message id is the idempotency key you put on payment
  initiation ([A1 Unit 3 §2](../A1-networking-api/unit-3-idempotency-auth.md#2-idempotency-keys-end-to-end)), and
  "dedupe atomically with the insert" is the same unique-constraint argument. A retry after a timeout must return
  the original `seq`, the way a retried transfer must return the original transfer id.
- **Receipts are the "did the member see the disclosure" question.** A read cursor per (conversation, member) that
  only moves forward, plus an append-only record of when it moved, is how you answer an auditor; "the push
  notification was sent" is not proof of anything.
- **Vendor callbacks and at-least-once.** Every hop here duplicates, exactly as card-processor and core-banking
  webhooks do; the two dedupe points (server by client id, client by seq) are the idempotent consumer from
  [A6 Unit 2 §2](../A6-async/unit-2-outbox-consumers.md#2-idempotent-consumers-and-dedupe) applied twice.
- **Long-lived sessions and SSO tokens.** A socket outlives its access token; the in-band refresh frame plus a
  "re-auth" close code is the same problem as keeping an SSO session alive in an agent console without logging
  6,000 agents out at the same second.

## Mastery gate checklist

- [ ] `sd quiz C6` ≥ 85% — attempt 1 (date: ____)
- [ ] `sd quiz C6` ≥ 85% — attempt 2 on a different day (date: ____)
- [ ] `sd lab C6` — all lab tests pass (with `-race`)
- [ ] Design exercise ([`design-exercise.md`](design-exercise.md)): self-score recorded, tutor score ≥ "meets"
      (`sd record design C6 --self <lvl> --tutor <lvl> --notes "..."`)
- [ ] Teach-back ([`teachback.md`](teachback.md)): tutor marks pass (`sd record teachback C6 --result pass --notes "..."`)

`sd status` is the source of truth; this list is for your own reading.

## What's next

C6 is one of the four case studies (with C1, C2 and C5) that unlock D3, the timed HLD mocks; expect "design
WhatsApp / Slack" or a variant (a support-chat product, a live-comments stream, a game lobby) there with no help.
C7 (notification service) is the push dispatcher this design hands offline members to, and C16 (collaborative
editing) returns to "who orders concurrent writes" when there is no single writer. The concepts lean on
[A1](../A1-networking-api/) (persistent connections, draining, idempotency keys, cursors),
[A5](../A5-consistency/) (why clocks don't order, single-writer sequencing, leases and fencing, exactly-once as an
end-to-end property), [A6](../A6-async/) (at-least-once, idempotent consumers, the outbox) and
[A7](../A7-reliability/) (retry storms and metastable failure); if a C6 question surprised you, re-read the linked
section there. Before moving on, run `sd review` and clear any C6 cards it surfaces, especially tags `ordering`,
`idempotency`, `websockets` and `fan-out`, and re-read both unit wrap-up tables the day before your first D3 mock.
