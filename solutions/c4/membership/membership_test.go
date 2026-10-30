package membership

import (
	"errors"
	"fmt"
	"math"
	"sync"
	"testing"
	"time"
)

// ---------- helpers (prefixed with t to avoid clashing with your code) ----------

func tNames(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = fmt.Sprintf("n%02d", i)
	}
	return out
}

func tCluster(t *testing.T, n int, cfg Config) *Cluster {
	t.Helper()
	c, err := NewCluster(tNames(n), cfg)
	if err != nil {
		t.Fatalf("NewCluster(%d nodes, %+v): %v", n, cfg, err)
	}
	if c == nil {
		t.Fatalf("NewCluster returned a nil cluster and no error")
	}
	return c
}

// tStatus fails the test if the names are unknown.
func tStatus(t *testing.T, c *Cluster, observer, member string) Status {
	t.Helper()
	s, ok := c.Status(observer, member)
	if !ok {
		t.Fatalf("Status(%q, %q): unknown name", observer, member)
	}
	return s
}

// tRunUntil runs rounds until cond holds or max rounds have run, and returns
// the number of rounds it took (max+1 if it never held).
func tRunUntil(c *Cluster, max int, cond func() bool) int {
	for i := 1; i <= max; i++ {
		c.Round()
		if cond() {
			return i
		}
	}
	return max + 1
}

// tAll reports whether every observer in obs sees every member in mem with status s.
func tAll(c *Cluster, obs, mem []string, s Status) bool {
	for _, o := range obs {
		for _, m := range mem {
			if o == m {
				continue
			}
			if got, ok := c.Status(o, m); !ok || got != s {
				return false
			}
		}
	}
	return true
}

func tWithout(names []string, drop ...string) []string {
	skip := make(map[string]bool)
	for _, d := range drop {
		skip[d] = true
	}
	var out []string
	for _, n := range names {
		if !skip[n] {
			out = append(out, n)
		}
	}
	return out
}

// ---------- heartbeats and config ----------

func TestHeartbeat_NewerComparesGenerationFirst(t *testing.T) {
	cases := []struct {
		h, o Heartbeat
		want bool
	}{
		{Heartbeat{0, 5}, Heartbeat{0, 4}, true},
		{Heartbeat{0, 4}, Heartbeat{0, 5}, false},
		{Heartbeat{0, 4}, Heartbeat{0, 4}, false},
		{Heartbeat{1, 0}, Heartbeat{0, 900}, true},
		{Heartbeat{0, 900}, Heartbeat{1, 0}, false},
	}
	for _, c := range cases {
		if got := c.h.Newer(c.o); got != c.want {
			t.Errorf("%+v.Newer(%+v) = %v, want %v (a restarted node's version starts again at 0; only the generation tells that it is newer)",
				c.h, c.o, got, c.want)
		}
	}
}

func TestNewCluster_RejectsBadConfig(t *testing.T) {
	good := Config{Fanout: 1, SuspectAfter: 2, DeadAfter: 3}
	bad := []struct {
		name  string
		names []string
		cfg   Config
	}{
		{"one node", []string{"a"}, good},
		{"duplicate name", []string{"a", "b", "a"}, good},
		{"empty name", []string{"a", ""}, good},
		{"fanout 0", []string{"a", "b"}, Config{Fanout: 0, SuspectAfter: 2, DeadAfter: 3}},
		{"suspect 0", []string{"a", "b"}, Config{Fanout: 1, SuspectAfter: 0, DeadAfter: 3}},
		{"dead not after suspect", []string{"a", "b"}, Config{Fanout: 1, SuspectAfter: 3, DeadAfter: 3}},
		{"negative probe interval", []string{"a", "b"}, Config{Fanout: 1, SuspectAfter: 2, DeadAfter: 3, DeadProbeEvery: -1}},
	}
	for _, b := range bad {
		if _, err := NewCluster(b.names, b.cfg); !errors.Is(err, ErrBadConfig) {
			t.Errorf("%s: NewCluster error = %v, want ErrBadConfig", b.name, err)
		}
	}
	c, err := NewCluster([]string{"b", "a"}, good)
	if err != nil || c == nil {
		t.Fatalf("NewCluster(valid) = %v, %v", c, err)
	}
	v := c.View("a")
	if len(v) != 2 || v["b"].Status != Alive || v["b"].Heartbeat != (Heartbeat{}) {
		t.Errorf("a fresh view = %+v; want both members, Alive, zero heartbeats", v)
	}
	if c.View("zz") != nil {
		t.Error("View of an unknown observer must be nil")
	}
	if _, ok := c.Status("a", "zz"); ok {
		t.Error("Status of an unknown member must report false")
	}
	if Dead.String() != "dead" {
		t.Errorf("Dead.String() = %q", Dead.String())
	}
}

// ---------- dissemination ----------

func TestRound_BumpsOwnVersionAndCountsMessages(t *testing.T) {
	c := tCluster(t, 8, Config{Fanout: 2, SuspectAfter: 5, DeadAfter: 10, Seed: 1})
	c.Round()
	c.Round()
	c.Round()
	if c.Rounds() != 3 {
		t.Fatalf("Rounds() = %d, want 3", c.Rounds())
	}
	for _, n := range tNames(8) {
		self := c.View(n)[n]
		if self.Heartbeat != (Heartbeat{Version: 3}) || self.LastProgress != 3 || self.Status != Alive {
			t.Errorf("%s sees itself as %+v after 3 rounds; want version 3, last progress 3, alive", n, self)
		}
	}
	if got := c.Messages(); got != 8*2*3 {
		t.Errorf("Messages() = %d, want %d (8 nodes × fanout 2 × 3 rounds)", got, 8*2*3)
	}
}

func TestRound_HeartbeatsReachEveryoneInLogarithmicRounds(t *testing.T) {
	for seed := int64(1); seed <= 5; seed++ {
		names := tNames(64)
		c := tCluster(t, 64, Config{Fanout: 1, SuspectAfter: 30, DeadAfter: 60, Seed: seed})
		rounds := tRunUntil(c, 40, func() bool {
			for _, o := range names {
				v := c.View(o)
				for _, m := range names {
					if v[m].Heartbeat.Version == 0 {
						return false
					}
				}
			}
			return true
		})
		// Push-pull gossip with fanout 1 spreads one update to n nodes in
		// ≈ log2(n) + ln(ln(n)) rounds; 64 nodes ≈ 7–8. 15 leaves slack.
		if rounds > 15 {
			t.Errorf("seed %d: it took %d rounds for every node to hear from every other among 64 (want ≤ 15; "+
				"is the exchange push-pull, and do merges keep the newer heartbeat?)", seed, rounds)
		}
	}
}

func TestRound_HealthyClusterHasNoFalseSuspicion(t *testing.T) {
	names := tNames(32)
	c := tCluster(t, 32, Config{Fanout: 3, SuspectAfter: 10, DeadAfter: 20, Seed: 3})
	for i := 0; i < 60; i++ {
		c.Round()
		if c.Rounds() < 12 {
			continue
		}
		for _, o := range names {
			for m, mem := range c.View(o) {
				if mem.Status != Alive {
					t.Fatalf("round %d: %s sees healthy %s as %v (age %d); gossip is not refreshing heartbeats",
						c.Rounds(), o, m, mem.Status, c.Rounds()-mem.LastProgress)
				}
			}
		}
	}
}

// ---------- failure detection ----------

func TestStop_CrashedNodeGoesSuspectThenDeadEverywhere(t *testing.T) {
	names := tNames(16)
	cfg := Config{Fanout: 2, SuspectAfter: 6, DeadAfter: 12, Seed: 5}
	c := tCluster(t, 16, cfg)
	for i := 0; i < 15; i++ {
		c.Round()
	}
	c.Stop("n03")
	others := tWithout(names, "n03")
	sawSuspect := make(map[string]bool)
	rounds := tRunUntil(c, cfg.DeadAfter+15, func() bool {
		all := true
		for _, o := range others {
			s := tStatus(t, c, o, "n03")
			if s == Suspect {
				sawSuspect[o] = true
			}
			if s != Dead {
				all = false
			}
		}
		return all
	})
	if rounds > cfg.DeadAfter+15 {
		t.Fatalf("n03 was not Dead to every live node within %d rounds of crashing", cfg.DeadAfter+15)
	}
	if rounds < cfg.DeadAfter {
		t.Errorf("every node convicted n03 after %d rounds, sooner than DeadAfter = %d", rounds, cfg.DeadAfter)
	}
	for _, o := range others {
		if !sawSuspect[o] {
			t.Errorf("%s went straight from alive to dead for n03; it must be Suspect for SuspectAfter ≤ age < DeadAfter", o)
		}
	}
	for _, o := range others {
		for _, m := range others {
			if s := tStatus(t, c, o, m); s != Alive {
				t.Errorf("%s sees healthy %s as %v after n03 crashed: a false positive", o, m, s)
			}
		}
	}
	if v := c.View("n00"); len(v) != 16 {
		t.Errorf("n00's table has %d members, want 16: Dead to the detector is not removed from the cluster", len(v))
	}
}

func TestRestart_NewGenerationIsAcceptedQuickly(t *testing.T) {
	names := tNames(16)
	cfg := Config{Fanout: 2, SuspectAfter: 5, DeadAfter: 10, DeadProbeEvery: 3, Seed: 9}
	c := tCluster(t, 16, cfg)
	for i := 0; i < 40; i++ {
		c.Round()
	}
	c.Stop("n07")
	if r := tRunUntil(c, 40, func() bool { return tAll(c, tWithout(names, "n07"), []string{"n07"}, Dead) }); r > 40 {
		t.Fatal("n07 never became Dead everywhere")
	}
	c.Restart("n07")
	r := tRunUntil(c, 12, func() bool { return tAll(c, names, names, Alive) })
	if r > 12 {
		v := c.View("n00")["n07"]
		t.Fatalf("12 rounds after the restart, n00 still sees n07 as %v with heartbeat %+v. The restarted node "+
			"counts from version 0 again; without comparing generations first, the cluster ignores it until it "+
			"catches up with its old version (≈ 50 rounds here)", v.Status, v.Heartbeat)
	}
	if hb := c.View("n00")["n07"].Heartbeat; hb.Generation != 1 {
		t.Errorf("n00 has n07's heartbeat %+v, want generation 1", hb)
	}
}

func TestPartition_EachSideConvictsTheOtherAndProbesHealIt(t *testing.T) {
	names := tNames(16)
	left, right := names[:8], names[8:]
	cfg := Config{Fanout: 2, SuspectAfter: 5, DeadAfter: 10, DeadProbeEvery: 4, Seed: 11}
	c := tCluster(t, 16, cfg)
	for i := 0; i < 20; i++ {
		c.Round()
	}
	c.Partition(left, right)
	r := tRunUntil(c, 40, func() bool { return tAll(c, left, right, Dead) && tAll(c, right, left, Dead) })
	if r > 40 {
		t.Fatal("after 40 rounds of partition, the two sides do not see each other as Dead")
	}
	if !tAll(c, left, left, Alive) || !tAll(c, right, right, Alive) {
		t.Fatal("nodes in the same partition must keep seeing each other as Alive")
	}
	c.Heal()
	// Each side considers the other Dead, so normal gossip never crosses;
	// only the dead-member probes can.
	r = tRunUntil(c, 2*cfg.DeadProbeEvery+15, func() bool { return tAll(c, names, names, Alive) })
	if r > 2*cfg.DeadProbeEvery+15 {
		t.Fatalf("%d rounds after the partition healed, the cluster is still split in two. Nodes must keep probing "+
			"members they consider Dead (every DeadProbeEvery rounds), or a healed partition never rejoins", 2*cfg.DeadProbeEvery+15)
	}
}

func TestCluster_ConcurrentReadersDuringRounds(t *testing.T) {
	names := tNames(24)
	c := tCluster(t, 24, Config{Fanout: 2, SuspectAfter: 5, DeadAfter: 10, DeadProbeEvery: 3, Seed: 2})
	var wg sync.WaitGroup
	stop := make(chan struct{})
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; ; i++ {
				select {
				case <-stop:
					return
				default:
				}
				o := names[(g+i)%len(names)]
				_ = c.View(o)
				c.Status(o, names[i%len(names)])
				_ = c.Messages()
			}
		}(g)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 100; i++ {
			c.Round()
			if i == 30 {
				c.Stop("n05")
			}
			if i == 60 {
				c.Restart("n05")
			}
		}
	}()
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("100 rounds did not finish within 20s (a deadlock between Round and the readers?)")
	}
	close(stop)
	wg.Wait()
	if c.Rounds() != 100 {
		t.Errorf("Rounds() = %d, want 100", c.Rounds())
	}
}

// ---------- phi accrual ----------

func TestPhi_GrowsLinearlyWithSilence(t *testing.T) {
	d, err := NewPhiDetector(100)
	if err != nil {
		t.Fatal(err)
	}
	if d.Phi(10) != 0 {
		t.Error("phi must be 0 before any interval is known")
	}
	for i := 0; i <= 10; i++ {
		d.Heartbeat(float64(i)) // one per second
	}
	if m := d.Mean(); math.Abs(m-1) > 1e-9 {
		t.Fatalf("Mean() = %v after heartbeats every second, want 1", m)
	}
	cases := []struct{ silence, want float64 }{
		{1, 0.4343},  // 1 / ln 10
		{2.3026, 1},  // one heartbeat in 10 is this late
		{18.42, 8.0}, // a common conviction threshold
	}
	for _, c := range cases {
		if got := d.Phi(10 + c.silence); math.Abs(got-c.want) > 0.01 {
			t.Errorf("phi after %.4gs of silence (mean 1s) = %.4f, want ≈ %.4f", c.silence, got, c.want)
		}
	}
	if d.Phi(9) != 0 {
		t.Error("phi at a time before the last heartbeat must be 0")
	}
}

func TestPhi_AdaptsToEachPeersRhythm(t *testing.T) {
	fast, _ := NewPhiDetector(10)
	slow, _ := NewPhiDetector(10)
	for i := 0; i <= 20; i++ {
		fast.Heartbeat(float64(i) * 0.1)
		slow.Heartbeat(float64(i) * 2)
	}
	pf := fast.Phi(2.0 + 1)
	ps := slow.Phi(40.0 + 1)
	if pf < 4 || ps > 0.3 {
		t.Errorf("1 s of silence: phi = %.2f for a peer that beats every 100 ms (want ≈ 4.3) and %.2f for one that "+
			"beats every 2 s (want ≈ 0.22): one threshold must mean different timeouts for different peers", pf, ps)
	}
	// The window: after ten slow intervals, the fast history is forgotten.
	for i := 1; i <= 10; i++ {
		fast.Heartbeat(2.0 + float64(i)*2)
	}
	if m := fast.Mean(); math.Abs(m-2) > 1e-9 {
		t.Errorf("Mean() = %v after 10 intervals of 2 s with window 10, want 2 (only the last window intervals count)", m)
	}
	fast.Heartbeat(5) // out of order: ignored
	if m := fast.Mean(); math.Abs(m-2) > 1e-9 {
		t.Errorf("an out-of-order heartbeat changed the mean to %v", m)
	}
	if _, err := NewPhiDetector(0); !errors.Is(err, ErrBadConfig) {
		t.Errorf("NewPhiDetector(0) error = %v, want ErrBadConfig", err)
	}
}

func TestPhi_ConcurrentHeartbeatsAndReads(t *testing.T) {
	d, _ := NewPhiDetector(50)
	var wg sync.WaitGroup
	for g := 0; g < 4; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 1000; i++ {
				d.Heartbeat(float64(g*1000 + i))
				_ = d.Phi(float64(g*1000+i) + 0.5)
				_ = d.Mean()
			}
		}(g)
	}
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("concurrent Heartbeat/Phi calls did not finish (deadlock?)")
	}
	if m := d.Mean(); m <= 0 {
		t.Errorf("Mean() = %v after 4,000 heartbeats, want > 0", m)
	}
}
