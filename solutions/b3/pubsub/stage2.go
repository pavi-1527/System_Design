package pubsub

import (
	"container/heap"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
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
//     waited 8 s in the queue still gets its full timeout.
//   - Unacked after the timeout (or Nacked): the message is redelivered on the
//     same subscription with Attempt+1 and the SAME ID. After WithMaxAttempts
//     attempts (default 5) it is dead-lettered instead: copied to the topic
//     named by WithDeadLetter (Message.Origin = the original topic) and
//     forgotten. Without WithDeadLetter it is only counted.
//   - This is at-least-once. Duplicates are expected (late ack after a
//     redelivery, lost ack, crash after processing). A consumer that must not
//     repeat a side effect keeps the IDs it has handled and skips repeats.
//   - Ack/Nack on an ID that is not awaiting an ack return ErrNotPending (a
//     benign answer for a duplicate). A late Ack, after a redelivery was
//     queued, still settles the message and cancels the queued copy.
//   - Redelivery never blocks the clock: if the subscriber's queue is full,
//     the redelivery is deferred by one more ack timeout without consuming an
//     attempt.
//   - ONE timer serves the whole broker (a min-heap on deadlines), however
//     many messages are in flight. Time comes from the injected Clock.
//   - DropOldest cannot be combined with acks (ErrInvalidOption).

var (
	// ErrNotPending is returned by Ack/Nack for an ID that is not awaiting an ack.
	ErrNotPending = errors.New("pubsub: no such message awaiting an ack")
	// ErrAckDisabled is returned by Ack/Nack on a subscription without WithAckTimeout.
	ErrAckDisabled = errors.New("pubsub: subscription has no ack timeout")
)

const defaultMaxAttempts = 5

// Clock is all the broker needs from time. AfterFunc runs f once after d, on
// a goroutine of the clock's choosing, and returns a func that cancels it
// (reporting whether it cancelled before f started).
type Clock interface {
	Now() time.Time
	AfterFunc(d time.Duration, f func()) (stop func() bool)
}

type systemClock struct{}

func (systemClock) Now() time.Time { return time.Now() }
func (systemClock) AfterFunc(d time.Duration, f func()) func() bool {
	return time.AfterFunc(d, f).Stop
}

// WithClock sets the broker's clock (default: the system clock).
func WithClock(c Clock) Option { return func(b *Broker) { b.clock = c } }

// WithAckTimeout turns on acks: a received message must be settled within d.
func WithAckTimeout(d time.Duration) SubOption {
	return func(c *subConfig) error {
		if d <= 0 {
			return fmt.Errorf("%w: ack timeout must be > 0", ErrInvalidOption)
		}
		c.ackTimeout = d
		return nil
	}
}

// WithMaxAttempts bounds deliveries per message (default 5). Needs WithAckTimeout.
func WithMaxAttempts(n int) SubOption {
	return func(c *subConfig) error {
		if n < 1 {
			return fmt.Errorf("%w: max attempts must be >= 1", ErrInvalidOption)
		}
		c.maxAttempts = n
		return nil
	}
}

// WithDeadLetter names the topic that receives messages which ran out of
// attempts. Needs WithAckTimeout. Subscribe to that topic BEFORE the failures
// happen: a topic nobody listens to discards what it is sent.
func WithDeadLetter(topic string) SubOption {
	return func(c *subConfig) error {
		if topic == "" {
			return fmt.Errorf("%w: dead-letter topic must not be empty", ErrInvalidOption)
		}
		c.deadLetter = topic
		return nil
	}
}

func (c *subConfig) validate(topic string) error {
	if c.ackTimeout == 0 {
		if c.maxAttempts != 0 || c.deadLetter != "" {
			return fmt.Errorf("%w: WithMaxAttempts/WithDeadLetter need WithAckTimeout", ErrInvalidOption)
		}
		return nil
	}
	if c.policy == DropOldest {
		return fmt.Errorf("%w: DropOldest would discard messages the broker already accepted; it cannot be combined with acks", ErrInvalidOption)
	}
	if c.deadLetter == topic {
		return fmt.Errorf("%w: dead-letter topic must differ from the topic itself", ErrInvalidOption)
	}
	if c.maxAttempts == 0 {
		c.maxAttempts = defaultMaxAttempts
	}
	return nil
}

// Stats are broker-wide counters.
type Stats struct {
	Published      uint64 // Publish calls accepted
	Redelivered    uint64 // attempts beyond the first, queued for a consumer
	DeadLettered   uint64 // messages that ran out of attempts
	DeadLetterLost uint64 // dead letters a Block-policy dead-letter subscriber had no room for
	Orphaned       uint64 // messages a leaving group member owed with nobody left to give them to
}

type counters struct {
	published, redelivered, deadLettered, deadLetterLost, orphaned atomic.Uint64
}

// Stats returns a snapshot of the counters.
func (b *Broker) Stats() Stats {
	return Stats{
		Published:      b.stats.published.Load(),
		Redelivered:    b.stats.redelivered.Load(),
		DeadLettered:   b.stats.deadLettered.Load(),
		DeadLetterLost: b.stats.deadLetterLost.Load(),
		Orphaned:       b.stats.orphaned.Load(),
	}
}

// pending is the broker's record that a subscriber still owes an ack for a
// message. Everything except deadline and heapIdx is guarded by the owning
// subscription's mutex; deadline and heapIdx belong to sched.mu.
type pending struct {
	sub     *Subscription
	msg     Message // msg.Attempt is the attempt currently outstanding
	offered bool    // the pump has started handing the current attempt to the consumer (it may hold it already)
	// inflight: the pump has seen the consumer receive the current attempt
	// and its ack timer is running. offered turns true a moment BEFORE the
	// consumer can possibly have the message, inflight a moment AFTER; Nack
	// must key on offered, or a Nack that arrives in that gap would be lost.
	inflight bool
	deadline time.Time
	heapIdx  int // position in sched.h, -1 when not scheduled
}

// ---- the one timer ------------------------------------------------------------

type sched struct {
	clock Clock

	mu    sync.Mutex
	h     pendingHeap
	stop  func() bool // cancels the single armed timer; nil when none is armed
	armed time.Time   // the deadline that timer was set for
}

func newSched(c Clock) *sched { return &sched{clock: c} }

// set (re)schedules p to expire at deadline.
func (sc *sched) set(p *pending, deadline time.Time) {
	sc.mu.Lock()
	p.deadline = deadline
	if p.heapIdx >= 0 {
		heap.Fix(&sc.h, p.heapIdx)
	} else {
		heap.Push(&sc.h, p)
	}
	sc.rearmLocked()
	sc.mu.Unlock()
}

// remove cancels p's expiry, if it has one.
func (sc *sched) remove(p *pending) {
	sc.mu.Lock()
	if p.heapIdx >= 0 {
		heap.Remove(&sc.h, p.heapIdx)
		sc.rearmLocked()
	}
	sc.mu.Unlock()
}

// rearmLocked makes sure exactly one timer is armed, for the earliest
// deadline, or none when nothing is scheduled.
func (sc *sched) rearmLocked() {
	if len(sc.h) == 0 {
		if sc.stop != nil {
			sc.stop()
			sc.stop = nil
		}
		return
	}
	head := sc.h[0].deadline
	if sc.stop != nil {
		if sc.armed.Equal(head) {
			return
		}
		sc.stop()
	}
	sc.armed = head
	sc.stop = sc.clock.AfterFunc(max(head.Sub(sc.clock.Now()), 0), sc.fire)
}

// fire runs on the clock's goroutine. It pops everything due, re-arms for
// the next deadline, and only then calls into subscriptions (sched.mu is
// never held while a subscription lock is taken).
func (sc *sched) fire() {
	now := sc.clock.Now()
	var due []*pending
	sc.mu.Lock()
	if sc.stop != nil { // also covers a stale callback racing a re-arm: keep exactly one timer
		sc.stop()
		sc.stop = nil
	}
	for len(sc.h) > 0 && !sc.h[0].deadline.After(now) {
		due = append(due, heap.Pop(&sc.h).(*pending))
	}
	sc.rearmLocked()
	sc.mu.Unlock()
	for _, p := range due {
		p.sub.expire(p, now)
	}
}

type pendingHeap []*pending

func (h pendingHeap) Len() int { return len(h) }
func (h pendingHeap) Less(i, j int) bool {
	if !h[i].deadline.Equal(h[j].deadline) {
		return h[i].deadline.Before(h[j].deadline)
	}
	return h[i].msg.seq < h[j].msg.seq // tie-break on publish order: deterministic redelivery order
}
func (h pendingHeap) Swap(i, j int) {
	h[i], h[j] = h[j], h[i]
	h[i].heapIdx, h[j].heapIdx = i, j
}
func (h *pendingHeap) Push(x any) {
	p := x.(*pending)
	p.heapIdx = len(*h)
	*h = append(*h, p)
}
func (h *pendingHeap) Pop() any {
	old := *h
	n := len(old)
	p := old[n-1]
	old[n-1] = nil
	*h = old[:n-1]
	p.heapIdx = -1
	return p
}

// ---- subscription side ------------------------------------------------------

// InFlight returns how many messages the consumer holds unsettled (received,
// ack timer running).
func (s *Subscription) InFlight() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.inflight
}

// Ack settles the message with this ID: it will not be redelivered, and a
// copy already queued for redelivery is cancelled.
func (s *Subscription) Ack(id string) error {
	p, err := s.lookup(id)
	if err != nil {
		return err
	}
	defer s.mu.Unlock()
	s.settleLocked(p)
	return nil
}

// Nack says "I could not process this": the message is redelivered at once
// (Attempt+1), or dead-lettered if it has used up its attempts.
func (s *Subscription) Nack(id string) error {
	p, err := s.lookup(id)
	if err != nil {
		return err
	}
	if !p.offered { // the current attempt is still queued: a redelivery is already on its way
		s.mu.Unlock()
		return nil
	}
	dead := s.retryLocked(p, s.b.clock.Now())
	s.mu.Unlock()
	s.b.deadLetter(s, dead)
	return nil
}

// lookup returns the pending entry with s.mu HELD, or an error with it released.
func (s *Subscription) lookup(id string) (*pending, error) {
	if s.cfg.ackTimeout == 0 {
		return nil, ErrAckDisabled
	}
	s.mu.Lock()
	p := s.pending[id]
	if p == nil {
		dead := s.dead
		s.mu.Unlock()
		if dead {
			return nil, ErrClosed
		}
		return nil, ErrNotPending
	}
	return p, nil
}

// expire is called by sched.fire for a pending whose ack timer ran out.
func (s *Subscription) expire(p *pending, now time.Time) {
	s.mu.Lock()
	if s.pending[p.msg.ID] != p { // acked, dead-lettered or forgotten since fire popped it
		s.mu.Unlock()
		return
	}
	dead := s.retryLocked(p, now)
	s.mu.Unlock()
	s.b.deadLetter(s, dead)
}

// retryLocked handles "this attempt failed" for both timeouts and Nacks. It
// returns the message to dead-letter, or nil. Requires s.mu. It never blocks.
func (s *Subscription) retryLocked(p *pending, now time.Time) *Message {
	s.b.sched.remove(p)
	if p.msg.Attempt >= s.cfg.maxAttempts {
		m := p.msg
		s.settleLocked(p)
		return &m
	}
	if s.occupancyLocked() >= s.cfg.buffer {
		// No room for another copy. Do not wait (that would stall the clock
		// for every other subscriber): look again one ack timeout from now.
		s.b.sched.set(p, now.Add(s.cfg.ackTimeout))
		return nil
	}
	if p.inflight {
		p.inflight = false
		s.inflight--
	}
	p.offered = false
	p.msg.Attempt++
	s.b.stats.redelivered.Add(1)
	s.enqueueLocked(p.msg) // the timer for this attempt starts when the consumer receives it
	return nil
}

// settleLocked forgets p for good: acked, or dead-lettered.
func (s *Subscription) settleLocked(p *pending) {
	delete(s.pending, p.msg.ID)
	if p.inflight {
		p.inflight = false
		s.inflight--
	}
	s.b.sched.remove(p)
	s.releaseKeyLocked(p.msg) // stage 3: let the next message for this key through
	s.signalLocked()
}

// admitLocked accepts m into the subscription (capacity was checked by the
// caller). In ack mode it also records that an ack is owed.
func (s *Subscription) admitLocked(m Message) {
	if s.cfg.ackTimeout == 0 {
		s.enqueueLocked(m)
		return
	}
	if s.holdBackLocked(m) { // stage 3: behind the in-flight message with the same key
		return
	}
	s.pending[m.ID] = &pending{sub: s, msg: m, heapIdx: -1}
	s.enqueueLocked(m)
}

// staleLocked: a queued copy of a message that has been settled meanwhile.
func (s *Subscription) staleLocked(m Message) bool {
	return s.cfg.ackTimeout != 0 && s.pending[m.ID] == nil
}

// offeringLocked records that the pump is about to hand m to the consumer.
func (s *Subscription) offeringLocked(m Message) {
	if s.cfg.ackTimeout == 0 {
		return
	}
	if p := s.pending[m.ID]; p != nil && p.msg.Attempt == m.Attempt {
		p.offered = true
	}
}

// deliveredLocked starts the ack timer for an attempt the consumer has just received.
func (s *Subscription) deliveredLocked(m Message) {
	if s.cfg.ackTimeout == 0 {
		return
	}
	p := s.pending[m.ID]
	if p == nil || p.msg.Attempt != m.Attempt {
		// Settled, or already superseded by a newer attempt (an Ack or Nack
		// got in between the receive and this bookkeeping): nothing to time.
		return
	}
	if !p.inflight {
		p.inflight = true
		s.inflight++
	}
	s.b.sched.set(p, s.b.clock.Now().Add(s.cfg.ackTimeout))
}

// discardLocked forgets every obligation when the subscription ends.
func (s *Subscription) discardLocked() {
	for _, p := range s.pending {
		s.b.sched.remove(p)
	}
	clear(s.pending)
	clear(s.holder)
	clear(s.parked)
	s.parkedN, s.inflight = 0, 0
}

// deadLetter moves m (which ran out of attempts on sub) to the dead-letter
// topic. It never waits: a dead-letter topic that is full loses the copy
// (counted) rather than stalling whoever noticed the failure.
func (b *Broker) deadLetter(sub *Subscription, m *Message) {
	if m == nil {
		return
	}
	b.stats.deadLettered.Add(1)
	dl := sub.cfg.deadLetter
	if dl == "" {
		return
	}
	cp := Message{
		ID: m.ID, Topic: dl, Key: m.Key, Payload: m.Payload, Attempt: 1, Origin: m.Topic,
		seq: b.seq.Add(1),
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	if t := b.topics[dl]; t != nil {
		b.stats.deadLetterLost.Add(uint64(len(t.offer(cp))))
	}
}
