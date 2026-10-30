# Labs — B0 Go concurrency for interviews

Five graded lab packages plus a set of intentionally broken programs (`breakit`). Every graded package has a stub
here (compiles, returns zero values or `errNotImplemented`, never panics or blocks) and a reference solution under
`solutions/b0/<pkg>/`. Test files are byte-identical between the two, so passing the tests here means you match
the reference behaviour. Don't read the solutions until your tests pass, or until you've been stuck for 30 minutes
and have written down what you're stuck on.

Run everything the gate checks with:

```sh
sd lab B0                       # runs: go test -race ./labs/b0/...   (breakit is excluded by its build tag)
go test -race ./labs/b0/...     # the same thing by hand
```

Every test that waits has a timeout (usually 5 s), so a wrong implementation fails with a message instead of hanging.
Most stubs fail in milliseconds. Before you call a lab done, also run it a few times to shake out schedule-dependent
bugs: `go test -race -count=20 ./labs/b0/<pkg>/`.

Requirements: Go 1.22+ (tested with 1.24) and a working race detector (cgo enabled and a C compiler on Linux;
`go env CGO_ENABLED` should print `1`).

## Suggested order

| # | Package | Unit § | Goal | Edit | Run |
|---|---|---|---|---|---|
| 1 | `batcher` | U1 §2 | Size-or-time batcher: flush at `MaxItems` or `Linger` after the first item; `Add` blocks with ctx (backpressure); idempotent `Close` flushes the remainder; flush from one goroutine; nothing accepted is lost | `batcher/batcher.go` | `go test -race ./labs/b0/batcher/` |
| 2 | `safemap` | U1 §3 | Generic `ShardedMap[K, V]` (`hash/maphash`, power-of-two shards); `Get`/`Set`/`Delete`/`Len`/`ShardLens`; atomic `Update(fn)`; `Range` that calls `fn` without holding a lock | `safemap/safemap.go` | `go test -race ./labs/b0/safemap/` |
| 3 | `breakit` (races) | U1 §4 | Read race reports; fix a racy counter, a racy lazy init and `wg.Add` inside the goroutine | `breakit/breakit_test.go` (in place, or a copy) | see below |
| 4 | `group` | U2 §2 | errgroup clone: `WithContext` (cancel with cause on first error, and on `Wait`), `Go`, `TryGo`, `SetLimit`, `Wait` | `group/group.go` | `go test -race ./labs/b0/group/` |
| 5 | `pipeline` | U2 §3 | `Generate`, `Map` (unordered), `MapOrdered` (input order, bounded window), `Merge`; everything stops and leaks nothing when ctx is cancelled mid-stream | `pipeline/pipeline.go` | `go test -race ./labs/b0/pipeline/` |
| 6 | `fanout` | U2 §4 | Capstone: `QuoteAll` with per-vendor timeout (from call start), overall deadline, `MaxConcurrency`, BestEffort/FailFast policy, abandoned ctx-ignoring vendors, contained panics | `fanout/fanout.go` | `go test -race ./labs/b0/fanout/` |
| 7 | `breakit` (leaks, panics, deadlocks) | U2 §3, §6 | Read a goroutine profile, a double-close panic, a global deadlock, a map-write crash and old loop-variable semantics | `breakit/...` | see below |

STRETCH (U1 §6): benchmarks in `safemap/safemap_test.go`:

```sh
go test -run '^$' -bench . -cpu 1,4 ./labs/b0/safemap/
```

## Lab notes

**batcher.** The tests inject a fake clock through `Config.NewTimer`, so "no flush before linger" and "flush after
linger" are deterministic. One test uses the real clock to check that a nil `NewTimer` defaults to
`time.NewTimer`. Read the `Add` contract carefully: "when Add returns nil, the item is already part of the current
batch". The racing-Adds-vs-Close test checks it.

**safemap.** The hash helpers (`HashString`, `HashInteger`, `defaultHasher`) are given. `maphash.Comparable` (Go 1.24)
would be nicer, but this module's `go.mod` declares go 1.22 and `go vet`'s `stdversion` check rejects newer APIs.
The key test is `TestUpdate_ConcurrentIncrementsAreNotLost`: a Get-then-Set `Update` fails it, and `-race` stays silent.

**group.** The zero `Group` must work (no limit, no context). `Go` at the limit must *block* (tested with a third
goroutine that must not start), and slots must be released when functions return.

**pipeline.** Every stage owns and closes its output; every send/receive selects on `ctx.Done()`. The two
cancel-mid-stream tests abandon the consumer without draining and then require the goroutine count to return to
its baseline. On failure they print the goroutine profile, so read it.

**fanout.** `results` must be index-aligned with `vendors`, always, even when `QuoteAll` returns an error. The
tests use fake vendors: fast, failing, slow-but-ctx-respecting, stubborn (ignores ctx until the test releases it)
and panicking. One design decision is left to you and is not tested: whether an abandoned call's
`MaxConcurrency` slot is released immediately or only when the call really returns. Write your choice and its cost
in the doc comment.

## Break-it exercises (`breakit/`, build tag `breakit`)

Every file under `labs/b0/breakit/` starts with `//go:build breakit`, so normal `go test`, `go vet` and `sd lab B0`
runs never compile it. Predict what the tool will report **before** each command, then read the output, then fix
the code until the test passes under `-race` (or the program runs cleanly). Line numbers below are from the
unmodified files. A panic ends the whole test binary, so run the tests one at a time with `-run` as shown. A full
`go test -race -tags breakit ./labs/b0/breakit/` run stops at `TestDoubleClose`.

### 1. Racy counter: lost updates (U1 §4)

```sh
go test -race -tags breakit -run '^TestRacyCounter$' ./labs/b0/breakit/
```

Expect several reports like:

```
WARNING: DATA RACE
Read at 0x00c000212498 by goroutine 26:
  github.com/chethu007/system-design-course/labs/b0/breakit.TestRacyCounter.func1()
      .../labs/b0/breakit/breakit_test.go:26 +0x99

Previous write at 0x00c000212498 by goroutine 20:
  github.com/chethu007/system-design-course/labs/b0/breakit.TestRacyCounter.func1()
      .../labs/b0/breakit/breakit_test.go:26 +0xab
...
--- FAIL: TestRacyCounter
    breakit_test.go:32: hits = 79719, want 100000: 20281 increments lost
    testing.go:...: race detected during execution of test
```

Now run it **without** `-race` (`go test -tags breakit -run '^TestRacyCounter$' ./labs/b0/breakit/`). On the
reference box it *passed* every time: the goroutines rarely interleave without the detector's slowdown. A green
racy test is normal. Fix: `atomic.Int64` or a mutex.

### 2. Racy lazy init (U1 §3–§4)

```sh
go test -race -tags breakit -run '^TestRacyLazyInit$' ./labs/b0/breakit/
```

Expect `WARNING: DATA RACE` on `cfg` (the `if cfg == nil` read at line 53/54 against the write) and on `loads`
(line 44), even in runs where `loads` happens to be 1. Fix: `sync.Once` or `sync.OnceValue`.

### 3. `wg.Add` inside the goroutine (U2 §2)

```sh
go test -race -tags breakit -run '^TestWaitGroupAddInsideGoroutine$' ./labs/b0/breakit/
```

Expect a DATA RACE between the read of `total` after `Wait` (line 96) and the late goroutines' writes, and
usually `total = 39 after Wait, want 100` (the number varies). Fix: `wg.Add(1)` before `go`.

### 4. Goroutine leak: first response wins (U2 §3)

```sh
go test -race -tags breakit -run '^TestLeakyFirstResponse$' ./labs/b0/breakit/
```

Expect (after a 2 s settle loop):

```
--- FAIL: TestLeakyFirstResponse (2.01s)
    breakit_test.go:137: leaked 200 goroutines after 100 calls (baseline 2, now 202). Goroutine profile:
        goroutine profile: total 202
        200 @ 0x4ac44e 0x444495 0x4440d7 0x5d57ad 0x4b3c21
        #	0x5d57ac	.../breakit.firstResponse.func1+0x4c	.../breakit_test.go:112
```

One line, a big count, the exact source line: that is how you read `/debug/pprof/goroutine?debug=1` in production
too. No race is reported, because the race detector does not find leaks. Fix: `make(chan string, len(vendors))`.

### 5. Double close (U2 §6)

```sh
go test -race -tags breakit -run '^TestDoubleClose$' ./labs/b0/breakit/
```

Expect:

```
panic: close of closed channel

goroutine 21 [running]:
.../breakit.TestDoubleClose.func1()
	.../breakit_test.go:151 +0x85
```

Fix: a single owner closes, or `sync.Once` around `close`, or a context's (idempotent) cancel func.

### 6. Global deadlock: producer forgets to close (U2 §6)

```sh
go run -tags breakit ./labs/b0/breakit/deadlock
```

Expect `got 0`, `got 1`, `got 2`, then:

```
fatal error: all goroutines are asleep - deadlock!

goroutine 1 [chan receive]:
main.main()
	.../labs/b0/breakit/deadlock/main.go:27 +0xdf
exit status 2
```

In a server the same bug is a silent hang, because some goroutine (the accept loop, a ticker) is always alive.
Fix: `close(ch)` (deferred) in the producer.

### 7. Concurrent map writes without `-race` (U1 §3)

```sh
go run -tags breakit ./labs/b0/breakit/mapwrites
```

Expect `fatal error: concurrent map writes` (occasionally `concurrent map read and map write`) and a goroutine dump.
It is fatal, so `recover` cannot catch it. Fix: a mutex, or your `safemap`.

### 8. Loop-variable semantics (U2 §6)

```sh
go run -tags breakit ./labs/b0/breakit/loopvar
```

Expect `3 3 3`: the `go1.21` term in the file's build constraint sets that file's language version to 1.21 (one
shared loop variable). Delete `&& go1.21` from the first line and run again: `0 1 2` (Go 1.22 per-iteration
variables, from this module's `go 1.22` line).

### Vet

`go vet -tags breakit ./labs/b0/...` is clean on purpose: none of these bugs is one `go vet` (Go 1.24) detects.
To see vet catch something, write the value-receiver counter from U1 §3 Predict P07 into a scratch file and run
`go vet` on it (`copylocks`).

## Break it in your own labs

Each unit section has a "Break it" exercise that mutates *your* passing solution (e.g. make the batcher's hand-off
channel buffered, move `wg.Add` into the goroutine, drop `ctx` from `fn`, make `fanout`'s result channel
unbuffered). Do those in your lab code, watch which tests fail and why, then revert.
