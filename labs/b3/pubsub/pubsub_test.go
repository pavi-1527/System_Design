package pubsub

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"reflect"
	"runtime"
	"runtime/pprof"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const testTimeout = 5 * time.Second

// ---- helpers shared by all three stages ---------------------------------------

func mustNew(t *testing.T, opts ...Option) *Broker {
	t.Helper()
	b, err := New(opts...)
	if err != nil {
		t.Fatalf("New returned error %v", err)
	}
	if b == nil {
		t.Fatal("New returned a nil *Broker with a nil error")
	}
	t.Cleanup(func() { _ = b.Close() })
	return b
}

func mustSub(t *testing.T, b *Broker, topic string, opts ...SubOption) *Subscription {
	t.Helper()
	s, err := b.Subscribe(topic, opts...)
	if err != nil {
		t.Fatalf("Subscribe(%q) returned error %v", topic, err)
	}
	if s == nil {
		t.Fatalf("Subscribe(%q) returned a nil *Subscription with a nil error", topic)
	}
	t.Cleanup(func() {
		done := make(chan struct{})
		go func() { defer close(done); s.Unsubscribe() }()
		select {
		case <-done:
		case <-time.After(testTimeout):
			t.Errorf("Unsubscribe did not return within %v", testTimeout)
		}
	})
	return s
}

// within fails the test if f does not return within testTimeout, so a
// deadlock or an accidental O(n^2) fails instead of hanging the run.
func within(t *testing.T, what string, f func()) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		f()
	}()
	select {
	case <-done:
	case <-time.After(testTimeout):
		t.Fatalf("%s did not finish within %v (deadlock, or a cost that grows with the amount of state)", what, testTimeout)
	}
}

func pub(t *testing.T, b *Broker, topic string, payload any) string {
	t.Helper()
	return pubKey(t, b, topic, "", payload)
}

func pubKey(t *testing.T, b *Broker, topic, key string, payload any) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()
	id, err := b.Publish(ctx, topic, Message{Key: key, Payload: payload})
	if err != nil {
		t.Fatalf("Publish(%q, %v) returned error %v", topic, payload, err)
	}
	return id
}

func recv(t *testing.T, s *Subscription) Message {
	t.Helper()
	select {
	case m, ok := <-s.C():
		if !ok {
			t.Fatal("subscription channel is closed; expected a message")
		}
		return m
	case <-time.After(testTimeout):
		t.Fatalf("no message within %v", testTimeout)
		return Message{}
	}
}

// recvMany receives n messages under ONE deadline timer (recv arms one per
// call, which dominates the cost of the large tests).
func recvMany(t *testing.T, s *Subscription, n int) []Message {
	t.Helper()
	out := make([]Message, 0, n)
	timer := time.NewTimer(testTimeout)
	defer timer.Stop()
	for len(out) < n {
		select {
		case m, ok := <-s.C():
			if !ok {
				t.Fatalf("subscription channel closed after %d of %d messages", len(out), n)
			}
			out = append(out, m)
		case <-timer.C:
			t.Fatalf("received only %d of %d messages within %v", len(out), n, testTimeout)
		}
	}
	return out
}

func recvPayloads(t *testing.T, s *Subscription, n int) []any {
	t.Helper()
	out := make([]any, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, recv(t, s).Payload)
	}
	return out
}

// expectNone asserts that nothing arrives. This is the one place the suite
// waits in real time (20 ms) because "nothing happens" cannot be observed
// any other way.
func expectNone(t *testing.T, s *Subscription, why string) {
	t.Helper()
	select {
	case m, ok := <-s.C():
		if ok {
			t.Fatalf("unexpected message %+v: %s", m, why)
		}
		t.Fatalf("channel closed unexpectedly: %s", why)
	case <-time.After(20 * time.Millisecond):
	}
}

// expectBlocked asserts that done stays open for a short real time.
func expectBlocked(t *testing.T, done <-chan struct{}, why string) {
	t.Helper()
	select {
	case <-done:
		t.Fatalf("returned too early: %s", why)
	case <-time.After(30 * time.Millisecond):
	}
}

// waitFor polls cond until it holds. Used for state that settles
// asynchronously a few microseconds after an event (for example the
// subscription's accounting right after the consumer received a message).
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(testTimeout)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out after %v waiting for %s", testTimeout, what)
		}
		time.Sleep(time.Millisecond)
	}
}

// drain reads until the channel is closed and returns what it saw; it fails
// the test if the channel never closes.
func drain(t *testing.T, s *Subscription) []Message {
	t.Helper()
	var out []Message
	deadline := time.After(testTimeout)
	for {
		select {
		case m, ok := <-s.C():
			if !ok {
				return out
			}
			out = append(out, m)
		case <-deadline:
			t.Fatalf("channel not closed within %v (got %d messages so far)", testTimeout, len(out))
			return out
		}
	}
}

func waitForGoroutines(t *testing.T, baseline int) {
	t.Helper()
	deadline := time.Now().Add(testTimeout)
	for {
		n := runtime.NumGoroutine()
		if n <= baseline {
			return
		}
		if time.Now().After(deadline) {
			var buf bytes.Buffer
			_ = pprof.Lookup("goroutine").WriteTo(&buf, 1)
			t.Fatalf("goroutine leak: %d goroutines still running, baseline was %d. Live stacks:\n%s", n, baseline, buf.String())
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// ---- stage 1 ----------------------------------------------------------------

func TestStage1_RejectsBadArguments(t *testing.T) {
	b := mustNew(t)
	if _, err := b.Subscribe(""); !errors.Is(err, ErrInvalidTopic) {
		t.Errorf("Subscribe(\"\") error = %v, want ErrInvalidTopic", err)
	}
	if _, err := b.Subscribe("t", WithBuffer(0)); !errors.Is(err, ErrInvalidOption) {
		t.Errorf("WithBuffer(0) error = %v, want ErrInvalidOption: a zero-length queue would block every publish forever", err)
	}
	if _, err := b.Subscribe("t", WithPolicy(OverflowPolicy(42))); !errors.Is(err, ErrInvalidOption) {
		t.Errorf("unknown policy error = %v, want ErrInvalidOption", err)
	}
	if _, err := b.Publish(context.Background(), "", Message{}); !errors.Is(err, ErrInvalidTopic) {
		t.Errorf("Publish to \"\" error = %v, want ErrInvalidTopic", err)
	}
}

func TestStage1_FanOutAndIDs(t *testing.T) {
	t.Run("PublishFansOutToEverySubscriber", stage1PublishFansOutToEverySubscriber)
	t.Run("IDsAreUnique", stage1IDsAreUnique)
	t.Run("NoSubscribersIsNotAnError", stage1NoSubscribersIsNotAnError)
}

func stage1PublishFansOutToEverySubscriber(t *testing.T) {
	b := mustNew(t)
	subs := []*Subscription{mustSub(t, b, "orders"), mustSub(t, b, "orders"), mustSub(t, b, "orders")}
	other := mustSub(t, b, "refunds")

	idA := pubKey(t, b, "orders", "acct-1", "a")
	idB := pub(t, b, "orders", "b")

	for i, s := range subs {
		a, bb := recv(t, s), recv(t, s)
		if a.Payload != "a" || bb.Payload != "b" {
			t.Fatalf("subscriber %d got %v then %v, want a then b", i, a.Payload, bb.Payload)
		}
		if a.ID != idA || bb.ID != idB {
			t.Errorf("subscriber %d saw IDs %q,%q, Publish returned %q,%q: every subscriber must see the broker-assigned ID, it is the consumers' dedupe key", i, a.ID, bb.ID, idA, idB)
		}
		if a.Topic != "orders" || a.Key != "acct-1" || a.Attempt != 1 {
			t.Errorf("subscriber %d: Topic=%q Key=%q Attempt=%d, want orders/acct-1/1", i, a.Topic, a.Key, a.Attempt)
		}
	}
	expectNone(t, other, "a subscriber of another topic must not receive orders messages")
}

func stage1IDsAreUnique(t *testing.T) {
	b := mustNew(t)
	s := mustSub(t, b, "t", WithBuffer(200))
	seen := map[string]bool{}
	for i := 0; i < 100; i++ {
		id := pub(t, b, "t", i)
		if id == "" || seen[id] {
			t.Fatalf("Publish #%d returned ID %q (empty or already used): IDs identify messages for dedupe", i, id)
		}
		seen[id] = true
	}
	for i := 0; i < 100; i++ {
		if m := recv(t, s); !seen[m.ID] {
			t.Fatalf("delivered ID %q was never returned by Publish", m.ID)
		}
	}
}

func stage1NoSubscribersIsNotAnError(t *testing.T) {
	b := mustNew(t)
	if id := pub(t, b, "nobody-listens", 1); id == "" {
		t.Error("Publish to a topic without subscribers should still return an ID")
	}
}

func TestStage1_OrderIsPreservedPerSubscriber(t *testing.T) {
	const n = 2000
	b := mustNew(t)
	// Tiny buffers force constant backpressure: the order must survive it.
	subs := []*Subscription{mustSub(t, b, "t", WithBuffer(4)), mustSub(t, b, "t", WithBuffer(1))}
	results := make([][]int, len(subs))
	var wg sync.WaitGroup
	for i, s := range subs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for len(results[i]) < n {
				select {
				case m := <-s.C():
					results[i] = append(results[i], m.Payload.(int))
				case <-time.After(testTimeout):
					return
				}
			}
		}()
	}
	within(t, "publishing 2000 ordered messages", func() {
		for i := 0; i < n; i++ {
			if _, err := b.Publish(context.Background(), "t", Message{Payload: i}); err != nil {
				t.Errorf("Publish(%d): %v", i, err)
				return
			}
		}
	})
	within(t, "collecting", wg.Wait)
	for si, got := range results {
		if len(got) != n {
			t.Fatalf("subscriber %d received %d messages, want %d", si, len(got), n)
		}
		for i, v := range got {
			if v != i {
				t.Fatalf("subscriber %d: position %d holds %d; one publisher's messages must arrive in publish order", si, i, v)
			}
		}
	}
}

func TestStage1_DropNewestKeepsOldestAndCounts(t *testing.T) {
	b := mustNew(t)
	s := mustSub(t, b, "t", WithBuffer(3), WithPolicy(DropNewest))
	for i := 1; i <= 10; i++ {
		pub(t, b, "t", i) // never blocks: DropNewest is the "do not stall the publisher" policy
	}
	if got := s.Dropped(); got != 7 {
		t.Fatalf("Dropped() = %d, want 7 (10 published, 3 fit)", got)
	}
	if got := s.Len(); got != 3 {
		t.Fatalf("Len() = %d, want 3", got)
	}
	if got := recvPayloads(t, s, 3); !reflect.DeepEqual(got, []any{1, 2, 3}) {
		t.Fatalf("received %v, want [1 2 3]: DropNewest keeps the oldest", got)
	}
	waitFor(t, "the queue to empty after the consumer caught up", func() bool { return s.Len() == 0 })
	pub(t, b, "t", 11)
	if got := recv(t, s).Payload; got != 11 {
		t.Fatalf("after catching up, received %v, want 11: dropping must not wedge the subscription", got)
	}
	if got := s.Dropped(); got != 7 {
		t.Fatalf("Dropped() = %d after the catch-up, want 7", got)
	}
}

func TestStage1_DropOldestKeepsNewest(t *testing.T) {
	b := mustNew(t)
	s := mustSub(t, b, "t", WithBuffer(3), WithPolicy(DropOldest))
	for i := 1; i <= 6; i++ {
		pub(t, b, "t", i)
	}
	if got := s.Dropped(); got != 3 {
		t.Fatalf("Dropped() = %d, want 3", got)
	}
	if got := recvPayloads(t, s, 3); !reflect.DeepEqual(got, []any{4, 5, 6}) {
		t.Fatalf("received %v, want [4 5 6]: DropOldest keeps the freshest (think telemetry, not payments)", got)
	}
}

func TestStage1_BlockPolicy(t *testing.T) {
	t.Run("BlockMakesThePublisherWaitForTheConsumer", stage1BlockMakesThePublisherWaitForTheConsumer)
	t.Run("BlockedPublishHonoursItsContext", stage1BlockedPublishHonoursItsContext)
}

func stage1BlockMakesThePublisherWaitForTheConsumer(t *testing.T) {
	b := mustNew(t)
	s := mustSub(t, b, "t", WithBuffer(1), WithPolicy(Block))
	pub(t, b, "t", "first") // fills the queue

	done := make(chan struct{})
	go func() {
		defer close(done)
		pub(t, b, "t", "second")
	}()
	expectBlocked(t, done, "the queue is full and the policy is Block: Publish must wait")

	if got := recv(t, s).Payload; got != "first" {
		t.Fatalf("got %v, want first", got)
	}
	select {
	case <-done:
	case <-time.After(testTimeout):
		t.Fatal("Publish stayed blocked after the consumer made room")
	}
	if got := recv(t, s).Payload; got != "second" {
		t.Fatalf("got %v, want second", got)
	}
	if s.Dropped() != 0 {
		t.Fatalf("Dropped() = %d under Block, want 0: Block never loses data", s.Dropped())
	}
}

func stage1BlockedPublishHonoursItsContext(t *testing.T) {
	b := mustNew(t)
	s := mustSub(t, b, "t", WithBuffer(1))
	pub(t, b, "t", "first")

	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() {
		_, err := b.Publish(ctx, "t", Message{Payload: "second"})
		errc <- err
	}()
	select {
	case err := <-errc:
		t.Fatalf("Publish returned %v while the queue was full", err)
	case <-time.After(30 * time.Millisecond):
	}
	cancel()
	select {
	case err := <-errc:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Publish error = %v, want context.Canceled: a dead subscriber must not pin publishers forever", err)
		}
	case <-time.After(testTimeout):
		t.Fatal("Publish ignored its context while blocked")
	}
	if _, err := b.Publish(canceledCtx(), "t", Message{}); !errors.Is(err, context.Canceled) {
		t.Errorf("Publish with an already-cancelled ctx error = %v, want context.Canceled", err)
	}
	if got := recv(t, s).Payload; got != "first" {
		t.Fatalf("got %v, want first", got)
	}
	expectNone(t, s, "the cancelled publish must not be delivered late")
}

func canceledCtx() context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return ctx
}

func TestStage1_StuckSubscribersDoNotStallOthers(t *testing.T) {
	t.Run("StuckBlockingSubscriberDoesNotStallOthers", stage1StuckBlockingSubscriberDoesNotStallOthers)
	t.Run("TwoBlockedSubscribersAreWaitedForTogether", stage1TwoBlockedSubscribersAreWaitedForTogether)
	t.Run("StuckDroppingSubscriberDoesNotStallOthers", stage1StuckDroppingSubscriberDoesNotStallOthers)
}

func stage1StuckBlockingSubscriberDoesNotStallOthers(t *testing.T) {
	b := mustNew(t)
	stuck := mustSub(t, b, "t", WithBuffer(1), WithPolicy(Block)) // subscribed FIRST, never reads
	healthy := mustSub(t, b, "t", WithBuffer(8))
	pub(t, b, "t", "m1") // fills stuck's queue

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errc := make(chan error, 1)
	go func() {
		_, err := b.Publish(ctx, "t", Message{Payload: "m2"})
		errc <- err
	}()

	// While the publisher is blocked on the stuck subscriber, the healthy one
	// must already have BOTH messages.
	if got := recvPayloads(t, healthy, 2); !reflect.DeepEqual(got, []any{"m1", "m2"}) {
		t.Fatalf("healthy subscriber got %v, want [m1 m2]", got)
	}
	select {
	case err := <-errc:
		t.Fatalf("Publish returned %v although the stuck Block subscriber has no room", err)
	default:
	}
	cancel()
	if err := <-errc; !errors.Is(err, context.Canceled) {
		t.Fatalf("Publish error = %v, want context.Canceled", err)
	}
	if stuck.Len() != 1 {
		t.Fatalf("stuck.Len() = %d, want 1: the cancelled publish must not have been enqueued for it", stuck.Len())
	}
}

func stage1TwoBlockedSubscribersAreWaitedForTogether(t *testing.T) {
	// Both subscribers are full. B's consumer is alive; A's is dead. If the
	// publisher waits for A first and then for B, B never gets the message
	// before the context runs out.
	b := mustNew(t)
	dead := mustSub(t, b, "t", WithBuffer(1))
	alive := mustSub(t, b, "t", WithBuffer(1))
	pub(t, b, "t", "m1")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errc := make(chan error, 1)
	go func() {
		_, err := b.Publish(ctx, "t", Message{Payload: "m2"})
		errc <- err
	}()
	select { // let the publisher find both queues full before anyone makes room
	case err := <-errc:
		t.Fatalf("Publish returned %v while both queues were full", err)
	case <-time.After(30 * time.Millisecond):
	}
	if got := recv(t, alive).Payload; got != "m1" {
		t.Fatalf("got %v, want m1", got)
	}
	if got := recv(t, alive).Payload; got != "m2" {
		t.Fatalf("alive subscriber got %v, want m2: waiting on the dead subscriber first starves everyone behind it", got)
	}
	cancel()
	if err := <-errc; !errors.Is(err, context.Canceled) {
		t.Fatalf("Publish error = %v, want context.Canceled (the dead subscriber never made room)", err)
	}
	_ = dead
}

func stage1StuckDroppingSubscriberDoesNotStallOthers(t *testing.T) {
	const n = 500
	b := mustNew(t)
	stuck := mustSub(t, b, "t", WithBuffer(1), WithPolicy(DropNewest))
	healthy := mustSub(t, b, "t", WithBuffer(64))
	var got atomic.Int64
	go func() {
		for range healthy.C() {
			got.Add(1)
		}
	}()
	within(t, fmt.Sprintf("publishing %d messages past a stuck subscriber", n), func() {
		for i := 0; i < n; i++ {
			pub(t, b, "t", i)
		}
	})
	waitFor(t, "the healthy subscriber to receive everything", func() bool { return got.Load() == n })
	if d := stuck.Dropped(); d != n-1 {
		t.Fatalf("stuck.Dropped() = %d, want %d", d, n-1)
	}
}

func TestStage1_Unsubscribe(t *testing.T) {
	t.Run("UnsubscribeClosesTheChannelAndStopsDelivery", stage1UnsubscribeClosesTheChannelAndStopsDelivery)
	t.Run("UnsubscribeReleasesABlockedPublisher", stage1UnsubscribeReleasesABlockedPublisher)
}

func stage1UnsubscribeClosesTheChannelAndStopsDelivery(t *testing.T) {
	b := mustNew(t)
	gone := mustSub(t, b, "t")
	stays := mustSub(t, b, "t")
	pub(t, b, "t", 1)
	gone.Unsubscribe()
	gone.Unsubscribe() // idempotent
	drain(t, gone)     // must terminate: the channel is closed
	pub(t, b, "t", 2)
	if got := recvPayloads(t, stays, 2); !reflect.DeepEqual(got, []any{1, 2}) {
		t.Fatalf("remaining subscriber got %v, want [1 2]", got)
	}
}

func stage1UnsubscribeReleasesABlockedPublisher(t *testing.T) {
	b := mustNew(t)
	s := mustSub(t, b, "t", WithBuffer(1))
	pub(t, b, "t", "first")
	done := make(chan error, 1)
	go func() {
		_, err := b.Publish(context.Background(), "t", Message{Payload: "second"}) // no deadline: only Unsubscribe can free it
		done <- err
	}()
	select {
	case err := <-done:
		t.Fatalf("Publish returned %v while the queue was full", err)
	case <-time.After(30 * time.Millisecond):
	}
	s.Unsubscribe()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Publish error = %v after the subscriber left, want nil: a departed subscriber is not the publisher's problem", err)
		}
	case <-time.After(testTimeout):
		t.Fatal("Publish stayed blocked on a subscription that was unsubscribed")
	}
	drain(t, s)
}

func TestStage1_Close(t *testing.T) {
	t.Run("CloseDrainsQueuedMessagesThenClosesChannels", stage1CloseDrainsQueuedMessagesThenClosesChannels)
	t.Run("CloseReleasesBlockedPublishers", stage1CloseReleasesBlockedPublishers)
}

func stage1CloseDrainsQueuedMessagesThenClosesChannels(t *testing.T) {
	b := mustNew(t)
	s1 := mustSub(t, b, "t", WithBuffer(10))
	s2 := mustSub(t, b, "t", WithBuffer(10))
	for i := 0; i < 5; i++ {
		pub(t, b, "t", i)
	}
	if err := b.Close(); err != nil {
		t.Fatalf("Close() = %v", err)
	}
	if err := b.Close(); err != nil {
		t.Fatalf("second Close() = %v, want nil (idempotent)", err)
	}
	if _, err := b.Publish(context.Background(), "t", Message{}); !errors.Is(err, ErrClosed) {
		t.Errorf("Publish after Close error = %v, want ErrClosed", err)
	}
	if _, err := b.Subscribe("t"); !errors.Is(err, ErrClosed) {
		t.Errorf("Subscribe after Close error = %v, want ErrClosed", err)
	}
	for i, s := range []*Subscription{s1, s2} {
		msgs := drain(t, s)
		if len(msgs) != 5 {
			t.Fatalf("subscriber %d drained %d messages, want 5: Close must not throw away what was already accepted", i, len(msgs))
		}
		for j, m := range msgs {
			if m.Payload != j {
				t.Fatalf("subscriber %d: position %d holds %v", i, j, m.Payload)
			}
		}
	}
}

func stage1CloseReleasesBlockedPublishers(t *testing.T) {
	b := mustNew(t)
	mustSub(t, b, "t", WithBuffer(1))
	pub(t, b, "t", "first")
	errc := make(chan error, 1)
	go func() {
		_, err := b.Publish(context.Background(), "t", Message{Payload: "second"})
		errc <- err
	}()
	select {
	case err := <-errc:
		t.Fatalf("Publish returned %v while the queue was full", err)
	case <-time.After(30 * time.Millisecond):
	}
	_ = b.Close()
	select {
	case err := <-errc:
		if !errors.Is(err, ErrClosed) {
			t.Fatalf("blocked Publish error = %v after Close, want ErrClosed", err)
		}
	case <-time.After(testTimeout):
		t.Fatal("Close left a publisher blocked forever")
	}
}

func TestStage1_CloseUnsubscribeAndPublishRacingNeverPanic(t *testing.T) {
	// Each round hammers one broker with publishers, a Close storm and an
	// Unsubscribe storm at once. A send on a closed channel, a double close
	// or a deadlock shows up as a panic, a -race report or a timeout.
	baseline := runtime.NumGoroutine()
	for round := 0; round < 40; round++ {
		b := mustNew(t)
		var subs []*Subscription
		for i := 0; i < 8; i++ {
			policy := Block
			if i%2 == 0 {
				policy = DropNewest
			}
			s, err := b.Subscribe("t", WithBuffer(2), WithPolicy(policy))
			if err != nil {
				t.Fatalf("Subscribe: %v", err)
			}
			subs = append(subs, s)
		}
		var wg sync.WaitGroup
		for p := 0; p < 4; p++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for i := 0; i < 50; i++ {
					ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
					_, _ = b.Publish(ctx, "t", Message{Payload: i})
					cancel()
				}
			}()
		}
		for c := 0; c < 4; c++ {
			wg.Add(1)
			go func() { defer wg.Done(); _ = b.Close() }()
		}
		for _, s := range subs {
			wg.Add(1)
			go func() { defer wg.Done(); s.Unsubscribe() }()
		}
		within(t, "the Close/Unsubscribe/Publish storm", wg.Wait)
		for _, s := range subs {
			drain(t, s)
		}
	}
	waitForGoroutines(t, baseline)
}

func TestStage1_ChurnNeverCostsAStableSubscriberAMessage(t *testing.T) {
	const publishers, perPublisher = 6, 400
	b := mustNew(t)
	stable := mustSub(t, b, "t", WithBuffer(32), WithPolicy(Block))
	var got atomic.Int64
	go func() {
		for range stable.C() {
			got.Add(1)
		}
	}()

	stop := make(chan struct{})
	var churn sync.WaitGroup
	for c := 0; c < 4; c++ {
		churn.Add(1)
		go func() {
			defer churn.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				s, err := b.Subscribe("t", WithBuffer(1), WithPolicy(DropNewest))
				if err != nil {
					return
				}
				select {
				case <-s.C():
				case <-time.After(time.Millisecond):
				}
				s.Unsubscribe()
			}
		}()
	}
	var wg sync.WaitGroup
	for p := 0; p < publishers; p++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < perPublisher; i++ {
				if _, err := b.Publish(context.Background(), "t", Message{Payload: i}); err != nil {
					t.Errorf("Publish: %v", err)
					return
				}
			}
		}()
	}
	within(t, "publishing while subscribers churn", wg.Wait)
	close(stop)
	within(t, "stopping the churn", churn.Wait)
	waitFor(t, "the stable subscriber to receive every message", func() bool { return got.Load() == publishers*perPublisher })
}

func TestStage1_NoGoroutineLeak(t *testing.T) {
	baseline := runtime.NumGoroutine()
	for round := 0; round < 10; round++ {
		b, err := New()
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		var subs []*Subscription
		for i := 0; i < 10; i++ {
			s, err := b.Subscribe("t", WithBuffer(2), WithPolicy(OverflowPolicy(i%3)))
			if err != nil {
				t.Fatalf("Subscribe: %v", err)
			}
			subs = append(subs, s)
		}
		for i := 0; i < 6; i++ {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Millisecond)
			_, _ = b.Publish(ctx, "t", Message{Payload: i})
			cancel()
		}
		for i, s := range subs {
			if i%2 == 0 {
				s.Unsubscribe()
			}
		}
		_ = b.Close()
		for i, s := range subs {
			if i%2 == 1 {
				drain(t, s)
			}
		}
	}
	waitForGoroutines(t, baseline)
}

func TestStage1_PublishCostDoesNotGrowWithOtherTopics(t *testing.T) {
	// 3000 topics with one subscriber each. A Publish that walks every
	// subscription (or every topic) does 3000 x 100000 = 3e8 steps and misses
	// the cap; a map lookup per topic finishes in a fraction of a second.
	const topics, publishes = 3000, 100_000
	b := mustNew(t)
	names := make([]string, topics)
	for i := range names {
		names[i] = fmt.Sprintf("topic-%d", i)
		mustSub(t, b, names[i], WithBuffer(1), WithPolicy(DropNewest))
	}
	within(t, fmt.Sprintf("%d publishes across %d topics", publishes, topics), func() {
		ctx := context.Background()
		for i := 0; i < publishes; i++ {
			if _, err := b.Publish(ctx, names[(i*7919)%topics], Message{Payload: i}); err != nil {
				t.Errorf("Publish: %v", err)
				return
			}
		}
	})
}
