package jobq

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// The broker's design (the tests assume it):
//
//   - State is rebuilt from data/queue.log, a sequence of frames (frame.go),
//     each one JSON-encoded logRecord. Every state change is appended to the
//     log *before* it is applied in memory and before the HTTP response, so a
//     201 means "it is in the log". Enqueues are fsynced (with -fsync) because
//     losing one loses a message; acks, nacks and leases are written but not
//     fsynced, because losing one only causes a redelivery, which idempotency
//     absorbs.
//   - Recovery replays every good frame and truncates a torn or corrupt tail.
//     Lease expiries are absolute times in the log, so a lease survives a
//     broker restart: the worker holding it can still ack, and if it never
//     does, the job is redelivered when the lease runs out, exactly as without
//     the restart. Attempt counts survive too, so a job that crashes workers
//     still runs out of attempts.
//   - A lease token is "<id>.<attempt>". Ack and nack are accepted only with
//     the token of the job's latest attempt, while the job is live; a token
//     from an older attempt gets 409 (the job belongs to someone else now).
//   - Leasing scans live jobs in enqueue order and takes the first one that is
//     ready (not-before reached) or whose lease has expired. An expired lease
//     on a job with no attempts left dead-letters it instead.
//   - Depth = ready + delayed + leased per queue. An enqueue (or a replay)
//     that would exceed -max-depth is refused with 429 and Retry-After.
//   - Producer Idempotency-Keys are remembered per queue for -dedupe-window,
//     rebuilt from the log on restart.

type jobState int

const (
	stateReady  jobState = iota // leasable once notBefore has passed
	stateLeased                 // held until leaseUntil
	stateDead                   // in the DLQ
)

type entry struct {
	seq         int64
	id, queue   string
	payload     json.RawMessage
	key         string
	maxAttempts int
	attempts    int
	state       jobState
	notBefore   time.Time
	leaseUntil  time.Time
	history     []Attempt
	lastErr     string
	deadAt      time.Time
	acked       bool
	pos         int // index of this entry's latest position in broker.order
}

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

type keyRec struct {
	id string
	at time.Time
}

type counters struct {
	enqueued, duplicates, rejected, leases, redelivered, acked, nacked, dead, replayed int64
}

type broker struct {
	cfg BrokerConfig
	now func() time.Time

	mu      sync.Mutex
	log     *os.File
	nextSeq int64
	jobs    map[string]*entry // live and dead jobs; acked jobs are removed
	order   []*entry          // live jobs in enqueue order (may hold acked/dead entries until compacted)
	live    map[string]int    // queue → depth
	keys    map[string]keyRec // queue + "\x00" + key → first enqueue
	count   map[string]*counters
	changed chan struct{} // closed and replaced whenever a job may have become leasable
}

func runBroker(ctx context.Context, cfg BrokerConfig, stdout, stderr io.Writer) error {
	b, err := openBroker(cfg)
	if err != nil {
		return err
	}
	defer b.close()
	ln, err := net.Listen("tcp", cfg.Addr)
	if err != nil {
		return err
	}
	return serveOn(ctx, ln, b.handler(), stdout, 5*time.Second)
}

// openBroker recovers state from cfg.Data/queue.log (creating it if needed).
func openBroker(cfg BrokerConfig) (*broker, error) {
	if err := os.MkdirAll(cfg.Data, 0o755); err != nil {
		return nil, err
	}
	b := &broker{
		cfg: cfg, now: time.Now,
		jobs: map[string]*entry{}, live: map[string]int{}, keys: map[string]keyRec{},
		count: map[string]*counters{}, changed: make(chan struct{}),
	}
	path := filepath.Join(cfg.Data, "queue.log")
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return nil, err
	}
	frames, good, err := ScanFrames(f)
	if err != nil {
		f.Close()
		return nil, fmt.Errorf("recover %s: %w", path, err)
	}
	for i, fr := range frames {
		var r logRecord
		if err := json.Unmarshal(fr, &r); err != nil {
			// A frame with a good CRC that isn't JSON is a bug, not a torn
			// write; stop here rather than guess.
			good = 0
			for _, prev := range frames[:i] {
				good += int64(FrameHeader + len(prev))
			}
			break
		}
		b.apply(r)
	}
	if err := f.Truncate(good); err != nil {
		f.Close()
		return nil, err
	}
	if _, err := f.Seek(good, io.SeekStart); err != nil {
		f.Close()
		return nil, err
	}
	b.log = f
	return b, nil
}

func (b *broker) close() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.log != nil {
		_ = b.log.Sync()
		_ = b.log.Close()
		b.log = nil
	}
}

func (b *broker) ctr(q string) *counters {
	c := b.count[q]
	if c == nil {
		c = &counters{}
		b.count[q] = c
	}
	return c
}

// write appends r to the log (fsyncing when sync is set). Caller holds b.mu.
func (b *broker) write(r logRecord, sync bool) error {
	p, err := json.Marshal(r)
	if err != nil {
		return err
	}
	if b.log == nil {
		return errors.New("broker closed")
	}
	if err := AppendFrame(b.log, p); err != nil {
		return err
	}
	if sync && b.cfg.Fsync {
		return b.log.Sync()
	}
	return nil
}

// commit writes then applies r. Caller holds b.mu.
func (b *broker) commit(r logRecord, sync bool) error {
	if err := b.write(r, sync); err != nil {
		return err
	}
	b.apply(r)
	return nil
}

// apply changes in-memory state for one record. It is used both live and
// during recovery, which is what keeps the two in agreement. Caller holds b.mu
// (or is the only goroutine, during recovery).
func (b *broker) apply(r logRecord) {
	if r.Op == "enq" {
		e := &entry{seq: r.Seq, id: r.ID, queue: r.Queue, payload: r.Payload, key: r.Key, maxAttempts: r.MaxAttempts}
		b.jobs[e.id] = e
		b.push(e)
		b.live[e.queue]++
		b.nextSeq = max(b.nextSeq, r.Seq)
		if r.Key != "" {
			b.keys[r.Queue+"\x00"+r.Key] = keyRec{id: r.ID, at: r.At}
		}
		b.ctr(e.queue).enqueued++
		b.notify()
		return
	}
	e := b.jobs[r.ID]
	if e == nil {
		return
	}
	c := b.ctr(e.queue)
	switch r.Op {
	case "lease":
		if e.attempts > 0 && e.state == stateLeased {
			c.redelivered++ // the previous lease expired without an ack
		}
		e.attempts = r.Attempt
		e.state = stateLeased
		e.leaseUntil = r.Until
		e.notBefore = time.Time{}
		e.history = append(e.history, Attempt{N: r.Attempt, At: r.At})
		c.leases++
	case "ack":
		e.acked = true
		delete(b.jobs, e.id)
		b.live[e.queue]--
		c.acked++
	case "nack":
		e.state = stateReady
		e.notBefore = r.Until
		e.leaseUntil = time.Time{}
		e.lastErr = r.Error
		b.setLastError(e, r.Error)
		c.nacked++
		b.notify()
	case "dead":
		if e.state != stateDead {
			b.live[e.queue]--
		}
		e.state = stateDead
		e.lastErr = r.Error
		e.deadAt = r.At
		b.setLastError(e, r.Error)
		c.dead++
	case "replay":
		if e.state == stateDead {
			b.live[e.queue]++
		}
		e.state = stateReady
		e.attempts = 0
		e.notBefore = time.Time{}
		e.deadAt = time.Time{}
		// A replayed job goes to the back of the line: it must not jump ahead
		// of work that arrived while it sat in the DLQ.
		b.push(e)
		c.replayed++
		b.notify()
	}
}

func (b *broker) setLastError(e *entry, msg string) {
	if n := len(e.history); n > 0 && msg != "" {
		e.history[n-1].Error = msg
	}
}

// push appends e to the lease order; an earlier position of e becomes stale.
func (b *broker) push(e *entry) {
	e.pos = len(b.order)
	b.order = append(b.order, e)
}

// compactOrder drops acked, dead and stale (replayed) positions.
func (b *broker) compactOrder() {
	out := make([]*entry, 0, len(b.jobs))
	for i, e := range b.order {
		if !e.acked && e.state != stateDead && e.pos == i {
			e.pos = len(out)
			out = append(out, e)
		}
	}
	b.order = out
}

func (b *broker) notify() {
	close(b.changed)
	b.changed = make(chan struct{})
}

func (b *broker) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /queues/{q}/jobs", b.handleEnqueue)
	mux.HandleFunc("POST /queues/{q}/lease", b.handleLease)
	mux.HandleFunc("POST /jobs/{id}/ack", b.handleAck)
	mux.HandleFunc("POST /jobs/{id}/nack", b.handleNack)
	mux.HandleFunc("GET /queues/{q}/dlq", b.handleDLQ)
	mux.HandleFunc("POST /queues/{q}/dlq/replay", b.handleReplay)
	mux.HandleFunc("GET /queues/{q}/stats", b.handleStats)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) })
	return mux
}

func (b *broker) handleEnqueue(w http.ResponseWriter, r *http.Request) {
	q := r.PathValue("q")
	var req EnqueueRequest
	if err := readJSON(r, &req); err != nil || len(bytes.TrimSpace(req.Payload)) == 0 || !json.Valid(req.Payload) {
		httpError(w, http.StatusBadRequest, "body must be {\"payload\": <JSON>, \"max_attempts\": n}")
		return
	}
	if req.MaxAttempts < 0 {
		httpError(w, http.StatusBadRequest, "max_attempts must be ≥ 0")
		return
	}
	key := r.Header.Get("Idempotency-Key")
	now := b.now()

	b.mu.Lock()
	defer b.mu.Unlock()
	c := b.ctr(q)
	if key != "" {
		if k, ok := b.keys[q+"\x00"+key]; ok && now.Sub(k.at) < b.cfg.DedupeWindow {
			c.duplicates++
			writeJSON(w, http.StatusOK, EnqueueResponse{ID: k.id, Duplicate: true})
			return
		}
	}
	if b.live[q] >= b.cfg.MaxDepth {
		c.rejected++
		w.Header().Set("Retry-After", "1")
		httpError(w, http.StatusTooManyRequests, fmt.Sprintf("queue %s is full (%d jobs)", q, b.cfg.MaxDepth))
		return
	}
	if len(b.keys) > 4*b.cfg.MaxDepth {
		b.pruneKeys(now)
	}
	attempts := req.MaxAttempts
	if attempts == 0 {
		attempts = b.cfg.MaxAttempts
	}
	seq := b.nextSeq + 1
	rec := logRecord{Op: "enq", ID: fmt.Sprintf("j%08d", seq), Seq: seq, Queue: q, Payload: req.Payload,
		Key: key, MaxAttempts: attempts, At: now}
	if err := b.commit(rec, true); err != nil {
		httpError(w, http.StatusInternalServerError, "log append failed: "+err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, EnqueueResponse{ID: rec.ID})
}

func (b *broker) pruneKeys(now time.Time) {
	for k, v := range b.keys {
		if now.Sub(v.at) >= b.cfg.DedupeWindow {
			delete(b.keys, k)
		}
	}
}

func (b *broker) handleLease(w http.ResponseWriter, r *http.Request) {
	q := r.PathValue("q")
	wait, err := durationParam(r, "wait", 0)
	if err != nil {
		httpError(w, http.StatusBadRequest, err.Error())
		return
	}
	vis, err := durationParam(r, "visibility", b.cfg.Visibility)
	if err != nil || vis <= 0 {
		httpError(w, http.StatusBadRequest, "visibility must be a positive duration")
		return
	}
	wait = min(wait, 30*time.Second)
	deadline := time.Now().Add(wait)
	for {
		b.mu.Lock()
		job, ok, err := b.tryLease(q, vis)
		ch := b.changed
		b.mu.Unlock()
		if err != nil {
			httpError(w, http.StatusInternalServerError, err.Error())
			return
		}
		if ok {
			writeJSON(w, http.StatusOK, job)
			return
		}
		left := time.Until(deadline)
		if left <= 0 {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		// Wake on a change, or soon enough to notice a not-before time or a
		// lease expiry passing.
		t := time.NewTimer(min(left, 25*time.Millisecond))
		select {
		case <-ch:
		case <-t.C:
		case <-r.Context().Done():
			t.Stop()
			w.WriteHeader(http.StatusNoContent)
			return
		}
		t.Stop()
	}
}

// tryLease grants the first eligible job of q. Caller holds b.mu.
func (b *broker) tryLease(q string, vis time.Duration) (Job, bool, error) {
	if len(b.order) > 64 && len(b.order) > 2*len(b.jobs) {
		b.compactOrder() // never during the loop below: commit may change state
	}
	now := b.now()
	for i, e := range b.order {
		if e.pos != i || e.queue != q || e.acked || e.state == stateDead {
			continue
		}
		switch e.state {
		case stateReady:
			if now.Before(e.notBefore) {
				continue
			}
		case stateLeased:
			if now.Before(e.leaseUntil) {
				continue
			}
			if e.attempts >= e.maxAttempts {
				msg := fmt.Sprintf("lease expired on attempt %d of %d (worker crashed or timed out)", e.attempts, e.maxAttempts)
				if err := b.commit(logRecord{Op: "dead", ID: e.id, At: now, Error: msg}, false); err != nil {
					return Job{}, false, err
				}
				continue
			}
		}
		if e.attempts >= e.maxAttempts { // a nacked job can't get here, but a recovered one can
			continue
		}
		attempt := e.attempts + 1
		rec := logRecord{Op: "lease", ID: e.id, Attempt: attempt, At: now, Until: now.Add(vis)}
		if err := b.commit(rec, false); err != nil {
			return Job{}, false, err
		}
		return Job{ID: e.id, Queue: e.queue, Payload: e.payload, Attempt: attempt, MaxAttempts: e.maxAttempts,
			Lease: leaseToken(e.id, attempt), LeaseExpiresAt: e.leaseUntil}, true, nil
	}
	return Job{}, false, nil
}

func leaseToken(id string, attempt int) string { return id + "." + strconv.Itoa(attempt) }

// checkLease validates a token against the job's latest attempt. Caller holds b.mu.
func (b *broker) checkLease(id, lease string) (*entry, int, string) {
	e := b.jobs[id]
	if e == nil || e.acked {
		return nil, http.StatusNotFound, "unknown job (or already acknowledged)"
	}
	if e.state == stateDead {
		return nil, http.StatusConflict, "job is dead-lettered"
	}
	if e.attempts == 0 || lease != leaseToken(id, e.attempts) {
		return nil, http.StatusConflict, "lease is not the job's current lease (it expired and was granted again)"
	}
	if e.state == stateReady && !e.notBefore.IsZero() {
		return nil, http.StatusConflict, "this attempt was already nacked"
	}
	return e, 0, ""
}

func (b *broker) handleAck(w http.ResponseWriter, r *http.Request) {
	var req AckRequest
	if err := readJSON(r, &req); err != nil {
		httpError(w, http.StatusBadRequest, err.Error())
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	e, status, msg := b.checkLease(r.PathValue("id"), req.Lease)
	if e == nil {
		httpError(w, status, msg)
		return
	}
	if err := b.commit(logRecord{Op: "ack", ID: e.id, At: b.now()}, false); err != nil {
		httpError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (b *broker) handleNack(w http.ResponseWriter, r *http.Request) {
	var req NackRequest
	if err := readJSON(r, &req); err != nil {
		httpError(w, http.StatusBadRequest, err.Error())
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	e, status, msg := b.checkLease(r.PathValue("id"), req.Lease)
	if e == nil {
		httpError(w, status, msg)
		return
	}
	now := b.now()
	errMsg := req.Error
	if errMsg == "" {
		errMsg = "nack without a reason"
	}
	rec := logRecord{Op: "nack", ID: e.id, At: now, Until: now.Add(time.Duration(max(req.RetryAfterMS, 0)) * time.Millisecond), Error: errMsg}
	switch {
	case req.Permanent:
		rec = logRecord{Op: "dead", ID: e.id, At: now, Error: "permanent: " + errMsg}
	case e.attempts >= e.maxAttempts:
		rec = logRecord{Op: "dead", ID: e.id, At: now, Error: fmt.Sprintf("attempts exhausted (%d): %s", e.attempts, errMsg)}
	}
	// Nack is the one place where a nack must be told apart from a fresh
	// ready job in checkLease, so a zero delay still records a not-before.
	if rec.Op == "nack" && !rec.Until.After(now) {
		rec.Until = now.Add(time.Nanosecond)
	}
	if err := b.commit(rec, false); err != nil {
		httpError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (b *broker) handleDLQ(w http.ResponseWriter, r *http.Request) {
	q := r.PathValue("q")
	b.mu.Lock()
	var out []DeadJob
	for _, e := range b.jobs {
		if e.queue == q && e.state == stateDead {
			out = append(out, DeadJob{ID: e.id, Queue: e.queue, Payload: e.payload, Attempts: e.attempts,
				LastError: e.lastErr, History: append([]Attempt(nil), e.history...), DeadAt: e.deadAt})
		}
	}
	b.mu.Unlock()
	sortDead(out)
	writeJSON(w, http.StatusOK, map[string]any{"jobs": out})
}

func sortDead(d []DeadJob) {
	for i := 1; i < len(d); i++ { // insertion sort by id; DLQs listed here are small
		for j := i; j > 0 && d[j].ID < d[j-1].ID; j-- {
			d[j], d[j-1] = d[j-1], d[j]
		}
	}
}

func (b *broker) handleReplay(w http.ResponseWriter, r *http.Request) {
	q := r.PathValue("q")
	var req struct {
		IDs []string `json:"ids"`
	}
	if err := readJSON(r, &req); err != nil {
		httpError(w, http.StatusBadRequest, err.Error())
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	var ids []string
	if len(req.IDs) == 0 {
		for id, e := range b.jobs {
			if e.queue == q && e.state == stateDead {
				ids = append(ids, id)
			}
		}
	} else {
		ids = req.IDs
	}
	sortStrings(ids)
	replayed, skipped := 0, 0
	now := b.now()
	for _, id := range ids {
		e := b.jobs[id]
		if e == nil || e.queue != q || e.state != stateDead {
			skipped++
			continue
		}
		if b.live[q] >= b.cfg.MaxDepth {
			skipped++ // replay respects the depth bound too: a DLQ replay is a burst
			continue
		}
		if err := b.commit(logRecord{Op: "replay", ID: id, At: now}, true); err != nil {
			httpError(w, http.StatusInternalServerError, err.Error())
			return
		}
		replayed++
	}
	writeJSON(w, http.StatusOK, map[string]int{"replayed": replayed, "skipped": skipped})
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

func (b *broker) handleStats(w http.ResponseWriter, r *http.Request) {
	q := r.PathValue("q")
	b.mu.Lock()
	now := b.now()
	st := QueueStats{Queue: q, MaxDepth: b.cfg.MaxDepth}
	for _, e := range b.jobs {
		if e.queue != q {
			continue
		}
		switch {
		case e.state == stateDead:
			st.Dead++
		case e.state == stateLeased && now.Before(e.leaseUntil):
			st.Leased++
		case e.state == stateReady && now.Before(e.notBefore):
			st.Delayed++
		default:
			st.Ready++
		}
	}
	st.Depth = b.live[q]
	c := b.ctr(q)
	st.Enqueued, st.Duplicates, st.Rejected = c.enqueued, c.duplicates, c.rejected
	st.Leases, st.Redelivered, st.Acked, st.Nacked = c.leases, c.redelivered, c.acked, c.nacked
	st.DeadTotal, st.Replayed = c.dead, c.replayed
	b.mu.Unlock()
	writeJSON(w, http.StatusOK, st)
}

func durationParam(r *http.Request, name string, def time.Duration) (time.Duration, error) {
	s := strings.TrimSpace(r.URL.Query().Get(name))
	if s == "" {
		return def, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil || d < 0 {
		return 0, fmt.Errorf("%s: want a duration like 2s, got %q", name, s)
	}
	return d, nil
}
