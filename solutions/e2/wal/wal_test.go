package wal

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func tEntry(epoch, index uint64, key, val string) Entry {
	e := Entry{Epoch: epoch, Index: index, Op: OpPut, Key: key, Time: int64(index) * 1000}
	if val != "" {
		e.Value = []byte(val)
	}
	return e
}

func tEqual(a, b Entry) bool {
	return a.Epoch == b.Epoch && a.Index == b.Index && a.Op == b.Op && a.Key == b.Key &&
		bytes.Equal(a.Value, b.Value) && a.Time == b.Time
}

// tOpen opens path and fails the test (instead of nil-dereferencing) if Open
// is not implemented yet.
func tOpen(t *testing.T, path string) (*Log, Recovery) {
	t.Helper()
	l, rec, err := Open(path, SyncAlways)
	if err != nil {
		t.Fatalf("Open(%s): %v", path, err)
	}
	if l == nil {
		t.Fatalf("Open returned a nil *Log without an error")
	}
	return l, rec
}

func tAppend(t *testing.T, l *Log, from, to uint64, epoch uint64) {
	t.Helper()
	for i := from; i <= to; i++ {
		if err := l.Append(tEntry(epoch, i, fmt.Sprintf("k%d", i), fmt.Sprintf("v%d", i))); err != nil {
			t.Fatalf("Append(index %d): %v", i, err)
		}
	}
}

func TestM1_EncodeDecodeRoundTrip(t *testing.T) {
	cases := []Entry{
		tEntry(1, 1, "a", "1"),
		tEntry(7, 42, "key with spaces", "value\nwith\x00bytes"),
		{Epoch: 3, Index: 9, Op: OpDelete, Key: "gone", Time: -5},
		tEntry(1, 2, "big", string(bytes.Repeat([]byte("x"), 100_000))),
	}
	for _, want := range cases {
		b := Encode(want)
		if len(b) < HeaderSize {
			t.Fatalf("Encode(%+v) returned %d bytes; a record is at least a %d-byte header", want.Key, len(b), HeaderSize)
		}
		got, n, err := Decode(append(b, 0xFF, 0xFF)) // trailing bytes belong to the next record
		if err != nil {
			t.Fatalf("Decode(Encode(%q)): %v", want.Key, err)
		}
		if n != len(b) {
			t.Errorf("Decode consumed %d bytes, record is %d; the next record would be misread", n, len(b))
		}
		if !tEqual(got, want) {
			t.Errorf("round trip changed the entry:\n got %+v\nwant %+v", got, want)
		}
	}
}

func TestM1_DecodeDistinguishesTornFromCorrupt(t *testing.T) {
	b := Encode(tEntry(1, 1, "k", "some value"))
	if len(b) <= HeaderSize {
		t.Fatalf("Encode returned %d bytes; implement Encode first", len(b))
	}
	for cut := 0; cut < len(b); cut++ {
		if _, _, err := Decode(b[:cut]); !errors.Is(err, ErrTorn) {
			t.Fatalf("Decode of the first %d of %d bytes: got %v, want ErrTorn (a write that never finished)", cut, len(b), err)
		}
	}
	flipped := append([]byte(nil), b...)
	flipped[len(flipped)-1] ^= 0x01
	if _, _, err := Decode(flipped); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("Decode with one flipped payload bit: got %v, want ErrCorrupt (the checksum exists for this)", err)
	}
	huge := append([]byte(nil), b...)
	huge[0], huge[1] = 0x7F, 0xFF // length ≈ 2 GB
	if _, _, err := Decode(huge); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("Decode with a garbage 2 GB length: got %v, want ErrCorrupt (never trust a length you have not checked)", err)
	}
}

func TestM1_AppendThenReopenRecoversEverything(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wal.log")
	l, rec := tOpen(t, path)
	if len(rec.Entries) != 0 || rec.TruncatedBytes != 0 {
		t.Fatalf("a new log recovered %d entries and cut %d bytes", len(rec.Entries), rec.TruncatedBytes)
	}
	tAppend(t, l, 1, 50, 1)
	if got := l.LastIndex(); got != 50 {
		t.Fatalf("LastIndex = %d after 50 appends", got)
	}
	if err := l.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	l2, rec := tOpen(t, path)
	defer l2.Close()
	if len(rec.Entries) != 50 || rec.TruncatedBytes != 0 || rec.Reason != nil {
		t.Fatalf("reopen recovered %d entries, cut %d bytes (%v); want 50, 0, nil", len(rec.Entries), rec.TruncatedBytes, rec.Reason)
	}
	for i, e := range rec.Entries {
		if !tEqual(e, tEntry(1, uint64(i+1), fmt.Sprintf("k%d", i+1), fmt.Sprintf("v%d", i+1))) {
			t.Fatalf("entry %d recovered as %+v", i+1, e)
		}
	}
	tAppend(t, l2, 51, 52, 2)
	if l2.LastIndex() != 52 {
		t.Fatalf("appends after reopen must continue at 51; LastIndex = %d", l2.LastIndex())
	}
}

func TestM1_TornTailIsCutOffAndTheLogStaysUsable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wal.log")
	l, _ := tOpen(t, path)
	tAppend(t, l, 1, 10, 1)
	good := l.Size()
	if good <= 0 {
		t.Fatalf("Size = %d after 10 appends", good)
	}
	l.Close()

	// A crash in the middle of writing record 11: only part of it reached the disk.
	partial := Encode(tEntry(1, 11, "k11", "v11"))
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatal(err)
	}
	f.Write(partial[:len(partial)/2])
	f.Close()

	l2, rec := tOpen(t, path)
	if len(rec.Entries) != 10 {
		t.Fatalf("recovered %d entries; the 10 complete records must all survive a torn 11th", len(rec.Entries))
	}
	if !errors.Is(rec.Reason, ErrTorn) || rec.TruncatedBytes != int64(len(partial)/2) {
		t.Fatalf("Recovery = {cut %d bytes, %v}; want {cut %d, ErrTorn}", rec.TruncatedBytes, rec.Reason, len(partial)/2)
	}
	if fi, _ := os.Stat(path); fi.Size() != good {
		t.Fatalf("file is %d bytes after recovery, want %d: the torn bytes must be truncated, or the next append lands after garbage", fi.Size(), good)
	}
	tAppend(t, l2, 11, 12, 1)
	l2.Close()
	l3, rec := tOpen(t, path)
	defer l3.Close()
	if len(rec.Entries) != 12 || rec.TruncatedBytes != 0 {
		t.Fatalf("after re-appending: recovered %d entries, cut %d bytes; want 12, 0", len(rec.Entries), rec.TruncatedBytes)
	}
}

func TestM1_CorruptRecordCutsTheLogThere(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wal.log")
	l, _ := tOpen(t, path)
	tAppend(t, l, 1, 3, 1)
	off3 := l.Size()
	tAppend(t, l, 4, 6, 1)
	l.Close()

	b, _ := os.ReadFile(path)
	b[off3+HeaderSize+3] ^= 0xFF // flip a byte inside record 4's payload
	os.WriteFile(path, b, 0o644)

	l2, rec := tOpen(t, path)
	defer l2.Close()
	if len(rec.Entries) != 3 || !errors.Is(rec.Reason, ErrCorrupt) {
		t.Fatalf("recovered %d entries (%v); want 3 and ErrCorrupt: nothing after a bad record can be trusted to be in order", len(rec.Entries), rec.Reason)
	}
	if l2.LastIndex() != 3 {
		t.Fatalf("LastIndex = %d, want 3", l2.LastIndex())
	}
}

func TestM1_AppendRejectsGapsAndDuplicates(t *testing.T) {
	l, _ := tOpen(t, filepath.Join(t.TempDir(), "wal.log"))
	defer l.Close()
	tAppend(t, l, 1, 2, 1)
	if err := l.Append(tEntry(1, 4, "k", "v")); !errors.Is(err, ErrGap) {
		t.Errorf("Append(index 4) after 2: got %v, want ErrGap (a hole means a lost write)", err)
	}
	if err := l.Append(tEntry(1, 2, "k", "v")); !errors.Is(err, ErrGap) {
		t.Errorf("Append(index 2) again: got %v, want ErrGap (a duplicate index means two writes claim one slot)", err)
	}
	if err := l.Append(tEntry(1, 3, "a", "1"), tEntry(1, 5, "b", "2")); !errors.Is(err, ErrGap) {
		t.Errorf("batch 3,5: got %v, want ErrGap", err)
	}
	if l.LastIndex() != 2 {
		t.Fatalf("a rejected batch must write nothing; LastIndex = %d, want 2", l.LastIndex())
	}
	if err := l.Append(tEntry(1, 3, "a", "1"), tEntry(1, 4, "b", "2")); err != nil {
		t.Fatalf("batch 3,4: %v", err)
	}
}

func TestM1_TruncateAfterDropsTheSuffixDurably(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wal.log")
	l, _ := tOpen(t, path)
	tAppend(t, l, 1, 8, 1)
	if err := l.TruncateAfter(5); err != nil {
		t.Fatalf("TruncateAfter(5): %v", err)
	}
	if l.LastIndex() != 5 {
		t.Fatalf("LastIndex = %d after TruncateAfter(5)", l.LastIndex())
	}
	if err := l.TruncateAfter(9); err != nil || l.LastIndex() != 5 {
		t.Fatalf("TruncateAfter past the end must be a no-op: err %v, LastIndex %d", err, l.LastIndex())
	}
	// The new leader's entries for 6 and 7 replace the divergent ones.
	tAppend(t, l, 6, 7, 2)
	l.Close()
	l2, rec := tOpen(t, path)
	defer l2.Close()
	if len(rec.Entries) != 7 || rec.TruncatedBytes != 0 {
		t.Fatalf("reopen: %d entries, %d bytes cut; want 7, 0", len(rec.Entries), rec.TruncatedBytes)
	}
	if rec.Entries[5].Epoch != 2 || rec.Entries[4].Epoch != 1 {
		t.Fatalf("entries 5 and 6 have epochs %d and %d; want 1 and 2", rec.Entries[4].Epoch, rec.Entries[5].Epoch)
	}
}

func TestM1_ConcurrentAppendersNeverInterleaveRecords(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wal.log")
	l, _ := tOpen(t, path)
	// Callers serialise on an index counter the way the node does (under its
	// own lock), but Append itself must also be safe to call concurrently
	// with LastIndex and Size.
	var mu sync.Mutex
	next := uint64(1)
	var wg sync.WaitGroup
	for w := 0; w < 8; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < 25; i++ {
				mu.Lock()
				e := tEntry(1, next, fmt.Sprintf("w%d-%d", w, i), "v")
				err := l.Append(e)
				if err == nil {
					next++
				}
				mu.Unlock()
				if err != nil {
					t.Errorf("Append: %v", err)
					return
				}
				_ = l.LastIndex()
				_ = l.Size()
			}
		}(w)
	}
	wg.Wait()
	l.Close()
	l2, rec := tOpen(t, path)
	defer l2.Close()
	if len(rec.Entries) != 200 || rec.Reason != nil {
		t.Fatalf("recovered %d entries (%v), want 200", len(rec.Entries), rec.Reason)
	}
	if err := l.Append(tEntry(1, 201, "k", "v")); !errors.Is(err, ErrClosed) {
		t.Fatalf("Append after Close: got %v, want ErrClosed", err)
	}
}
