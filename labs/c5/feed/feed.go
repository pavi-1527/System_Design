// Package feed is a toy home-timeline service: the hardest component of the
// C5 news-feed case study, small enough to test under -race.
//
// It implements the hybrid fan-out that C5 Unit 2 §5 derives:
//
//   - Ordinary authors are fanned out ON WRITE: a post is pushed into every
//     follower's bounded timeline (a cached list of (CreatedAt, ID, author)
//     entries, newest first, at most MaxTimeline long).
//   - Authors with at least CelebrityThreshold followers are NOT fanned out.
//     Their posts go to a per-author "pull" list and are merged into
//     followers' feeds ON READ.
//   - Feed does a k-way merge of the reader's timeline and the pull lists of
//     the celebrities they follow, newest first, with an opaque cursor so
//     paging never duplicates or skips a post that existed for the whole walk.
//
// Every post records at write time whether it was fanned out, so an author
// crossing the threshold in either direction never duplicates or loses posts.
// Deleted posts are dropped lazily at read time (hydration): timelines are
// never scanned on delete. Unfollow eagerly removes that author's entries from
// the follower's timeline.
//
// Posts are not durable, timelines never evict for memory pressure, and the
// cursor is unsigned; the lesson covers what production adds.
package feed

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Defaults and limits.
const (
	// DefaultMaxTimeline is the number of entries a cached timeline keeps.
	// Twitter's home-timeline cache historically kept ≈ 800 entries per user.
	DefaultMaxTimeline = 800
	// DefaultCelebrityThreshold: authors with at least this many followers
	// are merged on read instead of fanned out on write.
	DefaultCelebrityThreshold = 10_000
	// DefaultLimit and MaxLimit bound a Feed page.
	DefaultLimit = 20
	MaxLimit     = 100
)

var errNotImplemented = errors.New("feed: not implemented")

var (
	// ErrInvalidArgument: empty user, empty text, or following yourself.
	ErrInvalidArgument = errors.New("feed: invalid argument")
	// ErrNotFound: the post does not exist, or is not the caller's.
	ErrNotFound = errors.New("feed: not found")
	// ErrInvalidCursor: the cursor is malformed or truncated.
	ErrInvalidCursor = errors.New("feed: invalid cursor")
	// ErrInvalidLimit: limit outside [1, MaxLimit] (0 means DefaultLimit).
	ErrInvalidLimit = errors.New("feed: invalid limit")
)

// Config tunes the service. Zero values take the defaults above and time.Now.
// CelebrityThreshold 1 makes every followed author a celebrity (pure fan-out
// on read); math.MaxInt makes nobody one (pure fan-out on write).
type Config struct {
	MaxTimeline        int              // entries kept per cached timeline
	CelebrityThreshold int              // followers at which an author stops being fanned out
	Now                func() time.Time // injectable clock (tests use a fake one)
}

func (c Config) withDefaults() Config {
	if c.MaxTimeline <= 0 {
		c.MaxTimeline = DefaultMaxTimeline
	}
	if c.CelebrityThreshold <= 0 {
		c.CelebrityThreshold = DefaultCelebrityThreshold
	}
	if c.Now == nil {
		c.Now = time.Now
	}
	return c
}

// Post is a hydrated post as returned in a feed page.
type Post struct {
	ID        string    `json:"id"`
	Author    string    `json:"author"`
	Text      string    `json:"text"`
	CreatedAt time.Time `json:"created_at"`
}

// Key is a post's position in feed order: newest first, ties (same
// CreatedAt, which happens with coarse clocks) broken by ID descending. IDs
// are fixed-width so that string comparison follows creation order.
type Key struct {
	CreatedAt time.Time
	ID        string
}

// KeyOf returns the post's sort key.
func KeyOf(p Post) Key { return Key{CreatedAt: p.CreatedAt, ID: p.ID} }

// Before reports whether a sorts before b in feed order (a is newer, or the
// same instant with the larger ID). Provided.
func Before(a, b Key) bool {
	if !a.CreatedAt.Equal(b.CreatedAt) {
		return a.CreatedAt.After(b.CreatedAt)
	}
	return a.ID > b.ID
}

// EncodeCursor returns an opaque, URL-safe cursor for k. Provided. It is
// not signed; A1's pagination lab shows the HMAC version.
func EncodeCursor(k Key) string {
	return base64.RawURLEncoding.EncodeToString([]byte(strconv.FormatInt(k.CreatedAt.UnixNano(), 10) + ":" + k.ID))
}

// DecodeCursor parses a cursor produced by EncodeCursor. Any failure wraps
// ErrInvalidCursor. Provided.
func DecodeCursor(cursor string) (Key, error) {
	b, err := base64.RawURLEncoding.Strict().DecodeString(cursor)
	if err != nil {
		return Key{}, fmt.Errorf("%w: %v", ErrInvalidCursor, err)
	}
	nanos, id, ok := strings.Cut(string(b), ":")
	if !ok || id == "" {
		return Key{}, ErrInvalidCursor
	}
	n, err := strconv.ParseInt(nanos, 10, 64)
	if err != nil {
		return Key{}, fmt.Errorf("%w: %v", ErrInvalidCursor, err)
	}
	return Key{CreatedAt: time.Unix(0, n).UTC(), ID: id}, nil
}

// Page is one page of a feed. Items is never nil; NextCursor is empty on the
// last page.
type Page struct {
	Items      []Post `json:"items"`
	NextCursor string `json:"next_cursor,omitempty"`
}

// Stats are counters for the break-it experiments.
type Stats struct {
	Posts        int64 // posts created (duplicates by client ID not counted)
	FanoutWrites int64 // timeline insertions performed by Post
	FeedReads    int64 // Feed calls that returned a page
}

// Service is the in-memory feed service. All methods must be safe for
// concurrent use (the tests run posts, follows and reads in parallel under
// -race).
//
// Suggested state (add your own fields): the posts by ID; a map from
// (author, clientID) to the post it created; follower and followee sets; one
// bounded timeline per user holding (Key, author) entries newest first; and
// one "pull" list per author holding the entries that were NOT fanned out.
// A single RWMutex is fine; finer locking is your call.
type Service struct {
	cfg Config
	mu  sync.RWMutex
}

// New returns a Service with defaults applied.
func New(cfg Config) *Service {
	// TODO(lab): initialise your maps here.
	return &Service{cfg: cfg.withDefaults()}
}

// Config returns the effective configuration. Provided.
func (s *Service) Config() Config { return s.cfg }

// Follow makes follower receive followee's posts from now on. Idempotent.
// Following yourself or an empty user is ErrInvalidArgument. Follow does not
// backfill: posts made before the follow do not appear in the timeline (the
// lesson discusses the trade-off), but a celebrity's non-fanned-out posts do
// show up, because they are merged on read.
func (s *Service) Follow(follower, followee string) error {
	// TODO(lab): validate, then record the edge in both directions.
	return errNotImplemented
}

// Unfollow reverses Follow and removes followee's entries from follower's
// cached timeline, so the next Feed no longer shows them. Idempotent.
func (s *Service) Unfollow(follower, followee string) error {
	// TODO(lab): remove the edge and filter the follower's timeline in place.
	return errNotImplemented
}

// Followers returns how many users follow user.
func (s *Service) Followers(user string) int {
	// TODO(lab)
	return 0
}

// Post creates a post. It is idempotent per (author, clientID): a retry with
// the same clientID returns the original post and performs no fan-out. An
// empty clientID disables deduplication. The author's follower count at this
// moment decides fan-out on write (below CelebrityThreshold) versus merge on
// read (at or above it).
//
// IDs: use a counter formatted to a fixed width (e.g. fmt.Sprintf("p%012d",
// n)) so that Before's ID tiebreak follows creation order. Take CreatedAt
// from s.cfg.Now(). Count every timeline insertion in Stats.FanoutWrites.
func (s *Service) Post(ctx context.Context, author, text, clientID string) (Post, error) {
	// TODO(lab): ctx.Err() first; validate; dedupe by (author, clientID);
	// create the post; then either push an entry into every follower's
	// timeline (bounded to MaxTimeline, newest first) or into the author's
	// pull list.
	return Post{}, errNotImplemented
}

// Delete removes the author's post. Timelines are not scanned: entries that
// no longer hydrate are skipped by Feed. ErrNotFound if the post does not
// exist or belongs to someone else (a 404, not a 403, so IDs are not probed).
func (s *Service) Delete(author, postID string) error {
	// TODO(lab)
	return errNotImplemented
}

// Feed returns one page of user's home feed: posts by the accounts they
// follow, newest first, strictly after cursor ("" = from the top). limit 0
// means DefaultLimit. The page is a k-way merge of the user's cached
// timeline and the pull lists of every celebrity they follow; entries whose
// post has been deleted are skipped and do not count toward limit.
//
// Validate limit and cursor before touching any state. Use limit+1 logic (or
// a look-ahead) so NextCursor is set only when another live post exists, and
// never return an empty page with a cursor.
func (s *Service) Feed(user, cursor string, limit int) (Page, error) {
	// TODO(lab): k-way merge newest first; skip entries at or before the
	// cursor (sort.Search on each source) and entries whose post is gone.
	return Page{}, errNotImplemented
}

// Timeline returns the post IDs currently in user's cached timeline, newest
// first (fanned-out entries only, including ones whose post was deleted).
// For tests and break-it experiments.
func (s *Service) Timeline(user string) []string {
	// TODO(lab)
	return nil
}

// Stats returns a snapshot of the counters.
func (s *Service) Stats() Stats {
	// TODO(lab)
	return Stats{}
}
