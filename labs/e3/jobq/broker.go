package jobq

import (
	"context"
	"encoding/json"
	"io"
	"time"
)

// The broker. Milestones 1 (queue, leases, recovery), 2 (nack delays),
// 3 (producer Idempotency-Keys), 4 (dead letters, replay) and 5 (depth bound).
// modules/E3-job-pipeline/spec.md §3 is the HTTP contract and §4 the log
// format; the tests check both from outside the process.
//
// The design the tests assume:
//
//   - State is rebuilt from <data>/queue.log, a sequence of frames (frame.go),
//     each one a JSON-encoded record of one state change. Every change is
//     appended to the log *before* it is applied in memory and before the
//     HTTP response, so a 201 means "it is in the log". Enqueues are fsynced
//     (with -fsync); acks, nacks and leases may be written without fsync,
//     because losing one only causes a redelivery, which idempotency absorbs.
//     (Decide this yourself and defend it in the design write-up.)
//   - Recovery replays every good frame and truncates a torn or corrupt tail
//     (ScanFrames gives you the offset), then appends after it.
//   - Lease expiries are absolute times in the log, so a lease survives a
//     broker restart: the worker holding it can still ack, and if it never
//     does, the job is redelivered when the lease runs out. Attempt counts
//     survive too, so a job that crashes workers still runs out of attempts.
//   - A lease token identifies one attempt of one job (for example
//     "<id>.<attempt>"). Ack and nack succeed only with the token of the job's
//     latest attempt while that attempt is unsettled; an older token gets 409,
//     an unknown or already acked job 404.
//   - Leasing scans live jobs of the queue in enqueue order and takes the first
//     that is ready (not-before reached) or whose lease has expired. An expired
//     lease on a job with no attempts left dead-letters it instead. A nacked
//     job steps out of line until its not-before time; a replayed job goes to
//     the back of the line.
//   - Depth = ready + delayed + leased per queue. An enqueue (or a replay) that
//     would exceed -max-depth is refused with 429 and Retry-After. A producer
//     key seen within -dedupe-window answers 200 with the original id, even
//     when the queue is full.
//   - A lease request long-polls: it waits up to ?wait= for a job, waking on an
//     enqueue, a nack or a replay (and often enough to notice a not-before time
//     or a lease expiry passing). It returns 204 when nothing came, and at
//     once when the server is shutting down (serveOn derives request contexts
//     from ctx).
//
// One mutex around all queue state is enough here (and makes the
// check-then-insert of producer keys trivially atomic). Hold it while you
// append to the log, never while you wait in a long-poll.

// logRecord is one suggested shape for a log entry; change it if you like.
// Only the frame format (frame.go) and the file name are part of the contract.
type logRecord struct {
	Op          string          `json:"op"` // enq, lease, ack, nack, dead, replay
	ID          string          `json:"id"`
	Seq         int64           `json:"seq,omitempty"`
	Queue       string          `json:"queue,omitempty"`
	Payload     json.RawMessage `json:"payload,omitempty"`
	Key         string          `json:"key,omitempty"`
	MaxAttempts int             `json:"max_attempts,omitempty"`
	Attempt     int             `json:"attempt,omitempty"`
	At          time.Time       `json:"at"`
	Until       time.Time       `json:"until,omitempty"` // lease end or not-before
	Error       string          `json:"error,omitempty"`
}

// runBroker opens (and recovers) cfg.Data/queue.log, then serves the broker
// API on cfg.Addr until ctx is cancelled. Use net.Listen and serveOn (jobq.go)
// for the listener, the READY line and graceful shutdown.
//
// Routes (Go 1.22 ServeMux patterns):
//
//	POST /queues/{q}/jobs          enqueue      201 | 200 duplicate | 400 | 429 + Retry-After
//	POST /queues/{q}/lease         lease        200 Job | 204 (?wait=2s&visibility=30s)
//	POST /jobs/{id}/ack            ack          204 | 404 | 409
//	POST /jobs/{id}/nack           nack         204 | 404 | 409
//	GET  /queues/{q}/dlq           DLQ          200 {"jobs": [DeadJob]}
//	POST /queues/{q}/dlq/replay    replay       200 {"replayed": n, "skipped": n}
//	GET  /queues/{q}/stats         stats        200 QueueStats
//	GET  /healthz                               204
func runBroker(ctx context.Context, cfg BrokerConfig, stdout, stderr io.Writer) error {
	// TODO(lab): recover from the log, build the handlers, serve.
	return errNotImplemented
}
