//go:build breakit

package main

import (
	"flag"
	"fmt"
	"math"
	"math/rand/v2"
	"sort"
)

// hedge: a read whose latency is usually ≈ 20 ms but occasionally slow
// (≈ 3% of requests take ≈ 20× longer: a GC pause, a cold cache, a noisy
// neighbour). A hedged request sends a second copy if the first has not
// answered after a delay, and uses whichever answers first.
//
// -corr is the probability that the hedge copy is slow too when the first was
// slow (0 = independent slowness, 1 = the slowness belongs to the dependency
// and every copy suffers).

func latency(r *rand.Rand) (ms float64, slow bool) {
	if r.Float64() < 0.03 {
		return 400 * math.Exp(0.5*r.NormFloat64()), true
	}
	return 20 * math.Exp(0.3*r.NormFloat64()), false
}

func pct(xs []float64, p float64) float64 { return xs[int(p*float64(len(xs)-1))] }

func hedge(args []string) {
	fs := flag.NewFlagSet("hedge", flag.ExitOnError)
	corr := fs.Float64("corr", 0, "probability the hedge copy is slow when the original was slow")
	n := fs.Int("n", 200000, "requests")
	_ = fs.Parse(args)

	run := func(after float64) (p50, p99, p999, extra float64) {
		r := rand.New(rand.NewPCG(42, 7))
		out := make([]float64, 0, *n)
		hedged := 0
		for i := 0; i < *n; i++ {
			first, slow := latency(r)
			total := first
			if after > 0 && first > after {
				hedged++
				second, secondSlow := latency(r)
				if slow && r.Float64() < *corr && !secondSlow {
					second = 400 * math.Exp(0.5*r.NormFloat64())
				}
				total = min(first, after+second)
			}
			out = append(out, total)
		}
		sort.Float64s(out)
		return pct(out, .5), pct(out, .99), pct(out, .999), 100 * float64(hedged) / float64(*n)
	}

	// Find the baseline p95 and p99 to use as hedge delays.
	r := rand.New(rand.NewPCG(42, 7))
	base := make([]float64, 0, *n)
	for i := 0; i < *n; i++ {
		l, _ := latency(r)
		base = append(base, l)
	}
	sort.Float64s(base)
	p95, p99 := pct(base, .95), pct(base, .99)

	fmt.Printf("slow-copy correlation %.0f%%, %d requests\n\n", 100**corr, *n)
	fmt.Printf("%-26s %8s %8s %8s %12s\n", "policy", "p50 ms", "p99 ms", "p99.9 ms", "extra load")
	for _, c := range []struct {
		name  string
		after float64
	}{
		{"no hedge", 0},
		{fmt.Sprintf("hedge after p99 (%.0f ms)", p99), p99},
		{fmt.Sprintf("hedge after p95 (%.0f ms)", p95), p95},
		{"hedge after 10 ms", 10},
	} {
		a, b, c2, e := run(c.after)
		fmt.Printf("%-26s %8.1f %8.1f %8.1f %11.1f%%\n", c.name, a, b, c2, e)
	}
}
