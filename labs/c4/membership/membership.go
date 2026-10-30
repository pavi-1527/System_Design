// Package membership is how the nodes of a Dynamo-style key-value store learn
// who is in the cluster and who is down, without a coordinator: every node
// gossips a table of heartbeats with a few random peers each round, and each
// node decides for itself, from how long a member's heartbeat has been
// standing still, whether that member is alive, suspect or dead. It is the
// STRETCH lab for case study C4 (Unit 2 §8).
//
// Two pieces:
//
//  1. A simulated Cluster (all in memory, rounds instead of wall-clock time,
//     a seeded random source, so every run is reproducible) with crash,
//     restart and network-partition switches.
//  2. A PhiDetector: the accrual failure detector that turns "how late is
//     this heartbeat, given how often they usually arrive" into a suspicion
//     level, instead of a fixed timeout.
//
// The model (the tests depend on every line):
//
//   - A Heartbeat is (Generation, Version). A node bumps its own Version every
//     round; a restart bumps Generation and resets Version to 0. Newer
//     compares Generation first, then Version.
//   - Every node keeps a table with one Member per name in the cluster. For
//     each member it records the newest Heartbeat it has seen and the round in
//     which that heartbeat last got newer (LastProgress). A node never takes a
//     status from a peer: liveness is each observer's own judgement.
//   - Round r (r = 1, 2, ...): first every running node bumps its Version and
//     sets its own LastProgress to r. Then each running node, in name order,
//     picks min(Fanout, len(candidates)) distinct peers uniformly at random
//     from the members it does not consider Dead (never itself), and, if
//     DeadProbeEvery > 0 and r is a multiple of it, one more peer chosen at
//     random among the members it does consider Dead (if any). Every pick is
//     a message (Messages counts them). An exchange happens only if the peer
//     is running and in the same partition; it is push-pull: each side merges
//     the other's heartbeats (a newer Heartbeat replaces the old one and sets
//     LastProgress to r; entries about the merging node itself are ignored).
//     All exchanges of a round read the tables as they stood after the bumps,
//     as if they happened at the same instant, so news travels one hop per
//     exchange per round.
//     Finally every running node recomputes the status of every other member
//     from age = r - LastProgress: Alive if age < SuspectAfter, Suspect if
//     age < DeadAfter, Dead otherwise. A node is always Alive to itself.
//   - Stop crashes a node: it stops bumping, picking and answering, and its
//     table freezes. Restart brings it back with Generation+1, Version 0 and a
//     fresh table (every member Alive with a zero Heartbeat and LastProgress =
//     the current round), as if it had read its seed list at boot.
//   - Dead to the detector is not removed from the cluster: members are never
//     deleted from a table. Removing a node from the ring is an operator's
//     decision (see the unit).
//
// Everything is safe for concurrent use: Round may run while other goroutines
// call View, Status and Messages.
//
// Provided (identical in the reference solution): the types, the constants,
// Status.String and a suggested shape for the unexported fields of Cluster,
// node and PhiDetector (change those if you prefer). You implement every
// function and method.
//
// Run: go test -race ./labs/c4/membership/
package membership

import (
	"errors"
	"fmt"
	"math/rand"
	"sync"
)

var errNotImplemented = errors.New("membership: not implemented")

// ErrBadConfig is returned by NewCluster and NewPhiDetector for invalid
// arguments. Callers use errors.Is.
var ErrBadConfig = errors.New("membership: bad config")

// Heartbeat is a member's liveness counter.
type Heartbeat struct {
	Generation uint64 // bumped on every restart
	Version    uint64 // bumped every round while running
}

// Newer reports whether h is strictly newer than o: a higher Generation, or
// the same Generation and a higher Version.
func (h Heartbeat) Newer(o Heartbeat) bool {
	// TODO(lab): Generation first, then Version.
	_ = o
	return false
}

// Status is one observer's opinion of a member.
type Status int

// The three statuses, in order of increasing suspicion.
const (
	Alive Status = iota
	Suspect
	Dead
)

// String returns "alive", "suspect" or "dead".
//
// Provided.
func (s Status) String() string {
	switch s {
	case Alive:
		return "alive"
	case Suspect:
		return "suspect"
	case Dead:
		return "dead"
	}
	return fmt.Sprintf("Status(%d)", int(s))
}

// Member is one row of an observer's table.
type Member struct {
	Name         string
	Heartbeat    Heartbeat
	LastProgress int // the round in which Heartbeat last got newer
	Status       Status
}

// Config parameterises a Cluster.
type Config struct {
	Fanout         int   // live peers contacted per node per round; >= 1
	SuspectAfter   int   // rounds without progress before Suspect; >= 1
	DeadAfter      int   // rounds without progress before Dead; > SuspectAfter
	DeadProbeEvery int   // probe one Dead member every this many rounds; 0 = never; >= 0
	Seed           int64 // seeds the peer choice
}

type node struct {
	name    string
	running bool
	hb      Heartbeat
	table   map[string]*Member
}

// Cluster is a simulated gossip cluster.
type Cluster struct {
	mu       sync.Mutex
	cfg      Config
	names    []string // sorted
	nodes    map[string]*node
	group    map[string]int // partition group; all 0 when healed
	round    int
	messages int
	rng      *rand.Rand
}

// NewCluster returns a cluster of running nodes, one per name, each with a
// table that lists every name as Alive with a zero Heartbeat. It returns
// ErrBadConfig (wrapped) for fewer than two names, an empty or duplicate name,
// or a Config that breaks the rules on its fields.
func NewCluster(names []string, cfg Config) (*Cluster, error) {
	// TODO(lab): validate names and cfg; one running node per name, each with a
	// table listing every name (sort the names: rounds visit nodes in name order).
	_, _ = names, cfg
	return nil, errNotImplemented
}

// Round runs one gossip round as described in the package documentation.
func (c *Cluster) Round() {
	// TODO(lab): bump, snapshot the heartbeats, exchange (Fanout live peers + an
	// optional dead probe, each a message), then recompute every status from age.
}

// Stop crashes the named node. Unknown names are ignored.
func (c *Cluster) Stop(name string) {
	// TODO(lab)
	_ = name
}

// Restart brings a stopped node back with a new Generation, Version 0 and a
// fresh table. Restarting a running node or an unknown name does nothing.
func (c *Cluster) Restart(name string) {
	// TODO(lab): Generation+1, Version 0, a fresh table.
	_ = name
}

// Partition splits the network: nodes in different groups cannot exchange.
// Names not listed in any group form one more group together.
func (c *Cluster) Partition(groups ...[]string) {
	// TODO(lab)
	_ = groups
}

// Heal removes every partition.
func (c *Cluster) Heal() {
	// TODO(lab)
}

// View returns a copy of the observer's table, or nil for an unknown name.
// A stopped node's view is its table as it was when it stopped.
func (c *Cluster) View(observer string) map[string]Member {
	// TODO(lab): return copies, not pointers into the table.
	_ = observer
	return nil
}

// Status returns the observer's opinion of member, and false if either name
// is unknown.
func (c *Cluster) Status(observer, member string) (Status, bool) {
	// TODO(lab)
	_, _ = observer, member
	return 0, false
}

// Rounds returns the number of rounds run so far.
func (c *Cluster) Rounds() int {
	// TODO(lab)
	return 0
}

// Messages returns the number of peers picked so far (exchanges attempted,
// including those to stopped or partitioned peers).
func (c *Cluster) Messages() int {
	// TODO(lab)
	return 0
}

// PhiDetector is an accrual failure detector for one peer. It keeps the last
// Window inter-arrival times of the peer's heartbeats and models them as
// exponentially distributed with the observed mean, so the probability that a
// heartbeat is still to come after a silence of t is exp(-t/mean) and
//
//	phi(t) = -log10(exp(-t/mean)) = t / (mean · ln 10)
//
// phi = 1 means "a heartbeat this late happens about 1 time in 10", phi = 8
// about 1 time in 10^8. A caller convicts the peer when phi crosses a
// threshold; because mean adapts per peer, the same threshold means a short
// timeout for a peer that chats every 100 ms and a long one for a peer that
// heartbeats every 2 s. Safe for concurrent use.
type PhiDetector struct {
	mu        sync.Mutex
	window    int
	intervals []float64 // ring buffer of the last window intervals, seconds
	next      int
	sum       float64
	last      float64
	seen      bool
}

// NewPhiDetector returns a detector that averages the last window intervals.
// It returns ErrBadConfig (wrapped) for window < 1.
func NewPhiDetector(window int) (*PhiDetector, error) {
	// TODO(lab)
	_ = window
	return nil, errNotImplemented
}

// Heartbeat records a heartbeat that arrived at time at (seconds, any epoch).
// The first call only records the time; each later call records the interval
// since the previous one. Arrivals that are not later than the previous one
// are ignored.
func (d *PhiDetector) Heartbeat(at float64) {
	// TODO(lab): a ring buffer of the last window intervals and their running sum.
	_ = at
}

// Mean returns the mean of the recorded intervals (0 before the second
// heartbeat).
func (d *PhiDetector) Mean() float64 {
	// TODO(lab)
	return 0
}

// Phi returns the suspicion level at time now: 0 before the second heartbeat
// and for now not later than the last heartbeat, otherwise
// (now - last) / (mean · ln 10).
func (d *PhiDetector) Phi(now float64) float64 {
	// TODO(lab): (now - last) / (mean * math.Ln10).
	_ = now
	return 0
}
