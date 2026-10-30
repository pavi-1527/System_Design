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
	"net"
	"net/http"
	"net/url"
	"time"
)

var errNotImplemented = errors.New("loadgen: not implemented")

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
	// TODO(lab): compute offset i as round(i × 1s / rate), not by adding a
	// rounded step repeatedly (the error accumulates).
	return nil
}

// Percentile returns the nearest-rank p-th percentile (0 < p ≤ 100) of an
// ascending slice: the smallest value with at least p% of the samples at or
// below it. No interpolation: every answer is a latency that really happened.
// Empty input → 0.
func Percentile(sorted []time.Duration, p float64) time.Duration {
	// TODO(lab): rank = ceil(p/100 × n), clamped to [1, n].
	return 0
}

// Generate runs cfg until the schedule is exhausted (or ctx is cancelled)
// and every sent request has finished.
//
// The loop that matters: for each scheduled offset, sleep until its intended
// time, take a slot (MaxInFlight; this wait is the backlog a stall creates),
// and send in a goroutine that records intended, sent and done times. Then
// build both sorted slices, count statuses and errors, and set Elapsed.
func Generate(ctx context.Context, cfg Config) (Result, error) {
	// TODO(lab)
	return Result{Status: map[int]int{}, Intended: cfg.Rate}, errNotImplemented
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
	// TODO(lab): use round() for every duration and Percentile for p50/p95/p99.
}

// HTTPDoer returns a Doer that sends method to target with client and
// discards the body (reading it, so the connection is reused).
func HTTPDoer(client *http.Client, method, target string, header http.Header) Doer {
	return func(ctx context.Context) (int, error) {
		// TODO(lab)
		return 0, errNotImplemented
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
