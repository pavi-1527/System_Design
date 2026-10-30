package capacity

import (
	"errors"
	"math"
	"sync"
	"testing"
	"time"
)

// approx reports whether got is within a relative 1e-9 of want (absolute 1e-12 near zero). The expected values
// in this file are exact; the tolerance only absorbs floating-point noise, never an estimation shortcut.
func approx(got, want float64) bool {
	if want == 0 {
		return math.Abs(got) < 1e-12
	}
	return math.Abs(got-want) <= 1e-9*math.Abs(want)
}

func TestAvgQPS_DAUTimesActionsOverExactSecondsPerDay(t *testing.T) {
	cases := []struct {
		name         string
		dau, actions float64
		want         float64
	}{
		{"1M actions/day", 1e6, 1, 11.574074074074074},
		{"100M actions/day", 1e8, 1, 1157.4074074074074},
		{"500M DAU x 20", 5e8, 20, 115740.74074074074},
		{"fractional actions", 1e7, 0.1, 11.574074074074074},
		{"zero DAU", 0, 5, 0},
		{"negative DAU", -1, 5, 0},
		{"zero actions", 1e6, 0, 0},
		{"negative actions", 1e6, -2, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := AvgQPS(c.dau, c.actions); !approx(got, c.want) {
				t.Errorf("AvgQPS(%v, %v) = %v, want %v (dau × actions / 86400; using 1e5 s/day is a 14%% under-estimate)",
					c.dau, c.actions, got, c.want)
			}
		})
	}
}

func TestPeakQPS_ScalesAverageAndNeverDropsBelowIt(t *testing.T) {
	cases := []struct {
		name        string
		avg, factor float64
		want        float64
	}{
		{"3x peak", 100, 3, 300},
		{"factor 1", 100, 1, 100},
		{"factor below 1 is treated as 1", 100, 0.5, 100},
		{"zero factor is treated as 1", 100, 0, 100},
		{"zero avg", 0, 3, 0},
		{"negative avg", -5, 3, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := PeakQPS(c.avg, c.factor); !approx(got, c.want) {
				t.Errorf("PeakQPS(%v, %v) = %v, want %v (a peak below the average would under-size every box)",
					c.avg, c.factor, got, c.want)
			}
		})
	}
}

func TestStorageBytes_MultipliesRetentionAndReplication(t *testing.T) {
	cases := []struct {
		name                         string
		items, size, retention, repl float64
		want                         float64
	}{
		{"pastebin 2 years x3", 1e6, 1e4, 730, 3, 2.19e13},
		{"no replication", 1e6, 1e4, 730, 1, 7.3e12},
		{"replication 0 treated as 1", 1e6, 1e4, 730, 0, 7.3e12},
		{"one day", 1e8, 2e5, 1, 3, 6e13},
		{"zero retention keeps nothing", 1e6, 1e4, 0, 3, 0},
		{"negative items", -1, 1e4, 730, 3, 0},
		{"zero size", 1e6, 0, 730, 3, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := StorageBytes(c.items, c.size, c.retention, c.repl)
			if !approx(got, c.want) {
				t.Errorf("StorageBytes(%v, %v, %v, %v) = %v, want %v (forgetting replication under-sizes the cluster 3x)",
					c.items, c.size, c.retention, c.repl, got, c.want)
			}
		})
	}
}

func TestBandwidthBytesPerSec_AndBitsPerSec(t *testing.T) {
	if got, want := BandwidthBytesPerSec(347.22222222222223, 1e4), 3472222.2222222225; !approx(got, want) {
		t.Errorf("BandwidthBytesPerSec = %v, want %v", got, want)
	}
	if got := BandwidthBytesPerSec(-1, 1e4); got != 0 {
		t.Errorf("BandwidthBytesPerSec(-1, 1e4) = %v, want 0", got)
	}
	if got := BandwidthBytesPerSec(100, 0); got != 0 {
		t.Errorf("BandwidthBytesPerSec(100, 0) = %v, want 0", got)
	}
	// 1 Gbps is 125 MB/s: the 8x that turns "fits on one NIC" into "needs eight".
	if got, want := BitsPerSec(1.25e8), 1e9; !approx(got, want) {
		t.Errorf("BitsPerSec(125 MB/s) = %v, want %v (1 byte = 8 bits)", got, want)
	}
	if got := BitsPerSec(-3); got != 0 {
		t.Errorf("BitsPerSec(-3) = %v, want 0", got)
	}
}

func TestServersNeeded_RoundsUpAddsSparesAndIgnoresFloatNoise(t *testing.T) {
	cases := []struct {
		name            string
		peak, per, util float64
		spares          int
		want            int
	}{
		{"exact", 10000, 1000, 0.5, 1, 21},
		{"just over rounds up", 10001, 1000, 0.5, 0, 21},
		{"pastebin", 381.94444444444446, 1000, 0.6, 1, 2},
		{"at least one server before spares", 0, 1000, 0.7, 1, 2},
		{"tiny load still needs one", 100, 1000, 1, 0, 1},
		{"float noise: 4900/(700*0.7) is exactly 10", 4900, 700, 0.7, 0, 10},
		{"float noise with spares", 490, 700, 0.7, 2, 3},
		{"N+2 large fleet", 185185.1851851852, 5000, 0.7, 2, 55},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := ServersNeeded(c.peak, c.per, c.util, c.spares)
			if err != nil {
				t.Fatalf("ServersNeeded(%v, %v, %v, %d) returned error %v, want %d", c.peak, c.per, c.util, c.spares, err, c.want)
			}
			if got != c.want {
				t.Errorf("ServersNeeded(%v, %v, %v, %d) = %d, want %d (ceil(peak / (per x util)) + spares, min 1 before spares)",
					c.peak, c.per, c.util, c.spares, got, c.want)
			}
		})
	}
}

func TestServersNeeded_RejectsInputsThatCannotDescribeAFleet(t *testing.T) {
	cases := []struct {
		name            string
		peak, per, util float64
		spares          int
	}{
		{"zero per-server capacity", 1000, 0, 0.7, 1},
		{"negative per-server capacity", 1000, -10, 0.7, 1},
		{"zero utilisation", 1000, 1000, 0, 1},
		{"utilisation above 100%", 1000, 1000, 1.5, 1},
		{"negative spares", 1000, 1000, 0.7, -1},
		{"negative peak", -1, 1000, 0.7, 1},
		{"NaN peak", math.NaN(), 1000, 0.7, 1},
		{"infinite peak", math.Inf(1), 1000, 0.7, 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := ServersNeeded(c.peak, c.per, c.util, c.spares)
			if !errors.Is(err, ErrInvalidInput) {
				t.Errorf("ServersNeeded(%v, %v, %v, %d) error = %v, want ErrInvalidInput (a silent 0 or +Inf servers is worse than an error)",
					c.peak, c.per, c.util, c.spares, err)
			}
		})
	}
}

func TestConcurrency_LittlesLaw(t *testing.T) {
	cases := []struct {
		name    string
		qps     float64
		latency time.Duration
		want    float64
	}{
		{"numbers sheet example", 2000, 250 * time.Millisecond, 500},
		{"healthy vendor", 200, 300 * time.Millisecond, 60},
		{"vendor slows to 5s", 200, 5 * time.Second, 1000},
		{"sub-millisecond", 50000, 200 * time.Microsecond, 10},
		{"zero qps", 0, time.Second, 0},
		{"negative latency", 100, -time.Second, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Concurrency(c.qps, c.latency); !approx(got, c.want) {
				t.Errorf("Concurrency(%v, %v) = %v, want %v (L = λ × W; this is your pool size before headroom)",
					c.qps, c.latency, got, c.want)
			}
		})
	}
}

func TestSerialAvailability_MultipliesDependencies(t *testing.T) {
	cases := []struct {
		name  string
		avail []float64
		want  float64
	}{
		{"three 3-nines deps", []float64{0.999, 0.999, 0.999}, 0.997002999},
		{"mixed", []float64{0.9999, 0.999, 0.995}, 0.9939055995},
		{"single", []float64{0.95}, 0.95},
		{"empty is 1", nil, 1},
		{"value above 1 clamped", []float64{0.99, 1.5}, 0.99},
		{"negative clamped to 0", []float64{0.99, -0.1}, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := SerialAvailability(c.avail...); !approx(got, c.want) {
				t.Errorf("SerialAvailability(%v) = %v, want %v (every serial dependency multiplies in)", c.avail, got, c.want)
			}
		})
	}
}

func TestParallelAvailability_OneMinusProductOfFailures(t *testing.T) {
	cases := []struct {
		name  string
		avail []float64
		want  float64
	}{
		{"two 99% replicas", []float64{0.99, 0.99}, 0.9999},
		{"three 90% replicas", []float64{0.9, 0.9, 0.9}, 0.999},
		{"single", []float64{0.95}, 0.95},
		{"empty is 0", nil, 0},
		{"one perfect replica", []float64{0.5, 1}, 1},
		{"value above 1 clamped", []float64{0.9, 2}, 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := ParallelAvailability(c.avail...); !approx(got, c.want) {
				t.Errorf("ParallelAvailability(%v) = %v, want %v (1 − Π(1 − a), assuming independent failures)", c.avail, got, c.want)
			}
		})
	}
}

func TestDowntimePerYear_FromNines(t *testing.T) {
	cases := []struct {
		avail float64
		want  time.Duration
	}{
		{0.99, 87*time.Hour + 36*time.Minute},
		{0.999, 8*time.Hour + 45*time.Minute + 36*time.Second},
		{0.9999, 52*time.Minute + 34*time.Second},
		{0.99999, 5*time.Minute + 15*time.Second},
		{1, 0},
		{0, 365 * 24 * time.Hour},
		{1.2, 0},
	}
	for _, c := range cases {
		if got := DowntimePerYear(c.avail); got != c.want {
			t.Errorf("DowntimePerYear(%v) = %v, want %v (365-day year, rounded to the second)", c.avail, got, c.want)
		}
	}
}

func TestHumanBytes_DecimalSIOneDecimal(t *testing.T) {
	cases := []struct {
		in   float64
		want string
	}{
		{0, "0 B"},
		{0.5, "0.5 B"},
		{999, "999 B"},
		{1000, "1 KB"},
		{1234, "1.2 KB"},
		{999_999, "1 MB"},
		{1.5e9, "1.5 GB"},
		{4e13, "40 TB"},
		{2.409e13, "24.1 TB"},
		{2.19e16, "21.9 PB"},
		{-1.5e9, "-1.5 GB"},
		{-0.01, "0 B"},
		{1e21, "1000 EB"},
	}
	for _, c := range cases {
		if got := HumanBytes(c.in); got != c.want {
			t.Errorf("HumanBytes(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestHumanCount_SuffixesWithBnForBillion(t *testing.T) {
	cases := []struct {
		in   float64
		want string
	}{
		{0, "0"},
		{11.574074074074074, "11.6"},
		{999.96, "1k"},
		{1157.4074074074074, "1.2k"},
		{1e6, "1M"},
		{46296.2962962963, "46.3k"},
		{1.2e9, "1.2bn"},
		{3.5e12, "3.5T"},
		{-2500, "-2.5k"},
	}
	for _, c := range cases {
		if got := HumanCount(c.in); got != c.want {
			t.Errorf("HumanCount(%v) = %q, want %q (billions are \"bn\" so they never read as bytes)", c.in, got, c.want)
		}
	}
}

func TestHumanBits_LinkUnits(t *testing.T) {
	cases := []struct {
		in   float64
		want string
	}{
		{0, "0 bps"},
		{800, "800 bps"},
		{2.7777777777777776e6, "2.8 Mbps"},
		{1e9, "1 Gbps"},
		{1.1574074074074073e11, "115.7 Gbps"},
		{4e13, "40 Tbps"},
	}
	for _, c := range cases {
		if got := HumanBits(c.in); got != c.want {
			t.Errorf("HumanBits(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestParseQuantity_AcceptsEstimateNotation(t *testing.T) {
	cases := []struct {
		in   string
		want float64
	}{
		{"500", 500},
		{"86400", 86400},
		{"2e6", 2e6},
		{"2E6", 2e6},
		{"1.5e-3", 0.0015},
		{".5M", 5e5},
		{"1.2k", 1200},
		{"1.2K", 1200},
		{"3M", 3e6},
		{"1.2bn", 1.2e9},
		{"7G", 7e9},
		{"2T", 2e12},
		{"40TB", 4e13},
		{"1.5 GB", 1.5e9},
		{"10 KB", 1e4},
		{"10kB", 1e4},
		{"3B", 3},
		{"3 EB", 3e18},
		{"  7.5k  ", 7500},
		{"0", 0},
	}
	for _, c := range cases {
		got, err := ParseQuantity(c.in)
		if err != nil {
			t.Errorf("ParseQuantity(%q) returned error %v, want %v", c.in, err, c.want)
			continue
		}
		if !approx(got, c.want) {
			t.Errorf("ParseQuantity(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestParseQuantity_RejectsAmbiguousOrMalformed(t *testing.T) {
	for _, in := range []string{
		"", "   ", "abc", "k", "1.2.3", "1e", "1,000", "+5",
		"3m",            // milli, not mega
		"5MiB", "2 GiB", // binary prefixes
		"10Gb", "10Gbps", "8 bit", // bits, not bytes
		"-3k", // negative
		"NaN", "Inf",
		"1e400", "1e300E", // overflow
		"3 XB",
	} {
		v, err := ParseQuantity(in)
		if !errors.Is(err, ErrBadQuantity) {
			t.Errorf("ParseQuantity(%q) = %v, %v; want an error wrapping ErrBadQuantity (guessing here hides an 8x or 1000x mistake)",
				in, v, err)
		}
	}
}

func TestParseQuantity_ReadsBackHumanOutput(t *testing.T) {
	for _, v := range []float64{0, 7, 11.574, 1157.4, 46296.3, 3.5e6, 1.2e9, 3.5e12} {
		s := HumanCount(v)
		got, err := ParseQuantity(s)
		if err != nil || math.Abs(got-v) > 0.05*v+0.05 {
			t.Errorf("ParseQuantity(HumanCount(%v) = %q) = %v, %v; want ≈ %v", v, s, got, err, v)
		}
	}
	for _, v := range []float64{0, 999, 1234, 1.5e9, 2.409e13, 2.19e16} {
		s := HumanBytes(v)
		got, err := ParseQuantity(s)
		if err != nil || math.Abs(got-v) > 0.05*v+0.05 {
			t.Errorf("ParseQuantity(HumanBytes(%v) = %q) = %v, %v; want ≈ %v", v, s, got, err, v)
		}
	}
}

func pastebinSpec() Spec {
	return Spec{
		Name: "pastebin", DAU: 1e7, WritesPerUserPerDay: 0.1, ReadWriteRatio: 10,
		WriteBytes: 1e4, ReadBytes: 1e4, RetentionDays: 730, Replication: 3, IndexOverhead: 0.1,
		PeakFactor: 3, CacheHotFraction: 0.2, PerServerQPS: 1000, TargetUtil: 0.6, Spares: 1,
		Latency: 50 * time.Millisecond,
	}
}

func checkSheet(t *testing.T, got, want Sheet) {
	t.Helper()
	fields := []struct {
		name      string
		got, want float64
	}{
		{"WritesPerDay", got.WritesPerDay, want.WritesPerDay},
		{"ReadsPerDay", got.ReadsPerDay, want.ReadsPerDay},
		{"AvgWriteQPS", got.AvgWriteQPS, want.AvgWriteQPS},
		{"AvgReadQPS", got.AvgReadQPS, want.AvgReadQPS},
		{"AvgQPS", got.AvgQPS, want.AvgQPS},
		{"PeakWriteQPS", got.PeakWriteQPS, want.PeakWriteQPS},
		{"PeakReadQPS", got.PeakReadQPS, want.PeakReadQPS},
		{"PeakQPS", got.PeakQPS, want.PeakQPS},
		{"StorageBytes", got.StorageBytes, want.StorageBytes},
		{"PeakIngressBytesPerSec", got.PeakIngressBytesPerSec, want.PeakIngressBytesPerSec},
		{"PeakEgressBytesPerSec", got.PeakEgressBytesPerSec, want.PeakEgressBytesPerSec},
		{"CacheBytes", got.CacheBytes, want.CacheBytes},
		{"PeakConcurrency", got.PeakConcurrency, want.PeakConcurrency},
	}
	for _, f := range fields {
		if !approx(f.got, f.want) {
			t.Errorf("%s = %v, want %v", f.name, f.got, f.want)
		}
	}
	if got.Servers != want.Servers {
		t.Errorf("Servers = %d, want %d", got.Servers, want.Servers)
	}
}

func TestEstimate_PastebinSheet(t *testing.T) {
	got, err := Estimate(pastebinSpec())
	if err != nil {
		t.Fatalf("Estimate(pastebin) error = %v, want nil", err)
	}
	checkSheet(t, got, Sheet{
		WritesPerDay: 1e6, ReadsPerDay: 1e7,
		AvgWriteQPS: 11.574074074074074, AvgReadQPS: 115.74074074074075, AvgQPS: 127.31481481481482,
		PeakWriteQPS: 34.72222222222222, PeakReadQPS: 347.22222222222223, PeakQPS: 381.94444444444446,
		StorageBytes:           2.409e13, // 1M/day × 10 KB × 730 d × 3 × 1.1
		PeakIngressBytesPerSec: 347222.2222222222,
		PeakEgressBytesPerSec:  3472222.2222222225,
		CacheBytes:             2e10, // 20% of 10M reads × 10 KB
		Servers:                2,
		PeakConcurrency:        19.097222222222225,
	})
}

func TestEstimate_WriteHeavySheetNeedsAFleet(t *testing.T) {
	spec := Spec{
		Name: "chat ingest", DAU: 2e8, WritesPerUserPerDay: 20, ReadWriteRatio: 1,
		WriteBytes: 200, ReadBytes: 200, RetentionDays: 30, Replication: 3, IndexOverhead: 0.5,
		PeakFactor: 2, CacheHotFraction: 0.05, PerServerQPS: 5000, TargetUtil: 0.7, Spares: 2,
		Latency: 20 * time.Millisecond,
	}
	got, err := Estimate(spec)
	if err != nil {
		t.Fatalf("Estimate(chat ingest) error = %v, want nil", err)
	}
	checkSheet(t, got, Sheet{
		WritesPerDay: 4e9, ReadsPerDay: 4e9,
		AvgWriteQPS: 46296.2962962963, AvgReadQPS: 46296.2962962963, AvgQPS: 92592.5925925926,
		PeakWriteQPS: 92592.5925925926, PeakReadQPS: 92592.5925925926, PeakQPS: 185185.1851851852,
		StorageBytes:           1.08e14,
		PeakIngressBytesPerSec: 18518518.51851852,
		PeakEgressBytesPerSec:  18518518.51851852,
		CacheBytes:             4e10,
		Servers:                55,
		PeakConcurrency:        3703.703703703704,
	})
	if got.PeakWriteQPS < 10_000 {
		t.Errorf("PeakWriteQPS = %v; this fixture exists to show a write rate beyond one relational primary (≈1k–10k TPS)", got.PeakWriteQPS)
	}
}

func TestEstimate_ZeroReadRatioIsWriteOnly(t *testing.T) {
	spec := pastebinSpec()
	spec.ReadWriteRatio = 0
	got, err := Estimate(spec)
	if err != nil {
		t.Fatalf("Estimate error = %v, want nil", err)
	}
	if got.ReadsPerDay != 0 || got.AvgReadQPS != 0 || got.PeakReadQPS != 0 || got.PeakEgressBytesPerSec != 0 || got.CacheBytes != 0 {
		t.Errorf("write-only spec produced read traffic: %+v", got)
	}
	if !approx(got.PeakQPS, got.PeakWriteQPS) || !approx(got.PeakWriteQPS, 34.72222222222222) {
		t.Errorf("PeakQPS = %v, PeakWriteQPS = %v; want both 34.722…", got.PeakQPS, got.PeakWriteQPS)
	}
}

func TestEstimate_RejectsSpecsThatCannotDescribeASystem(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Spec)
	}{
		{"zero DAU", func(s *Spec) { s.DAU = 0 }},
		{"NaN DAU", func(s *Spec) { s.DAU = math.NaN() }},
		{"zero writes", func(s *Spec) { s.WritesPerUserPerDay = 0 }},
		{"negative ratio", func(s *Spec) { s.ReadWriteRatio = -1 }},
		{"negative write size", func(s *Spec) { s.WriteBytes = -1 }},
		{"negative read size", func(s *Spec) { s.ReadBytes = -1 }},
		{"negative retention", func(s *Spec) { s.RetentionDays = -1 }},
		{"replication below 1", func(s *Spec) { s.Replication = 0.5 }},
		{"negative index overhead", func(s *Spec) { s.IndexOverhead = -0.1 }},
		{"peak factor below 1", func(s *Spec) { s.PeakFactor = 0.8 }},
		{"hot fraction above 1", func(s *Spec) { s.CacheHotFraction = 1.2 }},
		{"zero per-server capacity", func(s *Spec) { s.PerServerQPS = 0 }},
		{"utilisation above 1", func(s *Spec) { s.TargetUtil = 1.1 }},
		{"zero utilisation", func(s *Spec) { s.TargetUtil = 0 }},
		{"negative spares", func(s *Spec) { s.Spares = -1 }},
		{"negative latency", func(s *Spec) { s.Latency = -time.Millisecond }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			spec := pastebinSpec()
			c.mutate(&spec)
			if err := spec.Validate(); !errors.Is(err, ErrInvalidInput) {
				t.Errorf("Validate() = %v, want ErrInvalidInput", err)
			}
			sh, err := Estimate(spec)
			if !errors.Is(err, ErrInvalidInput) {
				t.Errorf("Estimate() error = %v, want ErrInvalidInput (a sheet built on an impossible assumption looks plausible and is wrong)", err)
			}
			if sh != (Sheet{}) {
				t.Errorf("Estimate() returned a non-zero sheet with an error: %+v", sh)
			}
		})
	}
	if err := pastebinSpec().Validate(); err != nil {
		t.Errorf("Validate(valid pastebin spec) = %v, want nil", err)
	}
}

func TestEstimate_ConcurrentCallsAreIndependent(t *testing.T) {
	const workers = 32
	results := make([]Sheet, workers)
	errs := make([]error, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			s := pastebinSpec()
			s.DAU = float64(i+1) * 1e6
			results[i], errs[i] = Estimate(s)
		}(i)
	}
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("concurrent Estimate calls did not finish within 5s; Estimate must not block")
	}
	for i := 0; i < workers; i++ {
		if errs[i] != nil {
			t.Fatalf("worker %d: Estimate error = %v", i, errs[i])
		}
		wantWrites := float64(i+1) * 1e6 * 0.1
		if !approx(results[i].WritesPerDay, wantWrites) {
			t.Errorf("worker %d: WritesPerDay = %v, want %v (calls must not share mutable state)", i, results[i].WritesPerDay, wantWrites)
		}
	}
}
