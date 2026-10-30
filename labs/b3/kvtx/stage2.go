package kvtx

import "errors"

// ---- Stage 2: Count and savepoints (announced at about minute 30) ----------
//
// Contract:
//   - Count(value) is the number of keys whose value in this handle's view
//     equals value. Count("") counts keys holding the empty string; it never
//     counts absent keys. It is O(1) (a test caps the time, with a large
//     open transaction) and stays correct across Set (including overwrites),
//     Delete (including of an absent key), Rollback, Commit and the
//     savepoint calls below.
//   - Savepoint(name) opens a NAMED nested transaction inside an open
//     transaction. RollbackTo(name) undoes everything done since the
//     savepoint was set and keeps the savepoint open (it can be rolled back
//     to again). Release(name) forgets the savepoint and keeps its changes,
//     merging them into the enclosing transaction.
//   - Names are looked up from the innermost frame outward, so a repeated
//     name refers to the most recent one. RollbackTo and Release also close
//     every frame opened after the savepoint.
//   - A savepoint is just a frame: Depth counts it, and Commit or Rollback
//     with a savepoint innermost act on that savepoint like on any frame.

// ErrNoSavepoint is returned by RollbackTo and Release for an unknown name.
var ErrNoSavepoint = errors.New("kvtx: no such savepoint")

// ErrInvalidName is returned by Savepoint for the empty name.
var ErrInvalidName = errors.New("kvtx: savepoint name must not be empty")

// Count returns how many keys currently hold value in this handle's view.
func (s *Store) Count(value string) int {
	// TODO(lab)
	_ = value
	return 0
}

// Savepoint opens a named nested transaction. It returns ErrNoTransaction
// outside a transaction and ErrInvalidName for the empty name.
func (s *Store) Savepoint(name string) error {
	// TODO(lab)
	_ = name
	return errNotImplemented
}

// RollbackTo undoes all changes made since the named savepoint was set. The
// savepoint stays open. It returns ErrNoSavepoint for an unknown name.
func (s *Store) RollbackTo(name string) error {
	// TODO(lab)
	_ = name
	return errNotImplemented
}

// Release removes the named savepoint (and any frames opened after it),
// keeping their changes in the enclosing transaction. It returns
// ErrNoSavepoint for an unknown name.
func (s *Store) Release(name string) error {
	// TODO(lab)
	_ = name
	return errNotImplemented
}
