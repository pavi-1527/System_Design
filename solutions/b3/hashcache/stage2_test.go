package hashcache

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

var errBoom = errors.New("backend exploded")

// fakeClock is a manually advanced clock.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func newFakeClock() *fakeClock { return &fakeClock{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)} }

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// fakeBackend is an in-memory cache node with failure injection and counters.
type fakeBackend struct {
	mu      sync.Mutex
	data    map[string][]byte
	ttls    map[string]time.Duration
	failing bool
	onGet   func(key string) // runs after the lookup, before Get returns

	gets, sets, adds, deletes atomic.Int64
}

func newFake() *fakeBackend {
	return &fakeBackend{data: map[string][]byte{}, ttls: map[string]time.Duration{}}
}

func (f *fakeBackend) setFailing(v bool) {
	f.mu.Lock()
	f.failing = v
	f.mu.Unlock()
}

func (f *fakeBackend) fail(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failing {
		return errBoom
	}
	return nil
}

func (f *fakeBackend) Get(ctx context.Context, key string) ([]byte, error) {
	f.gets.Add(1)
	if err := f.fail(ctx); err != nil {
		return nil, err
	}
	f.mu.Lock()
	v, ok := f.data[key]
	hook := f.onGet
	f.mu.Unlock()
	if hook != nil {
		hook(key)
	}
	if !ok {
		return nil, ErrNotFound
	}
	return append([]byte(nil), v...), nil
}

func (f *fakeBackend) Set(ctx context.Context, key string, val []byte, ttl time.Duration) error {
	f.sets.Add(1)
	if err := f.fail(ctx); err != nil {
		return err
	}
	f.mu.Lock()
	f.data[key], f.ttls[key] = append([]byte(nil), val...), ttl
	f.mu.Unlock()
	return nil
}

func (f *fakeBackend) Add(ctx context.Context, key string, val []byte, ttl time.Duration) error {
	f.adds.Add(1)
	if err := f.fail(ctx); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.data[key]; ok {
		return ErrExists
	}
	f.data[key], f.ttls[key] = append([]byte(nil), val...), ttl
	return nil
}

func (f *fakeBackend) Delete(ctx context.Context, key string) error {
	f.deletes.Add(1)
	if err := f.fail(ctx); err != nil {
		return err
	}
	f.mu.Lock()
	delete(f.data, key)
	delete(f.ttls, key)
	f.mu.Unlock()
	return nil
}

func (f *fakeBackend) has(key string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.data[key]
	return ok
}

func (f *fakeBackend) value(key string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return string(f.data[key])
}

func (f *fakeBackend) ttl(key string) time.Duration {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.ttls[key]
}

func (f *fakeBackend) totalGets() int64 { return f.gets.Load() }

type cluster struct {
	fakes    map[string]*fakeBackend
	backends map[string]Backend
	clock    *fakeClock
}

func newCluster(n int) *cluster {
	cl := &cluster{fakes: map[string]*fakeBackend{}, backends: map[string]Backend{}, clock: newFakeClock()}
	for i := 0; i < n; i++ {
		cl.add(nodeN(i))
	}
	return cl
}

func (cl *cluster) add(name string) *fakeBackend {
	f := newFake()
	cl.fakes[name] = f
	cl.backends[name] = f
	return f
}

func (cl *cluster) subset(names ...string) map[string]Backend {
	out := map[string]Backend{}
	for _, n := range names {
		out[n] = cl.backends[n]
	}
	return out
}

func (cl *cluster) client(t *testing.T, cfg Config, names ...string) *Client {
	t.Helper()
	cfg.Now = cl.clock.Now
	bs := cl.backends
	if len(names) > 0 {
		bs = cl.subset(names...)
	}
	c, err := NewClient(cfg, bs)
	if err != nil {
		t.Fatalf("NewClient = %v, want nil", err)
	}
	return c
}

// ringFor builds the ring a Client with the given vnodes must be using.
func ringFor(t *testing.T, vnodes int, names ...string) *Ring {
	t.Helper()
	r := NewRing(vnodes)
	for _, n := range names {
		if err := r.Add(n, 1); err != nil {
			t.Fatal(err)
		}
	}
	return r
}

func namesOf(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = nodeN(i)
	}
	return out
}

func wantIs(t *testing.T, what string, got, want error) {
	t.Helper()
	if !errors.Is(got, want) {
		t.Fatalf("%s = %v, want an error satisfying errors.Is(err, %v)", what, got, want)
	}
}

func mustSet(t *testing.T, c *Client, key, val string) {
	t.Helper()
	if err := c.Set(context.Background(), key, []byte(val), time.Minute); err != nil {
		t.Fatalf("Set(%q) = %v, want nil", key, err)
	}
}

func mustGet(t *testing.T, c *Client, key, want string) {
	t.Helper()
	got, err := c.Get(context.Background(), key)
	if err != nil {
		t.Fatalf("Get(%q) = %v, want %q", key, err, want)
	}
	if string(got) != want {
		t.Fatalf("Get(%q) = %q, want %q", key, got, want)
	}
}

// ---------------------------------------------------------------- Stage 2

func TestStage2_NewClientValidatesBackends(t *testing.T) {
	_, err := NewClient(Config{}, map[string]Backend{})
	wantIs(t, "NewClient with no backends", err, ErrNoNodes)
	if _, err := NewClient(Config{}, map[string]Backend{"a": nil}); err == nil {
		t.Fatal("NewClient with a nil backend returned nil error")
	}
}

func TestStage2_KeysLiveOnlyOnTheirRingOwner(t *testing.T) {
	cl := newCluster(4)
	c := cl.client(t, Config{Vnodes: 100})
	ring := ringFor(t, 100, namesOf(4)...)
	ctx := context.Background()
	for i := 0; i < 300; i++ {
		key := keyN(i)
		if err := c.Set(ctx, key, []byte("v-"+key), 90*time.Second); err != nil {
			t.Fatalf("Set(%q) = %v", key, err)
		}
		owner, _ := ring.Get(key)
		for name, f := range cl.fakes {
			if f.has(key) != (name == owner) {
				t.Fatalf("key %q: node %s has=%v but the ring owner is %s (R=1: exactly the owner stores it)", key, name, f.has(key), owner)
			}
		}
		if got := cl.fakes[owner].ttl(key); got != 90*time.Second {
			t.Fatalf("owner stored %q with ttl %v, want the caller's 90s", key, got)
		}
	}
	for i := 0; i < 300; i++ {
		mustGet(t, c, keyN(i), "v-"+keyN(i))
	}
	if _, err := c.Get(ctx, "never-written"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get of an absent key = %v, want ErrNotFound", err)
	}
}

func TestStage2_ReplicationWritesToRDistinctReplicas(t *testing.T) {
	cl := newCluster(5)
	c := cl.client(t, Config{Vnodes: 100, Replicas: 3, WriteQuorum: 2})
	ring := ringFor(t, 100, namesOf(5)...)
	for i := 0; i < 100; i++ {
		key := keyN(i)
		mustSet(t, c, key, "v")
		want := map[string]bool{}
		for _, n := range ring.Replicas(key, 3) {
			want[n] = true
		}
		for name, f := range cl.fakes {
			if f.has(key) != want[name] {
				t.Fatalf("key %q: node %s has=%v, want %v (R=3 replicas = the next 3 distinct nodes clockwise)", key, name, f.has(key), want[name])
			}
		}
	}
}

func TestStage2_GetFallsBackToLaterReplicasOnMiss(t *testing.T) {
	cl := newCluster(4)
	c := cl.client(t, Config{Vnodes: 100, Replicas: 2})
	ring := ringFor(t, 100, namesOf(4)...)
	key := "fallback-key"
	mustSet(t, c, key, "hello")
	rs := ring.Replicas(key, 2)
	cl.fakes[rs[0]].mu.Lock()
	delete(cl.fakes[rs[0]].data, key) // the primary lost it (restart, eviction)
	cl.fakes[rs[0]].mu.Unlock()
	mustGet(t, c, key, "hello")
	if g0, g1 := cl.fakes[rs[0]].totalGets(), cl.fakes[rs[1]].totalGets(); g0 != 1 || g1 != 1 {
		t.Fatalf("reads: primary %d, secondary %d; want 1 and 1 (primary first, then fall back)", g0, g1)
	}
}

func TestStage2_GetFallsBackOnError(t *testing.T) {
	cl := newCluster(4)
	c := cl.client(t, Config{Vnodes: 100, Replicas: 2})
	ring := ringFor(t, 100, namesOf(4)...)
	key := "error-fallback"
	mustSet(t, c, key, "hello")
	cl.fakes[ring.Replicas(key, 2)[0]].setFailing(true)
	mustGet(t, c, key, "hello")
}

func TestStage2_MissCostsOneReadPerReplicaAndNoMore(t *testing.T) {
	cl := newCluster(5)
	c := cl.client(t, Config{Vnodes: 100, Replicas: 2})
	ring := ringFor(t, 100, namesOf(5)...)
	key := "absent"
	if _, err := c.Get(context.Background(), key); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get = %v, want ErrNotFound", err)
	}
	in := map[string]bool{}
	for _, n := range ring.Replicas(key, 2) {
		in[n] = true
	}
	for name, f := range cl.fakes {
		want := int64(0)
		if in[name] {
			want = 1
		}
		if f.totalGets() != want {
			t.Fatalf("node %s served %d reads for a miss, want %d (R=2: each replica once, nobody else)", name, f.totalGets(), want)
		}
	}
}

func TestStage2_GetIsUnavailableWhenNoReplicaCanAnswer(t *testing.T) {
	cl := newCluster(3)
	c := cl.client(t, Config{Replicas: 2})
	for _, f := range cl.fakes {
		f.setFailing(true)
	}
	_, err := c.Get(context.Background(), "k")
	wantIs(t, "Get with every node failing", err, ErrUnavailable)
	if errors.Is(err, ErrNotFound) {
		t.Fatal("a failure to answer must not be reported as a miss (callers would repopulate from the database during an outage)")
	}
}

func TestStage2_WriteQuorum(t *testing.T) {
	for _, tc := range []struct {
		w       int
		wantErr bool
	}{{w: 2, wantErr: true}, {w: 1, wantErr: false}} {
		cl := newCluster(4)
		c := cl.client(t, Config{Vnodes: 100, Replicas: 2, WriteQuorum: tc.w})
		ring := ringFor(t, 100, namesOf(4)...)
		key := fmt.Sprintf("quorum-%d", tc.w)
		rs := ring.Replicas(key, 2)
		cl.fakes[rs[0]].setFailing(true)
		err := c.Set(context.Background(), key, []byte("v"), time.Minute)
		if tc.wantErr {
			wantIs(t, "Set with W=2 and one replica failing", err, ErrQuorum)
		} else if err != nil {
			t.Fatalf("Set with W=1 and one healthy replica = %v, want nil", err)
		}
		if !cl.fakes[rs[1]].has(key) {
			t.Fatalf("W=%d: the healthy replica must still receive the write even when quorum fails (never stop early or skip replicas)", tc.w)
		}
	}
}

func TestStage2_DeleteRemovesFromEveryReplica(t *testing.T) {
	cl := newCluster(4)
	c := cl.client(t, Config{Vnodes: 100, Replicas: 3})
	mustSet(t, c, "k", "v")
	if err := c.Delete(context.Background(), "k"); err != nil {
		t.Fatalf("Delete = %v", err)
	}
	for name, f := range cl.fakes {
		if f.has("k") {
			t.Fatalf("node %s still holds the deleted key", name)
		}
	}
	if _, err := c.Get(context.Background(), "k"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get after Delete = %v, want ErrNotFound", err)
	}
	if err := c.Delete(context.Background(), "never-existed"); err != nil {
		t.Fatalf("Delete of an absent key = %v, want nil", err)
	}
}

func TestStage2_NodeIsMarkedDownAfterConsecutiveFailuresAndSkipped(t *testing.T) {
	cl := newCluster(4)
	c := cl.client(t, Config{Vnodes: 100, FailThreshold: 3, DownFor: 10 * time.Second})
	ring := ringFor(t, 100, namesOf(4)...)
	key := "failover"
	rs := ring.Replicas(key, 2)
	primary, next := cl.fakes[rs[0]], cl.fakes[rs[1]]
	primary.setFailing(true)

	for i := 1; i <= 3; i++ {
		if !c.Healthy(rs[0]) {
			t.Fatalf("node marked down after only %d failures, threshold is 3", i-1)
		}
		if err := c.Set(context.Background(), key, []byte("v"), time.Minute); err == nil {
			t.Fatalf("Set #%d succeeded although the only replica (R=1) is failing", i)
		}
	}
	if c.Healthy(rs[0]) {
		t.Fatal("node still healthy after 3 consecutive failures")
	}
	before := primary.sets.Load()
	mustSet(t, c, key, "v2") // fails over to the next node clockwise
	if primary.sets.Load() != before {
		t.Fatal("a node marked down still received a request: skip it until DownFor has passed")
	}
	if next.value(key) != "v2" {
		t.Fatalf("with the owner down the key should live on the next node clockwise (%s)", rs[1])
	}
	mustGet(t, c, key, "v2")
}

func TestStage2_DownNodeIsProbedAfterDownForAndSuccessResetsTheCount(t *testing.T) {
	cl := newCluster(3)
	c := cl.client(t, Config{Vnodes: 100, FailThreshold: 3, DownFor: 10 * time.Second})
	ring := ringFor(t, 100, namesOf(3)...)
	key := "probe"
	owner := cl.fakes[ring.Replicas(key, 1)[0]]
	ownerName := ring.Replicas(key, 1)[0]
	ctx := context.Background()
	set := func() error { return c.Set(ctx, key, []byte("v"), time.Minute) }

	// Two failures, a success, two failures: never three in a row, so never down.
	owner.setFailing(true)
	_ = set()
	_ = set()
	owner.setFailing(false)
	if err := set(); err != nil {
		t.Fatal(err)
	}
	owner.setFailing(true)
	_ = set()
	_ = set()
	if !c.Healthy(ownerName) {
		t.Fatal("node marked down after 2+2 failures separated by a success: the threshold counts CONSECUTIVE failures")
	}

	_ = set() // third consecutive failure
	if c.Healthy(ownerName) {
		t.Fatal("node should be down after 3 consecutive failures")
	}
	cl.clock.Advance(9 * time.Second)
	if c.Healthy(ownerName) {
		t.Fatal("node came back before DownFor elapsed")
	}
	cl.clock.Advance(2 * time.Second)
	if !c.Healthy(ownerName) {
		t.Fatal("node still down after DownFor elapsed")
	}

	// A probe that fails marks the node down again at once (no fresh 3 strikes)...
	before := owner.sets.Load()
	_ = set()
	if owner.sets.Load() != before+1 {
		t.Fatal("after DownFor the node should receive a probe request")
	}
	if c.Healthy(ownerName) {
		t.Fatal("a failed probe must mark the node down immediately")
	}
	// ...and a probe that succeeds restores it.
	cl.clock.Advance(11 * time.Second)
	owner.setFailing(false)
	if err := set(); err != nil {
		t.Fatalf("Set after recovery = %v", err)
	}
	if !c.Healthy(ownerName) || owner.value(key) != "v" {
		t.Fatal("recovered node should be healthy and own the key again")
	}
}

func TestStage2_CleanMissesAndCancelledContextsDoNotMarkANodeDown(t *testing.T) {
	cl := newCluster(2)
	c := cl.client(t, Config{FailThreshold: 2})
	ring := ringFor(t, DefaultVnodes, namesOf(2)...)
	key := "k"
	owner, _ := ring.Get(key)
	for i := 0; i < 10; i++ {
		if _, err := c.Get(context.Background(), key); !errors.Is(err, ErrNotFound) {
			t.Fatalf("Get = %v, want ErrNotFound", err)
		}
	}
	if !c.Healthy(owner) {
		t.Fatal("misses are successful round trips and must not count as failures")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for i := 0; i < 10; i++ {
		_, _ = c.Get(ctx, key)
		_ = c.Set(ctx, key, []byte("v"), time.Minute)
	}
	if !c.Healthy(owner) {
		t.Fatal("errors caused by the caller's own cancelled context say nothing about the node and must not mark it down")
	}
}

func TestStage2_ConcurrentUseWithAFlappingNode(t *testing.T) {
	cl := newCluster(5)
	c := cl.client(t, Config{Vnodes: 50, Replicas: 2, WriteQuorum: 1, FailThreshold: 2, DownFor: time.Millisecond})
	var wg sync.WaitGroup
	stop := make(chan struct{})
	wg.Add(1)
	go func() { // node-0 flaps
		defer wg.Done()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			cl.fakes[nodeN(0)].setFailing(i%2 == 0)
			cl.clock.Advance(time.Millisecond)
		}
	}()
	var workers sync.WaitGroup
	for g := 0; g < 8; g++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			r := rand.New(rand.NewSource(int64(g)))
			ctx := context.Background()
			for i := 0; i < 400; i++ {
				key := keyN(r.Intn(60))
				switch r.Intn(3) {
				case 0:
					_ = c.Set(ctx, key, []byte("v-"+key), time.Minute)
				case 1:
					if v, err := c.Get(ctx, key); err == nil && string(v) != "v-"+key {
						t.Errorf("Get(%q) = %q: a value belonging to another key", key, v)
						return
					}
				default:
					_ = c.Delete(ctx, key)
				}
				_ = c.Healthy(nodeN(r.Intn(5)))
			}
		}()
	}
	within(t, "concurrent clients", workers.Wait)
	close(stop)
	wg.Wait()
}
