//go:build breakit

package replkv

// Break-it experiments for A4 Unit 1 (§2–§3). Measurements, not pass/fail
// tests: they print tables. Predict the numbers before running.
//
// Run: go test -tags breakit -run BreakIt -v ./labs/a4/replkv/

import (
	"fmt"
	"testing"
)

// TestBreakIt_ReadAnomalyRates: a member updates their balance, then reloads
// the page, 300 times. Followers lag by 2, 4 and 7 ticks; reads rotate over
// the followers. For each session mode, how often does the member miss their
// own write, how often does a second read show an older state than the
// first, and how many reads had to be served by the leader (the price of the
// guarantee)?
func TestBreakIt_ReadAnomalyRates(t *testing.T) {
	modes := []struct {
		name string
		opts SessionOptions
	}{
		{"none", SessionOptions{}},
		{"read-your-writes", SessionOptions{ReadYourWrites: true}},
		{"monotonic-reads", SessionOptions{MonotonicReads: true}},
		{"both", SessionOptions{ReadYourWrites: true, MonotonicReads: true}},
	}
	t.Logf("%-18s %12s %14s %14s", "mode", "missed write", "went backwards", "leader reads")
	for _, m := range modes {
		c := NewCluster(Options{Followers: 3})
		c.Network().SetDelay("n1", 2)
		c.Network().SetDelay("n2", 4)
		c.Network().SetDelay("n3", 7)
		s := c.NewSession(m.opts)
		other := c.NewSession(SessionOptions{})
		const rounds = 300
		missed, backwards, leaderReads := 0, 0, 0
		for i := 1; i <= rounds; i++ {
			want := fmt.Sprint(i)
			_, _ = other.Put("noise", want) // someone else's traffic keeps replicas at different LSNs
			_, _ = s.Put("balance", want)
			c.Tick()
			r1, err := s.Get("balance")
			if err != nil {
				t.Fatal(err)
			}
			if r1.Value != want {
				missed++
			}
			r2, _ := s.Get("balance")
			if r2.AppliedLSN < r1.AppliedLSN {
				backwards++
			}
			for _, r := range []ReadResult{r1, r2} {
				if r.Node == "n0" {
					leaderReads++
				}
			}
			c.Tick()
		}
		t.Logf("%-18s %11.0f%% %13.0f%% %13.0f%%", m.name, 100*float64(missed)/rounds, 100*float64(backwards)/rounds, 100*float64(leaderReads)/(2*rounds))
	}
}

// TestBreakIt_SyncVersusAsync: 100 writes, one per tick, then the leader
// crashes and the most up-to-date follower is promoted. Followers lag by 2 and
// 5 ticks. How many ACKNOWLEDGED writes are lost, and what did the writer pay
// in waiting? Then the same with one follower partitioned from the start:
// how many writes can the client even get acknowledged?
func TestBreakIt_SyncVersusAsync(t *testing.T) {
	t.Logf("%-10s %8s %12s %14s", "sync acks", "acked", "lost (acked)", "avg ticks/put")
	for _, acks := range []int{0, 1, 2} {
		c := NewCluster(Options{SyncAcks: acks, SyncTimeoutTicks: 20})
		c.Network().SetDelay("n1", 2)
		c.Network().SetDelay("n2", 5)
		acked, waited := 0, 0
		for i := 0; i < 100; i++ {
			res, err := c.Put(fmt.Sprintf("k%d", i), "v")
			if err == nil {
				acked++
			}
			waited += res.Ticks
			c.Tick()
		}
		_ = c.Crash("n0")
		rep, _ := c.Failover()
		t.Logf("%-10d %8d %12d %14.1f", acks, acked, len(rep.LostWrites), float64(waited)/100)
	}
	t.Logf("")
	t.Logf("with n2 partitioned (never reachable):")
	t.Logf("%-10s %8s %12s", "sync acks", "acked", "timeouts")
	for _, acks := range []int{0, 1, 2} {
		c := NewCluster(Options{SyncAcks: acks, SyncTimeoutTicks: 20})
		c.Network().Partition("n2", true)
		acked, timeouts := 0, 0
		for i := 0; i < 100; i++ {
			if _, err := c.Put(fmt.Sprintf("k%d", i), "v"); err == nil {
				acked++
			} else {
				timeouts++
			}
			c.Tick()
		}
		t.Logf("%-10d %8d %12d", acks, acked, timeouts)
	}
}
