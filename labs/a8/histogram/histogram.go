// Package histogram is the lab for A8 Unit 1 §3–§4: latency distributions
// you can aggregate.
//
// It has three parts:
//
//  1. Sketch: a concurrency-safe, mergeable, log-linear ("exponential bucket")
//     histogram with a guaranteed relative error, in the style of DDSketch.
//  2. FixedBuckets / PromQuantile: the classic Prometheus cumulative-bucket
//     histogram and its histogram_quantile interpolation, so you can measure
//     how wrong a fixed-bucket estimate can be.
//  3. NaiveAverageQuantile / WeightedAverageQuantile / MergedQuantile: three
//     ways to answer "what is the fleet-wide p99?" from per-instance data,
//     two of them wrong.
//
// Suggested order: NewSketch, Record/RecordN, Count/Sum/Mean/Min/Max,
// Quantile, then Merge, then CountAtMost, then the Prometheus part, then the
// fleet helpers. Run: go test -race ./labs/a8/histogram/
//
// # The sketch, precisely
//
// Let relErr = α and γ = (1+α)/(1−α). A positive value v lands in bucket
// i = ceil(log_γ(v)), which covers (γ^(i−1), γ^i]. Every value in that bucket
// is estimated as 2γ^i/(γ+1), which is within a factor α of every value the
// bucket can hold. Exact zeros have their own counter. Buckets live in a map,
// so memory depends on the dynamic range of the data (log_γ(max/min)
// buckets at most), not on the number of samples.
//
// Quantile(q) estimates the element with 0-based sorted rank
// floor(q·(count−1)): the estimate x̂ satisfies |x̂ − x| ≤ α·x for that
// element x. The smallest and largest elements are tracked exactly, so
// Quantile(0) == Min() and Quantile(1) == Max(), and every estimate is
// clamped to [Min, Max].
//
// # Concurrency
//
// A Sketch is used by many request goroutines at once. Guard it with one
// mutex. Merge must never hold two sketches' locks at the same time (take a
// snapshot of the argument first), otherwise a.Merge(b) racing with
// b.Merge(a) deadlocks, and a.Merge(a) must not deadlock either.
//
// # Break-it
//
// breakit_on.go / breakit_off.go define breakNoLock (true only with
// -tags breakit). Wire it into your locking as the unit text describes
// (for example `if !breakNoLock { s.mu.Lock() }`), then run
//
//	go test -tags breakit -race -run Concurrent -v ./labs/a8/histogram/
package histogram

import (
	"errors"
	"sync"
)

var errNotImplemented = errors.New("histogram: not implemented")

// Errors returned by this package.
var (
	// ErrInvalidRelErr: the relative error must be in (0, 1).
	ErrInvalidRelErr = errors.New("histogram: relative error must be in (0, 1)")
	// ErrIncompatible: two sketches can only merge if they have the same
	// relative error (hence the same bucket boundaries).
	ErrIncompatible = errors.New("histogram: sketches have different relative error")
	// ErrNoData: there is nothing to compute a quantile from.
	ErrNoData = errors.New("histogram: no data")
)

// Sketch is a mergeable log-linear histogram. Safe for concurrent use.
type Sketch struct {
	// TODO(lab): suggested fields. You may restructure freely, the tests only
	// use the exported API.
	mu       sync.Mutex
	relErr   float64
	gamma    float64        // (1+α)/(1−α)
	logGamma float64        // math.Log(gamma), precomputed
	counts   map[int]uint64 // bucket index -> count of positive values
	zero     uint64         // exact zeros
	count    uint64
	sum      float64
	min, max float64
}

// NewSketch returns an empty sketch whose quantile estimates are within a
// factor relErr of the true value. relErr must be in (0, 1) (NaN is invalid);
// otherwise (nil, ErrInvalidRelErr).
func NewSketch(relErr float64) (*Sketch, error) {
	// TODO(lab)
	return nil, errNotImplemented
}

// RelativeError returns the α the sketch was created with.
func (s *Sketch) RelativeError() float64 {
	// TODO(lab)
	return 0
}

// Record adds one observation. Negative, NaN and infinite values are
// rejected (Record returns false and the sketch is unchanged).
func (s *Sketch) Record(v float64) bool {
	// TODO(lab): this is RecordN(v, 1).
	return false
}

// RecordN adds n observations of v in O(1), independent of n. Invalid
// values are rejected as in Record; n == 0 on a valid value is an accepted
// no-op (it must not change Min or Max either).
func (s *Sketch) RecordN(v float64, n uint64) bool {
	// TODO(lab): index = ceil(log(v)/logGamma) for v > 0; zeros are counted
	// separately; keep exact min, max, count and sum.
	return false
}

// Count returns the number of recorded observations.
func (s *Sketch) Count() uint64 {
	// TODO(lab)
	return 0
}

// Sum returns the sum of recorded observations.
func (s *Sketch) Sum() float64 {
	// TODO(lab)
	return 0
}

// Mean returns Sum/Count, or 0 for an empty sketch.
func (s *Sketch) Mean() float64 {
	// TODO(lab)
	return 0
}

// Min returns the smallest recorded value (exact), or 0 if empty.
func (s *Sketch) Min() float64 {
	// TODO(lab)
	return 0
}

// Max returns the largest recorded value (exact), or 0 if empty.
func (s *Sketch) Max() float64 {
	// TODO(lab)
	return 0
}

// BucketCount returns the number of non-empty buckets, counting the zero
// bucket if it is non-empty. It measures memory use.
func (s *Sketch) BucketCount() int {
	// TODO(lab)
	return 0
}

// Quantile returns the estimated q-quantile (q in [0, 1]) and true, or
// (0, false) if the sketch is empty or q is outside [0, 1] or NaN. See the
// package doc for the exact rank and error guarantee.
func (s *Sketch) Quantile(q float64) (float64, bool) {
	// TODO(lab): rank = floor(q*(count-1)); walk buckets in index order
	// (zeros first) until the cumulative count exceeds rank; return the
	// bucket's representative 2γ^i/(γ+1), clamped to [min, max].
	return 0, false
}

// Merge adds all of o's observations to s. o is not modified. Merging a nil
// or empty sketch is a no-op. Sketches with different relative errors return
// ErrIncompatible and s is left unchanged. s.Merge(s) doubles s. Safe against
// concurrent a.Merge(b) / b.Merge(a): it never holds both locks.
func (s *Sketch) Merge(o *Sketch) error {
	// TODO(lab): snapshot o under o's lock, release it, then lock s and add
	// the counts. Mind Min/Max when s is empty.
	return errNotImplemented
}

// CountAtMost brackets how many recorded values are <= threshold:
// lo <= (true count) <= hi. This is how an SLI like "fraction of requests
// under 300 ms" is computed from a sketch. Buckets entirely at or below the
// threshold count toward lo; the single bucket that straddles it is the
// uncertainty (hi - lo). Thresholds below Min give (0, 0); at or above Max
// give (Count, Count); a NaN or negative threshold gives (0, 0).
func (s *Sketch) CountAtMost(threshold float64) (lo, hi uint64) {
	// TODO(lab): lo = zeros + buckets with index < index(threshold);
	// hi = lo + the count in bucket index(threshold). Threshold 0 is exact.
	return 0, 0
}

// Bucket is one cumulative bucket of a Prometheus-style classic histogram:
// the number of observations <= UpperBound ("le" semantics).
type Bucket struct {
	UpperBound      float64
	CumulativeCount uint64
}

// FixedBuckets builds the cumulative buckets a Prometheus histogram with the
// given upper bounds would expose for samples. Bounds are sorted and
// de-duplicated; NaN and +Inf bounds are ignored (a +Inf bucket is always
// appended last and holds the total). A sample equal to a bound belongs in
// that bound's bucket (le = "less than or equal"). NaN samples are ignored.
func FixedBuckets(bounds, samples []float64) []Bucket {
	// TODO(lab): sort.SearchFloat64s returns the first bound >= v.
	return nil
}

// PromQuantile estimates the q-quantile from cumulative buckets the way
// Prometheus's histogram_quantile does for classic histograms: find the
// first bucket whose cumulative count >= q*total, then interpolate linearly
// inside it, assuming observations are spread evenly across the bucket
// (the first bucket's lower edge is 0).
//
// Edge cases, matching Prometheus: q < 0 gives -Inf and q > 1 gives +Inf
// (NaN q gives NaN); buckets are sorted by bound first (on a copy: do not
// reorder the caller's slice); if the last bucket's bound is not +Inf, there
// are fewer than two buckets or the total is 0, the result is NaN; if the
// quantile falls in the +Inf bucket the largest finite bound is returned
// (the histogram cannot say anything beyond it); if it falls in the first
// bucket and that bucket's bound is <= 0, that bound is returned.
func PromQuantile(q float64, buckets []Bucket) float64 {
	// TODO(lab): rank = q*total; b = first bucket with count >= rank;
	// result = start + (end-start) * (rank - prevCount) / (count - prevCount).
	return 0
}

// NaiveAverageQuantile is what a dashboard does when it runs avg() over
// per-instance p99 series: the unweighted mean of each instance's
// q-quantile. It is WRONG, and the lab tests show by how much. Nil and empty
// sketches are skipped; no data at all gives ErrNoData.
func NaiveAverageQuantile(sketches []*Sketch, q float64) (float64, error) {
	// TODO(lab)
	return 0, errNotImplemented
}

// WeightedAverageQuantile averages the per-instance q-quantiles weighted by
// each instance's request count. Better than the naive mean, still WRONG:
// quantiles do not compose linearly. No data gives ErrNoData.
func WeightedAverageQuantile(sketches []*Sketch, q float64) (float64, error) {
	// TODO(lab)
	return 0, errNotImplemented
}

// MergedQuantile is the right way: merge the sketches into a fresh one (the
// inputs are not modified) and ask it for the quantile. ErrIncompatible if
// the sketches' relative errors differ, ErrNoData if all are empty or the
// slice is empty.
func MergedQuantile(sketches []*Sketch, q float64) (float64, error) {
	// TODO(lab)
	return 0, errNotImplemented
}
