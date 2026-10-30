package chat

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"runtime"
	"runtime/pprof"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const testTimeout = 5 * time.Second

// ---------- helpers (prefixed with t to avoid clashing with your code) ----------

var tT0 = time.Date(2026, 4, 7, 9, 0, 0, 0, time.UTC)

func tNew(t *testing.T, opt Options) *Service {
	t.Helper()
	s := New(opt)
	if s == nil {
		t.Fatal("New returned nil")
	}
	return s
}

func tJoin(t *testing.T, s *Service, convo string, users ...string) {
	t.Helper()
	for _, u := range users {
		if err := s.Join(convo, u); err != nil {
			t.Fatalf("Join(%s, %s): %v", convo, u, err)
		}
	}
}

func tSend(t *testing.T, s *Service, convo, sender, id, body string) Message {
	t.Helper()
	m, err := s.Send(context.Background(), convo, sender, id, body)
	if err != nil {
		t.Fatalf("Send(%s, %s, %s): %v", convo, sender, id, err)
	}
	return m
}

func tConnect(t *testing.T, s *Service, ctx context.Context, user string) *Subscription {
	t.Helper()
	sub, err := s.Connect(ctx, user)
	if err != nil {
		t.Fatalf("Connect(%s): %v", user, err)
	}
	if sub == nil || sub.C == nil {
		t.Fatalf("Connect(%s) returned a nil subscription or a nil channel", user)
	}
	return sub
}

// tRecv receives one message or fails after testTimeout (or if C is closed).
func tRecv(t *testing.T, sub *Subscription, what string) Message {
	t.Helper()
	select {
	case m, ok := <-sub.C:
		if !ok {
			t.Fatalf("subscription channel closed while waiting for %s", what)
		}
		return m
	case <-time.After(testTimeout):
		t.Fatalf("timed out after %v waiting for %s", testTimeout, what)
	}
	return Message{}
}

// tRecvN receives n messages in order.
func tRecvN(t *testing.T, sub *Subscription, n int, what string) []Message {
	t.Helper()
	out := make([]Message, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, tRecv(t, sub, fmt.Sprintf("%s (message %d of %d)", what, i+1, n)))
	}
	return out
}

// tExpectClosed fails unless C is closed (after any buffered messages) within testTimeout.
func tExpectClosed(t *testing.T, sub *Subscription, why string) {
	t.Helper()
	deadline := time.After(testTimeout)
	for {
		select {
		case _, ok := <-sub.C:
			if !ok {
				return
			}
		case <-deadline:
			t.Fatalf("subscription channel still open after %v: %s", testTimeout, why)
		}
	}
}

func tExpectNothing(t *testing.T, sub *Subscription, why string) {
	t.Helper()
	select {
	case m, ok := <-sub.C:
		if ok {
			t.Fatalf("unexpected delivery %+v: %s", m, why)
		}
	case <-time.After(50 * time.Millisecond):
	}
}

func tSeqs(ms []Message) []uint64 {
	out := make([]uint64, len(ms))
	for i, m := range ms {
		out[i] = m.Seq
	}
	return out
}

// tCheckDense fails unless seqs are exactly 1..n in order.
func tCheckDense(t *testing.T, seqs []uint64, n int, what string) {
	t.Helper()
	if len(seqs) != n {
		t.Fatalf("%s: got %d seqs, want %d: %v", what, len(seqs), n, tHead(seqs))
	}
	for i, s := range seqs {
		if s != uint64(i)+1 {
			t.Fatalf("%s: seq at position %d is %d, want %d (dense per-conversation seqs: no gaps, no repeats, in order)", what, i, s, i+1)
		}
	}
}

func tHead(seqs []uint64) []uint64 {
	if len(seqs) > 20 {
		return seqs[:20]
	}
	return seqs
}

// tSyncAll walks Sync pages from afterSeq until it runs dry.
func tSyncAll(t *testing.T, s *Service, convo, user string, afterSeq uint64) []Message {
	t.Helper()
	var all []Message
	for page := 0; page < 100000; page++ {
		ms, err := s.Sync(convo, user, afterSeq, MaxSyncLimit)
		if err != nil {
			t.Fatalf("Sync(%s, %s, after %d): %v", convo, user, afterSeq, err)
		}
		if len(ms) == 0 {
			return all
		}
		all = append(all, ms...)
		afterSeq = ms[len(ms)-1].Seq
	}
	t.Fatal("Sync never ran dry; is afterSeq honoured?")
	return nil
}

func tWaitForGoroutines(t *testing.T, baseline int) {
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
			t.Fatalf("goroutine leak: %d goroutines still running, baseline was %d (a per-subscription goroutine that does not exit on Close or context cancellation keeps every disconnected socket alive forever). Live stacks:\n%s", n, baseline, buf.String())
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// ---------- Join ----------

func TestJoin_CreatesConversationAndIsIdempotent(t *testing.T) {
	s := tNew(t, Options{})
	if _, err := s.Members("dm:alice:bob"); !errors.Is(err, ErrUnknownConversation) {
		t.Fatalf("Members on an unknown conversation: err = %v, want ErrUnknownConversation", err)
	}
	tJoin(t, s, "dm:alice:bob", "bob", "alice", "bob")
	got, err := s.Members("dm:alice:bob")
	if err != nil {
		t.Fatalf("Members: %v", err)
	}
	if want := []string{"alice", "bob"}; fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("Members = %v, want %v (sorted, no duplicates after a repeated Join)", got, want)
	}
	if n, err := s.Latest("dm:alice:bob"); err != nil || n != 0 {
		t.Fatalf("Latest on an empty conversation = %d, %v; want 0, nil", n, err)
	}
	if err := s.Join("", "alice"); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("Join with an empty conversation id: err = %v, want ErrInvalidArgument", err)
	}
	if err := s.Join("dm:alice:bob", ""); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("Join with an empty user: err = %v, want ErrInvalidArgument", err)
	}
}

// ---------- Send ----------

func TestSend_RejectsUnknownConversationNonMembersAndEmptyFields(t *testing.T) {
	s := tNew(t, Options{})
	ctx := context.Background()
	if _, err := s.Send(ctx, "nope", "alice", "c1", "hi"); !errors.Is(err, ErrUnknownConversation) {
		t.Fatalf("Send to an unknown conversation: err = %v, want ErrUnknownConversation", err)
	}
	tJoin(t, s, "g1", "alice", "bob")
	if _, err := s.Send(ctx, "g1", "mallory", "c1", "hi"); !errors.Is(err, ErrNotMember) {
		t.Fatalf("Send by a non-member: err = %v, want ErrNotMember", err)
	}
	if n, _ := s.Latest("g1"); n != 0 {
		t.Fatalf("a non-member's message entered the log (Latest = %d); membership must be checked before the seq is assigned", n)
	}
	for _, tc := range []struct{ convo, sender, id, body string }{
		{"", "alice", "c1", "hi"}, {"g1", "", "c1", "hi"}, {"g1", "alice", "", "hi"}, {"g1", "alice", "c1", ""},
	} {
		if _, err := s.Send(ctx, tc.convo, tc.sender, tc.id, tc.body); !errors.Is(err, ErrInvalidArgument) {
			t.Fatalf("Send(%q, %q, %q, %q): err = %v, want ErrInvalidArgument (an empty client message id makes the send non-idempotent)", tc.convo, tc.sender, tc.id, tc.body, err)
		}
	}
	if n, _ := s.Latest("g1"); n != 0 {
		t.Fatalf("an invalid send entered the log (Latest = %d)", n)
	}
}

func TestSend_AssignsDenseSequenceFromOne(t *testing.T) {
	tick := 0
	now := func() time.Time { tick++; return tT0.Add(time.Duration(tick) * time.Second) }
	s := tNew(t, Options{Now: now})
	tJoin(t, s, "g1", "alice", "bob")
	for i := 1; i <= 5; i++ {
		sender := "alice"
		if i%2 == 0 {
			sender = "bob"
		}
		m := tSend(t, s, "g1", sender, fmt.Sprintf("c%d", i), fmt.Sprintf("msg %d", i))
		if m.Seq != uint64(i) {
			t.Fatalf("message %d got Seq %d, want %d", i, m.Seq, i)
		}
		if m.Conversation != "g1" || m.Sender != sender || m.ClientMsgID != fmt.Sprintf("c%d", i) || m.Body != fmt.Sprintf("msg %d", i) {
			t.Fatalf("stored message %+v does not echo the send", m)
		}
		if !m.SentAt.Equal(tT0.Add(time.Duration(i) * time.Second)) {
			t.Fatalf("SentAt = %v, want the injected clock's %v", m.SentAt, tT0.Add(time.Duration(i)*time.Second))
		}
	}
	if n, _ := s.Latest("g1"); n != 5 {
		t.Fatalf("Latest = %d, want 5", n)
	}
}

func TestSend_DenseUniqueSeqsUnder50ConcurrentSenders(t *testing.T) {
	const senders, perSender = 50, 40
	s := tNew(t, Options{})
	users := make([]string, senders)
	for i := range users {
		users[i] = fmt.Sprintf("u%02d", i)
		tJoin(t, s, "g1", users[i])
	}
	var mu sync.Mutex
	got := map[uint64]Message{}
	var dups atomic.Int64
	var wg sync.WaitGroup
	start := make(chan struct{})
	for _, u := range users {
		wg.Add(1)
		go func(u string) {
			defer wg.Done()
			<-start
			for i := 0; i < perSender; i++ {
				m, err := s.Send(context.Background(), "g1", u, fmt.Sprintf("%s-%d", u, i), "x")
				if err != nil {
					t.Errorf("Send: %v", err)
					return
				}
				mu.Lock()
				if _, dup := got[m.Seq]; dup {
					dups.Add(1)
				}
				got[m.Seq] = m
				mu.Unlock()
			}
		}(u)
	}
	close(start)
	wg.Wait()
	if dups.Load() > 0 {
		t.Fatalf("%d sends received a Seq another send already had; two messages with one seq means one of them is invisible to clients", dups.Load())
	}
	const total = senders * perSender
	seqs := make([]uint64, 0, len(got))
	for seq := range got {
		seqs = append(seqs, seq)
	}
	sort.Slice(seqs, func(i, j int) bool { return seqs[i] < seqs[j] })
	tCheckDense(t, seqs, total, "seqs returned to senders")
	if n, _ := s.Latest("g1"); n != total {
		t.Fatalf("Latest = %d, want %d", n, total)
	}
	log := tSyncAll(t, s, "g1", users[0], 0)
	tCheckDense(t, tSeqs(log), total, "the log seen through Sync")
	for _, m := range log {
		if got[m.Seq] != m {
			t.Fatalf("seq %d: sender received %+v but the log holds %+v; the ack and the stored message must be the same", m.Seq, got[m.Seq], m)
		}
	}
}

func TestSend_DuplicateClientMessageIDIsStoredOnce(t *testing.T) {
	s := tNew(t, Options{})
	tJoin(t, s, "g1", "alice", "bob")
	sub := tConnect(t, s, context.Background(), "bob")
	defer sub.Close()

	first := tSend(t, s, "g1", "alice", "c1", "hello")
	again := tSend(t, s, "g1", "alice", "c1", "hello (retried after a timeout)")
	if again != first {
		t.Fatalf("retried send returned %+v, want the original %+v (same Seq, original body)", again, first)
	}
	if n, _ := s.Latest("g1"); n != 1 {
		t.Fatalf("Latest = %d after a retried send, want 1: the member would see the message twice", n)
	}
	// A different sender may reuse the same client id: the key is (sender, id).
	if m := tSend(t, s, "g1", "bob", "c1", "hi"); m.Seq != 2 {
		t.Fatalf("bob's message with the same client id got Seq %d, want 2 (idempotency is scoped per sender)", m.Seq)
	}
	tRecv(t, sub, "alice's message")
	tRecv(t, sub, "bob's message")
	tExpectNothing(t, sub, "a retried send must not be delivered a second time")

	// Concurrent duplicates: all callers get the same message and one is stored.
	s2 := tNew(t, Options{})
	tJoin(t, s2, "g2", "alice")
	var wg sync.WaitGroup
	results := make([]Message, 20)
	errs := make([]error, 20)
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i], errs[i] = s2.Send(context.Background(), "g2", "alice", "same", "x")
		}(i)
	}
	wg.Wait()
	for i := range results {
		if errs[i] != nil {
			t.Fatalf("concurrent duplicate %d: %v", i, errs[i])
		}
		if results[i] != results[0] {
			t.Fatalf("concurrent duplicates got different messages: %+v vs %+v", results[i], results[0])
		}
	}
	if n, _ := s2.Latest("g2"); n != 1 {
		t.Fatalf("Latest = %d after 20 concurrent sends with one client id, want 1 (check-and-assign must be atomic)", n)
	}
}

func TestSend_HonoursCancelledContext(t *testing.T) {
	s := tNew(t, Options{})
	tJoin(t, s, "g1", "alice")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.Send(ctx, "g1", "alice", "c1", "hi"); !errors.Is(err, context.Canceled) {
		t.Fatalf("Send with a cancelled context: err = %v, want context.Canceled", err)
	}
	if n, _ := s.Latest("g1"); n != 0 {
		t.Fatalf("a cancelled send was stored (Latest = %d)", n)
	}
}

// ---------- Sync ----------

func TestSync_ReturnsExactlyTheMessagesAfterTheCursorInOrder(t *testing.T) {
	s := tNew(t, Options{})
	tJoin(t, s, "g1", "alice", "bob")
	for i := 1; i <= 25; i++ {
		tSend(t, s, "g1", "alice", fmt.Sprintf("c%d", i), "x")
	}
	want := func(from, to uint64) []uint64 {
		var out []uint64
		for i := from; i <= to; i++ {
			out = append(out, i)
		}
		return out
	}
	for _, tc := range []struct {
		after uint64
		limit int
		want  []uint64
	}{
		{0, 10, want(1, 10)}, {10, 10, want(11, 20)}, {20, 10, want(21, 25)}, {25, 10, nil}, {100, 10, nil}, {3, 1, want(4, 4)},
	} {
		ms, err := s.Sync("g1", "bob", tc.after, tc.limit)
		if err != nil {
			t.Fatalf("Sync(after %d, limit %d): %v", tc.after, tc.limit, err)
		}
		if ms == nil {
			t.Fatalf("Sync(after %d) returned a nil slice; return an empty, non-nil slice so clients see [] not null", tc.after)
		}
		if got := tSeqs(ms); fmt.Sprint(got) != fmt.Sprint(tc.want) {
			t.Fatalf("Sync(after %d, limit %d) = %v, want %v", tc.after, tc.limit, got, tc.want)
		}
	}
	// Paging with limit 7 visits every message exactly once.
	var all []uint64
	after := uint64(0)
	for {
		ms, err := s.Sync("g1", "bob", after, 7)
		if err != nil {
			t.Fatalf("Sync: %v", err)
		}
		if len(ms) == 0 {
			break
		}
		all = append(all, tSeqs(ms)...)
		after = ms[len(ms)-1].Seq
	}
	tCheckDense(t, all, 25, "paged sync")

	for _, limit := range []int{0, -1, MaxSyncLimit + 1} {
		if _, err := s.Sync("g1", "bob", 0, limit); !errors.Is(err, ErrInvalidLimit) {
			t.Fatalf("Sync with limit %d: err = %v, want ErrInvalidLimit", limit, err)
		}
	}
	if _, err := s.Sync("g1", "mallory", 0, 10); !errors.Is(err, ErrNotMember) {
		t.Fatalf("Sync by a non-member: err = %v, want ErrNotMember (history must not leak)", err)
	}
	if _, err := s.Sync("nope", "bob", 0, 10); !errors.Is(err, ErrUnknownConversation) {
		t.Fatalf("Sync on an unknown conversation: err = %v, want ErrUnknownConversation", err)
	}
}

func TestSync_ReturnsACopyOfTheLog(t *testing.T) {
	s := tNew(t, Options{})
	tJoin(t, s, "g1", "alice")
	tSend(t, s, "g1", "alice", "c1", "original")
	ms, err := s.Sync("g1", "alice", 0, 10)
	if err != nil || len(ms) != 1 {
		t.Fatalf("Sync = %v, %v", ms, err)
	}
	ms[0].Body = "tampered"
	ms2, _ := s.Sync("g1", "alice", 0, 10)
	if ms2[0].Body != "original" {
		t.Fatalf("mutating a Sync result changed the log; return a copy")
	}
}

// ---------- Connect ----------

func TestConnect_DeliversEveryMessageOnceAndInOrderToEveryOnlineMember(t *testing.T) {
	s := tNew(t, Options{SubscriberBuffer: 256})
	tJoin(t, s, "g1", "alice", "bob", "carol")
	ctx := context.Background()
	alicePhone := tConnect(t, s, ctx, "alice")
	defer alicePhone.Close()
	aliceLaptop := tConnect(t, s, ctx, "alice")
	defer aliceLaptop.Close()
	bob := tConnect(t, s, ctx, "bob")
	defer bob.Close()
	dave := tConnect(t, s, ctx, "dave") // connected, but not a member of g1
	defer dave.Close()

	const n = 100
	var wg sync.WaitGroup
	for g, sender := range []string{"alice", "bob", "carol", "alice"} {
		wg.Add(1)
		go func(g int, sender string) {
			defer wg.Done()
			for i := 0; i < n/4; i++ {
				if _, err := s.Send(ctx, "g1", sender, fmt.Sprintf("%s-%d-%d", sender, g, i), "x"); err != nil {
					t.Errorf("Send: %v", err)
				}
			}
		}(g, sender)
	}
	wg.Wait()
	for name, sub := range map[string]*Subscription{"alice's phone": alicePhone, "alice's laptop": aliceLaptop, "bob": bob} {
		got := tRecvN(t, sub, n, name)
		tCheckDense(t, tSeqs(got), n, name+" deliveries")
		for _, m := range got {
			if m.Conversation != "g1" {
				t.Fatalf("%s received a message for %q", name, m.Conversation)
			}
		}
		tExpectNothing(t, sub, name+" received more than one copy of some message")
		if d := sub.Dropped(); d != 0 {
			t.Fatalf("%s: Dropped = %d with a buffer that was never full", name, d)
		}
	}
	tExpectNothing(t, dave, "dave is not a member of g1 and must receive nothing")
}

func TestConnect_UserInTwoConversationsSeesBothStreamsInPerConversationOrder(t *testing.T) {
	s := tNew(t, Options{SubscriberBuffer: 512})
	tJoin(t, s, "c1", "alice", "bob")
	tJoin(t, s, "c2", "alice", "carol")
	sub := tConnect(t, s, context.Background(), "alice")
	defer sub.Close()
	const n = 100
	var wg sync.WaitGroup
	for _, c := range []struct{ convo, sender string }{{"c1", "bob"}, {"c2", "carol"}} {
		wg.Add(1)
		go func(convo, sender string) {
			defer wg.Done()
			for i := 0; i < n; i++ {
				if _, err := s.Send(context.Background(), convo, sender, fmt.Sprintf("%d", i), "x"); err != nil {
					t.Errorf("Send: %v", err)
				}
			}
		}(c.convo, c.sender)
	}
	wg.Wait()
	got := tRecvN(t, sub, 2*n, "alice's deliveries from two conversations")
	perConvo := map[string][]uint64{}
	for _, m := range got {
		perConvo[m.Conversation] = append(perConvo[m.Conversation], m.Seq)
	}
	for _, convo := range []string{"c1", "c2"} {
		tCheckDense(t, perConvo[convo], n, "deliveries for "+convo)
	}
}

func TestConnect_SlowSubscriberNeverBlocksSendersAndMustResync(t *testing.T) {
	const buf, n = 8, 1000
	s := tNew(t, Options{SubscriberBuffer: buf})
	tJoin(t, s, "g1", "alice", "bob")
	slow := tConnect(t, s, context.Background(), "bob") // never reads
	defer slow.Close()

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < n; i++ {
			if _, err := s.Send(context.Background(), "g1", "alice", fmt.Sprintf("c%d", i), "x"); err != nil {
				t.Errorf("Send %d: %v", i, err)
				return
			}
		}
	}()
	select {
	case <-done:
	case <-time.After(testTimeout):
		t.Fatalf("1000 sends did not complete within %v while one subscriber was not reading; a slow socket must never block senders (non-blocking offer, drop when full)", testTimeout)
	}
	if d := slow.Dropped(); d != n-buf {
		t.Fatalf("Dropped = %d, want %d (buffer %d holds the first %d, every later message is dropped for this subscription)", d, n-buf, buf, buf)
	}
	buffered := tRecvN(t, slow, buf, "the buffered messages")
	tCheckDense(t, tSeqs(buffered), buf, "buffered deliveries")
	tExpectNothing(t, slow, "dropped messages must not be delivered later on this subscription")
	// The client recovers from the log, not from the push channel.
	missed := tSyncAll(t, s, "g1", "bob", buffered[len(buffered)-1].Seq)
	if got := len(missed); got != n-buf {
		t.Fatalf("Sync after the last buffered seq returned %d messages, want %d", got, n-buf)
	}
	if first, last := missed[0].Seq, missed[len(missed)-1].Seq; first != buf+1 || last != n {
		t.Fatalf("Sync returned seqs %d..%d, want %d..%d", first, last, buf+1, n)
	}
}

func TestConnect_CloseClosesTheChannelAndStopsDelivery(t *testing.T) {
	s := tNew(t, Options{})
	tJoin(t, s, "g1", "alice", "bob")
	sub := tConnect(t, s, context.Background(), "bob")
	tSend(t, s, "g1", "alice", "c1", "before close")
	if m := tRecv(t, sub, "the message sent before Close"); m.Seq != 1 {
		t.Fatalf("got seq %d, want 1", m.Seq)
	}
	sub.Close()
	sub.Close() // idempotent
	tExpectClosed(t, sub, "Close must close C")
	// Sending after Close must neither panic nor count as dropped.
	tSend(t, s, "g1", "alice", "c2", "after close")
	if d := sub.Dropped(); d != 0 {
		t.Fatalf("Dropped = %d after Close; a closed subscription is unregistered, not lagging", d)
	}
	// Buffered messages remain readable after Close until drained.
	sub2 := tConnect(t, s, context.Background(), "bob")
	tSend(t, s, "g1", "alice", "c3", "buffered")
	sub2.Close()
	if m, ok := <-sub2.C; !ok || m.Seq != 3 {
		t.Fatalf("buffered message after Close: got %+v, ok=%v; want seq 3 readable before the close is observed", m, ok)
	}
	tExpectClosed(t, sub2, "after draining, C must be closed")
}

func TestConnect_ContextCancellationClosesTheSubscription(t *testing.T) {
	s := tNew(t, Options{})
	tJoin(t, s, "g1", "alice", "bob")
	ctx, cancel := context.WithCancel(context.Background())
	sub := tConnect(t, s, ctx, "bob")
	tSend(t, s, "g1", "alice", "c1", "x")
	tRecv(t, sub, "first message")
	cancel()
	tExpectClosed(t, sub, "cancelling the Connect context must close the subscription")
	sub.Close() // still safe after the context closed it

	done, cancel2 := context.WithCancel(context.Background())
	cancel2()
	if sub, err := s.Connect(done, "bob"); err == nil {
		sub.Close()
		t.Fatalf("Connect with an already-cancelled context returned a subscription; want an error")
	} else if !errors.Is(err, context.Canceled) {
		t.Fatalf("Connect with a cancelled context: err = %v, want context.Canceled", err)
	}
}

func TestConnect_NoGoroutineLeaksAfterCloseAndCancel(t *testing.T) {
	s := tNew(t, Options{SubscriberBuffer: 4})
	tJoin(t, s, "g1", "alice", "bob")
	runtime.GC()
	time.Sleep(20 * time.Millisecond)
	baseline := runtime.NumGoroutine()

	const subs = 100
	ctx, cancel := context.WithCancel(context.Background())
	var closers []*Subscription
	for i := 0; i < subs; i++ {
		if i%2 == 0 {
			tConnect(t, s, ctx, "bob") // closed by cancel below
		} else {
			closers = append(closers, tConnect(t, s, context.Background(), "bob"))
		}
	}
	for i := 0; i < 50; i++ {
		tSend(t, s, "g1", "alice", fmt.Sprintf("c%d", i), "x") // overflow the small buffers
	}
	cancel()
	for _, sub := range closers {
		sub.Close()
	}
	tWaitForGoroutines(t, baseline)
	// The registry must be empty too: a later send must not be offered to dead subscriptions.
	before := make([]uint64, len(closers))
	for i, sub := range closers {
		before[i] = sub.Dropped()
	}
	tSend(t, s, "g1", "alice", "after", "x")
	for i, sub := range closers {
		if d := sub.Dropped(); d != before[i] {
			t.Fatalf("a closed subscription still receives offers (Dropped went from %d to %d); Close must unregister it", before[i], d)
		}
	}
}

// ---------- Receipts ----------

func TestMarkRead_CursorIsMonotonicAndBounded(t *testing.T) {
	s := tNew(t, Options{})
	tJoin(t, s, "g1", "alice", "bob")
	for i := 1; i <= 10; i++ {
		tSend(t, s, "g1", "alice", fmt.Sprintf("c%d", i), "x")
	}
	if n, err := s.Unread("g1", "bob"); err != nil || n != 10 {
		t.Fatalf("Unread before any receipt = %d, %v; want 10", n, err)
	}
	if cur, err := s.MarkRead("g1", "bob", 4); err != nil || cur != 4 {
		t.Fatalf("MarkRead(4) = %d, %v; want 4", cur, err)
	}
	if cur, err := s.MarkRead("g1", "bob", 2); err != nil || cur != 4 {
		t.Fatalf("MarkRead(2) after 4 = %d, %v; want 4 (a late receipt from another device must not move the cursor backwards)", cur, err)
	}
	if cur, err := s.MarkRead("g1", "bob", 11); !errors.Is(err, ErrInvalidSeq) || cur != 4 {
		t.Fatalf("MarkRead(11) with 10 messages = %d, %v; want 4 and ErrInvalidSeq", cur, err)
	}
	if n, _ := s.Unread("g1", "bob"); n != 6 {
		t.Fatalf("Unread = %d, want 6", n)
	}
	if _, err := s.MarkRead("g1", "mallory", 1); !errors.Is(err, ErrNotMember) {
		t.Fatalf("MarkRead by a non-member: err = %v, want ErrNotMember", err)
	}
	if _, err := s.Unread("g1", "mallory"); !errors.Is(err, ErrNotMember) {
		t.Fatalf("Unread by a non-member: err = %v, want ErrNotMember", err)
	}
	// Concurrent receipts in random order converge on the maximum.
	var wg sync.WaitGroup
	for _, seq := range rand.Perm(10) {
		wg.Add(1)
		go func(seq uint64) {
			defer wg.Done()
			if _, err := s.MarkRead("g1", "bob", seq); err != nil {
				t.Errorf("MarkRead(%d): %v", seq, err)
			}
		}(uint64(seq + 1))
	}
	wg.Wait()
	if cur, _ := s.MarkRead("g1", "bob", 0); cur != 10 {
		t.Fatalf("cursor after concurrent receipts = %d, want 10", cur)
	}
	if n, _ := s.Unread("g1", "bob"); n != 0 {
		t.Fatalf("Unread = %d, want 0", n)
	}
	// A member who joins late has read nothing.
	tJoin(t, s, "g1", "carol")
	if n, _ := s.Unread("g1", "carol"); n != 10 {
		t.Fatalf("Unread for a new member = %d, want 10", n)
	}
}

// ---------- everything at once, under -race ----------

func TestChaos_ConcurrentSendSyncConnectAndReadKeepInvariants(t *testing.T) {
	s := tNew(t, Options{SubscriberBuffer: 16})
	convos := []string{"c1", "c2", "c3", "c4"}
	users := []string{"u1", "u2", "u3", "u4", "u5", "u6"}
	for i, c := range convos {
		// each conversation has 4 members: users[i%6 .. i%6+3]
		for j := 0; j < 4; j++ {
			tJoin(t, s, c, users[(i+j)%len(users)])
		}
	}
	member := func(c string) string {
		i := 0
		for k, cc := range convos {
			if cc == c {
				i = k
			}
		}
		return users[(i+rand.IntN(4))%len(users)]
	}
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	var sends, dupes atomic.Int64
	var ids sync.Map // convo+sender+id → struct{}
	// senders
	for w := 0; w < 8; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; ctx.Err() == nil; i++ {
				c := convos[rand.IntN(len(convos))]
				u := member(c)
				id := fmt.Sprintf("w%d-%d", w, i)
				if i%10 == 0 && i > 0 {
					id = fmt.Sprintf("w%d-%d", w, i-1) // a retry
				}
				if _, err := s.Send(context.Background(), c, u, id, "x"); err != nil {
					t.Errorf("Send: %v", err)
					return
				}
				if _, loaded := ids.LoadOrStore(c+"|"+u+"|"+id, struct{}{}); loaded {
					dupes.Add(1)
				} else {
					sends.Add(1)
				}
			}
		}(w)
	}
	// syncers and readers
	for w := 0; w < 4; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for ctx.Err() == nil {
				c := convos[rand.IntN(len(convos))]
				u := member(c)
				n, err := s.Latest(c)
				if err != nil {
					t.Errorf("Latest: %v", err)
					return
				}
				after := uint64(0)
				if n > 0 {
					after = uint64(rand.IntN(int(n)))
				}
				ms, err := s.Sync(c, u, after, 20)
				if err != nil {
					t.Errorf("Sync: %v", err)
					return
				}
				for i, m := range ms {
					if m.Seq != after+uint64(i)+1 {
						t.Errorf("Sync(%s, after %d) returned seq %d at position %d", c, after, m.Seq, i)
						return
					}
				}
				if n > 0 {
					if _, err := s.MarkRead(c, u, uint64(rand.IntN(int(n))+1)); err != nil {
						t.Errorf("MarkRead: %v", err)
						return
					}
				}
			}
		}()
	}
	// connectors: connect, read a little, close
	var open atomic.Int64
	for w := 0; w < 4; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for ctx.Err() == nil {
				u := users[rand.IntN(len(users))]
				sub, err := s.Connect(context.Background(), u)
				if err != nil {
					t.Errorf("Connect: %v", err)
					return
				}
				open.Add(1)
				last := map[string]uint64{}
				for i := 0; i < 20; i++ {
					select {
					case m, ok := <-sub.C:
						if !ok {
							t.Errorf("subscription closed unexpectedly")
							return
						}
						if m.Seq <= last[m.Conversation] {
							t.Errorf("out-of-order delivery for %s: seq %d after %d", m.Conversation, m.Seq, last[m.Conversation])
							return
						}
						last[m.Conversation] = m.Seq
					case <-time.After(time.Millisecond):
					}
				}
				sub.Close()
				open.Add(-1)
			}
		}()
	}
	time.Sleep(300 * time.Millisecond)
	cancel()
	wg.Wait()
	if t.Failed() {
		return
	}
	var stored int64
	for _, c := range convos {
		n, _ := s.Latest(c)
		stored += int64(n)
		log := tSyncAll(t, s, c, member(c), 0)
		tCheckDense(t, tSeqs(log), int(n), "final log of "+c)
		seen := map[string]bool{}
		for _, m := range log {
			k := m.Sender + "|" + m.ClientMsgID
			if seen[k] {
				t.Fatalf("%s: (sender %s, client id %s) stored twice", c, m.Sender, m.ClientMsgID)
			}
			seen[k] = true
		}
	}
	if stored != sends.Load() {
		t.Fatalf("stored %d messages but %d distinct (conversation, sender, client id) sends were made (%d retries)", stored, sends.Load(), dupes.Load())
	}
	if open.Load() != 0 {
		t.Fatalf("%d subscriptions left open by the test harness", open.Load())
	}
}
