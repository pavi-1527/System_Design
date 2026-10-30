// Package replkv is a single-leader replicated key-value store running on a
// deterministic, tick-driven network simulator: a leader with a replication
// log, followers that apply entries in order, injectable lag and partitions,
// client sessions that provide read-your-writes and monotonic reads with LSN
// tokens, and failover that promotes the most up-to-date follower and reports
// the writes it lost.
//
// It is the lab for A4 Unit 1 (§1–§3). There are no sleeps and no timers:
// replication lag is a number of ticks, and nothing moves until a test calls
// Tick (or Flush, or a synchronous Put waits). That makes every anomaly in
// the unit reproducible on demand.
//
// Model. Nodes are "n0" (the initial leader), "n1", "n2", ... The leader
// assigns consecutive LSNs (log sequence numbers) starting at 1, appends the
// entry to its own log, applies it and sends it to every other node through
// the Network. A node's log always holds LSNs 1..applied with no gaps.
//
// What the tests check:
//
//   - Follower apply rule (node.apply): entries are applied strictly in LSN
//     order. An entry from the future is buffered, a duplicate or old entry
//     is ignored, and a buffered entry is applied as soon as the gap fills.
//   - Put/Delete append to the leader's log with the next LSN and replicate;
//     followers converge once the network drains. Put on a node that is not
//     the leader fails with ErrNotLeader; on a crashed leader with ErrNoLeader.
//   - Synchronous replication: with Options.SyncAcks = k, Put does not return
//     until k followers have applied the entry. It advances simulated time
//     itself (calling Tick) and reports how many ticks it waited. If k acks do
//     not arrive within SyncTimeoutTicks it returns ErrSyncTimeout: the write
//     is in the leader's log (and will replicate) but was not acknowledged.
//   - Reads: Get reads one node's applied state and reports that node's
//     applied LSN; GetAtLeast refuses (ErrStaleReplica) a node that has not
//     applied minLSN yet.
//   - Sessions: without guarantees a client reading from a lagging follower
//     sees old data, and can see data go backwards between two reads.
//     ReadYourWrites makes every read reflect the session's own writes;
//     MonotonicReads makes the data a session sees never move backwards. They
//     are independent. Both are implemented with LSN floors: try followers
//     (starting from the one Pick chose) whose applied LSN meets the floor,
//     else the leader. Token() lets a different session continue.
//   - Failover: promotes the live follower with the highest applied LSN (ties
//     go to the earliest node), discards everything in flight and any
//     buffered out-of-order entries, fences the old leader (marks it down),
//     makes the surviving followers catch up from the new leader, and returns
//     the entries that were in the old leader's log but not on the new one
//     (lost writes). Recover brings a node back: it truncates its log to the
//     prefix it shares with the leader, discards its divergent suffix, and
//     catches up.
//   - The leader is safe for concurrent use (run with -race).
//
// Suggested order: node.apply, Put/Delete, Tick, Get/GetAtLeast, Session,
// Crash/Failover/Recover.
// Run: go test -race ./labs/a4/replkv/
package replkv

import (
	"errors"
	"strconv"
	"sync"
)

var errNotImplemented = errors.New("replkv: not implemented")

// Errors returned by the cluster and sessions.
var (
	ErrNotLeader      = errors.New("replkv: node is not the leader")
	ErrNoLeader       = errors.New("replkv: the leader is down")
	ErrNodeDown       = errors.New("replkv: node is down")
	ErrUnknownNode    = errors.New("replkv: unknown node")
	ErrSyncTimeout    = errors.New("replkv: not enough followers acknowledged in time")
	ErrStaleReplica   = errors.New("replkv: replica has not applied the requested LSN")
	ErrNoFreshReplica = errors.New("replkv: no replica satisfies the session's guarantees")
	ErrNoCandidate    = errors.New("replkv: no live follower to promote")
)

// NodeID names a node: "n0", "n1", ...
type NodeID string

// Entry is one record of the replication log.
type Entry struct {
	LSN    uint64
	Key    string
	Value  string
	Delete bool
}

// Options configures a Cluster. Zero values get defaults.
type Options struct {
	// Followers is the number of followers. Default 2 (so 3 nodes).
	Followers int
	// SyncAcks is how many followers must have applied an entry before Put
	// returns. 0 (default) is asynchronous replication.
	SyncAcks int
	// SyncTimeoutTicks bounds how long a synchronous Put waits. Default 10.
	SyncTimeoutTicks int
}

// PutResult describes an acknowledged (or timed-out) write.
type PutResult struct {
	LSN   uint64 // LSN the leader assigned
	Ticks int    // simulated ticks the call waited for acknowledgements
}

// ReadResult is what a read returns.
type ReadResult struct {
	Node       NodeID
	Value      string
	Found      bool
	AppliedLSN uint64 // the node's applied LSN at the time of the read
}

// FailoverReport describes a completed failover.
type FailoverReport struct {
	OldLeader    NodeID
	NewLeader    NodeID
	NewLeaderLSN uint64  // applied LSN of the new leader at promotion
	LostWrites   []Entry // entries in the old leader's log beyond NewLeaderLSN, in LSN order
}

// node is one replica: its log, the state machine built from it, and any
// out-of-order entries waiting for a gap to fill.
type node struct {
	id      NodeID
	log     []Entry // log[i].LSN == i+1
	data    map[string]string
	pending map[uint64]Entry
	down    bool
}

func newNode(id NodeID) *node {
	return &node{id: id, data: map[string]string{}, pending: map[uint64]Entry{}}
}

// applied returns the LSN of the last applied entry (0 for an empty log).
func (n *node) applied() uint64 { return uint64(len(n.log)) }

// applyOne appends e (which must be the next LSN) to the log and the state.
func (n *node) applyOne(e Entry) {
	n.log = append(n.log, e)
	if e.Delete {
		delete(n.data, e.Key)
	} else {
		n.data[e.Key] = e.Value
	}
}

// apply is the follower rule. e.LSN <= applied: duplicate, ignore.
// e.LSN == applied+1: apply it, then keep applying buffered successors.
// e.LSN > applied+1: buffer it until the gap is filled.
func (n *node) apply(e Entry) {
	// TODO(lab): the follower rule from the doc comment above.
}

// truncate cuts the log back to its first keep entries and rebuilds the state
// from what is left. Provided.
func (n *node) truncate(keep int) {
	if keep >= len(n.log) {
		return
	}
	n.log = n.log[:keep:keep]
	n.data = map[string]string{}
	for _, e := range n.log {
		if e.Delete {
			delete(n.data, e.Key)
		} else {
			n.data[e.Key] = e.Value
		}
	}
	n.pending = map[uint64]Entry{}
}

// commonPrefix returns the length of the longest common prefix of two logs.
// Provided.
func commonPrefix(a, b []Entry) int {
	i := 0
	for i < len(a) && i < len(b) && a[i] == b[i] {
		i++
	}
	return i
}

// Cluster is one leader and its followers on a simulated network.
type Cluster struct {
	mu     sync.Mutex
	opts   Options
	net    *Network
	nodes  map[NodeID]*node
	order  []NodeID
	leader NodeID
}

// NewCluster creates n0 (the leader) and opts.Followers followers.
// Provided.
func NewCluster(opts Options) *Cluster {
	if opts.Followers <= 0 {
		opts.Followers = 2
	}
	if opts.SyncTimeoutTicks <= 0 {
		opts.SyncTimeoutTicks = 10
	}
	c := &Cluster{opts: opts, net: NewNetwork(), nodes: map[NodeID]*node{}}
	for i := 0; i <= opts.Followers; i++ {
		id := NodeID("n" + strconv.Itoa(i))
		c.nodes[id] = newNode(id)
		c.order = append(c.order, id)
	}
	c.leader = c.order[0]
	return c
}

// Network returns the simulator, for SetDelay and Partition. Provided.
func (c *Cluster) Network() *Network { return c.net }

// Leader returns the current leader. Provided.
func (c *Cluster) Leader() NodeID {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.leader
}

// NodeIDs returns all node ids in creation order. Provided.
func (c *Cluster) NodeIDs() []NodeID { return append([]NodeID(nil), c.order...) }

// Followers returns the live non-leader nodes in creation order. Provided.
func (c *Cluster) Followers() []NodeID {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.followersLocked()
}

func (c *Cluster) followersLocked() []NodeID {
	var out []NodeID
	for _, id := range c.order {
		if id != c.leader && !c.nodes[id].down {
			out = append(out, id)
		}
	}
	return out
}

// Applied returns the applied LSN of a node (0 for an unknown node). Provided.
func (c *Cluster) Applied(id NodeID) uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	if n := c.nodes[id]; n != nil {
		return n.applied()
	}
	return 0
}

// Log returns a copy of a node's log. Provided.
func (c *Cluster) Log(id NodeID) []Entry {
	c.mu.Lock()
	defer c.mu.Unlock()
	if n := c.nodes[id]; n != nil {
		return append([]Entry(nil), n.log...)
	}
	return nil
}

// Snapshot returns a copy of a node's key-value state. Provided.
func (c *Cluster) Snapshot(id NodeID) map[string]string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := map[string]string{}
	if n := c.nodes[id]; n != nil {
		for k, v := range n.data {
			out[k] = v
		}
	}
	return out
}

// Flush ticks until nothing is deliverable (everything in flight has been
// delivered, apart from messages held behind a cut link) and returns the
// number of ticks it took. Provided.
func (c *Cluster) Flush() int {
	ticks := 0
	for !c.net.Idle() && ticks < 100000 {
		c.Tick()
		ticks++
	}
	return ticks
}

// Put writes key=value through the current leader. Provided.
func (c *Cluster) Put(key, value string) (PutResult, error) {
	return c.PutVia(c.Leader(), key, value)
}

// Delete removes key through the current leader. Provided.
func (c *Cluster) Delete(key string) (PutResult, error) {
	return c.write(c.Leader(), Entry{Key: key, Delete: true})
}

// PutVia sends a write to a specific node, as a client that believes that
// node is the leader would. Provided.
func (c *Cluster) PutVia(via NodeID, key, value string) (PutResult, error) {
	return c.write(via, Entry{Key: key, Value: value})
}

// write is the leader's write path.
//
// Order of checks: unknown node → ErrUnknownNode; via is not the leader →
// ErrNotLeader; the leader is down → ErrNoLeader. Then: assign
// LSN = leader.applied()+1, append and apply it on the leader, Send it to
// every other node (including crashed ones: the network drops it on
// delivery). With SyncAcks > 0, wait: before the first tick and after each
// tick, count live followers whose applied LSN >= LSN; return as soon as the
// count reaches SyncAcks; after SyncTimeoutTicks ticks without success return
// the PutResult (LSN, Ticks) together with ErrSyncTimeout. Ticks is the number
// of ticks waited (0 for async).
func (c *Cluster) write(via NodeID, e Entry) (PutResult, error) {
	// TODO(lab): checks, assign the LSN, append+apply on the leader, Send to
	// every other node, then (if SyncAcks > 0) wait for acknowledgements by
	// ticking. Hold c.mu throughout and use tickLocked, not Tick.
	return PutResult{}, errNotImplemented
}

// Tick advances simulated time by one tick and delivers every due message to
// its node. Deliveries to crashed nodes are discarded. Safe for concurrent use.
func (c *Cluster) Tick() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.tickLocked()
}

func (c *Cluster) tickLocked() {
	// TODO(lab): net.Tick() returns the due deliveries; apply each to its node
	// unless the node is down (or unknown).
}

// Get reads key from one node's applied state.
func (c *Cluster) Get(id NodeID, key string) (ReadResult, error) {
	return c.GetAtLeast(id, key, 0)
}

// GetAtLeast reads key from node id, but only if that node has applied at
// least minLSN; otherwise it returns ErrStaleReplica. The check and the read
// are one atomic step. ErrUnknownNode / ErrNodeDown as appropriate.
func (c *Cluster) GetAtLeast(id NodeID, key string, minLSN uint64) (ReadResult, error) {
	// TODO(lab): under c.mu: unknown node, node down, stale replica, then read.
	return ReadResult{}, errNotImplemented
}

// Crash stops a node: it serves nothing, its volatile out-of-order buffer is
// lost, and messages that reach it are discarded. Its log survives (it is
// durable). Crashing the leader makes writes fail with ErrNoLeader until
// Failover or Recover.
func (c *Cluster) Crash(id NodeID) error {
	// TODO(lab): mark the node down and drop its volatile reorder buffer.
	return errNotImplemented
}

// Recover restarts a crashed node. If it is the current leader it simply
// comes back. Otherwise it rejoins as a follower: truncate its log to the
// prefix it shares with the leader's log (a deposed leader's unreplicated
// writes are discarded here), then catch up from the leader.
func (c *Cluster) Recover(id NodeID) error {
	// TODO(lab): come back up; if not the leader, truncate to the common
	// prefix with the leader's log (truncate and commonPrefix are provided)
	// and catch up.
	return errNotImplemented
}

// catchUpLocked sends node id every entry it lacks from the leader's log.
func (c *Cluster) catchUpLocked(id NodeID) {
	// TODO(lab): Send id every entry of the leader's log beyond id's applied LSN.
}

// Failover replaces the leader (normally after Crash(leader)).
//
// It promotes the live follower with the highest applied LSN (ties: earliest
// in node order); ErrNoCandidate if there is none. It then drops everything
// in flight (the old leader's connections are gone), discards every
// follower's buffered out-of-order entries, fences the old leader by marking
// it down, and has every other live follower catch up from the new leader's
// log. The report lists the old leader's log entries beyond the new leader's
// applied LSN: writes that were acknowledged to clients (if the client was
// told "ok") but exist nowhere else.
func (c *Cluster) Failover() (FailoverReport, error) {
	// TODO(lab): choose the candidate, build the report, drop in-flight
	// messages and every node's pending buffer, fence the old leader, switch
	// the leader, and catch up the other live followers.
	return FailoverReport{}, errNotImplemented
}

// SessionOptions selects a session's guarantees.
type SessionOptions struct {
	// ReadYourWrites: a read reflects every write this session has made.
	ReadYourWrites bool
	// MonotonicReads: a read never reflects an older state than an earlier
	// read of this session did.
	MonotonicReads bool
	// Pick chooses which follower to try first among the live followers (in
	// node order). Default: round robin. Tests inject a script.
	Pick func(followers []NodeID) NodeID
	// Token continues another session's guarantees: it is treated as a write
	// the session has already made (see Session.Token).
	Token uint64
}

// Session is one client's view of the cluster. Safe for concurrent use.
type Session struct {
	c    *Cluster
	opts SessionOptions

	mu         sync.Mutex
	writeFloor uint64 // highest LSN this session wrote (or inherited via Token)
	readFloor  uint64 // highest applied LSN of any node this session read from
	rr         int
}

// NewSession starts a session. Provided.
func (c *Cluster) NewSession(opts SessionOptions) *Session {
	return &Session{c: c, opts: opts, writeFloor: opts.Token}
}

// Put writes through the cluster's leader and remembers the LSN. A write that
// returns ErrSyncTimeout still has a valid LSN and counts as written.
func (s *Session) Put(key, value string) (PutResult, error) {
	// TODO(lab): write through the cluster; remember the LSN as the write
	// floor (also when err is ErrSyncTimeout, as long as an LSN was assigned).
	return s.c.Put(key, value)
}

// Token returns the highest LSN this session has written or observed,
// regardless of which guarantees it enforces. Hand it to another session
// (another device, another app server) through SessionOptions.Token.
func (s *Session) Token() uint64 {
	// TODO(lab)
	return 0
}

// Get reads key honouring the session's guarantees.
//
// floor = writeFloor if ReadYourWrites, combined (max) with readFloor if
// MonotonicReads; 0 means no constraint. Candidates are the live followers.
// The first choice is opts.Pick(followers) (default round robin: advance an
// internal counter per call); try it and then the following followers in
// node order (wrapping), taking the first for which GetAtLeast(floor)
// succeeds; if none does, read from the leader. If the leader is also
// unusable return ErrNoFreshReplica. With no live followers read from the
// leader. After a successful read, raise readFloor to the node's AppliedLSN.
func (s *Session) Get(key string) (ReadResult, error) {
	// TODO(lab): compute the floor, choose the first follower (Pick or round
	// robin), try followers in order with GetAtLeast, fall back to the leader,
	// then raise readFloor. Do not call opts.Pick while holding s.mu.
	return ReadResult{}, errNotImplemented
}
