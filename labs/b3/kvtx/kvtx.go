// Package kvtx is an in-memory string key-value store with nested
// transactions.
//
// Lab B3 Problem 4 (75-minute box). Stage 1 is this file (Get/Set/Delete and
// nested Begin/Commit/Rollback through an undo log); stage 2 (Count and
// savepoints) lives in stage2.go and stage 3 (concurrent sessions, isolation
// and conflicts) in stage3.go. Treat the later files as sealed until the
// interviewer "announces" them (see unit-2-modelling-and-state.md §2).
//
// Stage 1 contract (one client):
//   - Get, Set, Delete and Len describe the handle's current view: the
//     committed data plus its own uncommitted writes. A key holding the
//     empty string is present; absent and "" are different states.
//   - Outside a transaction a write is applied immediately and cannot be
//     undone.
//   - Begin opens a nested transaction. Rollback undoes only the innermost
//     one. Commit of an inner transaction merges its changes into the
//     parent (an outer Rollback still undoes them); Commit of the outermost
//     makes the changes final.
//   - Commit and Rollback with no open transaction return ErrNoTransaction
//     and change nothing.
//   - Begin, Set, Delete, Rollback and an inner Commit cost O(1) per key
//     touched, independent of the store's size. A test caps the time. Do
//     not copy the map on Begin: keep an undo log (what each key held
//     before the transaction first touched it).
//   - The undo log is first-touch only: writing one key a million times in
//     a transaction keeps one undo record, not a million.
//
// Concurrency contract: a *Store handle is ONE client's transaction stack and
// is not safe for use by several goroutines at once (just like a database
// connection). Stage 3 adds NewSession: distinct handles onto the same data
// that may be used concurrently.
package kvtx

import "errors"

// ErrNoTransaction is returned by Commit, Rollback and Savepoint when no
// transaction is open.
var ErrNoTransaction = errors.New("kvtx: no transaction in progress")

var errNotImplemented = errors.New("kvtx: not implemented")

// Store is one client's handle onto a key-value store. The zero value is not
// usable; call New.
type Store struct {
	// TODO(lab): fields. Think about: where committed data lives, where this
	// client's uncommitted writes live, the stack of open transactions and
	// what each one records so Rollback can undo it. Stage 3 will want the
	// committed data to be shareable between several handles; stage 2 wants
	// per-value counts.
}

// New returns an empty store. The error is always nil in a finished
// implementation; it is in the signature so options can be added later.
func New() (*Store, error) {
	// TODO(lab)
	return nil, errNotImplemented
}

// Get returns the value of key in this handle's view.
func (s *Store) Get(key string) (string, bool) {
	// TODO(lab)
	_ = key
	return "", false
}

// Set stores value under key. Inside a transaction it is undone by Rollback;
// outside one it is final immediately.
func (s *Store) Set(key, value string) {
	// TODO(lab)
	_, _ = key, value
}

// Delete removes key and reports whether it was present in this handle's
// view. Deleting an absent key is a no-op.
func (s *Store) Delete(key string) bool {
	// TODO(lab)
	_ = key
	return false
}

// Len returns the number of keys in this handle's view.
func (s *Store) Len() int {
	// TODO(lab)
	return 0
}

// Depth returns the number of open transactions (stage 2: and savepoints).
func (s *Store) Depth() int {
	// TODO(lab)
	return 0
}

// Begin opens a nested transaction.
func (s *Store) Begin() {
	// TODO(lab)
}

// Rollback undoes the innermost transaction. It returns ErrNoTransaction when
// none is open.
func (s *Store) Rollback() error {
	// TODO(lab)
	return errNotImplemented
}

// Commit ends the innermost transaction: an inner one merges into its parent,
// the outermost makes the changes final. It returns ErrNoTransaction when none
// is open. (Stage 3: the outermost Commit may also fail with ErrConflict.)
func (s *Store) Commit() error {
	// TODO(lab)
	return errNotImplemented
}
