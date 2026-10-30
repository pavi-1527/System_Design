package idempotency

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// ---------- helpers (prefixed with t to avoid clashing with your code) ----------

type tClock struct {
	mu sync.Mutex
	t  time.Time
}

func tNewClock() *tClock { return &tClock{t: time.Date(2026, 5, 4, 12, 0, 0, 0, time.UTC)} }
func (c *tClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}
func (c *tClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// tTransfers is a fake "create transfer" handler that counts executions.
type tTransfers struct {
	calls  atomic.Int64
	delay  time.Duration
	status func(n int64) int // status for the n-th call (1-based); nil => 201
}

func (h *tTransfers) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	n := h.calls.Add(1)
	body, _ := io.ReadAll(r.Body)
	if h.delay > 0 {
		time.Sleep(h.delay)
	}
	status := http.StatusCreated
	if h.status != nil {
		status = h.status(n)
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Transfer-ID", fmt.Sprintf("tr_%d", n))
	if status == http.StatusCreated {
		w.Header().Set("Location", fmt.Sprintf("/v1/transfers/tr_%d", n))
	}
	w.WriteHeader(status)
	fmt.Fprintf(w, `{"id":"tr_%d","request":%s}`, n, body)
}

func tReq(method, path, client, key, body string) *http.Request {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	if client != "" {
		r.Header.Set(HeaderClient, client)
	}
	if key != "" {
		r.Header.Set(HeaderKey, key)
	}
	r.Header.Set("Content-Type", "application/json")
	return r
}

func tDo(h http.Handler, r *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	return rec
}

func tErrCode(rec *httptest.ResponseRecorder) string {
	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	return body.Error.Code
}

const tBody = `{"from":"acct_1","to":"acct_2","amount_cents":500000,"currency":"USD"}`

// tWait waits for wg, failing the test instead of hanging if it takes longer
// than d (e.g. a deadlock in the implementation under test).
func tWait(t *testing.T, wg *sync.WaitGroup, d time.Duration, what string) {
	t.Helper()
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(d):
		t.Fatalf("%s did not finish within %v (deadlock or livelock in the implementation?)", what, d)
	}
}

// ---------- store ----------

func TestMemoryStore_ReserveIsAtomic(t *testing.T) {
	s := NewMemoryStore()
	now := time.Now()
	var wins atomic.Int64
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			_, ok, err := s.Reserve(context.Background(), "k", now, Record{State: StateInFlight, Owner: fmt.Sprint(i), ExpiresAt: now.Add(time.Minute)})
			if err == nil && ok {
				wins.Add(1)
			}
		}(i)
	}
	close(start)
	tWait(t, &wg, 10*time.Second, "100 concurrent Reserve calls")
	if wins.Load() != 1 {
		t.Fatalf("%d of 100 concurrent Reserve calls won; exactly 1 must, or two workers will both execute the payment", wins.Load())
	}
}

func TestMemoryStore_CompleteAndReleaseRequireOwnership(t *testing.T) {
	s := NewMemoryStore()
	ctx := context.Background()
	now := time.Now()
	if _, ok, err := s.Reserve(ctx, "k", now, Record{State: StateInFlight, Owner: "a", ExpiresAt: now.Add(time.Minute)}); err != nil || !ok {
		t.Fatalf("Reserve on empty store = (%v, %v), want reserved", ok, err)
	}
	if err := s.Complete(ctx, "k", Record{State: StateCompleted, Owner: "b", Status: 201, ExpiresAt: now.Add(time.Hour)}); !errors.Is(err, ErrNotOwner) {
		t.Fatalf("Complete by non-owner = %v, want ErrNotOwner", err)
	}
	if err := s.Release(ctx, "k", "b"); !errors.Is(err, ErrNotOwner) {
		t.Fatalf("Release by non-owner = %v, want ErrNotOwner", err)
	}
	if err := s.Complete(ctx, "k", Record{State: StateCompleted, Owner: "a", Status: 201, ExpiresAt: now.Add(time.Hour)}); err != nil {
		t.Fatalf("Complete by owner: %v", err)
	}
	got, ok, err := s.Get(ctx, "k", now)
	if err != nil || !ok || got.State != StateCompleted || got.Status != 201 {
		t.Fatalf("Get after Complete = (%+v, %v, %v), want the completed record", got, ok, err)
	}
	if err := s.Release(ctx, "k", "a"); !errors.Is(err, ErrNotOwner) {
		t.Fatalf("Release of a completed record = %v, want ErrNotOwner (completed records are not reservations)", err)
	}
	if _, ok, _ := s.Get(ctx, "k", now.Add(2*time.Hour)); ok {
		t.Fatalf("Get returned a record past its ExpiresAt")
	}
	if _, ok, _ := s.Reserve(ctx, "k", now.Add(2*time.Hour), Record{State: StateInFlight, Owner: "c", ExpiresAt: now.Add(3 * time.Hour)}); !ok {
		t.Fatalf("Reserve over an expired record must succeed")
	}
}

// ---------- middleware ----------

func TestIdempotency_SameKeySameBodyExecutesOnceAndReplays(t *testing.T) {
	h := &tTransfers{}
	mw := Middleware(Config{}, h)
	first := tDo(mw, tReq("POST", "/v1/transfers", "fintech-a", "key-1", tBody))
	if first.Code != http.StatusCreated {
		t.Fatalf("first request: status %d body %s, want 201", first.Code, first.Body)
	}
	if first.Header().Get(HeaderReplayed) != "" {
		t.Fatalf("first request must not be marked as replayed")
	}
	for i := 0; i < 3; i++ {
		again := tDo(mw, tReq("POST", "/v1/transfers", "fintech-a", "key-1", tBody))
		if again.Code != first.Code || again.Body.String() != first.Body.String() {
			t.Fatalf("retry %d: got %d %s, want the stored %d %s", i, again.Code, again.Body, first.Code, first.Body)
		}
		for _, hdr := range []string{"Content-Type", "Location", "X-Transfer-ID"} {
			if again.Header().Get(hdr) != first.Header().Get(hdr) {
				t.Fatalf("retry %d: header %s = %q, want stored %q", i, hdr, again.Header().Get(hdr), first.Header().Get(hdr))
			}
		}
		if again.Header().Get(HeaderReplayed) != "true" {
			t.Fatalf("retry %d: missing %s: true", i, HeaderReplayed)
		}
	}
	if n := h.calls.Load(); n != 1 {
		t.Fatalf("handler ran %d times for one idempotency key; the member would be debited %d times", n, n)
	}
}

func TestIdempotency_HandlerSeesFullBody(t *testing.T) {
	var got string
	mw := Middleware(Config{}, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got = string(b)
		w.WriteHeader(http.StatusCreated)
	}))
	tDo(mw, tReq("POST", "/v1/transfers", "c", "k", tBody))
	if got != tBody {
		t.Fatalf("handler read body %q, want %q: the middleware must restore r.Body after reading it", got, tBody)
	}
}

func TestIdempotency_SameKeyDifferentRequestIs422(t *testing.T) {
	h := &tTransfers{}
	mw := Middleware(Config{}, h)
	tDo(mw, tReq("POST", "/v1/transfers", "c", "k", tBody))
	diffBody := tDo(mw, tReq("POST", "/v1/transfers", "c", "k", strings.Replace(tBody, "500000", "900000", 1)))
	if diffBody.Code != http.StatusUnprocessableEntity || tErrCode(diffBody) == "" {
		t.Fatalf("same key, different amount: status %d body %s, want 422 with an error code", diffBody.Code, diffBody.Body)
	}
	diffPath := tDo(mw, tReq("POST", "/v1/payouts", "c", "k", tBody))
	if diffPath.Code != http.StatusUnprocessableEntity {
		t.Fatalf("same key, different path: status %d, want 422 (the fingerprint must include the path)", diffPath.Code)
	}
	if h.calls.Load() != 1 {
		t.Fatalf("handler ran %d times, want 1", h.calls.Load())
	}
}

func TestIdempotency_KeysAreScopedPerClient(t *testing.T) {
	h := &tTransfers{}
	mw := Middleware(Config{}, h)
	a := tDo(mw, tReq("POST", "/v1/transfers", "fintech-a", "order-1001", tBody))
	b := tDo(mw, tReq("POST", "/v1/transfers", "fintech-b", "order-1001", tBody))
	if a.Code != http.StatusCreated || b.Code != http.StatusCreated || b.Header().Get(HeaderReplayed) != "" {
		t.Fatalf("two clients with the same key: got %d and %d (replayed=%q); client B must not receive client A's response", a.Code, b.Code, b.Header().Get(HeaderReplayed))
	}
	if h.calls.Load() != 2 {
		t.Fatalf("handler ran %d times, want 2 (one per client)", h.calls.Load())
	}
}

func TestIdempotency_PassThroughCases(t *testing.T) {
	h := &tTransfers{}
	mw := Middleware(Config{}, h)
	for i := 0; i < 2; i++ {
		if rec := tDo(mw, tReq("POST", "/v1/transfers", "c", "", tBody)); rec.Code != http.StatusCreated {
			t.Fatalf("POST without key: status %d, want handler's 201", rec.Code)
		}
	}
	for i := 0; i < 2; i++ {
		rec := tDo(mw, tReq("GET", "/v1/transfers/tr_1", "c", "k-get", ""))
		if rec.Code != http.StatusCreated || rec.Header().Get(HeaderReplayed) != "" {
			t.Fatalf("GET with key: status %d replayed=%q, want pass-through (GET is already idempotent)", rec.Code, rec.Header().Get(HeaderReplayed))
		}
	}
	if h.calls.Load() != 4 {
		t.Fatalf("handler ran %d times, want 4: requests without a key, and GETs, are not intercepted", h.calls.Load())
	}
}

func TestIdempotency_BadKeyOrMissingClientIs400(t *testing.T) {
	h := &tTransfers{}
	mw := Middleware(Config{}, h)
	if rec := tDo(mw, tReq("POST", "/v1/transfers", "", "k", tBody)); rec.Code != http.StatusBadRequest || tErrCode(rec) == "" {
		t.Fatalf("key without client id: status %d body %s, want 400 with error code", rec.Code, rec.Body)
	}
	long := strings.Repeat("x", MaxKeyLength+1)
	if rec := tDo(mw, tReq("POST", "/v1/transfers", "c", long, tBody)); rec.Code != http.StatusBadRequest {
		t.Fatalf("256-char key: status %d, want 400", rec.Code)
	}
	if rec := tDo(mw, tReq("POST", "/v1/transfers", "c", strings.Repeat("x", MaxKeyLength), tBody)); rec.Code != http.StatusCreated {
		t.Fatalf("255-char key: status %d, want 201 (255 is allowed)", rec.Code)
	}
	if h.calls.Load() != 1 {
		t.Fatalf("handler ran %d times, want 1 (rejected requests must not reach it)", h.calls.Load())
	}
}

func TestIdempotency_ConcurrentDuplicateExecutesOnce(t *testing.T) {
	h := &tTransfers{delay: 30 * time.Millisecond}
	mw := Middleware(Config{}, h)
	const n = 50
	start := make(chan struct{})
	codes := make(chan *httptest.ResponseRecorder, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			codes <- tDo(mw, tReq("POST", "/v1/transfers", "mobile-app", "tap-7f3a", tBody))
		}()
	}
	close(start)
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("concurrent requests did not finish within 10s (deadlock?)")
	}
	close(codes)
	created, conflicts := 0, 0
	for rec := range codes {
		switch rec.Code {
		case http.StatusCreated:
			created++
		case http.StatusConflict:
			conflicts++
			if rec.Header().Get("Retry-After") == "" {
				t.Errorf("409 without Retry-After; clients need to know when to retry")
			}
		default:
			t.Errorf("unexpected status %d (%s); want 201 or 409", rec.Code, rec.Body)
		}
	}
	if got := h.calls.Load(); got != 1 {
		t.Fatalf("handler ran %d times for 50 concurrent duplicates; a retried payment would be charged %d times", got, got)
	}
	if created < 1 {
		t.Fatalf("no request got 201")
	}
	final := tDo(mw, tReq("POST", "/v1/transfers", "mobile-app", "tap-7f3a", tBody))
	if final.Code != http.StatusCreated || final.Header().Get(HeaderReplayed) != "true" {
		t.Fatalf("retry after completion: status %d replayed=%q, want replayed 201", final.Code, final.Header().Get(HeaderReplayed))
	}
	t.Logf("50 concurrent duplicates: %d x 201, %d x 409, handler ran once", created, conflicts)
}

func TestIdempotency_ServerErrorIsNotCached(t *testing.T) {
	h := &tTransfers{status: func(n int64) int {
		if n == 1 {
			return http.StatusServiceUnavailable // core banking down; nothing happened
		}
		return http.StatusCreated
	}}
	mw := Middleware(Config{}, h)
	first := tDo(mw, tReq("POST", "/v1/transfers", "c", "k", tBody))
	if first.Code != http.StatusServiceUnavailable {
		t.Fatalf("first: status %d, want the handler's 503 passed through", first.Code)
	}
	second := tDo(mw, tReq("POST", "/v1/transfers", "c", "k", tBody))
	if second.Code != http.StatusCreated || second.Header().Get(HeaderReplayed) != "" {
		t.Fatalf("retry after 503: status %d replayed=%q, want a fresh 201 (5xx must not be stored)", second.Code, second.Header().Get(HeaderReplayed))
	}
	if h.calls.Load() != 2 {
		t.Fatalf("handler ran %d times, want 2", h.calls.Load())
	}
}

func TestIdempotency_ClientErrorIsCached(t *testing.T) {
	h := &tTransfers{status: func(int64) int { return http.StatusUnprocessableEntity }} // e.g. insufficient funds
	mw := Middleware(Config{}, h)
	first := tDo(mw, tReq("POST", "/v1/transfers", "c", "k", tBody))
	second := tDo(mw, tReq("POST", "/v1/transfers", "c", "k", tBody))
	if second.Code != first.Code || second.Body.String() != first.Body.String() || second.Header().Get(HeaderReplayed) != "true" {
		t.Fatalf("retry after a 4xx: got %d %s, want the stored %d replayed", second.Code, second.Body, first.Code)
	}
	if h.calls.Load() != 1 {
		t.Fatalf("handler ran %d times, want 1: a decided 4xx is a result, not a transient failure", h.calls.Load())
	}
}

func TestIdempotency_KeyExpiresAfterTTL(t *testing.T) {
	clk := tNewClock()
	h := &tTransfers{}
	mw := Middleware(Config{TTL: 24 * time.Hour, Now: clk.Now}, h)
	tDo(mw, tReq("POST", "/v1/transfers", "c", "k", tBody))
	clk.Advance(23 * time.Hour)
	if rec := tDo(mw, tReq("POST", "/v1/transfers", "c", "k", tBody)); rec.Header().Get(HeaderReplayed) != "true" {
		t.Fatalf("retry at +23h was not replayed; TTL is 24h")
	}
	clk.Advance(2 * time.Hour)
	if rec := tDo(mw, tReq("POST", "/v1/transfers", "c", "k", tBody)); rec.Code != http.StatusCreated || rec.Header().Get(HeaderReplayed) != "" {
		t.Fatalf("request at +25h: status %d replayed=%q, want a fresh execution after TTL", rec.Code, rec.Header().Get(HeaderReplayed))
	}
	if h.calls.Load() != 2 {
		t.Fatalf("handler ran %d times, want 2", h.calls.Load())
	}
}

func TestIdempotency_StuckReservationLeaseCanBeTakenOver(t *testing.T) {
	clk := tNewClock()
	release := make(chan struct{})
	started := make(chan struct{}, 1)
	var calls atomic.Int64
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		if n == 1 {
			started <- struct{}{}
			select { // simulate a worker that hangs (GC pause, stuck downstream, ...)
			case <-release:
			case <-time.After(5 * time.Second):
			}
		}
		w.WriteHeader(http.StatusCreated)
		fmt.Fprintf(w, "attempt-%d", n)
	})
	mw := Middleware(Config{LockTimeout: 30 * time.Second, Now: clk.Now}, h)

	firstDone := make(chan *httptest.ResponseRecorder, 1)
	go func() { firstDone <- tDo(mw, tReq("POST", "/v1/transfers", "c", "k", tBody)) }()
	select {
	case <-started:
	case rec := <-firstDone:
		t.Fatalf("first request finished (status %d) without reaching the handler", rec.Code)
	case <-time.After(2 * time.Second):
		t.Fatal("first request never reached the handler")
	}
	clk.Advance(10 * time.Second)
	if rec := tDo(mw, tReq("POST", "/v1/transfers", "c", "k", tBody)); rec.Code != http.StatusConflict {
		t.Fatalf("retry at +10s while first is in flight: status %d, want 409", rec.Code)
	}
	clk.Advance(21 * time.Second) // +31s: lease expired
	second := tDo(mw, tReq("POST", "/v1/transfers", "c", "k", tBody))
	if second.Code != http.StatusCreated || second.Body.String() != "attempt-2" {
		t.Fatalf("retry after lease expiry: %d %q, want 201 attempt-2 (a crashed worker must not wedge the key forever)", second.Code, second.Body)
	}
	close(release)
	select {
	case <-firstDone:
	case <-time.After(5 * time.Second):
		t.Fatal("first request never finished")
	}
	final := tDo(mw, tReq("POST", "/v1/transfers", "c", "k", tBody))
	if final.Body.String() != "attempt-2" || final.Header().Get(HeaderReplayed) != "true" {
		t.Fatalf("after the stale owner finished: replay %q, want attempt-2; a stale owner must not overwrite the new owner's record", final.Body)
	}
}

func TestIdempotency_HandlerPanicReleasesReservation(t *testing.T) {
	var calls atomic.Int64
	mw := Middleware(Config{}, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			panic("nil pointer in transfer handler")
		}
		w.WriteHeader(http.StatusCreated)
	}))
	func() {
		defer func() { _ = recover() }()
		tDo(mw, tReq("POST", "/v1/transfers", "c", "k", tBody))
	}()
	rec := tDo(mw, tReq("POST", "/v1/transfers", "c", "k", tBody))
	if rec.Code != http.StatusCreated {
		t.Fatalf("retry after a handler panic: status %d, want 201 (the reservation must be released, not left in flight)", rec.Code)
	}
}

type tFailingStore struct{}

func (tFailingStore) Complete(context.Context, string, Record) error {
	return errors.New("redis: connection refused")
}
func (tFailingStore) Release(context.Context, string, string) error {
	return errors.New("redis: connection refused")
}

func (tFailingStore) Reserve(context.Context, string, time.Time, Record) (Record, bool, error) {
	return Record{}, false, errors.New("redis: connection refused")
}
func (tFailingStore) Get(context.Context, string, time.Time) (Record, bool, error) {
	return Record{}, false, errors.New("redis: connection refused")
}

func TestIdempotency_StoreFailureFailsClosed(t *testing.T) {
	h := &tTransfers{}
	mw := Middleware(Config{Store: tFailingStore{}}, h)
	rec := tDo(mw, tReq("POST", "/v1/transfers", "c", "k", tBody))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("store down: status %d, want 503 (fail closed for money movement)", rec.Code)
	}
	if h.calls.Load() != 0 {
		t.Fatalf("handler ran with the idempotency store down; duplicates would go undetected")
	}
}
