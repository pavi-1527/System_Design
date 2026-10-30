//go:build breakit

package main

import (
	"flag"
	"fmt"
	"math"
	"math/rand/v2"
)

// distlimit: one tenant with a limit of 1,000 req/s, served by 10 API nodes.
// Poisson arrivals, virtual time, token buckets with a 100-token burst.
//
//	shared      one bucket (rate 1000/s) that every node consults, e.g. in Redis
//	local       every node enforces 1000/10 = 100 req/s on its own
//	local+2     the same, but sticky routing pins this tenant to 2 of the 10 nodes
//
// The question: how many requests are rejected although the tenant is within
// its limit, and how many are admitted beyond it?

type bucket struct {
	tokens, rate, burst, last float64
}

func (b *bucket) allow(now float64) bool {
	b.tokens = math.Min(b.burst, b.tokens+(now-b.last)*b.rate)
	b.last = now
	if b.tokens >= 1 {
		b.tokens--
		return true
	}
	return false
}

func distlimit(args []string) {
	fs := flag.NewFlagSet("distlimit", flag.ExitOnError)
	secs := fs.Int("secs", 60, "virtual seconds to simulate")
	nodesFlag := fs.Int("nodes", 10, "number of API nodes (each enforces limit/nodes with a burst of 10)")
	_ = fs.Parse(args)
	nodes := *nodesFlag
	const limit, burst = 1000.0, 100.0 // shared bucket: 1,000/s, burst 100

	run := func(offered float64, layout string) (admitted, total int) {
		r := rand.New(rand.NewPCG(11, 3))
		var shared *bucket
		local := make([]*bucket, nodes)
		for i := range local {
			local[i] = &bucket{tokens: 10, rate: limit / float64(nodes), burst: 10}
		}
		shared = &bucket{tokens: burst, rate: limit, burst: burst}
		now := 0.0
		for now < float64(*secs) {
			now += r.ExpFloat64() / offered
			total++
			var ok bool
			switch layout {
			case "shared":
				ok = shared.allow(now)
			case "local":
				ok = local[r.IntN(nodes)].allow(now)
			case "local+2":
				// sticky routing: this tenant lands on nodes 0 and 1 only,
				// so only two local buckets are ever used.
				ok = local[r.IntN(2)].allow(now)
			}
			if ok {
				admitted++
			}
		}
		return admitted, total
	}

	fmt.Printf("limit %.0f req/s per tenant, %d nodes (local limit %.0f req/s, burst 10), %d virtual seconds, Poisson arrivals\n\n", limit, nodes, limit/float64(nodes), *secs)
	fmt.Printf("%-14s %-10s %12s %12s %14s\n", "offered", "layout", "admitted/s", "rejected", "vs. limit")
	for _, offered := range []float64{500, 800, 950, 1500} {
		for _, layout := range []string{"shared", "local", "local+2"} {
			adm, tot := run(offered, layout)
			rate := float64(adm) / float64(*secs)
			note := ""
			switch {
			case offered <= limit && adm < tot:
				note = fmt.Sprintf("%.1f%% wrongly rejected", 100*float64(tot-adm)/float64(tot))
			case offered > limit:
				note = fmt.Sprintf("%+.0f%% vs limit", 100*(rate-limit)/limit)
			}
			fmt.Printf("%-14s %-10s %12.0f %11.1f%% %s\n", fmt.Sprintf("%.0f req/s", offered), layout, rate,
				100*float64(tot-adm)/float64(tot), note)
		}
	}
}
