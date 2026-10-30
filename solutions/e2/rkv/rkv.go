// Package rkv is the E2 project: a replicated key-value store that runs as
// several real processes on 127.0.0.1 and survives real failures.
//
// Two roles share one binary (cmd/rkv) and one entry point, Run:
//
//   - node: a KV server with a write-ahead log (package wal). One node is the
//     leader: it assigns every write an (epoch, index), appends it to its WAL,
//     fsyncs, applies it, and acknowledges it, either at once (async mode) or
//     once at least one follower has fetched past it (semi-sync mode).
//     Followers pull the leader's log with a long-poll fetch whose "from"
//     index doubles as their acknowledgement, Kafka-style.
//   - coord: a sentinel. It renews the leader's lease, declares the leader
//     dead after consecutive failed renewals, waits for the lease to expire,
//     promotes the most up-to-date follower with a higher epoch (the fencing
//     token), and repoints or demotes everyone else.
//
// The full specification, including every endpoint, status code and the
// on-disk layout, is modules/E2-replicated-kv/spec.md. The tests assume it.
//
// This file (types, flags, the process skeleton) is provided. The work is in
// node.go (milestones 1–3 and 5) and coord.go (milestone 4).
//
// Run: go test -race ./labs/e2/rkv/   (stage with -run TestM1, TestM2, …)
package rkv

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

	"github.com/chethu007/system-design-course/solutions/e2/wal"
)

var errNotImplemented = errors.New("rkv: not implemented")

// Roles a node can be in.
const (
	RoleLeader   = "leader"
	RoleFollower = "follower"
)

// Replication modes.
const (
	ModeAsync    = "async"
	ModeSemiSync = "semi-sync"
)

// Response headers.
const (
	HeaderLeader  = "X-Leader"  // advertised URL of the leader, on 421 and 307
	HeaderIndex   = "X-Index"   // the node's last applied index, on reads
	HeaderEpoch   = "X-Epoch"   // the node's current epoch, on reads
	HeaderRole    = "X-Role"    // leader or follower, on reads
	WALFileName   = "wal.log"   // inside -data
	MetaFileName  = "meta.json" // inside -data: Meta, written atomically
	maxFetchBatch = 512
	maxFetchWait  = 2 * time.Second
)

// ---------- wire types (JSON) ----------

// WriteResult is the 200 body of PUT and DELETE /kv/{key}. Index is the
// read-your-writes token: pass it back as ?min_index=.
type WriteResult struct {
	Index uint64 `json:"index"`
	Epoch uint64 `json:"epoch"`
}

// ErrorBody is the body of every 4xx/5xx answer.
type ErrorBody struct {
	Error  string `json:"error"`
	Leader string `json:"leader,omitempty"` // on 421 and 307
	Index  uint64 `json:"index,omitempty"`  // on a semi-sync 503: the entry is in the leader's log, outcome unknown
}

// FollowerStatus is the leader's view of one follower, built from its fetches.
type FollowerStatus struct {
	ID             string  `json:"id"`
	AckedIndex     uint64  `json:"acked_index"`       // from-1 of its last matching fetch: durably on its disk
	LagRecords     uint64  `json:"lag_records"`       // leader last index − AckedIndex
	LagSeconds     float64 `json:"lag_seconds"`       // age of the oldest entry it does not have yet (0 if none)
	LastFetchAgoMs int64   `json:"last_fetch_ago_ms"` // time since its last fetch arrived
}

// NodeStatus is the body of GET /status.
type NodeStatus struct {
	ID         string `json:"id"`
	URL        string `json:"url"` // this node's own http://host:port
	Role       string `json:"role"`
	Mode       string `json:"mode"`
	Epoch      uint64 `json:"epoch"`
	LastIndex  uint64 `json:"last_index"` // = applied index: entries are applied when appended
	LastEpoch  uint64 `json:"last_epoch"` // epoch of the last entry (0 if empty)
	Keys       int    `json:"keys"`
	LeaseValid bool   `json:"lease_valid"` // leader only

	Followers []FollowerStatus `json:"followers,omitempty"` // leader only, sorted by ID

	Leader           string `json:"leader,omitempty"`     // follower: the URL it fetches from
	LeaderURL        string `json:"leader_url,omitempty"` // follower: the leader's advertised URL (from fetch responses)
	LeaderLastIndex  uint64 `json:"leader_last_index"`    // follower: leader's last index as of the last fetch
	LastContactAgoMs int64  `json:"last_contact_ago_ms"`  // follower: since the last successful fetch; -1 = never

	RecoveredTruncatedBytes int64 `json:"recovered_truncated_bytes"` // WAL bytes cut off at start-up
}

// FetchResponse is the 200 body of GET /repl/fetch. Exactly one of
// TruncateTo and Entries is meaningful: when TruncateTo is set, the follower's
// log diverges from the leader's and it must drop every entry after
// *TruncateTo, then fetch again.
type FetchResponse struct {
	Epoch      uint64      `json:"epoch"`
	LeaderURL  string      `json:"leader_url"`
	LeaderLast uint64      `json:"leader_last"`
	TruncateTo *uint64     `json:"truncate_to,omitempty"`
	Entries    []wal.Entry `json:"entries,omitempty"`
}

// PromoteRequest is the body of POST /admin/promote.
type PromoteRequest struct {
	Epoch              uint64 `json:"epoch"`
	LeaseExpiresUnixMs int64  `json:"lease_expires_unix_ms"`
}

// FollowRequest is the body of POST /admin/follow.
type FollowRequest struct {
	Leader string `json:"leader"`
	Epoch  uint64 `json:"epoch"`
}

// LeaseRequest is the body of POST /admin/lease.
type LeaseRequest struct {
	Epoch         uint64 `json:"epoch"`
	ExpiresUnixMs int64  `json:"expires_unix_ms"`
}

// Meta is the node's durable identity, in MetaFileName. It wins over flags
// once it exists: a restarted node comes back in the role and epoch it last
// knew, not the ones on its command line.
type Meta struct {
	Epoch  uint64 `json:"epoch"`
	Role   string `json:"role"`
	Leader string `json:"leader,omitempty"`
}

// CoordStatus is the body of the coordinator's GET /status.
type CoordStatus struct {
	Leader         string   `json:"leader"` // node id, "" while unknown
	LeaderURL      string   `json:"leader_url"`
	Epoch          uint64   `json:"epoch"`
	Failovers      int      `json:"failovers"`
	LastFailoverMs int64    `json:"last_failover_ms"` // first failed renewal → promotion answered
	Events         []string `json:"events"`           // human-readable, newest last, at most 50
}

// ---------- configuration ----------

// NodeConfig is parsed from the node role's flags.
type NodeConfig struct {
	ID           string
	Addr         string
	DataDir      string
	Role         string // initial role, used only when DataDir has no meta.json
	Leader       string // initial leader URL for a follower
	Epoch        uint64 // initial epoch
	Mode         string
	AckTimeout   time.Duration // semi-sync: longest wait for a follower before answering 503
	RequireLease bool          // leader refuses writes without a valid lease from the coordinator
	RYWWait      time.Duration // follower: longest wait for ?min_index before redirecting to the leader
	FetchWait    time.Duration // follower: long-poll wait it asks the leader for
	Sync         wal.SyncPolicy
}

// CoordConfig is parsed from the coord role's flags.
type CoordConfig struct {
	Addr         string
	Nodes        []NodeRef
	Interval     time.Duration // between ticks (lease renewal + reconciliation)
	FailAfter    int           // consecutive failed renewals that declare the leader dead
	Lease        time.Duration // lease length granted on each renewal
	CheckTimeout time.Duration // per-request timeout towards nodes
}

// NodeRef names one node for the coordinator.
type NodeRef struct {
	ID  string
	URL string
}

// Run is the process entry point shared by cmd/rkv and the tests. args[0] is
// the role ("node" or "coord"), the rest are its flags. It returns the exit
// code: 0 after a graceful shutdown (ctx cancelled), 1 on a runtime error, 2
// on bad usage, including a non-loopback -addr.
func Run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: rkv node|coord [flags]")
		return 2
	}
	var err error
	switch args[0] {
	case "node":
		cfg, perr := parseNode(args[1:], stderr)
		if perr != nil {
			fmt.Fprintf(stderr, "rkv node: %v\n", perr)
			return 2
		}
		err = runNode(ctx, cfg, stdout, stderr)
	case "coord":
		cfg, perr := parseCoord(args[1:], stderr)
		if perr != nil {
			fmt.Fprintf(stderr, "rkv coord: %v\n", perr)
			return 2
		}
		err = runCoord(ctx, cfg, stdout, stderr)
	default:
		fmt.Fprintf(stderr, "rkv: unknown role %q (want node or coord)\n", args[0])
		return 2
	}
	if err != nil {
		fmt.Fprintf(stderr, "rkv %s: %v\n", args[0], err)
		return 1
	}
	return 0
}

func parseNode(args []string, stderr io.Writer) (NodeConfig, error) {
	var c NodeConfig
	fs := flag.NewFlagSet("rkv node", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&c.ID, "id", "n1", "node id")
	fs.StringVar(&c.Addr, "addr", "127.0.0.1:0", "listen address (loopback only)")
	fs.StringVar(&c.DataDir, "data", "", "data directory (WAL and meta.json)")
	fs.StringVar(&c.Role, "role", RoleLeader, "initial role: leader or follower")
	fs.StringVar(&c.Leader, "leader", "", "initial leader URL (followers)")
	fs.Uint64Var(&c.Epoch, "epoch", 1, "initial epoch")
	fs.StringVar(&c.Mode, "mode", ModeAsync, "replication mode: async or semi-sync")
	fs.DurationVar(&c.AckTimeout, "ack-timeout", 2*time.Second, "semi-sync: wait for a follower at most this long")
	fs.BoolVar(&c.RequireLease, "require-lease", false, "leader accepts writes only under a valid lease")
	fs.DurationVar(&c.RYWWait, "ryw-wait", 500*time.Millisecond, "follower: wait this long for ?min_index")
	fs.DurationVar(&c.FetchWait, "fetch-wait", 500*time.Millisecond, "follower: long-poll wait per fetch")
	syncFlag := fs.String("sync", "always", "WAL fsync policy: always or never")
	if err := fs.Parse(args); err != nil {
		return c, err
	}
	if err := checkLoopback(c.Addr); err != nil {
		return c, err
	}
	if c.DataDir == "" {
		return c, errors.New("-data is required")
	}
	if c.Role != RoleLeader && c.Role != RoleFollower {
		return c, fmt.Errorf("-role %q: want leader or follower", c.Role)
	}
	if c.Role == RoleFollower && c.Leader == "" {
		return c, errors.New("a follower needs -leader")
	}
	if c.Mode != ModeAsync && c.Mode != ModeSemiSync {
		return c, fmt.Errorf("-mode %q: want async or semi-sync", c.Mode)
	}
	switch *syncFlag {
	case "always":
		c.Sync = wal.SyncAlways
	case "never":
		c.Sync = wal.SyncNever
	default:
		return c, fmt.Errorf("-sync %q: want always or never", *syncFlag)
	}
	return c, nil
}

func parseCoord(args []string, stderr io.Writer) (CoordConfig, error) {
	var c CoordConfig
	fs := flag.NewFlagSet("rkv coord", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&c.Addr, "addr", "127.0.0.1:0", "listen address (loopback only)")
	nodes := fs.String("nodes", "", "comma-separated id=url list")
	fs.DurationVar(&c.Interval, "interval", 100*time.Millisecond, "tick interval")
	fs.IntVar(&c.FailAfter, "fail-after", 3, "consecutive failed renewals before failover")
	fs.DurationVar(&c.Lease, "lease", time.Second, "lease length")
	fs.DurationVar(&c.CheckTimeout, "check-timeout", 250*time.Millisecond, "per-request timeout")
	if err := fs.Parse(args); err != nil {
		return c, err
	}
	if err := checkLoopback(c.Addr); err != nil {
		return c, err
	}
	for _, kv := range strings.Split(*nodes, ",") {
		id, u, ok := strings.Cut(strings.TrimSpace(kv), "=")
		if !ok || id == "" || u == "" {
			return c, fmt.Errorf("-nodes entry %q: want id=url", kv)
		}
		c.Nodes = append(c.Nodes, NodeRef{ID: id, URL: strings.TrimRight(u, "/")})
	}
	if c.FailAfter < 1 || c.Interval <= 0 || c.Lease <= 0 {
		return c, errors.New("-fail-after, -interval and -lease must be positive")
	}
	return c, nil
}

// checkLoopback refuses anything but 127.0.0.0/8, ::1 and localhost: this
// project has no authentication, so it must never be reachable from a
// network.
func checkLoopback(addr string) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("-addr %q: %v", addr, err)
	}
	if host == "localhost" {
		return nil
	}
	if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("refusing to listen on non-loopback address %q (use 127.0.0.1)", addr)
	}
	return nil
}

// serve listens on addr, calls ready with the bound URL, prints
// "READY host:port", serves h until ctx is cancelled, then shuts down
// gracefully (in-flight requests get up to 5 s). onShutdown runs when shutdown
// starts, so long-poll handlers can return early.
func serve(ctx context.Context, addr string, h http.Handler, stdout io.Writer, ready func(url string), onShutdown func()) error {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	srv := &http.Server{Handler: h, ReadHeaderTimeout: 5 * time.Second}
	if onShutdown != nil {
		srv.RegisterOnShutdown(onShutdown)
	}
	if ready != nil {
		ready("http://" + ln.Addr().String())
	}
	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(ln) }()
	fmt.Fprintf(stdout, "READY %s\n", ln.Addr())
	select {
	case <-ctx.Done():
	case err := <-errc:
		return err
	}
	sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(sctx); err != nil {
		return err
	}
	return nil
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, body ErrorBody) {
	if body.Leader != "" {
		w.Header().Set(HeaderLeader, body.Leader)
	}
	writeJSON(w, code, body)
}
