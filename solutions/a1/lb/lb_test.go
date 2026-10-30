package lb

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// ---------- helpers ----------

func tBackend(t *testing.T, raw string) *Backend {
	t.Helper()
	b, err := NewBackend(raw)
	if err != nil {
		t.Fatalf("NewBackend(%q): %v", raw, err)
	}
	return b
}

func tBackends(t *testing.T, n int) []*Backend {
	t.Helper()
	out := make([]*Backend, n)
	for i := range out {
		out[i] = tBackend(t, fmt.Sprintf("http://10.0.0.%d:8080", i+1))
	}
	return out
}

func tIndex(bs []*Backend, b *Backend) int {
	for i, x := range bs {
		if x == b {
			return i
		}
	}
	return -1
}

func tContains(bs []*Backend, b *Backend) bool { return tIndex(bs, b) >= 0 }

type tClock struct {
	mu sync.Mutex
	t  time.Time
}

func tNewClock() *tClock {
	return &tClock{t: time.Date(2026, 1, 1, 9, 0, 0, 0, time.UTC)}
}
func (c *tClock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *tClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// tScriptedRand returns the scripted values in order and records the n it was
// called with. It fails the test (instead of panicking) if the script runs out.
type tScriptedRand struct {
	t      *testing.T
	mu     sync.Mutex
	vals   []int
	gotNs  []int
	called int
}

func (s *tScriptedRand) IntN(n int) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.gotNs = append(s.gotNs, n)
	if s.called >= len(s.vals) {
		s.t.Errorf("Rand called %d times; script only has %d values", s.called+1, len(s.vals))
		return 0
	}
	v := s.vals[s.called]
	s.called++
	return v
}

// tEventually polls cond until it is true or the deadline passes.
func tEventually(t *testing.T, d time.Duration, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatal(msg)
}

// tNamedServer returns an httptest server that answers with its name and the
// request path it saw.
func tNamedServer(name string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "%s %s", name, r.URL.RequestURI())
	}))
}

var tClient = &http.Client{Timeout: 5 * time.Second}

func tGet(t *testing.T, url string) (int, string) {
	t.Helper()
	resp, err := tClient.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(body)
}

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

// ---------- pickers ----------

func TestPickers_SatisfyPicker(t *testing.T) {
	var _ Picker = &RoundRobin{}
	var _ Picker = LeastConnections{}
	var _ Picker = PowerOfTwoChoices{}
}

func TestRoundRobin_CyclesInOrder(t *testing.T) {
	bs := tBackends(t, 3)
	rr := &RoundRobin{}
	for i := 0; i < 7; i++ {
		got := rr.Pick(bs)
		if want := bs[i%3]; got != want {
			t.Fatalf("pick %d: got backend index %d, want %d (round robin must cycle 0,1,2,0,...)", i, tIndex(bs, got), i%3)
		}
	}
	if rr.Pick(nil) != nil {
		t.Fatalf("Pick(nil) must return nil")
	}
}

func TestRoundRobin_ConcurrentPicksAreExactlyEven(t *testing.T) {
	bs := tBackends(t, 3)
	rr := &RoundRobin{}
	const goroutines, per = 8, 300
	var counts [3]atomic.Int64
	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < per; i++ {
				if idx := tIndex(bs, rr.Pick(bs)); idx >= 0 {
					counts[idx].Add(1)
				}
			}
		}()
	}
	tWait(t, &wg, 10*time.Second, "concurrent RoundRobin picks")
	for i := range counts {
		if got := counts[i].Load(); got != goroutines*per/3 {
			t.Errorf("backend %d got %d picks, want exactly %d: a shared atomic counter makes concurrent round robin exact", i, got, goroutines*per/3)
		}
	}
}

func TestLeastConnections_PicksFewestInFlightAndBreaksTiesByOrder(t *testing.T) {
	bs := tBackends(t, 3)
	for i, n := range []int{3, 1, 2} {
		for k := 0; k < n; k++ {
			bs[i].Acquire()
		}
	}
	if got := (LeastConnections{}).Pick(bs); got != bs[1] {
		t.Fatalf("got index %d, want 1 (in-flight 3,1,2 => pick the one with 1)", tIndex(bs, got))
	}
	bs[1].Acquire() // now 3,2,2 => tie between 1 and 2 => earliest wins
	if got := (LeastConnections{}).Pick(bs); got != bs[1] {
		t.Fatalf("got index %d, want 1 (tie on 2 in-flight must go to the earliest candidate)", tIndex(bs, got))
	}
	if (LeastConnections{}).Pick(nil) != nil {
		t.Fatalf("Pick(nil) must return nil")
	}
}

func TestPowerOfTwoChoices_FollowsDrawProtocolAndPicksLessLoaded(t *testing.T) {
	bs := tBackends(t, 4)
	for i, n := range []int{0, 4, 5, 1} {
		for k := 0; k < n; k++ {
			bs[i].Acquire()
		}
	}
	cases := []struct {
		script []int
		want   int
		why    string
	}{
		{[]int{2, 2}, 3, "i=2, j=2 -> j>=i so j=3; in-flight 5 vs 1 -> pick 3"},
		{[]int{1, 0}, 0, "i=1, j=0; in-flight 4 vs 0 -> pick 0"},
		{[]int{3, 2}, 3, "i=3, j=2; in-flight 1 vs 5 -> keep 3"},
		{[]int{0, 1}, 0, "i=0, j=1 -> j>=i so j=2; in-flight 0 vs 5 -> keep 0"},
	}
	for _, c := range cases {
		sr := &tScriptedRand{t: t, vals: c.script}
		p := PowerOfTwoChoices{Rand: sr.IntN}
		got := p.Pick(bs)
		if tIndex(bs, got) != c.want {
			t.Errorf("script %v: got index %d, want %d (%s)", c.script, tIndex(bs, got), c.want, c.why)
		}
		if len(sr.gotNs) != 2 || sr.gotNs[0] != 4 || sr.gotNs[1] != 3 {
			t.Errorf("script %v: Rand called with %v, want [4 3] (i := Rand(n); j := Rand(n-1))", c.script, sr.gotNs)
		}
	}
}

func TestPowerOfTwoChoices_TieGoesToFirstDraw(t *testing.T) {
	bs := tBackends(t, 3) // all zero in flight
	sr := &tScriptedRand{t: t, vals: []int{2, 0}}
	got := PowerOfTwoChoices{Rand: sr.IntN}.Pick(bs)
	if got != bs[2] {
		t.Fatalf("got index %d, want 2: on a tie, prefer the first draw i", tIndex(bs, got))
	}
}

func TestPowerOfTwoChoices_SingleCandidateNeedsNoRandomness(t *testing.T) {
	bs := tBackends(t, 1)
	sr := &tScriptedRand{t: t}
	if got := (PowerOfTwoChoices{Rand: sr.IntN}).Pick(bs); got != bs[0] {
		t.Fatalf("single candidate must be returned")
	}
	if (PowerOfTwoChoices{}).Pick(nil) != nil {
		t.Fatalf("Pick(nil) must return nil")
	}
}

func TestPowerOfTwoChoices_DefaultRandIsConcurrencySafeAndInRange(t *testing.T) {
	bs := tBackends(t, 5)
	p := PowerOfTwoChoices{}
	var wg sync.WaitGroup
	var bad atomic.Int64
	seen := make([]atomic.Int64, len(bs))
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 500; i++ {
				idx := tIndex(bs, p.Pick(bs))
				if idx < 0 {
					bad.Add(1)
					continue
				}
				seen[idx].Add(1)
			}
		}()
	}
	tWait(t, &wg, 10*time.Second, "concurrent PowerOfTwoChoices picks")
	if bad.Load() > 0 {
		t.Fatalf("%d picks returned a backend outside the candidate list", bad.Load())
	}
	for i := range seen {
		if seen[i].Load() == 0 {
			t.Errorf("backend %d never picked in 4000 random picks; the sampling is not covering all candidates", i)
		}
	}
}

// ---------- pool health ----------

func TestPool_EjectsAfterConsecutiveFailures(t *testing.T) {
	bs := tBackends(t, 3)
	clk := tNewClock()
	p := NewPool(Config{FailureThreshold: 3, Cooldown: 30 * time.Second, Now: clk.Now}, bs...)

	p.ReportFailure(bs[0])
	p.ReportFailure(bs[0])
	p.ReportSuccess(bs[0]) // resets the streak
	p.ReportFailure(bs[0])
	p.ReportFailure(bs[0])
	if !tContains(p.Healthy(), bs[0]) {
		t.Fatalf("backend ejected after 2 consecutive failures (a success reset the streak); threshold is 3")
	}
	p.ReportFailure(bs[0])
	h := p.Healthy()
	if tContains(h, bs[0]) {
		t.Fatalf("backend still healthy after 3 consecutive failures; it should be ejected")
	}
	if len(h) != 2 || h[0] != bs[1] || h[1] != bs[2] {
		t.Fatalf("Healthy() = %v, want the other two backends in original order", h)
	}
	for i := 0; i < 50; i++ {
		b, err := p.Next()
		if err != nil {
			t.Fatalf("Next: %v", err)
		}
		if b == bs[0] {
			t.Fatalf("Next returned an ejected backend")
		}
	}
}

func TestPool_ReadmitsAfterCooldownOnProbation(t *testing.T) {
	bs := tBackends(t, 2)
	clk := tNewClock()
	p := NewPool(Config{FailureThreshold: 3, Cooldown: 30 * time.Second, Now: clk.Now}, bs...)
	for i := 0; i < 3; i++ {
		p.ReportFailure(bs[0])
	}
	clk.Advance(29 * time.Second)
	p.ReportSuccess(bs[0]) // late success from an in-flight request: ignored while ejected
	if tContains(p.Healthy(), bs[0]) {
		t.Fatalf("backend re-admitted before its 30s cooldown elapsed (or a late success cut the ejection short)")
	}
	clk.Advance(1 * time.Second)
	if !tContains(p.Healthy(), bs[0]) {
		t.Fatalf("backend not re-admitted after the 30s cooldown")
	}
	// Probation: one failure re-ejects.
	p.ReportFailure(bs[0])
	if tContains(p.Healthy(), bs[0]) {
		t.Fatalf("backend on probation survived a failure; a re-admitted backend should be re-ejected on its first failure")
	}
	clk.Advance(30 * time.Second)
	if !tContains(p.Healthy(), bs[0]) {
		t.Fatalf("backend not re-admitted after second cooldown")
	}
	p.ReportSuccess(bs[0]) // clears probation
	p.ReportFailure(bs[0])
	p.ReportFailure(bs[0])
	if !tContains(p.Healthy(), bs[0]) {
		t.Fatalf("after a success clears probation, 2 failures (< threshold 3) must not eject")
	}
}

func TestPool_AllEjectedReturnsErrNoHealthyBackends(t *testing.T) {
	bs := tBackends(t, 2)
	p := NewPool(Config{FailureThreshold: 1, Cooldown: time.Hour}, bs...)
	p.ReportFailure(bs[0])
	p.ReportFailure(bs[1])
	b, err := p.Next()
	if !errors.Is(err, ErrNoHealthyBackends) || b != nil {
		t.Fatalf("Next() = (%v, %v), want (nil, ErrNoHealthyBackends)", b, err)
	}
}

func TestPool_DefaultsThresholdIsThree(t *testing.T) {
	bs := tBackends(t, 2)
	p := NewPool(Config{}, bs...)
	p.ReportFailure(bs[0])
	p.ReportFailure(bs[0])
	if !tContains(p.Healthy(), bs[0]) {
		t.Fatalf("default FailureThreshold should be 3; backend ejected after 2")
	}
	p.ReportFailure(bs[0])
	if tContains(p.Healthy(), bs[0]) {
		t.Fatalf("default FailureThreshold should be 3; backend not ejected after 3")
	}
	if _, err := p.Next(); err != nil {
		t.Fatalf("Next with default picker: %v", err)
	}
}

func TestPool_DrainStopsNewTrafficAndWaitsForInFlight(t *testing.T) {
	bs := tBackends(t, 2)
	p := NewPool(Config{}, bs...)
	bs[0].Acquire() // one request in flight

	done := make(chan error, 1)
	go func() { done <- p.Drain(context.Background(), bs[0]) }()

	tEventually(t, time.Second, func() bool { return !tContains(p.Healthy(), bs[0]) },
		"draining backend still listed as healthy; it must stop receiving new requests immediately")
	for i := 0; i < 20; i++ {
		b, err := p.Next()
		if err != nil {
			t.Fatalf("Next while draining: %v", err)
		}
		if b == bs[0] {
			t.Fatalf("Next returned the draining backend")
		}
	}
	select {
	case err := <-done:
		t.Fatalf("Drain returned (%v) while a request was still in flight; it must wait", err)
	case <-time.After(30 * time.Millisecond):
	}
	bs[0].Release()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Drain returned %v, want nil once in-flight reached 0", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("Drain did not return within 2s of in-flight reaching 0")
	}
	p.Undrain(bs[0])
	if !tContains(p.Healthy(), bs[0]) {
		t.Fatalf("Undrain did not restore the backend")
	}
}

func TestPool_DrainRespectsContextDeadline(t *testing.T) {
	bs := tBackends(t, 1)
	p := NewPool(Config{}, bs...)
	bs[0].Acquire()
	defer bs[0].Release()
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	start := time.Now()
	err := p.Drain(ctx, bs[0])
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Drain = %v, want context.DeadlineExceeded (a stuck request must not block a deploy forever)", err)
	}
	if time.Since(start) > time.Second {
		t.Fatalf("Drain ignored the context deadline")
	}
	other := tBackend(t, "http://10.9.9.9:1")
	if err := p.Drain(context.Background(), other); !errors.Is(err, ErrUnknownBackend) {
		t.Fatalf("Drain(unknown) = %v, want ErrUnknownBackend", err)
	}
}

// ---------- handler ----------

func TestHandler_ProxiesRoundRobinAndPreservesPath(t *testing.T) {
	var servers []*httptest.Server
	var bs []*Backend
	for i := 0; i < 3; i++ {
		s := tNamedServer(fmt.Sprintf("b%d", i))
		defer s.Close()
		servers = append(servers, s)
		bs = append(bs, tBackend(t, s.URL))
	}
	p := NewPool(Config{Picker: &RoundRobin{}}, bs...)
	front := httptest.NewServer(NewHandler(p, nil))
	defer front.Close()

	counts := map[string]int{}
	for i := 0; i < 6; i++ {
		code, body := tGet(t, front.URL+"/accounts/42?x=1")
		if code != http.StatusOK {
			t.Fatalf("status %d, want 200 (body %q)", code, body)
		}
		var name, path string
		fmt.Sscanf(body, "%s %s", &name, &path)
		if path != "/accounts/42?x=1" {
			t.Fatalf("backend saw %q, want path and query preserved", path)
		}
		counts[name]++
	}
	for i := 0; i < 3; i++ {
		if c := counts[fmt.Sprintf("b%d", i)]; c != 2 {
			t.Errorf("backend b%d served %d of 6 requests, want 2 (round robin)", i, c)
		}
	}
	for i, b := range bs {
		if b.InFlight() != 0 {
			t.Errorf("backend %d InFlight = %d after all requests finished; Acquire/Release are unbalanced", i, b.InFlight())
		}
	}
}

func TestHandler_KilledBackendIsEjectedAfterThreshold(t *testing.T) {
	var bs []*Backend
	var servers []*httptest.Server
	for i := 0; i < 3; i++ {
		s := tNamedServer(fmt.Sprintf("b%d", i))
		servers = append(servers, s)
		bs = append(bs, tBackend(t, s.URL))
	}
	defer servers[0].Close()
	defer servers[2].Close()
	servers[1].Close() // kill backend 1: connections are refused from now on

	p := NewPool(Config{Picker: &RoundRobin{}, FailureThreshold: 2, Cooldown: time.Hour}, bs...)
	front := httptest.NewServer(NewHandler(p, nil))
	defer front.Close()

	failures := 0
	for i := 0; i < 30; i++ {
		code, _ := tGet(t, front.URL+"/")
		switch code {
		case http.StatusOK:
		case http.StatusBadGateway:
			failures++
			if i >= 10 {
				t.Fatalf("request %d still hit the dead backend; it should have been ejected after 2 failures", i)
			}
		default:
			t.Fatalf("request %d: status %d, want 200 or 502", i, code)
		}
	}
	if failures != 2 {
		t.Errorf("clients saw %d 502s, want exactly 2 (threshold 2, then ejected)", failures)
	}
	if tContains(p.Healthy(), bs[1]) {
		t.Errorf("dead backend still in Healthy()")
	}
}

func TestHandler_Upstream5xxIsPassedThroughAndCountsAsFailure(t *testing.T) {
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "core banking timeout", http.StatusInternalServerError)
	}))
	defer bad.Close()
	b := tBackend(t, bad.URL)
	p := NewPool(Config{FailureThreshold: 2, Cooldown: time.Hour}, b)
	front := httptest.NewServer(NewHandler(p, nil))
	defer front.Close()

	for i := 0; i < 2; i++ {
		if code, _ := tGet(t, front.URL); code != http.StatusInternalServerError {
			t.Fatalf("request %d: status %d, want the upstream's 500 passed through", i, code)
		}
	}
	if code, _ := tGet(t, front.URL); code != http.StatusServiceUnavailable {
		t.Fatalf("status %d after 2 upstream 500s, want 503 (only backend ejected => no healthy backends)", code)
	}
}

func TestHandler_ClientCancelDoesNotEjectBackend(t *testing.T) {
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(2 * time.Second):
		}
	}))
	defer slow.Close()
	b := tBackend(t, slow.URL)
	p := NewPool(Config{FailureThreshold: 1, Cooldown: time.Hour}, b)
	front := httptest.NewServer(NewHandler(p, nil))
	defer front.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, front.URL, nil)
	if resp, err := tClient.Do(req); err == nil {
		resp.Body.Close()
		if resp.StatusCode == http.StatusNotImplemented {
			t.Fatalf("handler returned 501: not implemented")
		}
	}
	tEventually(t, 3*time.Second, func() bool { return b.InFlight() == 0 },
		"backend in-flight count never returned to 0 after the client gave up")
	// Give the proxy a moment to finish its bookkeeping after Release.
	time.Sleep(20 * time.Millisecond)
	if !tContains(p.Healthy(), b) {
		t.Fatalf("backend ejected because a *client* cancelled; with threshold 1 that would let impatient clients take a healthy backend out of rotation")
	}
}

func TestHandler_NoHealthyBackendsReturns503(t *testing.T) {
	b := tBackend(t, "http://127.0.0.1:1")
	p := NewPool(Config{FailureThreshold: 1, Cooldown: time.Hour}, b)
	p.ReportFailure(b)
	front := httptest.NewServer(NewHandler(p, nil))
	defer front.Close()
	if code, _ := tGet(t, front.URL); code != http.StatusServiceUnavailable {
		t.Fatalf("status %d, want 503 when no backend is eligible", code)
	}
}

func TestHandler_ConcurrentLoad(t *testing.T) {
	var bs []*Backend
	for i := 0; i < 3; i++ {
		s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			time.Sleep(time.Millisecond)
			io.WriteString(w, "ok")
		}))
		defer s.Close()
		bs = append(bs, tBackend(t, s.URL))
	}
	for _, picker := range []Picker{&RoundRobin{}, LeastConnections{}, PowerOfTwoChoices{}} {
		t.Run(fmt.Sprintf("%T", picker), func(t *testing.T) {
			p := NewPool(Config{Picker: picker}, bs...)
			front := httptest.NewServer(NewHandler(p, nil))
			defer front.Close()
			var wg sync.WaitGroup
			var bad atomic.Int64
			for g := 0; g < 16; g++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					for i := 0; i < 25; i++ {
						resp, err := tClient.Get(front.URL)
						if err != nil {
							bad.Add(1)
							continue
						}
						io.Copy(io.Discard, resp.Body)
						resp.Body.Close()
						if resp.StatusCode != http.StatusOK {
							bad.Add(1)
						}
					}
				}()
			}
			tWait(t, &wg, 30*time.Second, "concurrent proxied requests")
			if bad.Load() != 0 {
				t.Fatalf("%d of 400 concurrent requests failed; all backends are healthy", bad.Load())
			}
			for i, b := range bs {
				if b.InFlight() != 0 {
					t.Errorf("backend %d InFlight = %d after load; Acquire/Release leak", i, b.InFlight())
				}
			}
		})
	}
}
