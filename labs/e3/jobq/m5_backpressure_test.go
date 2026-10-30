package jobq

// Milestone 5: backpressure and graceful drain. A bounded queue answers 429 +
// Retry-After instead of growing without limit, a producer's retry of an
// accepted job is still recognised when the queue is full, and SIGTERM drains
// a worker without losing or duplicating anything.

import (
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestM5_AFullQueueRefusesWith429AndRetryAfter(t *testing.T) {
	t.Parallel()
	b := tStartBroker(t, t.TempDir(), "-max-depth", "50", "-fsync=false")
	const n = 80
	var wg sync.WaitGroup
	var mu sync.Mutex
	codes := map[int]int{}
	missingRetryAfter := 0
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			code, _, h, err := tDo("POST", b.URL+"/queues/default/jobs", fmt.Sprintf(`{"payload":{"n":%d}}`, i))
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				codes[-1]++
				return
			}
			codes[code]++
			if code == http.StatusTooManyRequests {
				if _, ok := ParseRetryAfter(h.Get("Retry-After"), time.Now()); !ok {
					missingRetryAfter++
				}
			}
		}()
	}
	wg.Wait()
	if codes[http.StatusCreated] != 50 || codes[http.StatusTooManyRequests] != 30 {
		t.Fatalf("80 concurrent enqueues into a queue bounded at 50: %v; want 50 × 201 and 30 × 429", codes)
	}
	if missingRetryAfter > 0 {
		t.Errorf("%d of the 429s had no valid Retry-After; a producer needs to know when to come back", missingRetryAfter)
	}
	if s := tQueueStats(t, b.URL, "default"); s.Depth != 50 || s.Rejected != 30 {
		t.Errorf("stats: %+v; want depth 50 (bounded: memory and drain time are both proportional to it), rejected 30", s)
	}
	_, j := tLease(t, b.URL, "default", 0, 0)
	tAck(t, b.URL, j, j.Lease)
	if code, _, _ := tEnqueue(t, b.URL, "default", `{"n":"after"}`, "", 0); code != http.StatusCreated {
		t.Errorf("enqueue after one job drained: %d; want 201", code)
	}
}

func TestM5_AProducerRetryIsADuplicateEvenWhenTheQueueIsFull(t *testing.T) {
	t.Parallel()
	b := tStartBroker(t, t.TempDir(), "-max-depth", "2")
	_, r, _ := tEnqueue(t, b.URL, "default", `{"to":"member-1"}`, "alert-1", 0)
	tEnqueue(t, b.URL, "default", `{"to":"member-2"}`, "alert-2", 0)
	code, again, _ := tEnqueue(t, b.URL, "default", `{"to":"member-1"}`, "alert-1", 0)
	if code != http.StatusOK || again.ID != r.ID {
		t.Errorf("retry of an accepted job into a full queue: %d %+v; want 200 duplicate of %s: answering 429 "+
			"tells the producer its job was not taken, so it keeps retrying something that is already queued", code, again, r.ID)
	}
	if code, _, _ := tEnqueue(t, b.URL, "default", `{"to":"member-3"}`, "alert-3", 0); code != http.StatusTooManyRequests {
		t.Errorf("a new job into the full queue: %d; want 429", code)
	}
}

func TestM5_SIGTERMDrainsAWorkerWithoutLosingOrDuplicating(t *testing.T) {
	t.Parallel()
	b := tStartBroker(t, t.TempDir(), "-visibility", "2s", "-fsync=false")
	p := tStartProvider(t)
	tControl(t, p.URL, ProviderControl{LatencyMS: 150})
	w := tStartWorker(t, "draining-worker", b.URL, p.URL, "-concurrency", "4")
	ids := tMustEnqueue(t, b.URL, 30)
	tEventually(t, 10*time.Second, func() (bool, string) {
		s := tProviderStats(t, p.URL)
		return s.InFlight >= 2, fmt.Sprintf("vendor in flight %d; waiting for the worker to be mid-delivery", s.InFlight)
	})
	start := time.Now()
	if code := w.Stop(8 * time.Second); code != 0 {
		t.Fatalf("worker exit code after SIGTERM: %d; want 0 after a clean drain", code)
	}
	if el := time.Since(start); el > 3*time.Second {
		t.Errorf("drain took %v with 150ms calls; want it to finish the in-flight jobs and stop leasing", el)
	}
	if !strings.Contains(w.Stderr(), "drained") {
		t.Errorf("worker stderr does not report the drain:\n%s", w.Stderr())
	}
	tStartWorker(t, "replacement-worker", b.URL, p.URL)
	s := tWaitEffects(t, p.URL, 30, 20*time.Second)
	tExactlyOnce(t, s, ids)
	if s.Duplicates != 0 {
		t.Errorf("vendor saw %d duplicate sends; want 0: a drain must finish and ack what it already sent, "+
			"not cancel calls the vendor has accepted", s.Duplicates)
	}
}

func TestM5_ADrainThatCannotFinishGivesUpAtItsTimeout(t *testing.T) {
	t.Parallel()
	b := tStartBroker(t, t.TempDir())
	p := tStartProvider(t)
	tControl(t, p.URL, ProviderControl{LatencyMS: 6000})
	w := tStartWorker(t, "stuck-worker", b.URL, p.URL, "-call-timeout", "10s", "-drain-timeout", "500ms")
	tMustEnqueue(t, b.URL, 1)
	tEventually(t, 10*time.Second, func() (bool, string) {
		s := tProviderStats(t, p.URL)
		return s.InFlight == 1, "waiting for the worker's call to be in flight"
	})
	start := time.Now()
	code := w.Stop(5 * time.Second)
	if el := time.Since(start); el > 2500*time.Millisecond {
		t.Errorf("SIGTERM with a 500ms -drain-timeout took %v; an orchestrator kills it at its own grace period anyway, "+
			"so the drain must have a bound of its own", el)
	}
	if code == 0 {
		t.Errorf("exit code 0 after an incomplete drain; want non-zero so the operator knows a job was abandoned to its lease")
	}
}
