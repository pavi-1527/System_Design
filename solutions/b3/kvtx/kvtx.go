// Package kvtx is an in-memory string key-value store with nested
// transactions.
//
// Lab B3 Problem 4 (75-minute box). Stage 1 is this file (Get/Set/Delete and
// nested Begin/Commit/Rollback through an undo log); stage 2 (Count and
// savepoints) lives in stage2.go and stage 3 (concurrent sessions, isolation
// and conflicts) in stage3.go. Treat the later files as sealed until the
// interviewer "announces" them (see unit-2-modelling-and-state.md §2).
//
// Reference design, in one paragraph: committed data lives in a shared map
// guarded by an RWMutex. Every handle (*Store value) owns a private overlay,
// its write set, which holds the handle's uncommitted writes, and a stack of
// frames, one per open (nested) transaction. Each frame is an UNDO LOG: for
// every key the frame touches it remembers what the overlay held for that key
// before the frame's FIRST touch. Rollback replays the innermost frame's log;
// an inner Commit merges it into its parent (the parent keeps its own, older
// record when it has one); the outermost Commit validates and applies the
// whole overlay to the shared map in one critical section. Nothing ever
// copies the whole map.
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
//     touched, independent of the store's size. A test caps the time.
//   - The undo log is first-touch only: writing one key a million times in a
//     transaction keeps one undo record, not a million.
//
// Concurrency contract: a *Store handle is ONE client's transaction stack and
// is not safe for use by several goroutines at once (just like a database
// connection). Distinct handles made by NewSession (stage 3) may be used
// concurrently; they share the committed data safely.
package kvtx

import (
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
)

// ErrNoTransaction is returned by Commit, Rollback and Savepoint when no
// transaction is open.
var ErrNoTransaction = errors.New("kvtx: no transaction in progress")

// slotKind says what a handle's overlay holds for a key.
type slotKind uint8

const (
	guard slotKind = iota + 1 // key read FOR UPDATE: the view is unchanged, only a conflict base is held
	put                       // key written in this transaction
	del                       // key deleted in this transaction (a tombstone, not absence)
)

// slot is one overlay entry. base is the committed version of the key when
// this transaction first touched it; the outermost Commit fails if the key's
// committed version has moved since.
type slot struct {
	kind  slotKind
	value string
	base  uint64
}

// undoRec is one undo-log entry: the overlay's entry for a key before the
// frame first touched it. had == false means "no overlay entry at all", which
// is not the same as "an entry holding the empty string" or "a tombstone".
type undoRec struct {
	prev slot
	had  bool
}

// frame is one open (nested) transaction or savepoint.
type frame struct {
	name string             // savepoint name, "" for a plain Begin
	undo map[string]undoRec // first-touch records, allocated lazily
}

// cell is a committed value with the version (commit sequence number) that
// last wrote it.
type cell struct {
	value string
	ver   uint64
}

// shared is the committed state that every handle sees.
type shared struct {
	mu      sync.RWMutex
	data    map[string]cell   // committed live keys
	tomb    map[string]uint64 // version of the last delete, kept only while some tx has a write set
	counts  map[string]int    // committed value -> number of keys holding it
	seq     uint64            // incremented by every commit that changes data
	writers atomic.Int64      // handles whose overlay is non-empty
}

// Store is one client's handle onto a key-value store. Create the first with
// New; NewSession (stage 3) makes more handles onto the same data.
type Store struct {
	sh      *shared
	overlay map[string]slot
	frames  []frame

	// The view relative to committed data, kept incrementally so Len and
	// Count are O(1). Valid as of commit sequence number seen; sync()
	// rebuilds it from the overlay when another handle has committed since.
	seen     uint64
	lenDelta int
	cntDelta map[string]int
}

// New returns an empty store. The error is always nil today; it is in the
// signature so options can be added without breaking callers.
func New() (*Store, error) {
	return newHandle(&shared{
		data:   map[string]cell{},
		tomb:   map[string]uint64{},
		counts: map[string]int{},
	}), nil
}

func newHandle(sh *shared) *Store {
	return &Store{sh: sh, overlay: map[string]slot{}, cntDelta: map[string]int{}}
}

// Get returns the value of key in this handle's view.
func (s *Store) Get(key string) (string, bool) {
	s.sh.mu.RLock()
	defer s.sh.mu.RUnlock()
	return s.view(key)
}

// Set stores value under key. Inside a transaction it is undone by Rollback;
// outside one it is final immediately.
func (s *Store) Set(key, value string) {
	if len(s.frames) == 0 {
		s.sh.autocommit(key, value, true)
		return
	}
	s.sh.mu.RLock()
	defer s.sh.mu.RUnlock()
	s.sync()
	s.touch(key)
	s.setSlot(key, &slot{kind: put, value: value, base: s.baseOf(key)})
}

// Delete removes key and reports whether it was present in this handle's
// view. Deleting an absent key is a no-op (nothing to undo, nothing to count).
func (s *Store) Delete(key string) bool {
	if len(s.frames) == 0 {
		return s.sh.autocommit(key, "", false)
	}
	s.sh.mu.RLock()
	defer s.sh.mu.RUnlock()
	s.sync()
	if _, ok := s.view(key); !ok {
		return false
	}
	s.touch(key)
	s.setSlot(key, &slot{kind: del, base: s.baseOf(key)})
	return true
}

// Len returns the number of keys in this handle's view.
func (s *Store) Len() int {
	s.sh.mu.RLock()
	defer s.sh.mu.RUnlock()
	s.sync()
	return len(s.sh.data) + s.lenDelta
}

// Depth returns the number of open frames (transactions and savepoints).
func (s *Store) Depth() int { return len(s.frames) }

// Begin opens a nested transaction.
func (s *Store) Begin() { s.frames = append(s.frames, frame{}) }

// Rollback undoes the innermost transaction.
func (s *Store) Rollback() error {
	if len(s.frames) == 0 {
		return ErrNoTransaction
	}
	s.restoreTop()
	n := len(s.frames) - 1
	s.frames[n] = frame{} // let the GC have the log
	s.frames = s.frames[:n]
	return nil
}

// Commit ends the innermost transaction. An inner Commit merges the changes
// into the parent; the outermost Commit makes them final and can fail with
// ErrConflict (stage 3), in which case the whole transaction is aborted.
func (s *Store) Commit() error {
	n := len(s.frames)
	switch {
	case n == 0:
		return ErrNoTransaction
	case n > 1:
		top, parent := &s.frames[n-1], &s.frames[n-2]
		if len(parent.undo) == 0 {
			parent.undo = top.undo
		} else {
			// First touch wins: the parent's record is older than the child's.
			for k, rec := range top.undo {
				if _, has := parent.undo[k]; !has {
					parent.undo[k] = rec
				}
			}
		}
		s.frames[n-1] = frame{}
		s.frames = s.frames[:n-1]
		return nil
	}
	return s.sh.commit(s)
}

// ---- internals --------------------------------------------------------------

// view resolves key through the overlay, then the committed data. Requires sh.mu.
func (s *Store) view(key string) (string, bool) {
	if sl, ok := s.overlay[key]; ok {
		switch sl.kind {
		case put:
			return sl.value, true
		case del:
			return "", false
		}
	}
	c, ok := s.sh.data[key]
	return c.value, ok
}

// touch records key's overlay entry in the innermost frame's undo log, but
// only the first time the frame touches the key. Requires an open frame.
func (s *Store) touch(key string) {
	top := &s.frames[len(s.frames)-1]
	if _, done := top.undo[key]; done {
		return
	}
	if top.undo == nil {
		top.undo = map[string]undoRec{}
	}
	prev, had := s.overlay[key]
	top.undo[key] = undoRec{prev: prev, had: had}
}

// baseOf is the conflict base for a write to key: the base from the first
// touch if this transaction already has one, else the committed version now.
func (s *Store) baseOf(key string) uint64 {
	if sl, ok := s.overlay[key]; ok {
		return sl.base
	}
	return s.sh.versionOf(key)
}

// setSlot replaces (or, with nil, removes) key's overlay entry and keeps the
// incremental Len/Count deltas and the writers gauge in step. Rollback uses it
// too, so undoing a write adjusts the counters exactly like making it did.
// Requires sh.mu (read side is enough) and a fresh delta (call sync first).
func (s *Store) setSlot(key string, sl *slot) {
	before := len(s.overlay)
	if old, ok := s.overlay[key]; ok {
		s.account(key, old, -1)
	}
	if sl == nil {
		delete(s.overlay, key)
	} else {
		s.overlay[key] = *sl
		s.account(key, *sl, +1)
	}
	switch after := len(s.overlay); {
	case before == 0 && after > 0:
		s.sh.writers.Add(1)
	case before > 0 && after == 0:
		s.sh.writers.Add(-1)
	}
}

// account adds (sign +1) or removes (sign -1) the effect that sl has on the
// view relative to the committed data.
func (s *Store) account(key string, sl slot, sign int) {
	if sl.kind == guard {
		return
	}
	if c, ok := s.sh.data[key]; ok { // the overlay shadows this committed value
		s.bump(c.value, -sign)
		s.lenDelta -= sign
	}
	if sl.kind == put {
		s.bump(sl.value, sign)
		s.lenDelta += sign
	}
}

func (s *Store) bump(value string, d int) {
	if n := s.cntDelta[value] + d; n == 0 {
		delete(s.cntDelta, value)
	} else {
		s.cntDelta[value] = n
	}
}

// sync rebuilds the deltas when another handle has committed since they were
// computed (a foreign commit can change the committed value under one of this
// handle's overlay keys). O(len(overlay)), and only after a foreign commit.
// Requires sh.mu.
func (s *Store) sync() {
	if s.seen == s.sh.seq {
		return
	}
	s.seen = s.sh.seq
	clear(s.cntDelta)
	s.lenDelta = 0
	for k, sl := range s.overlay {
		s.account(k, sl, +1)
	}
}

// restoreTop replays the innermost frame's undo log into the overlay and
// empties the log. The frame itself stays on the stack.
func (s *Store) restoreTop() {
	s.sh.mu.RLock()
	defer s.sh.mu.RUnlock()
	s.sync()
	top := &s.frames[len(s.frames)-1]
	for k, rec := range top.undo {
		if rec.had {
			prev := rec.prev
			s.setSlot(k, &prev)
		} else {
			s.setSlot(k, nil)
		}
	}
	top.undo = nil
}

// versionOf is the commit sequence number that last changed key, whether it
// is live or was deleted. 0 means "never seen" (or forgotten, see tomb).
// Requires sh.mu.
func (sh *shared) versionOf(key string) uint64 {
	if c, ok := sh.data[key]; ok {
		return c.ver
	}
	return sh.tomb[key]
}

// commit validates and applies s's overlay as one atomic step, then resets s.
func (sh *shared) commit(s *Store) error {
	sh.mu.Lock()
	defer sh.mu.Unlock()
	if key, bad := sh.firstConflict(s.overlay); bad {
		s.finish()
		sh.sweepTombstones()
		return fmt.Errorf("%w: key %q was committed by another session after this transaction first touched it", ErrConflict, key)
	}
	if len(s.overlay) > 0 {
		sh.seq++
		for k, sl := range s.overlay {
			switch sl.kind {
			case put:
				sh.storeLocked(k, sl.value)
			case del:
				sh.removeLocked(k)
			}
		}
	}
	s.finish()
	sh.sweepTombstones()
	return nil
}

// finish clears s's transaction state after a commit or an abort.
func (s *Store) finish() {
	if len(s.overlay) > 0 {
		s.sh.writers.Add(-1)
	}
	clear(s.overlay)
	clear(s.frames)
	s.frames = s.frames[:0]
	clear(s.cntDelta)
	s.lenDelta = 0
	s.seen = s.sh.seq
}

// sweepTombstones forgets delete versions once no handle holds a write set:
// nobody can hold a base version that needs them. Requires sh.mu (write).
func (sh *shared) sweepTombstones() {
	if sh.writers.Load() == 0 {
		clear(sh.tomb)
	}
}

// autocommit is a single-key transaction for writes outside Begin/Commit.
func (sh *shared) autocommit(key, value string, isSet bool) bool {
	sh.mu.Lock()
	defer sh.mu.Unlock()
	if isSet {
		sh.seq++
		sh.storeLocked(key, value)
		return true
	}
	if _, ok := sh.data[key]; !ok {
		return false
	}
	sh.seq++
	sh.removeLocked(key)
	return true
}

// storeLocked and removeLocked mutate committed state at version sh.seq.
func (sh *shared) storeLocked(key, value string) {
	if old, ok := sh.data[key]; ok {
		sh.decCount(old.value)
	}
	sh.data[key] = cell{value: value, ver: sh.seq}
	sh.counts[value]++
	delete(sh.tomb, key)
}

func (sh *shared) removeLocked(key string) {
	old, ok := sh.data[key]
	if !ok {
		return
	}
	sh.decCount(old.value)
	delete(sh.data, key)
	if sh.writers.Load() > 0 { // someone may hold a base version of this key
		sh.tomb[key] = sh.seq
	}
}

func (sh *shared) decCount(value string) {
	if sh.counts[value]--; sh.counts[value] == 0 {
		delete(sh.counts, value)
	}
}
