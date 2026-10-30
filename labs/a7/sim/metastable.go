//go:build breakit

package main

import (
	"flag"
	"fmt"
	"os"
	"strings"
)

// metastable: a server with a FIFO queue, clients that time out and retry, a
// temporary capacity drop (the trigger), and the question "does the system
// recover when the trigger ends?"
//
// Virtual time, 10 ms ticks. Offered load 80 req/s of new user requests;
// capacity 100 req/s; for 5 s the capacity falls to 30 req/s. Clients give up
// after 1 s and, if the mode allows, immediately send another attempt (up to
// 3 attempts per user request). The server cannot tell that a queued request
// has been abandoned unless the mode says it checks the deadline.
//
// "Goodput" counts only completions that reach a client that is still
// waiting. Work done for a client that has already gone is waste.

type mode struct {
	name        string
	retries     int  // retries after the first attempt
	budget      bool // shared 10% retry budget
	dropExpired bool // server skips requests whose client has already given up
	lifo        bool // serve the newest request first
	qmax        int  // >0: reject at arrival when the queue is this long
}

var modes = []mode{
	{name: "no-retry"},
	{name: "naive", retries: 2},
	{name: "budget", retries: 2, budget: true},
	{name: "deadline", retries: 2, dropExpired: true},
	{name: "lifo", retries: 2, dropExpired: true, lifo: true},
	{name: "shed", retries: 2, qmax: 25},
	{name: "shed+budget", retries: 2, qmax: 25, budget: true},
}

const (
	tickMS       = 10
	ticksPerSec  = 1000 / tickMS
	clientTO     = 100 // ticks: 1 s
	arrivalsTick = 0.8 // 80 req/s
	capNormal    = 1.0 // 100 req/s
	capTrigger   = 0.3 // 30 req/s
	triggerFrom  = 10 * ticksPerSec
	triggerTo    = 15 * ticksPerSec
	runTicks     = 40 * ticksPerSec
)

type attempt struct {
	deadline int
	done     bool
	user     *user
}

type user struct{ attempts int }

type result struct {
	goodput  [40]int // useful completions per second
	sent     [40]int // attempts sent per second
	maxQueue int
	failed   int // user requests that never succeeded
	wasted   int // completions for clients that had left
	rejected int
}

func simulate(m mode) result {
	var res result
	var queue []*attempt
	var outstanding []*attempt // by deadline (monotone)
	var delayed []struct {
		at int
		u  *user
	}
	acc, credit, tokens := 0.0, 0.0, 10.0
	sec := func(t int) int { return min(t/ticksPerSec, 39) }

	var send func(t int, u *user)
	allowRetry := func(u *user) bool {
		if u.attempts > m.retries {
			return false
		}
		if m.budget {
			if tokens < 1 {
				return false
			}
			tokens--
		}
		return true
	}
	fail := func(t int, u *user) {
		if allowRetry(u) {
			send(t, u)
		} else {
			res.failed++
		}
	}
	send = func(t int, u *user) {
		u.attempts++
		res.sent[sec(t)]++
		if m.qmax > 0 && len(queue) >= m.qmax {
			res.rejected++
			delayed = append(delayed, struct {
				at int
				u  *user
			}{t + 20, u}) // a fast error; the client backs off 200 ms
			return
		}
		a := &attempt{deadline: t + clientTO, user: u}
		queue = append(queue, a)
		outstanding = append(outstanding, a)
	}

	for t := 0; t < runTicks; t++ {
		// 1. new user requests
		acc += arrivalsTick
		for acc >= 1 {
			acc--
			if m.budget {
				tokens = min(tokens+0.1, 10)
			}
			send(t, &user{})
		}
		// 2. delayed retries (after a rejection)
		due := delayed
		delayed = nil // send may append new delayed retries while we iterate
		for _, d := range due {
			if d.at > t {
				delayed = append(delayed, d)
			} else if allowRetry(d.u) {
				send(t, d.u)
			} else {
				res.failed++
			}
		}
		// 3. client timeouts
		for len(outstanding) > 0 && outstanding[0].deadline <= t {
			a := outstanding[0]
			outstanding = outstanding[1:]
			if !a.done {
				fail(t, a.user)
			}
		}
		// 4. the server
		c := capNormal
		if t >= triggerFrom && t < triggerTo {
			c = capTrigger
		}
		credit = min(credit+c, 1+c)
		for credit >= 1 && len(queue) > 0 {
			var a *attempt
			if m.lifo {
				a = queue[len(queue)-1]
				queue = queue[:len(queue)-1]
			} else {
				a = queue[0]
				queue = queue[1:]
			}
			if m.dropExpired && t >= a.deadline {
				continue // free: the server notices the client is gone
			}
			credit--
			a.done = true
			if t < a.deadline {
				res.goodput[sec(t)]++
			} else {
				res.wasted++
			}
		}
		if len(queue) == 0 {
			credit = min(credit, 1)
		}
		res.maxQueue = max(res.maxQueue, len(queue))
	}
	return res
}

func avg(a [40]int, from, to int) float64 {
	s := 0
	for i := from; i < to; i++ {
		s += a[i]
	}
	return float64(s) / float64(to-from)
}

func metastable(args []string) {
	fs := flag.NewFlagSet("metastable", flag.ExitOnError)
	which := fs.String("mode", "all", "all or one of: "+strings.Join(modeNames(), ", "))
	verbose := fs.Bool("v", false, "print the per-second goodput and attempt rate")
	qmax := fs.Int("qmax", 0, "override the queue limit of the shed modes (default 25)")
	_ = fs.Parse(args)

	fmt.Println("goodput = useful completions/s. offered load 80 req/s, capacity 100 req/s,")
	fmt.Println("capacity 30 req/s during t=10..15 s (the trigger). Client timeout 1 s, up to 3 attempts.")
	fmt.Printf("\n%-12s %8s %8s %8s %8s %9s %9s %9s\n", "mode", "0-10s", "10-15s", "15-25s", "25-40s", "max queue", "user fail", "wasted")
	for _, m := range modes {
		if *which != "all" && *which != m.name {
			continue
		}
		if *qmax > 0 && m.qmax > 0 {
			m.qmax = *qmax
		}
		r := simulate(m)
		fmt.Printf("%-12s %8.0f %8.0f %8.0f %8.0f %9d %9d %9d\n", m.name,
			avg(r.goodput, 0, 10), avg(r.goodput, 10, 15), avg(r.goodput, 15, 25), avg(r.goodput, 25, 40),
			r.maxQueue, r.failed, r.wasted)
		if *verbose {
			fmt.Print("  goodput/s:")
			for i := 8; i < 40; i++ {
				fmt.Printf(" %d", r.goodput[i])
			}
			fmt.Print("\n  attempts/s:")
			for i := 8; i < 40; i++ {
				fmt.Printf(" %d", r.sent[i])
			}
			fmt.Println()
		}
	}
	if *which != "all" {
		for _, m := range modes {
			if m.name == *which {
				return
			}
		}
		fmt.Fprintf(os.Stderr, "unknown mode %q\n", *which)
		os.Exit(2)
	}
}

func modeNames() []string {
	var n []string
	for _, m := range modes {
		n = append(n, m.name)
	}
	return n
}
