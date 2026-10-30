package burnrate

import (
	"errors"
	"math"
	"math/rand/v2"
	"sync"
	"testing"
	"time"
)

// ---------- helpers (prefixed with t to avoid clashing with your code) ----------

const (
	tMinute = time.Minute
	tHour   = time.Hour
	tDay    = 24 * time.Hour
)

// tBase is minute-aligned and hour-aligned: bucket arithmetic in the tests is exact.
var tBase = time.Date(2026, 1, 5, 0, 0, 0, 0, time.UTC)

func tAt(minute int) time.Time { return tBase.Add(time.Duration(minute) * tMinute) }

func tTracker(t *testing.T, objective float64, period time.Duration) *Tracker {
	t.Helper()
	tr, err := NewTracker(Config{Objective: objective, Period: period, Resolution: tMinute})
	if err != nil || tr == nil {
		t.Fatalf("NewTracker(objective=%v, period=%v) = (%v, %v); expected a usable tracker", objective, period, tr, err)
	}
	return tr
}

// tFeed records one bucket per minute for `minutes` minutes starting at minute
// `from`: perMin requests, `bad` of them failed.
func tFeed(t *testing.T, tr *Tracker, from, minutes int, perMin, bad uint64) {
	t.Helper()
	for m := from; m < from+minutes; m++ {
		if err := tr.RecordAt(tAt(m), perMin-bad, perMin); err != nil {
			t.Fatalf("RecordAt(minute %d, good=%d, total=%d): %v", m, perMin-bad, perMin, err)
		}
	}
}

func tNear(got, want, tol float64) bool { return math.Abs(got-want) <= tol }

func tFiring(alerts []Alert) map[string]Alert {
	out := map[string]Alert{}
	for _, a := range alerts {
		out[a.Rule.Name] = a
	}
	return out
}

func tEvaluator(t *testing.T, tr *Tracker, rules []Rule) *Evaluator {
	t.Helper()
	ev, err := NewEvaluator(tr, rules)
	if err != nil || ev == nil {
		t.Fatalf("NewEvaluator = (%v, %v); expected a usable evaluator", ev, err)
	}
	return ev
}

var tFast = Rule{Name: "fast", Severity: "page", Long: tHour, Short: 5 * tMinute, Threshold: 14.4}

// ---------- configuration and recording ----------

func TestNewTracker_ValidatesConfig(t *testing.T) {
	bad := []Config{
		{Objective: 0, Period: tDay, Resolution: tMinute},
		{Objective: 1, Period: tDay, Resolution: tMinute},
		{Objective: 1.2, Period: tDay, Resolution: tMinute},
		{Objective: -0.5, Period: tDay, Resolution: tMinute},
		{Objective: math.NaN(), Period: tDay, Resolution: tMinute},
		{Objective: 0.999, Period: 0, Resolution: tMinute},
		{Objective: 0.999, Period: -tDay, Resolution: tMinute},
		{Objective: 0.999, Period: tDay, Resolution: -tMinute},
		{Objective: 0.999, Period: tMinute, Resolution: tHour},
	}
	for _, cfg := range bad {
		if tr, err := NewTracker(cfg); !errors.Is(err, ErrInvalidConfig) || tr != nil {
			t.Errorf("NewTracker(%+v) = (%v, %v); expected (nil, ErrInvalidConfig)", cfg, tr, err)
		}
	}
	tr, err := NewTracker(Config{Objective: 0.999, Period: 30 * tDay}) // zero Resolution => 1 minute
	if err != nil || tr == nil {
		t.Fatalf("NewTracker with default resolution = (%v, %v)", tr, err)
	}
	if err := tr.RecordAt(tAt(0), 1, 1); err != nil {
		t.Fatal(err)
	}
	if err := tr.RecordAt(tAt(0).Add(59*time.Second), 1, 1); err != nil {
		t.Fatal(err)
	}
	if tr.Buckets() != 1 {
		t.Errorf("two events within the same minute made %d buckets; the default resolution is 1 minute", tr.Buckets())
	}
}

func TestRecordAt_RejectsGoodAboveTotal(t *testing.T) {
	tr := tTracker(t, 0.999, 30*tDay)
	if err := tr.RecordAt(tAt(0), 11, 10); !errors.Is(err, ErrInvalidCounts) {
		t.Fatalf("RecordAt(good=11, total=10) = %v, want ErrInvalidCounts", err)
	}
	if _, total, ok := tr.SLI(tAt(0), tMinute); ok || total != 0 {
		t.Errorf("a rejected record changed the tracker: total=%d ok=%v", total, ok)
	}
	if err := tr.RecordAt(tAt(0), 10, 10); err != nil {
		t.Errorf("RecordAt(good=total) = %v; good == total is valid", err)
	}
}

// ---------- SLI over windows ----------

func TestSLI_ExactFractionsOverTrailingWindows(t *testing.T) {
	tr := tTracker(t, 0.999, 30*tDay)
	tFeed(t, tr, 0, 50, 1000, 0)   // minutes 0..49: clean
	tFeed(t, tr, 50, 10, 1000, 10) // minutes 50..59: 1% errors
	now := tAt(59)
	cases := []struct {
		window time.Duration
		bad    float64
		total  uint64
	}{
		{5 * tMinute, 50, 5000},
		{10 * tMinute, 100, 10_000},
		{11 * tMinute, 100, 11_000}, // one clean minute joins
		{tHour, 100, 60_000},
		{2 * tHour, 100, 60_000}, // longer than the data we have
	}
	for _, c := range cases {
		sli, total, ok := tr.SLI(now, c.window)
		want := 1 - c.bad/float64(c.total)
		if !ok || total != c.total || !tNear(sli, want, 1e-12) {
			t.Errorf("SLI(window=%v) = (%v, %d, %v), want (%v, %d, true)", c.window, sli, total, ok, want, c.total)
		}
	}
}

func TestSLI_WindowIncludesCurrentBucketAndExcludesOlderOnes(t *testing.T) {
	tr := tTracker(t, 0.99, 7*tDay)
	// One bad event at 00:10:30 (inside bucket 10).
	if err := tr.RecordAt(tAt(10).Add(30*time.Second), 0, 1); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		now    time.Time
		window time.Duration
		seen   bool
	}{
		{tAt(10).Add(59 * time.Second), tMinute, true},         // same minute, 1-bucket window
		{tAt(10).Add(5 * time.Second), 30 * time.Second, true}, // sub-resolution windows round up to 1 bucket
		{tAt(11), tMinute, false},                              // next minute, 1-bucket window: gone
		{tAt(11), 2 * tMinute, true},                           // buckets 10 and 11
		{tAt(12), 2 * tMinute, false},                          // buckets 11 and 12
		{tAt(12), 3 * tMinute, true},
		{tAt(9), 5 * tMinute, false},       // the event is in the future of "now": not counted yet
		{tAt(11), 90 * time.Second, true},  // 90 s rounds UP to 2 buckets: 10 and 11
		{tAt(12), 90 * time.Second, false}, // buckets 11 and 12
	}
	for _, c := range cases {
		_, total, ok := tr.SLI(c.now, c.window)
		if (ok && total == 1) != c.seen {
			t.Errorf("SLI(now=+%v, window=%v): saw the event = %v, want %v (window = ceil(window/resolution) buckets ending with now's bucket)",
				c.now.Sub(tBase), c.window, ok && total == 1, c.seen)
		}
	}
}

func TestSLI_NoTrafficIsUnknownNotPerfect(t *testing.T) {
	tr := tTracker(t, 0.999, 30*tDay)
	if sli, total, ok := tr.SLI(tAt(100), tHour); ok || total != 0 || sli != 0 {
		t.Errorf("SLI with no events = (%v, %d, %v); expected (0, 0, false): 0/0 is unknown, not 100%%", sli, total, ok)
	}
	if b, ok := tr.BurnRate(tAt(100), tHour); ok || b != 0 {
		t.Errorf("BurnRate with no events = (%v, %v), want (0, false)", b, ok)
	}
	tFeed(t, tr, 200, 1, 100, 0)
	if _, _, ok := tr.SLI(tAt(100), tHour); ok {
		t.Error("an event at minute 200 was counted in a query at minute 100")
	}
	if _, total, ok := tr.SLI(tAt(200), tHour); !ok || total != 100 {
		t.Errorf("once now reaches the event, SLI should see it: total=%d ok=%v", total, ok)
	}
}

func TestSLI_FutureEventsAreNotCountedEvenWithManyBuckets(t *testing.T) {
	tr := tTracker(t, 0.999, 30*tDay)
	tFeed(t, tr, 0, 100, 1000, 0)
	if err := tr.RecordAt(tAt(103), 0, 777); err != nil { // clock-skewed producer: 3 minutes ahead
		t.Fatal(err)
	}
	if sli, total, ok := tr.SLI(tAt(99), 10*tMinute); !ok || total != 10_000 || sli != 1 {
		t.Errorf("SLI at minute 99 = (%v, %d, %v); the event stamped minute 103 is in the future and must not count yet", sli, total, ok)
	}
	if _, total, _ := tr.SLI(tAt(103), 10*tMinute); total != 6*1000+777 {
		t.Errorf("SLI at minute 103 saw %d events, want %d once now catches up", total, 6*1000+777)
	}
}

func TestRecordAt_OutOfOrderMatchesInOrder(t *testing.T) {
	ordered := tTracker(t, 0.999, 30*tDay)
	shuffled := tTracker(t, 0.999, 30*tDay)
	type ev struct {
		m         int
		good, tot uint64
	}
	var evs []ev
	r := rand.New(rand.NewPCG(4, 4))
	for m := 0; m < 400; m++ {
		tot := uint64(100 + r.IntN(900))
		evs = append(evs, ev{m, tot - uint64(r.IntN(int(tot/20)+1)), tot})
	}
	for _, e := range evs {
		if err := ordered.RecordAt(tAt(e.m), e.good, e.tot); err != nil {
			t.Fatal(err)
		}
	}
	r.Shuffle(len(evs), func(i, j int) { evs[i], evs[j] = evs[j], evs[i] })
	for _, e := range evs {
		if err := shuffled.RecordAt(tAt(e.m), e.good, e.tot); err != nil {
			t.Fatal(err)
		}
	}
	for _, w := range []time.Duration{5 * tMinute, 30 * tMinute, tHour, 6 * tHour} {
		a, at, _ := ordered.SLI(tAt(399), w)
		b, bt, _ := shuffled.SLI(tAt(399), w)
		if a != b || at != bt {
			t.Errorf("window %v: in-order (%v, %d) != shuffled (%v, %d); late events must land in their own bucket", w, a, at, b, bt)
		}
	}
}

func TestTracker_MemoryIsBoundedByPeriodAndOldDataIsForgotten(t *testing.T) {
	tr := tTracker(t, 0.999, 2*tHour)
	tFeed(t, tr, 0, 600, 100, 1) // ten hours of traffic into a 2-hour tracker
	if n := tr.Buckets(); n > 120 {
		t.Errorf("Buckets() = %d after 10 h with a 2 h period; want ≤ 120 (memory must be bounded by Period/Resolution)", n)
	}
	// A window longer than retention only sees what was kept.
	if _, total, _ := tr.SLI(tAt(599), 10*tHour); total != 120*100 {
		t.Errorf("a 10 h window saw %d events; only the last 2 h (12000) are retained", total)
	}
	// A very late event, older than retention, is dropped, not resurrected.
	if err := tr.RecordAt(tAt(0), 0, 5000); err != nil {
		t.Fatal(err)
	}
	if n := tr.Buckets(); n > 120 {
		t.Errorf("Buckets() = %d after a stale record", n)
	}
	if _, total, _ := tr.SLI(tAt(599), 10*tHour); total != 120*100 {
		t.Errorf("a stale event (older than the period) changed the totals to %d", total)
	}
	// An event inside retention but behind the newest one is still accepted.
	if err := tr.RecordAt(tAt(599-100), 0, 7); err != nil {
		t.Fatal(err)
	}
	if _, total, _ := tr.SLI(tAt(599), 10*tHour); total != 120*100+7 {
		t.Errorf("a late-but-retained event was lost: total=%d", total)
	}
}

// ---------- burn rate, budget, time to exhaustion ----------

func TestBurnRate_IsErrorRateOverAllowedErrorRate(t *testing.T) {
	cases := []struct {
		name       string
		objective  float64
		bad, total uint64
		want       float64
	}{
		{"on budget exactly", 0.999, 1, 1000, 1},
		{"10x", 0.999, 10, 1000, 10},
		{"full outage at 99.9%", 0.999, 1000, 1000, 1000},
		{"full outage at 99%", 0.99, 1000, 1000, 100},
		{"healthy", 0.999, 0, 1000, 0},
		{"half the allowance", 0.99, 5, 1000, 0.5},
	}
	for _, c := range cases {
		tr := tTracker(t, c.objective, 30*tDay)
		if err := tr.RecordAt(tAt(3), c.total-c.bad, c.total); err != nil {
			t.Fatal(err)
		}
		got, ok := tr.BurnRate(tAt(3), 5*tMinute)
		if !ok || !tNear(got, c.want, 1e-9*math.Max(1, c.want)) {
			t.Errorf("%s: BurnRate = (%v, %v), want %v", c.name, got, ok, c.want)
		}
	}
}

func TestBudgetRemaining_IsRelativeToTrafficInThePeriod(t *testing.T) {
	cases := []struct {
		name       string
		bad, total uint64
		want       float64
	}{
		{"untouched", 0, 1_000_000, 1},
		{"half spent", 500, 1_000_000, 0.5},
		{"exactly spent", 1000, 1_000_000, 0},
		{"overspent twice", 2000, 1_000_000, -1},
		{"more traffic, same errors, more budget", 500, 2_000_000, 0.75},
	}
	for _, c := range cases {
		tr := tTracker(t, 0.999, 30*tDay)
		if err := tr.RecordAt(tAt(0), c.total-c.bad, c.total); err != nil {
			t.Fatal(err)
		}
		if got := tr.BudgetRemaining(tAt(0)); !tNear(got, c.want, 1e-9) {
			t.Errorf("%s: BudgetRemaining = %v, want %v (allowed bad = (1−0.999) × total)", c.name, got, c.want)
		}
	}
	if got := tTracker(t, 0.999, 30*tDay).BudgetRemaining(tAt(0)); got != 1 {
		t.Errorf("no traffic: BudgetRemaining = %v, want 1", got)
	}
}

func TestBudgetRemaining_ForgetsErrorsOlderThanThePeriod(t *testing.T) {
	tr := tTracker(t, 0.999, 30*tDay)
	if err := tr.RecordAt(tAt(0), 0, 1000); err != nil { // a total outage minute, day 0
		t.Fatal(err)
	}
	if got := tr.BudgetRemaining(tAt(5)); got > 0 {
		t.Errorf("BudgetRemaining right after the outage should be spent, got %v", got)
	}
	m := 31 * 24 * 60 // 31 days later
	if err := tr.RecordAt(tAt(m), 1000, 1000); err != nil {
		t.Fatal(err)
	}
	if got := tr.BudgetRemaining(tAt(m)); got != 1 {
		t.Errorf("BudgetRemaining 31 days after the outage = %v, want 1: the outage left the 30-day period", got)
	}
}

func TestTimeToExhaustion_UsesRemainingBudgetAndCurrentBurn(t *testing.T) {
	tr := tTracker(t, 0.999, 30*tDay)
	tFeed(t, tr, 0, 30*24*60, 1000, 0) // 30 days, clean
	last := 30*24*60 - 1
	if _, ok := tr.TimeToExhaustion(tAt(last), 5*tMinute); ok {
		t.Error("with zero errors the budget never runs out: expected ok == false")
	}
	// One full-outage minute: 1000 bad of 43.2M requests => 1/43.2 of the budget is gone.
	tr2 := tTracker(t, 0.999, 30*tDay)
	tFeed(t, tr2, 0, 30*24*60-1, 1000, 0)
	tFeed(t, tr2, last, 1, 1000, 1000)
	rem := tr2.BudgetRemaining(tAt(last))
	if !tNear(rem, 1-1000.0/43200, 1e-9) {
		t.Fatalf("BudgetRemaining = %v, want %v", rem, 1-1000.0/43200)
	}
	burn, ok := tr2.BurnRate(tAt(last), 5*tMinute)
	if !ok || !tNear(burn, 200, 1e-6) {
		t.Fatalf("BurnRate over 5 min = (%v, %v), want 200 (1 bad minute of 5 = 20%% errors / 0.1%%)", burn, ok)
	}
	d, ok := tr2.TimeToExhaustion(tAt(last), 5*tMinute)
	want := 12660 * time.Second // remaining 0.97685 × 30 d / 200 = 3 h 31 min
	if !ok || d < want-2*time.Second || d > want+2*time.Second {
		t.Errorf("TimeToExhaustion = (%v, %v), want ≈ %v = remaining × period / burn", d, ok, want)
	}
	// Already overspent: zero, and ok (it is exhausted now).
	tr3 := tTracker(t, 0.999, 30*tDay)
	if err := tr3.RecordAt(tAt(0), 0, 1000); err != nil {
		t.Fatal(err)
	}
	if d, ok := tr3.TimeToExhaustion(tAt(0), 5*tMinute); !ok || d != 0 {
		t.Errorf("overspent budget: TimeToExhaustion = (%v, %v), want (0, true)", d, ok)
	}
}

func TestTimeToExhaustion_ShorterWindowsSeeFasterBurn(t *testing.T) {
	// 30 clean days (43.2M requests), then one total-outage minute. Over a
	// 1-minute window the burn is 1/(1−0.999) = 1000, so the (almost fresh)
	// budget lasts ≈ 30 d / 1000 = 43.2 min × remaining (0.97685) = 42.2 min.
	// Over a 1-hour window the same minute is only 1/60 of the traffic:
	// burn 16.67, and the budget "lasts" 60× longer.
	tr := tTracker(t, 0.999, 30*tDay)
	last := 30*24*60 - 1
	tFeed(t, tr, 0, last, 1000, 0)
	tFeed(t, tr, last, 1, 1000, 1000)
	d1, ok1 := tr.TimeToExhaustion(tAt(last), tMinute)
	d60, ok60 := tr.TimeToExhaustion(tAt(last), tHour)
	if !ok1 || !ok60 {
		t.Fatalf("expected both estimates to exist, got ok=%v/%v", ok1, ok60)
	}
	if want := 2532 * time.Second; d1 < want-2*time.Second || d1 > want+2*time.Second {
		t.Errorf("1-minute-window estimate = %v, want ≈ %v", d1, want)
	}
	if want := 2532 * time.Second * 60; d60 < want-120*time.Second || d60 > want+120*time.Second {
		t.Errorf("1-hour-window estimate = %v, want ≈ %v (60× longer: the same minute is 1/60 of the window)", d60, want)
	}
}

// ---------- rules and the evaluator ----------

func TestDefaultRules_ThresholdsFollowTheBudgetFormula(t *testing.T) {
	// threshold = budgetFraction × period / longWindow
	check := func(period time.Duration, want []Rule) {
		t.Helper()
		got := DefaultRules(period)
		if len(got) != len(want) {
			t.Fatalf("DefaultRules(%v) has %d rules, want %d", period, len(got), len(want))
		}
		for i := range want {
			g, w := got[i], want[i]
			if g.Severity != w.Severity || g.Long != w.Long || g.Short != w.Short || !tNear(g.Threshold, w.Threshold, 1e-9) || g.Name == "" {
				t.Errorf("rule %d for %v = %+v, want severity %q long %v short %v threshold %v", i, period, g, w.Severity, w.Long, w.Short, w.Threshold)
			}
		}
	}
	check(30*tDay, []Rule{
		{Severity: "page", Long: tHour, Short: 5 * tMinute, Threshold: 14.4},   // 2% of the budget in 1 h
		{Severity: "page", Long: 6 * tHour, Short: 30 * tMinute, Threshold: 6}, // 5% in 6 h
		{Severity: "ticket", Long: 3 * tDay, Short: 6 * tHour, Threshold: 1},   // 10% in 3 d
	})
	check(7*tDay, []Rule{
		{Severity: "page", Long: tHour, Short: 5 * tMinute, Threshold: 0.02 * 168},
		{Severity: "page", Long: 6 * tHour, Short: 30 * tMinute, Threshold: 0.05 * 168 / 6},
		{Severity: "ticket", Long: 3 * tDay, Short: 6 * tHour, Threshold: 0.10 * 168 / 72},
	})
}

func TestNewEvaluator_ValidatesRules(t *testing.T) {
	tr := tTracker(t, 0.999, 30*tDay)
	bad := []Rule{
		{Name: "no long", Long: 0, Short: tMinute, Threshold: 1},
		{Name: "negative short", Long: tHour, Short: -tMinute, Threshold: 1},
		{Name: "short > long", Long: tMinute, Short: tHour, Threshold: 1},
		{Name: "zero threshold", Long: tHour, Short: tMinute, Threshold: 0},
		{Name: "negative threshold", Long: tHour, Short: tMinute, Threshold: -3},
		{Name: "NaN threshold", Long: tHour, Short: tMinute, Threshold: math.NaN()},
	}
	for _, r := range bad {
		if ev, err := NewEvaluator(tr, []Rule{tFast, r}); !errors.Is(err, ErrInvalidRule) || ev != nil {
			t.Errorf("NewEvaluator with rule %q = (%v, %v); expected (nil, ErrInvalidRule)", r.Name, ev, err)
		}
	}
	if ev, err := NewEvaluator(nil, nil); !errors.Is(err, ErrInvalidRule) || ev != nil {
		t.Errorf("NewEvaluator(nil tracker) = (%v, %v); expected ErrInvalidRule", ev, err)
	}
	if _, err := NewEvaluator(tr, []Rule{{Name: "single window", Long: tHour, Threshold: 2}}); err != nil {
		t.Errorf("a rule with Short == 0 (single window) is valid, got %v", err)
	}
}

func TestEvaluate_FiresOnlyWhenBothWindowsExceedTheThreshold(t *testing.T) {
	const history = 3 * 24 * 60 // 3 clean days so the 3-day window is well populated
	build := func(t *testing.T, start, minutes int, bad uint64) (*Evaluator, time.Time, *Tracker) {
		tr := tTracker(t, 0.999, 30*tDay)
		tFeed(t, tr, 0, history, 1000, 0)
		tFeed(t, tr, start, minutes, 1000, bad)
		return tEvaluator(t, tr, DefaultRules(30*tDay)), tAt(start + minutes - 1), tr
	}
	t.Run("both windows burning fires, with the burn rates reported", func(t *testing.T) {
		ev, now, _ := build(t, history, 60, 1000) // an hour of total outage
		a, ok := tFiring(ev.Evaluate(now))["page-fast-burn"]
		if !ok {
			t.Fatalf("page-fast-burn did not fire during a total outage; alerts: %+v", ev.Evaluate(now))
		}
		if !tNear(a.LongBurn, 1000, 1e-6) || !tNear(a.ShortBurn, 1000, 1e-6) {
			t.Errorf("burns = (%v, %v), want (1000, 1000)", a.LongBurn, a.ShortBurn)
		}
		if a.Rule.Severity != "page" || a.Rule.Long != tHour {
			t.Errorf("alert carries rule %+v", a.Rule)
		}
	})
	t.Run("short blip: short window hot, long window cool, no alert", func(t *testing.T) {
		ev, now, tr := build(t, history, 3, 10) // 3 minutes at 1% errors
		short, _ := tr.BurnRate(now, 5*tMinute)
		long, _ := tr.BurnRate(now, tHour)
		if short < 5 || long > 1 {
			t.Fatalf("scenario broken: short burn %v, long burn %v", short, long)
		}
		if got := ev.Evaluate(now); len(got) != 0 {
			t.Errorf("a 3-minute 1%% error blip fired %+v; only a rule whose long window is also burning may fire", got)
		}
	})
	t.Run("a blip hot enough to burn 5% of an hour's allowance does page", func(t *testing.T) {
		ev, now, _ := build(t, history, 3, 1000) // 3 minutes of total outage
		if _, ok := tFiring(ev.Evaluate(now))["page-fast-burn"]; !ok {
			t.Error("3 minutes of total outage is burn 50 over the hour and 600 over 5 minutes: it must page")
		}
	})
	t.Run("outage over: long window hot, short window cool, no alert", func(t *testing.T) {
		tr := tTracker(t, 0.999, 30*tDay)
		tFeed(t, tr, 0, history, 1000, 0)
		tFeed(t, tr, history, 30, 1000, 1000) // 30 min total outage
		tFeed(t, tr, history+30, 10, 1000, 0)
		ev := tEvaluator(t, tr, []Rule{tFast})
		now := tAt(history + 39)
		if long, _ := tr.BurnRate(now, tHour); long < 14.4 {
			t.Fatalf("scenario broken: long burn %v should still exceed 14.4", long)
		}
		if got := ev.Evaluate(now); len(got) != 0 {
			t.Errorf("fired %+v ten minutes after recovery; the short window is clean so this must not alert", got)
		}
	})
	t.Run("several rules fire in rule order", func(t *testing.T) {
		ev, now, _ := build(t, history, 8*60, 1000) // 8 hours of total outage
		got := ev.Evaluate(now)
		if len(got) != 3 || got[0].Rule.Name != "page-fast-burn" || got[1].Rule.Name != "page-slow-burn" || got[2].Rule.Name != "ticket-slow-burn" {
			names := []string{}
			for _, a := range got {
				names = append(names, a.Rule.Name)
			}
			t.Errorf("alerts = %v, want all three rules in rule order", names)
		}
	})
}

func TestEvaluate_ShortWindowResetsAnAlertWhileTheLongWindowIsStillHot(t *testing.T) {
	const outageStart, outageMinutes = 3 * 24 * 60, 60
	tr := tTracker(t, 0.999, 30*tDay)
	tFeed(t, tr, 0, outageStart, 1000, 0)
	tFeed(t, tr, outageStart, outageMinutes, 1000, 1000)
	ev := tEvaluator(t, tr, []Rule{tFast})
	lastBad := outageStart + outageMinutes - 1
	firing := func(m int) bool { return len(ev.Evaluate(tAt(m))) > 0 }

	if !firing(lastBad) {
		t.Fatal("not firing at the end of the outage")
	}
	resetAt := -1
	for m := lastBad + 1; m < lastBad+30; m++ {
		tFeed(t, tr, m, 1, 1000, 0)
		if !firing(m) {
			resetAt = m
			break
		}
	}
	if resetAt != lastBad+5 {
		t.Errorf("alert reset %d minutes after the last bad minute, want 5: the 5-minute short window must clear exactly when its last bad bucket leaves it", resetAt-lastBad)
	}
	if long, _ := tr.BurnRate(tAt(resetAt), tHour); long < 14.4 {
		t.Errorf("at reset the long window burn is %v; the point of the test is that it is STILL above 14.4", long)
	}
}

func TestEvaluate_SingleWindowRuleKeepsFiringLongAfterTheIncident(t *testing.T) {
	const outageStart, outageMinutes = 3 * 24 * 60, 5
	tr := tTracker(t, 0.999, 30*tDay)
	tFeed(t, tr, 0, outageStart, 1000, 0)
	tFeed(t, tr, outageStart, outageMinutes, 1000, 1000)
	lastBad := outageStart + outageMinutes - 1
	tFeed(t, tr, lastBad+1, 120, 1000, 0) // two clean hours after the outage
	single := tEvaluator(t, tr, []Rule{{Name: "single", Severity: "page", Long: tHour, Short: 0, Threshold: 14.4}})
	multi := tEvaluator(t, tr, []Rule{tFast})

	// Windows are trailing, so evaluating at an earlier "now" ignores later buckets.
	got := single.Evaluate(tAt(lastBad))
	if len(got) != 1 || got[0].ShortBurn != 0 {
		t.Fatalf("single-window rule at the end of the outage = %+v; expected it to fire with ShortBurn 0 (no short window)", got)
	}
	mid := lastBad + 40
	if len(single.Evaluate(tAt(mid))) != 1 {
		t.Error("single-window rule stopped 40 minutes after a 5-minute outage; its 1 h window still holds the bad minutes")
	}
	if len(multi.Evaluate(tAt(mid))) != 0 {
		t.Error("multi-window rule still fires 40 minutes after the outage; the 5-minute short window should have reset it")
	}
	stop := -1
	for m := lastBad; m < lastBad+120; m++ {
		if len(single.Evaluate(tAt(m))) == 0 {
			stop = m
			break
		}
	}
	// The first bad bucket leaves the 60-bucket window 60 minutes after it entered... the last one at outageStart+4+60.
	if want := lastBad + 60; stop != want {
		t.Errorf("single-window alert stopped at minute +%d after the outage start, want +%d: it only stops when the last bad minute leaves the 1 h window",
			stop-outageStart, want-outageStart)
	}
}

func TestEvaluate_NoTrafficNeverFires(t *testing.T) {
	tr := tTracker(t, 0.999, 30*tDay)
	ev := tEvaluator(t, tr, DefaultRules(30*tDay))
	if got := ev.Evaluate(tAt(1000)); len(got) != 0 {
		t.Errorf("fired %+v with no data at all", got)
	}
	// Long window has an outage, but the short window has seen no traffic at all
	// (a dead load balancer looks exactly like this: no requests, no errors).
	tFeed(t, tr, 0, 30, 1000, 1000)
	ev2 := tEvaluator(t, tr, []Rule{tFast})
	if got := ev2.Evaluate(tAt(29)); len(got) != 1 {
		t.Fatalf("expected an alert at the end of the outage, got %+v", got)
	}
	if got := ev2.Evaluate(tAt(29 + 10)); len(got) != 0 {
		t.Errorf("fired %+v when the short window has no traffic: a window with no events cannot exceed a threshold", got)
	}
}

func TestEvaluate_DetectionTimeScalesWithSeverity(t *testing.T) {
	const start = 3 * 24 * 60
	detect := func(t *testing.T, bad uint64, rule Rule, maxMinutes int) int {
		t.Helper()
		tr := tTracker(t, 0.999, 30*tDay)
		tFeed(t, tr, 0, start, 1000, 0)
		ev := tEvaluator(t, tr, []Rule{rule})
		for k := 0; k < maxMinutes; k++ {
			tFeed(t, tr, start+k, 1, 1000, bad)
			if len(ev.Evaluate(tAt(start+k))) > 0 {
				return k + 1 // minutes of incident, including the current one
			}
		}
		return -1
	}
	// Total outage (burn 1000): 1 bad minute is already 1.67% of the hour => detected in the first minute.
	if got := detect(t, 1000, tFast, 10); got != 1 {
		t.Errorf("total outage detected after %d minutes, want 1", got)
	}
	// 2% errors (burn 20): the 1 h window needs 14.4/20 = 72% of its length, i.e. 43.2 => 44 minutes.
	if got := detect(t, 20, tFast, 120); got != 44 {
		t.Errorf("2%% error rate detected after %d minutes, want 44 (= ceil(60 × 14.4 / 20))", got)
	}
	// 1% errors (burn 10) never reaches the 14.4× page threshold.
	if got := detect(t, 10, tFast, 180); got != -1 {
		t.Errorf("a burn of 10 paged after %d minutes; the 14.4× rule must stay silent for it", got)
	}
}

func TestEvaluate_SlowBurnIsInvisibleToPagesButCaughtByTheTicketRule(t *testing.T) {
	// 0.5% errors = burn 5: the 30-day budget is gone in 6 days. Below the
	// 6× page threshold, above the 1× ticket threshold.
	const start = 3 * 24 * 60
	tr := tTracker(t, 0.999, 30*tDay)
	tFeed(t, tr, 0, start, 1000, 0)
	ev := tEvaluator(t, tr, DefaultRules(30*tDay))
	firstTicket := -1
	for k := 0; k < 4*24*60; k++ {
		tFeed(t, tr, start+k, 1, 1000, 5)
		for _, a := range ev.Evaluate(tAt(start + k)) {
			if a.Rule.Severity == "page" {
				t.Fatalf("a burn of 5 paged (%s) after %d minutes; it is below both page thresholds", a.Rule.Name, k+1)
			}
			if a.Rule.Name == "ticket-slow-burn" && firstTicket < 0 {
				firstTicket = k + 1
			}
		}
	}
	// The 3-day window needs 1/5 of its 4320 minutes to be at 5× => 864 minutes (14.4 h).
	if firstTicket < 864 || firstTicket > 866 {
		t.Errorf("ticket fired after %d minutes, want ≈ 864 (14.4 h): 3 days × (1× / 5×)", firstTicket)
	}
}

// ---------- concurrency ----------

func TestTracker_ConcurrentRecordAndQueryIsExact(t *testing.T) {
	const writers, perWriter = 8, 2000
	tr := tTracker(t, 0.99, 7*tDay)
	var wg sync.WaitGroup
	stop := make(chan struct{})
	var readers sync.WaitGroup
	ev := tEvaluator(t, tr, DefaultRules(7*tDay))
	for i := 0; i < 2; i++ {
		readers.Add(1)
		go func() {
			defer readers.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				tr.SLI(tAt(10), tHour)
				tr.BudgetRemaining(tAt(10))
				tr.TimeToExhaustion(tAt(10), tHour)
				ev.Evaluate(tAt(10))
				tr.Buckets()
			}
		}()
	}
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < perWriter; i++ {
				if err := tr.RecordAt(tAt(i%10), 9, 10); err != nil {
					t.Error(err)
					return
				}
			}
		}()
	}
	done := make(chan struct{})
	go func() { wg.Wait(); close(stop); readers.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("concurrent Record/query did not finish within 20s (deadlock?)")
	}
	sli, total, ok := tr.SLI(tAt(9), tHour)
	if !ok || total != writers*perWriter*10 || !tNear(sli, 0.9, 1e-12) {
		t.Errorf("SLI = (%v, %d, %v), want (0.9, %d, true): updates were lost", sli, total, ok, writers*perWriter*10)
	}
}
