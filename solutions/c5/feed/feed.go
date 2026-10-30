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
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
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

// entry is what a timeline or pull list stores: just enough to order and
// hydrate. The author is kept so Unfollow can remove that author's entries.
type entry struct {
	key    Key
	author string
}

// Service is the in-memory feed service. All methods are safe for concurrent
// use.
type Service struct {
	cfg Config
	mu  sync.RWMutex

	seq       int64
	posts     map[string]Post                // id → post (deleted posts are removed)
	byClient  map[string]Post                // author + "\x00" + clientID → the post created for it
	followers map[string]map[string]struct{} // author → followers
	following map[string]map[string]struct{} // user → followees
	timelines map[string][]entry             // user → fanned-out entries, newest first, ≤ MaxTimeline
	pull      map[string][]entry             // author → entries NOT fanned out, newest first

	posted  atomic.Int64
	fanouts atomic.Int64
	reads   atomic.Int64
}

// New returns a Service with defaults applied.
func New(cfg Config) *Service {
	return &Service{
		cfg:       cfg.withDefaults(),
		posts:     map[string]Post{},
		byClient:  map[string]Post{},
		followers: map[string]map[string]struct{}{},
		following: map[string]map[string]struct{}{},
		timelines: map[string][]entry{},
		pull:      map[string][]entry{},
	}
}

// Config returns the effective configuration. Provided.
func (s *Service) Config() Config { return s.cfg }

// Follow makes follower receive followee's posts from now on. Idempotent.
// Following yourself or an empty user is ErrInvalidArgument. Follow does not
// backfill: posts made before the follow do not appear in the timeline (the
// lesson discusses the trade-off), but a celebrity's non-fanned-out posts do
// show up, because they are merged on read.
func (s *Service) Follow(follower, followee string) error {
	if follower == "" || followee == "" || follower == followee {
		return ErrInvalidArgument
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.following[follower] == nil {
		s.following[follower] = map[string]struct{}{}
	}
	if s.followers[followee] == nil {
		s.followers[followee] = map[string]struct{}{}
	}
	s.following[follower][followee] = struct{}{}
	s.followers[followee][follower] = struct{}{}
	return nil
}

// Unfollow reverses Follow and removes followee's entries from follower's
// cached timeline, so the next Feed no longer shows them. Idempotent.
func (s *Service) Unfollow(follower, followee string) error {
	if follower == "" || followee == "" || follower == followee {
		return ErrInvalidArgument
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.following[follower][followee]; !ok {
		return nil
	}
	delete(s.following[follower], followee)
	delete(s.followers[followee], follower)
	tl := s.timelines[follower]
	kept := tl[:0]
	for _, e := range tl {
		if e.author != followee {
			kept = append(kept, e)
		}
	}
	for i := len(kept); i < len(tl); i++ {
		tl[i] = entry{}
	}
	s.timelines[follower] = kept
	return nil
}

// Followers returns how many users follow user.
func (s *Service) Followers(user string) int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.followers[user])
}

// Post creates a post. It is idempotent per (author, clientID): a retry with
// the same clientID returns the original post and performs no fan-out. An
// empty clientID disables deduplication. The author's follower count at this
// moment decides fan-out on write (below CelebrityThreshold) versus merge on
// read (at or above it).
func (s *Service) Post(ctx context.Context, author, text, clientID string) (Post, error) {
	if err := ctx.Err(); err != nil {
		return Post{}, err
	}
	if author == "" || strings.TrimSpace(text) == "" {
		return Post{}, ErrInvalidArgument
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	ck := author + "\x00" + clientID
	if clientID != "" {
		if p, ok := s.byClient[ck]; ok {
			return p, nil
		}
	}
	s.seq++
	p := Post{ID: fmt.Sprintf("p%012d", s.seq), Author: author, Text: text, CreatedAt: s.cfg.Now()}
	s.posts[p.ID] = p
	if clientID != "" {
		s.byClient[ck] = p
	}
	s.posted.Add(1)
	e := entry{key: KeyOf(p), author: author}
	if len(s.followers[author]) >= s.cfg.CelebrityThreshold {
		s.pull[author] = insertSorted(s.pull[author], e, 0)
		return p, nil
	}
	for f := range s.followers[author] {
		s.timelines[f] = insertSorted(s.timelines[f], e, s.cfg.MaxTimeline)
		s.fanouts.Add(1)
	}
	return p, nil
}

// Delete removes the author's post. Timelines are not scanned: entries that
// no longer hydrate are skipped by Feed. ErrNotFound if the post does not
// exist or belongs to someone else (a 404, not a 403, so IDs are not probed).
func (s *Service) Delete(author, postID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.posts[postID]
	if !ok || p.Author != author {
		return ErrNotFound
	}
	delete(s.posts, postID)
	pl := s.pull[author]
	for i, e := range pl {
		if e.key.ID == postID {
			s.pull[author] = append(pl[:i:i], pl[i+1:]...)
			break
		}
	}
	return nil
}

// Feed returns one page of user's home feed: posts by the accounts they
// follow, newest first, strictly after cursor ("" = from the top). limit 0
// means DefaultLimit. The page is a k-way merge of the user's cached
// timeline and the pull lists of every celebrity they follow; entries whose
// post has been deleted are skipped and do not count toward limit.
func (s *Service) Feed(user, cursor string, limit int) (Page, error) {
	if limit == 0 {
		limit = DefaultLimit
	}
	if limit < 0 || limit > MaxLimit {
		return Page{}, ErrInvalidLimit
	}
	var after *Key
	if cursor != "" {
		k, err := DecodeCursor(cursor)
		if err != nil {
			return Page{}, err
		}
		after = &k
	}
	if user == "" {
		return Page{}, ErrInvalidArgument
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	sources := make([][]entry, 0, 1+len(s.following[user]))
	if tl := s.timelines[user]; len(tl) > 0 {
		sources = append(sources, tl)
	}
	for f := range s.following[user] {
		if pl := s.pull[f]; len(pl) > 0 {
			sources = append(sources, pl)
		}
	}
	pos := make([]int, len(sources))
	if after != nil {
		for i, src := range sources {
			pos[i] = sort.Search(len(src), func(j int) bool { return Before(*after, src[j].key) })
		}
	}
	items := make([]Post, 0, limit)
	var next string
	for {
		best := -1
		for i, src := range sources {
			if pos[i] >= len(src) {
				continue
			}
			if best < 0 || Before(src[pos[i]].key, sources[best][pos[best]].key) {
				best = i
			}
		}
		if best < 0 {
			break
		}
		e := sources[best][pos[best]]
		pos[best]++
		p, ok := s.posts[e.key.ID]
		if !ok {
			continue // deleted since it was fanned out
		}
		if len(items) == limit {
			next = EncodeCursor(KeyOf(items[limit-1]))
			break
		}
		items = append(items, p)
	}
	s.reads.Add(1)
	return Page{Items: items, NextCursor: next}, nil
}

// Timeline returns the post IDs currently in user's cached timeline, newest
// first (fanned-out entries only, including ones whose post was deleted).
// For tests and break-it experiments.
func (s *Service) Timeline(user string) []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	tl := s.timelines[user]
	out := make([]string, len(tl))
	for i, e := range tl {
		out[i] = e.key.ID
	}
	return out
}

// Stats returns a snapshot of the counters.
func (s *Service) Stats() Stats {
	return Stats{Posts: s.posted.Load(), FanoutWrites: s.fanouts.Load(), FeedReads: s.reads.Load()}
}

// insertSorted inserts e into list (sorted by Before, newest first) and,
// when max > 0, drops the oldest entries beyond max.
func insertSorted(list []entry, e entry, max int) []entry {
	i := sort.Search(len(list), func(i int) bool { return Before(e.key, list[i].key) })
	list = append(list, entry{})
	copy(list[i+1:], list[i:])
	list[i] = e
	if max > 0 && len(list) > max {
		for j := max; j < len(list); j++ {
			list[j] = entry{}
		}
		list = list[:max]
	}
	return list
}
