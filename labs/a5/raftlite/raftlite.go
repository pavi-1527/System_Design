// Package raftlite is the core of Raft (Ongaro & Ousterhout, 2014) at
// intuition level: leader election with terms and randomized timeouts, and log
// replication with a commit index, as a deterministic state machine driven by
// ticks and messages.
//
// Lab A5 Unit 3 §2–§3 (STRETCH). What you will see for yourself: why a
// cluster needs a majority, why a node votes at most once per term, why a
// candidate with a stale log cannot win, how a follower's conflicting log is
// overwritten, and why a leader only counts replicas to commit entries from
// its OWN term.
//
// What this deliberately leaves out (a real implementation needs all of it):
// persistence and fsync before replying (term, vote and log are assumed durable
// across Restart), membership changes, log compaction and snapshots, client
// sessions and exactly-once command application, read-only queries (ReadIndex
// or leases), pre-vote and leader stickiness, batching and flow control, and a
// faster log-backtracking protocol.
//
// A Node never touches the network, the clock or a random number generator:
// the caller delivers messages with Step, advances time with Tick, and sends
// whatever those return. That makes everything reproducible (see sim.go, a
// provided in-memory network that does exactly that). Node is not safe for
// concurrent use.
//
// Run: go test -race ./labs/a5/raftlite/
//
// Behaviour (tests depend on every line). State: currentTerm, votedFor, log
// (entries are numbered from 1), commitIndex, role, and the known leader.
//
//   - NewNode: Follower, term 0, no vote, empty log, election timer armed with
//     cfg.ElectionTimeout() ticks.
//   - Any message with Term > currentTerm first sets currentTerm = Term,
//     clears votedFor, becomes Follower and forgets the leader. A message with
//     Term < currentTerm is answered with the current term and a refusal
//     (VoteRequest: Granted=false; Append: Success=false) and changes nothing
//     else; stale responses are ignored.
//   - Tick, Follower or Candidate: the election timer counts up; when it
//     reaches its timeout the node starts an election: currentTerm++, role
//     Candidate, votedFor = self, votes = {self}, no known leader, timer re-armed
//     (cfg.ElectionTimeout() is called again), and a MsgVoteRequest carrying
//     LastLogIndex/LastLogTerm goes to every peer. A node with no peers wins at
//     once.
//   - VoteRequest (Term == currentTerm after the rule above): grant iff
//     (votedFor is empty or already the candidate) AND the candidate's log is at
//     least as up to date as ours: its LastLogTerm is higher than ours, or equal
//     with LastLogIndex >= ours. Granting records votedFor and re-arms the
//     election timer; refusing does not touch the timer. The reply is a
//     MsgVoteResponse with Term = currentTerm.
//   - VoteResponse: counted only by a Candidate, only when Term == currentTerm
//     and Granted, once per voter. With a majority (more than half of
//     len(peers)+1) the node becomes Leader: it sets nextIndex[peer] = lastIndex+1
//     and matchIndex[peer] = 0, appends a NO-OP entry (Cmd == "") in the new
//     term, and returns an Append for every peer immediately. The no-op is what
//     lets a new leader commit entries inherited from earlier terms.
//   - Tick, Leader: every cfg.HeartbeatTicks ticks send every peer a MsgAppend
//     built from nextIndex: PrevLogIndex = nextIndex-1, PrevLogTerm its term (0
//     for index 0), all entries from nextIndex on, LeaderCommit = commitIndex.
//   - Append (Term >= currentTerm): a Candidate steps down; the sender becomes
//     the known leader; the election timer is re-armed. If our log has no entry
//     at PrevLogIndex with term PrevLogTerm (index 0 always matches), refuse
//     (Success=false). Otherwise walk the new entries: an existing entry with
//     the same term is kept; at the first entry whose term differs, delete it
//     and everything after it, then append the rest. Entries that already
//     match are never deleted (an old or duplicated Append must not shorten a
//     log). Then commitIndex = max(commitIndex, min(LeaderCommit,
//     PrevLogIndex+len(Entries))). Reply Success=true with MatchIndex =
//     PrevLogIndex+len(Entries).
//   - AppendResponse (Leader, Term == currentTerm): on success raise
//     matchIndex[from] (never lower it), set nextIndex = matchIndex+1, then
//     try to advance commitIndex. On refusal decrement nextIndex[from] (never
//     to or below matchIndex[from], never below 1) and return a fresh Append
//     for that peer at once.
//   - Commit rule: the highest N > commitIndex such that log[N].Term ==
//     currentTerm AND a majority (counting the leader) has matchIndex >= N.
//     Entries from earlier terms are committed only indirectly, when a later
//     entry of the current term commits.
//   - Propose (Leader only): append Entry{currentTerm, cmd}, return its index.
//     It is replicated on the next heartbeat. Anyone else returns (0, false).
//   - Restart simulates a crash and recovery: term, vote, log and commitIndex
//     survive; the role becomes Follower, the leader is forgotten, votes and
//     leader bookkeeping are dropped, and the election timer is re-armed.
//
// Break-it: when breakSkipLogCheck is true (go test -tags breakit), your vote
// handler must treat every candidate's log as up to date. See labs/a5/README.md.
package raftlite

// ID identifies a node.
type ID string

// Role is a node's current role. The zero value is not a valid role.
type Role int

// Roles.
const (
	Follower Role = iota + 1
	Candidate
	Leader
)

func (r Role) String() string {
	switch r {
	case Follower:
		return "follower"
	case Candidate:
		return "candidate"
	case Leader:
		return "leader"
	}
	return "invalid"
}

// Entry is one log entry. Cmd == "" is the leader's no-op.
type Entry struct {
	Term uint64
	Cmd  string
}

// IsNoop reports whether e is a leader no-op.
func (e Entry) IsNoop() bool { return e.Cmd == "" }

// MsgType identifies a Message. The zero value is not a valid type.
type MsgType int

// Message types.
const (
	MsgVoteRequest MsgType = iota + 1
	MsgVoteResponse
	MsgAppend
	MsgAppendResponse
)

// Message is the single wire format; each type uses a subset of the fields.
type Message struct {
	Type     MsgType
	From, To ID
	Term     uint64

	// MsgVoteRequest
	LastLogIndex, LastLogTerm uint64
	// MsgVoteResponse
	Granted bool
	// MsgAppend
	PrevLogIndex, PrevLogTerm uint64
	Entries                   []Entry
	LeaderCommit              uint64
	// MsgAppendResponse
	Success    bool
	MatchIndex uint64
}

// Config configures a Node.
type Config struct {
	ID    ID
	Peers []ID // every other node in the cluster (not including ID)
	// ElectionTimeout returns the next election timeout in ticks (values < 1
	// are treated as 1). It is called every time the timer is armed. Inject a
	// seeded random source for randomized timeouts. nil means a constant 10.
	ElectionTimeout func() int
	// HeartbeatTicks is how often a leader sends Appends (values < 1 mean 1).
	HeartbeatTicks int
}

// Node is one Raft participant.
type Node struct {
	// TODO(lab): everything in the "State" line of the package doc, plus the
	// election/heartbeat counters, the vote set, and nextIndex/matchIndex.
	cfg Config
}

// NewNode returns a follower.
func NewNode(cfg Config) *Node {
	// TODO(lab): copy cfg, default HeartbeatTicks, arm the election timer.
	return &Node{cfg: cfg}
}

// ID returns the node's ID.
func (n *Node) ID() ID { return n.cfg.ID }

// Role returns the current role.
func (n *Node) Role() Role {
	// TODO(lab)
	return 0
}

// Term returns the current term.
func (n *Node) Term() uint64 {
	// TODO(lab)
	return 0
}

// Leader returns the leader this node currently believes in ("" if unknown).
func (n *Node) Leader() ID {
	// TODO(lab)
	return ""
}

// VotedFor returns who this node voted for in the current term ("" if nobody).
func (n *Node) VotedFor() ID {
	// TODO(lab)
	return ""
}

// CommitIndex returns the highest log index known to be committed.
func (n *Node) CommitIndex() uint64 {
	// TODO(lab)
	return 0
}

// Log returns a copy of the log (entry i is at index i+1).
func (n *Node) Log() []Entry {
	// TODO(lab)
	return nil
}

// Restart simulates a crash followed by recovery from durable state.
func (n *Node) Restart() {
	// TODO(lab)
}

// Propose appends cmd to the log if this node is the leader.
func (n *Node) Propose(cmd string) (index uint64, ok bool) {
	// TODO(lab)
	_ = cmd
	return 0, false
}

// Tick advances logical time by one tick and returns messages to send.
func (n *Node) Tick() []Message {
	// TODO(lab): follower/candidate election timer; leader heartbeat timer.
	return nil
}

// Step handles one incoming message and returns messages to send.
func (n *Node) Step(m Message) []Message {
	// TODO(lab): the term rules first, then one case per message type.
	// Build it in this order: votes (tests Vote*), campaigns and winning
	// (Election*), Append handling (Append*), then the leader side
	// (Leader*), then the Cluster* tests will tell you what is still wrong.
	_ = m
	return nil
}
