// Package pagination implements keyset (cursor) pagination over an
// in-memory ordered store, with opaque HMAC-signed cursors, plus an offset
// implementation for comparison. It is the lab for A1 Unit 2 §2.
//
// Sort order everywhere: newest first (CreatedAt descending), ties broken by
// ID descending. The (CreatedAt, ID) pair is unique, so the order is total
// and stable: that is what makes "give me everything after this key" well
// defined.
package pagination

import (
	"errors"
	"net/http"
	"sort"
	"sync"
	"time"
)

// Limits for List and the HTTP handler.
const (
	DefaultLimit = 20
	MaxLimit     = 100
)

var errNotImplemented = errors.New("pagination: not implemented")

var (
	// ErrInvalidCursor: the cursor is malformed, truncated, tampered with, or
	// was signed with a different secret.
	ErrInvalidCursor = errors.New("pagination: invalid cursor")
	// ErrInvalidLimit: limit outside [1, MaxLimit].
	ErrInvalidLimit = errors.New("pagination: invalid limit")
	// ErrDuplicateID: Store.Insert with an ID that already exists.
	ErrDuplicateID = errors.New("pagination: duplicate id")
)

// Item is one row, e.g. a posted transaction on a member's account.
type Item struct {
	ID        string    `json:"id"`
	CreatedAt time.Time `json:"created_at"`
	Memo      string    `json:"memo,omitempty"`
}

// Key is an item's position in the sort order.
type Key struct {
	CreatedAt time.Time
	ID        string
}

// KeyOf returns the item's sort key.
func KeyOf(it Item) Key { return Key{CreatedAt: it.CreatedAt, ID: it.ID} }

// Before reports whether a sorts before b: newer first, then larger ID first.
// Provided.
func Before(a, b Key) bool {
	if !a.CreatedAt.Equal(b.CreatedAt) {
		return a.CreatedAt.After(b.CreatedAt)
	}
	return a.ID > b.ID
}

// Store is an in-memory table kept sorted by Before. It is safe for
// concurrent use. Insert, Delete and Len are provided; After and Offset are
// the exercise.
type Store struct {
	mu    sync.RWMutex
	items []Item         // sorted by Before
	byID  map[string]Key // id -> key, for Delete
}

// NewStore returns an empty store.
func NewStore() *Store { return &Store{byID: map[string]Key{}} }

// Insert adds it in sorted position. Provided.
func (s *Store) Insert(it Item) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.byID[it.ID]; ok {
		return ErrDuplicateID
	}
	k := KeyOf(it)
	i := sort.Search(len(s.items), func(i int) bool { return Before(k, KeyOf(s.items[i])) })
	s.items = append(s.items, Item{})
	copy(s.items[i+1:], s.items[i:])
	s.items[i] = it
	s.byID[it.ID] = k
	return nil
}

// Delete removes the item with id, reporting whether it existed. Provided.
func (s *Store) Delete(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	k, ok := s.byID[id]
	if !ok {
		return false
	}
	i := sort.Search(len(s.items), func(i int) bool { return !Before(KeyOf(s.items[i]), k) })
	s.items = append(s.items[:i], s.items[i+1:]...)
	delete(s.byID, id)
	return true
}

// Len returns the number of items. Provided.
func (s *Store) Len() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.items)
}

// After returns up to limit items that sort strictly after *after, in sort
// order. after == nil means "from the start". The key need not belong to an
// existing item (the item may have been deleted since the cursor was
// issued). The returned slice is a copy. limit <= 0 returns nil.
//
// This is the "seek" in keyset pagination: in SQL it is
//
//	WHERE (created_at, id) < ($1, $2) ORDER BY created_at DESC, id DESC LIMIT $3
//
// and an index on (created_at, id) makes it O(log n + limit) no matter how
// deep you are.
func (s *Store) After(after *Key, limit int) []Item {
	// TODO(lab): binary-search (sort.Search) for the first item whose key
	// sorts strictly after *after, then copy up to limit items. Hold s.mu
	// for reading.
	return nil
}

// Offset returns up to limit items starting at position offset (0-based) in
// sort order: SQL's ORDER BY ... LIMIT limit OFFSET offset. The returned
// slice is a copy. offset < 0 or limit <= 0 returns nil.
func (s *Store) Offset(offset, limit int) []Item {
	// TODO(lab): bounds-checked copy of the window under s.mu.RLock.
	return nil
}

// Cursor format: your choice, but it must be opaque, URL-safe
// (base64.RawURLEncoding), carry CreatedAt with nanosecond precision plus the
// ID, and be authenticated with HMAC-SHA256 so that tampering, truncation or
// a different secret is detected. Suggested layout:
//
//	base64url_nopad( version(1) | unix nanos (8, big endian) | id | HMAC-SHA256(secret, previous bytes)(32) )
//
// Use hmac.Equal (constant time) to compare MACs, and consider
// base64.RawURLEncoding.Strict() so non-canonical encodings are rejected.

// EncodeCursor returns an opaque, URL-safe cursor for k, signed with secret.
func EncodeCursor(secret []byte, k Key) string {
	// TODO(lab)
	return ""
}

// DecodeCursor verifies and decodes a cursor produced by EncodeCursor with
// the same secret. Any failure returns an error wrapping ErrInvalidCursor.
// The returned CreatedAt is in UTC.
func DecodeCursor(secret []byte, cursor string) (Key, error) {
	// TODO(lab): decode, check length, verify the MAC, then parse.
	return Key{}, errNotImplemented
}

// Page is one page of results. NextCursor is empty on the last page.
type Page struct {
	Items      []Item `json:"items"`
	NextCursor string `json:"next_cursor,omitempty"`
}

// Paginator serves keyset pages over a Store with signed cursors.
type Paginator struct {
	store  *Store
	secret []byte
}

// NewPaginator returns a Paginator over s that signs cursors with secret.
func NewPaginator(s *Store, secret []byte) *Paginator {
	return &Paginator{store: s, secret: append([]byte(nil), secret...)} // provided
}

// List returns the page after cursor ("" = first page) with at most limit
// items. limit must be in [1, MaxLimit] (else ErrInvalidLimit). A bad cursor
// returns ErrInvalidCursor. Page.Items is never nil (an empty page encodes
// as [] in JSON, not null). NextCursor is set iff at least one more item
// existed after this page at the time of the read: no empty trailing page
// when the item count is an exact multiple of limit.
func (p *Paginator) List(cursor string, limit int) (Page, error) {
	// TODO(lab): validate limit, decode cursor, fetch limit+1 via After,
	// trim and set NextCursor from the last returned item.
	return Page{}, errNotImplemented
}

// NewHandler serves GET ?limit=N&cursor=C as JSON:
//
//	200 {"items":[...],"next_cursor":"..."}           (next_cursor omitted on the last page)
//	400 {"error":{"code":"invalid_limit","message":...}}   limit not an integer in [1, MaxLimit]
//	400 {"error":{"code":"invalid_cursor","message":...}}  cursor rejected
//	405 with an "Allow: GET" header for any other method
//
// A missing limit means DefaultLimit. Content-Type is application/json.
func NewHandler(p *Paginator) http.Handler {
	// TODO(lab): parse limit/cursor, call List, write JSON. Errors use the
	// {"error":{"code":...,"message":...}} model.
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "pagination: not implemented", http.StatusNotImplemented)
	})
}
