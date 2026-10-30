package pubsub

import (
	"errors"
	"fmt"
	"runtime"
	"sync"
	"testing"
	"time"
)

// fakeClock is a manually advanced clock. Advance runs due callbacks
// synchronously on the caller's goroutine, in deadline order, with the clock
// set to each deadline in turn, so a test decides exactly when time passes
// and knows the redelivery has been queued when Advance returns.
type fakeClock struct {
	mu     sync.Mutex
	now    time.Time
	timers []*fakeTimer
}

type fakeTimer struct {
	at             time.Time
	f              func()
	stopped, fired bool
}

func newFakeClock() *fakeClock {
	return &fakeClock{now: time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) AfterFunc(d time.Duration, f func()) func() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	ft := &fakeTimer{at: c.now.Add(d), f: f}
	c.timers = append(c.timers, ft)
	return func() bool {
		c.mu.Lock()
		defer c.mu.Unlock()
		if ft.stopped || ft.fired {
			return false
		}
		ft.stopped = true
		return true
	}
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	target := c.now.Add(d)
	c.mu.Unlock()
	for {
		c.mu.Lock()
		var next *fakeTimer
		for _, ft := range c.timers {
			if !ft.stopped && !ft.fired && !ft.at.After(target) && (next == nil || ft.at.Before(next.at)) {
				next = ft
			}
		}
		if next == nil {
			c.now = target
			live := c.timers[:0]
			for _, ft := range c.timers {
				if !ft.stopped && !ft.fired {
					live = append(live, ft)
				}
			}
			c.timers = live
			c.mu.Unlock()
			return
		}
		next.fired = true
		if next.at.After(c.now) {
			c.now = next.at
		}
		c.mu.Unlock()
		next.f()
	}
}

// active is the number of timers that are armed and not yet fired or stopped.
func (c *fakeClock) active() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for _, ft := range c.timers {
		if !ft.stopped && !ft.fired {
			n++
		}
	}
	return n
}

const ackTimeout = 10 * time.Second

func ackBroker(t *testing.T) (*Broker, *fakeClock) {
	t.Helper()
	clk := newFakeClock()
	return mustNew(t, WithClock(clk)), clk
}

// recvInFlight receives one message and waits until the broker has started
// its ack timer (InFlight reaches n). The timer starts a moment after the
// receive, so a test that advances the clock straight away would race it.
func recvInFlight(t *testing.T, s *Subscription, n int) Message {
	t.Helper()
	m := recv(t, s)
	waitFor(t, fmt.Sprintf("InFlight() == %d after receiving %s", n, m.ID), func() bool { return s.InFlight() == n })
	return m
}

func TestStage2_AckConfigurationAndErrors(t *testing.T) {
	t.Run("RejectsBadAckConfiguration", stage2RejectsBadAckConfiguration)
	t.Run("AckAndNackNeedAckMode", stage2AckAndNackNeedAckMode)
	t.Run("AckingAnUnknownIDIsHarmless", stage2AckingAnUnknownIDIsHarmless)
}

func stage2RejectsBadAckConfiguration(t *testing.T) {
	b, _ := ackBroker(t)
	cases := map[string][]SubOption{
		"zero ack timeout":                  {WithAckTimeout(0)},
		"negative ack timeout":              {WithAckTimeout(-time.Second)},
		"zero max attempts":                 {WithAckTimeout(time.Second), WithMaxAttempts(0)},
		"empty dead-letter topic":           {WithAckTimeout(time.Second), WithDeadLetter("")},
		"max attempts without acks":         {WithMaxAttempts(3)},
		"dead letter without acks":          {WithDeadLetter("dlq")},
		"drop-oldest cannot be combined":    {WithAckTimeout(time.Second), WithPolicy(DropOldest)},
		"dead-letter topic equal to source": {WithAckTimeout(time.Second), WithDeadLetter("t")},
	}
	for name, opts := range cases {
		if _, err := b.Subscribe("t", opts...); !errors.Is(err, ErrInvalidOption) {
			t.Errorf("%s: Subscribe error = %v, want ErrInvalidOption", name, err)
		}
	}
	if _, err := New(WithClock(nil)); err == nil {
		t.Error("New(WithClock(nil)) succeeded; a broker without a clock cannot schedule redeliveries")
	}
}

func stage2AckAndNackNeedAckMode(t *testing.T) {
	b, _ := ackBroker(t)
	plain := mustSub(t, b, "t")
	pub(t, b, "t", "x")
	m := recv(t, plain)
	if err := plain.Ack(m.ID); !errors.Is(err, ErrAckDisabled) {
		t.Errorf("Ack on a subscription without WithAckTimeout = %v, want ErrAckDisabled", err)
	}
	if err := plain.Nack(m.ID); !errors.Is(err, ErrAckDisabled) {
		t.Errorf("Nack on a subscription without WithAckTimeout = %v, want ErrAckDisabled", err)
	}
}

func TestStage2_Redelivery(t *testing.T) {
	t.Run("UnackedMessageIsRedeliveredAfterTheTimeout", stage2UnackedMessageIsRedeliveredAfterTheTimeout)
	t.Run("AckStopsRedelivery", stage2AckStopsRedelivery)
	t.Run("AckTimerStartsWhenTheConsumerReceives", stage2AckTimerStartsWhenTheConsumerReceives)
	t.Run("RedeliveriesKeepPublishOrder", stage2RedeliveriesKeepPublishOrder)
}

func stage2UnackedMessageIsRedeliveredAfterTheTimeout(t *testing.T) {
	b, clk := ackBroker(t)
	s := mustSub(t, b, "t", WithAckTimeout(ackTimeout))
	id := pubKey(t, b, "t", "acct-9", map[string]int{"amount": 100})
	m1 := recvInFlight(t, s, 1)
	if m1.Attempt != 1 || m1.ID != id {
		t.Fatalf("first delivery: Attempt=%d ID=%q, want 1 and %q", m1.Attempt, m1.ID, id)
	}

	clk.Advance(ackTimeout - time.Nanosecond)
	expectNone(t, s, "1ns before the ack deadline there is nothing to redeliver")
	clk.Advance(time.Nanosecond)

	m2 := recv(t, s)
	if m2.ID != id || m2.Attempt != 2 || m2.Key != "acct-9" || m2.Topic != "t" {
		t.Fatalf("redelivery = %+v, want the same ID %q, Key, Topic, and Attempt 2: the ID is how a consumer recognises a duplicate", m2, id)
	}
	if p, ok := m2.Payload.(map[string]int); !ok || p["amount"] != 100 {
		t.Fatalf("redelivered payload = %v, want the original", m2.Payload)
	}
	if got := b.Stats().Redelivered; got != 1 {
		t.Errorf("Stats().Redelivered = %d, want 1", got)
	}
}

func stage2AckStopsRedelivery(t *testing.T) {
	b, clk := ackBroker(t)
	s := mustSub(t, b, "t", WithAckTimeout(ackTimeout))
	pub(t, b, "t", "x")
	m := recvInFlight(t, s, 1)
	if err := s.Ack(m.ID); err != nil {
		t.Fatalf("Ack = %v", err)
	}
	if got := s.InFlight(); got != 0 {
		t.Fatalf("InFlight() = %d after Ack, want 0", got)
	}
	clk.Advance(100 * ackTimeout)
	expectNone(t, s, "an acked message must never come back")
	if got := clk.active(); got != 0 {
		t.Errorf("%d timers still armed after the only pending message was acked; a leaked timer keeps the broker (and its subscriptions) alive", got)
	}
}

func TestStage2_AckAndNack(t *testing.T) {
	t.Run("AckAndNackRightAfterReceiveAreNeverLost", stage2AckAndNackRightAfterReceiveAreNeverLost)
	t.Run("NackRedeliversImmediately", stage2NackRedeliversImmediately)
}

func stage2AckAndNackRightAfterReceiveAreNeverLost(t *testing.T) {
	// The consumer settles the moment it has the message, before the broker
	// has even noted the receive. Neither call may be ignored.
	b, _ := ackBroker(t)
	s := mustSub(t, b, "t", WithAckTimeout(ackTimeout))
	for i := 0; i < 300; i++ {
		id := pub(t, b, "t", i)
		m := recv(t, s)
		if err := s.Nack(m.ID); err != nil {
			t.Fatalf("round %d: Nack = %v", i, err)
		}
		m2 := recv(t, s)
		if m2.ID != id || m2.Attempt != 2 {
			t.Fatalf("round %d: after an immediate Nack got %+v, want %s as Attempt 2", i, m2, id)
		}
		if err := s.Ack(m2.ID); err != nil {
			t.Fatalf("round %d: immediate Ack = %v", i, err)
		}
	}
	waitFor(t, "InFlight() == 0", func() bool { return s.InFlight() == 0 })
}

func stage2NackRedeliversImmediately(t *testing.T) {
	b, _ := ackBroker(t)
	s := mustSub(t, b, "t", WithAckTimeout(ackTimeout))
	id := pub(t, b, "t", "x")
	m := recvInFlight(t, s, 1)
	if err := s.Nack(m.ID); err != nil {
		t.Fatalf("Nack = %v", err)
	}
	m2 := recv(t, s) // no clock advance: Nack means "retry now"
	if m2.ID != id || m2.Attempt != 2 {
		t.Fatalf("after Nack got %+v, want the same message as Attempt 2", m2)
	}
}

func stage2AckTimerStartsWhenTheConsumerReceives(t *testing.T) {
	b, clk := ackBroker(t)
	s := mustSub(t, b, "t", WithAckTimeout(ackTimeout))
	pub(t, b, "t", "x")
	clk.Advance(8 * time.Second) // the message waits in the queue; the consumer is busy elsewhere
	m1 := recvInFlight(t, s, 1)  // the clock for attempt 1 starts NOW

	clk.Advance(5 * time.Second) // 13 s after publish, 5 s after receive
	expectNone(t, s, "an ack timer that started at publish time would have fired by now; queue time must not burn the consumer's budget (it would cause a duplicate for every slow reader)")
	clk.Advance(5 * time.Second) // exactly 10 s after receive
	if m2 := recv(t, s); m2.ID != m1.ID || m2.Attempt != 2 {
		t.Fatalf("got %+v, want redelivery of %s as Attempt 2", m2, m1.ID)
	}
}

func stage2RedeliveriesKeepPublishOrder(t *testing.T) {
	b, clk := ackBroker(t)
	s := mustSub(t, b, "t", WithAckTimeout(ackTimeout), WithBuffer(16))
	for i := 0; i < 5; i++ {
		pub(t, b, "t", i)
	}
	for i := 1; i <= 5; i++ {
		recvInFlight(t, s, i)
	}
	clk.Advance(ackTimeout)
	for i := 0; i < 5; i++ {
		m := recv(t, s)
		if m.Payload != i || m.Attempt != 2 {
			t.Fatalf("redelivery #%d = payload %v attempt %d, want payload %d attempt 2: messages that expire together come back in publish order", i, m.Payload, m.Attempt, i)
		}
	}
}

func TestStage2_DeadLetters(t *testing.T) {
	t.Run("MaxAttemptsThenDeadLetter", stage2MaxAttemptsThenDeadLetter)
	t.Run("NackCountsAsAnAttemptAndCanDeadLetter", stage2NackCountsAsAnAttemptAndCanDeadLetter)
	t.Run("PoisonMessageDoesNotHoldUpOthers", stage2PoisonMessageDoesNotHoldUpOthers)
	t.Run("DeadLetterWithoutATopicIsCounted", stage2DeadLetterWithoutATopicIsCounted)
}

func stage2MaxAttemptsThenDeadLetter(t *testing.T) {
	b, clk := ackBroker(t)
	dlq := mustSub(t, b, "orders.dlq")
	s := mustSub(t, b, "orders", WithAckTimeout(ackTimeout), WithMaxAttempts(3), WithDeadLetter("orders.dlq"))
	id := pubKey(t, b, "orders", "acct-3", "poison")

	for attempt := 1; attempt <= 3; attempt++ {
		m := recvInFlight(t, s, 1)
		if m.Attempt != attempt {
			t.Fatalf("delivery %d has Attempt %d", attempt, m.Attempt)
		}
		expectNone(t, dlq, "the message has attempts left")
		clk.Advance(ackTimeout)
	}
	d := recv(t, dlq)
	if d.ID != id || d.Origin != "orders" || d.Topic != "orders.dlq" || d.Payload != "poison" || d.Key != "acct-3" {
		t.Fatalf("dead letter = %+v, want ID %q, Origin orders, Topic orders.dlq, original payload and key", d, id)
	}
	expectNone(t, s, "after the last attempt the message must not be delivered to the source subscription again")
	clk.Advance(10 * ackTimeout)
	expectNone(t, s, "dead-lettered messages are forgotten")
	expectNone(t, dlq, "exactly one dead letter per message")
	if got := b.Stats().DeadLettered; got != 1 {
		t.Errorf("Stats().DeadLettered = %d, want 1", got)
	}
	if err := s.Ack(id); !errors.Is(err, ErrNotPending) {
		t.Errorf("Ack of a dead-lettered message = %v, want ErrNotPending", err)
	}
}

func stage2NackCountsAsAnAttemptAndCanDeadLetter(t *testing.T) {
	b, _ := ackBroker(t)
	dlq := mustSub(t, b, "dlq")
	s := mustSub(t, b, "t", WithAckTimeout(ackTimeout), WithMaxAttempts(2), WithDeadLetter("dlq"))
	id := pub(t, b, "t", "x")
	m := recvInFlight(t, s, 1)
	_ = s.Nack(m.ID)
	m = recv(t, s) // attempt 2, the last
	if m.Attempt != 2 {
		t.Fatalf("Attempt = %d, want 2", m.Attempt)
	}
	if err := s.Nack(m.ID); err != nil {
		t.Fatalf("Nack = %v", err)
	}
	if d := recv(t, dlq); d.ID != id || d.Origin != "t" {
		t.Fatalf("dead letter = %+v, want ID %q Origin t", d, id)
	}
	expectNone(t, s, "no third attempt")
}

func stage2PoisonMessageDoesNotHoldUpOthers(t *testing.T) {
	b, clk := ackBroker(t)
	dlq := mustSub(t, b, "dlq")
	s := mustSub(t, b, "t", WithAckTimeout(ackTimeout), WithMaxAttempts(2), WithDeadLetter("dlq"), WithBuffer(8))
	poison := pub(t, b, "t", "poison")
	good := pub(t, b, "t", "good")

	a, c := recvInFlight(t, s, 1), recvInFlight(t, s, 2)
	if a.ID != poison || c.ID != good {
		t.Fatalf("delivery order %s,%s", a.ID, c.ID)
	}
	if err := s.Ack(good); err != nil {
		t.Fatalf("Ack(good) = %v", err)
	}
	clk.Advance(ackTimeout) // poison: attempt 1 expires
	if m := recv(t, s); m.ID != poison || m.Attempt != 2 {
		t.Fatalf("got %+v, want the poison message again as Attempt 2", m)
	}
	waitFor(t, "InFlight() == 1", func() bool { return s.InFlight() == 1 })
	clk.Advance(ackTimeout) // attempt 2 expires: out of attempts
	if d := recv(t, dlq); d.ID != poison {
		t.Fatalf("dead letter = %+v, want the poison message", d)
	}
	pub(t, b, "t", "after")
	if m := recv(t, s); m.Payload != "after" {
		t.Fatalf("got %v: the subscription must keep flowing after a dead letter", m.Payload)
	}
}

func stage2DeadLetterWithoutATopicIsCounted(t *testing.T) {
	b, clk := ackBroker(t)
	s := mustSub(t, b, "t", WithAckTimeout(ackTimeout), WithMaxAttempts(1))
	pub(t, b, "t", "x")
	recvInFlight(t, s, 1)
	clk.Advance(ackTimeout)
	expectNone(t, s, "max attempts 1: the message gets exactly one delivery")
	if got := b.Stats().DeadLettered; got != 1 {
		t.Fatalf("Stats().DeadLettered = %d, want 1", got)
	}
}

func TestStage2_AtLeastOnceDuplicates(t *testing.T) {
	t.Run("LateAckAfterRedeliveryStillSettles", stage2LateAckAfterRedeliveryStillSettles)
	t.Run("AckCancelsAQueuedRedelivery", stage2AckCancelsAQueuedRedelivery)
	t.Run("ConsumerCanDedupeByID", stage2ConsumerCanDedupeByID)
}

func stage2LateAckAfterRedeliveryStillSettles(t *testing.T) {
	// The consumer is slow, the timeout fires and a redelivery is sent. The
	// consumer then finishes attempt 1 and acks. It did the work, so the ack
	// counts, and the second copy it is about to receive is a duplicate.
	b, clk := ackBroker(t)
	s := mustSub(t, b, "t", WithAckTimeout(ackTimeout))
	pub(t, b, "t", "pay invoice 17")
	m1 := recvInFlight(t, s, 1)
	clk.Advance(ackTimeout)
	m2 := recv(t, s) // the duplicate: same ID, Attempt 2
	if m2.ID != m1.ID || m2.Attempt != 2 {
		t.Fatalf("redelivery = %+v", m2)
	}
	if err := s.Ack(m1.ID); err != nil {
		t.Fatalf("late Ack of attempt 1 = %v, want nil: the message was still pending when it arrived", err)
	}
	if err := s.Ack(m2.ID); !errors.Is(err, ErrNotPending) {
		t.Fatalf("Ack of the duplicate = %v, want ErrNotPending (benign: already settled)", err)
	}
	clk.Advance(100 * ackTimeout)
	expectNone(t, s, "nothing may come back after the message was settled")
	waitFor(t, "InFlight() == 0", func() bool { return s.InFlight() == 0 })
}

func stage2AckCancelsAQueuedRedelivery(t *testing.T) {
	b, clk := ackBroker(t)
	s := mustSub(t, b, "t", WithAckTimeout(ackTimeout), WithBuffer(8))
	pub(t, b, "t", "m")
	m := recvInFlight(t, s, 1)
	pub(t, b, "t", "filler") // sits ahead of the redelivery copy in the queue
	clk.Advance(ackTimeout)  // queues the copy of m behind filler
	if got := b.Stats().Redelivered; got != 1 {
		t.Fatalf("Stats().Redelivered = %d, want 1", got)
	}
	if err := s.Ack(m.ID); err != nil { // the consumer finished attempt 1 after all
		t.Fatalf("Ack = %v", err)
	}
	f := recv(t, s)
	if f.Payload != "filler" {
		t.Fatalf("got %v, want filler", f.Payload)
	}
	_ = s.Ack(f.ID)
	pub(t, b, "t", "sentinel")
	if got := recv(t, s).Payload; got != "sentinel" {
		t.Fatalf("got %v before the sentinel: a copy queued for redelivery must be dropped once its message is acked", got)
	}
}

func stage2ConsumerCanDedupeByID(t *testing.T) {
	// The at-least-once contract in one test. The consumer processes each
	// message but its ack "gets lost" (it never calls Ack on the first
	// attempt). Every message is therefore delivered twice. Skipping IDs it
	// has already handled makes the side effect happen exactly once.
	const n = 50
	b, clk := ackBroker(t)
	s := mustSub(t, b, "webhooks", WithAckTimeout(ackTimeout), WithBuffer(2*n))
	for i := 0; i < n; i++ {
		pub(t, b, "webhooks", i)
	}
	processed := map[string]int{}
	handle := func(m Message) {
		if processed[m.ID] > 0 {
			return // duplicate: skip the side effect, just settle
		}
		processed[m.ID]++
	}
	for i := 0; i < n; i++ {
		handle(recvInFlight(t, s, i+1)) // processed, ack lost
	}
	clk.Advance(ackTimeout)
	for i := 0; i < n; i++ {
		m := recv(t, s)
		if m.Attempt != 2 {
			t.Fatalf("redelivery %d has Attempt %d, want 2", i, m.Attempt)
		}
		handle(m)
		if err := s.Ack(m.ID); err != nil {
			t.Fatalf("Ack(%s) = %v", m.ID, err)
		}
	}
	if len(processed) != n {
		t.Fatalf("processed %d distinct messages, want %d", len(processed), n)
	}
	for id, c := range processed {
		if c != 1 {
			t.Fatalf("message %s had its side effect %d times", id, c)
		}
	}
	waitFor(t, "InFlight() == 0", func() bool { return s.InFlight() == 0 })
	clk.Advance(100 * ackTimeout)
	expectNone(t, s, "everything is acked")
}

func stage2AckingAnUnknownIDIsHarmless(t *testing.T) {
	b, _ := ackBroker(t)
	s := mustSub(t, b, "t", WithAckTimeout(ackTimeout))
	if err := s.Ack("m-424242"); !errors.Is(err, ErrNotPending) {
		t.Errorf("Ack(unknown) = %v, want ErrNotPending", err)
	}
	if err := s.Nack("m-424242"); !errors.Is(err, ErrNotPending) {
		t.Errorf("Nack(unknown) = %v, want ErrNotPending", err)
	}
	pub(t, b, "t", "x")
	m := recvInFlight(t, s, 1)
	if err := s.Ack(m.ID); err != nil {
		t.Fatalf("Ack = %v", err)
	}
	if err := s.Ack(m.ID); !errors.Is(err, ErrNotPending) {
		t.Errorf("second Ack = %v, want ErrNotPending (a duplicate ack is not a failure)", err)
	}
}

func TestStage2_SlowSubscriberDoesNotBlockTheClockOrOthers(t *testing.T) {
	// Advance runs the redelivery callbacks on this goroutine, exactly as a
	// real timer goroutine would. If redelivery to a full subscriber waited
	// for room, the clock (and every other subscriber's redelivery) would
	// stall behind it.
	b, clk := ackBroker(t)
	slow := mustSub(t, b, "t", WithAckTimeout(ackTimeout), WithBuffer(1), WithPolicy(Block))
	fast := mustSub(t, b, "t", WithAckTimeout(ackTimeout), WithBuffer(8))
	pub(t, b, "t", "m1")
	slowM1 := recvInFlight(t, slow, 1)
	recvInFlight(t, fast, 1)
	pub(t, b, "t", "m2") // slow's queue is now full (m2 waiting); fast takes m2 too
	recvInFlight(t, fast, 2)

	within(t, "Advance while a subscriber's queue is full", func() { clk.Advance(ackTimeout) })

	for _, want := range []string{"m1", "m2"} {
		if m := recv(t, fast); m.Payload != want || m.Attempt != 2 {
			t.Fatalf("fast subscriber got %+v, want %s as Attempt 2", m, want)
		}
	}
	if slow.Len() != 1 {
		t.Fatalf("slow.Len() = %d, want 1: the redelivery must wait, not jump the bound", slow.Len())
	}

	// Once the slow consumer makes room, the deferred redelivery arrives
	// (and no attempt was burnt while it waited).
	if m := recv(t, slow); m.Payload != "m2" {
		t.Fatalf("slow got %v, want m2", m.Payload)
	}
	waitFor(t, "slow.Len() == 0", func() bool { return slow.Len() == 0 })
	clk.Advance(ackTimeout)
	if m := recv(t, slow); m.ID != slowM1.ID || m.Attempt != 2 {
		t.Fatalf("slow got %+v, want %s as Attempt 2", m, slowM1.ID)
	}
}

func TestStage2_AckRacingTheTimeoutNeverLosesTheSettlement(t *testing.T) {
	b, clk := ackBroker(t)
	s := mustSub(t, b, "t", WithAckTimeout(ackTimeout), WithBuffer(8))
	for round := 0; round < 100; round++ {
		id := pub(t, b, "t", round)
		m := recvInFlight(t, s, 1)
		if m.ID != id {
			t.Fatalf("round %d: got %s want %s", round, m.ID, id)
		}
		var ackErr error
		var wg sync.WaitGroup
		wg.Add(2)
		go func() { defer wg.Done(); clk.Advance(ackTimeout) }()
		go func() { defer wg.Done(); ackErr = s.Ack(id) }()
		within(t, "Ack racing Advance", wg.Wait)
		if ackErr != nil {
			t.Fatalf("round %d: Ack = %v; whether the timeout or the ack ran first, the ack must succeed (the message was pending until it did)", round, ackErr)
		}
		// Whatever the interleaving, the next message arrives after at most
		// one stray copy of this one (attempt 2), never a third attempt.
		sentinel := pub(t, b, "t", "sentinel")
		for {
			x := recv(t, s)
			if x.ID == sentinel {
				_ = s.Ack(sentinel)
				break
			}
			if x.ID != id || x.Attempt != 2 {
				t.Fatalf("round %d: unexpected %+v while waiting for the sentinel", round, x)
			}
			if err := s.Ack(x.ID); !errors.Is(err, ErrNotPending) {
				t.Fatalf("round %d: Ack of the stray duplicate = %v, want ErrNotPending", round, err)
			}
		}
		waitFor(t, "InFlight() == 0", func() bool { return s.InFlight() == 0 })
	}
}

func TestStage2_OneTimerNoMatterHowManyMessagesAreInFlight(t *testing.T) {
	// A timer (or a goroutine) per message needs 5,000 of them here; the
	// reference uses one timer over a heap of deadlines.
	const n = 5_000
	baseline := runtime.NumGoroutine()
	b, clk := ackBroker(t)
	s := mustSub(t, b, "t", WithAckTimeout(ackTimeout), WithBuffer(n))
	ids := make([]string, 0, n)
	within(t, fmt.Sprintf("publishing and receiving %d messages", n), func() {
		for i := 0; i < n; i++ {
			ids = append(ids, pub(t, b, "t", i))
		}
		recvMany(t, s, n)
	})
	waitFor(t, "all messages in flight", func() bool { return s.InFlight() == n })
	if got := clk.active(); got != 1 {
		t.Fatalf("%d timers armed for %d in-flight messages, want exactly 1 (one timer for the earliest deadline)", got, n)
	}
	if g := runtime.NumGoroutine(); g > baseline+50 {
		t.Fatalf("%d goroutines running (baseline %d): do not spawn a goroutine per message", g, baseline)
	}

	within(t, "redelivering everything after one Advance", func() {
		clk.Advance(ackTimeout)
		for i, m := range recvMany(t, s, n) {
			if m.Attempt != 2 || m.ID != ids[i] {
				t.Errorf("redelivery %d = %s attempt %d, want %s attempt 2", i, m.ID, m.Attempt, ids[i])
				return
			}
		}
		for _, id := range ids {
			if err := s.Ack(id); err != nil {
				t.Errorf("Ack(%s) = %v", id, err)
				return
			}
		}
	})
	waitFor(t, "InFlight() == 0", func() bool { return s.InFlight() == 0 })
	if got := clk.active(); got != 0 {
		t.Fatalf("%d timers left armed with nothing pending", got)
	}
}

func TestStage2_CloseForgetsPendingWorkAndTimers(t *testing.T) {
	baseline := runtime.NumGoroutine()
	b, clk := ackBroker(t)
	s := mustSub(t, b, "t", WithAckTimeout(ackTimeout), WithBuffer(8))
	for i := 0; i < 5; i++ {
		pub(t, b, "t", i)
	}
	for i := 1; i <= 5; i++ {
		recvInFlight(t, s, i)
	}
	_ = b.Close()
	drain(t, s) // nothing queued: the channel just closes
	waitFor(t, "all timers to be cancelled after Close", func() bool { return clk.active() == 0 })
	clk.Advance(10 * ackTimeout) // must not resurrect anything or panic
	if err := s.Ack("m-1"); err == nil {
		t.Error("Ack on a closed subscription succeeded")
	}
	waitForGoroutines(t, baseline)
}

func TestStage2_RealClockRedeliversToo(t *testing.T) {
	// One test with the real clock and a real (short) timeout: the plumbing
	// between the default clock and the scheduler must work end to end.
	b := mustNew(t)
	s := mustSub(t, b, "t", WithAckTimeout(40*time.Millisecond))
	id := pub(t, b, "t", "x")
	if m := recv(t, s); m.Attempt != 1 {
		t.Fatalf("Attempt = %d", m.Attempt)
	}
	m := recv(t, s) // blocks until the real timer fires
	if m.ID != id || m.Attempt != 2 {
		t.Fatalf("redelivery = %+v", m)
	}
	if err := s.Ack(id); err != nil {
		t.Fatalf("Ack = %v", err)
	}
}
