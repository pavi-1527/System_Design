package quorum

// This file is PROVIDED (identical in the lab and the solution). It is the
// fault injector: tests flip these switches per replica, and your coordinator
// reads them through Sim.state. There are no goroutines, sleeps or random
// numbers here: the "network" is a table of per-replica behaviours, so every
// test is deterministic.

import (
	"sync"
	"time"
)

// NodeState is how one replica currently behaves.
type NodeState struct {
	// Down: the replica is unreachable. Requests to it fail immediately and
	// have no effect.
	Down bool
	// Lag: the replica is reachable but slow to apply writes (asynchronous
	// replication, a long GC pause). A write addressed to it is queued, is NOT
	// applied now, and does not count as an acknowledgement. Cluster.Flush
	// applies the queue later. Reads are unaffected: they see whatever the
	// replica holds right now.
	Lag bool
	// AckLoss: the write is applied, but the acknowledgement never reaches
	// the coordinator (a timeout from the caller's point of view).
	AckLoss bool
	// Latency is virtual: it only decides which replicas answer a read first
	// (smallest first; ties keep preference-list order). Nothing sleeps.
	Latency time.Duration
}

// Sim holds the behaviour of every replica. Safe for concurrent use.
type Sim struct {
	mu sync.Mutex
	st map[string]NodeState
}

// NewSim returns a Sim in which every replica is healthy with zero latency.
func NewSim() *Sim { return &Sim{st: map[string]NodeState{}} }

func (s *Sim) update(id string, f func(*NodeState)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.st[id]
	f(&st)
	s.st[id] = st
}

// SetDown marks id unreachable (or reachable again).
func (s *Sim) SetDown(id string, down bool) { s.update(id, func(n *NodeState) { n.Down = down }) }

// SetLag makes id queue incoming writes instead of applying them.
func (s *Sim) SetLag(id string, lag bool) { s.update(id, func(n *NodeState) { n.Lag = lag }) }

// SetAckLoss makes id apply writes but lose the acknowledgements.
func (s *Sim) SetAckLoss(id string, loss bool) { s.update(id, func(n *NodeState) { n.AckLoss = loss }) }

// SetLatency sets id's virtual read latency.
func (s *Sim) SetLatency(id string, d time.Duration) {
	s.update(id, func(n *NodeState) { n.Latency = d })
}

// Heal restores every replica to the healthy state (queued writes and hints
// are not touched).
func (s *Sim) Heal() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.st = map[string]NodeState{}
}

// state returns id's current behaviour.
func (s *Sim) state(id string) NodeState {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.st[id]
}
