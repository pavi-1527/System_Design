//go:build breakit

package jobq

// Break-it experiments for E3. These are measurements, not pass/fail tests:
// they print tables so you can compare your predictions with what the
// processes actually do.
//
//	go test -tags breakit -run BreakIt -v ./labs/e3/jobq/

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"
)

// BreakIt_RetryStorm: 200 jobs hit a vendor that fails its next 400 sends,
// once with synchronised retries (no jitter) and once with full jitter. The
// table is the vendor's arrivals per 100ms bucket: look at the peaks.
func TestBreakIt_RetryStorm(t *testing.T) {
	for _, jitter := range []string{"none", "full"} {
		t.Run(jitter, func(t *testing.T) {
			b := tStartBroker(t, t.TempDir(), "-fsync=false", "-max-attempts", "10")
			p := tStartProvider(t)
			tControl(t, p.URL, ProviderControl{FailNext: 400})
			for i := range 4 {
				tStartWorker(t, fmt.Sprintf("worker-%d", i), b.URL, p.URL, "-concurrency", "16", "-jitter", jitter,
					"-backoff-base", "200ms", "-backoff-max", "3s")
			}
			tMustEnqueue(t, b.URL, 200)
			s := tWaitEffects(t, p.URL, 200, 60*time.Second)
			var all []int64
			for _, ts := range s.AttemptsByKey {
				all = append(all, ts...)
			}
			slices.Sort(all)
			buckets := map[int64]int{}
			var last int64
			for _, ts := range all {
				k := (ts - all[0]) / 100
				buckets[k]++
				last = k
			}
			// The first wave is every job's first attempt; the retry peak is the
			// busiest bucket after it.
			firstWave := (all[min(199, len(all)-1)] - all[0]) / 100
			peak := 0
			var sb strings.Builder
			for k := int64(0); k <= last; k++ {
				if k > firstWave {
					peak = max(peak, buckets[k])
				}
				fmt.Fprintf(&sb, "%5dms %4d %s\n", k*100, buckets[k], strings.Repeat("#", buckets[k]/4))
			}
			t.Logf("jitter=%s: %d sends for 200 jobs, retry peak %d per 100ms, done after %dms\n%s",
				jitter, len(all), peak, (last+1)*100, sb.String())
		})
	}
}

// BreakIt_FsyncCost: 300 sequential enqueues with and without -fsync. The
// difference is the price of "a 201 survives a power cut", per message.
func TestBreakIt_FsyncCost(t *testing.T) {
	for _, fsync := range []string{"true", "false"} {
		b := tStartBroker(t, t.TempDir(), "-fsync="+fsync)
		start := time.Now()
		tMustEnqueue(t, b.URL, 300)
		el := time.Since(start)
		t.Logf("-fsync=%-5s 300 enqueues in %v ≈ %v each ≈ %.0f enqueues/s on one connection",
			fsync, el.Round(time.Millisecond), (el / 300).Round(time.Microsecond), 300/el.Seconds())
	}
}
