// Package leaderboard ranks players by score and answers "top k", "what is
// my rank" and "who is around me" while scores change.
//
// Lab B3 Unit 8 §1 (problem 17, STRETCH). Stage 1 is this file; Stage 2 lives
// in stage2.go and Stage 3 in stage3.go. Open those only when the interviewer
// (the unit text) has asked for them.
//
// Reference design: an order-statistic treap (treap.go) holds the ranking, a
// map holds each player's current record, and one sync.RWMutex guards both.
// Every operation is O(log n) except Snapshot (O(n) copy). Readers share the
// lock, so TopK/Rank/Page calls run in parallel; writers serialise.
package leaderboard

import (
	"sync"
	"sync/atomic"
)

// Entry is one row of the ranking.
type Entry struct {
	Rank   int // 1-based position; filled in by TopK, Page and Around
	Player string
	Score  int64
}

// Board is a leaderboard. It is safe for concurrent use.
//
// Ordering (a strict total order): higher score first; among equal scores the
// player who REACHED that score first ranks higher. "Reached" is a logical
// clock that ticks every time any player's score changes to a different value
// (including a player's first appearance). Setting a score to the value it
// already has, or adding 0, is not a change and keeps the player's place.
// Removing a player and adding them back counts as a new arrival.
// Because the clock values are unique, no two players ever share a rank.
type Board struct {
	mu      sync.RWMutex
	root    *node          // ranking, guarded by mu
	players map[string]rec // current record per player, guarded by mu
	seq     *atomic.Uint64 // logical clock; shared by the shards of a Sharded board
}

// New returns an empty board.
func New() *Board { return newBoard(new(atomic.Uint64)) }

func newBoard(seq *atomic.Uint64) *Board {
	return &Board{players: map[string]rec{}, seq: seq}
}

// put records a score change under b.mu.Lock.
func (b *Board) put(player string, score int64) {
	if old, ok := b.players[player]; ok {
		b.root = remove(b.root, &old)
	}
	n := rec{player: player, score: score, seq: b.seq.Add(1)}
	b.players[player] = n
	b.root = insert(b.root, n)
}

// Add adds delta to player's score (creating the player at 0 first if
// needed, even when delta is 0) and returns the new score. The
// read-modify-write is atomic.
func (b *Board) Add(player string, delta int64) int64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	old, ok := b.players[player]
	if ok && delta == 0 {
		return old.score
	}
	b.put(player, old.score+delta)
	return old.score + delta
}

// Set sets player's score, creating the player if needed.
func (b *Board) Set(player string, score int64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if old, ok := b.players[player]; ok && old.score == score {
		return
	}
	b.put(player, score)
}

// Remove deletes player and reports whether they were on the board.
func (b *Board) Remove(player string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	old, ok := b.players[player]
	if !ok {
		return false
	}
	b.root = remove(b.root, &old)
	delete(b.players, player)
	return true
}

// Score returns player's current score.
func (b *Board) Score(player string) (int64, bool) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	r, ok := b.players[player]
	return r.score, ok
}

// Rank returns player's 1-based rank (1 = best).
func (b *Board) Rank(player string) (int, bool) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	r, ok := b.players[player]
	if !ok {
		return 0, false
	}
	return countBefore(b.root, &r) + 1, true
}

// TopK returns the k best entries, best first, with Rank filled in. k <= 0
// gives an empty result and k > Len gives everything. The slice is the
// caller's.
func (b *Board) TopK(k int) []Entry {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.windowLocked(0, k)
}

// Len returns the number of players.
func (b *Board) Len() int {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return len(b.players)
}

// windowLocked returns the entries at 0-based positions [lo, hi) with ranks
// set. The caller holds b.mu (read or write).
func (b *Board) windowLocked(lo, hi int) []Entry {
	rs := b.recsLocked(lo, hi)
	out := make([]Entry, len(rs))
	lo = max(lo, 0)
	for i, r := range rs {
		out[i] = Entry{Rank: lo + i + 1, Player: r.player, Score: r.score}
	}
	return out
}

func (b *Board) recsLocked(lo, hi int) []rec {
	lo, hi = max(lo, 0), min(hi, len(b.players))
	if lo >= hi {
		return nil
	}
	out := make([]rec, 0, hi-lo)
	collect(b.root, lo, hi, &out)
	return out
}
