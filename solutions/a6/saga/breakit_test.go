//go:build breakit

package saga

// Break-it experiments for A6 Unit 3 (§1–§2). They reuse the fake services
// from saga_test.go and switch off, one at a time, the properties a saga
// relies on. These are measurements, not pass/fail tests.
//
// Run: go test -tags breakit -run BreakIt -v ./labs/a6/saga/

import (
	"context"
	"testing"
	"time"
)

func tAllLedgers(w *tWorld) []*tLedger {
	return []*tLedger{w.funds, w.records, w.network}
}

// TestBreakIt_NonIdempotentServicesDoubleApplyAfterACrash: the orchestrator
// is crashed once at every possible point of a successful run and then
// resumed. With idempotent services every row is "1 1 1". Make the services
// naive (they apply every call they receive) and see which crash points
// double-charge.
func TestBreakIt_NonIdempotentServicesDoubleApplyAfterACrash(t *testing.T) {
	var points []string
	{
		w := tNewWorld()
		o := tOrch(t, w.transferSteps(), NewMemoryStore(), func(c *Config) {
			c.Hook = func(p string) error { points = append(points, p); return nil }
		})
		if _, err := o.Run(context.Background(), "s1"); err != nil {
			t.Logf("dry run: %v (is the orchestrator implemented?)", err)
			return
		}
	}
	t.Logf("%-32s %7s %8s %8s   %s", "crash at", "reserve", "record", "submit", "")
	bad := 0
	for k := 1; k <= len(points); k++ {
		w, store := tNewWorld(), NewMemoryStore()
		for _, l := range tAllLedgers(w) {
			l.idem = false // naive services
		}
		n := 0
		crashing := tOrch(t, w.transferSteps(), store, func(c *Config) {
			c.Hook = func(string) error {
				n++
				if n == k {
					return tErrCrash
				}
				return nil
			}
		})
		crashing.Run(context.Background(), "s1")
		tOrch(t, w.transferSteps(), store).Run(context.Background(), "s1")
		verdict := ""
		if w.funds.value() != 1 || w.records.value() != 1 || w.network.value() != 1 {
			verdict = "<-- applied twice"
			bad++
		}
		t.Logf("#%-2d %-27s %7d %8d %8d   %s", k, points[k-1], w.funds.value(), w.records.value(), w.network.value(), verdict)
	}
	t.Logf("%d of %d crash points double-applied a step", bad, len(points))
	t.Log("Question: the orchestrator persisted state after every step. Why is that not enough? Which crash points are the dangerous ones, and why exactly those?")
}

// TestBreakIt_CompensationThatCannotRunBeforeItsAction: a step times out. The
// orchestrator compensates it (the outcome is unknown) and finishes the
// rollback. Then the hung call finally lands. Compare a service with a
// tombstone against one without, and a naive "refund" that assumes the
// action happened.
func TestBreakIt_CompensationThatCannotRunBeforeItsAction(t *testing.T) {
	type variant struct {
		name  string
		setup func(l *tLedger)
		late  bool // does the hung call complete after the rollback?
	}
	variants := []variant{
		{"idempotent + tombstone (correct)", func(l *tLedger) {}, true},
		{"no tombstone: late call lands", func(l *tLedger) { l.tombstones = false }, true},
		{"naive refund: call never happened", func(l *tLedger) { l.idem = false }, false},
	}
	t.Logf("%-40s %s", "network service", "net effect after rollback and the late call (0 is correct)")
	for _, v := range variants {
		w := tNewWorld()
		v.setup(w.network)
		gate := tNewGate(true)
		w.gateKey(Key("s1", "submit", false), gate)
		timers := &tTimers{}
		o := tOrch(t, w.transferSteps(), NewMemoryStore(), func(c *Config) { c.NewTimer = timers.NewTimer })
		done := make(chan struct{})
		go func() { o.Run(context.Background(), "s1"); close(done) }()
		select {
		case <-gate.started:
		case <-time.After(3 * time.Second):
			t.Logf("%-40s submit never started (is the orchestrator implemented?)", v.name)
			continue
		}
		timers.fire(t, 2)
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Logf("%-40s Run did not return after the timeout", v.name)
			continue
		}
		if v.late {
			close(gate.release)
			deadline := time.Now().Add(2 * time.Second)
			for w.network.callCount(Key("s1", "submit", false)) == 0 && time.Now().Before(deadline) {
				time.Sleep(time.Millisecond)
			}
		}
		t.Logf("%-40s %d", v.name, w.network.value())
	}
	t.Log("Question: why must a compensation be allowed to arrive BEFORE the action it undoes, and what does the service have to remember to make that safe?")
}
