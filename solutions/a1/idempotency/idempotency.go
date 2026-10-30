// Package idempotency is net/http middleware that makes non-idempotent
// requests (POST, PATCH) safe to retry, Stripe-style: the client sends an
// Idempotency-Key header; the first request with that key runs the handler
// and its response is stored; retries with the same key and the same request
// get the stored response replayed instead of running the handler again.
//
// It is the lab for A1 Unit 3 §2.
//
// Behaviour (tests depend on every line):
//   - Only POST and PATCH are intercepted; other methods pass straight
//     through. Requests without an Idempotency-Key also pass through.
//   - Keys longer than MaxKeyLength => 400. A key without an X-Client-ID
//     header => 400 (keys are scoped per client: two clients may use the
//     same key for unrelated requests).
//   - The request fingerprint covers method, path and body. Same key with a
//     different fingerprint => 422 (the client has a bug; never replay a
//     response to a request it does not belong to).
//   - Concurrent duplicate while the first is still in flight => 409 with
//     Retry-After: 1. (Design choice: reject rather than wait-and-replay; see
//     the unit text for the trade-off.)
//   - Completed => replay the stored status, headers and body, plus
//     Idempotent-Replayed: true. The handler does not run.
//   - Handler responses with status >= 500 are NOT stored: the reservation is
//     released so a retry runs the handler again. This assumes the handler's
//     5xx means "nothing happened" (it rolled back). 2xx/3xx/4xx are stored.
//   - A handler panic releases the reservation and re-panics.
//   - Completed records expire after TTL. In-flight reservations are leases
//     that expire after LockTimeout so a crashed worker cannot wedge a key
//     forever; a stale owner that finishes after its lease was taken over
//     cannot overwrite the new owner's record (ErrNotOwner).
//   - If the store fails, fail closed with 503: for money movement, refusing
//     is better than risking a double execution.
package idempotency

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sync"
	"time"
)

// Header names.
const (
	HeaderKey      = "Idempotency-Key"
	HeaderClient   = "X-Client-ID"
	HeaderReplayed = "Idempotent-Replayed"
	MaxKeyLength   = 255
)

// ErrNotOwner is returned by Store.Complete/Release when the caller's
// reservation is gone or belongs to someone else (its lease expired and was
// taken over, or it was already completed/released).
var ErrNotOwner = errors.New("idempotency: reservation not owned by caller")

// State of a Record.
type State int

// Record states.
const (
	StateInFlight State = iota + 1
	StateCompleted
)

// Record is what the store keeps per scoped key.
type Record struct {
	State       State
	Fingerprint string
	Owner       string // random token identifying the reserving request
	Status      int
	Header      http.Header
	Body        []byte
	CreatedAt   time.Time
	// ExpiresAt: for StateInFlight, when the lease ends; for StateCompleted,
	// when the record may be forgotten. A record with !now.Before(ExpiresAt)
	// is treated as absent.
	ExpiresAt time.Time
}

// Store persists idempotency records. Implementations must make Reserve
// atomic: of N concurrent Reserve calls for an absent key, exactly one
// returns reserved == true. In production this is a single conditional write
// (INSERT ... ON CONFLICT DO NOTHING, Redis SET NX PX, DynamoDB
// attribute_not_exists).
type Store interface {
	// Get returns the live record for key, if any.
	Get(ctx context.Context, key string, now time.Time) (Record, bool, error)
	// Reserve stores rec under key if no live record exists and returns
	// reserved == true. Otherwise it returns the existing live record and
	// reserved == false.
	Reserve(ctx context.Context, key string, now time.Time, rec Record) (existing Record, reserved bool, err error)
	// Complete replaces the in-flight record owned by rec.Owner with rec.
	// ErrNotOwner if the stored record is not in flight with that owner.
	Complete(ctx context.Context, key string, rec Record) error
	// Release deletes the in-flight record owned by owner. ErrNotOwner if the
	// stored record is not in flight with that owner.
	Release(ctx context.Context, key, owner string) error
}

// MemoryStore is an in-process Store. Safe for concurrent use.
type MemoryStore struct {
	mu sync.Mutex
	m  map[string]Record
}

// NewMemoryStore returns an empty MemoryStore.
func NewMemoryStore() *MemoryStore { return &MemoryStore{m: map[string]Record{}} }

func live(rec Record, now time.Time) bool { return now.Before(rec.ExpiresAt) }

// Get implements Store.
func (s *MemoryStore) Get(_ context.Context, key string, now time.Time) (Record, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.m[key]
	if !ok || !live(rec, now) {
		return Record{}, false, nil
	}
	return rec, true, nil
}

// Reserve implements Store.
func (s *MemoryStore) Reserve(_ context.Context, key string, now time.Time, rec Record) (Record, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if cur, ok := s.m[key]; ok && live(cur, now) {
		return cur, false, nil
	}
	s.m[key] = rec
	return Record{}, true, nil
}

// Complete implements Store.
func (s *MemoryStore) Complete(_ context.Context, key string, rec Record) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cur, ok := s.m[key]
	if !ok || cur.State != StateInFlight || cur.Owner != rec.Owner {
		return ErrNotOwner
	}
	s.m[key] = rec
	return nil
}

// Release implements Store.
func (s *MemoryStore) Release(_ context.Context, key, owner string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cur, ok := s.m[key]
	if !ok || cur.State != StateInFlight || cur.Owner != owner {
		return ErrNotOwner
	}
	delete(s.m, key)
	return nil
}

// Config configures Middleware. Zero values get defaults.
type Config struct {
	Store        Store            // default: NewMemoryStore()
	TTL          time.Duration    // retention of completed records; default 24h
	LockTimeout  time.Duration    // in-flight lease; default 30s
	Now          func() time.Time // default time.Now
	MaxBodyBytes int64            // default 1 MiB; larger bodies => 413
}

// ScopedKey is the store key for a client's idempotency key.
func ScopedKey(clientID, key string) string { return clientID + "\x00" + key }

// Fingerprint identifies "the same request": method, path and body.
func Fingerprint(method, path string, body []byte) string {
	h := sha256.New()
	io.WriteString(h, method)
	h.Write([]byte{0})
	io.WriteString(h, path)
	h.Write([]byte{0})
	h.Write(body)
	return hex.EncodeToString(h.Sum(nil))
}

func newOwner() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

func writeError(w http.ResponseWriter, status int, code, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"code": code, "message": msg}})
}

// capture buffers a handler's response so it can be stored and replayed.
type capture struct {
	header http.Header
	status int
	body   bytes.Buffer
}

func (c *capture) Header() http.Header { return c.header }
func (c *capture) WriteHeader(code int) {
	if c.status == 0 {
		c.status = code
	}
}
func (c *capture) Write(p []byte) (int, error) {
	if c.status == 0 {
		c.status = http.StatusOK
	}
	return c.body.Write(p)
}

func writeRecord(w http.ResponseWriter, status int, h http.Header, body []byte) {
	dst := w.Header()
	for k, v := range h {
		dst[k] = append([]string(nil), v...)
	}
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

// Middleware wraps next with idempotency-key handling. See the package doc
// for the exact behaviour.
func Middleware(cfg Config, next http.Handler) http.Handler {
	if cfg.Store == nil {
		cfg.Store = NewMemoryStore()
	}
	if cfg.TTL <= 0 {
		cfg.TTL = 24 * time.Hour
	}
	if cfg.LockTimeout <= 0 {
		cfg.LockTimeout = 30 * time.Second
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.MaxBodyBytes <= 0 {
		cfg.MaxBodyBytes = 1 << 20
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost && r.Method != http.MethodPatch {
			next.ServeHTTP(w, r)
			return
		}
		key := r.Header.Get(HeaderKey)
		if key == "" {
			next.ServeHTTP(w, r)
			return
		}
		if len(key) > MaxKeyLength {
			writeError(w, http.StatusBadRequest, "invalid_idempotency_key", "Idempotency-Key must be at most 255 characters")
			return
		}
		client := r.Header.Get(HeaderClient)
		if client == "" {
			writeError(w, http.StatusBadRequest, "missing_client_id", "X-Client-ID is required with Idempotency-Key")
			return
		}
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, cfg.MaxBodyBytes))
		if err != nil {
			var mbe *http.MaxBytesError
			if errors.As(err, &mbe) {
				writeError(w, http.StatusRequestEntityTooLarge, "body_too_large", "request body too large")
				return
			}
			writeError(w, http.StatusBadRequest, "bad_body", "could not read request body")
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(body))

		ctx := r.Context()
		sk := ScopedKey(client, key)
		fp := Fingerprint(r.Method, r.URL.Path, body)
		now := cfg.Now()
		owner := newOwner()

		var existing Record
		var found bool
		if breakNoInFlightLock {
			// BREAK-IT: check-then-act with no atomic reservation.
			existing, found, err = cfg.Store.Get(ctx, sk, now)
		} else {
			var reserved bool
			existing, reserved, err = cfg.Store.Reserve(ctx, sk, now, Record{
				State: StateInFlight, Fingerprint: fp, Owner: owner,
				CreatedAt: now, ExpiresAt: now.Add(cfg.LockTimeout),
			})
			found = !reserved
		}
		if err != nil {
			writeError(w, http.StatusServiceUnavailable, "idempotency_store_unavailable", "try again later")
			return
		}
		if found {
			switch {
			case existing.Fingerprint != fp:
				writeError(w, http.StatusUnprocessableEntity, "idempotency_key_reused",
					"this Idempotency-Key was used with a different request")
			case existing.State == StateInFlight:
				w.Header().Set("Retry-After", "1")
				writeError(w, http.StatusConflict, "idempotency_key_in_use",
					"a request with this Idempotency-Key is still in progress")
			default:
				w.Header().Set(HeaderReplayed, "true")
				writeRecord(w, existing.Status, existing.Header, existing.Body)
			}
			return
		}

		c := &capture{header: http.Header{}}
		completed := false
		defer func() {
			if !completed && !breakNoInFlightLock {
				// Panic (or anything else that skipped completion): free the key.
				_ = cfg.Store.Release(context.WithoutCancel(ctx), sk, owner)
			}
		}()
		next.ServeHTTP(c, r)
		if c.status == 0 {
			c.status = http.StatusOK
		}

		if c.status >= 500 {
			// Not stored: release so a retry re-executes (see package doc).
			if !breakNoInFlightLock {
				_ = cfg.Store.Release(context.WithoutCancel(ctx), sk, owner)
			}
			completed = true
			writeRecord(w, c.status, c.header, c.body.Bytes())
			return
		}
		done := cfg.Now()
		rec := Record{
			State: StateCompleted, Fingerprint: fp, Owner: owner,
			Status: c.status, Header: c.header.Clone(), Body: append([]byte(nil), c.body.Bytes()...),
			CreatedAt: now, ExpiresAt: done.Add(cfg.TTL),
		}
		if breakNoInFlightLock {
			_, _, _ = cfg.Store.Reserve(context.WithoutCancel(ctx), sk, done, rec)
		} else {
			// ErrNotOwner here means our lease expired and another request took
			// the key over: the handler ran twice. We still answer this client,
			// but must not clobber the new owner's record. In production: log
			// and alert; this is the case leases trade for liveness.
			_ = cfg.Store.Complete(context.WithoutCancel(ctx), sk, rec)
		}
		completed = true
		writeRecord(w, c.status, c.header, c.body.Bytes())
	})
}
