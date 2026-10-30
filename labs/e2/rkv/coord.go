package rkv

import (
	"context"
	"io"
	"net/http"
	"sync"
	"time"
)

// The coord role: milestone 4. It is a sentinel, not a consensus group: one
// process decides, so it is a single point of failure for failover (not for
// serving). Unit 4 §3 is about exactly that.

// coord holds the sentinel's state. Suggested design: one goroutine (loop)
// does all the deciding, ticking every -interval; mu only protects the fields
// GET /status reads.
type coord struct {
	cfg    CoordConfig
	client *http.Client // Timeout: cfg.CheckTimeout
	stderr io.Writer

	mu         sync.Mutex
	leader     string    // node id; "" = unknown
	epoch      uint64    // the current leader's epoch
	failures   int       // consecutive failed lease renewals
	failStart  time.Time // first failure of the current streak (for LastFailoverMs)
	lastExpiry time.Time // latest lease expiry SENT to the current leader, acknowledged or not
	failovers  int
	lastFoMs   int64
	events     []string // newest last, at most 50
}

// runCoord runs the coordinator until ctx is cancelled: start the loop, serve
// GET /status (CoordStatus) with serve(ctx, cfg.Addr, mux, stdout, nil, nil),
// then stop the loop and wait for it.
//
// Each tick:
//
//   - No known leader: GET /status from every node; the node claiming
//     leadership with the highest epoch is the leader. If none claims it but
//     some are reachable, elect one (failover with no dead node).
//   - Known leader: renew its lease, POST /admin/lease {epoch, now + -lease}.
//     Record the expiry as granted *before* sending: a renewal that times out
//     here can still sit in the leader's socket buffer and be applied when it
//     wakes up. On success reset the failure count; on failure count it, and
//     after -fail-after consecutive failures fail over.
//   - Then reconcile: every reachable node other than the leader that is at
//     an older epoch, or claims leadership, gets POST /admin/follow {leader
//     URL, epoch}. Followers already at the current epoch are left alone. A
//     node leading at a newer epoch than ours means we are the stale one:
//     adopt it.
//
// Failover: sleep until lastExpiry + 50 ms (until then the old leader may
// still believe it leads), GET /status from every other node, pick the one
// with the highest (last_epoch, last_index), ties by lowest id, POST
// /admin/promote {max epoch seen + 1, lease expiry}, record it, count the
// failover and its duration, then reconcile.
func runCoord(ctx context.Context, cfg CoordConfig, stdout, stderr io.Writer) error {
	// TODO(lab)
	return errNotImplemented
}
