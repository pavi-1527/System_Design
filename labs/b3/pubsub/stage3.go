package pubsub

import "context"

// ---- Stage 3: consumer groups, per-key ordering, graceful shutdown (minute 55) --
//
// Contract:
//   - WithGroup(name) makes a subscription a member of the group `name` on its
//     topic. Every group receives every message; within a group each message
//     goes to exactly ONE member (competing consumers). Subscriptions without
//     a group each receive every message, as in stage 1.
//   - A message with a Key always goes to the same member of a group while the
//     membership is unchanged (for example hash(key) mod members). Messages
//     without a key go to a member with room (round robin is fine); a full or
//     stuck member must not make the group refuse a message while another
//     member has room.
//   - In ack mode, at most ONE message per key is in flight on a subscription
//     at a time (keyed or not, grouped or not). Later messages with that key
//     are held back until the earlier one is acked or dead-lettered. Held-back
//     messages still count toward the subscription's bound (Len, WithBuffer,
//     overflow policy). This keeps per-key order intact across redelivery:
//     m1's redelivery comes before m2's first delivery. Messages with
//     different keys, and messages without a key, do not wait for each other.
//   - When a member leaves (Unsubscribe) in ack mode, every message it still
//     owed an ack for, and every message held back for it, goes to the
//     remaining members, in publish order (capacity is not checked: refusing
//     would lose a message the broker owes). With no member left they are
//     counted in Stats().Orphaned. Order across a membership change is best
//     effort (see "Stage 4 - discuss").
//   - A Block publisher that is waiting on a group member which then leaves
//     must not lose the message: it is re-offered to the group.
//   - Shutdown(ctx) stops intake (Publish/Subscribe return ErrClosed), then
//     waits until every accepted message has been received and settled (acked
//     or dead-lettered; plain subscriptions: received) or ctx is done,
//     whichever comes first, while acks, nacks and redeliveries keep working.
//     It then closes all subscriptions. On ctx expiry it force-closes them and
//     returns an error wrapping ctx.Err() that says how many messages were
//     abandoned.

// WithGroup makes the subscription a member of the consumer group `name`
// (must not be empty).
func WithGroup(name string) SubOption {
	return func(c *subConfig) error {
		// TODO(lab)
		_ = name
		return nil
	}
}

// Shutdown closes the broker gracefully: Publish and Subscribe fail from now
// on; redelivery keeps running; Shutdown returns once every accepted message
// has been settled and all subscriptions are closed. If ctx ends first, it
// force-closes everything and returns an error wrapping ctx.Err().
func (b *Broker) Shutdown(ctx context.Context) error {
	// TODO(lab)
	_ = ctx
	return errNotImplemented
}
