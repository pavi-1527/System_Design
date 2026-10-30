package loadgen

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func ms(n ...int) []time.Duration {
	out := make([]time.Duration, len(n))
	for i, v := range n {
		out[i] = time.Duration(v) * time.Millisecond
	}
	return out
}

func TestM5_ScheduleIsEvenlySpacedAndOpenLoop(t *testing.T) {
	got := Schedule(4, time.Second)
	want := ms(0, 250, 500, 750)
	if len(got) != len(want) {
		t.Fatalf("Schedule(4/s, 1s) = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("Schedule(4/s, 1s) = %v, want %v", got, want)
		}
	}
	if n := len(Schedule(1000, 3*time.Second)); n != 3000 {
		t.Errorf("Schedule(1000/s, 3s) has %d entries, want 3000", n)
	}
	if Schedule(0, time.Second) != nil || Schedule(10, 0) != nil {
		t.Error("Schedule with rate 0 or duration 0 should be empty")
	}
}

func TestM5_PercentileIsNearestRank(t *testing.T) {
	s := ms(10, 20, 30, 40, 50, 60, 70, 80, 90, 100)
	cases := map[float64]time.Duration{
		50: 50 * time.Millisecond, 90: 90 * time.Millisecond, 95: 100 * time.Millisecond,
		99: 100 * time.Millisecond, 100: 100 * time.Millisecond, 0.1: 10 * time.Millisecond,
		10: 10 * time.Millisecond, 11: 20 * time.Millisecond,
	}
	for p, want := range cases {
		if got := Percentile(s, p); got != want {
			t.Errorf("p%g of 10..100ms = %v, want %v (nearest rank: no interpolation, no averaging)", p, got, want)
		}
	}
	if Percentile(nil, 99) != 0 {
		t.Error("percentile of no samples should be 0")
	}
	one := ms(7)
	if Percentile(one, 1) != 7*time.Millisecond || Percentile(one, 100) != 7*time.Millisecond {
		t.Error("every percentile of one sample is that sample")
	}
}

func TestM5_ReportFormat(t *testing.T) {
	r := Result{
		Latency:  ms(10, 20, 30, 40, 50, 60, 70, 80, 90, 100),
		Service:  ms(1, 1, 1, 1, 1, 1, 1, 1, 1, 2),
		Status:   map[int]int{503: 2, 302: 8},
		Errors:   0,
		Elapsed:  2 * time.Second,
		Intended: 5,
	}
	var b bytes.Buffer
	r.Report(&b)
	out := b.String()
	for _, want := range []string{
		"requests 10 in 2s (intended 5/s, achieved 5.0/s)",
		"status   302×8 503×2",
		"errors   0",
		"latency  p50=50ms p95=100ms p99=100ms max=100ms",
		"service  p50=1ms p95=2ms p99=2ms max=2ms",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("report is missing %q\n%s", want, out)
		}
	}
}

func TestM5_OpenLoopCountsTheQueueBehindAStall(t *testing.T) {
	var calls atomic.Int32
	do := func(ctx context.Context) (int, error) {
		if calls.Add(1) == 20 {
			time.Sleep(300 * time.Millisecond) // one stall: a GC pause, a lock, a failover
		} else {
			time.Sleep(time.Millisecond)
		}
		return 200, nil
	}
	res, err := Generate(context.Background(), Config{Rate: 100, Duration: time.Second, MaxInFlight: 1, Do: do})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if len(res.Latency) != 100 || len(res.Service) != 100 || res.Status[200] != 100 {
		t.Fatalf("got %d latency / %d service samples, %v statuses; want 100 of each: an open loop sends every scheduled request",
			len(res.Latency), len(res.Service), res.Status)
	}
	lp99, sp99 := Percentile(res.Latency, 99), Percentile(res.Service, 99)
	t.Logf("p99 from intended start %v, from actual send %v", lp99, sp99)
	if lp99 < 200*time.Millisecond {
		t.Errorf("latency p99 = %v; ≈ 30 requests waited behind a 300 ms stall, so p99 must show it (coordinated omission?)", lp99)
	}
	if sp99 > 150*time.Millisecond {
		t.Errorf("service p99 = %v; only one request was itself slow, so the service view should not show the queue", sp99)
	}
	for i := 1; i < len(res.Latency); i++ {
		if res.Latency[i] < res.Latency[i-1] {
			t.Fatal("Latency must be sorted ascending")
		}
	}
}

func TestM5_SmokeRunAgainstAnHTTPServer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Client-ID") == "" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Location", "https://example.com")
		w.WriteHeader(http.StatusFound)
	}))
	defer srv.Close()
	var out, errb bytes.Buffer
	code := Run(context.Background(), []string{"-url", srv.URL + "/abc1234", "-rate", "200", "-duration", "300ms"}, &out, &errb)
	if code != 0 {
		t.Fatalf("Run exit %d: %s", code, errb.String())
	}
	if !strings.Contains(out.String(), "302×60") || !strings.Contains(out.String(), "p99=") {
		t.Errorf("want 60 redirects (200/s × 0.3 s, not followed) and a p99 line:\n%s", out.String())
	}
}

func TestM5_RefusesNonLoopbackTargets(t *testing.T) {
	var out, errb bytes.Buffer
	if code := Run(context.Background(), []string{"-url", "http://198.51.100.7/", "-duration", "10ms"}, &out, &errb); code != 2 {
		t.Errorf("exit %d for a non-loopback target, want 2", code)
	}
	if !strings.Contains(errb.String(), "loopback") {
		t.Errorf("stderr should explain the refusal: %q", errb.String())
	}
}
