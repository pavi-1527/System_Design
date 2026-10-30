package replkv

import (
	"sort"
	"sync"
)

// This file is the deterministic network simulator. It is provided: the
// lab is about what the leader, followers and clients do with it, not about
// building it. There are no goroutines, timers or sleeps in here. Time is a
// counter that only moves when somebody calls Tick.

// Delivery is one message that Tick hands back: entry e arriving at node To.
type Delivery struct {
	To    NodeID
	Entry Entry
}

type message struct {
	to        NodeID
	entry     Entry
	deliverAt int64
	seq       uint64
}

// Network carries replication entries from the leader to followers with a
// per-destination delay, and lets a test cut a link. Safe for concurrent use.
//
// Semantics (the tests rely on them):
//   - Send never delivers anything. A message sent at time T with delay d is
//     due at T + max(d, 1); only Tick delivers.
//   - SetDelay affects messages sent after the call. Because each message
//     carries its own due time, a later message can overtake an earlier one:
//     the simulator reorders, and followers must cope.
//   - A cut link (Partition) holds its messages: they are neither delivered
//     nor lost, and become deliverable on the first Tick after the link is
//     healed (their due time has usually passed by then).
//   - Tick returns due, deliverable messages ordered by (due time, send
//     order).
type Network struct {
	mu    sync.Mutex
	now   int64
	seq   uint64
	delay map[NodeID]int
	cut   map[NodeID]bool
	queue []message
}

// NewNetwork returns a network at time 0 with every link at delay 1.
func NewNetwork() *Network {
	return &Network{delay: map[NodeID]int{}, cut: map[NodeID]bool{}}
}

// Now returns the current simulated time (number of Ticks so far).
func (n *Network) Now() int64 {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.now
}

// SetDelay sets the delay, in ticks, for messages sent to node from now on.
// Values below 1 behave as 1.
func (n *Network) SetDelay(to NodeID, ticks int) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.delay[to] = ticks
}

// Partition cuts (cut=true) or heals (cut=false) the link to node.
func (n *Network) Partition(to NodeID, cut bool) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.cut[to] = cut
}

// Send schedules entry e for node to.
func (n *Network) Send(to NodeID, e Entry) {
	n.mu.Lock()
	defer n.mu.Unlock()
	d := n.delay[to]
	if d < 1 {
		d = 1
	}
	n.seq++
	n.queue = append(n.queue, message{to: to, entry: e, deliverAt: n.now + int64(d), seq: n.seq})
}

// Tick advances time by one and returns every message that is now due and
// whose link is not cut, removing them from the network.
func (n *Network) Tick() []Delivery {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.now++
	var due, rest []message
	for _, m := range n.queue {
		if m.deliverAt <= n.now && !n.cut[m.to] {
			due = append(due, m)
		} else {
			rest = append(rest, m)
		}
	}
	n.queue = rest
	sort.Slice(due, func(i, j int) bool {
		if due[i].deliverAt != due[j].deliverAt {
			return due[i].deliverAt < due[j].deliverAt
		}
		return due[i].seq < due[j].seq
	})
	out := make([]Delivery, len(due))
	for i, m := range due {
		out[i] = Delivery{To: m.to, Entry: m.entry}
	}
	return out
}

// DropAll discards every message in flight (used when a leader dies: its
// connections are gone).
func (n *Network) DropAll() {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.queue = nil
}

// InFlight returns the number of messages in the network, including those
// held behind a cut link.
func (n *Network) InFlight() int {
	n.mu.Lock()
	defer n.mu.Unlock()
	return len(n.queue)
}

// Idle reports whether nothing can ever be delivered without a heal: the
// network is empty, or every remaining message sits behind a cut link.
func (n *Network) Idle() bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	for _, m := range n.queue {
		if !n.cut[m.to] {
			return false
		}
	}
	return true
}
