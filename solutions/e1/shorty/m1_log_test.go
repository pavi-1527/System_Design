package shorty

// Milestone 1, in-process: the append-only log (spec.md "On-disk format",
// Unit 1 §1–§2).

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func tOpen(t *testing.T, path string) (*Log, Recovery) {
	t.Helper()
	l, rec, err := OpenLog(path, SyncAlways)
	if err != nil {
		t.Fatalf("OpenLog(%s): %v", path, err)
	}
	if l == nil {
		t.Fatalf("OpenLog returned a nil *Log without an error")
	}
	t.Cleanup(func() { _ = l.Close() })
	return l, rec
}

func tMustPut(t *testing.T, l *Log, k, v string) {
	t.Helper()
	if err := l.Put(k, []byte(v)); err != nil {
		t.Fatalf("Put(%q): %v", k, err)
	}
}

func TestM1_ParseSyncPolicy(t *testing.T) {
	for s, want := range map[string]SyncPolicy{"always": SyncAlways, "never": SyncNever} {
		got, err := ParseSyncPolicy(s)
		if err != nil || got != want {
			t.Errorf("ParseSyncPolicy(%q) = %v, %v; want %v", s, got, err, want)
		}
	}
	if _, err := ParseSyncPolicy("sometimes"); err == nil {
		t.Error(`ParseSyncPolicy("sometimes") succeeded; an unknown durability setting must be an error, not a default`)
	}
}

func TestM1_LogPutGetDeleteSurviveReopen(t *testing.T) {
	path := tLogPath(t.TempDir())
	l, rec := tOpen(t, path)
	if rec.Records != 0 || rec.TruncatedBytes != 0 || l.Len() != 0 {
		t.Fatalf("a new log should be empty: %+v, Len %d", rec, l.Len())
	}
	for i := 0; i < 100; i++ {
		tMustPut(t, l, fmt.Sprintf("k%03d", i), fmt.Sprintf("v%d", i))
	}
	for i := 0; i < 5; i++ {
		tMustPut(t, l, fmt.Sprintf("k%03d", i), "overwritten")
	}
	for i := 90; i < 100; i++ {
		if err := l.Delete(fmt.Sprintf("k%03d", i)); err != nil {
			t.Fatal(err)
		}
	}
	if err := l.Delete("never-existed"); err != nil {
		t.Fatalf("deleting an absent key: %v", err)
	}
	if v, ok := l.Get("k000"); !ok || string(v) != "overwritten" {
		t.Fatalf("Get(k000) = %q, %v before reopen", v, ok)
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}

	l2, rec2 := tOpen(t, path)
	if rec2.Records != 115 {
		t.Errorf("recovered %d records, want 115 (100 puts + 5 overwrites + 10 deletes; an absent delete writes nothing)", rec2.Records)
	}
	if l2.Len() != 90 {
		t.Errorf("Len after reopen = %d, want 90", l2.Len())
	}
	if v, ok := l2.Get("k000"); !ok || string(v) != "overwritten" {
		t.Errorf("Get(k000) after reopen = %q, %v; the last write must win on replay", v, ok)
	}
	if v, ok := l2.Get("k050"); !ok || string(v) != "v50" {
		t.Errorf("Get(k050) after reopen = %q, %v", v, ok)
	}
	if _, ok := l2.Get("k095"); ok {
		t.Error("k095 was deleted before the reopen but came back: deletes must be logged, not just applied in memory")
	}
}

func TestM1_LogGetReturnsACopy(t *testing.T) {
	l, _ := tOpen(t, tLogPath(t.TempDir()))
	val := []byte("original")
	if err := l.Put("k", val); err != nil {
		t.Fatal(err)
	}
	val[0] = 'X'
	got, _ := l.Get("k")
	if string(got) != "original" {
		t.Fatalf("Get = %q after the caller modified its slice; Put must copy", got)
	}
	got[0] = 'Y'
	again, _ := l.Get("k")
	if string(again) != "original" {
		t.Fatalf("Get = %q after a caller modified a previous Get result; Get must copy", again)
	}
}

func TestM1_LogOnDiskFormatMatchesSpec(t *testing.T) {
	path := tLogPath(t.TempDir())
	l, _ := tOpen(t, path)
	tMustPut(t, l, "link/abc", "hello")
	if err := l.Delete("link/abc"); err != nil {
		t.Fatal(err)
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want := append(tRecord(1, "link/abc", []byte("hello")), tRecord(2, "link/abc", nil)...)
	if !bytes.Equal(got, want) {
		t.Fatalf("log bytes do not match spec.md's record format\n got % x\nwant % x", got, want)
	}
}

func TestM1_LogReadsRecordsWrittenBySomeoneElse(t *testing.T) {
	path := tLogPath(t.TempDir())
	var b []byte
	b = append(b, tRecord(1, "a", []byte("1"))...)
	b = append(b, tRecord(1, "b", []byte("2"))...)
	b = append(b, tRecord(2, "a", nil)...)
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
	l, rec := tOpen(t, path)
	if rec.Records != 3 || l.Len() != 1 {
		t.Fatalf("replayed %d records, Len %d; want 3 records, 1 live key", rec.Records, l.Len())
	}
	if v, ok := l.Get("b"); !ok || string(v) != "2" {
		t.Fatalf("Get(b) = %q, %v", v, ok)
	}
}

func TestM1_LogTornTailIsTruncatedAndLogStaysUsable(t *testing.T) {
	full := tRecord(1, "torn", bytes.Repeat([]byte("x"), 40))
	badCRC := append([]byte(nil), full...)
	badCRC[len(badCRC)-1] ^= 0xFF
	cases := map[string][]byte{
		"short header":            full[:5],
		"body cut short":          full[:len(full)-7],
		"bad checksum at the end": badCRC,
		"zero-filled tail":        make([]byte, 4096),
	}
	for name, junk := range cases {
		t.Run(strings.ReplaceAll(name, " ", "_"), func(t *testing.T) {
			path := tLogPath(t.TempDir())
			l, _ := tOpen(t, path)
			for i := 0; i < 5; i++ {
				tMustPut(t, l, fmt.Sprintf("k%d", i), "v")
			}
			if err := l.Close(); err != nil {
				t.Fatal(err)
			}
			good := tFileSize(t, path)
			tAppendFile(t, path, junk)

			l2, rec, err := OpenLog(path, SyncAlways)
			if err != nil {
				t.Fatalf("OpenLog with a %s: %v; a crash mid-append must not stop the store from starting", name, err)
			}
			if l2 == nil {
				t.Fatal("OpenLog returned nil without an error")
			}
			defer l2.Close()
			if rec.Records != 5 || l2.Len() != 5 {
				t.Errorf("recovered %d records / %d keys, want 5 / 5", rec.Records, l2.Len())
			}
			if rec.TruncatedBytes != int64(len(junk)) {
				t.Errorf("TruncatedBytes = %d, want %d", rec.TruncatedBytes, len(junk))
			}
			if sz := tFileSize(t, path); sz != good {
				t.Errorf("file is %d bytes after recovery, want %d: the torn tail must be cut off, or the next append lands after garbage", sz, good)
			}
			tMustPut(t, l2, "after", "recovery")
			if err := l2.Close(); err != nil {
				t.Fatal(err)
			}
			l3, rec3 := tOpen(t, path)
			if rec3.Records != 6 || rec3.TruncatedBytes != 0 {
				t.Errorf("second reopen: %+v, want 6 records and nothing truncated", rec3)
			}
			if v, ok := l3.Get("after"); !ok || string(v) != "recovery" {
				t.Errorf("write after recovery lost: %q %v", v, ok)
			}
		})
	}
}

func TestM1_LogMidFileCorruptionRefusesToOpen(t *testing.T) {
	path := tLogPath(t.TempDir())
	l, _ := tOpen(t, path)
	for i := 0; i < 5; i++ {
		tMustPut(t, l, fmt.Sprintf("k%d", i), "value-value-value")
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	rec := len(tRecord(1, "k0", []byte("value-value-value")))
	b[rec+rec/2] ^= 0x01 // one bit, in the middle of the second record
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
	l2, _, err := OpenLog(path, SyncAlways)
	if err == nil {
		l2.Close()
		t.Fatal("OpenLog succeeded on a log with a corrupt second record followed by three good ones; " +
			"truncating there would silently delete acknowledged writes")
	}
	if !errors.Is(err, ErrCorrupt) {
		t.Errorf("error %v does not wrap ErrCorrupt", err)
	}
	if after, _ := os.ReadFile(path); !bytes.Equal(after, b) {
		t.Error("OpenLog modified a corrupt log; leave it untouched for the operator")
	}
}

func TestM1_LogRejectsBadKeysAndValues(t *testing.T) {
	l, _ := tOpen(t, tLogPath(t.TempDir()))
	if err := l.Put("", []byte("v")); err == nil {
		t.Error("Put with an empty key succeeded")
	}
	if err := l.Put(strings.Repeat("k", MaxKeyLen+1), []byte("v")); err == nil {
		t.Error("Put with a key longer than MaxKeyLen succeeded")
	}
	if err := l.Put("k", make([]byte, MaxValueLen+1)); err == nil {
		t.Error("Put with a value longer than MaxValueLen succeeded")
	}
	if err := l.Put("k", make([]byte, MaxValueLen)); err != nil {
		t.Errorf("Put with a value of exactly MaxValueLen: %v", err)
	}
	if l.Len() != 1 {
		t.Errorf("Len = %d after one valid put", l.Len())
	}
}

func TestM1_LogPutIfAbsentHasExactlyOneWinner(t *testing.T) {
	path := tLogPath(t.TempDir())
	l, _ := tOpen(t, path)
	var wins atomic.Int32
	var mu sync.Mutex
	winner := ""
	tParallel(t, 50, 10*time.Second, func(i int) {
		val := fmt.Sprintf("candidate-%d", i)
		ins, existing, err := l.PutIfAbsent("code", []byte(val))
		if err != nil {
			t.Errorf("PutIfAbsent: %v", err)
			return
		}
		if ins {
			wins.Add(1)
			mu.Lock()
			winner = val
			mu.Unlock()
		} else if !strings.HasPrefix(string(existing), "candidate-") {
			t.Errorf("a losing PutIfAbsent returned existing = %q; it should see the winner's value", existing)
		}
	})
	if n := wins.Load(); n != 1 {
		t.Fatalf("%d of 50 concurrent PutIfAbsent calls inserted; exactly one may, or two API instances hand out the same code", n)
	}
	if v, _ := l.Get("code"); string(v) != winner {
		t.Fatalf("stored %q, but the winner was %q", v, winner)
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	l2, rec := tOpen(t, path)
	if rec.Records != 1 {
		t.Errorf("%d records on disk, want 1: losers must not write", rec.Records)
	}
	if v, _ := l2.Get("code"); string(v) != winner {
		t.Errorf("after reopen: %q, want %q", v, winner)
	}
}

func TestM1_LogConcurrentWritersAndReaders(t *testing.T) {
	path := tLogPath(t.TempDir())
	l, err := func() (*Log, error) { l, _, err := OpenLog(path, SyncNever); return l, err }()
	if err != nil || l == nil {
		t.Fatalf("OpenLog: %v", err)
	}
	var stop atomic.Bool
	var readers sync.WaitGroup
	for r := 0; r < 4; r++ {
		readers.Add(1)
		go func() {
			defer readers.Done()
			for !stop.Load() {
				l.Get("w0-k0")
				l.Len()
			}
		}()
	}
	tParallel(t, 8, 20*time.Second, func(w int) {
		for i := 0; i < 100; i++ {
			if err := l.Put(fmt.Sprintf("w%d-k%d", w, i), []byte("v")); err != nil {
				t.Errorf("Put: %v", err)
				return
			}
		}
	})
	stop.Store(true)
	readers.Wait()
	if l.Len() != 800 {
		t.Errorf("Len = %d, want 800", l.Len())
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	l2, rec := tOpen(t, path)
	if rec.Records != 800 || l2.Len() != 800 || rec.TruncatedBytes != 0 {
		t.Errorf("after reopen: %+v, Len %d; want 800 intact records (interleaved writes corrupted the log?)", rec, l2.Len())
	}
}

func TestM1_LogWritesFailAfterClose(t *testing.T) {
	l, _ := tOpen(t, tLogPath(t.TempDir()))
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	if err := l.Put("k", []byte("v")); err == nil {
		t.Error("Put after Close succeeded; an acknowledged write that was never written is data loss")
	}
}
