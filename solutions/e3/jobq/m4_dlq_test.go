package jobq

// Milestone 4: dead letters and replay. Permanent errors go straight to the
// DLQ, exhausted retries go there with their history, a job that keeps
// killing workers runs out of attempts too, a poison job never blocks the
// rest, and replay delivers after the fix.

import (
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestM4_APermanentErrorDeadLettersAfterOneAttempt(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	b := tStartBroker(t, dir)
	p := tStartProvider(t)
	tControl(t, p.URL, ProviderControl{RejectClass: "bad-template", RejectStatus: http.StatusBadRequest})
	tStartWorker(t, "worker", b.URL, p.URL)
	_, r, _ := tEnqueue(t, b.URL, "default", `{"to":"member-1","class":"bad-template"}`, "", 0)
	var dead []DeadJob
	tEventually(t, 10*time.Second, func() (bool, string) {
		dead = tDLQ(t, b.URL, "default")
		return len(dead) == 1, fmt.Sprintf("DLQ holds %d jobs; want the rejected one", len(dead))
	})
	d := dead[0]
	if d.ID != r.ID || d.Attempts != 1 || !strings.Contains(d.LastError, "400") {
		t.Errorf("dead job %+v; want id %s, 1 attempt, last_error mentioning 400: retrying a 400 four more times only "+
			"delays the page to the template owner", d, r.ID)
	}
	if n := len(tProviderStats(t, p.URL).AttemptsByKey[r.ID]); n != 1 {
		t.Errorf("vendor saw %d attempts; want 1", n)
	}
	b.Kill()
	b.Restart()
	if dead := tDLQ(t, b.URL, "default"); len(dead) != 1 || dead[0].ID != r.ID {
		t.Errorf("DLQ after restart: %+v; want the dead job still there (dead-lettering is a logged state change)", dead)
	}
	if s := tQueueStats(t, b.URL, "default"); s.Depth != 0 || s.Dead != 1 {
		t.Errorf("stats: %+v; want depth 0 (dead jobs don't count), dead 1", s)
	}
}

func TestM4_ExhaustedRetriesDeadLetterWithTheirHistoryAndReplayDelivers(t *testing.T) {
	t.Parallel()
	b := tStartBroker(t, t.TempDir())
	p := tStartProvider(t)
	tControl(t, p.URL, ProviderControl{RejectClass: "flaky", RejectStatus: http.StatusServiceUnavailable})
	tStartWorker(t, "worker", b.URL, p.URL)
	_, r, _ := tEnqueue(t, b.URL, "default", `{"to":"member-2","class":"flaky"}`, "", 3)
	var dead []DeadJob
	tEventually(t, 10*time.Second, func() (bool, string) {
		dead = tDLQ(t, b.URL, "default")
		return len(dead) == 1, fmt.Sprintf("DLQ holds %d jobs; want 1 after 3 failed attempts", len(dead))
	})
	d := dead[0]
	if d.Attempts != 3 || len(d.History) != 3 {
		t.Fatalf("dead job: %d attempts, %d history entries; want 3 and 3 (max_attempts 3)", d.Attempts, len(d.History))
	}
	for i, a := range d.History {
		if a.N != i+1 || a.Error == "" || a.At.IsZero() {
			t.Errorf("history[%d] = %+v; want attempt %d with its time and error: whoever replays this needs to know what happened", i, a, i+1)
		}
	}
	if n := len(tProviderStats(t, p.URL).AttemptsByKey[r.ID]); n != 3 {
		t.Errorf("vendor saw %d attempts; want 3", n)
	}

	tControl(t, p.URL, ProviderControl{}) // the vendor is fixed
	var rep struct {
		Replayed int `json:"replayed"`
	}
	if code, _ := tJSON(t, "POST", b.URL+"/queues/default/dlq/replay", map[string]any{"ids": []string{r.ID}}, &rep); code != http.StatusOK || rep.Replayed != 1 {
		t.Fatalf("replay: status %d, replayed %d; want 200 and 1", code, rep.Replayed)
	}
	tExactlyOnce(t, tWaitEffects(t, p.URL, 1, 10*time.Second), []string{r.ID})
	tEventually(t, 5*time.Second, func() (bool, string) {
		n := len(tDLQ(t, b.URL, "default"))
		q := tQueueStats(t, b.URL, "default")
		return n == 0 && q.Depth == 0, fmt.Sprintf("DLQ %d, depth %d after replay; want 0 and 0", n, q.Depth)
	})
}

func TestM4_AJobThatKeepsKillingWorkersRunsOutOfAttempts(t *testing.T) {
	t.Parallel()
	b := tStartBroker(t, t.TempDir(), "-visibility", "300ms")
	_, r, _ := tEnqueue(t, b.URL, "default", `{"to":"member-6","class":"crashes-the-renderer"}`, "", 2)
	for attempt := 1; attempt <= 2; attempt++ {
		code, j := tLease(t, b.URL, "default", 3*time.Second, 0)
		if code != http.StatusOK || j.Attempt != attempt {
			t.Fatalf("lease %d: status %d attempt %d", attempt, code, j.Attempt)
		}
		// ...and the "worker" dies: no ack, no nack.
	}
	if code, _ := tLease(t, b.URL, "default", time.Second, 0); code != http.StatusNoContent {
		t.Fatalf("third lease: %d; want 204: a job whose leases keep expiring must not be redelivered forever", code)
	}
	dead := tDLQ(t, b.URL, "default")
	if len(dead) != 1 || dead[0].ID != r.ID || !strings.Contains(dead[0].LastError, "lease") {
		t.Fatalf("DLQ: %+v; want job %s with a last_error that says its lease expired", dead, r.ID)
	}
}

func TestM4_APoisonJobDoesNotBlockTheQueue(t *testing.T) {
	t.Parallel()
	b := tStartBroker(t, t.TempDir(), "-fsync=false")
	p := tStartProvider(t)
	tControl(t, p.URL, ProviderControl{RejectClass: "poison", RejectStatus: http.StatusServiceUnavailable})
	_, poison, _ := tEnqueue(t, b.URL, "default", `{"to":"member-0","class":"poison"}`, "", 4)
	ids := tMustEnqueue(t, b.URL, 20)
	// One worker, one job at a time, slow backoff: if the poison job held the
	// head of the line, the others would wait out ≈ 0.3 + 0.6 + 1.2 s of its retries.
	tStartWorker(t, "worker", b.URL, p.URL, "-concurrency", "1", "-jitter", "none", "-backoff-base", "300ms", "-backoff-max", "2s")
	s := tWaitEffects(t, p.URL, 20, 15*time.Second)
	tEventually(t, 15*time.Second, func() (bool, string) {
		n := len(tDLQ(t, b.URL, "default"))
		return n == 1, fmt.Sprintf("DLQ holds %d; want the poison job after its 4 attempts", n)
	})
	s = tProviderStats(t, p.URL)
	pa := s.AttemptsByKey[poison.ID]
	if len(pa) != 4 {
		t.Fatalf("poison job attempts at the vendor: %d; want 4", len(pa))
	}
	var lastGood int64
	for _, id := range ids {
		if at := s.AttemptsByKey[id]; len(at) > 0 {
			lastGood = max(lastGood, slices.Max(at))
		}
	}
	if lastGood > pa[len(pa)-1] {
		t.Errorf("the last healthy job reached the vendor %dms after the poison job's final attempt; "+
			"want all 20 delivered while the poison job waits out its backoff (a nacked job steps out of line)",
			lastGood-pa[len(pa)-1])
	}
	tExactlyOnce(t, s, ids)
}
