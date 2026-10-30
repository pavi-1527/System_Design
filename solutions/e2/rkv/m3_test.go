package rkv

// Milestone 3: measuring replication lag, and followers that fail.
//
//	go test -race -run TestM3 ./labs/e2/rkv/

import (
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

func TestM3_LeaderMeasuresLagForAPartitionedFollower(t *testing.T) {
	l := tStartLeader(t, "n1")
	px := tNewProxy(t, l.Addr)
	f := tStartFollower(t, "n2", px.URL)
	r := tMustPut(t, l, "k0", "v")
	tWaitIndex(t, f, r.Index, 10*time.Second)
	tEventually(t, 5*time.Second, func() (bool, string) {
		fs, ok := tFollower(tStatus(t, l), "n2")
		return ok && fs.LagRecords == 0 && fs.AckedIndex == 1, fmt.Sprintf("leader's view of n2: %+v", fs)
	})

	px.Partition()
	for i := 1; i <= 20; i++ {
		tMustPut(t, l, fmt.Sprintf("k%d", i), "v")
	}
	time.Sleep(400 * time.Millisecond)

	st := tStatus(t, l)
	fs, ok := tFollower(st, "n2")
	if !ok || fs.LagRecords != 20 || fs.AckedIndex != 1 {
		t.Fatalf("leader's view during the partition: %+v (found %v); want acked_index 1 and lag_records 20", fs, ok)
	}
	if fs.LagSeconds < 0.3 || fs.LastFetchAgoMs < 300 {
		t.Fatalf("lag_seconds %.3f, last_fetch_ago_ms %d after 400ms of partition; both must grow while the follower is silent", fs.LagSeconds, fs.LastFetchAgoMs)
	}
	// The follower's own view is the trap: it has not heard of the 20 new
	// entries, so by its own arithmetic it is perfectly caught up.
	fst := tStatus(t, f)
	if fst.LeaderLastIndex != 1 || fst.LastIndex != 1 {
		t.Fatalf("follower sees leader_last_index %d, last_index %d; want 1 and 1 (it cannot know what it has not been sent)", fst.LeaderLastIndex, fst.LastIndex)
	}
	if fst.LastContactAgoMs < 300 {
		t.Fatalf("follower last_contact_ago_ms %d; staleness of contact is the only honest signal a follower has", fst.LastContactAgoMs)
	}

	px.Heal()
	tWaitIndex(t, f, 21, 15*time.Second)
	tEventually(t, 5*time.Second, func() (bool, string) {
		fs, _ := tFollower(tStatus(t, l), "n2")
		return fs.LagRecords == 0 && fs.LagSeconds == 0, fmt.Sprintf("after heal: %+v", fs)
	})
}

func TestM3_KilledFollowerRestartsAndCatchesUp(t *testing.T) {
	l := tStartLeader(t, "n1")
	f := tStartFollower(t, "n2", l.URL)
	for i := 1; i <= 10; i++ {
		tMustPut(t, l, fmt.Sprintf("k%d", i), fmt.Sprintf("a%d", i))
	}
	tWaitIndex(t, f, 10, 10*time.Second)
	tEventually(t, 5*time.Second, func() (bool, string) {
		fs, _ := tFollower(tStatus(t, l), "n2")
		return fs.AckedIndex == 10, fmt.Sprintf("leader's view of n2: %+v", fs)
	})
	f.Kill()
	for i := 6; i <= 15; i++ {
		tMustPut(t, l, fmt.Sprintf("k%d", i), fmt.Sprintf("b%d", i))
	}
	fs, _ := tFollower(tStatus(t, l), "n2")
	if fs.LagRecords != 10 {
		t.Fatalf("leader reports lag_records %d for the dead follower, want 10", fs.LagRecords)
	}
	f.Restart()
	tWaitIndex(t, f, 20, 10*time.Second)
	for i := 1; i <= 15; i++ {
		want := fmt.Sprintf("a%d", i)
		if i >= 6 {
			want = fmt.Sprintf("b%d", i)
		}
		tExpectValue(t, f, fmt.Sprintf("k%d", i), want)
	}
	if st := tStatus(t, f); st.Role != RoleFollower || st.Leader != l.URL {
		t.Fatalf("restarted follower: %+v; it must come back as a follower of the same leader", st)
	}
}

func TestM3_FollowerWithATornWALRecoversAndRefetches(t *testing.T) {
	l := tStartLeader(t, "n1")
	f := tStartFollower(t, "n2", l.URL)
	for i := 1; i <= 8; i++ {
		tMustPut(t, l, fmt.Sprintf("k%d", i), "v")
	}
	tWaitIndex(t, f, 8, 10*time.Second)
	f.Kill()
	tAppendFile(t, filepath.Join(tDataDir(f), WALFileName), []byte{0, 0, 1, 0, 1, 2, 3, 4, 5})
	tMustPut(t, l, "k9", "v")
	f.Restart()
	if st := tStatus(t, f); st.RecoveredTruncatedBytes != 9 {
		t.Fatalf("follower cut %d bytes at start-up, want 9", st.RecoveredTruncatedBytes)
	}
	tWaitIndex(t, f, 9, 10*time.Second)
	tExpectValue(t, f, "k9", "v")
}
