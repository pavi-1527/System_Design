package rkv

// Milestone 5: read-your-writes on followers with a version token.
//
//	go test -race -run TestM5 ./labs/e2/rkv/

import (
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// tPartitionedPair writes x=1 everywhere, cuts the follower off, writes x=2
// on the leader, and returns the token of the second write.
func tPartitionedPair(t *testing.T) (l, f *tProc, px *tProxy, token WriteResult) {
	l = tStartLeader(t, "n1")
	px = tNewProxy(t, l.Addr)
	f = tStartFollower(t, "n2", px.URL, "-ryw-wait", "300ms")
	r := tMustPut(t, l, "x", "1")
	tWaitIndex(t, f, r.Index, 10*time.Second)
	px.Partition()
	token = tMustPut(t, l, "x", "2")
	return l, f, px, token
}

func TestM5_FollowerReadWithoutATokenCanBeStale(t *testing.T) {
	_, f, _, token := tPartitionedPair(t)
	code, body, hdr, err := tGet(f, "x", "")
	if err != nil || code != http.StatusOK || body != "1" {
		t.Fatalf("plain follower read = %d %q %v; want the stale 200 \"1\": a follower serves what it has, "+
			"which is exactly the read-your-writes anomaly (A4 Unit 1 §3)", code, body, err)
	}
	if hdr.Get(HeaderIndex) != "1" {
		t.Fatalf("stale read carries %s %q, want 1; the client can only notice staleness if the index is exposed", HeaderIndex, hdr.Get(HeaderIndex))
	}
	_ = token
}

func TestM5_MinIndexWaitsThenRedirectsToTheLeader(t *testing.T) {
	l, f, px, token := tPartitionedPair(t)
	q := fmt.Sprintf("?min_index=%d", token.Index)
	start := time.Now()
	code, body, hdr, err := tGet(f, "x", q)
	el := time.Since(start)
	if err != nil || code != http.StatusTemporaryRedirect {
		t.Fatalf("follower read with min_index=%d while it has 1 = %d %q %v; want 307 to the leader", token.Index, code, body, err)
	}
	if el < 250*time.Millisecond || el > 3*time.Second {
		t.Fatalf("307 came after %v; the follower should wait about -ryw-wait (300ms) for replication first, and no longer", el)
	}
	want := l.URL + "/kv/x" + q
	if loc := hdr.Get("Location"); loc != want {
		t.Fatalf("Location %q, want %q (the leader's advertised URL, not the replication path the follower uses)", loc, want)
	}
	code, body, _, err = tDo("GET", hdr.Get("Location"), "")
	if err != nil || code != http.StatusOK || body != "2" {
		t.Fatalf("following the redirect: %d %q %v; want 200 \"2\"", code, body, err)
	}

	px.Heal()
	tEventually(t, 10*time.Second, func() (bool, string) {
		code, body, hdr, err := tGet(f, "x", q)
		return err == nil && code == http.StatusOK && body == "2" && hdr.Get(HeaderRole) == RoleFollower,
			fmt.Sprintf("after heal, follower read with the token = %d %q %v", code, body, err)
	})
}

func TestM5_ATokenTheFollowerAlreadyHasIsServedLocallyAtOnce(t *testing.T) {
	l := tStartLeader(t, "n1")
	f := tStartFollower(t, "n2", l.URL, "-ryw-wait", "2s")
	r := tMustPut(t, l, "y", "7")
	tWaitIndex(t, f, r.Index, 10*time.Second)
	start := time.Now()
	code, body, hdr, err := tGet(f, "y", fmt.Sprintf("?min_index=%d", r.Index))
	if err != nil || code != http.StatusOK || body != "7" || hdr.Get(HeaderRole) != RoleFollower {
		t.Fatalf("follower read with a satisfied token = %d %q %v (%s %q); want 200 \"7\" from the follower", code, body, err, HeaderRole, hdr.Get(HeaderRole))
	}
	if el := time.Since(start); el > time.Second {
		t.Fatalf("a satisfied token took %v; it must not wait", el)
	}
	if code, _, _, _ := tGet(f, "y", "?min_index=abc"); code != http.StatusBadRequest {
		t.Fatalf("min_index=abc = %d, want 400", code)
	}
}

func TestM5_ConcurrentClientsAlwaysReadTheirOwnWrites(t *testing.T) {
	l := tStartLeader(t, "n1")
	f := tStartFollower(t, "n2", l.URL, "-ryw-wait", "200ms")
	tWaitIndex(t, f, 0, 5*time.Second)
	var redirects, local atomic.Int64
	var wg sync.WaitGroup
	for g := 0; g < 6; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			key := fmt.Sprintf("member-%d", g)
			for i := 0; i < 15; i++ {
				val := fmt.Sprintf("%d-%d", g, i)
				code, r, body, err := tPut(l, key, val)
				if err != nil || code != http.StatusOK {
					t.Errorf("PUT: %d %s %v", code, body, err)
					return
				}
				code, got, hdr, err := tGet(f, key, fmt.Sprintf("?min_index=%d", r.Index))
				if err == nil && code == http.StatusTemporaryRedirect {
					redirects.Add(1)
					code, got, _, err = tDo("GET", hdr.Get("Location"), "")
				} else {
					local.Add(1)
				}
				if err != nil || code != http.StatusOK || got != val {
					t.Errorf("%s wrote %q (index %d) and then read %d %q %v: a member must always see their own write", key, val, r.Index, code, got, err)
					return
				}
			}
		}(g)
	}
	wg.Wait()
	t.Logf("served by the follower: %d, redirected to the leader: %d", local.Load(), redirects.Load())
}
