package tenancy

import (
	"context"
	"fmt"
	"math"
	"math/rand"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// ---------- helpers (prefixed with t to avoid clashing with your code) ----------

func tCtx(id string) context.Context { return WithTenant(context.Background(), TenantID(id)) }

type tClock struct {
	mu sync.Mutex
	t  time.Time
}

func tNewClock() *tClock { return &tClock{t: time.Date(2026, 5, 4, 12, 0, 0, 0, time.UTC)} }
func (c *tClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}
func (c *tClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}
func (c *tClock) Set(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = t
}

// tWait waits for wg, failing the test instead of hanging if it takes longer
// than d (e.g. a deadlock in the implementation under test).
func tWait(t *testing.T, wg *sync.WaitGroup, d time.Duration, what string) {
	t.Helper()
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(d):
		t.Fatalf("%s did not finish within %v (deadlock or livelock in the implementation?)", what, d)
	}
}

func tInsert(t *testing.T, s *Store, tenant, id, owner string) Row {
	t.Helper()
	r, err := s.Insert(tCtx(tenant), Row{ID: id, Owner: owner, Data: tenant + ":" + id, Labels: map[string]string{"k": "v"}})
	if err != nil {
		t.Fatalf("Insert(%s/%s) failed: %v; every other expectation in this test depends on it", tenant, id, err)
	}
	return r
}

func tIDs(rows []Row) []string {
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = r.ID
	}
	return out
}

// ---------- Store: basics ----------

func TestStore_InsertGetStampsTenantAndVersion(t *testing.T) {
	s := NewStore()
	got := tInsert(t, s, "cu-alpha", "acct-1", "member-7")
	if got.Tenant != "cu-alpha" || got.Version != 1 {
		t.Fatalf("Insert returned Tenant=%q Version=%d; expected the tenant to be stamped from the context (cu-alpha) and Version 1", got.Tenant, got.Version)
	}
	r, err := s.Get(tCtx("cu-alpha"), "acct-1")
	if err != nil {
		t.Fatalf("Get after Insert: %v", err)
	}
	if r.ID != "acct-1" || r.Owner != "member-7" || r.Data != "cu-alpha:acct-1" || r.Labels["k"] != "v" || r.Tenant != "cu-alpha" || r.Version != 1 {
		t.Fatalf("Get returned %+v; expected exactly what was inserted, stamped with tenant cu-alpha", r)
	}
}

func TestStore_NoTenantOrInvalidTenantFailsClosed(t *testing.T) {
	s := NewStore()
	tInsert(t, s, "cu-alpha", "x", "m")

	call := func(ctx context.Context) map[string]error {
		_, e1 := s.Insert(ctx, Row{ID: "y"})
		_, e2 := s.Get(ctx, "x")
		_, e3 := s.Update(ctx, "x", func(r *Row) { r.Data = "changed" })
		e4 := s.Delete(ctx, "x")
		_, e5 := s.List(ctx, ListOptions{})
		_, e6 := s.Count(ctx)
		return map[string]error{"Insert": e1, "Get": e2, "Update": e3, "Delete": e4, "List": e5, "Count": e6}
	}
	for op, err := range call(context.Background()) {
		if err != ErrNoTenant {
			t.Errorf("%s with no tenant in the context returned %v; expected ErrNoTenant (fail closed, never 'all tenants')", op, err)
		}
	}
	for _, bad := range []string{"", "UPPER", "a:b", "-lead", "has space", "dot.dot", strings.Repeat("a", 64)} {
		for op, err := range call(tCtx(bad)) {
			if err != ErrInvalidTenant {
				t.Errorf("%s with tenant %q returned %v; expected ErrInvalidTenant", op, bad, err)
			}
		}
	}
	if n, err := s.Count(tCtx("cu-alpha")); err != nil || n != 1 {
		t.Fatalf("after the rejected calls cu-alpha has Count=%d err=%v; expected the single original row untouched", n, err)
	}
	r, _ := s.Get(tCtx("cu-alpha"), "x")
	if r.Data != "cu-alpha:x" {
		t.Fatalf("row was modified by a call that should have been rejected: %+v", r)
	}
}

func TestStore_SameIDInTwoTenantsAreDifferentRows(t *testing.T) {
	s := NewStore()
	tInsert(t, s, "cu-alpha", "acct-1", "a")
	if _, err := s.Insert(tCtx("cu-beta"), Row{ID: "acct-1", Owner: "b", Data: "beta data"}); err != nil {
		t.Fatalf("cu-beta could not insert acct-1 because cu-alpha owns that id: %v; ids are unique per tenant, not globally (a global unique id is itself a cross-tenant existence oracle)", err)
	}
	a, _ := s.Get(tCtx("cu-alpha"), "acct-1")
	b, _ := s.Get(tCtx("cu-beta"), "acct-1")
	if a.Owner != "a" || b.Owner != "b" || a.Data == b.Data {
		t.Fatalf("rows collided: alpha=%+v beta=%+v", a, b)
	}
	if _, err := s.Insert(tCtx("cu-alpha"), Row{ID: "acct-1"}); err != ErrExists {
		t.Fatalf("duplicate insert in the same tenant returned %v; expected ErrExists", err)
	}
	after, _ := s.Get(tCtx("cu-alpha"), "acct-1")
	if after.Owner != "a" || after.Version != 1 {
		t.Fatalf("failed duplicate insert changed the stored row: %+v", after)
	}
}

func TestStore_InsertRejectsEmptyIDAndForeignTenantField(t *testing.T) {
	s := NewStore()
	if _, err := s.Insert(tCtx("cu-alpha"), Row{}); err != ErrInvalidRow {
		t.Fatalf("empty id returned %v; expected ErrInvalidRow", err)
	}
	_, err := s.Insert(tCtx("cu-beta"), Row{ID: "planted", Tenant: "cu-alpha", Data: "x"})
	if err != ErrTenantMismatch {
		t.Fatalf("inserting a row stamped for cu-alpha while acting as cu-beta returned %v; expected ErrTenantMismatch (do not silently re-stamp: that hides an attack or a bug)", err)
	}
	for _, tn := range []string{"cu-alpha", "cu-beta"} {
		if n, _ := s.Count(tCtx(tn)); n != 0 {
			t.Fatalf("%s has %d rows after the rejected insert; expected 0", tn, n)
		}
	}
	// An explicit but matching tenant is fine.
	if _, err := s.Insert(tCtx("cu-beta"), Row{ID: "ok", Tenant: "cu-beta"}); err != nil {
		t.Fatalf("matching tenant field rejected: %v", err)
	}
}

// ---------- Store: isolation (BOLA / IDOR) ----------

func TestStore_GetUpdateDeleteOfAnotherTenantsRowIsNotFound(t *testing.T) {
	s := NewStore()
	tInsert(t, s, "cu-alpha", "secret", "member-1")
	b := tCtx("cu-beta")

	if _, err := s.Get(b, "secret"); err != ErrNotFound {
		t.Errorf("Get of another tenant's id returned %v; expected ErrNotFound (BOLA: guessing ids must not work)", err)
	}
	called := false
	if _, err := s.Update(b, "secret", func(r *Row) { called = true; r.Data = "pwned" }); err != ErrNotFound {
		t.Errorf("Update of another tenant's id returned %v; expected ErrNotFound", err)
	}
	if called {
		t.Errorf("mutate was invoked for a row the caller's tenant does not own; the other tenant's data was handed to caller code")
	}
	if err := s.Delete(b, "secret"); err != ErrNotFound {
		t.Errorf("Delete of another tenant's id returned %v; expected ErrNotFound", err)
	}
	r, err := s.Get(tCtx("cu-alpha"), "secret")
	if err != nil || r.Data != "cu-alpha:secret" || r.Version != 1 {
		t.Fatalf("owner's row was damaged by the other tenant's attempts: %+v err=%v", r, err)
	}
}

func TestStore_AnotherTenantsRowIsIndistinguishableFromMissing(t *testing.T) {
	withOther := NewStore()
	tInsert(t, withOther, "cu-alpha", "x", "m")
	nobody := NewStore()
	b := tCtx("cu-beta")

	_, e1 := withOther.Get(b, "x")
	_, e2 := nobody.Get(b, "x")
	_, u1 := withOther.Update(b, "x", func(*Row) {})
	_, u2 := nobody.Update(b, "x", func(*Row) {})
	d1 := withOther.Delete(b, "x")
	d2 := nobody.Delete(b, "x")
	for _, p := range [][2]error{{e1, e2}, {u1, u2}, {d1, d2}} {
		if p[0] == nil || p[1] == nil || p[0].Error() != p[1].Error() {
			t.Fatalf("errors differ between 'exists in another tenant' (%v) and 'exists nowhere' (%v); a different message, code or wrapped detail lets an attacker enumerate other tenants' ids", p[0], p[1])
		}
		if strings.Contains(p[0].Error(), "cu-alpha") {
			t.Fatalf("error %q leaks the other tenant's name", p[0])
		}
	}
}

func TestStore_UpdateIgnoresAttemptsToChangeIdentity(t *testing.T) {
	s := NewStore()
	tInsert(t, s, "cu-alpha", "acct-1", "m")
	got, err := s.Update(tCtx("cu-alpha"), "acct-1", func(r *Row) {
		r.ID = "moved"
		r.Tenant = "cu-beta"
		r.Version = 99
		r.Data = "new"
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if got.ID != "acct-1" || got.Tenant != "cu-alpha" || got.Version != 2 || got.Data != "new" {
		t.Fatalf("Update returned %+v; expected ID and Tenant unchanged, Version 2 (not 99) and the Data change applied", got)
	}
	if _, err := s.Get(tCtx("cu-alpha"), "moved"); err != ErrNotFound {
		t.Fatalf("row is reachable under the id mutate assigned (%v); identity fields must be restored after mutate", err)
	}
	if n, _ := s.Count(tCtx("cu-beta")); n != 0 {
		t.Fatalf("cu-beta gained %d rows because mutate rewrote Tenant; a caller-controlled tenant field must never decide where a row lives", n)
	}
	if _, err := s.Update(tCtx("cu-alpha"), "acct-1", nil); err != ErrInvalidRow {
		t.Fatalf("nil mutate returned %v; expected ErrInvalidRow", err)
	}
}

func TestStore_ListIsTenantScopedAndFilterOnlySeesOwnRows(t *testing.T) {
	s := NewStore()
	tenants := []string{"cu-alpha", "cu-beta", "cu-gamma"}
	for _, tn := range tenants {
		for i := 0; i < 10; i++ {
			tInsert(t, s, tn, fmt.Sprintf("r%02d", i), "m")
		}
	}
	for _, tn := range tenants {
		var seen []TenantID
		page, err := s.List(tCtx(tn), ListOptions{Filter: func(r Row) bool { seen = append(seen, r.Tenant); return true }})
		if err != nil {
			t.Fatalf("List(%s): %v", tn, err)
		}
		if len(page.Rows) != 10 {
			t.Fatalf("List(%s) returned %d rows; expected the tenant's own 10 (ids overlap across tenants, so a missing predicate shows up as 30)", tn, len(page.Rows))
		}
		for _, r := range page.Rows {
			if r.Tenant != TenantID(tn) || r.Data != tn+":"+r.ID {
				t.Fatalf("List(%s) returned a foreign row: %+v", tn, r)
			}
		}
		for _, st := range seen {
			if st != TenantID(tn) {
				t.Fatalf("Filter for %s was called with a row of tenant %q; caller code must never see other tenants' rows", tn, st)
			}
		}
	}
}

func TestStore_CountIsPerTenant(t *testing.T) {
	s := NewStore()
	for i := 0; i < 5; i++ {
		tInsert(t, s, "cu-alpha", fmt.Sprint("a", i), "m")
	}
	for i := 0; i < 3; i++ {
		tInsert(t, s, "cu-beta", fmt.Sprint("b", i), "m")
	}
	a, _ := s.Count(tCtx("cu-alpha"))
	b, _ := s.Count(tCtx("cu-beta"))
	c, _ := s.Count(tCtx("cu-empty"))
	if a != 5 || b != 3 || c != 0 {
		t.Fatalf("counts alpha=%d beta=%d empty=%d; expected 5, 3, 0", a, b, c)
	}
}

// ---------- Store: copies, listing, concurrency ----------

func TestStore_RowsAreDeepCopies(t *testing.T) {
	s := NewStore()
	ctx := tCtx("cu-alpha")
	in := Row{ID: "r", Labels: map[string]string{"k": "v"}}
	ins, err := s.Insert(ctx, in)
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}
	in.Labels["k"] = "mutated-input"
	ins.Labels["k"] = "mutated-insert-result"
	g, err := s.Get(ctx, "r")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	g.Labels["k"] = "mutated-get-result"
	page, err := s.List(ctx, ListOptions{})
	if err != nil || len(page.Rows) != 1 {
		t.Fatalf("List: %v (%d rows)", err, len(page.Rows))
	}
	page.Rows[0].Labels["k"] = "mutated-list-result"
	var leaked *Row
	up, err := s.Update(ctx, "r", func(r *Row) { leaked = r })
	if err != nil || leaked == nil {
		t.Fatalf("Update: %v", err)
	}
	leaked.Labels["k"] = "mutated-after-update"
	up.Labels["k"] = "mutated-update-result"

	final, err := s.Get(ctx, "r")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if final.Labels["k"] != "v" {
		t.Fatalf("stored label is %q after callers mutated their copies; the store shares a map with its callers (input, Insert/Get/List/Update results, or the pointer given to mutate), so one request can corrupt, or read, state another request is using", final.Labels["k"])
	}
}

func TestStore_ListPaginatesByKeysetExactlyOnce(t *testing.T) {
	for _, n := range []int{53, 50} {
		s := NewStore()
		for i := 0; i < n; i++ {
			tInsert(t, s, "cu-alpha", fmt.Sprintf("id%03d", i), "m")
			tInsert(t, s, "cu-beta", fmt.Sprintf("id%03d", i), "m") // interleaved noise
		}
		var all []string
		after := ""
		pages := 0
		for {
			p, err := s.List(tCtx("cu-alpha"), ListOptions{Limit: 10, After: after})
			if err != nil {
				t.Fatalf("List: %v", err)
			}
			pages++
			all = append(all, tIDs(p.Rows)...)
			if p.Next == "" {
				break
			}
			if len(p.Rows) != 10 {
				t.Fatalf("n=%d: a non-final page had %d rows; expected a full page of 10 when Next is set", n, len(p.Rows))
			}
			after = p.Next
			if pages > 20 {
				t.Fatalf("n=%d: pagination did not terminate", n)
			}
		}
		if len(all) != n || !sort.StringsAreSorted(all) {
			t.Fatalf("n=%d: collected %d ids (sorted=%v); expected every id exactly once in order", n, len(all), sort.StringsAreSorted(all))
		}
		want := (n + 9) / 10
		if pages != want {
			t.Fatalf("n=%d: took %d pages; expected %d (with exactly 50 rows the last page must report Next=\"\", not point at an empty extra page)", n, pages, want)
		}
	}
}

func TestStore_ListFilterAppliesBeforeLimit(t *testing.T) {
	s := NewStore()
	for i := 0; i < 30; i++ {
		owner := "odd"
		if i%2 == 0 {
			owner = "even"
		}
		tInsert(t, s, "cu-alpha", fmt.Sprintf("r%02d", i), owner)
	}
	even := func(r Row) bool { return r.Owner == "even" }
	p1, _ := s.List(tCtx("cu-alpha"), ListOptions{Limit: 10, Filter: even})
	if len(p1.Rows) != 10 || p1.Next == "" {
		t.Fatalf("page 1: %d rows, Next=%q; expected 10 matching rows and a cursor (15 rows match)", len(p1.Rows), p1.Next)
	}
	p2, _ := s.List(tCtx("cu-alpha"), ListOptions{Limit: 10, After: p1.Next, Filter: even})
	if len(p2.Rows) != 5 || p2.Next != "" {
		t.Fatalf("page 2: %d rows, Next=%q; expected the remaining 5 matches and no cursor", len(p2.Rows), p2.Next)
	}
	for _, r := range append(p1.Rows, p2.Rows...) {
		if r.Owner != "even" {
			t.Fatalf("filter not applied: %+v", r)
		}
	}
}

func TestStore_ListDefaultAndMaximumPageSize(t *testing.T) {
	s := NewStore()
	for i := 0; i < 300; i++ {
		tInsert(t, s, "cu-alpha", fmt.Sprintf("r%03d", i), "m")
	}
	def, _ := s.List(tCtx("cu-alpha"), ListOptions{})
	big, _ := s.List(tCtx("cu-alpha"), ListOptions{Limit: 100000})
	if len(def.Rows) != 50 || len(big.Rows) != 200 {
		t.Fatalf("default page %d rows (want 50), huge limit %d rows (want the 200 cap); an unbounded list is both a memory problem and a bulk-exfiltration primitive", len(def.Rows), len(big.Rows))
	}
}

func TestStore_DeleteThenReinsertStartsAtVersionOne(t *testing.T) {
	s := NewStore()
	tInsert(t, s, "cu-alpha", "x", "m")
	if _, err := s.Update(tCtx("cu-alpha"), "x", func(r *Row) { r.Data = "v2" }); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(tCtx("cu-alpha"), "x"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if err := s.Delete(tCtx("cu-alpha"), "x"); err != ErrNotFound {
		t.Fatalf("second Delete returned %v; expected ErrNotFound", err)
	}
	r := tInsert(t, s, "cu-alpha", "x", "m2")
	if r.Version != 1 {
		t.Fatalf("re-inserted row has Version %d; expected 1", r.Version)
	}
}

func TestStore_ConcurrentUpdatesAreNotLost(t *testing.T) {
	s := NewStore()
	if _, err := s.Insert(tCtx("cu-alpha"), Row{ID: "ctr", Labels: map[string]string{"n": "0"}}); err != nil {
		t.Fatal(err)
	}
	const goroutines, each = 50, 20
	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < each; i++ {
				_, err := s.Update(tCtx("cu-alpha"), "ctr", func(r *Row) {
					n, _ := strconv.Atoi(r.Labels["n"])
					r.Labels["n"] = strconv.Itoa(n + 1)
				})
				if err != nil {
					t.Errorf("Update: %v", err)
					return
				}
			}
		}()
	}
	tWait(t, &wg, 10*time.Second, "concurrent updates")
	r, _ := s.Get(tCtx("cu-alpha"), "ctr")
	if r.Labels["n"] != strconv.Itoa(goroutines*each) || r.Version != goroutines*each+1 {
		t.Fatalf("counter=%s version=%d after %d increments; expected %d and %d (read-modify-write must be atomic inside Update)", r.Labels["n"], r.Version, goroutines*each, goroutines*each, goroutines*each+1)
	}
}

func TestStore_ConcurrentMixedTenantsNeverLeakOrLose(t *testing.T) {
	s := NewStore()
	tenants := []string{"cu-a", "cu-b", "cu-c", "cu-d", "cu-e", "cu-f", "cu-g", "cu-h"}
	const perTenant, ops = 4, 300
	var wg sync.WaitGroup
	var live [8][perTenant]int
	var foreignHits atomic.Int64
	for ti, tn := range tenants {
		for g := 0; g < perTenant; g++ {
			wg.Add(1)
			go func(ti, g int, tn string) {
				defer wg.Done()
				rng := rand.New(rand.NewSource(int64(ti*100 + g)))
				ctx := tCtx(tn)
				mine := map[string]bool{}
				other := tenants[(ti+1)%len(tenants)]
				for i := 0; i < ops; i++ {
					id := fmt.Sprintf("%s-g%d-%02d", tn, g, rng.Intn(40))
					switch rng.Intn(5) {
					case 0, 1:
						_, err := s.Insert(ctx, Row{ID: id, Data: tn})
						if err == nil {
							mine[id] = true
						}
					case 2:
						if r, err := s.Get(ctx, id); err == nil && (r.Tenant != TenantID(tn) || r.Data != tn) {
							foreignHits.Add(1)
						}
					case 3:
						s.Update(ctx, id, func(r *Row) { r.Owner = "touched" })
					case 4:
						if s.Delete(ctx, id) == nil {
							delete(mine, id)
						}
					}
					// Probe a neighbour's id namespace: must never be visible.
					probe := fmt.Sprintf("%s-g%d-%02d", other, g, rng.Intn(40))
					if _, err := s.Get(ctx, probe); err != ErrNotFound {
						foreignHits.Add(1)
					}
					if i%50 == 0 {
						p, err := s.List(ctx, ListOptions{Limit: 200})
						if err != nil {
							t.Errorf("List: %v", err)
							return
						}
						for _, r := range p.Rows {
							if r.Tenant != TenantID(tn) || r.Data != tn {
								foreignHits.Add(1)
							}
						}
					}
				}
				live[ti][g] = len(mine)
			}(ti, g, tn)
		}
	}
	tWait(t, &wg, 30*time.Second, "mixed multi-tenant workload")
	if n := foreignHits.Load(); n != 0 {
		t.Fatalf("%d operations observed another tenant's data", n)
	}
	for ti, tn := range tenants {
		want := 0
		for g := 0; g < perTenant; g++ {
			want += live[ti][g]
		}
		if got, _ := s.Count(tCtx(tn)); got != want {
			t.Fatalf("%s: Count=%d, but its goroutines believe %d rows are live (lost or phantom writes)", tn, got, want)
		}
	}
}

// ---------- Limiter ----------

func tLimiter(t *testing.T, cfg LimiterConfig, clk *tClock, weights map[string]float64) *Limiter {
	t.Helper()
	cfg.Now = clk.Now
	l := NewLimiter(cfg)
	names := make([]string, 0, len(weights))
	for n := range weights {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		if err := l.SetWeight(TenantID(n), weights[n]); err != nil {
			t.Fatalf("SetWeight(%s, %v): %v; every limiter test depends on it", n, weights[n], err)
		}
	}
	return l
}

// tBurst sends n requests as the tenant and returns how many were admitted.
func tBurst(l *Limiter, tenant string, n int) int {
	ctx := tCtx(tenant)
	ok := 0
	for i := 0; i < n; i++ {
		if l.Allow(ctx).Allowed {
			ok++
		}
	}
	return ok
}

func TestLimiter_SingleTenantBurstThenSteadyRate(t *testing.T) {
	clk := tNewClock()
	l := tLimiter(t, LimiterConfig{Rate: 10}, clk, map[string]float64{"a": 1})
	if got := tBurst(l, "a", 20); got != 10 {
		t.Fatalf("initial burst admitted %d of 20; expected 10 (Rate 10/s x 1s burst window)", got)
	}
	clk.Advance(100 * time.Millisecond)
	if got := tBurst(l, "a", 5); got != 1 {
		t.Fatalf("after 100ms admitted %d; expected exactly 1 token to have refilled", got)
	}
	clk.Advance(time.Hour)
	if got := tBurst(l, "a", 50); got != 10 {
		t.Fatalf("after an idle hour admitted %d; expected the bucket to be capped at its depth (10), not to bank an hour of tokens", got)
	}
}

func TestLimiter_NoTenantIsDeniedAndWeightsAreValidated(t *testing.T) {
	l := NewLimiter(LimiterConfig{Rate: 10, Now: tNewClock().Now})
	d := l.Allow(context.Background())
	if d.Allowed || d.Reason != "no-tenant" {
		t.Fatalf("request without a tenant: %+v; expected denied with Reason no-tenant (fail closed)", d)
	}
	d = l.Allow(tCtx("Bad Tenant"))
	if d.Allowed || d.Reason != "no-tenant" {
		t.Fatalf("request with an invalid tenant: %+v; expected denied with Reason no-tenant", d)
	}
	for _, w := range []float64{0, -1, math.NaN(), math.Inf(1)} {
		if err := l.SetWeight("a", w); err != ErrInvalidWeight {
			t.Errorf("SetWeight(%v) returned %v; expected ErrInvalidWeight", w, err)
		}
	}
}

func TestLimiter_RetryAfterIsExactAndReasonsSeparateShareFromCap(t *testing.T) {
	clk := tNewClock()
	// Rate 100, two equal tenants: guarantee 50/s each. Cap 60/s.
	l := tLimiter(t, LimiterConfig{Rate: 100, TenantCap: 60}, clk, map[string]float64{"a": 1, "b": 1})
	ctx := tCtx("a")

	if got := tBurst(l, "a", 50); got != 50 {
		t.Fatalf("a's guaranteed burst admitted %d of 50", got)
	}
	d := l.Allow(ctx)
	if d.Allowed || d.Reason != "over-share" {
		t.Fatalf("51st request: %+v; expected denied, Reason over-share (guarantee used up, nothing spare yet)", d)
	}
	if d.RetryAfter < 20*time.Millisecond || d.RetryAfter > 20*time.Millisecond+time.Microsecond {
		t.Fatalf("RetryAfter=%v; expected 20ms (one token at 50 tokens/s)", d.RetryAfter)
	}
	clk.Advance(d.RetryAfter - time.Nanosecond)
	if l.Allow(ctx).Allowed {
		t.Fatalf("allowed 1ns before RetryAfter elapsed")
	}
	clk.Advance(time.Nanosecond)
	if !l.Allow(ctx).Allowed {
		t.Fatalf("still denied after waiting the advertised RetryAfter")
	}

	// Let a and b refill fully; b is idle so its unused guarantee spills into the spare pool.
	clk.Advance(time.Second)
	var borrowed int
	allowed := 0
	for i := 0; i < 100; i++ {
		d := l.Allow(ctx)
		if d.Allowed {
			allowed++
			if d.Borrowed {
				borrowed++
			}
		}
	}
	if allowed != 60 {
		t.Fatalf("admitted %d in one window; expected exactly the cap (60) even though more spare capacity exists", allowed)
	}
	if borrowed < 9 || borrowed > 11 {
		t.Fatalf("%d of the admissions were marked Borrowed; expected ~10 (50 from a's own bucket, the rest from the spare pool)", borrowed)
	}
	d = l.Allow(ctx)
	if d.Allowed || d.Reason != "over-cap" {
		t.Fatalf("after hitting the cap: %+v; expected denied, Reason over-cap", d)
	}
	if d.RetryAfter < 16*time.Millisecond || d.RetryAfter > 17*time.Millisecond {
		t.Fatalf("RetryAfter=%v; expected ~16.7ms (one token at the 60/s cap)", d.RetryAfter)
	}
	clk.Advance(d.RetryAfter)
	if !l.Allow(ctx).Allowed {
		t.Fatalf("still denied after waiting the advertised RetryAfter of %v; round the wait up, never down, or clients retry into another denial", d.RetryAfter)
	}
}

// tick drives a deterministic open-loop workload: every `step` the clock
// advances, then each tenant fires its requests for that tick.
func tTicks(l *Limiter, clk *tClock, ticks int, step time.Duration, perTick map[string]int) map[string]int {
	names := make([]string, 0, len(perTick))
	for n := range perTick {
		names = append(names, n)
	}
	sort.Strings(names) // a goes before b: the noisy tenant always gets first pick
	admitted := map[string]int{}
	for i := 0; i < ticks; i++ {
		if i > 0 {
			clk.Advance(step)
		}
		for _, n := range names {
			admitted[n] += tBurst(l, n, perTick[n])
		}
	}
	return admitted
}

func TestLimiter_NoisyNeighbourCannotTakeAnotherTenantsGuarantee(t *testing.T) {
	clk := tNewClock()
	// Rate 100/s, guarantee 50/s each. "a" floods at 500 req/s and goes first in every tick;
	// "b" sends 40 req/s, below its guarantee.
	l := tLimiter(t, LimiterConfig{Rate: 100, TenantCap: 80}, clk, map[string]float64{"a": 1, "b": 1})
	got := tTicks(l, clk, 100, 100*time.Millisecond, map[string]int{"a": 50, "b": 4})
	if got["b"] != 400 {
		t.Fatalf("b (40 req/s, under its 50/s guarantee) had %d of 400 requests admitted while a flooded at 500 req/s; a guarantee that a neighbour can erode is not a guarantee", got["b"])
	}
	if got["a"] < 600 || got["a"] > 700 {
		t.Fatalf("a admitted %d; expected ~650: its own 50/s plus the ~10/s b leaves unused, plus its first burst (and well under a's 80/s cap)", got["a"])
	}
	_, _, borrowed := l.Stats("a")
	if borrowed < 40 {
		t.Fatalf("a borrowed only %d tokens; b's unused guarantee should have flowed to the spare pool and been lent out (work conservation)", borrowed)
	}
}

func TestLimiter_CapBoundsAGreedyTenantAndLeavesRoomForOthers(t *testing.T) {
	clk := tNewClock()
	l := tLimiter(t, LimiterConfig{Rate: 100, TenantCap: 80}, clk, map[string]float64{"a": 1, "b": 1})
	// b is idle for 10 s while a hammers at 500 req/s.
	got := tTicks(l, clk, 100, 100*time.Millisecond, map[string]int{"a": 50})
	if got["a"] < 800 || got["a"] > 880 {
		t.Fatalf("a admitted %d in 10s; expected ~850: capped at 80/s (+ a 1s burst) although half the platform was idle", got["a"])
	}
	// b wakes up at the very instant a is still hammering: a goes first, then b.
	aNow := tBurst(l, "a", 500)
	bNow := tBurst(l, "b", 50)
	if bNow != 50 {
		t.Fatalf("b's first 50 requests after waking: %d admitted (a admitted %d in the same instant); the cap exists precisely so b's guaranteed burst is still there", bNow, aNow)
	}
}

func TestLimiter_UncappedTenantUsesAllIdleCapacity(t *testing.T) {
	clk := tNewClock()
	l := tLimiter(t, LimiterConfig{Rate: 100}, clk, map[string]float64{"a": 1, "b": 1})
	got := tTicks(l, clk, 100, 100*time.Millisecond, map[string]int{"a": 50})
	if got["a"] < 1000 || got["a"] > 1100 {
		t.Fatalf("a admitted %d with b idle and no cap; expected ~1050 (all 100/s of platform capacity: work-conserving)", got["a"])
	}
}

func TestLimiter_WeightedSharesUnderContention(t *testing.T) {
	clk := tNewClock()
	l := tLimiter(t, LimiterConfig{Rate: 100}, clk, map[string]float64{"a": 3, "b": 1})
	got := tTicks(l, clk, 100, 100*time.Millisecond, map[string]int{"a": 100, "b": 100})
	// One initial burst (75 and 25) plus 99 refills of 7.5 and 2.5 tokens.
	if math.Abs(float64(got["a"]-817)) > 2 || math.Abs(float64(got["b"]-272)) > 2 {
		t.Fatalf("admitted a=%d b=%d under saturation; expected ~817 and ~272 (guarantees 75/s and 25/s, plus one burst each): shares must follow the 3:1 weights", got["a"], got["b"])
	}
}

func TestLimiter_NewTenantReshapesGuarantees(t *testing.T) {
	clk := tNewClock()
	l := tLimiter(t, LimiterConfig{Rate: 100}, clk, map[string]float64{"a": 3})
	if d := l.Allow(tCtx("newcomer")); !d.Allowed {
		t.Fatalf("unknown tenant denied: %+v; it should be registered at DefaultWeight and admitted", d)
	}
	// a was alone (guarantee 100/s); with the newcomer at weight 1 it is 75/s and its bucket is clamped.
	if got := tBurst(l, "a", 200); got != 75 {
		t.Fatalf("a admitted %d right after a second tenant appeared; expected 75 (weights 3:1 of 100/s, bucket clamped to the new depth)", got)
	}
}

func TestLimiter_TimeNeverRunsBackwards(t *testing.T) {
	clk := tNewClock()
	t0 := clk.Now()
	l := tLimiter(t, LimiterConfig{Rate: 10}, clk, map[string]float64{"a": 1})
	tBurst(l, "a", 10)
	clk.Set(t0.Add(-10 * time.Second)) // NTP step backwards
	if got := tBurst(l, "a", 10); got != 0 {
		t.Fatalf("clock went backwards and %d requests were admitted; negative elapsed time must not mint tokens", got)
	}
	clk.Set(t0.Add(100 * time.Millisecond))
	if got := tBurst(l, "a", 10); got != 1 {
		t.Fatalf("after the clock recovered to t0+100ms admitted %d; expected exactly 1 (elapsed time is measured from the last forward timestamp, not the backwards one)", got)
	}
}

func TestLimiter_FrozenClockConcurrentAdmissionIsExact(t *testing.T) {
	clk := tNewClock()
	weights := map[string]float64{}
	for i := 0; i < 8; i++ {
		weights[fmt.Sprintf("cu-%d", i)] = 1
	}
	l := tLimiter(t, LimiterConfig{Rate: 800}, clk, weights)
	var wg sync.WaitGroup
	var admitted [8]atomic.Int64
	for i := 0; i < 8; i++ {
		for g := 0; g < 8; g++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				admitted[i].Add(int64(tBurst(l, fmt.Sprintf("cu-%d", i), 200)))
			}(i)
		}
	}
	tWait(t, &wg, 10*time.Second, "concurrent admissions")
	for i := 0; i < 8; i++ {
		if got := admitted[i].Load(); got != 100 {
			t.Errorf("tenant cu-%d admitted %d of 1600 simultaneous requests; expected exactly its 100-token guarantee (a data race or a shared bucket changes this number)", i, got)
		}
		a, d, _ := l.Stats(TenantID(fmt.Sprintf("cu-%d", i)))
		if a != 100 || d != 1500 {
			t.Errorf("tenant cu-%d stats allowed=%d denied=%d; expected 100 and 1500", i, a, d)
		}
	}
}

func TestLimiter_ConcurrentUseWithAdvancingClockRespectsTotalBudget(t *testing.T) {
	clk := tNewClock()
	start := clk.Now()
	weights := map[string]float64{"a": 4, "b": 2, "c": 1, "d": 1}
	l := tLimiter(t, LimiterConfig{Rate: 1000}, clk, weights)
	var wg sync.WaitGroup
	var total atomic.Int64
	stop := make(chan struct{})
	for tn := range weights {
		for g := 0; g < 4; g++ {
			wg.Add(1)
			go func(tn string) {
				defer wg.Done()
				ctx := tCtx(tn)
				for {
					select {
					case <-stop:
						return
					default:
					}
					if l.Allow(ctx).Allowed {
						total.Add(1)
					}
					runtime.Gosched() // keep the test quick on machines with few cores
				}
			}(tn)
		}
	}
	for i := 0; i < 200; i++ {
		clk.Advance(5 * time.Millisecond)
		time.Sleep(100 * time.Microsecond) // let the workers run; correctness does not depend on it
	}
	close(stop)
	tWait(t, &wg, 10*time.Second, "limiter workers")
	elapsed := clk.Now().Sub(start).Seconds()
	budget := 1000*1.0 + 1000*elapsed // initial buckets (Rate x window) + Rate x elapsed
	if float64(total.Load()) > budget+1 {
		t.Fatalf("admitted %d requests; the platform budget over %.1fs is %.0f (initial burst + Rate x elapsed): tokens are being created from nothing", total.Load(), elapsed, budget)
	}
	var sum int64
	for tn := range weights {
		a, d, _ := l.Stats(TenantID(tn))
		sum += a
		if a+d == 0 {
			t.Errorf("tenant %s never got a decision", tn)
		}
	}
	if sum != total.Load() {
		t.Fatalf("per-tenant allowed counters sum to %d but callers saw %d admissions", sum, total.Load())
	}
}
