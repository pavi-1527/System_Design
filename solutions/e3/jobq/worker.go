package jobq

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// The worker's design (the tests assume it):
//
//   - Concurrency goroutines each run lease → deliver → ack/nack. That bounds
//     the jobs in flight, and so the load on the vendor, at Concurrency.
//   - A lease request long-polls the broker for up to LeaseWait. If the broker
//     is unreachable the loop backs off (with jitter) and tries again; it never
//     exits and never spins.
//   - Delivery is POST provider/send with the job payload and
//     Idempotency-Key: <job id>, the same on every attempt and every
//     redelivery. The call's deadline is min(CallTimeout, time left on the
//     lease minus a margin): a call that outlives its lease could race a
//     second worker on the same job. There is no lease extension; a job that
//     needs longer than one lease needs a longer -visibility.
//   - Classify decides: Success → ack; Retryable → nack with
//     RetryDelay(Backoff(attempt), Retry-After); Permanent → nack permanent.
//     The broker dead-letters when attempts run out.
//   - Ack and nack are retried briefly. If they still fail, the lease expires
//     and the job is redelivered; the idempotency key makes that harmless.
//   - On SIGTERM (ctx cancelled): stop leasing, finish the jobs in flight
//     (their calls are not cancelled), ack them, exit 0. A job whose lease
//     response was in transit at that moment stays leased to nobody until its
//     visibility timeout, then goes to another worker: late, not lost.

type worker struct {
	cfg      WorkerConfig
	stderr   io.Writer
	broker   *http.Client // ack, nack
	poll     *http.Client // lease long-polls
	vendor   *http.Client // provider calls; deadline per call
	draining atomic.Bool

	inFlight, leased, acked, retried, dead, ackFailed, leaseLost, leaseError atomic.Int64
}

func runWorker(ctx context.Context, cfg WorkerConfig, stdout, stderr io.Writer) error {
	ln, err := net.Listen("tcp", cfg.Addr)
	if err != nil {
		return err
	}
	tr := &http.Transport{MaxIdleConnsPerHost: cfg.Concurrency + 2, IdleConnTimeout: 30 * time.Second}
	w := &worker{
		cfg: cfg, stderr: stderr,
		broker: &http.Client{Transport: tr, Timeout: 2 * time.Second},
		poll:   &http.Client{Transport: tr, Timeout: cfg.LeaseWait + 2*time.Second},
		vendor: &http.Client{Transport: tr},
	}
	srvCtx, stopSrv := context.WithCancel(context.Background())
	defer stopSrv()
	srvErr := make(chan error, 1)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /stats", w.handleStats)
	go func() { srvErr <- serveOn(srvCtx, ln, mux, stdout, 2*time.Second) }()

	var wg sync.WaitGroup
	for range cfg.Concurrency {
		wg.Add(1)
		go func() {
			defer wg.Done()
			w.loop(ctx)
		}()
	}
	select {
	case <-ctx.Done():
	case err := <-srvErr:
		return fmt.Errorf("status endpoint: %w", err)
	}
	w.draining.Store(true)
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
		fmt.Fprintf(stderr, "jobq worker: drained (acked %d)\n", w.acked.Load())
		return nil
	case <-time.After(cfg.DrainTimeout):
		return fmt.Errorf("drain timed out with %d jobs in flight; their leases will expire and be redelivered", w.inFlight.Load())
	}
}

func (w *worker) loop(ctx context.Context) {
	failures := 0
	for ctx.Err() == nil {
		job, ok, err := w.lease(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			w.leaseError.Add(1)
			failures++
			sleepCtx(ctx, Backoff(failures, 50*time.Millisecond, 2*time.Second, FullJitter, rand.Float64()))
			continue
		}
		failures = 0
		if ok {
			w.handle(job) // deliberately not tied to ctx: a drain finishes in-flight work
		}
	}
}

func (w *worker) lease(ctx context.Context) (Job, bool, error) {
	u := fmt.Sprintf("%s/queues/%s/lease?wait=%s", w.cfg.Broker, url.PathEscape(w.cfg.Queue), w.cfg.LeaseWait)
	if w.cfg.Visibility > 0 {
		u += "&visibility=" + w.cfg.Visibility.String()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, nil)
	if err != nil {
		return Job{}, false, err
	}
	resp, err := w.poll.Do(req)
	if err != nil {
		return Job{}, false, err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusNoContent:
		return Job{}, false, nil
	case http.StatusOK:
		var j Job
		if err := json.NewDecoder(resp.Body).Decode(&j); err != nil {
			return Job{}, false, err
		}
		return j, true, nil
	default:
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return Job{}, false, fmt.Errorf("lease: %s: %s", resp.Status, strings.TrimSpace(string(b)))
	}
}

func (w *worker) handle(j Job) {
	w.inFlight.Add(1)
	defer w.inFlight.Add(-1)
	w.leased.Add(1)

	left := time.Until(j.LeaseExpiresAt)
	budget := min(w.cfg.CallTimeout, left-min(left/5, time.Second))
	if budget <= 0 {
		w.nack(j, "lease too short to attempt a call", 0, false)
		return
	}
	status, retryAfter, err := w.send(j, budget)
	switch Classify(status, err) {
	case Success:
		if w.cfg.HoldBeforeAck > 0 {
			time.Sleep(w.cfg.HoldBeforeAck) // fault injection: the crash window between effect and ack
		}
		w.ack(j)
	case Retryable:
		reason := fmt.Sprintf("attempt %d: status %d", j.Attempt, status)
		if err != nil {
			reason = fmt.Sprintf("attempt %d: %v", j.Attempt, err)
		}
		d := RetryDelay(Backoff(j.Attempt, w.cfg.BackoffBase, w.cfg.BackoffMax, w.cfg.Jitter, rand.Float64()), retryAfter)
		w.nack(j, reason, d, false)
	case Permanent:
		w.nack(j, fmt.Sprintf("attempt %d: status %d", j.Attempt, status), 0, true)
	}
}

// send delivers one job to the provider. It returns the status (0 if no
// response arrived), the Retry-After delay if one was given, and the transport
// error.
func (w *worker) send(j Job, budget time.Duration) (int, time.Duration, error) {
	ctx, cancel := context.WithTimeout(context.Background(), budget)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, w.cfg.Provider+"/send", bytes.NewReader(j.Payload))
	if err != nil {
		return 0, 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", j.ID)
	resp, err := w.vendor.Do(req)
	if err != nil {
		return 0, 0, err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	ra, _ := ParseRetryAfter(resp.Header.Get("Retry-After"), time.Now())
	return resp.StatusCode, ra, nil
}

func (w *worker) ack(j Job) {
	switch w.settle(j.ID, "ack", AckRequest{Lease: j.Lease}) {
	case http.StatusNoContent:
		w.acked.Add(1)
	case http.StatusConflict, http.StatusNotFound:
		w.leaseLost.Add(1)
	default:
		w.ackFailed.Add(1)
	}
}

func (w *worker) nack(j Job, reason string, delay time.Duration, permanent bool) {
	req := NackRequest{Lease: j.Lease, Error: reason, RetryAfterMS: delay.Milliseconds(), Permanent: permanent}
	switch w.settle(j.ID, "nack", req) {
	case http.StatusNoContent:
		if permanent {
			w.dead.Add(1)
		} else {
			w.retried.Add(1)
		}
	case http.StatusConflict, http.StatusNotFound:
		w.leaseLost.Add(1)
	default:
		w.ackFailed.Add(1)
	}
}

// settle posts an ack or nack, retrying transport errors and 5xx a few times.
// It returns the final status, or 0 if the broker never answered.
func (w *worker) settle(id, op string, body any) int {
	b, _ := json.Marshal(body)
	u := fmt.Sprintf("%s/jobs/%s/%s", w.cfg.Broker, url.PathEscape(id), op)
	for try := 1; ; try++ {
		resp, err := w.broker.Post(u, "application/json", bytes.NewReader(b))
		status := 0
		if err == nil {
			status = resp.StatusCode
			_, _ = io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			if status < 500 {
				return status
			}
		}
		if try == 3 {
			fmt.Fprintf(w.stderr, "jobq worker: %s %s failed after %d tries (status %d, %v); the lease will expire\n", op, id, try, status, err)
			return status
		}
		time.Sleep(Backoff(try, 50*time.Millisecond, 500*time.Millisecond, FullJitter, rand.Float64()))
	}
}

func (w *worker) handleStats(rw http.ResponseWriter, r *http.Request) {
	writeJSON(rw, http.StatusOK, WorkerStats{
		InFlight: w.inFlight.Load(), Leased: w.leased.Load(), Acked: w.acked.Load(), Retried: w.retried.Load(),
		Dead: w.dead.Load(), AckFailed: w.ackFailed.Load(), LeaseLost: w.leaseLost.Load(),
		LeaseError: w.leaseError.Load(), Draining: w.draining.Load(),
	})
}
