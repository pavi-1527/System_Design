// Package quantity parses and formats the loose numeric answers people write when
// estimating: "1.2k", "3.5 M req/s", "40TB", "12 Gbps", "2.5e6", "3x10^7", "45 min".
// It is shared by quiz grading and the estimation drills.
//
// Every answer is compared in a base unit: count, bytes, bits/s or seconds. A Unit is
// what a question asks for ("TB", "Gbps", "req/s", "minutes"); a bare number typed by
// the learner is read in that unit, while an explicit unit ("0.04 PB") is converted.
package quantity

import (
	"errors"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
)

// Dim is a physical dimension.
type Dim int

const (
	Count    Dim = iota // requests/s, servers, connections, users
	Bytes               // storage, memory, bytes/s
	Bits                // network bandwidth in bits/s
	Duration            // seconds
)

func (d Dim) String() string {
	switch d {
	case Bytes:
		return "data size in bytes"
	case Bits:
		return "data size in bits"
	case Duration:
		return "duration"
	default:
		return "count"
	}
}

// Unit is what a question asks for: a dimension plus the scale of one unit in base units.
type Unit struct {
	Dim   Dim
	Scale float64 // base units per one of this unit, e.g. TB → 1e12 bytes
	Label string
}

var (
	numRe      = regexp.MustCompile(`^([-+]?(?:\d+\.?\d*|\.\d+)(?:[eE][-+]?\d+)?)\s*(.*?)\s*$`)
	sciRe      = regexp.MustCompile(`(\d)\s*[x*×]\s*10\s*\^\s*([-+]?\d+)`)
	powRe      = regexp.MustCompile(`^10\s*\^\s*([-+]?\d+)`)
	byteBitRe  = regexp.MustCompile(`^([kKmMgGtTpP]i?)?(B|bytes?|b|bits?)(ps|/s|/sec| per second)?$`)
	countMulRe = regexp.MustCompile(`^(?i)(thousand|million|billion|trillion|mil|mn|bn|k|m|b|g|t)(?:\s+|$|[^a-z])(.*)$`)
)

var prefix = map[string]float64{
	"": 1, "k": 1e3, "m": 1e6, "g": 1e9, "t": 1e12, "p": 1e15,
	"ki": 1 << 10, "mi": 1 << 20, "gi": 1 << 30, "ti": 1 << 40, "pi": 1 << 50,
}

var countMul = map[string]float64{
	"k": 1e3, "thousand": 1e3,
	"m": 1e6, "mil": 1e6, "mn": 1e6, "million": 1e6,
	"b": 1e9, "bn": 1e9, "billion": 1e9, "g": 1e9,
	"t": 1e12, "trillion": 1e12,
}

// durations maps lower-cased duration words to seconds.
var durations = map[string]float64{
	"ns": 1e-9, "us": 1e-6, "µs": 1e-6, "ms": 1e-3,
	"s": 1, "sec": 1, "secs": 1, "second": 1, "seconds": 1,
	"m": 60, "min": 60, "mins": 60, "minute": 60, "minutes": 60,
	"h": 3600, "hr": 3600, "hrs": 3600, "hour": 3600, "hours": 3600,
	"d": 86400, "day": 86400, "days": 86400,
}

// ParseUnit interprets a unit label such as "TB", "Gbps", "req/s", "ms" or "minutes".
// Unknown labels are counts with scale 1.
func ParseUnit(label string) Unit {
	l := strings.TrimSpace(label)
	u := Unit{Dim: Count, Scale: 1, Label: l}
	if l == "" {
		return u
	}
	if bm := byteBitRe.FindStringSubmatch(l); bm != nil {
		if p, ok := prefix[strings.ToLower(bm[1])]; ok {
			u.Scale = p
			if strings.HasPrefix(bm[2], "bit") || bm[2] == "b" {
				u.Dim = Bits
			} else {
				u.Dim = Bytes
			}
			return u
		}
	}
	if s, ok := durations[strings.ToLower(l)]; ok {
		u.Dim, u.Scale = Duration, s
	}
	return u
}

// ErrEmpty is returned for blank input.
var ErrEmpty = errors.New("empty answer")

// Parse converts the learner's answer into base units (count, bytes, bits, seconds).
// note is non-empty when a conversion happened that the learner should notice.
func (u Unit) Parse(s string) (base float64, note string, err error) {
	if u.Scale == 0 {
		u.Scale = 1
	}
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, "", ErrEmpty
	}
	s = strings.ReplaceAll(s, ",", "")
	s = strings.ReplaceAll(s, "_", "")
	s = sciRe.ReplaceAllString(s, "${1}e${2}")
	if m := powRe.FindStringSubmatch(s); m != nil {
		s = "1e" + m[1] + s[len(m[0]):]
	}
	m := numRe.FindStringSubmatch(s)
	if m == nil {
		return 0, "", fmt.Errorf("could not read a number from %q", s)
	}
	v, err := strconv.ParseFloat(m[1], 64)
	if err != nil {
		return 0, "", fmt.Errorf("bad number %q: %w", m[1], err)
	}
	return u.parseRest(v, strings.TrimSpace(m[2]))
}

func (u Unit) parseRest(v float64, rest string) (float64, string, error) {
	if rest == "" {
		return v * u.Scale, "", nil
	}
	// "1.5B" means billion when a count is asked for.
	if u.Dim == Count && (rest == "B" || rest == "b") {
		return v * 1e9, "", nil
	}
	// Byte / bit units (case matters: B = bytes, b = bits).
	if bm := byteBitRe.FindStringSubmatch(rest); bm != nil && (u.Dim == Bytes || u.Dim == Bits || bm[2] != "b") {
		p, ok := prefix[strings.ToLower(bm[1])]
		if !ok {
			return 0, "", fmt.Errorf("unknown prefix %q", bm[1])
		}
		unit := bm[2]
		isBits := strings.HasPrefix(unit, "bit") || (unit == "b" && (bm[3] == "ps" || u.Dim == Bits))
		got := v * p
		switch {
		case isBits && u.Dim == Bits, !isBits && u.Dim == Bytes:
			return got, "", nil
		case isBits && u.Dim == Bytes:
			return got / 8, "converted bits to bytes (÷8)", nil
		case !isBits && u.Dim == Bits:
			return got * 8, "converted bytes to bits (×8)", nil
		default:
			return 0, "", fmt.Errorf("expected a %s, but %q is a data size", u.Dim, rest)
		}
	}
	if u.Dim == Duration {
		if s, ok := durations[strings.ToLower(rest)]; ok {
			return v * s, "", nil
		}
	}
	if cm := countMulRe.FindStringSubmatch(rest); cm != nil {
		mul := countMul[strings.ToLower(cm[1])]
		if tail := strings.TrimSpace(cm[2]); tail != "" && u.Dim != Count {
			// "1.5k MB": a multiplier followed by a real unit.
			return u.parseRest(v*mul, tail)
		}
		if u.Dim != Count {
			return v * mul, fmt.Sprintf("no unit after the multiplier; read as %s", Format(v*mul, u.Dim)), nil
		}
		return v * mul * u.Scale, "", nil
	}
	if u.Dim != Count {
		return 0, "", fmt.Errorf("did not understand unit %q (answer is a %s, e.g. %s)", rest, u.Dim, u.example())
	}
	// Anything else after a number is a label like "req/s" or "servers".
	return v * u.Scale, "", nil
}

func (u Unit) example() string {
	switch u.Dim {
	case Bytes:
		return "40TB or 1.5 GB"
	case Bits:
		return "12 Gbps"
	case Duration:
		return "250 ms or 45 min"
	}
	return "1.2k"
}

// RelErr is |got-want|/|want| (or |got| when want is zero).
func RelErr(got, want float64) float64 {
	if want == 0 {
		return math.Abs(got)
	}
	return math.Abs(got-want) / math.Abs(want)
}

// Within reports whether got is within relative tolerance tol of want.
func Within(got, want, tol float64) bool { return RelErr(got, want) <= tol+1e-9 }

// Format renders a base-unit value for humans: 1.2k, 3.4 TB, 12 Gbps, 250 ms, 44 min.
func Format(base float64, d Dim) string {
	switch d {
	case Bytes:
		return si(base, "B", " ")
	case Bits:
		return si(base, "bps", " ")
	case Duration:
		a := math.Abs(base)
		switch {
		case a < 1e-3:
			return trim(base*1e6) + " µs"
		case a < 1:
			return trim(base*1e3) + " ms"
		case a < 120:
			return trim(base) + " s"
		case a < 7200:
			return trim(base/60) + " min"
		case a < 172800:
			return trim(base/3600) + " h"
		default:
			return trim(base/86400) + " days"
		}
	default:
		return si(base, "", "")
	}
}

// Format renders a base-unit value using this unit's dimension.
func (u Unit) Format(base float64) string { return Format(base, u.Dim) }

func si(v float64, unit, sep string) string {
	abs := math.Abs(v)
	type step struct {
		f float64
		p string
	}
	steps := []step{{1e15, "P"}, {1e12, "T"}, {1e9, "G"}, {1e6, "M"}, {1e3, "k"}}
	if unit == "" {
		steps = []step{{1e12, "T"}, {1e9, "B"}, {1e6, "M"}, {1e3, "k"}}
	}
	for _, s := range steps {
		if abs >= s.f {
			return trim(v/s.f) + sep + s.p + unit
		}
	}
	if unit == "" {
		return trim(v)
	}
	return trim(v) + sep + unit
}

// Trim formats a float with 2–3 significant digits and no trailing zeros.
func Trim(v float64) string { return trim(v) }

func trim(v float64) string {
	var s string
	switch a := math.Abs(v); {
	case a >= 100:
		s = strconv.FormatFloat(v, 'f', 0, 64)
	case a >= 10:
		s = strconv.FormatFloat(v, 'f', 1, 64)
	default:
		s = strconv.FormatFloat(v, 'f', 2, 64)
	}
	if strings.Contains(s, ".") {
		s = strings.TrimRight(strings.TrimRight(s, "0"), ".")
	}
	return s
}
