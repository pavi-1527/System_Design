package pagination

import (
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

// ---------- helpers (prefixed with t to avoid clashing with your code) ----------

var tT0 = time.Date(2026, 3, 2, 9, 0, 0, 0, time.UTC)

var tSecret = []byte("test-secret-do-not-use-in-prod")

// tFill inserts n items "tx-000".."tx-(n-1)"; groupSize items share each
// timestamp (think: a batch of ACH postings stamped with the same second).
func tFill(t *testing.T, s *Store, n, groupSize int) {
	t.Helper()
	for i := 0; i < n; i++ {
		it := Item{ID: fmt.Sprintf("tx-%03d", i), CreatedAt: tT0.Add(time.Duration(i/groupSize) * time.Second)}
		if err := s.Insert(it); err != nil {
			t.Fatalf("Insert(%s): %v", it.ID, err)
		}
	}
}

func tIDs(items []Item) []string {
	out := make([]string, len(items))
	for i, it := range items {
		out[i] = it.ID
	}
	return out
}

// tWalk pages through p with limit, calling between(pageNo, lastPage) after
// each page that has a NextCursor. It returns every item seen, in order.
func tWalk(t *testing.T, p *Paginator, limit int, between func(page int, items []Item)) []Item {
	t.Helper()
	var all []Item
	cursor := ""
	for page := 1; page <= 10000; page++ {
		pg, err := p.List(cursor, limit)
		if err != nil {
			t.Fatalf("List(page %d): %v", page, err)
		}
		if len(pg.Items) > limit {
			t.Fatalf("page %d has %d items, limit %d", page, len(pg.Items), limit)
		}
		all = append(all, pg.Items...)
		if pg.NextCursor == "" {
			return all
		}
		if len(pg.Items) == 0 {
			t.Fatalf("page %d is empty but has a NextCursor; clients would loop forever", page)
		}
		if between != nil {
			between(page, pg.Items)
		}
		cursor = pg.NextCursor
	}
	t.Fatalf("pagination did not terminate after 10000 pages")
	return nil
}

// tCheckStrictOrder fails if items are not strictly in sort order (which also
// rules out duplicates).
func tCheckStrictOrder(t *testing.T, items []Item) {
	t.Helper()
	for i := 1; i < len(items); i++ {
		if !Before(KeyOf(items[i-1]), KeyOf(items[i])) {
			t.Fatalf("items %d (%s) and %d (%s) are out of order or duplicated", i-1, items[i-1].ID, i, items[i].ID)
		}
	}
}

func tCount(items []Item) map[string]int {
	m := map[string]int{}
	for _, it := range items {
		m[it.ID]++
	}
	return m
}

// tWait waits for wg, failing the test instead of hanging if it takes longer
// than d (e.g. a deadlock in the implementation under test).
func tWait(t *testing.T, wg *sync.WaitGroup, d time.Duration, what string) {
	t.Helper()
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(d):
		t.Fatalf("%s did not finish within %v (deadlock or livelock in the implementation?)", what, d)
	}
}

// ---------- store (provided code; these pass on the stub) ----------

func TestStore_SortsNewestFirstWithIDTiebreak(t *testing.T) {
	s := NewStore()
	_ = s.Insert(Item{ID: "a", CreatedAt: tT0})
	_ = s.Insert(Item{ID: "c", CreatedAt: tT0})
	_ = s.Insert(Item{ID: "b", CreatedAt: tT0.Add(time.Second)})
	_ = s.Insert(Item{ID: "d", CreatedAt: tT0.Add(-time.Second)})
	if err := s.Insert(Item{ID: "a", CreatedAt: tT0}); !errors.Is(err, ErrDuplicateID) {
		t.Fatalf("duplicate insert = %v, want ErrDuplicateID", err)
	}
	if !s.Delete("d") || s.Delete("d") || s.Len() != 3 {
		t.Fatalf("Delete/Len misbehave")
	}
}

// ---------- offset ----------

func TestOffset_ReturnsWindowInSortOrder(t *testing.T) {
	s := NewStore()
	tFill(t, s, 10, 1) // tx-009 is newest
	got := tIDs(s.Offset(3, 4))
	want := []string{"tx-006", "tx-005", "tx-004", "tx-003"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("Offset(3,4) = %v, want %v", got, want)
	}
	if got := s.Offset(8, 5); len(got) != 2 {
		t.Fatalf("Offset(8,5) returned %d items, want the last 2", len(got))
	}
	if got := s.Offset(10, 5); len(got) != 0 {
		t.Fatalf("Offset past the end returned %d items, want 0", len(got))
	}
	if s.Offset(-1, 5) != nil || s.Offset(0, 0) != nil {
		t.Fatalf("negative offset or non-positive limit must return nil")
	}
}

// The next two tests pin down *why* offset pagination is wrong for live
// data. They pass with a correct Offset implementation: the anomalies are the
// expected behaviour of offset paging, not bugs in your code.

func TestOffset_InsertAtFrontDuplicatesRows(t *testing.T) {
	s := NewStore()
	tFill(t, s, 20, 1)
	page1 := s.Offset(0, 5)
	// Two new transactions post while the member is reading page 1.
	_ = s.Insert(Item{ID: "new-1", CreatedAt: tT0.Add(time.Hour)})
	_ = s.Insert(Item{ID: "new-2", CreatedAt: tT0.Add(time.Hour + time.Second)})
	page2 := s.Offset(5, 5)
	seen := tCount(append(page1, page2...))
	dups := 0
	for _, n := range seen {
		if n > 1 {
			dups++
		}
	}
	if dups != 2 {
		t.Fatalf("offset paging after 2 inserts at the front showed %d duplicated rows, want exactly 2 (rows shift right by 2)", dups)
	}
}

func TestOffset_DeleteSeenRowSkipsRows(t *testing.T) {
	s := NewStore()
	tFill(t, s, 20, 1)
	page1 := s.Offset(0, 5)
	if len(page1) != 5 {
		t.Fatalf("Offset(0,5) returned %d items, want 5", len(page1))
	}
	s.Delete(page1[0].ID)
	s.Delete(page1[1].ID)
	page2 := s.Offset(5, 5)
	// Rows tx-014 and tx-013 (positions 5 and 6 before the delete) now sit at
	// positions 3 and 4 and are never returned.
	seen := tCount(append(page1, page2...))
	skipped := 0
	for _, id := range []string{"tx-014", "tx-013"} {
		if seen[id] == 0 {
			skipped++
		}
	}
	if skipped != 2 {
		t.Fatalf("offset paging after deleting 2 seen rows skipped %d rows, want exactly 2 (rows shift left by 2)", skipped)
	}
}

// ---------- keyset ----------

func TestAfter_SeeksStrictlyAfterKey(t *testing.T) {
	s := NewStore()
	tFill(t, s, 10, 3) // groups of 3 share a timestamp
	if got := tIDs(s.After(nil, 3)); strings.Join(got, ",") != "tx-009,tx-008,tx-007" {
		t.Fatalf("After(nil,3) = %v, want the 3 newest", got)
	}
	k := Key{CreatedAt: tT0.Add(2 * time.Second), ID: "tx-007"} // tx-006..008 share this timestamp
	if got := tIDs(s.After(&k, 3)); strings.Join(got, ",") != "tx-006,tx-005,tx-004" {
		t.Fatalf("After(tx-007,3) = %v, want [tx-006 tx-005 tx-004]: same timestamp, smaller ID comes next", got)
	}
	if s.After(&k, 0) != nil {
		t.Fatalf("After with limit 0 must return nil")
	}
}

func TestAfter_WorksWhenCursorItemWasDeleted(t *testing.T) {
	s := NewStore()
	tFill(t, s, 10, 1)
	k := KeyOf(Item{ID: "tx-005", CreatedAt: tT0.Add(5 * time.Second)})
	s.Delete("tx-005")
	if got := tIDs(s.After(&k, 2)); strings.Join(got, ",") != "tx-004,tx-003" {
		t.Fatalf("After(deleted tx-005) = %v, want [tx-004 tx-003]: a cursor is a position, not a row", got)
	}
}

var tURLSafe = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

func TestCursor_RoundTrip(t *testing.T) {
	keys := []Key{
		{CreatedAt: tT0, ID: "tx-001"},
		{CreatedAt: tT0.Add(123456789 * time.Nanosecond), ID: "café/член 7"},
		{CreatedAt: time.Date(1999, 12, 31, 23, 59, 59, 999999999, time.UTC), ID: ""},
	}
	for _, k := range keys {
		c := EncodeCursor(tSecret, k)
		if !tURLSafe.MatchString(c) {
			t.Errorf("cursor %q is not URL-safe base64 (no padding, no +/)", c)
		}
		got, err := DecodeCursor(tSecret, c)
		if err != nil {
			t.Fatalf("DecodeCursor(EncodeCursor(%v)): %v", k, err)
		}
		if !got.CreatedAt.Equal(k.CreatedAt) || got.ID != k.ID {
			t.Errorf("round trip: got %v, want %v (nanosecond precision matters: tied timestamps differ only in the tiebreak)", got, k)
		}
	}
}

func TestCursor_TamperingNeverYieldsADifferentKey(t *testing.T) {
	k := Key{CreatedAt: tT0.Add(42 * time.Second), ID: "tx-042"}
	c := EncodeCursor(tSecret, k)
	for i := 0; i < len(c); i++ {
		for _, r := range []byte{'A', 'z', '0', '_'} {
			if c[i] == r {
				continue
			}
			tampered := c[:i] + string(r) + c[i+1:]
			got, err := DecodeCursor(tSecret, tampered)
			if err == nil && (!got.CreatedAt.Equal(k.CreatedAt) || got.ID != k.ID) {
				t.Fatalf("tampered cursor (pos %d -> %q) decoded to a different key %v; a client could jump to arbitrary positions or probe other tenants' data", i, r, got)
			}
			if err != nil && !errors.Is(err, ErrInvalidCursor) {
				t.Fatalf("tampered cursor: error %v does not wrap ErrInvalidCursor", err)
			}
		}
	}
	for n := 0; n < len(c); n++ {
		if _, err := DecodeCursor(tSecret, c[:n]); !errors.Is(err, ErrInvalidCursor) {
			t.Fatalf("truncated cursor (len %d of %d) accepted: %v", n, len(c), err)
		}
	}
	if _, err := DecodeCursor([]byte("another-secret"), c); !errors.Is(err, ErrInvalidCursor) {
		t.Fatalf("cursor signed with a different secret accepted: %v", err)
	}
	for _, g := range []string{"", "abc", "!!!!", "eyJpZCI6InR4LTA0MiJ9", strings.Repeat("A", 200), c + "=="} {
		if _, err := DecodeCursor(tSecret, g); !errors.Is(err, ErrInvalidCursor) {
			t.Errorf("garbage cursor %q accepted: %v", g, err)
		}
	}
}

func TestList_WalksEveryItemExactlyOnceWithTiedTimestamps(t *testing.T) {
	for _, tc := range []struct{ n, group, limit, pages int }{
		{53, 5, 10, 6},
		{50, 7, 10, 5}, // exact multiple: no empty trailing page
		{3, 3, 10, 1},
	} {
		t.Run(fmt.Sprintf("n=%d,limit=%d", tc.n, tc.limit), func(t *testing.T) {
			s := NewStore()
			tFill(t, s, tc.n, tc.group)
			p := NewPaginator(s, tSecret)
			pages := 0
			all := tWalk(t, p, tc.limit, func(int, []Item) { pages++ })
			pages++ // the last page
			if len(all) != tc.n {
				t.Fatalf("walk returned %d items, want %d", len(all), tc.n)
			}
			tCheckStrictOrder(t, all)
			if pages != tc.pages {
				t.Fatalf("walk took %d pages, want %d (fetch limit+1 to know whether another page exists)", pages, tc.pages)
			}
		})
	}
}

func TestList_RejectsBadLimitAndCursor(t *testing.T) {
	s := NewStore()
	tFill(t, s, 5, 1)
	p := NewPaginator(s, tSecret)
	for _, l := range []int{0, -1, MaxLimit + 1} {
		if _, err := p.List("", l); !errors.Is(err, ErrInvalidLimit) {
			t.Errorf("List(limit=%d) = %v, want ErrInvalidLimit", l, err)
		}
	}
	if _, err := p.List("not-a-cursor", 2); !errors.Is(err, ErrInvalidCursor) {
		t.Errorf("List(bad cursor) = %v, want ErrInvalidCursor", err)
	}
	other := NewPaginator(s, []byte("other-tenant-secret"))
	pg, err := other.List("", 2)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if _, err := p.List(pg.NextCursor, 2); !errors.Is(err, ErrInvalidCursor) {
		t.Errorf("cursor from a paginator with a different secret accepted: %v", err)
	}
}

func TestList_EmptyStoreReturnsEmptyNonNilItems(t *testing.T) {
	p := NewPaginator(NewStore(), tSecret)
	pg, err := p.List("", 10)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if pg.Items == nil || len(pg.Items) != 0 || pg.NextCursor != "" {
		t.Fatalf("empty store: got %#v, want Items=[] (non-nil) and no NextCursor", pg)
	}
}

func TestList_NoDuplicatesNoSkipsUnderMutation(t *testing.T) {
	s := NewStore()
	tFill(t, s, 40, 4)
	p := NewPaginator(s, tSecret)
	deleted := map[string]bool{}
	all := tWalk(t, p, 7, func(page int, items []Item) {
		switch page {
		case 1:
			// New transactions post at the top of the list.
			for i := 0; i < 5; i++ {
				_ = s.Insert(Item{ID: fmt.Sprintf("new-%d", i), CreatedAt: tT0.Add(time.Hour + time.Duration(i)*time.Second)})
			}
			// Two already-seen rows are reversed (deleted).
			s.Delete(items[0].ID)
			s.Delete(items[1].ID)
			// One not-yet-seen row is deleted.
			s.Delete("tx-000")
			deleted["tx-000"] = true
			// A late-arriving, backdated posting lands in the unseen region.
			_ = s.Insert(Item{ID: "late-1", CreatedAt: tT0.Add(1500 * time.Millisecond)})
		case 3:
			last := items[len(items)-1]
			// Same timestamp as the cursor row: larger ID sorts before it
			// (already passed), smaller ID sorts after it (still ahead).
			_ = s.Insert(Item{ID: last.ID + "~", CreatedAt: last.CreatedAt})
			_ = s.Insert(Item{ID: last.ID[:len(last.ID)-1], CreatedAt: last.CreatedAt})
		}
	})
	tCheckStrictOrder(t, all)
	seen := tCount(all)
	for i := 0; i < 40; i++ {
		id := fmt.Sprintf("tx-%03d", i)
		if deleted[id] {
			if seen[id] > 0 {
				t.Errorf("%s was deleted before its page was read but was returned", id)
			}
			continue
		}
		if seen[id] != 1 {
			t.Errorf("%s seen %d times, want exactly 1 (it existed for the whole walk)", id, seen[id])
		}
	}
	if seen["late-1"] != 1 {
		t.Errorf("backdated insert into the unread region was not returned; keyset pagination should pick it up")
	}
	for i := 0; i < 5; i++ {
		if seen[fmt.Sprintf("new-%d", i)] != 0 {
			t.Errorf("new-%d (inserted above the cursor) was returned; it sorts before rows already read", i)
		}
	}
}

func TestList_ConcurrentWritersAndReaders(t *testing.T) {
	s := NewStore()
	const stable = 200
	for i := 0; i < stable; i++ {
		_ = s.Insert(Item{ID: fmt.Sprintf("s-%03d", i), CreatedAt: tT0.Add(time.Duration(i/3) * time.Millisecond)})
	}
	p := NewPaginator(s, tSecret)
	stop := make(chan struct{})
	var writers sync.WaitGroup
	for g := 0; g < 4; g++ {
		writers.Add(1)
		go func(g int) {
			defer writers.Done()
			r := rand.New(rand.NewPCG(uint64(g), 7))
			for i := 0; ; i++ {
				select {
				case <-stop:
					return
				default:
				}
				id := fmt.Sprintf("w-%d-%d", g, i)
				ts := tT0.Add(time.Duration(r.IntN(stable/3+20)-10) * time.Millisecond)
				_ = s.Insert(Item{ID: id, CreatedAt: ts})
				if i%2 == 1 {
					s.Delete(fmt.Sprintf("w-%d-%d", g, i-1))
				}
			}
		}(g)
	}
	errs := make(chan string, 64)
	var readers sync.WaitGroup
	for g := 0; g < 4; g++ {
		readers.Add(1)
		go func() {
			defer readers.Done()
			for walk := 0; walk < 3; walk++ {
				var all []Item
				cursor := ""
				for page := 0; page < 5000; page++ {
					pg, err := p.List(cursor, 7)
					if err != nil {
						errs <- "List: " + err.Error()
						return
					}
					all = append(all, pg.Items...)
					if pg.NextCursor == "" {
						break
					}
					cursor = pg.NextCursor
				}
				for i := 1; i < len(all); i++ {
					if !Before(KeyOf(all[i-1]), KeyOf(all[i])) {
						errs <- fmt.Sprintf("out of order or duplicate: %s then %s", all[i-1].ID, all[i].ID)
						return
					}
				}
				seen := tCount(all)
				for i := 0; i < stable; i++ {
					if id := fmt.Sprintf("s-%03d", i); seen[id] != 1 {
						errs <- fmt.Sprintf("stable row %s seen %d times under concurrent writes, want 1", id, seen[id])
						return
					}
				}
			}
		}()
	}
	done := make(chan struct{})
	go func() { readers.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		close(stop)
		t.Fatal("readers did not finish within 20s")
	}
	close(stop)
	tWait(t, &writers, 10*time.Second, "writers")
	close(errs)
	for e := range errs {
		t.Error(e)
	}
}

// ---------- HTTP ----------

func tServe(h http.Handler, method, target string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(method, target, nil))
	return rec
}

func TestHandler_FollowsNextCursorToTheEnd(t *testing.T) {
	s := NewStore()
	tFill(t, s, 45, 4)
	h := NewHandler(NewPaginator(s, tSecret))
	var all []Item
	target := "/transactions"
	for i := 0; i < 100; i++ {
		rec := tServe(h, http.MethodGet, target)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s: status %d, body %s", target, rec.Code, rec.Body)
		}
		if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
			t.Fatalf("Content-Type %q, want application/json", ct)
		}
		var pg Page
		if err := json.Unmarshal(rec.Body.Bytes(), &pg); err != nil {
			t.Fatalf("bad JSON %q: %v", rec.Body, err)
		}
		if i == 0 && len(pg.Items) != DefaultLimit {
			t.Fatalf("first page has %d items, want DefaultLimit=%d when limit is omitted", len(pg.Items), DefaultLimit)
		}
		all = append(all, pg.Items...)
		if pg.NextCursor == "" {
			if strings.Contains(rec.Body.String(), "next_cursor") {
				t.Errorf("last page should omit next_cursor entirely, got %s", rec.Body)
			}
			break
		}
		target = "/transactions?cursor=" + url.QueryEscape(pg.NextCursor)
	}
	if len(all) != 45 {
		t.Fatalf("walked %d items over HTTP, want 45", len(all))
	}
	tCheckStrictOrder(t, all)
}

func TestHandler_ErrorsUseErrorModel(t *testing.T) {
	s := NewStore()
	tFill(t, s, 5, 1)
	h := NewHandler(NewPaginator(s, tSecret))
	cases := []struct {
		target string
		code   string
	}{
		{"/transactions?limit=0", "invalid_limit"},
		{"/transactions?limit=101", "invalid_limit"},
		{"/transactions?limit=ten", "invalid_limit"},
		{"/transactions?cursor=Zm9v", "invalid_cursor"},
	}
	for _, c := range cases {
		rec := tServe(h, http.MethodGet, c.target)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("GET %s: status %d, want 400", c.target, rec.Code)
			continue
		}
		var body struct {
			Error struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body.Error.Code != c.code || body.Error.Message == "" {
			t.Errorf("GET %s: body %s, want {\"error\":{\"code\":%q,\"message\":\"...\"}}", c.target, rec.Body, c.code)
		}
	}
	rec := tServe(h, http.MethodPost, "/transactions")
	if rec.Code != http.StatusMethodNotAllowed || rec.Header().Get("Allow") != http.MethodGet {
		t.Errorf("POST: status %d Allow %q, want 405 with Allow: GET", rec.Code, rec.Header().Get("Allow"))
	}
	empty := tServe(NewHandler(NewPaginator(NewStore(), tSecret)), http.MethodGet, "/transactions")
	if empty.Code != http.StatusOK || !strings.Contains(empty.Body.String(), `"items":[]`) {
		t.Errorf("empty list: status %d body %s, want 200 with \"items\":[] (clients should not have to handle null)", empty.Code, empty.Body)
	}
}
