package leaderboard

// Around returns up to n entries ranked above player, then player, then up to
// n ranked below, in rank order with Rank filled in. Near the top or bottom
// you get fewer. n < 0 is treated as 0. ok is false for an unknown player.
func (b *Board) Around(player string, n int) ([]Entry, bool) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	r, ok := b.players[player]
	if !ok {
		return nil, false
	}
	n = max(n, 0)
	pos := countBefore(b.root, &r) // 0-based position
	return b.windowLocked(pos-n, pos+n+1), true
}

// Page returns the entries at positions offset, offset+1, ... offset+limit-1
// (0-based), i.e. ranks offset+1 ... offset+limit, clipped to the board. A
// negative offset behaves as 0; limit <= 0 or offset >= Len gives an empty
// result.
func (b *Board) Page(offset, limit int) []Entry {
	if limit <= 0 {
		return []Entry{}
	}
	offset = max(offset, 0)
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.windowLocked(offset, offset+limit)
}
