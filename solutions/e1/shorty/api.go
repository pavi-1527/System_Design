package shorty

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"sync/atomic"
	"time"
)

// Link is the value stored under "link/<code>" (JSON). Provided.
type Link struct {
	Code      string    `json:"code"`
	URL       string    `json:"url"`
	Owner     string    `json:"owner"`
	CreatedAt time.Time `json:"created_at"`
	ExpiresAt time.Time `json:"expires_at"` // zero: never expires
	Deleted   bool      `json:"deleted,omitempty"`
}

// expired reports whether the link has a set expiry at or before now.
func (l Link) expired(now time.Time) bool { return !l.ExpiresAt.IsZero() && !now.Before(l.ExpiresAt) }

// idemRecord is the value stored under "idem/<client>/<key>" (JSON): the
// fingerprint of the first request that used the key and the link it
// promised. Whoever holds the record can finish the work. Provided.
type idemRecord struct {
	Fingerprint string `json:"fingerprint"`
	Link        Link   `json:"link"`
}

// createRequest is the body of POST /v1/links. Provided.
type createRequest struct {
	URL        string `json:"url"`
	TTLSeconds int64  `json:"ttl_seconds"`
}

// createResponse is the body of a 201 from POST /v1/links. Provided.
type createResponse struct {
	Code      string     `json:"code"`
	ShortURL  string     `json:"short_url"`
	URL       string     `json:"url"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
}

// cached is what the redirect cache holds per code: the answer, not the row.
// Status is 302 (found), 404 (never existed) or 410 (deleted). Provided.
type cached struct {
	Status int
	Link   Link
}

// errCodeTaken means the code is already used by a different link.
var errCodeTaken = errors.New("code already taken")

const maxTTLSeconds = 10 * 365 * 24 * 3600

// api is the api role: handlers plus the cache, limiter and store client.
type api struct {
	cfg     APIConfig
	store   *StoreClient
	cache   *Cache[cached]
	flight  Group[cached]
	limiter *TokenBucket
	now     func() time.Time
	stats   struct {
		hits, staleServed, lookups, coalesced, storeErrors, rateLimited, created, replayed atomic.Int64
	}
}

// runAPI is the api role. Provided.
func runAPI(ctx context.Context, cfg APIConfig, stdout io.Writer) error {
	a, err := newAPI(cfg)
	if err != nil {
		return err
	}
	return serve(ctx, cfg.Addr, a.routes(), cfg.Drain, stdout)
}

func newAPI(cfg APIConfig) (*api, error) {
	return &api{
		cfg:     cfg,
		store:   NewStoreClient(cfg.StoreURL),
		cache:   NewCache[cached](cfg.CacheSize, nil),
		limiter: NewTokenBucket(cfg.CreateRate, cfg.CreateBurst, cfg.MaxClients, nil),
		now:     time.Now,
	}, nil
}

// routes (spec.md "API"):
//
//	POST   /v1/links          create; optional Idempotency-Key
//	DELETE /v1/links/{code}   owner only; 204, or 404 for anyone else
//	GET    /{code}            302 | 404 | 410, X-Cache: HIT | MISS | STALE
//	GET    /-/stats, /-/healthz
func (a *api) routes() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/links", a.create)
	mux.HandleFunc("DELETE /v1/links/{code}", a.remove)
	mux.HandleFunc("GET /{code}", a.redirect)
	mux.HandleFunc("GET /-/stats", a.statsHandler)
	mux.HandleFunc("GET /-/healthz", func(w http.ResponseWriter, _ *http.Request) { io.WriteString(w, "ok\n") })
	return mux
}

// clientID identifies the caller for rate limiting, idempotency scope and
// ownership: the X-Client-ID header, else the remote IP. (A real deployment
// takes it from an authenticated token at the edge; trusting a header is fine
// only because nothing but loopback can reach this process.) Provided.
func clientID(r *http.Request) (string, bool) {
	if id := r.Header.Get("X-Client-ID"); id != "" {
		return id, validToken(id, 64, true)
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr, true
	}
	return host, true
}

// validToken accepts 1..max characters from [A-Za-z0-9_-] (plus '.' and ':'
// when dots is true). Provided.
func validToken(s string, max int, dots bool) bool {
	if s == "" || len(s) > max {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case '0' <= c && c <= '9', 'A' <= c && c <= 'Z', 'a' <= c && c <= 'z', c == '_', c == '-':
		case dots && (c == '.' || c == ':'):
		default:
			return false
		}
	}
	return true
}

// apiError writes {"error": code} with status. Provided.
func apiError(w http.ResponseWriter, status int, code string) {
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, status, map[string]string{"error": code})
}

// unavailable is the answer when the store is failing: fast, explicit, and
// telling the client when to come back. Provided.
func unavailable(w http.ResponseWriter) {
	w.Header().Set("Retry-After", "1")
	apiError(w, http.StatusServiceUnavailable, "store_unavailable")
}

func (a *api) create(w http.ResponseWriter, r *http.Request) {
	client, ok := clientID(r)
	if !ok {
		apiError(w, http.StatusBadRequest, "bad_client_id")
		return
	}
	// Shed before doing any work: a refused request must be cheap.
	if ok, wait := a.limiter.Allow(client); !ok {
		a.stats.rateLimited.Add(1)
		w.Header().Set("Retry-After", strconv.Itoa(RetryAfterSeconds(wait)))
		apiError(w, http.StatusTooManyRequests, "rate_limited")
		return
	}
	var req createRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10))
	if err := dec.Decode(&req); err != nil {
		apiError(w, http.StatusBadRequest, "bad_json")
		return
	}
	if !validTarget(req.URL) {
		apiError(w, http.StatusBadRequest, "bad_url")
		return
	}
	if req.TTLSeconds < 0 || req.TTLSeconds > maxTTLSeconds {
		apiError(w, http.StatusBadRequest, "bad_ttl")
		return
	}
	idemKey := r.Header.Get("Idempotency-Key")
	if idemKey != "" && !validToken(idemKey, 128, false) {
		apiError(w, http.StatusBadRequest, "bad_idempotency_key")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), a.cfg.RequestBudget)
	defer cancel()
	now := a.now().UTC()
	link := Link{Code: NewCode(a.cfg.CodeLen), URL: req.URL, Owner: client, CreatedAt: now}
	if req.TTLSeconds > 0 {
		link.ExpiresAt = now.Add(time.Duration(req.TTLSeconds) * time.Second)
	}

	var replayed bool
	var err error
	if idemKey == "" {
		link, err = a.insertNew(ctx, link)
	} else {
		link, replayed, err = a.insertIdempotent(ctx, client, idemKey, fingerprint(req), link)
	}
	switch {
	case errors.Is(err, errFingerprint):
		apiError(w, http.StatusUnprocessableEntity, "idempotency_key_reused")
		return
	case err != nil:
		a.stats.storeErrors.Add(1)
		unavailable(w)
		return
	}
	if replayed {
		// Don't warm the cache from a replay: the link may have been deleted
		// since the first response.
		a.stats.replayed.Add(1)
		w.Header().Set("Idempotent-Replayed", "true")
	} else {
		// Warm the cache: a new link is usually clicked within seconds.
		a.stats.created.Add(1)
		a.cache.Set(link.Code, cached{Status: http.StatusFound, Link: link}, a.cfg.CacheTTL, a.cfg.StaleIfError)
	}
	resp := createResponse{Code: link.Code, ShortURL: "http://" + r.Host + "/" + link.Code, URL: link.URL}
	if !link.ExpiresAt.IsZero() {
		t := link.ExpiresAt
		resp.ExpiresAt = &t
	}
	writeJSON(w, http.StatusCreated, resp)
}

var errFingerprint = errors.New("idempotency key reused with a different request")

func fingerprint(req createRequest) string {
	h := sha256.Sum256([]byte(fmt.Sprintf("%d|%s", req.TTLSeconds, req.URL)))
	return hex.EncodeToString(h[:])
}

// validTarget accepts absolute http(s) URLs with a host, up to 2 KiB. Provided.
func validTarget(s string) bool {
	if len(s) == 0 || len(s) > 2048 {
		return false
	}
	u, err := url.Parse(s)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != ""
}

// insertNew writes link under a fresh code, retrying with a new code on the
// (astronomically rare, but possible) collision.
func (a *api) insertNew(ctx context.Context, link Link) (Link, error) {
	for attempt := 0; attempt < 5; attempt++ {
		if attempt > 0 {
			link.Code = NewCode(a.cfg.CodeLen)
		}
		err := a.ensureLink(ctx, link)
		if !errors.Is(err, errCodeTaken) {
			return link, err
		}
	}
	return link, errCodeTaken
}

// ensureLink makes sure "link/<code>" holds this link: it inserts it, or
// finds it already there (a concurrent duplicate or an earlier attempt wrote
// it), or reports errCodeTaken if a different link owns the code.
func (a *api) ensureLink(ctx context.Context, link Link) error {
	b, err := json.Marshal(link)
	if err != nil {
		return err
	}
	sctx, cancel := context.WithTimeout(ctx, a.cfg.StoreTimeout)
	defer cancel()
	inserted, existing, err := a.store.PutIfAbsent(sctx, "link/"+link.Code, b)
	if err != nil || inserted {
		return err
	}
	var have Link
	if json.Unmarshal(existing, &have) == nil && have.URL == link.URL && have.Owner == link.Owner &&
		have.CreatedAt.Equal(link.CreatedAt) {
		return nil
	}
	return errCodeTaken
}

// insertIdempotent claims "idem/<client>/<key>" with insert-if-absent. The
// winner writes the link; a duplicate (concurrent or later) reads the record,
// checks the fingerprint, and rolls the promised link forward itself, so a
// winner that crashed between the two writes is finished by its retry.
func (a *api) insertIdempotent(ctx context.Context, client, key, fp string, link Link) (Link, bool, error) {
	ikey := "idem/" + client + "/" + key
	rec := idemRecord{Fingerprint: fp, Link: link}
	b, err := json.Marshal(rec)
	if err != nil {
		return link, false, err
	}
	sctx, cancel := context.WithTimeout(ctx, a.cfg.StoreTimeout)
	inserted, existing, err := a.store.PutIfAbsent(sctx, ikey, b)
	cancel()
	if err != nil {
		return link, false, err
	}
	if !inserted {
		var prev idemRecord
		if err := json.Unmarshal(existing, &prev); err != nil {
			return link, false, fmt.Errorf("corrupt idempotency record %s: %w", ikey, err)
		}
		if prev.Fingerprint != fp {
			return link, false, errFingerprint
		}
		if err := a.ensureLink(ctx, prev.Link); err != nil {
			return prev.Link, false, err // errCodeTaken: the owner is re-coding; the client retries
		}
		return prev.Link, true, nil
	}
	for attempt := 0; ; attempt++ {
		err := a.ensureLink(ctx, rec.Link)
		if !errors.Is(err, errCodeTaken) || attempt == 4 {
			return rec.Link, false, err
		}
		rec.Link.Code = NewCode(a.cfg.CodeLen)
		if b, err = json.Marshal(rec); err != nil {
			return link, false, err
		}
		sctx, cancel := context.WithTimeout(ctx, a.cfg.StoreTimeout)
		err = a.store.Put(sctx, ikey, b)
		cancel()
		if err != nil {
			return rec.Link, false, err
		}
	}
}

func (a *api) redirect(w http.ResponseWriter, r *http.Request) {
	code := r.PathValue("code")
	if !validCode(code) {
		apiError(w, http.StatusNotFound, "not_found")
		return
	}
	v, state := a.cache.Get(code)
	if state == Fresh {
		a.stats.hits.Add(1)
		w.Header().Set("X-Cache", "HIT")
		a.answer(w, v)
		return
	}
	res, err, shared := a.flight.Do(code, func() (cached, error) { return a.lookup(code) })
	if shared {
		a.stats.coalesced.Add(1)
	}
	if err != nil {
		a.stats.storeErrors.Add(1)
		if state == Stale {
			a.stats.staleServed.Add(1)
			w.Header().Set("X-Cache", "STALE")
			a.answer(w, v)
			return
		}
		unavailable(w)
		return
	}
	w.Header().Set("X-Cache", "MISS")
	a.answer(w, res)
}

// lookup reads one code from the store and caches the answer. It runs once
// per code at a time on behalf of every waiting request, so it must not use
// any one request's context: if the first caller hangs up, the others still
// want the answer. Its own deadline is StoreTimeout.
func (a *api) lookup(code string) (cached, error) {
	a.stats.lookups.Add(1)
	tok := a.cache.Token()
	ctx, cancel := context.WithTimeout(context.Background(), a.cfg.StoreTimeout)
	defer cancel()
	b, found, err := a.store.Get(ctx, "link/"+code)
	if err != nil {
		return cached{}, err
	}
	if !found {
		c := cached{Status: http.StatusNotFound}
		a.cache.SetIfToken(code, tok, c, a.cfg.NegativeTTL, a.cfg.StaleIfError)
		return c, nil
	}
	var l Link
	if err := json.Unmarshal(b, &l); err != nil {
		return cached{}, fmt.Errorf("corrupt link %s: %w", code, err)
	}
	c := cached{Status: http.StatusFound, Link: l}
	if l.Deleted {
		c.Status = http.StatusGone
	}
	a.cache.SetIfToken(code, tok, c, a.cfg.CacheTTL, a.cfg.StaleIfError)
	return c, nil
}

// answer turns a cached answer into the response. Expiry is checked here, at
// serve time, so a cached entry can never outlive its link.
func (a *api) answer(w http.ResponseWriter, c cached) {
	switch {
	case c.Status == http.StatusNotFound:
		apiError(w, http.StatusNotFound, "not_found")
	case c.Status == http.StatusGone || c.Link.expired(a.now()):
		apiError(w, http.StatusGone, "gone")
	default:
		cc := "private, no-store"
		if a.cfg.RedirectMaxAge > 0 {
			age := a.cfg.RedirectMaxAge
			if !c.Link.ExpiresAt.IsZero() {
				age = min(age, c.Link.ExpiresAt.Sub(a.now()))
			}
			cc = fmt.Sprintf("private, max-age=%d", int(age/time.Second))
		}
		w.Header().Set("Cache-Control", cc)
		w.Header().Set("Location", c.Link.URL)
		w.WriteHeader(http.StatusFound)
	}
}

// remove marks the link deleted (a tombstone, so it answers 410 rather than
// 404) and invalidates this instance's cache. Other instances keep serving
// their cached copy until it stops being fresh: staleness is bounded by
// CacheTTL, not eliminated.
func (a *api) remove(w http.ResponseWriter, r *http.Request) {
	client, ok := clientID(r)
	code := r.PathValue("code")
	if !ok || !validCode(code) {
		apiError(w, http.StatusNotFound, "not_found")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), a.cfg.RequestBudget)
	defer cancel()
	sctx, scancel := context.WithTimeout(ctx, a.cfg.StoreTimeout)
	b, found, err := a.store.Get(sctx, "link/"+code)
	scancel()
	if err != nil {
		a.stats.storeErrors.Add(1)
		unavailable(w)
		return
	}
	var l Link
	if !found || json.Unmarshal(b, &l) != nil || l.Owner != client {
		// Not yours looks exactly like not there: no existence oracle.
		apiError(w, http.StatusNotFound, "not_found")
		return
	}
	if !l.Deleted {
		l.Deleted = true
		nb, _ := json.Marshal(l)
		sctx, scancel := context.WithTimeout(ctx, a.cfg.StoreTimeout)
		err := a.store.Put(sctx, "link/"+code, nb)
		scancel()
		if err != nil {
			a.stats.storeErrors.Add(1)
			unavailable(w)
			return
		}
	}
	a.cache.Delete(code)
	w.WriteHeader(http.StatusNoContent)
}

func (a *api) statsHandler(w http.ResponseWriter, _ *http.Request) {
	s := &a.stats
	writeJSON(w, http.StatusOK, map[string]int64{
		"cache_entries": int64(a.cache.Len()),
		"cache_hits":    s.hits.Load(),
		"stale_served":  s.staleServed.Load(),
		"store_lookups": s.lookups.Load(),
		"coalesced":     s.coalesced.Load(),
		"store_errors":  s.storeErrors.Load(),
		"rate_limited":  s.rateLimited.Load(),
		"created":       s.created.Load(),
		"replayed":      s.replayed.Load(),
		"limiter_keys":  int64(a.limiter.Len()),
	})
}
