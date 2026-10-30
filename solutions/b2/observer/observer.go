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

import (
	"errors"
	"fmt"
	"sync"
)

// Event is what publishers emit.
type Event struct {
	Topic string
	Key   string // e.g. a transfer id
	Data  string
}

type sub struct {
	id uint64
	fn func(Event)
}

// Bus delivers events to subscribers of a topic. Safe for concurrent use.
type Bus struct {
	mu   sync.Mutex
	next uint64
	subs map[string][]*sub
}

// NewBus returns an empty bus.
func NewBus() *Bus { return &Bus{subs: map[string][]*sub{}} }

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
	b.mu.Lock()
	b.next++
	s := &sub{id: b.next, fn: fn}
	b.subs[topic] = append(b.subs[topic], s)
	b.mu.Unlock()

	return func() {
		b.mu.Lock()
		defer b.mu.Unlock()
		list := b.subs[topic]
		for i, x := range list {
			if x.id == s.id {
				// copy-on-write: a Publish may be iterating the old slice
				nl := make([]*sub, 0, len(list)-1)
				nl = append(nl, list[:i]...)
				nl = append(nl, list[i+1:]...)
				b.subs[topic] = nl
				return
			}
		}
	}
}

// Publish delivers e to every subscriber of e.Topic that was subscribed when
// Publish started (subscribers added by a callback see the next event, not
// this one). A subscriber that panics is recovered; the remaining subscribers
// still run, and Publish returns the panics as one joined error (nil if none).
func (b *Bus) Publish(e Event) error {
	b.mu.Lock()
	snapshot := b.subs[e.Topic] // slices are never mutated in place, so this is safe to read unlocked
	b.mu.Unlock()

	var errs []error
	for _, s := range snapshot {
		if err := call(s.fn, e); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func call(fn func(Event), e Event) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("observer: subscriber for %q panicked: %v", e.Topic, r)
		}
	}()
	fn(e)
	return nil
}

// Subscription is a channel-based subscription. Events arrive on C. If the
// consumer is slower than the publisher the newest events are dropped (and
// counted) rather than blocking Publish.
type Subscription struct {
	// C delivers events; it is closed by Cancel.
	C <-chan Event

	mu      sync.Mutex
	ch      chan Event
	closed  bool
	dropped int
	unsub   func()
}

// SubscribeChan subscribes with a buffered channel of size buf (values below
// 1 are treated as 1: an unbuffered channel would drop everything unless the
// consumer happened to be blocked in a receive).
func (b *Bus) SubscribeChan(topic string, buf int) *Subscription {
	if buf < 1 {
		buf = 1
	}
	s := &Subscription{ch: make(chan Event, buf)}
	s.C = s.ch
	s.unsub = b.Subscribe(topic, s.deliver)
	return s
}

func (s *Subscription) deliver(e Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	select {
	case s.ch <- e:
	default:
		s.dropped++
	}
}

// Dropped reports how many events were discarded because the buffer was full.
func (s *Subscription) Dropped() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.dropped
}

// Cancel unsubscribes and closes C exactly once. It is safe to call more than
// once and concurrently with Publish (a send racing the close would panic if
// delivery and close were not serialised).
func (s *Subscription) Cancel() {
	s.unsub()
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.closed {
		s.closed = true
		close(s.ch)
	}
}
