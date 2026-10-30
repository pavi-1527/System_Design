//go:build breakit

package shorty

// Break-it experiments for E1. These are measurements, not pass/fail tests:
// they print tables so you can compare your prediction with your machine.
//
// Run: go test -tags breakit -run BreakIt -v ./labs/e1/shorty/

import (
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// What does SyncAlways cost? Unit 1 §1 asks you to predict the ratio first.
func TestBreakIt_FsyncCost(t *testing.T) {
	for _, writers := range []int{1, 8} {
		for _, p := range []SyncPolicy{SyncAlways, SyncNever} {
			l, _, err := OpenLog(tLogPath(t.TempDir()), p)
			if err != nil || l == nil {
				t.Fatalf("OpenLog: %v", err)
			}
			var n atomic.Int64
			deadline := time.Now().Add(time.Second)
			var wg sync.WaitGroup
			for w := 0; w < writers; w++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					for i := 0; time.Now().Before(deadline); i++ {
						if l.Put(fmt.Sprintf("w%d-%d", w, i), []byte("a 64-byte-ish value for a link record, roughly")) == nil {
							n.Add(1)
						}
					}
				}()
			}
			wg.Wait()
			l.Close()
			t.Logf("sync=%-6v writers=%d  ≈ %7d writes/s  (≈ %v per write)", p, writers, n.Load(),
				(time.Second / time.Duration(max(n.Load(), 1))).Round(time.Microsecond))
		}
	}
	t.Log("On macOS, Go's File.Sync issues F_FULLFSYNC (it flushes the drive's own write cache), so expect " +
		"milliseconds; Linux fsync on a local NVMe drive is often much faster. Group commit is the fix for the writers=8 row.")
}

// How many creates does "5 per client" really admit with N API instances?
// Unit 3 §1.
func TestBreakIt_LimiterIsPerInstance(t *testing.T) {
	store := tStore(t, t.TempDir())
	for _, instances := range []int{1, 2, 3} {
		var apis []*tProc
		for i := 0; i < instances; i++ {
			apis = append(apis, tAPI(t, fmt.Sprintf("api-%d-%d", instances, i), store.URL,
				"-create-rate", "0.001", "-create-burst", "5"))
		}
		admitted := 0
		for i := 0; i < 30; i++ {
			api := apis[i%len(apis)] // a load balancer spreading one client's requests
			code, _, _, err := tDo("POST", api.URL+"/v1/links", `{"url":"https://example.com/x"}`, "X-Client-ID", "alice")
			if err == nil && code == http.StatusCreated {
				admitted++
			}
		}
		t.Logf("instances=%d  configured limit 5  admitted %2d of 30", instances, admitted)
		for _, a := range apis {
			a.Stop(10 * time.Second)
		}
	}
}

// How long does a deleted link keep redirecting on another instance? Unit 2 §3.
func TestBreakIt_TakedownWindow(t *testing.T) {
	store := tStore(t, t.TempDir())
	for _, ttl := range []string{"250ms", "1s", "3s"} {
		a := tAPI(t, "api-a-"+ttl, store.URL, "-cache-ttl", ttl)
		b := tAPI(t, "api-b-"+ttl, store.URL, "-cache-ttl", ttl)
		code := tMustCreate(t, a.URL, "alice", "https://example.com/phish")
		tGet(t, b.URL+"/"+code)
		start := time.Now()
		tDo("DELETE", a.URL+"/v1/links/"+code, "", "X-Client-ID", "alice")
		served := 0
		for time.Since(start) < 10*time.Second {
			status, _ := tGet(t, b.URL+"/"+code)
			if status == http.StatusGone {
				break
			}
			served++
			time.Sleep(10 * time.Millisecond)
		}
		t.Logf("cache-ttl=%-6s  B stopped redirecting after %v (%d redirects served after the delete)",
			ttl, time.Since(start).Round(10*time.Millisecond), served)
		a.Stop(10 * time.Second)
		b.Stop(10 * time.Second)
	}
}
