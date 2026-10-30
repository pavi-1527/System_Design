//go:build breakit

package tenancy

// Break-it experiments for A9 Unit 1 §4 and §5. These are measurements, not
// pass/fail tests: they print what happened so you can compare it with what
// you predicted.
//
// Run: go test -tags breakit -run BreakIt -v ./labs/a9/tenancy/

import (
	"fmt"
	"testing"
	"time"
)

// TestBreakIt_CrossTenantLeak counts how many of another tenant's rows
// tenant cu-alpha can read through List. With the isolation intact it is 0.
// Wire breakNoTenantPredicate into your List (see breakit_on.go) and re-run:
// the number is the size of your breach, and the normal tests that catch it
// are TestStore_ListIsTenantScopedAndFilterOnlySeesOwnRows,
// TestStore_ListPaginatesByKeysetExactlyOnce and
// TestStore_ConcurrentMixedTenantsNeverLeakOrLose.
func TestBreakIt_CrossTenantLeak(t *testing.T) {
	s := NewStore()
	const perTenant = 40
	tenants := []string{"cu-alpha", "cu-beta", "cu-gamma"}
	for _, tn := range tenants {
		for i := 0; i < perTenant; i++ {
			if _, err := s.Insert(tCtx(tn), Row{ID: fmt.Sprintf("%s-acct-%03d", tn, i), Data: "balance of a member of " + tn}); err != nil {
				t.Fatalf("setup: %v", err)
			}
		}
	}
	visible, foreign := 0, 0
	after := ""
	for pages := 0; pages < 100; pages++ {
		p, err := s.List(tCtx("cu-alpha"), ListOptions{Limit: 50, After: after})
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		for _, r := range p.Rows {
			visible++
			if r.Tenant != "cu-alpha" {
				foreign++
			}
		}
		if p.Next == "" {
			break
		}
		after = p.Next
	}
	t.Logf("cu-alpha owns %d rows; List returned %d rows, %d of them belong to other credit unions", perTenant, visible, foreign)
	if foreign > 0 {
		t.Logf("BREACH: %.0f%% of what cu-alpha can enumerate is other tenants' data (and no per-row authorization check was ever consulted)", 100*float64(foreign)/float64(visible))
	}
}

// globalBucket is the "one rate limit for the whole API" that most teams ship
// first: first come, first served.
type globalBucket struct {
	rate, burst, tokens float64
	last                time.Time
}

func (g *globalBucket) allow(now time.Time) bool {
	if !g.last.IsZero() {
		g.tokens += now.Sub(g.last).Seconds() * g.rate
		if g.tokens > g.burst {
			g.tokens = g.burst
		}
	}
	g.last = now
	if g.tokens >= 1 {
		g.tokens--
		return true
	}
	return false
}

// TestBreakIt_NoisyNeighbour: the platform admits 100 req/s. Tenant "big"
// floods at 500 req/s (and, being first in the loop, gets first pick every
// tick); tenant "small" sends a polite 40 req/s. Compare one global limit with
// the per-tenant Limiter (equal weights, 80 req/s cap). Predict the
// percentage of small's requests that succeed under each before you run it.
func TestBreakIt_NoisyNeighbour(t *testing.T) {
	const ticks = 100
	step := 100 * time.Millisecond
	bigPerTick, smallPerTick := 50, 4

	// 1. One global bucket.
	start := time.Date(2026, 5, 4, 12, 0, 0, 0, time.UTC)
	g := &globalBucket{rate: 100, burst: 100, tokens: 100}
	var gBig, gSmall int
	for i := 0; i < ticks; i++ {
		now := start.Add(time.Duration(i) * step)
		for j := 0; j < bigPerTick; j++ {
			if g.allow(now) {
				gBig++
			}
		}
		for j := 0; j < smallPerTick; j++ {
			if g.allow(now) {
				gSmall++
			}
		}
	}

	// 2. Per-tenant weighted-fair limiter.
	clk := tNewClock()
	l := tLimiter(t, LimiterConfig{Rate: 100, TenantCap: 80}, clk, map[string]float64{"big": 1, "small": 1})
	got := tTicks(l, clk, ticks, step, map[string]int{"big": bigPerTick, "small": smallPerTick})

	sent := ticks * smallPerTick
	t.Logf("%-28s %12s %12s %16s", "strategy", "big admitted", "small ok", "small success %")
	t.Logf("%-28s %12d %12d %15.1f%%", "one global bucket", gBig, gSmall, 100*float64(gSmall)/float64(sent))
	t.Logf("%-28s %12d %12d %15.1f%%", "per-tenant fair limiter", got["big"], got["small"], 100*float64(got["small"])/float64(sent))
	t.Logf("(small sent %d requests, comfortably under its guaranteed 50 req/s)", sent)
}
