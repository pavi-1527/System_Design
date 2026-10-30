package raftlite

// This file is PROVIDED (identical in the lab and the solution): a
// deterministic, single-threaded, in-memory network and cluster harness.
//
// Time moves in ticks. In every Tick the harness (1) delivers all messages sent
// in the previous tick, in order, (2) ticks every live node in ID order, and
// (3) checks the safety invariants. Whatever nodes return from Step/Tick is
// queued for the next tick, so every message takes exactly one tick. Crashed
// nodes neither tick nor receive; a partition drops messages between groups.
// There are no goroutines, sleeps or global random numbers.

import (
	"fmt"
	"math/rand/v2"
)

// Cluster is a simulated cluster of Nodes.
type Cluster struct {
	ids      []ID
	nodes    map[ID]*Node
	down     map[ID]bool
	group    map[ID]int // nil = fully connected
	inflight []Message
	ticks    int

	leaders    map[uint64]ID
	committed  []Entry
	violations []string
	seen       map[string]bool
}

// NewCluster builds a cluster whose nodes use a heartbeat of 3 ticks and
// election timeouts drawn uniformly from [10, 20) by per-node generators
// seeded from seed.
func NewCluster(ids []ID, seed uint64) *Cluster {
	return NewClusterWith(ids, 3, func(id ID) func() int {
		var h uint64
		for _, b := range []byte(id) {
			h = h*131 + uint64(b)
		}
		r := rand.New(rand.NewPCG(seed, h))
		return func() int { return 10 + r.IntN(10) }
	})
}

// NewClusterWith builds a cluster with an explicit heartbeat and a per-node
// election-timeout function.
func NewClusterWith(ids []ID, heartbeat int, timeout func(id ID) func() int) *Cluster {
	c := &Cluster{
		ids:     append([]ID(nil), ids...),
		nodes:   map[ID]*Node{},
		down:    map[ID]bool{},
		leaders: map[uint64]ID{},
	}
	for _, id := range ids {
		var peers []ID
		for _, p := range ids {
			if p != id {
				peers = append(peers, p)
			}
		}
		c.nodes[id] = NewNode(Config{ID: id, Peers: peers, ElectionTimeout: timeout(id), HeartbeatTicks: heartbeat})
	}
	return c
}

// Node returns the node with the given ID.
func (c *Cluster) Node(id ID) *Node { return c.nodes[id] }

// IDs returns the node IDs in the order given to the constructor.
func (c *Cluster) IDs() []ID { return append([]ID(nil), c.ids...) }

// Ticks returns how many ticks have elapsed.
func (c *Cluster) Ticks() int { return c.ticks }

func (c *Cluster) deliverable(m Message) bool {
	if c.down[m.To] {
		return false
	}
	if c.group != nil && c.group[m.From] != c.group[m.To] {
		return false
	}
	return true
}

// Tick advances the whole cluster by one tick.
func (c *Cluster) Tick() {
	c.ticks++
	batch := c.inflight
	c.inflight = nil
	for _, m := range batch {
		if !c.deliverable(m) {
			continue
		}
		c.inflight = append(c.inflight, c.nodes[m.To].Step(m)...)
	}
	for _, id := range c.ids {
		if c.down[id] {
			continue
		}
		c.inflight = append(c.inflight, c.nodes[id].Tick()...)
	}
	c.check()
}

// RunUntil ticks until cond() is true or max ticks have elapsed, and reports
// whether cond() became true.
func (c *Cluster) RunUntil(max int, cond func() bool) bool {
	for i := 0; i < max; i++ {
		if cond() {
			return true
		}
		c.Tick()
	}
	return cond()
}

// Run ticks n times.
func (c *Cluster) Run(n int) {
	for i := 0; i < n; i++ {
		c.Tick()
	}
}

// Crash stops a node: it neither ticks nor receives until Restart.
func (c *Cluster) Crash(id ID) { c.down[id] = true }

// Restart brings a crashed node back as a follower with its durable state.
func (c *Cluster) Restart(id ID) {
	if c.down[id] {
		c.down[id] = false
		c.nodes[id].Restart()
	}
}

// Up reports whether the node is running.
func (c *Cluster) Up(id ID) bool { return !c.down[id] }

// Partition splits the network: nodes talk only to members of their own
// group. Nodes not listed in any group are isolated from everyone.
func (c *Cluster) Partition(groups ...[]ID) {
	c.group = map[ID]int{}
	for gi, g := range groups {
		for _, id := range g {
			c.group[id] = gi
		}
	}
	for i, id := range c.ids {
		if _, ok := c.group[id]; !ok {
			c.group[id] = len(groups) + 1 + i
		}
	}
}

// Heal removes any partition.
func (c *Cluster) Heal() { c.group = nil }

// Leader returns the running node in the Leader role with the highest term
// ("" if there is none). A stale leader cut off from the majority may still
// believe it leads; it loses to any newer leader here.
func (c *Cluster) Leader() ID {
	var best ID
	var bestTerm uint64
	for _, id := range c.ids {
		n := c.nodes[id]
		if c.down[id] || n.Role() != Leader {
			continue
		}
		if best == "" || n.Term() > bestTerm {
			best, bestTerm = id, n.Term()
		}
	}
	return best
}

// Propose submits cmd to the current Leader() and reports who took it and the
// index it was given.
func (c *Cluster) Propose(cmd string) (leader ID, index uint64, ok bool) {
	l := c.Leader()
	if l == "" {
		return "", 0, false
	}
	idx, ok := c.nodes[l].Propose(cmd)
	return l, idx, ok
}

func (c *Cluster) violate(format string, a ...any) {
	msg := fmt.Sprintf(format, a...)
	if c.seen == nil {
		c.seen = map[string]bool{}
	}
	if c.seen[msg] {
		return // report each distinct problem once, at the tick it first appeared
	}
	c.seen[msg] = true
	c.violations = append(c.violations, fmt.Sprintf("tick %d: %s", c.ticks, msg))
}

// check records safety violations: two leaders in one term (Election Safety)
// and committed entries that differ between nodes or change over time (State
// Machine Safety).
func (c *Cluster) check() {
	for _, id := range c.ids {
		if c.down[id] {
			continue
		}
		n := c.nodes[id]
		if n.Role() == Leader {
			if prev, ok := c.leaders[n.Term()]; ok && prev != id {
				c.violate("two leaders in term %d: %s and %s", n.Term(), prev, id)
			}
			c.leaders[n.Term()] = id
		}
		log := n.Log()
		if int(n.CommitIndex()) > len(log) {
			c.violate("%s: commitIndex %d beyond its log of %d entries", id, n.CommitIndex(), len(log))
			continue
		}
		for i := 0; i < int(n.CommitIndex()); i++ {
			if i >= len(c.committed) {
				c.committed = append(c.committed, log[i])
			} else if c.committed[i] != log[i] {
				c.violate("%s: committed entry %d changed from %+v to %+v", id, i+1, c.committed[i], log[i])
			}
		}
	}
}

// Violations returns every safety violation observed so far.
func (c *Cluster) Violations() []string { return append([]string(nil), c.violations...) }

// Committed returns the globally committed prefix observed so far.
func (c *Cluster) Committed() []Entry { return append([]Entry(nil), c.committed...) }

// CheckLogMatching verifies the Log Matching property across all nodes: if two
// logs contain an entry with the same index and term, the logs are identical
// up to and including that index.
func (c *Cluster) CheckLogMatching() error {
	for i, a := range c.ids {
		for _, b := range c.ids[i+1:] {
			la, lb := c.nodes[a].Log(), c.nodes[b].Log()
			last := -1
			for k := 0; k < len(la) && k < len(lb); k++ {
				if la[k].Term == lb[k].Term {
					last = k
				}
			}
			for k := 0; k <= last; k++ {
				if la[k] != lb[k] {
					return fmt.Errorf("log matching violated between %s and %s at index %d: %+v vs %+v (they agree on term at index %d)", a, b, k+1, la[k], lb[k], last+1)
				}
			}
		}
	}
	return nil
}
