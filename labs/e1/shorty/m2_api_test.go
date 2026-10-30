package shorty

// Milestone 2, multi-process: the API and its read cache (spec.md "API",
// Unit 2 §1–§3).

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// tSystem starts a store and one api instance.
func tSystem(t *testing.T, apiFlags ...string) (store, api *tProc) {
	t.Helper()
	store = tStore(t, t.TempDir())
	api = tAPI(t, "api", store.URL, apiFlags...)
	return store, api
}

func tGet(t *testing.T, url string) (int, http.Header) {
	t.Helper()
	code, _, h, err := tDo("GET", url, "")
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	return code, h
}

func TestM2_CreateAndRedirect(t *testing.T) {
	_, api := tSystem(t)
	long := "https://example.com/statements/2026-01?member=42&x=%20y"
	code, c, _ := tCreate(t, api.URL, "alice", long, 0)
	if code != http.StatusCreated {
		t.Fatalf("create = %d, want 201", code)
	}
	if len(c.Code) != 7 || !validCode(c.Code) {
		t.Errorf("code %q: want 7 base-62 characters (the -code-len default)", c.Code)
	}
	if c.ShortURL != api.URL+"/"+c.Code || c.URL != long || c.ExpiresAt != nil {
		t.Errorf("create body %+v: want short_url %s/%s, url echoed, no expires_at", c, api.URL, c.Code)
	}
	status, h := tGet(t, api.URL+"/"+c.Code)
	if status != http.StatusFound {
		t.Fatalf("GET /%s = %d, want 302 (a 301 is cached by browsers forever: no analytics, no takedown)", c.Code, status)
	}
	if h.Get("Location") != long {
		t.Errorf("Location = %q, want %q byte for byte", h.Get("Location"), long)
	}
	if cc := h.Get("Cache-Control"); cc != "private, no-store" {
		t.Errorf("Cache-Control = %q, want %q by default", cc, "private, no-store")
	}
	if code, _, _, _ := tDo("GET", api.URL+"/-/healthz", ""); code != 200 {
		t.Errorf("healthz = %d", code)
	}
}

func TestM2_CreateValidatesInput(t *testing.T) {
	store, api := tSystem(t)
	bad := map[string]string{
		"not JSON":       `{"url":`,
		"ftp scheme":     `{"url":"ftp://example.com/f"}`,
		"javascript URL": `{"url":"javascript:alert(1)"}`,
		"relative URL":   `{"url":"/just/a/path"}`,
		"negative TTL":   `{"url":"https://example.com","ttl_seconds":-1}`,
		"oversized URL":  `{"url":"https://example.com/` + strings.Repeat("a", 3000) + `"}`,
	}
	for name, body := range bad {
		code, resp, _, err := tDo("POST", api.URL+"/v1/links", body, "X-Client-ID", "alice")
		if err != nil || code != http.StatusBadRequest {
			t.Errorf("%s: got %d %v %s, want 400", name, code, err, resp)
		}
	}
	if code, _, _, _ := tDo("POST", api.URL+"/v1/links", `{"url":"https://example.com"}`,
		"X-Client-ID", "alice", "Idempotency-Key", "has spaces in it"); code != http.StatusBadRequest {
		t.Errorf("malformed Idempotency-Key: %d, want 400", code)
	}
	before := tStats(t, store.URL)["reads"]
	for _, path := range []string{"/no!", "/ab", "/" + strings.Repeat("a", 40)} {
		if status, _ := tGet(t, api.URL+path); status != http.StatusNotFound {
			t.Errorf("GET %s = %d, want 404", path, status)
		}
	}
	if after := tStats(t, store.URL)["reads"]; after != before {
		t.Errorf("malformed codes caused %d store reads; reject them before the cache and the store", after-before)
	}
}

func TestM2_ExpiredAndDeletedLinksAre410AndUnknownIs404(t *testing.T) {
	_, api := tSystem(t)
	code, c, _ := tCreate(t, api.URL, "alice", "https://example.com/short-lived", 1)
	if code != http.StatusCreated || c.ExpiresAt == nil {
		t.Fatalf("create with ttl_seconds=1: %d %+v, want 201 with expires_at", code, c)
	}
	if status, _ := tGet(t, api.URL+"/"+c.Code); status != http.StatusFound {
		t.Fatalf("before expiry: %d, want 302", status)
	}
	tEventually(t, 5*time.Second, func() (bool, string) {
		status, _ := tGet(t, api.URL+"/"+c.Code)
		return status == http.StatusGone, fmt.Sprintf("GET after expiry = %d, want 410 (the cache must not outlive the link)", status)
	})

	gone := tMustCreate(t, api.URL, "alice", "https://example.com/to-delete")
	if code, _, _, _ := tDo("DELETE", api.URL+"/v1/links/"+gone, "", "X-Client-ID", "mallory"); code != http.StatusNotFound {
		t.Errorf("DELETE by a non-owner = %d, want 404 (not 403: don't confirm the code exists)", code)
	}
	if status, _ := tGet(t, api.URL+"/"+gone); status != http.StatusFound {
		t.Fatalf("a non-owner's DELETE took effect: %d", status)
	}
	if code, _, _, _ := tDo("DELETE", api.URL+"/v1/links/"+gone, "", "X-Client-ID", "alice"); code != http.StatusNoContent {
		t.Fatalf("DELETE by owner = %d, want 204", code)
	}
	if status, h := tGet(t, api.URL+"/"+gone); status != http.StatusGone || h.Get("Cache-Control") != "no-store" {
		t.Errorf("GET deleted = %d (Cache-Control %q), want 410 with no-store, immediately on this instance",
			status, h.Get("Cache-Control"))
	}
	if code, _, _, _ := tDo("DELETE", api.URL+"/v1/links/"+gone, "", "X-Client-ID", "alice"); code != http.StatusNoContent {
		t.Errorf("second DELETE = %d, want 204 (DELETE is idempotent)", code)
	}
	if status, _ := tGet(t, api.URL+"/"+NewCode(7)); status != http.StatusNotFound {
		t.Errorf("unknown code = %d, want 404", status)
	}
}

func TestM2_IdempotentCreateReplaysTheFirstResponse(t *testing.T) {
	_, api := tSystem(t)
	long := "https://example.com/transfer-receipt/9001"
	c1, r1, h1 := tCreate(t, api.URL, "alice", long, 0, "Idempotency-Key", "key-1")
	c2, r2, h2 := tCreate(t, api.URL, "alice", long, 0, "Idempotency-Key", "key-1")
	if c1 != 201 || c2 != 201 {
		t.Fatalf("statuses %d, %d; want 201 both times", c1, c2)
	}
	if r1.Code != r2.Code {
		t.Fatalf("retry with the same Idempotency-Key created a second link (%s, %s); "+
			"a retrying notification sender would send two different links", r1.Code, r2.Code)
	}
	if h1.Get("Idempotent-Replayed") != "" || h2.Get("Idempotent-Replayed") != "true" {
		t.Errorf("Idempotent-Replayed: first %q, second %q; want empty, then true",
			h1.Get("Idempotent-Replayed"), h2.Get("Idempotent-Replayed"))
	}
	if c, _, _ := tCreate(t, api.URL, "alice", "https://example.com/something-else", 0, "Idempotency-Key", "key-1"); c != 422 {
		t.Errorf("same key, different URL = %d, want 422 (a client bug must not get someone else's link)", c)
	}
	c3, r3, _ := tCreate(t, api.URL, "bob", long, 0, "Idempotency-Key", "key-1")
	if c3 != 201 || r3.Code == r1.Code {
		t.Errorf("bob reusing alice's key got %d %s; keys are scoped per client", c3, r3.Code)
	}
}

func TestM2_ConcurrentDuplicatesCreateOneLink(t *testing.T) {
	store, api := tSystem(t)
	body := `{"url":"https://example.com/once"}`
	var mu sync.Mutex
	codes := map[string]int{}
	var originals atomic.Int32
	tParallel(t, 20, 20*time.Second, func(int) {
		code, resp, h, err := tDo("POST", api.URL+"/v1/links", body, "X-Client-ID", "alice", "Idempotency-Key", "dup")
		if err != nil || code != http.StatusCreated {
			t.Errorf("concurrent duplicate: %d %v %s", code, err, resp)
			return
		}
		if h.Get("Idempotent-Replayed") == "" {
			originals.Add(1)
		}
		i := strings.Index(resp, `"code":"`)
		if i < 0 {
			t.Errorf("no code in %s", resp)
			return
		}
		mu.Lock()
		codes[resp[i+8:i+8+7]]++
		mu.Unlock()
	})
	if len(codes) != 1 {
		t.Fatalf("20 concurrent requests with one Idempotency-Key produced %d different codes: %v", len(codes), codes)
	}
	if n := originals.Load(); n != 1 {
		t.Errorf("%d responses were not marked Idempotent-Replayed, want exactly 1 (the request that claimed the key)", n)
	}
	if keys := tStats(t, store.URL)["keys"]; keys != 2 {
		t.Errorf("store holds %d keys, want 2 (one idem/ record, one link/ record)", keys)
	}
}

func TestM2_RedirectMaxAgeIsCappedByExpiry(t *testing.T) {
	_, api := tSystem(t, "-redirect-max-age", "60s")
	forever := tMustCreate(t, api.URL, "alice", "https://example.com/forever")
	if _, h := tGet(t, api.URL+"/"+forever); h.Get("Cache-Control") != "private, max-age=60" {
		t.Errorf("Cache-Control = %q, want %q", h.Get("Cache-Control"), "private, max-age=60")
	}
	code, c, _ := tCreate(t, api.URL, "alice", "https://example.com/soon", 10)
	if code != 201 {
		t.Fatalf("create: %d", code)
	}
	_, h := tGet(t, api.URL+"/"+c.Code)
	age, err := strconv.Atoi(strings.TrimPrefix(h.Get("Cache-Control"), "private, max-age="))
	if err != nil || age > 10 || age < 7 {
		t.Errorf("Cache-Control = %q for a link expiring in 10 s; max-age must not let a browser use it after expiry",
			h.Get("Cache-Control"))
	}
}

func TestM2_RepeatRedirectsAreServedFromCache(t *testing.T) {
	store, api := tSystem(t)
	code := tMustCreate(t, api.URL, "alice", "https://example.com/popular")
	before := tStats(t, store.URL)["reads"]
	for i := 0; i < 20; i++ {
		status, h := tGet(t, api.URL+"/"+code)
		if status != http.StatusFound || h.Get("X-Cache") != "HIT" {
			t.Fatalf("redirect %d: %d X-Cache=%q, want 302 HIT (create warms the cache)", i, status, h.Get("X-Cache"))
		}
	}
	if reads := tStats(t, store.URL)["reads"] - before; reads != 0 {
		t.Errorf("20 redirects of a cached link caused %d store reads, want 0", reads)
	}
}

func TestM2_NegativeCachingShieldsTheStore(t *testing.T) {
	store, api := tSystem(t, "-neg-ttl", "1s")
	missing := NewCode(7)
	before := tStats(t, store.URL)["reads"]
	for i := 0; i < 30; i++ {
		if status, _ := tGet(t, api.URL+"/"+missing); status != http.StatusNotFound {
			t.Fatalf("unknown code = %d, want 404", status)
		}
	}
	if reads := tStats(t, store.URL)["reads"] - before; reads != 1 {
		t.Fatalf("30 lookups of one unknown code caused %d store reads, want 1: "+
			"without negative caching an enumeration attack goes straight to the store", reads)
	}
	time.Sleep(1200 * time.Millisecond)
	tGet(t, api.URL+"/"+missing)
	if reads := tStats(t, store.URL)["reads"] - before; reads != 2 {
		t.Errorf("after -neg-ttl passed: %d store reads in total, want 2 (the negative entry must expire)", reads)
	}
}

func TestM2_StampedeOnAColdKeyIsCoalesced(t *testing.T) {
	store := tStore(t, t.TempDir())
	writer := tAPI(t, "api-writer", store.URL)
	slow := tNewProxy(t, store.Addr)
	slow.Delay(300 * time.Millisecond) // every store round trip from the reader takes ≥ 600 ms
	reader := tAPI(t, "api-reader", slow.URL, "-store-timeout", "5s", "-request-budget", "5s")

	code := tMustCreate(t, writer.URL, "alice", "https://example.com/viral")
	before := tStats(t, store.URL)["reads"]
	tParallel(t, 50, 20*time.Second, func(int) {
		status, _, h, err := tDo("GET", reader.URL+"/"+code, "")
		if err != nil || status != http.StatusFound {
			t.Errorf("stampede GET: %d %v (X-Cache %q)", status, err, h.Get("X-Cache"))
		}
	})
	if reads := tStats(t, store.URL)["reads"] - before; reads != 1 {
		t.Fatalf("50 concurrent misses on one cold key caused %d store reads, want 1: "+
			"one viral link would multiply store load by the number of waiting requests", reads)
	}
	if st := tStats(t, reader.URL); st["coalesced"] < 40 {
		t.Errorf("coalesced = %d, want most of the 49 waiters to be counted", st["coalesced"])
	}
}

func TestM2_DeleteIsImmediateLocallyAndBoundedByTTLElsewhere(t *testing.T) {
	store := tStore(t, t.TempDir())
	a := tAPI(t, "api-a", store.URL, "-cache-ttl", "1500ms")
	b := tAPI(t, "api-b", store.URL, "-cache-ttl", "1500ms")
	code := tMustCreate(t, a.URL, "alice", "https://example.com/takedown")
	if status, _ := tGet(t, b.URL+"/"+code); status != http.StatusFound {
		t.Fatalf("instance B: %d, want 302 (and now B has it cached)", status)
	}
	deleted := time.Now()
	if status, _, _, _ := tDo("DELETE", a.URL+"/v1/links/"+code, "", "X-Client-ID", "alice"); status != http.StatusNoContent {
		t.Fatalf("DELETE via A: %d", status)
	}
	if status, _ := tGet(t, a.URL+"/"+code); status != http.StatusGone {
		t.Errorf("instance A right after its own delete: %d, want 410 (invalidate on write)", status)
	}
	status, h := tGet(t, b.URL+"/"+code)
	t.Logf("instance B right after the delete: %d X-Cache=%s (stale by design until its entry expires)", status, h.Get("X-Cache"))
	tEventually(t, 4*time.Second, func() (bool, string) {
		status, _ := tGet(t, b.URL+"/"+code)
		return status == http.StatusGone, fmt.Sprintf("instance B still answers %d %v after the delete; "+
			"staleness must be bounded by -cache-ttl (1.5 s)", status, time.Since(deleted).Round(time.Millisecond))
	})
	if el := time.Since(deleted); el > 3500*time.Millisecond {
		t.Errorf("B took %v to stop serving a deleted link; the bound is the 1.5 s TTL plus scheduling slack", el)
	}
}
