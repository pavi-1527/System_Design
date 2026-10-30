// Package wal is the write-ahead log under the E2 replicated KV store: an
// append-only file of checksummed, length-prefixed records, one per log entry,
// fsynced before a write is acknowledged and recovered on start-up by keeping
// every complete, valid record and cutting off whatever a crash left at the
// tail.
//
// It is the lab for E2 Unit 1 §1. The ideas are A2 Unit 2 §4 (durability and
// the write-ahead log) made real: the same record framing Postgres, etcd and
// Kafka use in spirit, minus segments, compaction and snapshots.
//
// Record layout (all integers big-endian):
//
//	+---------+----------+------------------------------------------------+
//	| len u32 | crc  u32 | payload (len bytes)                            |
//	+---------+----------+------------------------------------------------+
//	payload = epoch u64 | index u64 | time i64 | op u8 |
//	          uvarint keyLen | key | uvarint valueLen | value
//
// crc is CRC-32C (Castagnoli) over the payload. Indexes in a log are
// contiguous and start at 1, so entry i lives at position i-1.
//
// Suggested order: Encode and Decode (the round-trip and torn/corrupt tests),
// then Open with recovery, then Append, TruncateAfter, LastIndex, Size and
// Close. Provided: the types, constants, errors and syncDir.
//
// Run: go test -race -run TestM1 ./labs/e2/wal/
package wal

import (
	"errors"
	"os"
	"sync"
)

// Op is what an entry does to its key.
type Op uint8

const (
	// OpPut sets Key to Value.
	OpPut Op = 1
	// OpDelete removes Key.
	OpDelete Op = 2
)

// Entry is one replicated write. Epoch and Index identify it uniquely across
// the cluster: only one leader ever writes in a given epoch, and it assigns
// each index once.
type Entry struct {
	Epoch uint64 `json:"epoch"`
	Index uint64 `json:"index"`
	Op    Op     `json:"op"`
	Key   string `json:"key"`
	Value []byte `json:"value,omitempty"`
	Time  int64  `json:"time"` // leader wall clock when written, Unix nanoseconds (lag in seconds)
}

// Errors returned by Decode, Open and Append.
var (
	// ErrTorn means the data ends in the middle of a record: a write that
	// never finished (the process or the machine died during it).
	ErrTorn = errors.New("wal: torn record at end of log")
	// ErrCorrupt means a record is complete but its checksum, length or
	// payload is invalid.
	ErrCorrupt = errors.New("wal: corrupt record")
	// ErrGap means an entry's index does not follow the previous one.
	ErrGap = errors.New("wal: index not contiguous")
	// ErrClosed is returned after Close.
	ErrClosed = errors.New("wal: log closed")

	errNotImplemented = errors.New("wal: not implemented")
)

// HeaderSize is the length prefix plus the checksum.
const HeaderSize = 8

// MaxPayload bounds one record; a length above it is treated as corruption
// rather than as a reason to allocate gigabytes.
const MaxPayload = 4 << 20

// Encode frames e as one record (layout in the package comment). Use
// crc32.MakeTable(crc32.Castagnoli) for the checksum and
// encoding/binary.PutUvarint for the two lengths.
func Encode(e Entry) []byte {
	// TODO(lab): build the payload, then prepend the length and the CRC-32C.
	return nil
}

// Decode reads the first record in b. It returns the entry and the number of
// bytes the record occupies. If b ends before the record does (fewer than
// HeaderSize bytes, or fewer than HeaderSize+len), it returns ErrTorn; if the
// record is complete but invalid (length outside [27, MaxPayload], checksum
// mismatch, lengths inside the payload that do not add up, unknown op), an
// error wrapping ErrCorrupt. Check the length before you allocate or slice.
func Decode(b []byte) (Entry, int, error) {
	// TODO(lab)
	return Entry{}, 0, errNotImplemented
}

// SyncPolicy says when Append calls fsync.
type SyncPolicy int

const (
	// SyncAlways fsyncs before Append returns: an acknowledged entry survives
	// a crash of the process and of the machine.
	SyncAlways SyncPolicy = iota
	// SyncNever leaves flushing to the OS: survives a process crash (the page
	// cache still has it), not a power loss. For break-it comparisons only.
	SyncNever
)

// Recovery reports what Open found.
type Recovery struct {
	Entries        []Entry // every valid entry, in index order
	TruncatedBytes int64   // bytes cut off the tail (0 after a clean shutdown)
	Reason         error   // why the tail was cut (ErrTorn, ErrCorrupt, ErrGap), nil if nothing was
}

// Log is an open write-ahead log. It is safe for concurrent use.
//
// Suggested state: the *os.File, the policy, offsets[i-1] = file offset where
// entry i starts (TruncateAfter needs it), the current size, a closed flag,
// all under one mutex.
type Log struct {
	mu sync.Mutex
	f  *os.File
	// TODO(lab): the rest of the state.
}

// Open opens or creates the log at path and recovers it: it keeps the longest
// prefix of valid records whose indexes are contiguous from 1, truncates the
// file after it (and fsyncs the truncation), and returns those entries. A
// newly created file also needs its directory fsynced (syncDir).
func Open(path string, policy SyncPolicy) (*Log, Recovery, error) {
	// TODO(lab): open with os.O_RDWR|os.O_CREATE, read it all, Decode in a
	// loop, stop at the first error or index gap, Truncate + Sync, Seek to
	// the end of the good prefix.
	return nil, Recovery{}, errNotImplemented
}

// LastIndex is the index of the last entry (0 when empty).
func (l *Log) LastIndex() uint64 {
	// TODO(lab)
	return 0
}

// Append writes entries as one write and, under SyncAlways, one fsync (a tiny
// group commit). The first entry's index must be LastIndex()+1 and the rest
// must follow it; otherwise nothing is written and the error wraps ErrGap.
// After Close it returns ErrClosed.
func (l *Log) Append(entries ...Entry) error {
	// TODO(lab)
	return errNotImplemented
}

// TruncateAfter removes every entry with an index above index, durably
// (truncate, fsync). Past the end it is a no-op. It is how a follower drops a
// divergent suffix it got from a deposed leader.
func (l *Log) TruncateAfter(index uint64) error {
	// TODO(lab)
	return errNotImplemented
}

// Size is the file size in bytes.
func (l *Log) Size() int64 {
	// TODO(lab)
	return 0
}

// Close flushes and closes the file. Further calls return ErrClosed.
func (l *Log) Close() error {
	// TODO(lab)
	return errNotImplemented
}

// syncDir fsyncs a directory so a newly created or renamed file's directory
// entry is durable too (A2 Unit 2 §4: the file can survive while its name
// does not). Provided.
func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}
