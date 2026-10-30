package feed

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

// Every test that could block (a deadlock in your locking, a loop that never
// terminates) is wrapped in tDone, so a wrong implementation fails with a
// message instead of hanging the gate run.
const testTimeout = 5 * time.Second

// ---------- helpers (prefixed with t to avoid clashing with your code) ----------

// tClock is an injectable clock. Tests advance it explicitly, so several posts
// can share one timestamp (the ID tiebreak must then order them) or be spaced
// out deterministically.
type tClock struct {
	mu sync.Mutex
	t  time.Time
}

func newTClock() *tClock { return &tClock{t: time.Date(2026, 3, 2, 9, 0, 0, 0, time.UTC)} }

func (c *tClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *tClock) advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// tDone runs f and fails the test if it has not returned within testTimeout.
func tDone(t *testing.T, what string, f func()) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		f()
	}()
	select {
	case <-done:
	case <-time.After(testTimeout):
		t.Fatalf("%s did not finish within %v: a deadlock (RLock inside Lock?) or an unbounded loop in Feed", what, testTimeout)
	}
}

func tNew(t *testing.T, cfg Config) *Service {
	t.Helper()
	s := New(cfg)
	if s == nil {
		t.Fatal("New returned nil")
	}
	return s
}

func tFollow(t *testing.T, s *Service, follower string, followees ...string) {
	t.Helper()
	for _, f := range followees {
		if err := s.Follow(follower, f); err != nil {
			t.Fatalf("Follow(%s, %s): %v", follower, f, err)
		}
	}
}

func tPost(t *testing.T, s *Service, author, text string) Post {
	t.Helper()
	p, err := s.Post(context.Background(), author, text, "")
	if err != nil {
		t.Fatalf("Post(%s, %q): %v", author, text, err)
	}
	if p.ID == "" || p.Author != author || p.Text != text {
		t.Fatalf("Post(%s, %q) returned %+v; want a non-empty ID and the given author and text", author, text, p)
	}
	return p
}

func tIDs(items []Post) []string {
	out := make([]string, len(items))
	for i, p := range items {
		out[i] = p.ID
	}
	return out
}

func tTexts(items []Post) []string {
	out := make([]string, len(items))
	for i, p := range items {
		out[i] = p.Text
	}
	return out
}

func tContains(ids []string, id string) bool {
	for _, x := range ids {
		if x == id {
			return true
		}
	}
	return false
}

// tFeed fetches one page and checks the basic page contract.
func tFeed(t *testing.T, s *Service, user, cursor string, limit int) Page {
	t.Helper()
	var pg Page
	var err error
	tDone(t, "Feed", func() { pg, err = s.Feed(user, cursor, limit) })
	if err != nil {
		t.Fatalf("Feed(%s, %q, %d): %v", user, cursor, limit, err)
	}
	if pg.Items == nil {
		t.Fatalf("Feed(%s) returned a nil Items slice; JSON clients get null instead of []", user)
	}
	want := limit
	if want == 0 {
		want = DefaultLimit
	}
	if len(pg.Items) > want {
		t.Fatalf("Feed(%s, limit %d) returned %d items", user, want, len(pg.Items))
	}
	if len(pg.Items) == 0 && pg.NextCursor != "" {
		t.Fatalf("Feed(%s) returned an empty page with a NextCursor; clients would loop forever", user)
	}
	tCheckStrictOrder(t, pg.Items)
	return pg
}

// tWalk pages through user's feed, calling between(page, items) after each
// page that has a NextCursor, and returns everything seen in order.
func tWalk(t *testing.T, s *Service, user string, limit int, between func(page int, items []Post)) []Post {
	t.Helper()
	var all []Post
	cursor := ""
	for page := 1; page <= 10000; page++ {
		pg := tFeed(t, s, user, cursor, limit)
		all = append(all, pg.Items...)
		if pg.NextCursor == "" {
			return all
		}
		if between != nil {
			between(page, pg.Items)
		}
		cursor = pg.NextCursor
	}
	t.Fatalf("feed walk for %s did not terminate after 10000 pages", user)
	return nil
}

// tCheckStrictOrder fails unless items are strictly in feed order, which also
// rules out duplicates.
func tCheckStrictOrder(t *testing.T, items []Post) {
	t.Helper()
	for i := 1; i < len(items); i++ {
		if !Before(KeyOf(items[i-1]), KeyOf(items[i])) {
			t.Fatalf("items out of order (or duplicated) at %d: %s (%v) then %s (%v)",
				i, items[i-1].ID, items[i-1].CreatedAt, items[i].ID, items[i].CreatedAt)
		}
	}
}

func tEqual(t *testing.T, what string, got, want []string) {
	t.Helper()
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("%s:\n got  %v\n want %v", what, got, want)
	}
}

// ---------- tests ----------

func TestNew_AppliesDefaults(t *testing.T) {
	s := tNew(t, Config{})
	cfg := s.Config()
	if cfg.MaxTimeline != DefaultMaxTimeline || cfg.CelebrityThreshold != DefaultCelebrityThreshold || cfg.Now == nil {
		t.Fatalf("Config() = %+v; want MaxTimeline %d, CelebrityThreshold %d and a clock", cfg, DefaultMaxTimeline, DefaultCelebrityThreshold)
	}
}

func TestCursor_RoundTripAndRejectsGarbage(t *testing.T) {
	k := Key{CreatedAt: time.Date(2026, 3, 2, 9, 0, 0, 123456789, time.UTC), ID: "p000000000042"}
	c := EncodeCursor(k)
	if strings.Contains(c, k.ID) || strings.ContainsAny(c, "+/=") {
		t.Fatalf("cursor %q is not opaque and URL-safe", c)
	}
	got, err := DecodeCursor(c)
	if err != nil || !got.CreatedAt.Equal(k.CreatedAt) || got.ID != k.ID {
		t.Fatalf("DecodeCursor(EncodeCursor(k)) = %+v, %v; want %+v", got, err, k)
	}
	for _, bad := range []string{"garbage!", "", "MTIz", c[:len(c)/2]} {
		if bad == "" {
			continue
		}
		if _, err := DecodeCursor(bad); !errors.Is(err, ErrInvalidCursor) {
			t.Errorf("DecodeCursor(%q) = %v; want ErrInvalidCursor", bad, err)
		}
	}
}

func TestFollow_ValidationAndIdempotence(t *testing.T) {
	s := tNew(t, Config{})
	for _, c := range [][2]string{{"alice", "alice"}, {"", "bob"}, {"alice", ""}} {
		if err := s.Follow(c[0], c[1]); !errors.Is(err, ErrInvalidArgument) {
			t.Errorf("Follow(%q, %q) = %v; want ErrInvalidArgument", c[0], c[1], err)
		}
	}
	tFollow(t, s, "alice", "bob", "bob", "bob")
	tFollow(t, s, "carol", "bob")
	if n := s.Followers("bob"); n != 2 {
		t.Fatalf("Followers(bob) = %d after alice (3×) and carol followed; want 2 (Follow must be idempotent)", n)
	}
	if err := s.Unfollow("dave", "bob"); err != nil {
		t.Fatalf("Unfollow of a non-existent edge = %v; want nil (idempotent)", err)
	}
	if err := s.Unfollow("alice", "bob"); err != nil {
		t.Fatalf("Unfollow: %v", err)
	}
	if n := s.Followers("bob"); n != 1 {
		t.Fatalf("Followers(bob) = %d after alice unfollowed; want 1", n)
	}
}

func TestPost_RejectsEmptyAuthorOrText(t *testing.T) {
	s := tNew(t, Config{})
	ctx := context.Background()
	if _, err := s.Post(ctx, "", "hi", ""); !errors.Is(err, ErrInvalidArgument) {
		t.Errorf("Post with empty author = %v; want ErrInvalidArgument", err)
	}
	if _, err := s.Post(ctx, "alice", "   ", ""); !errors.Is(err, ErrInvalidArgument) {
		t.Errorf("Post with blank text = %v; want ErrInvalidArgument", err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := s.Post(cancelled, "alice", "hi", ""); !errors.Is(err, context.Canceled) {
		t.Errorf("Post with a cancelled context = %v; want context.Canceled", err)
	}
	if st := s.Stats(); st.Posts != 0 {
		t.Errorf("Stats().Posts = %d after only rejected posts; want 0", st.Posts)
	}
}

func TestPost_FansOutToEveryFollowerAndNobodyElse(t *testing.T) {
	s := tNew(t, Config{})
	tFollow(t, s, "f1", "alice")
	tFollow(t, s, "f2", "alice")
	tFollow(t, s, "f3", "alice", "bob")
	tFollow(t, s, "bystander", "bob")
	p := tPost(t, s, "alice", "hello")
	for _, f := range []string{"f1", "f2", "f3"} {
		if tl := s.Timeline(f); !tContains(tl, p.ID) {
			t.Errorf("Timeline(%s) = %v; want it to contain %s (fan-out on write)", f, tl, p.ID)
		}
		pg := tFeed(t, s, f, "", 10)
		tEqual(t, "Feed("+f+")", tIDs(pg.Items), []string{p.ID})
	}
	for _, u := range []string{"bystander", "alice", "nobody"} {
		if tl := s.Timeline(u); len(tl) != 0 {
			t.Errorf("Timeline(%s) = %v; want empty (not a follower)", u, tl)
		}
	}
	if st := s.Stats(); st.Posts != 1 || st.FanoutWrites != 3 {
		t.Errorf("Stats() = %+v; want Posts 1, FanoutWrites 3 (one insertion per follower)", st)
	}
}

func TestPost_DuplicateClientIDReturnsSamePost(t *testing.T) {
	s := tNew(t, Config{})
	tFollow(t, s, "f1", "alice")
	ctx := context.Background()
	p1, err := s.Post(ctx, "alice", "pay rent", "req-1")
	if err != nil {
		t.Fatal(err)
	}
	p2, err := s.Post(ctx, "alice", "pay rent", "req-1") // the client retried
	if err != nil {
		t.Fatal(err)
	}
	if p1.ID != p2.ID {
		t.Fatalf("a retried Post with the same clientID created a second post (%s then %s); the member sees it twice", p1.ID, p2.ID)
	}
	if tl := s.Timeline("f1"); len(tl) != 1 {
		t.Fatalf("Timeline(f1) = %v after a retried post; want exactly one entry", tl)
	}
	p3, err := s.Post(ctx, "alice", "pay rent", "req-2")
	if err != nil {
		t.Fatal(err)
	}
	p4, err := s.Post(ctx, "bob", "pay rent", "req-1") // same clientID, different author
	if err != nil {
		t.Fatal(err)
	}
	if p3.ID == p1.ID || p4.ID == p1.ID || p3.ID == p4.ID {
		t.Fatalf("different (author, clientID) pairs must create distinct posts: %s %s %s", p1.ID, p3.ID, p4.ID)
	}
	if st := s.Stats(); st.Posts != 3 || st.FanoutWrites != 2 {
		t.Fatalf("Stats() = %+v; want Posts 3 and FanoutWrites 2 (the retry must not fan out again)", st)
	}
}

func TestPost_CelebrityIsNotFannedOutButAppearsInFeeds(t *testing.T) {
	s := tNew(t, Config{CelebrityThreshold: 3})
	tFollow(t, s, "f1", "star", "alice")
	tFollow(t, s, "f2", "star", "alice")
	tFollow(t, s, "f3", "star") // star has 3 followers (a celebrity), alice 2 (ordinary)
	ps := tPost(t, s, "star", "celebrity post")
	pa := tPost(t, s, "alice", "ordinary post")
	for _, f := range []string{"f1", "f2", "f3"} {
		tl := s.Timeline(f)
		if tContains(tl, ps.ID) {
			t.Errorf("Timeline(%s) contains the celebrity's post %s; authors at or above the threshold must not be fanned out", f, ps.ID)
		}
		if f != "f3" && !tContains(tl, pa.ID) {
			t.Errorf("Timeline(%s) = %v; want the ordinary author's post %s", f, tl, pa.ID)
		}
		pg := tFeed(t, s, f, "", 10)
		want := []string{pa.ID, ps.ID}
		if f == "f3" {
			want = []string{ps.ID}
		}
		tEqual(t, "Feed("+f+")", tIDs(pg.Items), want)
	}
	if st := s.Stats(); st.FanoutWrites != 2 {
		t.Errorf("Stats().FanoutWrites = %d; want 2 (only alice's post is fanned out, to f1 and f2)", st.FanoutWrites)
	}
	// A late follower of the celebrity still sees the merged post.
	tFollow(t, s, "late", "star")
	pg := tFeed(t, s, "late", "", 10)
	tEqual(t, "Feed(late)", tIDs(pg.Items), []string{ps.ID})
}

func TestPost_ThresholdCrossingNeitherDuplicatesNorLosesPosts(t *testing.T) {
	clock := newTClock()
	s := tNew(t, Config{CelebrityThreshold: 2, Now: clock.now})
	tFollow(t, s, "f1", "alice")
	p1 := tPost(t, s, "alice", "1: fanned out (1 follower)")
	clock.advance(time.Second)
	tFollow(t, s, "f2", "alice") // alice now has 2 followers: a celebrity
	p2 := tPost(t, s, "alice", "2: merged on read")
	clock.advance(time.Second)
	if err := s.Unfollow("f2", "alice"); err != nil {
		t.Fatal(err)
	}
	p3 := tPost(t, s, "alice", "3: fanned out again")
	pg := tFeed(t, s, "f1", "", 10)
	tEqual(t, "Feed(f1) after crossing the threshold both ways", tIDs(pg.Items), []string{p3.ID, p2.ID, p1.ID})
	if tl := s.Timeline("f1"); !tContains(tl, p1.ID) || tContains(tl, p2.ID) || !tContains(tl, p3.ID) {
		t.Errorf("Timeline(f1) = %v; want p1 and p3 (fanned out) but not p2 (merged on read)", tl)
	}
	// f2 followed after p1 was fanned out (no backfill) and unfollowed before p3.
	pg = tFeed(t, s, "f2", "", 10)
	tEqual(t, "Feed(f2) after unfollowing", tIDs(pg.Items), nil)
}

func TestFeed_NewestFirstWithIDTieBreak(t *testing.T) {
	clock := newTClock()
	s := tNew(t, Config{Now: clock.now})
	tFollow(t, s, "reader", "alice", "bob")
	// Same instant for all three: order must fall back to ID, newest ID first.
	a1 := tPost(t, s, "alice", "a1")
	b1 := tPost(t, s, "bob", "b1")
	a2 := tPost(t, s, "alice", "a2")
	clock.advance(time.Millisecond)
	b2 := tPost(t, s, "bob", "b2")
	pg := tFeed(t, s, "reader", "", 10)
	tEqual(t, "Feed(reader)", tIDs(pg.Items), []string{b2.ID, a2.ID, b1.ID, a1.ID})
	if !pg.Items[0].CreatedAt.After(pg.Items[1].CreatedAt) || !pg.Items[1].CreatedAt.Equal(pg.Items[3].CreatedAt) {
		t.Fatalf("CreatedAt must come from Config.Now: got %v", pg.Items)
	}
}

func TestFeed_MergesTimelineAndCelebritySourcesInGlobalOrder(t *testing.T) {
	clock := newTClock()
	s := tNew(t, Config{CelebrityThreshold: 2, Now: clock.now})
	tFollow(t, s, "reader", "alice", "star", "mega")
	tFollow(t, s, "other", "star", "mega")
	var want []string
	for i := 0; i < 12; i++ {
		clock.advance(time.Second)
		author := []string{"alice", "star", "mega", "star"}[i%4]
		want = append([]string{tPost(t, s, author, fmt.Sprintf("%s-%d", author, i)).ID}, want...)
	}
	if tl := s.Timeline("reader"); len(tl) != 3 {
		t.Fatalf("Timeline(reader) = %v; want only alice's 3 posts (star and mega are celebrities)", tl)
	}
	pg := tFeed(t, s, "reader", "", 20)
	tEqual(t, "Feed(reader) merged across three sources", tIDs(pg.Items), want)
	if pg.NextCursor != "" {
		t.Fatalf("NextCursor = %q on a page that held everything; want empty", pg.NextCursor)
	}
}

func TestFeed_PagesWithoutDuplicatesOrSkipsUnderMutation(t *testing.T) {
	clock := newTClock()
	s := tNew(t, Config{CelebrityThreshold: 2, Now: clock.now})
	tFollow(t, s, "reader", "alice", "bob", "star")
	tFollow(t, s, "other", "star")
	var original []string
	for i := 0; i < 23; i++ {
		if i%3 == 0 {
			clock.advance(time.Second) // groups of ~3 posts share a timestamp
		}
		author := []string{"alice", "bob", "star"}[i%3]
		original = append([]string{tPost(t, s, author, fmt.Sprintf("%s-%d", author, i)).ID}, original...)
	}
	deletedUnseen := original[len(original)-2] // near the end of the walk, not yet seen on page 1
	var pages int
	seen := tWalk(t, s, "reader", 4, func(page int, items []Post) {
		pages++
		// New posts arrive at the top while the reader pages: they must never
		// show up mid-walk as duplicates of what was already served.
		clock.advance(time.Second)
		tPost(t, s, "alice", fmt.Sprintf("new-%d", page))
		tPost(t, s, "star", fmt.Sprintf("newstar-%d", page))
		if page == 1 {
			// Delete a post the reader has already seen and one they have not.
			if err := s.Delete(items[1].Author, items[1].ID); err != nil {
				t.Fatalf("Delete(%s): %v", items[1].ID, err)
			}
			if err := s.Delete("bob", deletedUnseen); err != nil {
				t.Fatalf("Delete(%s): %v", deletedUnseen, err)
			}
		}
	})
	var want []string
	for _, id := range original {
		if id != deletedUnseen {
			want = append(want, id)
		}
	}
	tEqual(t, "walk with limit 4 while posts were added and deleted", tIDs(seen), want)
	if pages < 5 {
		t.Fatalf("walk used %d pages for %d items with limit 4; cursor paging is not limiting pages", pages+1, len(want))
	}
	// Pages must stay full: a deleted entry must be skipped, not count toward limit.
	first := tFeed(t, s, "reader", "", 4)
	if len(first.Items) != 4 {
		t.Fatalf("first page has %d items after a deletion; want 4 (skip deleted entries before counting)", len(first.Items))
	}
}

func TestFeed_ValidatesCursorAndLimit(t *testing.T) {
	s := tNew(t, Config{})
	tFollow(t, s, "reader", "alice")
	tPost(t, s, "alice", "x")
	if _, err := s.Feed("reader", "not-a-cursor!", 10); !errors.Is(err, ErrInvalidCursor) {
		t.Errorf("Feed with garbage cursor = %v; want ErrInvalidCursor", err)
	}
	for _, l := range []int{-1, MaxLimit + 1, 10_000} {
		if _, err := s.Feed("reader", "", l); !errors.Is(err, ErrInvalidLimit) {
			t.Errorf("Feed with limit %d = %v; want ErrInvalidLimit", l, err)
		}
	}
	if _, err := s.Feed("", "", 10); !errors.Is(err, ErrInvalidArgument) {
		t.Errorf("Feed for an empty user = %v; want ErrInvalidArgument", err)
	}
	pg := tFeed(t, s, "reader", "", 0)
	if len(pg.Items) != 1 {
		t.Errorf("Feed with limit 0 returned %d items; want DefaultLimit behaviour", len(pg.Items))
	}
	for i := 0; i < DefaultLimit+5; i++ {
		tPost(t, s, "alice", fmt.Sprintf("x%d", i))
	}
	pg = tFeed(t, s, "reader", "", 0)
	if len(pg.Items) != DefaultLimit || pg.NextCursor == "" {
		t.Errorf("Feed with limit 0 returned %d items and cursor %q; want %d items and a cursor", len(pg.Items), pg.NextCursor, DefaultLimit)
	}
}

func TestFeed_DepthIsBoundedByMaxTimeline(t *testing.T) {
	clock := newTClock()
	s := tNew(t, Config{MaxTimeline: 5, Now: clock.now})
	tFollow(t, s, "reader", "alice")
	var ids []string
	for i := 0; i < 10; i++ {
		clock.advance(time.Second)
		ids = append([]string{tPost(t, s, "alice", fmt.Sprintf("%d", i)).ID}, ids...)
	}
	tEqual(t, "Timeline(reader)", s.Timeline("reader"), ids[:5])
	seen := tWalk(t, s, "reader", 2, nil)
	tEqual(t, "walk of a bounded timeline", tIDs(seen), ids[:5])
}

func TestFeed_UnknownUserGetsEmptyNonNilPage(t *testing.T) {
	s := tNew(t, Config{})
	pg := tFeed(t, s, "nobody", "", 10)
	if len(pg.Items) != 0 || pg.NextCursor != "" {
		t.Fatalf("Feed(nobody) = %+v; want an empty page", pg)
	}
	tFollow(t, s, "lurker", "quiet")
	pg = tFeed(t, s, "lurker", "", 10)
	if len(pg.Items) != 0 {
		t.Fatalf("Feed(lurker) = %+v; want empty (the followee never posted)", pg)
	}
}

func TestDelete_DeletedPostsAreNeverServedAndPagesStayFull(t *testing.T) {
	clock := newTClock()
	s := tNew(t, Config{CelebrityThreshold: 2, Now: clock.now})
	tFollow(t, s, "reader", "alice", "star")
	tFollow(t, s, "other", "star")
	var ps []Post
	for i := 1; i <= 6; i++ {
		clock.advance(time.Second)
		ps = append(ps, tPost(t, s, "alice", fmt.Sprintf("p%d", i)))
	}
	clock.advance(time.Second)
	starPost := tPost(t, s, "star", "star post")
	if err := s.Delete("mallory", ps[4].ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Delete by a non-author = %v; want ErrNotFound", err)
	}
	if err := s.Delete("alice", "p999999999999"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Delete of an unknown post = %v; want ErrNotFound", err)
	}
	for _, p := range []Post{ps[4], ps[1], starPost} {
		if err := s.Delete(p.Author, p.ID); err != nil {
			t.Fatalf("Delete(%s): %v", p.ID, err)
		}
	}
	if err := s.Delete("alice", ps[4].ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second Delete of the same post = %v; want ErrNotFound", err)
	}
	pg := tFeed(t, s, "reader", "", 3)
	tEqual(t, "first page after deletes", tTexts(pg.Items), []string{"p6", "p4", "p3"})
	if pg.NextCursor == "" {
		t.Fatal("NextCursor empty although p1 is still unread")
	}
	pg2 := tFeed(t, s, "reader", pg.NextCursor, 3)
	tEqual(t, "second page after deletes", tTexts(pg2.Items), []string{"p1"})
	if pg2.NextCursor != "" {
		t.Fatalf("NextCursor = %q on the last page; want empty", pg2.NextCursor)
	}
}

func TestUnfollow_RemovesThatAuthorsPostsFromFeed(t *testing.T) {
	clock := newTClock()
	s := tNew(t, Config{CelebrityThreshold: 2, Now: clock.now})
	tFollow(t, s, "reader", "alice", "bob", "star")
	tFollow(t, s, "other", "star")
	var want []string
	for i := 0; i < 9; i++ {
		clock.advance(time.Second)
		author := []string{"alice", "bob", "star"}[i%3]
		p := tPost(t, s, author, fmt.Sprintf("%s-%d", author, i))
		if author == "bob" {
			want = append([]string{p.ID}, want...)
		}
	}
	if err := s.Unfollow("reader", "alice"); err != nil {
		t.Fatal(err)
	}
	if err := s.Unfollow("reader", "star"); err != nil {
		t.Fatal(err)
	}
	pg := tFeed(t, s, "reader", "", 20)
	tEqual(t, "Feed(reader) after unfollowing alice (fanned out) and star (merged)", tIDs(pg.Items), want)
	if tl := s.Timeline("reader"); len(tl) != 3 {
		t.Fatalf("Timeline(reader) = %v; want only bob's 3 entries (Unfollow must remove alice's eagerly)", tl)
	}
}

func TestConcurrent_PostsFollowsAndReadsUnderRace(t *testing.T) {
	s := tNew(t, Config{CelebrityThreshold: 5})
	tFollow(t, s, "reader", "a1", "a2", "a3", "star")
	for i := 0; i < 5; i++ {
		tFollow(t, s, fmt.Sprintf("fan%d", i), "star")
	}
	const perAuthor = 50
	tDone(t, "concurrent posts, follows and reads", func() {
		var wg sync.WaitGroup
		for _, author := range []string{"a1", "a2", "a3", "star"} {
			wg.Add(1)
			go func(author string) {
				defer wg.Done()
				for i := 0; i < perAuthor; i++ {
					if _, err := s.Post(context.Background(), author, fmt.Sprintf("%s-%d", author, i), fmt.Sprintf("c%d", i)); err != nil {
						t.Errorf("Post(%s): %v", author, err)
						return
					}
				}
			}(author)
		}
		for g := 0; g < 2; g++ {
			wg.Add(1)
			go func(g int) {
				defer wg.Done()
				u := fmt.Sprintf("churn%d", g)
				for i := 0; i < 100; i++ {
					if err := s.Follow(u, "a1"); err != nil {
						t.Errorf("Follow: %v", err)
					}
					if err := s.Unfollow(u, "a1"); err != nil {
						t.Errorf("Unfollow: %v", err)
					}
				}
			}(g)
		}
		for r := 0; r < 3; r++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for i := 0; i < 100; i++ {
					pg, err := s.Feed("reader", "", 20)
					if err != nil {
						t.Errorf("Feed: %v", err)
						return
					}
					tCheckStrictOrder(t, pg.Items)
					_ = s.Timeline("reader")
					_ = s.Stats()
				}
			}()
		}
		wg.Wait()
	})
	seen := tWalk(t, s, "reader", 7, nil)
	if len(seen) != 4*perAuthor {
		t.Fatalf("after all writers finished the walk saw %d posts; want %d (every post exactly once)", len(seen), 4*perAuthor)
	}
	uniq := map[string]bool{}
	for _, p := range seen {
		uniq[p.ID] = true
	}
	if len(uniq) != 4*perAuthor {
		t.Fatalf("walk returned duplicates: %d distinct of %d", len(uniq), len(seen))
	}
	st := s.Stats()
	if st.Posts != 4*perAuthor || st.FanoutWrites < 3*perAuthor {
		t.Fatalf("Stats() = %+v; want Posts %d and FanoutWrites ≥ %d (reader follows three ordinary authors)", st, 4*perAuthor, 3*perAuthor)
	}
	if n := len(s.Timeline("reader")); n != 3*perAuthor {
		t.Fatalf("Timeline(reader) has %d entries; want %d (star is a celebrity and must not be fanned out)", n, 3*perAuthor)
	}
}
