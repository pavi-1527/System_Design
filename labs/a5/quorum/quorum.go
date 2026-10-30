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
	"sync"
)

var errNotImplemented = errors.New("quorum: not implemented")

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

// Cluster coordinates reads and writes over a set of replicas.
type Cluster struct {
	// TODO(lab): the config, the ordered ids, a map id -> *Node (use
	// NewNode), the *Sim, and a mutex-protected queue of writes held back for
	// Lag replicas (target id, key, Version).
	mu sync.Mutex
}

// NewCluster validates cfg and builds the cluster. Requirements: 1 <= R <= N,
// 1 <= W <= N, N <= len(ids), ids non-empty strings with no duplicates; a nil
// sim means a fresh healthy Sim. Violations return an error wrapping ErrConfig.
func NewCluster(cfg Config, ids []string, sim *Sim) (*Cluster, error) {
	// TODO(lab)
	_, _, _ = cfg, ids, sim
	return nil, errNotImplemented
}

// Node returns the replica with the given ID (nil if unknown). Tests use it to
// look inside a replica.
func (c *Cluster) Node(id string) *Node {
	// TODO(lab)
	_ = id
	return nil
}

// PreferenceList returns the N replicas responsible for key, in ring order.
func (c *Cluster) PreferenceList(key string) []string {
	// TODO(lab): hash/fnv New32a unless cfg.Place is set; take the position
	// modulo len(ids) and fix up negatives.
	_ = key
	return nil
}

// Fallbacks returns the nodes outside key's preference list, in ring order
// starting right after it. Sloppy quorums borrow stand-ins from here.
func (c *Cluster) Fallbacks(key string) []string {
	// TODO(lab)
	_ = key
	return nil
}

// Put writes value for key on behalf of client, against context ctx (nil for a
// blind write). See the package doc for the exact fan-out rules.
func (c *Cluster) Put(client, key, value string, ctx VClock) (VClock, error) {
	// TODO(lab): clock := ctx.Tick(client); walk the preference list; count
	// acknowledgements; compare with W. Remember: no rollback on failure.
	_, _, _, _ = client, key, value, ctx
	return nil, errNotImplemented
}

// Result is what Get returns.
type Result struct {
	Versions []Version // siblings; empty if the key is unknown to the responders
	Context  VClock    // pass to the next Put to supersede everything seen here
}

// Values returns the sibling values in Versions order. (Provided.)
func (r Result) Values() []string {
	out := make([]string, len(r.Versions))
	for i, v := range r.Versions {
		out[i] = v.Value
	}
	return out
}

// Get reads key from R replicas. See the package doc for the exact rules.
func (c *Cluster) Get(key string) (Result, error) {
	// TODO(lab): candidates (not Down), sort.SliceStable by Latency, take R,
	// Reconcile their versions, optionally read-repair the responders.
	_ = key
	return Result{}, errNotImplemented
}

// Flush delivers queued writes (see NodeState.Lag) to replicas that are not
// Down and returns how many were applied.
func (c *Cluster) Flush() int {
	// TODO(lab)
	return 0
}

// DeliverHints hands hints to their target replicas once those are not Down,
// and returns how many were delivered. Hints whose target is still Down stay
// where they are.
func (c *Cluster) DeliverHints() int {
	// TODO(lab): Node.TakeHints with a "target is up" predicate.
	return 0
}
