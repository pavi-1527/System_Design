package pubsub

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"sync/atomic"
)

// ---- Stage 3: consumer groups, per-key ordering, graceful shutdown (minute 55) --
//
// Contract:
//   - WithGroup(name) makes a subscription a member of the group `name` on its
//     topic. Every group receives every message; within a group each message
//     goes to exactly ONE member (competing consumers). Subscriptions without
//     a group each receive every message, as in stage 1.
//   - A message with a Key always goes to the same member of a group while the
//     membership is unchanged (hash of the key). Messages without a key go to
//     the next member with room (round robin).
//   - In ack mode, at most ONE message per key is in flight on a subscription
//     at a time. Later messages with that key are held back (they count
//     toward the queue bound) until the earlier one is acked or
//     dead-lettered. This is what keeps per-key order intact across
//     redelivery: m1's redelivery comes before m2's first delivery.
//   - When a member leaves (Unsubscribe) in ack mode, every message it still
//     owed an ack for, and every message held back for it, goes to the
//     remaining members, in publish order. Order across a membership change
//     is best effort (see "Stage 4 - discuss").
//   - Shutdown(ctx) stops intake, then waits until every accepted message has
//     been received and settled (acked or dead-lettered) or ctx is done,
//     whichever comes first, and closes all subscriptions. On ctx expiry it
//     force-closes them and reports how many messages were abandoned.

// WithGroup makes the subscription a member of the consumer group `name`.
func WithGroup(name string) SubOption {
	return func(c *subConfig) error {
		if name == "" {
			return fmt.Errorf("%w: group name must not be empty", ErrInvalidOption)
		}
		c.group = name
		return nil
	}
}

type group struct {
	name    string
	members []*Subscription // guarded by Broker.mu
	rr      atomic.Uint64   // round-robin cursor for unkeyed messages
}

func (t *topic) findGroup(name string) *group {
	for _, g := range t.groups {
		if g.name == name {
			return g
		}
	}
	return nil
}

func (t *topic) groupFor(name string) *group {
	if g := t.findGroup(name); g != nil {
		return g
	}
	g := &group{name: name}
	t.groups = append(t.groups, g)
	return g
}

func removeGroup(list []*group, g *group) []*group {
	if i := slices.Index(list, g); i >= 0 {
		return slices.Delete(list, i, i+1)
	}
	return list
}

// offerGroups is the stage 3 half of topic.offer: every group gets the
// message once, at the member chosen below.
func (t *topic) offerGroups(m Message, full []*Subscription) []*Subscription {
	for _, g := range t.groups {
		if len(g.members) > 0 {
			if s := g.offer(m); s != nil {
				full = append(full, s)
			}
		}
	}
	return full
}

// offer hands m to ONE member of the group, without blocking. It returns the
// member to wait for if its queue was full under policy Block.
//
// A keyed message has exactly one possible member, so that member's policy
// applies. An unkeyed message tries the members round robin and takes the
// first with room: only when all of them are full does the policy of the
// round-robin cursor's member decide. (A stuck member therefore costs the
// group nothing while any other member has room.)
func (g *group) offer(m Message) *Subscription {
	n := len(g.members)
	if m.Key != "" {
		if s := g.members[hashKey(m.Key)%uint32(n)]; s.tryOffer(m) == offerFull {
			return s
		}
		return nil
	}
	start := int(g.rr.Add(1) % uint64(n))
	for i := 0; i < n; i++ {
		if g.members[(start+i)%n].tryAccept(m) {
			return nil
		}
	}
	if s := g.members[start]; s.tryOffer(m) == offerFull {
		return s
	}
	return nil
}

// route picks the member that should adopt a message handed over by a
// departed member; capacity is irrelevant there. Requires Broker.mu.
func (g *group) route(key string) *Subscription {
	n := len(g.members)
	if key != "" {
		return g.members[hashKey(key)%uint32(n)]
	}
	return g.members[int(g.rr.Add(1)%uint64(n))]
}

// reroute re-offers m to the groups of members that left while a publisher
// was waiting for them, so the message is not lost with them.
func (b *Broker) reroute(m Message, gone []*Subscription) (full []*Subscription) {
	if len(gone) == 0 {
		return nil
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	t := b.topics[m.Topic]
	if t == nil {
		return nil
	}
	done := map[string]bool{}
	for _, s := range gone {
		if done[s.cfg.group] {
			continue
		}
		done[s.cfg.group] = true
		if g := t.findGroup(s.cfg.group); g != nil && len(g.members) > 0 {
			if f := g.offer(m); f != nil {
				full = append(full, f)
			}
		}
	}
	return full
}

// hashKey is FNV-1a, inlined so routing allocates nothing.
func hashKey(k string) uint32 {
	h := uint32(2166136261)
	for i := 0; i < len(k); i++ {
		h ^= uint32(k[i])
		h *= 16777619
	}
	return h
}

// holdBackLocked implements "one in flight per key". It reports true when m
// was parked behind the in-flight message with the same key; otherwise it
// makes m the key's in-flight message.
func (s *Subscription) holdBackLocked(m Message) bool {
	if m.Key == "" {
		return false
	}
	if _, busy := s.holder[m.Key]; busy {
		s.parked[m.Key] = append(s.parked[m.Key], m)
		s.parkedN++
		return true
	}
	s.holder[m.Key] = m.ID
	return false
}

// releaseKeyLocked is called when the in-flight message of m.Key is settled:
// the next held-back message for the key becomes in flight. Occupancy does
// not change (parked -> queued), so there is always room.
func (s *Subscription) releaseKeyLocked(m Message) {
	if m.Key == "" || s.holder[m.Key] != m.ID {
		return
	}
	delete(s.holder, m.Key)
	q := s.parked[m.Key]
	if len(q) == 0 {
		delete(s.parked, m.Key)
		return
	}
	next := q[0]
	q[0] = Message{}
	if len(q) == 1 {
		delete(s.parked, m.Key)
	} else {
		s.parked[m.Key] = q[1:]
	}
	s.parkedN--
	s.admitLocked(next)
}

// owedLocked lists what a leaving group member still owes, in publish order.
func (s *Subscription) owedLocked() []Message {
	if s.cfg.group == "" || s.cfg.ackTimeout == 0 {
		return nil
	}
	owed := make([]Message, 0, len(s.pending)+s.parkedN)
	for _, p := range s.pending {
		m := p.msg
		if p.offered {
			m.Attempt++ // the consumer may already have had an attempt; the next one is a redelivery
		}
		owed = append(owed, m)
	}
	for _, held := range s.parked {
		owed = append(owed, held...)
	}
	slices.SortFunc(owed, func(a, b Message) int { return cmp.Compare(a.seq, b.seq) })
	return owed
}

// rehome hands msgs, owed by the departed member `from`, to the rest of its
// group. Requires b.mu for writing, which is also what keeps publishers from
// interleaving newer messages for the same keys.
func (b *Broker) rehome(from *Subscription, msgs []Message) {
	if len(msgs) == 0 {
		return
	}
	var g *group
	if t := b.topics[from.topic]; t != nil {
		g = t.findGroup(from.cfg.group)
	}
	if g == nil || len(g.members) == 0 || b.closed.Load() {
		b.stats.orphaned.Add(uint64(len(msgs)))
		return
	}
	for _, m := range msgs {
		g.route(m.Key).adopt(m)
	}
}

// adopt accepts a message handed over by a departed member. Capacity is
// deliberately not checked: refusing would lose a message the broker owes.
func (s *Subscription) adopt(m Message) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closing {
		s.b.stats.orphaned.Add(1)
		return
	}
	s.admitLocked(m)
}

// outstandingLocked is the number of accepted messages not yet settled: for
// an ack subscription everything pending or held back, otherwise everything
// still waiting for the consumer.
func (s *Subscription) outstandingLocked() int {
	if s.cfg.ackTimeout != 0 {
		return len(s.pending) + s.parkedN
	}
	return len(s.q)
}

// awaitSettled blocks until s has nothing outstanding or ctx is done. It
// returns how many messages were still outstanding.
func (s *Subscription) awaitSettled(ctx context.Context) (int, error) {
	for {
		s.mu.Lock()
		n := s.outstandingLocked()
		sp := s.spaceLocked()
		s.mu.Unlock()
		if n == 0 {
			return 0, nil
		}
		select {
		case <-sp:
		case <-ctx.Done():
			return n, ctx.Err()
		case <-s.finished:
			return 0, nil
		}
	}
}

// Shutdown closes the broker gracefully: Publish and Subscribe fail from now
// on; redelivery keeps running; Shutdown returns once every accepted message
// has been received and acked (or dead-lettered) and all subscriptions are
// closed. If ctx ends first, it force-closes everything and returns an error
// wrapping ctx.Err() that says how many messages were abandoned.
func (b *Broker) Shutdown(ctx context.Context) error {
	b.mu.Lock()
	b.closed.Store(true)
	subs := b.allSubs()
	b.mu.Unlock()

	left := 0
	var cause error
	for _, s := range subs {
		n, err := s.awaitSettled(ctx)
		left += n
		if err != nil && cause == nil {
			cause = err
		}
	}
	if cause == nil {
		for _, s := range subs {
			s.closeIntake()
		}
		for _, s := range subs {
			select {
			case <-s.finished:
			case <-ctx.Done():
				cause = ctx.Err()
			}
		}
	}
	if cause != nil {
		for _, s := range subs {
			s.Unsubscribe()
		}
		return fmt.Errorf("pubsub: shutdown gave up with %d unsettled messages: %w", left, cause)
	}
	return nil
}
