package shortener

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func alias(t *testing.T, s *Service, url, a string, ttl time.Duration) (string, error) {
	t.Helper()
	return s.ShortenWith(bg, url, ShortenOptions{Alias: a, TTL: ttl})
}

func TestStage2_CustomAlias(t *testing.T) {
	s := newSvc(t, Config{})
	code, err := alias(t, s, "https://example.com/promo", "promo_2026", 0)
	if err != nil || code != "promo_2026" {
		t.Fatalf("ShortenWith alias = %q, %v, want %q", code, err, "promo_2026")
	}
	mustResolve(t, s, "promo_2026", "https://example.com/promo")
}

func TestStage2_AliasValidation(t *testing.T) {
	st := newStore()
	s := newSvc(t, Config{Store: st})
	bad := []string{"ab", strings.Repeat("a", 33), "has space", "bad/char", "ünï", "a.b", "api", "ADMIN", "Login", "static", "health", "logout"}
	for _, a := range bad {
		_, err := alias(t, s, "https://example.com/x", a, 0)
		if !errors.Is(err, ErrInvalidAlias) {
			t.Errorf("alias %q = %v, want ErrInvalidAlias", a, err)
		}
	}
	if puts, _, _ := st.calls(); puts != 0 {
		t.Errorf("%d Puts for invalid aliases, want 0: validate before touching the store", puts)
	}
	for _, a := range []string{"abc", "promo_2026", "A-b_9", strings.Repeat("z", 32)} {
		if _, err := alias(t, s, "https://example.com/x", a, 0); err != nil {
			t.Errorf("alias %q = %v, want nil", a, err)
		}
	}
	_, err := alias(t, s, "javascript:alert(1)", "fine-alias", 0)
	wantErr(t, "valid alias, invalid URL", err, ErrInvalidURL)
}

func TestStage2_AliasTaken(t *testing.T) {
	s := newSvc(t, Config{})
	if _, err := alias(t, s, "https://example.com/one", "promo", 0); err != nil {
		t.Fatal(err)
	}
	_, err := alias(t, s, "https://example.com/two", "promo", 0)
	wantErr(t, "claiming a taken alias", err, ErrAliasTaken)
	_, err = alias(t, s, "https://example.com/one", "promo", 0)
	wantErr(t, "claiming a taken alias with the same URL", err, ErrAliasTaken)
	mustResolve(t, s, "promo", "https://example.com/one")
}

func TestStage2_GeneratedCodeSkipsAnAliasThatTookItsName(t *testing.T) {
	st := newStore()
	gen := ids(1, 2)
	s := newSvc(t, Config{Store: st, IDs: gen, Encoder: enc})
	if _, err := alias(t, s, "https://example.com/vanity", "id-1", 0); err != nil {
		t.Fatal(err)
	}
	code := mustShorten(t, s, "https://example.com/plain")
	if code != "id-2" {
		t.Fatalf("code = %q, want id-2: id-1 is taken by an alias, so the generator's first value collides and must be retried", code)
	}
	mustResolve(t, s, "id-1", "https://example.com/vanity")
	mustResolve(t, s, "id-2", "https://example.com/plain")
}

func TestStage2_CollisionRetryIsBounded(t *testing.T) {
	st := newStore()
	st.seed("id-7")
	gen := ids(7, 7, 7, 9)
	s := newSvc(t, Config{Store: st, IDs: gen, Encoder: enc, MaxAttempts: 3})
	_, err := s.Shorten(bg, "https://example.com/a")
	wantErr(t, "Shorten with 3 consecutive collisions and MaxAttempts 3", err, ErrCollisionExhausted)
	if puts, _, _ := st.calls(); puts != 3 {
		t.Fatalf("%d Put attempts, want exactly MaxAttempts = 3", puts)
	}
	// Exhaustion must leave nothing behind: the next call just works.
	code := mustShorten(t, s, "https://example.com/a")
	if code != "id-9" {
		t.Fatalf("code = %q after exhaustion, want id-9", code)
	}
}

func TestStage2_CollisionRetrySucceedsWithinTheDefaultBudget(t *testing.T) {
	st := newStore()
	st.seed("id-7")
	s := newSvc(t, Config{Store: st, IDs: ids(7, 7, 7, 8), Encoder: enc})
	code := mustShorten(t, s, "https://example.com/a")
	if code != "id-8" {
		t.Fatalf("code = %q, want id-8 (default MaxAttempts is 5)", code)
	}
	if puts, _, _ := st.calls(); puts != 4 {
		t.Fatalf("%d Puts, want 4 (three collisions then success)", puts)
	}
}

func TestStage2_GeneratorFailureIsReturned(t *testing.T) {
	s := newSvc(t, Config{Store: newStore(), IDs: ids(), Encoder: enc})
	_, err := s.Shorten(bg, "https://example.com/a")
	wantErr(t, "Shorten with an exhausted generator", err, errIDsExhausted)
}

func TestStage2_TTLExpiryIsExactAndLazy(t *testing.T) {
	clk := newClock()
	s := newSvc(t, Config{Now: clk.Now})
	t0 := clk.Now()
	code, err := alias(t, s, "https://example.com/flash", "flash", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	clk.Advance(time.Hour - time.Nanosecond)
	mustResolve(t, s, code, "https://example.com/flash")
	st, err := s.Stats(bg, code)
	if err != nil || !st.CreatedAt.Equal(t0) || !st.ExpiresAt.Equal(t0.Add(time.Hour)) {
		t.Fatalf("Stats = %+v, %v; want CreatedAt = t0 and ExpiresAt = t0 + 1h", st, err)
	}
	clk.Advance(time.Nanosecond) // now == ExpiresAt: expired
	_, err = s.Resolve(bg, code)
	wantErr(t, "Resolve at exactly ExpiresAt", err, ErrNotFound)
	_, err = s.Stats(bg, code)
	wantErr(t, "Stats of an expired link", err, ErrNotFound)
}

func TestStage2_TTLValidation(t *testing.T) {
	st := newStore()
	s := newSvc(t, Config{Store: st})
	_, err := s.ShortenWith(bg, "https://example.com/a", ShortenOptions{TTL: -time.Second})
	wantErr(t, "negative TTL", err, ErrInvalidTTL)
	if puts, _, _ := st.calls(); puts != 0 {
		t.Fatalf("%d Puts for an invalid TTL, want 0", puts)
	}
	code, err := s.ShortenWith(bg, "https://example.com/a", ShortenOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if rec := func() Record { st.mu.Lock(); defer st.mu.Unlock(); return st.recs[code] }(); !rec.ExpiresAt.IsZero() {
		t.Fatalf("ExpiresAt = %v for TTL 0, want the zero time (never expires)", rec.ExpiresAt)
	}
}

func TestStage2_OptionsCreateDistinctLinksOutsideTheDedupeIndex(t *testing.T) {
	clk := newClock()
	s := newSvc(t, Config{IDs: ids(1, 2, 3, 4, 5), Encoder: enc, Now: clk.Now})
	const u = "https://example.com/same"
	temp := mustTTL(t, s, u, time.Hour)
	plain := mustShorten(t, s, u)
	if plain == temp {
		t.Fatalf("plain Shorten returned the TTL link %q: a TTL link is not the URL's canonical short link", temp)
	}
	if again := mustShorten(t, s, u); again != plain {
		t.Fatalf("plain Shorten twice gave %q then %q", plain, again)
	}
	if other := mustTTL(t, s, u, time.Hour); other == temp || other == plain {
		t.Fatalf("second TTL link = %q: every call with options creates a new link", other)
	}
	clk.Advance(2 * time.Hour)
	mustResolve(t, s, plain, u) // the permanent link outlives the TTL ones
	if again := mustShorten(t, s, u); again != plain {
		t.Fatalf("plain Shorten after the TTL links expired = %q, want %q", again, plain)
	}
}

func mustTTL(t *testing.T, s *Service, u string, ttl time.Duration) string {
	t.Helper()
	code, err := s.ShortenWith(bg, u, ShortenOptions{TTL: ttl})
	if err != nil {
		t.Fatalf("ShortenWith(TTL %v) = %v", ttl, err)
	}
	return code
}

func TestStage2_AnExpiredAliasCanBeReclaimedALiveOneCannot(t *testing.T) {
	clk := newClock()
	s := newSvc(t, Config{Now: clk.Now})
	if _, err := alias(t, s, "https://example.com/old", "summer", time.Hour); err != nil {
		t.Fatal(err)
	}
	mustResolve(t, s, "summer", "https://example.com/old")
	mustResolve(t, s, "summer", "https://example.com/old")
	clk.Advance(time.Hour - time.Second)
	_, err := alias(t, s, "https://example.com/new", "summer", 0)
	wantErr(t, "claiming a live alias", err, ErrAliasTaken)

	clk.Advance(time.Second) // expired
	if _, err := alias(t, s, "https://example.com/new", "summer", 0); err != nil {
		t.Fatalf("claiming an expired alias = %v, want nil", err)
	}
	mustResolve(t, s, "summer", "https://example.com/new")
	st, err := s.Stats(bg, "summer")
	if err != nil || st.Clicks != 1 { // the Resolve just above; the old link's two clicks must not carry over
		t.Fatalf("Stats = %+v, %v; want Clicks = 1 (a reclaimed alias starts a new link)", st, err)
	}
}

func TestStage2_ClickCounting(t *testing.T) {
	clk := newClock()
	s := newSvc(t, Config{Now: clk.Now})
	code := mustShorten(t, s, "https://example.com/a")
	st, err := s.Stats(bg, code)
	if err != nil || st.Clicks != 0 || st.URL != "https://example.com/a" || !st.CreatedAt.Equal(clk.Now()) || !st.ExpiresAt.IsZero() {
		t.Fatalf("fresh Stats = %+v, %v", st, err)
	}
	for range 3 {
		mustResolve(t, s, code, "https://example.com/a")
	}
	_, _ = s.Resolve(bg, "missing")
	if st, _ := s.Stats(bg, code); st.Clicks != 3 {
		t.Fatalf("Clicks = %d, want 3 (failed resolves must not count)", st.Clicks)
	}
	_, err = s.Stats(bg, "missing")
	wantErr(t, "Stats of an unknown code", err, ErrNotFound)

	ttlCode := mustTTL(t, s, "https://example.com/b", time.Minute)
	mustResolve(t, s, ttlCode, "https://example.com/b")
	clk.Advance(time.Minute)
	_, _ = s.Resolve(bg, ttlCode)
	if _, err := s.Stats(bg, ttlCode); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Stats after expiry = %v, want ErrNotFound", err)
	}
}

func TestStage2_DeleteRemovesTheLinkAndItsCounter(t *testing.T) {
	s := newSvc(t, Config{})
	if _, err := alias(t, s, "https://example.com/a", "promo", 0); err != nil {
		t.Fatal(err)
	}
	mustResolve(t, s, "promo", "https://example.com/a")
	if err := s.Delete(bg, "promo"); err != nil {
		t.Fatalf("Delete = %v", err)
	}
	_, err := s.Resolve(bg, "promo")
	wantErr(t, "Resolve after Delete", err, ErrNotFound)
	wantErr(t, "second Delete", s.Delete(bg, "promo"), ErrNotFound)
	wantErr(t, "Delete of an unknown code", s.Delete(bg, "nope"), ErrNotFound)
	wantErr(t, "Delete of the empty code", s.Delete(bg, ""), ErrNotFound)

	if _, err := alias(t, s, "https://example.com/b", "promo", 0); err != nil {
		t.Fatalf("re-claiming a deleted alias = %v, want nil", err)
	}
	if st, err := s.Stats(bg, "promo"); err != nil || st.Clicks != 0 || st.URL != "https://example.com/b" {
		t.Fatalf("Stats = %+v, %v; want a fresh link with 0 clicks", st, err)
	}
}

func TestStage2_ShortenAfterDeleteMintsAWorkingCode(t *testing.T) {
	// The dedupe index remembers URL -> code. After Delete that memory is stale,
	// and trusting it hands out a dead link.
	s := newSvc(t, Config{IDs: ids(1, 2, 3), Encoder: enc})
	const u = "https://example.com/a"
	first := mustShorten(t, s, u)
	if err := s.Delete(bg, first); err != nil {
		t.Fatal(err)
	}
	second := mustShorten(t, s, u)
	if second == first {
		t.Fatalf("Shorten returned the deleted code %q", first)
	}
	mustResolve(t, s, second, u)
}

func TestStage2_StaleIndexIsCheckedAgainstTheStoreNotTrusted(t *testing.T) {
	// u1 -> id-1; delete it; a *different* URL takes the name id-1 as an alias.
	// Shorten(u1) must not answer id-1: that code now resolves to u2.
	s := newSvc(t, Config{IDs: ids(1, 2, 3), Encoder: enc})
	const u1, u2 = "https://example.com/one", "https://example.com/two"
	first := mustShorten(t, s, u1)
	if err := s.Delete(bg, first); err != nil {
		t.Fatal(err)
	}
	if _, err := alias(t, s, u2, first, 0); err != nil {
		t.Fatalf("claiming the freed name = %v", err)
	}
	code := mustShorten(t, s, u1)
	mustResolve(t, s, code, u1)
	mustResolve(t, s, first, u2)
}

func TestStage2_RandomGenerator(t *testing.T) {
	r, err := NewRandom(func() uint64 { return ^uint64(0) }, 20)
	if err != nil {
		t.Fatal(err)
	}
	if id, _ := r.Next(); id != 1<<20-1 {
		t.Fatalf("Next = %d, want %d: values are masked to the requested bit width", id, 1<<20-1)
	}
	if r, err = NewRandom(func() uint64 { return 5 }, 63); err != nil {
		t.Fatal(err)
	}
	if id, _ := r.Next(); id != 5 {
		t.Fatalf("Next = %d, want 5", id)
	}
	for _, bits := range []uint{0, 64, 100} {
		if _, err := NewRandom(nil, bits); err == nil {
			t.Errorf("NewRandom(nil, %d) succeeded, want an error (bits must be in [1, 63])", bits)
		}
	}
	d, err := NewRandom(nil, 40)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[uint64]bool{}
	for range 200 {
		id, _ := d.Next()
		if id >= 1<<40 {
			t.Fatalf("id %d does not fit in 40 bits", id)
		}
		seen[id] = true
	}
	if len(seen) < 190 {
		t.Fatalf("only %d distinct values in 200 draws: the default source is not random", len(seen))
	}
}

func TestStage2_RandomStrategyPlugsIntoTheServiceAndRetriesCollisions(t *testing.T) {
	draws := []uint64{3, 3, 4}
	next := func() uint64 { v := draws[0]; draws = draws[1:]; return v }
	r, err := NewRandom(next, 8)
	if err != nil {
		t.Fatal(err)
	}
	s := newSvc(t, Config{IDs: r, Encoder: enc})
	a := mustShorten(t, s, "https://example.com/a")
	b := mustShorten(t, s, "https://example.com/b") // draws 3 (taken), then 4
	if a != "id-3" || b != "id-4" {
		t.Fatalf("codes = %q, %q, want id-3 and id-4", a, b)
	}
}
