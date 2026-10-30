// Package capacity is a small back-of-envelope capacity-modelling toolkit.
//
// It turns the arithmetic you do on a whiteboard in minutes 6–10 of a design interview into code you can test:
// average and peak QPS, storage with retention and replication, bandwidth, cache size for a hot set, server
// counts with a utilisation target and spares, Little's-law concurrency, and availability of serial and parallel
// dependencies. Estimate ties them together into one estimate Sheet for a described system.
//
// Conventions used throughout:
//   - Sizes are bytes, rates are per second, and all prefixes are decimal SI (1 KB = 1000 B, 1 GB = 1e9 B).
//     Network links are quoted in bits per second; use BitsPerSec to convert before comparing with a NIC.
//   - A day has exactly SecondsPerDay seconds. (On a whiteboard 1e5 is fine; in code it under-states every rate
//     by ≈ 14% and compounds with every other shortcut. See the break-it exercise in labs/a0/README.md.)
//   - The simple formula helpers never return errors: a negative or zero input that makes the quantity
//     meaningless yields 0, as documented on each function. Functions where a bad input is almost certainly a
//     bug (ServersNeeded, Estimate, ParseQuantity) return an error instead.
package capacity

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// SecondsPerDay is the exact number of seconds in a (non-leap-second) day.
const SecondsPerDay = 86_400

// ErrInvalidInput is returned (wrapped) by ServersNeeded and Estimate when an input cannot describe a real system,
// e.g. a per-server capacity of zero or a utilisation target above 100%.
var ErrInvalidInput = errors.New("capacity: invalid input")

// ErrBadQuantity is returned (wrapped) by ParseQuantity when the string is not a quantity it understands.
var ErrBadQuantity = errors.New("capacity: cannot parse quantity")

// errNotImplemented is what the stubs return until you implement them. It deliberately wraps neither sentinel
// above, so the error-path tests fail too.
var errNotImplemented = errors.New("capacity: not implemented (lab stub)")

// AvgQPS returns the average request rate in requests/second for dau daily active users who each perform
// actionsPerUserPerDay actions per day: dau × actions / SecondsPerDay.
//
// If either input is zero or negative the result is 0.
func AvgQPS(dau, actionsPerUserPerDay float64) float64 {
	// TODO(lab): dau × actions / SecondsPerDay; 0 if either input is ≤ 0.
	return 0
}

// PeakQPS returns avg × peakFactor.
//
// A peak cannot be below the average, so a peakFactor below 1 is treated as 1. A zero or negative avg yields 0.
func PeakQPS(avg, peakFactor float64) float64 {
	// TODO(lab): avg × peakFactor; treat peakFactor < 1 as 1; 0 if avg ≤ 0.
	return 0
}

// StorageBytes returns the raw bytes needed to keep itemsPerDay items of bytesPerItem bytes for retentionDays
// days, with replication copies of each: items/day × size × retention × replication.
//
// Index and metadata overhead are not included; multiply by (1 + overhead) yourself, or use Estimate.
// A zero or negative itemsPerDay, bytesPerItem or retentionDays yields 0. You cannot keep fewer than one copy of
// data you store, so a replication below 1 is treated as 1.
func StorageBytes(itemsPerDay, bytesPerItem, retentionDays, replication float64) float64 {
	// TODO(lab): items × size × retention × replication; replication < 1 counts as 1.
	return 0
}

// BandwidthBytesPerSec returns qps × bytesPerRequest, in bytes/second. Zero or negative inputs yield 0.
func BandwidthBytesPerSec(qps, bytesPerRequest float64) float64 {
	// TODO(lab): qps × bytesPerRequest.
	return 0
}

// BitsPerSec converts bytes/second to bits/second (× 8), the unit network links are sold in.
// A zero or negative input yields 0.
func BitsPerSec(bytesPerSec float64) float64 {
	// TODO(lab): bytes → bits.
	return 0
}

// ServersNeeded returns how many servers to run so that peakQPS lands at no more than targetUtil of each
// server's measured capacity perServerQPS, plus spares extra servers for failures and deploys:
//
//	ceil(peakQPS / (perServerQPS × targetUtil)) + spares
//
// At least one server is always counted before spares are added (a service with no traffic still runs).
// The ceiling tolerates floating-point noise, so an exact division such as 4900 / (700 × 0.7) gives 10, not 11.
//
// It returns an error wrapping ErrInvalidInput if peakQPS < 0, perServerQPS ≤ 0, targetUtil is outside (0, 1],
// or spares < 0.
func ServersNeeded(peakQPS, perServerQPS, targetUtil float64, spares int) (int, error) {
	// TODO(lab): validate inputs (wrap ErrInvalidInput), then ceil(peak / (per × util)) with a float-noise
	// tolerance, at least 1, plus spares.
	return 0, errNotImplemented
}

// Concurrency applies Little's law, L = λ × W: the average number of requests in flight when requests arrive at
// qps per second and each spends latency in the system. Use it to size thread pools, connection pools and
// in-flight limits. Zero or negative inputs yield 0.
func Concurrency(qps float64, latency time.Duration) float64 {
	// TODO(lab): Little's law.
	return 0
}

// SerialAvailability returns the availability of a request that needs every one of the given dependencies to be
// up: the product of their availabilities. With no arguments it returns 1 (the empty product). Each value is
// clamped to [0, 1].
func SerialAvailability(avail ...float64) float64 {
	// TODO(lab): product of availabilities, each clamped to [0, 1]; empty → 1.
	return 0
}

// ParallelAvailability returns the availability of a request that succeeds if at least one of the given
// redundant replicas is up, assuming failures are independent: 1 − Π(1 − aᵢ). With no arguments it returns 0
// (no replicas, never available). Each value is clamped to [0, 1].
//
// The independence assumption is the weak point: replicas that share a deploy, a config, a region or a bug fail
// together, and the real number is closer to that of a single replica.
func ParallelAvailability(avail ...float64) float64 {
	// TODO(lab): 1 − Π(1 − aᵢ), each clamped to [0, 1]; empty → 0.
	return 0
}

// DowntimePerYear returns the downtime a given availability allows over a 365-day year, rounded to the nearest
// second. 0.999 gives 8h45m36s. The availability is clamped to [0, 1].
func DowntimePerYear(availability float64) time.Duration {
	// TODO(lab): (1 − a) × 365 days, rounded to the second.
	return 0
}

// HumanBytes formats a byte count with decimal SI units and one decimal place, trimming a trailing ".0":
// 999 → "999 B", 1234 → "1.2 KB", 1.5e9 → "1.5 GB", 4e13 → "40 TB", 999_999 → "1 MB". Negative values keep
// their sign. Values beyond the largest unit stay in EB ("1000 EB").
func HumanBytes(b float64) string {
	// TODO(lab): units B KB MB GB TB PB EB, one decimal, trim ".0", "999 B" style with a space.
	// Hint: one unexported helper can implement HumanBytes, HumanCount and HumanBits.
	return ""
}

// HumanCount formats a count or rate with the suffixes k, M, bn and T (thousand, million, billion, trillion),
// one decimal place, trailing ".0" trimmed: 11.574 → "11.6", 1157.4 → "1.2k", 1.2e9 → "1.2bn".
// Billions are "bn" rather than "B" so the output never looks like bytes; ParseQuantity reads it back.
func HumanCount(n float64) string {
	// TODO(lab): suffixes "" k M bn T, no space.
	return ""
}

// HumanBits formats a bit rate the way links are sold: bps, kbps, Mbps, Gbps, Tbps, one decimal place, trailing
// ".0" trimmed: 2.78e6 → "2.8 Mbps", 1e9 → "1 Gbps". Pass it BitsPerSec(bytesPerSec), not a byte rate.
func HumanBits(bitsPerSec float64) string {
	// TODO(lab): units bps kbps Mbps Gbps Tbps, with a space.
	return ""
}

// ParseQuantity parses a non-negative quantity written the way people write them in estimates:
//
//	"500", "86400", "2e6", ".5M", "1.2k", "3M", "1.2bn", "40TB", "1.5 GB", "10 KB"
//
// The grammar is <number>[optional space][suffix]. The number is a decimal with an optional exponent. The suffix
// is an optional decimal SI prefix (k or K, M, G, T, P, E; "bn" also means 1e9), optionally followed by B for
// bytes, which multiplies by 1. So "3B" is three bytes; write billions as "3bn", "3G" or "3e9".
//
// Everything else is rejected with an error wrapping ErrBadQuantity, in particular:
//   - lowercase "m" (milli, not mega: almost always a typo for M),
//   - binary prefixes such as "MiB" (this course uses decimal SI throughout),
//   - bit units such as "Gb" or "Gbps" (convert to bytes explicitly, dividing by 8, so the 8× is visible),
//   - negative numbers, thousands separators ("1,000"), NaN, Inf, and values that overflow float64.
func ParseQuantity(s string) (float64, error) {
	// TODO(lab): parse <number>[space][suffix]; reject everything listed above with ErrBadQuantity.
	// Hint: regexp and strconv.ParseFloat from the standard library are enough.
	return 0, errNotImplemented
}

// Spec describes a system to estimate. Every field is an assumption you would say out loud in an interview.
type Spec struct {
	Name string

	DAU                 float64 // daily active users
	WritesPerUserPerDay float64 // items created per user per day (posts, pastes, messages, uploads)
	ReadWriteRatio      float64 // reads per write; 0 means write-only

	WriteBytes float64 // bytes persisted per write (payload + metadata), before replication
	ReadBytes  float64 // bytes sent back per read response

	RetentionDays float64 // how long written items are kept; 0 means nothing is persisted
	Replication   float64 // copies kept of each item (≥ 1); 3 is typical for a replicated store
	IndexOverhead float64 // extra storage as a fraction of data, e.g. 0.2 = +20% for indexes

	PeakFactor       float64 // peak QPS / average QPS (≥ 1); consumer traffic ≈ 2–3
	CacheHotFraction float64 // fraction of a day's read volume to hold in cache, in [0, 1]; 0.2 = the 80/20 rule

	PerServerQPS float64       // measured (or assumed) capacity of one application server
	TargetUtil   float64       // planned utilisation at peak, in (0, 1]; ≈ 0.5–0.7
	Spares       int           // extra servers on top (N+1 → 1, N+2 → 2)
	Latency      time.Duration // mean time a request spends in the service, for Little's law
}

// Sheet is the estimate sheet produced by Estimate. Rates are per second; sizes are bytes.
type Sheet struct {
	WritesPerDay float64
	ReadsPerDay  float64

	AvgWriteQPS float64
	AvgReadQPS  float64
	AvgQPS      float64 // reads + writes

	PeakWriteQPS float64
	PeakReadQPS  float64
	PeakQPS      float64 // reads + writes

	StorageBytes float64 // writes/day × WriteBytes × retention × replication × (1 + index overhead)

	PeakIngressBytesPerSec float64 // peak write QPS × WriteBytes
	PeakEgressBytesPerSec  float64 // peak read QPS × ReadBytes

	CacheBytes float64 // CacheHotFraction × reads/day × ReadBytes (an upper bound: see Estimate)

	Servers         int     // ServersNeeded(PeakQPS, PerServerQPS, TargetUtil, Spares)
	PeakConcurrency float64 // Concurrency(PeakQPS, Latency)
}

// Validate reports the first field of s that cannot describe a real system, wrapped in ErrInvalidInput.
func (s Spec) Validate() error {
	// TODO(lab): check every field against the rule in its comment; wrap ErrInvalidInput and name the field.
	return errNotImplemented
}

// Estimate produces the full estimate sheet for s, or an error wrapping ErrInvalidInput if s fails Validate.
//
// Writes per day are DAU × WritesPerUserPerDay; reads per day are writes × ReadWriteRatio. Peak rates apply
// PeakFactor to both. Servers are sized for total peak QPS. CacheBytes is computed from read volume, which
// treats every read as a distinct object and is therefore an upper bound; when you know the number of distinct
// hot objects, size the cache from that instead.
func Estimate(s Spec) (Sheet, error) {
	// TODO(lab): Validate, then build the sheet from the helpers above.
	return Sheet{}, errNotImplemented
}

// String renders the sheet the way you would write it on the board.
func (sh Sheet) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "per day      %s writes, %s reads\n", HumanCount(sh.WritesPerDay), HumanCount(sh.ReadsPerDay))
	fmt.Fprintf(&b, "avg QPS      %s (w %s, r %s)\n", HumanCount(sh.AvgQPS), HumanCount(sh.AvgWriteQPS), HumanCount(sh.AvgReadQPS))
	fmt.Fprintf(&b, "peak QPS     %s (w %s, r %s)\n", HumanCount(sh.PeakQPS), HumanCount(sh.PeakWriteQPS), HumanCount(sh.PeakReadQPS))
	fmt.Fprintf(&b, "storage      %s\n", HumanBytes(sh.StorageBytes))
	fmt.Fprintf(&b, "peak ingress %s/s (%s)\n", HumanBytes(sh.PeakIngressBytesPerSec), HumanBits(BitsPerSec(sh.PeakIngressBytesPerSec)))
	fmt.Fprintf(&b, "peak egress  %s/s (%s)\n", HumanBytes(sh.PeakEgressBytesPerSec), HumanBits(BitsPerSec(sh.PeakEgressBytesPerSec)))
	fmt.Fprintf(&b, "cache        %s\n", HumanBytes(sh.CacheBytes))
	fmt.Fprintf(&b, "servers      %d\n", sh.Servers)
	fmt.Fprintf(&b, "in flight    %s\n", HumanCount(sh.PeakConcurrency))
	return b.String()
}
