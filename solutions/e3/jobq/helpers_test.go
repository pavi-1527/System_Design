package jobq

// Helpers for the E3 tests (not part of the exercise). Everything here talks
// to the processes over HTTP, exactly as a producer, an operator or a
// dashboard would; nothing reaches into your code's memory.

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// tStartBroker starts a broker on dir with test-friendly defaults; extra flags
// override them (the flag package keeps the last value).
func tStartBroker(t *testing.T, dir string, extra ...string) *tProc {
	t.Helper()
	args := append([]string{"broker", "-addr", "127.0.0.1:0", "-data", dir, "-visibility", "2s", "-max-attempts", "5"}, extra...)
	return tStart(t, "broker", args...)
}

func tStartProvider(t *testing.T) *tProc {
	t.Helper()
	return tStart(t, "provider", "provider", "-addr", "127.0.0.1:0")
}

// tStartWorker starts a worker with short backoffs and long-polls so tests
// run in seconds; extra flags override them.
func tStartWorker(t *testing.T, name, brokerURL, providerURL string, extra ...string) *tProc {
	t.Helper()
	args := append([]string{"worker", "-addr", "127.0.0.1:0", "-broker", brokerURL, "-provider", providerURL,
		"-concurrency", "4", "-call-timeout", "1s", "-backoff-base", "20ms", "-backoff-max", "200ms",
		"-lease-wait", "300ms", "-drain-timeout", "5s"}, extra...)
	return tStart(t, name, args...)
}

// tRunToExit runs Run(args) in a child process that is expected to exit by
// itself (for example on bad flags) and returns its exit code and stderr.
func tRunToExit(t *testing.T, args ...string) (int, string) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(args)
	cmd := exec.Command(exe)
	cmd.Env = append(os.Environ(), tChildEnv+"="+string(raw))
	var stderr strings.Builder
	cmd.Stderr = &stderr
	stdin, _ := cmd.StdinPipe()
	defer stdin.Close()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { _ = cmd.Wait(); close(done) }()
	select {
	case <-done:
		return cmd.ProcessState.ExitCode(), stderr.String()
	case <-time.After(10 * time.Second):
		_ = cmd.Process.Kill()
		<-done
		t.Fatalf("%q did not exit within 10s; it should have refused to start", args)
		return 0, ""
	}
}

func tJSON(t *testing.T, method, url string, body any, out any, hdr ...string) (int, http.Header) {
	t.Helper()
	var s string
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		s = string(b)
	}
	code, resp, h, err := tDo(method, url, s, hdr...)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	if out != nil && strings.TrimSpace(resp) != "" && code < 300 {
		if err := json.Unmarshal([]byte(resp), out); err != nil {
			t.Fatalf("%s %s: decoding %q: %v", method, url, resp, err)
		}
	}
	return code, h
}

// tEnqueue enqueues payload (a JSON document) on queue q, with an
// Idempotency-Key when key is not empty.
func tEnqueue(t *testing.T, brokerURL, q, payload, key string, maxAttempts int) (int, EnqueueResponse, http.Header) {
	t.Helper()
	var hdr []string
	if key != "" {
		hdr = []string{"Idempotency-Key", key}
	}
	var r EnqueueResponse
	code, h := tJSON(t, "POST", brokerURL+"/queues/"+q+"/jobs",
		EnqueueRequest{Payload: json.RawMessage(payload), MaxAttempts: maxAttempts}, &r, hdr...)
	return code, r, h
}

// tMustEnqueue enqueues n alert jobs on "default" and fails unless each one
// is accepted with 201.
func tMustEnqueue(t *testing.T, brokerURL string, n int) []string {
	t.Helper()
	ids := make([]string, 0, n)
	for i := range n {
		code, r, _ := tEnqueue(t, brokerURL, "default", fmt.Sprintf(`{"to":"member-%d","class":"alert","n":%d}`, i, i), "", 0)
		if code != http.StatusCreated || r.ID == "" {
			t.Fatalf("enqueue %d: status %d id %q; want 201 and an id", i, code, r.ID)
		}
		ids = append(ids, r.ID)
	}
	return ids
}

func tLease(t *testing.T, brokerURL, q string, wait, vis time.Duration) (int, Job) {
	t.Helper()
	u := fmt.Sprintf("%s/queues/%s/lease?wait=%s", brokerURL, q, wait)
	if vis > 0 {
		u += "&visibility=" + vis.String()
	}
	var j Job
	code, _ := tJSON(t, "POST", u, nil, &j)
	return code, j
}

func tAck(t *testing.T, brokerURL string, j Job, lease string) int {
	t.Helper()
	code, _ := tJSON(t, "POST", brokerURL+"/jobs/"+j.ID+"/ack", AckRequest{Lease: lease}, nil)
	return code
}

func tNack(t *testing.T, brokerURL string, j Job, n NackRequest) int {
	t.Helper()
	code, _ := tJSON(t, "POST", brokerURL+"/jobs/"+j.ID+"/nack", n, nil)
	return code
}

func tQueueStats(t *testing.T, brokerURL, q string) QueueStats {
	t.Helper()
	var s QueueStats
	if code, _ := tJSON(t, "GET", brokerURL+"/queues/"+q+"/stats", nil, &s); code != http.StatusOK {
		t.Fatalf("GET /queues/%s/stats: status %d", q, code)
	}
	return s
}

func tDLQ(t *testing.T, brokerURL, q string) []DeadJob {
	t.Helper()
	var r struct {
		Jobs []DeadJob `json:"jobs"`
	}
	if code, _ := tJSON(t, "GET", brokerURL+"/queues/"+q+"/dlq", nil, &r); code != http.StatusOK {
		t.Fatalf("GET /queues/%s/dlq: status %d", q, code)
	}
	return r.Jobs
}

func tProviderStats(t *testing.T, providerURL string) ProviderStats {
	t.Helper()
	var s ProviderStats
	if code, _ := tJSON(t, "GET", providerURL+"/stats", nil, &s); code != http.StatusOK {
		t.Fatalf("provider /stats: status %d", code)
	}
	return s
}

func tControl(t *testing.T, providerURL string, c ProviderControl) {
	t.Helper()
	if code, _ := tJSON(t, "POST", providerURL+"/control", c, nil); code != http.StatusNoContent {
		t.Fatalf("provider /control: status %d", code)
	}
}

func tWorkerStats(t *testing.T, workerURL string) (WorkerStats, error) {
	var s WorkerStats
	code, body, _, err := tDo("GET", workerURL+"/stats", "")
	if err != nil {
		return s, err
	}
	if code != http.StatusOK {
		return s, fmt.Errorf("worker /stats: status %d", code)
	}
	return s, json.Unmarshal([]byte(body), &s)
}

// tWaitEffects waits until the provider has delivered n distinct jobs.
func tWaitEffects(t *testing.T, providerURL string, n int64, timeout time.Duration) ProviderStats {
	t.Helper()
	var s ProviderStats
	tEventually(t, timeout, func() (bool, string) {
		s = tProviderStats(t, providerURL)
		return s.Effects >= n, fmt.Sprintf("provider delivered %d of %d jobs (requests %d, failed %d, rejected %d)",
			s.Effects, n, s.Requests, s.Failed, s.Rejected)
	})
	return s
}

// tExactlyOnce fails unless every id in ids had exactly one effect at the
// provider.
func tExactlyOnce(t *testing.T, s ProviderStats, ids []string) {
	t.Helper()
	for _, id := range ids {
		if n := s.EffectsByKey[id]; n != 1 {
			t.Errorf("job %s had %d effects at the vendor; want exactly 1 (0 = a lost alert, 2 = a member texted twice)", id, n)
		}
	}
	if s.Effects != int64(len(ids)) {
		t.Errorf("vendor recorded %d effects for %d jobs", s.Effects, len(ids))
	}
}

func tLogPath(dir string) string { return filepath.Join(dir, "queue.log") }

// tFrameOffsets returns the start offset of every complete frame in a log
// written in the spec's format (4-byte length, 4-byte CRC, payload).
func tFrameOffsets(t *testing.T, b []byte) []int {
	t.Helper()
	var offs []int
	for off := 0; off+8 <= len(b); {
		n := int(binary.BigEndian.Uint32(b[off : off+4]))
		if n == 0 || off+8+n > len(b) {
			break
		}
		offs = append(offs, off)
		off += 8 + n
	}
	return offs
}
