# C6 labs

One Go package, stdlib only, no network. The stub compiles and returns immediately (zero values or
`errNotImplemented`), so every test fails fast until you implement it. The reference solution lives in
`solutions/c6/chat/`; its `chat_test.go` is byte-identical to the one here — edit the code, never the tests.

Run what the gate checks with `sd lab C6` (`go test -race` over the package). While working, run it directly:

```sh
go test -race ./labs/c6/chat/
```

Every test that waits has a 5 s timeout, so a wrong implementation fails with a message instead of hanging. Before
you call it done, shake out schedule-dependent bugs: `go test -race -count=20 ./labs/c6/chat/`.

| Order | Package | Unit | Core/Stretch |
|---|---|---|---|
| 1 | [`chat`](chat/) | 2 (§5–§7) | CORE |

---

## 1. `chat` — the conversation core

**Goal.** The component every chat deep dive ends up at: a single writer per conversation that assigns dense
sequence numbers, idempotent sends keyed by (sender, client message id), sync-from-a-cursor for clients that were
offline, bounded live subscriptions that never let a slow socket slow a sender down, and read receipts that only
move forward. It is the chat service, the message store, the session registry and the gateway's per-socket queue
collapsed into one process, which is exactly the right size to reason about the guarantees.

**Edit** `labs/c6/chat/chat.go`. Provided: the types, the errors, `New`, `Options`, and the `Subscription` struct's
exported surface. Implement, in order:

1. `Join`, `Members`, `Latest` — conversations are created on first join; membership is a set.
2. `Send` — validate, honour an already-cancelled context, then under the conversation's lock: membership check,
   dedupe lookup, `seq = len(log)+1`, append, record the dedupe entry, fan out to the members' live subscriptions
   with non-blocking offers. Everything under one lock is what makes seqs dense and the order identical on every
   subscription.
3. `Sync` — dense seqs make this a slice: `log[afterSeq : afterSeq+limit]`, copied, never nil.
4. `Connect`, `Subscription.Close`, `Subscription.Dropped` — a buffered channel per subscription, a registry keyed
   by user, `context.AfterFunc` (or a goroutine with a done channel) so the subscription closes when the context
   is done, and a `sync.Once` so `Close` is idempotent. Make sure no `Send` can be offering to the channel when you
   close it.
5. `MarkRead`, `Unread` — a per-(conversation, user) cursor that is `max(old, seq)` and never exceeds `Latest`.

**Acceptance tests** (`go test -race ./labs/c6/chat/`):

| Guarantee | Tests |
|---|---|
| Membership and validation | `TestJoin_CreatesConversationAndIsIdempotent`, `TestSend_RejectsUnknownConversationNonMembersAndEmptyFields` |
| Dense, total order from a single writer | `TestSend_AssignsDenseSequenceFromOne`, `TestSend_DenseUniqueSeqsUnder50ConcurrentSenders` |
| Idempotent send | `TestSend_DuplicateClientMessageIDIsStoredOnce`, `TestSend_HonoursCancelledContext` |
| Sync from a cursor | `TestSync_ReturnsExactlyTheMessagesAfterTheCursorInOrder`, `TestSync_ReturnsACopyOfTheLog` |
| Live delivery, once and in order, multi-device, non-members excluded | `TestConnect_DeliversEveryMessageOnceAndInOrderToEveryOnlineMember`, `TestConnect_UserInTwoConversationsSeesBothStreamsInPerConversationOrder` |
| Bounded subscriptions: drop when full, resync from the log | `TestConnect_SlowSubscriberNeverBlocksSendersAndMustResync` |
| Close and cancellation, no leaks | `TestConnect_CloseClosesTheChannelAndStopsDelivery`, `TestConnect_ContextCancellationClosesTheSubscription`, `TestConnect_NoGoroutineLeaksAfterCloseAndCancel` |
| Monotonic receipts | `TestMarkRead_CursorIsMonotonicAndBounded` |
| Everything at once under `-race` | `TestChaos_ConcurrentSendSyncConnectAndReadKeepInvariants` |

**Delivery policy you are implementing** (it is pinned by the tests, and it is the policy a real gateway applies to
a slow socket): offers to a subscription never block; when its buffer is full the message is dropped *for that
subscription*, `Dropped` is incremented, and the client is expected to `Sync` from its last seen seq. The log is the
truth; the push is a hint.

**Break it** (each on a copy of your passing code, then revert):

1. **Timestamps as order.** Assign `Seq` from `uint64(time.Now().UnixNano())` instead of `len(log)+1`, and make
   `Sync` filter by `Seq > afterSeq`. Run `go test -race -count=5 -run 'Dense|Sync|Chaos' ./labs/c6/chat/`. Predict
   which tests fail and why; then think about what the same change does to two devices and a client whose clock is
   a day fast.
2. **Check-then-act dedupe.** Move the dedupe lookup *outside* the conversation lock (look up, release, re-acquire
   to append). Run `go test -race -count=20 -run 'Duplicate|Chaos' ./labs/c6/chat/`. The concurrent-duplicate test
   should catch it; note that `-race` may stay silent, because a logical race is not a data race.
3. **Blocking fan-out.** Replace the non-blocking offer with a plain channel send. Run
   `go test -race -run 'SlowSubscriber' ./labs/c6/chat/` and watch the senders stall behind one subscriber that
   never reads; that is one slow phone freezing a 256-member group.
4. **Unbounded fan-out.** Replace the channel with an unbounded slice per subscription. Nothing blocks, no test
   fails quickly — and memory grows without limit for every dead socket. Write the test that would catch it
   (hint: `Dropped` must be able to be non-zero).
5. **The leak.** Implement the context hook as a goroutine that selects only on `ctx.Done()`. Run
   `go test -race -run 'NoGoroutineLeaks' ./labs/c6/chat/` and read the goroutine profile it prints: every
   explicitly closed subscription still has a goroutine parked on a context that will never be cancelled.
6. **Backwards receipts.** Let `MarkRead` set the cursor unconditionally. `TestMarkRead_CursorIsMonotonicAndBounded`
   fails on the late receipt; picture the unread badge flickering as a user's two devices race.

<details data-kind="answer">
<summary>What you should see</summary>

1. `TestSend_DenseUniqueSeqsUnder50ConcurrentSenders` and the chaos test fail on density (gaps, and on a fast
   machine two messages with the same nanosecond); `TestSync_*` fails because `afterSeq+limit` arithmetic no longer
   means "the next messages". With client clocks it is worse: a skewed device sorts above everyone else forever.
2. The concurrent-duplicate test reports `Latest = 2..20 after 20 concurrent sends with one client id`. The lookup
   and the insert must be one atomic step: in SQL that is a unique constraint on (conversation, sender, client id);
   here it is one lock.
3. The slow-subscriber test times out: `1000 sends did not complete within 5s`.
4. `Dropped` is always 0 and the "must resync" half of the test never exercises the policy; in production the
   gateway's heap is the queue, and it is the gateway that dies, not the slow client.
5. `goroutine leak: N+50 goroutines still running, baseline was N` (one per subscription closed with `Close`), with stacks parked in `select` on `ctx.Done()`.
6. `MarkRead(2) after 4 = 2, want 4`.

</details>
