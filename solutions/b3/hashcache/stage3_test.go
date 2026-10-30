package hashcache

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"math"
	"reflect"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// movedKeys returns the keys whose owner differs between two rings.
func movedKeys(t *testing.T, a, b *Ring, keys []string) []string {
	t.Helper()
	var out []string
	for _, k := range keys {
		x, _ := a.Get(k)
		y, _ := b.Get(k)
		if x != y {
			out = append(out, k)
		}
	}
	return out
}

// modClient is the naive baseline: node = hash(key) % len(nodes).
type modClient struct{ nodes []Backend }

func (m *modClient) pick(key string) Backend {
	h := fnv.New32a()
	_, _ = h.Write([]byte(key))
	return m.nodes[int(h.Sum32())%len(m.nodes)]
}

func hitRatio(t *testing.T, keys []string, get func(string) ([]byte, error)) float64 {
	t.Helper()
	hits := 0
	for _, k := range keys {
		v, err := get(k)
		switch {
		case err == nil:
			if string(v) != "v-"+k {
				t.Fatalf("Get(%q) = %q: a value belonging to another key", k, v)
			}
			hits++
		case errors.Is(err, ErrNotFound):
		default:
			t.Fatalf("Get(%q) = %v", k, err)
		}
	}
	return float64(hits) / float64(len(keys))
}

// ---------------------------------------------------------------- Stage 3

func TestStage3_SetMembershipRejectsAnEmptySetAndChangesNothing(t *testing.T) {
	cl := newCluster(3)
	c := cl.client(t, Config{})
	mustSet(t, c, "k", "v")
	wantIs(t, "SetMembership(empty)", c.SetMembership(map[string]Backend{}, Migration{}), ErrNoNodes)
	mustGet(t, c, "k", "v")
}

func TestStage3_ResizeKeepsAboutNOverNPlusOneOfTheCacheWhereModNLosesMost(t *testing.T) {
	keys := keyList(10000)
	ctx := context.Background()

	// Consistent hashing: 4 -> 5 nodes, no migration.
	cl := newCluster(5)
	c := cl.client(t, Config{}, namesOf(4)...)
	for _, k := range keys {
		if err := c.Set(ctx, k, []byte("v-"+k), time.Hour); err != nil {
			t.Fatal(err)
		}
	}
	if err := c.SetMembership(cl.subset(namesOf(5)...), Migration{}); err != nil {
		t.Fatalf("SetMembership = %v", err)
	}
	consistent := hitRatio(t, keys, func(k string) ([]byte, error) { return c.Get(ctx, k) })

	// Baseline: hash % N, 4 -> 5 nodes.
	var four []Backend
	for i := 0; i < 4; i++ {
		four = append(four, newFake())
	}
	naive := &modClient{nodes: four}
	for _, k := range keys {
		_ = naive.pick(k).Set(ctx, k, []byte("v-"+k), time.Hour)
	}
	naive.nodes = append(naive.nodes, newFake())
	modN := hitRatio(t, keys, func(k string) ([]byte, error) { return naive.pick(k).Get(ctx, k) })

	if consistent < 0.72 || consistent > 0.88 {
		t.Fatalf("after adding a 5th node the consistent-hash client still hits %.1f%% of keys, want ≈ 4/5 = 80%% (between 72%% and 88%%)", consistent*100)
	}
	if modN > 0.3 {
		t.Fatalf("test baseline broken: hash%%N kept %.1f%% of keys, expected ≈ 20%%", modN*100)
	}
	t.Logf("hit ratio after 4->5 nodes: consistent %.1f%%, hash%%N %.1f%%", consistent*100, modN*100)
}

func TestStage3_LazyMigrationRecoversMovedKeysOnReadAndOnlyOnce(t *testing.T) {
	keys := keyList(10000)
	ctx := context.Background()
	cl := newCluster(5)
	c := cl.client(t, Config{}, namesOf(4)...)
	for _, k := range keys {
		mustSetQuiet(t, c, k)
	}
	err := c.SetMembership(cl.subset(namesOf(5)...), Migration{Grace: time.Minute, TTL: 7 * time.Second})
	if err != nil {
		t.Fatal(err)
	}

	if r := hitRatio(t, keys, func(k string) ([]byte, error) { return c.Get(ctx, k) }); r != 1 {
		t.Fatalf("with a grace window every key must still be readable, got %.1f%% hits: a miss on the new owner should fall back to the previous owner", r*100)
	}
	newNode := cl.fakes[nodeN(4)]
	migrated := float64(newNode.adds.Load()) / float64(len(keys))
	if migrated < 0.12 || migrated > 0.28 {
		t.Fatalf("%.1f%% of keys were copied to the new node, want ≈ 20%% (between 12%% and 28%%): copy only keys that moved", migrated*100)
	}
	var sample string
	newNode.mu.Lock()
	for k := range newNode.data {
		sample = k
		break
	}
	newNode.mu.Unlock()
	if got := newNode.ttl(sample); got != 7*time.Second {
		t.Fatalf("migrated entry %q has ttl %v, want Migration.TTL = 7s", sample, got)
	}

	var before int64
	for _, f := range cl.fakes {
		before += f.totalGets()
	}
	if r := hitRatio(t, keys, func(k string) ([]byte, error) { return c.Get(ctx, k) }); r != 1 {
		t.Fatalf("second pass hit ratio %.1f%%, want 100%%", r*100)
	}
	var after int64
	for _, f := range cl.fakes {
		after += f.totalGets()
	}
	if after-before != int64(len(keys)) {
		t.Fatalf("second pass issued %d backend reads for %d keys: once migrated, a key must be served by its new owner alone (copy-forward on the first read)", after-before, len(keys))
	}
}

func mustSetQuiet(t *testing.T, c *Client, k string) {
	t.Helper()
	if err := c.Set(context.Background(), k, []byte("v-"+k), time.Hour); err != nil {
		t.Fatalf("Set(%q) = %v", k, err)
	}
}

func TestStage3_GraceWindowExpires(t *testing.T) {
	cl := newCluster(5)
	c := cl.client(t, Config{}, namesOf(4)...)
	oldRing, newRing := ringFor(t, DefaultVnodes, namesOf(4)...), ringFor(t, DefaultVnodes, namesOf(5)...)
	moved := movedKeys(t, oldRing, newRing, keyList(2000))
	if len(moved) < 100 {
		t.Fatalf("test setup: only %d keys moved", len(moved))
	}
	key := moved[0]
	mustSet(t, c, key, "v")
	if err := c.SetMembership(cl.subset(namesOf(5)...), Migration{Grace: 10 * time.Second}); err != nil {
		t.Fatal(err)
	}
	cl.clock.Advance(11 * time.Second)
	var before int64
	for _, f := range cl.fakes {
		before += f.totalGets()
	}
	if _, err := c.Get(context.Background(), key); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get after the grace window = %v, want ErrNotFound (the previous owners are no longer consulted)", err)
	}
	var after int64
	for _, f := range cl.fakes {
		after += f.totalGets()
	}
	if after-before != 1 {
		t.Fatalf("Get after the grace window issued %d backend reads, want 1", after-before)
	}
}

func TestStage3_AGenuineMissOnAnUnmovedKeyIsNotReadTwice(t *testing.T) {
	cl := newCluster(5)
	c := cl.client(t, Config{}, namesOf(4)...)
	oldRing, newRing := ringFor(t, DefaultVnodes, namesOf(4)...), ringFor(t, DefaultVnodes, namesOf(5)...)
	var key string
	for _, k := range keyList(500) {
		a, _ := oldRing.Get(k)
		b, _ := newRing.Get(k)
		if a == b {
			key = k
			break
		}
	}
	if err := c.SetMembership(cl.subset(namesOf(5)...), Migration{Grace: time.Minute}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Get(context.Background(), key); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get = %v, want ErrNotFound", err)
	}
	var total int64
	for _, f := range cl.fakes {
		total += f.totalGets()
	}
	if total != 1 {
		t.Fatalf("a miss on a key whose owner did not change cost %d backend reads, want 1: the previous owner is the same node, so asking again is wasted load", total)
	}
}

func TestStage3_RemovedNodeStaysReadableDuringGraceButGetsNoWrites(t *testing.T) {
	cl := newCluster(4)
	c := cl.client(t, Config{})
	ctx := context.Background()
	keys := keyList(2000)
	for _, k := range keys {
		mustSetQuiet(t, c, k)
	}
	if err := c.SetMembership(cl.subset(namesOf(3)...), Migration{Grace: time.Minute}); err != nil {
		t.Fatal(err)
	}
	if r := hitRatio(t, keys, func(k string) ([]byte, error) { return c.Get(ctx, k) }); r != 1 {
		t.Fatalf("hit ratio %.1f%% after removing a node with a grace window, want 100%%: its backend must stay readable", r*100)
	}
	if cl.fakes[nodeN(3)].gets.Load() == 0 {
		t.Fatal("the removed node was never read, yet it held ≈ 1/4 of the keys")
	}
	sets, adds := cl.fakes[nodeN(3)].sets.Load(), cl.fakes[nodeN(3)].adds.Load()
	for i := 0; i < 500; i++ {
		mustSetQuiet(t, c, fmt.Sprintf("fresh-%d", i))
	}
	if cl.fakes[nodeN(3)].sets.Load() != sets || cl.fakes[nodeN(3)].adds.Load() != adds {
		t.Fatal("the removed node received writes after SetMembership: only reads are allowed during the grace window")
	}
}

func TestStage3_WritesDuringGraceCannotBeShadowedOrResurrectedByTheOldCopy(t *testing.T) {
	oldRing, newRing := ringFor(t, DefaultVnodes, namesOf(4)...), ringFor(t, DefaultVnodes, namesOf(5)...)
	moved := movedKeys(t, oldRing, newRing, keyList(2000))
	for name, run := range map[string]func(c *Client, key string){
		"delete": func(c *Client, key string) {
			if err := c.Delete(context.Background(), key); err != nil {
				t.Fatalf("Delete = %v", err)
			}
			if _, err := c.Get(context.Background(), key); !errors.Is(err, ErrNotFound) {
				t.Fatalf("Get after Delete during grace = %v, want ErrNotFound: the old owner's copy was migrated back (a resurrected delete)", err)
			}
		},
		"overwrite": func(c *Client, key string) {
			mustSet(t, c, key, "v2")
			mustGet(t, c, key, "v2")
		},
	} {
		cl := newCluster(5)
		c := cl.client(t, Config{}, namesOf(4)...)
		key := moved[0]
		mustSet(t, c, key, "v1")
		oldOwner, _ := oldRing.Get(key)
		if err := c.SetMembership(cl.subset(namesOf(5)...), Migration{Grace: time.Minute}); err != nil {
			t.Fatal(err)
		}
		run(c, key)
		if cl.fakes[oldOwner].has(key) {
			t.Fatalf("%s: the previous owner %s still holds its stale copy; a write during the grace window must remove it", name, oldOwner)
		}
	}
}

func TestStage3_MigrationNeverOverwritesANewerValue(t *testing.T) {
	oldRing, newRing := ringFor(t, DefaultVnodes, namesOf(4)...), ringFor(t, DefaultVnodes, namesOf(5)...)
	key := movedKeys(t, oldRing, newRing, keyList(2000))[0]
	oldOwner, _ := oldRing.Get(key)
	cl := newCluster(5)
	c := cl.client(t, Config{}, namesOf(4)...)
	mustSet(t, c, key, "v1")
	if err := c.SetMembership(cl.subset(namesOf(5)...), Migration{Grace: time.Minute}); err != nil {
		t.Fatal(err)
	}
	// The reader has fetched v1 from the old owner when a writer stores v2 on
	// the new owner. The reader then copies v1 forward: that must not win.
	var once sync.Once
	cl.fakes[oldOwner].onGet = func(k string) {
		once.Do(func() {
			if err := c.Set(context.Background(), k, []byte("v2"), time.Minute); err != nil {
				t.Errorf("Set during migration = %v", err)
			}
		})
	}
	_, _ = c.Get(context.Background(), key)
	cl.fakes[oldOwner].onGet = nil
	mustGet(t, c, key, "v2")
	if got := cl.fakes[nodeN(4)].value(key); got != "v2" {
		t.Fatalf("new owner holds %q, want v2: copy-forward must use Add (set-if-absent), never Set", got)
	}
}

func TestStage3_AssignBoundedCapsEveryNodeAndOverflowsClockwise(t *testing.T) {
	ring := ringFor(t, 100, namesOf(5)...)
	hot := nodeN(0)
	var keys []string
	for i := 0; len(keys) < 3000; i++ {
		if k := fmt.Sprintf("hot-%d", i); func() bool { n, _ := ring.Get(k); return n == hot }() {
			keys = append(keys, k)
		}
	}
	for i := 0; i < 1000; i++ {
		keys = append(keys, fmt.Sprintf("cold-%d", i))
	}
	capacity := int(math.Ceil(1.25 * float64(len(keys)) / 5))

	if natural := loads(ring, keys)[hot]; natural <= capacity {
		t.Fatalf("test setup: the hot node naturally gets %d keys, not above the cap %d", natural, capacity)
	}
	got, err := ring.AssignBounded(keys, 1.25)
	if err != nil {
		t.Fatalf("AssignBounded = %v", err)
	}
	if len(got) != len(keys) {
		t.Fatalf("assigned %d of %d keys", len(got), len(keys))
	}
	load := map[string]int{}
	for _, n := range got {
		load[n]++
	}
	for n, l := range load {
		if l > capacity {
			t.Fatalf("node %s received %d keys, over the bound ceil(1.25 × %d / 5) = %d", n, l, len(keys), capacity)
		}
	}
	if load[hot] != capacity {
		t.Fatalf("the hot node holds %d keys, want exactly the cap %d (it should fill up before spilling)", load[hot], capacity)
	}
	// A key may leave its natural owner only because every node before its
	// assigned node, clockwise, is full.
	for _, k := range keys {
		for _, n := range ring.Replicas(k, 5) {
			if n == got[k] {
				break
			}
			if load[n] != capacity {
				t.Fatalf("key %q was placed on %s although %s, earlier clockwise, still has room (%d/%d)", k, got[k], n, load[n], capacity)
			}
		}
	}
	again, _ := ring.AssignBounded(keys, 1.25)
	if !reflect.DeepEqual(got, again) {
		t.Fatal("AssignBounded is not deterministic for the same input order")
	}
}

func TestStage3_AssignBoundedValidatesAndIgnoresDuplicates(t *testing.T) {
	ring := ringFor(t, 50, namesOf(3)...)
	if _, err := ring.AssignBounded([]string{"a"}, 0.9); !errors.Is(err, ErrInvalidFactor) {
		t.Fatalf("factor 0.9 = %v, want ErrInvalidFactor", err)
	}
	if _, err := NewRing(10).AssignBounded([]string{"a"}, 1.25); !errors.Is(err, ErrEmptyRing) {
		t.Fatalf("empty ring = %v, want ErrEmptyRing", err)
	}
	dup := []string{"a", "b", "a", "c", "b", "a"}
	got, err := ring.AssignBounded(dup, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("assigned %d distinct keys, want 3", len(got))
	}
	load := map[string]int{}
	for _, n := range got {
		load[n]++
	}
	for n, l := range load {
		if l > 1 {
			t.Fatalf("with factor 1 and 3 keys on 3 nodes every node holds at most 1 key; %s holds %d (capacity must be computed over DISTINCT keys)", n, l)
		}
	}
}

func TestStage3_MembershipChangesRaceWithTraffic(t *testing.T) {
	cl := newCluster(6)
	c := cl.client(t, Config{Vnodes: 50}, namesOf(3)...)
	const workers, keysPer = 8, 20
	var iters atomic.Int64
	var done atomic.Bool
	var wg sync.WaitGroup
	for g := 0; g < workers; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx := context.Background()
			ver := make([]int, keysPer)
			check := func(k int, exact bool) {
				key := fmt.Sprintf("w%d-k%d", g, k)
				ver[k]++
				want := fmt.Sprintf("%s=v%d", key, ver[k])
				if err := c.Set(ctx, key, []byte(want), time.Hour); err != nil {
					t.Errorf("Set(%q) = %v", key, err)
					return
				}
				got, err := c.Get(ctx, key)
				switch {
				case err == nil && string(got) != want:
					t.Errorf("Get(%q) = %q right after Set(%q): a stale or foreign value", key, got, want)
				case err != nil && exact:
					t.Errorf("Get(%q) = %v after membership settled, want %q: a write was lost", key, err, want)
				case err != nil && !errors.Is(err, ErrNotFound):
					t.Errorf("Get(%q) = %v", key, err)
				}
			}
			for step := 0; !done.Load(); step++ {
				check(step%keysPer, false)
				iters.Add(1)
			}
			for k := 0; k < keysPer; k++ {
				check(k, true)
			}
		}()
	}
	resize := func() {
		for i, n := range []int{4, 5, 6} {
			for iters.Load() < int64((i+1)*400) {
				runtime.Gosched()
			}
			if err := c.SetMembership(cl.subset(namesOf(n)...), Migration{Grace: time.Hour}); err != nil {
				t.Errorf("SetMembership = %v", err)
			}
		}
		done.Store(true)
	}
	within(t, "membership changes under traffic", func() { resize(); wg.Wait() })
}
