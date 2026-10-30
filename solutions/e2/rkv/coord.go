package rkv

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"
)

// coord is the sentinel. One goroutine (loop) does all the deciding; mu only
// protects the fields /status reads.
type coord struct {
	cfg    CoordConfig
	client *http.Client
	stderr io.Writer

	mu         sync.Mutex
	leader     string // node id; "" = unknown
	epoch      uint64
	failures   int       // consecutive failed renewals
	failStart  time.Time // first failure of the current streak
	lastExpiry time.Time // latest lease expiry we sent the current leader (acknowledged or not)
	failovers  int
	lastFoMs   int64
	events     []string
}

func runCoord(ctx context.Context, cfg CoordConfig, stdout, stderr io.Writer) error {
	c := &coord{cfg: cfg, client: &http.Client{Timeout: cfg.CheckTimeout}, stderr: stderr}
	loopCtx, stop := context.WithCancel(ctx)
	defer stop()
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		c.loop(loopCtx)
	}()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /status", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, c.status())
	})
	err := serve(ctx, cfg.Addr, mux, stdout, nil, nil)
	stop()
	wg.Wait()
	return err
}

func (c *coord) status() CoordStatus {
	c.mu.Lock()
	defer c.mu.Unlock()
	return CoordStatus{Leader: c.leader, LeaderURL: c.urlOf(c.leader), Epoch: c.epoch, Failovers: c.failovers,
		LastFailoverMs: c.lastFoMs, Events: append([]string(nil), c.events...)}
}

func (c *coord) urlOf(id string) string {
	for _, n := range c.cfg.Nodes {
		if n.ID == id {
			return n.URL
		}
	}
	return ""
}

func (c *coord) event(format string, a ...any) {
	msg := time.Now().Format("15:04:05.000 ") + fmt.Sprintf(format, a...)
	fmt.Fprintln(c.stderr, "rkv coord:", msg)
	c.mu.Lock()
	c.events = append(c.events, msg)
	if len(c.events) > 50 {
		c.events = c.events[len(c.events)-50:]
	}
	c.mu.Unlock()
}

func (c *coord) loop(ctx context.Context) {
	t := time.NewTicker(c.cfg.Interval)
	defer t.Stop()
	for {
		c.tick(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (c *coord) tick(ctx context.Context) {
	c.mu.Lock()
	leader, epoch := c.leader, c.epoch
	c.mu.Unlock()
	if leader == "" {
		c.discover(ctx)
		return
	}
	// Count the lease as granted the moment we send it: a renewal that times
	// out here may still sit in the leader's socket buffer and be applied
	// later, so failover must outwait it too.
	exp := time.Now().Add(c.cfg.Lease)
	c.mu.Lock()
	if exp.After(c.lastExpiry) {
		c.lastExpiry = exp
	}
	c.mu.Unlock()
	err := c.post(ctx, c.urlOf(leader)+"/admin/lease", LeaseRequest{Epoch: epoch, ExpiresUnixMs: exp.UnixMilli()}, nil)
	c.mu.Lock()
	if err == nil {
		c.failures = 0
		c.mu.Unlock()
	} else {
		if c.failures == 0 {
			c.failStart = time.Now()
		}
		c.failures++
		n := c.failures
		c.mu.Unlock()
		if n == 1 || n == c.cfg.FailAfter {
			c.event("lease renewal for %s (epoch %d) failed %d time(s): %v", leader, epoch, n, err)
		}
		if n >= c.cfg.FailAfter {
			c.failover(ctx, leader)
			return
		}
	}
	c.reconcile(ctx)
}

// discover finds the current leader (highest epoch among nodes claiming the
// role); if nobody claims it, it elects one.
func (c *coord) discover(ctx context.Context) {
	sts := c.statuses(ctx, "")
	var best *NodeStatus
	var bestID string
	for id, st := range sts {
		if st.Role == RoleLeader && (best == nil || st.Epoch > best.Epoch) {
			s := st
			best, bestID = &s, id
		}
	}
	if best == nil {
		if len(sts) > 0 {
			c.failover(ctx, "")
		}
		return
	}
	c.mu.Lock()
	c.leader, c.epoch, c.failures = bestID, best.Epoch, 0
	c.mu.Unlock()
	c.event("leader is %s at epoch %d", bestID, best.Epoch)
}

// failover promotes the most up-to-date reachable node other than dead, after
// dead's lease has certainly expired, then repoints everyone else.
func (c *coord) failover(ctx context.Context, dead string) {
	c.mu.Lock()
	wait := time.Until(c.lastExpiry) + 50*time.Millisecond
	start := c.failStart
	c.mu.Unlock()
	if wait > 0 {
		sleepCtx(ctx, wait) // until then, the old leader may still believe it leads
	}
	sts := c.statuses(ctx, dead)
	var bestID string
	var best NodeStatus
	maxEpoch := c.status().Epoch
	for id, st := range sts {
		maxEpoch = max(maxEpoch, st.Epoch)
		if bestID == "" || st.LastEpoch > best.LastEpoch ||
			(st.LastEpoch == best.LastEpoch && st.LastIndex > best.LastIndex) ||
			(st.LastEpoch == best.LastEpoch && st.LastIndex == best.LastIndex && id < bestID) {
			bestID, best = id, st
		}
	}
	if bestID == "" {
		c.event("failover: no reachable candidate")
		return
	}
	newEpoch := maxEpoch + 1
	exp := time.Now().Add(c.cfg.Lease)
	if err := c.post(ctx, c.urlOf(bestID)+"/admin/promote", PromoteRequest{Epoch: newEpoch, LeaseExpiresUnixMs: exp.UnixMilli()}, nil); err != nil {
		c.event("failover: promoting %s failed: %v", bestID, err)
		return
	}
	c.mu.Lock()
	c.leader, c.epoch, c.failures, c.lastExpiry = bestID, newEpoch, 0, exp
	c.failovers++
	if !start.IsZero() {
		c.lastFoMs = time.Since(start).Milliseconds()
	}
	c.mu.Unlock()
	c.event("promoted %s (last index %d, epoch %d) to leader at epoch %d", bestID, best.LastIndex, best.LastEpoch, newEpoch)
	c.reconcile(ctx)
}

// reconcile tells every reachable node that is behind the current epoch, or
// claims leadership without being the current leader, to follow the current
// leader. This is also how an old leader that comes back is demoted.
func (c *coord) reconcile(ctx context.Context) {
	c.mu.Lock()
	leader, epoch := c.leader, c.epoch
	c.mu.Unlock()
	lurl := c.urlOf(leader)
	for id, st := range c.statuses(ctx, leader) {
		if st.Role == RoleLeader && st.Epoch > epoch {
			c.mu.Lock()
			c.leader, c.epoch, c.failures = id, st.Epoch, 0
			c.mu.Unlock()
			c.event("found %s leading at newer epoch %d; adopting it", id, st.Epoch)
			return
		}
		// A follower already at the current epoch was told about this leader
		// (by us, or by the leader's own fetch responses), possibly through a
		// different address than ours: leave it alone.
		if st.Role == RoleFollower && st.Epoch == epoch {
			continue
		}
		if err := c.post(ctx, c.urlOf(id)+"/admin/follow", FollowRequest{Leader: lurl, Epoch: epoch}, nil); err != nil {
			c.event("repointing %s failed: %v", id, err)
			continue
		}
		if st.Role == RoleLeader {
			c.event("demoted stale leader %s (epoch %d) to follow %s at epoch %d", id, st.Epoch, leader, epoch)
		} else {
			c.event("%s now follows %s at epoch %d", id, leader, epoch)
		}
	}
}

// statuses fetches /status from every node except skip, concurrently;
// unreachable nodes are left out.
func (c *coord) statuses(ctx context.Context, skip string) map[string]NodeStatus {
	var mu sync.Mutex
	out := map[string]NodeStatus{}
	var wg sync.WaitGroup
	for _, n := range c.cfg.Nodes {
		if n.ID == skip {
			continue
		}
		wg.Add(1)
		go func(n NodeRef) {
			defer wg.Done()
			var st NodeStatus
			if err := c.get(ctx, n.URL+"/status", &st); err == nil {
				mu.Lock()
				out[n.ID] = st
				mu.Unlock()
			}
		}(n)
	}
	wg.Wait()
	return out
}

func (c *coord) get(ctx context.Context, u string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	return c.do(req, out)
}

func (c *coord) post(ctx context.Context, u string, body, out any) error {
	b, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	return c.do(req, out)
}

func (c *coord) do(req *http.Request, out any) error {
	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("%s %s: %s %s", req.Method, req.URL.Path, resp.Status, bytes.TrimSpace(b))
	}
	if out == nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}
