package shorty

// Milestone 1, multi-process: the store role (spec.md "Store API",
// Unit 1 §3).

import (
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestM1_ProcessesRefuseNonLoopbackAddresses(t *testing.T) {
	dir := t.TempDir()
	cases := [][]string{
		{"store", "-addr", "0.0.0.0:0", "-data", dir},
		{"api", "-addr", "0.0.0.0:0", "-store", "http://127.0.0.1:1"},
		{"api", "-addr", "127.0.0.1:0", "-store", "http://192.0.2.10:7001"},
	}
	for _, args := range cases {
		code, stderr := tExpectExit(t, args...)
		if code != 2 {
			t.Errorf("Run(%q) exit code %d, want 2 (configuration error)", args, code)
		}
		if !strings.Contains(stderr, "loopback") {
			t.Errorf("Run(%q) stderr should say why (mention loopback): %q", args, stderr)
		}
	}
}

func TestM1_StoreHTTPAPI(t *testing.T) {
	s := tStore(t, t.TempDir())
	kv := s.URL + "/kv/"
	expect := func(method, key, body string, wantCode int, wantBody string) {
		t.Helper()
		code, got, _, err := tDo(method, kv+key, body)
		if err != nil {
			t.Fatalf("%s %s: %v", method, key, err)
		}
		if code != wantCode || (wantBody != "" && got != wantBody) {
			t.Fatalf("%s /kv/%s = %d %q, want %d %q", method, key, code, got, wantCode, wantBody)
		}
	}
	expect("PUT", "link/a", "one", 204, "")
	expect("GET", "link/a", "", 200, "one")
	expect("PUT", "link/a", "two", 204, "")
	expect("GET", "link/a", "", 200, "two")
	expect("GET", "link/missing", "", 404, "")
	expect("PUT", "link/a?if-absent=1", "three", 409, "two")
	expect("PUT", "link/b?if-absent=1", "bee", 201, "")
	expect("GET", "link/b", "", 200, "bee")
	expect("DELETE", "link/a", "", 204, "")
	expect("GET", "link/a", "", 404, "")
	expect("PUT", "link/big", strings.Repeat("x", MaxValueLen+1), 400, "")

	st := tStats(t, s.URL)
	if st["keys"] != 1 {
		t.Errorf("stats keys = %d, want 1 (only link/b is live)", st["keys"])
	}
	if st["reads"] != 5 {
		t.Errorf("stats reads = %d, want 5: every GET /kv counts, found or not", st["reads"])
	}
	if st["conflicts"] != 1 {
		t.Errorf("stats conflicts = %d, want 1", st["conflicts"])
	}
	if code, _, _, _ := tDo("GET", s.URL+"/-/healthz", ""); code != 200 {
		t.Errorf("healthz = %d", code)
	}
}

func TestM1_StoreGracefulStopExitsZero(t *testing.T) {
	s := tStore(t, t.TempDir())
	if code, _, _, err := tDo("PUT", s.URL+"/kv/k", "v"); err != nil || code != 204 {
		t.Fatalf("PUT: %d %v", code, err)
	}
	if code := s.Stop(10 * time.Second); code != 0 {
		t.Fatalf("store exited %d after SIGTERM, want 0 (a clean shutdown)\n%s", code, s.Stderr())
	}
}

func TestM1_StoreAcknowledgedWritesSurviveSIGKILL(t *testing.T) {
	dir := t.TempDir()
	s := tStore(t, dir, "-sync", "always")
	var (
		mu    sync.Mutex
		acked []string
		n     atomic.Int64
		stop  atomic.Bool
	)
	var wg sync.WaitGroup
	for w := 0; w < 8; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; !stop.Load() && i < 10000; i++ {
				key := fmt.Sprintf("w%d-%d", w, i)
				code, _, _, err := tDo("PUT", s.URL+"/kv/"+key, "value-"+key)
				if err != nil {
					return // the store is gone
				}
				if code == 204 {
					mu.Lock()
					acked = append(acked, key)
					mu.Unlock()
					n.Add(1)
				}
			}
		}()
	}
	tEventually(t, 20*time.Second, func() (bool, string) {
		return n.Load() >= 200, fmt.Sprintf("only %d writes acknowledged", n.Load())
	})
	s.Kill() // mid-traffic: no shutdown hook, no final flush
	stop.Store(true)
	wg.Wait()

	s.Restart()
	mu.Lock()
	keys := append([]string(nil), acked...)
	mu.Unlock()
	for _, k := range keys {
		code, body, _, err := tDo("GET", s.URL+"/kv/"+k, "")
		if err != nil || code != 200 || body != "value-"+k {
			t.Fatalf("acknowledged write %s is gone after SIGKILL + restart (%d %q %v): "+
				"a 204 must mean the record is in the log, not just in memory", k, code, body, err)
		}
	}
	st := tStats(t, s.URL)
	if st["recovered_records"] < int64(len(keys)) {
		t.Errorf("recovered_records = %d, want ≥ %d", st["recovered_records"], len(keys))
	}
}

func TestM1_StoreRestartsAfterATornTail(t *testing.T) {
	dir := t.TempDir()
	s := tStore(t, dir)
	for i := 0; i < 10; i++ {
		if code, _, _, err := tDo("PUT", fmt.Sprintf("%s/kv/k%d", s.URL, i), "v"); err != nil || code != 204 {
			t.Fatalf("PUT: %d %v", code, err)
		}
	}
	s.Kill()
	junk := tRecord(1, "half-written", []byte("this record never finished"))[:19]
	tAppendFile(t, tLogPath(dir), junk)

	s.Restart()
	st := tStats(t, s.URL)
	if st["keys"] != 10 || st["recovered_records"] != 10 {
		t.Errorf("after restart: keys %d, recovered %d; want 10 and 10", st["keys"], st["recovered_records"])
	}
	if st["truncated_bytes"] != int64(len(junk)) {
		t.Errorf("truncated_bytes = %d, want %d", st["truncated_bytes"], len(junk))
	}
	if code, _, _, err := tDo("PUT", s.URL+"/kv/new", "v"); err != nil || code != 204 {
		t.Fatalf("PUT after recovery: %d %v", code, err)
	}
	s.Restart()
	if st := tStats(t, s.URL); st["keys"] != 11 || st["truncated_bytes"] != 0 {
		t.Errorf("second restart: keys %d truncated %d; want 11 and 0", st["keys"], st["truncated_bytes"])
	}
}

func TestM1_StoreRefusesToStartOnMidFileCorruption(t *testing.T) {
	dir := t.TempDir()
	s := tStore(t, dir)
	for i := 0; i < 10; i++ {
		if code, _, _, err := tDo("PUT", fmt.Sprintf("%s/kv/k%d", s.URL, i), "some value"); err != nil || code != 204 {
			t.Fatalf("PUT: %d %v", code, err)
		}
	}
	if code := s.Stop(10 * time.Second); code != 0 {
		t.Fatalf("stop: exit %d", code)
	}
	b, err := os.ReadFile(tLogPath(dir))
	if err != nil {
		t.Fatal(err)
	}
	rec := len(tRecord(1, "k0", []byte("some value")))
	b[3*rec+rec-3] ^= 0x40 // inside the value of the fourth of ten records
	if err := os.WriteFile(tLogPath(dir), b, 0o644); err != nil {
		t.Fatal(err)
	}
	code, stderr := tExpectExit(t, "store", "-addr", "127.0.0.1:0", "-data", dir)
	if code == 0 {
		t.Fatal("store exited 0 on a corrupt log; it must refuse to serve")
	}
	if !strings.Contains(strings.ToLower(stderr), "corrupt") {
		t.Errorf("stderr should say the log is corrupt: %q", stderr)
	}
}
