package jobq

import (
	"context"
	"io"
)

// The worker. Milestones 1 (pull loop, bounded concurrency), 2 (classify,
// back off, survive a broker outage), 3 (a stable Idempotency-Key) and
// 5 (graceful drain). modules/E3-job-pipeline/spec.md §5 is the contract.
//
// The design the tests assume:
//
//   - cfg.Concurrency goroutines each run lease → deliver → ack/nack. That
//     bounds the jobs in flight, and so the load on the vendor.
//   - A lease request long-polls the broker for up to cfg.LeaseWait
//     (POST {broker}/queues/{queue}/lease?wait=...). If the broker is
//     unreachable the loop backs off with jitter and tries again; it never
//     exits and never spins. Give the lease HTTP client a timeout a little
//     longer than LeaseWait, or a partitioned broker hangs the loop forever.
//   - Delivery is POST {provider}/send with the job payload as the body and
//     Idempotency-Key: <job id>, the same on every attempt and redelivery.
//     The call's deadline is min(cfg.CallTimeout, time left on the lease minus
//     a margin): a call that outlives its lease can race a second worker.
//   - Classify decides: Success → ack; Retryable → nack with
//     RetryDelay(Backoff(attempt, ...), Retry-After) as retry_after_ms;
//     Permanent → nack with "permanent": true. The broker dead-letters when
//     attempts run out.
//   - cfg.HoldBeforeAck > 0 (fault injection for the tests) sleeps between a
//     successful send and its ack: the window in which a crash causes a
//     redelivery of something the vendor already did.
//   - Ack and nack are retried briefly on transport errors and 5xx; after
//     that the lease expires and the job is redelivered, which the key makes
//     harmless. 409/404 mean the job is no longer yours: count it, move on.
//   - On ctx cancellation (SIGTERM): stop leasing, let the jobs in flight
//     finish (do not cancel their calls: the vendor may already have accepted
//     them), ack them, print "drained" to stderr and return nil. If that takes
//     longer than cfg.DrainTimeout, return an error (Run exits 1).
//
// GET /stats on cfg.Addr serves WorkerStats (jobq.go); start that listener
// first so READY is printed (net.Listen + serveOn in a goroutine, with its own
// context so it outlives the drain).

// runWorker runs the worker until ctx is cancelled and the drain completes.
func runWorker(ctx context.Context, cfg WorkerConfig, stdout, stderr io.Writer) error {
	// TODO(lab): status listener, cfg.Concurrency loops, drain on ctx.Done().
	return errNotImplemented
}
