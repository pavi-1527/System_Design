//go:build breakit

package retry

// Break-it experiments for A7 Unit 1 §2–§3. These are measurements, not
// pass/fail tests: they print tables so you can compare policies.
//
// Run: go test -tags breakit -run BreakIt -v ./labs/a7/retry/

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"sync/atomic"
	"testing"
	"time"
)

var errDown = errors.New("dependency down")

// layers builds k nested layers, each retrying r times around the next; the
// bottom call always fails. It returns the function to call and the bottom
// call counter.
func breakitLayers(k, r int, budget *Budget) (func(context.Context) error, *atomic.Int64) {
	var bottom atomic.Int64
	cfg := Config{
		MaxAttempts: r + 1, Idempotent: true, Budget: budget,
		Sleep: func(context.Context, time.Duration) error { return nil }, // no real waiting
	}
	var build func(depth int) func(context.Context) error
	build = func(depth int) func(context.Context) error {
		if depth == k {
			return func(context.Context) error { bottom.Add(1); return errDown }
		}
		inner := build(depth + 1)
		return func(ctx context.Context) error { return DoErr(ctx, cfg, inner) }
	}
	return build(0), &bottom
}

// TestBreakIt_AmplificationTable: how many calls reach the dependency for ONE
// user action when every layer of the stack retries? Predict the k=3, r=2
// cell before you run it, then compare the same stack with one shared 10%
// retry budget over 1,000 user actions.
func TestBreakIt_AmplificationTable(t *testing.T) {
	t.Log("dependency calls per user action, dependency always failing (no budget):")
	t.Logf("%-10s %8s %8s %8s %8s", "layers k", "r=0", "r=1", "r=2", "r=3")
	for k := 1; k <= 4; k++ {
		row := fmt.Sprintf("%-10d", k)
		for r := 0; r <= 3; r++ {
			fn, bottom := breakitLayers(k, r, nil)
			_ = fn(context.Background())
			row += fmt.Sprintf(" %8d", bottom.Load())
		}
		t.Log(row)
	}

	const actions = 1000
	t.Logf("\nsame stacks, %d user actions through ONE shared budget (10%% of requests, burst 10):", actions)
	t.Logf("%-10s %8s %8s %8s %8s", "layers k", "r=0", "r=1", "r=2", "r=3")
	for k := 1; k <= 4; k++ {
		row := fmt.Sprintf("%-10d", k)
		for r := 0; r <= 3; r++ {
			fn, bottom := breakitLayers(k, r, NewBudget(0.1, 10, 0))
			for i := 0; i < actions; i++ {
				_ = fn(context.Background())
			}
			row += fmt.Sprintf(" %8.2f", float64(bottom.Load())/actions)
		}
		t.Log(row)
	}
	t.Log("Question: why is the budgeted k=4 row not 4× the k=1 row, and what would the numbers look like if each layer had its own budget?")
}

// TestBreakIt_JitterStrategies: 100 clients contend for one slot per 10 ms
// (a hot row, a lock). Compare total calls and completion time per strategy.
// The standalone version with more strategies is `go run -tags breakit
// ./labs/a7/sim backoff`; this one runs YOUR Delay.
func TestBreakIt_JitterStrategies(t *testing.T) {
	const clients, slot = 100, 10 * time.Millisecond
	t.Logf("%-14s %12s %14s", "strategy", "total calls", "last client done")
	for _, j := range []Jitter{NoJitter, FullJitter, EqualJitter, DecorrelatedJitter} {
		var calls int
		var last time.Duration
		const seeds = 10
		for s := 0; s < seeds; s++ {
			r := rand.New(rand.NewPCG(uint64(s+1), 9))
			type c struct {
				next, prev time.Duration
				attempt    int
				done       bool
			}
			cs := make([]*c, clients)
			for i := range cs {
				cs[i] = &c{prev: slot}
			}
			left := clients
			var end time.Duration
			for step := 0; left > 0 && step < 1_000_000; step++ {
				now := time.Duration(step) * slot
				var callers []*c
				for _, x := range cs {
					if !x.done && x.next < now+slot {
						callers = append(callers, x)
					}
				}
				if len(callers) == 0 {
					continue
				}
				calls += len(callers)
				w := callers[r.IntN(len(callers))]
				w.done = true
				left--
				end = now + slot
				for _, x := range callers {
					if x == w {
						continue
					}
					d := Delay(j, slot, 2*time.Second, x.attempt, x.prev, r.Float64())
					x.prev = d
					x.attempt++
					x.next = now + slot + d
				}
			}
			last += end
		}
		t.Logf("%-14v %12d %14s", j, calls/seeds, (last / seeds).Round(time.Millisecond))
	}
}
