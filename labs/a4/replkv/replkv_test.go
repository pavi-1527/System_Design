package replkv

import (
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

// ---------- helpers (prefixed with t to avoid clashing with your code) ----------

func tTicks(c *Cluster, n int) {
	for i := 0; i < n; i++ {
		c.Tick()
	}
}

func tMustPut(t *testing.T, c *Cluster, k, v string) PutResult {
	t.Helper()
	res, err := c.Put(k, v)
	if err != nil {
		t.Fatalf("Put(%s,%s): %v", k, v, err)
	}
	return res
}

func tGet(t *testing.T, c *Cluster, id NodeID, k string) ReadResult {
	t.Helper()
	r, err := c.Get(id, k)
	if err != nil {
		t.Fatalf("Get(%s,%s): %v", id, k, err)
	}
	return r
}

func tWithin(t *testing.T, d time.Duration, fn func()) {
	t.Helper()
	done := make(chan struct{})
	go func() { defer close(done); fn() }()
	select {
	case <-done:
	case <-time.After(d):
		t.Fatalf("timed out after %v (deadlock?)", d)
	}
}

// tPick returns a Pick function that returns the scripted node each call
// (repeating the last one when the script runs out).
func tPick(ids ...NodeID) func([]NodeID) NodeID {
	var mu sync.Mutex
	i := 0
	return func(followers []NodeID) NodeID {
		mu.Lock()
		defer mu.Unlock()
		id := ids[min(i, len(ids)-1)]
		i++
		return id
	}
}

// tLagged returns a cluster where n1 is fast (delay 1) and n2 is slow (delay
// slow ticks), the shape most anomalies need.
func tLagged(slow int, opts Options) *Cluster {
	c := NewCluster(opts)
	c.Network().SetDelay("n1", 1)
	c.Network().SetDelay("n2", slow)
	return c
}

// ---------- follower apply rule ----------

func TestNodeApply_InOrderOutOfOrderAndDuplicates(t *testing.T) {
	n := newNode("n1")
	e := func(lsn uint64, k, v string) Entry { return Entry{LSN: lsn, Key: k, Value: v} }
	n.apply(e(3, "c", "3"))
	n.apply(e(2, "b", "2"))
	if n.applied() != 0 || len(n.data) != 0 {
		t.Fatalf("applied %d entries before LSN 1 arrived (data=%v); a follower must apply in LSN order, otherwise it exposes a state the leader never had", n.applied(), n.data)
	}
	n.apply(e(1, "a", "1"))
	if n.applied() != 3 || n.data["a"] != "1" || n.data["b"] != "2" || n.data["c"] != "3" {
		t.Fatalf("after the gap filled: applied=%d data=%v; want buffered entries 2 and 3 applied too", n.applied(), n.data)
	}
	n.apply(e(2, "b", "STALE"))
	n.apply(e(3, "c", "STALE"))
	if n.data["b"] != "2" || n.data["c"] != "3" || n.applied() != 3 {
		t.Fatalf("a duplicate delivery changed state: applied=%d data=%v", n.applied(), n.data)
	}
	n.apply(Entry{LSN: 4, Key: "a", Delete: true})
	if _, ok := n.data["a"]; ok || n.applied() != 4 {
		t.Fatalf("delete entry not applied: applied=%d data=%v", n.applied(), n.data)
	}
	for i, en := range n.log {
		if en.LSN != uint64(i+1) {
			t.Fatalf("log[%d].LSN = %d; the log must hold LSNs 1..n with no gaps", i, en.LSN)
		}
	}
}

// ---------- leader write path and replication ----------

func TestPut_AssignsConsecutiveLSNsAndAppendsToLeaderLog(t *testing.T) {
	c := NewCluster(Options{})
	for i := 1; i <= 5; i++ {
		res := tMustPut(t, c, fmt.Sprintf("k%d", i), "v")
		if res.LSN != uint64(i) || res.Ticks != 0 {
			t.Fatalf("write %d: %+v, want LSN %d and 0 ticks (asynchronous)", i, res, i)
		}
	}
	log := c.Log("n0")
	if len(log) != 5 || log[4].Key != "k5" || log[4].LSN != 5 {
		t.Fatalf("leader log = %+v", log)
	}
	if got := tGet(t, c, "n0", "k3"); !got.Found || got.AppliedLSN != 5 {
		t.Fatalf("leader read = %+v; the leader applies its own writes immediately", got)
	}
	if c.Applied("n1") != 0 {
		t.Fatalf("follower applied %d before any Tick; Put must not deliver synchronously", c.Applied("n1"))
	}
}

func TestReplication_FollowersConvergeAfterFlush(t *testing.T) {
	c := NewCluster(Options{Followers: 3})
	c.Network().SetDelay("n2", 4)
	c.Network().SetDelay("n3", 9)
	tMustPut(t, c, "a", "1")
	tMustPut(t, c, "b", "2")
	if _, err := c.Delete("a"); err != nil {
		t.Fatal(err)
	}
	tMustPut(t, c, "c", "3")
	c.Flush()
	want := c.Snapshot("n0")
	if len(want) != 2 || want["b"] != "2" || want["c"] != "3" {
		t.Fatalf("leader state = %v", want)
	}
	for _, id := range []NodeID{"n1", "n2", "n3"} {
		got := c.Snapshot(id)
		if fmt.Sprint(got) != fmt.Sprint(want) || c.Applied(id) != 4 {
			t.Errorf("%s: applied=%d state=%v, want applied=4 state=%v", id, c.Applied(id), got, want)
		}
	}
}

func TestPut_ErrorsForNonLeaderUnknownAndDownLeader(t *testing.T) {
	c := NewCluster(Options{})
	if _, err := c.PutVia("n1", "k", "v"); !errors.Is(err, ErrNotLeader) {
		t.Errorf("write to a follower: %v, want ErrNotLeader", err)
	}
	if _, err := c.PutVia("zz", "k", "v"); !errors.Is(err, ErrUnknownNode) {
		t.Errorf("write to an unknown node: %v, want ErrUnknownNode", err)
	}
	if err := c.Crash("n0"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Put("k", "v"); !errors.Is(err, ErrNoLeader) {
		t.Errorf("write while the leader is down: %v, want ErrNoLeader", err)
	}
	if _, err := c.Get("n0", "k"); !errors.Is(err, ErrNodeDown) {
		t.Errorf("read from a down node: %v, want ErrNodeDown", err)
	}
	if _, err := c.Get("zz", "k"); !errors.Is(err, ErrUnknownNode) {
		t.Errorf("read from an unknown node: %v, want ErrUnknownNode", err)
	}
}

// ---------- the lag simulator: reproducible anomalies ----------

func TestLag_FollowerServesStaleReadUntilTheEntryArrives(t *testing.T) {
	c := tLagged(4, Options{})
	tMustPut(t, c, "balance", "100")
	c.Flush()
	tMustPut(t, c, "balance", "60") // transfer of 40 out
	if got := tGet(t, c, "n0", "balance").Value; got != "60" {
		t.Fatalf("leader shows %q, want 60", got)
	}
	tTicks(c, 1)
	if got := tGet(t, c, "n1", "balance").Value; got != "60" {
		t.Errorf("fast follower after 1 tick shows %q, want 60", got)
	}
	if got := tGet(t, c, "n2", "balance"); got.Value != "100" || got.AppliedLSN != 1 {
		t.Errorf("slow follower after 1 tick = %+v; want the OLD balance 100 at applied LSN 1: this is replication lag", got)
	}
	tTicks(c, 3)
	if got := tGet(t, c, "n2", "balance").Value; got != "60" {
		t.Errorf("slow follower after 4 ticks shows %q, want 60 (delay was 4)", got)
	}
}

func TestLag_LaterEntryOvertakesEarlierOneButIsNotAppliedFirst(t *testing.T) {
	c := NewCluster(Options{})
	c.Network().SetDelay("n1", 5)
	tMustPut(t, c, "x", "first") // LSN 1, due in 5 ticks
	c.Network().SetDelay("n1", 1)
	tMustPut(t, c, "y", "second") // LSN 2, due in 1 tick: arrives first
	tTicks(c, 1)
	if got := tGet(t, c, "n1", "y"); got.Found || got.AppliedLSN != 0 {
		t.Fatalf("follower exposed LSN 2 (%+v) before LSN 1 arrived: a prefix of the leader's history is required", got)
	}
	tTicks(c, 4)
	if c.Applied("n1") != 2 || tGet(t, c, "n1", "x").Value != "first" || tGet(t, c, "n1", "y").Value != "second" {
		t.Fatalf("after both arrived: applied=%d state=%v", c.Applied("n1"), c.Snapshot("n1"))
	}
}

func TestPartition_HoldsEntriesAndHealCatchesUp(t *testing.T) {
	c := NewCluster(Options{})
	c.Network().Partition("n2", true)
	for i := 1; i <= 5; i++ {
		tMustPut(t, c, fmt.Sprintf("k%d", i), "v")
	}
	c.Flush() // returns even though n2's link is cut
	if c.Applied("n1") != 5 {
		t.Fatalf("healthy follower applied %d, want 5", c.Applied("n1"))
	}
	if c.Applied("n2") != 0 {
		t.Fatalf("partitioned follower applied %d, want 0", c.Applied("n2"))
	}
	c.Network().Partition("n2", false)
	c.Flush()
	if c.Applied("n2") != 5 {
		t.Fatalf("after healing the partition n2 applied %d, want 5", c.Applied("n2"))
	}
}

// ---------- synchronous replication ----------

func TestSyncReplication_PutWaitsForTheFastestRequiredAcks(t *testing.T) {
	c := tLagged(6, Options{SyncAcks: 1})
	res, err := c.Put("k", "v")
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if res.Ticks != 1 {
		t.Errorf("waited %d ticks, want 1: one ack is enough and n1 answers in one tick", res.Ticks)
	}
	if c.Applied("n1") != 1 {
		t.Errorf("n1 applied %d, want 1 after an acknowledged synchronous write", c.Applied("n1"))
	}
	if c.Applied("n2") != 0 {
		t.Errorf("n2 applied %d; it is 6 ticks away and nobody should have waited for it", c.Applied("n2"))
	}

	c2 := tLagged(6, Options{SyncAcks: 2})
	res, err = c2.Put("k", "v")
	if err != nil || res.Ticks != 6 {
		t.Errorf("SyncAcks=2: %+v, %v; want to wait 6 ticks (the slowest of the two followers)", res, err)
	}
}

func TestSyncReplication_TimesOutButTheWriteIsStillInTheLeaderLog(t *testing.T) {
	c := NewCluster(Options{SyncAcks: 1, SyncTimeoutTicks: 5})
	c.Network().Partition("n1", true)
	c.Network().Partition("n2", true)
	res, err := c.Put("k", "v")
	if !errors.Is(err, ErrSyncTimeout) {
		t.Fatalf("err = %v, want ErrSyncTimeout: availability is sacrificed when followers are unreachable", err)
	}
	if res.LSN != 1 || res.Ticks != 5 {
		t.Errorf("result = %+v, want LSN 1 after waiting 5 ticks", res)
	}
	if got := tGet(t, c, "n0", "k"); !got.Found {
		t.Error("the unacknowledged write is not on the leader; it was applied locally and must stay so (the client simply was not told ok)")
	}
	c.Network().Partition("n1", false)
	c.Network().Partition("n2", false)
	c.Flush()
	if c.Applied("n1") != 1 || c.Applied("n2") != 1 {
		t.Errorf("after healing: n1=%d n2=%d, want 1 and 1: the entry replicates eventually", c.Applied("n1"), c.Applied("n2"))
	}
}

func TestSyncReplication_OneSlowFollowerDoesNotBlockWhenAnotherCanAck(t *testing.T) {
	c := NewCluster(Options{SyncAcks: 1, SyncTimeoutTicks: 5})
	c.Network().Partition("n2", true)
	if _, err := c.Put("k", "v"); err != nil {
		t.Fatalf("with n1 reachable and 1 ack required, Put failed: %v", err)
	}
}

// ---------- reads ----------

func TestGetAtLeast_RefusesALaggingReplica(t *testing.T) {
	c := tLagged(5, Options{})
	res := tMustPut(t, c, "k", "v")
	tTicks(c, 1)
	if _, err := c.GetAtLeast("n2", "k", res.LSN); !errors.Is(err, ErrStaleReplica) {
		t.Errorf("GetAtLeast on the lagging follower: %v, want ErrStaleReplica", err)
	}
	if r, err := c.GetAtLeast("n1", "k", res.LSN); err != nil || r.Value != "v" {
		t.Errorf("GetAtLeast on the caught-up follower: %+v, %v", r, err)
	}
	if r, err := c.GetAtLeast("n2", "k", 0); err != nil || r.Found {
		t.Errorf("minLSN 0 means no constraint: %+v, %v", r, err)
	}
}

// ---------- sessions ----------

func TestSession_WithoutGuaranteesYouMissYourOwnWrite(t *testing.T) {
	c := tLagged(5, Options{})
	s := c.NewSession(SessionOptions{Pick: tPick("n2")})
	if _, err := s.Put("balance", "60"); err != nil {
		t.Fatal(err)
	}
	r, err := s.Get("balance")
	if err != nil {
		t.Fatal(err)
	}
	if r.Found || r.Node != "n2" {
		t.Errorf("read = %+v; the anomaly should be reproducible: reading the lagging follower right after the write shows no balance", r)
	}
}

func TestSession_ReadYourWritesSkipsStaleFollowersThenFallsBackToLeader(t *testing.T) {
	c := tLagged(5, Options{})
	s := c.NewSession(SessionOptions{ReadYourWrites: true, Pick: tPick("n2", "n2", "n2")})
	res, _ := s.Put("balance", "60")
	// Tick 0: n1 and n2 both lag → leader.
	r, err := s.Get("balance")
	if err != nil || r.Node != "n0" || r.Value != "60" {
		t.Fatalf("both followers stale: read = %+v, %v; want a fallback to the leader n0 with the new value", r, err)
	}
	// After one tick n1 has the write; pick says n2 but n1 satisfies the floor.
	tTicks(c, 1)
	r, err = s.Get("balance")
	if err != nil || r.Node != "n1" || r.Value != "60" || r.AppliedLSN < res.LSN {
		t.Fatalf("n1 fresh, n2 stale: read = %+v, %v; want n1 (first follower from the pick that has applied LSN %d)", r, err, res.LSN)
	}
	// Once the picked follower is fresh it is used.
	tTicks(c, 4)
	r, _ = s.Get("balance")
	if r.Node != "n2" || r.Value != "60" {
		t.Fatalf("all fresh: read = %+v, want the picked follower n2", r)
	}
}

func TestSession_ReadYourWritesDoesNotRequireWritesByOthers(t *testing.T) {
	c := tLagged(5, Options{})
	other := c.NewSession(SessionOptions{})
	mine := c.NewSession(SessionOptions{ReadYourWrites: true, Pick: tPick("n2")})
	if _, err := other.Put("rate", "4.5"); err != nil { // someone else writes
		t.Fatal(err)
	}
	r, err := mine.Get("rate")
	if err != nil || r.Node != "n2" {
		t.Fatalf("a session that has written nothing has no floor: read = %+v, %v; want the picked follower (stale reads of other people's writes are allowed)", r, err)
	}
}

func TestSession_MonotonicReadsAnomalyWithoutTheGuarantee(t *testing.T) {
	c := tLagged(6, Options{})
	tMustPut(t, c, "balance", "100")
	c.Flush()
	tMustPut(t, c, "balance", "60")
	tTicks(c, 1) // n1 has 60, n2 still shows 100
	s := c.NewSession(SessionOptions{Pick: tPick("n1", "n2")})
	first, _ := s.Get("balance")
	second, _ := s.Get("balance")
	if first.Value != "60" || second.Value != "100" {
		t.Fatalf("reads = %q then %q; want 60 then 100: the member sees the transfer, refreshes and it vanishes", first.Value, second.Value)
	}
}

func TestSession_MonotonicReadsNeverGoBackwards(t *testing.T) {
	c := tLagged(6, Options{})
	tMustPut(t, c, "balance", "100")
	c.Flush()
	tMustPut(t, c, "balance", "60")
	tTicks(c, 1)
	s := c.NewSession(SessionOptions{MonotonicReads: true, Pick: tPick("n1", "n2", "n2")})
	first, _ := s.Get("balance")
	second, err := s.Get("balance")
	if err != nil {
		t.Fatal(err)
	}
	if second.AppliedLSN < first.AppliedLSN || second.Value != "60" {
		t.Fatalf("first=%+v second=%+v; the second read went back in time", first, second)
	}
	if second.Node != "n1" {
		t.Errorf("second read came from %s; n2 is behind the session's floor (LSN %d) so n1 should serve it", second.Node, first.AppliedLSN)
	}
}

func TestSession_GuaranteesAreIndependent(t *testing.T) {
	// Monotonic reads alone do not give read-your-writes.
	c := tLagged(5, Options{})
	mono := c.NewSession(SessionOptions{MonotonicReads: true, Pick: tPick("n2")})
	if _, err := mono.Put("k", "mine"); err != nil {
		t.Fatal(err)
	}
	if r, _ := mono.Get("k"); r.Found {
		t.Errorf("MonotonicReads alone returned %+v; it must not imply read-your-writes (its floor comes from reads only)", r)
	}
	// Read-your-writes alone does not give monotonic reads.
	c2 := tLagged(6, Options{})
	rw := c2.NewSession(SessionOptions{ReadYourWrites: true, Pick: tPick("n1", "n2")})
	tMustPut(t, c2, "balance", "100")
	c2.Flush()
	tMustPut(t, c2, "balance", "60")
	tTicks(c2, 1)
	a, _ := rw.Get("balance")
	b, _ := rw.Get("balance")
	if a.Value != "60" || b.Value != "100" {
		t.Errorf("ReadYourWrites alone: reads %q then %q; with no writes in the session it must still allow time to go backwards (60 then 100)", a.Value, b.Value)
	}
}

func TestSession_TokenLetsAnotherSessionContinueTheGuarantee(t *testing.T) {
	c := tLagged(5, Options{})
	phone := c.NewSession(SessionOptions{ReadYourWrites: true})
	res, _ := phone.Put("balance", "60")
	if phone.Token() != res.LSN {
		t.Fatalf("Token() = %d, want the session's last write LSN %d", phone.Token(), res.LSN)
	}
	web := c.NewSession(SessionOptions{ReadYourWrites: true, Token: phone.Token(), Pick: tPick("n2")})
	r, err := web.Get("balance")
	if err != nil || r.Value != "60" || r.Node == "n2" {
		t.Fatalf("second device with the token: %+v, %v; want the new balance from a replica that has applied LSN %d (not stale n2)", r, err, res.LSN)
	}
	if web.Token() < res.LSN {
		t.Errorf("web.Token() = %d, want >= %d", web.Token(), res.LSN)
	}
}

func TestSession_ReadsFromLeaderWhenThereAreNoFollowers(t *testing.T) {
	c := NewCluster(Options{Followers: 1})
	s := c.NewSession(SessionOptions{ReadYourWrites: true})
	if _, err := s.Put("k", "v"); err != nil {
		t.Fatal(err)
	}
	if err := c.Crash("n1"); err != nil {
		t.Fatal(err)
	}
	r, err := s.Get("k")
	if err != nil || r.Node != "n0" || r.Value != "v" {
		t.Errorf("only the leader is alive: %+v, %v", r, err)
	}
}

func TestSession_NoFreshReplicaWhenLeaderIsDownAndFollowersLag(t *testing.T) {
	c := tLagged(5, Options{})
	s := c.NewSession(SessionOptions{ReadYourWrites: true})
	if _, err := s.Put("k", "v"); err != nil {
		t.Fatal(err)
	}
	if err := c.Crash("n0"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get("k"); !errors.Is(err, ErrNoFreshReplica) {
		t.Errorf("err = %v, want ErrNoFreshReplica: serving a stale read would break the guarantee, so fail instead", err)
	}
}

func TestSession_DefaultPickRoundRobinsAcrossFollowers(t *testing.T) {
	c := tLagged(50, Options{})
	tMustPut(t, c, "k", "v")
	tTicks(c, 1)
	s := c.NewSession(SessionOptions{})
	var got []NodeID
	for i := 0; i < 4; i++ {
		r, err := s.Get("k")
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, r.Node)
	}
	if fmt.Sprint(got) != "[n1 n2 n1 n2]" {
		t.Errorf("default routing = %v, want round robin over live followers [n1 n2 n1 n2]", got)
	}
}

// ---------- failover ----------

func TestFailover_PromotesTheMostUpToDateFollowerAndReportsLostWrites(t *testing.T) {
	c := tLagged(8, Options{})
	for i := 1; i <= 6; i++ {
		tMustPut(t, c, fmt.Sprintf("k%d", i), fmt.Sprintf("v%d", i))
	}
	tTicks(c, 1) // n1 has all 6? delay 1 → yes; n2 has none
	// Make n1 fall behind too: its last entries are still in flight.
	c.Network().SetDelay("n1", 5)
	tMustPut(t, c, "k7", "v7")
	tMustPut(t, c, "k8", "v8")
	if c.Applied("n1") != 6 || c.Applied("n2") != 0 {
		t.Fatalf("setup: n1=%d n2=%d, want 6 and 0", c.Applied("n1"), c.Applied("n2"))
	}
	if err := c.Crash("n0"); err != nil {
		t.Fatal(err)
	}
	rep, err := c.Failover()
	if err != nil {
		t.Fatal(err)
	}
	if rep.OldLeader != "n0" || rep.NewLeader != "n1" || rep.NewLeaderLSN != 6 {
		t.Fatalf("report = %+v; want n1 promoted at LSN 6 (it is the most up to date, n2 has applied nothing)", rep)
	}
	if len(rep.LostWrites) != 2 || rep.LostWrites[0].Key != "k7" || rep.LostWrites[1].Key != "k8" || rep.LostWrites[1].LSN != 8 {
		t.Fatalf("lost writes = %+v; want k7 (LSN 7) and k8 (LSN 8): acknowledged by the old leader, present nowhere else", rep.LostWrites)
	}
	if c.Leader() != "n1" {
		t.Errorf("Leader() = %s, want n1", c.Leader())
	}
	if got := tGet(t, c, "n1", "k7"); got.Found {
		t.Error("lost write k7 is visible on the new leader")
	}
	// The remaining follower catches up from the new leader.
	c.Flush()
	if c.Applied("n2") != 6 || fmt.Sprint(c.Snapshot("n2")) != fmt.Sprint(c.Snapshot("n1")) {
		t.Errorf("n2 applied=%d state=%v; want it caught up to the new leader's 6 entries", c.Applied("n2"), c.Snapshot("n2"))
	}
	// New writes continue the log of the new leader.
	if res := tMustPut(t, c, "k9", "v9"); res.LSN != 7 {
		t.Errorf("first write after failover got LSN %d, want 7 (the new leader's applied LSN + 1)", res.LSN)
	}
}

func TestFailover_PromotesTheFurtherAheadFollowerEvenWhenItIsNotTheFirst(t *testing.T) {
	c := NewCluster(Options{})
	c.Network().SetDelay("n1", 8) // n1 is the laggard this time
	c.Network().SetDelay("n2", 1)
	for i := 1; i <= 4; i++ {
		tMustPut(t, c, fmt.Sprintf("k%d", i), "v")
	}
	tTicks(c, 1)
	_ = c.Crash("n0")
	rep, err := c.Failover()
	if err != nil || rep.NewLeader != "n2" || rep.NewLeaderLSN != 4 || len(rep.LostWrites) != 0 {
		t.Errorf("report = %+v, %v; want n2 (applied 4) promoted, not n1 (applied 0), with nothing lost", rep, err)
	}
	c.Flush()
	if c.Applied("n1") != 4 {
		t.Errorf("laggard n1 applied %d after failover and flush, want 4", c.Applied("n1"))
	}
}

func TestFailover_TieGoesToTheEarliestNode(t *testing.T) {
	c := NewCluster(Options{Followers: 3})
	tMustPut(t, c, "k", "v")
	c.Flush()
	if err := c.Crash("n0"); err != nil {
		t.Fatal(err)
	}
	rep, err := c.Failover()
	if err != nil || rep.NewLeader != "n1" || len(rep.LostWrites) != 0 {
		t.Errorf("report = %+v, %v; want n1 and no lost writes (everyone was caught up)", rep, err)
	}
}

func TestFailover_SkipsCrashedFollowersEvenIfTheyAreAhead(t *testing.T) {
	c := tLagged(3, Options{})
	tMustPut(t, c, "k1", "v")
	tMustPut(t, c, "k2", "v")
	tTicks(c, 1) // n1 has 2, n2 has 0
	if err := c.Crash("n1"); err != nil {
		t.Fatal(err)
	}
	if err := c.Crash("n0"); err != nil {
		t.Fatal(err)
	}
	rep, err := c.Failover()
	if err != nil || rep.NewLeader != "n2" || rep.NewLeaderLSN != 0 || len(rep.LostWrites) != 2 {
		t.Errorf("report = %+v, %v; want n2 (the only live follower) promoted at LSN 0, losing both writes", rep, err)
	}
}

func TestFailover_NoLiveFollowerIsAnError(t *testing.T) {
	c := NewCluster(Options{Followers: 1})
	_ = c.Crash("n1")
	_ = c.Crash("n0")
	if _, err := c.Failover(); !errors.Is(err, ErrNoCandidate) {
		t.Errorf("err = %v, want ErrNoCandidate", err)
	}
	if c.Leader() != "n0" {
		t.Errorf("leader changed to %s despite the failed failover", c.Leader())
	}
}

func TestFailover_SynchronousAckedWritesSurviveAsyncOnesMayNot(t *testing.T) {
	run := func(syncAcks int) (lost int, acked int) {
		c := tLagged(7, Options{SyncAcks: syncAcks})
		for i := 0; i < 20; i++ {
			if _, err := c.Put(fmt.Sprintf("k%d", i), "v"); err == nil {
				acked++
			}
		}
		_ = c.Crash("n0")
		rep, err := c.Failover()
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range rep.LostWrites {
			if e.LSN > 0 {
				lost++
			}
		}
		return lost, acked
	}
	lostAsync, ackedAsync := run(0)
	if ackedAsync != 20 || lostAsync == 0 {
		t.Errorf("async: acked=%d lost=%d; with followers lagging, an immediate leader crash must lose acknowledged writes", ackedAsync, lostAsync)
	}
	lostSync, ackedSync := run(1)
	if ackedSync != 20 || lostSync != 0 {
		t.Errorf("sync (1 ack): acked=%d lost=%d; every acknowledged write is on a follower, so failover to the most up-to-date one loses nothing", ackedSync, lostSync)
	}
}

func TestFailover_DropsInFlightEntriesFromTheOldLeader(t *testing.T) {
	c := NewCluster(Options{})
	c.Network().SetDelay("n1", 1)
	c.Network().SetDelay("n2", 6)
	tMustPut(t, c, "a", "old-leader-1") // LSN 1
	tTicks(c, 1)                        // n1 applied 1; n2's copy is in flight
	_ = c.Crash("n0")
	if _, err := c.Failover(); err != nil { // n1 becomes leader at LSN 1
		t.Fatal(err)
	}
	tMustPut(t, c, "b", "new-leader-2") // LSN 2 on the new leader
	c.Flush()
	if c.Leader() != "n1" {
		t.Fatalf("leader = %s", c.Leader())
	}
	for _, id := range []NodeID{"n1", "n2"} {
		if got := c.Snapshot(id); got["a"] != "old-leader-1" || got["b"] != "new-leader-2" || c.Applied(id) != 2 {
			t.Errorf("%s: applied=%d state=%v; want a and b from the single history of the new leader", id, c.Applied(id), got)
		}
	}
}

func TestFailover_DiscardsBufferedOutOfOrderEntries(t *testing.T) {
	c := NewCluster(Options{})
	c.Network().SetDelay("n1", 1)
	c.Network().SetDelay("n2", 9)
	tMustPut(t, c, "a", "1") // LSN 1: n1 gets it next tick, n2's copy is slow
	tTicks(c, 1)
	c.Network().Partition("n1", true)
	c.Network().SetDelay("n2", 1)
	tMustPut(t, c, "b", "LOST-2") // reach n2 only; n2 buffers them (LSN 1 has not arrived)
	tMustPut(t, c, "c", "LOST-3")
	tTicks(c, 1)
	if c.Applied("n1") != 1 || c.Applied("n2") != 0 {
		t.Fatalf("setup: n1=%d n2=%d, want 1 and 0 (n2 holds LSN 2 and 3 in its reorder buffer)", c.Applied("n1"), c.Applied("n2"))
	}
	c.Network().Partition("n1", false)
	_ = c.Crash("n0")
	rep, err := c.Failover()
	if err != nil || rep.NewLeader != "n1" || len(rep.LostWrites) != 2 {
		t.Fatalf("report = %+v, %v; want n1 promoted, LSN 2 and 3 lost", rep, err)
	}
	tMustPut(t, c, "b", "NEW-2") // the new leader reuses LSN 2 for different content
	c.Flush()
	got := c.Snapshot("n2")
	if got["a"] != "1" || got["b"] != "NEW-2" || got["c"] != "" || c.Applied("n2") != 2 {
		t.Errorf("n2: applied=%d state=%v; entries buffered from the old leader (LSN 2 'LOST-2', LSN 3) must be discarded at failover, not merged into the new leader's history", c.Applied("n2"), got)
	}
}

func TestFailover_DeposedLeaderIsFencedAndRejoinsWithoutItsDivergentWrites(t *testing.T) {
	c := tLagged(3, Options{})
	tMustPut(t, c, "a", "1")
	c.Flush()
	c.Network().Partition("n1", true)
	c.Network().Partition("n2", true)
	tMustPut(t, c, "b", "UNREPLICATED") // only on n0
	_ = c.Crash("n0")
	c.Network().Partition("n1", false)
	c.Network().Partition("n2", false)
	rep, err := c.Failover()
	if err != nil || len(rep.LostWrites) != 1 || rep.LostWrites[0].Value != "UNREPLICATED" {
		t.Fatalf("report = %+v, %v; want exactly the unreplicated write reported lost", rep, err)
	}
	if _, err := c.PutVia("n0", "x", "y"); !errors.Is(err, ErrNotLeader) {
		t.Errorf("write to the deposed leader: %v, want ErrNotLeader (fenced)", err)
	}
	if _, err := c.Get("n0", "b"); !errors.Is(err, ErrNodeDown) {
		t.Errorf("read from the deposed leader: %v, want ErrNodeDown: it holds writes the cluster has abandoned and must not serve them", err)
	}
	tMustPut(t, c, "b", "NEW") // new leader reuses LSN 2 with different content
	if err := c.Recover("n0"); err != nil {
		t.Fatal(err)
	}
	c.Flush()
	got := c.Snapshot("n0")
	if got["b"] != "NEW" || got["a"] != "1" || c.Applied("n0") != 2 {
		t.Errorf("rejoined old leader: applied=%d state=%v; its divergent LSN 2 must be truncated and replaced by the new leader's", c.Applied("n0"), got)
	}
	if fmt.Sprint(c.Log("n0")) != fmt.Sprint(c.Log(c.Leader())) {
		t.Errorf("logs differ after rejoin: %v vs %v", c.Log("n0"), c.Log(c.Leader()))
	}
}

func TestFailover_FencesAnOldLeaderThatIsStillAlive(t *testing.T) {
	// A false suspicion: the operator promotes n1 although n0 is healthy. The
	// old leader must be taken out of service, or two nodes would accept writes.
	c := NewCluster(Options{})
	tMustPut(t, c, "a", "1")
	c.Flush()
	if _, err := c.Failover(); err != nil {
		t.Fatal(err)
	}
	if _, err := c.PutVia("n0", "a", "split-brain"); !errors.Is(err, ErrNotLeader) {
		t.Errorf("write to the old leader after failover: %v, want ErrNotLeader", err)
	}
	if _, err := c.Get("n0", "a"); !errors.Is(err, ErrNodeDown) {
		t.Errorf("read from the old leader after failover: %v, want ErrNodeDown (fenced)", err)
	}
	tMustPut(t, c, "b", "2")
	c.Flush()
	if c.Applied("n0") != 1 {
		t.Errorf("the fenced node applied %d, want it frozen at 1", c.Applied("n0"))
	}
}

func TestRecover_FollowerCatchesUpAfterCrash(t *testing.T) {
	c := NewCluster(Options{})
	tMustPut(t, c, "a", "1")
	c.Flush()
	_ = c.Crash("n2")
	tMustPut(t, c, "b", "2")
	tMustPut(t, c, "c", "3")
	c.Flush() // deliveries to the crashed node are discarded
	if c.Applied("n2") != 1 {
		t.Fatalf("crashed node applied %d, want it frozen at 1", c.Applied("n2"))
	}
	if err := c.Recover("n2"); err != nil {
		t.Fatal(err)
	}
	c.Flush()
	if c.Applied("n2") != 3 || c.Snapshot("n2")["c"] != "3" {
		t.Errorf("after recover: applied=%d state=%v, want 3 and c=3", c.Applied("n2"), c.Snapshot("n2"))
	}
}

func TestRecover_CrashedLeaderWithoutFailoverResumesLeading(t *testing.T) {
	c := NewCluster(Options{})
	tMustPut(t, c, "a", "1")
	_ = c.Crash("n0")
	if err := c.Recover("n0"); err != nil {
		t.Fatal(err)
	}
	if res := tMustPut(t, c, "b", "2"); res.LSN != 2 {
		t.Errorf("LSN after the leader restarted = %d, want 2 (its log is durable)", res.LSN)
	}
}

// ---------- concurrency (run with -race) ----------

func TestCluster_ConcurrentWritersGetUniqueConsecutiveLSNs(t *testing.T) {
	c := NewCluster(Options{Followers: 2})
	c.Network().SetDelay("n2", 3)
	const writers, each = 8, 60
	lsns := make(chan uint64, writers*each)
	tWithin(t, 20*time.Second, func() {
		var wg sync.WaitGroup
		for w := 0; w < writers; w++ {
			wg.Add(1)
			go func(w int) {
				defer wg.Done()
				for i := 0; i < each; i++ {
					res, err := c.Put(fmt.Sprintf("w%d-k%d", w, i), fmt.Sprint(i))
					if err != nil {
						t.Errorf("Put: %v", err)
						return
					}
					lsns <- res.LSN
					if i%10 == 0 {
						c.Tick()
					}
				}
			}(w)
		}
		wg.Wait()
	})
	close(lsns)
	seen := map[uint64]bool{}
	for l := range lsns {
		if seen[l] {
			t.Fatalf("LSN %d assigned twice", l)
		}
		seen[l] = true
	}
	if len(seen) != writers*each {
		t.Fatalf("%d distinct LSNs, want %d", len(seen), writers*each)
	}
	c.Flush()
	for _, id := range []NodeID{"n0", "n1", "n2"} {
		if c.Applied(id) != writers*each {
			t.Errorf("%s applied %d, want %d", id, c.Applied(id), writers*each)
		}
		for i, e := range c.Log(id) {
			if e.LSN != uint64(i+1) {
				t.Fatalf("%s log has LSN %d at index %d", id, e.LSN, i)
			}
		}
	}
	if fmt.Sprint(c.Snapshot("n1")) != fmt.Sprint(c.Snapshot("n0")) || fmt.Sprint(c.Snapshot("n2")) != fmt.Sprint(c.Snapshot("n0")) {
		t.Error("followers did not converge to the leader's state")
	}
}

func TestSession_ConcurrentReadYourWritesHoldsWhileReplicationRuns(t *testing.T) {
	c := NewCluster(Options{Followers: 2})
	c.Network().SetDelay("n1", 2)
	c.Network().SetDelay("n2", 5)
	stop := make(chan struct{})
	var ticker sync.WaitGroup
	ticker.Add(1)
	go func() {
		defer ticker.Done()
		for {
			select {
			case <-stop:
				return
			default:
				c.Tick()
				time.Sleep(50 * time.Microsecond)
			}
		}
	}()
	tWithin(t, 30*time.Second, func() {
		var wg sync.WaitGroup
		for g := 0; g < 6; g++ {
			wg.Add(1)
			go func(g int) {
				defer wg.Done()
				s := c.NewSession(SessionOptions{ReadYourWrites: true, MonotonicReads: true})
				key := fmt.Sprintf("acct-%d", g)
				var last uint64
				for i := 1; i <= 40; i++ {
					want := fmt.Sprint(i)
					if _, err := s.Put(key, want); err != nil {
						t.Errorf("Put: %v", err)
						return
					}
					r, err := s.Get(key)
					if err != nil || r.Value != want {
						t.Errorf("goroutine %d: after writing %s read %+v, %v; read-your-writes violated", g, want, r, err)
						return
					}
					if r.AppliedLSN < last {
						t.Errorf("goroutine %d: applied LSN went back from %d to %d", g, last, r.AppliedLSN)
						return
					}
					last = r.AppliedLSN
				}
			}(g)
		}
		wg.Wait()
	})
	close(stop)
	ticker.Wait()
}
