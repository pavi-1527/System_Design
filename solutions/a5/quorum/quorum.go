// Package quorum is a Dynamo-style replicated key-value store reduced to its
// essentials: N replicas per key, a write that needs W acknowledgements, a read
// that consults R replicas, versioned values with siblings, read repair, and
// (optionally) sloppy quorums with hinted handoff.
//
// Lab A5 Unit 1 §3–§5. The point of the lab is to make the guarantees (and the
// non-guarantees) of "R + W > N" observable: when it holds, when it silently
// doesn't, and why even a correct quorum is not linearizable.
//
// Provided in this package: versions.go (VClock, Version, Reconcile, the
// per-replica Node and its hint list) and sim.go (the fault injector).
// You implement the coordinator in this file. Run:
//
//	go test -race ./labs/a5/quorum/
//
// The model (tests depend on every line):
//
//   - The cluster has a fixed, ordered list of node IDs (the ring). A key's
//     preference list is the N consecutive nodes starting at ring position
//     Place(key) mod len(ids), wrapping around. Its fallbacks are the
//     remaining nodes in ring order, starting right after the preference list.
//     Config.Place defaults to the FNV-1a (32-bit) hash of the key.
//   - The caller acts as the coordinator. Put fans out to the whole preference
//     list; Get consults replicas of the preference list only.
//   - Put(client, key, value, ctx): the new version's clock is
//     ctx.Tick(client). For each replica of the preference list, in order:
//   - Down: nothing happens. In Sloppy mode, the write instead goes to the
//     first fallback that is not Down and has not already been used as a
//     stand-in for this Put: the stand-in stores a Hint{Target: the down
//     replica} (not an Apply) and its acknowledgement counts.
//   - Lag: the write is queued for that replica (applied later by Flush);
//     it is not applied now and does not count as an acknowledgement.
//   - otherwise the write is applied to the replica now; it counts as an
//     acknowledgement unless the replica has AckLoss set.
//     If acknowledgements >= W, Put returns the new clock (the caller's context
//     for its next Put). Otherwise it returns ErrQuorumNotMet (wrapped) and
//     nil, but anything already applied or queued STAYS: there is no rollback.
//   - Get(key): the candidates are the preference-list replicas that are not
//     Down, stably sorted by Latency. If fewer than R, ErrQuorumNotMet.
//     The R fastest are the responders; the answer is Reconcile of all their
//     versions. With ReadRepair, every responder whose own versions differ from
//     the answer is brought up to date by applying every answer version to it
//     (non-responders are not touched). The result's Context is the Merge of
//     the answer versions' clocks (nil if none). A key nobody has is not an
//     error: Versions is empty.
//   - Flush applies queued writes to their target replicas, skipping (and
//     keeping queued) those whose replica is Down, and returns how many it
//     applied. DeliverHints moves hints from stand-ins to their targets when
//     the target is not Down, and returns how many it delivered.
//
// Everything must be safe for concurrent use (the tests hammer it under -race).
package quorum

import (
	"errors"
	"fmt"
	"hash/fnv"
	"sort"
	"sync"
)

// ErrQuorumNotMet is returned by Put and Get when fewer than W / R replicas
// took part. Wrap it with detail; callers use errors.Is.
var ErrQuorumNotMet = errors.New("quorum: not enough replicas")

// ErrConfig is returned by NewCluster for an invalid configuration.
var ErrConfig = errors.New("quorum: invalid configuration")

// Config selects the quorum parameters and behaviours.
type Config struct {
	N, R, W    int
	ReadRepair bool // repair stale responders on Get
	Sloppy     bool // accept writes on stand-ins when home replicas are Down
	// Place returns the ring position where key's preference list starts. nil
	// means FNV-1a(key). Tests inject a constant to control placement.
	Place func(key string) int
}

type queued struct {
	target, key string
	v           Version
}

// Cluster coordinates reads and writes over a set of replicas.
type Cluster struct {
	cfg   Config
	ids   []string
	nodes map[string]*Node
	sim   *Sim

	mu      sync.Mutex
	pending []queued // writes held back for Lag replicas
}

// NewCluster validates cfg and builds the cluster. Requirements: 1 <= R <= N,
// 1 <= W <= N, N <= len(ids), ids non-empty strings with no duplicates; a nil
// sim means a fresh healthy Sim. Violations return an error wrapping ErrConfig.
func NewCluster(cfg Config, ids []string, sim *Sim) (*Cluster, error) {
	if cfg.N < 1 || cfg.R < 1 || cfg.W < 1 || cfg.R > cfg.N || cfg.W > cfg.N {
		return nil, fmt.Errorf("%w: need 1 <= R,W <= N, got N=%d R=%d W=%d", ErrConfig, cfg.N, cfg.R, cfg.W)
	}
	if cfg.N > len(ids) {
		return nil, fmt.Errorf("%w: N=%d but only %d nodes", ErrConfig, cfg.N, len(ids))
	}
	nodes := make(map[string]*Node, len(ids))
	for _, id := range ids {
		if id == "" || nodes[id] != nil {
			return nil, fmt.Errorf("%w: empty or duplicate node id %q", ErrConfig, id)
		}
		nodes[id] = NewNode(id)
	}
	if sim == nil {
		sim = NewSim()
	}
	return &Cluster{cfg: cfg, ids: append([]string(nil), ids...), nodes: nodes, sim: sim}, nil
}

// Node returns the replica with the given ID (nil if unknown). Tests use it to
// look inside a replica.
func (c *Cluster) Node(id string) *Node { return c.nodes[id] }

func (c *Cluster) start(key string) int {
	var p int
	if c.cfg.Place != nil {
		p = c.cfg.Place(key)
	} else {
		h := fnv.New32a()
		h.Write([]byte(key))
		p = int(h.Sum32() % uint32(len(c.ids)))
	}
	p %= len(c.ids)
	if p < 0 {
		p += len(c.ids)
	}
	return p
}

// PreferenceList returns the N replicas responsible for key, in ring order.
func (c *Cluster) PreferenceList(key string) []string {
	s := c.start(key)
	out := make([]string, c.cfg.N)
	for i := range out {
		out[i] = c.ids[(s+i)%len(c.ids)]
	}
	return out
}

// Fallbacks returns the nodes outside key's preference list, in ring order
// starting right after it. Sloppy quorums borrow stand-ins from here.
func (c *Cluster) Fallbacks(key string) []string {
	s := c.start(key)
	var out []string
	for i := c.cfg.N; i < len(c.ids); i++ {
		out = append(out, c.ids[(s+i)%len(c.ids)])
	}
	return out
}

// Put writes value for key on behalf of client, against context ctx (nil for a
// blind write). See the package doc for the exact fan-out rules.
func (c *Cluster) Put(client, key, value string, ctx VClock) (VClock, error) {
	clock := ctx.Tick(client)
	v := Version{Clock: clock, Value: value}
	acks := 0
	usedFallback := map[string]bool{}
	fallbacks := c.Fallbacks(key)
	for _, home := range c.PreferenceList(key) {
		st := c.sim.state(home)
		switch {
		case st.Down:
			if !c.cfg.Sloppy {
				continue
			}
			for _, fb := range fallbacks {
				if usedFallback[fb] || c.sim.state(fb).Down {
					continue
				}
				usedFallback[fb] = true
				c.nodes[fb].AddHint(Hint{Target: home, Key: key, Version: v})
				acks++
				break
			}
		case st.Lag:
			c.mu.Lock()
			c.pending = append(c.pending, queued{target: home, key: key, v: v.clone()})
			c.mu.Unlock()
		default:
			c.nodes[home].Apply(key, v)
			if !st.AckLoss {
				acks++
			}
		}
	}
	if acks < c.cfg.W {
		return nil, fmt.Errorf("%w: write needed %d acknowledgements, got %d (applied replicas keep the value)", ErrQuorumNotMet, c.cfg.W, acks)
	}
	return clock.Clone(), nil
}

// Result is what Get returns.
type Result struct {
	Versions []Version // siblings; empty if the key is unknown to the responders
	Context  VClock    // pass to the next Put to supersede everything seen here
}

// Values returns the sibling values in Versions order.
func (r Result) Values() []string {
	out := make([]string, len(r.Versions))
	for i, v := range r.Versions {
		out[i] = v.Value
	}
	return out
}

// Get reads key from R replicas. See the package doc for the exact rules.
func (c *Cluster) Get(key string) (Result, error) {
	type cand struct {
		id  string
		lat int64
	}
	var cands []cand
	for _, id := range c.PreferenceList(key) {
		st := c.sim.state(id)
		if !st.Down {
			cands = append(cands, cand{id, int64(st.Latency)})
		}
	}
	if len(cands) < c.cfg.R {
		return Result{}, fmt.Errorf("%w: read needed %d replicas, %d reachable", ErrQuorumNotMet, c.cfg.R, len(cands))
	}
	sort.SliceStable(cands, func(i, j int) bool { return cands[i].lat < cands[j].lat })
	cands = cands[:c.cfg.R]

	var all []Version
	local := make(map[string][]Version, len(cands))
	for _, cd := range cands {
		vs := c.nodes[cd.id].Versions(key)
		local[cd.id] = vs
		all = append(all, vs...)
	}
	merged := Reconcile(all)
	if c.cfg.ReadRepair {
		for _, cd := range cands {
			if sameVersions(Reconcile(local[cd.id]), merged) {
				continue
			}
			for _, v := range merged {
				c.nodes[cd.id].Apply(key, v)
			}
		}
	}
	var ctx VClock
	for _, v := range merged {
		ctx = ctx.Merge(v.Clock)
	}
	return Result{Versions: merged, Context: ctx}, nil
}

// Flush delivers queued writes (see NodeState.Lag) to replicas that are not
// Down and returns how many were applied.
func (c *Cluster) Flush() int {
	c.mu.Lock()
	pending := c.pending
	c.pending = nil
	c.mu.Unlock()
	applied := 0
	var keep []queued
	for _, q := range pending {
		if c.sim.state(q.target).Down {
			keep = append(keep, q)
			continue
		}
		c.nodes[q.target].Apply(q.key, q.v)
		applied++
	}
	if len(keep) > 0 {
		c.mu.Lock()
		c.pending = append(keep, c.pending...)
		c.mu.Unlock()
	}
	return applied
}

// DeliverHints hands hints to their target replicas once those are not Down,
// and returns how many were delivered. Hints whose target is still Down stay
// where they are.
func (c *Cluster) DeliverHints() int {
	delivered := 0
	ready := func(target string) bool { return !c.sim.state(target).Down }
	for _, id := range c.ids {
		for _, h := range c.nodes[id].TakeHints(ready) {
			c.nodes[h.Target].Apply(h.Key, h.Version)
			delivered++
		}
	}
	return delivered
}
