package rkv

// Milestone 2: leader/follower replication, async and semi-sync.
//
//	go test -race -run TestM2 ./labs/e2/rkv/

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"testing"
	"time"
)

func TestM2_FollowersReplicateTheLeadersLogInOrder(t *testing.T) {
	l := tStartLeader(t, "n1")
	f1 := tStartFollower(t, "n2", l.URL)
	f2 := tStartFollower(t, "n3", l.URL)
	var last WriteResult
	for i := 0; i < 30; i++ {
		last = tMustPut(t, l, fmt.Sprintf("k%d", i%10), fmt.Sprintf("v%d", i))
	}
	if code, _, _, err := tDo("DELETE", l.URL+"/kv/k3", ""); err != nil || code != http.StatusOK {
		t.Fatalf("DELETE on leader: %d %v", code, err)
	}
	for _, f := range []*tProc{f1, f2} {
		tWaitIndex(t, f, last.Index+1, 10*time.Second)
		for k := 0; k < 10; k++ {
			key := fmt.Sprintf("k%d", k)
			lc, lb, _, _ := tGet(l, key, "")
			fc, fb, hdr, _ := tGet(f, key, "")
			if lc != fc || lb != fb {
				t.Fatalf("%s on %s = %d %q, on the leader %d %q: a follower must apply the same log in the same order", key, f.name, fc, fb, lc, lb)
			}
			if hdr.Get(HeaderRole) != RoleFollower {
				t.Fatalf("read on %s has %s %q, want follower", f.name, HeaderRole, hdr.Get(HeaderRole))
			}
		}
		st := tStatus(t, f)
		if st.Role != RoleFollower || st.Epoch != 1 || st.LeaderURL != l.URL || st.Leader != l.URL {
			t.Fatalf("%s status = %+v; want follower at epoch 1 with leader and leader_url %s", f.name, st, l.URL)
		}
	}
	st := tStatus(t, l)
	if len(st.Followers) != 2 || st.Followers[0].ID != "n2" || st.Followers[1].ID != "n3" {
		t.Fatalf("leader sees followers %+v; want n2 and n3 (it learns them from their fetches)", st.Followers)
	}
}

func TestM2_FollowerRefusesWritesAndNamesTheLeader(t *testing.T) {
	l := tStartLeader(t, "n1")
	f := tStartFollower(t, "n2", l.URL)
	r := tMustPut(t, l, "a", "1")
	tWaitIndex(t, f, r.Index, 10*time.Second)
	code, body, hdr, err := tDo("PUT", f.URL+"/kv/b", "2")
	if err != nil || code != http.StatusMisdirectedRequest {
		t.Fatalf("PUT on a follower = %d %q %v; want 421: only the leader orders writes", code, body, err)
	}
	var eb ErrorBody
	_ = json.Unmarshal([]byte(body), &eb)
	if hdr.Get(HeaderLeader) != l.URL || eb.Leader != l.URL {
		t.Fatalf("421 names leader %q (header) / %q (body); want %s so the client can retry there", hdr.Get(HeaderLeader), eb.Leader, l.URL)
	}
	if code, _, _, _ := tGet(f, "b", ""); code != http.StatusNotFound {
		t.Fatalf("the refused write is readable on the follower (%d)", code)
	}
}

func TestM2_ConcurrentWritersGetDistinctIndexesAndOneOrder(t *testing.T) {
	l := tStartLeader(t, "n1")
	f := tStartFollower(t, "n2", l.URL)
	var mu sync.Mutex
	seen := map[uint64]bool{}
	var wg sync.WaitGroup
	for w := 0; w < 8; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < 20; i++ {
				code, res, body, err := tPut(l, fmt.Sprintf("shared-%d", i%4), fmt.Sprintf("w%d-%d", w, i))
				if err != nil || code != http.StatusOK {
					t.Errorf("PUT: %d %s %v", code, body, err)
					return
				}
				mu.Lock()
				if seen[res.Index] {
					t.Errorf("index %d handed out twice: two writes claim one slot in the log", res.Index)
				}
				seen[res.Index] = true
				mu.Unlock()
			}
		}(w)
	}
	wg.Wait()
	if t.Failed() {
		return
	}
	for i := uint64(1); i <= 160; i++ {
		if !seen[i] {
			t.Fatalf("index %d never acknowledged; 160 writes must get indexes 1..160 with no holes", i)
		}
	}
	tWaitIndex(t, f, 160, 10*time.Second)
	for k := 0; k < 4; k++ {
		key := fmt.Sprintf("shared-%d", k)
		_, lb, _, _ := tGet(l, key, "")
		_, fb, _, _ := tGet(f, key, "")
		if lb != fb {
			t.Fatalf("%s: leader %q, follower %q; concurrent writes must reach every replica in the leader's order", key, lb, fb)
		}
	}
}

func TestM2_SemiSyncAcksOnlyAfterAFollowerHasTheWrite(t *testing.T) {
	l := tStartLeader(t, "n1", "-mode", ModeSemiSync)
	f := tStartFollower(t, "n2", l.URL)
	tEventuallyPut(t, l, "warmup", "x", 10*time.Second)
	for i := 0; i < 20; i++ {
		r := tMustPut(t, l, fmt.Sprintf("k%d", i), "v")
		// No waiting: at the moment of the ack the follower must already
		// have the entry on its disk, because it asked for something after it.
		if st := tStatus(t, f); st.LastIndex < r.Index {
			t.Fatalf("semi-sync acked index %d while the follower had only %d: that ack would not survive losing the leader", r.Index, st.LastIndex)
		}
	}
}

func TestM2_SemiSyncWithoutAFollowerAnswers503NotASilentAck(t *testing.T) {
	l := tStartLeader(t, "n1", "-mode", ModeSemiSync, "-ack-timeout", "300ms")
	start := time.Now()
	code, body, _, err := tDo("PUT", l.URL+"/kv/a", "1")
	if err != nil || code != http.StatusServiceUnavailable {
		t.Fatalf("semi-sync PUT with no follower = %d %q %v; want 503 after the ack timeout (never a silent async ack)", code, body, err)
	}
	if el := time.Since(start); el > 4*time.Second {
		t.Fatalf("the 503 took %v; the wait must be bounded by -ack-timeout (300ms)", el)
	}
	var eb ErrorBody
	_ = json.Unmarshal([]byte(body), &eb)
	if eb.Index != 1 {
		t.Fatalf("503 body %q should carry index 1: the entry is in the leader's log and may still replicate", body)
	}
	if st := tStatus(t, l); st.LastIndex != 1 {
		t.Fatalf("leader last_index %d after the 503; the entry was written before the wait, so it is 1", st.LastIndex)
	}
	// "Outcome unknown" is literal: a follower that shows up later gets it.
	f := tStartFollower(t, "n2", l.URL)
	tWaitIndex(t, f, 1, 10*time.Second)
	tExpectValue(t, f, "a", "1")
}
