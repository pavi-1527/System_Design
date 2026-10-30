// Package wal is a write-ahead log: an append-only file of checksummed
// records, with crash recovery that truncates a torn tail, and group commit
// so concurrent committers share fsyncs. It is the STRETCH lab for A2 Unit 2
// §4. Nothing here touches a real disk: the log writes to a File, and the
// provided SimFile models the one property that matters, that bytes are only
// durable after Sync, and a crash keeps the synced bytes plus an arbitrary
// prefix of the unsynced ones.
//
// Record format (little endian), pinned by the tests so other tools could
// read the file:
//
//	| crc32c (4 bytes) | length (4 bytes) | payload (length bytes) |
//
// The CRC-32C (Castagnoli) covers the length field and the payload. The i-th
// record in the file has LSN i (log sequence number), starting at 1.
//
// Recovery rules (Scan, used by Open):
//
//   - A record whose header or payload runs past the end of the data is a
//     torn tail: the process crashed in the middle of writing it. It, and
//     nothing before it, is dropped.
//   - A record that is complete but whose checksum fails is a torn tail if
//     it is the LAST record in the data (the final write was only partly
//     persisted) and is corruption otherwise: later records exist, so the
//     damage cannot be an interrupted append. Corruption is an error
//     (*CorruptError) and Open must not modify the file.
//   - MaxPayload bounds a single record.
//
// Durability contract: Append only writes; Sync makes everything appended so
// far durable; Commit appends and returns only once the record is durable.
// Concurrent Commits are batched: while one goroutine is inside Sync, others
// append behind it and the next Sync covers all of them (group commit). If
// Sync ever fails the log is poisoned: every later call returns ErrLogFailed.
// After a failed fsync the kernel may have dropped the dirty pages, so a
// retry that "succeeds" proves nothing.
//
// Suggested order: EncodeRecord, Scan (the byte-level tests), Open, Append
// and Sync, Commit with group commit. Run: go test -race ./labs/a2/wal/
package wal

import (
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"sync"
	"time"
)

const (
	// HeaderSize is the size of the crc and length fields.
	HeaderSize = 8
	// MaxPayload is the largest payload one record may carry.
	MaxPayload = 1 << 20
)

var crcTable = crc32.MakeTable(crc32.Castagnoli)

var (
	// ErrCorrupt matches the error returned for mid-log corruption.
	ErrCorrupt = errors.New("wal: corrupt record in the middle of the log")
	// ErrLogFailed: a previous Sync failed; the log refuses further use.
	ErrLogFailed = errors.New("wal: log is failed after a sync error")
	// ErrTooLarge: payload exceeds MaxPayload.
	ErrTooLarge = errors.New("wal: payload too large")
)

// CorruptError reports where mid-log corruption was found.
type CorruptError struct{ Offset int }

func (e *CorruptError) Error() string {
	return fmt.Sprintf("wal: corrupt record at offset %d with valid data after it", e.Offset)
}

// Is makes errors.Is(err, ErrCorrupt) true.
func (e *CorruptError) Is(target error) bool { return target == ErrCorrupt }

// Record is one log entry.
type Record struct {
	LSN     uint64
	Payload []byte
}

// ---------- the file abstraction and the crash simulator ----------

// File is the part of *os.File the log uses. Write appends; the data is not
// durable until Sync returns.
type File interface {
	Write(p []byte) (int, error)
	Sync() error
	ReadAll() ([]byte, error)
	Truncate(size int64) error
	Size() int64
}

// SimFile is an in-memory File with a volatile layer. Provided.
//
// Write goes to the volatile (page cache) layer. Sync moves what was
// volatile when it started into the durable layer. Crash simulates power
// loss: the durable bytes survive, plus the first `keep` volatile bytes
// (a torn write can persist any prefix, even half a record). Truncate takes
// effect on both layers immediately and durably (real systems need an fsync;
// the simulation keeps the lab about the log).
type SimFile struct {
	mu        sync.Mutex
	durable   []byte
	pending   []byte
	syncs     int
	syncDelay time.Duration
	failNext  error
}

// NewSimFile returns an empty SimFile.
func NewSimFile() *SimFile { return &SimFile{} }

// SetSyncDelay makes every Sync take d, like a device that needs ~1 ms to
// flush. Writes proceed while a Sync is in progress.
func (f *SimFile) SetSyncDelay(d time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.syncDelay = d
}

// FailNextSync makes the next Sync return err without persisting anything.
func (f *SimFile) FailNextSync(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failNext = err
}

// Write appends p to the volatile layer.
func (f *SimFile) Write(p []byte) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pending = append(f.pending, p...)
	return len(p), nil
}

// Sync persists the bytes that were volatile when it was called.
func (f *SimFile) Sync() error {
	f.mu.Lock()
	if f.failNext != nil {
		err := f.failNext
		f.failNext = nil
		f.mu.Unlock()
		return err
	}
	n, delay := len(f.pending), f.syncDelay
	f.mu.Unlock()
	if delay > 0 {
		time.Sleep(delay)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if n > len(f.pending) { // truncated meanwhile
		n = len(f.pending)
	}
	f.durable = append(f.durable, f.pending[:n]...)
	f.pending = append([]byte(nil), f.pending[n:]...)
	f.syncs++
	return nil
}

// ReadAll returns a copy of the current contents (durable then volatile).
func (f *SimFile) ReadAll() ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]byte, 0, len(f.durable)+len(f.pending))
	out = append(out, f.durable...)
	return append(out, f.pending...), nil
}

// Size returns the current length.
func (f *SimFile) Size() int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return int64(len(f.durable) + len(f.pending))
}

// Truncate shortens the file to size bytes (no-op if already shorter).
func (f *SimFile) Truncate(size int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if size < 0 {
		return errors.New("wal: negative size")
	}
	if int(size) >= len(f.durable)+len(f.pending) {
		return nil
	}
	if int(size) <= len(f.durable) {
		f.durable = f.durable[:size]
		f.pending = nil
		return nil
	}
	f.pending = f.pending[:int(size)-len(f.durable)]
	return nil
}

// Crash simulates power loss: the volatile layer is lost except for its
// first keep bytes, which are persisted (keep <= 0: none; keep >=
// PendingLen: all).
func (f *SimFile) Crash(keep int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if keep > len(f.pending) {
		keep = len(f.pending)
	}
	if keep > 0 {
		f.durable = append(f.durable, f.pending[:keep]...)
	}
	f.pending = nil
}

// Corrupt XORs mask into the byte at offset (bit rot, a misdirected write).
func (f *SimFile) Corrupt(offset int, mask byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if offset < len(f.durable) {
		f.durable[offset] ^= mask
	} else if offset-len(f.durable) < len(f.pending) {
		f.pending[offset-len(f.durable)] ^= mask
	}
}

// PendingLen is the number of volatile (unsynced) bytes.
func (f *SimFile) PendingLen() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.pending)
}

// DurableLen is the number of bytes that would survive Crash(0).
func (f *SimFile) DurableLen() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.durable)
}

// Syncs is the number of successful Sync calls.
func (f *SimFile) Syncs() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.syncs
}

// ---------- the format ----------

// EncodeRecord returns the on-disk bytes for payload: crc32c, length,
// payload, as described in the package comment.
func EncodeRecord(payload []byte) []byte {
	buf := make([]byte, HeaderSize+len(payload))
	binary.LittleEndian.PutUint32(buf[4:8], uint32(len(payload)))
	copy(buf[HeaderSize:], payload)
	binary.LittleEndian.PutUint32(buf[0:4], crc32.Checksum(buf[4:], crcTable))
	return buf
}

// Scan parses data as a sequence of records and returns the valid ones in
// order (LSNs from 1), plus validLen, the number of leading bytes that
// consist of whole valid records. A torn tail (see the package comment) is
// not an error: validLen simply stops before it. Mid-log corruption returns
// the records before it and a *CorruptError. Payloads are copies.
func Scan(data []byte) (records []Record, validLen int, err error) {
	off := 0
	for off < len(data) {
		if len(data)-off < HeaderSize {
			return records, off, nil // torn header
		}
		length := int(binary.LittleEndian.Uint32(data[off+4 : off+8]))
		end := off + HeaderSize + length
		if length > MaxPayload || end > len(data) {
			// A length that is absurd or runs past EOF can only be an
			// interrupted append (or damage we cannot tell apart from one).
			return records, off, nil
		}
		want := binary.LittleEndian.Uint32(data[off : off+4])
		if crc32.Checksum(data[off+4:end], crcTable) != want {
			if end == len(data) {
				return records, off, nil // damaged last record: torn tail
			}
			return records, off, &CorruptError{Offset: off}
		}
		records = append(records, Record{
			LSN:     uint64(len(records) + 1),
			Payload: append([]byte(nil), data[off+HeaderSize:end]...),
		})
		off = end
	}
	return records, off, nil
}

// ---------- the log ----------

// Log is an append-only log on a File. Safe for concurrent use.
type Log struct {
	mu   sync.Mutex
	cond *sync.Cond
	f    File

	lastLSN   uint64 // LSN of the last appended record
	syncedLSN uint64 // every record up to and including this LSN is durable
	syncing   bool   // some goroutine is inside f.Sync
	err       error  // sticky failure
	syncs     int    // successful Syncs issued by this Log
}

// Open reads f, recovers its records with Scan, truncates a torn tail (and
// syncs the truncation) and returns a log positioned to append after the last
// valid record. On mid-log corruption it returns the *CorruptError (and the
// records before it) and leaves the file untouched.
func Open(f File) (*Log, []Record, error) {
	data, err := f.ReadAll()
	if err != nil {
		return nil, nil, err
	}
	recs, valid, err := Scan(data)
	if err != nil {
		return nil, recs, err
	}
	// breakSkipTruncate is the Break-it switch: see breakit_on.go.
	if valid < len(data) && !breakSkipTruncate {
		if err := f.Truncate(int64(valid)); err != nil {
			return nil, recs, err
		}
		if err := f.Sync(); err != nil {
			return nil, recs, err
		}
	}
	l := &Log{f: f, lastLSN: uint64(len(recs)), syncedLSN: uint64(len(recs))}
	l.cond = sync.NewCond(&l.mu)
	return l, recs, nil
}

// appendLocked writes one record and assigns its LSN. Caller holds l.mu.
func (l *Log) appendLocked(payload []byte) (uint64, error) {
	if l.err != nil {
		return 0, l.err
	}
	if len(payload) > MaxPayload {
		return 0, ErrTooLarge
	}
	if _, err := l.f.Write(EncodeRecord(payload)); err != nil {
		l.err = fmt.Errorf("%w: write: %v", ErrLogFailed, err)
		l.cond.Broadcast()
		return 0, l.err
	}
	l.lastLSN++
	return l.lastLSN, nil
}

// Append writes payload as the next record and returns its LSN. The record is
// NOT durable until Sync (or a Commit that covers it) returns.
func (l *Log) Append(payload []byte) (uint64, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.appendLocked(payload)
}

// Sync makes every record appended so far durable. It shares the work with
// concurrent Commit and Sync callers (see waitDurable).
func (l *Log) Sync() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.err != nil {
		return l.err
	}
	return l.waitDurable(l.lastLSN)
}

// Commit appends payload and returns its LSN once it is durable. Concurrent
// Commits are batched into shared Syncs.
func (l *Log) Commit(payload []byte) (uint64, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	lsn, err := l.appendLocked(payload)
	if err != nil {
		return 0, err
	}
	if err := l.waitDurable(lsn); err != nil {
		return 0, err
	}
	return lsn, nil
}

// waitDurable blocks until syncedLSN >= lsn or the log fails. If nobody is
// syncing it becomes the leader: it releases the lock, calls f.Sync once for
// everything appended so far, then wakes the waiters. Otherwise it waits on
// the leader. Caller holds l.mu.
func (l *Log) waitDurable(lsn uint64) error {
	for l.syncedLSN < lsn {
		if l.err != nil {
			return l.err
		}
		if l.syncing {
			l.cond.Wait()
			continue
		}
		l.syncing = true
		target := l.lastLSN
		l.mu.Unlock()
		err := l.f.Sync()
		l.mu.Lock()
		l.syncing = false
		if err != nil {
			l.err = fmt.Errorf("%w: sync: %v", ErrLogFailed, err)
		} else {
			l.syncedLSN = target
			l.syncs++
		}
		l.cond.Broadcast()
	}
	return nil
}

// DurableLSN is the highest LSN known to be durable.
func (l *Log) DurableLSN() uint64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.syncedLSN
}

// LastLSN is the LSN of the last appended record (durable or not).
func (l *Log) LastLSN() uint64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.lastLSN
}

// SyncCount is the number of successful Syncs this Log issued.
func (l *Log) SyncCount() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.syncs
}
