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
//     updates unless the read is done with GetForUpdate.
//   - Count and Len describe the session's own view, as in stage 2. They
//     must stay correct when ANOTHER session commits a key this session has
//     uncommitted writes for.
//
// Concurrency contract: a Session is one client's handle and must be used by
// one goroutine at a time; different Sessions may be used concurrently. Run
// `go test -race`.
//
// Your stage 1 *Store was one client. The API for stage 3 is additive: a
// *Store IS a session, and NewSession returns another one onto the same data.

// ErrConflict is returned (wrapped) by the outermost Commit when another
// session committed a key this transaction had written or locked.
var ErrConflict = errors.New("kvtx: write-write conflict")

// Session is a handle with its own transaction stack. A *Store from New is
// itself a session; NewSession makes more onto the same data.
type Session = Store

// NewSession returns a new session onto the same committed data as s, with an
// empty transaction stack.
func (s *Store) NewSession() *Session {
	// TODO(lab)
	return nil
}

// Result is one entry of an MGet answer.
type Result struct {
	Value string
	OK    bool // false: the key is absent in this session's view
}

// MGet reads all keys under ONE consistent view: committed data as of a
// single instant plus this session's own writes. Reading the same keys with
// separate Gets can straddle a commit.
func (s *Store) MGet(keys ...string) []Result {
	// TODO(lab)
	_ = keys
	return nil
}

// GetForUpdate is Get that also guards key: inside a transaction, the outermost
// Commit fails with ErrConflict if another session commits key after this call
// (the optimistic form of SELECT ... FOR UPDATE), even if this transaction
// never writes it. Outside a transaction it is just Get.
func (s *Store) GetForUpdate(key string) (string, bool) {
	// TODO(lab)
	_ = key
	return "", false
}
