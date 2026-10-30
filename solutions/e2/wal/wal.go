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
// Run: go test -race ./labs/e2/wal/
package wal

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"os"
	"path/filepath"
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
)

// HeaderSize is the length prefix plus the checksum.
const HeaderSize = 8

// MaxPayload bounds one record; a length above it is treated as corruption
// rather than as a reason to allocate gigabytes.
const MaxPayload = 4 << 20

var castagnoli = crc32.MakeTable(crc32.Castagnoli)

// Encode frames e as one record.
func Encode(e Entry) []byte {
	var p bytes.Buffer
	var u [binary.MaxVarintLen64]byte
	var fixed [25]byte
	binary.BigEndian.PutUint64(fixed[0:], e.Epoch)
	binary.BigEndian.PutUint64(fixed[8:], e.Index)
	binary.BigEndian.PutUint64(fixed[16:], uint64(e.Time))
	fixed[24] = byte(e.Op)
	p.Write(fixed[:])
	p.Write(u[:binary.PutUvarint(u[:], uint64(len(e.Key)))])
	p.WriteString(e.Key)
	p.Write(u[:binary.PutUvarint(u[:], uint64(len(e.Value)))])
	p.Write(e.Value)

	payload := p.Bytes()
	out := make([]byte, HeaderSize+len(payload))
	binary.BigEndian.PutUint32(out[0:], uint32(len(payload)))
	binary.BigEndian.PutUint32(out[4:], crc32.Checksum(payload, castagnoli))
	copy(out[HeaderSize:], payload)
	return out
}

// Decode reads the first record in b. It returns the entry and the number of
// bytes the record occupies. If b ends before the record does, it returns
// ErrTorn; if the record is complete but invalid, ErrCorrupt.
func Decode(b []byte) (Entry, int, error) {
	if len(b) < HeaderSize {
		return Entry{}, 0, ErrTorn
	}
	n := binary.BigEndian.Uint32(b[0:4])
	if n > MaxPayload || n < 27 {
		return Entry{}, 0, fmt.Errorf("%w: length %d", ErrCorrupt, n)
	}
	if uint64(len(b)) < uint64(HeaderSize)+uint64(n) {
		return Entry{}, 0, ErrTorn
	}
	payload := b[HeaderSize : HeaderSize+int(n)]
	if crc32.Checksum(payload, castagnoli) != binary.BigEndian.Uint32(b[4:8]) {
		return Entry{}, 0, fmt.Errorf("%w: checksum mismatch", ErrCorrupt)
	}
	e := Entry{
		Epoch: binary.BigEndian.Uint64(payload[0:]),
		Index: binary.BigEndian.Uint64(payload[8:]),
		Time:  int64(binary.BigEndian.Uint64(payload[16:])),
		Op:    Op(payload[24]),
	}
	rest := payload[25:]
	kl, k := binary.Uvarint(rest)
	if k <= 0 || kl > uint64(len(rest)-k) {
		return Entry{}, 0, fmt.Errorf("%w: bad key length", ErrCorrupt)
	}
	rest = rest[k:]
	e.Key = string(rest[:kl])
	rest = rest[kl:]
	vl, k := binary.Uvarint(rest)
	if k <= 0 || vl != uint64(len(rest)-k) {
		return Entry{}, 0, fmt.Errorf("%w: bad value length", ErrCorrupt)
	}
	if vl > 0 {
		e.Value = append([]byte(nil), rest[k:]...)
	}
	if e.Op != OpPut && e.Op != OpDelete {
		return Entry{}, 0, fmt.Errorf("%w: unknown op %d", ErrCorrupt, e.Op)
	}
	return e, HeaderSize + int(n), nil
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
type Log struct {
	mu      sync.Mutex
	f       *os.File
	sync    SyncPolicy
	offsets []int64 // offsets[i-1] = file offset where entry i starts
	size    int64
	closed  bool
}

// Open opens or creates the log at path and recovers it: it keeps the longest
// prefix of valid records whose indexes are contiguous from 1, truncates the
// file after it (and fsyncs the truncation), and returns those entries.
func Open(path string, policy SyncPolicy) (*Log, Recovery, error) {
	var rec Recovery
	_, statErr := os.Stat(path)
	created := errors.Is(statErr, os.ErrNotExist)
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return nil, rec, err
	}
	if created {
		if err := syncDir(filepath.Dir(path)); err != nil {
			f.Close()
			return nil, rec, err
		}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		f.Close()
		return nil, rec, err
	}
	l := &Log{f: f, sync: policy}
	var off int64
	for off < int64(len(data)) {
		e, n, err := Decode(data[off:])
		if err == nil && e.Index != uint64(len(rec.Entries))+1 {
			err = fmt.Errorf("%w: found index %d after %d", ErrGap, e.Index, len(rec.Entries))
		}
		if err != nil {
			rec.Reason = err
			break
		}
		rec.Entries = append(rec.Entries, e)
		l.offsets = append(l.offsets, off)
		off += int64(n)
	}
	if off < int64(len(data)) {
		rec.TruncatedBytes = int64(len(data)) - off
		if err := f.Truncate(off); err != nil {
			f.Close()
			return nil, rec, err
		}
		if err := f.Sync(); err != nil {
			f.Close()
			return nil, rec, err
		}
	}
	l.size = off
	if _, err := f.Seek(off, 0); err != nil {
		f.Close()
		return nil, rec, err
	}
	return l, rec, nil
}

// LastIndex is the index of the last entry (0 when empty).
func (l *Log) LastIndex() uint64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	return uint64(len(l.offsets))
}

// Append writes entries as one write and, under SyncAlways, one fsync (a tiny
// group commit). The first entry's index must be LastIndex()+1 and the rest
// must follow it; otherwise nothing is written and the error wraps ErrGap.
func (l *Log) Append(entries ...Entry) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return ErrClosed
	}
	next := uint64(len(l.offsets)) + 1
	var buf bytes.Buffer
	offs := make([]int64, 0, len(entries))
	for i, e := range entries {
		if e.Index != next+uint64(i) {
			return fmt.Errorf("%w: append index %d, expected %d", ErrGap, e.Index, next+uint64(i))
		}
		offs = append(offs, l.size+int64(buf.Len()))
		buf.Write(Encode(e))
	}
	if _, err := l.f.Write(buf.Bytes()); err != nil {
		// Best effort: cut off whatever part of the write landed.
		_ = l.f.Truncate(l.size)
		_, _ = l.f.Seek(l.size, 0)
		return err
	}
	if l.sync == SyncAlways {
		if err := l.f.Sync(); err != nil {
			return err
		}
	}
	l.size += int64(buf.Len())
	l.offsets = append(l.offsets, offs...)
	return nil
}

// TruncateAfter removes every entry with an index above index, durably. It is
// how a follower drops a divergent suffix it got from a deposed leader.
func (l *Log) TruncateAfter(index uint64) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return ErrClosed
	}
	if index >= uint64(len(l.offsets)) {
		return nil
	}
	off := l.offsets[index]
	if err := l.f.Truncate(off); err != nil {
		return err
	}
	if err := l.f.Sync(); err != nil {
		return err
	}
	if _, err := l.f.Seek(off, 0); err != nil {
		return err
	}
	l.offsets = l.offsets[:index]
	l.size = off
	return nil
}

// Size is the file size in bytes.
func (l *Log) Size() int64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.size
}

// Close flushes and closes the file. Further calls return ErrClosed.
func (l *Log) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return ErrClosed
	}
	l.closed = true
	err := l.f.Sync()
	if cerr := l.f.Close(); err == nil {
		err = cerr
	}
	return err
}

// syncDir fsyncs a directory so a newly created or renamed file's directory
// entry is durable too (A2 Unit 2 §4: the file can survive while its name
// does not).
func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}
