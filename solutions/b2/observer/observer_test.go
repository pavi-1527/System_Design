package observer

import (
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const testTimeout = 5 * time.Second

// recorder is a subscriber that appends the Key of every event it sees.
type recorder struct {
	mu   sync.Mutex
	keys []string
}

func (r *recorder) on(e Event) {
	r.mu.Lock()
	r.keys = append(r.keys, e.Key)
	r.mu.Unlock()
}

func (r *recorder) got() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.keys...)
}

// within runs f and fails the test if it does not finish in time (a deadlock
// must fail with a message, not hang CI).
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
		t.Fatalf("%s did not finish within %v: deadlock? (does the bus hold its lock while calling subscribers?)", what, testTimeout)
	}
}

func TestPublish_DeliversToTopicSubscribersInSubscriptionOrder(t *testing.T) {
	b := NewBus()
	var order []string
	var mu sync.Mutex
	add := func(name string) func(Event) {
		return func(Event) { mu.Lock(); order = append(order, name); mu.Unlock() }
	}
	b.Subscribe("settled", add("audit"))
	b.Subscribe("settled", add("notify"))
	b.Subscribe("failed", add("alert"))
	b.Subscribe("settled", add("fraud"))
	if err := b.Publish(Event{Topic: "settled", Key: "t1"}); err != nil {
		t.Fatalf("Publish returned %v", err)
	}
	if want := []string{"audit", "notify", "fraud"}; !reflect.DeepEqual(order, want) {
		t.Errorf("delivery order = %v, want %v (other topics must not fire)", order, want)
	}
}

func TestPublish_WithNoSubscribersIsANoOp(t *testing.T) {
	if err := NewBus().Publish(Event{Topic: "nobody-listens"}); err != nil {
		t.Errorf("Publish with no subscribers = %v, want nil", err)
	}
}

func TestUnsubscribe_StopsDeliveryAndIsIdempotent(t *testing.T) {
	b := NewBus()
	var a, bb, c recorder
	b.Subscribe("t", a.on)
	unsubB := b.Subscribe("t", bb.on)
	b.Subscribe("t", c.on)

	_ = b.Publish(Event{Topic: "t", Key: "1"})
	unsubB()
	unsubB() // a second call must not remove someone else (for example by a stale slice index)
	unsubB()
	_ = b.Publish(Event{Topic: "t", Key: "2"})

	if got := a.got(); !reflect.DeepEqual(got, []string{"1", "2"}) {
		t.Errorf("first subscriber saw %v, want [1 2]", got)
	}
	if got := bb.got(); !reflect.DeepEqual(got, []string{"1"}) {
		t.Errorf("unsubscribed subscriber saw %v, want only [1]", got)
	}
	if got := c.got(); !reflect.DeepEqual(got, []string{"1", "2"}) {
		t.Errorf("third subscriber saw %v, want [1 2]: repeated unsubscribe removed the wrong subscriber", got)
	}
}

func TestCallbacks_MayReenterTheBus(t *testing.T) {
	b := NewBus()
	var late, other recorder
	var unsubSelf func()
	var inner atomic.Int32

	unsubSelf = b.Subscribe("t", func(e Event) {
		inner.Add(1)
		b.Subscribe("t", late.on)                                  // subscribe from a callback
		unsubSelf()                                                // unsubscribe from a callback
		_ = b.Publish(Event{Topic: "other", Key: "from-callback"}) // publish from a callback
	})
	b.Subscribe("other", other.on)

	within(t, "Publish with a re-entrant subscriber", func() {
		_ = b.Publish(Event{Topic: "t", Key: "1"})
	})
	if got := late.got(); len(got) != 0 {
		t.Errorf("subscriber added during Publish saw the in-flight event %v; it should only see later events", got)
	}
	if got := other.got(); !reflect.DeepEqual(got, []string{"from-callback"}) {
		t.Errorf("nested Publish delivered %v, want [from-callback]", got)
	}

	within(t, "second Publish", func() { _ = b.Publish(Event{Topic: "t", Key: "2"}) })
	if n := inner.Load(); n != 1 {
		t.Errorf("self-unsubscribing callback ran %d times, want 1", n)
	}
	if got := late.got(); !reflect.DeepEqual(got, []string{"2"}) {
		t.Errorf("late subscriber saw %v, want [2]", got)
	}
}

func TestSlowSubscriber_DoesNotBlockOtherTopicsOrSubscriptions(t *testing.T) {
	b := NewBus()
	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	releaseAll := func() { once.Do(func() { close(release) }) }
	defer releaseAll()
	b.Subscribe("slow", func(Event) {
		close(entered)
		<-release
	})
	var fast recorder
	b.Subscribe("fast", fast.on)

	pubDone := make(chan struct{})
	go func() {
		_ = b.Publish(Event{Topic: "slow", Key: "s"})
		close(pubDone)
	}()
	select {
	case <-entered:
	case <-pubDone:
		t.Fatal("Publish returned without calling the slow subscriber")
	case <-time.After(testTimeout):
		t.Fatal("slow subscriber was never called")
	}

	within(t, "Publish/Subscribe on another topic while a slow callback runs", func() {
		b.Subscribe("fast", func(Event) {})
		_ = b.Publish(Event{Topic: "fast", Key: "f"})
	})
	if got := fast.got(); !reflect.DeepEqual(got, []string{"f"}) {
		t.Errorf("fast subscriber saw %v, want [f]", got)
	}
	releaseAll()
	select {
	case <-pubDone:
	case <-time.After(testTimeout):
		t.Fatal("slow Publish never finished after release")
	}
}

func TestPublish_ContainsSubscriberPanics(t *testing.T) {
	b := NewBus()
	var before, after recorder
	b.Subscribe("t", before.on)
	b.Subscribe("t", func(Event) { panic("audit sink exploded") })
	b.Subscribe("t", after.on)

	err := b.Publish(Event{Topic: "t", Key: "1"})
	if err == nil || !strings.Contains(err.Error(), "audit sink exploded") {
		t.Errorf("Publish error = %v, want one mentioning the panic value", err)
	}
	if got := after.got(); !reflect.DeepEqual(got, []string{"1"}) {
		t.Errorf("subscriber after the panicking one saw %v, want [1]: one bad observer must not starve the rest", got)
	}
	if got := before.got(); !reflect.DeepEqual(got, []string{"1"}) {
		t.Errorf("subscriber before the panicking one saw %v, want [1]", got)
	}
	if err := b.Publish(Event{Topic: "t", Key: "2"}); err == nil {
		t.Error("second Publish returned nil error, but the panicking subscriber is still registered")
	}
}

func TestBus_ConcurrentPublishSubscribeUnsubscribe(t *testing.T) {
	b := NewBus()
	var stable atomic.Int64
	b.Subscribe("t", func(Event) { stable.Add(1) })

	const publishers, perPublisher = 8, 500
	var wg sync.WaitGroup
	for p := 0; p < publishers; p++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < perPublisher; i++ {
				_ = b.Publish(Event{Topic: "t"})
			}
		}()
	}
	for s := 0; s < 4; s++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < perPublisher; i++ {
				unsub := b.Subscribe("t", func(Event) {})
				unsub()
			}
		}()
	}
	within(t, "concurrent publish/subscribe/unsubscribe", wg.Wait)
	if got := stable.Load(); got != publishers*perPublisher {
		t.Errorf("stable subscriber saw %d events, want %d: events were lost while others subscribed/unsubscribed", got, publishers*perPublisher)
	}
}

func TestSubscribeChan_DeliversInOrderThenCloses(t *testing.T) {
	b := NewBus()
	sub := b.SubscribeChan("t", 8)
	for _, k := range []string{"a", "b", "c"} {
		_ = b.Publish(Event{Topic: "t", Key: k})
	}
	sub.Cancel()
	var got []string
	within(t, "draining C", func() {
		for e := range sub.C { // C must be closed by Cancel or this never ends
			got = append(got, e.Key)
		}
	})
	if !reflect.DeepEqual(got, []string{"a", "b", "c"}) {
		t.Errorf("channel subscriber saw %v, want [a b c]", got)
	}
}

func TestSubscribeChan_SlowConsumerNeverBlocksPublish(t *testing.T) {
	b := NewBus()
	sub := b.SubscribeChan("t", 2)
	defer sub.Cancel()
	within(t, "10 Publishes to a subscriber that never reads", func() {
		for i := 0; i < 10; i++ {
			_ = b.Publish(Event{Topic: "t", Key: string(rune('a' + i))})
		}
	})
	if got := sub.Dropped(); got != 8 {
		t.Errorf("Dropped() = %d, want 8 (buffer 2, 10 published, nobody reading)", got)
	}
	if a, c := (<-sub.C).Key, (<-sub.C).Key; a != "a" || c != "b" {
		t.Errorf("kept events %q,%q; want the OLDEST two (a, b): drop the newest when full", a, c)
	}
}

func TestSubscribeChan_ZeroBufferStillReceives(t *testing.T) {
	b := NewBus()
	sub := b.SubscribeChan("t", 0)
	defer sub.Cancel()
	_ = b.Publish(Event{Topic: "t", Key: "x"})
	select {
	case e := <-sub.C:
		if e.Key != "x" {
			t.Errorf("got %q, want x", e.Key)
		}
	case <-time.After(testTimeout):
		t.Fatal("buf 0 must behave as buf 1; an unbuffered channel would drop every event")
	}
}

func TestSubscribeChan_CancelRacingPublishNeverPanics(t *testing.T) {
	for round := 0; round < 200; round++ {
		b := NewBus()
		sub := b.SubscribeChan("t", 1)
		var wg sync.WaitGroup
		for p := 0; p < 4; p++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for i := 0; i < 50; i++ {
					_ = b.Publish(Event{Topic: "t"})
				}
			}()
		}
		for c := 0; c < 3; c++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				sub.Cancel() // concurrent and repeated: close exactly once
			}()
		}
		within(t, "publishers racing Cancel", wg.Wait)
		within(t, "draining C after Cancel (is C closed exactly once?)", func() {
			for range sub.C {
			}
		})
	}
}

func TestSubscribeChan_NoEventsAfterCancel(t *testing.T) {
	b := NewBus()
	sub := b.SubscribeChan("t", 4)
	sub.Cancel()
	_ = b.Publish(Event{Topic: "t", Key: "late"})
	if e, ok := <-sub.C; ok {
		t.Errorf("received %v after Cancel; the channel should be closed and empty", e)
	}
	if sub.Dropped() != 0 {
		t.Errorf("Dropped() = %d after Cancel, want 0: a cancelled subscription is no longer a subscriber", sub.Dropped())
	}
}
