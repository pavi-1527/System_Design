package raftlite

import (
	"fmt"
	"math/rand/v2"
	"reflect"
	"testing"
)

// ---------- helpers (prefixed with t to avoid clashing with your code) ----------

// tNew returns a node with a constant 10-tick election timeout and a
// 3-tick heartbeat.
func tNew(id ID, peers ...ID) *Node {
	return NewNode(Config{ID: id, Peers: peers, ElectionTimeout: func() int { return 10 }, HeartbeatTicks: 3})
}

// tTicks ticks n times and returns every message produced.
func tTicks(n *Node, k int) []Message {
	var out []Message
	for i := 0; i < k; i++ {
		out = append(out, n.Tick()...)
	}
	return out
}

func tAppendMsg(from, to ID, term, prevIdx, prevTerm, commit uint64, entries ...Entry) Message {
	return Message{Type: MsgAppend, From: from, To: to, Term: term, PrevLogIndex: prevIdx, PrevLogTerm: prevTerm, Entries: entries, LeaderCommit: commit}
}

func tGrant(from, to ID, term uint64) Message {
	return Message{Type: MsgVoteResponse, From: from, To: to, Term: term, Granted: true}
}

func tAppendOK(from, to ID, term, match uint64) Message {
	return Message{Type: MsgAppendResponse, From: from, To: to, Term: term, Success: true, MatchIndex: match}
}

func tAppendNo(from, to ID, term uint64) Message {
	return Message{Type: MsgAppendResponse, From: from, To: to, Term: term}
}

// tCampaign ticks n until it becomes a candidate (at most 10 ticks) and
// returns its vote requests.
func tCampaign(t *testing.T, n *Node) []Message {
	t.Helper()
	msgs := tTicks(n, 10)
	if n.Role() != Candidate {
		t.Fatalf("after 10 ticks with a 10-tick timeout the node is %v, want candidate", n.Role())
	}
	return msgs
}

// tLeaderOf3 returns node x (peers y, z) as leader of term 1 with an empty
// log before the no-op, plus the messages it sent on winning.
func tLeaderOf3(t *testing.T) (*Node, []Message) {
	t.Helper()
	n := tNew("x", "y", "z")
	tCampaign(t, n)
	out := n.Step(tGrant("y", "x", n.Term()))
	if n.Role() != Leader {
		t.Fatalf("self + one of two peers is a majority of 3, but the node is %v", n.Role())
	}
	return n, out
}

func tEntries(es ...Entry) []Entry { return es }

// tCmds returns the non-no-op commands of the first upto entries.
func tCmds(log []Entry, upto int) []string {
	var out []string
	for i := 0; i < upto && i < len(log); i++ {
		if !log[i].IsNoop() {
			out = append(out, log[i].Cmd)
		}
	}
	return out
}

// ---------- election: single node behaviour ----------

func TestElection_TimeoutStartsACampaignAndTheTimerRestarts(t *testing.T) {
	n := tNew("x", "y", "z")
	if n.Role() != Follower || n.Term() != 0 {
		t.Fatalf("new node = %v at term %d, want follower at term 0", n.Role(), n.Term())
	}
	if msgs := tTicks(n, 9); len(msgs) != 0 || n.Role() != Follower {
		t.Fatalf("9 ticks of a 10-tick timeout produced %v / %v, want silence", msgs, n.Role())
	}
	msgs := n.Tick()
	if n.Role() != Candidate || n.Term() != 1 || n.VotedFor() != "x" {
		t.Fatalf("after the timeout: %v, term %d, voted for %q; want candidate, term 1, voted for itself", n.Role(), n.Term(), n.VotedFor())
	}
	if len(msgs) != 2 {
		t.Fatalf("a campaign sends %d messages, want one vote request per peer (2)", len(msgs))
	}
	to := map[ID]bool{}
	for _, m := range msgs {
		to[m.To] = true
		if m.Type != MsgVoteRequest || m.From != "x" || m.Term != 1 || m.LastLogIndex != 0 || m.LastLogTerm != 0 {
			t.Errorf("bad vote request %+v", m)
		}
	}
	if !to["y"] || !to["z"] {
		t.Fatalf("vote requests went to %v, want y and z", to)
	}
	if msgs := tTicks(n, 9); len(msgs) != 0 {
		t.Fatalf("the timer must restart when a campaign starts; got messages after only 9 more ticks: %v", msgs)
	}
	n.Tick()
	if n.Term() != 2 {
		t.Fatalf("a candidate that gets no majority must start a new election in a higher term; term = %d, want 2", n.Term())
	}
}

func TestElection_VoteRequestCarriesTheCandidatesLastLogPosition(t *testing.T) {
	n := tNew("x", "y")
	n.Step(tAppendMsg("y", "x", 2, 0, 0, 0, Entry{1, "a"}, Entry{2, "b"}))
	msgs := tTicks(n, 10)
	if len(msgs) != 1 || msgs[0].Type != MsgVoteRequest || msgs[0].Term != 3 || msgs[0].LastLogIndex != 2 || msgs[0].LastLogTerm != 2 {
		t.Fatalf("vote request = %+v, want term 3 with LastLogIndex 2 and LastLogTerm 2: voters decide on exactly these two numbers", msgs)
	}
}

func TestElection_CandidateNeedsAStrictMajorityOfDistinctVotes(t *testing.T) {
	n := tNew("a", "b", "c", "d", "e") // majority is 3 of 5
	tCampaign(t, n)
	term := n.Term()
	n.Step(tGrant("b", "a", term))
	if n.Role() != Candidate {
		t.Fatalf("2 of 5 votes made the node %v; a minority must not lead", n.Role())
	}
	n.Step(tGrant("b", "a", term)) // duplicate delivery of the same vote
	if n.Role() != Candidate {
		t.Fatalf("a duplicated vote from the same peer was counted twice")
	}
	n.Step(Message{Type: MsgVoteResponse, From: "c", To: "a", Term: term, Granted: false})
	if n.Role() != Candidate {
		t.Fatalf("a refusal was counted as a vote")
	}
	n.Step(tGrant("d", "a", term-1)) // a vote for an older election
	if n.Role() != Candidate {
		t.Fatalf("a vote from an older term was counted")
	}
	n.Step(tGrant("c", "a", term))
	if n.Role() != Leader || n.Leader() != "a" {
		t.Fatalf("3 of 5 votes: role %v, leader %q; want leader a", n.Role(), n.Leader())
	}
}

func TestElection_SingleNodeClusterElectsItselfAndCommitsAlone(t *testing.T) {
	n := NewNode(Config{ID: "solo", ElectionTimeout: func() int { return 3 }, HeartbeatTicks: 1})
	tTicks(n, 3)
	if n.Role() != Leader || n.Term() != 1 {
		t.Fatalf("a lone node is %v at term %d, want leader at term 1", n.Role(), n.Term())
	}
	idx, ok := n.Propose("x")
	if !ok || idx != 2 || n.CommitIndex() != 2 {
		t.Fatalf("Propose = (%d, %v), commit = %d; want index 2 (after the no-op at 1), accepted, committed immediately", idx, ok, n.CommitIndex())
	}
}

// ---------- election: voting rules ----------

func TestVote_OnePerTermAndRepeatsAreIdempotent(t *testing.T) {
	n := tNew("x", "y", "z")
	req := func(from ID, term uint64) Message {
		return Message{Type: MsgVoteRequest, From: from, To: "x", Term: term}
	}
	grant := func(out []Message) bool { return len(out) == 1 && out[0].Type == MsgVoteResponse && out[0].Granted }
	if out := n.Step(req("y", 1)); !grant(out) || out[0].Term != 1 || out[0].To != "y" {
		t.Fatalf("first candidate in term 1: %+v, want a granted response", out)
	}
	if n.VotedFor() != "y" || n.Term() != 1 {
		t.Fatalf("after granting: votedFor %q term %d", n.VotedFor(), n.Term())
	}
	if out := n.Step(req("z", 1)); grant(out) {
		t.Fatalf("voted for two different candidates in the same term: two leaders could win it")
	}
	if out := n.Step(req("y", 1)); !grant(out) {
		t.Fatalf("a repeated request from the candidate we already voted for must be granted again (the reply may have been lost)")
	}
	if out := n.Step(req("z", 2)); !grant(out) {
		t.Fatalf("a new term resets the vote; request from z in term 2 should be granted, got %+v", out)
	}
}

func TestVote_RejectsACandidateWithAStaleLog(t *testing.T) {
	// x holds [(1,a) (1,b)]. A candidate must have a log at least as up to
	// date: a higher last term wins; equal last terms compare length.
	setup := func() *Node {
		n := tNew("x", "y", "z")
		n.Step(tAppendMsg("y", "x", 1, 0, 0, 0, Entry{1, "a"}, Entry{1, "b"}))
		return n
	}
	cases := []struct {
		name            string
		lastIdx, lastTm uint64
		want            bool
	}{
		{"empty log", 0, 0, false},
		{"same term, shorter log", 1, 1, false},
		{"same term, same length", 2, 1, true},
		{"same term, longer log", 3, 1, true},
		{"higher last term, shorter log", 1, 2, true},
		{"lower last term, longer log", 9, 0, false},
	}
	for _, c := range cases {
		n := setup()
		out := n.Step(Message{Type: MsgVoteRequest, From: "z", To: "x", Term: 2, LastLogIndex: c.lastIdx, LastLogTerm: c.lastTm})
		if len(out) != 1 || out[0].Granted != c.want {
			t.Errorf("%s: candidate (idx %d, term %d) vs log [(1,a) (1,b)]: granted = %v, want %v; granting a stale candidate lets a leader overwrite committed entries", c.name, c.lastIdx, c.lastTm, out, c.want)
		}
		if n.Term() != 2 {
			t.Errorf("%s: term = %d, want 2: a higher term must be adopted even when the vote is refused", c.name, n.Term())
		}
	}
}

func TestVote_StaleTermRequestIsRefusedWithTheCurrentTerm(t *testing.T) {
	n := tNew("x", "y", "z")
	n.Step(tAppendMsg("y", "x", 5, 0, 0, 0))
	out := n.Step(Message{Type: MsgVoteRequest, From: "z", To: "x", Term: 3})
	if len(out) != 1 || out[0].Granted || out[0].Term != 5 || out[0].Type != MsgVoteResponse {
		t.Fatalf("reply to a stale request = %+v, want a refusal carrying term 5 so the candidate steps down", out)
	}
}

func TestVote_GrantingRestartsTheElectionTimerButRefusingDoesNot(t *testing.T) {
	n := tNew("x", "y", "z")
	tTicks(n, 8)
	n.Step(Message{Type: MsgVoteRequest, From: "y", To: "x", Term: 1}) // granted: timer restarts
	tTicks(n, 8)
	if n.Role() != Follower {
		t.Fatalf("a granted vote must restart the election timer; node started a campaign after 8 more ticks")
	}
	n.Step(Message{Type: MsgVoteRequest, From: "z", To: "x", Term: 1}) // refused: no restart
	tTicks(n, 2)
	if n.Role() != Candidate {
		t.Fatalf("a refused vote must NOT restart the timer (a flapping candidate would otherwise starve elections); role = %v", n.Role())
	}
}

// ---------- terms and stepping down ----------

func TestTerms_HigherTermForcesAnyRoleToStepDown(t *testing.T) {
	cand := tNew("x", "y", "z")
	tCampaign(t, cand)
	cand.Step(Message{Type: MsgVoteResponse, From: "y", To: "x", Term: 7})
	if cand.Role() != Follower || cand.Term() != 7 || cand.VotedFor() != "" {
		t.Fatalf("candidate that saw term 7: %v term %d vote %q; want follower, term 7, vote cleared", cand.Role(), cand.Term(), cand.VotedFor())
	}

	lead, _ := tLeaderOf3(t)
	lead.Step(tAppendNo("y", "x", 9))
	if lead.Role() != Follower || lead.Term() != 9 || lead.Leader() != "" {
		t.Fatalf("leader that saw term 9: %v term %d leader %q; want follower, term 9, no known leader", lead.Role(), lead.Term(), lead.Leader())
	}
	if msgs := tTicks(lead, 2); len(msgs) != 0 {
		t.Fatalf("a deposed leader kept sending: %v", msgs)
	}
	if msgs := tTicks(lead, 10); len(msgs) == 0 || lead.Role() != Candidate {
		t.Fatalf("a deposed leader must run an election timer like any follower; role = %v", lead.Role())
	}
}

func TestTerms_CandidateStepsDownForALeaderOfTheSameTerm(t *testing.T) {
	n := tNew("x", "y", "z")
	tCampaign(t, n)
	n.Step(tAppendMsg("y", "x", n.Term(), 0, 0, 0))
	if n.Role() != Follower || n.Leader() != "y" {
		t.Fatalf("candidate that received an Append from this term's leader: %v / leader %q; want follower of y", n.Role(), n.Leader())
	}
}

// ---------- log replication: follower side ----------

func TestAppend_FollowerRefusesWhenItsLogDoesNotContainPrev(t *testing.T) {
	n := tNew("x", "y", "z")
	n.Step(tAppendMsg("y", "x", 1, 0, 0, 0, Entry{1, "a"}, Entry{1, "b"}))
	for _, c := range []struct {
		name         string
		prevI, prevT uint64
	}{
		{"prev beyond the end", 5, 1},
		{"prev has a different term", 2, 2},
		{"prev index 1 with wrong term", 1, 3},
	} {
		out := n.Step(tAppendMsg("y", "x", 1, c.prevI, c.prevT, 0, Entry{1, "z"}))
		if len(out) != 1 || out[0].Type != MsgAppendResponse || out[0].Success {
			t.Errorf("%s: reply %+v, want a refusal", c.name, out)
		}
		if got := len(n.Log()); got != 2 {
			t.Errorf("%s: a refused Append changed the log (len %d)", c.name, got)
		}
	}
}

func TestAppend_ConflictingSuffixIsOverwritten(t *testing.T) {
	n := tNew("x", "y", "z")
	n.Step(tAppendMsg("y", "x", 1, 0, 0, 0, Entry{1, "a"}, Entry{1, "b"}, Entry{1, "c"})) // old leader's uncommitted tail
	out := n.Step(tAppendMsg("z", "x", 2, 1, 1, 0, Entry{2, "X"}))
	want := tEntries(Entry{1, "a"}, Entry{2, "X"})
	if got := n.Log(); !reflect.DeepEqual(got, want) {
		t.Fatalf("log = %v, want %v: from the first conflicting entry on, the follower's log must be replaced by the leader's (log matching)", got, want)
	}
	if len(out) != 1 || !out[0].Success || out[0].MatchIndex != 2 || out[0].Term != 2 || out[0].To != "z" {
		t.Fatalf("reply = %+v, want success with MatchIndex 2 in term 2 to z", out)
	}
}

func TestAppend_OldOrDuplicatedAppendNeverShortensTheLog(t *testing.T) {
	n := tNew("x", "y", "z")
	full := tEntries(Entry{1, "a"}, Entry{1, "b"}, Entry{1, "c"})
	n.Step(tAppendMsg("y", "x", 1, 0, 0, 0, full...))
	out := n.Step(tAppendMsg("y", "x", 1, 0, 0, 0, Entry{1, "a"})) // a delayed, shorter copy
	if got := n.Log(); !reflect.DeepEqual(got, full) {
		t.Fatalf("log = %v, want it unchanged %v: only a CONFLICTING entry may cause truncation", got, full)
	}
	if len(out) != 1 || !out[0].Success || out[0].MatchIndex != 1 {
		t.Fatalf("reply = %+v, want success with MatchIndex 1 (what this message covered)", out)
	}
}

func TestAppend_CommitIndexFollowsTheLeaderButNeverPastWhatWasSent(t *testing.T) {
	n := tNew("x", "y", "z")
	n.Step(tAppendMsg("y", "x", 1, 0, 0, 10, Entry{1, "a"}, Entry{1, "b"}))
	if n.CommitIndex() != 2 {
		t.Fatalf("commit = %d, want 2: min(LeaderCommit 10, last entry sent 2)", n.CommitIndex())
	}
	n.Step(tAppendMsg("y", "x", 1, 2, 1, 1, Entry{1, "c"}))
	if n.CommitIndex() != 2 {
		t.Fatalf("commit = %d after an Append with a smaller LeaderCommit, want it to stay 2 (never decreases)", n.CommitIndex())
	}
	n.Step(tAppendMsg("y", "x", 1, 3, 1, 3))
	if n.CommitIndex() != 3 {
		t.Fatalf("commit = %d after a heartbeat with LeaderCommit 3, want 3", n.CommitIndex())
	}
}

func TestAppend_StaleLeaderIsToldTheNewTermAndChangesNothing(t *testing.T) {
	n := tNew("x", "y", "z")
	n.Step(tAppendMsg("y", "x", 5, 0, 0, 0, Entry{5, "a"}))
	out := n.Step(tAppendMsg("z", "x", 3, 0, 0, 0, Entry{3, "old"}))
	if len(out) != 1 || out[0].Success || out[0].Term != 5 {
		t.Fatalf("reply to a stale leader = %+v, want a refusal in term 5", out)
	}
	if got := n.Log(); !reflect.DeepEqual(got, tEntries(Entry{5, "a"})) || n.Leader() != "y" {
		t.Fatalf("a stale leader changed the log (%v) or the known leader (%q)", got, n.Leader())
	}
}

func TestAppend_ValidAppendRestartsTheElectionTimerAndStaleOneDoesNot(t *testing.T) {
	n := tNew("x", "y", "z")
	n.Step(tAppendMsg("y", "x", 2, 0, 0, 0)) // term 2, leader y
	tTicks(n, 9)
	n.Step(tAppendMsg("y", "x", 2, 0, 0, 0)) // heartbeat
	tTicks(n, 9)
	if n.Role() != Follower {
		t.Fatalf("heartbeats must keep a follower from campaigning")
	}
	n.Step(tAppendMsg("z", "x", 1, 0, 0, 0)) // stale term: must not count as contact from a leader
	n.Tick()
	if n.Role() != Candidate {
		t.Fatalf("a stale-term Append restarted the election timer; role = %v", n.Role())
	}
}

// ---------- log replication: leader side ----------

func TestLeader_AppendsANoOpAndBroadcastsImmediately(t *testing.T) {
	n, out := tLeaderOf3(t)
	if got := n.Log(); len(got) != 1 || !got[0].IsNoop() || got[0].Term != 1 {
		t.Fatalf("log after winning = %v, want one no-op entry in term 1", got)
	}
	if len(out) != 2 {
		t.Fatalf("winning sends %d messages, want an Append to each of the 2 peers", len(out))
	}
	for _, m := range out {
		if m.Type != MsgAppend || m.Term != 1 || m.PrevLogIndex != 0 || m.PrevLogTerm != 0 || len(m.Entries) != 1 || !m.Entries[0].IsNoop() || m.LeaderCommit != 0 {
			t.Errorf("bad first Append %+v", m)
		}
	}
}

func TestLeader_HeartbeatsAreSentEveryHeartbeatTicks(t *testing.T) {
	n, _ := tLeaderOf3(t)
	if msgs := tTicks(n, 2); len(msgs) != 0 {
		t.Fatalf("leader sent %d messages in 2 ticks of a 3-tick heartbeat, want 0", len(msgs))
	}
	if msgs := n.Tick(); len(msgs) != 2 {
		t.Fatalf("third tick sent %d messages, want 2 (one per peer)", len(msgs))
	}
	n.Step(tAppendOK("y", "x", 1, 1)) // both peers hold the no-op
	n.Step(tAppendOK("z", "x", 1, 1))
	idx, ok := n.Propose("pay")
	if !ok || idx != 2 {
		t.Fatalf("Propose = (%d, %v), want (2, true)", idx, ok)
	}
	msgs := tTicks(n, 3)
	if len(msgs) != 2 || !reflect.DeepEqual(msgs[0].Entries, tEntries(Entry{1, "pay"})) || msgs[0].PrevLogIndex != 1 {
		t.Fatalf("next heartbeat = %+v, want Appends carrying the new entry after prev=1", msgs)
	}
}

func TestLeader_ProposeIsRefusedByNonLeaders(t *testing.T) {
	n := tNew("x", "y", "z")
	if idx, ok := n.Propose("c"); ok || idx != 0 {
		t.Fatalf("a follower accepted a proposal: (%d, %v)", idx, ok)
	}
	tCampaign(t, n)
	if _, ok := n.Propose("c"); ok {
		t.Fatalf("a candidate accepted a proposal")
	}
	if len(n.Log()) != 0 {
		t.Fatalf("a refused proposal still changed the log")
	}
}

func TestLeader_CommitsOnlyWhenAMajorityHoldsTheEntry(t *testing.T) {
	n := tNew("a", "b", "c", "d", "e") // majority is 3
	tCampaign(t, n)
	n.Step(tGrant("b", "a", 1))
	n.Step(tGrant("c", "a", 1))
	idx, _ := n.Propose("x") // log: [noop x]
	if idx != 2 {
		t.Fatalf("Propose index = %d, want 2", idx)
	}
	n.Step(tAppendOK("b", "a", 1, 2))
	if n.CommitIndex() != 0 {
		t.Fatalf("committed with 2 of 5 copies (leader + one follower); commit = %d", n.CommitIndex())
	}
	n.Step(tAppendOK("b", "a", 1, 2)) // duplicate acknowledgement
	if n.CommitIndex() != 0 {
		t.Fatalf("a duplicated acknowledgement counted twice; commit = %d", n.CommitIndex())
	}
	n.Step(tAppendOK("c", "a", 1, 2))
	if n.CommitIndex() != 2 {
		t.Fatalf("commit = %d with 3 of 5 copies, want 2 (this also commits the no-op before it)", n.CommitIndex())
	}
	n.Step(tAppendOK("d", "a", 1, 1)) // a lagging follower acknowledging less must not move anything back
	if n.CommitIndex() != 2 {
		t.Fatalf("commit moved to %d", n.CommitIndex())
	}
}

func TestLeader_NeverCommitsAnEarlierTermsEntryByCountingReplicas(t *testing.T) {
	// x inherits [(1,a) (2,b)] from a term-2 leader, then wins term 3. Entry 2
	// may already be on a majority, but that alone does NOT make it safe to
	// commit: a different future leader could still overwrite it. It becomes
	// committed only when an entry of the CURRENT term is committed after it.
	n := tNew("x", "y", "z")
	n.Step(tAppendMsg("y", "x", 2, 0, 0, 0, Entry{1, "a"}, Entry{2, "b"}))
	out := func() []Message { tCampaign(t, n); return n.Step(tGrant("y", "x", n.Term())) }()
	if n.Role() != Leader || n.Term() != 3 {
		t.Fatalf("setup: %v at term %d, want leader at term 3", n.Role(), n.Term())
	}
	if len(out) != 2 || out[0].PrevLogIndex != 2 || out[0].PrevLogTerm != 2 || !reflect.DeepEqual(out[0].Entries, tEntries(Entry{3, ""})) {
		t.Fatalf("first Append after winning = %+v, want prev=(2,term 2) carrying only the new no-op", out)
	}
	n.Step(tAppendOK("y", "x", 3, 2)) // entry 2 is now on x and y: a majority
	if n.CommitIndex() != 0 {
		t.Fatalf("commit = %d: an entry from term 2 was committed by counting replicas in term 3 (the Raft paper's Figure 8 bug)", n.CommitIndex())
	}
	n.Step(tAppendOK("y", "x", 3, 3)) // the term-3 no-op reaches a majority
	if n.CommitIndex() != 3 {
		t.Fatalf("commit = %d, want 3: committing the no-op commits everything before it", n.CommitIndex())
	}
}

func TestLeader_RefusalBacksUpNextIndexAndResendsAtOnce(t *testing.T) {
	n := tNew("x", "y", "z")
	n.Step(tAppendMsg("y", "x", 2, 0, 0, 0, Entry{1, "a"}, Entry{2, "b"}))
	tCampaign(t, n)
	n.Step(tGrant("y", "x", n.Term())) // log: [(1,a) (2,b) (3,noop)]

	out := n.Step(tAppendNo("z", "x", 3))
	if len(out) != 1 || out[0].To != "z" || out[0].PrevLogIndex != 1 || out[0].PrevLogTerm != 1 || len(out[0].Entries) != 2 {
		t.Fatalf("after the first refusal: %+v, want an immediate retry to z with prev=(1, term 1) and 2 entries", out)
	}
	out = n.Step(tAppendNo("z", "x", 3))
	if len(out) != 1 || out[0].PrevLogIndex != 0 || len(out[0].Entries) != 3 {
		t.Fatalf("after the second refusal: %+v, want prev=0 with the whole log", out)
	}
	out = n.Step(tAppendNo("z", "x", 3))
	if len(out) != 1 || out[0].PrevLogIndex != 0 {
		t.Fatalf("nextIndex went below 1: %+v", out)
	}
	n.Step(tAppendOK("z", "x", 3, 3))
	if n.CommitIndex() != 3 {
		t.Fatalf("commit = %d after z caught up (x + z = 2 of 3), want 3", n.CommitIndex())
	}
}

func TestLeader_IgnoresResponsesFromOtherTerms(t *testing.T) {
	n, _ := tLeaderOf3(t)
	n.Propose("c")
	n.Step(tAppendOK("y", "x", 0, 2)) // a response to some long-gone leadership
	if n.CommitIndex() != 0 {
		t.Fatalf("a response from term 0 advanced the commit index to %d", n.CommitIndex())
	}
}

func TestLeader_ADelayedAcknowledgementNeverMovesProgressBackwards(t *testing.T) {
	n, _ := tLeaderOf3(t)
	n.Propose("a")
	n.Propose("b") // log: [noop a b]
	n.Step(tAppendOK("y", "x", 1, 3))
	n.Step(tAppendOK("y", "x", 1, 1)) // an old acknowledgement arrives late
	if n.CommitIndex() != 3 {
		t.Fatalf("commit = %d, want 3", n.CommitIndex())
	}
	for _, m := range tTicks(n, 3) {
		if m.To == "y" && (m.PrevLogIndex != 3 || len(m.Entries) != 0) {
			t.Fatalf("next Append to y = %+v, want a heartbeat after prev=3: a late acknowledgement must not make the leader resend what y already holds", m)
		}
	}
}

// ---------- cluster behaviour ----------

var tIDs5 = []ID{"A", "B", "C", "D", "E"}

func tStable(c *Cluster) bool {
	l := c.Leader()
	if l == "" {
		return false
	}
	for _, id := range c.IDs() {
		if !c.Up(id) {
			continue
		}
		n := c.Node(id)
		if n.Leader() != l || n.Term() != c.Node(l).Term() {
			return false
		}
	}
	return true
}

func tNoViolations(t *testing.T, c *Cluster, ctx string) {
	t.Helper()
	if v := c.Violations(); len(v) > 0 {
		t.Fatalf("%s: safety violated: %v", ctx, v)
	}
}

func TestCluster_ElectsExactlyOneStableLeader(t *testing.T) {
	for seed := uint64(1); seed <= 40; seed++ {
		c := NewCluster(tIDs5, seed)
		if !c.RunUntil(400, func() bool { return tStable(c) }) {
			t.Fatalf("seed %d: no leader known to everyone after 400 ticks; randomized timeouts should break ties within a few rounds", seed)
		}
		l, term := c.Leader(), c.Node(c.Leader()).Term()
		c.Run(100)
		if c.Leader() != l || c.Node(l).Term() != term {
			t.Fatalf("seed %d: leadership changed from %s/term %d to %s/term %d with a healthy network; heartbeats must suppress elections", seed, l, term, c.Leader(), c.Node(c.Leader()).Term())
		}
		tNoViolations(t, c, fmt.Sprintf("seed %d", seed))
	}
}

func TestCluster_SameSeedGivesTheSameHistory(t *testing.T) {
	run := func(seed uint64) (ID, uint64, int) {
		c := NewCluster(tIDs5, seed)
		c.RunUntil(400, func() bool { return tStable(c) })
		return c.Leader(), c.Node(c.Leader()).Term(), c.Ticks()
	}
	for seed := uint64(1); seed <= 5; seed++ {
		l1, t1, k1 := run(seed)
		l2, t2, k2 := run(seed)
		if l1 == "" {
			t.Fatalf("seed %d: no leader was elected", seed)
		}
		if l1 != l2 || t1 != t2 || k1 != k2 {
			t.Fatalf("seed %d: runs differ (%s/%d/%d ticks vs %s/%d/%d ticks); a node must use only the injected timeout source and the messages it is given", seed, l1, t1, k1, l2, t2, k2)
		}
	}
}

func TestCluster_ReelectsAfterTheLeaderCrashesAndTheOldLeaderRejoinsAsAFollower(t *testing.T) {
	for seed := uint64(1); seed <= 20; seed++ {
		c := NewCluster(tIDs5, seed)
		if !c.RunUntil(400, func() bool { return tStable(c) }) {
			t.Fatalf("seed %d: no initial leader", seed)
		}
		old := c.Leader()
		oldTerm := c.Node(old).Term()
		c.Crash(old)
		if !c.RunUntil(400, func() bool { return tStable(c) }) {
			t.Fatalf("seed %d: no new leader after %s crashed", seed, old)
		}
		if c.Leader() == old || c.Node(c.Leader()).Term() <= oldTerm {
			t.Fatalf("seed %d: new leader %s at term %d; want a different node in a higher term than %d", seed, c.Leader(), c.Node(c.Leader()).Term(), oldTerm)
		}
		c.Restart(old)
		if !c.RunUntil(300, func() bool { return tStable(c) }) || c.Node(old).Role() != Follower {
			t.Fatalf("seed %d: restarted old leader %s did not settle as a follower (role %v)", seed, old, c.Node(old).Role())
		}
		tNoViolations(t, c, fmt.Sprintf("seed %d", seed))
	}
}

// tCommit proposes cmd on the current leader and runs until every running
// node has committed it.
func tCommit(t *testing.T, c *Cluster, cmd string) {
	t.Helper()
	_, idx, ok := c.Propose(cmd)
	if !ok {
		t.Fatalf("no leader to take %q", cmd)
	}
	done := func() bool {
		for _, id := range c.IDs() {
			if c.Up(id) && c.Node(id).CommitIndex() < idx {
				return false
			}
		}
		return true
	}
	if !c.RunUntil(300, done) {
		t.Fatalf("%q (index %d) not committed on every running node after 300 ticks", cmd, idx)
	}
}

func TestCluster_CommittedEntriesSurviveLeaderChanges(t *testing.T) {
	for seed := uint64(1); seed <= 15; seed++ {
		c := NewCluster(tIDs5, seed)
		if !c.RunUntil(400, func() bool { return tStable(c) }) {
			t.Fatalf("seed %d: no leader", seed)
		}
		var want []string
		for i := 1; i <= 5; i++ {
			cmd := fmt.Sprintf("cmd-%d", i)
			want = append(want, cmd)
			tCommit(t, c, cmd)
		}
		old := c.Leader()
		c.Crash(old)
		if !c.RunUntil(400, func() bool { return tStable(c) }) {
			t.Fatalf("seed %d: no leader after crash", seed)
		}
		for i := 6; i <= 8; i++ {
			cmd := fmt.Sprintf("cmd-%d", i)
			want = append(want, cmd)
			tCommit(t, c, cmd)
		}
		c.Restart(old)
		c.RunUntil(300, func() bool { return tStable(c) && c.Node(old).CommitIndex() == c.Node(c.Leader()).CommitIndex() })
		for _, id := range c.IDs() {
			n := c.Node(id)
			if got := tCmds(n.Log(), int(n.CommitIndex())); !reflect.DeepEqual(got, want) {
				t.Fatalf("seed %d: node %s committed %v, want %v: acknowledged commands must never be lost or reordered", seed, id, got, want)
			}
		}
		tNoViolations(t, c, fmt.Sprintf("seed %d", seed))
	}
}

func TestCluster_LaggingFollowerCatchesUpAfterRestart(t *testing.T) {
	c := NewCluster(tIDs5, 3)
	if !c.RunUntil(400, func() bool { return tStable(c) }) {
		t.Fatal("no leader")
	}
	victim := ID("A")
	if c.Leader() == victim {
		victim = "B"
	}
	c.Crash(victim)
	for i := 1; i <= 10; i++ {
		tCommit(t, c, fmt.Sprintf("cmd-%d", i))
	}
	c.Restart(victim)
	caughtUp := func() bool {
		l := c.Node(c.Leader())
		return reflect.DeepEqual(c.Node(victim).Log(), l.Log()) && c.Node(victim).CommitIndex() == l.CommitIndex()
	}
	if !c.RunUntil(300, caughtUp) {
		t.Fatalf("restarted follower %s did not catch up: log %d entries, commit %d; leader has %d entries, commit %d", victim, len(c.Node(victim).Log()), c.Node(victim).CommitIndex(), len(c.Node(c.Leader()).Log()), c.Node(c.Leader()).CommitIndex())
	}
	tNoViolations(t, c, "catch-up")
}

func TestCluster_MinorityPartitionCannotCommitAndItsEntriesAreDiscardedOnHeal(t *testing.T) {
	for seed := uint64(1); seed <= 10; seed++ {
		c := NewCluster(tIDs5, seed)
		if !c.RunUntil(400, func() bool { return tStable(c) }) {
			t.Fatalf("seed %d: no leader", seed)
		}
		tCommit(t, c, "before")
		old := c.Leader()
		buddy := ID("A")
		if old == buddy {
			buddy = "B"
		}
		var majority []ID
		for _, id := range tIDs5 {
			if id != old && id != buddy {
				majority = append(majority, id)
			}
		}
		c.Partition([]ID{old, buddy}, majority)

		staleIdx, ok := c.Node(old).Propose("stale")
		if !ok {
			t.Fatalf("seed %d: old leader refused a proposal while still believing it leads", seed)
		}
		if !c.RunUntil(400, func() bool { l := c.Leader(); return l != "" && l != old }) {
			t.Fatalf("seed %d: the 3-node side never elected a leader", seed)
		}
		c.Run(60)
		if c.Node(old).CommitIndex() >= staleIdx {
			t.Fatalf("seed %d: the isolated leader committed index %d with only 2 of 5 nodes: a minority must never commit", seed, staleIdx)
		}
		l2 := c.Leader()
		if _, _, ok := c.Propose("fresh"); !ok {
			t.Fatalf("seed %d: new leader refused a proposal", seed)
		}
		c.Run(60)
		c.Heal()
		if !c.RunUntil(400, func() bool { return tStable(c) && c.Node(old).CommitIndex() == c.Node(l2).CommitIndex() }) {
			t.Fatalf("seed %d: cluster did not converge after healing", seed)
		}
		got := tCmds(c.Node(old).Log(), len(c.Node(old).Log()))
		if !reflect.DeepEqual(got, []string{"before", "fresh"}) {
			t.Fatalf("seed %d: old leader's log commands = %v, want [before fresh]: its uncommitted \"stale\" entry must be overwritten, never committed", seed, got)
		}
		if c.Node(old).Role() != Follower {
			t.Fatalf("seed %d: old leader is still %v after hearing a higher term", seed, c.Node(old).Role())
		}
		tNoViolations(t, c, fmt.Sprintf("seed %d", seed))
	}
}

// This test is also the break-it target for skipping the vote's log check
// (go test -tags breakit -run StaleLog).
func TestCluster_StaleLogCandidateCannotWinElection(t *testing.T) {
	ids := []ID{"A", "B", "C"}
	fixed := map[ID]int{"A": 10, "C": 14, "B": 18}
	c := NewClusterWith(ids, 3, func(id ID) func() int { return func() int { return fixed[id] } })
	if !c.RunUntil(100, func() bool { return tStable(c) && c.Leader() == "A" }) {
		t.Fatalf("setup: A (shortest timeout) should lead; leader = %q", c.Leader())
	}
	c.Partition([]ID{"A", "B"}, []ID{"C"}) // C misses everything from here on
	idx, ok := c.Node("A").Propose("x")
	if !ok {
		t.Fatal("A refused a proposal")
	}
	if !c.RunUntil(100, func() bool { return c.Node("A").CommitIndex() >= idx && c.Node("B").CommitIndex() >= idx }) {
		t.Fatal("x was not committed by A and B (2 of 3)")
	}
	// A dies; the partition heals. C, whose log lacks x, times out first.
	c.Crash("A")
	c.Heal()
	if !c.RunUntil(400, func() bool { return c.Leader() != "" }) {
		t.Fatal("no leader after A crashed")
	}
	c.Run(30)
	tNoViolations(t, c, "stale-log candidate")
	for _, id := range []ID{"B", "C"} {
		if got := tCmds(c.Node(id).Log(), len(c.Node(id).Log())); !reflect.DeepEqual(got, []string{"x"}) {
			t.Fatalf("node %s log commands = %v, want [x]: the committed entry was lost", id, got)
		}
	}
	if l := c.Leader(); l != "B" {
		t.Fatalf("leader = %s, want B: C's log lacks the committed entry x, so B must refuse to vote for C and C must not become leader", l)
	}
}

func TestCluster_RandomFaultsKeepEverySafetyProperty(t *testing.T) {
	for seed := uint64(1); seed <= 12; seed++ {
		c := NewCluster(tIDs5, seed)
		r := rand.New(rand.NewPCG(seed, 99))
		n := 0
		for step := 0; step < 700; step++ {
			c.Tick()
			if step%5 == 0 {
				if _, _, ok := c.Propose(fmt.Sprintf("op-%d", n)); ok {
					n++
				}
			}
			if step%37 == 36 {
				switch r.IntN(4) {
				case 0:
					id := tIDs5[r.IntN(5)]
					down := 0
					for _, x := range tIDs5 {
						if !c.Up(x) {
							down++
						}
					}
					if down < 2 {
						c.Crash(id)
					}
				case 1:
					c.Restart(tIDs5[r.IntN(5)])
				case 2:
					perm := r.Perm(5)
					c.Partition([]ID{tIDs5[perm[0]], tIDs5[perm[1]]}, []ID{tIDs5[perm[2]], tIDs5[perm[3]], tIDs5[perm[4]]})
				case 3:
					c.Heal()
				}
			}
			if err := c.CheckLogMatching(); err != nil {
				t.Fatalf("seed %d tick %d: %v", seed, c.Ticks(), err)
			}
		}
		c.Heal()
		for _, id := range tIDs5 {
			c.Restart(id)
		}
		same := func() bool {
			if !tStable(c) {
				return false
			}
			l := c.Node(c.Leader())
			for _, id := range tIDs5 {
				if !reflect.DeepEqual(c.Node(id).Log(), l.Log()) || c.Node(id).CommitIndex() != l.CommitIndex() {
					return false
				}
			}
			return true
		}
		if !c.RunUntil(600, same) {
			t.Fatalf("seed %d: cluster never converged to identical logs after the faults stopped", seed)
		}
		tNoViolations(t, c, fmt.Sprintf("seed %d", seed))
		if len(c.Committed()) == 0 {
			t.Fatalf("seed %d: nothing was ever committed; the cluster made no progress", seed)
		}
	}
}
