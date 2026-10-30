package histogram

import (
	"errors"
	"fmt"
	"math"
	"math/rand/v2"
	"sort"
	"sync"
	"testing"
	"time"
)

// ---------- helpers (prefixed with t to avoid clashing with your code) ----------

// tWithin runs fn and fails the test if it does not finish in d. Tests that
// could deadlock use it so a wrong implementation fails instead of hanging.
func tWithin(t *testing.T, d time.Duration, what string, fn func()) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		fn()
	}()
	select {
	case <-done:
	case <-time.After(d):
		t.Fatalf("%s did not finish within %v (deadlock or livelock?)", what, d)
	}
}

func tSketch(t *testing.T, relErr float64) *Sketch {
	t.Helper()
	s, err := NewSketch(relErr)
	if err != nil || s == nil {
		t.Fatalf("NewSketch(%v) = (%v, %v); expected a usable sketch", relErr, s, err)
	}
	return s
}

func tRand(seed uint64) *rand.Rand { return rand.New(rand.NewPCG(seed, seed*7919+1)) }

// tExact is the reference quantile the sketch documents: the element with
// 0-based sorted rank floor(q*(n-1)).
func tExact(sorted []float64, q float64) float64 {
	return sorted[int(math.Floor(q*float64(len(sorted)-1)))]
}

func tSorted(vs []float64) []float64 {
	c := append([]float64(nil), vs...)
	sort.Float64s(c)
	return c
}

var tQuantiles = []float64{0, 0.001, 0.01, 0.1, 0.25, 0.5, 0.75, 0.9, 0.95, 0.99, 0.999, 0.9999, 1}

// tDistributions are latency-shaped samples in milliseconds.
var tDistributions = map[string]func(r *rand.Rand, n int) []float64{
	"uniform": func(r *rand.Rand, n int) []float64 {
		out := make([]float64, n)
		for i := range out {
			out[i] = 1 + r.Float64()*999
		}
		return out
	},
	"lognormal-heavy-tail": func(r *rand.Rand, n int) []float64 {
		out := make([]float64, n)
		for i := range out {
			out[i] = math.Exp(3 + 1.2*r.NormFloat64())
		}
		return out
	},
	"bimodal-fast-and-slow": func(r *rand.Rand, n int) []float64 {
		out := make([]float64, n)
		for i := range out {
			if r.Float64() < 0.97 {
				out[i] = 5 + r.Float64()*10 // 97% fast: 5-15 ms
			} else {
				out[i] = 800 + r.Float64()*700 // 3% slow: 0.8-1.5 s
			}
		}
		return out
	},
}

func tRecordAll(t *testing.T, s *Sketch, vs []float64) {
	t.Helper()
	for _, v := range vs {
		if !s.Record(v) {
			t.Fatalf("Record(%v) = false; a valid non-negative finite value must be accepted", v)
		}
	}
}

// ---------- the sketch: construction and basic statistics ----------

func TestNewSketch_RejectsInvalidRelativeError(t *testing.T) {
	for _, bad := range []float64{0, -0.01, 1, 1.5, math.NaN(), math.Inf(1)} {
		s, err := NewSketch(bad)
		if !errors.Is(err, ErrInvalidRelErr) || s != nil {
			t.Errorf("NewSketch(%v) = (%v, %v); expected (nil, ErrInvalidRelErr) because α must be in (0, 1)", bad, s, err)
		}
	}
	s := tSketch(t, 0.01)
	if s.RelativeError() != 0.01 {
		t.Errorf("RelativeError() = %v, want 0.01", s.RelativeError())
	}
}

func TestSketch_EmptyReportsNoData(t *testing.T) {
	s := tSketch(t, 0.01)
	if v, ok := s.Quantile(0.5); ok || v != 0 {
		t.Errorf("empty Quantile(0.5) = (%v, %v); expected (0, false): there is no median of nothing", v, ok)
	}
	if s.Count() != 0 || s.Sum() != 0 || s.Min() != 0 || s.Max() != 0 || s.Mean() != 0 || s.BucketCount() != 0 {
		t.Errorf("empty sketch stats not all zero: count=%d sum=%v min=%v max=%v mean=%v buckets=%d",
			s.Count(), s.Sum(), s.Min(), s.Max(), s.Mean(), s.BucketCount())
	}
}

func TestSketch_CountSumMinMaxMean(t *testing.T) {
	// Orders chosen so that "initialise min/max to 0" and "min/max of the
	// first value only" bugs both show.
	for name, vs := range map[string][]float64{
		"ascending":  {3, 5, 8, 13, 21},
		"descending": {21, 13, 8, 5, 3},
		"shuffled":   {8, 21, 3, 13, 5},
	} {
		s := tSketch(t, 0.02)
		tRecordAll(t, s, vs)
		if s.Count() != 5 || s.Sum() != 50 || s.Mean() != 10 {
			t.Errorf("%s: count=%d sum=%v mean=%v, want 5, 50, 10", name, s.Count(), s.Sum(), s.Mean())
		}
		if s.Min() != 3 || s.Max() != 21 {
			t.Errorf("%s: min=%v max=%v, want 3 and 21 (exact, not bucket estimates; all values are > 0 so a zero-initialised min is wrong)", name, s.Min(), s.Max())
		}
	}
}

func TestSketch_RejectsInvalidValues(t *testing.T) {
	s := tSketch(t, 0.01)
	for _, bad := range []float64{math.NaN(), math.Inf(1), math.Inf(-1), -1, -0.000001} {
		if s.Record(bad) {
			t.Errorf("Record(%v) = true; NaN, ±Inf and negative values must be rejected", bad)
		}
		if s.RecordN(bad, 5) {
			t.Errorf("RecordN(%v, 5) = true; must be rejected", bad)
		}
	}
	if s.Count() != 0 || s.Sum() != 0 {
		t.Errorf("rejected values changed the sketch: count=%d sum=%v (one NaN would poison Sum forever)", s.Count(), s.Sum())
	}
	if !s.Record(0) || !s.Record(12.5) {
		t.Error("Record(0) and Record(12.5) must be accepted")
	}
	if s.Count() != 2 {
		t.Errorf("Count() = %d, want 2", s.Count())
	}
}

func TestSketch_RecordNEqualsRepeatedRecord(t *testing.T) {
	a, b := tSketch(t, 0.01), tSketch(t, 0.01)
	for i := 0; i < 1000; i++ {
		a.Record(42.5)
	}
	a.Record(7)
	b.RecordN(42.5, 1000)
	b.RecordN(7, 1)
	b.RecordN(99, 0) // accepted no-op: must not even move Max
	for _, q := range tQuantiles {
		x, _ := a.Quantile(q)
		y, _ := b.Quantile(q)
		if x != y {
			t.Errorf("q=%v: RecordN gives %v, repeated Record gives %v", q, y, x)
		}
	}
	if a.Count() != b.Count() || a.Sum() != b.Sum() || a.Max() != b.Max() || a.Min() != b.Min() {
		t.Errorf("stats differ: Record count=%d sum=%v min=%v max=%v; RecordN count=%d sum=%v min=%v max=%v",
			a.Count(), a.Sum(), a.Min(), a.Max(), b.Count(), b.Sum(), b.Min(), b.Max())
	}
	tWithin(t, 2*time.Second, "RecordN with a huge n", func() {
		c := tSketch(t, 0.01)
		c.RecordN(1, 1<<40) // must be O(1), not a loop
		if c.Count() != 1<<40 {
			t.Errorf("Count() = %d, want %d", c.Count(), uint64(1)<<40)
		}
	})
}

// ---------- the sketch: quantile accuracy ----------

func TestSketch_QuantileWithinRelativeError(t *testing.T) {
	for _, relErr := range []float64{0.05, 0.01, 0.001} {
		for name, gen := range tDistributions {
			t.Run(fmt.Sprintf("%s/alpha=%v", name, relErr), func(t *testing.T) {
				vs := gen(tRand(11), 50_000)
				s := tSketch(t, relErr)
				tRecordAll(t, s, vs)
				sorted := tSorted(vs)
				for _, q := range tQuantiles {
					got, ok := s.Quantile(q)
					if !ok {
						t.Fatalf("Quantile(%v) not ok on a non-empty sketch", q)
					}
					want := tExact(sorted, q)
					if math.Abs(got-want) > relErr*want*(1+1e-9) {
						t.Errorf("q=%v: got %.6g, exact %.6g, relative error %.4f%% > guarantee %.4f%%",
							q, got, want, 100*math.Abs(got-want)/want, 100*relErr)
					}
				}
			})
		}
	}
}

func TestSketch_ExtremeQuantilesAreExactMinAndMax(t *testing.T) {
	s := tSketch(t, 0.05) // coarse buckets: only clamping to [Min, Max] makes the ends exact
	tRecordAll(t, s, []float64{101.3, 250.7, 977.1, 333.3, 12.9})
	if v, _ := s.Quantile(0); v != 12.9 {
		t.Errorf("Quantile(0) = %v, want exactly Min() = 12.9", v)
	}
	if v, _ := s.Quantile(1); v != 977.1 {
		t.Errorf("Quantile(1) = %v, want exactly Max() = 977.1", v)
	}
}

func TestSketch_ConstantStreamStaysWithinBoundAndUsesOneBucket(t *testing.T) {
	s := tSketch(t, 0.01)
	s.RecordN(0.31, 100_000)
	for _, q := range tQuantiles {
		v, _ := s.Quantile(q)
		if math.Abs(v-0.31) > 0.01*0.31*(1+1e-9) {
			t.Errorf("q=%v: got %v for a constant 0.31 stream; error exceeds 1%%", q, v)
		}
	}
	if s.BucketCount() != 1 {
		t.Errorf("BucketCount() = %d for one distinct value, want 1", s.BucketCount())
	}
}

func TestSketch_ZeroValuesAreCountedExactly(t *testing.T) {
	s := tSketch(t, 0.01)
	s.RecordN(0, 50)
	s.RecordN(200, 50)
	if v, _ := s.Quantile(0.25); v != 0 {
		t.Errorf("Quantile(0.25) = %v with half the data at exactly 0, want 0", v)
	}
	if v, _ := s.Quantile(0.75); math.Abs(v-200) > 2.0001 {
		t.Errorf("Quantile(0.75) = %v, want ≈ 200 within 1%%", v)
	}
	if s.Min() != 0 || s.Max() != 200 || s.BucketCount() != 2 {
		t.Errorf("min=%v max=%v buckets=%d, want 0, 200, 2", s.Min(), s.Max(), s.BucketCount())
	}
}

func TestSketch_WideDynamicRangeDoesNotBreak(t *testing.T) {
	s := tSketch(t, 0.01)
	vs := []float64{1e-300, 1e-9, 1e-3, 1, 1e3, 1e9, 1e300}
	tRecordAll(t, s, vs)
	sorted := tSorted(vs)
	for _, q := range tQuantiles {
		got, _ := s.Quantile(q)
		want := tExact(sorted, q)
		if math.IsNaN(got) || math.IsInf(got, 0) || math.Abs(got-want) > 0.01*want*(1+1e-9) {
			t.Errorf("q=%v: got %v, exact %v (values from 1e-300 to 1e300 must stay finite and within 1%%)", q, got, want)
		}
	}
}

func TestSketch_QuantileIsMonotonic(t *testing.T) {
	s := tSketch(t, 0.01)
	tRecordAll(t, s, tDistributions["lognormal-heavy-tail"](tRand(5), 20_000))
	prev := math.Inf(-1)
	for i := 0; i <= 1000; i++ {
		q := float64(i) / 1000
		v, ok := s.Quantile(q)
		if !ok || v < prev {
			t.Fatalf("Quantile(%v) = (%v, %v) after %v: quantiles must be non-decreasing in q", q, v, ok, prev)
		}
		prev = v
	}
}

func TestSketch_QuantileRejectsOutOfRangeQ(t *testing.T) {
	s := tSketch(t, 0.01)
	tRecordAll(t, s, []float64{1, 2, 3})
	for _, q := range []float64{-0.1, 1.0001, 2, math.NaN(), math.Inf(1)} {
		if v, ok := s.Quantile(q); ok || v != 0 {
			t.Errorf("Quantile(%v) = (%v, %v); expected (0, false) for q outside [0, 1]", q, v, ok)
		}
	}
}

func TestSketch_MemoryIsBoundedByDynamicRangeNotSampleCount(t *testing.T) {
	// log_γ(10^9) for α = 1% is ≈ 1036 buckets, however many samples arrive.
	s := tSketch(t, 0.01)
	r := tRand(3)
	for i := 0; i < 300_000; i++ {
		s.Record(math.Pow(10, -6+9*r.Float64())) // 1 µs .. 1000 s, uniform in log space
	}
	if n := s.BucketCount(); n > 1100 {
		t.Errorf("BucketCount() = %d after 300k samples over 9 decades; want ≤ ≈1036 (+ rounding). Memory must not grow with samples", n)
	}
	before := s.BucketCount()
	for i := 0; i < 300_000; i++ {
		s.Record(math.Pow(10, -6+9*r.Float64()))
	}
	if after := s.BucketCount(); after > before+30 {
		t.Errorf("BucketCount grew from %d to %d after doubling the samples over the same range", before, after)
	}
}

// ---------- the sketch: merging ----------

func TestMerge_EqualsRecordingEverythingInOneSketch(t *testing.T) {
	r := tRand(21)
	vs := tDistributions["lognormal-heavy-tail"](r, 30_000)
	// Seven instances with very unequal traffic.
	weights := []int{1, 2, 4, 8, 16, 32, 937}
	parts := make([]*Sketch, len(weights))
	for i := range parts {
		parts[i] = tSketch(t, 0.01)
	}
	whole := tSketch(t, 0.01)
	for _, v := range vs {
		whole.Record(v)
		x := r.IntN(1000)
		i := 0
		for acc := weights[0]; x >= acc; acc += weights[i] {
			i++
		}
		parts[i].Record(v)
	}
	merged := tSketch(t, 0.01)
	for _, p := range parts {
		if err := merged.Merge(p); err != nil {
			t.Fatalf("Merge: %v", err)
		}
	}
	if merged.Count() != whole.Count() || merged.Min() != whole.Min() || merged.Max() != whole.Max() ||
		merged.BucketCount() != whole.BucketCount() {
		t.Errorf("merged count/min/max/buckets = %d/%v/%v/%d, one-sketch = %d/%v/%v/%d",
			merged.Count(), merged.Min(), merged.Max(), merged.BucketCount(),
			whole.Count(), whole.Min(), whole.Max(), whole.BucketCount())
	}
	if d := math.Abs(merged.Sum()-whole.Sum()) / whole.Sum(); d > 1e-9 {
		t.Errorf("merged Sum differs from one-sketch Sum by relative %g", d)
	}
	for i := 0; i <= 200; i++ {
		q := float64(i) / 200
		a, _ := merged.Quantile(q)
		b, _ := whole.Quantile(q)
		if a != b {
			t.Fatalf("q=%v: merged quantile %v != single-sketch quantile %v; merging must lose nothing "+
				"(bucket counts add; this is why sketches, unlike quantiles, are aggregatable)", q, a, b)
		}
	}
}

func TestMerge_IntoEmptyAdoptsMinAndMax(t *testing.T) {
	dst, src := tSketch(t, 0.01), tSketch(t, 0.01)
	tRecordAll(t, src, []float64{40, 50, 60})
	if err := dst.Merge(src); err != nil {
		t.Fatal(err)
	}
	if dst.Min() != 40 || dst.Max() != 60 || dst.Count() != 3 {
		t.Errorf("after merging into an empty sketch: min=%v max=%v count=%d, want 40, 60, 3 (an empty sketch's zero Min must not win)",
			dst.Min(), dst.Max(), dst.Count())
	}
	// Merging an empty sketch must not disturb Min either.
	if err := dst.Merge(tSketch(t, 0.01)); err != nil {
		t.Fatal(err)
	}
	if dst.Min() != 40 {
		t.Errorf("merging an empty sketch changed Min to %v", dst.Min())
	}
	// The merged sketch must not share storage with its source.
	dst.Record(1000)
	dst.Record(41)
	if src.Count() != 3 || src.Max() != 60 || src.BucketCount() != 3 {
		t.Errorf("recording into the merged sketch changed the source: count=%d max=%v buckets=%d (a Merge must copy counts, not alias them)",
			src.Count(), src.Max(), src.BucketCount())
	}
}

func TestMerge_DoesNotModifyArgumentAndHandlesNil(t *testing.T) {
	a, b := tSketch(t, 0.01), tSketch(t, 0.01)
	tRecordAll(t, a, []float64{1, 2, 3})
	tRecordAll(t, b, []float64{10, 20})
	if err := a.Merge(b); err != nil {
		t.Fatal(err)
	}
	if b.Count() != 2 || b.Max() != 20 || b.Min() != 10 {
		t.Errorf("Merge modified its argument: count=%d min=%v max=%v", b.Count(), b.Min(), b.Max())
	}
	if err := a.Merge(nil); err != nil || a.Count() != 5 {
		t.Errorf("Merge(nil) = %v, count=%d; expected a no-op", err, a.Count())
	}
}

func TestMerge_RejectsDifferentRelativeError(t *testing.T) {
	a, b := tSketch(t, 0.01), tSketch(t, 0.02)
	tRecordAll(t, a, []float64{1, 2, 3})
	tRecordAll(t, b, []float64{100, 200})
	if err := a.Merge(b); !errors.Is(err, ErrIncompatible) {
		t.Fatalf("Merge of α=0.02 into α=0.01 = %v, want ErrIncompatible (bucket boundaries differ; adding counts would be meaningless)", err)
	}
	if a.Count() != 3 || a.Max() != 3 {
		t.Errorf("failed Merge changed the receiver: count=%d max=%v", a.Count(), a.Max())
	}
}

func TestMerge_SelfMergeDoublesWithoutDeadlock(t *testing.T) {
	s := tSketch(t, 0.01)
	tRecordAll(t, s, []float64{5, 10, 15})
	tWithin(t, 2*time.Second, "s.Merge(s)", func() {
		if err := s.Merge(s); err != nil {
			t.Errorf("self-merge: %v", err)
		}
	})
	if s.Count() != 6 || s.Sum() != 60 {
		t.Errorf("after self-merge count=%d sum=%v, want 6 and 60", s.Count(), s.Sum())
	}
}

func TestMerge_ConcurrentOppositeOrderDoesNotDeadlock(t *testing.T) {
	a, b := tSketch(t, 0.01), tSketch(t, 0.01)
	tRecordAll(t, a, []float64{1, 2, 3})
	tRecordAll(t, b, []float64{4, 5, 6})
	tWithin(t, 10*time.Second, "opposite-order merges", func() {
		var wg sync.WaitGroup
		for i := 0; i < 2; i++ {
			from, to := a, b
			if i == 1 {
				from, to = b, a
			}
			wg.Add(1)
			go func() {
				defer wg.Done()
				for j := 0; j < 25; j++ { // each merge roughly doubles the target: keep uint64 away from overflow
					_ = to.Merge(from)
					if j%20 == 0 {
						_ = to.Merge(tSketch(t, 0.01)) // an empty sketch is a no-op
					}
				}
			}()
		}
		wg.Wait()
	})
	// Counts roughly double each round, so we only check they did not shrink
	// and the lock discipline held under -race.
	if a.Count() < 3 || b.Count() < 3 {
		t.Errorf("counts shrank: a=%d b=%d", a.Count(), b.Count())
	}
}

// ---------- the sketch: concurrency ----------

func TestSketch_ConcurrentRecordIsExact(t *testing.T) {
	const writers, perWriter = 8, 20_000
	s := tSketch(t, 0.01)
	tWithin(t, 20*time.Second, "concurrent Record", func() {
		var wg sync.WaitGroup
		stop := make(chan struct{})
		var readers sync.WaitGroup
		for i := 0; i < 2; i++ {
			readers.Add(1)
			go func() { // readers run concurrently with the writers
				defer readers.Done()
				last := uint64(0)
				for {
					select {
					case <-stop:
						return
					default:
					}
					c := s.Count()
					if c < last {
						t.Errorf("Count went backwards: %d after %d", c, last)
						return
					}
					last = c
					s.Quantile(0.99)
					s.Sum()
					s.Min()
					s.Max()
				}
			}()
		}
		for w := 0; w < writers; w++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for i := 1; i <= perWriter; i++ {
					s.Record(float64(i)) // integers: Sum is exact in float64
				}
			}()
		}
		wg.Wait()
		close(stop)
		readers.Wait()
	})
	if s.Count() != writers*perWriter {
		t.Errorf("Count() = %d, want %d: updates were lost without a lock", s.Count(), writers*perWriter)
	}
	wantSum := float64(writers) * perWriter * (perWriter + 1) / 2
	if s.Sum() != wantSum {
		t.Errorf("Sum() = %v, want %v", s.Sum(), wantSum)
	}
	if s.Min() != 1 || s.Max() != perWriter {
		t.Errorf("min=%v max=%v, want 1 and %d", s.Min(), s.Max(), perWriter)
	}
}

// ---------- the sketch: SLI helper ----------

func TestCountAtMost_BracketsTheTruthAndPinsTheStraddlingBucket(t *testing.T) {
	r := tRand(8)
	vs := tDistributions["lognormal-heavy-tail"](r, 40_000)
	s := tSketch(t, 0.01)
	tRecordAll(t, s, vs)
	sorted := tSorted(vs)
	exactAtMost := func(th float64) uint64 {
		return uint64(sort.Search(len(sorted), func(i int) bool { return sorted[i] > th }))
	}

	thresholds := []float64{sorted[0], 5, 10, 19.99, 20, 20.0001, 50, 100, 300, 1000, 1e5, sorted[len(sorted)-1]}
	for _, k := range []int{-3, 0, 1, 50, 120, 200} { // thresholds exactly on bucket boundaries γ^k
		thresholds = append(thresholds, math.Pow((1+0.01)/(1-0.01), float64(k)))
	}
	for _, th := range thresholds {
		lo, hi := s.CountAtMost(th)
		exact := exactAtMost(th)
		if lo > exact || exact > hi {
			t.Errorf("threshold %v: exact count %d is outside [%d, %d]", th, exact, lo, hi)
		}
		// The uncertainty is one bucket: for smooth data at α = 1% that is a
		// small share of the points (the busiest bucket here holds well under 1%).
		if float64(hi-lo) > 0.01*float64(len(vs)) {
			t.Errorf("threshold %v: bracket [%d, %d] is wider than one bucket should be (1%% of the data)", th, lo, hi)
		}
	}
}

func TestCountAtMost_EdgeCases(t *testing.T) {
	s := tSketch(t, 0.01)
	if lo, hi := s.CountAtMost(100); lo != 0 || hi != 0 {
		t.Errorf("empty sketch: (%d, %d), want (0, 0)", lo, hi)
	}
	s.RecordN(0, 3)
	s.RecordN(10, 4)
	s.RecordN(500, 5)
	exact := []struct {
		th     float64
		lo, hi uint64
	}{
		{-1, 0, 0},
		{math.NaN(), 0, 0},
		{0, 3, 3},      // the exact zeros are all <= 0, and nothing positive is
		{0.0001, 3, 3}, // positive, but far below the smallest positive value
		{math.SmallestNonzeroFloat64, 3, 3},
		{250, 7, 7},           // between the 10s and the 500s: no bucket straddles it
		{500, 12, 12},         // at Max: everything
		{1e9, 12, 12},         // above Max
		{math.Inf(1), 12, 12}, // above Max
	}
	for _, c := range exact {
		if lo, hi := s.CountAtMost(c.th); lo != c.lo || hi != c.hi {
			t.Errorf("CountAtMost(%v) = (%d, %d), want (%d, %d)", c.th, lo, hi, c.lo, c.hi)
		}
	}
	// Thresholds inside a populated bucket (at α = 1%, 9.99 and 10 share a
	// bucket, as do 499.99 and 500): only the bracket property is guaranteed.
	inside := []struct {
		th    float64
		truth uint64
	}{{9.99, 3}, {10, 7}, {499.99, 7}}
	for _, c := range inside {
		lo, hi := s.CountAtMost(c.th)
		if lo > c.truth || c.truth > hi {
			t.Errorf("CountAtMost(%v) = (%d, %d); must bracket the true count %d", c.th, lo, hi, c.truth)
		}
	}
}

// ---------- fixed buckets and the Prometheus estimator ----------

func TestFixedBuckets_LeSemanticsSortingAndInf(t *testing.T) {
	bs := FixedBuckets([]float64{0.5, 0.1, 0.25, 0.1, math.NaN(), math.Inf(1)}, []float64{0.05, 0.1, 0.1, 0.2, 0.25, 0.3, 0.5, 0.9, 7, math.NaN()})
	want := []Bucket{{0.1, 3}, {0.25, 5}, {0.5, 7}, {math.Inf(1), 9}}
	if len(bs) != len(want) {
		t.Fatalf("got %d buckets %v, want %d %v (sorted, de-duplicated, NaN/+Inf bounds dropped, one +Inf bucket)", len(bs), bs, len(want), want)
	}
	for i := range want {
		if bs[i] != want[i] {
			t.Errorf("bucket %d = %+v, want %+v (a sample equal to a bound belongs to that bound: le means ≤)", i, bs[i], want[i])
		}
	}
	if only := FixedBuckets(nil, []float64{1, 2, 3}); len(only) != 1 || !math.IsInf(only[0].UpperBound, 1) || only[0].CumulativeCount != 3 {
		t.Errorf("no bounds: got %v, want a single +Inf bucket holding 3", only)
	}
}

func TestPromQuantile_InterpolatesLinearlyInsideTheBucket(t *testing.T) {
	// le=0.1:50  le=0.25:90  le=0.5:100  le=+Inf:100
	bs := []Bucket{{0.1, 50}, {0.25, 90}, {0.5, 100}, {math.Inf(1), 100}}
	cases := []struct{ q, want float64 }{
		{0.50, 0.1},   // rank 50: first bucket, 50/50 of the way: its upper bound
		{0.25, 0.05},  // rank 25: halfway through the first bucket, which starts at 0
		{0.70, 0.175}, // rank 70: 20 of 40 into (0.1, 0.25]
		{0.90, 0.25},  // rank 90: end of the second bucket
		{0.99, 0.475}, // rank 99: 9 of 10 into (0.25, 0.5]
		{1.00, 0.5},   // rank 100: end of the third bucket
		{0.00, 0},     // rank 0: first bucket, 0 of 50 into it
	}
	for _, c := range cases {
		if got := PromQuantile(c.q, bs); math.Abs(got-c.want) > 1e-12 {
			t.Errorf("PromQuantile(%v) = %v, want %v", c.q, got, c.want)
		}
	}
}

func TestPromQuantile_EdgeCasesMatchPrometheus(t *testing.T) {
	inf := math.Inf(1)
	ok := []Bucket{{1, 10}, {2, 20}, {inf, 30}}
	if v := PromQuantile(0.99, ok); v != 2 {
		t.Errorf("quantile inside the +Inf bucket = %v, want the largest finite bound 2 (the histogram cannot see past it)", v)
	}
	if v := PromQuantile(-0.1, ok); !math.IsInf(v, -1) {
		t.Errorf("q < 0 = %v, want -Inf", v)
	}
	if v := PromQuantile(1.1, ok); !math.IsInf(v, 1) {
		t.Errorf("q > 1 = %v, want +Inf", v)
	}
	if v := PromQuantile(math.NaN(), ok); !math.IsNaN(v) {
		t.Errorf("q = NaN gave %v, want NaN", v)
	}
	if v := PromQuantile(0.5, []Bucket{{1, 0}, {2, 0}, {inf, 0}}); !math.IsNaN(v) {
		t.Errorf("no observations gave %v, want NaN", v)
	}
	if v := PromQuantile(0.5, []Bucket{{1, 10}, {2, 20}}); !math.IsNaN(v) {
		t.Errorf("no +Inf bucket gave %v, want NaN", v)
	}
	if v := PromQuantile(0.5, []Bucket{{inf, 10}}); !math.IsNaN(v) {
		t.Errorf("a single bucket gave %v, want NaN", v)
	}
	if v := PromQuantile(0.5, []Bucket{{inf, 30}, {2, 20}, {1, 10}}); v != 1.5 {
		t.Errorf("unsorted input gave %v, want 1.5 (buckets must be sorted by bound first)", v)
	}
	if v := PromQuantile(0.5, []Bucket{{-5, 10}, {5, 20}, {inf, 20}}); v != -5 {
		t.Errorf("quantile in a first bucket with a bound <= 0 gave %v, want that bound -5", v)
	}
	in := []Bucket{{inf, 30}, {2, 20}, {1, 10}}
	PromQuantile(0.5, in)
	if in[0].UpperBound != inf || in[2].UpperBound != 1 {
		t.Error("PromQuantile sorted its argument in place; callers' slices must not be reordered")
	}
}

func TestPromQuantile_ConstantLatencyShowsInterpolationError(t *testing.T) {
	// Every request takes exactly 310 ms. Default-ish Prometheus buckets
	// around an SLO of 300 ms: 100 ms, 250 ms, 500 ms, 1 s.
	samples := make([]float64, 10_000)
	for i := range samples {
		samples[i] = 0.310
	}
	prom := PromQuantile(0.99, FixedBuckets([]float64{0.1, 0.25, 0.5, 1}, samples))
	if math.Abs(prom-0.4975) > 1e-9 {
		t.Errorf("PromQuantile p99 = %v, want 0.4975 (0.25 + 0.25 × 0.99: it assumes samples spread evenly over (0.25, 0.5])", prom)
	}
	s := tSketch(t, 0.01)
	tRecordAll(t, s, samples)
	sk, _ := s.Quantile(0.99)
	if math.Abs(sk-0.310) > 0.01*0.310*(1+1e-9) {
		t.Errorf("sketch p99 = %v, want 0.310 ±1%%", sk)
	}
	if errProm := math.Abs(prom-0.310) / 0.310; errProm < 0.5 {
		t.Errorf("fixed-bucket error is %.0f%%; this scenario should show ≥ 50%% (the bucket is 250 ms wide)", 100*errProm)
	}
}

func TestPromQuantile_AlwaysLandsInsideTheBucketHoldingTheTrueQuantile(t *testing.T) {
	// The estimate can be wrong by up to a bucket width, but never outside the bucket.
	bounds := []float64{5, 10, 25, 50, 100, 250, 500, 1000, 2500}
	vs := tDistributions["lognormal-heavy-tail"](tRand(77), 20_000)
	sorted := tSorted(vs)
	bs := FixedBuckets(bounds, vs)
	for _, q := range []float64{0.5, 0.9, 0.95, 0.99, 0.999} {
		truth := tExact(sorted, q)
		est := PromQuantile(q, bs)
		lo, hi := 0.0, math.Inf(1)
		for _, b := range bounds {
			if truth <= b {
				hi = b
				break
			}
			lo = b
		}
		if est < lo-1e-9 || est > hi+1e-9 {
			t.Errorf("q=%v: estimate %v is outside the bucket (%v, %v] that holds the true quantile %v", q, est, lo, hi, truth)
		}
	}
}

// ---------- the fleet: why averaging quantiles is wrong ----------

// tFleet builds instances from (count, latency) groups of uniform noise ±5%.
func tFleet(t *testing.T, groups ...[2]float64) ([]*Sketch, []float64) {
	t.Helper()
	r := tRand(99)
	var all []float64
	var out []*Sketch
	for _, g := range groups {
		s := tSketch(t, 0.01)
		for i := 0; i < int(g[0]); i++ {
			v := g[1] * (0.95 + 0.1*r.Float64())
			s.Record(v)
			all = append(all, v)
		}
		out = append(out, s)
	}
	return out, all
}

func TestFleetQuantile_AveragingP99sOverestimatesWhenTheSlowInstanceIsTiny(t *testing.T) {
	// 990 requests at ≈10 ms on one instance, 10 requests at ≈1000 ms on another.
	// 1% of the traffic is slow, so the fleet p99 sits at the top of the fast
	// group: ≈ 10 ms. Averaging the two p99s says ≈ 505 ms.
	inst, all := tFleet(t, [2]float64{990, 10}, [2]float64{10, 1000})
	truth := tExact(tSorted(all), 0.99)

	merged, err := MergedQuantile(inst, 0.99)
	if err != nil || math.Abs(merged-truth) > 0.01*truth*(1+1e-9) {
		t.Errorf("MergedQuantile = (%v, %v), exact fleet p99 = %v; the merged sketch must be within 1%%", merged, err, truth)
	}
	naive, err := NaiveAverageQuantile(inst, 0.99)
	if err != nil || naive < 20*truth {
		t.Errorf("NaiveAverageQuantile = (%v, %v); expected ≈ (10.5+1050)/2 ≈ 530, i.e. > 20× the true p99 %v", naive, err, truth)
	}
	weighted, err := WeightedAverageQuantile(inst, 0.99)
	if err != nil || weighted < 1.5*truth {
		t.Errorf("WeightedAverageQuantile = (%v, %v); expected ≈ 0.99×10 + 0.01×1000 ≈ 20, still ≥ 1.5× the true p99 %v", weighted, err, truth)
	}
}

func TestFleetQuantile_AveragingP99sHidesAnOutageWhenTheSlowInstanceIsBigEnough(t *testing.T) {
	// 980 fast + 20 slow: 2% of requests take ≈ 1 s, so the fleet p99 IS
	// slow. Weighted averaging reports ≈ 30 ms and the dashboard stays green.
	inst, all := tFleet(t, [2]float64{980, 10}, [2]float64{20, 1000})
	truth := tExact(tSorted(all), 0.99)
	if truth < 900 {
		t.Fatalf("scenario broken: exact fleet p99 = %v, expected ≈ 1000", truth)
	}
	merged, err := MergedQuantile(inst, 0.99)
	if err != nil || math.Abs(merged-truth) > 0.01*truth*(1+1e-9) {
		t.Errorf("MergedQuantile = (%v, %v), exact = %v", merged, err, truth)
	}
	weighted, err := WeightedAverageQuantile(inst, 0.99)
	if err != nil || weighted > truth/10 {
		t.Errorf("WeightedAverageQuantile = (%v, %v); expected ≈ 30, more than 10× below the true p99 %v: that is the outage the dashboard hides", weighted, err, truth)
	}
	naive, err := NaiveAverageQuantile(inst, 0.99)
	if err != nil || naive > 0.7*truth {
		t.Errorf("NaiveAverageQuantile = (%v, %v); expected ≈ 530, well below the true p99 %v", naive, err, truth)
	}
}

func TestFleetQuantile_EdgeCases(t *testing.T) {
	for name, fn := range map[string]func([]*Sketch, float64) (float64, error){
		"Naive": NaiveAverageQuantile, "Weighted": WeightedAverageQuantile, "Merged": MergedQuantile,
	} {
		if _, err := fn(nil, 0.99); !errors.Is(err, ErrNoData) {
			t.Errorf("%s(nil) error = %v, want ErrNoData", name, err)
		}
		if _, err := fn([]*Sketch{tSketch(t, 0.01), nil}, 0.99); !errors.Is(err, ErrNoData) {
			t.Errorf("%s(empty, nil) error = %v, want ErrNoData", name, err)
		}
	}
	a, b := tSketch(t, 0.01), tSketch(t, 0.05)
	a.Record(1)
	b.Record(2)
	if _, err := MergedQuantile([]*Sketch{a, b}, 0.5); !errors.Is(err, ErrIncompatible) {
		t.Errorf("MergedQuantile of mismatched sketches error = %v, want ErrIncompatible", err)
	}
	// Inputs are never modified.
	c, d := tSketch(t, 0.01), tSketch(t, 0.01)
	tRecordAll(t, c, []float64{1, 2, 3})
	tRecordAll(t, d, []float64{4, 5})
	if _, err := MergedQuantile([]*Sketch{c, d}, 0.5); err != nil {
		t.Fatal(err)
	}
	if c.Count() != 3 || d.Count() != 2 {
		t.Errorf("MergedQuantile modified its inputs: counts %d and %d, want 3 and 2", c.Count(), d.Count())
	}
	// An idle instance (no data) is skipped, not counted as a zero.
	idle := tSketch(t, 0.01)
	if v, err := NaiveAverageQuantile([]*Sketch{c, idle}, 1); err != nil || v != 3 {
		t.Errorf("Naive with an idle instance = (%v, %v), want (3, nil): idle instances are skipped", v, err)
	}
}
