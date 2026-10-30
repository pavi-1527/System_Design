package rkv

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/chethu007/system-design-course/solutions/e2/wal"
)

const (
	maxKeyLen   = 256
	maxValueLen = 1 << 20
)

// followerState is the leader's record of one follower, updated by its fetches.
type followerState struct {
	acked     uint64    // from-1 of its last matching fetch
	lastFetch time.Time // when that fetch arrived
}

// node is one KV server. Every field below mu is guarded by it. Writes,
// fetches and role changes all happen under mu; waiting (for a follower ack,
// for new entries, for min_index) happens with mu released, on the changed
// channel, which is closed and replaced whenever anything a waiter could care
// about changes (a broadcast).
type node struct {
	cfg    NodeConfig
	log    *wal.Log
	stderr io.Writer
	client *http.Client

	closing chan struct{} // closed when graceful shutdown starts

	mu          sync.Mutex
	self        string // our own URL, once listening
	role        string
	epoch       uint64
	leader      string // follower: URL we fetch from
	leaderURL   string // follower: leader's advertised URL, from its responses
	leaderLast  uint64 // follower: leader's last index as of the last fetch
	lastContact time.Time
	entries     []wal.Entry // entries[i] has Index i+1
	data        map[string][]byte
	followers   map[string]*followerState // leader only
	leaseUntil  time.Time
	changed     chan struct{}
	fetchCancel context.CancelFunc // cancels the follower's in-flight fetch
	truncated   int64              // bytes cut from the WAL at start-up
}

func runNode(ctx context.Context, cfg NodeConfig, stdout, stderr io.Writer) error {
	if err := os.MkdirAll(cfg.DataDir, 0o755); err != nil {
		return err
	}
	lg, rec, err := wal.Open(filepath.Join(cfg.DataDir, WALFileName), cfg.Sync)
	if err != nil {
		return err
	}
	defer lg.Close()
	n := &node{
		cfg: cfg, log: lg, stderr: stderr, client: &http.Client{},
		closing: make(chan struct{}), changed: make(chan struct{}),
		data: map[string][]byte{}, followers: map[string]*followerState{},
		truncated: rec.TruncatedBytes,
	}
	if rec.TruncatedBytes > 0 {
		fmt.Fprintf(stderr, "rkv node %s: WAL recovery kept %d entries and cut %d bytes (%v)\n",
			cfg.ID, len(rec.Entries), rec.TruncatedBytes, rec.Reason)
	}
	meta, ok, err := readMeta(cfg.DataDir)
	if err != nil {
		return err
	}
	if !ok {
		meta = Meta{Epoch: cfg.Epoch, Role: cfg.Role, Leader: cfg.Leader}
		if err := writeMeta(cfg.DataDir, meta); err != nil {
			return err
		}
	}
	n.role, n.epoch, n.leader = meta.Role, meta.Epoch, meta.Leader
	for _, e := range rec.Entries {
		n.applyLocked(e)
		n.epoch = max(n.epoch, e.Epoch)
	}

	loopCtx, stopLoop := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		n.replicate(loopCtx)
	}()
	err = serve(ctx, cfg.Addr, n.routes(), stdout,
		func(u string) { n.mu.Lock(); n.self = u; n.mu.Unlock() },
		func() { close(n.closing) })
	stopLoop()
	wg.Wait()
	return err
}

func (n *node) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /kv/{key}", n.handleGet)
	mux.HandleFunc("PUT /kv/{key}", n.handleWrite(wal.OpPut))
	mux.HandleFunc("DELETE /kv/{key}", n.handleWrite(wal.OpDelete))
	mux.HandleFunc("GET /status", n.handleStatus)
	mux.HandleFunc("GET /repl/fetch", n.handleFetch)
	mux.HandleFunc("POST /admin/promote", n.handlePromote)
	mux.HandleFunc("POST /admin/follow", n.handleFollow)
	mux.HandleFunc("POST /admin/lease", n.handleLease)
	return mux
}

// ---------- state helpers (call with mu held) ----------

func (n *node) applyLocked(e wal.Entry) {
	n.entries = append(n.entries, e)
	switch e.Op {
	case wal.OpPut:
		n.data[e.Key] = e.Value
	case wal.OpDelete:
		delete(n.data, e.Key)
	}
}

func (n *node) broadcastLocked() {
	close(n.changed)
	n.changed = make(chan struct{})
}

// waitLocked releases mu until something changes, the deadline passes, ctx
// ends or shutdown starts, then re-acquires it. It reports whether it woke
// because of a change.
func (n *node) waitLocked(ctx context.Context, deadline time.Time) bool {
	d := time.Until(deadline)
	if d <= 0 {
		return false
	}
	ch := n.changed
	n.mu.Unlock()
	t := time.NewTimer(d)
	woke := false
	select {
	case <-ch:
		woke = true
	case <-t.C:
	case <-ctx.Done():
	case <-n.closing:
	}
	t.Stop()
	n.mu.Lock()
	return woke
}

func (n *node) lastLocked() (index, epoch uint64) {
	if len(n.entries) == 0 {
		return 0, 0
	}
	e := n.entries[len(n.entries)-1]
	return e.Index, e.Epoch
}

func (n *node) advertisedLeaderLocked() string {
	if n.leaderURL != "" {
		return n.leaderURL
	}
	return n.leader
}

// replicatedLocked reports whether some follower has the entry at index on
// its disk (it asked for something after it).
func (n *node) replicatedLocked(index uint64) bool {
	for _, f := range n.followers {
		if f.acked >= index {
			return true
		}
	}
	return false
}

// endOfEpochLocked is the last index whose entry has an epoch ≤ e.
func (n *node) endOfEpochLocked(e uint64) uint64 {
	for i := len(n.entries) - 1; i >= 0; i-- {
		if n.entries[i].Epoch <= e {
			return uint64(i + 1)
		}
	}
	return 0
}

func (n *node) persistMetaLocked() {
	m := Meta{Epoch: n.epoch, Role: n.role, Leader: n.leader}
	if err := writeMeta(n.cfg.DataDir, m); err != nil {
		fmt.Fprintf(n.stderr, "rkv node %s: writing meta: %v\n", n.cfg.ID, err)
	}
}

func (n *node) statusLocked() NodeStatus {
	last, lastEpoch := n.lastLocked()
	now := time.Now()
	st := NodeStatus{
		ID: n.cfg.ID, URL: n.self, Role: n.role, Mode: n.cfg.Mode, Epoch: n.epoch,
		LastIndex: last, LastEpoch: lastEpoch, Keys: len(n.data),
		RecoveredTruncatedBytes: n.truncated, LastContactAgoMs: -1,
	}
	if n.role == RoleLeader {
		st.LeaseValid = !n.cfg.RequireLease || now.Before(n.leaseUntil)
		for id, f := range n.followers {
			fs := FollowerStatus{ID: id, AckedIndex: f.acked, LastFetchAgoMs: now.Sub(f.lastFetch).Milliseconds()}
			if f.acked < last {
				fs.LagRecords = last - f.acked
				fs.LagSeconds = now.Sub(time.Unix(0, n.entries[f.acked].Time)).Seconds()
			}
			st.Followers = append(st.Followers, fs)
		}
		sort.Slice(st.Followers, func(i, j int) bool { return st.Followers[i].ID < st.Followers[j].ID })
		return st
	}
	st.Leader, st.LeaderURL, st.LeaderLastIndex = n.leader, n.leaderURL, n.leaderLast
	if !n.lastContact.IsZero() {
		st.LastContactAgoMs = now.Sub(n.lastContact).Milliseconds()
	}
	return st
}

// ---------- client API ----------

func validKey(k string) bool { return k != "" && len(k) <= maxKeyLen }

func (n *node) handleWrite(op wal.Op) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		key := r.PathValue("key")
		if !validKey(key) {
			writeErr(w, http.StatusBadRequest, ErrorBody{Error: "key must be 1–256 bytes"})
			return
		}
		var val []byte
		if op == wal.OpPut {
			b, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxValueLen))
			if err != nil {
				writeErr(w, http.StatusRequestEntityTooLarge, ErrorBody{Error: "value larger than 1 MiB"})
				return
			}
			val = b
		}
		n.mu.Lock()
		if n.role != RoleLeader {
			l := n.advertisedLeaderLocked()
			n.mu.Unlock()
			writeErr(w, http.StatusMisdirectedRequest, ErrorBody{Error: "not the leader", Leader: l})
			return
		}
		if n.cfg.RequireLease && !time.Now().Before(n.leaseUntil) {
			n.mu.Unlock()
			writeErr(w, http.StatusServiceUnavailable, ErrorBody{Error: "no valid lease: this leader may have been replaced"})
			return
		}
		last, _ := n.lastLocked()
		e := wal.Entry{Epoch: n.epoch, Index: last + 1, Op: op, Key: key, Value: val, Time: time.Now().UnixNano()}
		if err := n.log.Append(e); err != nil {
			n.mu.Unlock()
			writeErr(w, http.StatusInternalServerError, ErrorBody{Error: "wal: " + err.Error()})
			return
		}
		n.applyLocked(e)
		n.broadcastLocked()
		if n.cfg.Mode == ModeSemiSync {
			deadline := time.Now().Add(n.cfg.AckTimeout)
			for !n.replicatedLocked(e.Index) {
				if n.role != RoleLeader || n.epoch != e.Epoch {
					n.mu.Unlock()
					writeErr(w, http.StatusServiceUnavailable, ErrorBody{Error: "lost leadership before the write replicated; outcome unknown", Index: e.Index})
					return
				}
				if !n.waitLocked(r.Context(), deadline) && !n.replicatedLocked(e.Index) {
					n.mu.Unlock()
					writeErr(w, http.StatusServiceUnavailable, ErrorBody{Error: "not replicated within ack timeout; outcome unknown", Index: e.Index})
					return
				}
			}
		}
		n.mu.Unlock()
		writeJSON(w, http.StatusOK, WriteResult{Index: e.Index, Epoch: e.Epoch})
	}
}

func (n *node) handleGet(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")
	if !validKey(key) {
		writeErr(w, http.StatusBadRequest, ErrorBody{Error: "key must be 1–256 bytes"})
		return
	}
	var minIndex uint64
	if s := r.URL.Query().Get("min_index"); s != "" {
		v, err := strconv.ParseUint(s, 10, 64)
		if err != nil {
			writeErr(w, http.StatusBadRequest, ErrorBody{Error: "min_index must be an unsigned integer"})
			return
		}
		minIndex = v
	}
	n.mu.Lock()
	if n.role == RoleFollower && minIndex > 0 {
		deadline := time.Now().Add(n.cfg.RYWWait)
		for n.role == RoleFollower && uint64(len(n.entries)) < minIndex {
			if !n.waitLocked(r.Context(), deadline) {
				break
			}
		}
		if n.role == RoleFollower && uint64(len(n.entries)) < minIndex {
			l := n.advertisedLeaderLocked()
			n.mu.Unlock()
			if l == "" {
				writeErr(w, http.StatusServiceUnavailable, ErrorBody{Error: "not caught up to min_index and no leader known"})
				return
			}
			w.Header().Set("Location", l+r.URL.RequestURI())
			writeErr(w, http.StatusTemporaryRedirect, ErrorBody{Error: "not caught up to min_index; read from the leader", Leader: l})
			return
		}
	}
	v, ok := n.data[key]
	last, _ := n.lastLocked()
	w.Header().Set(HeaderIndex, strconv.FormatUint(last, 10))
	w.Header().Set(HeaderEpoch, strconv.FormatUint(n.epoch, 10))
	w.Header().Set(HeaderRole, n.role)
	n.mu.Unlock()
	if !ok {
		writeErr(w, http.StatusNotFound, ErrorBody{Error: "not found"})
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	_, _ = w.Write(v)
}

func (n *node) handleStatus(w http.ResponseWriter, r *http.Request) {
	n.mu.Lock()
	st := n.statusLocked()
	n.mu.Unlock()
	writeJSON(w, http.StatusOK, st)
}

// ---------- replication: leader side ----------

func queryUint(q url.Values, name string, def uint64) (uint64, error) {
	s := q.Get(name)
	if s == "" {
		return def, nil
	}
	return strconv.ParseUint(s, 10, 64)
}

func (n *node) handleFetch(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	id := q.Get("id")
	from, err1 := queryUint(q, "from", 0)
	lastEpoch, err2 := queryUint(q, "last_epoch", 0)
	epoch, err3 := queryUint(q, "epoch", 0)
	waitMs, err4 := queryUint(q, "wait_ms", 500)
	if id == "" || from == 0 || err1 != nil || err2 != nil || err3 != nil || err4 != nil {
		writeErr(w, http.StatusBadRequest, ErrorBody{Error: "need id, from ≥ 1, last_epoch, epoch, wait_ms"})
		return
	}
	wait := min(time.Duration(waitMs)*time.Millisecond, maxFetchWait)

	n.mu.Lock()
	if n.role != RoleLeader {
		l := n.advertisedLeaderLocked()
		n.mu.Unlock()
		writeErr(w, http.StatusMisdirectedRequest, ErrorBody{Error: "not the leader", Leader: l})
		return
	}
	if epoch > n.epoch {
		n.mu.Unlock()
		writeErr(w, http.StatusConflict, ErrorBody{Error: fmt.Sprintf("stale leader: follower is at epoch %d, this leader at %d", epoch, n.epoch)})
		return
	}
	last, _ := n.lastLocked()
	prev := from - 1
	if prev > 0 && (prev > last || n.entries[prev-1].Epoch != lastEpoch) {
		// Log matching failed: the follower holds entries this leader does
		// not. Tell it where our logs can still agree; it truncates and asks
		// again, and every round strictly lowers the point.
		limit := last
		if prev <= last {
			limit = prev - 1
		}
		t := min(n.endOfEpochLocked(lastEpoch), limit)
		resp := FetchResponse{Epoch: n.epoch, LeaderURL: n.self, LeaderLast: last, TruncateTo: &t}
		n.mu.Unlock()
		writeJSON(w, http.StatusOK, resp)
		return
	}
	f := n.followers[id]
	if f == nil {
		f = &followerState{}
		n.followers[id] = f
	}
	f.acked, f.lastFetch = prev, time.Now()
	n.broadcastLocked() // semi-sync writers may be waiting for exactly this

	deadline := time.Now().Add(wait)
	for uint64(len(n.entries)) < from {
		if !n.waitLocked(r.Context(), deadline) {
			break
		}
		if n.role != RoleLeader {
			n.mu.Unlock()
			writeErr(w, http.StatusMisdirectedRequest, ErrorBody{Error: "no longer the leader"})
			return
		}
	}
	last, _ = n.lastLocked()
	resp := FetchResponse{Epoch: n.epoch, LeaderURL: n.self, LeaderLast: last}
	if from <= last {
		end := min(last, from-1+maxFetchBatch)
		resp.Entries = append([]wal.Entry(nil), n.entries[from-1:end]...)
	}
	n.mu.Unlock()
	writeJSON(w, http.StatusOK, resp)
}

// ---------- replication: follower side ----------

// replicate is the follower's fetch loop. It runs for the node's lifetime
// and idles while the node is a leader.
func (n *node) replicate(ctx context.Context) {
	backoff := 50 * time.Millisecond
	for ctx.Err() == nil {
		n.mu.Lock()
		if n.role != RoleFollower || n.leader == "" {
			ch := n.changed
			n.mu.Unlock()
			select {
			case <-ch:
			case <-ctx.Done():
				return
			case <-time.After(200 * time.Millisecond):
			}
			continue
		}
		leader := n.leader
		last, lastEpoch := n.lastLocked()
		epoch := n.epoch
		fctx, cancel := context.WithTimeout(ctx, n.cfg.FetchWait+2*time.Second)
		n.fetchCancel = cancel
		n.mu.Unlock()

		resp, err := n.fetchOnce(fctx, leader, last+1, lastEpoch, epoch)
		cancel()
		if err != nil {
			sleepCtx(ctx, backoff/2+rand.N(backoff/2+1)) // equal jitter (A7 Unit 1 §2)
			backoff = min(2*backoff, time.Second)
			continue
		}
		backoff = 50 * time.Millisecond
		n.mu.Lock()
		cur, _ := n.lastLocked()
		if n.role == RoleFollower && n.leader == leader && cur == last {
			n.applyFetchLocked(resp)
		}
		n.mu.Unlock()
	}
}

func (n *node) fetchOnce(ctx context.Context, leader string, from, lastEpoch, epoch uint64) (FetchResponse, error) {
	var resp FetchResponse
	u := fmt.Sprintf("%s/repl/fetch?id=%s&from=%d&last_epoch=%d&epoch=%d&wait_ms=%d",
		leader, url.QueryEscape(n.cfg.ID), from, lastEpoch, epoch, n.cfg.FetchWait.Milliseconds())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return resp, err
	}
	r, err := n.client.Do(req)
	if err != nil {
		return resp, err
	}
	defer r.Body.Close()
	if r.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(r.Body, 512))
		return resp, fmt.Errorf("fetch: %s: %s", r.Status, bytes.TrimSpace(b))
	}
	err = json.NewDecoder(r.Body).Decode(&resp)
	return resp, err
}

func (n *node) applyFetchLocked(resp FetchResponse) {
	if resp.Epoch > n.epoch {
		n.epoch = resp.Epoch
		n.persistMetaLocked()
	}
	n.leaderURL, n.leaderLast, n.lastContact = resp.LeaderURL, resp.LeaderLast, time.Now()
	if resp.TruncateTo != nil {
		t := *resp.TruncateTo
		if t < uint64(len(n.entries)) {
			if err := n.log.TruncateAfter(t); err != nil {
				fmt.Fprintf(n.stderr, "rkv node %s: truncate: %v\n", n.cfg.ID, err)
				return
			}
			fmt.Fprintf(n.stderr, "rkv node %s: dropped divergent entries %d–%d\n", n.cfg.ID, t+1, len(n.entries))
			kept := n.entries[:t]
			n.entries, n.data = nil, map[string][]byte{}
			for _, e := range kept {
				n.applyLocked(e)
			}
		}
		n.broadcastLocked()
		return
	}
	if len(resp.Entries) == 0 {
		return
	}
	if err := n.log.Append(resp.Entries...); err != nil {
		fmt.Fprintf(n.stderr, "rkv node %s: append: %v\n", n.cfg.ID, err)
		return
	}
	for _, e := range resp.Entries {
		n.applyLocked(e)
	}
	n.broadcastLocked()
}

func sleepCtx(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
	case <-ctx.Done():
	}
}

// ---------- admin API (the coordinator's) ----------

func decodeBody(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(v); err != nil {
		writeErr(w, http.StatusBadRequest, ErrorBody{Error: "bad JSON body: " + err.Error()})
		return false
	}
	return true
}

func (n *node) handlePromote(w http.ResponseWriter, r *http.Request) {
	var req PromoteRequest
	if !decodeBody(w, r, &req) {
		return
	}
	n.mu.Lock()
	if req.Epoch <= n.epoch {
		cur := n.epoch
		n.mu.Unlock()
		writeErr(w, http.StatusConflict, ErrorBody{Error: fmt.Sprintf("epoch %d is not newer than %d", req.Epoch, cur)})
		return
	}
	n.epoch, n.role, n.leader, n.leaderURL = req.Epoch, RoleLeader, "", ""
	n.followers = map[string]*followerState{}
	n.leaseUntil = time.UnixMilli(req.LeaseExpiresUnixMs)
	n.persistMetaLocked()
	if n.fetchCancel != nil {
		n.fetchCancel()
	}
	n.broadcastLocked()
	st := n.statusLocked()
	n.mu.Unlock()
	fmt.Fprintf(n.stderr, "rkv node %s: promoted to leader at epoch %d\n", n.cfg.ID, req.Epoch)
	writeJSON(w, http.StatusOK, st)
}

func (n *node) handleFollow(w http.ResponseWriter, r *http.Request) {
	var req FollowRequest
	if !decodeBody(w, r, &req) {
		return
	}
	if req.Leader == "" {
		writeErr(w, http.StatusBadRequest, ErrorBody{Error: "leader is required"})
		return
	}
	n.mu.Lock()
	if req.Epoch < n.epoch {
		cur := n.epoch
		n.mu.Unlock()
		writeErr(w, http.StatusConflict, ErrorBody{Error: fmt.Sprintf("epoch %d is older than %d", req.Epoch, cur)})
		return
	}
	wasLeader := n.role == RoleLeader
	if n.leader != req.Leader {
		n.leaderURL, n.leaderLast = "", 0
	}
	n.epoch, n.role, n.leader = req.Epoch, RoleFollower, req.Leader
	n.followers, n.leaseUntil = map[string]*followerState{}, time.Time{}
	n.persistMetaLocked()
	if n.fetchCancel != nil {
		n.fetchCancel()
	}
	n.broadcastLocked()
	st := n.statusLocked()
	n.mu.Unlock()
	if wasLeader {
		fmt.Fprintf(n.stderr, "rkv node %s: demoted; following %s at epoch %d\n", n.cfg.ID, req.Leader, req.Epoch)
	}
	writeJSON(w, http.StatusOK, st)
}

func (n *node) handleLease(w http.ResponseWriter, r *http.Request) {
	var req LeaseRequest
	if !decodeBody(w, r, &req) {
		return
	}
	n.mu.Lock()
	if n.role != RoleLeader || req.Epoch != n.epoch {
		role, cur := n.role, n.epoch
		n.mu.Unlock()
		writeErr(w, http.StatusConflict, ErrorBody{Error: fmt.Sprintf("lease for epoch %d refused: this node is %s at epoch %d", req.Epoch, role, cur)})
		return
	}
	if exp := time.UnixMilli(req.ExpiresUnixMs); exp.After(n.leaseUntil) {
		n.leaseUntil = exp
	}
	n.mu.Unlock()
	writeJSON(w, http.StatusOK, struct{}{})
}

// ---------- meta.json ----------

func readMeta(dir string) (Meta, bool, error) {
	var m Meta
	b, err := os.ReadFile(filepath.Join(dir, MetaFileName))
	if os.IsNotExist(err) {
		return m, false, nil
	}
	if err != nil {
		return m, false, err
	}
	if err := json.Unmarshal(b, &m); err != nil {
		return m, false, fmt.Errorf("%s: %w", MetaFileName, err)
	}
	return m, true, nil
}

// writeMeta replaces meta.json atomically: write a temporary file, fsync it,
// rename it over the old one, fsync the directory (A2 Unit 2 §4).
func writeMeta(dir string, m Meta) error {
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	tmp := filepath.Join(dir, MetaFileName+".tmp")
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.Write(b); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, filepath.Join(dir, MetaFileName)); err != nil {
		return err
	}
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}
