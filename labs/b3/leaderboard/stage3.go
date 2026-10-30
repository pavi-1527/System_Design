package leaderboard

import "time"

// Stage 3 (≈ 15 min): consistent reads. Then two follow-ups an interviewer
// asks if you finish early: sharding and time windows.
//
//	go test -race -run 'Stage3_(Concurrent|Snapshot)' ./labs/b3/leaderboard/   # the 15-minute part
//	go test -race -run 'Stage3_(Sharded|Windowed)' ./labs/b3/leaderboard/      # follow-ups

// Snapshot returns an independent, point-in-time copy of the board: it holds
// exactly the scores b held at one instant (never a mix of two), later writes
// to b do not show up in it, and writes to the copy do not show up in b. The
// copy is a normal Board. Paging through a Snapshot gives a stable listing
// that shows every player exactly once while the live board keeps changing.
func (b *Board) Snapshot() *Board {
	// TODO(lab)
	return New()
}

// ---------------------------------------------------------------- Sharded

// Sharded splits players over independent boards by a hash of their name, so
// writers to different shards never contend. A call that spans shards (TopK,
// Rank) combines per-shard answers taken at slightly different instants: each
// shard's part is consistent, the combination is not one atomic snapshot.
//
// Its observable behaviour for a single goroutine must be IDENTICAL to a
// Board's, including tie-breaking: "who reached the score first" has to mean
// the same thing across shards.
type Sharded struct {
	// TODO(lab)
}

// NewSharded returns a board of n shards (n < 1 means 1).
func NewSharded(n int) *Sharded {
	// TODO(lab)
	_ = n
	return &Sharded{}
}

// Add, Set, Remove, Score, Len and Rank behave as on Board.
func (s *Sharded) Add(player string, delta int64) int64 {
	// TODO(lab)
	_, _ = player, delta
	return 0
}

func (s *Sharded) Set(player string, score int64) {
	// TODO(lab)
	_, _ = player, score
}

func (s *Sharded) Remove(player string) bool {
	// TODO(lab)
	_ = player
	return false
}

func (s *Sharded) Score(player string) (int64, bool) {
	// TODO(lab)
	_ = player
	return 0, false
}

func (s *Sharded) Len() int {
	// TODO(lab)
	return 0
}

func (s *Sharded) Rank(player string) (int, bool) {
	// TODO(lab)
	_ = player
	return 0, false
}

// TopK returns the k best entries over all shards, best first, with Rank set.
// Hint: how many entries do you need from EACH shard to be sure the global top
// k is among them?
func (s *Sharded) TopK(k int) []Entry {
	// TODO(lab)
	_ = k
	return nil
}

// --------------------------------------------------------------- Windowed

// Windowed keeps one Board per calendar window (a day, a week...), where
// window i covers [i·period, (i+1)·period) of the injected clock (measured
// from the Unix epoch). There are no timers or goroutines: a window is closed
// lazily by the first call that sees the clock in a later window. Periods in
// which nobody called anything become EMPTY closed windows, so Previous(n)
// counts calendar periods back, not "windows that happened to have activity"
// (only the last keep of them are remembered).
type Windowed struct {
	// TODO(lab)
}

// NewWindowed returns a windowed board that remembers the last keep closed
// windows. period must be > 0.
func NewWindowed(now func() time.Time, period time.Duration, keep int) *Windowed {
	// TODO(lab)
	_, _, _ = now, period, keep
	return &Windowed{}
}

// Add adds delta to player in the current window and returns their score in
// it. Safe for concurrent use, including across a window boundary: no Add is
// lost or counted twice.
func (w *Windowed) Add(player string, delta int64) int64 {
	// TODO(lab)
	_, _ = player, delta
	return 0
}

// Current returns the current window's board.
func (w *Windowed) Current() *Board {
	// TODO(lab)
	return New()
}

// Previous returns the n-th most recent closed window (1 = the latest). ok is
// false if n < 1 or that window is older than keep or never existed.
func (w *Windowed) Previous(n int) (*Board, bool) {
	// TODO(lab)
	_ = n
	return nil, false
}

// Rotate closes the current window now, whatever the clock says (a "season
// reset"), and starts an empty one. The calendar index does not change, so
// the next clock boundary closes the new window as well.
func (w *Windowed) Rotate() {
	// TODO(lab)
}
