//go:build breakit

package breakit

import (
	"bytes"
	"errors"
	"fmt"
	"runtime/pprof"
	"sort"
	"sync"
	"testing"
	"time"
)

// ---- 1. Observer: callbacks under a lock (Unit §2) ----------------------------
//
// An audit sink wants to attach a second listener the first time it sees an
// event. The bus below holds its mutex while it calls subscribers. Predict
// what Publish does, then run:
//
//	go test -tags breakit -run '^TestNaiveBus' ./labs/b2/breakit/
//
// Fix: copy the subscriber slice under the lock, release the lock, then call.

type naiveBus struct {
	mu   sync.Mutex
	subs []func(string)
}

func (b *naiveBus) Subscribe(fn func(string)) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.subs = append(b.subs, fn)
}

func (b *naiveBus) Publish(e string) {
	b.mu.Lock()
	defer b.mu.Unlock() // BUG: the lock is still held while subscribers run
	for _, fn := range b.subs {
		fn(e)
	}
}

func TestNaiveBus_CallbackThatSubscribesDeadlocks(t *testing.T) {
	b := &naiveBus{}
	var once sync.Once
	b.Subscribe(func(string) {
		once.Do(func() { b.Subscribe(func(string) {}) }) // re-enters the bus from inside a callback
	})

	done := make(chan struct{})
	go func() {
		b.Publish("settled")
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		var buf bytes.Buffer
		_ = pprof.Lookup("goroutine").WriteTo(&buf, 1)
		t.Fatalf("Publish did not return within 2s: a subscriber called Subscribe while Publish held the lock.\n"+
			"Read the profile below: find the goroutine parked in sync.(*Mutex).Lock and the callback above it.\n%s", buf.String())
	}
}

// ---- 2. State: a transition table with holes (Unit §3) -------------------------
//
// The lifecycle the business wants is in `want`. The table below was typed in
// by hand and has two bugs. Predict which edges the diff will report and which
// state the reachability test blames, then run:
//
//	go test -tags breakit -run '^TestBrokenTable' ./labs/b2/breakit/
//
// Fix the table until both tests pass.

var brokenTable = map[string][]string{
	"initiated":  {"authorise", "fail"},
	"authorised": {"settle"},  // BUG 1
	"settled":    {"reverce"}, // BUG 2
	"failed":     {},
	"reversed":   {},
}

var want = map[string][]string{
	"initiated":  {"authorise", "fail"},
	"authorised": {"fail", "settle"},
	"settled":    {"reverse"},
	"failed":     {},
	"reversed":   {},
}

var states = []string{"initiated", "authorised", "settled", "failed", "reversed"}

var terminal = map[string]bool{"failed": true, "reversed": true}

// target says where each known event leads. Misspelled events lead nowhere.
var target = map[string]string{"authorise": "authorised", "settle": "settled", "fail": "failed", "reverse": "reversed"}

func TestBrokenTable_MatchesTheBusinessLifecycle(t *testing.T) {
	for _, state := range states {
		wantEvents := want[state]
		have := map[string]bool{}
		for _, ev := range brokenTable[state] {
			have[ev] = true
		}
		for _, ev := range wantEvents {
			if !have[ev] {
				t.Errorf("missing edge: %s --%s--> (a transfer here can never take this step)", state, ev)
			}
			delete(have, ev)
		}
		var extra []string
		for ev := range have {
			extra = append(extra, ev)
		}
		sort.Strings(extra)
		for _, ev := range extra {
			t.Errorf("unexpected edge: %s --%s--> (not part of the lifecycle; likely a typo)", state, ev)
		}
	}
}

func TestBrokenTable_EveryStateIsReachableAndEveryNonTerminalStateHasAWayOut(t *testing.T) {
	seen := map[string]bool{"initiated": true}
	queue := []string{"initiated"}
	for len(queue) > 0 {
		s := queue[0]
		queue = queue[1:]
		for _, ev := range brokenTable[s] {
			if n, ok := target[ev]; ok && !seen[n] {
				seen[n] = true
				queue = append(queue, n)
			}
		}
	}
	for _, state := range states {
		if !seen[state] {
			t.Errorf("state %q is unreachable from initiated: no sequence of the table's events leads there", state)
		}
		known := 0
		for _, ev := range brokenTable[state] {
			if _, ok := target[ev]; ok {
				known++
			}
		}
		if !terminal[state] && known == 0 {
			t.Errorf("state %q is a dead end but is not terminal: transfers pile up here forever", state)
		}
	}
}

// ---- 3. Decorator order: where the retry sits (Unit §6) -----------------------
//
// Requirement from the vendor contract: "once our breaker's threshold of 3
// failures is reached we must stop calling the vendor". The chain below
// satisfies the letter of "retry and breaker" but not the requirement. Predict
// the number of vendor calls before the first ErrOpen, then run:
//
//	go test -tags breakit -run '^TestDecoratorOrder' ./labs/b2/breakit/
//
// Fix: change the order of the two wrappers (one line), and explain what the
// breaker now counts.

type call func() error

var errOpen = errors.New("circuit open")

func retry(attempts int, next call) call {
	return func() error {
		var err error
		for i := 0; i < attempts; i++ {
			if err = next(); err == nil || errors.Is(err, errOpen) {
				return err
			}
		}
		return err
	}
}

func breaker(threshold int, next call) call {
	failures := 0
	return func() error {
		if failures >= threshold {
			return errOpen
		}
		err := next()
		if err != nil {
			failures++
		} else {
			failures = 0
		}
		return err
	}
}

func TestDecoratorOrder_BreakerMustBoundVendorCalls(t *testing.T) {
	vendorCalls := 0
	vendor := func() error { vendorCalls++; return errors.New("vendor down") }

	chain := breaker(3, retry(3, vendor)) // BUG: the breaker only sees one failure per logical call

	for logical := 1; logical <= 20; logical++ {
		if err := chain(); errors.Is(err, errOpen) {
			break
		}
	}
	if vendorCalls > 3 {
		t.Fatalf("the vendor was called %d times before the circuit opened; the contract allows 3. "+
			"Which wrapper sees each attempt, and which sees only the logical call?", vendorCalls)
	}
	fmt.Println("vendor calls before open:", vendorCalls)
}
