package leaderboard

import (
	"container/heap"
	"hash/fnv"
	"sync"
	"sync/atomic"
	"time"
)

// Snapshot returns an independent, point-in-time copy of the board: it holds
// exactly the scores b held at one instant (never a mix of two), and later
// writes to b do not show up in it, nor writes to the copy in b. Page through
// a Snapshot to get a stable, duplicate-free listing while the live board keeps
// changing. The copy costs O(n); see the unit for the O(1) alternative.
func (b *Board) Snapshot() *Board {
	b.mu.RLock()
	defer b.mu.RUnlock()
	c := new(atomic.Uint64)
	c.Store(b.seq.Load())
	nb := &Board{root: clone(b.root), players: make(map[string]rec, len(b.players)), seq: c}
	for p, r := range b.players {
		nb.players[p] = r
	}
	return nb
}

// ---------------------------------------------------------------- Sharded

// Sharded splits players over independent boards by a hash of their name, so
// writers to different shards never contend. Reads that span shards (TopK,
// Rank) combine per-shard answers taken at slightly different instants: each
// shard's part is consistent, the combination is not one atomic snapshot.
type Sharded struct {
	shards []*Board
	seq    atomic.Uint64 // ONE logical clock for all shards, so ties break the same way as on a single Board
}

// NewSharded returns a board of n shards (n < 1 means 1).
func NewSharded(n int) *Sharded {
	n = max(n, 1)
	s := &Sharded{shards: make([]*Board, n)}
	for i := range s.shards {
		s.shards[i] = newBoard(&s.seq)
	}
	return s
}

func (s *Sharded) shard(player string) *Board {
	h := fnv.New32a()
	h.Write([]byte(player))
	return s.shards[int(h.Sum32()%uint32(len(s.shards)))]
}

// Add, Set, Remove and Score behave as on Board.
func (s *Sharded) Add(player string, delta int64) int64 { return s.shard(player).Add(player, delta) }
func (s *Sharded) Set(player string, score int64)       { s.shard(player).Set(player, score) }
func (s *Sharded) Remove(player string) bool            { return s.shard(player).Remove(player) }
func (s *Sharded) Score(player string) (int64, bool)    { return s.shard(player).Score(player) }

// Len is the total number of players.
func (s *Sharded) Len() int {
	n := 0
	for _, b := range s.shards {
		n += b.Len()
	}
	return n
}

// Rank is 1 + the number of players ahead of player in ANY shard.
func (s *Sharded) Rank(player string) (int, bool) {
	own := s.shard(player)
	r, ok := own.recOf(player)
	if !ok {
		return 0, false
	}
	ahead := 0
	for _, b := range s.shards {
		ahead += b.countAhead(&r)
	}
	return ahead + 1, true
}

func (b *Board) recOf(player string) (rec, bool) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	r, ok := b.players[player]
	return r, ok
}

func (b *Board) countAhead(key *rec) int {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return countBefore(b.root, key)
}

// cursor walks one shard's best-first records during the k-way merge.
type cursor struct {
	recs []rec
	at   int
}

type mergeHeap []*cursor

func (h mergeHeap) Len() int           { return len(h) }
func (h mergeHeap) Less(i, j int) bool { return before(&h[i].recs[h[i].at], &h[j].recs[h[j].at]) }
func (h mergeHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }
func (h *mergeHeap) Push(x any)        { *h = append(*h, x.(*cursor)) }
func (h *mergeHeap) Pop() any          { o := *h; x := o[len(o)-1]; *h = o[:len(o)-1]; return x }

// topRecs returns the k best records of one shard, best first.
func (b *Board) topRecs(k int) []rec {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.recsLocked(0, k)
}

// TopK merges each shard's own top k. k per shard is enough: a player ranked
// in the global top k has fewer than k players ahead of them overall, so
// fewer than k ahead of them inside their own shard, so they are in their
// shard's top k.
func (s *Sharded) TopK(k int) []Entry {
	if k <= 0 {
		return []Entry{}
	}
	h := &mergeHeap{}
	for _, b := range s.shards {
		if rs := b.topRecs(k); len(rs) > 0 {
			*h = append(*h, &cursor{recs: rs})
		}
	}
	heap.Init(h)
	out := make([]Entry, 0, k)
	for h.Len() > 0 && len(out) < k {
		c := (*h)[0]
		r := c.recs[c.at]
		out = append(out, Entry{Rank: len(out) + 1, Player: r.player, Score: r.score})
		c.at++
		if c.at == len(c.recs) {
			heap.Pop(h)
		} else {
			heap.Fix(h, 0)
		}
	}
	return out
}

// --------------------------------------------------------------- Windowed

// Windowed keeps one Board per calendar window (a day, a week...), where
// window i covers [i·period, (i+1)·period) of the injected clock. There are
// no timers: a window is closed lazily by the first call that sees the clock
// in a later window.
type Windowed struct {
	mu     sync.Mutex
	now    func() time.Time
	period time.Duration
	keep   int
	idx    int64    // index of the current window
	cur    *Board   // current window
	prev   []*Board // closed windows, most recent first, at most keep
}

// NewWindowed returns a windowed board that remembers the last keep closed
// windows. period must be > 0.
func NewWindowed(now func() time.Time, period time.Duration, keep int) *Windowed {
	return &Windowed{now: now, period: period, keep: max(keep, 0),
		idx: now().UnixNano() / int64(period), cur: New()}
}

func (w *Windowed) push(b *Board) {
	w.prev = append([]*Board{b}, w.prev...)
	if len(w.prev) > w.keep {
		w.prev = w.prev[:w.keep]
	}
}

// advance closes windows up to the clock's window. Periods in which nobody
// called anything are closed as EMPTY windows (at most keep of them matter).
func (w *Windowed) advance() {
	want := w.now().UnixNano() / int64(w.period)
	if want <= w.idx {
		return
	}
	w.push(w.cur)
	for i := w.idx + 1; i < want && i <= w.idx+int64(w.keep); i++ {
		w.push(New())
	}
	w.cur, w.idx = New(), want
}

// Add adds delta to player in the current window and returns their score in it.
func (w *Windowed) Add(player string, delta int64) int64 {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.advance()
	return w.cur.Add(player, delta)
}

// Current returns the current window's board (live; it may be closed by a
// later call).
func (w *Windowed) Current() *Board {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.advance()
	return w.cur
}

// Previous returns the n-th most recent closed window (1 = the latest), as a
// frozen snapshot. ok is false if it is older than keep or never existed.
func (w *Windowed) Previous(n int) (*Board, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.advance()
	if n < 1 || n > len(w.prev) {
		return nil, false
	}
	return w.prev[n-1], true
}

// Rotate closes the current window now, whatever the clock says (a "season
// reset"), and starts an empty one. The calendar index does not change, so
// the next clock boundary closes the new window as well.
func (w *Windowed) Rotate() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.advance()
	w.push(w.cur)
	w.cur = New()
}
