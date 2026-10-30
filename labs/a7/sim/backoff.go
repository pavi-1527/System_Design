//go:build breakit

package main

import (
	"flag"
	"fmt"
	"math/rand/v2"
	"sort"
	"time"
)

// backoff: N clients each need ONE success from a contended resource (think
// optimistic-concurrency conflicts on a hot row, or a lock). Time is divided
// into 10 ms slots. In each slot, if k ≥ 1 clients call, exactly one (chosen
// at random) succeeds and the other k−1 get a retryable error. Every failed
// client waits according to its backoff strategy and calls again.
//
// This is the experiment from the AWS Architecture Blog's "Exponential
// Backoff And Jitter", rebuilt in miniature. It counts total calls (load on
// the resource) and time until everyone has succeeded.

type client struct {
	next    time.Duration // when the client calls next
	attempt int
	prev    time.Duration // for decorrelated jitter
	done    time.Duration
}

func backoffDelay(kind string, base, max time.Duration, attempt int, prev time.Duration, r *rand.Rand) time.Duration {
	c := base << attempt
	if c > max || c <= 0 {
		c = max
	}
	switch kind {
	case "none":
		return c
	case "full":
		return time.Duration(r.Float64() * float64(c))
	case "equal":
		return c/2 + time.Duration(r.Float64()*float64(c/2))
	case "decorrelated":
		hi := prev * 3
		if hi < base {
			hi = base
		}
		d := base + time.Duration(r.Float64()*float64(hi-base))
		return min(d, max)
	}
	panic("unknown strategy " + kind)
}

func runBackoff(kind string, n int, seed uint64) (calls int, last, median time.Duration) {
	const slot = 10 * time.Millisecond
	base, max := 10*time.Millisecond, 2*time.Second
	r := rand.New(rand.NewPCG(seed, 1))
	clients := make([]*client, n)
	for i := range clients {
		clients[i] = &client{prev: base}
	}
	remaining := n
	for s := 0; remaining > 0; s++ {
		now := time.Duration(s) * slot
		var caller []*client
		for _, c := range clients {
			if c.done == 0 && c.next < now+slot {
				caller = append(caller, c)
			}
		}
		if len(caller) == 0 {
			continue
		}
		calls += len(caller)
		winner := caller[r.IntN(len(caller))]
		winner.done = now + slot
		remaining--
		for _, c := range caller {
			if c == winner {
				continue
			}
			d := backoffDelay(kind, base, max, c.attempt, c.prev, r)
			c.prev = d
			c.attempt++
			c.next = now + slot + d
		}
	}
	var dones []time.Duration
	for _, c := range clients {
		dones = append(dones, c.done)
		last = max2(last, c.done)
	}
	sort.Slice(dones, func(i, j int) bool { return dones[i] < dones[j] })
	return calls, last, dones[len(dones)/2]
}

func max2(a, b time.Duration) time.Duration {
	if a > b {
		return a
	}
	return b
}

func backoff(args []string) {
	fs := flag.NewFlagSet("backoff", flag.ExitOnError)
	n := fs.Int("n", 100, "number of contending clients")
	seeds := fs.Int("seeds", 20, "average over this many random seeds")
	_ = fs.Parse(args)

	fmt.Printf("%d clients, one success per 10 ms slot, base 10 ms, cap 2 s, mean of %d seeds\n\n", *n, *seeds)
	fmt.Printf("%-14s %12s %14s %14s\n", "strategy", "total calls", "median done", "last done")
	for _, kind := range []string{"none", "full", "equal", "decorrelated"} {
		var calls int
		var last, med time.Duration
		for s := 0; s < *seeds; s++ {
			c, l, m := runBackoff(kind, *n, uint64(s+1))
			calls += c
			last += l
			med += m
		}
		k := time.Duration(*seeds)
		fmt.Printf("%-14s %12d %14s %14s\n", kind, calls / *seeds, (med / k).Round(time.Millisecond), (last / k).Round(time.Millisecond))
	}
}
