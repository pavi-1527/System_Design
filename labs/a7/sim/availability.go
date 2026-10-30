//go:build breakit

package main

import (
	"flag"
	"fmt"
	"math"
	"math/rand/v2"
	"strings"
)

// availability: what redundancy buys when failures are NOT independent.
//
// A service runs on k replicas. Each replica is down on its own for a
// fraction u of the time (independent failures). Separately, a common-cause
// event (a bad deploy, a bad config push, an expired certificate, a shared
// dependency) takes every replica down together for a fraction c of the time.
//
//	A = (1 − c) × (1 − u^k)
//
// The first table is that formula. The second table checks the formula with
// a Monte-Carlo run on a coarse failure rate (so that rare events are sampled)
// to show that the arithmetic is not a trick.

func availability(args []string) {
	fs := flag.NewFlagSet("availability", flag.ExitOnError)
	_ = fs.Parse(args)

	const minutesPerYear = 365 * 24 * 60.0
	u := 0.001 // each replica is independently down 0.1% of the time
	fmt.Println("each replica down independently 0.1% of the time (99.9%)")
	fmt.Printf("\n%-34s", "common-cause downtime c  \\  replicas k")
	ks := []int{1, 2, 3, 5}
	for _, k := range ks {
		fmt.Printf(" %14d", k)
	}
	fmt.Println()
	for _, c := range []float64{0, 0.0001, 0.0005, 0.001} {
		fmt.Printf("%-34s", fmt.Sprintf("c = %.2f%% (%.0f min/yr)", 100*c, c*minutesPerYear))
		for _, k := range ks {
			a := (1 - c) * (1 - math.Pow(u, float64(k)))
			fmt.Printf(" %14s", fmt.Sprintf("%.6f%%", 100*a))
		}
		fmt.Println()
	}

	fmt.Println("\nMonte-Carlo check (10M sampled minutes; u = 5% per replica, c = 1%):")
	r := rand.New(rand.NewPCG(5, 5))
	const trials = 10_000_000
	for _, k := range []int{1, 2, 3} {
		down := 0
		for i := 0; i < trials; i++ {
			if r.Float64() < 0.01 { // common cause
				down++
				continue
			}
			all := true
			for j := 0; j < k; j++ {
				if r.Float64() >= 0.05 {
					all = false
					break
				}
			}
			if all {
				down++
			}
		}
		sim := 1 - float64(down)/trials
		exact := (1 - 0.01) * (1 - math.Pow(0.05, float64(k)))
		fmt.Printf("  k=%d  simulated %.4f%%   formula %.4f%%   %s\n", k, 100*sim, 100*exact, strings.Repeat("·", 3))
	}
}
