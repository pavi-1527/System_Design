// Package leaderboard ranks players by score and answers "top k", "what is
// my rank" and "who is around me" while scores change.
//
// Lab B3 Unit 8 §1 (problem 17, STRETCH), time box 60 minutes. Work in stages
// and open the next stage's file only when you get there, like an interviewer
// who adds requirements as you go:
//
//	Stage 1 (≈ 20 min)  this file:  Add, Set, Remove, Score, Rank, TopK, Len
//	Stage 2 (≈ 25 min)  stage2.go:  Around, Page, and "it must stay fast at 100,000 players"
//	Stage 3 (≈ 15 min)  stage3.go:  concurrency, Snapshot; then Sharded and Windowed
//
// Run one stage at a time:
//
//	go test -race -run 'Stage1' ./labs/b3/leaderboard/
//
// Every method of Board must be safe for concurrent use, even though the
// Stage 1 and 2 tests are sequential: decide the locking model at minute 5.
package leaderboard

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
// already has, or adding 0 to an existing player, is NOT a change and keeps
// the player's place. Removing a player and adding them back counts as a new
// arrival. Because the clock values are unique, no two players ever share a
// rank.
type Board struct {
	// TODO(lab): your ranking structure, the per-player records, a lock, and
	// the logical clock.
}

// New returns an empty board.
func New() *Board {
	// TODO(lab)
	return &Board{}
}

// Add adds delta to player's score (creating the player at 0 first if
// needed, even when delta is 0) and returns the new score. The
// read-modify-write is atomic with respect to other callers.
func (b *Board) Add(player string, delta int64) int64 {
	// TODO(lab)
	_, _ = player, delta
	return 0
}

// Set sets player's score, creating the player if needed.
func (b *Board) Set(player string, score int64) {
	// TODO(lab)
	_, _ = player, score
}

// Remove deletes player and reports whether they were on the board.
func (b *Board) Remove(player string) bool {
	// TODO(lab)
	_ = player
	return false
}

// Score returns player's current score.
func (b *Board) Score(player string) (int64, bool) {
	// TODO(lab)
	_ = player
	return 0, false
}

// Rank returns player's 1-based rank (1 = best).
func (b *Board) Rank(player string) (int, bool) {
	// TODO(lab)
	_ = player
	return 0, false
}

// TopK returns the k best entries, best first, with Rank filled in. k <= 0
// gives an empty result and k > Len gives everything. The returned slice
// belongs to the caller.
func (b *Board) TopK(k int) []Entry {
	// TODO(lab)
	_ = k
	return nil
}

// Len returns the number of players.
func (b *Board) Len() int {
	// TODO(lab)
	return 0
}
