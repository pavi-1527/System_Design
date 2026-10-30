package rkv

// Milestone 4: failure injection and failover. Every test here kills,
// partitions or freezes a real process and checks what the cluster did about
// it against what the design promised.
//
//	go test -race -run TestM4 ./labs/e2/rkv/

import (
	"fmt"
	"net/http"
	"sync"
	"testing"
	"time"
)

const tFailoverTimeout = 15 * time.Second

// tWaitLeader waits until the coordinator reports one of want as leader at an
// epoch above minEpoch, and returns its id.
func tWaitLeader(t *testing.T, c *tProc, minEpoch uint64, want ...string) string {
	t.Helper()
	var got CoordStatus
	tEventually(t, tFailoverTimeout, func() (bool, string) {
		got = tCoordStatus(t, c)
		for _, w := range want {
			if got.Leader == w && got.Epoch > minEpoch {
				return true, ""
			}
		}
		return false, fmt.Sprintf("coordinator says leader %q at epoch %d; want one of %v above epoch %d (events: %v)",
			got.Leader, got.Epoch, want, minEpoch, got.Events)
	})
	return got.Leader
}

func TestM4_CoordinatorFailsOverWhenTheLeaderIsKilled(t *testing.T) {
	l := tStartLeader(t, "n1", "-require-lease")
	f1 := tStartFollower(t, "n2", l.URL, "-require-lease")
	f2 := tStartFollower(t, "n3", l.URL, "-require-lease")
	c := tStartCoord(t, l, f1, f2)
	tEventuallyPut(t, l, "k0", "v0", 10*time.Second) // first lease
	var last WriteResult
	for i := 1; i <= 10; i++ {
		last = tMustPut(t, l, fmt.Sprintf("k%d", i), fmt.Sprintf("v%d", i))
	}
	tWaitIndex(t, f1, last.Index, 10*time.Second)
	tWaitIndex(t, f2, last.Index, 10*time.Second)

	killed := time.Now()
	l.Kill()
	id := tWaitLeader(t, c, 1, "n2", "n3")
	took := time.Since(killed)
	t.Logf("failover took %v (detection: 3 failed renewals; then the old lease must expire)", took.Round(10*time.Millisecond))
	nl, other := f1, f2
	if id == "n3" {
		nl, other = f2, f1
	}
	cs := tCoordStatus(t, c)
	if cs.Epoch != 2 || cs.Failovers != 1 || cs.LeaderURL != nl.URL {
		t.Fatalf("coordinator after failover: %+v; want epoch 2, failovers 1, leader_url %s", cs, nl.URL)
	}
	if st := tStatus(t, nl); st.Role != RoleLeader || st.Epoch != 2 {
		t.Fatalf("promoted node: role %s epoch %d; want leader at epoch 2 (the new epoch is the fencing token)", st.Role, st.Epoch)
	}
	tEventually(t, 10*time.Second, func() (bool, string) {
		st := tStatus(t, other)
		return st.Role == RoleFollower && st.Epoch == 2 && st.Leader == nl.URL,
			fmt.Sprintf("%s: role %s epoch %d leader %s; want follower of %s at epoch 2", other.name, st.Role, st.Epoch, st.Leader, nl.URL)
	})
	for i := 0; i <= 10; i++ {
		tExpectValue(t, nl, fmt.Sprintf("k%d", i), fmt.Sprintf("v%d", i))
	}
	r := tEventuallyPut(t, nl, "after", "failover", 10*time.Second)
	if r.Epoch != 2 || r.Index != last.Index+1 {
		t.Fatalf("first write on the new leader got %+v; want epoch 2, index %d (the log continues, the epoch changes)", r, last.Index+1)
	}
	tWaitIndex(t, other, r.Index, 10*time.Second)
	tExpectValue(t, other, "after", "failover")
}

// The same scenario twice: the follower is cut off, the leader acknowledges a
// write, the leader dies, the follower is promoted. Async loses the write;
// semi-sync never acknowledged it.

func TestM4_AsyncModeLosesAnAcknowledgedWriteAndFencesTheOldLeader(t *testing.T) {
	l := tStartLeader(t, "n1", "-require-lease")
	px := tNewProxy(t, l.Addr)
	f := tStartFollower(t, "n2", px.URL, "-require-lease")
	c := tStartCoord(t, l, f)
	a := tEventuallyPut(t, l, "a", "1", 10*time.Second)
	tWaitIndex(t, f, a.Index, 10*time.Second)

	px.Partition()
	b := tMustPut(t, l, "b", "2") // 200: async acks on the leader's disk alone
	l.Kill()
	tWaitLeader(t, c, 1, "n2")

	t.Run("acknowledged write is lost", func(t *testing.T) {
		tExpectValue(t, f, "a", "1")
		code, body, _, err := tGet(f, "b", "")
		if err != nil || code != http.StatusNotFound {
			t.Fatalf("b (acknowledged at index %d) reads %d %q %v on the new leader; with the follower partitioned it cannot have it: "+
				"if this passes, the partition did not hold", b.Index, code, body, err)
		}
		t.Logf("RPO observed: 1 acknowledged write lost (index %d, epoch %d). Async replication's RPO is the replication lag at the moment of the crash.", b.Index, b.Epoch)
	})

	t.Run("returning old leader is fenced and truncates its divergent suffix", func(t *testing.T) {
		c3 := tEventuallyPut(t, f, "c", "3", 10*time.Second)
		if c3.Index != b.Index || c3.Epoch != 2 {
			t.Fatalf("new leader's first write = %+v; want index %d (the slot the lost write had) at epoch 2", c3, b.Index)
		}
		l.Restart() // comes back believing, from meta.json, that it leads epoch 1
		code, _, body, err := tPut(l, "d", "4")
		if err == nil && code == http.StatusOK {
			t.Fatalf("the restarted old leader acknowledged a write (%s): two leaders accepting writes is split brain; without a lease it must refuse", body)
		}
		tEventually(t, tFailoverTimeout, func() (bool, string) {
			st := tStatus(t, l)
			return st.Role == RoleFollower && st.Epoch == 2 && st.LastIndex >= c3.Index && st.LastEpoch == 2,
				fmt.Sprintf("old leader: role %s epoch %d last (%d, epoch %d); want a follower at epoch 2 holding index %d from epoch 2",
					st.Role, st.Epoch, st.LastIndex, st.LastEpoch, c3.Index)
		})
		if code, _, _, _ := tGet(l, "b", ""); code != http.StatusNotFound {
			t.Fatalf("the old leader still has b (%d): its divergent entry must be truncated, or replicas disagree forever", code)
		}
		tExpectValue(t, l, "c", "3")
	})
}

func TestM4_SemiSyncLosesNoAcknowledgedWrite(t *testing.T) {
	l := tStartLeader(t, "n1", "-require-lease", "-mode", ModeSemiSync, "-ack-timeout", "500ms")
	px := tNewProxy(t, l.Addr)
	f := tStartFollower(t, "n2", px.URL, "-require-lease", "-mode", ModeSemiSync, "-ack-timeout", "500ms")
	c := tStartCoord(t, l, f)

	var mu sync.Mutex
	acked := map[string]string{}
	a := tEventuallyPut(t, l, "a", "1", 10*time.Second)
	acked["a"] = "1"
	tWaitIndex(t, f, a.Index, 10*time.Second)

	px.Partition()
	var wg sync.WaitGroup
	for w := 0; w < 4; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < 3; i++ {
				key := fmt.Sprintf("p%d-%d", w, i)
				code, _, body, err := tPut(l, key, "x")
				if err == nil && code == http.StatusOK {
					mu.Lock()
					acked[key] = "x"
					mu.Unlock()
					t.Errorf("semi-sync acknowledged %s while its only follower was partitioned: %s", key, body)
				} else if err == nil && code != http.StatusServiceUnavailable {
					t.Errorf("PUT %s during the partition = %d %s; want 503 (not replicated, outcome unknown)", key, code, body)
				}
			}
		}(w)
	}
	wg.Wait()
	l.Kill()
	tWaitLeader(t, c, 1, "n2")
	mu.Lock()
	defer mu.Unlock()
	for k, v := range acked {
		tExpectValue(t, f, k, v)
	}
	t.Logf("RPO observed: 0 acknowledged writes lost; %d writes during the partition were refused instead", 12)
}

func TestM4_PausedZombieLeaderCannotAcceptWritesAfterFailover(t *testing.T) {
	l := tStartLeader(t, "n1", "-require-lease")
	f := tStartFollower(t, "n2", l.URL, "-require-lease")
	c := tStartCoord(t, l, f)
	r := tEventuallyPut(t, l, "a", "1", 10*time.Second)
	tWaitIndex(t, f, r.Index, 10*time.Second)

	l.Pause() // a 10-second GC pause, a frozen VM: the process is alive but silent
	tWaitLeader(t, c, 1, "n2")
	l.Resume()
	// The zombie wakes up mid-thought. Whatever renewals were queued in its
	// socket buffer are applied now; none of them may let it accept a write.
	for i := 0; i < 5; i++ {
		code, body, _, err := tDo("PUT", l.URL+"/kv/zombie", fmt.Sprint(i))
		if err == nil && code == http.StatusOK {
			t.Fatalf("the resumed zombie leader acknowledged a write (%s) after n2 was promoted: its lease must have expired before the promotion", body)
		}
	}
	tEventually(t, tFailoverTimeout, func() (bool, string) {
		st := tStatus(t, l)
		return st.Role == RoleFollower && st.Epoch == 2, fmt.Sprintf("zombie: role %s epoch %d; want demoted to follower at epoch 2", st.Role, st.Epoch)
	})
	w := tEventuallyPut(t, f, "b", "2", 10*time.Second)
	tWaitIndex(t, l, w.Index, 10*time.Second)
	tExpectValue(t, l, "b", "2")
	if code, _, _, _ := tGet(f, "zombie", ""); code != http.StatusNotFound {
		t.Fatalf("a write sent to the zombie reached the new leader (%d)", code)
	}
}

func TestM4_FollowerRejectsAnOlderEpoch(t *testing.T) {
	l := tStartLeader(t, "n1", "-epoch", "5")
	f := tStartFollower(t, "n2", l.URL, "-epoch", "5")
	r := tMustPut(t, l, "a", "1")
	tWaitIndex(t, f, r.Index, 10*time.Second)
	code, body, _, err := tDo("POST", f.URL+"/admin/follow", `{"leader":"http://127.0.0.1:1","epoch":4}`)
	if err != nil || code != http.StatusConflict {
		t.Fatalf("follow at epoch 4 on a node at epoch 5 = %d %s %v; want 409: an older epoch is a deposed leader's", code, body, err)
	}
	code, body, _, err = tDo("POST", l.URL+"/admin/promote", `{"epoch":5,"lease_expires_unix_ms":0}`)
	if err != nil || code != http.StatusConflict {
		t.Fatalf("promote to the current epoch = %d %s %v; want 409: two leaders must never share an epoch", code, body, err)
	}
	code, body, _, err = tDo("POST", l.URL+"/admin/lease", `{"epoch":4,"expires_unix_ms":9999999999999}`)
	if err != nil || code != http.StatusConflict {
		t.Fatalf("lease for epoch 4 on the epoch-5 leader = %d %s %v; want 409", code, body, err)
	}
	if st := tStatus(t, f); st.Epoch != 5 || st.Leader != l.URL {
		t.Fatalf("follower changed state after a refused request: %+v", st)
	}
}
