// Package burnrate is the lab for A8 Unit 2: SLO error budgets and
// multi-window, multi-burn-rate alerts.
//
// A Tracker stores (good, total) event counts in fixed-width time buckets and
// answers four questions about any trailing window: what was the SLI, how
// much error budget is left, how fast is it burning, and when will it run
// out. An Evaluator turns those answers into alerts using Google-SRE-
// Workbook-style window pairs: a rule fires only when BOTH its long and its
// short window burn faster than the rule's threshold.
//
// Time is always a parameter (Record takes the event time, every query takes
// "now"), so the whole package is deterministic: tests never sleep, and a
// production wrapper simply passes time.Now().
//
// Suggested order: NewTracker, RecordAt, SLI, BurnRate, BudgetRemaining,
// TimeToExhaustion, DefaultRules, NewEvaluator, Evaluate.
// Run: go test -race ./labs/a8/burnrate/
//
// # Window semantics (tests depend on this exactly)
//
// Time is cut into buckets of width Resolution, aligned to the Unix epoch:
// bucket index = floor(unixNano / Resolution). An event belongs to the bucket
// containing its timestamp. A query at "now" over a trailing window w uses
// n = ceil(w / Resolution) buckets: the bucket containing now and the n−1
// buckets before it. So with Resolution = 1 minute, a 5-minute window at
// 12:07:30 covers the buckets 12:03, 12:04, 12:05, 12:06 and 12:07 (the
// current, partial minute is included). Buckets after "now" are not counted
// until now reaches them.
//
// # Definitions
//
//	SLI              good / total over the window
//	error rate       1 − SLI
//	burn rate        error rate / (1 − Objective); 1.0 means "spending exactly
//	                 the budget the objective allows"
//	budget remaining 1 − bad / ((1 − Objective) × total), over the whole SLO
//	                 Period; negative once overspent; 1 when there is no traffic
//	time to exhaust  remaining × Period / burn rate (constant-rate assumption)
package burnrate

import (
	"errors"
	"fmt"
	"math"
	"sync"
	"time"
)

// Errors returned by this package.
var (
	// ErrInvalidConfig: Objective not in (0,1), or Period/Resolution not
	// positive, or Resolution > Period.
	ErrInvalidConfig = errors.New("burnrate: invalid config")
	// ErrInvalidCounts: good > total.
	ErrInvalidCounts = errors.New("burnrate: good must not exceed total")
	// ErrInvalidRule: a Rule has a non-positive window or threshold, or its
	// short window is longer than its long window.
	ErrInvalidRule = errors.New("burnrate: invalid rule")
)

// Config configures a Tracker.
type Config struct {
	// Objective is the target SLI, e.g. 0.999. Must be in (0, 1).
	Objective float64
	// Period is the SLO compliance window, e.g. 30 * 24 * time.Hour. Data
	// older than Period buckets is discarded.
	Period time.Duration
	// Resolution is the bucket width, e.g. time.Minute. Zero means one
	// minute. Must not exceed Period.
	Resolution time.Duration
}

type cell struct{ good, total uint64 }

// Tracker is a rolling-window SLO tracker. Safe for concurrent use.
type Tracker struct {
	mu         sync.Mutex
	cfg        Config
	periodBkts int64
	cells      map[int64]*cell
	maxIdx     int64
	any        bool
}

// NewTracker validates cfg and returns an empty Tracker.
func NewTracker(cfg Config) (*Tracker, error) {
	if cfg.Resolution == 0 {
		cfg.Resolution = time.Minute
	}
	if !(cfg.Objective > 0 && cfg.Objective < 1) || cfg.Period <= 0 || cfg.Resolution < 0 || cfg.Resolution > cfg.Period {
		return nil, fmt.Errorf("%w: %+v", ErrInvalidConfig, cfg)
	}
	return &Tracker{
		cfg:        cfg,
		periodBkts: bucketsIn(cfg.Period, cfg.Resolution),
		cells:      map[int64]*cell{},
	}, nil
}

func bucketsIn(d, res time.Duration) int64 {
	n := int64((d + res - 1) / res)
	if n < 1 {
		n = 1
	}
	return n
}

func (t *Tracker) index(at time.Time) int64 {
	return at.UnixNano() / int64(t.cfg.Resolution)
}

// Objective returns the configured objective.
func (t *Tracker) Objective() float64 { return t.cfg.Objective }

// Period returns the configured SLO period.
func (t *Tracker) Period() time.Duration { return t.cfg.Period }

// RecordAt adds good and total events that happened at time at. Events may
// arrive out of order. good > total is rejected with ErrInvalidCounts (and
// nothing is recorded). Events older than the retention (more than Period
// before the newest event ever recorded) are silently dropped.
func (t *Tracker) RecordAt(at time.Time, good, total uint64) error {
	if good > total {
		return ErrInvalidCounts
	}
	idx := t.index(at)
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.any && idx <= t.maxIdx-t.periodBkts {
		return nil
	}
	c := t.cells[idx]
	if c == nil {
		c = &cell{}
		t.cells[idx] = c
	}
	c.good += good
	c.total += total
	if !t.any || idx > t.maxIdx {
		old, hadAny := t.maxIdx, t.any
		t.maxIdx, t.any = idx, true
		t.prune(old, hadAny)
	}
	return nil
}

// prune forgets buckets that fell out of the retention window when maxIdx
// advanced from oldMax. Amortised O(1) per new bucket. t.mu held.
func (t *Tracker) prune(oldMax int64, hadAny bool) {
	hi := t.maxIdx - t.periodBkts // newest key to drop
	if !hadAny {
		return
	}
	lo := oldMax - t.periodBkts + 1 // oldest key that was kept so far
	if hi-lo+1 > int64(len(t.cells)) {
		for k := range t.cells {
			if k <= hi {
				delete(t.cells, k)
			}
		}
		return
	}
	for k := lo; k <= hi; k++ {
		delete(t.cells, k)
	}
}

// Buckets returns the number of buckets currently stored (memory use). It
// never exceeds Period/Resolution rounded up.
func (t *Tracker) Buckets() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.cells)
}

// sum adds the n buckets ending at the one containing now. t.mu held.
func (t *Tracker) sum(now time.Time, n int64) (good, total uint64) {
	cur := t.index(now)
	if n > int64(len(t.cells)) {
		for k, c := range t.cells {
			if k > cur-n && k <= cur {
				good += c.good
				total += c.total
			}
		}
		return
	}
	for k := cur - n + 1; k <= cur; k++ {
		if c := t.cells[k]; c != nil {
			good += c.good
			total += c.total
		}
	}
	return
}

func (t *Tracker) window(now time.Time, w time.Duration) (good, total uint64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.sum(now, bucketsIn(w, t.cfg.Resolution))
}

// SLI returns good/total over the trailing window ending at now, and the
// total event count. ok is false when the window saw no events (an SLI of
// "0 / 0" is not 100%, it is unknown).
func (t *Tracker) SLI(now time.Time, window time.Duration) (sli float64, total uint64, ok bool) {
	good, total := t.window(now, window)
	if total == 0 {
		return 0, 0, false
	}
	return float64(good) / float64(total), total, true
}

// BurnRate returns the error rate over the window divided by the error rate
// the objective allows. ok is false when the window saw no events.
func (t *Tracker) BurnRate(now time.Time, window time.Duration) (float64, bool) {
	good, total := t.window(now, window)
	if total == 0 {
		return 0, false
	}
	return t.burn(good, total), true
}

func (t *Tracker) burn(good, total uint64) float64 {
	return (float64(total-good) / float64(total)) / (1 - t.cfg.Objective)
}

// BudgetRemaining returns the fraction of the error budget left over the SLO
// Period ending at now: 1 − bad / ((1 − Objective) × total). 1 means
// untouched (or no traffic), 0 means exactly spent, negative means the SLO is
// violated for the period.
func (t *Tracker) BudgetRemaining(now time.Time) float64 {
	good, total := t.window(now, t.cfg.Period)
	if total == 0 {
		return 1
	}
	allowed := (1 - t.cfg.Objective) * float64(total)
	return 1 - float64(total-good)/allowed
}

// TimeToExhaustion estimates how long until the error budget is gone if the
// burn rate seen over window continues: remaining × Period / burn. If the
// budget is already spent it returns (0, true). If the window has no
// traffic or no errors (burn rate 0) the budget never runs out at this
// rate: (0, false).
func (t *Tracker) TimeToExhaustion(now time.Time, window time.Duration) (time.Duration, bool) {
	rem := t.BudgetRemaining(now)
	if rem <= 0 {
		return 0, true
	}
	b, ok := t.BurnRate(now, window)
	if !ok || b <= 0 {
		return 0, false
	}
	return time.Duration(rem * float64(t.cfg.Period) / b), true
}

// Rule is one multi-window burn-rate alert: it fires when the burn rate over
// the Long window AND over the Short window are both >= Threshold. The long
// window gives significance (enough budget really was spent), the short
// window gives fast reset (the problem is still happening). Short == 0
// disables the short-window condition, i.e. a classic single-window alert.
type Rule struct {
	Name      string
	Severity  string // "page" or "ticket"
	Long      time.Duration
	Short     time.Duration
	Threshold float64
}

// Alert is a firing rule with the burn rates that triggered it.
type Alert struct {
	Rule      Rule
	LongBurn  float64
	ShortBurn float64 // 0 when the rule has no short window
}

// DefaultRules returns the three window pairs recommended in the Google SRE
// Workbook, with thresholds derived from the budget they are meant to catch:
// threshold = budgetFraction × period / longWindow.
//
//	page   1 h  / 5 min   2% of the budget  -> 14.4 × for a 30-day period
//	page   6 h  / 30 min  5% of the budget  ->  6 ×
//	ticket 3 d  / 6 h     10% of the budget ->  1 ×
func DefaultRules(period time.Duration) []Rule {
	mk := func(name, sev string, long, short time.Duration, fraction float64) Rule {
		return Rule{name, sev, long, short, fraction * float64(period) / float64(long)}
	}
	return []Rule{
		mk("page-fast-burn", "page", time.Hour, 5*time.Minute, 0.02),
		mk("page-slow-burn", "page", 6*time.Hour, 30*time.Minute, 0.05),
		mk("ticket-slow-burn", "ticket", 72*time.Hour, 6*time.Hour, 0.10),
	}
}

// Evaluator evaluates rules against a Tracker. It is stateless: whether a
// rule fires is a pure function of the data and "now", which is what makes
// the short window reset it quickly.
type Evaluator struct {
	t     *Tracker
	rules []Rule
}

// NewEvaluator validates the rules (Long > 0, Threshold > 0, 0 <= Short <=
// Long) and returns an Evaluator. The rules are copied.
func NewEvaluator(t *Tracker, rules []Rule) (*Evaluator, error) {
	if t == nil {
		return nil, fmt.Errorf("%w: nil tracker", ErrInvalidRule)
	}
	for _, r := range rules {
		if r.Long <= 0 || r.Short < 0 || r.Short > r.Long || !(r.Threshold > 0) || math.IsInf(r.Threshold, 0) {
			return nil, fmt.Errorf("%w: %+v", ErrInvalidRule, r)
		}
	}
	return &Evaluator{t: t, rules: append([]Rule(nil), rules...)}, nil
}

// Evaluate returns the alerts firing at now, in rule order. A window with no
// traffic cannot exceed a threshold, so it never fires a rule.
func (e *Evaluator) Evaluate(now time.Time) []Alert {
	var out []Alert
	for _, r := range e.rules {
		long, ok := e.t.BurnRate(now, r.Long)
		if !ok || long < r.Threshold {
			continue
		}
		var short float64
		if r.Short > 0 {
			short, ok = e.t.BurnRate(now, r.Short)
			if !ok || short < r.Threshold {
				continue
			}
		}
		out = append(out, Alert{Rule: r, LongBurn: long, ShortBurn: short})
	}
	return out
}
