package rkv

import (
	"context"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/chethu007/system-design-course/labs/e2/wal"
)

// The node role: milestones 1, 2, 3 and 5. modules/E2-replicated-kv/spec.md
// is the contract; the doc comments below are the design the tests assume.

// followerState is the leader's record of one follower, updated by its fetches.
type followerState struct {
	acked     uint64    // from-1 of its last matching fetch: on its disk
	lastFetch time.Time // when that fetch arrived
}

// node is one KV server. Suggested design (the one the reference uses):
//
//   - One mutex guards everything below it. Writes, fetches and role changes
//     happen under it; the WAL append and fsync happen under it too, which
//     serialises writes and keeps index assignment trivially correct.
//   - Waiting (a semi-sync writer for its ack, a fetch long-poll for new
//     entries, a follower read for ?min_index) happens with the mutex
//     released, on the changed channel. Whoever changes something a waiter
//     could care about closes changed and replaces it (a broadcast), and
//     every waiter re-checks its own condition. Each wait is bounded by a
//     deadline, the request's context, and closing.
//   - entries mirrors the WAL in memory (entries[i] has Index i+1); data is
//     the applied map. A follower applies entries as it appends them.
type node struct {
	cfg    NodeConfig
	log    *wal.Log
	stderr io.Writer
	client *http.Client

	closing chan struct{} // closed when graceful shutdown starts (serve's onShutdown)

	mu          sync.Mutex
	self        string // our own URL, once listening
	role        string
	epoch       uint64
	leader      string // follower: URL we fetch from
	leaderURL   string // follower: leader's advertised URL, from its fetch responses
	leaderLast  uint64 // follower: leader's last index as of the last fetch
	lastContact time.Time
	entries     []wal.Entry
	data        map[string][]byte
	followers   map[string]*followerState // leader only
	leaseUntil  time.Time
	changed     chan struct{}
	fetchCancel context.CancelFunc // cancels the follower's in-flight fetch on a role change
	truncated   int64              // bytes cut from the WAL at start-up
}

// runNode runs the node role until ctx is cancelled.
//
//  1. Create cfg.DataDir; wal.Open(<data>/wal.log, cfg.Sync) and keep the
//     Recovery's TruncatedBytes for /status.
//  2. Read <data>/meta.json (Meta). If it does not exist, build it from the
//     flags (Epoch, Role, Leader) and write it. Meta wins over flags.
//  3. Replay the recovered entries into data; epoch = max(meta epoch, last
//     entry's epoch).
//  4. Start the follower fetch loop (replicate) in a goroutine with its own
//     context; it idles while the node leads.
//  5. serve(ctx, cfg.Addr, n.routes(), stdout, set n.self, close n.closing).
//  6. After serve returns: stop the loop, wait for it, close the WAL.
func runNode(ctx context.Context, cfg NodeConfig, stdout, stderr io.Writer) error {
	// TODO(lab)
	return errNotImplemented
}

// routes is the HTTP API. Provided.
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

func notImplemented(w http.ResponseWriter) {
	writeErr(w, http.StatusNotImplemented, ErrorBody{Error: errNotImplemented.Error()})
}

// handleWrite serves PUT and DELETE /kv/{key} (key 1–256 bytes, value ≤ 1 MiB).
//
//   - Not the leader: 421 with ErrorBody.Leader (and the X-Leader header) set
//     to the advertised leader URL.
//   - -require-lease and no valid lease: 503. A leader without a lease may
//     already have been replaced.
//   - Otherwise build the entry (current epoch, last index + 1, now), append
//     it to the WAL (fsync), apply it, broadcast.
//   - async: 200 WriteResult at once.
//   - semi-sync: wait (mutex released, at most -ack-timeout) until some
//     follower's acked ≥ the entry's index, then 200. On timeout: 503 with
//     ErrorBody.Index set: the entry is in the log, the outcome is unknown.
//     If the node loses leadership while waiting: 503 the same way.
func (n *node) handleWrite(op wal.Op) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// TODO(lab)
		notImplemented(w)
	}
}

// handleGet serves GET /kv/{key}[?min_index=N]: 200 with the raw value or
// 404, both carrying X-Index (last applied index), X-Epoch and X-Role; 400 if
// min_index is not a number.
//
// On a follower with min_index > its last index: wait up to -ryw-wait for
// replication; if still behind, 307 with Location = <leader URL> +
// r.URL.RequestURI() (503 if no leader is known). A leader always serves.
func (n *node) handleGet(w http.ResponseWriter, r *http.Request) {
	// TODO(lab)
	notImplemented(w)
}

// handleStatus serves GET /status as a NodeStatus. Leader: LeaseValid
// (always true without -require-lease) and one FollowerStatus per follower
// that has fetched, sorted by id, with lag_records = last − acked and
// lag_seconds = age of entry acked+1 (0 when caught up). Follower: Leader,
// LeaderURL, LeaderLastIndex and LastContactAgoMs (-1 before the first
// successful fetch).
func (n *node) handleStatus(w http.ResponseWriter, r *http.Request) {
	// TODO(lab)
	notImplemented(w)
}

// handleFetch serves GET /repl/fetch?id=&from=&last_epoch=&epoch=&wait_ms=
// on the leader (spec.md, "Replication protocol").
//
//   - Not the leader: 421. Requester's epoch > ours: 409 (we are stale).
//   - Log matching: let prev = from−1. If prev > 0 and either prev > our last
//     index or our entry prev has an epoch ≠ last_epoch, answer 200 with
//     TruncateTo = min(last index whose epoch ≤ last_epoch, limit), where
//     limit is our last index if prev > last, else prev−1. Strictly lower
//     every round, so it converges.
//   - Match: record the follower's acked = prev and lastFetch = now,
//     broadcast (semi-sync writers wait for this), then long-poll up to
//     min(wait_ms, 2 s) for entries ≥ from and return at most 512 of them.
//     Every response carries our epoch, our own URL and our last index.
func (n *node) handleFetch(w http.ResponseWriter, r *http.Request) {
	// TODO(lab)
	notImplemented(w)
}

// replicate is the follower's fetch loop, for the node's whole lifetime.
//
// While the node is a follower: fetch from n.leader with from = last+1,
// last_epoch = epoch of the last entry, epoch = n.epoch, wait_ms =
// -fetch-wait, under a context with timeout -fetch-wait + 2 s whose cancel
// you store in n.fetchCancel (promote/follow call it). On any error, back off
// with jitter (≈ 50 ms doubling to 1 s). On success, under the mutex and only
// if nothing changed meanwhile (still a follower of the same leader, same
// last index): adopt a higher epoch (persist meta.json), record leaderURL,
// leaderLast and lastContact, then either truncate (TruncateTo: WAL
// TruncateAfter, rebuild data from the kept entries) or append the entries
// to the WAL (one Append, one fsync) and apply them. Broadcast.
//
// While the node leads: wait on changed (or 200 ms) and look again.
func (n *node) replicate(ctx context.Context) {
	// TODO(lab)
}

// handlePromote serves POST /admin/promote (PromoteRequest). Epoch must be
// greater than ours, else 409. Become leader at that epoch with a lease until
// LeaseExpiresUnixMs, forget the old followers, persist meta.json, cancel any
// in-flight fetch, broadcast, answer 200 with the new NodeStatus.
func (n *node) handlePromote(w http.ResponseWriter, r *http.Request) {
	// TODO(lab)
	notImplemented(w)
}

// handleFollow serves POST /admin/follow (FollowRequest). Leader required
// (400); Epoch must be ≥ ours, else 409. Become a follower of Leader at that
// epoch, drop any lease, persist meta.json, cancel any in-flight fetch,
// broadcast, answer 200 with the new NodeStatus. This is how a deposed leader
// is demoted; its divergent suffix goes at the first fetch.
func (n *node) handleFollow(w http.ResponseWriter, r *http.Request) {
	// TODO(lab)
	notImplemented(w)
}

// handleLease serves POST /admin/lease (LeaseRequest). Only a leader whose
// epoch equals Epoch accepts it (else 409); the lease is extended to
// ExpiresUnixMs if that is later than the current one (a delayed renewal
// must never shorten or, being absolute, wrongly lengthen it). 200 {}.
func (n *node) handleLease(w http.ResponseWriter, r *http.Request) {
	// TODO(lab)
	notImplemented(w)
}

// writeMeta replaces <dir>/meta.json atomically: write meta.json.tmp, fsync
// it, rename it over meta.json, fsync the directory (A2 Unit 2 §4). A crash at
// any point leaves either the old file or the new one, never half of one.
func writeMeta(dir string, m Meta) error {
	// TODO(lab)
	return errNotImplemented
}

// readMeta reads <dir>/meta.json. ok is false (and err nil) if it does not
// exist yet.
func readMeta(dir string) (m Meta, ok bool, err error) {
	// TODO(lab)
	return Meta{}, false, errNotImplemented
}
