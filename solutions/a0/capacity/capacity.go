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
	"math"
	"regexp"
	"strconv"
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

// AvgQPS returns the average request rate in requests/second for dau daily active users who each perform
// actionsPerUserPerDay actions per day: dau × actions / SecondsPerDay.
//
// If either input is zero or negative the result is 0.
func AvgQPS(dau, actionsPerUserPerDay float64) float64 {
	if !(dau > 0) || !(actionsPerUserPerDay > 0) {
		return 0
	}
	return dau * actionsPerUserPerDay / SecondsPerDay
}

// PeakQPS returns avg × peakFactor.
//
// A peak cannot be below the average, so a peakFactor below 1 is treated as 1. A zero or negative avg yields 0.
func PeakQPS(avg, peakFactor float64) float64 {
	if !(avg > 0) {
		return 0
	}
	if !(peakFactor >= 1) {
		peakFactor = 1
	}
	return avg * peakFactor
}

// StorageBytes returns the raw bytes needed to keep itemsPerDay items of bytesPerItem bytes for retentionDays
// days, with replication copies of each: items/day × size × retention × replication.
//
// Index and metadata overhead are not included; multiply by (1 + overhead) yourself, or use Estimate.
// A zero or negative itemsPerDay, bytesPerItem or retentionDays yields 0. You cannot keep fewer than one copy of
// data you store, so a replication below 1 is treated as 1.
func StorageBytes(itemsPerDay, bytesPerItem, retentionDays, replication float64) float64 {
	if !(itemsPerDay > 0) || !(bytesPerItem > 0) || !(retentionDays > 0) {
		return 0
	}
	if !(replication >= 1) {
		replication = 1
	}
	return itemsPerDay * bytesPerItem * retentionDays * replication
}

// BandwidthBytesPerSec returns qps × bytesPerRequest, in bytes/second. Zero or negative inputs yield 0.
func BandwidthBytesPerSec(qps, bytesPerRequest float64) float64 {
	if !(qps > 0) || !(bytesPerRequest > 0) {
		return 0
	}
	return qps * bytesPerRequest
}

// BitsPerSec converts bytes/second to bits/second (× 8), the unit network links are sold in.
// A zero or negative input yields 0.
func BitsPerSec(bytesPerSec float64) float64 {
	if !(bytesPerSec > 0) {
		return 0
	}
	return bytesPerSec * 8
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
	switch {
	case !(peakQPS >= 0) || math.IsInf(peakQPS, 0):
		return 0, fmt.Errorf("%w: peak QPS must be a finite number ≥ 0, got %v", ErrInvalidInput, peakQPS)
	case !(perServerQPS > 0) || math.IsInf(perServerQPS, 0):
		return 0, fmt.Errorf("%w: per-server QPS must be > 0, got %v", ErrInvalidInput, perServerQPS)
	case !(targetUtil > 0 && targetUtil <= 1):
		return 0, fmt.Errorf("%w: target utilisation must be in (0, 1], got %v", ErrInvalidInput, targetUtil)
	case spares < 0:
		return 0, fmt.Errorf("%w: spares must be ≥ 0, got %d", ErrInvalidInput, spares)
	}
	raw := peakQPS / (perServerQPS * targetUtil)
	n := int(math.Ceil(raw * (1 - 1e-9))) // relative epsilon: 10.000000000000002 is 10
	if n < 1 {
		n = 1
	}
	return n + spares, nil
}

// Concurrency applies Little's law, L = λ × W: the average number of requests in flight when requests arrive at
// qps per second and each spends latency in the system. Use it to size thread pools, connection pools and
// in-flight limits. Zero or negative inputs yield 0.
func Concurrency(qps float64, latency time.Duration) float64 {
	if !(qps > 0) || latency <= 0 {
		return 0
	}
	return qps * latency.Seconds()
}

func clamp01(a float64) float64 {
	switch {
	case !(a >= 0): // also catches NaN
		return 0
	case a > 1:
		return 1
	}
	return a
}

// SerialAvailability returns the availability of a request that needs every one of the given dependencies to be
// up: the product of their availabilities. With no arguments it returns 1 (the empty product). Each value is
// clamped to [0, 1].
func SerialAvailability(avail ...float64) float64 {
	p := 1.0
	for _, a := range avail {
		p *= clamp01(a)
	}
	return p
}

// ParallelAvailability returns the availability of a request that succeeds if at least one of the given
// redundant replicas is up, assuming failures are independent: 1 − Π(1 − aᵢ). With no arguments it returns 0
// (no replicas, never available). Each value is clamped to [0, 1].
//
// The independence assumption is the weak point: replicas that share a deploy, a config, a region or a bug fail
// together, and the real number is closer to that of a single replica.
func ParallelAvailability(avail ...float64) float64 {
	if len(avail) == 0 {
		return 0
	}
	q := 1.0
	for _, a := range avail {
		q *= 1 - clamp01(a)
	}
	return 1 - q
}

// DowntimePerYear returns the downtime a given availability allows over a 365-day year, rounded to the nearest
// second. 0.999 gives 8h45m36s. The availability is clamped to [0, 1].
func DowntimePerYear(availability float64) time.Duration {
	year := 365 * 24 * time.Hour
	d := time.Duration((1 - clamp01(availability)) * float64(year))
	return d.Round(time.Second)
}

var (
	byteUnits  = []string{"B", "KB", "MB", "GB", "TB", "PB", "EB"}
	countUnits = []string{"", "k", "M", "bn", "T"}
	bitUnits   = []string{"bps", "kbps", "Mbps", "Gbps", "Tbps"}
)

// human formats v with one decimal place (a trailing ".0" trimmed) in the largest unit that keeps it below 1000.
func human(v float64, units []string, sep string) string {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return strconv.FormatFloat(v, 'f', -1, 64)
	}
	sign := ""
	if v < 0 {
		sign, v = "-", -v
	}
	i := 0
	for v >= 1000 && i < len(units)-1 {
		v /= 1000
		i++
	}
	s := strconv.FormatFloat(v, 'f', 1, 64)
	if s == "1000.0" && i < len(units)-1 { // 999.96 rounds up to the next unit: "1 MB", not "1000 KB"
		v /= 1000
		i++
		s = strconv.FormatFloat(v, 'f', 1, 64)
	}
	s = strings.TrimSuffix(s, ".0")
	if s == "0" {
		sign = ""
	}
	return sign + s + sep + units[i]
}

// HumanBytes formats a byte count with decimal SI units and one decimal place, trimming a trailing ".0":
// 999 → "999 B", 1234 → "1.2 KB", 1.5e9 → "1.5 GB", 4e13 → "40 TB", 999_999 → "1 MB". Negative values keep
// their sign. Values beyond the largest unit stay in EB ("1000 EB").
func HumanBytes(b float64) string { return human(b, byteUnits, " ") }

// HumanCount formats a count or rate with the suffixes k, M, bn and T (thousand, million, billion, trillion),
// one decimal place, trailing ".0" trimmed: 11.574 → "11.6", 1157.4 → "1.2k", 1.2e9 → "1.2bn".
// Billions are "bn" rather than "B" so the output never looks like bytes; ParseQuantity reads it back.
func HumanCount(n float64) string { return human(n, countUnits, "") }

// HumanBits formats a bit rate the way links are sold: bps, kbps, Mbps, Gbps, Tbps, one decimal place, trailing
// ".0" trimmed: 2.78e6 → "2.8 Mbps", 1e9 → "1 Gbps". Pass it BitsPerSec(bytesPerSec), not a byte rate.
func HumanBits(bitsPerSec float64) string { return human(bitsPerSec, bitUnits, " ") }

var quantityRE = regexp.MustCompile(`^([0-9]+(?:\.[0-9]*)?|\.[0-9]+)([eE][+-]?[0-9]+)?\s*([A-Za-z]*)$`)

var quantitySuffixes = map[string]float64{
	"": 1, "k": 1e3, "K": 1e3, "M": 1e6, "G": 1e9, "bn": 1e9, "T": 1e12, "P": 1e15, "E": 1e18,
	"B": 1, "kB": 1e3, "KB": 1e3, "MB": 1e6, "GB": 1e9, "TB": 1e12, "PB": 1e15, "EB": 1e18,
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
	t := strings.TrimSpace(s)
	if t == "" {
		return 0, fmt.Errorf("%w: empty string", ErrBadQuantity)
	}
	if strings.HasPrefix(t, "-") {
		return 0, fmt.Errorf("%w: %q is negative; quantities in an estimate are ≥ 0", ErrBadQuantity, s)
	}
	m := quantityRE.FindStringSubmatch(t)
	if m == nil {
		return 0, fmt.Errorf("%w: %q is not <number>[suffix], e.g. 1.2k, 3M, 2e6, 40TB", ErrBadQuantity, s)
	}
	num, err := strconv.ParseFloat(m[1]+m[2], 64)
	if err != nil {
		return 0, fmt.Errorf("%w: %q: %v", ErrBadQuantity, s, err)
	}
	suffix := m[3]
	mult, ok := quantitySuffixes[suffix]
	if !ok {
		hint := "known suffixes: k M G bn T P E, optionally followed by B"
		switch {
		case suffix == "m":
			hint = "lowercase m means milli; write M for million"
		case strings.HasSuffix(suffix, "iB"):
			hint = "binary prefixes (KiB, MiB, ...) are not supported; this course uses decimal SI, 1 KB = 1000 B"
		case strings.HasSuffix(suffix, "b") || strings.HasSuffix(suffix, "bps") || strings.HasSuffix(suffix, "bit"):
			hint = "bit units are not supported; divide by 8 to get bytes and write the 8× explicitly"
		}
		return 0, fmt.Errorf("%w: %q: unknown suffix %q (%s)", ErrBadQuantity, s, suffix, hint)
	}
	v := num * mult
	if math.IsInf(v, 0) {
		return 0, fmt.Errorf("%w: %q overflows", ErrBadQuantity, s)
	}
	return v, nil
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
	bad := func(field, rule string, v any) error {
		return fmt.Errorf("%w: %s must be %s, got %v", ErrInvalidInput, field, rule, v)
	}
	switch {
	case !(s.DAU > 0):
		return bad("DAU", "> 0", s.DAU)
	case !(s.WritesPerUserPerDay > 0):
		return bad("WritesPerUserPerDay", "> 0", s.WritesPerUserPerDay)
	case !(s.ReadWriteRatio >= 0):
		return bad("ReadWriteRatio", "≥ 0", s.ReadWriteRatio)
	case !(s.WriteBytes >= 0):
		return bad("WriteBytes", "≥ 0", s.WriteBytes)
	case !(s.ReadBytes >= 0):
		return bad("ReadBytes", "≥ 0", s.ReadBytes)
	case !(s.RetentionDays >= 0):
		return bad("RetentionDays", "≥ 0", s.RetentionDays)
	case !(s.Replication >= 1):
		return bad("Replication", "≥ 1 (you keep at least one copy)", s.Replication)
	case !(s.IndexOverhead >= 0):
		return bad("IndexOverhead", "≥ 0", s.IndexOverhead)
	case !(s.PeakFactor >= 1):
		return bad("PeakFactor", "≥ 1 (a peak is never below the average)", s.PeakFactor)
	case !(s.CacheHotFraction >= 0 && s.CacheHotFraction <= 1):
		return bad("CacheHotFraction", "in [0, 1]", s.CacheHotFraction)
	case !(s.PerServerQPS > 0):
		return bad("PerServerQPS", "> 0", s.PerServerQPS)
	case !(s.TargetUtil > 0 && s.TargetUtil <= 1):
		return bad("TargetUtil", "in (0, 1]", s.TargetUtil)
	case s.Spares < 0:
		return bad("Spares", "≥ 0", s.Spares)
	case s.Latency < 0:
		return bad("Latency", "≥ 0", s.Latency)
	}
	return nil
}

// Estimate produces the full estimate sheet for s, or an error wrapping ErrInvalidInput if s fails Validate.
//
// Writes per day are DAU × WritesPerUserPerDay; reads per day are writes × ReadWriteRatio. Peak rates apply
// PeakFactor to both. Servers are sized for total peak QPS. CacheBytes is computed from read volume, which
// treats every read as a distinct object and is therefore an upper bound; when you know the number of distinct
// hot objects, size the cache from that instead.
func Estimate(s Spec) (Sheet, error) {
	if err := s.Validate(); err != nil {
		return Sheet{}, err
	}
	var sh Sheet
	sh.WritesPerDay = s.DAU * s.WritesPerUserPerDay
	sh.ReadsPerDay = sh.WritesPerDay * s.ReadWriteRatio

	sh.AvgWriteQPS = AvgQPS(s.DAU, s.WritesPerUserPerDay)
	sh.AvgReadQPS = AvgQPS(s.DAU, s.WritesPerUserPerDay*s.ReadWriteRatio)
	sh.AvgQPS = sh.AvgWriteQPS + sh.AvgReadQPS

	sh.PeakWriteQPS = PeakQPS(sh.AvgWriteQPS, s.PeakFactor)
	sh.PeakReadQPS = PeakQPS(sh.AvgReadQPS, s.PeakFactor)
	sh.PeakQPS = sh.PeakWriteQPS + sh.PeakReadQPS

	sh.StorageBytes = StorageBytes(sh.WritesPerDay, s.WriteBytes, s.RetentionDays, s.Replication) * (1 + s.IndexOverhead)

	sh.PeakIngressBytesPerSec = BandwidthBytesPerSec(sh.PeakWriteQPS, s.WriteBytes)
	sh.PeakEgressBytesPerSec = BandwidthBytesPerSec(sh.PeakReadQPS, s.ReadBytes)

	sh.CacheBytes = s.CacheHotFraction * sh.ReadsPerDay * s.ReadBytes

	n, err := ServersNeeded(sh.PeakQPS, s.PerServerQPS, s.TargetUtil, s.Spares)
	if err != nil { // unreachable after Validate, kept so a future field change cannot silently return 0 servers
		return Sheet{}, err
	}
	sh.Servers = n
	sh.PeakConcurrency = Concurrency(sh.PeakQPS, s.Latency)
	return sh, nil
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
