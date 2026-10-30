package shorty

// Milestone 4: failure injection (spec.md "Failure scenarios", Unit 3 §2).
// Each test breaks one thing on purpose and checks the behaviour the spec
// predicts: what keeps working, what fails, how fast, and what recovers.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// tTimed runs one request and returns its status, headers and duration.
func tTimed(method, url, body string, hdr ...string) (int, http.Header, time.Duration, error) {
	start := time.Now()
	code, _, h, err := tDo(method, url, body, hdr...)
	return code, h, time.Since(start), err
}

const tCreateBody = `{"url":"https://example.com/during-an-outage"}`

// tFailsFast checks a 503 with Retry-After within bound.
func tFailsFast(t *testing.T, what, method, url, body string, bound time.Duration) {
	t.Helper()
	code, h, el, err := tTimed(method, url, body, "X-Client-ID", "alice")
	if err != nil {
		t.Fatalf("%s: transport error %v; the API must answer, not hang up", what, err)
	}
	if code != http.StatusServiceUnavailable || h.Get("Retry-After") == "" {
		t.Errorf("%s: %d Retry-After=%q, want 503 with Retry-After", what, code, h.Get("Retry-After"))
	}
	if el > bound {
		t.Errorf("%s took %v, want ≤ %v: a dead dependency must cost a bounded wait, not a hung request", what, el, bound)
	}
}

func TestM4_StoreDownCachedRedirectsKeepWorking(t *testing.T) {
	store, api := tSystem(t)
	var codes []string
	for i := 0; i < 5; i++ {
		codes = append(codes, tMustCreate(t, api.URL, "alice", fmt.Sprintf("https://example.com/%d", i)))
	}
	store.Kill()
	for _, c := range codes {
		if status, _ := tGet(t, api.URL+"/"+c); status != http.StatusFound {
			t.Errorf("cached redirect with the store down: %d, want 302 (the hot path must not need the store)", status)
		}
	}
	tFailsFast(t, "uncached redirect, store down", "GET", api.URL+"/"+NewCode(7), "", 1500*time.Millisecond)
	tFailsFast(t, "create, store down", "POST", api.URL+"/v1/links", tCreateBody, 1500*time.Millisecond)

	store.Restart()
	tEventually(t, 10*time.Second, func() (bool, string) {
		code, _, _, err := tDo("POST", api.URL+"/v1/links", tCreateBody, "X-Client-ID", "alice")
		return code == http.StatusCreated, fmt.Sprintf("create after the store came back: %d %v (the API must reconnect by itself)", code, err)
	})
	fresh := tAPI(t, "api-fresh", store.URL) // no cache: the links must be in the store
	for _, c := range codes {
		if status, _ := tGet(t, fresh.URL+"/"+c); status != http.StatusFound {
			t.Errorf("link %s after the store restart: %d, want 302", c, status)
		}
	}
}

func TestM4_SlowStoreFailsFastNotSlow(t *testing.T) {
	store := tStore(t, t.TempDir())
	px := tNewProxy(t, store.Addr)
	api := tAPI(t, "api", px.URL, "-store-timeout", "200ms", "-request-budget", "500ms")
	cachedCode := tMustCreate(t, api.URL, "alice", "https://example.com/cached")

	px.Delay(2 * time.Second) // the store is alive but every round trip takes ≥ 4 s
	tFailsFast(t, "create, slow store", "POST", api.URL+"/v1/links", tCreateBody, 1500*time.Millisecond)
	tFailsFast(t, "uncached redirect, slow store", "GET", api.URL+"/"+NewCode(7), "", 1500*time.Millisecond)
	code, _, el, err := tTimed("GET", api.URL+"/"+cachedCode, "")
	if err != nil || code != http.StatusFound || el > 500*time.Millisecond {
		t.Errorf("cached redirect, slow store: %d %v in %v; want a fast 302", code, err, el)
	}

	px.Heal()
	tEventually(t, 10*time.Second, func() (bool, string) {
		code, _, _, err := tDo("POST", api.URL+"/v1/links", tCreateBody, "X-Client-ID", "alice")
		return code == http.StatusCreated, fmt.Sprintf("create after the store recovered: %d %v", code, err)
	})
}

func TestM4_PartitionedStoreFailsFastAndRecovers(t *testing.T) {
	store := tStore(t, t.TempDir())
	px := tNewProxy(t, store.Addr)
	api := tAPI(t, "api", px.URL, "-store-timeout", "200ms", "-request-budget", "500ms")
	tMustCreate(t, api.URL, "alice", "https://example.com/before")

	px.Partition() // bytes vanish; nothing tells the API the store is unreachable
	tFailsFast(t, "create, partitioned store", "POST", api.URL+"/v1/links", tCreateBody, 1500*time.Millisecond)
	px.Heal()
	tEventually(t, 10*time.Second, func() (bool, string) {
		code, _, _, err := tDo("POST", api.URL+"/v1/links", tCreateBody, "X-Client-ID", "alice")
		return code == http.StatusCreated, fmt.Sprintf("create after the partition healed: %d %v", code, err)
	})
}

func TestM4_StaleIfErrorServesExpiredEntriesWhileTheStoreIsDown(t *testing.T) {
	store := tStore(t, t.TempDir())
	lenient := tAPI(t, "api-lenient", store.URL, "-cache-ttl", "300ms", "-stale-if-error", "1m")
	strict := tAPI(t, "api-strict", store.URL, "-cache-ttl", "300ms", "-stale-if-error", "0s")
	c1 := tMustCreate(t, lenient.URL, "alice", "https://example.com/lenient")
	c2 := tMustCreate(t, strict.URL, "alice", "https://example.com/strict")
	time.Sleep(500 * time.Millisecond) // both entries are past their freshness
	store.Kill()

	status, h := tGet(t, lenient.URL+"/"+c1)
	if status != http.StatusFound || h.Get("X-Cache") != "STALE" {
		t.Errorf("stale entry with the store down and -stale-if-error 1m: %d X-Cache=%q, want 302 STALE", status, h.Get("X-Cache"))
	}
	tFailsFast(t, "stale entry with -stale-if-error 0", "GET", strict.URL+"/"+c2, "", 1500*time.Millisecond)
	if st := tStats(t, lenient.URL); st["stale_served"] < 1 {
		t.Errorf("stale_served = %d, want ≥ 1: serving stale must be visible on a dashboard", st["stale_served"])
	}
}

func TestM4_AcknowledgedCreatesSurviveAStoreCrash(t *testing.T) {
	store, api := tSystem(t, "-create-rate", "10000", "-create-burst", "10000")
	var (
		mu    sync.Mutex
		acked = map[string]string{}
		n     atomic.Int64
		stop  atomic.Bool
		wg    sync.WaitGroup
	)
	for w := 0; w < 8; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; !stop.Load() && i < 5000; i++ {
				long := fmt.Sprintf("https://example.com/w%d/%d", w, i)
				code, body, _, err := tDo("POST", api.URL+"/v1/links", `{"url":"`+long+`"}`, "X-Client-ID", "alice")
				if err != nil || code != http.StatusCreated {
					continue
				}
				var c tCreated
				if json.Unmarshal([]byte(body), &c) != nil {
					continue
				}
				mu.Lock()
				acked[c.Code] = long
				mu.Unlock()
				n.Add(1)
			}
		}()
	}
	tEventually(t, 20*time.Second, func() (bool, string) {
		return n.Load() >= 60, fmt.Sprintf("only %d creates acknowledged", n.Load())
	})
	store.Kill()
	time.Sleep(200 * time.Millisecond) // creates now fail with 503
	stop.Store(true)
	tWait(t, &wg, 20*time.Second)

	store.Restart()
	fresh := tAPI(t, "api-fresh", store.URL)
	mu.Lock()
	defer mu.Unlock()
	for code, long := range acked {
		status, h := tGet(t, fresh.URL+"/"+code)
		if status != http.StatusFound || h.Get("Location") != long {
			t.Fatalf("acknowledged create %s → %s is gone after a store crash (%d %q): "+
				"a 201 is a promise the link works", code, long, status, h.Get("Location"))
		}
	}
	t.Logf("%d acknowledged creates all survived SIGKILL of the store", len(acked))
}

func TestM4_GracefulShutdownDrainsInFlightRequests(t *testing.T) {
	store := tStore(t, t.TempDir())
	px := tNewProxy(t, store.Addr)
	px.Delay(300 * time.Millisecond) // each create spends ≈ 600 ms waiting on the store
	api := tAPI(t, "api", px.URL, "-store-timeout", "5s", "-request-budget", "5s", "-drain", "10s",
		"-create-rate", "1000", "-create-burst", "1000")

	results := make(chan string, 20)
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			code, _, _, err := tDo("POST", api.URL+"/v1/links", fmt.Sprintf(`{"url":"https://example.com/%d"}`, i), "X-Client-ID", "alice")
			if err != nil {
				results <- "transport error: " + err.Error()
				return
			}
			results <- fmt.Sprint(code)
		}()
	}
	time.Sleep(150 * time.Millisecond) // all 20 are now in flight inside the API
	exit := api.Stop(15 * time.Second)
	tWait(t, &wg, 15*time.Second)
	close(results)
	for r := range results {
		if r != "201" {
			t.Errorf("in-flight create during SIGTERM got %s; a deploy must not fail requests it already accepted", r)
		}
	}
	if exit != 0 {
		t.Errorf("api exit code %d after a drain that finished in time, want 0", exit)
	}
	if _, _, _, err := tDo("GET", api.URL+"/-/healthz", ""); err == nil {
		t.Error("the API still accepts connections after shutting down")
	}
}
