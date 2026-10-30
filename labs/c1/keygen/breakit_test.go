//go:build breakit

package keygen

import (
	"context"
	"fmt"
	"runtime"
	"sort"
	"sync"
	"testing"
)

// tRacyCounter is what a "counter" looks like when someone writes it as
// SELECT then UPDATE without a WHERE clause on the old value: a check, a
// pause, then an unconditional store. Run with:
//
//	go test -tags breakit -run BreakIt_RacyCounter -v ./labs/c1/keygen/
type tRacyCounter struct {
	mu sync.Mutex
	v  uint64
}

func (c *tRacyCounter) Load(context.Context) (uint64, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.v, nil
}

func (c *tRacyCounter) CompareAndSwap(_ context.Context, old, new uint64) (bool, error) {
	// No comparison: the "swap" is a blind write. Yield first so that other
	// allocators get to load the same old value, as they would across a
	// network round trip.
	runtime.Gosched()
	c.mu.Lock()
	defer c.mu.Unlock()
	c.v = new
	return true, nil
}

func TestBreakIt_RacyCounter(t *testing.T) {
	racy := &tRacyCounter{}
	a, err := NewAllocator(AllocatorConfig{Counter: racy, Size: 10, MaxRetries: 1000})
	if err != nil {
		t.Fatal(err)
	}
	const workers, each = 16, 50
	var mu sync.Mutex
	var ranges []Range
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < each; i++ {
				r, err := a.Reserve(context.Background())
				if err != nil {
					t.Errorf("Reserve: %v", err)
					return
				}
				mu.Lock()
				ranges = append(ranges, r)
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	sort.Slice(ranges, func(i, j int) bool { return ranges[i].Start < ranges[j].Start })
	dups := 0
	for i := 1; i < len(ranges); i++ {
		if ranges[i].Start == ranges[i-1].Start {
			dups++
		}
	}
	t.Logf("%d reservations from a check-then-write counter: %d ranges handed out twice (%.1f%%); every one of them is %d duplicate short codes",
		len(ranges), dups, 100*float64(dups)/float64(len(ranges)), 10)
}

// BreakIt_StoreOpsPerID shows what the range size buys: counter operations
// per id handed out, and the ids a crash can waste.
//
//	go test -tags breakit -run BreakIt_StoreOps -v ./labs/c1/keygen/
func TestBreakIt_StoreOpsPerID(t *testing.T) {
	const ids = 10_000
	fmt.Printf("%-12s %-14s %-18s %s\n", "range size", "counter ops", "ops per 1000 ids", "ids lost by one crash (max)")
	for _, size := range []uint64{1, 10, 100, 1000, 10000} {
		c := NewMemoryCounter(0)
		a, _ := NewAllocator(AllocatorConfig{Counter: c, Size: size})
		g := NewGenerator(a)
		for i := 0; i < ids; i++ {
			if _, err := g.Next(context.Background()); err != nil {
				t.Fatal(err)
			}
		}
		loads, swaps := c.Ops()
		fmt.Printf("%-12d %-14d %-18.1f %d\n", size, loads+swaps, float64(loads+swaps)/ids*1000, size)
	}
}

// BreakIt_RandomCollisionRate fills a small keyspace and measures inserts per
// claimed code against the 1/(1-p) prediction.
//
//	go test -tags breakit -run BreakIt_RandomCollision -v ./labs/c1/keygen/
func TestBreakIt_RandomCollisionRate(t *testing.T) {
	const length = 3 // 238,328 codes
	ins := NewMemoryInserter()
	g, _ := NewRandomGenerator(RandomConfig{Inserter: ins, Length: length, MaxAttempts: 1000})
	total := int(Capacity(length))
	fmt.Printf("%-12s %-16s %-16s %s\n", "occupancy", "inserts/code", "predicted", "max attempts seen")
	for _, target := range []float64{0.10, 0.30, 0.50, 0.70, 0.90} {
		n := int(target*float64(total)) - ins.Len()
		attempts, worst := 0, 0
		for i := 0; i < n; i++ {
			_, k, err := g.Next(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			attempts += k
			if k > worst {
				worst = k
			}
		}
		p := CollisionProbability(length, float64(ins.Len())-float64(n)/2) // mid-batch occupancy
		fmt.Printf("%-12s %-16.2f %-16.2f %d\n", fmt.Sprintf("%.0f%%", 100*float64(ins.Len())/float64(total)), float64(attempts)/float64(n), 1/(1-p), worst)
	}
}
