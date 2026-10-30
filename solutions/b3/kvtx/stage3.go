package kvtx

import "errors"

// ---- Stage 3: concurrent sessions (announced at about minute 50) -----------
//
// Isolation level: READ COMMITTED with first-committer-wins on write-write
// conflicts.
//   - A session sees the committed data plus its own uncommitted writes
//     (including writes of its inner, already merged transactions). It never
//     sees another session's uncommitted writes, and never sees a rolled
//     back write. Every read sees the latest committed state, so two reads
//     in one transaction may differ (non-repeatable reads are allowed).
//   - The outermost Commit is atomic: it applies every key of the write set
//     in one critical section, so no reader (see MGet) observes half of it.
//   - Each key a transaction writes (or reads with GetForUpdate) remembers
//     the committed VERSION it had at first touch. At the outermost Commit,
//     if any such key has been committed by someone else since, Commit applies
//     NOTHING, aborts the whole transaction (Depth becomes 0, all writes
//     discarded) and returns an error satisfying errors.Is(err, ErrConflict).
//     The first committer wins; the loser retries. Versions, not values, are
//     compared, so a key changed and changed back (ABA) still conflicts, and
//     so does a key deleted or recreated by someone else.
//   - A write that was rolled back (inner Rollback or RollbackTo) is no longer
//     part of the write set and cannot cause a conflict.
//   - Writes outside a transaction are single-key transactions that always
//     commit; they make any open transaction that touched the key conflict.
//   - Plain reads are not validated. Read-modify-write therefore loses
//     updates unless the read is done with GetForUpdate. That is the price
//     of read committed; snapshot isolation would remove it.
//
// Concurrency contract: a Session is one client's handle and must be used by
// one goroutine at a time; different Sessions may be used concurrently.

// ErrConflict is returned (wrapped) by the outermost Commit when another
// session committed a key this transaction had written or locked.
var ErrConflict = errors.New("kvtx: write-write conflict")

// Session is a handle with its own transaction stack. A *Store from New is
// itself a session; NewSession makes more onto the same data.
type Session = Store

// NewSession returns a new session onto the same committed data as s, with an
// empty transaction stack.
func (s *Store) NewSession() *Session { return newHandle(s.sh) }

// Result is one entry of an MGet answer.
type Result struct {
	Value string
	OK    bool // false: the key is absent in this session's view
}

// MGet reads all keys under ONE lock acquisition, so the answer is a single
// consistent state: committed data as of one instant plus this session's own
// writes. Reading the same keys with separate Gets can straddle a commit.
func (s *Store) MGet(keys ...string) []Result {
	out := make([]Result, len(keys))
	s.sh.mu.RLock()
	defer s.sh.mu.RUnlock()
	for i, k := range keys {
		v, ok := s.view(k)
		out[i] = Result{Value: v, OK: ok}
	}
	return out
}

// GetForUpdate is Get that also guards key: inside a transaction, the outermost
// Commit fails with ErrConflict if another session commits key after this call
// (the optimistic form of SELECT ... FOR UPDATE). Outside a transaction it is
// just Get.
func (s *Store) GetForUpdate(key string) (string, bool) {
	if len(s.frames) == 0 {
		return s.Get(key)
	}
	s.sh.mu.RLock()
	defer s.sh.mu.RUnlock()
	s.sync()
	if _, held := s.overlay[key]; !held {
		s.touch(key)
		s.setSlot(key, &slot{kind: guard, base: s.sh.versionOf(key)})
	}
	return s.view(key)
}

// firstConflict reports a key of the write set whose committed version moved
// since this transaction first touched it. Requires sh.mu.
func (sh *shared) firstConflict(overlay map[string]slot) (string, bool) {
	for k, sl := range overlay {
		if sh.versionOf(k) != sl.base {
			return k, true
		}
	}
	return "", false
}
