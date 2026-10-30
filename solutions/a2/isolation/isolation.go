// Package isolation is a multi-version (MVCC) key/value store with
// transactions at three isolation levels, built to make the classic anomalies
// reproducible. It is the lab for A2 Unit 3 §7–§8.
//
// Every committed write creates a new version of a key stamped with a commit
// timestamp from a global counter; versions are never updated in place. A
// transaction buffers its writes privately and installs them atomically at
// Commit. The levels differ in which versions a transaction reads and in what
// Commit validates:
//
//   - ReadCommitted: every read sees the newest committed version at the
//     moment of the read (so two reads of one key can differ: a
//     non-repeatable read). Commit performs no checks, so the last committer
//     silently overwrites: lost updates happen.
//   - SnapshotIsolation: the transaction reads the database as of the
//     instant it began (Begin records clock; a version is visible if its
//     commit timestamp is <= that snapshot) plus its own buffered writes.
//     Commit applies first-committer-wins: if any key in the write set has a
//     committed version newer than the snapshot, Commit fails with
//     ErrWriteConflict and installs nothing. Lost updates are prevented;
//     write skew and phantom-based invariant violations are NOT.
//   - Serializable: snapshot isolation plus read validation at Commit (an
//     optimistic "backward validation", the idea behind the read-conflict
//     check in several optimistic engines): if any key the transaction read
//     (including reads that found nothing) or any key under a prefix it
//     scanned has a committed version newer than the snapshot, Commit fails
//     with ErrSerialization. A transaction that wrote nothing never fails.
//     This is stricter than PostgreSQL's SSI (it aborts on every read-write
//     overlap, not only on dangerous structures) but it is simple and safe.
//
// A failed Commit makes the transaction finished and installs none of its
// writes (atomicity). Transactions are not safe for concurrent use by
// several goroutines; the Store is.
//
// Commit timestamps: Commit assigns clock+1 to all of a transaction's
// versions. Vacuum discards versions no snapshot can still see.
//
// Suggested order: readAt and Tx.Get/Put/Delete/Rollback (visibility, no
// dirty reads), Commit for ReadCommitted, then the write-conflict check,
// Scan, the Serializable validation, finally Vacuum. Run:
// go test -race ./labs/a2/isolation/
package isolation

import (
	"errors"
	"sort"
	"strings"
	"sync"
)

// Level is a transaction isolation level.
type Level int

// Isolation levels.
const (
	ReadCommitted Level = iota + 1
	SnapshotIsolation
	Serializable
)

func (l Level) String() string {
	switch l {
	case ReadCommitted:
		return "read-committed"
	case SnapshotIsolation:
		return "snapshot-isolation"
	case Serializable:
		return "serializable"
	}
	return "unknown"
}

var (
	// ErrWriteConflict: another transaction committed a newer version of a
	// key this transaction wrote after this transaction's snapshot (first
	// committer wins). Retry the whole transaction.
	ErrWriteConflict = errors.New("isolation: write-write conflict (first committer wins)")
	// ErrSerialization: something this transaction read was changed by a
	// transaction that committed after its snapshot. Retry the whole
	// transaction.
	ErrSerialization = errors.New("isolation: serialization failure (data this transaction read has changed)")
	// ErrTxDone: the transaction already committed or rolled back.
	ErrTxDone = errors.New("isolation: transaction already finished")
	// ErrTooManyRetries is returned by Store.Run when every attempt hit a
	// conflict.
	ErrTooManyRetries = errors.New("isolation: transaction kept conflicting; giving up")
)

// KV is one key/value pair returned by Scan.
type KV struct {
	Key   string
	Value string
}

// version is one committed version of a key. Versions of a key are kept in
// ascending commit-timestamp order.
type version struct {
	ts      uint64
	value   string
	deleted bool
}

type write struct {
	value   string
	deleted bool
}

// Store is the multi-version store.
type Store struct {
	mu     sync.Mutex
	clock  uint64 // commit timestamp of the newest commit
	data   map[string][]version
	active map[*Tx]struct{}
}

// NewStore returns an empty store. Provided.
func NewStore() *Store {
	return &Store{data: map[string][]version{}, active: map[*Tx]struct{}{}}
}

// Tx is one transaction.
type Tx struct {
	s        *Store
	level    Level
	snapshot uint64
	writes   map[string]write
	reads    map[string]struct{} // keys read from the store (found or not)
	scans    []string            // prefixes scanned
	done     bool
}

// Begin starts a transaction. Its snapshot is the store's clock at this
// instant, which is what SnapshotIsolation and Serializable read from.
// Provided.
func (s *Store) Begin(level Level) *Tx {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx := &Tx{s: s, level: level, snapshot: s.clock, writes: map[string]write{}, reads: map[string]struct{}{}}
	s.active[tx] = struct{}{}
	return tx
}

// Level returns the transaction's isolation level. Provided.
func (tx *Tx) Level() Level { return tx.level }

// Snapshot returns the commit timestamp the transaction's snapshot was taken
// at. Provided.
func (tx *Tx) Snapshot() uint64 { return tx.snapshot }

// Clock returns the commit timestamp of the newest commit. Provided.
func (s *Store) Clock() uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.clock
}

// Versions returns how many versions (including deletion markers) are stored
// for key. Provided.
func (s *Store) Versions(key string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.data[key])
}

// latestTs returns the commit timestamp of the newest stored version of key,
// or 0 if none. Caller holds s.mu. Provided.
func (s *Store) latestTs(key string) uint64 {
	vs := s.data[key]
	if len(vs) == 0 {
		return 0
	}
	return vs[len(vs)-1].ts
}

// finish marks tx done and removes it from the active set. Caller holds s.mu.
// Provided.
func (s *Store) finish(tx *Tx) {
	tx.done = true
	delete(s.active, tx)
}

// Run executes fn in a new transaction at the given level and commits it. If
// the commit (or fn) reports ErrWriteConflict or ErrSerialization, the whole
// transaction is retried, up to maxAttempts attempts in total. Any other
// error from fn rolls back and is returned as is. attempts is the number of
// transactions started. Provided.
func (s *Store) Run(level Level, maxAttempts int, fn func(tx *Tx) error) (attempts int, err error) {
	for attempts < maxAttempts {
		attempts++
		tx := s.Begin(level)
		err = fn(tx)
		if err == nil {
			err = tx.Commit()
		} else {
			tx.Rollback()
		}
		if err == nil {
			return attempts, nil
		}
		if !errors.Is(err, ErrWriteConflict) && !errors.Is(err, ErrSerialization) {
			return attempts, err
		}
	}
	return attempts, ErrTooManyRetries
}

// readAt returns the value of the newest version of key with commit
// timestamp <= ts. found is false if there is no such version or it is a
// deletion marker. Caller holds s.mu.
func (s *Store) readAt(key string, ts uint64) (value string, found bool) {
	vs := s.data[key]
	i := sort.Search(len(vs), func(i int) bool { return vs[i].ts > ts })
	if i == 0 {
		return "", false
	}
	v := vs[i-1]
	if v.deleted {
		return "", false
	}
	return v.value, true
}

// readTs is the timestamp this transaction reads at: its snapshot, or the
// newest commit for ReadCommitted. Caller holds tx.s.mu.
func (tx *Tx) readTs() uint64 {
	if tx.level == ReadCommitted {
		return tx.s.clock
	}
	return tx.snapshot
}

// Get returns the value of key as this transaction sees it: its own buffered
// write if any, otherwise the version visible at its read timestamp. It
// records the read for Serializable validation.
func (tx *Tx) Get(key string) (string, bool) {
	if w, ok := tx.writes[key]; ok {
		return w.value, !w.deleted
	}
	tx.s.mu.Lock()
	defer tx.s.mu.Unlock()
	tx.reads[key] = struct{}{}
	return tx.s.readAt(key, tx.readTs())
}

// Put buffers a write of key. Nothing is visible to others until Commit.
// ErrTxDone if the transaction has finished.
func (tx *Tx) Put(key, value string) error {
	if tx.done {
		return ErrTxDone
	}
	tx.writes[key] = write{value: value}
	return nil
}

// Delete buffers a deletion of key. ErrTxDone if the transaction has
// finished.
func (tx *Tx) Delete(key string) error {
	if tx.done {
		return ErrTxDone
	}
	tx.writes[key] = write{deleted: true}
	return nil
}

// Scan returns, sorted by key, every key with the given prefix ("" = all)
// that exists as this transaction sees it: the versions visible at its read
// timestamp, overlaid with its own buffered writes and deletions. It records
// the prefix as a predicate read for Serializable validation (this is what
// catches phantoms).
func (tx *Tx) Scan(prefix string) []KV {
	tx.s.mu.Lock()
	defer tx.s.mu.Unlock()
	ts := tx.readTs()
	seen := map[string]string{}
	for k := range tx.s.data {
		if !strings.HasPrefix(k, prefix) {
			continue
		}
		if v, ok := tx.s.readAt(k, ts); ok {
			seen[k] = v
		}
	}
	for k, w := range tx.writes {
		if !strings.HasPrefix(k, prefix) {
			continue
		}
		if w.deleted {
			delete(seen, k)
		} else {
			seen[k] = w.value
		}
	}
	tx.scans = append(tx.scans, prefix)
	out := make([]KV, 0, len(seen))
	for k, v := range seen {
		out = append(out, KV{k, v})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

// Rollback discards the transaction. Safe to call after Commit (no-op).
func (tx *Tx) Rollback() {
	tx.s.mu.Lock()
	defer tx.s.mu.Unlock()
	tx.s.finish(tx)
}

// Commit validates and installs the transaction's writes atomically. See the
// package comment for what each level validates. A transaction with no writes
// always commits. On any error nothing is installed and the transaction is
// finished. ErrTxDone if it was already finished.
func (tx *Tx) Commit() error {
	s := tx.s
	s.mu.Lock()
	defer s.mu.Unlock()
	if tx.done {
		return ErrTxDone
	}
	s.finish(tx)
	if len(tx.writes) == 0 {
		return nil
	}
	if tx.level >= SnapshotIsolation {
		for k := range tx.writes {
			if s.latestTs(k) > tx.snapshot {
				return ErrWriteConflict
			}
		}
	}
	if tx.level == Serializable {
		for k := range tx.reads {
			if s.latestTs(k) > tx.snapshot {
				return ErrSerialization
			}
		}
		for _, prefix := range tx.scans {
			for k := range s.data {
				if strings.HasPrefix(k, prefix) && s.latestTs(k) > tx.snapshot {
					return ErrSerialization
				}
			}
		}
	}
	s.clock++
	for k, w := range tx.writes {
		s.data[k] = append(s.data[k], version{ts: s.clock, value: w.value, deleted: w.deleted})
	}
	return nil
}

// Vacuum discards versions that no current or future snapshot can read and
// returns how many it removed. The horizon is the oldest snapshot among
// unfinished SnapshotIsolation/Serializable transactions (or the current
// clock if there are none). For each key, keep every version newer than the
// horizon and the newest version at or before it (the one a transaction at
// the horizon reads); remove older ones. If what remains is a single
// deletion marker at or before the horizon, remove it too (the key is gone
// for everyone). This is PostgreSQL's VACUUM in miniature: a long-running
// transaction holds the horizon back and bloat accumulates.
func (s *Store) Vacuum() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	horizon := s.clock
	for tx := range s.active {
		if tx.level != ReadCommitted && tx.snapshot < horizon {
			horizon = tx.snapshot
		}
	}
	removed := 0
	for k, vs := range s.data {
		// first version newer than the horizon
		i := sort.Search(len(vs), func(i int) bool { return vs[i].ts > horizon })
		keepFrom := i - 1 // newest version visible at the horizon
		if keepFrom < 0 {
			keepFrom = 0
		}
		if i == len(vs) && i > 0 && vs[i-1].deleted {
			removed += len(vs)
			delete(s.data, k)
			continue
		}
		if keepFrom > 0 {
			removed += keepFrom
			s.data[k] = append([]version(nil), vs[keepFrom:]...)
		}
	}
	return removed
}
