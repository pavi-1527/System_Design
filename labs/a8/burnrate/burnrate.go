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
// Time is always a parameter (RecordAt takes the event time, every query
// takes "now"), so the whole package is deterministic: tests never sleep, and
// a production wrapper simply passes time.Now().
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
// n = ceil(w / Resolution) buckets (at least 1): the bucket containing now
// and the n−1 buckets before it. So with Resolution = 1 minute, a 5-minute
// window at 12:07:30 covers the buckets 12:03, 12:04, 12:05, 12:06 and 12:07
// (the current, partial minute is included). Buckets after "now" are not
// counted until now reaches them.
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
//
// # Memory
//
// Keep at most Period/Resolution buckets: when an event arrives for a newer
// bucket than any seen so far, forget buckets that fell out of the Period.
// Events older than the retention are dropped. Do not scan the whole map on
// every event (a month of minutes is 43,200 buckets).
//
// # Break-it
//
// breakit_test.go (build tag breakit) replays ten simulated days through six
// alerting strategies; see labs/a8/README.md.
package burnrate

import (
	"errors"
	"sync"
	"time"
)

var errNotImplemented = errors.New("burnrate: not implemented")

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
	// older than Period is discarded.
	Period time.Duration
	// Resolution is the bucket width, e.g. time.Minute. Zero means one
	// minute. Must not exceed Period.
	Resolution time.Duration
}

// Tracker is a rolling-window SLO tracker. Safe for concurrent use.
type Tracker struct {
	// TODO(lab): suggested state. You may restructure freely, the tests only
	// use the exported API.
	mu    sync.Mutex
	cfg   Config
	cells map[int64]*cell // bucket index -> counts
	// ... newest bucket index seen, number of buckets in a Period, ...
}

type cell struct{ good, total uint64 }

// NewTracker validates cfg (wrapping ErrInvalidConfig with fmt.Errorf("%w")
// or returning it directly) and returns an empty Tracker.
func NewTracker(cfg Config) (*Tracker, error) {
	// TODO(lab)
	return nil, errNotImplemented
}

// RecordAt adds good and total events that happened at time at. Events may
// arrive out of order. good > total is rejected with ErrInvalidCounts (and
// nothing is recorded). Events older than the retention (more than Period
// before the newest event ever recorded) are silently dropped.
func (t *Tracker) RecordAt(at time.Time, good, total uint64) error {
	// TODO(lab)
	return errNotImplemented
}

// Buckets returns the number of buckets currently stored (memory use). It
// never exceeds Period/Resolution rounded up.
func (t *Tracker) Buckets() int {
	// TODO(lab)
	return 0
}

// SLI returns good/total over the trailing window ending at now, and the
// total event count. ok is false when the window saw no events (an SLI of
// "0 / 0" is not 100%, it is unknown).
func (t *Tracker) SLI(now time.Time, window time.Duration) (sli float64, total uint64, ok bool) {
	// TODO(lab)
	return 0, 0, false
}

// BurnRate returns the error rate over the window divided by the error rate
// the objective allows. ok is false when the window saw no events.
func (t *Tracker) BurnRate(now time.Time, window time.Duration) (float64, bool) {
	// TODO(lab)
	return 0, false
}

// BudgetRemaining returns the fraction of the error budget left over the SLO
// Period ending at now: 1 − bad / ((1 − Objective) × total). 1 means
// untouched (or no traffic), 0 means exactly spent, negative means the SLO is
// violated for the period.
func (t *Tracker) BudgetRemaining(now time.Time) float64 {
	// TODO(lab)
	return 0
}

// TimeToExhaustion estimates how long until the error budget is gone if the
// burn rate seen over window continues: remaining × Period / burn. If the
// budget is already spent it returns (0, true). If the window has no
// traffic or no errors (burn rate 0) the budget never runs out at this
// rate: (0, false).
func (t *Tracker) TimeToExhaustion(now time.Time, window time.Duration) (time.Duration, bool) {
	// TODO(lab)
	return 0, false
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
// threshold = budgetFraction × period / longWindow. Names must be non-empty
// and distinct.
//
//	page   1 h  / 5 min   2% of the budget  -> 14.4 × for a 30-day period
//	page   6 h  / 30 min  5% of the budget  ->  6 ×
//	ticket 3 d  / 6 h     10% of the budget ->  1 ×
func DefaultRules(period time.Duration) []Rule {
	// TODO(lab)
	return nil
}

// Evaluator evaluates rules against a Tracker. It is stateless: whether a
// rule fires is a pure function of the data and "now", which is what makes
// the short window reset it quickly.
type Evaluator struct {
	// TODO(lab): the tracker and a copy of the rules.
}

// NewEvaluator validates the rules (Long > 0, Threshold > 0 and finite,
// 0 <= Short <= Long; a nil tracker is also invalid) and returns an
// Evaluator. Invalid input gives an error wrapping ErrInvalidRule. The rules
// are copied.
func NewEvaluator(t *Tracker, rules []Rule) (*Evaluator, error) {
	// TODO(lab)
	return nil, errNotImplemented
}

// Evaluate returns the alerts firing at now, in rule order. A window with no
// traffic cannot exceed a threshold, so it never fires a rule.
func (e *Evaluator) Evaluate(now time.Time) []Alert {
	// TODO(lab)
	return nil
}
