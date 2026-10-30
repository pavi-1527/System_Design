package quorum

import (
	"errors"
	"fmt"
	"reflect"
	"sort"
	"sync"
	"testing"
	"time"
)

// ---------- helpers (prefixed with t to avoid clashing with your code) ----------

// tWait waits for wg, failing the test instead of hanging if it takes longer
// than d (e.g. a deadlock in the implementation under test).
func tWait(t *testing.T, wg *sync.WaitGroup, d time.Duration, what string) {
	t.Helper()
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(d):
		t.Fatalf("%s: still running after %v; a lock is probably never released", what, d)
	}
}

func tPlaceZero(string) int { return 0 }

// tCluster builds a cluster whose preference list for every key is the first
// N ids, in the order given.
func tCluster(t *testing.T, cfg Config, ids ...string) (*Cluster, *Sim) {
	t.Helper()
	cfg.Place = tPlaceZero
	sim := NewSim()
	c, err := NewCluster(cfg, ids, sim)
	if err != nil {
		t.Fatalf("NewCluster(%+v, %v) failed: %v", cfg, ids, err)
	}
	return c, sim
}

func tABC() []string { return []string{"A", "B", "C"} }

// tOrder makes the given replicas answer reads in the given order (first = fastest).
func tOrder(sim *Sim, ids ...string) {
	for i, id := range ids {
		sim.SetLatency(id, time.Duration(i+1)*time.Millisecond)
	}
}

func tPut(t *testing.T, c *Cluster, client, key, value string, ctx VClock) VClock {
	t.Helper()
	out, err := c.Put(client, key, value, ctx)
	if err != nil {
		t.Fatalf("Put(%s, %s, %s) failed: %v", client, key, value, err)
	}
	return out
}

func tGet(t *testing.T, c *Cluster, key string) Result {
	t.Helper()
	res, err := c.Get(key)
	if err != nil {
		t.Fatalf("Get(%s) failed: %v", key, err)
	}
	return res
}

func tVals(c *Cluster, id, key string) []string {
	var out []string
	for _, v := range c.Node(id).Versions(key) {
		out = append(out, v.Value)
	}
	sort.Strings(out)
	return out
}

func tSorted(ss []string) []string {
	out := append([]string(nil), ss...)
	sort.Strings(out)
	return out
}

// ---------- configuration and placement ----------

func TestNewCluster_ValidatesConfiguration(t *testing.T) {
	ids := []string{"A", "B", "C"}
	bad := []struct {
		name string
		cfg  Config
		ids  []string
	}{
		{"N is zero", Config{N: 0, R: 1, W: 1}, ids},
		{"R is zero", Config{N: 3, R: 0, W: 2}, ids},
		{"W is zero", Config{N: 3, R: 2, W: 0}, ids},
		{"R exceeds N", Config{N: 3, R: 4, W: 1}, ids},
		{"W exceeds N", Config{N: 3, R: 1, W: 4}, ids},
		{"fewer nodes than N", Config{N: 3, R: 2, W: 2}, []string{"A", "B"}},
		{"duplicate node id", Config{N: 2, R: 1, W: 2}, []string{"A", "A", "B"}},
		{"empty node id", Config{N: 2, R: 1, W: 2}, []string{"A", "", "B"}},
	}
	for _, b := range bad {
		c, err := NewCluster(b.cfg, b.ids, nil)
		if !errors.Is(err, ErrConfig) || c != nil {
			t.Errorf("%s: NewCluster = (%v, %v), want (nil, error wrapping ErrConfig)", b.name, c, err)
		}
	}
	if c, err := NewCluster(Config{N: 3, R: 2, W: 2}, ids, nil); err != nil || c == nil {
		t.Fatalf("valid configuration rejected: %v (a nil Sim must mean a fresh healthy one)", err)
	}
}

func TestPreferenceList_IsNConsecutiveRingNodesAndFallbacksTheRest(t *testing.T) {
	ids := []string{"A", "B", "C", "D", "E"}
	place := 3
	c, err := NewCluster(Config{N: 3, R: 2, W: 2, Place: func(string) int { return place }}, ids, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := c.PreferenceList("k"), []string{"D", "E", "A"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("PreferenceList = %v, want %v (start at ring position 3 and wrap)", got, want)
	}
	if got, want := c.Fallbacks("k"), []string{"B", "C"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Fallbacks = %v, want %v (the rest of the ring, starting right after the preference list)", got, want)
	}
	place = -1
	if got, want := c.PreferenceList("k"), []string{"E", "A", "B"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("PreferenceList with a negative position = %v, want %v", got, want)
	}
	place = 12
	if got, want := c.PreferenceList("k"), []string{"C", "D", "E"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("PreferenceList with position 12 on 5 nodes = %v, want %v", got, want)
	}
}

func TestPreferenceList_DefaultPlacementIsDeterministicAndSpread(t *testing.T) {
	ids := []string{"A", "B", "C", "D", "E"}
	c, err := NewCluster(Config{N: 3, R: 2, W: 2}, ids, nil)
	if err != nil {
		t.Fatal(err)
	}
	starts := map[string]int{}
	for i := 0; i < 200; i++ {
		key := fmt.Sprintf("account-%d", i)
		pl := c.PreferenceList(key)
		if again := c.PreferenceList(key); !reflect.DeepEqual(pl, again) {
			t.Fatalf("PreferenceList(%q) changed between calls: %v vs %v", key, pl, again)
		}
		seen := map[string]bool{}
		for _, id := range pl {
			seen[id] = true
		}
		if len(pl) != 3 || len(seen) != 3 {
			t.Fatalf("PreferenceList(%q) = %v, want 3 distinct nodes", key, pl)
		}
		starts[pl[0]]++
	}
	if len(starts) != 5 {
		t.Fatalf("200 keys started on only %d distinct nodes (%v); the default placement should spread keys over the ring", len(starts), starts)
	}
}

// ---------- the basics ----------

func TestPutGet_RoundTripAndOverwriteWithContext(t *testing.T) {
	c, _ := tCluster(t, Config{N: 3, R: 2, W: 2}, tABC()...)
	if res := tGet(t, c, "k"); len(res.Versions) != 0 || res.Context != nil {
		t.Fatalf("Get of an unknown key = %+v, want no versions and a nil context (not an error)", res)
	}
	ctx1 := tPut(t, c, "c1", "k", "v1", nil)
	if !reflect.DeepEqual(ctx1, VClock{"c1": 1}) {
		t.Fatalf("Put returned context %v, want {c1:1}", ctx1)
	}
	res := tGet(t, c, "k")
	if !reflect.DeepEqual(res.Values(), []string{"v1"}) || !reflect.DeepEqual(res.Context, VClock{"c1": 1}) {
		t.Fatalf("Get = %v / %v, want [v1] / {c1:1}", res.Values(), res.Context)
	}
	tPut(t, c, "c1", "k", "v2", res.Context)
	res = tGet(t, c, "k")
	if !reflect.DeepEqual(res.Values(), []string{"v2"}) || !reflect.DeepEqual(res.Context, VClock{"c1": 2}) {
		t.Fatalf("after overwriting with the context from Get: %v / %v, want [v2] / {c1:2}: a write made after a read supersedes it", res.Values(), res.Context)
	}
	for _, id := range tABC() {
		if got := tVals(c, id, "k"); !reflect.DeepEqual(got, []string{"v2"}) {
			t.Errorf("replica %s holds %v, want [v2]: with every replica healthy a write reaches all of them", id, got)
		}
	}
}

func TestConcurrentWritesFromDifferentClientsBecomeSiblings(t *testing.T) {
	c, _ := tCluster(t, Config{N: 3, R: 2, W: 2}, tABC()...)
	base := tPut(t, c, "c0", "profile", "v0", nil)
	tPut(t, c, "alice", "profile", "email=a@new", base) // both read v0, neither saw the other
	tPut(t, c, "bob", "profile", "phone=555", base)
	res := tGet(t, c, "profile")
	if got, want := tSorted(res.Values()), []string{"email=a@new", "phone=555"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Get = %v, want both concurrent writes as siblings %v: dropping one is silent data loss", got, want)
	}
	if !res.Context.Descends(VClock{"c0": 1, "alice": 1, "bob": 1}) {
		t.Fatalf("context %v must cover both siblings so that a resolving write supersedes them", res.Context)
	}
	tPut(t, c, "carol", "profile", "email=a@new phone=555", res.Context)
	res = tGet(t, c, "profile")
	if !reflect.DeepEqual(res.Values(), []string{"email=a@new phone=555"}) {
		t.Fatalf("after the application merged the siblings Get = %v, want just the merge", res.Values())
	}
}

// ---------- R + W > N ----------

func TestQuorumOverlap_ReadsSeeTheLatestAcknowledgedWrite(t *testing.T) {
	// N=3, R=2, W=2: C lags, so the write is acknowledged by A and B only.
	// Whichever single replica is unreachable at read time, every read set of
	// size 2 contains A or B.
	for _, down := range tABC() {
		c, sim := tCluster(t, Config{N: 3, R: 2, W: 2}, tABC()...)
		ctx := tPut(t, c, "c1", "k", "old", nil)
		sim.SetLag("C", true)
		tPut(t, c, "c1", "k", "new", ctx)
		sim.SetDown(down, true)
		tOrder(sim, "C", "B", "A") // the stale replica answers first when it can
		res := tGet(t, c, "k")
		if !reflect.DeepEqual(res.Values(), []string{"new"}) {
			t.Errorf("with %s down the read returned %v, want [new]: R+W>N means the read set overlaps the write set", down, res.Values())
		}
	}
}

func TestOverlapRule_ExhaustiveForThreeReplicas(t *testing.T) {
	// For every R and W in 1..3, every set S of replicas that applied a
	// successful write synchronously (|S| >= W) and every read set T of size
	// R, check whether the read can return the stale value. The rule says a
	// stale read is possible exactly when R + W <= N.
	ids := tABC()
	subsets := func(size int) [][]string {
		var out [][]string
		for mask := 0; mask < 8; mask++ {
			var s []string
			for i, id := range ids {
				if mask&(1<<i) != 0 {
					s = append(s, id)
				}
			}
			if len(s) == size {
				out = append(out, s)
			}
		}
		return out
	}
	contains := func(s []string, id string) bool {
		for _, x := range s {
			if x == id {
				return true
			}
		}
		return false
	}
	for r := 1; r <= 3; r++ {
		for w := 1; w <= 3; w++ {
			staleSeen := false
			for size := 0; size <= 3; size++ {
				for _, s := range subsets(size) {
					c, sim := tCluster(t, Config{N: 3, R: r, W: w}, ids...)
					ctx := tPut(t, c, "c1", "k", "v1", nil)
					for _, id := range ids {
						sim.SetLag(id, !contains(s, id))
					}
					_, err := c.Put("c1", "k", "v2", ctx)
					if size < w {
						if !errors.Is(err, ErrQuorumNotMet) {
							t.Fatalf("R=%d W=%d: a write that only %v applied synchronously returned %v, want ErrQuorumNotMet: queued writes are not acknowledgements", r, w, s, err)
						}
						continue // rejected writes are not part of the overlap argument
					}
					if err != nil {
						t.Fatalf("R=%d W=%d: write acknowledged by %v failed: %v", r, w, s, err)
					}
					sim.Heal()
					for _, rs := range subsets(r) {
						for _, id := range ids {
							sim.SetDown(id, !contains(rs, id))
						}
						res, err := c.Get("k")
						if err != nil {
							t.Fatalf("R=%d W=%d: read from %v failed: %v", r, w, rs, err)
						}
						switch got := res.Values(); {
						case reflect.DeepEqual(got, []string{"v2"}):
						case reflect.DeepEqual(got, []string{"v1"}):
							staleSeen = true
						default:
							t.Fatalf("R=%d W=%d: write set %v, read set %v returned %v, want exactly [v1] or [v2]", r, w, s, rs, got)
						}
					}
				}
			}
			if want := r+w <= 3; staleSeen != want {
				t.Errorf("N=3 R=%d W=%d: stale read possible = %v, want %v (R+W>N must guarantee overlap; R+W<=N must allow a miss)", r, w, staleSeen, want)
			}
		}
	}
}

func TestQuorumWithRPlusWAtMostN_CanReadStaleUntilReplicationCatchesUp(t *testing.T) {
	c, sim := tCluster(t, Config{N: 3, R: 1, W: 1}, tABC()...)
	ctx := tPut(t, c, "c1", "k", "v1", nil)
	sim.SetLag("B", true)
	sim.SetLag("C", true)
	tPut(t, c, "c1", "k", "v2", ctx) // acknowledged by A alone
	tOrder(sim, "B", "C", "A")
	if got := tGet(t, c, "k").Values(); !reflect.DeepEqual(got, []string{"v1"}) {
		t.Fatalf("read from a lagging replica returned %v, want the stale [v1]: with R=1, W=1 nothing forces the read to meet the write", got)
	}
	if n := c.Flush(); n != 2 {
		t.Fatalf("Flush applied %d queued writes, want 2", n)
	}
	if got := tGet(t, c, "k").Values(); !reflect.DeepEqual(got, []string{"v2"}) {
		t.Fatalf("after replication caught up the read returned %v, want [v2] (eventual consistency)", got)
	}
}

// ---------- what a quorum does not promise ----------

func TestFailedWrite_IsNotRolledBack(t *testing.T) {
	c, sim := tCluster(t, Config{N: 3, R: 1, W: 3}, tABC()...)
	ctx := tPut(t, c, "c1", "k", "v1", nil)
	sim.SetDown("C", true)
	_, err := c.Put("c1", "k", "v2", ctx)
	if !errors.Is(err, ErrQuorumNotMet) {
		t.Fatalf("Put with W=3 and one replica down returned %v, want ErrQuorumNotMet", err)
	}
	for _, id := range []string{"A", "B"} {
		if got := tVals(c, id, "k"); !reflect.DeepEqual(got, []string{"v2"}) {
			t.Fatalf("replica %s holds %v, want [v2]: a failed quorum write leaves its effects on the replicas that took it (no rollback)", id, got)
		}
	}
	tOrder(sim, "A", "B")
	if got := tGet(t, c, "k").Values(); !reflect.DeepEqual(got, []string{"v2"}) {
		t.Fatalf("a read after the failed write returned %v, want [v2]: the caller was told the write failed, yet readers can see it", got)
	}
}

func TestQuorumIsNotLinearizable_ReadsCanGoBackInTime(t *testing.T) {
	// R+W>N, yet: a write that is only partly applied (it reached A, and will
	// reach B and C later) is visible to some readers and not to others.
	c, sim := tCluster(t, Config{N: 3, R: 2, W: 2}, tABC()...)
	ctx := tPut(t, c, "c1", "balance", "100", nil)
	sim.SetLag("B", true)
	sim.SetLag("C", true)
	if _, err := c.Put("c1", "balance", "250", ctx); !errors.Is(err, ErrQuorumNotMet) {
		t.Fatalf("Put reached one replica of W=2; got %v, want ErrQuorumNotMet", err)
	}
	tOrder(sim, "A", "B", "C")
	first := tGet(t, c, "balance").Values()
	tOrder(sim, "C", "B", "A") // a different pair of replicas answers the next read
	second := tGet(t, c, "balance").Values()
	if !reflect.DeepEqual(first, []string{"250"}) || !reflect.DeepEqual(second, []string{"100"}) {
		t.Fatalf("reads returned %v then %v, want [250] then [100]: new-then-old is the anomaly linearizability forbids and quorum reads permit while a write is in flight", first, second)
	}
}

func TestLostAcknowledgement_WriteTookEffectAndRetryIsIdempotent(t *testing.T) {
	c, sim := tCluster(t, Config{N: 3, R: 1, W: 2}, tABC()...)
	ctx := tPut(t, c, "c1", "k", "v1", nil)
	sim.SetAckLoss("B", true)
	sim.SetAckLoss("C", true)
	if _, err := c.Put("c1", "k", "v2", ctx); !errors.Is(err, ErrQuorumNotMet) {
		t.Fatalf("Put with two lost acks returned %v, want ErrQuorumNotMet (a timeout, from the caller's side)", err)
	}
	for _, id := range tABC() {
		if got := tVals(c, id, "k"); !reflect.DeepEqual(got, []string{"v2"}) {
			t.Fatalf("replica %s holds %v, want [v2]: the write was applied even though the caller saw an error", id, got)
		}
	}
	sim.Heal()
	got, err := c.Put("c1", "k", "v2", ctx) // the client retries with the same context and value
	if err != nil || !reflect.DeepEqual(got, VClock{"c1": 2}) {
		t.Fatalf("retry = (%v, %v), want ({c1:2}, nil)", got, err)
	}
	res := tGet(t, c, "k")
	if len(res.Versions) != 1 || res.Values()[0] != "v2" {
		t.Fatalf("after the retry Get = %v (%d versions), want exactly one version [v2]: a retry carrying the same client, context and value must be idempotent, not a duplicate", res.Values(), len(res.Versions))
	}
}

func TestUnreachableReplicas_FailTheOperationWithErrQuorumNotMet(t *testing.T) {
	c, sim := tCluster(t, Config{N: 3, R: 2, W: 2}, tABC()...)
	tPut(t, c, "c1", "k", "v1", nil)
	sim.SetDown("B", true)
	sim.SetDown("C", true)
	if _, err := c.Get("k"); !errors.Is(err, ErrQuorumNotMet) {
		t.Fatalf("Get with 1 of 3 replicas reachable and R=2 returned %v, want ErrQuorumNotMet", err)
	}
	if _, err := c.Put("c1", "k", "v2", nil); !errors.Is(err, ErrQuorumNotMet) {
		t.Fatalf("Put with 1 of 3 replicas reachable and W=2 returned %v, want ErrQuorumNotMet", err)
	}
	sim.SetDown("C", false)
	if _, err := c.Get("k"); err != nil {
		t.Fatalf("Get with 2 of 3 replicas reachable and R=2 failed: %v", err)
	}
}

// ---------- read repair ----------

func TestReadRepair_HealsStaleResponders(t *testing.T) {
	c, sim := tCluster(t, Config{N: 3, R: 2, W: 2, ReadRepair: true}, tABC()...)
	ctx := tPut(t, c, "c1", "k", "v1", nil)
	sim.SetLag("C", true)
	tPut(t, c, "c1", "k", "v2", ctx) // C is queued, still holds v1
	if got := tVals(c, "C", "k"); !reflect.DeepEqual(got, []string{"v1"}) {
		t.Fatalf("setup: C holds %v, want [v1]", got)
	}
	tOrder(sim, "C", "A", "B")
	if got := tGet(t, c, "k").Values(); !reflect.DeepEqual(got, []string{"v2"}) {
		t.Fatalf("Get returned %v, want [v2] (the newest version among the responders)", got)
	}
	if got := tVals(c, "C", "k"); !reflect.DeepEqual(got, []string{"v2"}) {
		t.Fatalf("after the read C holds %v, want exactly [v2]: read repair writes the answer back to a stale responder and drops the superseded version", got)
	}
}

func TestReadRepair_OnlyTouchesTheRespondersAndCanBeDisabled(t *testing.T) {
	for _, repair := range []bool{true, false} {
		c, sim := tCluster(t, Config{N: 3, R: 2, W: 1, ReadRepair: repair}, tABC()...)
		ctx := tPut(t, c, "c1", "k", "v1", nil)
		sim.SetLag("B", true)
		sim.SetLag("C", true)
		tPut(t, c, "c1", "k", "v2", ctx) // only A has v2
		tOrder(sim, "A", "B", "C")       // responders: A and B; C is not asked
		tGet(t, c, "k")
		gotB, gotC := tVals(c, "B", "k"), tVals(c, "C", "k")
		wantB := []string{"v1"}
		if repair {
			wantB = []string{"v2"}
		}
		if !reflect.DeepEqual(gotB, wantB) {
			t.Errorf("ReadRepair=%v: responder B holds %v, want %v", repair, gotB, wantB)
		}
		if !reflect.DeepEqual(gotC, []string{"v1"}) {
			t.Errorf("ReadRepair=%v: C (not a responder) holds %v, want it untouched [v1]", repair, gotC)
		}
	}
}

func TestReadRepair_NoWritesWhenResponsersAgree(t *testing.T) {
	c, _ := tCluster(t, Config{N: 3, R: 3, W: 3, ReadRepair: true}, tABC()...)
	tPut(t, c, "c1", "k", "v1", nil)
	before := c.Node("A").Versions("k")
	tGet(t, c, "k")
	if after := c.Node("A").Versions("k"); !reflect.DeepEqual(before, after) {
		t.Fatalf("a read of agreeing replicas changed A: %v -> %v", before, after)
	}
}

// ---------- queued writes ----------

func TestFlush_SkipsDownReplicasAndKeepsTheirWritesQueued(t *testing.T) {
	c, sim := tCluster(t, Config{N: 3, R: 1, W: 1}, tABC()...)
	sim.SetLag("B", true)
	sim.SetLag("C", true)
	tPut(t, c, "c1", "k", "v1", nil)
	sim.SetDown("C", true)
	if n := c.Flush(); n != 1 {
		t.Fatalf("Flush with C down applied %d, want 1 (only B)", n)
	}
	if got := tVals(c, "B", "k"); !reflect.DeepEqual(got, []string{"v1"}) {
		t.Fatalf("B holds %v after Flush, want [v1]", got)
	}
	if got := tVals(c, "C", "k"); len(got) != 0 {
		t.Fatalf("C was Down but holds %v", got)
	}
	sim.SetDown("C", false)
	if n := c.Flush(); n != 1 {
		t.Fatalf("second Flush applied %d, want 1: C's write must stay queued while C is down", n)
	}
	if n := c.Flush(); n != 0 {
		t.Fatalf("third Flush applied %d, want 0 (queue is empty)", n)
	}
}

// ---------- sloppy quorums and hinted handoff ----------

func TestSloppyQuorum_AcceptsWritesThatAStrictQuorumRejects(t *testing.T) {
	ids := []string{"A", "B", "C", "D", "E"}
	strict, sim1 := tCluster(t, Config{N: 3, R: 2, W: 2}, ids...)
	sim1.SetDown("B", true)
	sim1.SetDown("C", true)
	if _, err := strict.Put("c1", "k", "v", nil); !errors.Is(err, ErrQuorumNotMet) {
		t.Fatalf("strict quorum with 2 of 3 home replicas down returned %v, want ErrQuorumNotMet", err)
	}

	sloppy, sim2 := tCluster(t, Config{N: 3, R: 2, W: 2, Sloppy: true}, ids...)
	sim2.SetDown("B", true)
	sim2.SetDown("C", true)
	if _, err := sloppy.Put("c1", "k", "v", nil); err != nil {
		t.Fatalf("sloppy quorum should accept the write using stand-ins D and E, got %v", err)
	}
	for id, wantTarget := range map[string]string{"D": "B", "E": "C"} {
		hs := sloppy.Node(id).Hints()
		if len(hs) != 1 || hs[0].Target != wantTarget || hs[0].Key != "k" {
			t.Errorf("stand-in %s holds hints %+v, want one hint for %s", id, hs, wantTarget)
		}
		if got := tVals(sloppy, id, "k"); len(got) != 0 {
			t.Errorf("stand-in %s serves %v as its own data; hints must be kept apart from the node's data", id, got)
		}
	}
}

func TestSloppyQuorum_StandInsAreNotReusedAndRunOut(t *testing.T) {
	ids := []string{"A", "B", "C", "D", "E"}
	c, sim := tCluster(t, Config{N: 3, R: 2, W: 2, Sloppy: true}, ids...)
	sim.SetDown("B", true)
	sim.SetDown("C", true)
	sim.SetDown("E", true) // only D is available as a stand-in
	if _, err := c.Put("c1", "k", "v", nil); err != nil {
		t.Fatalf("A (own ack) + D (stand-in for B) = 2 acks should satisfy W=2: %v", err)
	}
	if hs := c.Node("D").Hints(); len(hs) != 1 || hs[0].Target != "B" {
		t.Fatalf("D holds %+v, want exactly one hint for B (one stand-in carries at most one hint per write)", hs)
	}
	sim.SetDown("D", true)
	if _, err := c.Put("c1", "k2", "v", nil); !errors.Is(err, ErrQuorumNotMet) {
		t.Fatalf("with every fallback down only A can acknowledge; got %v, want ErrQuorumNotMet", err)
	}
}

func TestSloppyQuorum_StrictReadsMissAnAcknowledgedWriteUntilHandoff(t *testing.T) {
	ids := []string{"A", "B", "C", "D", "E"}
	c, sim := tCluster(t, Config{N: 3, R: 2, W: 2, Sloppy: true}, ids...)
	sim.SetDown("B", true)
	sim.SetDown("C", true)
	if _, err := c.Put("c1", "k", "v", nil); err != nil {
		t.Fatalf("write failed: %v", err)
	}
	sim.SetDown("B", false)
	sim.SetDown("C", false)
	tOrder(sim, "B", "C", "A")
	res := tGet(t, c, "k")
	if len(res.Versions) != 0 {
		t.Fatalf("read returned %v, want nothing: R+W>N, yet B and C never saw the write (it sits as a hint on D and E), so the quorum overlap guarantee does not hold under sloppy quorums", res.Values())
	}
	if n := c.DeliverHints(); n != 2 {
		t.Fatalf("DeliverHints delivered %d, want 2", n)
	}
	if got := tGet(t, c, "k").Values(); !reflect.DeepEqual(got, []string{"v"}) {
		t.Fatalf("after hinted handoff the read returned %v, want [v]", got)
	}
	if n := c.DeliverHints(); n != 0 {
		t.Fatalf("second DeliverHints delivered %d, want 0: delivered hints must be removed from the stand-ins", n)
	}
	if hs := c.Node("D").Hints(); len(hs) != 0 {
		t.Fatalf("D still holds hints %+v after handoff", hs)
	}
}

func TestHintedHandoff_KeepsHintsWhileTheTargetIsStillDown(t *testing.T) {
	ids := []string{"A", "B", "C", "D", "E"}
	c, sim := tCluster(t, Config{N: 3, R: 2, W: 2, Sloppy: true}, ids...)
	sim.SetDown("B", true)
	sim.SetDown("C", true)
	if _, err := c.Put("c1", "k", "v", nil); err != nil {
		t.Fatal(err)
	}
	sim.SetDown("B", false) // C is still down
	if n := c.DeliverHints(); n != 1 {
		t.Fatalf("DeliverHints delivered %d, want 1 (only B is back)", n)
	}
	if hs := c.Node("E").Hints(); len(hs) != 1 || hs[0].Target != "C" {
		t.Fatalf("E holds %+v, want C's hint to be kept while C is down", hs)
	}
	if got := tVals(c, "B", "k"); !reflect.DeepEqual(got, []string{"v"}) {
		t.Fatalf("B holds %v after handoff, want [v]", got)
	}
	sim.SetDown("C", false)
	if n := c.DeliverHints(); n != 1 {
		t.Fatalf("DeliverHints delivered %d once C returned, want 1", n)
	}
}

// ---------- concurrency ----------

func TestConcurrentPutsGetsAndFlushes_NoRacesAndNoLostSiblings(t *testing.T) {
	const writers = 20
	c, sim := tCluster(t, Config{N: 3, R: 2, W: 2, ReadRepair: true}, tABC()...)
	var wg sync.WaitGroup
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := c.Put(fmt.Sprintf("client-%02d", i), "k", fmt.Sprintf("value-%02d", i), nil); err != nil {
				t.Errorf("Put by client %d failed: %v", i, err)
			}
		}()
	}
	for r := 0; r < 4; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 100; i++ {
				if _, err := c.Get("k"); err != nil {
					t.Errorf("Get failed: %v", err)
					return
				}
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 100; i++ {
			sim.SetLag("C", i%2 == 0)
			c.Flush()
			c.DeliverHints()
		}
	}()
	tWait(t, &wg, 10*time.Second, "concurrent Put/Get/Flush")
	sim.SetLag("C", false)
	c.Flush()
	res := tGet(t, c, "k")
	if len(res.Versions) != writers {
		t.Fatalf("Get returned %d versions after %d blind concurrent writes, want %d siblings: no write may be lost or overwritten", len(res.Versions), writers, writers)
	}
}
