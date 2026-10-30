package wal

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"sync"
	"testing"
	"time"
)

// ---------- helpers (prefixed with t to avoid clashing with your code) ----------

func tPayloads(n int) [][]byte {
	out := make([][]byte, n)
	for i := range out {
		out[i] = []byte(fmt.Sprintf("record-%03d-%s", i, bytes.Repeat([]byte{'x'}, i%7)))
	}
	return out
}

// tEncode is the record format written out independently of the code under
// test, so that Scan and recovery are tested against the documented bytes.
func tEncode(payload []byte) []byte {
	buf := make([]byte, HeaderSize+len(payload))
	binary.LittleEndian.PutUint32(buf[4:8], uint32(len(payload)))
	copy(buf[HeaderSize:], payload)
	binary.LittleEndian.PutUint32(buf[0:4], crc32.Checksum(buf[4:], crc32.MakeTable(crc32.Castagnoli)))
	return buf
}

func tImage(ps [][]byte) []byte {
	var img []byte
	for _, p := range ps {
		img = append(img, tEncode(p)...)
	}
	return img
}

// tEnds returns the end offset of each record in the image.
func tEnds(ps [][]byte) []int {
	var ends []int
	off := 0
	for _, p := range ps {
		off += HeaderSize + len(p)
		ends = append(ends, off)
	}
	return ends
}

func tOpen(t *testing.T, f File) (*Log, []Record) {
	t.Helper()
	l, recs, err := Open(f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return l, recs
}

func tSamePayloads(t *testing.T, recs []Record, want [][]byte) {
	t.Helper()
	if len(recs) != len(want) {
		t.Fatalf("got %d records, want %d", len(recs), len(want))
	}
	for i, r := range recs {
		if r.LSN != uint64(i+1) {
			t.Fatalf("record %d has LSN %d, want %d", i, r.LSN, i+1)
		}
		if !bytes.Equal(r.Payload, want[i]) {
			t.Fatalf("record %d payload = %q, want %q", i, r.Payload, want[i])
		}
	}
}

func tWait(t *testing.T, wg *sync.WaitGroup, d time.Duration) {
	t.Helper()
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(d):
		t.Fatal("timed out: committers are stuck (lost wake-up or deadlock?)")
	}
}

// ---------- format ----------

func TestEncodeRecord_LayoutIsCRCThenLengthThenPayload(t *testing.T) {
	got := EncodeRecord([]byte("hello"))
	if len(got) != HeaderSize+5 {
		t.Fatalf("len = %d, want %d", len(got), HeaderSize+5)
	}
	if n := binary.LittleEndian.Uint32(got[4:8]); n != 5 {
		t.Errorf("length field = %d, want 5 (bytes 4..8, little endian)", n)
	}
	if string(got[8:]) != "hello" {
		t.Errorf("payload bytes = %q", got[8:])
	}
	want := crc32.Checksum(got[4:], crc32.MakeTable(crc32.Castagnoli))
	if c := binary.LittleEndian.Uint32(got[0:4]); c != want {
		t.Errorf("crc field = %#x, want CRC-32C over length+payload = %#x", c, want)
	}
	if len(EncodeRecord(nil)) != HeaderSize {
		t.Error("an empty payload must encode to just the header")
	}
	for _, p := range tPayloads(10) {
		if !bytes.Equal(EncodeRecord(p), tEncode(p)) {
			t.Fatalf("EncodeRecord(%q) differs from the documented layout", p)
		}
	}
}

func TestScan_RoundTripsRecordsWithSequentialLSNs(t *testing.T) {
	ps := tPayloads(20)
	ps = append(ps, nil, []byte{}, bytes.Repeat([]byte("big"), 5000)) // empty and large payloads are legal
	recs, valid, err := Scan(tImage(ps))
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if valid != len(tImage(ps)) {
		t.Errorf("validLen = %d, want the whole image (%d)", valid, len(tImage(ps)))
	}
	for i := range ps {
		if ps[i] == nil {
			ps[i] = []byte{}
		}
	}
	tSamePayloads(t, recs, ps)
}

func TestScan_ATornTailAtEveryCutPointYieldsExactlyTheCompleteRecords(t *testing.T) {
	ps := tPayloads(6)
	img, ends := tImage(ps), tEnds(ps)
	for cut := 0; cut <= len(img); cut++ {
		complete := 0
		for _, e := range ends {
			if e <= cut {
				complete++
			}
		}
		recs, valid, err := Scan(img[:cut])
		if err != nil {
			t.Fatalf("cut at %d: unexpected error %v; a half-written last record is normal after a crash, not corruption", cut, err)
		}
		wantValid := 0
		if complete > 0 {
			wantValid = ends[complete-1]
		}
		if valid != wantValid {
			t.Fatalf("cut at %d: validLen = %d, want %d", cut, valid, wantValid)
		}
		tSamePayloads(t, recs, ps[:complete])
	}
}

func TestScan_ADamagedLastRecordIsATornTail(t *testing.T) {
	ps := tPayloads(4)
	img, ends := tImage(ps), tEnds(ps)
	for _, off := range []int{ends[2] + 1, ends[2] + 5, ends[2] + HeaderSize + 1, len(img) - 1} { // crc, length, payload bytes of record 4
		bad := append([]byte(nil), img...)
		bad[off] ^= 0x40
		recs, valid, err := Scan(bad)
		if err != nil {
			t.Fatalf("flip at %d inside the last record: %v; the last record failing its checksum is what an interrupted append looks like", off, err)
		}
		if valid != ends[2] {
			t.Fatalf("flip at %d: validLen = %d, want %d (drop only the last record)", off, valid, ends[2])
		}
		tSamePayloads(t, recs, ps[:3])
	}
}

func TestScan_ADamagedMiddleRecordIsCorruptionNotATornTail(t *testing.T) {
	ps := tPayloads(5)
	img, ends := tImage(ps), tEnds(ps)
	bad := append([]byte(nil), img...)
	bad[ends[1]+HeaderSize+2] ^= 0x01 // payload of record 3, with two valid records after it
	recs, _, err := Scan(bad)
	if !errors.Is(err, ErrCorrupt) {
		t.Fatalf("err = %v, want ErrCorrupt: records 4 and 5 are intact, so silently truncating here would discard committed data", err)
	}
	var ce *CorruptError
	if !errors.As(err, &ce) || ce.Offset != ends[1] {
		t.Fatalf("err = %#v, want *CorruptError at offset %d", err, ends[1])
	}
	tSamePayloads(t, recs, ps[:2])
}

// ---------- recovery ----------

func TestOpen_EmptyFileStartsAtLSN1AndContinuesNumberingAfterReopen(t *testing.T) {
	f := NewSimFile()
	l, recs := tOpen(t, f)
	if len(recs) != 0 {
		t.Fatalf("recovered %d records from an empty file", len(recs))
	}
	ps := tPayloads(5)
	for i, p := range ps {
		lsn, err := l.Append(p)
		if err != nil || lsn != uint64(i+1) {
			t.Fatalf("Append #%d = %d, %v; want LSN %d", i, lsn, err, i+1)
		}
	}
	if err := l.Sync(); err != nil {
		t.Fatal(err)
	}
	if l.DurableLSN() != 5 || l.LastLSN() != 5 {
		t.Errorf("durable/last LSN = %d/%d, want 5/5", l.DurableLSN(), l.LastLSN())
	}
	l2, recs2 := tOpen(t, f)
	tSamePayloads(t, recs2, ps)
	if lsn, _ := l2.Append([]byte("next")); lsn != 6 {
		t.Errorf("first Append after recovery has LSN %d, want 6", lsn)
	}
}

func TestAppend_IsNotDurableUntilSynced(t *testing.T) {
	f := NewSimFile()
	l, _ := tOpen(t, f)
	_, _ = l.Append([]byte("a"))
	_, _ = l.Append([]byte("b"))
	if l.DurableLSN() != 0 {
		t.Errorf("DurableLSN = %d before any Sync, want 0", l.DurableLSN())
	}
	f.Crash(0)
	_, recs := tOpen(t, f)
	if len(recs) != 0 {
		t.Fatalf("recovered %d records that were never synced; they were only in the page cache", len(recs))
	}
}

func TestCrash_SyncedRecordsAlwaysSurviveAndUnsyncedOnesMaySurviveAsAPrefix(t *testing.T) {
	ps := tPayloads(8)
	probe := NewSimFile()
	l0, _ := tOpen(t, probe)
	for _, p := range ps[5:] {
		_, _ = l0.Append(p)
	}
	pending := probe.PendingLen() // bytes of the three unsynced records

	for keep := 0; keep <= pending; keep++ {
		f := NewSimFile()
		l, _ := tOpen(t, f)
		for _, p := range ps[:5] {
			_, _ = l.Append(p)
		}
		if err := l.Sync(); err != nil {
			t.Fatal(err)
		}
		for _, p := range ps[5:] {
			_, _ = l.Append(p)
		}
		f.Crash(keep)

		l2, recs, err := Open(f)
		if err != nil {
			t.Fatalf("keep=%d: Open after crash: %v (a torn tail must never be an error)", keep, err)
		}
		if len(recs) < 5 {
			t.Fatalf("keep=%d: recovered %d records, but 5 were synced before the crash", keep, len(recs))
		}
		tSamePayloads(t, recs, ps[:len(recs)]) // a prefix, never reordered or garbled

		// The torn tail must be gone so new records land on a record boundary.
		if got, want := int(f.Size()), len(tImage(ps[:len(recs)])); got != want {
			t.Fatalf("keep=%d: file is %d bytes after recovery, want %d (torn bytes must be truncated)", keep, got, want)
		}
		fresh := []byte("after-recovery")
		lsn, err := l2.Commit(fresh)
		if err != nil || lsn != uint64(len(recs)+1) {
			t.Fatalf("keep=%d: Commit after recovery = %d, %v", keep, lsn, err)
		}
		f.Crash(0)
		_, again := tOpen(t, f)
		tSamePayloads(t, again, append(append([][]byte{}, ps[:len(recs)]...), fresh))
	}
}

func TestOpen_RefusesMidLogCorruptionAndLeavesTheFileAlone(t *testing.T) {
	ps := tPayloads(5)
	f := NewSimFile()
	img := tImage(ps)
	_, _ = f.Write(img)
	_ = f.Sync()
	f.Corrupt(tEnds(ps)[0]+HeaderSize, 0xff)
	before, _ := f.ReadAll()
	l, recs, err := Open(f)
	if !errors.Is(err, ErrCorrupt) || l != nil {
		t.Fatalf("Open = %v, %v; want ErrCorrupt and no log", l, err)
	}
	tSamePayloads(t, recs, ps[:1])
	after, _ := f.ReadAll()
	if !bytes.Equal(before, after) {
		t.Fatal("Open modified a file it refused to open; operators need the evidence intact")
	}
}

func TestAppend_RejectsOversizedPayloadsWithoutWritingAnything(t *testing.T) {
	f := NewSimFile()
	l, _ := tOpen(t, f)
	if _, err := l.Append(make([]byte, MaxPayload+1)); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("err = %v, want ErrTooLarge", err)
	}
	if f.Size() != 0 || l.LastLSN() != 0 {
		t.Fatalf("a rejected append left %d bytes and LSN %d behind", f.Size(), l.LastLSN())
	}
	if _, err := l.Append(make([]byte, MaxPayload)); err != nil {
		t.Fatalf("a payload of exactly MaxPayload must be accepted: %v", err)
	}
}

// ---------- commit and group commit ----------

func TestCommit_IsDurableWhenItReturns(t *testing.T) {
	f := NewSimFile()
	l, _ := tOpen(t, f)
	ps := tPayloads(6)
	for i, p := range ps {
		lsn, err := l.Commit(p)
		if err != nil || lsn != uint64(i+1) {
			t.Fatalf("Commit #%d = %d, %v", i, lsn, err)
		}
		if l.DurableLSN() < lsn {
			t.Fatalf("Commit returned LSN %d but DurableLSN is %d", lsn, l.DurableLSN())
		}
	}
	f.Crash(0) // lose everything not fsynced
	_, recs := tOpen(t, f)
	tSamePayloads(t, recs, ps)
}

func TestCommit_GroupCommitSharesSyncsAcrossConcurrentCommitters(t *testing.T) {
	f := NewSimFile()
	f.SetSyncDelay(2 * time.Millisecond) // a slow device: committers pile up behind the running sync
	l, _ := tOpen(t, f)
	const workers, each = 16, 20
	var wg sync.WaitGroup
	var mu sync.Mutex
	seen := map[uint64]bool{}
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < each; i++ {
				lsn, err := l.Commit([]byte(fmt.Sprintf("w%d-%d", w, i)))
				if err != nil {
					t.Errorf("Commit: %v", err)
					return
				}
				mu.Lock()
				if seen[lsn] {
					t.Errorf("LSN %d handed out twice", lsn)
				}
				seen[lsn] = true
				mu.Unlock()
				if l.DurableLSN() < lsn {
					t.Errorf("Commit returned before LSN %d was durable", lsn)
				}
			}
		}(w)
	}
	tWait(t, &wg, 30*time.Second)
	total := workers * each
	if got := f.Syncs(); got > total/2 {
		t.Errorf("%d commits used %d syncs; with 16 concurrent committers behind a 2 ms fsync, most should piggy-back (want <= %d)", total, got, total/2)
	}
	if l.SyncCount() != f.Syncs() {
		t.Errorf("Log counted %d syncs, file saw %d", l.SyncCount(), f.Syncs())
	}
	f.Crash(0)
	_, recs := tOpen(t, f)
	if len(recs) != total {
		t.Fatalf("recovered %d records after a crash, want all %d acknowledged commits", len(recs), total)
	}
}

func TestSync_AFailedSyncPoisonsTheLogAndNothingAfterItIsAcknowledged(t *testing.T) {
	f := NewSimFile()
	l, _ := tOpen(t, f)
	if _, err := l.Commit([]byte("ok")); err != nil {
		t.Fatal(err)
	}
	f.FailNextSync(errors.New("EIO"))
	if _, err := l.Commit([]byte("doomed")); !errors.Is(err, ErrLogFailed) {
		t.Fatalf("Commit during a failing fsync = %v, want ErrLogFailed (never acknowledge what may not be on disk)", err)
	}
	if l.DurableLSN() != 1 {
		t.Errorf("DurableLSN = %d, want 1", l.DurableLSN())
	}
	// The next fsync would "succeed", but the kernel may already have dropped
	// the pages of the failed one. The log must refuse to carry on.
	if _, err := l.Commit([]byte("after")); !errors.Is(err, ErrLogFailed) {
		t.Errorf("Commit after a failed sync = %v, want ErrLogFailed", err)
	}
	if _, err := l.Append([]byte("after")); !errors.Is(err, ErrLogFailed) {
		t.Errorf("Append after a failed sync = %v, want ErrLogFailed", err)
	}
	if err := l.Sync(); !errors.Is(err, ErrLogFailed) {
		t.Errorf("Sync after a failed sync = %v, want ErrLogFailed", err)
	}
}

func TestSync_AFailedSyncWakesEveryBlockedCommitter(t *testing.T) {
	f := NewSimFile()
	f.SetSyncDelay(5 * time.Millisecond)
	l, _ := tOpen(t, f)
	f.FailNextSync(errors.New("EIO"))
	var wg sync.WaitGroup
	var mu sync.Mutex
	failed := 0
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if _, err := l.Commit([]byte{byte(i)}); err != nil {
				mu.Lock()
				failed++
				mu.Unlock()
			}
		}(i)
	}
	tWait(t, &wg, 10*time.Second)
	if failed == 0 {
		t.Fatal("no committer saw the sync failure")
	}
}

func TestAppend_ConcurrentAppendersGetUniqueContiguousLSNsAndAParsableFile(t *testing.T) {
	f := NewSimFile()
	l, _ := tOpen(t, f)
	const workers, each = 8, 100
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < each; i++ {
				if _, err := l.Append([]byte(fmt.Sprintf("w%d-%03d", w, i))); err != nil {
					t.Errorf("Append: %v", err)
					return
				}
			}
		}(w)
	}
	tWait(t, &wg, 30*time.Second)
	if err := l.Sync(); err != nil {
		t.Fatal(err)
	}
	data, _ := f.ReadAll()
	recs, valid, err := Scan(data)
	if err != nil || valid != len(data) || len(recs) != workers*each {
		t.Fatalf("Scan: %d records, valid %d of %d bytes, err %v; want %d records covering the whole file (interleaved writes would tear records)", len(recs), valid, len(data), err, workers*each)
	}
	// Each worker's own records must appear in its program order.
	next := map[int]int{}
	for _, r := range recs {
		var w, i int
		if _, err := fmt.Sscanf(string(r.Payload), "w%d-%d", &w, &i); err != nil {
			t.Fatalf("payload %q: %v", r.Payload, err)
		}
		if i != next[w] {
			t.Fatalf("worker %d: record %d appeared where %d was expected", w, i, next[w])
		}
		next[w]++
	}
}
