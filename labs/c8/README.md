# C8 labs — ticket booking

One Go package, stdlib only. The stub compiles and returns immediately (`errNotImplemented` or zero values), so
every test fails fast until you implement it. The reference solution lives in `solutions/c8/`; its `*_test.go`
is byte-identical to the one here, so edit the code, never the tests.

Run what the gate checks with `sd lab C8` (`go test -race ./labs/c8/...`). While working on the package, run it
directly as shown below, and before you call it done shake out schedule-dependent bugs with
`go test -race -count=20 ./labs/c8/waitroom/`.

| Order | Package | Unit | Core/Stretch |
|---|---|---|---|
| 1 | [`waitroom`](waitroom/) | 2 (§7) | CORE |

The seat-hold path itself (all-or-nothing holds, lazy expiry, idempotent confirmation under contention) is
B3 problem 9, [`labs/b3/ticketing`](../b3/ticketing/). If you have done B3, this lab is the component that sits in
front of it; if you haven't, Unit 2 §5–§6 explain the hold path on paper and the waiting room is the code you write.

---

## 1. `waitroom` — virtual waiting room for a flash sale

**Goal.** The admission controller for a sale where ≈ 500,000 people arrive for ≈ 18,000 seats: a FIFO queue
with a position and an estimated wait, a token bucket that admits users at the rate the inventory can serve,
HMAC-signed passes with a TTL that the booking API verifies without a database round trip, idempotent joins
(a double tap must not take two slots), a bound on the queue, and expiry that sends an idle admitted user to the
back of the line.

**Edit** `labs/c8/waitroom/waitroom.go`. Provided: the errors, `Config`, `Ticket`. Implement, in order:

1. `New` (validation and defaults), `Join`, `Status`, `Leave`, `Len`: the queue. Use `container/list` plus a
   map from user to element so `Leave` and `Status` are O(1) apart from counting the position.
2. `Tick`: accrue tokens from the injected clock (fractional, capped at `Burst`), admit in FIFO order, issue
   passes, keep `Admitted` honest.
3. `Verify`: parse `user|expiry|mac`, recompute the MAC, compare with `hmac.Equal`, then check expiry.
4. Expiry: an admitted user whose pass has lapsed is forgotten everywhere (`Status` → `ErrNotInQueue`, `Join`
   starts over, `Admitted` drops).

**Run:** `go test -race ./labs/c8/waitroom/` (or `-run 'Join|Tick'` while you build the queue).

**Break it** (edit your passing solution, watch which test fails, then revert):

- Delete the mutex (or stop locking in `Tick`). Run `go test -race -run Concurrent ./labs/c8/waitroom/`. Expected:
  `WARNING: DATA RACE` reports, and `TestConcurrent_EveryUserAdmittedExactlyOnce` finds users admitted twice or
  more than 200 admissions per second. That is the oversell, one layer up.
- Set the bucket so tokens are never capped (ignore `Burst`). Run `go test -run Tick ./labs/c8/waitroom/`.
  Expected: `TestTick_TokensCarryOverUpToBurstWhenNobodyWaits` fails at its second tick (all 60 waiting users are
  admitted after 10 idle seconds instead of 30). Scale that up: leave the room idle for an hour before the sale and
  the first `Tick` admits the whole crowd at once, which is exactly the stampede the room exists to prevent.
- In a scratch copy of the tests, set `PassTTL` to 1 minute and advance the clock 2 minutes between admission and
  checkout. Expected: `Verify` returns `ErrPassExpired` and the user is back at position N. Decide what the TTL
  must cover (payment p99 + callback delay + human time), and compare with the hold TTLs in Unit 2 §6.
- Replace `hmac.Equal` with `==`. Nothing fails. Explain why it is still wrong (timing side channel on the MAC
  comparison) and why a test can't easily show it.
- Remove the `MaxQueue` check and run `TestJoin_RefusesWhenQueueIsFull`. Then estimate the memory of one million
  waiting users at ≈ 100 B each and decide what the bound should be for your inventory's drain rate.

Unit 2 §7 has the expected observations and the design discussion (why FIFO, why signed passes, why the room is
not the inventory's lock).

---

## Reference

- Solution: `go test -race ./solutions/c8/...`
- The waiting room is the C8 answer to B3's stage-4 question "a flash sale sends 500,000 users at 18,000 seats;
  what do you put in front of this service?"
