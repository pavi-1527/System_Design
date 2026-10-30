//go:build breakit

package snowflake

import (
	"errors"
	"fmt"
	"hash/fnv"
	"math/rand"
	"sort"
	"sync"
	"testing"
	"time"
)

// tNaive is the generator most blog posts show: read the clock; same tick as
// last time → seq++; any other tick → seq = 0. It has a mutex and it waits on
// overflow, but it trusts the clock: after a step back it simply starts again
// at seq 0 in a tick it has already used. It uses your Layout. Run with:
//
//	go test -tags breakit -run BreakIt_ClockStepBack -v ./labs/c3/snowflake/
type tNaive struct {
	mu    sync.Mutex
	l     Layout
	node  uint64
	clock Clock
	last  uint64
	seq   uint64
}

func (n *tNaive) Next() (ID, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	for {
		now, err := n.l.TickOf(n.clock.Now())
		if err != nil {
			return 0, err
		}
		if now != n.last {
			n.last, n.seq = now, 0
			return n.l.Compose(now, n.node, 0)
		}
		if n.seq+1 < n.l.PerTick() {
			n.seq++
			return n.l.Compose(now, n.node, n.seq)
		}
		n.clock.Sleep(n.l.Tick)
	}
}

// 30,000 ids over 3 s (10 per ms), then the clock steps back, then 30,000 more.
func TestBreakIt_ClockStepBack(t *testing.T) {
	l := tLayout(41, 10, 12)
	t.Logf("%-8s | %-16s | %-16s | %-14s | %s", "step", "naive duplicates", "yours duplicates", "yours refusals", "yours slept")
	for _, step := range []time.Duration{2 * time.Millisecond, 10 * time.Millisecond, 50 * time.Millisecond, 2 * time.Second} {
		run := func(next func() (ID, error), c *tClock) (dups, refusals int) {
			seen := make(map[ID]bool, 60000)
			mint := func(n int) {
				for i := 0; i < n; {
					id, err := next()
					if errors.Is(err, ErrClockBackwards) {
						refusals++
						c.Add(time.Millisecond) // the caller retries a millisecond later
						continue
					}
					if err != nil {
						t.Fatalf("Next: %v", err)
					}
					if seen[id] {
						dups++
					}
					seen[id] = true
					i++
				}
			}
			mint(30000)
			c.Add(-step)
			mint(30000)
			return dups, refusals
		}
		cn := tNewClock(tStart)
		cn.step = 100 * time.Microsecond
		naive := &tNaive{l: l, node: 1, clock: cn}
		nd, _ := run(naive.Next, cn)

		cy := tNewClock(tStart)
		cy.step = 100 * time.Microsecond
		g := tGen(t, Config{Layout: l, Node: 1, Clock: cy, MaxBackwards: 10 * time.Millisecond})
		yd, yr := run(g.Next, cy)
		_, slept := cy.Stats()
		t.Logf("%-8v | %16d | %16d | %14d | %v", step, nd, yd, yr, slept)
		if yd != 0 {
			t.Errorf("your generator minted %d duplicates after a %v step back", yd, step)
		}
	}
}

// Two of your generators, copy-pasted with the same node id, each serving a
// Poisson stream. Duplicates per million ids, by rate. Run with:
//
//	go test -tags breakit -run BreakIt_SharedNodeID -v ./labs/c3/snowflake/
func TestBreakIt_SharedNodeID(t *testing.T) {
	l := tLayout(41, 10, 12)
	const perGen = 100000
	t.Logf("%-18s | %-10s | %s", "ids/s per process", "duplicates", "per million ids")
	for _, rate := range []float64{1, 10, 100, 1000} {
		rng := rand.New(rand.NewSource(42))
		type event struct {
			at  time.Time
			gen int
		}
		events := make([]event, 0, 2*perGen)
		for gen := 0; gen < 2; gen++ {
			at := tStart
			for i := 0; i < perGen; i++ {
				at = at.Add(time.Duration(rng.ExpFloat64() / rate * float64(time.Second)))
				events = append(events, event{at, gen})
			}
		}
		sort.Slice(events, func(i, j int) bool { return events[i].at.Before(events[j].at) })
		c := tNewClock(tStart)
		gens := []*Generator{
			tGen(t, Config{Layout: l, Node: 5, Clock: c, MaxBackwards: 10 * time.Millisecond}),
			tGen(t, Config{Layout: l, Node: 5, Clock: c, MaxBackwards: 10 * time.Millisecond}),
		}
		seen := make(map[ID]bool, 2*perGen)
		dups := 0
		for _, e := range events {
			if e.at.After(c.Peek()) {
				c.Set(e.at)
			}
			id := tNext(t, gens[e.gen])
			if seen[id] {
				dups++
			}
			seen[id] = true
		}
		t.Logf("%-18.0f | %10d | %.0f", rate, dups, float64(dups)/float64(2*perGen)*1e6)
	}
}

// Capacity of a few layouts, computed with your Layout methods. Run with:
//
//	go test -tags breakit -run BreakIt_Layouts -v ./labs/c3/snowflake/
func TestBreakIt_Layouts(t *testing.T) {
	layouts := []struct {
		name string
		l    Layout
	}{
		{"41/10/12, 1 ms (classic)", Layout{41, 10, 12, time.Millisecond, DefaultEpoch}},
		{"41/11/11, 1 ms", Layout{41, 11, 11, time.Millisecond, DefaultEpoch}},
		{"42/9/12, 1 ms", Layout{42, 9, 12, time.Millisecond, DefaultEpoch}},
		{"39/16/8, 10 ms", Layout{39, 16, 8, 10 * time.Millisecond, DefaultEpoch}},
		{"31/16/16, 1 s", Layout{31, 16, 16, time.Second, DefaultEpoch}},
		{"41/10/12, 1 ms, 1970 epoch", Layout{41, 10, 12, time.Millisecond, time.Unix(0, 0).UTC()}},
	}
	t.Logf("%-28s | %-8s | %-8s | %-6s | %-12s | %s", "layout", "years", "ends", "nodes", "ids/s/node", "ids/s fleet")
	for _, x := range layouts {
		if err := x.l.Validate(); err != nil {
			t.Fatalf("%s: %v", x.name, err)
		}
		life := x.l.Lifetime()
		years := life.Hours() / 24 / 365.25
		t.Logf("%-28s | %8.1f | %8d | %6d | %12.0f | %.3g", x.name, years, x.l.Epoch.Add(life).Year(),
			x.l.Nodes(), x.l.NodeRate(), x.l.NodeRate()*float64(x.l.Nodes()))
	}
}

// 10,000 ids from one node at one id every 3 ms, sharded three ways. Run with:
//
//	go test -tags breakit -run BreakIt_ShardByModulo -v ./labs/c3/snowflake/
func TestBreakIt_ShardByModulo(t *testing.T) {
	l := tLayout(41, 10, 12)
	c := tNewClock(tStart)
	g := tGen(t, Config{Layout: l, Node: 5, Clock: c})
	var mod16, mod10, hash16 [16]int
	for i := 0; i < 10000; i++ {
		id := tNext(t, g)
		c.Add(3 * time.Millisecond)
		mod16[id%16]++
		mod10[id%10]++
		h := fnv.New64a()
		fmt.Fprintf(h, "%d", id)
		hash16[h.Sum64()%16]++
	}
	t.Logf("id %% 16      : %v", mod16)
	t.Logf("id %% 10      : %v", mod10[:10])
	t.Logf("fnv(id) %% 16 : %v", hash16)
}

// A paused old holder and a skewed new holder, without and with leases.
// Run with:
//
//	go test -tags breakit -run BreakIt_PausedHolder -v ./labs/c3/snowflake/
func TestBreakIt_PausedHolder(t *testing.T) {
	l := tLayout(41, 10, 12)

	// 1. Zombie: a keeps minting on node 0 after b took it, clocks in sync.
	{
		c := tNewClock(tStart.Add(15500 * time.Millisecond))
		a := tGen(t, Config{Layout: l, Node: 0, Clock: c})
		b := tGen(t, Config{Layout: l, Node: 0, Clock: c})
		seen, dups := map[ID]bool{}, 0
		for i := 0; i < 1000; i++ {
			for _, g := range []*Generator{a, b} {
				id := tNext(t, g)
				if seen[id] {
					dups++
				}
				seen[id] = true
			}
			c.Add(time.Millisecond)
		}
		t.Logf("zombie old holder, plain generators     : %4d duplicates of 2,000 ids", dups)
	}
	{
		c := tNewClock(tStart)
		r := tRegistry(t, 1, 10*time.Second, c)
		a := tWorker(t, r, "a", WorkerConfig{Layout: l, Clock: c, Margin: time.Second})
		c.Set(tStart.Add(15500 * time.Millisecond))
		b := tWorker(t, r, "b", WorkerConfig{Layout: l, Clock: c, Margin: time.Second, MaxBackwards: time.Second})
		seen, dups, refused := map[ID]bool{}, 0, 0
		for i := 0; i < 1000; i++ {
			for _, w := range []*Worker{a, b} {
				id, err := w.Next()
				if err != nil {
					refused++
					continue
				}
				if seen[id] {
					dups++
				}
				seen[id] = true
			}
			c.Add(time.Millisecond)
		}
		t.Logf("zombie old holder, workers              : %4d duplicates, %d refused (the old holder's deadline)", dups, refused)
	}

	// 2. Skewed successor: b's clock is 7 s behind and it starts in ticks a used.
	mintA := func(next func() (ID, error), c *tClock, seen map[ID]bool) {
		for c.Peek().Before(tStart.Add(8900 * time.Millisecond)) {
			id, err := next()
			if err != nil {
				t.Fatalf("a: %v", err)
			}
			seen[id] = true
			c.Add(time.Millisecond)
		}
	}
	{
		c := tNewClock(tStart)
		a := tGen(t, Config{Layout: l, Node: 0, Clock: c})
		seen := map[ID]bool{}
		mintA(a.Next, c, seen)
		c.Set(tStart.Add(15500 * time.Millisecond))
		b := tGen(t, Config{Layout: l, Node: 0, Clock: tSkewed{base: c, off: -7 * time.Second}})
		dups := 0
		for i := 0; i < 1000; i++ {
			id := tNext(t, b)
			if seen[id] {
				dups++
			}
			seen[id] = true
			c.Add(time.Millisecond)
		}
		t.Logf("skewed new holder, plain generators     : %4d duplicates of b's 1,000 ids", dups)
	}
	{
		c := tNewClock(tStart)
		r := tRegistry(t, 1, 10*time.Second, c)
		a := tWorker(t, r, "a", WorkerConfig{Layout: l, Clock: c, Margin: time.Second})
		seen := map[ID]bool{}
		mintA(a.Next, c, seen)
		c.Set(tStart.Add(15500 * time.Millisecond))
		b := tWorker(t, r, "b", WorkerConfig{Layout: l, Clock: tSkewed{base: c, off: -7 * time.Second}, Margin: time.Second, MaxBackwards: time.Second})
		dups := 0
		for i := 0; i < 1000; i++ {
			id := tNext(t, b)
			if seen[id] {
				dups++
			}
			seen[id] = true
			c.Add(time.Millisecond)
		}
		_, slept := c.Stats()
		t.Logf("skewed new holder, workers              : %4d duplicates (b waited %v for its floor)", dups, slept)
	}
}
