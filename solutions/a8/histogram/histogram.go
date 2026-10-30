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
// Suggested order: NewSketch, Record, Count/Sum/Min/Max, Quantile, then
// Merge, then CountAtMost, then the Prometheus part, then the fleet helpers.
// Run: go test -race ./labs/a8/histogram/
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
package histogram

import (
	"errors"
	"math"
	"sort"
	"sync"
)

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
	mu       sync.Mutex
	relErr   float64
	gamma    float64
	logGamma float64
	counts   map[int]uint64 // bucket index -> count of positive values
	zero     uint64         // exact zeros
	count    uint64
	sum      float64
	min, max float64
}

// NewSketch returns an empty sketch whose quantile estimates are within a
// factor relErr of the true value. relErr must be in (0, 1); otherwise
// ErrInvalidRelErr.
func NewSketch(relErr float64) (*Sketch, error) {
	if !(relErr > 0 && relErr < 1) { // also rejects NaN
		return nil, ErrInvalidRelErr
	}
	gamma := (1 + relErr) / (1 - relErr)
	return &Sketch{
		relErr:   relErr,
		gamma:    gamma,
		logGamma: math.Log(gamma),
		counts:   map[int]uint64{},
	}, nil
}

func (s *Sketch) lock() {
	if !breakNoLock {
		s.mu.Lock()
	}
}

func (s *Sketch) unlock() {
	if !breakNoLock {
		s.mu.Unlock()
	}
}

// index returns the bucket index of a positive, finite v.
func (s *Sketch) index(v float64) int {
	return int(math.Ceil(math.Log(v) / s.logGamma))
}

// value returns the representative value of bucket i.
func (s *Sketch) value(i int) float64 {
	return 2 * math.Exp(float64(i)*s.logGamma) / (s.gamma + 1)
}

// RelativeError returns the α the sketch was created with.
func (s *Sketch) RelativeError() float64 { return s.relErr }

// Record adds one observation. Negative, NaN and infinite values are
// rejected (Record returns false and the sketch is unchanged).
func (s *Sketch) Record(v float64) bool { return s.RecordN(v, 1) }

// RecordN adds n observations of v in O(1), independent of n. Invalid
// values are rejected as in Record; n == 0 on a valid value is an accepted
// no-op.
func (s *Sketch) RecordN(v float64, n uint64) bool {
	if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 {
		return false
	}
	if n == 0 {
		return true
	}
	s.lock()
	defer s.unlock()
	if v == 0 {
		s.zero += n
	} else {
		s.counts[s.index(v)] += n
	}
	if s.count == 0 {
		s.min, s.max = v, v
	} else {
		s.min = math.Min(s.min, v)
		s.max = math.Max(s.max, v)
	}
	s.count += n
	s.sum += v * float64(n)
	return true
}

// Count returns the number of recorded observations.
func (s *Sketch) Count() uint64 {
	s.lock()
	defer s.unlock()
	return s.count
}

// Sum returns the sum of recorded observations.
func (s *Sketch) Sum() float64 {
	s.lock()
	defer s.unlock()
	return s.sum
}

// Mean returns Sum/Count, or 0 for an empty sketch.
func (s *Sketch) Mean() float64 {
	s.lock()
	defer s.unlock()
	if s.count == 0 {
		return 0
	}
	return s.sum / float64(s.count)
}

// Min returns the smallest recorded value (exact), or 0 if empty.
func (s *Sketch) Min() float64 {
	s.lock()
	defer s.unlock()
	return s.min
}

// Max returns the largest recorded value (exact), or 0 if empty.
func (s *Sketch) Max() float64 {
	s.lock()
	defer s.unlock()
	return s.max
}

// BucketCount returns the number of non-empty buckets, counting the zero
// bucket if it is non-empty. It measures memory use.
func (s *Sketch) BucketCount() int {
	s.lock()
	defer s.unlock()
	n := len(s.counts)
	if s.zero > 0 {
		n++
	}
	return n
}

// Quantile returns the estimated q-quantile (q in [0, 1]) and true, or
// (0, false) if the sketch is empty or q is outside [0, 1] or NaN. See the
// package doc for the exact rank and error guarantee.
func (s *Sketch) Quantile(q float64) (float64, bool) {
	if !(q >= 0 && q <= 1) {
		return 0, false
	}
	s.lock()
	defer s.unlock()
	if s.count == 0 {
		return 0, false
	}
	rank := uint64(math.Floor(q * float64(s.count-1)))
	switch {
	case rank == 0:
		return s.min, true // the smallest element is tracked exactly
	case rank == s.count-1:
		return s.max, true // so is the largest
	case rank < s.zero:
		return 0, true
	}
	keys := make([]int, 0, len(s.counts))
	for k := range s.counts {
		keys = append(keys, k)
	}
	sort.Ints(keys)
	cum := s.zero
	est := s.max
	for _, k := range keys {
		cum += s.counts[k]
		if cum > rank {
			est = s.value(k)
			break
		}
	}
	return math.Min(math.Max(est, s.min), s.max), true
}

type snapshot struct {
	relErr        float64
	counts        map[int]uint64
	zero, count   uint64
	sum, min, max float64
}

func (s *Sketch) snapshot() snapshot {
	s.lock()
	defer s.unlock()
	c := make(map[int]uint64, len(s.counts))
	for k, v := range s.counts {
		c[k] = v
	}
	return snapshot{s.relErr, c, s.zero, s.count, s.sum, s.min, s.max}
}

// Merge adds all of o's observations to s. o is not modified. Merging a nil
// or empty sketch is a no-op. Sketches with different relative errors return
// ErrIncompatible and s is left unchanged. s.Merge(s) doubles s. Safe against
// concurrent a.Merge(b) / b.Merge(a): it never holds both locks.
func (s *Sketch) Merge(o *Sketch) error {
	if o == nil {
		return nil
	}
	snap := o.snapshot()
	if snap.relErr != s.relErr {
		return ErrIncompatible
	}
	if snap.count == 0 {
		return nil
	}
	s.lock()
	defer s.unlock()
	for k, v := range snap.counts {
		s.counts[k] += v
	}
	s.zero += snap.zero
	if s.count == 0 {
		s.min, s.max = snap.min, snap.max
	} else {
		s.min = math.Min(s.min, snap.min)
		s.max = math.Max(s.max, snap.max)
	}
	s.count += snap.count
	s.sum += snap.sum
	return nil
}

// CountAtMost brackets how many recorded values are <= threshold:
// lo <= (true count) <= hi. This is how an SLI like "fraction of requests
// under 300 ms" is computed from a sketch. Buckets entirely at or below the
// threshold count toward lo; the single bucket that straddles it is the
// uncertainty (hi - lo). Thresholds below Min give (0, 0); at or above Max
// give (Count, Count); a NaN or negative threshold gives (0, 0).
func (s *Sketch) CountAtMost(threshold float64) (lo, hi uint64) {
	if math.IsNaN(threshold) || threshold < 0 {
		return 0, 0
	}
	s.lock()
	defer s.unlock()
	if s.count == 0 || threshold < s.min {
		return 0, 0
	}
	if threshold >= s.max {
		return s.count, s.count
	}
	lo = s.zero
	if threshold == 0 {
		return lo, lo
	}
	ti := s.index(threshold)
	for k, c := range s.counts {
		if k < ti {
			lo += c
		}
	}
	return lo, lo + s.counts[ti]
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
	bs := make([]float64, 0, len(bounds))
	for _, b := range bounds {
		if !math.IsNaN(b) && !math.IsInf(b, 0) {
			bs = append(bs, b)
		}
	}
	sort.Float64s(bs)
	uniq := bs[:0]
	for i, b := range bs {
		if i == 0 || b != bs[i-1] {
			uniq = append(uniq, b)
		}
	}
	bs = uniq
	per := make([]uint64, len(bs)+1)
	for _, v := range samples {
		if math.IsNaN(v) {
			continue
		}
		per[sort.SearchFloat64s(bs, v)]++ // first bound >= v
	}
	out := make([]Bucket, 0, len(bs)+1)
	var cum uint64
	for i, b := range bs {
		cum += per[i]
		out = append(out, Bucket{b, cum})
	}
	cum += per[len(bs)]
	return append(out, Bucket{math.Inf(1), cum})
}

// PromQuantile estimates the q-quantile from cumulative buckets the way
// Prometheus's histogram_quantile does for classic histograms: find the
// first bucket whose cumulative count >= q*total, then interpolate linearly
// inside it, assuming observations are spread evenly across the bucket
// (the first bucket's lower edge is 0).
//
// Edge cases, matching Prometheus: q < 0 gives -Inf and q > 1 gives +Inf;
// buckets are sorted by bound first; if the last bucket's bound is not +Inf,
// there are fewer than two buckets or the total is 0, the result is NaN; if
// the quantile falls in the +Inf bucket the largest finite bound is returned
// (the histogram cannot say anything beyond it); if it falls in the first
// bucket and that bucket's bound is <= 0, that bound is returned.
func PromQuantile(q float64, buckets []Bucket) float64 {
	if math.IsNaN(q) {
		return math.NaN()
	}
	if q < 0 {
		return math.Inf(-1)
	}
	if q > 1 {
		return math.Inf(1)
	}
	bs := append([]Bucket(nil), buckets...)
	sort.Slice(bs, func(i, j int) bool { return bs[i].UpperBound < bs[j].UpperBound })
	if len(bs) < 2 || !math.IsInf(bs[len(bs)-1].UpperBound, 1) {
		return math.NaN()
	}
	total := float64(bs[len(bs)-1].CumulativeCount)
	if total == 0 {
		return math.NaN()
	}
	rank := q * total
	b := sort.Search(len(bs)-1, func(i int) bool { return float64(bs[i].CumulativeCount) >= rank })
	if b == len(bs)-1 {
		return bs[len(bs)-2].UpperBound
	}
	if b == 0 && bs[0].UpperBound <= 0 {
		return bs[0].UpperBound
	}
	start, end := 0.0, bs[b].UpperBound
	count := float64(bs[b].CumulativeCount)
	if b > 0 {
		start = bs[b-1].UpperBound
		count -= float64(bs[b-1].CumulativeCount)
		rank -= float64(bs[b-1].CumulativeCount)
	}
	return start + (end-start)*(rank/count)
}

// NaiveAverageQuantile is what a dashboard does when it runs avg() over
// per-instance p99 series: the unweighted mean of each instance's
// q-quantile. It is WRONG, and the lab tests show by how much. Empty
// sketches are skipped; no data at all gives ErrNoData.
func NaiveAverageQuantile(sketches []*Sketch, q float64) (float64, error) {
	var sum float64
	var n int
	for _, s := range sketches {
		if s == nil {
			continue
		}
		v, ok := s.Quantile(q)
		if !ok {
			continue
		}
		sum += v
		n++
	}
	if n == 0 {
		return 0, ErrNoData
	}
	return sum / float64(n), nil
}

// WeightedAverageQuantile averages the per-instance q-quantiles weighted by
// each instance's request count. Better than the naive mean, still WRONG:
// quantiles do not compose linearly. No data gives ErrNoData.
func WeightedAverageQuantile(sketches []*Sketch, q float64) (float64, error) {
	var sum, weight float64
	for _, s := range sketches {
		if s == nil {
			continue
		}
		v, ok := s.Quantile(q)
		if !ok {
			continue
		}
		w := float64(s.Count())
		sum += v * w
		weight += w
	}
	if weight == 0 {
		return 0, ErrNoData
	}
	return sum / weight, nil
}

// MergedQuantile is the right way: merge the sketches into a fresh one (the
// inputs are not modified) and ask it for the quantile. ErrIncompatible if
// the sketches' relative errors differ, ErrNoData if all are empty or the
// slice is empty.
func MergedQuantile(sketches []*Sketch, q float64) (float64, error) {
	var merged *Sketch
	for _, s := range sketches {
		if s == nil {
			continue
		}
		if merged == nil {
			m, err := NewSketch(s.RelativeError())
			if err != nil {
				return 0, err
			}
			merged = m
		}
		if err := merged.Merge(s); err != nil {
			return 0, err
		}
	}
	if merged == nil {
		return 0, ErrNoData
	}
	v, ok := merged.Quantile(q)
	if !ok {
		return 0, ErrNoData
	}
	return v, nil
}
