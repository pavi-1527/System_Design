package pubsub

import (
	"errors"
	"time"
)

// ---- Stage 2: acks, redelivery, dead letters (announced at about minute 30) --
//
// Contract:
//   - WithAckTimeout(d) puts a subscription in ack mode. Every message the
//     consumer receives must be settled with Ack(id) or Nack(id) on that
//     Subscription. Settling is by message ID, whichever attempt the consumer
//     happens to hold.
//   - The ack clock for an attempt starts when the consumer RECEIVES the
//     message from C(), not when it was published or queued. A message that
//     waited 8 s in the queue still gets its full timeout. (Hint: how can the
//     broker know when a plain channel receive happened?)
//   - Unacked after the timeout (or Nacked): the message is redelivered on the
//     same subscription with Attempt+1 and the SAME ID. After WithMaxAttempts
//     attempts (default 5) it is dead-lettered instead: copied to the topic
//     named by WithDeadLetter (Message.Origin = the original topic, Topic =
//     the dead-letter topic, Attempt = 1) and forgotten. Without
//     WithDeadLetter it is only counted in Stats().DeadLettered.
//   - This is at-least-once. Duplicates are expected (late ack after a
//     redelivery, lost ack, crash after processing). A consumer that must not
//     repeat a side effect keeps the IDs it has handled and skips repeats.
//   - Ack/Nack on an ID that is not awaiting an ack return ErrNotPending (a
//     benign answer for a duplicate). A late Ack, after a redelivery was
//     queued, still settles the message and cancels the queued copy. Ack and
//     Nack work even if called the instant the consumer has the message.
//   - Redelivery never blocks the clock: if the subscriber's queue is full,
//     the redelivery is deferred by one more ack timeout without consuming an
//     attempt.
//   - ONE timer serves the whole broker (a min-heap on deadlines), however
//     many messages are in flight; no goroutine per message. A test checks
//     that exactly one timer is armed for thousands of in-flight messages.
//   - DropOldest cannot be combined with acks (Subscribe returns
//     ErrInvalidOption): evicting a message the broker already accepted would
//     break at-least-once. WithMaxAttempts/WithDeadLetter without
//     WithAckTimeout, and a dead-letter topic equal to the topic itself, are
//     ErrInvalidOption too.
//   - Time comes from the injected Clock so tests never sleep.

var (
	// ErrNotPending is returned by Ack/Nack for an ID that is not awaiting an ack.
	ErrNotPending = errors.New("pubsub: no such message awaiting an ack")
	// ErrAckDisabled is returned by Ack/Nack on a subscription without WithAckTimeout.
	ErrAckDisabled = errors.New("pubsub: subscription has no ack timeout")
)

// Clock is all the broker needs from time. AfterFunc runs f once after d, on
// a goroutine of the clock's choosing, and returns a func that cancels it
// (reporting whether it cancelled before f started). A fake clock may run f
// synchronously inside its Advance method.
type Clock interface {
	Now() time.Time
	AfterFunc(d time.Duration, f func()) (stop func() bool)
}

// WithClock sets the broker's clock (default: the system clock). New must
// reject a nil clock with ErrInvalidOption.
func WithClock(c Clock) Option {
	return func(b *Broker) {
		// TODO(lab)
		_ = c
	}
}

// WithAckTimeout turns on acks: a received message must be settled within d.
func WithAckTimeout(d time.Duration) SubOption {
	return func(c *subConfig) error {
		// TODO(lab)
		_ = d
		return nil
	}
}

// WithMaxAttempts bounds deliveries per message (default 5, must be >= 1).
// Needs WithAckTimeout.
func WithMaxAttempts(n int) SubOption {
	return func(c *subConfig) error {
		// TODO(lab)
		_ = n
		return nil
	}
}

// WithDeadLetter names the topic that receives messages which ran out of
// attempts. Needs WithAckTimeout. Subscribe to that topic BEFORE the failures
// happen: a topic nobody listens to discards what it is sent.
func WithDeadLetter(topic string) SubOption {
	return func(c *subConfig) error {
		// TODO(lab)
		_ = topic
		return nil
	}
}

// Stats are broker-wide counters. The tests read Redelivered, DeadLettered and
// Orphaned; the rest are suggestions.
type Stats struct {
	Published      uint64 // Publish calls accepted
	Redelivered    uint64 // attempts beyond the first, queued for a consumer
	DeadLettered   uint64 // messages that ran out of attempts
	DeadLetterLost uint64 // dead letters a Block-policy dead-letter subscriber had no room for
	Orphaned       uint64 // messages a leaving group member owed with nobody left to give them to (stage 3)
}

// Stats returns a snapshot of the counters.
func (b *Broker) Stats() Stats {
	// TODO(lab)
	return Stats{}
}

// InFlight returns how many messages the consumer holds unsettled (received,
// ack timer running).
func (s *Subscription) InFlight() int {
	// TODO(lab)
	return 0
}

// Ack settles the message with this ID: it will not be redelivered, and a copy
// already queued for redelivery is cancelled. ErrNotPending if the ID is not
// awaiting an ack; ErrAckDisabled without WithAckTimeout; ErrClosed on a
// finished subscription.
func (s *Subscription) Ack(id string) error {
	// TODO(lab)
	_ = id
	return errNotImplemented
}

// Nack says "I could not process this": the message is redelivered at once
// (Attempt+1), or dead-lettered if it has used up its attempts. Same errors
// as Ack.
func (s *Subscription) Nack(id string) error {
	// TODO(lab)
	_ = id
	return errNotImplemented
}
