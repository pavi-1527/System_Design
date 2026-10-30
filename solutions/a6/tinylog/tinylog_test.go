package tinylog

import (
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// ---------- helpers (prefixed with t to avoid clashing with your code) ----------

type tClock struct {
	mu sync.Mutex
	t  time.Time
}

func tNewClock() *tClock { return &tClock{t: time.Date(2026, 5, 4, 12, 0, 0, 0, time.UTC)} }
func (c *tClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}
func (c *tClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// tHashSuffix maps "anything-N" to N, so tests choose partitions directly:
// with 3 partitions, "k-4" lands in partition 1.
func tHashSuffix(key string) uint32 {
	i := strings.LastIndexByte(key, '-')
	n, err := strconv.Atoi(key[i+1:])
	if err != nil {
		return 0
	}
	return uint32(n)
}

func tAppend(t *testing.T, l *Log, key, value string) (int, int64) {
	t.Helper()
	p, off, err := l.Append(key, value)
	if err != nil {
		t.Fatalf("Append(%q, %q): %v", key, value, err)
	}
	return p, off
}

func tJoin(t *testing.T, g *Group, id string) *Consumer {
	t.Helper()
	c, err := g.Join(id)
	if err != nil || c == nil {
		t.Fatalf("Join(%q) = (%v, %v)", id, c, err)
	}
	return c
}

func tPoll(t *testing.T, c *Consumer, max int) []Message {
	t.Helper()
	msgs, err := c.Poll(max)
	if err != nil {
		t.Fatalf("%s.Poll(%d): %v", c.ID(), max, err)
	}
	return msgs
}

func tValues(msgs []Message) []string {
	out := make([]string, len(msgs))
	for i, m := range msgs {
		out[i] = m.Value
	}
	return out
}

func tEq(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// tWait waits for wg, failing the test instead of hanging if it takes longer
// than d (a deadlock or livelock in the implementation under test).
func tWait(t *testing.T, wg *sync.WaitGroup, d time.Duration, what string) {
	t.Helper()
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(d):
		t.Fatalf("%s did not finish within %v (deadlock or livelock in the implementation?)", what, d)
	}
}

func tOwners(t *testing.T, parts int, cs ...*Consumer) map[int]string {
	t.Helper()
	owner := map[int]string{}
	for _, c := range cs {
		for _, p := range c.Assignment() {
			if prev, dup := owner[p]; dup {
				t.Fatalf("partition %d is assigned to both %s and %s; two consumers in one group would process the same partition concurrently and break per-partition ordering", p, prev, c.ID())
			}
			owner[p] = c.ID()
		}
	}
	for p := 0; p < parts; p++ {
		if _, ok := owner[p]; !ok {
			t.Fatalf("partition %d has no owner; its messages would never be consumed", p)
		}
	}
	return owner
}

// ---------- the log ----------

func TestAppend_AssignsDenseOffsetsPerPartition(t *testing.T) {
	l := New(Config{Partitions: 3, Hash: tHashSuffix})
	type pos struct {
		p   int
		off int64
	}
	steps := []struct {
		key  string
		want pos
	}{
		{"k-0", pos{0, 0}}, {"k-1", pos{1, 0}}, {"k-0", pos{0, 1}},
		{"k-3", pos{0, 2}}, {"k-1", pos{1, 1}}, {"k-2", pos{2, 0}},
	}
	for i, s := range steps {
		p, off := tAppend(t, l, s.key, fmt.Sprint(i))
		if p != s.want.p || off != s.want.off {
			t.Fatalf("append #%d key %q landed at (partition %d, offset %d), want (%d, %d): offsets are per partition, dense and start at 0",
				i, s.key, p, off, s.want.p, s.want.off)
		}
	}
	for p, wantEnd := range []int64{3, 2, 1} {
		if end, err := l.EndOffset(p); err != nil || end != wantEnd {
			t.Fatalf("EndOffset(%d) = (%d, %v), want %d", p, end, err, wantEnd)
		}
	}
}

func TestAppend_SameKeyAlwaysLandsInSamePartition(t *testing.T) {
	l := New(Config{Partitions: 4})
	used := map[int]bool{}
	for k := 0; k < 100; k++ {
		key := fmt.Sprintf("account-%d", k)
		want := l.PartitionFor(key)
		for i := 0; i < 4; i++ {
			p, _ := tAppend(t, l, key, fmt.Sprint(i))
			if p != want {
				t.Fatalf("key %q went to partition %d, then %d: per-key ordering depends on a key always mapping to the same partition", key, want, p)
			}
		}
		used[want] = true
	}
	if len(used) < 2 {
		t.Fatalf("100 different keys all hashed to %d partition(s); the default hash must spread keys", len(used))
	}
}

func TestAppend_EmptyKeySpreadsRoundRobin(t *testing.T) {
	l := New(Config{Partitions: 3})
	var got []int
	for i := 0; i < 9; i++ {
		p, _ := tAppend(t, l, "", fmt.Sprint(i))
		got = append(got, p)
	}
	want := []int{0, 1, 2, 0, 1, 2, 0, 1, 2}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("keyless partitions = %v, want %v: with no key there is no ordering to protect, so spread evenly", got, want)
		}
	}
}

func TestAppendTo_RejectsUnknownPartition(t *testing.T) {
	l := New(Config{Partitions: 2})
	if _, err := l.AppendTo(2, "k", "v"); !errors.Is(err, ErrNoSuchPartition) {
		t.Fatalf("AppendTo(2) on a 2-partition log: err = %v, want ErrNoSuchPartition", err)
	}
	if off, err := l.AppendTo(1, "k", "v"); err != nil || off != 0 {
		t.Fatalf("AppendTo(1) = (%d, %v), want (0, nil)", off, err)
	}
}

func TestRead_ReturnsMessagesFromOffsetInOrder(t *testing.T) {
	l := New(Config{Partitions: 1})
	for i := 0; i < 5; i++ {
		tAppend(t, l, "k", fmt.Sprint(i))
	}
	got, err := l.Read(0, 2, 10)
	if err != nil || !tEq(tValues(got), []string{"2", "3", "4"}) {
		t.Fatalf("Read(0, 2, 10) = %v, %v; want values 2,3,4", tValues(got), err)
	}
	if got[0].Offset != 2 || got[0].Partition != 0 || got[0].Key != "k" {
		t.Fatalf("message metadata = %+v, want partition 0, offset 2, key k", got[0])
	}
	got, err = l.Read(0, 1, 2)
	if err != nil || !tEq(tValues(got), []string{"1", "2"}) {
		t.Fatalf("Read(0, 1, 2) = %v, %v; want values 1,2 (max respected)", tValues(got), err)
	}
	// Mutating the result must not corrupt the log.
	got[0].Value = "tampered"
	again, _ := l.Read(0, 1, 1)
	if len(again) != 1 || again[0].Value != "1" {
		t.Fatalf("Read returned a view into the log's storage; callers can corrupt history")
	}
}

func TestRead_EndOffsetIsEmptyButBeyondItIsAnError(t *testing.T) {
	l := New(Config{Partitions: 2})
	tAppend(t, l, "", "a")
	got, err := l.Read(0, 1, 5)
	if err != nil || len(got) != 0 {
		t.Fatalf("Read at EndOffset = (%v, %v), want no messages and no error (that is how a tailing consumer waits)", got, err)
	}
	if _, err := l.Read(0, 2, 5); !errors.Is(err, ErrOffsetOutOfRange) {
		t.Fatalf("Read beyond EndOffset: err = %v, want ErrOffsetOutOfRange", err)
	}
	if _, err := l.Read(0, -1, 5); !errors.Is(err, ErrOffsetOutOfRange) {
		t.Fatalf("Read(-1): err = %v, want ErrOffsetOutOfRange", err)
	}
	if _, err := l.Read(7, 0, 5); !errors.Is(err, ErrNoSuchPartition) {
		t.Fatalf("Read of partition 7: err = %v, want ErrNoSuchPartition", err)
	}
}

func TestRetention_MaxMessagesDropsOldestAndKeepsOffsetsStable(t *testing.T) {
	l := New(Config{Partitions: 1, MaxMessages: 5})
	for i := 0; i < 8; i++ {
		tAppend(t, l, "k", fmt.Sprint(i))
	}
	start, _ := l.StartOffset(0)
	end, _ := l.EndOffset(0)
	if start != 3 || end != 8 {
		t.Fatalf("Start/End = %d/%d, want 3/8 after keeping the newest 5 of 8", start, end)
	}
	if _, err := l.Read(0, 0, 10); !errors.Is(err, ErrOffsetOutOfRange) {
		t.Fatalf("Read of a truncated offset: err = %v, want ErrOffsetOutOfRange (the reader must learn it fell behind)", err)
	}
	got, err := l.Read(0, 3, 10)
	if err != nil || len(got) != 5 || got[0].Offset != 3 || got[0].Value != "3" || got[4].Offset != 7 {
		t.Fatalf("Read(0, 3) = %+v, %v; want offsets 3..7 with their original values: truncation must not renumber", got, err)
	}
}

func TestRetention_MaxAgeDropsOldMessagesOnApply(t *testing.T) {
	clk := tNewClock()
	l := New(Config{Partitions: 1, MaxAge: time.Hour, Now: clk.Now})
	tAppend(t, l, "k", "old-1")
	tAppend(t, l, "k", "old-2")
	clk.Advance(40 * time.Minute)
	tAppend(t, l, "k", "mid")
	clk.Advance(30 * time.Minute) // old-* are now 70 min old, mid is 30 min old
	l.ApplyRetention()
	start, _ := l.StartOffset(0)
	if start != 2 {
		t.Fatalf("StartOffset = %d after ageing out two messages, want 2", start)
	}
	got, err := l.Read(0, 2, 10)
	if err != nil || !tEq(tValues(got), []string{"mid"}) {
		t.Fatalf("remaining = %v, %v; want [mid]", tValues(got), err)
	}
}

func TestPerKeyOrdering_HoldsUnderConcurrentProducers(t *testing.T) {
	const producers, keysPer, perKey = 8, 6, 50
	l := New(Config{Partitions: 4})
	var wg sync.WaitGroup
	for pr := 0; pr < producers; pr++ {
		wg.Add(1)
		go func(pr int) {
			defer wg.Done()
			// One producer owns each key and writes its sequence in order,
			// interleaving its keys the way a service handling many accounts would.
			for seq := 0; seq < perKey; seq++ {
				for k := 0; k < keysPer; k++ {
					key := fmt.Sprintf("acct-%d-%d", pr, k)
					if _, _, err := l.Append(key, strconv.Itoa(seq)); err != nil {
						t.Errorf("Append: %v", err)
						return
					}
				}
			}
		}(pr)
	}
	tWait(t, &wg, 10*time.Second, "producers")

	last := map[string]int{}
	total := 0
	for p := 0; p < l.Partitions(); p++ {
		msgs, err := l.Read(p, 0, 1<<20)
		if err != nil {
			t.Fatalf("Read(%d): %v", p, err)
		}
		for i, m := range msgs {
			if m.Offset != int64(i) {
				t.Fatalf("partition %d position %d has offset %d: offsets must be dense even under concurrent appends", p, i, m.Offset)
			}
			seq, _ := strconv.Atoi(m.Value)
			if prev, seen := last[m.Key]; seen && seq != prev+1 {
				t.Fatalf("key %s: saw seq %d after %d; a key's messages must stay in the order its producer wrote them", m.Key, seq, prev)
			} else if !seen && seq != 0 {
				t.Fatalf("key %s: first message seen has seq %d, want 0", m.Key, seq)
			}
			last[m.Key] = seq
			total++
		}
	}
	if want := producers * keysPer * perKey; total != want {
		t.Fatalf("read back %d messages, want %d: concurrent appends were lost", total, want)
	}
}

// ---------- groups: assignment and rebalancing ----------

func TestGroup_SingleMemberGetsEveryPartition(t *testing.T) {
	l := New(Config{Partitions: 4})
	c := tJoin(t, l.Group("g"), "a")
	if got := c.Assignment(); len(got) != 4 {
		t.Fatalf("assignment = %v, want all 4 partitions", got)
	}
	if g := l.Group("g"); g.Generation() < 1 {
		t.Fatalf("generation = %d after the first join, want >= 1", g.Generation())
	}
}

func TestGroup_PartitionsAreSplitWithExactlyOneOwnerEach(t *testing.T) {
	l := New(Config{Partitions: 6})
	g := l.Group("g")
	a, b, c := tJoin(t, g, "a"), tJoin(t, g, "b"), tJoin(t, g, "c")
	for _, x := range []*Consumer{a, b} {
		if err := x.Rejoin(); err != nil {
			t.Fatalf("%s.Rejoin: %v", x.ID(), err)
		}
	}
	tOwners(t, 6, a, b, c)
	for _, x := range []*Consumer{a, b, c} {
		if n := len(x.Assignment()); n != 2 {
			t.Fatalf("%s owns %d partitions, want 2 (6 partitions over 3 members)", x.ID(), n)
		}
	}
	// c leaves: two members, three partitions each.
	c.Close()
	for _, x := range []*Consumer{a, b} {
		if err := x.Rejoin(); err != nil {
			t.Fatalf("%s.Rejoin: %v", x.ID(), err)
		}
	}
	tOwners(t, 6, a, b)
	if len(a.Assignment()) != 3 || len(b.Assignment()) != 3 {
		t.Fatalf("after c left, assignments are %v and %v, want 3 partitions each", a.Assignment(), b.Assignment())
	}
}

func TestGroup_MoreMembersThanPartitionsLeavesSomeIdle(t *testing.T) {
	l := New(Config{Partitions: 2})
	g := l.Group("g")
	a, b, c := tJoin(t, g, "a"), tJoin(t, g, "b"), tJoin(t, g, "c")
	a.Rejoin()
	b.Rejoin()
	tOwners(t, 2, a, b, c)
	idle := 0
	for _, x := range []*Consumer{a, b, c} {
		if len(x.Assignment()) == 0 {
			idle++
		}
	}
	if idle != 1 {
		t.Fatalf("%d idle members with 3 members and 2 partitions, want exactly 1: parallelism is capped by the partition count", idle)
	}
}

func TestGroup_DuplicateMemberIDIsRejected(t *testing.T) {
	l := New(Config{Partitions: 2})
	g := l.Group("g")
	tJoin(t, g, "a")
	if _, err := g.Join("a"); !errors.Is(err, ErrDuplicateMember) {
		t.Fatalf("second Join(a): err = %v, want ErrDuplicateMember", err)
	}
}

func TestGroup_RebalanceBumpsGenerationAndStopsStaleMembers(t *testing.T) {
	l := New(Config{Partitions: 2})
	g := l.Group("g")
	a := tJoin(t, g, "a")
	gen1 := g.Generation()
	b := tJoin(t, g, "b")
	if g.Generation() != gen1+1 {
		t.Fatalf("generation = %d after a second member joined, want %d", g.Generation(), gen1+1)
	}
	if _, err := a.Poll(1); !errors.Is(err, ErrRebalance) {
		t.Fatalf("stale member Poll: err = %v, want ErrRebalance (a stop-the-world rebalance pauses consumption until members rejoin)", err)
	}
	if err := a.Rejoin(); err != nil {
		t.Fatalf("Rejoin: %v", err)
	}
	if _, err := a.Poll(1); err != nil {
		t.Fatalf("Poll after Rejoin: %v", err)
	}
	if a.Generation() != g.Generation() || b.Generation() != g.Generation() {
		t.Fatalf("generations a=%d b=%d group=%d, want all equal", a.Generation(), b.Generation(), g.Generation())
	}
}

func TestGroup_EvictedConsumerIsFencedEverywhere(t *testing.T) {
	l := New(Config{Partitions: 2})
	g := l.Group("g")
	zombie := tJoin(t, g, "a")
	tAppend(t, l, "", "x")
	m := tPoll(t, zombie, 1)
	if !g.Evict("a") {
		t.Fatalf("Evict(a) = false for a current member")
	}
	if g.Evict("a") {
		t.Fatalf("Evict(a) = true for a member that is already gone")
	}
	if _, err := zombie.Poll(1); !errors.Is(err, ErrFenced) {
		t.Fatalf("zombie Poll: err = %v, want ErrFenced", err)
	}
	if err := zombie.Commit(m[0]); !errors.Is(err, ErrFenced) {
		t.Fatalf("zombie Commit: err = %v, want ErrFenced: a paused-then-resumed consumer must not be able to overwrite the new owner's progress", err)
	}
	if err := zombie.Rejoin(); !errors.Is(err, ErrFenced) {
		t.Fatalf("zombie Rejoin: err = %v, want ErrFenced", err)
	}
	// The same member id can come back as a brand-new consumer.
	fresh := tJoin(t, g, "a")
	if _, err := fresh.Poll(1); err != nil {
		t.Fatalf("fresh consumer Poll: %v", err)
	}
	if _, err := zombie.Poll(1); !errors.Is(err, ErrFenced) {
		t.Fatalf("the old Consumer value must stay fenced after its id is reused, got %v", err)
	}
}

// ---------- groups: polling, commits, delivery semantics ----------

func TestPoll_TakesFairlyFromEveryAssignedPartition(t *testing.T) {
	l := New(Config{Partitions: 2})
	for i := 0; i < 100; i++ {
		if _, err := l.AppendTo(0, "", fmt.Sprintf("hot-%d", i)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := l.AppendTo(1, "", "lonely"); err != nil {
		t.Fatal(err)
	}
	c := tJoin(t, l.Group("g"), "a")
	got := tValues(tPoll(t, c, 4))
	want := []string{"hot-0", "lonely", "hot-1", "hot-2"}
	if !tEq(got, want) {
		t.Fatalf("Poll(4) = %v, want %v: one busy partition must not starve the others", got, want)
	}
}

func TestPoll_NoCrossPartitionOrderIsPromised(t *testing.T) {
	l := New(Config{Partitions: 2, Hash: tHashSuffix})
	tAppend(t, l, "acct-1", "debit-in-partition-1") // appended FIRST
	tAppend(t, l, "acct-0", "credit-in-partition-0")
	got := tValues(tPoll(t, tJoin(t, l.Group("g"), "a"), 10))
	want := []string{"credit-in-partition-0", "debit-in-partition-1"}
	if !tEq(got, want) {
		t.Fatalf("Poll = %v, want %v: the consumer sees partitions in partition order, not append order. If two events must be ordered, they need the same key (same partition)", got, want)
	}
	// ...but within a partition order is always the append order.
	l2 := New(Config{Partitions: 2, Hash: tHashSuffix})
	for i := 0; i < 5; i++ {
		tAppend(t, l2, "acct-1", fmt.Sprint(i))
	}
	got = tValues(tPoll(t, tJoin(t, l2.Group("g"), "a"), 10))
	if !tEq(got, []string{"0", "1", "2", "3", "4"}) {
		t.Fatalf("same-key messages = %v, want append order", got)
	}
}

func TestCommit_AdvancesCommittedOffsetAndNeverMovesBackwards(t *testing.T) {
	l := New(Config{Partitions: 1})
	for i := 0; i < 5; i++ {
		tAppend(t, l, "k", fmt.Sprint(i))
	}
	g := l.Group("g")
	c := tJoin(t, g, "a")
	msgs := tPoll(t, c, 5)
	if got := g.Committed(0); got != 0 {
		t.Fatalf("Committed before any commit = %d, want 0 (start of the log)", got)
	}
	if err := c.Commit(msgs[2]); err != nil {
		t.Fatalf("Commit(offset 2): %v", err)
	}
	if got := g.Committed(0); got != 3 {
		t.Fatalf("Committed = %d after committing offset 2, want 3 (the NEXT offset to read)", got)
	}
	if err := c.Commit(msgs[0]); err != nil {
		t.Fatalf("late Commit(offset 0): %v", err)
	}
	if got := g.Committed(0); got != 3 {
		t.Fatalf("Committed = %d after an older commit, want 3: commits never go backwards", got)
	}
	if got := g.Lag(0); got != 2 {
		t.Fatalf("Lag = %d, want 2 (offsets 3 and 4 are unprocessed)", got)
	}
}

func TestCommit_RejectsPartitionsTheConsumerDoesNotOwn(t *testing.T) {
	l := New(Config{Partitions: 2, Hash: tHashSuffix})
	tAppend(t, l, "k-0", "a")
	tAppend(t, l, "k-1", "b")
	g := l.Group("g")
	a := tJoin(t, g, "a")
	b := tJoin(t, g, "b")
	a.Rejoin()
	am := tPoll(t, a, 5)
	bm := tPoll(t, b, 5)
	if len(am) != 1 || len(bm) != 1 {
		t.Fatalf("a polled %d, b polled %d messages, want 1 each", len(am), len(bm))
	}
	if err := a.Commit(bm[0]); !errors.Is(err, ErrNotAssigned) {
		t.Fatalf("committing another member's partition: err = %v, want ErrNotAssigned", err)
	}
}

func TestAtLeastOnce_CrashBeforeCommitRedeliversTheUncommittedTail(t *testing.T) {
	l := New(Config{Partitions: 1})
	for i := 0; i < 6; i++ {
		tAppend(t, l, "k", fmt.Sprint(i))
	}
	g := l.Group("g")
	c1 := tJoin(t, g, "worker-1")
	msgs := tPoll(t, c1, 6)
	// Process all six, but only manage to commit the first three before dying.
	for _, m := range msgs[:3] {
		if err := c1.Commit(m); err != nil {
			t.Fatal(err)
		}
	}
	c1.Close() // crash: no more commits

	c2 := tJoin(t, g, "worker-2")
	redelivered := tValues(tPoll(t, c2, 10))
	if !tEq(redelivered, []string{"3", "4", "5"}) {
		t.Fatalf("after the crash the new owner got %v, want [3 4 5]: processed-but-uncommitted messages are delivered again (at-least-once), committed ones are not", redelivered)
	}
}

func TestAtMostOnce_CommitBeforeProcessingLosesMessagesOnCrash(t *testing.T) {
	l := New(Config{Partitions: 1})
	for i := 0; i < 4; i++ {
		tAppend(t, l, "k", fmt.Sprint(i))
	}
	g := l.Group("g")
	c1 := tJoin(t, g, "worker-1")
	msgs := tPoll(t, c1, 4)
	for _, m := range msgs { // commit first...
		if err := c1.Commit(m); err != nil {
			t.Fatal(err)
		}
	}
	// ...then crash before processing anything.
	c1.Close()
	c2 := tJoin(t, g, "worker-2")
	if got := tPoll(t, c2, 10); len(got) != 0 {
		t.Fatalf("new owner received %v; with commit-before-process nothing is redelivered, so the 4 unprocessed messages are lost (at-most-once)", tValues(got))
	}
}

func TestCommit_StaleGenerationIsRejectedAndMessageIsRedelivered(t *testing.T) {
	l := New(Config{Partitions: 1})
	tAppend(t, l, "k", "payment-17")
	g := l.Group("g")
	slow := tJoin(t, g, "slow")
	m := tPoll(t, slow, 1)
	if len(m) != 1 {
		t.Fatalf("polled %d messages, want 1", len(m))
	}
	// While "slow" is stuck processing (GC pause, slow vendor call), the group
	// rebalances and the partition moves to the other member.
	other := tJoin(t, g, "a-other") // sorts before "slow", so it takes partition 0
	if err := slow.Commit(m[0]); !errors.Is(err, ErrStaleGeneration) {
		t.Fatalf("Commit from a stale generation: err = %v, want ErrStaleGeneration", err)
	}
	if got := g.Committed(0); got != 0 {
		t.Fatalf("Committed = %d, want 0: a stale member must not advance the group's progress", got)
	}
	got := tValues(tPoll(t, other, 5))
	if !tEq(got, []string{"payment-17"}) {
		t.Fatalf("new owner got %v, want [payment-17]: the message is processed a second time, so consumers must be idempotent", got)
	}
}

func TestRejoin_RewindsToCommittedSoUncommittedWorkIsRepeated(t *testing.T) {
	l := New(Config{Partitions: 1})
	for i := 0; i < 3; i++ {
		tAppend(t, l, "k", fmt.Sprint(i))
	}
	g := l.Group("g")
	a := tJoin(t, g, "a")
	msgs := tPoll(t, a, 3)
	if err := a.Commit(msgs[0]); err != nil {
		t.Fatal(err)
	}
	tJoin(t, g, "b") // rebalance: partition 0 stays with "a" (sorts first)
	if err := a.Rejoin(); err != nil {
		t.Fatal(err)
	}
	got := tValues(tPoll(t, a, 10))
	if !tEq(got, []string{"1", "2"}) {
		t.Fatalf("after Rejoin, Poll = %v, want [1 2]: positions reset to the committed offset, so polled-but-uncommitted messages are read again", got)
	}
}

func TestGroups_AreIndependentSubscriptions(t *testing.T) {
	l := New(Config{Partitions: 2})
	for i := 0; i < 6; i++ {
		tAppend(t, l, "", fmt.Sprint(i))
	}
	billing := tJoin(t, l.Group("billing"), "b1")
	audit := tJoin(t, l.Group("audit"), "a1")
	if n := len(tPoll(t, billing, 10)); n != 6 {
		t.Fatalf("billing group saw %d messages, want 6", n)
	}
	if n := len(tPoll(t, audit, 10)); n != 6 {
		t.Fatalf("audit group saw %d messages, want 6: every group gets its own full copy of the stream (pub/sub fan-out)", n)
	}
	bg := l.Group("billing")
	for p := 0; p < 2; p++ {
		msgs, _ := l.Read(p, 0, 10)
		for _, m := range msgs {
			if err := billing.Commit(m); err != nil {
				t.Fatal(err)
			}
		}
	}
	if bg.TotalLag() != 0 || l.Group("audit").TotalLag() != 6 {
		t.Fatalf("lag billing=%d audit=%d, want 0 and 6: offsets are tracked per group", bg.TotalLag(), l.Group("audit").TotalLag())
	}
}

func TestRetention_ConsumerThatFellBehindSkipsAheadAndTheLossIsCounted(t *testing.T) {
	l := New(Config{Partitions: 1, MaxMessages: 5})
	g := l.Group("g")
	c := tJoin(t, g, "a")
	for i := 0; i < 12; i++ {
		tAppend(t, l, "k", fmt.Sprint(i))
	}
	got := tPoll(t, c, 20)
	if len(got) != 5 || got[0].Offset != 7 {
		t.Fatalf("Poll after retention = %d messages starting at offset %d, want 5 messages starting at 7", len(got), func() int64 {
			if len(got) == 0 {
				return -1
			}
			return got[0].Offset
		}())
	}
	if g.Skipped() != 7 {
		t.Fatalf("Skipped = %d, want 7: messages 0..6 were deleted before this group read them. This is silent data loss unless someone alerts on it", g.Skipped())
	}
}

func TestResetOffset_ReplaysHistory(t *testing.T) {
	l := New(Config{Partitions: 1})
	for i := 0; i < 4; i++ {
		tAppend(t, l, "k", fmt.Sprint(i))
	}
	g := l.Group("g")
	c := tJoin(t, g, "a")
	for _, m := range tPoll(t, c, 10) {
		if err := c.Commit(m); err != nil {
			t.Fatal(err)
		}
	}
	if g.TotalLag() != 0 {
		t.Fatalf("lag = %d after committing everything, want 0", g.TotalLag())
	}
	if err := g.ResetOffset(0, 1); err != nil {
		t.Fatalf("ResetOffset: %v", err)
	}
	if err := c.Rejoin(); err != nil {
		t.Fatal(err)
	}
	if got := tValues(tPoll(t, c, 10)); !tEq(got, []string{"1", "2", "3"}) {
		t.Fatalf("replay from offset 1 returned %v, want [1 2 3]: a log lets you re-read history; a queue that deletes on ack cannot", got)
	}
	if err := g.ResetOffset(0, 99); !errors.Is(err, ErrOffsetOutOfRange) {
		t.Fatalf("ResetOffset(99): err = %v, want ErrOffsetOutOfRange", err)
	}
}

// ---------- concurrency ----------

func TestGroup_ConcurrentProducersAndConsumersDeliverEverythingOnceWhenNothingFails(t *testing.T) {
	const producers, perProducer = 4, 250
	const total = producers * perProducer
	l := New(Config{Partitions: 4})
	g := l.Group("g")
	cs := []*Consumer{tJoin(t, g, "c1"), tJoin(t, g, "c2"), tJoin(t, g, "c3")}
	for _, c := range cs[:2] {
		if err := c.Rejoin(); err != nil {
			t.Fatal(err)
		}
	}
	tOwners(t, 4, cs...)

	var mu sync.Mutex
	seen := map[string]int{}
	var consumed atomic.Int64
	var stop atomic.Bool

	var pwg, cwg sync.WaitGroup
	for p := 0; p < producers; p++ {
		pwg.Add(1)
		go func(p int) {
			defer pwg.Done()
			for i := 0; i < perProducer; i++ {
				if _, _, err := l.Append(fmt.Sprintf("acct-%d", (p*perProducer+i)%37), fmt.Sprintf("m-%d-%d", p, i)); err != nil {
					t.Errorf("Append: %v", err)
					return
				}
			}
		}(p)
	}
	for _, c := range cs {
		cwg.Add(1)
		go func(c *Consumer) {
			defer cwg.Done()
			for !stop.Load() {
				msgs, err := c.Poll(7)
				if err != nil {
					t.Errorf("%s.Poll: %v (no membership change happened, so this must not fail)", c.ID(), err)
					return
				}
				if len(msgs) == 0 {
					time.Sleep(200 * time.Microsecond) // yield; correctness does not depend on it
					continue
				}
				for _, m := range msgs {
					mu.Lock()
					seen[m.Value]++
					mu.Unlock()
					if err := c.Commit(m); err != nil {
						t.Errorf("%s.Commit: %v", c.ID(), err)
						return
					}
					consumed.Add(1)
				}
			}
		}(c)
	}
	tWait(t, &pwg, 10*time.Second, "producers")
	deadline := time.Now().Add(10 * time.Second)
	for consumed.Load() < total && time.Now().Before(deadline) && !t.Failed() {
		time.Sleep(time.Millisecond)
	}
	stop.Store(true)
	tWait(t, &cwg, 5*time.Second, "consumers")

	mu.Lock()
	defer mu.Unlock()
	if len(seen) != total {
		t.Fatalf("consumed %d distinct messages, want %d", len(seen), total)
	}
	for v, n := range seen {
		if n != 1 {
			t.Fatalf("message %s delivered %d times with no failures and no rebalances; want exactly once", v, n)
		}
	}
	if g.TotalLag() != 0 {
		t.Fatalf("lag = %d after everything was committed, want 0", g.TotalLag())
	}
}

func TestGroup_RebalanceChurnStillDeliversEveryMessageAtLeastOnce(t *testing.T) {
	const total = 600
	l := New(Config{Partitions: 6})
	for i := 0; i < total; i++ {
		tAppend(t, l, fmt.Sprintf("acct-%d", i%23), fmt.Sprintf("m-%d", i))
	}
	g := l.Group("g")
	var cs []*Consumer
	for _, id := range []string{"c1", "c2", "c3"} {
		cs = append(cs, tJoin(t, g, id))
	}
	var mu sync.Mutex
	seen := map[string]int{}
	var stop atomic.Bool
	var wg sync.WaitGroup
	for _, c := range cs {
		wg.Add(1)
		go func(c *Consumer) {
			defer wg.Done()
			for !stop.Load() {
				msgs, err := c.Poll(5)
				switch {
				case errors.Is(err, ErrRebalance):
					if err := c.Rejoin(); err != nil {
						t.Errorf("%s.Rejoin: %v", c.ID(), err)
						return
					}
					continue
				case err != nil:
					t.Errorf("%s.Poll: %v", c.ID(), err)
					return
				}
				if len(msgs) == 0 {
					time.Sleep(100 * time.Microsecond)
					continue
				}
				for _, m := range msgs {
					mu.Lock()
					seen[m.Value]++
					mu.Unlock()
					if err := c.Commit(m); err != nil {
						if errors.Is(err, ErrStaleGeneration) {
							break // our batch is now someone else's problem; poll again after Rejoin
						}
						t.Errorf("%s.Commit: %v", c.ID(), err)
						return
					}
				}
			}
		}(c)
	}
	// A fourth member repeatedly joins and leaves while the others work.
	var churn sync.WaitGroup
	churn.Add(1)
	go func() {
		defer churn.Done()
		for i := 0; i < 25; i++ {
			x, err := g.Join("churner")
			if err != nil {
				t.Errorf("churner Join: %v", err)
				return
			}
			time.Sleep(200 * time.Microsecond)
			x.Close()
			time.Sleep(200 * time.Microsecond)
		}
	}()
	tWait(t, &churn, 10*time.Second, "membership churn")

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) && !t.Failed() {
		mu.Lock()
		n := len(seen)
		mu.Unlock()
		if n == total {
			break
		}
		time.Sleep(time.Millisecond)
	}
	stop.Store(true)
	tWait(t, &wg, 5*time.Second, "consumers")

	mu.Lock()
	defer mu.Unlock()
	if len(seen) != total {
		var missing []string
		for i := 0; i < total && len(missing) < 5; i++ {
			if seen[fmt.Sprintf("m-%d", i)] == 0 {
				missing = append(missing, fmt.Sprintf("m-%d", i))
			}
		}
		sort.Strings(missing)
		t.Fatalf("only %d of %d messages were ever delivered (e.g. missing %v): rebalances must never lose messages, only repeat them", len(seen), total, missing)
	}
}
