# A9 labs

Three Go packages, stdlib only, no network. Each stub compiles and returns immediately (zero values or
`errNotImplemented`), so every test fails fast until you implement it. Reference solutions live in `solutions/a9/`;
the `*_test.go` files there are byte-identical to the ones here: don't edit the tests, edit the code.

Run everything the gate checks with `sd lab A9` (it runs `go test -race` over the core packages and reports `envelope`
separately). While working on one package, run it directly as shown below.

| Order | Package | Unit | Core/Stretch |
|---|---|---|---|
| 1 | [`tenancy`](tenancy/) | 1 (§3–§5) | CORE |
| 2 | [`auditlog`](auditlog/) | 2 (§4) | CORE |
| 3 | [`envelope`](envelope/) | 2 (§3) | STRETCH |

---

## 1. `tenancy` — tenant-scoped data access and a noisy-neighbour limiter

**Goal.** Two components every multi-tenant platform needs on every request path.

1. A **`Store`** in which the tenant comes from the `context.Context` and *no method accepts a tenant parameter*.
   Cross-tenant reads and writes are not "forbidden by a check someone must remember": there is no code path that
   could express them. `Get`/`Update`/`Delete` of another tenant's id behave exactly like a missing id (same error,
   same message), `List` can only see the caller's partition, and every row handed out is a deep copy.
2. A **`Limiter`**: per-tenant admission control with weighted fair sharing (guaranteed rates), borrowing of idle
   capacity through a shared spare pool, and a per-tenant cap, built from token buckets and an injected clock (a
   hierarchical token bucket in the spirit of Linux `tc` HTB).

**Edit** `labs/a9/tenancy/tenancy.go`. Provided: `TenantID` (+ `Valid`), `WithTenant`, `FromContext`, the types and
error values. Implement, in this order:

1. `NewStore`, `Insert`, `Get` (tenant validation first: `ErrNoTenant` / `ErrInvalidTenant`), then `Update`, `Delete`,
   `Count`.
2. `List` (keyset pagination by id; fetch `Limit+1` to know whether `Next` is needed; `Filter` is applied before the
   limit and only ever sees the caller's rows).
3. `NewLimiter`, `SetWeight`, `Allow` (single tenant first), then guarantees and borrowing (`TestLimiter_Noisy…`), then
   the cap (`TestLimiter_Cap…`), `RetryAfter` and the reshaping rules.

The package documentation lists the exact behaviour; the tests check every line of it.

**Run:** `go test -race ./labs/a9/tenancy/` (`-run Store` for the data layer, `-run Limiter` for the limiter).

**Break it:**
- Wire the constant `breakNoTenantPredicate` (files `breakit_on.go` / `breakit_off.go`) into your `List` so that, with
  `-tags breakit`, it forgets the tenant (for example by scanning every partition). Then
  `go test -tags breakit -run BreakIt_CrossTenantLeak -v ./labs/a9/tenancy/` prints how much of other credit unions'
  data one tenant can enumerate, and `go test -tags breakit -run Store ./labs/a9/tenancy/` shows which isolation tests
  notice. Predict the percentage first (Unit 1 §3 has the expected observation).
- `go test -tags breakit -run BreakIt_NoisyNeighbour -v ./labs/a9/tenancy/` compares one global rate limit with your
  fair limiter when one tenant floods: predict the small tenant's success rate under each. Then copy
  `breakit_test.go` to a scratch file such as `breakit_scratch_test.go` (keep the `//go:build breakit` line, rename the
  test functions) and change the flood rate, weights or `TenantCap` there. The shared test files must stay identical
  to the solution's.
- Delete the mutex from `Limiter.Allow` and run `go test -race -run Concurrent ./labs/a9/tenancy/`.

Unit 1 §3–§5 have the expected observations.

---

## 2. `auditlog` — a tamper-evident, hash-chained audit log

**Goal.** An append-only log in which every entry commits to the one before it, plus the verifier that proves what
was changed, deleted, reordered or cut off, and what a hash chain alone *cannot* prove (tail truncation and
whole-chain rewrites) until you publish an external anchor.

**Edit** `labs/a9/auditlog/auditlog.go`. Provided: types, error values, `Genesis`, `Checkpoint.String`. Implement:
`Canonical` and `ComputeHash` (the byte-exact encoding is in the package doc; a second, independent encoder inside
the tests acts as the spec), `New`, `Append` (concurrency-safe total order), `Len`, `Head`, `Entries`, `Since`
(deep copies), `Verify` (the order of checks is part of the contract), `ParseCheckpoint`, `VerifyError.Error`,
`TenantLogs` (`Append`, `Entries`, `Head`, `Heads`) and `CombineHeads`.

**Run:** `go test -race ./labs/a9/auditlog/`

**Break it:**
- `go test -tags breakit -run BreakIt_TamperMatrix -v ./labs/a9/auditlog/` — nine attacks on a ten-entry log, with the
  check that catches each one, with and without an external anchor. Predict both columns first.
- `go test -tags breakit -run BreakIt_VerifyCost -v ./labs/a9/auditlog/` — appends and verifies 100,000 entries and
  extrapolates to a billion; use the numbers in the Unit 2 §4 capacity estimate.
- In your `putString`, drop the length prefix and run the suite: watch `TestCanonical_FieldBoundariesAreUnambiguous`
  fail (the attack is moving bytes between adjacent fields). Then skip the `PrevHash` check in `Verify` and see which
  tamper cases stop being detected.

---

## 3. `envelope` — envelope encryption and key rotation (STRETCH, not gated)

**Goal.** A teaching sketch of the key hierarchy behind every managed encryption service: an in-process `KMS` holding
versioned key-encryption keys (KEKs) that wrap per-tenant data-encryption keys (DEKs), and a `Vault` that encrypts
objects with AES-256-GCM, binds each ciphertext to its tenant and object through the associated data, caches DEKs so
KMS calls do not scale with objects, rotates a KEK by re-wrapping keys (not re-encrypting data), retires a DEK after
a usage limit, refuses to encrypt with a stuck random source, and crypto-shreds a tenant.

> **Teaching sketch. Never roll your own crypto in production.** Use a vetted library (Tink, the AWS Encryption SDK)
> and a managed KMS or HSM. This package keeps keys in ordinary memory and has no backup, audit or access control.

**Edit** `labs/a9/envelope/envelope.go` using only `crypto/aes`, `crypto/cipher` (`cipher.NewGCM`) and `crypto/rand`
(or the injected reader). Implement `KMS` (`NewKMS`, `CreateKey`, `Rotate`, `Wrap`, `Unwrap`, `Rewrap`,
`DestroyVersion`, `Calls`) and `Vault` (`NewVault`, `Seal`, `Open`, `RewrapKeys`, `DropCache`, `KeyCount`,
`ShredTenant`).

**Run:** `go test -race ./labs/a9/envelope/`

**Break it:**
- Wire `breakNoAAD` into your `Seal`/`Open` (nil associated data under `-tags breakit`), then
  `go test -tags breakit -run BreakIt_SwapWithoutAAD -v ./labs/a9/envelope/`: does a cross-tenant ciphertext swap
  succeed? Which tests notice?
- `go test -tags breakit -run BreakIt_KMSCalls -v ./labs/a9/envelope/` — KMS calls for 10,000 reads with and without
  the DEK cache. Predict both numbers.
- `go test -tags breakit -run BreakIt_NonceCollisionOdds -v ./labs/a9/envelope/` — the birthday bound for random
  96-bit nonces, which is why a DEK is retired after `MaxSealsPerKey` messages.
- Destroy KEK version 1 *without* calling `RewrapKeys` (the test `DestroyingAnOldKEKWithoutRewrappingLosesData` does
  exactly this) and explain why a warm cache hides the bug until the next restart.

---

## Reference

- Solutions: `go test -race ./solutions/a9/...`
- Break-it code is behind the `breakit` build tag, so default runs ignore it: `go vet -tags breakit ./labs/a9/...`
  type-checks it.
