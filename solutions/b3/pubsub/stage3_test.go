package pubsub

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// collect starts a goroutine that appends every message from s to a slice.
type collector struct {
	mu   sync.Mutex
	msgs []Message
}

func collect(s *Subscription) *collector {
	c := &collector{}
	go func() {
		for m := range s.C() {
			c.mu.Lock()
			c.msgs = append(c.msgs, m)
			c.mu.Unlock()
		}
	}()
	return c
}

func (c *collector) snapshot() []Message {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]Message(nil), c.msgs...)
}

func (c *collector) len() int { return len(c.snapshot()) }

func TestStage3_Groups(t *testing.T) {
	t.Run("GroupMembersCompeteForMessages", stage3GroupMembersCompeteForMessages)
	t.Run("EveryGroupAndEveryPlainSubscriberGetsEveryMessage", stage3EveryGroupAndEveryPlainSubscriberGetsEveryMessage)
	t.Run("SameKeyAlwaysGoesToTheSameMember", stage3SameKeyAlwaysGoesToTheSameMember)
	t.Run("RejectsEmptyGroupName", stage3RejectsEmptyGroupName)
}

func stage3GroupMembersCompeteForMessages(t *testing.T) {
	const members, n = 3, 300
	b := mustNew(t)
	var cols []*collector
	for i := 0; i < members; i++ {
		cols = append(cols, collect(mustSub(t, b, "jobs", WithGroup("workers"), WithBuffer(n))))
	}
	for i := 0; i < n; i++ {
		pub(t, b, "jobs", i)
	}
	waitFor(t, "all messages to be consumed", func() bool {
		total := 0
		for _, c := range cols {
			total += c.len()
		}
		return total >= n
	})
	seen := map[string]int{}
	for i, c := range cols {
		got := c.snapshot()
		if len(got) < n/(members*2) {
			t.Errorf("member %d received only %d of %d messages: unkeyed messages should spread across the group", i, len(got), n)
		}
		for _, m := range got {
			seen[m.ID]++
		}
	}
	if len(seen) != n {
		t.Fatalf("group received %d distinct messages, want %d", len(seen), n)
	}
	for id, c := range seen {
		if c != 1 {
			t.Fatalf("message %s went to %d members of one group, want exactly 1 (competing consumers)", id, c)
		}
	}
}

func stage3EveryGroupAndEveryPlainSubscriberGetsEveryMessage(t *testing.T) {
	const n = 40
	b := mustNew(t)
	var billing, audit []*collector
	for i := 0; i < 2; i++ {
		billing = append(billing, collect(mustSub(t, b, "events", WithGroup("billing"), WithBuffer(n))))
	}
	audit = append(audit, collect(mustSub(t, b, "events", WithGroup("audit"), WithBuffer(n))))
	plain := collect(mustSub(t, b, "events", WithBuffer(n)))
	for i := 0; i < n; i++ {
		pub(t, b, "events", i)
	}
	count := func(cs []*collector) int {
		total := 0
		for _, c := range cs {
			total += c.len()
		}
		return total
	}
	waitFor(t, "delivery", func() bool { return count(billing) == n && count(audit) == n && plain.len() == n })
	time.Sleep(20 * time.Millisecond) // a bug that over-delivers would show up as more than n
	if count(billing) != n || count(audit) != n || plain.len() != n {
		t.Fatalf("billing=%d audit=%d plain=%d, want %d each: a group is ONE logical consumer, but every group (and every ungrouped subscriber) gets everything", count(billing), count(audit), plain.len(), n)
	}
}

func stage3SameKeyAlwaysGoesToTheSameMember(t *testing.T) {
	const members, keys, perKey = 3, 10, 8 // 10 keys over 3 members: a round-robin router would scatter them
	b := mustNew(t)
	var cols []*collector
	for i := 0; i < members; i++ {
		cols = append(cols, collect(mustSub(t, b, "ledger", WithGroup("posting"), WithBuffer(keys*perKey))))
	}
	for i := 0; i < perKey; i++ {
		for k := 0; k < keys; k++ {
			pubKey(t, b, "ledger", fmt.Sprintf("acct-%d", k), i)
		}
	}
	waitFor(t, "delivery", func() bool {
		total := 0
		for _, c := range cols {
			total += c.len()
		}
		return total == keys*perKey
	})
	owner := map[string]int{}
	next := map[string]int{}
	for mi, c := range cols {
		for _, m := range c.snapshot() {
			if o, ok := owner[m.Key]; ok && o != mi {
				t.Fatalf("key %s was delivered to members %d and %d: per-key ordering needs one owner", m.Key, o, mi)
			}
			owner[m.Key] = mi
			if m.Payload != next[m.Key] {
				t.Fatalf("key %s: got payload %v, want %d (order within a key)", m.Key, m.Payload, next[m.Key])
			}
			next[m.Key]++
		}
	}
	if len(owner) != keys {
		t.Fatalf("saw %d keys, want %d", len(owner), keys)
	}
}

func TestStage3_KeyOrdering(t *testing.T) {
	t.Run("OnlyOneMessagePerKeyIsInFlight", stage3OnlyOneMessagePerKeyIsInFlight)
	t.Run("DifferentKeysAreIndependent", stage3DifferentKeysAreIndependent)
	t.Run("HeldBackMessagesCountTowardTheBound", stage3HeldBackMessagesCountTowardTheBound)
}

func stage3OnlyOneMessagePerKeyIsInFlight(t *testing.T) {
	b, clk := ackBroker(t)
	s := mustSub(t, b, "ledger", WithAckTimeout(ackTimeout), WithBuffer(8))
	id1 := pubKey(t, b, "ledger", "acct-7", "m1")
	id2 := pubKey(t, b, "ledger", "acct-7", "m2")
	id3 := pubKey(t, b, "ledger", "acct-7", "m3")

	m1 := recvInFlight(t, s, 1)
	if m1.ID != id1 {
		t.Fatalf("got %s, want %s", m1.ID, id1)
	}
	expectNone(t, s, "m2 must wait: m1 for the same key is still in flight, and an earlier message must not be overtaken by a later one")
	if s.Len() != 2 {
		t.Fatalf("Len() = %d, want 2 (m2 and m3 are held back but accepted)", s.Len())
	}

	// m1 times out: its REDELIVERY must come before m2's first delivery.
	clk.Advance(ackTimeout)
	if m := recv(t, s); m.ID != id1 || m.Attempt != 2 {
		t.Fatalf("got %+v, want m1 again (Attempt 2) before m2: redelivery must not reorder a key", m)
	}
	waitFor(t, "InFlight() == 1", func() bool { return s.InFlight() == 1 })
	expectNone(t, s, "m2 still waits")

	if err := s.Ack(id1); err != nil {
		t.Fatalf("Ack(m1) = %v", err)
	}
	if m := recv(t, s); m.ID != id2 {
		t.Fatalf("got %+v, want m2 once m1 was acked", m)
	}
	expectNone(t, s, "m3 waits for m2")
	waitFor(t, "InFlight() == 1", func() bool { return s.InFlight() == 1 })
	_ = s.Ack(id2)
	if m := recv(t, s); m.ID != id3 {
		t.Fatalf("got %+v, want m3", m)
	}
}

func stage3DifferentKeysAreIndependent(t *testing.T) {
	b, _ := ackBroker(t)
	s := mustSub(t, b, "ledger", WithAckTimeout(ackTimeout), WithBuffer(8))
	a1 := pubKey(t, b, "ledger", "A", "a1")
	b1 := pubKey(t, b, "ledger", "B", "b1")
	a2 := pubKey(t, b, "ledger", "A", "a2")
	pub(t, b, "ledger", "unkeyed") // no key: never held back

	got := map[string]bool{}
	for i := 1; i <= 3; i++ {
		got[recvInFlight(t, s, i).ID] = true
	}
	if !got[a1] || !got[b1] || got[a2] {
		t.Fatalf("first three deliveries %v: want a1, b1 and the unkeyed message, with a2 held behind a1", got)
	}
	_ = s.Ack(a1)
	if m := recv(t, s); m.ID != a2 {
		t.Fatalf("got %+v, want a2 after a1 was acked", m)
	}
}

func stage3HeldBackMessagesCountTowardTheBound(t *testing.T) {
	b, _ := ackBroker(t)
	s := mustSub(t, b, "ledger", WithAckTimeout(ackTimeout), WithBuffer(2), WithPolicy(DropNewest))
	pubKey(t, b, "ledger", "hot", 1)
	m1 := recvInFlight(t, s, 1)
	pubKey(t, b, "ledger", "hot", 2) // held back
	pubKey(t, b, "ledger", "hot", 3) // held back: the bound (2) is reached
	pubKey(t, b, "ledger", "hot", 4) // no room: dropped
	if got := s.Dropped(); got != 1 {
		t.Fatalf("Dropped() = %d, want 1: messages held back behind a hot key still occupy the subscriber's bounded queue, or memory grows without limit", got)
	}
	if s.Len() != 2 {
		t.Fatalf("Len() = %d, want 2", s.Len())
	}
	_ = s.Ack(m1.ID)
	if m := recv(t, s); m.Payload != 2 {
		t.Fatalf("got %v, want 2", m.Payload)
	}
}

func TestStage3_MembersLeaving(t *testing.T) {
	t.Run("UnackedWorkOfALeavingMemberGoesToTheRest", stage3UnackedWorkOfALeavingMemberGoesToTheRest)
	t.Run("NoMessageIsLostWhileMembersComeAndGo", stage3NoMessageIsLostWhileMembersComeAndGo)
}

func stage3UnackedWorkOfALeavingMemberGoesToTheRest(t *testing.T) {
	b, _ := ackBroker(t)
	s1 := mustSub(t, b, "ledger", WithGroup("g"), WithAckTimeout(ackTimeout), WithBuffer(8))
	s2 := mustSub(t, b, "ledger", WithGroup("g"), WithAckTimeout(ackTimeout), WithBuffer(8))
	subs := []*Subscription{s1, s2}
	type tagged struct {
		who int
		m   Message
	}
	fan := make(chan tagged, 64)
	for i, s := range subs {
		go func() {
			for m := range s.C() {
				fan <- tagged{i, m}
			}
		}()
	}
	next := func() tagged {
		select {
		case x := <-fan:
			return x
		case <-time.After(testTimeout):
			t.Fatal("no message within timeout")
			return tagged{}
		}
	}

	id1 := pubKey(t, b, "ledger", "acct-1", "first")
	id2 := pubKey(t, b, "ledger", "acct-1", "second") // held back behind first on the same member
	owner := next()
	if owner.m.ID != id1 {
		t.Fatalf("got %s, want %s", owner.m.ID, id1)
	}
	waitFor(t, "the owner to hold the message in flight", func() bool { return subs[owner.who].InFlight() == 1 })

	subs[owner.who].Unsubscribe() // the owner dies without acking

	survivor := 1 - owner.who
	first := next()
	if first.who != survivor || first.m.ID != id1 {
		t.Fatalf("after the owner left, got %+v from member %d; want %s from member %d (at-least-once: a departing member's unacked messages are handed to the group)", first.m, first.who, id1, survivor)
	}
	select {
	case x := <-fan:
		t.Fatalf("got %+v before the first message was acked: the held-back message must stay behind it", x.m)
	case <-time.After(20 * time.Millisecond):
	}
	waitFor(t, "InFlight() == 1", func() bool { return subs[survivor].InFlight() == 1 })
	_ = subs[survivor].Ack(id1)
	if second := next(); second.m.ID != id2 || second.who != survivor {
		t.Fatalf("got %+v, want the held-back message %s on the surviving member", second.m, id2)
	}
	// New messages for the key now belong to the survivor.
	id3 := pubKey(t, b, "ledger", "acct-1", "third")
	_ = subs[survivor].Ack(id2)
	if third := next(); third.m.ID != id3 || third.who != survivor {
		t.Fatalf("got %+v, want %s on the surviving member", third.m, id3)
	}
}

func TestStage3_Shutdown(t *testing.T) {
	t.Run("ShutdownWaitsForInFlightAcks", stage3ShutdownWaitsForInFlightAcks)
	t.Run("ShutdownKeepsRedeliveringWhileItWaits", stage3ShutdownKeepsRedeliveringWhileItWaits)
	t.Run("ShutdownGivesUpWhenItsContextEnds", stage3ShutdownGivesUpWhenItsContextEnds)
	t.Run("ShutdownDrainsPlainSubscribersToo", stage3ShutdownDrainsPlainSubscribersToo)
}

func stage3ShutdownWaitsForInFlightAcks(t *testing.T) {
	b, _ := ackBroker(t)
	s := mustSub(t, b, "t", WithAckTimeout(ackTimeout), WithBuffer(8))
	var ids []string
	for i := 0; i < 3; i++ {
		ids = append(ids, pub(t, b, "t", i))
	}
	for i := 1; i <= 3; i++ {
		recvInFlight(t, s, i)
	}

	done := make(chan error, 1)
	go func() { done <- b.Shutdown(context.Background()) }()
	select {
	case err := <-done:
		t.Fatalf("Shutdown returned %v while 3 messages were still unacked", err)
	case <-time.After(30 * time.Millisecond):
	}
	if _, err := b.Publish(context.Background(), "t", Message{}); !errors.Is(err, ErrClosed) {
		t.Errorf("Publish during Shutdown error = %v, want ErrClosed", err)
	}
	for _, id := range ids {
		if err := s.Ack(id); err != nil {
			t.Fatalf("Ack(%s) during Shutdown = %v: acks must keep working while shutting down", id, err)
		}
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Shutdown = %v after everything was acked, want nil", err)
		}
	case <-time.After(testTimeout):
		t.Fatal("Shutdown did not return after the last ack")
	}
	drain(t, s) // closed
}

func stage3ShutdownKeepsRedeliveringWhileItWaits(t *testing.T) {
	b, clk := ackBroker(t)
	s := mustSub(t, b, "t", WithAckTimeout(ackTimeout))
	id := pub(t, b, "t", "x")
	recvInFlight(t, s, 1) // never acked

	done := make(chan error, 1)
	go func() { done <- b.Shutdown(context.Background()) }()
	select {
	case err := <-done:
		t.Fatalf("Shutdown returned %v while a message was unacked", err)
	case <-time.After(30 * time.Millisecond):
	}
	clk.Advance(ackTimeout)
	m := recv(t, s) // the redelivery still happens during shutdown
	if m.ID != id || m.Attempt != 2 {
		t.Fatalf("got %+v, want a redelivery of %s", m, id)
	}
	_ = s.Ack(id)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Shutdown = %v", err)
		}
	case <-time.After(testTimeout):
		t.Fatal("Shutdown did not finish after the ack")
	}
}

func stage3ShutdownGivesUpWhenItsContextEnds(t *testing.T) {
	baseline := runtime.NumGoroutine()
	b, _ := ackBroker(t)
	s := mustSub(t, b, "t", WithAckTimeout(ackTimeout), WithBuffer(8))
	for i := 0; i < 3; i++ {
		pub(t, b, "t", i)
	}
	recvInFlight(t, s, 1) // one in flight, two still queued, nobody will ever ack

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- b.Shutdown(ctx) }()
	select {
	case err := <-done:
		t.Fatalf("Shutdown returned %v before its context ended", err)
	case <-time.After(30 * time.Millisecond):
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Shutdown error = %v, want it to wrap context.Canceled", err)
		}
	case <-time.After(testTimeout):
		t.Fatal("Shutdown ignored its context")
	}
	drain(t, s) // force-closed: the channel must be closed now
	waitForGoroutines(t, baseline)
}

func stage3ShutdownDrainsPlainSubscribersToo(t *testing.T) {
	b := mustNew(t)
	s := mustSub(t, b, "t", WithBuffer(5))
	for i := 0; i < 5; i++ {
		pub(t, b, "t", i)
	}
	done := make(chan error, 1)
	go func() { done <- b.Shutdown(context.Background()) }()
	if got := len(drain(t, s)); got != 5 {
		t.Fatalf("drained %d messages, want 5", got)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Shutdown = %v", err)
		}
	case <-time.After(testTimeout):
		t.Fatal("Shutdown did not return once the consumer had everything")
	}
}

func TestStage3_ConcurrentGroupKeepsKeyOrderAndLosesNothing(t *testing.T) {
	const publishers, keysPer, perKey, members = 4, 5, 40, 4
	baseline := runtime.NumGoroutine()
	b, err := New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	var mu sync.Mutex
	lastSeen := map[string]int{}
	var received atomic.Int64
	var bad atomic.Value
	var subs []*Subscription
	for i := 0; i < members; i++ {
		s, err := b.Subscribe("ledger", WithGroup("posting"), WithAckTimeout(time.Minute), WithBuffer(64))
		if err != nil {
			t.Fatalf("Subscribe: %v", err)
		}
		subs = append(subs, s)
		go func() {
			for m := range s.C() {
				mu.Lock()
				n := m.Payload.(int)
				if prev, ok := lastSeen[m.Key]; ok && n != prev+1 {
					bad.Store(fmt.Sprintf("key %s: payload %d after %d", m.Key, n, prev))
				}
				lastSeen[m.Key] = n
				mu.Unlock()
				received.Add(1)
				if err := s.Ack(m.ID); err != nil {
					bad.Store(fmt.Sprintf("Ack(%s) = %v", m.ID, err))
				}
			}
		}()
	}
	var wg sync.WaitGroup
	for p := 0; p < publishers; p++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < perKey; i++ {
				for k := 0; k < keysPer; k++ {
					key := fmt.Sprintf("p%d-k%d", p, k)
					if _, err := b.Publish(context.Background(), "ledger", Message{Key: key, Payload: i}); err != nil {
						bad.Store(fmt.Sprintf("Publish: %v", err))
						return
					}
				}
			}
		}()
	}
	within(t, "publishing", wg.Wait)
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()
	if err := b.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown = %v: every message was acked immediately, so it should finish cleanly", err)
	}
	if v := bad.Load(); v != nil {
		t.Fatal(v)
	}
	if got, want := received.Load(), int64(publishers*keysPer*perKey); got != want {
		t.Fatalf("received %d messages, want %d", got, want)
	}
	waitForGoroutines(t, baseline)
	_ = subs
}

func stage3NoMessageIsLostWhileMembersComeAndGo(t *testing.T) {
	// One stable member acks everything. Three churners join the group and
	// leave again without ever reading: whatever was routed to them while
	// they were members is owed, and must be handed to the stable member
	// when they leave.
	const n = 300
	b := mustNew(t)
	stable := mustSub(t, b, "t", WithGroup("g"), WithAckTimeout(time.Minute), WithBuffer(n))
	var mu sync.Mutex
	seen := map[string]int{}
	go func() {
		for m := range stable.C() {
			mu.Lock()
			seen[m.ID]++
			mu.Unlock()
			_ = stable.Ack(m.ID)
		}
	}()
	stop := make(chan struct{})
	var churn sync.WaitGroup
	for c := 0; c < 3; c++ {
		churn.Add(1)
		go func() {
			defer churn.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				s, err := b.Subscribe("t", WithGroup("g"), WithAckTimeout(time.Minute), WithBuffer(2), WithPolicy(DropNewest))
				if err != nil {
					return
				}
				runtime.Gosched()
				s.Unsubscribe()
			}
		}()
	}
	ids := make([]string, 0, n)
	within(t, "publishing while group members churn", func() {
		for i := 0; i < n; i++ {
			ids = append(ids, pub(t, b, "t", i))
		}
	})
	close(stop)
	within(t, "stopping churn", churn.Wait)
	waitFor(t, "the stable member to receive every message", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(seen) == n
	})
	mu.Lock()
	defer mu.Unlock()
	for _, id := range ids {
		if seen[id] != 1 {
			t.Fatalf("message %s was delivered %d times, want exactly once (the churners never read, so no duplicates are possible either)", id, seen[id])
		}
	}
	if st := b.Stats(); st.Orphaned != 0 {
		t.Fatalf("Stats().Orphaned = %d, want 0: a stable member was always present", st.Orphaned)
	}
}

func stage3RejectsEmptyGroupName(t *testing.T) {
	b := mustNew(t)
	if _, err := b.Subscribe("t", WithGroup("")); !errors.Is(err, ErrInvalidOption) {
		t.Errorf("WithGroup(\"\") error = %v, want ErrInvalidOption", err)
	}
}
