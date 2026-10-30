//go:build breakit

package histogram

// Break-it experiments for A8 Unit 1 §3–§4. These are measurements, not
// pass/fail tests: they print tables so you can predict first and compare.
//
// Run: go test -tags breakit -run BreakIt -v ./labs/a8/histogram/
//
// (The third break-it, removing the mutex, is wired through breakNoLock; see
// breakit_on.go and labs/a8/README.md.)

import (
	"fmt"
	"math"
	"testing"
)

// TestBreakIt_BucketsStraddlingTheSLOThreshold: the SLO says "99% of requests
// complete in under 300 ms". The service's real latencies are lognormal with a
// median of 200 ms. A Prometheus-style histogram has no bucket edge at 300 ms,
// so the SLI must be read from the nearest edge, the sketch brackets it, and
// a histogram with an edge ON the threshold is exact.
func TestBreakIt_BucketsStraddlingTheSLOThreshold(t *testing.T) {
	const slo = 0.300
	r := tRand(1)
	samples := make([]float64, 200_000)
	for i := range samples {
		samples[i] = 0.200 * math.Exp(0.5*r.NormFloat64())
	}
	var truth int
	for _, v := range samples {
		if v <= slo {
			truth++
		}
	}
	frac := func(n uint64) string { return fmt.Sprintf("%.4f%%", 100*float64(n)/float64(len(samples))) }

	t.Logf("true share of requests <= 300 ms: %.4f%%", 100*float64(truth)/float64(len(samples)))
	for _, layout := range []struct {
		name   string
		bounds []float64
	}{
		{"edges 0.1 0.25 0.5 1 (no edge near 300ms)", []float64{0.1, 0.25, 0.5, 1}},
		{"edges 0.1 0.2 0.3 0.5 1 (edge ON the SLO)", []float64{0.1, 0.2, 0.3, 0.5, 1}},
	} {
		bs := FixedBuckets(layout.bounds, samples)
		var below, above uint64 // reading the SLI at the nearest edge below / above 300 ms
		for _, b := range bs {
			if b.UpperBound <= slo {
				below = b.CumulativeCount
			}
		}
		for i := len(bs) - 1; i >= 0; i-- {
			if bs[i].UpperBound >= slo {
				above = bs[i].CumulativeCount
			}
		}
		t.Logf("%-46s SLI read at edge below: %s   at edge above: %s", layout.name, frac(below), frac(above))
	}
	s, err := NewSketch(0.01)
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range samples {
		s.Record(v)
	}
	lo, hi := s.CountAtMost(slo)
	t.Logf("%-46s SLI bracket: [%s, %s]   (%d buckets in memory)", "sketch, alpha = 1%", frac(lo), frac(hi), s.BucketCount())
	t.Log("Predict first: which of these could tell you the error budget is burning at 2x instead of 1x?")
}

// TestBreakIt_AverageOfP99s: two instances behind one load balancer, unequal
// traffic. Sweep the slow instance's share and watch three "fleet p99"
// answers against the truth.
func TestBreakIt_AverageOfP99s(t *testing.T) {
	const total = 100_000
	t.Logf("%-12s %10s %10s %10s %10s", "slow share", "true p99", "naive avg", "weighted", "merged")
	for _, slowShare := range []float64{0.002, 0.005, 0.0099, 0.0101, 0.02, 0.05} {
		nSlow := int(slowShare * total)
		r := tRand(5)
		fast, slow := mustSketch(t), mustSketch(t)
		var all []float64
		for i := 0; i < total-nSlow; i++ {
			v := 10 * (0.95 + 0.1*r.Float64())
			fast.Record(v)
			all = append(all, v)
		}
		for i := 0; i < nSlow; i++ {
			v := 1000 * (0.95 + 0.1*r.Float64())
			slow.Record(v)
			all = append(all, v)
		}
		truth := tExact(tSorted(all), 0.99)
		inst := []*Sketch{fast, slow}
		naive, _ := NaiveAverageQuantile(inst, 0.99)
		weighted, _ := WeightedAverageQuantile(inst, 0.99)
		merged, _ := MergedQuantile(inst, 0.99)
		t.Logf("%-12s %9.1fms %9.1fms %9.1fms %9.1fms", fmt.Sprintf("%.2f%%", 100*slowShare), truth, naive, weighted, merged)
	}
	t.Log("The slow instance serves a fraction of the traffic; the true p99 flips from ~10 ms to ~1000 ms")
	t.Log("exactly when that fraction crosses 1%. Neither average notices.")
}

func mustSketch(t *testing.T) *Sketch {
	t.Helper()
	s, err := NewSketch(0.01)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// TestBreakIt_FixedBucketQuantileError: how wrong can histogram_quantile be?
// The same traffic read through three bucket layouts and a sketch.
func TestBreakIt_FixedBucketQuantileError(t *testing.T) {
	r := tRand(9)
	samples := make([]float64, 100_000)
	for i := range samples {
		samples[i] = 0.05 * math.Exp(0.9*r.NormFloat64()) // median 50 ms, long tail
	}
	sorted := tSorted(samples)
	s := mustSketch(t)
	for _, v := range samples {
		s.Record(v)
	}
	layouts := []struct {
		name   string
		bounds []float64
	}{
		{"coarse  (0.1 0.5 1 5)", []float64{0.1, 0.5, 1, 5}},
		{"default (.005 .01 .025 .05 .1 .25 .5 1)", []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1}},
		{"fine    (x1.5 steps, 14 buckets)", []float64{0.01, 0.015, 0.0225, 0.034, 0.05, 0.076, 0.114, 0.17, 0.26, 0.38, 0.57, 0.86, 1.3, 1.9}},
	}
	t.Logf("%-42s %12s %12s %12s", "estimator", "p50", "p99", "p99.9")
	row := func(name string, f func(q float64) float64) {
		t.Logf("%-42s %11.2fms %11.2fms %11.2fms", name, 1000*f(0.5), 1000*f(0.99), 1000*f(0.999))
	}
	row("exact", func(q float64) float64 { return tExact(sorted, q) })
	row("sketch alpha=1%", func(q float64) float64 { v, _ := s.Quantile(q); return v })
	for _, l := range layouts {
		bs := FixedBuckets(l.bounds, samples)
		row(l.name, func(q float64) float64 { return PromQuantile(q, bs) })
	}
}
