package hashcache

import (
	"context"
	"errors"
	"math"
	"time"
)

// Stage 3 errors.
var (
	ErrEmptyRing     = errors.New("hashcache: empty ring")
	ErrInvalidFactor = errors.New("hashcache: bounded-load factor must be >= 1")
)

// Migration controls what happens to keys whose owner changes in SetMembership.
type Migration struct {
	// Grace is how long the previous membership stays readable. Zero means a
	// hard cut-over: moved keys simply miss (consistent hashing keeps that to
	// about 1/(N+1) of the keys when one node is added).
	Grace time.Duration
	// TTL is applied to entries copied forward from a previous owner (their
	// remaining TTL is unknown), bounding how long a migrated entry can live.
	// Zero means one minute.
	TTL time.Duration
}

// SetMembership atomically replaces the set of backends. Requests already in
// flight finish against the membership they started with; new requests see the
// new one. Only the immediately previous membership is kept for the grace
// window (a second change replaces it).
//
// During the window:
//   - Get that misses (or finds no healthy replica) on the current owners asks
//     the previous owners, unless they are the same nodes, and on a hit copies
//     the value forward with Backend.Add (never Set: a newer concurrent write
//     must not be overwritten) before returning it. Each moved key is
//     migrated at most once; later reads hit the new owner directly.
//   - Set and Delete also delete the key from previous owners that are no
//     longer owners, so a stale copy cannot be migrated back later.
//   - Backends of removed nodes stay reachable for reads, but receive no new
//     writes.
func (c *Client) SetMembership(backends map[string]Backend, m Migration) error {
	c.setMu.Lock()
	defer c.setMu.Unlock()
	next, err := c.build(backends)
	if err != nil {
		return err
	}
	if m.Grace > 0 {
		old := c.st.Load()
		prev := *old // copy: drop the older generation so chains do not grow
		prev.prev = nil
		next.prev = &prev
		next.prevUntil = c.cfg.Now().Add(m.Grace)
		next.migrateTTL = m.TTL
		if next.migrateTTL <= 0 {
			next.migrateTTL = time.Minute
		}
	}
	c.st.Store(next)
	return nil
}

func (c *Client) prevActive(st *state) bool {
	return st.prev != nil && c.cfg.Now().Before(st.prevUntil)
}

func sameNodes(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// getFromPrev implements the read-through migration described on SetMembership.
func (c *Client) getFromPrev(ctx context.Context, st *state, key string) ([]byte, bool) {
	if !c.prevActive(st) {
		return nil, false
	}
	cur := c.replicasFor(st, key)
	old := c.replicasFor(st.prev, key)
	if sameNodes(cur, old) {
		return nil, false // already asked exactly those nodes
	}
	v, err := c.getFrom(ctx, st.prev, key)
	if err != nil {
		return nil, false
	}
	for _, n := range cur {
		err := st.backends[n].Add(ctx, key, v, st.migrateTTL)
		c.record(ctx, n, err)
	}
	return v, true
}

// dropFromPrev deletes key from previous owners that no longer own it.
func (c *Client) dropFromPrev(ctx context.Context, st *state, key string) {
	if !c.prevActive(st) {
		return
	}
	cur := map[string]bool{}
	for _, n := range c.replicasFor(st, key) {
		cur[n] = true
	}
	for _, n := range c.replicasFor(st.prev, key) {
		if cur[n] {
			continue
		}
		err := st.prev.backends[n].Delete(ctx, key)
		if errors.Is(err, ErrNotFound) {
			err = nil
		}
		c.record(ctx, n, err)
	}
}

// AssignBounded places keys on r's nodes with "consistent hashing with bounded
// loads": no node receives more than ceil(c * len(unique keys) / len(nodes))
// keys. Keys are processed in order (duplicates ignored); each goes to its
// natural owner unless that node is full, then to the next node clockwise with
// room. c must be at least 1; c = 1.25 is typical. The result depends on key
// order, which is the price of the bound.
func (r *Ring) AssignBounded(keys []string, c float64) (map[string]string, error) {
	if c < 1 {
		return nil, ErrInvalidFactor
	}
	nodes := r.Nodes()
	if len(nodes) == 0 {
		return nil, ErrEmptyRing
	}
	seen := make(map[string]struct{}, len(keys))
	uniq := keys[:0:0]
	for _, k := range keys {
		if _, dup := seen[k]; !dup {
			seen[k] = struct{}{}
			uniq = append(uniq, k)
		}
	}
	capacity := int(math.Ceil(c * float64(len(uniq)) / float64(len(nodes))))
	load := make(map[string]int, len(nodes))
	out := make(map[string]string, len(uniq))
	for _, k := range uniq {
		for _, n := range r.Replicas(k, len(nodes)) {
			if load[n] < capacity {
				load[n]++
				out[k] = n
				break
			}
		}
	}
	return out, nil
}
