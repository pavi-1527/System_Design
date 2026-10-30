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
	cfg   Config
	peers []ID

	term     uint64
	votedFor ID
	log      []Entry
	commit   uint64

	role   Role
	leader ID

	electionElapsed  int
	electionTimeout  int
	heartbeatElapsed int

	votes map[ID]bool
	next  map[ID]uint64
	match map[ID]uint64
}

// NewNode returns a follower.
func NewNode(cfg Config) *Node {
	if cfg.HeartbeatTicks < 1 {
		cfg.HeartbeatTicks = 1
	}
	n := &Node{cfg: cfg, peers: append([]ID(nil), cfg.Peers...), role: Follower}
	n.armElectionTimer()
	return n
}

// ID returns the node's ID.
func (n *Node) ID() ID { return n.cfg.ID }

// Role returns the current role.
func (n *Node) Role() Role { return n.role }

// Term returns the current term.
func (n *Node) Term() uint64 { return n.term }

// Leader returns the leader this node currently believes in ("" if unknown).
func (n *Node) Leader() ID { return n.leader }

// VotedFor returns who this node voted for in the current term ("" if nobody).
func (n *Node) VotedFor() ID { return n.votedFor }

// CommitIndex returns the highest log index known to be committed.
func (n *Node) CommitIndex() uint64 { return n.commit }

// Log returns a copy of the log (entry i is at index i+1).
func (n *Node) Log() []Entry { return append([]Entry(nil), n.log...) }

func (n *Node) lastIndex() uint64 { return uint64(len(n.log)) }

func (n *Node) termAt(i uint64) uint64 {
	if i == 0 || i > n.lastIndex() {
		return 0
	}
	return n.log[i-1].Term
}

func (n *Node) majority() int { return (len(n.peers)+1)/2 + 1 }

func (n *Node) armElectionTimer() {
	n.electionElapsed = 0
	n.electionTimeout = 10
	if n.cfg.ElectionTimeout != nil {
		n.electionTimeout = n.cfg.ElectionTimeout()
	}
	if n.electionTimeout < 1 {
		n.electionTimeout = 1
	}
}

func (n *Node) becomeFollower() {
	if n.role != Follower {
		if n.role == Leader {
			n.armElectionTimer() // leaders do not count election time
		}
		n.role = Follower
		n.votes, n.next, n.match = nil, nil, nil
	}
}

// Restart simulates a crash followed by recovery from durable state.
func (n *Node) Restart() {
	n.role = Follower
	n.leader = ""
	n.votes, n.next, n.match = nil, nil, nil
	n.heartbeatElapsed = 0
	n.armElectionTimer()
}

// Propose appends cmd to the log if this node is the leader.
func (n *Node) Propose(cmd string) (index uint64, ok bool) {
	if n.role != Leader {
		return 0, false
	}
	n.log = append(n.log, Entry{Term: n.term, Cmd: cmd})
	n.advanceCommit()
	return n.lastIndex(), true
}

// Tick advances logical time by one tick and returns messages to send.
func (n *Node) Tick() []Message {
	if n.role == Leader {
		n.heartbeatElapsed++
		if n.heartbeatElapsed >= n.cfg.HeartbeatTicks {
			n.heartbeatElapsed = 0
			return n.broadcastAppend()
		}
		return nil
	}
	n.electionElapsed++
	if n.electionElapsed >= n.electionTimeout {
		return n.startElection()
	}
	return nil
}

func (n *Node) startElection() []Message {
	n.term++
	n.role = Candidate
	n.votedFor = n.cfg.ID
	n.votes = map[ID]bool{n.cfg.ID: true}
	n.leader = ""
	n.armElectionTimer()
	if len(n.votes) >= n.majority() {
		return n.becomeLeader()
	}
	var out []Message
	for _, p := range n.peers {
		out = append(out, Message{
			Type: MsgVoteRequest, From: n.cfg.ID, To: p, Term: n.term,
			LastLogIndex: n.lastIndex(), LastLogTerm: n.termAt(n.lastIndex()),
		})
	}
	return out
}

func (n *Node) becomeLeader() []Message {
	n.role = Leader
	n.leader = n.cfg.ID
	n.votes = nil
	n.next = make(map[ID]uint64, len(n.peers))
	n.match = make(map[ID]uint64, len(n.peers))
	for _, p := range n.peers {
		n.next[p] = n.lastIndex() + 1
		n.match[p] = 0
	}
	n.log = append(n.log, Entry{Term: n.term}) // no-op
	n.heartbeatElapsed = 0
	n.advanceCommit()
	return n.broadcastAppend()
}

func (n *Node) appendFor(p ID) Message {
	prev := n.next[p] - 1
	return Message{
		Type: MsgAppend, From: n.cfg.ID, To: p, Term: n.term,
		PrevLogIndex: prev, PrevLogTerm: n.termAt(prev),
		Entries:      append([]Entry(nil), n.log[prev:]...),
		LeaderCommit: n.commit,
	}
}

func (n *Node) broadcastAppend() []Message {
	out := make([]Message, 0, len(n.peers))
	for _, p := range n.peers {
		out = append(out, n.appendFor(p))
	}
	return out
}

func (n *Node) advanceCommit() {
	for idx := n.lastIndex(); idx > n.commit; idx-- {
		if n.termAt(idx) != n.term {
			return // older terms: only committed indirectly
		}
		count := 1
		for _, p := range n.peers {
			if n.match[p] >= idx {
				count++
			}
		}
		if count >= n.majority() {
			n.commit = idx
			return
		}
	}
}

// Step handles one incoming message and returns messages to send.
func (n *Node) Step(m Message) []Message {
	if m.Term > n.term {
		n.term = m.Term
		n.votedFor = ""
		n.leader = ""
		n.becomeFollower()
	}
	if m.Term < n.term {
		switch m.Type {
		case MsgVoteRequest:
			return []Message{{Type: MsgVoteResponse, From: n.cfg.ID, To: m.From, Term: n.term}}
		case MsgAppend:
			return []Message{{Type: MsgAppendResponse, From: n.cfg.ID, To: m.From, Term: n.term}}
		}
		return nil
	}

	switch m.Type {
	case MsgVoteRequest:
		upToDate := m.LastLogTerm > n.termAt(n.lastIndex()) ||
			(m.LastLogTerm == n.termAt(n.lastIndex()) && m.LastLogIndex >= n.lastIndex())
		if breakSkipLogCheck {
			upToDate = true // BREAK-IT: accept candidates with stale logs
		}
		grant := (n.votedFor == "" || n.votedFor == m.From) && upToDate
		if grant {
			n.votedFor = m.From
			n.electionElapsed = 0
		}
		return []Message{{Type: MsgVoteResponse, From: n.cfg.ID, To: m.From, Term: n.term, Granted: grant}}

	case MsgVoteResponse:
		if n.role != Candidate || !m.Granted {
			return nil
		}
		n.votes[m.From] = true
		if len(n.votes) >= n.majority() {
			return n.becomeLeader()
		}

	case MsgAppend:
		n.becomeFollower()
		n.leader = m.From
		n.electionElapsed = 0
		if m.PrevLogIndex > n.lastIndex() || n.termAt(m.PrevLogIndex) != m.PrevLogTerm {
			return []Message{{Type: MsgAppendResponse, From: n.cfg.ID, To: m.From, Term: n.term}}
		}
		for i, e := range m.Entries {
			idx := m.PrevLogIndex + 1 + uint64(i)
			if idx <= n.lastIndex() {
				if n.log[idx-1].Term == e.Term {
					continue
				}
				n.log = n.log[:idx-1]
			}
			n.log = append(n.log, e)
		}
		lastNew := m.PrevLogIndex + uint64(len(m.Entries))
		if c := min(m.LeaderCommit, lastNew); c > n.commit {
			n.commit = c
		}
		return []Message{{Type: MsgAppendResponse, From: n.cfg.ID, To: m.From, Term: n.term, Success: true, MatchIndex: lastNew}}

	case MsgAppendResponse:
		if n.role != Leader {
			return nil
		}
		if m.Success {
			if m.MatchIndex > n.match[m.From] {
				n.match[m.From] = m.MatchIndex
			}
			n.next[m.From] = n.match[m.From] + 1
			n.advanceCommit()
			return nil
		}
		if n.next[m.From] > n.match[m.From]+1 && n.next[m.From] > 1 {
			n.next[m.From]--
		}
		return []Message{n.appendFor(m.From)}
	}
	return nil
}
