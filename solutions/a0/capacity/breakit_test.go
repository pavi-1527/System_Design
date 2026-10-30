//go:build breakit

package capacity

import (
	"math"
	"testing"
	"time"
)

// TestBreakit_NaiveEstimateMatchesCorrectSheet compares NaiveEstimate with hand-checked values for a photo-sharing
// service and logs naive/correct for every field. The tolerance is 1%: on a whiteboard ±25% is fine, but code that
// other people reuse should not carry a known 14% error that then compounds with every other shortcut.
//
// Run: go test -tags breakit -run Breakit -v ./labs/a0/capacity/
func TestBreakit_NaiveEstimateMatchesCorrectSheet(t *testing.T) {
	spec := Spec{
		Name: "photo sharing", DAU: 5e7, WritesPerUserPerDay: 2, ReadWriteRatio: 50,
		WriteBytes: 2e5, ReadBytes: 1e5, RetentionDays: 365, Replication: 3, IndexOverhead: 0,
		PeakFactor: 2.5, CacheHotFraction: 0.02, PerServerQPS: 2000, TargetUtil: 0.6, Spares: 2,
		Latency: 120 * time.Millisecond,
	}
	got := NaiveEstimate(spec)

	want := []struct {
		name    string
		got     float64
		correct float64
	}{
		{"AvgQPS", got.AvgQPS, 59027.77777777778},
		{"PeakQPS", got.PeakQPS, 147569.44444444444},
		{"StorageBytes", got.StorageBytes, 2.19e16},                               // 21.9 PB for one year
		{"PeakIngressBytesPerSec", got.PeakIngressBytesPerSec, 578703703.7037036}, // ≈ 579 MB/s ≈ 4.6 Gbps
		{"PeakEgressBytesPerSec", got.PeakEgressBytesPerSec, 14467592592.592592},  // ≈ 14.5 GB/s ≈ 116 Gbps
		{"CacheBytes", got.CacheBytes, 1e13},
		{"Servers", float64(got.Servers), 125},
		{"PeakConcurrency", got.PeakConcurrency, 17708.333333333332},
	}
	t.Logf("%-24s %16s %16s %8s", "field", "naive", "correct", "ratio")
	for _, w := range want {
		ratio := w.got / w.correct
		t.Logf("%-24s %16.4g %16.4g %8.3f", w.name, w.got, w.correct, ratio)
		if math.Abs(ratio-1) > 0.01 {
			t.Errorf("%s: naive/correct = %.3f; an estimate off by %.2fx in this direction means %s",
				w.name, ratio, math.Max(ratio, 1/ratio), consequence(w.name, ratio))
		}
	}
}

func consequence(field string, ratio float64) string {
	under := ratio < 1
	switch field {
	case "StorageBytes":
		if under {
			return "the cluster fills up years early"
		}
		return "you buy disks you do not need"
	case "PeakIngressBytesPerSec", "PeakEgressBytesPerSec":
		if under {
			return "NICs, load balancers and the egress bill are all under-planned"
		}
		return "you over-provision network capacity"
	case "Servers":
		if under {
			return "the fleet melts at the first daily peak"
		}
		return "you pay for idle servers"
	}
	if under {
		return "every downstream number inherits the under-estimate"
	}
	return "every downstream number inherits the over-estimate"
}
