// Package pubsub is an in-process publish/subscribe broker with bounded
// per-subscriber queues, acknowledgements with redelivery (at-least-once),
// dead-lettering and consumer groups.
//
// Lab B3 Problem 5 (90-minute box). Stage 1 is this file (topics, fan-out,
// bounded queues with an explicit slow-consumer policy, Close/Unsubscribe).
// Stage 2 (acks, redelivery, dead letters) lives in stage2.go and stage 3
// (consumer groups, per-key ordering, graceful Shutdown) in stage3.go.
//
// Reference design, in one paragraph: every Subscription owns ONE goroutine,
// the pump. The pump is the only sender on, and the only closer of, the
// consumer-facing channel, so "send on closed channel" cannot happen by
// construction. Publishers never touch that channel: they append to the
// subscription's bounded queue under its mutex and wake the pump. Because
// the pump is also the only thing that sees the consumer actually receive a
// message, it is the right place to start the ack timer. Publish takes the
// broker's read lock only for a non-blocking first pass over the target
// subscriptions; publishers that must wait (policy Block, queue full) wait
// outside every broker lock, so a stuck subscriber can stall neither other
// subscribers nor Subscribe/Unsubscribe.
//
// Lock order (always outer to inner): Broker.mu, Subscription.mu, sched.mu.
// No lock is ever held while sending on a channel, waiting on a context, or
// calling user code.
package pubsub

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

var (
	// ErrClosed is returned when the broker (or the subscription) is closed.
	ErrClosed = errors.New("pubsub: closed")
	// ErrInvalidOption is returned for a bad or contradictory option.
	ErrInvalidOption = errors.New("pubsub: invalid option")
	// ErrInvalidTopic is returned for an empty topic name.
	ErrInvalidTopic = errors.New("pubsub: topic must not be empty")
	// errGone is internal: this one subscription went away while a publisher
	// was waiting for it. It is not a Publish error.
	errGone = errors.New("pubsub: subscription gone")
)

const defaultBuffer = 16

// Message is what publishers send and consumers receive. Payload is shared
// by every subscriber and never copied: treat it as immutable.
type Message struct {
	ID      string // assigned by the broker; identical for every subscriber and every redelivery
	Topic   string // set by the broker
	Key     string // optional ordering key (stage 3)
	Payload any
	Attempt int    // 1 on first delivery; incremented by every redelivery (stage 2)
	Origin  string // dead-letter copies: the topic the message failed on

	seq uint64 // broker-wide publish order; internal
}

// OverflowPolicy says what Publish does when a subscriber's queue is full.
type OverflowPolicy int

// Policies. The zero value is Block: the safe default never loses data
// silently.
const (
	// Block makes the publisher wait (honouring its context) until this
	// subscriber has room. Other subscribers still receive the message.
	Block OverflowPolicy = iota
	// DropNewest rejects the new message for this subscriber and counts it.
	DropNewest
	// DropOldest evicts the oldest waiting message to make room and counts
	// it. Not allowed together with acks: evicting a message the broker
	// already accepted would break at-least-once.
	DropOldest
)

// subConfig is filled in by SubOptions. Stage 2 and 3 add options that set
// the later fields.
type subConfig struct {
	buffer int
	policy OverflowPolicy

	ackTimeout  time.Duration // stage 2: 0 = no acks
	maxAttempts int
	deadLetter  string

	group string // stage 3
}

// SubOption customises a subscription.
type SubOption func(*subConfig) error

// WithBuffer sets how many messages may wait for the consumer (default 16).
func WithBuffer(n int) SubOption {
	return func(c *subConfig) error {
		if n < 1 {
			return fmt.Errorf("%w: buffer must be >= 1", ErrInvalidOption)
		}
		c.buffer = n
		return nil
	}
}

// WithPolicy sets the slow-consumer policy (default Block).
func WithPolicy(p OverflowPolicy) SubOption {
	return func(c *subConfig) error {
		if p < Block || p > DropOldest {
			return fmt.Errorf("%w: unknown overflow policy %d", ErrInvalidOption, p)
		}
		c.policy = p
		return nil
	}
}

// Option customises a Broker.
type Option func(*Broker)

// Broker routes published messages to subscriptions. Safe for concurrent use.
type Broker struct {
	mu     sync.RWMutex // guards topics (and group membership); see lock order above
	topics map[string]*topic

	closed atomic.Bool
	seq    atomic.Uint64

	clock Clock  // stage 2
	sched *sched // stage 2
	stats counters
}

type topic struct {
	subs   []*Subscription // ungrouped subscriptions
	groups []*group        // stage 3, in creation order
}

// New creates a broker.
func New(opts ...Option) (*Broker, error) {
	b := &Broker{topics: make(map[string]*topic), clock: systemClock{}}
	for _, o := range opts {
		o(b)
	}
	if b.clock == nil {
		return nil, fmt.Errorf("%w: nil clock", ErrInvalidOption)
	}
	b.sched = newSched(b.clock)
	return b, nil
}

// Subscribe registers a subscription on topic. The consumer reads from
// Subscription.C. Always call Unsubscribe (or Close the broker and drain C)
// when done: the subscription owns a goroutine until then.
func (b *Broker) Subscribe(topic string, opts ...SubOption) (*Subscription, error) {
	if topic == "" {
		return nil, ErrInvalidTopic
	}
	cfg := subConfig{buffer: defaultBuffer, policy: Block}
	for _, o := range opts {
		if err := o(&cfg); err != nil {
			return nil, err
		}
	}
	if err := cfg.validate(topic); err != nil {
		return nil, err
	}
	s := newSubscription(b, topic, cfg)
	b.mu.Lock()
	if b.closed.Load() {
		b.mu.Unlock()
		return nil, ErrClosed
	}
	b.register(s)
	b.mu.Unlock()
	go s.run()
	return s, nil
}

// Publish delivers msg to every subscription of topic and returns the ID the
// broker assigned. Each subscriber's overflow policy decides what happens if
// its queue is full; only policy Block can make Publish wait, and then only
// until ctx is done.
//
// Publish is NOT atomic across subscribers: if it returns an error, the
// subscribers that had room already have the message. A topic without
// subscribers silently discards the message (pub-sub keeps no history).
func (b *Broker) Publish(ctx context.Context, topic string, msg Message) (string, error) {
	if topic == "" {
		return "", ErrInvalidTopic
	}
	if b.closed.Load() {
		return "", ErrClosed
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	seq := b.seq.Add(1)
	msg.seq, msg.ID, msg.Topic, msg.Attempt, msg.Origin = seq, "m-"+strconv.FormatUint(seq, 10), topic, 1, ""
	b.stats.published.Add(1)

	// Pass 1, under the read lock and never blocking: everyone with room (or
	// a drop policy) is served immediately, so a stuck subscriber cannot
	// delay the others.
	var full []*Subscription
	b.mu.RLock()
	if t := b.topics[topic]; t != nil {
		full = t.offer(msg)
	}
	b.mu.RUnlock()

	// Pass 2, with no lock held: wait for the Block subscribers whose queues
	// were full, concurrently so that one stuck subscriber does not starve a
	// slow-but-alive one of the time left on ctx.
	return msg.ID, b.waitAll(ctx, msg, full)
}

// waitAll waits, with no lock held, for every subscription in subs to make
// room for m. It returns the first real error (a done ctx, a closed broker).
func (b *Broker) waitAll(ctx context.Context, m Message, subs []*Subscription) error {
	for len(subs) > 0 {
		errs := make([]error, len(subs))
		if len(subs) == 1 {
			errs[0] = subs[0].waitOffer(ctx, m)
		} else {
			// Concurrently, so that one dead subscriber does not eat the time
			// a slow-but-alive one needs. Bounded: every waiter returns when
			// ctx is done or its subscription closes.
			var wg sync.WaitGroup
			for i, s := range subs {
				wg.Add(1)
				go func() {
					defer wg.Done()
					errs[i] = s.waitOffer(ctx, m)
				}()
			}
			wg.Wait()
		}
		var vanished []*Subscription
		for i, err := range errs {
			switch {
			case errors.Is(err, errGone):
				if subs[i].cfg.group != "" {
					vanished = append(vanished, subs[i]) // stage 3: a group member left while we waited
				}
			case err != nil:
				return err
			}
		}
		subs = b.reroute(m, vanished)
	}
	return nil
}

// Close stops intake (Publish and Subscribe return ErrClosed) and lets every
// subscription drain: each consumer still receives what was already queued
// for it, then its channel is closed, exactly once. Close never waits for a
// consumer; a consumer that stops reading must call Unsubscribe. It is safe
// to call Close more than once and concurrently. For a bounded wait that
// also settles unacked messages, see Shutdown (stage 3).
func (b *Broker) Close() error {
	b.mu.Lock()
	b.closed.Store(true)
	subs := b.allSubs()
	b.mu.Unlock()
	for _, s := range subs {
		s.closeIntake()
	}
	return nil
}

// allSubs requires b.mu.
func (b *Broker) allSubs() []*Subscription {
	var all []*Subscription
	for _, t := range b.topics {
		all = append(all, t.subs...)
		for _, g := range t.groups {
			all = append(all, g.members...)
		}
	}
	return all
}

// offer is publish pass 1 for one topic: hand m to every subscription that
// should get it, without ever blocking. It returns the Block subscriptions
// that were full and must be waited for. Requires b.mu (read or write).
func (t *topic) offer(m Message) (full []*Subscription) {
	for _, s := range t.subs {
		if s.tryOffer(m) == offerFull {
			full = append(full, s)
		}
	}
	return t.offerGroups(m, full) // stage 3 seam
}

// register adds s to its topic (requires b.mu held for writing).
func (b *Broker) register(s *Subscription) {
	t := b.topics[s.topic]
	if t == nil {
		t = &topic{}
		b.topics[s.topic] = t
	}
	if s.cfg.group != "" { // stage 3 seam
		g := t.groupFor(s.cfg.group)
		g.members = append(g.members, s)
		return
	}
	t.subs = append(t.subs, s)
}

// unregister removes s from its topic. Idempotent. Requires b.mu for writing.
func (b *Broker) unregister(s *Subscription) {
	t := b.topics[s.topic]
	if t == nil {
		return
	}
	if s.cfg.group != "" { // stage 3 seam
		if g := t.findGroup(s.cfg.group); g != nil {
			g.members = removeSub(g.members, s)
			if len(g.members) == 0 {
				t.groups = removeGroup(t.groups, g)
			}
		}
	} else {
		t.subs = removeSub(t.subs, s)
	}
	if len(t.subs) == 0 && len(t.groups) == 0 {
		delete(b.topics, s.topic) // no leak of empty topics
	}
}

// removeSub deletes s in place. Safe because readers hold b.mu for reading
// and this runs under the write lock.
func removeSub(list []*Subscription, s *Subscription) []*Subscription {
	if i := slices.Index(list, s); i >= 0 {
		return slices.Delete(list, i, i+1)
	}
	return list
}

// ---- Subscription ----------------------------------------------------------

// Subscription is one consumer's view of a topic. Receive from C; call
// Unsubscribe when done. All methods are safe for concurrent use.
type Subscription struct {
	b     *Broker
	topic string
	cfg   subConfig

	out      chan Message  // consumer-facing, unbuffered; ONLY run() sends on or closes it
	wake     chan struct{} // cap 1: "q changed", producer -> pump
	done     chan struct{} // closed by Unsubscribe: stop now
	finished chan struct{} // closed when the pump has exited and out is closed

	leave sync.Once

	mu      sync.Mutex // guards everything below
	q       []Message  // accepted, not yet received; q[0] is being offered to the consumer
	closing bool       // intake closed: no new publishes
	dead    bool       // pump finished
	dropped uint64
	space   chan struct{} // lazily created broadcast: closed whenever outstanding work shrinks or intake closes

	// stage 2
	pending  map[string]*pending // by message ID: everything the broker still owes an ack for
	inflight int                 // pending entries the consumer holds (ack timer running)

	// stage 3
	holder  map[string]string    // key -> ID of its in-flight message
	parked  map[string][]Message // messages held back behind their key's in-flight message
	parkedN int
}

func newSubscription(b *Broker, topic string, cfg subConfig) *Subscription {
	return &Subscription{
		b: b, topic: topic, cfg: cfg,
		out:      make(chan Message),
		wake:     make(chan struct{}, 1),
		done:     make(chan struct{}),
		finished: make(chan struct{}),
		pending:  make(map[string]*pending),
		holder:   make(map[string]string),
		parked:   make(map[string][]Message),
	}
}

// C returns the channel messages arrive on. It is closed after Unsubscribe,
// or after Close once the queue has been drained. Never close it yourself.
func (s *Subscription) C() <-chan Message { return s.out }

// Len returns how many accepted messages are still waiting for the consumer
// (including messages held back behind their key, stage 3).
func (s *Subscription) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.occupancyLocked()
}

// Dropped returns how many messages the overflow policy has discarded for
// this subscription.
func (s *Subscription) Dropped() uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.dropped
}

// Unsubscribe stops delivery, discards whatever is still queued and closes C.
// When it returns, the pump has exited and no further message will arrive.
// Publishers blocked on this subscription are released. Idempotent.
func (s *Subscription) Unsubscribe() {
	s.leave.Do(func() {
		s.b.mu.Lock()
		s.b.unregister(s) // publishers stop picking us before we start closing
		owed := s.beginLeave()
		close(s.done)
		s.b.rehome(s, owed) // stage 3: a group member's unacked work goes back to the group
		s.b.mu.Unlock()
	})
	<-s.finished
}

// ---- publisher side -------------------------------------------------------

type offerStatus int

const (
	offerOK      offerStatus = iota // accepted
	offerDropped                    // not accepted, counted by the policy
	offerFull                       // full and policy Block: the caller must wait
	offerGone                       // closing, nothing to do
)

func (s *Subscription) occupancyLocked() int { return len(s.q) + s.parkedN }

// tryOffer never blocks. It accepts m if there is room, otherwise applies the
// drop policies, otherwise reports offerFull.
func (s *Subscription) tryOffer(m Message) offerStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.offerLocked(m, true)
}

// tryAccept accepts m only if there is room; it never applies a policy.
func (s *Subscription) tryAccept(m Message) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.offerLocked(m, false) == offerOK
}

func (s *Subscription) offerLocked(m Message, applyPolicy bool) offerStatus {
	if s.closing || s.b.closed.Load() {
		return offerGone
	}
	if s.occupancyLocked() < s.cfg.buffer {
		s.admitLocked(m)
		return offerOK
	}
	if !applyPolicy {
		return offerFull
	}
	switch s.cfg.policy {
	case DropNewest:
		s.dropped++
		return offerDropped
	case DropOldest:
		if len(s.q) > 0 { // always true here: acks (and so parking) are incompatible with DropOldest
			s.popLocked() // evicts even the head being offered; the pump re-peeks on wake
			s.dropped++
			s.admitLocked(m)
			return offerOK
		}
		s.dropped++
		return offerDropped
	}
	return offerFull
}

// waitOffer blocks until m is accepted, ctx is done, or the subscription
// goes away. No lock is held while waiting.
func (s *Subscription) waitOffer(ctx context.Context, m Message) error {
	for {
		s.mu.Lock()
		if s.b.closed.Load() {
			s.mu.Unlock()
			return ErrClosed
		}
		if s.closing {
			s.mu.Unlock()
			return errGone
		}
		if s.occupancyLocked() < s.cfg.buffer {
			s.admitLocked(m)
			s.mu.Unlock()
			return nil
		}
		sp := s.spaceLocked()
		s.mu.Unlock()
		select {
		case <-sp:
		case <-ctx.Done():
			return ctx.Err()
		case <-s.done:
			return errGone
		}
	}
}

// spaceLocked returns the channel that is closed the next time room appears.
func (s *Subscription) spaceLocked() chan struct{} {
	if s.space == nil {
		s.space = make(chan struct{})
	}
	return s.space
}

// signalLocked wakes everyone waiting for room or for the queue to settle.
func (s *Subscription) signalLocked() {
	if s.space != nil {
		close(s.space)
		s.space = nil
	}
}

func (s *Subscription) enqueueLocked(m Message) {
	s.q = append(s.q, m)
	s.wakePump()
}

func (s *Subscription) wakePump() {
	select {
	case s.wake <- struct{}{}:
	default: // a wake-up is already pending
	}
}

// popLocked removes the head of the queue. O(1); the slot is zeroed so the
// payload can be collected.
func (s *Subscription) popLocked() {
	s.q[0] = Message{}
	s.q = s.q[1:]
	s.signalLocked()
}

// beginLeave closes intake for good and returns what the subscription still
// owes its group (stage 3; nil otherwise). Requires b.mu for writing.
func (s *Subscription) beginLeave() []Message {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closing = true
	owed := s.owedLocked()
	s.signalLocked()
	return owed
}

// closeIntake stops new publishes; the pump drains q and then exits.
func (s *Subscription) closeIntake() {
	s.mu.Lock()
	s.closing = true
	s.signalLocked()
	s.mu.Unlock()
	s.wakePump()
}

// ---- the pump ---------------------------------------------------------------

// run is the subscription's only goroutine. It is the sole sender on, and
// the sole closer of, s.out.
func (s *Subscription) run() {
	defer s.finish()
	for {
		head, ok := s.nextHead()
		if !ok {
			return
		}
		select {
		case s.out <- head:
			s.afterReceive(head)
		case <-s.wake: // the queue changed under us (DropOldest may have evicted head): re-peek
		case <-s.done:
			return
		}
	}
}

// nextHead waits for a deliverable message. It reports false when the pump
// should exit: Unsubscribe was called, or intake is closed and q is empty.
func (s *Subscription) nextHead() (Message, bool) {
	for {
		s.mu.Lock()
		for len(s.q) > 0 && s.staleLocked(s.q[0]) {
			s.popLocked() // an acked message's queued redelivery copy: nobody wants it any more
		}
		if len(s.q) > 0 {
			h := s.q[0]
			s.offeringLocked(h) // stage 2: from here on the consumer may have it
			s.mu.Unlock()
			return h, true
		}
		closing := s.closing
		s.mu.Unlock()
		if closing {
			return Message{}, false
		}
		select {
		case <-s.wake:
		case <-s.done:
			return Message{}, false
		}
	}
}

// afterReceive runs when the consumer has taken head from C.
func (s *Subscription) afterReceive(head Message) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.q) > 0 && s.q[0].seq == head.seq {
		s.popLocked()
	} else {
		// DropOldest evicted head at the instant the consumer received it:
		// it was delivered after all, so it is not a drop.
		s.dropped--
	}
	s.deliveredLocked(head) // stage 2: start the ack timer now
}

// finish tears the subscription down after the pump exits.
func (s *Subscription) finish() {
	s.b.mu.Lock()
	s.b.unregister(s)
	s.b.mu.Unlock()

	s.mu.Lock()
	s.closing, s.dead = true, true
	s.discardLocked() // stage 2/3: forget pending, parked and timers
	for i := range s.q {
		s.q[i] = Message{}
	}
	s.q = nil
	s.signalLocked()
	s.mu.Unlock()

	close(s.out) // the only close, by the only sender
	close(s.finished)
}
