//go:build breakit

package capstone

// A7 Unit 3 capstone: a vendor adapter that composes every piece you built.
// It runs YOUR retry, breaker, ratelimit and bulkhead packages against a
// simulated vendor, so it only produces sensible output once those labs pass.
//
//	go test -tags breakit -run Capstone -v ./labs/a7/capstone/
//
// Real time at millisecond scale (≈ 4 s per row); counts vary by a few
// percent between runs, the shape does not.

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chethu007/system-design-course/labs/a7/breaker"
	"github.com/chethu007/system-design-course/labs/a7/bulkhead"
	"github.com/chethu007/system-design-course/labs/a7/ratelimit"
	"github.com/chethu007/system-design-course/labs/a7/retry"
)

var errLocalLimit = errors.New("adapter: local rate limit")
var errVendor = errors.New("vendor error")

// vendor simulates the remote service. Its behaviour depends on the elapsed
// time since the run started.
type vendor struct {
	start time.Time
	phase func(elapsed time.Duration) string // "ok" | "flaky" | "down" | "slow"
	calls atomic.Int64
	rnd   func() float64
}

func (v *vendor) call(ctx context.Context) error {
	v.calls.Add(1)
	wait := func(d time.Duration) error {
		select {
		case <-time.After(d):
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	switch v.phase(time.Since(v.start)) {
	case "flaky":
		if err := wait(20 * time.Millisecond); err != nil {
			return err
		}
		if v.rnd() < 0.3 {
			return errVendor
		}
		return nil
	case "down":
		if err := wait(2 * time.Second); err != nil { // hangs until the caller gives up
			return err
		}
		return errVendor
	case "slow":
		return wait(400 * time.Millisecond)
	default:
		return wait(20 * time.Millisecond)
	}
}

// adapter is one of the designs under test.
type adapter interface {
	call(ctx context.Context) (degraded bool, err error)
}

// naive: retry 3 times at the adapter, nothing else.
type naive struct{ v *vendor }

func (a naive) call(ctx context.Context) (bool, error) {
	return false, retry.DoErr(ctx, retry.Config{MaxAttempts: 3, BaseDelay: 20 * time.Millisecond, MaxDelay: 200 * time.Millisecond,
		Idempotent: true, AttemptTimeout: 300 * time.Millisecond}, a.v.call)
}

// resilient: retry (with a shared budget) → breaker → bulkhead → limiter → call.
// Local rejections are not retried, not counted against the vendor, and are
// answered from a degraded fallback.
type resilient struct {
	v  *vendor
	br *breaker.Breaker
	bh *bulkhead.Bulkhead
	rl ratelimit.Limiter
	b  *retry.Budget
}

func newResilient(v *vendor, budgetRatio float64) *resilient {
	return &resilient{
		v: v,
		br: breaker.New(breaker.Config{
			Window: 2 * time.Second, Buckets: 10, MinRequests: 10, FailureRatio: 0.5,
			OpenFor: 500 * time.Millisecond, HalfOpenMax: 2, HalfOpenSuccesses: 2,
			Classify: func(err error) breaker.Outcome {
				switch {
				case err == nil:
					return breaker.Success
				case errors.Is(err, errLocalLimit), errors.Is(err, bulkhead.ErrFull), errors.Is(err, context.Canceled):
					return breaker.Ignore // not the vendor's fault
				}
				return breaker.Failure
			},
		}),
		bh: bulkhead.New(bulkhead.Config{MaxConcurrent: 20}),
		rl: ratelimit.NewTokenBucket(ratelimit.TokenBucketConfig{Rate: 150, Burst: 30}), // the vendor contract
		b:  retry.NewBudget(budgetRatio, 10, 5),
	}
}

func (a *resilient) call(ctx context.Context) (bool, error) {
	err := retry.DoErr(ctx, retry.Config{
		MaxAttempts: 3, BaseDelay: 20 * time.Millisecond, MaxDelay: 200 * time.Millisecond,
		Idempotent: true, AttemptTimeout: 300 * time.Millisecond, Budget: a.b,
		IsRetryable: func(err error) bool {
			return !errors.Is(err, breaker.ErrOpen) && !errors.Is(err, bulkhead.ErrFull) && !errors.Is(err, errLocalLimit)
		},
	}, func(ctx context.Context) error {
		return a.br.Do(ctx, func(ctx context.Context) error {
			return a.bh.Do(ctx, func(ctx context.Context) error {
				if !a.rl.Allow("vendor").Allowed {
					return errLocalLimit
				}
				return a.v.call(ctx)
			})
		})
	})
	if errors.Is(err, breaker.ErrOpen) || errors.Is(err, bulkhead.ErrFull) || errors.Is(err, errLocalLimit) {
		return true, nil // degraded: answer from the cache / "pending" response
	}
	return false, err
}

type tally struct {
	mu       sync.Mutex
	ok       int
	degraded int
	failed   int
	lat      []time.Duration
}

func (t *tally) add(d time.Duration, degraded bool, err error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	switch {
	case err != nil:
		t.failed++
	case degraded:
		t.degraded++
	default:
		t.ok++
	}
	t.lat = append(t.lat, d)
}

func (t *tally) p(q float64) time.Duration {
	sort.Slice(t.lat, func(i, j int) bool { return t.lat[i] < t.lat[j] })
	return t.lat[int(q*float64(len(t.lat)-1))]
}

// run offers 100 req/s for 4 s, each request with a 1 s client deadline.
func run(a adapter, v *vendor) *tally {
	tl := &tally{}
	var wg sync.WaitGroup
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	end := time.After(4 * time.Second)
loop:
	for {
		select {
		case <-end:
			break loop
		case <-tick.C:
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			start := time.Now()
			degraded, err := a.call(ctx)
			tl.add(time.Since(start), degraded, err)
		}()
	}
	wg.Wait()
	return tl
}

func TestCapstone_VendorBehaviours(t *testing.T) {
	scenarios := []struct {
		name  string
		phase func(time.Duration) string
	}{
		{"healthy vendor", func(time.Duration) string { return "ok" }},
		{"flaky vendor (30% errors)", func(time.Duration) string { return "flaky" }},
		{"vendor down from t=1 s to t=3 s", func(e time.Duration) string {
			if e >= time.Second && e < 3*time.Second {
				return "down"
			}
			return "ok"
		}},
	}
	t.Logf("400 client requests (100/s for 4 s), 1 s client deadline; adapters retry up to 3×, 300 ms per attempt")
	t.Logf("%-34s %-13s %7s %9s %7s %12s %10s %10s", "scenario", "adapter", "served", "degraded", "failed", "vendor calls", "p50", "p99")
	for _, sc := range scenarios {
		for _, name := range []string{"naive", "resilient", "resilient-40"} {
			r := rand.New(rand.NewPCG(1, 2))
			var mu sync.Mutex
			v := &vendor{start: time.Now(), phase: sc.phase, rnd: func() float64 { mu.Lock(); defer mu.Unlock(); return r.Float64() }}
			var a adapter = naive{v}
			switch name {
			case "resilient":
				a = newResilient(v, 0.1) // retry budget: 10% of requests
			case "resilient-40":
				a = newResilient(v, 0.4) // retry budget: 40% of requests
			}
			tl := run(a, v)
			t.Logf("%-34s %-13s %7d %9d %7d %12d %10s %10s", sc.name, name, tl.ok, tl.degraded, tl.failed, v.calls.Load(),
				tl.p(.5).Round(time.Millisecond), tl.p(.99).Round(time.Millisecond))
		}
	}
	t.Log(fmt.Sprint("Question: which column shows the cost of the 'down' scenario to the VENDOR, and which shows it to your MEMBERS? Which design is kinder to both?"))
}
