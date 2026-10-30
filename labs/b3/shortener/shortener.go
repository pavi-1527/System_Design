// Package shortener is an in-process URL shortening service with pluggable ID
// generation, encoding and storage.
//
// Lab B3 Unit 7 §3 (problem 16), time box 60 minutes. Work in stages and open
// the next stage's file only when you get there, like an interviewer who adds
// requirements as you go:
//
//	Stage 1 (≈ 25 min)  this file:  Service.Shorten/Resolve, validation, Base62, Counter, MemoryStore
//	Stage 2 (≈ 20 min)  stage2.go:  aliases, TTL, stats, delete, a random ID strategy, collision retry
//	Stage 3 (≈ 15 min)  stage3.go:  concurrency guarantees and a ShardedStore
//
// Run one stage at a time:
//
//	go test -race -run 'Stage1' ./labs/b3/shortener/
//
// The point of this problem is the *seams*. The tests bring their own fake
// Store, IDGenerator, Encoder and clock, and a shared contract suite
// (RunStoreContract in shortener_test.go) that every Store must pass. If your
// Service reaches for anything outside Config, those fakes will break it.
package shortener

import (
	"context"
	"errors"
	"time"
)

// Errors. Store implementations return ErrExists and ErrNotFound (possibly
// wrapped), so the Service and its callers use errors.Is, never ==.
var (
	ErrInvalidURL = errors.New("shortener: invalid URL")
	ErrNotFound   = errors.New("shortener: not found")
	ErrExists     = errors.New("shortener: code already exists")
)

var errNotImplemented = errors.New("shortener: not implemented")

// Record is what the Store keeps for one short code.
type Record struct {
	Code      string
	URL       string // normalized long URL
	CreatedAt time.Time
	ExpiresAt time.Time // zero = never expires (used from Stage 2)
}

// Store persists records. Implementations must be safe for concurrent use.
//
//   - Put inserts rec only if no record with rec.Code exists, atomically; if
//     one exists it returns ErrExists and changes nothing.
//   - Get returns ErrNotFound if there is no record.
//   - Delete returns ErrNotFound if there is no record.
//
// The Store knows nothing about expiry: it stores ExpiresAt, the Service
// decides what "expired" means.
type Store interface {
	Put(ctx context.Context, rec Record) error
	Get(ctx context.Context, code string) (Record, error)
	Delete(ctx context.Context, code string) error
}

// IDGenerator hands out numeric IDs. Next may return the same value twice
// (random strategies do); the Service copes with collisions.
type IDGenerator interface {
	Next() (uint64, error)
}

// Encoder turns an ID into a short code. The Service only ever encodes.
type Encoder interface {
	Encode(id uint64) string
}

// Base62 encodes IDs using the alphabet 0-9, a-z, A-Z (in that order), most
// significant digit first, with no padding: 0 -> "0", 10 -> "a", 36 -> "A",
// 61 -> "Z", 62 -> "10".
type Base62 struct{}

// Encode returns the base-62 representation of id.
func (Base62) Encode(id uint64) string {
	// TODO(lab)
	_ = id
	return ""
}

// Decode is the inverse of Encode. It rejects the empty string, characters
// outside the alphabet, and values that overflow uint64.
func (Base62) Decode(code string) (uint64, error) {
	// TODO(lab)
	_ = code
	return 0, errNotImplemented
}

// Counter is an IDGenerator that counts up from a starting value. Safe for
// concurrent use.
type Counter struct {
	// TODO(lab)
}

// NewCounter returns a Counter whose first ID is start.
func NewCounter(start uint64) *Counter {
	// TODO(lab)
	_ = start
	return &Counter{}
}

// Next returns the next ID.
func (c *Counter) Next() (uint64, error) {
	// TODO(lab)
	return 0, errNotImplemented
}

// MemoryStore is a map-backed Store.
type MemoryStore struct {
	// TODO(lab)
}

// NewMemoryStore returns an empty MemoryStore.
func NewMemoryStore() *MemoryStore {
	// TODO(lab)
	return &MemoryStore{}
}

func (m *MemoryStore) Put(ctx context.Context, rec Record) error {
	// TODO(lab)
	_, _ = ctx, rec
	return errNotImplemented
}

func (m *MemoryStore) Get(ctx context.Context, code string) (Record, error) {
	// TODO(lab)
	_, _ = ctx, code
	return Record{}, errNotImplemented
}

func (m *MemoryStore) Delete(ctx context.Context, code string) error {
	// TODO(lab)
	_, _ = ctx, code
	return errNotImplemented
}

// Config wires a Service. Every field is optional.
type Config struct {
	Store        Store            // default: NewMemoryStore()
	IDs          IDGenerator      // default: NewCounter(1)
	Encoder      Encoder          // default: Base62{}
	Now          func() time.Time // default: time.Now
	MaxURLLength int              // default 2048: bytes of the trimmed input URL, inclusive
	MaxAttempts  int              // default 5: Store.Put attempts per generated code (Stage 2)
}

// Service shortens and resolves URLs. Safe for concurrent use.
type Service struct {
	// TODO(lab): the injected parts, plus whatever state and locks you decide on.
}

// New builds a Service from cfg, filling in defaults. Negative MaxURLLength or
// MaxAttempts is an error.
func New(cfg Config) (*Service, error) {
	// TODO(lab)
	_ = cfg
	return nil, errNotImplemented
}

// Shorten returns a short code for longURL.
//
// Validation and normalization (the same policy for every method that takes a
// URL): trim surrounding whitespace; reject empty input, anything longer than
// MaxURLLength, control characters or spaces anywhere, anything that does not
// parse; the scheme must be http or https (case-insensitive); there must be a
// host; user info ("https://user@host") is rejected. All failures are
// ErrInvalidURL. The stored form is canonical: lower-case scheme and host, the
// scheme's default port (80/443) dropped, an empty path becomes "/", path,
// query and fragment untouched.
//
// Idempotent: while its link is alive, shortening the same *normalized* URL
// again returns the same code and does not consume an ID or touch Put.
//
// The code is Encoder.Encode(IDGenerator.Next()). A cancelled context returns
// ctx.Err() before the Store is touched. Store errors other than ErrExists are
// returned (wrapping is fine); never swallow or relabel them.
func (s *Service) Shorten(ctx context.Context, longURL string) (string, error) {
	// TODO(lab)
	_, _ = ctx, longURL
	return "", errNotImplemented
}

// Resolve returns the (normalized) long URL for code. An unknown code, the
// empty code, or an expired link is ErrNotFound; a Store failure is returned
// as is and is NOT ErrNotFound. A cancelled context returns ctx.Err().
func (s *Service) Resolve(ctx context.Context, code string) (string, error) {
	// TODO(lab)
	_, _ = ctx, code
	return "", errNotImplemented
}
