// Package pubsub is an in-process publish/subscribe broker with bounded
// per-subscriber queues, acknowledgements with redelivery (at-least-once),
// dead-lettering and consumer groups.
//
// Lab B3 Problem 5 (90-minute box). Stage 1 is this file; stage 2 (acks,
// redelivery, dead letters) lives in stage2.go and stage 3 (consumer groups,
// per-key ordering, graceful Shutdown) in stage3.go. Treat the later files as
// sealed until the interviewer "announces" them (see
// unit-3-messaging-and-scheduling.md, "Pub-sub broker with acks and
// redelivery").
//
// Stage 1 contract:
//   - Broker, Subscribe, Publish, Close and every Subscription method are safe
//     for concurrent use.
//   - Publish(ctx, topic, msg) delivers msg to EVERY current subscriber of
//     topic and returns the ID the broker assigned (unique per Publish, the
//     same for every subscriber). The broker also sets Topic and Attempt=1.
//     A topic without subscribers just discards the message.
//   - Each subscription has a bounded queue (WithBuffer, default 16) and an
//     explicit slow-consumer policy (WithPolicy, default Block):
//     Block      Publish waits (honouring ctx) until this subscriber has room;
//     DropNewest the new message is not delivered to this subscriber and
//     Dropped() counts it;
//     DropOldest the oldest waiting message is evicted to make room and
//     Dropped() counts it.
//   - Order: one publisher's messages arrive at each subscriber in the order
//     they were published.
//   - Isolation: a stuck subscriber must not stall the others. With Block, the
//     publisher is held up until ctx ends, but every subscriber that had room
//     already has the message, and Subscribe/Unsubscribe/Close of OTHER
//     subscriptions keep working. Several full Block subscribers are waited
//     for together, not one after another.
//   - Publish is not atomic across subscribers: if it returns an error, the
//     subscribers that had room have the message.
//   - Unsubscribe stops delivery and closes C(); queued messages may be
//     discarded. It releases publishers blocked on that subscription (their
//     Publish still succeeds) and is idempotent.
//   - Close stops intake (Publish/Subscribe return ErrClosed), releases blocked
//     publishers with ErrClosed, lets each consumer receive what is already
//     queued for it, then closes every C() exactly once. Close never waits for
//     a consumer, is idempotent and safe to call concurrently with everything.
//   - Nothing may panic (no send on a closed channel, no double close) and no
//     goroutine may outlive Unsubscribe/Close+drain.
//   - Len() is the number of accepted messages not yet received by the
//     consumer. It may lag the consumer's receive by a few microseconds; the
//     tests poll for it where that matters.
package pubsub

import (
	"context"
	"errors"
)

var (
	// ErrClosed is returned when the broker (or the subscription) is closed.
	ErrClosed = errors.New("pubsub: closed")
	// ErrInvalidOption is returned for a bad or contradictory option.
	ErrInvalidOption = errors.New("pubsub: invalid option")
	// ErrInvalidTopic is returned for an empty topic name.
	ErrInvalidTopic = errors.New("pubsub: topic must not be empty")
)

var errNotImplemented = errors.New("pubsub: not implemented")

// Message is what publishers send and consumers receive. Payload is shared by
// every subscriber and never copied: treat it as immutable.
type Message struct {
	ID      string // assigned by the broker; identical for every subscriber and every redelivery
	Topic   string // set by the broker
	Key     string // optional ordering key (stage 3)
	Payload any
	Attempt int    // 1 on first delivery; incremented by every redelivery (stage 2)
	Origin  string // dead-letter copies: the topic the message failed on (stage 2)

	// TODO(lab): add unexported fields if you need them (a publish sequence
	// number is handy for ordering and tie-breaks).
}

// OverflowPolicy says what Publish does when a subscriber's queue is full.
type OverflowPolicy int

// Policies. The zero value is Block: the safe default never loses data
// silently.
const (
	Block OverflowPolicy = iota
	DropNewest
	DropOldest
)

// subConfig is filled in by SubOptions. Stage 2 and 3 add options that set
// more fields.
type subConfig struct {
	// TODO(lab): buffer size, policy, and (later stages) ack timeout, max
	// attempts, dead-letter topic and group name.
}

// SubOption customises a subscription. It reports ErrInvalidOption (wrapped)
// for a bad argument.
type SubOption func(*subConfig) error

// WithBuffer sets how many messages may wait for the consumer (default 16,
// must be >= 1).
func WithBuffer(n int) SubOption {
	return func(c *subConfig) error {
		// TODO(lab)
		_ = n
		return nil
	}
}

// WithPolicy sets the slow-consumer policy (default Block).
func WithPolicy(p OverflowPolicy) SubOption {
	return func(c *subConfig) error {
		// TODO(lab)
		_ = p
		return nil
	}
}

// Option customises a Broker. Stage 2 adds WithClock.
type Option func(*Broker)

// Broker routes published messages to subscriptions.
type Broker struct {
	// TODO(lab): fields. Think about: who owns each subscriber's channel
	// (exactly one goroutine should send on it AND close it), what lock
	// protects the topic table, and what Publish may hold while it waits.
}

// New creates a broker.
func New(opts ...Option) (*Broker, error) {
	// TODO(lab)
	_ = opts
	return nil, errNotImplemented
}

// Subscribe registers a subscription on topic. The consumer reads from
// Subscription.C. Always call Unsubscribe (or Close the broker and drain C)
// when done.
func (b *Broker) Subscribe(topic string, opts ...SubOption) (*Subscription, error) {
	// TODO(lab)
	_, _ = topic, opts
	return nil, errNotImplemented
}

// Publish delivers msg to every subscription of topic and returns the ID the
// broker assigned.
func (b *Broker) Publish(ctx context.Context, topic string, msg Message) (string, error) {
	// TODO(lab)
	_, _, _ = ctx, topic, msg
	return "", errNotImplemented
}

// Close stops intake and lets every subscription drain, then closes every
// subscriber channel exactly once. Idempotent.
func (b *Broker) Close() error {
	// TODO(lab)
	return errNotImplemented
}

// Subscription is one consumer's view of a topic.
type Subscription struct {
	// TODO(lab): fields.
}

// C returns the channel messages arrive on. It is closed after Unsubscribe,
// or after Close once the queue has been drained. Consumers never close it.
func (s *Subscription) C() <-chan Message {
	// TODO(lab)
	return nil
}

// Len returns how many accepted messages are still waiting for the consumer.
func (s *Subscription) Len() int {
	// TODO(lab)
	return 0
}

// Dropped returns how many messages the overflow policy has discarded for
// this subscription.
func (s *Subscription) Dropped() uint64 {
	// TODO(lab)
	return 0
}

// Unsubscribe stops delivery and closes C. When it returns, no further
// message will arrive. Idempotent.
func (s *Subscription) Unsubscribe() {
	// TODO(lab)
}
