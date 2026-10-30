// Package jobq is the E3 project: an asynchronous job pipeline made of three
// kinds of process, all on 127.0.0.1.
//
//   - broker: a durable job queue. Jobs live in an append-only, checksummed log
//     (frame.go) and are handed to workers on leases with a visibility timeout,
//     acknowledged or negatively acknowledged, retried at a not-before time,
//     dead-lettered after too many attempts or a permanent error, and replayed
//     from the dead-letter queue on request. A depth bound turns overload into
//     429 + Retry-After for producers (broker.go).
//   - worker: a pull loop with bounded concurrency that delivers each job to the
//     notification vendor with an Idempotency-Key derived from the job id,
//     classifies the result, backs off with jitter, and drains on SIGTERM
//     (worker.go, backoff.go).
//   - provider: a fake notification vendor that deduplicates by Idempotency-Key,
//     counts effects per key, and fails, slows down or rejects on command
//     (provider.go; provided, not part of the exercise).
//
// Every role is started through Run, by cmd/jobq and by the tests (which re-run
// the test binary as a child process; see harness_test.go). spec.md in
// modules/E3-job-pipeline is the contract; this file is the provided skeleton.
package jobq

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

var errNotImplemented = errors.New("jobq: not implemented")

// errUsage marks bad flags or a refused address: Run exits 2 for these.
var errUsage = errors.New("usage")

const usage = `usage:
  jobq broker   -addr 127.0.0.1:0 -data DIR [-visibility 30s] [-max-attempts 5] [-max-depth 10000]
                [-dedupe-window 10m] [-fsync=true]
  jobq worker   -addr 127.0.0.1:0 -broker URL -provider URL [-queue default] [-concurrency 4]
                [-call-timeout 5s] [-backoff-base 200ms] [-backoff-max 30s] [-jitter full|equal|none]
                [-lease-wait 2s] [-visibility 0] [-drain-timeout 15s] [-hold-before-ack 0]
  jobq provider -addr 127.0.0.1:0`

// Run starts one role and blocks until ctx is cancelled (graceful shutdown) or
// the role fails. args[0] is the role. It prints "READY <host:port>" on stdout
// once the role's HTTP listener is up, and returns the process exit code:
// 0 after a clean shutdown, 1 on a runtime failure, 2 on bad usage (including a
// non-loopback -addr).
//
// Provided (not part of the exercise).
func Run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, usage)
		return 2
	}
	role, rest := args[0], args[1:]
	var err error
	switch role {
	case "broker":
		var cfg BrokerConfig
		if cfg, err = parseBrokerFlags(rest, stderr); err == nil {
			err = runBroker(ctx, cfg, stdout, stderr)
		}
	case "worker":
		var cfg WorkerConfig
		if cfg, err = parseWorkerFlags(rest, stderr); err == nil {
			err = runWorker(ctx, cfg, stdout, stderr)
		}
	case "provider":
		var addr string
		if addr, err = parseProviderFlags(rest, stderr); err == nil {
			err = runProvider(ctx, addr, stdout, stderr)
		}
	default:
		err = fmt.Errorf("%w: unknown role %q\n%s", errUsage, role, usage)
	}
	switch {
	case err == nil:
		return 0
	case errors.Is(err, errUsage):
		fmt.Fprintf(stderr, "jobq %s: %v\n", role, err)
		return 2
	default:
		fmt.Fprintf(stderr, "jobq %s: %v\n", role, err)
		return 1
	}
}

// BrokerConfig is the broker's command line. Provided.
type BrokerConfig struct {
	Addr         string        // loopback listen address
	Data         string        // directory holding queue.log
	Visibility   time.Duration // default lease length (a worker may ask for another)
	MaxAttempts  int           // default attempts before a job is dead-lettered
	MaxDepth     int           // live jobs (ready + delayed + leased) per queue before 429
	DedupeWindow time.Duration // how long a producer Idempotency-Key is remembered
	Fsync        bool          // fsync every enqueue before acknowledging it
}

// WorkerConfig is the worker's command line. Provided.
type WorkerConfig struct {
	Addr          string        // loopback address of the worker's status endpoint
	Broker        string        // broker base URL
	Provider      string        // provider base URL
	Queue         string        // queue to consume
	Concurrency   int           // jobs in flight at most
	CallTimeout   time.Duration // per provider call, further capped by the lease
	BackoffBase   time.Duration // first retry delay cap
	BackoffMax    time.Duration // largest retry delay cap
	Jitter        Jitter        // how the delay is randomised
	LeaseWait     time.Duration // long-poll duration of one lease request
	Visibility    time.Duration // lease length to ask for; 0 = broker default
	DrainTimeout  time.Duration // how long SIGTERM waits for in-flight jobs
	HoldBeforeAck time.Duration // FAULT INJECTION (tests only): pause between a successful send and its ack
}

func parseBrokerFlags(args []string, stderr io.Writer) (BrokerConfig, error) {
	var c BrokerConfig
	fs := flag.NewFlagSet("broker", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&c.Addr, "addr", "127.0.0.1:7300", "listen address (loopback only)")
	fs.StringVar(&c.Data, "data", "", "data directory (required)")
	fs.DurationVar(&c.Visibility, "visibility", 30*time.Second, "default lease (visibility timeout)")
	fs.IntVar(&c.MaxAttempts, "max-attempts", 5, "default attempts before dead-lettering")
	fs.IntVar(&c.MaxDepth, "max-depth", 10000, "live jobs per queue before producers get 429")
	fs.DurationVar(&c.DedupeWindow, "dedupe-window", 10*time.Minute, "producer Idempotency-Key window")
	fs.BoolVar(&c.Fsync, "fsync", true, "fsync each enqueue before acknowledging it")
	if err := fs.Parse(args); err != nil {
		return c, fmt.Errorf("%w: %v", errUsage, err)
	}
	if c.Data == "" {
		return c, fmt.Errorf("%w: -data is required", errUsage)
	}
	if c.Visibility <= 0 || c.MaxAttempts < 1 || c.MaxDepth < 1 || c.DedupeWindow <= 0 {
		return c, fmt.Errorf("%w: -visibility, -max-attempts, -max-depth and -dedupe-window must be positive", errUsage)
	}
	return c, checkLoopback(c.Addr)
}

func parseWorkerFlags(args []string, stderr io.Writer) (WorkerConfig, error) {
	var c WorkerConfig
	var jitter string
	fs := flag.NewFlagSet("worker", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&c.Addr, "addr", "127.0.0.1:0", "status endpoint address (loopback only)")
	fs.StringVar(&c.Broker, "broker", "", "broker base URL (required)")
	fs.StringVar(&c.Provider, "provider", "", "provider base URL (required)")
	fs.StringVar(&c.Queue, "queue", "default", "queue to consume")
	fs.IntVar(&c.Concurrency, "concurrency", 4, "jobs in flight at most")
	fs.DurationVar(&c.CallTimeout, "call-timeout", 5*time.Second, "per provider call")
	fs.DurationVar(&c.BackoffBase, "backoff-base", 200*time.Millisecond, "first retry delay cap")
	fs.DurationVar(&c.BackoffMax, "backoff-max", 30*time.Second, "largest retry delay cap")
	fs.StringVar(&jitter, "jitter", "full", "full, equal or none")
	fs.DurationVar(&c.LeaseWait, "lease-wait", 2*time.Second, "long-poll duration of one lease request")
	fs.DurationVar(&c.Visibility, "visibility", 0, "lease length to request (0 = broker default)")
	fs.DurationVar(&c.DrainTimeout, "drain-timeout", 15*time.Second, "SIGTERM drain limit")
	fs.DurationVar(&c.HoldBeforeAck, "hold-before-ack", 0, "FAULT INJECTION: pause after a successful send, before the ack")
	if err := fs.Parse(args); err != nil {
		return c, fmt.Errorf("%w: %v", errUsage, err)
	}
	if c.Broker == "" || c.Provider == "" {
		return c, fmt.Errorf("%w: -broker and -provider are required", errUsage)
	}
	if c.Concurrency < 1 || c.CallTimeout <= 0 || c.BackoffBase <= 0 || c.BackoffMax < c.BackoffBase || c.LeaseWait <= 0 {
		return c, fmt.Errorf("%w: -concurrency, -call-timeout, -backoff-base and -lease-wait must be positive, -backoff-max ≥ -backoff-base", errUsage)
	}
	j, ok := ParseJitter(jitter)
	if !ok {
		return c, fmt.Errorf("%w: -jitter must be full, equal or none", errUsage)
	}
	c.Jitter = j
	return c, checkLoopback(c.Addr)
}

func parseProviderFlags(args []string, stderr io.Writer) (string, error) {
	fs := flag.NewFlagSet("provider", flag.ContinueOnError)
	fs.SetOutput(stderr)
	addr := fs.String("addr", "127.0.0.1:7400", "listen address (loopback only)")
	if err := fs.Parse(args); err != nil {
		return "", fmt.Errorf("%w: %v", errUsage, err)
	}
	return *addr, checkLoopback(*addr)
}

// checkLoopback refuses any listen address that is not on the loopback
// interface. These processes have no authentication: binding 0.0.0.0 would
// publish an unauthenticated "send a message to any member" API to the network.
func checkLoopback(addr string) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("%w: -addr %q: %v", errUsage, addr, err)
	}
	if host == "localhost" {
		return nil
	}
	if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("%w: refusing to bind non-loopback address %s (use 127.0.0.1)", errUsage, addr)
	}
	return nil
}

// serveOn serves h on ln, prints "READY <addr>" once it is accepting, and shuts
// down gracefully (up to grace) when ctx is cancelled. Request contexts are
// derived from ctx, so long-poll handlers return as soon as shutdown starts.
func serveOn(ctx context.Context, ln net.Listener, h http.Handler, stdout io.Writer, grace time.Duration) error {
	srv := &http.Server{
		Handler:           h,
		ReadHeaderTimeout: 5 * time.Second,
		BaseContext:       func(net.Listener) context.Context { return ctx },
	}
	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(ln) }()
	fmt.Fprintf(stdout, "READY %s\n", ln.Addr())
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	sctx, cancel := context.WithTimeout(context.Background(), grace)
	defer cancel()
	if err := srv.Shutdown(sctx); err != nil {
		return fmt.Errorf("shutdown: %w", err)
	}
	return nil
}

// writeJSON writes v with the given status. Provided.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// httpError writes {"error": msg}. Provided.
func httpError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// readJSON decodes at most 1 MiB of request body into v. An empty body leaves
// v untouched. Provided.
func readJSON(r *http.Request, v any) error {
	b, err := io.ReadAll(io.LimitReader(r.Body, 1<<20+1))
	if err != nil {
		return err
	}
	if len(b) > 1<<20 {
		return errors.New("body larger than 1 MiB")
	}
	if strings.TrimSpace(string(b)) == "" {
		return nil
	}
	return json.Unmarshal(b, v)
}

// ---------- wire types (shared by broker, worker and tests) ----------

// EnqueueRequest is the body of POST /queues/{q}/jobs.
type EnqueueRequest struct {
	Payload     json.RawMessage `json:"payload"`
	MaxAttempts int             `json:"max_attempts,omitempty"` // 0 = broker default
}

// EnqueueResponse is returned with 201 (new job) or 200 (duplicate key).
type EnqueueResponse struct {
	ID        string `json:"id"`
	Duplicate bool   `json:"duplicate"`
}

// Job is one leased delivery, as returned by POST /queues/{q}/lease.
type Job struct {
	ID             string          `json:"id"`
	Queue          string          `json:"queue"`
	Payload        json.RawMessage `json:"payload"`
	Attempt        int             `json:"attempt"` // 1 on the first delivery
	MaxAttempts    int             `json:"max_attempts"`
	Lease          string          `json:"lease"` // token for ack/nack; changes on every lease
	LeaseExpiresAt time.Time       `json:"lease_expires_at"`
}

// AckRequest is the body of POST /jobs/{id}/ack.
type AckRequest struct {
	Lease string `json:"lease"`
}

// NackRequest is the body of POST /jobs/{id}/nack.
type NackRequest struct {
	Lease        string `json:"lease"`
	Error        string `json:"error"`
	RetryAfterMS int64  `json:"retry_after_ms"` // earliest redelivery, from now
	Permanent    bool   `json:"permanent"`      // dead-letter now, whatever the attempt count
}

// Attempt is one delivery in a job's history.
type Attempt struct {
	N     int       `json:"n"`
	At    time.Time `json:"at"`
	Error string    `json:"error,omitempty"`
}

// DeadJob is one entry of GET /queues/{q}/dlq.
type DeadJob struct {
	ID        string          `json:"id"`
	Queue     string          `json:"queue"`
	Payload   json.RawMessage `json:"payload"`
	Attempts  int             `json:"attempts"`
	LastError string          `json:"last_error"`
	History   []Attempt       `json:"history"`
	DeadAt    time.Time       `json:"dead_at"`
}

// QueueStats is GET /queues/{q}/stats.
type QueueStats struct {
	Queue       string `json:"queue"`
	Ready       int    `json:"ready"`   // leasable now
	Delayed     int    `json:"delayed"` // waiting for their not-before time
	Leased      int    `json:"leased"`  // held by a worker (lease not yet expired)
	Dead        int    `json:"dead"`    // in the dead-letter queue
	Depth       int    `json:"depth"`   // ready + delayed + leased: what -max-depth bounds
	MaxDepth    int    `json:"max_depth"`
	Enqueued    int64  `json:"enqueued_total"`
	Duplicates  int64  `json:"duplicates_total"` // enqueues answered from the dedupe window
	Rejected    int64  `json:"rejected_total"`   // enqueues refused with 429
	Leases      int64  `json:"leases_total"`
	Redelivered int64  `json:"redelivered_total"` // leases granted after a previous lease expired
	Acked       int64  `json:"acked_total"`
	Nacked      int64  `json:"nacked_total"`
	DeadTotal   int64  `json:"dead_total"`
	Replayed    int64  `json:"replayed_total"`
}

// WorkerStats is the worker's GET /stats.
type WorkerStats struct {
	InFlight   int64 `json:"in_flight"`
	Leased     int64 `json:"leased"`
	Acked      int64 `json:"acked"`
	Retried    int64 `json:"retried"`     // nacked with a delay
	Dead       int64 `json:"dead"`        // nacked as permanent
	AckFailed  int64 `json:"ack_failed"`  // ack/nack that never reached the broker
	LeaseLost  int64 `json:"lease_lost"`  // ack/nack refused with 409: someone else owns the job now
	LeaseError int64 `json:"lease_error"` // lease requests that failed (broker down, partition)
	Draining   bool  `json:"draining"`
}
