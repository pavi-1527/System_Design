package kvtx

import "errors"

// ---- Stage 2: Count and savepoints (announced at about minute 30) ----------
//
// Contract:
//   - Count(value) is the number of keys whose value in this handle's view
//     equals value. Count("") counts keys holding the empty string; it never
//     counts absent keys. It is O(1) and stays correct across Set (including
//     overwrites), Delete (including of an absent key), Rollback, Commit and
//     the savepoint calls below.
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
	s.sh.mu.RLock()
	defer s.sh.mu.RUnlock()
	s.sync()
	return s.sh.counts[value] + s.cntDelta[value]
}

// Savepoint opens a named nested transaction. It needs an open transaction.
func (s *Store) Savepoint(name string) error {
	if name == "" {
		return ErrInvalidName
	}
	if len(s.frames) == 0 {
		return ErrNoTransaction
	}
	s.frames = append(s.frames, frame{name: name})
	return nil
}

// RollbackTo undoes all changes made since the named savepoint was set. The
// savepoint stays open.
func (s *Store) RollbackTo(name string) error {
	i := s.find(name)
	if i < 0 {
		return ErrNoSavepoint
	}
	for len(s.frames)-1 > i {
		_ = s.Rollback()
	}
	s.restoreTop()
	return nil
}

// Release removes the named savepoint (and any frames opened after it),
// keeping their changes in the enclosing transaction.
func (s *Store) Release(name string) error {
	i := s.find(name)
	if i < 0 {
		return ErrNoSavepoint
	}
	// A savepoint can only be opened on top of an existing frame, so i >= 1
	// and every Commit here is an inner merge that cannot fail.
	for len(s.frames) > i {
		_ = s.Commit()
	}
	return nil
}

// find returns the index of the innermost frame called name, or -1.
func (s *Store) find(name string) int {
	if name == "" {
		return -1
	}
	for i := len(s.frames) - 1; i >= 0; i-- {
		if s.frames[i].name == name {
			return i
		}
	}
	return -1
}
