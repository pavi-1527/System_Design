package leaderboard

// Stage 2 (≈ 25 min): neighbours, pages, and speed.
//
// The scale tests run 100,000 players through three workloads (random
// scores, strictly ascending scores, everyone tied) with a generous wall-clock
// budget each, under -race. They do not measure micro-performance; they fail
// anything whose per-operation cost grows linearly with the number of
// players: a sorted slice with O(n) insertion, a full sort per query, or a
// scan to compute a rank. Think about which structure answers "how many are
// ahead of this key" and "give me positions 5000..5009" in O(log n).

// Around returns up to n entries ranked above player, then player, then up to
// n ranked below, in rank order with Rank filled in. Near the top or bottom
// you get fewer. n < 0 is treated as 0. ok is false for an unknown player.
func (b *Board) Around(player string, n int) ([]Entry, bool) {
	// TODO(lab)
	_, _ = player, n
	return nil, false
}

// Page returns the entries at positions offset, offset+1, ... offset+limit-1
// (0-based), i.e. ranks offset+1 ... offset+limit, clipped to the board. A
// negative offset behaves as 0; limit <= 0 or offset >= Len gives an empty
// result.
func (b *Board) Page(offset, limit int) []Entry {
	// TODO(lab)
	_, _ = offset, limit
	return nil
}
