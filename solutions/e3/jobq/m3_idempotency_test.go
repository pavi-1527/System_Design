package jobq

// Milestone 3: idempotency at both ends. Producer Idempotency-Keys at the
// broker (including concurrent duplicates and a restart), and a stable
// consumer key that turns at-least-once delivery into one effect at the
// vendor, even when a worker dies between the effect and the ack.

import (
	"fmt"
	"net/http"
	"sync"
	"testing"
	"time"
)

func TestM3_AProducerKeyDeduplicatesEnqueues(t *testing.T) {
	t.Parallel()
	b := tStartBroker(t, t.TempDir())
	code1, r1, _ := tEnqueue(t, b.URL, "default", `{"to":"member-42","class":"transfer-posted"}`, "transfer-42", 0)
	code2, r2, _ := tEnqueue(t, b.URL, "default", `{"to":"member-42","class":"transfer-posted"}`, "transfer-42", 0)
	if code1 != http.StatusCreated || code2 != http.StatusOK || !r2.Duplicate || r1.ID != r2.ID {
		t.Fatalf("enqueue twice with one key: %d %+v then %d %+v; want 201, then 200 duplicate with the same id: "+
			"the producer retried after a timeout, and the member must not get two alerts", code1, r1, code2, r2)
	}
	if code, r, _ := tEnqueue(t, b.URL, "other", `{"to":"member-42"}`, "transfer-42", 0); code != http.StatusCreated || r.ID == r1.ID {
		t.Errorf("same key on another queue: %d %+v; want a new job (keys are scoped to a queue)", code, r)
	}
	if s := tQueueStats(t, b.URL, "default"); s.Depth != 1 || s.Duplicates != 1 {
		t.Errorf("stats: %+v; want depth 1, duplicates_total 1", s)
	}
}

func TestM3_ConcurrentDuplicateEnqueuesCreateOneJob(t *testing.T) {
	t.Parallel()
	b := tStartBroker(t, t.TempDir())
	const n = 20
	var wg sync.WaitGroup
	codes := make([]int, n)
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			code, _, _, err := tDo("POST", b.URL+"/queues/default/jobs", `{"payload":{"to":"member-9"}}`, "Idempotency-Key", "otp-9")
			if err == nil {
				codes[i] = code
			}
		}()
	}
	wg.Wait()
	created := 0
	for _, c := range codes {
		switch c {
		case http.StatusCreated:
			created++
		case http.StatusOK:
		default:
			t.Errorf("concurrent enqueue returned %d; want 201 or 200", c)
		}
	}
	if created != 1 {
		t.Errorf("%d of %d concurrent enqueues with one key created a job; want exactly 1 (check-then-insert needs one critical section)", created, n)
	}
	if s := tQueueStats(t, b.URL, "default"); s.Depth != 1 {
		t.Errorf("depth %d; want 1", s.Depth)
	}
}

func TestM3_ProducerKeysSurviveARestart(t *testing.T) {
	t.Parallel()
	b := tStartBroker(t, t.TempDir())
	_, r1, _ := tEnqueue(t, b.URL, "default", `{"to":"member-3"}`, "stmt-3", 0)
	b.Kill()
	b.Restart()
	code, r2, _ := tEnqueue(t, b.URL, "default", `{"to":"member-3"}`, "stmt-3", 0)
	if code != http.StatusOK || r2.ID != r1.ID {
		t.Fatalf("same key after SIGKILL + restart: %d %+v; want 200 duplicate of %s: the dedupe window must be rebuilt from the log", code, r2, r1.ID)
	}
}

func TestM3_TheDedupeWindowExpires(t *testing.T) {
	t.Parallel()
	b := tStartBroker(t, t.TempDir(), "-dedupe-window", "400ms")
	_, r1, _ := tEnqueue(t, b.URL, "default", `{"to":"member-5"}`, "k-5", 0)
	time.Sleep(600 * time.Millisecond)
	code, r2, _ := tEnqueue(t, b.URL, "default", `{"to":"member-5"}`, "k-5", 0)
	if code != http.StatusCreated || r2.ID == r1.ID {
		t.Errorf("same key after the 400ms window: %d %+v; want a new job: the window is a promise about time, and memory is bounded by it", code, r2)
	}
}

func TestM3_AWorkerKilledBetweenEffectAndAckCausesOneEffect(t *testing.T) {
	t.Parallel()
	b := tStartBroker(t, t.TempDir(), "-visibility", "1s")
	p := tStartProvider(t)
	doomed := tStartWorker(t, "doomed-worker", b.URL, p.URL, "-hold-before-ack", "1m")
	ids := tMustEnqueue(t, b.URL, 1)
	tWaitEffects(t, p.URL, 1, 10*time.Second) // the vendor has sent the text...
	doomed.Kill()                             // ...and the worker dies before acking it
	tStartWorker(t, "second-worker", b.URL, p.URL)
	tEventually(t, 10*time.Second, func() (bool, string) {
		q := tQueueStats(t, b.URL, "default")
		return q.Acked == 1, fmt.Sprintf("broker stats %+v; want the job redelivered after its 1s lease and acked", q)
	})
	s := tProviderStats(t, p.URL)
	if n := len(s.AttemptsByKey[ids[0]]); n != 2 {
		t.Errorf("vendor saw %d attempts with key %s; want 2: the redelivery must reuse the job's key", n, ids[0])
	}
	if s.Duplicates != 1 {
		t.Errorf("vendor duplicates = %d; want 1 (the redelivery, recognised by its key)", s.Duplicates)
	}
	tExactlyOnce(t, s, ids)
}

func TestM3_AnAmbiguousTimeoutCausesOneEffect(t *testing.T) {
	t.Parallel()
	b := tStartBroker(t, t.TempDir())
	p := tStartProvider(t)
	// The vendor takes 700ms on the first call and completes it even though
	// the worker gave up after 200ms: the classic "did it go through?".
	tControl(t, p.URL, ProviderControl{SlowNext: 1, SlowMS: 700})
	tStartWorker(t, "worker", b.URL, p.URL, "-call-timeout", "200ms")
	ids := tMustEnqueue(t, b.URL, 1)
	tEventually(t, 10*time.Second, func() (bool, string) {
		s := tProviderStats(t, p.URL)
		return s.Effects+s.Duplicates >= 2 && s.InFlight == 0,
			fmt.Sprintf("vendor stats: effects %d, duplicates %d, in flight %d", s.Effects, s.Duplicates, s.InFlight)
	})
	s := tProviderStats(t, p.URL)
	tExactlyOnce(t, s, ids)
	if s.Duplicates < 1 {
		t.Errorf("vendor duplicates = %d; want ≥ 1: both the timed-out call and the retry reached it", s.Duplicates)
	}
}
