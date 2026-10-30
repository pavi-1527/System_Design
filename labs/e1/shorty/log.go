package shorty

import (
	"errors"
	"fmt"
	"hash/crc32"
	"os"
	"sync"
)

// SyncPolicy says when the log forces its writes to stable storage.
type SyncPolicy int

const (
	// SyncAlways fsyncs after every record, before the write is acknowledged.
	// An acknowledged write survives a power cut. Throughput ≈ 1 / fsync time.
	SyncAlways SyncPolicy = iota
	// SyncNever leaves flushing to the OS. A process crash (SIGKILL) loses
	// nothing, because the page cache survives the process; a power cut or
	// kernel panic loses whatever the OS had not yet written back.
	SyncNever
)

func (p SyncPolicy) String() string {
	switch p {
	case SyncAlways:
		return "always"
	case SyncNever:
		return "never"
	}
	return fmt.Sprintf("SyncPolicy(%d)", int(p))
}

// ParseSyncPolicy parses "always" or "never"; anything else is an error.
func ParseSyncPolicy(s string) (SyncPolicy, error) {
	// TODO(lab): a warm-up. An unknown durability setting must be an error,
	// never a silent default.
	return 0, errNotImplemented
}

// Limits the log enforces on every write. Provided.
const (
	MaxKeyLen   = 1024
	MaxValueLen = 64 << 10
)

// ErrCorrupt means a record in the middle of the log failed its checksum (or
// is malformed) while valid-looking data follows it. That is not a crash
// artefact: truncating there would silently delete the records after it, so
// the log refuses to open and an operator decides. Provided.
var ErrCorrupt = errors.New("log corrupt")

// errLogBroken is for every write after a failed write or fsync. After a
// failed fsync the kernel may already have dropped the dirty pages, so a retry
// that "succeeds" can still have lost data; the only safe answer is to stop
// accepting writes and recover from the file on restart. Provided.
var errLogBroken = errors.New("log unusable after a failed write or fsync; restart to recover")

// Recovery reports what OpenLog found. Provided.
type Recovery struct {
	Records        int   // valid records replayed into the index
	TruncatedBytes int64 // bytes of torn tail removed from the end of the file
}

// On-disk record format (big endian; spec.md "On-disk format"). The tests
// write and read these bytes themselves, so follow it exactly:
//
//	crc32c  uint32   Castagnoli CRC of every byte after this field
//	length  uint32   number of body bytes
//	body:   op uint8 (1 = put, 2 = delete) | keylen uint16 | key | value
//
// The value is everything after the key, so a delete has an empty value.
// Provided.
const (
	opPut      byte = 1
	opDelete   byte = 2
	headerSize      = 8
	minBody         = 3
	maxBody         = minBody + MaxKeyLen + MaxValueLen
)

var castagnoli = crc32.MakeTable(crc32.Castagnoli)

// encodeRecord returns one record in the format above.
func encodeRecord(op byte, key string, val []byte) []byte {
	// TODO(lab): encoding/binary's BigEndian.PutUint32 / AppendUint16, then
	// crc32.Checksum(rec[4:], castagnoli) into rec[0:4].
	return nil
}

// Log is an append-only, checksummed key-value log with an in-memory index of
// the latest value of every key (Bitcask keeps file offsets instead of values;
// at this size the values fit in memory). Safe for concurrent use: one writer
// at a time, many readers.
//
// The fields are a suggestion; the tests only use the methods.
type Log struct {
	mu     sync.RWMutex
	f      *os.File
	policy SyncPolicy
	size   int64             // offset of the next record
	data   map[string][]byte // key → latest value
	broken error             // set by a failed write or fsync; returned by every later write
}

// OpenLog opens or creates the log at path and replays it into memory.
//
// Recovery rules (the tests check each):
//   - a record that runs past the end of the file is a torn tail: truncate the
//     file at the record's start (and fsync) and carry on;
//   - a complete record with a bad checksum or a malformed body is a torn tail
//     too if it is the last thing in the file (a crash can persist a record's
//     length before its bytes), and so is a tail that is all zero bytes;
//   - anything else is mid-file corruption: return an error wrapping
//     ErrCorrupt and leave the file untouched.
//
// Never return a nil *Log with a nil error.
func OpenLog(path string, policy SyncPolicy) (*Log, Recovery, error) {
	// TODO(lab): open with os.O_RDWR|os.O_CREATE, read it all, walk the
	// records applying puts and deletes to the map, truncate a torn tail,
	// remember where the next record goes.
	return nil, Recovery{}, errNotImplemented
}

// checkKV validates a key and value against the limits. Provided.
func checkKV(key string, val []byte) error {
	if key == "" || len(key) > MaxKeyLen {
		return fmt.Errorf("key length %d not in 1..%d", len(key), MaxKeyLen)
	}
	if len(val) > MaxValueLen {
		return fmt.Errorf("value length %d exceeds %d", len(val), MaxValueLen)
	}
	return nil
}

// append writes one record at the end of the file and, under SyncAlways,
// fsyncs before returning. A failed write or fsync poisons the log (broken).
// Caller holds l.mu.
func (l *Log) append(op byte, key string, val []byte) error {
	// TODO(lab): WriteAt(rec, l.size), Sync if SyncAlways, then advance size.
	// Writes after Close must fail.
	return errNotImplemented
}

// Get returns a copy of the latest value for key.
func (l *Log) Get(key string) ([]byte, bool) {
	// TODO(lab): read lock; return a copy (bytes.Clone) so callers can't
	// mutate the index.
	return nil, false
}

// Put durably sets key to val (durable as far as the SyncPolicy promises)
// before it returns. The log keeps its own copy of val.
func (l *Log) Put(key string, val []byte) error {
	// TODO(lab): checkKV, lock, append, then update the map. Append first:
	// the index must never show a value the file doesn't have.
	return errNotImplemented
}

// PutIfAbsent sets key only if it has no value. It reports whether it inserted;
// when it did not, existing is the current value. Check and write happen under
// one lock, so of many concurrent callers exactly one inserts — that is what
// lets several API instances allocate codes without talking to each other.
func (l *Log) PutIfAbsent(key string, val []byte) (inserted bool, existing []byte, err error) {
	// TODO(lab)
	return false, nil, errNotImplemented
}

// Delete removes key. Deleting an absent key writes nothing.
func (l *Log) Delete(key string) error {
	// TODO(lab): log a delete record, then remove from the map.
	return errNotImplemented
}

// Len is the number of live keys.
func (l *Log) Len() int {
	// TODO(lab)
	return 0
}

// Close syncs and closes the file. Writes after Close fail.
func (l *Log) Close() error {
	// TODO(lab)
	return nil
}
