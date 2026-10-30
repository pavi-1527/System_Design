//go:build breakit

package rkv

// Break-it experiments for E2. These are measurements, not pass/fail tests:
// they print tables so you can compare configurations against the
// predictions in your design write-up.
//
//	go test -tags breakit -run BreakIt -v ./labs/e2/rkv/

import (
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

func tPercentile(ds []time.Duration, p float64) time.Duration {
	if len(ds) == 0 {
		return 0
	}
	s := append([]time.Duration(nil), ds...)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	return s[min(len(s)-1, int(p*float64(len(s))))]
}

// tLoad runs writers concurrent PUT loops against p for d and returns the
// latencies of acknowledged writes and the number of failures.
func tLoad(p *tProc, writers int, d time.Duration) (lat []time.Duration, failed int) {
	var mu sync.Mutex
	var wg sync.WaitGroup
	stop := time.Now().Add(d)
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; time.Now().Before(stop); i++ {
				start := time.Now()
				code, _, _, err := tPut(p, fmt.Sprintf("w%d-%d", w, i%100), "value")
				el := time.Since(start)
				mu.Lock()
				if err == nil && code == http.StatusOK {
					lat = append(lat, el)
				} else {
					failed++
				}
				mu.Unlock()
			}
		}(w)
	}
	wg.Wait()
	return lat, failed
}

// BreakIt_WriteCostByMode: what does durability cost per write? Predict the
// ratios before you run it: fsync vs no fsync, async vs semi-sync (one extra
// network round trip plus the follower's own fsync).
func TestBreakIt_WriteCostByMode(t *testing.T) {
	cases := []struct {
		name     string
		leader   []string
		follower bool
	}{
		{"async, fsync never", []string{"-sync", "never"}, false},
		{"async, fsync always", nil, false},
		{"async + follower, fsync always", nil, true},
		{"semi-sync + follower, fsync always", []string{"-mode", ModeSemiSync}, true},
	}
	const writers, dur = 8, 2 * time.Second
	fmt.Printf("\n%-38s %10s %9s %9s %7s\n", "configuration", "writes/s", "p50", "p99", "failed")
	for _, c := range cases {
		t.Run(strings.ReplaceAll(c.name, " ", "_"), func(t *testing.T) {
			l := tStartLeader(t, "n1", c.leader...)
			if c.follower {
				tStartFollower(t, "n2", l.URL)
				tEventuallyPut(t, l, "warmup", "x", 10*time.Second)
			}
			lat, failed := tLoad(l, writers, dur)
			fmt.Printf("%-38s %10.0f %9v %9v %7d\n", c.name, float64(len(lat))/dur.Seconds(),
				tPercentile(lat, 0.50).Round(10*time.Microsecond), tPercentile(lat, 0.99).Round(10*time.Microsecond), failed)
		})
	}
	fmt.Println("\nThe reference serialises writes under one mutex, so fsync latency caps throughput: group commit is the fix (Unit 1 §1, Senior bar).")
}

// BreakIt_FailoverTimeVsLease: failover time is detection + the rest of the
// old lease. Predict each row from -interval, -check-timeout, -fail-after and
// -lease before running it.
func TestBreakIt_FailoverTimeVsLease(t *testing.T) {
	fmt.Printf("\n%-8s %-12s %14s %14s\n", "lease", "fail-after", "unavailable", "coord says")
	for _, c := range []struct {
		lease     string
		failAfter string
	}{{"300ms", "3"}, {"1s", "3"}, {"3s", "3"}, {"1s", "1"}, {"1s", "10"}} {
		t.Run("lease_"+c.lease+"_fail_"+c.failAfter, func(t *testing.T) {
			l := tStartLeader(t, "n1", "-require-lease")
			f := tStartFollower(t, "n2", l.URL, "-require-lease")
			coord := tStart(t, "coord", "coord", "-addr", "127.0.0.1:0", "-nodes", "n1="+l.URL+",n2="+f.URL,
				"-interval", "100ms", "-fail-after", c.failAfter, "-lease", c.lease, "-check-timeout", "250ms")
			tEventuallyPut(t, l, "a", "1", 10*time.Second)
			killed := time.Now()
			l.Kill()
			tEventuallyPut(t, f, "b", "2", 30*time.Second)
			unavailable := time.Since(killed)
			cs := tCoordStatus(t, coord)
			fmt.Printf("%-8s %-12s %14v %12dms\n", c.lease, c.failAfter, unavailable.Round(10*time.Millisecond), cs.LastFailoverMs)
		})
	}
	fmt.Println("\nShorter leases fail over faster and renew more often; fail-after 1 turns one slow renewal into a failover.")
}

// BreakIt_LagUnderLoad: how far behind does an async follower fall while the
// leader takes writes as fast as it can, and how does a 20 ms link change it?
func TestBreakIt_LagUnderLoad(t *testing.T) {
	fmt.Printf("\n%-14s %12s %14s %14s\n", "link delay", "max lag rec", "max lag sec", "writes")
	for _, delay := range []time.Duration{0, 5 * time.Millisecond, 20 * time.Millisecond} {
		t.Run(delay.String(), func(t *testing.T) {
			l := tStartLeader(t, "n1", "-sync", "never")
			px := tNewProxy(t, l.Addr)
			px.Delay(delay)
			tStartFollower(t, "n2", px.URL)
			tEventuallyPut(t, l, "warmup", "x", 10*time.Second)
			done := make(chan struct{})
			var maxRec uint64
			var maxSec float64
			go func() {
				defer close(done)
				for i := 0; i < 40; i++ {
					time.Sleep(50 * time.Millisecond)
					var st NodeStatus
					if tGetJSON(l.URL+"/status", &st) == nil {
						if fs, ok := tFollower(st, "n2"); ok {
							maxRec, maxSec = max(maxRec, fs.LagRecords), max(maxSec, fs.LagSeconds)
						}
					}
				}
			}()
			lat, _ := tLoad(l, 8, 2*time.Second)
			<-done
			fmt.Printf("%-14v %12d %13.3fs %14d\n", delay, maxRec, maxSec, len(lat))
		})
	}
	fmt.Println("\nWith one fetch in flight per follower, lag grows with the round-trip time: pipelining fetches is the fix.")
}
