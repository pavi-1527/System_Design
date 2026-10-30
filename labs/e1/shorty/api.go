package shorty

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
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

// newAPI wires the components from cfg: a StoreClient for cfg.StoreURL, a
// Cache[cached] of cfg.CacheSize, a TokenBucket from the Create* flags, the
// real clock.
func newAPI(cfg APIConfig) (*api, error) {
	// TODO(lab)
	return nil, errNotImplemented
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

// create handles POST /v1/links (spec.md "API"). In order: client id (400),
// rate limit (429 + Retry-After, before any other work), body and URL
// validation (400), Idempotency-Key format (400), then one request budget
// (cfg.RequestBudget) for every store call this request makes. Success is 201
// with createResponse; a replay adds "Idempotent-Replayed: true"; a key reused
// with a different request is 422 idempotency_key_reused; a failing store is
// unavailable(w). Warm the cache with a new link (not with a replay: the link
// may have been deleted since).
func (a *api) create(w http.ResponseWriter, r *http.Request) {
	// TODO(lab)
	apiError(w, http.StatusNotImplemented, "not_implemented")
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
	// TODO(lab): ensureLink, and a new code on errCodeTaken (up to 5 tries).
	return link, errNotImplemented
}

// ensureLink makes sure "link/<code>" holds this link: it inserts it, or
// finds it already there (a concurrent duplicate or an earlier attempt wrote
// it), or reports errCodeTaken if a different link owns the code.
func (a *api) ensureLink(ctx context.Context, link Link) error {
	// TODO(lab): PutIfAbsent under its own cfg.StoreTimeout; an existing value
	// with the same URL, Owner and CreatedAt is this link (return nil).
	return errNotImplemented
}

// insertIdempotent claims "idem/<client>/<key>" with insert-if-absent. The
// winner writes the link; a duplicate (concurrent or later) reads the record,
// checks the fingerprint, and rolls the promised link forward itself, so a
// winner that crashed between the two writes is finished by its retry.
func (a *api) insertIdempotent(ctx context.Context, client, key, fp string, link Link) (Link, bool, error) {
	// TODO(lab): returns (the link, replayed?, error). A fingerprint mismatch
	// is errFingerprint. On errCodeTaken the owner re-codes the link and
	// overwrites its record (Put) before trying again.
	return link, false, errNotImplemented
}

// redirect handles GET /{code}: malformed codes are 404 without touching the
// cache or the store; a Fresh hit is answered with X-Cache: HIT; otherwise one
// lookup per code runs through a.flight (count waiters in stats.coalesced)
// and the answer goes out with X-Cache: MISS. If the lookup fails, a Stale
// entry is served with X-Cache: STALE (count it in stats.staleServed), and
// without one the answer is unavailable(w).
func (a *api) redirect(w http.ResponseWriter, r *http.Request) {
	// TODO(lab)
	apiError(w, http.StatusNotImplemented, "not_implemented")
}

// lookup reads one code from the store and caches the answer. It runs once
// per code at a time on behalf of every waiting request, so it must not use
// any one request's context: if the first caller hangs up, the others still
// want the answer. Its own deadline is StoreTimeout.
//
// Take a.cache.Token() before reading and store with SetIfToken: found links
// for CacheTTL (410 if Deleted), not-found for NegativeTTL, both with an
// extra StaleIfError window.
func (a *api) lookup(code string) (cached, error) {
	// TODO(lab)
	return cached{}, errNotImplemented
}

// answer turns a cached answer into the response. Expiry is checked here, at
// serve time, so a cached entry can never outlive its link.
//
//	404           apiError not_found (no-store)
//	410 / expired apiError gone (no-store)
//	302           Location, Cache-Control "private, no-store", or with
//	              RedirectMaxAge > 0 "private, max-age=N", N in whole seconds
//	              and never beyond the link's expiry
func (a *api) answer(w http.ResponseWriter, c cached) {
	// TODO(lab)
}

// remove marks the link deleted (a tombstone, so it answers 410 rather than
// 404) and invalidates this instance's cache. Other instances keep serving
// their cached copy until it stops being fresh: staleness is bounded by
// CacheTTL, not eliminated.
//
// DELETE /v1/links/{code}: 404 unless the caller owns it (not 403: no
// existence oracle), 204 otherwise, including when it was already deleted.
func (a *api) remove(w http.ResponseWriter, r *http.Request) {
	// TODO(lab)
	apiError(w, http.StatusNotImplemented, "not_implemented")
}

// statsHandler serves GET /-/stats: a JSON object of integers with at least
// cache_entries, cache_hits, stale_served, store_lookups, coalesced,
// store_errors, rate_limited, created, replayed and limiter_keys.
func (a *api) statsHandler(w http.ResponseWriter, _ *http.Request) {
	// TODO(lab)
	writeJSON(w, http.StatusOK, map[string]int64{})
}
