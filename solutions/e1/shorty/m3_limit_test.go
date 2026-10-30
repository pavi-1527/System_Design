package shorty

// Milestone 3: the per-client token bucket, in-process and in the API
// (Unit 3 §1).

import (
	"fmt"
	"net/http"
	"strconv"
	"sync/atomic"
	"testing"
	"time"
)

func tNewBucket(t *testing.T, rate float64, burst, maxKeys int, clk *tClock) *TokenBucket {
	t.Helper()
	b := NewTokenBucket(rate, burst, maxKeys, clk.Now)
	if b == nil {
		t.Fatal("NewTokenBucket returned nil")
	}
	return b
}

func TestM3_TokenBucketAllowsTheBurstThenTheRate(t *testing.T) {
	clk := tNewClock()
	b := tNewBucket(t, 2, 5, 100, clk)
	for i := 0; i < 5; i++ {
		if ok, _ := b.Allow("alice"); !ok {
			t.Fatalf("request %d of the burst of 5 was refused", i+1)
		}
	}
	ok, wait := b.Allow("alice")
	if ok {
		t.Fatal("6th request in the same instant was allowed; burst is 5")
	}
	if wait != 500*time.Millisecond {
		t.Errorf("retryAfter = %v, want 500ms (one token at 2/s)", wait)
	}
	clk.Advance(499 * time.Millisecond)
	if ok, _ := b.Allow("alice"); ok {
		t.Error("allowed after 499ms; the token is not there yet")
	}
	clk.Advance(time.Millisecond)
	if ok, _ := b.Allow("alice"); !ok {
		t.Error("refused at exactly retryAfter; Retry-After must be truthful")
	}
	if ok, _ := b.Allow("alice"); ok {
		t.Error("two requests on one refilled token")
	}
}

func TestM3_TokenBucketIdleTimeNeverBanksMoreThanBurst(t *testing.T) {
	clk := tNewClock()
	b := tNewBucket(t, 10, 3, 100, clk)
	b.Allow("k")
	clk.Advance(time.Hour)
	n := 0
	for i := 0; i < 100; i++ {
		if ok, _ := b.Allow("k"); ok {
			n++
		}
	}
	if n != 3 {
		t.Fatalf("after an hour idle, %d requests were allowed at once, want 3 (burst caps the bank)", n)
	}
}

func TestM3_TokenBucketKeysAreIndependent(t *testing.T) {
	clk := tNewClock()
	b := tNewBucket(t, 1, 2, 100, clk)
	b.Allow("alice")
	b.Allow("alice")
	if ok, _ := b.Allow("alice"); ok {
		t.Fatal("alice's third request allowed")
	}
	if ok, _ := b.Allow("bob"); !ok {
		t.Fatal("bob was refused because alice used her tokens; buckets are per client")
	}
}

func TestM3_TokenBucketMemoryIsBounded(t *testing.T) {
	clk := tNewClock()
	b := tNewBucket(t, 1, 1, 100, clk)
	for i := 0; i < 10000; i++ {
		b.Allow(fmt.Sprintf("client-%d", i))
	}
	if n := b.Len(); n == 0 || n > 100 {
		t.Fatalf("Len = %d after 10,000 distinct clients with maxKeys 100; "+
			"unbounded per-key state is a memory leak anyone can trigger", n)
	}
	b.Allow("client-9999") // most recent: still tracked
	if ok, _ := b.Allow("client-9999"); ok {
		t.Error("the most recently used key lost its state; evict the least recently used")
	}
}

func TestM3_TokenBucketConcurrentCallersNeverOverspend(t *testing.T) {
	clk := tNewClock() // frozen: no refill during the test
	b := tNewBucket(t, 1, 100, 100, clk)
	var allowed atomic.Int32
	tParallel(t, 50, 10*time.Second, func(int) {
		for i := 0; i < 10; i++ {
			if ok, _ := b.Allow("shared"); ok {
				allowed.Add(1)
			}
		}
	})
	if n := allowed.Load(); n != 100 {
		t.Fatalf("%d of 500 concurrent requests were allowed from a bucket of 100", n)
	}
}

func TestM3_RetryAfterSecondsRoundsUp(t *testing.T) {
	for d, want := range map[time.Duration]int{
		time.Millisecond: 1, 999 * time.Millisecond: 1, time.Second: 1,
		1001 * time.Millisecond: 2, 2500 * time.Millisecond: 3, 0: 1,
	} {
		if got := RetryAfterSeconds(d); got != want {
			t.Errorf("RetryAfterSeconds(%v) = %d, want %d (round up: a client that returns early is refused again)", d, got, want)
		}
	}
}

func TestM3_CreatesAreRateLimitedPerClient(t *testing.T) {
	_, api := tSystem(t, "-create-rate", "1", "-create-burst", "3")
	for i := 0; i < 3; i++ {
		tMustCreate(t, api.URL, "alice", fmt.Sprintf("https://example.com/%d", i))
	}
	code, body, h, err := tDo("POST", api.URL+"/v1/links", `{"url":"https://example.com/x"}`, "X-Client-ID", "alice")
	if err != nil || code != http.StatusTooManyRequests {
		t.Fatalf("4th create in a burst of 3: %d %v %s, want 429", code, err, body)
	}
	ra, err := strconv.Atoi(h.Get("Retry-After"))
	if err != nil || ra < 1 || ra > 2 {
		t.Fatalf("Retry-After = %q, want whole seconds ≥ 1 (≈ 1 at 1 create/s)", h.Get("Retry-After"))
	}
	tMustCreate(t, api.URL, "bob", "https://example.com/bob") // another client is unaffected
	for i := 0; i < 20; i++ {
		if code, _, _, _ := tDo("GET", api.URL+"/"+NewCode(7), "", "X-Client-ID", "alice"); code == http.StatusTooManyRequests {
			t.Fatal("redirects were rate limited by the create limiter")
		}
	}
	time.Sleep(time.Duration(ra)*time.Second + 100*time.Millisecond)
	tMustCreate(t, api.URL, "alice", "https://example.com/after-waiting")
	if st := tStats(t, api.URL); st["rate_limited"] != 1 {
		t.Errorf("stats rate_limited = %d, want 1", st["rate_limited"])
	}
}

func TestM3_RateLimitIsCheckedBeforeTheStore(t *testing.T) {
	store, api := tSystem(t, "-create-rate", "0.1", "-create-burst", "1")
	tMustCreate(t, api.URL, "alice", "https://example.com/one")
	store.Kill()
	start := time.Now()
	code, _, _, err := tDo("POST", api.URL+"/v1/links", `{"url":"https://example.com/two"}`, "X-Client-ID", "alice")
	if err != nil || code != http.StatusTooManyRequests {
		t.Errorf("over-limit client with the store down: %d %v, want 429: shed before spending a store call", code, err)
	}
	if el := time.Since(start); el > 200*time.Millisecond {
		t.Errorf("a 429 took %v; refusing must be cheap", el)
	}
	code, _, h, err := tDo("POST", api.URL+"/v1/links", `{"url":"https://example.com/three"}`, "X-Client-ID", "carol")
	if err != nil || code != http.StatusServiceUnavailable || h.Get("Retry-After") == "" {
		t.Errorf("under-limit client with the store down: %d %v Retry-After=%q, want 503 with Retry-After", code, err, h.Get("Retry-After"))
	}
}
