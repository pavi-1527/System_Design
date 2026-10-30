// Package observer is an in-process event bus: the observer pattern in the
// two forms Go programs actually use, callbacks and channels.
//
// Lab B2 Unit 1 §2. When a transfer settles, the audit log, the notifier and
// the fraud scorer all want to know. The transfer code must not know about any
// of them. The traps are all about what happens while a callback runs: a bus
// that holds its lock while calling subscribers deadlocks the moment a
// subscriber subscribes, unsubscribes or publishes; a subscriber that panics
// must not take the others (or the publisher) down; a slow channel subscriber
// must not stall the publisher.
package observer

import "errors"

var errNotImplemented = errors.New("observer: not implemented")

// Event is what publishers emit.
type Event struct {
	Topic string
	Key   string // e.g. a transfer id
	Data  string
}

// Bus delivers events to subscribers of a topic. Safe for concurrent use.
type Bus struct {
	// TODO(lab): a mutex and topic -> ordered subscribers. Give every
	// subscriber an id so unsubscribe does not depend on slice positions.
}

// NewBus returns an empty bus.
func NewBus() *Bus {
	// TODO(lab)
	return &Bus{}
}

// Subscribe registers fn for topic and returns an unsubscribe func.
//
// Contract:
//   - Subscribers of a topic are called in subscription order, synchronously,
//     on the publisher's goroutine.
//   - unsubscribe is idempotent, and calling it twice never removes another
//     subscriber. An unsubscribe that returns before a Publish starts is
//     respected by that Publish; a Publish already in flight may still deliver
//     its current event.
//   - fn runs with NO bus lock held, so it may call Subscribe, unsubscribe and
//     Publish itself.
func (b *Bus) Subscribe(topic string, fn func(Event)) (unsubscribe func()) {
	// TODO(lab)
	_, _ = topic, fn
	return func() {}
}

// Publish delivers e to every subscriber of e.Topic that was subscribed when
// Publish started (subscribers added by a callback see the next event, not
// this one). A subscriber that panics is recovered; the remaining subscribers
// still run, and Publish returns the panics as one joined error (nil if none).
//
// Hint: snapshot the subscriber list under the lock, release the lock, then
// call. Never mutate a slice a Publish may be iterating (copy on write).
func (b *Bus) Publish(e Event) error {
	// TODO(lab)
	_ = e
	return errNotImplemented
}

// Subscription is a channel-based subscription. Events arrive on C. If the
// consumer is slower than the publisher the newest events are dropped (and
// counted) rather than blocking Publish.
type Subscription struct {
	// C delivers events; it is closed by Cancel.
	C <-chan Event

	// TODO(lab): the writable channel, a mutex that serialises sends with the
	// close, a closed flag, a drop counter and the unsubscribe func.
}

// SubscribeChan subscribes with a buffered channel of size buf (values below
// 1 are treated as 1: an unbuffered channel would drop everything unless the
// consumer happened to be blocked in a receive).
func (b *Bus) SubscribeChan(topic string, buf int) *Subscription {
	// TODO(lab)
	_, _ = topic, buf
	closed := make(chan Event)
	close(closed) // stub: ranging over C ends at once instead of hanging on a nil channel
	return &Subscription{C: closed}
}

// Dropped reports how many events were discarded because the buffer was full.
func (s *Subscription) Dropped() int {
	// TODO(lab)
	return 0
}

// Cancel unsubscribes and closes C exactly once. It is safe to call more than
// once and concurrently with Publish (a send racing the close would panic if
// delivery and close were not serialised).
func (s *Subscription) Cancel() {
	// TODO(lab)
}
