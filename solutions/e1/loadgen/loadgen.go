// Package loadgen is E1's STRETCH milestone 5: an open-loop load generator
// that records latency the way a user experiences it, and the p50/p95/p99
// report you put in your design write-up.
//
// Open loop means requests are sent on a fixed schedule (rate × duration)
// whether or not earlier ones have finished. Each request's latency is
// measured from its *intended* start, so when the system stalls and requests
// queue up behind the stall, the queueing time is counted. A closed-loop tool
// that waits for each response before sending the next silently skips the
// requests it would have sent during the stall: coordinated omission
// (A8 Unit 1 §5). Result keeps both views so you can see the difference.
//
//	go run ./labs/e1/cmd/loadgen -url http://127.0.0.1:8081/abc1234 -rate 500 -duration 10s
//
// Run the tests: go test -race ./labs/e1/loadgen/
package loadgen

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"
)

// Doer sends one request and returns its status code.
type Doer func(ctx context.Context) (status int, err error)

// Config is one run.
type Config struct {
	Rate        float64       // intended requests per second
	Duration    time.Duration // how long to keep sending
	MaxInFlight int           // concurrency cap; a request that waits for a slot still counts from its intended start
	Timeout     time.Duration // per-request timeout (0: none beyond ctx)
	Do          Doer
}

// Result is what a run measured. Both latency slices are sorted ascending.
type Result struct {
	Latency  []time.Duration // done − intended start: what the user saw
	Service  []time.Duration // done − actual send: what the server took once it had the request
	Status   map[int]int     // responses by status code
	Errors   int             // transport errors and timeouts
	Elapsed  time.Duration   // first intended start to last completion
	Intended float64         // the configured rate
}

// Schedule returns the intended start offsets for rate requests per second
// over d: 0, 1/rate, 2/rate, … strictly before d. rate ≤ 0 or d ≤ 0 → nil.
func Schedule(rate float64, d time.Duration) []time.Duration {
	if rate <= 0 || d <= 0 {
		return nil
	}
	step := float64(time.Second) / rate
	var out []time.Duration
	for i := 0; ; i++ {
		off := time.Duration(math.Round(float64(i) * step))
		if off >= d {
			return out
		}
		out = append(out, off)
	}
}

// Percentile returns the nearest-rank p-th percentile (0 < p ≤ 100) of an
// ascending slice: the smallest value with at least p% of the samples at or
// below it. No interpolation: every answer is a latency that really happened.
// Empty input → 0.
func Percentile(sorted []time.Duration, p float64) time.Duration {
	if len(sorted) == 0 {
		return 0
	}
	rank := int(math.Ceil(p / 100 * float64(len(sorted))))
	rank = min(max(rank, 1), len(sorted))
	return sorted[rank-1]
}

// Generate runs cfg until the schedule is exhausted (or ctx is cancelled)
// and every sent request has finished.
func Generate(ctx context.Context, cfg Config) (Result, error) {
	res := Result{Status: map[int]int{}, Intended: cfg.Rate}
	if cfg.Do == nil {
		return res, errors.New("loadgen: Config.Do is nil")
	}
	if cfg.MaxInFlight < 1 {
		cfg.MaxInFlight = 1
	}
	offsets := Schedule(cfg.Rate, cfg.Duration)
	type sample struct {
		intended, sent, done time.Time
		status               int
		err                  error
	}
	samples := make([]sample, len(offsets))
	slots := make(chan struct{}, cfg.MaxInFlight)
	var wg sync.WaitGroup
	start := time.Now()
	n := 0
loop:
	for i, off := range offsets {
		intended := start.Add(off)
		if d := time.Until(intended); d > 0 {
			t := time.NewTimer(d)
			select {
			case <-t.C:
			case <-ctx.Done():
				t.Stop()
				break loop
			}
		}
		select {
		case slots <- struct{}{}: // may block: that wait is the backlog a stall creates
		case <-ctx.Done():
			break loop
		}
		n++
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-slots }()
			rctx, cancel := ctx, context.CancelFunc(func() {})
			if cfg.Timeout > 0 {
				rctx, cancel = context.WithTimeout(ctx, cfg.Timeout)
			}
			sent := time.Now()
			status, err := cfg.Do(rctx)
			cancel()
			samples[i] = sample{intended: intended, sent: sent, done: time.Now(), status: status, err: err}
		}()
	}
	wg.Wait()
	var last time.Time
	for _, s := range samples[:n] {
		res.Latency = append(res.Latency, s.done.Sub(s.intended))
		res.Service = append(res.Service, s.done.Sub(s.sent))
		if s.err != nil {
			res.Errors++
		} else {
			res.Status[s.status]++
		}
		if s.done.After(last) {
			last = s.done
		}
	}
	if n > 0 {
		res.Elapsed = last.Sub(start)
	}
	sort.Slice(res.Latency, func(i, j int) bool { return res.Latency[i] < res.Latency[j] })
	sort.Slice(res.Service, func(i, j int) bool { return res.Service[i] < res.Service[j] })
	return res, ctx.Err()
}

// round keeps reports readable: 10 µs resolution.
func round(d time.Duration) string { return d.Round(10 * time.Microsecond).String() }

// Report writes the result in the fixed format the design write-up quotes:
//
//	requests 600 in 3.002s (intended 200/s, achieved 199.9/s)
//	status   200×598 503×2
//	errors   0
//	latency  p50=1.21ms p95=3.4ms p99=9.8ms max=12.03ms   (from intended start)
//	service  p50=1.2ms p95=3.1ms p99=4.4ms max=12.01ms    (from actual send)
//
// Status codes are listed in ascending order.
func (r Result) Report(w io.Writer) {
	n := len(r.Latency)
	achieved := 0.0
	if r.Elapsed > 0 {
		achieved = float64(n) / r.Elapsed.Seconds()
	}
	fmt.Fprintf(w, "requests %d in %s (intended %g/s, achieved %.1f/s)\n", n, round(r.Elapsed), r.Intended, achieved)
	codes := make([]int, 0, len(r.Status))
	for c := range r.Status {
		codes = append(codes, c)
	}
	sort.Ints(codes)
	var parts []string
	for _, c := range codes {
		parts = append(parts, fmt.Sprintf("%d×%d", c, r.Status[c]))
	}
	fmt.Fprintf(w, "status   %s\n", strings.Join(parts, " "))
	fmt.Fprintf(w, "errors   %d\n", r.Errors)
	line := func(name string, s []time.Duration, note string) {
		var mx time.Duration
		if len(s) > 0 {
			mx = s[len(s)-1]
		}
		fmt.Fprintf(w, "%-8s p50=%s p95=%s p99=%s max=%s   (%s)\n", name,
			round(Percentile(s, 50)), round(Percentile(s, 95)), round(Percentile(s, 99)), round(mx), note)
	}
	line("latency", r.Latency, "from intended start")
	line("service", r.Service, "from actual send")
}

// HTTPDoer returns a Doer that sends method to target with client and
// discards the body (reading it, so the connection is reused).
func HTTPDoer(client *http.Client, method, target string, header http.Header) Doer {
	return func(ctx context.Context) (int, error) {
		req, err := http.NewRequestWithContext(ctx, method, target, nil)
		if err != nil {
			return 0, err
		}
		for k, v := range header {
			req.Header[k] = v
		}
		resp, err := client.Do(req)
		if err != nil {
			return 0, err
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		return resp.StatusCode, nil
	}
}

// Run is the command-line entry point (cmd/loadgen). It refuses targets that
// are not on loopback: this tool exists to load-test your own processes, and
// pointing it at someone else's server is an attack. Exit 0 on a completed
// run, 2 for bad flags, 1 otherwise. Provided.
func Run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("loadgen", flag.ContinueOnError)
	fs.SetOutput(stderr)
	target := fs.String("url", "", "target URL (loopback only)")
	method := fs.String("method", "GET", "HTTP method")
	rate := fs.Float64("rate", 100, "requests per second")
	dur := fs.Duration("duration", 5*time.Second, "how long to send")
	inflight := fs.Int("max-inflight", 64, "maximum concurrent requests")
	timeout := fs.Duration("timeout", 2*time.Second, "per-request timeout")
	client := fs.String("client-id", "loadgen", "X-Client-ID header")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	u, err := url.Parse(*target)
	if err != nil || u.Host == "" {
		fmt.Fprintf(stderr, "loadgen: -url must be an absolute URL, got %q\n", *target)
		return 2
	}
	host := u.Hostname()
	if ip := net.ParseIP(host); host != "localhost" && (ip == nil || !ip.IsLoopback()) {
		fmt.Fprintf(stderr, "loadgen: refusing non-loopback target %q\n", *target)
		return 2
	}
	hc := &http.Client{
		Transport:     &http.Transport{MaxIdleConnsPerHost: *inflight},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	res, err := Generate(ctx, Config{
		Rate: *rate, Duration: *dur, MaxInFlight: *inflight, Timeout: *timeout,
		Do: HTTPDoer(hc, *method, *target, http.Header{"X-Client-Id": {*client}}),
	})
	if err != nil {
		fmt.Fprintf(stderr, "loadgen: %v\n", err)
		return 1
	}
	res.Report(stdout)
	return 0
}
