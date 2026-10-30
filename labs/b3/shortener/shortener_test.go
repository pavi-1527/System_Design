package shortener

import (
	"context"
	"errors"
	"fmt"
	"math"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

const testTimeout = 5 * time.Second

var bg = context.Background()

// within fails the test if fn does not return in time, so a deadlock in your
// implementation is a message instead of a hung suite.
func within(t *testing.T, what string, fn func()) {
	t.Helper()
	done := make(chan struct{})
	go func() { defer close(done); fn() }()
	select {
	case <-done:
	case <-time.After(testTimeout):
		t.Fatalf("%s did not finish within %v (deadlock?)", what, testTimeout)
	}
}

// ---------------------------------------------------------------- fakes
//
// The tests bring their OWN implementations of every interface the Service
// depends on. If your Service reached for anything outside Config, or assumed
// anything about the implementations beyond the documented contract, these
// fakes would break it. That is the point of the problem.

var errBoom = errors.New("boom (injected)")

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func newClock() *fakeClock { return &fakeClock{t: time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)} }

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// scriptedIDs returns the given IDs in order, then fails.
type scriptedIDs struct {
	mu    sync.Mutex
	ids   []uint64
	calls int
}

var errIDsExhausted = errors.New("scripted ids exhausted")

func ids(v ...uint64) *scriptedIDs { return &scriptedIDs{ids: v} }

func (s *scriptedIDs) Next() (uint64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	if len(s.ids) == 0 {
		return 0, errIDsExhausted
	}
	id := s.ids[0]
	s.ids = s.ids[1:]
	return id, nil
}

func (s *scriptedIDs) Calls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

// prefixEncoder encodes 7 as "id-7": readable, and every code is also a valid alias.
type prefixEncoder string

func (p prefixEncoder) Encode(id uint64) string { return string(p) + strconv.FormatUint(id, 10) }

const enc = prefixEncoder("id-")

// fakeStore is a map-backed Store that counts calls, can inject errors, and can
// yield the processor at the start of Put and Get to widen race windows. Its
// errors are WRAPPED on purpose: a Service that compares with == is broken.
type fakeStore struct {
	mu      sync.Mutex
	recs    map[string]Record
	puts    int
	gets    int
	deletes int
	yield   bool
	failPut func(call int, rec Record) error
	failGet func(code string) error
}

func newStore() *fakeStore { return &fakeStore{recs: map[string]Record{}} }

func (f *fakeStore) Put(_ context.Context, rec Record) error {
	if f.yield {
		runtime.Gosched()
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.puts++
	if f.failPut != nil {
		if err := f.failPut(f.puts, rec); err != nil {
			return err
		}
	}
	if _, ok := f.recs[rec.Code]; ok {
		return fmt.Errorf("fake store: put %q: %w", rec.Code, ErrExists)
	}
	f.recs[rec.Code] = rec
	return nil
}

func (f *fakeStore) Get(_ context.Context, code string) (Record, error) {
	if f.yield {
		runtime.Gosched()
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.gets++
	if f.failGet != nil {
		if err := f.failGet(code); err != nil {
			return Record{}, err
		}
	}
	rec, ok := f.recs[code]
	if !ok {
		return Record{}, fmt.Errorf("fake store: get %q: %w", code, ErrNotFound)
	}
	return rec, nil
}

func (f *fakeStore) Delete(_ context.Context, code string) error {
	if f.yield {
		runtime.Gosched()
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deletes++
	if _, ok := f.recs[code]; !ok {
		return fmt.Errorf("fake store: delete %q: %w", code, ErrNotFound)
	}
	delete(f.recs, code)
	return nil
}

func (f *fakeStore) Len() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.recs)
}

func (f *fakeStore) seed(code string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.recs[code] = Record{Code: code, URL: "https://seeded.example/" + code}
}

func (f *fakeStore) calls() (puts, gets, deletes int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.puts, f.gets, f.deletes
}

func newSvc(t *testing.T, cfg Config) *Service {
	t.Helper()
	s, err := New(cfg)
	if err != nil {
		t.Fatalf("New(%+v) = %v, want nil", cfg, err)
	}
	if s == nil {
		t.Fatal("New returned a nil *Service with a nil error")
	}
	return s
}

func mustShorten(t *testing.T, s *Service, u string) string {
	t.Helper()
	code, err := s.Shorten(bg, u)
	if err != nil {
		t.Fatalf("Shorten(%q) = %v, want nil", u, err)
	}
	return code
}

func mustResolve(t *testing.T, s *Service, code, want string) {
	t.Helper()
	got, err := s.Resolve(bg, code)
	if err != nil {
		t.Fatalf("Resolve(%q) = %v, want %q", code, err, want)
	}
	if got != want {
		t.Fatalf("Resolve(%q) = %q, want %q", code, got, want)
	}
}

func wantErr(t *testing.T, what string, got, want error) {
	t.Helper()
	if !errors.Is(got, want) {
		t.Fatalf("%s = %v, want an error satisfying errors.Is(err, %v)", what, got, want)
	}
}

// ------------------------------------------------- the Store contract suite
//
// Anything that claims to be a Store must pass this. The tests run it against
// MemoryStore (Stage 1), ShardedStore (Stage 3) and the fake above.

// RunStoreContract checks the documented behaviour of a Store implementation.
func RunStoreContract(t *testing.T, newStore func() Store) {
	t.Helper()
	rec := func(code string) Record {
		return Record{Code: code, URL: "https://example.com/" + code, CreatedAt: time.Unix(1_700_000_000, 0).UTC(), ExpiresAt: time.Unix(1_800_000_000, 0).UTC()}
	}

	t.Run("PutThenGet", func(t *testing.T) {
		s := newStore()
		want := rec("abc")
		if err := s.Put(bg, want); err != nil {
			t.Fatalf("Put = %v", err)
		}
		got, err := s.Get(bg, "abc")
		if err != nil {
			t.Fatalf("Get = %v", err)
		}
		if got.Code != want.Code || got.URL != want.URL || !got.CreatedAt.Equal(want.CreatedAt) || !got.ExpiresAt.Equal(want.ExpiresAt) {
			t.Fatalf("Get = %+v, want %+v (every field must round-trip)", got, want)
		}
	})
	t.Run("GetMissingIsErrNotFound", func(t *testing.T) {
		_, err := newStore().Get(bg, "nope")
		wantErr(t, "Get of a missing code", err, ErrNotFound)
	})
	t.Run("PutIsCreateIfAbsent", func(t *testing.T) {
		s := newStore()
		first := rec("abc")
		if err := s.Put(bg, first); err != nil {
			t.Fatal(err)
		}
		second := rec("abc")
		second.URL = "https://other.example/"
		wantErr(t, "Put of an existing code", s.Put(bg, second), ErrExists)
		got, _ := s.Get(bg, "abc")
		if got.URL != first.URL {
			t.Fatalf("a rejected Put overwrote the record: URL = %q, want %q", got.URL, first.URL)
		}
	})
	t.Run("DeleteRemovesAndFreesTheCode", func(t *testing.T) {
		s := newStore()
		if err := s.Put(bg, rec("abc")); err != nil {
			t.Fatal(err)
		}
		if err := s.Delete(bg, "abc"); err != nil {
			t.Fatalf("Delete = %v", err)
		}
		_, err := s.Get(bg, "abc")
		wantErr(t, "Get after Delete", err, ErrNotFound)
		wantErr(t, "second Delete", s.Delete(bg, "abc"), ErrNotFound)
		if err := s.Put(bg, rec("abc")); err != nil {
			t.Fatalf("Put after Delete = %v, want nil: the code is free again", err)
		}
	})
	t.Run("ConcurrentPutOfOneCodeHasExactlyOneWinner", func(t *testing.T) {
		s := newStore()
		const n = 32
		var wg sync.WaitGroup
		errs := make([]error, n)
		for i := range n {
			wg.Add(1)
			go func() {
				defer wg.Done()
				r := rec("same")
				r.URL = "https://example.com/" + strconv.Itoa(i)
				errs[i] = s.Put(bg, r)
			}()
		}
		within(t, "concurrent Puts", wg.Wait)
		winners := 0
		for _, err := range errs {
			switch {
			case err == nil:
				winners++
			case !errors.Is(err, ErrExists):
				t.Fatalf("Put = %v, want nil or ErrExists", err)
			}
		}
		if winners != 1 {
			t.Fatalf("%d concurrent Puts of one code succeeded, want exactly 1 (create-if-absent must be atomic)", winners)
		}
	})
	t.Run("ConcurrentDistinctPutsAndGets", func(t *testing.T) {
		s := newStore()
		var wg sync.WaitGroup
		for g := range 8 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for i := range 100 {
					code := fmt.Sprintf("c-%d-%d", g, i)
					if err := s.Put(bg, rec(code)); err != nil {
						t.Errorf("Put(%q) = %v", code, err)
						return
					}
					if got, err := s.Get(bg, code); err != nil || got.Code != code {
						t.Errorf("Get(%q) = %+v, %v", code, got, err)
						return
					}
				}
			}()
		}
		within(t, "concurrent Puts and Gets", wg.Wait)
	})
}

// ---------------------------------------------------------------- Stage 1

func TestStage1_Base62(t *testing.T) {
	var b Base62
	cases := map[uint64]string{0: "0", 9: "9", 10: "a", 35: "z", 36: "A", 61: "Z", 62: "10", 3843: "ZZ", 3844: "100"}
	for id, want := range cases {
		if got := b.Encode(id); got != want {
			t.Errorf("Encode(%d) = %q, want %q (alphabet is 0-9, a-z, A-Z; most significant digit first)", id, got, want)
		}
	}
	for _, id := range []uint64{0, 1, 61, 62, 12345, 1 << 32, 1<<63 + 12345, math.MaxUint64} {
		code := b.Encode(id)
		got, err := b.Decode(code)
		if err != nil || got != id {
			t.Errorf("Decode(Encode(%d)) = %d, %v (code %q)", id, got, err, code)
		}
	}
	if got := len(b.Encode(math.MaxUint64)); got != 11 {
		t.Errorf("len(Encode(MaxUint64)) = %d, want 11", got)
	}
	for _, bad := range []string{"", "-", "a b", "é", strings.Repeat("Z", 12)} {
		if _, err := b.Decode(bad); err == nil {
			t.Errorf("Decode(%q) = nil error, want an error (empty, outside the alphabet, or overflow)", bad)
		}
	}
}

func TestStage1_CounterIsSequentialAndConcurrencySafe(t *testing.T) {
	c := NewCounter(100)
	for want := uint64(100); want < 103; want++ {
		if got, err := c.Next(); err != nil || got != want {
			t.Fatalf("Next = %d, %v, want %d", got, err, want)
		}
	}
	c = NewCounter(0)
	seen := make([]bool, 8000)
	var mu sync.Mutex
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 1000 {
				id, _ := c.Next()
				mu.Lock()
				if id >= uint64(len(seen)) || seen[id] {
					t.Errorf("id %d handed out twice or out of range", id)
				}
				if id < uint64(len(seen)) {
					seen[id] = true
				}
				mu.Unlock()
			}
		}()
	}
	within(t, "concurrent Next", wg.Wait)
}

func TestStage1_MemoryStorePassesTheContract(t *testing.T) {
	RunStoreContract(t, func() Store { return NewMemoryStore() })
}

func TestStage1_TheContractSuiteAcceptsTheTestFake(t *testing.T) {
	// If this fails the suite, not your code, is wrong; it validates the harness.
	RunStoreContract(t, func() Store { return newStore() })
}

func TestStage1_ShortenResolveRoundTripUsesTheInjectedParts(t *testing.T) {
	st := newStore()
	clk := newClock()
	s := newSvc(t, Config{Store: st, IDs: ids(5), Encoder: enc, Now: clk.Now})
	code := mustShorten(t, s, "https://example.com/a/b?x=1")
	if code != "id-5" {
		t.Fatalf("code = %q, want %q: the code must be Encoder.Encode(IDGenerator.Next())", code, "id-5")
	}
	mustResolve(t, s, code, "https://example.com/a/b?x=1")
	st.mu.Lock()
	rec, ok := st.recs["id-5"]
	st.mu.Unlock()
	if !ok {
		t.Fatal("the record was not written through the injected Store")
	}
	if rec.URL != "https://example.com/a/b?x=1" || !rec.CreatedAt.Equal(clk.Now()) || !rec.ExpiresAt.IsZero() {
		t.Fatalf("record = %+v, want the URL, CreatedAt = the injected clock, and no expiry", rec)
	}
}

func TestStage1_DefaultsWork(t *testing.T) {
	s := newSvc(t, Config{})
	a := mustShorten(t, s, "https://example.com/a")
	b := mustShorten(t, s, "https://example.com/b")
	if a == b || a == "" || b == "" {
		t.Fatalf("codes %q and %q: want two distinct non-empty codes from the default counter", a, b)
	}
	mustResolve(t, s, a, "https://example.com/a")
	mustResolve(t, s, b, "https://example.com/b")
}

func TestStage1_ConfigValidation(t *testing.T) {
	if _, err := New(Config{MaxURLLength: -1}); err == nil {
		t.Error("New with a negative MaxURLLength succeeded, want an error")
	}
	if _, err := New(Config{MaxAttempts: -1}); err == nil {
		t.Error("New with a negative MaxAttempts succeeded, want an error")
	}
}

func TestStage1_URLValidation(t *testing.T) {
	s := newSvc(t, Config{})
	valid := []string{
		"https://example.com", "http://example.com/path?x=1#frag", "https://sub.example.co.uk:8443/a/b",
		"  https://example.com/trim  ", "HTTPS://EXAMPLE.com", "http://[::1]:8080/x", "https://example.com/a%20b",
	}
	for _, u := range valid {
		if _, err := s.Shorten(bg, u); err != nil {
			t.Errorf("Shorten(%q) = %v, want nil", u, err)
		}
	}
	invalid := map[string]string{
		"":                                      "empty",
		"   ":                                   "blank",
		"example.com":                           "no scheme",
		"//example.com/x":                       "scheme-relative",
		"ftp://example.com/f":                   "scheme not http(s)",
		"javascript:alert(1)":                   "javascript:",
		"data:text/html;base64,AAAA":            "data:",
		"file:///etc/passwd":                    "file:",
		"mailto:a@b.c":                          "mailto:",
		"http://":                               "no host",
		"http:///path":                          "empty host",
		"https:example.com":                     "opaque, no host",
		"https://user:pw@example.com/":          "user info",
		"https://mybank.com@evil.example/login": "user-info trick: looks like mybank.com",
		"http://exa mple.com":                   "space in host",
		"https://example.com/a b":               "space in path",
		"https://example.com/\x00":              "NUL byte",
		"https://example.com/a\nb":              "embedded control character",
		"http://example.com:notaport/":          "bad port",
	}
	for u, why := range invalid {
		code, err := s.Shorten(bg, u)
		if !errors.Is(err, ErrInvalidURL) {
			t.Errorf("Shorten(%q) = %q, %v; want ErrInvalidURL (%s)", u, code, err, why)
		}
	}
}

func TestStage1_URLLengthLimit(t *testing.T) {
	s := newSvc(t, Config{})
	base := "https://example.com/"
	atLimit := base + strings.Repeat("a", 2048-len(base))
	if _, err := s.Shorten(bg, atLimit); err != nil {
		t.Fatalf("Shorten(2048-byte URL) = %v, want nil: the default limit is inclusive", err)
	}
	_, err := s.Shorten(bg, atLimit+"a")
	wantErr(t, "Shorten of a 2049-byte URL", err, ErrInvalidURL)

	small := newSvc(t, Config{MaxURLLength: 30})
	if _, err := small.Shorten(bg, "https://example.com/"+strings.Repeat("a", 11)); !errors.Is(err, ErrInvalidURL) {
		t.Fatalf("31-byte URL with MaxURLLength 30 = %v, want ErrInvalidURL", err)
	}
}

func TestStage1_NormalizationIsAStatedPolicy(t *testing.T) {
	cases := []struct{ in, want string }{
		{"https://example.com", "https://example.com/"},
		{"HTTPS://Example.COM:443/Path?q=1#frag", "https://example.com/Path?q=1#frag"},
		{"http://example.com:80", "http://example.com/"},
		{"http://example.com:8080/x", "http://example.com:8080/x"},
		{"https://example.com:80/x", "https://example.com:80/x"},
		{"  https://example.com/trim  ", "https://example.com/trim"},
		{"http://[::1]:8080/x", "http://[::1]:8080/x"},
		{"https://example.com/a%20b", "https://example.com/a%20b"},
	}
	for _, c := range cases {
		s := newSvc(t, Config{})
		code, err := s.Shorten(bg, c.in)
		if err != nil {
			t.Errorf("Shorten(%q) = %v", c.in, err)
			continue
		}
		mustResolve(t, s, code, c.want)
	}
}

func TestStage1_SameURLSameCodeAndNoWastedWork(t *testing.T) {
	st := newStore()
	gen := ids(1, 2, 3)
	s := newSvc(t, Config{Store: st, IDs: gen, Encoder: enc})
	a := mustShorten(t, s, "https://example.com/a")
	again := mustShorten(t, s, "https://example.com/a")
	equiv := mustShorten(t, s, "HTTPS://EXAMPLE.com:443/a")
	if a != again || a != equiv {
		t.Fatalf("codes %q, %q, %q: the same (normalized) URL must always get the same code", a, again, equiv)
	}
	if puts, _, _ := st.calls(); puts != 1 || gen.Calls() != 1 {
		t.Fatalf("%d Puts and %d IDs consumed for three Shortens of one URL, want 1 and 1: a repeat must not mint and discard a code", puts, gen.Calls())
	}
	b := mustShorten(t, s, "https://example.com/b")
	if b == a {
		t.Fatal("two different URLs got the same code")
	}
	mustResolve(t, s, a, "https://example.com/a")
	mustResolve(t, s, b, "https://example.com/b")
}

func TestStage1_UnknownCodeIsErrNotFound(t *testing.T) {
	s := newSvc(t, Config{Store: newStore()})
	for _, code := range []string{"nope", "", "id-1"} {
		_, err := s.Resolve(bg, code)
		wantErr(t, fmt.Sprintf("Resolve(%q)", code), err, ErrNotFound)
	}
}

func TestStage1_StoreErrorsAreNotSwallowedOrRelabelled(t *testing.T) {
	st := newStore()
	st.failPut = func(call int, _ Record) error {
		if call == 1 {
			return errBoom
		}
		return nil
	}
	s := newSvc(t, Config{Store: st, IDs: ids(1, 2, 3), Encoder: enc})

	_, err := s.Shorten(bg, "https://example.com/a")
	wantErr(t, "Shorten while the store fails", err, errBoom)
	if st.Len() != 0 {
		t.Fatalf("store holds %d records after a failed Put, want 0", st.Len())
	}
	// The failure must leave no half-created state: the retry works and resolves.
	code := mustShorten(t, s, "https://example.com/a")
	mustResolve(t, s, code, "https://example.com/a")

	st.failGet = func(c string) error {
		if c == code {
			return errBoom
		}
		return nil
	}
	_, err = s.Resolve(bg, code)
	wantErr(t, "Resolve while the store fails", err, errBoom)
	if errors.Is(err, ErrNotFound) {
		t.Fatal("a store failure was reported as ErrNotFound: callers would cache a 404 for a link that exists")
	}
}

func TestStage1_ContextIsHonouredBeforeTouchingTheStore(t *testing.T) {
	st := newStore()
	s := newSvc(t, Config{Store: st, IDs: ids(1, 2)})
	code := mustShorten(t, s, "https://example.com/a")
	ctx, cancel := context.WithCancel(bg)
	cancel()
	puts, gets, _ := st.calls()
	if _, err := s.Shorten(ctx, "https://example.com/b"); !errors.Is(err, context.Canceled) {
		t.Fatalf("Shorten with a cancelled context = %v, want context.Canceled", err)
	}
	if _, err := s.Resolve(ctx, code); !errors.Is(err, context.Canceled) {
		t.Fatalf("Resolve with a cancelled context = %v, want context.Canceled", err)
	}
	if p, g, _ := st.calls(); p != puts || g != gets {
		t.Fatalf("the store was called (%d Puts, %d Gets more) with an already-cancelled context", p-puts, g-gets)
	}
}
