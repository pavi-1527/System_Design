//go:build breakit

package membership

import (
	"fmt"
	"testing"
)

// TestBreakIt_FanoutSweep measures how many rounds it takes for every node to
// hear from every other, and the messages per node per round, for cluster
// sizes 16 to 1,024 and fanouts 1 to 3. Predict the rounds column from
// log2(n) first:
//
//	go test -tags breakit -run BreakIt_FanoutSweep -v ./labs/c4/membership/
func TestBreakIt_FanoutSweep(t *testing.T) {
	t.Logf("%6s %7s %8s %18s", "nodes", "fanout", "rounds", "msgs/node/round")
	for _, n := range []int{16, 64, 256, 1024} {
		for _, f := range []int{1, 2, 3} {
			names := make([]string, n)
			for i := range names {
				names[i] = fmt.Sprintf("n%04d", i)
			}
			c, err := NewCluster(names, Config{Fanout: f, SuspectAfter: 100, DeadAfter: 200, Seed: 1})
			if err != nil {
				t.Fatal(err)
			}
			rounds := 0
			for rounds < 60 {
				c.Round()
				rounds++
				if tEveryoneHeard(c, names) {
					break
				}
			}
			t.Logf("%6d %7d %8d %18.1f", n, f, rounds, float64(c.Messages())/float64(n*rounds))
		}
	}
	t.Log("Rounds grow with log(n); messages per node per round do not grow with n at all.")
}

func tEveryoneHeard(c *Cluster, names []string) bool {
	for _, o := range names {
		v := c.View(o)
		for _, m := range names {
			if v[m].Heartbeat.Version == 0 {
				return false
			}
		}
	}
	return true
}

// TestBreakIt_PartitionWithoutDeadProbes partitions 16 nodes into two halves
// until each side has convicted the other, heals the network, and counts the
// rounds until the cluster is whole again, with dead-member probes every 4
// rounds and with none. Predict the second row before you run it:
//
//	go test -tags breakit -run BreakIt_PartitionWithoutDeadProbes -v ./labs/c4/membership/
func TestBreakIt_PartitionWithoutDeadProbes(t *testing.T) {
	for _, probe := range []int{4, 0} {
		names := make([]string, 16)
		for i := range names {
			names[i] = fmt.Sprintf("n%02d", i)
		}
		c, err := NewCluster(names, Config{Fanout: 2, SuspectAfter: 5, DeadAfter: 10, DeadProbeEvery: probe, Seed: 4})
		if err != nil {
			t.Fatal(err)
		}
		for i := 0; i < 20; i++ {
			c.Round()
		}
		c.Partition(names[:8], names[8:])
		for i := 0; i < 30; i++ {
			c.Round()
		}
		c.Heal()
		healed := -1
		for i := 1; i <= 200; i++ {
			c.Round()
			if tWhole(c, names) {
				healed = i
				break
			}
		}
		if healed < 0 {
			t.Logf("DeadProbeEvery=%d: still split 200 rounds after the network healed", probe)
		} else {
			t.Logf("DeadProbeEvery=%d: whole again %d rounds after the network healed", probe, healed)
		}
	}
}

func tWhole(c *Cluster, names []string) bool {
	for _, o := range names {
		for _, m := range names {
			if s, _ := c.Status(o, m); s != Alive {
				return false
			}
		}
	}
	return true
}
