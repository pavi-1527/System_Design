package shorty

// E1-specific test helpers (the shared harness is in harness_test.go).

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"hash/crc32"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// tStore starts a store process on a fresh port with its log in dir.
func tStore(t *testing.T, dir string, extra ...string) *tProc {
	t.Helper()
	args := append([]string{"store", "-addr", "127.0.0.1:0", "-data", dir}, extra...)
	return tStart(t, "store", args...)
}

// tAPI starts an api process pointed at storeURL.
func tAPI(t *testing.T, name, storeURL string, extra ...string) *tProc {
	t.Helper()
	args := append([]string{"api", "-addr", "127.0.0.1:0", "-store", storeURL}, extra...)
	return tStart(t, name, args...)
}

// tExpectExit runs Run(args) in a child that is expected to exit by itself
// (not become READY) and returns its exit code and stderr.
func tExpectExit(t *testing.T, args ...string) (int, string) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(args)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, exe)
	cmd.Env = append(os.Environ(), tChildEnv+"="+string(raw))
	var stdout, stderr strings.Builder
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	stdin, err := cmd.StdinPipe() // keep stdin open: EOF means "parent died"
	if err != nil {
		t.Fatal(err)
	}
	defer stdin.Close()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	_ = cmd.Wait()
	if ctx.Err() != nil {
		t.Fatalf("Run(%q) was still running after 15s; it should have exited with an error\nstdout: %s", args, stdout.String())
	}
	if strings.Contains(stdout.String(), "READY") {
		t.Errorf("Run(%q) printed READY before exiting; it must refuse before listening", args)
	}
	return cmd.ProcessState.ExitCode(), stderr.String()
}

// tStats reads GET <base>/-/stats as numbers.
func tStats(t *testing.T, base string) map[string]int64 {
	t.Helper()
	code, body, _, err := tDo("GET", base+"/-/stats", "")
	if err != nil || code != 200 {
		t.Fatalf("GET %s/-/stats: %d %v %s", base, code, err, body)
	}
	var m map[string]int64
	if err := json.Unmarshal([]byte(body), &m); err != nil {
		t.Fatalf("stats are not a JSON object of integers: %v: %s", err, body)
	}
	return m
}

type tCreated struct {
	Code      string     `json:"code"`
	ShortURL  string     `json:"short_url"`
	URL       string     `json:"url"`
	ExpiresAt *time.Time `json:"expires_at"`
}

// tCreate posts a link as client and returns the status, decoded body and headers.
func tCreate(t *testing.T, apiURL, client, long string, ttl int, hdr ...string) (int, tCreated, http.Header) {
	t.Helper()
	body := `{"url":` + strconv.Quote(long) + `,"ttl_seconds":` + strconv.Itoa(ttl) + `}`
	code, resp, h, err := tDo("POST", apiURL+"/v1/links", body, append([]string{"X-Client-ID", client}, hdr...)...)
	if err != nil {
		t.Fatalf("POST /v1/links: %v", err)
	}
	var c tCreated
	if code == http.StatusCreated {
		if err := json.Unmarshal([]byte(resp), &c); err != nil {
			t.Fatalf("201 body is not the documented JSON: %v: %s", err, resp)
		}
	}
	return code, c, h
}

// tMustCreate creates a link and fails unless the answer is 201 with a code.
func tMustCreate(t *testing.T, apiURL, client, long string) string {
	t.Helper()
	code, c, _ := tCreate(t, apiURL, client, long, 0)
	if code != http.StatusCreated || c.Code == "" {
		t.Fatalf("create %s: got %d %+v, want 201 with a code", long, code, c)
	}
	return c.Code
}

// tRecord encodes one log record exactly as spec.md "On-disk format" says.
func tRecord(op byte, key string, val []byte) []byte {
	body := []byte{op}
	body = binary.BigEndian.AppendUint16(body, uint16(len(key)))
	body = append(body, key...)
	body = append(body, val...)
	rec := make([]byte, 8, 8+len(body))
	binary.BigEndian.PutUint32(rec[4:], uint32(len(body)))
	rec = append(rec, body...)
	binary.BigEndian.PutUint32(rec[0:], crc32.Checksum(rec[4:], crc32.MakeTable(crc32.Castagnoli)))
	return rec
}

func tAppendFile(t *testing.T, path string, b []byte) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write(b); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

func tFileSize(t *testing.T, path string) int64 {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return fi.Size()
}

func tLogPath(dir string) string { return filepath.Join(dir, "store.log") }

// tClock is a settable fake clock.
type tClock struct {
	mu  sync.Mutex
	now time.Time
}

func tNewClock() *tClock { return &tClock{now: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)} }

func (c *tClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *tClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// tParallel runs f(i) for i in [0, n) on n goroutines and waits, failing the
// test if they take longer than timeout.
func tParallel(t *testing.T, n int, timeout time.Duration, f func(i int)) {
	t.Helper()
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			f(i)
		}()
	}
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(timeout):
		t.Fatalf("%d concurrent callers did not finish within %v (deadlock, or a call with no timeout?)", n, timeout)
	}
}
