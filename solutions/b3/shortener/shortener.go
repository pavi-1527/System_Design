// Package shortener is an in-process URL shortening service with pluggable ID
// generation, encoding and storage.
//
// Lab B3 Unit 7 §3 (problem 16). Stage 1 is this file; Stage 2 lives in
// stage2.go and Stage 3 in stage3.go. Open those only when the interviewer
// (the unit text) has asked for them.
//
// Reference design: Service depends on three small interfaces that it defines
// itself (Store, IDGenerator, Encoder) and receives them through Config. It
// keeps only two pieces of state of its own: a dedupe index (URL -> code) that
// is a *hint* validated against the Store on every hit, and click counters.
// Per-key striped mutexes make "same URL, one code" and "one winner per alias"
// hold without a global lock around store I/O.
package shortener

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"net"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Errors. Store implementations return ErrExists and ErrNotFound (possibly
// wrapped), so the Service and its callers use errors.Is, never ==.
var (
	ErrInvalidURL = errors.New("shortener: invalid URL")
	ErrNotFound   = errors.New("shortener: not found")
	ErrExists     = errors.New("shortener: code already exists")
)

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

const alphabet = "0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ"

// Base62 encodes IDs using 0-9, a-z, A-Z (in that order), most significant
// digit first, with no padding: 0 -> "0", 61 -> "Z", 62 -> "10".
type Base62 struct{}

// Encode returns the base-62 representation of id.
func (Base62) Encode(id uint64) string {
	if id == 0 {
		return "0"
	}
	var buf [11]byte // 62^11 > 2^64
	i := len(buf)
	for id > 0 {
		i--
		buf[i] = alphabet[id%62]
		id /= 62
	}
	return string(buf[i:])
}

// Decode is the inverse of Encode. It rejects the empty string, characters
// outside the alphabet, and values that overflow uint64.
func (Base62) Decode(code string) (uint64, error) {
	if code == "" {
		return 0, errors.New("shortener: empty code")
	}
	var n uint64
	for i := 0; i < len(code); i++ {
		d := strings.IndexByte(alphabet, code[i])
		if d < 0 {
			return 0, fmt.Errorf("shortener: invalid character %q in code", code[i])
		}
		if n > (^uint64(0)-uint64(d))/62 {
			return 0, errors.New("shortener: code overflows uint64")
		}
		n = n*62 + uint64(d)
	}
	return n, nil
}

// Counter is an IDGenerator that counts up from a starting value.
type Counter struct{ next atomic.Uint64 }

// NewCounter returns a Counter whose first ID is start.
func NewCounter(start uint64) *Counter {
	c := &Counter{}
	c.next.Store(start)
	return c
}

// Next returns the next ID. Safe for concurrent use.
func (c *Counter) Next() (uint64, error) { return c.next.Add(1) - 1, nil }

// MemoryStore is a map-backed Store guarded by one RWMutex.
type MemoryStore struct {
	mu   sync.RWMutex
	recs map[string]Record
}

// NewMemoryStore returns an empty MemoryStore.
func NewMemoryStore() *MemoryStore { return &MemoryStore{recs: map[string]Record{}} }

func (m *MemoryStore) Put(_ context.Context, rec Record) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.recs[rec.Code]; ok {
		return ErrExists
	}
	m.recs[rec.Code] = rec
	return nil
}

func (m *MemoryStore) Get(_ context.Context, code string) (Record, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	rec, ok := m.recs[code]
	if !ok {
		return Record{}, ErrNotFound
	}
	return rec, nil
}

func (m *MemoryStore) Delete(_ context.Context, code string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.recs[code]; !ok {
		return ErrNotFound
	}
	delete(m.recs, code)
	return nil
}

// Config wires a Service. Every field is optional.
type Config struct {
	Store        Store            // default: NewMemoryStore()
	IDs          IDGenerator      // default: NewCounter(1)
	Encoder      Encoder          // default: Base62{}
	Now          func() time.Time // default: time.Now
	MaxURLLength int              // default 2048 (bytes of the trimmed input URL)
	MaxAttempts  int              // default 5: Store.Put attempts per generated code
}

const stripes = 64

// Service shortens and resolves URLs. Safe for concurrent use.
type Service struct {
	store       Store
	ids         IDGenerator
	enc         Encoder
	now         func() time.Time
	maxURL      int
	maxAttempts int

	locks [stripes]sync.Mutex // per-key critical sections (URL or code), never nested

	mu     sync.Mutex        // guards index and clicks; never held across store calls
	index  map[string]string // normalized URL -> code; a hint, validated against the Store
	clicks map[string]int64  // code -> resolves (Stage 2)
}

// New builds a Service from cfg, filling in defaults. Negative limits are an
// error.
func New(cfg Config) (*Service, error) {
	if cfg.MaxURLLength < 0 || cfg.MaxAttempts < 0 {
		return nil, errors.New("shortener: negative limit in Config")
	}
	s := &Service{
		store: cfg.Store, ids: cfg.IDs, enc: cfg.Encoder, now: cfg.Now,
		maxURL: cfg.MaxURLLength, maxAttempts: cfg.MaxAttempts,
		index: map[string]string{}, clicks: map[string]int64{},
	}
	if s.store == nil {
		s.store = NewMemoryStore()
	}
	if s.ids == nil {
		s.ids = NewCounter(1)
	}
	if s.enc == nil {
		s.enc = Base62{}
	}
	if s.now == nil {
		s.now = time.Now
	}
	if s.maxURL == 0 {
		s.maxURL = 2048
	}
	if s.maxAttempts == 0 {
		s.maxAttempts = 5
	}
	return s, nil
}

// lock returns the stripe guarding key. Callers hold at most one stripe at a
// time, so stripes cannot deadlock.
func (s *Service) lock(key string) *sync.Mutex {
	h := fnv.New32a()
	h.Write([]byte(key))
	return &s.locks[h.Sum32()%stripes]
}

// normalize validates raw and returns its canonical form: trimmed, http or
// https only, no user info, lower-case scheme and host, default port dropped,
// empty path "/". The fragment is kept.
func (s *Service) normalize(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > s.maxURL {
		return "", ErrInvalidURL
	}
	for i := 0; i < len(raw); i++ {
		if raw[i] <= ' ' || raw[i] == 0x7f {
			return "", ErrInvalidURL
		}
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", ErrInvalidURL
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return "", ErrInvalidURL
	}
	if u.User != nil { // https://mybank.com@evil.example/ looks like mybank.com
		return "", ErrInvalidURL
	}
	host := strings.ToLower(u.Hostname())
	if host == "" {
		return "", ErrInvalidURL
	}
	port := u.Port()
	if (scheme == "http" && port == "80") || (scheme == "https" && port == "443") {
		port = ""
	}
	if port != "" {
		host = net.JoinHostPort(host, port)
	} else if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	u.Scheme, u.Host = scheme, host
	if u.Path == "" && u.RawPath == "" {
		u.Path = "/"
	}
	return u.String(), nil
}

func (s *Service) expired(rec Record) bool {
	return !rec.ExpiresAt.IsZero() && !s.now().Before(rec.ExpiresAt)
}

func (s *Service) resetClicks(code string) {
	s.mu.Lock()
	delete(s.clicks, code)
	s.mu.Unlock()
}

// mint stores u under a freshly generated code, retrying on collisions.
func (s *Service) mint(ctx context.Context, u string, ttl time.Duration) (string, error) {
	for range s.maxAttempts {
		id, err := s.ids.Next()
		if err != nil {
			return "", fmt.Errorf("shortener: id generator: %w", err)
		}
		code := s.enc.Encode(id)
		err = s.store.Put(ctx, s.newRecord(code, u, ttl))
		switch {
		case err == nil:
			s.resetClicks(code)
			return code, nil
		case errors.Is(err, ErrExists):
			continue
		default:
			return "", err
		}
	}
	return "", ErrCollisionExhausted
}

func (s *Service) newRecord(code, u string, ttl time.Duration) Record {
	now := s.now()
	rec := Record{Code: code, URL: u, CreatedAt: now}
	if ttl > 0 {
		rec.ExpiresAt = now.Add(ttl)
	}
	return rec
}

// Shorten returns a short code for longURL. Calling it again with the same
// (normalized) URL returns the same code for as long as that link is alive.
// Errors: ErrInvalidURL, the context's error, or whatever the Store returns.
func (s *Service) Shorten(ctx context.Context, longURL string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	u, err := s.normalize(longURL)
	if err != nil {
		return "", err
	}
	l := s.lock(u)
	l.Lock()
	defer l.Unlock()

	s.mu.Lock()
	code, ok := s.index[u]
	s.mu.Unlock()
	if ok {
		rec, err := s.store.Get(ctx, code)
		switch {
		case err == nil && rec.URL == u && !s.expired(rec):
			return code, nil
		case err == nil || errors.Is(err, ErrNotFound):
			// stale hint (deleted, expired, or the code now belongs to another URL)
		default:
			return "", err
		}
	}
	code, err = s.mint(ctx, u, 0)
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	s.index[u] = code
	s.mu.Unlock()
	return code, nil
}

// Resolve returns the long URL for code, or ErrNotFound if there is no such
// live link. Other Store errors are returned as is, never as ErrNotFound.
func (s *Service) Resolve(ctx context.Context, code string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if code == "" {
		return "", ErrNotFound
	}
	rec, err := s.store.Get(ctx, code)
	if err != nil {
		return "", err
	}
	if s.expired(rec) {
		return "", ErrNotFound
	}
	s.mu.Lock()
	s.clicks[code]++
	s.mu.Unlock()
	return rec.URL, nil
}
