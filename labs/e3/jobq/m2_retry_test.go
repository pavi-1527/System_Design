package jobq

// Milestone 2: retries with backoff and jitter. The broker's nack delay, the
// worker's backoff schedule, Retry-After, timeouts, and a worker that keeps
// going through a broker outage.

import (
	"fmt"
	"net/http"
	"testing"
	"time"
)

func TestM2_ANackDelaysRedelivery(t *testing.T) {
	t.Parallel()
	b := tStartBroker(t, t.TempDir())
	tMustEnqueue(t, b.URL, 1)
	_, j := tLease(t, b.URL, "default", 0, 0)
	nackAt := time.Now()
	if code := tNack(t, b.URL, j, NackRequest{Lease: j.Lease, Error: "vendor 503", RetryAfterMS: 600}); code != http.StatusNoContent {
		t.Fatalf("nack: status %d; want 204", code)
	}
	if code, _ := tLease(t, b.URL, "default", 0, 0); code != http.StatusNoContent {
		t.Fatalf("lease right after a 600ms nack: %d; want 204: the job must wait out its delay", code)
	}
	if s := tQueueStats(t, b.URL, "default"); s.Delayed != 1 || s.Depth != 1 {
		t.Errorf("stats during the delay: %+v; want delayed 1, depth 1 (a delayed job still counts against depth)", s)
	}
	code, again := tLease(t, b.URL, "default", 3*time.Second, 0)
	if code != http.StatusOK || again.Attempt != 2 {
		t.Fatalf("lease after the delay: status %d, attempt %d; want 200 on attempt 2", code, again.Attempt)
	}
	if el := time.Since(nackAt); el < 550*time.Millisecond {
		t.Errorf("redelivered %v after the nack; want ≥ the 600ms it asked for", el)
	}
	if code := tNack(t, b.URL, j, NackRequest{Lease: j.Lease, Error: "late"}); code != http.StatusConflict {
		t.Errorf("nack with attempt 1's lease during attempt 2: %d; want 409", code)
	}
}

// tGaps returns the gaps between consecutive arrival times (Unix ms).
func tGaps(ts []int64) []time.Duration {
	var g []time.Duration
	for i := 1; i < len(ts); i++ {
		g = append(g, time.Duration(ts[i]-ts[i-1])*time.Millisecond)
	}
	return g
}

func TestM2_AVendorBurstIsRetriedWithGrowingBackoff(t *testing.T) {
	t.Parallel()
	b := tStartBroker(t, t.TempDir())
	p := tStartProvider(t)
	tControl(t, p.URL, ProviderControl{FailNext: 3, FailStatus: http.StatusServiceUnavailable})
	tStartWorker(t, "worker", b.URL, p.URL, "-jitter", "none", "-backoff-base", "150ms", "-backoff-max", "2s")
	ids := tMustEnqueue(t, b.URL, 1)
	s := tWaitEffects(t, p.URL, 1, 15*time.Second)
	tExactlyOnce(t, s, ids)
	at := s.AttemptsByKey[ids[0]]
	if len(at) != 4 {
		t.Fatalf("vendor saw %d attempts; want 4 (3 injected 503s, then success)", len(at))
	}
	gaps := tGaps(at)
	for i, min := range []time.Duration{150 * time.Millisecond, 300 * time.Millisecond, 600 * time.Millisecond} {
		if gaps[i] < min-30*time.Millisecond {
			t.Errorf("gap before retry %d was %v; want ≥ %v (base 150ms doubling, no jitter): retrying a struggling "+
				"vendor immediately is how a blip becomes an outage", i+1, gaps[i], min)
		}
	}
	tEventually(t, 5*time.Second, func() (bool, string) {
		q := tQueueStats(t, b.URL, "default")
		return q.Acked == 1 && q.Nacked == 3, fmt.Sprintf("broker stats %+v; want nacked 3, acked 1", q)
	})
}

func TestM2_RetryAfterFromTheVendorIsAFloor(t *testing.T) {
	t.Parallel()
	b := tStartBroker(t, t.TempDir())
	p := tStartProvider(t)
	tControl(t, p.URL, ProviderControl{FailNext: 1, FailStatus: http.StatusTooManyRequests, RetryAfter: 1})
	tStartWorker(t, "worker", b.URL, p.URL, "-backoff-base", "10ms", "-backoff-max", "50ms")
	ids := tMustEnqueue(t, b.URL, 1)
	s := tWaitEffects(t, p.URL, 1, 15*time.Second)
	at := s.AttemptsByKey[ids[0]]
	if len(at) != 2 {
		t.Fatalf("vendor saw %d attempts; want 2 (one 429, one success)", len(at))
	}
	if gap := tGaps(at)[0]; gap < 950*time.Millisecond {
		t.Errorf("retried %v after a 429 with Retry-After: 1; want ≥ 1s even though the backoff cap is 50ms", gap)
	}
}

func TestM2_ATimedOutCallIsRetried(t *testing.T) {
	t.Parallel()
	b := tStartBroker(t, t.TempDir())
	p := tStartProvider(t)
	tControl(t, p.URL, ProviderControl{SlowNext: 1, SlowMS: 1500})
	tStartWorker(t, "worker", b.URL, p.URL, "-call-timeout", "200ms")
	ids := tMustEnqueue(t, b.URL, 1)
	s := tWaitEffects(t, p.URL, 1, 15*time.Second)
	at := s.AttemptsByKey[ids[0]]
	if len(at) < 2 {
		t.Fatalf("vendor saw %d attempt(s); want ≥ 2 (the timed-out one and the retry)", len(at))
	}
	if gap := tGaps(at)[0]; gap > 1200*time.Millisecond {
		t.Errorf("the retry arrived %v after the first attempt; want it soon after the worker's 200ms call timeout, "+
			"not after the vendor's 1.5s stall", gap)
	}
}

func TestM2_TheWorkerRidesOutABrokerPartition(t *testing.T) {
	t.Parallel()
	b := tStartBroker(t, t.TempDir(), "-fsync=false")
	px := tNewProxy(t, b.Addr)
	p := tStartProvider(t)
	w := tStartWorker(t, "worker", px.URL, p.URL)
	ids := tMustEnqueue(t, b.URL, 1) // warm up the path
	tWaitEffects(t, p.URL, 1, 10*time.Second)

	px.Partition()
	ids = append(ids, tMustEnqueue(t, b.URL, 5)...)
	tEventually(t, 10*time.Second, func() (bool, string) {
		s, err := tWorkerStats(t, w.URL)
		return err == nil && s.LeaseError > 0, fmt.Sprintf("worker stats %+v (%v); want lease_error > 0 once its long-poll times out", s, err)
	})
	if s := tProviderStats(t, p.URL); s.Effects != 1 {
		t.Fatalf("vendor got %d effects during the partition; want 1 (the worker cannot reach the broker)", s.Effects)
	}
	if !w.Running() {
		t.Fatal("the worker exited during a broker partition; it must back off and keep trying")
	}
	px.Heal()
	tExactlyOnce(t, tWaitEffects(t, p.URL, 6, 15*time.Second), ids)
}
