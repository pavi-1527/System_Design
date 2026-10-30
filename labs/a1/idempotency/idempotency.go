// Package idempotency is net/http middleware that makes non-idempotent
// requests (POST, PATCH) safe to retry, Stripe-style: the client sends an
// Idempotency-Key header; the first request with that key runs the handler
// and its response is stored; retries with the same key and the same request
// get the stored response replayed instead of running the handler again.
//
// It is the lab for A1 Unit 3 §2. Suggested order: MemoryStore (the two
// store tests), then Middleware for the sequential cases, then the
// concurrent and lease tests. Run: go test -race ./labs/a1/idempotency/
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
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"time"
)

var errNotImplemented = errors.New("idempotency: not implemented")

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
	// TODO(lab): a mutex and a map[string]Record. A record whose ExpiresAt is
	// not after now is treated as absent by Get and Reserve.
}

// NewMemoryStore returns an empty MemoryStore.
func NewMemoryStore() *MemoryStore {
	// TODO(lab)
	return &MemoryStore{}
}

// Get implements Store.
func (s *MemoryStore) Get(_ context.Context, key string, now time.Time) (Record, bool, error) {
	// TODO(lab)
	return Record{}, false, errNotImplemented
}

// Reserve implements Store. The check and the write must happen under one
// lock acquisition: that is the whole point.
func (s *MemoryStore) Reserve(_ context.Context, key string, now time.Time, rec Record) (Record, bool, error) {
	// TODO(lab)
	return Record{}, false, errNotImplemented
}

// Complete implements Store.
func (s *MemoryStore) Complete(_ context.Context, key string, rec Record) error {
	// TODO(lab): only if the stored record is in flight with rec.Owner.
	return errNotImplemented
}

// Release implements Store.
func (s *MemoryStore) Release(_ context.Context, key, owner string) error {
	// TODO(lab): only if the stored record is in flight with owner.
	return errNotImplemented
}

// Config configures Middleware. Zero values get defaults.
type Config struct {
	Store        Store            // default: NewMemoryStore()
	TTL          time.Duration    // retention of completed records; default 24h
	LockTimeout  time.Duration    // in-flight lease; default 30s
	Now          func() time.Time // default time.Now
	MaxBodyBytes int64            // default 1 MiB; larger bodies => 413
}

// ScopedKey is the store key for a client's idempotency key. Provided.
func ScopedKey(clientID, key string) string { return clientID + "\x00" + key }

// Fingerprint identifies "the same request": method, path and body. Provided.
func Fingerprint(method, path string, body []byte) string {
	h := sha256.New()
	io.WriteString(h, method)
	h.Write([]byte{0})
	io.WriteString(h, path)
	h.Write([]byte{0})
	h.Write(body)
	return hex.EncodeToString(h.Sum(nil))
}

// Middleware wraps next with idempotency-key handling. See the package doc
// for the exact behaviour.
//
// Hints:
//   - Read the body with http.MaxBytesReader, then restore r.Body with
//     io.NopCloser(bytes.NewReader(body)) so the handler can read it too.
//   - Owner tokens: 16 bytes from crypto/rand, hex encoded.
//   - To store the handler's response you must buffer it: pass the handler a
//     small type implementing http.ResponseWriter (Header, WriteHeader,
//     Write) that records status, headers and body; then write it to w.
//   - Errors are JSON: {"error":{"code":"...","message":"..."}}. Codes used
//     by the reference: invalid_idempotency_key, missing_client_id,
//     body_too_large, idempotency_key_reused (422), idempotency_key_in_use
//     (409), idempotency_store_unavailable (503).
//   - Use context.WithoutCancel(r.Context()) for Complete/Release so a client
//     disconnect does not skip the bookkeeping.
//   - Break-it: when breakNoInFlightLock is true (go test -tags breakit),
//     replace Reserve with Store.Get + run handler + store the result, i.e. a
//     non-atomic check-then-act, and watch the concurrent test fail.
func Middleware(cfg Config, next http.Handler) http.Handler {
	// TODO(lab)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "idempotency: not implemented", http.StatusNotImplemented)
	})
}
