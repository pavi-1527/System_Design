package shorty

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
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

// ParseSyncPolicy parses "always" or "never".
func ParseSyncPolicy(s string) (SyncPolicy, error) {
	switch s {
	case "always":
		return SyncAlways, nil
	case "never":
		return SyncNever, nil
	}
	return 0, fmt.Errorf("unknown sync policy %q (want always or never)", s)
}

// Limits the log enforces on every write.
const (
	MaxKeyLen   = 1024
	MaxValueLen = 64 << 10
)

// ErrCorrupt means a record in the middle of the log failed its checksum (or
// is malformed) while valid-looking data follows it. That is not a crash
// artefact: truncating there would silently delete the records after it, so
// the log refuses to open and an operator decides.
var ErrCorrupt = errors.New("log corrupt")

// errLogBroken is returned for every write after a failed write or fsync. After
// a failed fsync the kernel may already have dropped the dirty pages, so a
// retry that "succeeds" can still have lost data; the only safe answer is to
// stop accepting writes and recover from the file on restart.
var errLogBroken = errors.New("log unusable after a failed write or fsync; restart to recover")

// Recovery reports what OpenLog found.
type Recovery struct {
	Records        int   // valid records replayed into the index
	TruncatedBytes int64 // bytes of torn tail removed from the end of the file
}

// On-disk record format (big endian; spec.md "On-disk format"):
//
//	crc32c  uint32   Castagnoli CRC of every byte after this field
//	length  uint32   number of body bytes
//	body:   op uint8 (1 = put, 2 = delete) | keylen uint16 | key | value
//
// The value is everything after the key, so a delete has an empty value.
const (
	opPut      byte = 1
	opDelete   byte = 2
	headerSize      = 8
	minBody         = 3
	maxBody         = minBody + MaxKeyLen + MaxValueLen
)

var castagnoli = crc32.MakeTable(crc32.Castagnoli)

func encodeRecord(op byte, key string, val []byte) []byte {
	n := minBody + len(key) + len(val)
	rec := make([]byte, headerSize, headerSize+n)
	binary.BigEndian.PutUint32(rec[4:8], uint32(n))
	rec = append(rec, op)
	rec = binary.BigEndian.AppendUint16(rec, uint16(len(key)))
	rec = append(rec, key...)
	rec = append(rec, val...)
	binary.BigEndian.PutUint32(rec[0:4], crc32.Checksum(rec[4:], castagnoli))
	return rec
}

// decodeBody parses a body whose checksum already matched.
func decodeBody(body []byte) (op byte, key string, val []byte, ok bool) {
	if len(body) < minBody {
		return 0, "", nil, false
	}
	op = body[0]
	kl := int(binary.BigEndian.Uint16(body[1:3]))
	if (op != opPut && op != opDelete) || minBody+kl > len(body) {
		return 0, "", nil, false
	}
	return op, string(body[3 : 3+kl]), body[3+kl:], true
}

// Log is an append-only, checksummed key-value log with an in-memory index of
// the latest value of every key (Bitcask keeps file offsets instead of values;
// at this size the values fit in memory). Safe for concurrent use: one writer
// at a time, many readers.
type Log struct {
	mu     sync.RWMutex
	f      *os.File
	policy SyncPolicy
	size   int64 // offset of the next record
	data   map[string][]byte
	broken error
}

// OpenLog opens or creates the log at path and replays it into memory.
//
// Recovery rules (the tests check each):
//   - a record that runs past the end of the file is a torn tail: truncate the
//     file at the record's start and carry on;
//   - a complete record with a bad checksum or a malformed body is a torn tail
//     too if it is the last thing in the file (a crash can persist a record's
//     length before its bytes), and so is a tail that is all zero bytes;
//   - anything else is mid-file corruption: return ErrCorrupt and leave the
//     file untouched.
func OpenLog(path string, policy SyncPolicy) (*Log, Recovery, error) {
	var rec Recovery
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return nil, rec, err
	}
	b, err := io.ReadAll(f)
	if err != nil {
		f.Close()
		return nil, rec, err
	}
	l := &Log{f: f, policy: policy, data: map[string][]byte{}}
	off := 0
	for off < len(b) {
		rest := b[off:]
		if len(rest) < headerSize || allZero(rest) {
			break // torn tail
		}
		n := int(binary.BigEndian.Uint32(rest[4:8]))
		end := headerSize + n
		if n < 0 || end > len(rest) {
			break // the record runs past EOF: torn tail
		}
		crcOK := crc32.Checksum(rest[4:end], castagnoli) == binary.BigEndian.Uint32(rest[0:4])
		op, key, val, ok := decodeBody(rest[headerSize:end])
		if !crcOK || !ok || n > maxBody {
			if end == len(rest) {
				break // bad last record: torn tail
			}
			f.Close()
			return nil, rec, fmt.Errorf("%w: bad record at offset %d of %d bytes", ErrCorrupt, off, len(b))
		}
		switch op {
		case opPut:
			l.data[key] = bytes.Clone(val)
		case opDelete:
			delete(l.data, key)
		}
		rec.Records++
		off += end
	}
	if off < len(b) {
		rec.TruncatedBytes = int64(len(b) - off)
		if err := f.Truncate(int64(off)); err != nil {
			f.Close()
			return nil, rec, err
		}
		if err := f.Sync(); err != nil {
			f.Close()
			return nil, rec, err
		}
	}
	l.size = int64(off)
	return l, rec, nil
}

func allZero(b []byte) bool {
	for _, c := range b {
		if c != 0 {
			return false
		}
	}
	return true
}

func checkKV(key string, val []byte) error {
	if key == "" || len(key) > MaxKeyLen {
		return fmt.Errorf("key length %d not in 1..%d", len(key), MaxKeyLen)
	}
	if len(val) > MaxValueLen {
		return fmt.Errorf("value length %d exceeds %d", len(val), MaxValueLen)
	}
	return nil
}

// append writes one record (and fsyncs under SyncAlways). Caller holds l.mu.
func (l *Log) append(op byte, key string, val []byte) error {
	if l.broken != nil {
		return l.broken
	}
	if l.f == nil {
		return os.ErrClosed
	}
	rec := encodeRecord(op, key, val)
	if _, err := l.f.WriteAt(rec, l.size); err != nil {
		l.broken = fmt.Errorf("%w: %v", errLogBroken, err)
		return l.broken
	}
	if l.policy == SyncAlways {
		if err := l.f.Sync(); err != nil {
			l.broken = fmt.Errorf("%w: %v", errLogBroken, err)
			return l.broken
		}
	}
	l.size += int64(len(rec))
	return nil
}

// Get returns a copy of the latest value for key.
func (l *Log) Get(key string) ([]byte, bool) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	v, ok := l.data[key]
	if !ok {
		return nil, false
	}
	return bytes.Clone(v), true
}

// Put durably sets key to val (durable as far as the SyncPolicy promises)
// before it returns.
func (l *Log) Put(key string, val []byte) error {
	if err := checkKV(key, val); err != nil {
		return err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := l.append(opPut, key, val); err != nil {
		return err
	}
	l.data[key] = bytes.Clone(val)
	return nil
}

// PutIfAbsent sets key only if it has no value. It reports whether it inserted;
// when it did not, existing is the current value. Check and write happen under
// one lock, so of many concurrent callers exactly one inserts.
func (l *Log) PutIfAbsent(key string, val []byte) (inserted bool, existing []byte, err error) {
	if err := checkKV(key, val); err != nil {
		return false, nil, err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if v, ok := l.data[key]; ok {
		return false, bytes.Clone(v), nil
	}
	if err := l.append(opPut, key, val); err != nil {
		return false, nil, err
	}
	l.data[key] = bytes.Clone(val)
	return true, nil, nil
}

// Delete removes key. Deleting an absent key writes nothing.
func (l *Log) Delete(key string) error {
	if err := checkKV(key, nil); err != nil {
		return err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if _, ok := l.data[key]; !ok {
		return nil
	}
	if err := l.append(opDelete, key, nil); err != nil {
		return err
	}
	delete(l.data, key)
	return nil
}

// Len is the number of live keys.
func (l *Log) Len() int {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return len(l.data)
}

// Close syncs and closes the file. Writes after Close fail.
func (l *Log) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.f == nil {
		return nil
	}
	err := l.f.Sync()
	if cerr := l.f.Close(); err == nil {
		err = cerr
	}
	l.f = nil
	return err
}
