//go:build breakit

package outbox

// Break-it experiments for A6 Unit 2 (§1–§2). Build tag breakit also turns on
// breakSplitDedupe, so Consumer.Handle commits its effect and its dedupe record
// in two transactions. These are measurements, not pass/fail tests.
//
// Run: go test -tags breakit -run BreakIt -v ./labs/a6/outbox/

import (
	"errors"
	"fmt"
	"math/rand"
	"testing"
	"time"
)

// TestBreakIt_DualWriteLosesOrInventsEvents compares three ways of "update my
// state and tell the world" when the process dies 10% of the time at the
// worst moment.
func TestBreakIt_DualWriteLosesOrInventsEvents(t *testing.T) {
	const ops = 1000
	const pCrash = 0.10

	// Approach A: commit the state, then publish. Crash in between: lost event.
	// Approach B: publish, then commit. Failed commit: phantom event.
	// Approach C: outbox in the same transaction, relay publishes later.
	type result struct{ stateChanges, events, lost, phantom, duplicates int }
	run := func(name string) result {
		rng := rand.New(rand.NewSource(42))
		var res result
		committed := map[int]bool{}
		published := map[int]int{}
		pub := func(op int) { published[op]++ }
		switch name {
		case "A":
			for op := 0; op < ops; op++ {
				committed[op] = true // db.Commit()
				if rng.Float64() < pCrash {
					continue // process died before broker.Publish
				}
				pub(op)
			}
		case "B":
			for op := 0; op < ops; op++ {
				pub(op) // broker.Publish()
				if rng.Float64() < pCrash {
					continue // db.Commit() failed / process died
				}
				committed[op] = true
			}
		case "C":
			clk := time.Date(2026, 5, 4, 12, 0, 0, 0, time.UTC)
			crashBetween := func(point string) func(p, n string) error {
				return func(p, n string) error {
					if p == point && rng.Float64() < pCrash {
						return errors.New("crash")
					}
					return nil
				}
			}
			db := NewDB(crashBetween(PointAfterCommit))
			for op := 0; op < ops; op++ {
				err := db.Do("t", func(tx *Tx) error {
					tx.Enqueue("t", "k", fmt.Sprint(op))
					return nil
				})
				_ = err // after-commit crash: the row IS there, the caller just doesn't know
			}
			for _, r := range db.Rows() {
				var n int
				fmt.Sscan(r.Msg.Payload, &n)
				committed[n] = true
			}
			sink := brokerFunc(func(m Message) error {
				var n int
				fmt.Sscan(m.Payload, &n)
				pub(n)
				return nil
			})
			relay := NewRelay(RelayConfig{ID: "r", DB: db, Broker: sink, Lease: time.Second, Batch: 50,
				Now: func() time.Time { return clk }, Hook: crashBetween(PointRelayAfterPublish)})
			for db.Pending() > 0 {
				relay.RunOnce()
				clk = clk.Add(2 * time.Second)
			}
		}
		res.stateChanges = len(committed)
		for op, n := range published {
			res.events++
			if !committed[op] {
				res.phantom++
			}
			if n > 1 {
				res.duplicates++
			}
		}
		for op := range committed {
			if published[op] == 0 {
				res.lost++
			}
		}
		return res
	}
	t.Logf("%-34s %8s %8s %8s %8s %8s", "approach (10% crash at worst moment)", "changes", "events", "lost", "phantom", "dupes")
	for _, a := range []struct{ id, label string }{{"A", "commit, then publish"}, {"B", "publish, then commit"}, {"C", "transactional outbox + relay"}} {
		r := run(a.id)
		t.Logf("%-34s %8d %8d %8d %8d %8d", a.label, r.stateChanges, r.events, r.lost, r.phantom, r.duplicates)
	}
	t.Log("Question: the outbox row has duplicates but no losses and no phantoms. Who is responsible for the duplicates, and how?")
}

type brokerFunc func(Message) error

func (f brokerFunc) Publish(m Message) error { return f(m) }

// TestBreakIt_SplitDedupeAppliesTwice: the consumer commits its effect and
// its dedupe record in separate transactions (breakSplitDedupe). A crash
// between them, followed by the broker's redelivery, credits the account
// twice.
func TestBreakIt_SplitDedupeAppliesTwice(t *testing.T) {
	const deliveries = 200
	crashes := 0
	db := NewDB(func(point, name string) error {
		if point == PointConsumerBetween {
			crashes++
			if crashes%2 == 1 { // each message dies once in the window (odd calls crash)
				return tErrCrash
			}
		}
		return nil
	})
	c := NewConsumer(db, tCredit)
	for i := 0; i < deliveries; i++ {
		m := Message{ID: fmt.Sprintf("msg-%d", i), Payload: "alice|bob|100"}
		for attempt := 0; ; attempt++ {
			_, err := c.Handle(m)
			if err == nil {
				break // acked
			}
			if attempt > 5 {
				t.Logf("Handle keeps failing with %v (is it implemented?)", err)
				return
			}
			// no ack: the broker redelivers the same message
		}
	}
	t.Logf("messages: %d, credited: %d cents (correct: %d), effect ran %d times (correct: %d)",
		deliveries, tBal(db, "credit:bob"), deliveries*100, tBal(db, "applied"), deliveries)
	t.Log("Question: which single line change makes this consumer correct again, and why must it be a transaction rather than a clever ordering?")
}
