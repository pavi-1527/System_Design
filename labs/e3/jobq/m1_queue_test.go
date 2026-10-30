package jobq

// Milestone 1: a durable queue and a pool of workers. Broker API, leases and
// visibility timeouts, recovery from the log (including after SIGKILL and a
// torn tail), and the worker pull loop with bounded concurrency.

import (
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestM1_EveryRoleRefusesANonLoopbackAddress(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	for _, args := range [][]string{
		{"broker", "-addr", "0.0.0.0:0", "-data", dir},
		{"worker", "-addr", "0.0.0.0:0", "-broker", "http://127.0.0.1:1", "-provider", "http://127.0.0.1:1"},
		{"provider", "-addr", "0.0.0.0:0"},
	} {
		code, stderr := tRunToExit(t, args...)
		if code != 2 {
			t.Errorf("%s with -addr 0.0.0.0:0 exited %d; want 2: these processes have no authentication, so "+
				"binding every interface publishes 'send a message to any member' to the network\nstderr: %s", args[0], code, stderr)
		}
	}
}

func TestM1_EnqueueLeaseAck(t *testing.T) {
	t.Parallel()
	b := tStartBroker(t, t.TempDir())
	code, r, _ := tEnqueue(t, b.URL, "default", `{"to":"member-7","class":"alert"}`, "", 0)
	if code != http.StatusCreated || r.ID == "" || r.Duplicate {
		t.Fatalf("enqueue: status %d, %+v; want 201 with a new id", code, r)
	}
	if code, _, _, _ := tDo("POST", b.URL+"/queues/default/jobs", `{"payload": not json}`); code != http.StatusBadRequest {
		t.Errorf("enqueue with an invalid payload: status %d; want 400", code)
	}
	code, j := tLease(t, b.URL, "default", 0, 0)
	if code != http.StatusOK || j.ID != r.ID || j.Attempt != 1 || j.Lease == "" {
		t.Fatalf("lease: status %d, %+v; want 200 with job %s on attempt 1 and a lease token", code, j, r.ID)
	}
	if !strings.Contains(string(j.Payload), "member-7") {
		t.Errorf("leased payload %s; want the enqueued payload back", j.Payload)
	}
	if time.Until(j.LeaseExpiresAt) < time.Second || time.Until(j.LeaseExpiresAt) > 3*time.Second {
		t.Errorf("lease expires in %v; want ≈ the broker's 2s -visibility", time.Until(j.LeaseExpiresAt))
	}
	if s := tQueueStats(t, b.URL, "default"); s.Leased != 1 || s.Ready != 0 || s.Depth != 1 {
		t.Errorf("stats while leased: %+v; want leased 1, ready 0, depth 1", s)
	}
	if code, _ := tLease(t, b.URL, "default", 0, 0); code != http.StatusNoContent {
		t.Errorf("second lease: status %d; want 204: a leased job is invisible to other workers", code)
	}
	if code := tAck(t, b.URL, j, j.Lease); code != http.StatusNoContent {
		t.Fatalf("ack: status %d; want 204", code)
	}
	if s := tQueueStats(t, b.URL, "default"); s.Depth != 0 || s.Acked != 1 || s.Enqueued != 1 {
		t.Errorf("stats after ack: %+v; want depth 0, acked 1, enqueued 1", s)
	}
	if code := tAck(t, b.URL, j, j.Lease); code != http.StatusNotFound {
		t.Errorf("second ack of the same job: status %d; want 404 (it is gone)", code)
	}
}

func TestM1_JobsAreLeasedInEnqueueOrder(t *testing.T) {
	t.Parallel()
	b := tStartBroker(t, t.TempDir())
	ids := tMustEnqueue(t, b.URL, 5)
	for i, id := range ids {
		code, j := tLease(t, b.URL, "default", 0, 0)
		if code != http.StatusOK || j.ID != id {
			t.Fatalf("lease %d: status %d id %s; want %s: FIFO among ready jobs", i, code, j.ID, id)
		}
	}
	if code, _ := tLease(t, b.URL, "other", 0, 0); code != http.StatusNoContent {
		t.Errorf("lease on an empty queue 'other': status %d; want 204 (queues are separate)", code)
	}
}

func TestM1_LongPollWakesUpWhenAJobArrives(t *testing.T) {
	t.Parallel()
	b := tStartBroker(t, t.TempDir())
	type result struct {
		code int
		at   time.Time
	}
	got := make(chan result, 1)
	start := time.Now()
	go func() {
		code, _, _, _ := tDo("POST", b.URL+"/queues/default/lease?wait=4s", "")
		got <- result{code, time.Now()}
	}()
	time.Sleep(300 * time.Millisecond)
	enqAt := time.Now()
	tMustEnqueue(t, b.URL, 1)
	select {
	case r := <-got:
		if r.code != http.StatusOK {
			t.Fatalf("long-poll lease: status %d; want 200 once the job arrives", r.code)
		}
		if r.at.Before(enqAt) {
			t.Fatal("long-poll returned before anything was enqueued")
		}
		if el := r.at.Sub(start); el > 3*time.Second {
			t.Errorf("long-poll took %v; want it to return soon after the enqueue at ≈ 300ms, not at the 4s wait", el)
		}
	case <-time.After(6 * time.Second):
		t.Fatal("long-poll lease never returned")
	}
}

func TestM1_AnExpiredLeaseIsRedelivered(t *testing.T) {
	t.Parallel()
	b := tStartBroker(t, t.TempDir(), "-visibility", "400ms")
	tMustEnqueue(t, b.URL, 1)
	_, first := tLease(t, b.URL, "default", 0, 0)
	if first.ID == "" {
		t.Fatal("lease returned no job")
	}
	if code, _ := tLease(t, b.URL, "default", 0, 0); code != http.StatusNoContent {
		t.Fatalf("lease during the visibility timeout: %d; want 204", code)
	}
	code, second := tLease(t, b.URL, "default", 3*time.Second, 0)
	if code != http.StatusOK || second.ID != first.ID || second.Attempt != 2 {
		t.Fatalf("after the lease expired: status %d, %+v; want job %s again on attempt 2: "+
			"a worker that dies holding a job must not lose it", code, second, first.ID)
	}
	if second.Lease == first.Lease {
		t.Error("the redelivery reused the first lease token; each lease needs its own")
	}
	if code := tAck(t, b.URL, first, first.Lease); code != http.StatusConflict {
		t.Errorf("ack with the expired first lease: status %d; want 409: the job belongs to the second worker now", code)
	}
	if code := tAck(t, b.URL, second, second.Lease); code != http.StatusNoContent {
		t.Errorf("ack with the current lease: status %d; want 204", code)
	}
	if s := tQueueStats(t, b.URL, "default"); s.Redelivered != 1 || s.Acked != 1 {
		t.Errorf("stats: %+v; want redelivered_total 1, acked_total 1", s)
	}
}

func TestM1_WorkersDeliverEveryJobExactlyOnce(t *testing.T) {
	t.Parallel()
	b := tStartBroker(t, t.TempDir(), "-fsync=false")
	p := tStartProvider(t)
	tStartWorker(t, "worker-1", b.URL, p.URL)
	tStartWorker(t, "worker-2", b.URL, p.URL)
	ids := tMustEnqueue(t, b.URL, 60)
	s := tWaitEffects(t, p.URL, 60, 20*time.Second)
	tExactlyOnce(t, s, ids)
	tEventually(t, 5*time.Second, func() (bool, string) {
		q := tQueueStats(t, b.URL, "default")
		return q.Depth == 0 && q.Acked == 60, fmt.Sprintf("broker stats %+v; want depth 0, acked 60", q)
	})
}

func TestM1_WorkerConcurrencyIsBounded(t *testing.T) {
	t.Parallel()
	b := tStartBroker(t, t.TempDir(), "-fsync=false")
	p := tStartProvider(t)
	tControl(t, p.URL, ProviderControl{LatencyMS: 80})
	tStartWorker(t, "worker", b.URL, p.URL, "-concurrency", "3")
	tMustEnqueue(t, b.URL, 24)
	s := tWaitEffects(t, p.URL, 24, 20*time.Second)
	if s.MaxInFlight > 3 {
		t.Errorf("vendor saw %d concurrent calls from one worker with -concurrency 3; the pool must bound the load it puts on a dependency", s.MaxInFlight)
	}
	if s.MaxInFlight < 2 {
		t.Errorf("vendor saw at most %d concurrent call(s); with -concurrency 3 and 80ms calls the worker should run jobs in parallel", s.MaxInFlight)
	}
}

func TestM1_BrokerCrashLosesNoAcknowledgedEnqueue(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	b := tStartBroker(t, dir)
	ids := tMustEnqueue(t, b.URL, 40)
	b.Kill() // SIGKILL: no shutdown hook runs
	b.Restart()
	if s := tQueueStats(t, b.URL, "default"); s.Depth != 40 || s.Ready != 40 {
		t.Fatalf("after SIGKILL + restart: %+v; want all 40 acknowledged jobs ready: a 201 is a promise", s)
	}
	p := tStartProvider(t)
	tStartWorker(t, "worker", b.URL, p.URL)
	tExactlyOnce(t, tWaitEffects(t, p.URL, 40, 20*time.Second), ids)
}

func TestM1_BrokerCrashMidDeliveryLosesNothing(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	b := tStartBroker(t, dir, "-visibility", "1s", "-fsync=false")
	p := tStartProvider(t)
	tControl(t, p.URL, ProviderControl{LatencyMS: 20})
	w := tStartWorker(t, "worker", b.URL, p.URL)
	ids := tMustEnqueue(t, b.URL, 40)
	tWaitEffects(t, p.URL, 8, 20*time.Second)
	b.Kill()
	time.Sleep(300 * time.Millisecond) // the worker's acks and leases now fail
	b.Restart()
	s := tWaitEffects(t, p.URL, 40, 30*time.Second)
	tExactlyOnce(t, s, ids)
	if !w.Running() {
		t.Fatal("the worker exited while the broker was down; it must back off and keep polling")
	}
	tEventually(t, 10*time.Second, func() (bool, string) {
		q := tQueueStats(t, b.URL, "default")
		return q.Depth == 0, fmt.Sprintf("broker depth %d after every job was delivered; unacked leases should expire and be acked", q.Depth)
	})
}

func TestM1_AckedJobsStayAckedAfterARestart(t *testing.T) {
	t.Parallel()
	b := tStartBroker(t, t.TempDir())
	tMustEnqueue(t, b.URL, 3)
	for range 3 {
		_, j := tLease(t, b.URL, "default", 0, 0)
		if code := tAck(t, b.URL, j, j.Lease); code != http.StatusNoContent {
			t.Fatalf("ack: %d", code)
		}
	}
	b.Kill()
	b.Restart()
	if s := tQueueStats(t, b.URL, "default"); s.Depth != 0 {
		t.Fatalf("after restart: depth %d; want 0: acked jobs came back (every ack must be in the log)", s.Depth)
	}
	if code, _ := tLease(t, b.URL, "default", 0, 0); code != http.StatusNoContent {
		t.Errorf("lease after restart: %d; want 204", code)
	}
}

func TestM1_ALeaseSurvivesABrokerRestart(t *testing.T) {
	t.Parallel()
	b := tStartBroker(t, t.TempDir(), "-visibility", "10s")
	tMustEnqueue(t, b.URL, 1)
	_, j := tLease(t, b.URL, "default", 0, 0)
	b.Kill()
	b.Restart()
	if code, _ := tLease(t, b.URL, "default", 0, 0); code != http.StatusNoContent {
		t.Errorf("lease after restart: %d; want 204: the job's lease (an absolute expiry in the log) is still running", code)
	}
	if code := tAck(t, b.URL, j, j.Lease); code != http.StatusNoContent {
		t.Errorf("ack after restart with the pre-crash lease: %d; want 204: the worker that holds it did nothing wrong", code)
	}
}

func TestM1_ATornLogTailIsTruncatedOnRecovery(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	b := tStartBroker(t, dir)
	tMustEnqueue(t, b.URL, 5)
	b.Kill()
	f, err := os.OpenFile(tLogPath(dir), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatalf("the broker must keep its log at <data>/queue.log: %v", err)
	}
	// A header that promises 300 bytes, followed by 11: a write cut short by a power loss.
	_, _ = f.Write([]byte{0, 0, 1, 44, 0xde, 0xad, 0xbe, 0xef, '{', '"', 'o', 'p', '"', ':', '"', 'e', 'n', 'q', '"'})
	_ = f.Close()
	b.Restart()
	if s := tQueueStats(t, b.URL, "default"); s.Depth != 5 {
		t.Fatalf("after a torn tail: depth %d; want the 5 complete jobs", s.Depth)
	}
	tMustEnqueue(t, b.URL, 1)
	b.Kill()
	b.Restart()
	if s := tQueueStats(t, b.URL, "default"); s.Depth != 6 {
		t.Fatalf("after a second restart: depth %d; want 6: if recovery does not truncate the torn bytes, everything "+
			"appended after them is unreadable and silently lost on the next restart", s.Depth)
	}
}

func TestM1_ACorruptFrameEndsRecoveryThere(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	b := tStartBroker(t, dir)
	tMustEnqueue(t, b.URL, 4)
	b.Kill()
	raw, err := os.ReadFile(tLogPath(dir))
	if err != nil {
		t.Fatal(err)
	}
	offs := tFrameOffsets(t, raw)
	if len(offs) != 4 {
		t.Fatalf("log holds %d frames after 4 enqueues; want 4 in the spec's frame format", len(offs))
	}
	raw[offs[3]+8+3] ^= 0x20 // flip a bit inside the last record's payload
	if err := os.WriteFile(tLogPath(dir), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	b.Restart()
	if s := tQueueStats(t, b.URL, "default"); s.Depth != 3 {
		t.Fatalf("after corrupting the last frame: depth %d; want 3 (the CRC must catch it)", s.Depth)
	}
	if fi, _ := os.Stat(tLogPath(dir)); fi.Size() != int64(offs[3]) {
		t.Errorf("log is %d bytes after recovery; want it truncated to %d, the end of the last good frame", fi.Size(), offs[3])
	}
}

func TestM1_BrokerShutsDownGracefullyDuringALongPoll(t *testing.T) {
	t.Parallel()
	b := tStartBroker(t, t.TempDir())
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, _, _, _ = tDo("POST", b.URL+"/queues/default/lease?wait=20s", "")
	}()
	time.Sleep(200 * time.Millisecond)
	start := time.Now()
	if code := b.Stop(8 * time.Second); code != 0 {
		t.Errorf("broker exit code after SIGTERM: %d; want 0", code)
	}
	if el := time.Since(start); el > 4*time.Second {
		t.Errorf("SIGTERM took %v with a 20s long-poll open; shutdown must end long-polls, not wait them out", el)
	}
	wg.Wait()
}
