# A1 labs

Four Go packages, stdlib only, no network beyond loopback (`httptest`). Each stub compiles and returns immediately
(zero values, `errNotImplemented`, or HTTP 501), so every test fails fast until you implement it. Reference solutions
live in `solutions/a1/`; the `*_test.go` files there are byte-identical to the ones here — don't edit the tests, edit
the code.

Run everything the gate checks with `sd lab A1` (it runs `go test -race` over the core packages and reports `token`
separately). While working on one package, run it directly as shown below.

| Order | Package | Unit | Core/Stretch |
|---|---|---|---|
| 1 | [`lb`](lb/) | 1 (§4–§5) | CORE |
| 2 | [`pagination`](pagination/) | 2 (§1–§2) | CORE |
| 3 | [`idempotency`](idempotency/) | 3 (§2) | CORE |
| 4 | [`token`](token/) | 3 (§3) | STRETCH |

---

## 1. `lb` — load balancer

**Goal.** A load balancer you could reason about in an interview: pluggable pickers, passive health checking
(outlier ejection after N consecutive failures, cooldown, probation on re-admission), connection draining, and an
`http.Handler` reverse proxy in front of it.

**Edit** `labs/a1/lb/lb.go`. `Backend` (URL + in-flight counter) is provided. Implement, in order:

1. `(*RoundRobin).Pick`, `LeastConnections.Pick`, `PowerOfTwoChoices.Pick` (follow the documented draw protocol
   exactly; tests inject a scripted random source).
2. `NewPool`, `Healthy`, `Next`, `ReportSuccess`, `ReportFailure` (the state machine is in the `Pool` doc comment;
   compute re-admission lazily from the injected clock — no background goroutines).
3. `Drain`, `Undrain`.
4. `NewHandler` — `httputil.ReverseProxy` with `Rewrite` (`SetURL` + `SetXForwarded`), `ModifyResponse` (status ≥ 500
   is a failure) and `ErrorHandler` (transport error → failure + 502, but not when the *client* went away).

**Run:** `go test -race ./labs/a1/lb/` (or `-run 'RoundRobin|Least|Power'` for just the pickers).

**Break it:**
- `go test -tags breakit -run BreakIt_SlowBackendTail -v ./labs/a1/lb/` — one of four backends is 16x slower; compare
  mean/p50/p90/p99 and the slow backend's share of traffic for each picker. Predict first. Then copy
  `breakit_test.go` to a scratch file such as `breakit_scratch_test.go` (keep the `//go:build breakit` line, rename
  the test functions) and change concurrency, the slow backend's latency, or the P2C random source there — the shared
  test files must stay identical to the solution's.
- `go test -tags breakit -run BreakIt_DeadBackend -v ./labs/a1/lb/` — client-visible errors with ejection off vs on.
- Remove the client-cancellation check from your `ErrorHandler` and run
  `go test -race -run ClientCancel ./labs/a1/lb/`.

Unit 1 §4–§5 has the expected observations.

---

## 2. `pagination` — keyset vs offset

**Goal.** Cursor pagination that returns every row exactly once while rows are inserted and deleted, with opaque,
tamper-proof cursors — and an offset implementation to show why it doesn't.

**Edit** `labs/a1/pagination/pagination.go`. Provided: `Item`, `Key`, `KeyOf`, `Before` (newest first, ID tiebreak),
`Store.Insert/Delete/Len`, `NewPaginator`. Implement: `Store.Offset`, `Store.After`, `EncodeCursor`, `DecodeCursor`
(HMAC-SHA256, `hmac.Equal`, URL-safe base64), `Paginator.List` (fetch `limit + 1`), `NewHandler` (JSON, error model,
405 with `Allow`).

**Run:** `go test -race ./labs/a1/pagination/`

**Break it:**
- `go test -tags breakit -run BreakIt -v ./labs/a1/pagination/` — a 500-row table mutated between pages; duplicates
  and skips per mode. Predict both columns first.
- In your copy of `Before`, drop the ID tiebreak and re-run the normal suite: watch
  `TestList_WalksEveryItemExactlyOnceWithTiedTimestamps` fail.
- Make your handler return 500 (then 200) for a bad cursor and reason about what an outlier-ejecting load balancer
  and a status-code-based dashboard do with it (Unit 2 §1).

---

## 3. `idempotency` — Idempotency-Key middleware

**Goal.** `net/http` middleware that makes `POST`/`PATCH` safe to retry: the handler runs once per (client, key) and the
stored status/headers/body are replayed; concurrent duplicates get 409 + `Retry-After`; a different request under the
same key gets 422; 5xx results are not stored; completed records expire after a TTL; in-flight reservations are leases
with owner tokens; a store failure fails closed (503).

**Edit** `labs/a1/idempotency/idempotency.go`. Provided: types, the `Store` interface, `ScopedKey`, `Fingerprint`.
Implement `MemoryStore` (`Get`, `Reserve` — atomic, `Complete`, `Release`) and `Middleware`. The package doc lists the
exact behaviour the tests check.

**Run:** `go test -race ./labs/a1/idempotency/`

**Break it:**
- The files `breakit_on.go` / `breakit_off.go` define `breakNoInFlightLock` (true only with `-tags breakit`). Wire it
  into your middleware as the stub's comment says (replace the atomic `Reserve` with `Get` + run + store), then:
  `go test -tags breakit -race -run Concurrent -v ./labs/a1/idempotency/`
  Expected: `handler ran ~50 times for 50 concurrent duplicates; a retried payment would be charged ~50 times`.
- Shrink `LockTimeout` below the handler's duration (in a scratch test) and watch leases expire mid-execution.
- Store 5xx responses and run `-run ServerError`; then argue for the change anyway (Unit 3 §2).

---

## 4. `token` — signed tokens (STRETCH, not gated)

**Goal.** JWT-compatible HS256 tokens (`header.payload.signature`, base64url without padding) with a `kid`-based
keyring for rotation, `exp`/`nbf` with clock-skew leeway, an audience check, and strict algorithm handling (`none`,
`RS256`, `HS512` rejected). Verification order matters and is tested: structure → `alg` → `kid` → signature → claims.

**Edit** `labs/a1/token/token.go`: `Keyring` (`NewKeyring`, `Add` copies secrets, `SetCurrent`, `Remove`), `Sign`,
`Verifier.Verify`.

**Run:** `go test -race ./labs/a1/token/`

**Break it:** remove your `alg` check; check claims before the signature; set `Leeway: 0` with a verifier clock 2 s
behind the signer's. Run the suite after each change and name the attack or outage it corresponds to (Unit 3 §3).

---

## Reference

- Solutions: `go test -race ./solutions/a1/...`
- Break-it code is behind the `breakit` build tag, so default runs ignore it: `go vet -tags breakit ./labs/a1/...`
  type-checks it.
