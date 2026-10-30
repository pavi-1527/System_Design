# Labs for A0 — Interview framework & estimation

One package, built across Unit 2 of [module A0](../../modules/A0-framework-estimation/README.md). Unit 1 has no Go
lab: its practice is timed rehearsals, described in each section's *Build it* and *Break it*.

| Order | Package | Unit / sections | Core |
|---|---|---|---|
| 1 | [`capacity`](capacity/) | Unit 2 §1–§7 | CORE |

Run every lab test (the gate): `go test -race ./labs/a0/...`, or `sd lab A0`, which runs the same command and
records the result. The reference solution is in `solutions/a0/`. Look at it only after your tests pass, or
after you've been stuck on one test for more than ≈ 30 minutes.

## `capacity` — back-of-envelope capacity modelling

**Goal.** Turn the whiteboard arithmetic of the 0:06–0:10 estimation slot into a small, tested Go package. You
get two things out of it: the formulas become precise (exact seconds per day, a ceiling that survives
floating-point noise, unit parsing that refuses ambiguous input), and you end up with a tool for checking your
`sd estimate` drills and design-exercise numbers.

**Files to edit:** `labs/a0/capacity/capacity.go` only. The tests (`capacity_test.go`, `example_test.go`,
`breakit_test.go`) are identical to the solution's copies. Don't change them.

**Suggested order** (it follows Unit 2's sections, and each step's tests can be run alone with `-run`):

1. `ParseQuantity`, `HumanBytes`, `HumanCount`, `HumanBits`, `BitsPerSec` (Unit 2 §1):
   `go test -race ./labs/a0/capacity/ -run 'Parse|Human|Bandwidth'`
2. `AvgQPS`, `PeakQPS` (§2): `go test -race ./labs/a0/capacity/ -run 'AvgQPS|PeakQPS'`
3. `StorageBytes`, `BandwidthBytesPerSec` (§3): `go test -race ./labs/a0/capacity/ -run 'Storage|Bandwidth'`
4. `ServersNeeded`, `Concurrency` (§5): `go test -race ./labs/a0/capacity/ -run 'Servers|Concurrency'`
5. `SerialAvailability`, `ParallelAvailability`, `DowntimePerYear` (§6):
   `go test -race ./labs/a0/capacity/ -run 'Availability|Downtime'`
6. `Spec.Validate`, `Estimate`, where the cache sizing from §4 lives (§4, §7):
   `go test -race ./labs/a0/capacity/ -run 'Estimate'`
7. Everything: `go test -race ./labs/a0/capacity/`

**Exported API** (doc comments in `capacity.go` are the spec):

```go
const SecondsPerDay = 86_400
var ErrInvalidInput, ErrBadQuantity error

func AvgQPS(dau, actionsPerUserPerDay float64) float64
func PeakQPS(avg, peakFactor float64) float64
func StorageBytes(itemsPerDay, bytesPerItem, retentionDays, replication float64) float64
func BandwidthBytesPerSec(qps, bytesPerRequest float64) float64
func BitsPerSec(bytesPerSec float64) float64
func ServersNeeded(peakQPS, perServerQPS, targetUtil float64, spares int) (int, error)
func Concurrency(qps float64, latency time.Duration) float64
func SerialAvailability(avail ...float64) float64
func ParallelAvailability(avail ...float64) float64
func DowntimePerYear(availability float64) time.Duration
func HumanBytes(b float64) string
func HumanCount(n float64) string
func HumanBits(bitsPerSec float64) string
func ParseQuantity(s string) (float64, error)

type Spec struct { /* DAU, WritesPerUserPerDay, ReadWriteRatio, WriteBytes, ReadBytes, RetentionDays,
                      Replication, IndexOverhead, PeakFactor, CacheHotFraction, PerServerQPS, TargetUtil,
                      Spares, Latency */ }
func (s Spec) Validate() error
type Sheet struct { /* per-day volumes, avg/peak read/write QPS, StorageBytes, peak ingress/egress,
                       CacheBytes, Servers, PeakConcurrency */ }
func Estimate(s Spec) (Sheet, error)
func (sh Sheet) String() string // provided: renders the sheet "as on the board"
```

**Documented edge-case behaviour the tests check:**
- The simple formulas return 0 for zero or negative inputs. `PeakQPS` treats a factor below 1 as 1;
  `StorageBytes` treats replication below 1 as 1.
- `ServersNeeded` returns an error wrapping `ErrInvalidInput` for impossible fleets, counts at least one server
  before spares, and must return 10 (not 11) for `4900 / (700 × 0.7)`, which is 10.000000000000002 in float64.
- The availability functions clamp inputs to [0, 1]. Empty serial = 1, empty parallel = 0.
- `ParseQuantity` rejects lowercase `m`, binary prefixes (`MiB`), bit units (`Gbps`), negatives, `1,000`,
  NaN/Inf and overflow. `3B` is three *bytes*; billions are `3bn`, `3G` or `3e9`.
- `Estimate` validates first, returning a zero `Sheet` and an `ErrInvalidInput` error for a bad `Spec`, and is
  safe to call concurrently (`TestEstimate_ConcurrentCallsAreIndependent` runs it from 32 goroutines under
  `-race`).

**Acceptance:** `go test -race ./labs/a0/capacity/` passes, including `ExampleEstimate`, whose expected output is
the pastebin estimate sheet from Unit 1 §7.

### Break-it exercises

**Break-it 1 — compound estimation error (the main one, Unit 2 §7).**
`labs/a0/capacity/breakit.go` (build tag `breakit`) contains `NaiveEstimate`, an estimator a teammate wrote in a
hurry. It has four bugs: 10^5 seconds per day; a bogus "bits-to-bytes" `/ 8` on sizes that are already bytes;
storage without replication; servers sized for average traffic at 100% utilisation.

1. *Before running anything*, predict naive/correct for each field of the photo-sharing sheet (the table is the
   Predict in Unit 2 §7) and write the ratios down.
2. Run the test and compare its logged ratios with your predictions:

   ```
   go test -tags breakit -run Breakit -v ./labs/a0/capacity/
   ```

3. Fix `NaiveEstimate` with the smallest changes you can until the test passes (tolerance 1%). Don't change
   `breakit_test.go`. The reference fix is `solutions/a0/capacity/breakit.go`.

This test doesn't depend on the rest of your lab, so you can do it before or after step 7. Note that
`go test -tags breakit ./labs/a0/...` *without* `-run Breakit` also runs the main tests.

**Break-it 2 — float noise in a ceiling (Unit 2 §5).** In your finished `ServersNeeded`, replace the
noise-tolerant ceiling with a plain `math.Ceil(raw)`. Run `go test ./labs/a0/capacity/ -run Servers` and see which
cases fail and by how much. Then restore it.

**Break-it 3 — forget replication (Unit 2 §3).** In your finished `StorageBytes`, drop the replication factor.
Run `go test ./labs/a0/capacity/ -run Storage` and read the failure message. Then work out how many days into the
year a cluster sized with this bug would fill up (answer in Unit 2 §3).

**Break-it 4 — a lenient parser (Unit 2 §1).** Make `ParseQuantity` accept `Gbps` and silently divide by 8.
`TestParseQuantity_RejectsAmbiguousOrMalformed` fails. Before you revert, write one sentence on why silently
converting is worse than refusing.

Vet both build configurations with: `go vet ./labs/a0/... && go vet -tags breakit ./labs/a0/...`
