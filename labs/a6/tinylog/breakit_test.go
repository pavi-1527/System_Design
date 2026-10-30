//go:build breakit

package tinylog

// Break-it experiments for A6 Unit 1 (§2–§4). These are measurements, not
// pass/fail tests: they print tables so you can compare strategies.
//
// Run: go test -tags breakit -run BreakIt -v ./labs/a6/tinylog/

import (
	"fmt"
	"testing"
)

// TestBreakIt_CommitTiming: a worker polls a batch of 10 messages and dies
// after completing the side effect of the 6th message of the batch. Count
// what a replacement worker loses and repeats under three commit strategies.
func TestBreakIt_CommitTiming(t *testing.T) {
	const batch, crashAfterEffects = 10, 6
	type strategy struct {
		name string
		run  func(c *Consumer, msgs []Message, effect func(Message) bool) // effect returns false once the worker "dies"
	}
	strategies := []strategy{
		{"commit BEFORE processing (at-most-once)", func(c *Consumer, msgs []Message, effect func(Message) bool) {
			for _, m := range msgs {
				if err := c.Commit(m); err != nil {
					t.Fatal(err)
				}
			}
			for _, m := range msgs {
				if !effect(m) {
					return
				}
			}
		}},
		{"commit AFTER each message (at-least-once)", func(c *Consumer, msgs []Message, effect func(Message) bool) {
			for _, m := range msgs {
				if !effect(m) {
					return
				}
				if err := c.Commit(m); err != nil {
					t.Fatal(err)
				}
			}
		}},
		{"commit once AFTER the whole batch", func(c *Consumer, msgs []Message, effect func(Message) bool) {
			for _, m := range msgs {
				if !effect(m) {
					return
				}
			}
			if err := c.Commit(msgs[len(msgs)-1]); err != nil {
				t.Fatal(err)
			}
		}},
	}
	t.Logf("%-42s %6s %10s", "strategy", "lost", "repeated")
	for _, s := range strategies {
		l := New(Config{Partitions: 1})
		for i := 0; i < 30; i++ {
			if _, _, err := l.Append("acct", fmt.Sprint(i)); err != nil {
				t.Logf("Append: %v (is the log implemented?)", err)
				return
			}
		}
		g := l.Group("g")
		w1, err := g.Join("w1")
		if err != nil {
			t.Logf("Join: %v (is the group implemented?)", err)
			return
		}
		// Process the first 10 messages normally, then crash in the second batch.
		first, _ := w1.Poll(batch)
		for _, m := range first {
			w1.Commit(m)
		}
		effects := map[string]int{}
		for _, m := range first {
			effects[m.Value]++
		}
		second, _ := w1.Poll(batch)
		done := 0
		s.run(w1, second, func(m Message) bool {
			effects[m.Value]++ // the side effect happens...
			done++
			return done < crashAfterEffects // ...and then the worker may die before it commits
		})
		w1.Close() // crash
		w2, err := g.Join("w2")
		if err != nil {
			t.Logf("Join: %v", err)
			return
		}
		for {
			msgs, err := w2.Poll(batch)
			if err != nil || len(msgs) == 0 {
				break
			}
			for _, m := range msgs {
				effects[m.Value]++
				w2.Commit(m)
			}
		}
		lost, repeated := 0, 0
		for i := 0; i < 30; i++ {
			switch n := effects[fmt.Sprint(i)]; {
			case n == 0:
				lost++
			case n > 1:
				repeated++
			}
		}
		t.Logf("%-42s %6d %10d", s.name, lost, repeated)
	}
	t.Log("Question: which row would you pick for a payments consumer, and what must the consumer then guarantee?")
}

// TestBreakIt_PartitionCountChangeRemapsKeys: hash(key) % P is not stable
// when P changes. How many keys change partition when you add one?
func TestBreakIt_PartitionCountChangeRemapsKeys(t *testing.T) {
	const keys = 20000
	t.Logf("%-14s %10s %10s", "partitions", "keys moved", "fraction")
	for _, p := range []int{3, 6, 12, 24, 48} {
		before, after := New(Config{Partitions: p}), New(Config{Partitions: p + 1})
		moved := 0
		for i := 0; i < keys; i++ {
			k := fmt.Sprintf("acct-%d", i)
			if before.PartitionFor(k) != after.PartitionFor(k) {
				moved++
			}
		}
		t.Logf("%-14s %10d %9.1f%%", fmt.Sprintf("%d -> %d", p, p+1), moved, 100*float64(moved)/keys)
	}
	t.Log("Question: for a topic with 24 partitions that already holds 3 days of per-account history, what breaks for per-key ordering the moment you add a partition?")
}

// TestBreakIt_HotKeyCapsTheGroup: 10 partitions, 10 consumers that each
// process 100 messages per tick. One tenant produces most of the traffic.
func TestBreakIt_HotKeyCapsTheGroup(t *testing.T) {
	const parts, perTick, total = 10, 100, 20000
	for _, hotShare := range []float64{0, 0.3, 0.6} {
		l := New(Config{Partitions: parts})
		hot := int(hotShare * total)
		for i := 0; i < total; i++ {
			key := fmt.Sprintf("acct-%d", i) // spread across ~20000 keys
			if i < hot {
				key = "tenant-big"
			}
			if _, _, err := l.Append(key, "x"); err != nil {
				t.Logf("Append: %v (is the log implemented?)", err)
				return
			}
		}
		maxEnd := int64(0)
		for p := 0; p < parts; p++ {
			end, _ := l.EndOffset(p)
			if end > maxEnd {
				maxEnd = end
			}
		}
		ideal := (total + parts*perTick - 1) / (parts * perTick)
		actual := (int(maxEnd) + perTick - 1) / perTick
		t.Logf("hot key share %3.0f%%: fullest partition %5d msgs, drain takes %3d ticks, perfectly balanced would take %d (%.1fx slower)",
			100*hotShare, maxEnd, actual, ideal, float64(actual)/float64(ideal))
	}
	t.Log("Question: you have 10 consumers. Why does adding an 11th not help? What would you change in the key, not the consumer count?")
}
