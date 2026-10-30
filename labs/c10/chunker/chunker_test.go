package chunker

import (
	"bytes"
	"errors"
	"math/rand"
	"sync"
	"testing"
	"time"
)

// ---------- helpers (prefixed with t to avoid clashing with your code) ----------

type tClock struct {
	mu sync.Mutex
	t  time.Time
}

func tNewClock() *tClock { return &tClock{t: time.Date(2026, 7, 1, 8, 0, 0, 0, time.UTC)} }
func (c *tClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}
func (c *tClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// tRandom returns deterministic pseudo-random bytes.
func tRandom(seed int64, n int) []byte {
	b := make([]byte, n)
	rand.New(rand.NewSource(seed)).Read(b)
	return b
}

func tCDC(t *testing.T, data []byte) [][]byte {
	t.Helper()
	chunks, err := CDCChunks(data, DefaultCDC)
	if err != nil {
		t.Fatalf("CDCChunks: %v", err)
	}
	return chunks
}

func tHashSet(chunks [][]byte) map[string]bool {
	m := map[string]bool{}
	for _, c := range chunks {
		m[HashChunk(c)] = true
	}
	return m
}

// tShared is the fraction of a's chunk hashes that also appear in b.
func tShared(a, b [][]byte) float64 {
	bs := tHashSet(b)
	n := 0
	for _, c := range a {
		if bs[HashChunk(c)] {
			n++
		}
	}
	return float64(n) / float64(len(a))
}

func tWait(t *testing.T, wg *sync.WaitGroup, d time.Duration, what string) {
	t.Helper()
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(d):
		t.Fatalf("%s did not finish within %v (deadlock in the implementation?)", what, d)
	}
}

// ---------- chunking ----------

func TestFixedChunks_SplitsEvenlyWithShortTail(t *testing.T) {
	data := tRandom(1, 10_500)
	chunks, err := FixedChunks(data, 4_000)
	if err != nil || len(chunks) != 3 || len(chunks[0]) != 4_000 || len(chunks[1]) != 4_000 || len(chunks[2]) != 2_500 {
		t.Fatalf("FixedChunks(10500, 4000): %d chunks (%v); want sizes 4000, 4000, 2500", len(chunks), err)
	}
	if !bytes.Equal(bytes.Join(chunks, nil), data) {
		t.Fatal("concatenated chunks differ from the input")
	}
	chunks[0][0] ^= 0xFF
	if data[0] == chunks[0][0] {
		t.Fatal("chunks alias the caller's buffer; return copies")
	}
	if got, err := FixedChunks(nil, 100); err != nil || len(got) != 0 {
		t.Fatalf("FixedChunks(empty) = %v, %v; want no chunks and no error", got, err)
	}
	if _, err := FixedChunks(data, 0); !errors.Is(err, ErrInvalid) {
		t.Fatalf("FixedChunks(size 0): %v, want ErrInvalid", err)
	}
}

func TestCDCChunks_ValidatesParamsAndBounds(t *testing.T) {
	data := tRandom(2, 200_000)
	bad := []CDCParams{{0, 4096, 16384}, {1024, 3000, 16384}, {5000, 4096, 16384}, {1024, 4096, 2048}}
	for _, p := range bad {
		if _, err := CDCChunks(data, p); !errors.Is(err, ErrInvalid) {
			t.Errorf("CDCChunks(%+v): %v, want ErrInvalid", p, err)
		}
	}
	chunks := tCDC(t, data)
	if !bytes.Equal(bytes.Join(chunks, nil), data) {
		t.Fatal("concatenated chunks differ from the input")
	}
	var total int
	for i, c := range chunks {
		total += len(c)
		if len(c) > DefaultCDC.Max {
			t.Fatalf("chunk %d is %d bytes, above Max %d", i, len(c), DefaultCDC.Max)
		}
		if len(c) < DefaultCDC.Min && i != len(chunks)-1 {
			t.Fatalf("chunk %d is %d bytes, below Min %d (only the last chunk may be short)", i, len(c), DefaultCDC.Min)
		}
	}
	// With Avg = 4 KiB, 200 KB should give on the order of 50 chunks: not 1, not 200.
	if n := len(chunks); n < 20 || n > 150 {
		t.Fatalf("200 KB split into %d chunks; expected roughly 200000/4096 ≈ 49 (±3×)", n)
	}
	again := tCDC(t, data)
	if len(again) != len(chunks) {
		t.Fatalf("CDCChunks is not deterministic: %d then %d chunks", len(chunks), len(again))
	}
	for i := range chunks {
		if !bytes.Equal(chunks[i], again[i]) {
			t.Fatalf("chunk %d differs between runs", i)
		}
	}
	if got := tCDC(t, nil); len(got) != 0 {
		t.Fatalf("CDCChunks(empty) gave %d chunks", len(got))
	}
}

func TestCDCChunks_InsertionOnlyDisturbsNearbyChunks(t *testing.T) {
	original := tRandom(3, 1_000_000)
	insertAt := 200_000
	edited := append(append(append([]byte(nil), original[:insertAt]...), tRandom(4, 100)...), original[insertAt:]...)

	cdcA, cdcB := tCDC(t, original), tCDC(t, edited)
	fixedA, _ := FixedChunks(original, 4096)
	fixedB, _ := FixedChunks(edited, 4096)

	cdcShared := tShared(cdcA, cdcB)
	fixedShared := tShared(fixedA, fixedB)
	t.Logf("after inserting 100 bytes at 20%%: CDC chunks shared %.1f%%, fixed-size chunks shared %.1f%%", 100*cdcShared, 100*fixedShared)
	if cdcShared < 0.9 {
		t.Fatalf("content-defined chunking shares only %.1f%% of chunks after a 100-byte insertion; want ≥ 90%% (boundaries must depend on content, not offsets)", 100*cdcShared)
	}
	if fixedShared > 0.3 {
		t.Fatalf("fixed-size chunking shares %.1f%% of chunks; expected < 30%% (everything after the insertion shifts)", 100*fixedShared)
	}
}

func TestHashAndManifest(t *testing.T) {
	if h := HashChunk([]byte("abc")); h != "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad" {
		t.Fatalf("HashChunk(abc) = %s, want the SHA-256 of abc", h)
	}
	chunks := [][]byte{[]byte("hello "), []byte("world")}
	m := BuildManifest(chunks)
	if m.Size != 11 || len(m.Chunks) != 2 || m.Chunks[0].Size != 6 || m.Chunks[1].Hash != HashChunk([]byte("world")) {
		t.Fatalf("BuildManifest = %+v", m)
	}
	if m.Hash != HashChunk([]byte("hello world")) {
		t.Fatalf("manifest Hash = %s, want the SHA-256 of the whole content", m.Hash)
	}
}

// ---------- store ----------

func TestStore_DedupesAndReferenceCounts(t *testing.T) {
	s := NewStore()
	shared := tRandom(5, 5_000)
	fileA := BuildManifest([][]byte{shared, []byte("only in A")})
	fileB := BuildManifest([][]byte{shared, []byte("only in B")})
	if len(fileA.Chunks) != 2 || len(fileB.Chunks) != 2 {
		t.Fatalf("BuildManifest returned %d and %d chunks, want 2 each", len(fileA.Chunks), len(fileB.Chunks))
	}
	for _, c := range [][]byte{shared, []byte("only in A")} {
		if _, existed := s.PutChunk(c); existed {
			t.Fatalf("first PutChunk reported existed=true")
		}
	}
	if _, existed := s.PutChunk(shared); !existed {
		t.Fatal("second PutChunk of the same bytes must report existed=true (dedupe)")
	}
	if err := s.Commit(fileB); !errors.Is(err, ErrMissingChunk) {
		t.Fatalf("Commit with a missing chunk: %v, want ErrMissingChunk", err)
	}
	s.PutChunk([]byte("only in B"))
	if err := s.Commit(fileA); err != nil {
		t.Fatal(err)
	}
	if err := s.Commit(fileB); err != nil {
		t.Fatal(err)
	}
	st := s.Stats()
	if st.Chunks != 3 || st.PhysicalBytes != 5_000+9+9 || st.LogicalBytes != 2*(5_000+9) {
		t.Fatalf("Stats after two files sharing a 5 KB chunk = %+v; want 3 chunks, 5018 physical, 10018 logical", st)
	}
	s.Release(fileA)
	if !s.Has(fileA.Chunks[0].Hash) {
		t.Fatal("the shared chunk was deleted while file B still references it")
	}
	if s.Has(fileA.Chunks[1].Hash) {
		t.Fatal("file A's private chunk survived Release with no references left")
	}
	s.Release(fileB)
	if st := s.Stats(); st.Chunks != 0 || st.PhysicalBytes != 0 || st.LogicalBytes != 0 {
		t.Fatalf("Stats after releasing everything = %+v, want empty", st)
	}
	if _, err := s.Get("0000"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get(unknown): %v, want ErrNotFound", err)
	}
}

func TestStore_AssembleVerifiesEveryChunkAndTheWholeFile(t *testing.T) {
	s := NewStore()
	data := tRandom(6, 300_000)
	chunks := tCDC(t, data)
	m := BuildManifest(chunks)
	for _, c := range chunks {
		s.PutChunk(c)
	}
	if err := s.Commit(m); err != nil {
		t.Fatal(err)
	}
	got, err := s.Assemble(m)
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("Assemble round trip failed: %v (equal=%v)", err, bytes.Equal(got, data))
	}
	bad := m
	bad.Chunks = append([]ChunkRef(nil), m.Chunks...)
	bad.Chunks[0], bad.Chunks[1] = bad.Chunks[1], bad.Chunks[0] // same chunks, wrong order
	if _, err := s.Assemble(bad); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("Assemble with reordered chunks: %v, want ErrCorrupt (the whole-file hash must be checked)", err)
	}
	if !s.Corrupt(m.Chunks[3].Hash) {
		t.Fatal("Corrupt returned false")
	}
	if _, err := s.Get(m.Chunks[3].Hash); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("Get of a bit-rotted chunk: %v, want ErrCorrupt", err)
	}
	if _, err := s.Assemble(m); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("Assemble over a bit-rotted chunk: %v, want ErrCorrupt (never hand back silently wrong bytes)", err)
	}
}

// ---------- upload sessions ----------

func tManifest(t *testing.T, seed int64, size int) (Manifest, [][]byte) {
	t.Helper()
	chunks := tCDC(t, tRandom(seed, size))
	return BuildManifest(chunks), chunks
}

func TestUpload_MissingSkipsWhatTheStoreAlreadyHolds(t *testing.T) {
	clk := tNewClock()
	s := NewStore()
	up := NewUploader(s, time.Hour, clk.Now)
	m, chunks := tManifest(t, 7, 100_000)
	sess, err := up.Begin(m)
	if err != nil || sess.ID == "" || len(sess.Missing) != len(m.Chunks) {
		t.Fatalf("Begin on an empty store = %+v, %v; want every chunk missing", sess, err)
	}
	for i := range sess.Missing {
		if sess.Missing[i] != m.Chunks[i] {
			t.Fatalf("Missing[%d] = %+v, want manifest order %+v", i, sess.Missing[i], m.Chunks[i])
		}
	}
	for _, c := range chunks {
		if err := up.PutChunk(sess.ID, c); err != nil {
			t.Fatalf("PutChunk: %v", err)
		}
	}
	if _, err := up.Commit(sess.ID); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	// A second user uploads the same file: nothing to transfer.
	again, err := up.Begin(m)
	if err != nil || len(again.Missing) != 0 {
		t.Fatalf("Begin for a duplicate file: missing %d chunks (%v), want 0 (cross-user dedupe)", len(again.Missing), err)
	}
	if _, err := up.Commit(again.ID); err != nil {
		t.Fatalf("Commit of a fully deduped upload: %v", err)
	}
	if st := s.Stats(); st.LogicalBytes != 2*m.Size || st.PhysicalBytes != m.Size {
		t.Fatalf("Stats = %+v; want logical 2× the file and physical 1×", st)
	}
	// A file that shares a prefix only needs the new chunks.
	data2 := append(append([]byte(nil), tRandom(7, 100_000)...), tRandom(8, 20_000)...)
	m2 := BuildManifest(tCDC(t, data2))
	s2, err := up.Begin(m2)
	if err != nil {
		t.Fatal(err)
	}
	if len(s2.Missing) == 0 || len(s2.Missing) > len(m2.Chunks)/2 {
		t.Fatalf("appending 20 KB to a 100 KB file: %d of %d chunks missing; want only the tail (a few chunks)", len(s2.Missing), len(m2.Chunks))
	}
}

func TestUpload_ChunksArriveInAnyOrderAndRepeatedly(t *testing.T) {
	clk := tNewClock()
	s := NewStore()
	up := NewUploader(s, time.Hour, clk.Now)
	m, chunks := tManifest(t, 9, 60_000)
	sess, _ := up.Begin(m)
	if _, err := up.Commit(sess.ID); !errors.Is(err, ErrIncomplete) {
		t.Fatalf("Commit before any chunk: %v, want ErrIncomplete", err)
	}
	for i := len(chunks) - 1; i >= 0; i -= 2 { // odd-indexed chunks, backwards
		if err := up.PutChunk(sess.ID, chunks[i]); err != nil {
			t.Fatal(err)
		}
		if err := up.PutChunk(sess.ID, chunks[i]); err != nil {
			t.Fatalf("repeated PutChunk: %v, want idempotent success (a retried chunk is normal)", err)
		}
	}
	missing, err := up.Missing(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	wantMissing := 0
	for i := range chunks {
		if (len(chunks)-1-i)%2 != 0 {
			wantMissing++
		}
	}
	if len(missing) != wantMissing {
		t.Fatalf("Missing after half the chunks = %d, want %d", len(missing), wantMissing)
	}
	if _, err := up.Commit(sess.ID); !errors.Is(err, ErrIncomplete) {
		t.Fatalf("Commit with half the chunks: %v, want ErrIncomplete", err)
	}
	if err := up.PutChunk(sess.ID, []byte("not part of this file")); !errors.Is(err, ErrUnexpectedChunk) {
		t.Fatalf("PutChunk of a foreign chunk: %v, want ErrUnexpectedChunk", err)
	}
	for _, c := range missing {
		for _, raw := range chunks {
			if HashChunk(raw) == c.Hash {
				if err := up.PutChunk(sess.ID, raw); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	got, err := up.Commit(sess.ID)
	if err != nil || got.Hash != m.Hash {
		t.Fatalf("Commit = %+v, %v", got, err)
	}
	again, err := up.Commit(sess.ID)
	if err != nil || again.Hash != m.Hash {
		t.Fatalf("second Commit = %v, want the same manifest (a retried commit must not fail or double-reference)", err)
	}
	if st := s.Stats(); st.LogicalBytes != m.Size {
		t.Fatalf("LogicalBytes = %d after a repeated Commit, want %d (references added once)", st.LogicalBytes, m.Size)
	}
	file, err := s.Assemble(got)
	if err != nil || !bytes.Equal(file, bytes.Join(chunks, nil)) {
		t.Fatalf("Assemble after upload: %v", err)
	}
}

func TestUpload_ConcurrentChunkUploadsFromManyConnections(t *testing.T) {
	clk := tNewClock()
	s := NewStore()
	up := NewUploader(s, time.Hour, clk.Now)
	m, chunks := tManifest(t, 10, 800_000)
	sess, _ := up.Begin(m)
	var wg sync.WaitGroup
	for w := 0; w < 16; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := w; i < len(chunks); i += 16 {
				if err := up.PutChunk(sess.ID, chunks[i]); err != nil {
					t.Errorf("PutChunk(%d): %v", i, err)
				}
				if i%5 == 0 { // some retries race with the original
					if err := up.PutChunk(sess.ID, chunks[i]); err != nil {
						t.Errorf("duplicate PutChunk(%d): %v", i, err)
					}
				}
				if _, err := up.Missing(sess.ID); err != nil {
					t.Errorf("Missing: %v", err)
				}
			}
		}(w)
	}
	tWait(t, &wg, 20*time.Second, "16 parallel uploaders")
	got, err := up.Commit(sess.ID)
	if err != nil {
		t.Fatalf("Commit after parallel upload: %v", err)
	}
	if file, err := s.Assemble(got); err != nil || !bytes.Equal(file, bytes.Join(chunks, nil)) {
		t.Fatalf("Assemble: %v", err)
	}
	if st := s.Stats(); st.Chunks != len(tHashSet(chunks)) {
		t.Fatalf("store holds %d chunks, want %d distinct", st.Chunks, len(tHashSet(chunks)))
	}
}

func TestUpload_SessionsExpireAndGCReapsOrphans(t *testing.T) {
	clk := tNewClock()
	s := NewStore()
	up := NewUploader(s, 30*time.Minute, clk.Now)
	m, chunks := tManifest(t, 11, 50_000)
	sess, _ := up.Begin(m)
	if err := up.PutChunk(sess.ID, chunks[0]); err != nil {
		t.Fatal(err)
	}
	if n := up.GC(); n != 0 {
		t.Fatalf("GC deleted %d chunks of a live session, want 0 (an upload in progress is not an orphan)", n)
	}
	clk.Advance(29 * time.Minute)
	if err := up.PutChunk(sess.ID, chunks[1]); err != nil {
		t.Fatalf("PutChunk at 29 min: %v (activity must extend the session)", err)
	}
	clk.Advance(31 * time.Minute)
	if _, err := up.Missing(sess.ID); !errors.Is(err, ErrSessionExpired) {
		t.Fatalf("Missing after 31 idle minutes: %v, want ErrSessionExpired", err)
	}
	if _, err := up.Commit(sess.ID); !errors.Is(err, ErrSessionExpired) {
		t.Fatalf("Commit after expiry: %v, want ErrSessionExpired", err)
	}
	if n := up.GC(); n != 2 {
		t.Fatalf("GC deleted %d chunks, want the 2 orphaned by the expired session", n)
	}
	if st := s.Stats(); st.Chunks != 0 {
		t.Fatalf("store still holds %d chunks after GC", st.Chunks)
	}
	if _, err := up.Missing(sess.ID); !errors.Is(err, ErrUnknownSession) {
		t.Fatalf("after GC the expired session must be forgotten: %v, want ErrUnknownSession", err)
	}
	if _, err := up.Missing("up_nope"); !errors.Is(err, ErrUnknownSession) {
		t.Fatalf("Missing(unknown): %v, want ErrUnknownSession", err)
	}
	// Committed data is never collected.
	sess2, _ := up.Begin(m)
	for _, c := range chunks {
		_ = up.PutChunk(sess2.ID, c)
	}
	if _, err := up.Commit(sess2.ID); err != nil {
		t.Fatal(err)
	}
	clk.Advance(2 * time.Hour)
	if n := up.GC(); n != 0 {
		t.Fatalf("GC deleted %d committed chunks", n)
	}
	if _, err := s.Assemble(m); err != nil {
		t.Fatalf("Assemble after GC: %v", err)
	}
	if err := up.Abort("up_nope"); !errors.Is(err, ErrUnknownSession) {
		t.Fatalf("Abort(unknown): %v", err)
	}
}

func TestUpload_BeginValidatesTheManifest(t *testing.T) {
	up := NewUploader(NewStore(), time.Hour, nil)
	m := BuildManifest([][]byte{[]byte("x")})
	if len(m.Chunks) != 1 {
		t.Fatalf("BuildManifest returned %d chunks, want 1", len(m.Chunks))
	}
	cases := map[string]Manifest{
		"no chunks":       {Size: 0, Hash: m.Hash},
		"bad size":        {Size: 2, Hash: m.Hash, Chunks: []ChunkRef{{Hash: m.Chunks[0].Hash, Size: 1}}},
		"malformed hash":  {Size: 1, Hash: m.Hash, Chunks: []ChunkRef{{Hash: "zz", Size: 1}}},
		"zero chunk size": {Size: 0, Hash: m.Hash, Chunks: []ChunkRef{{Hash: m.Chunks[0].Hash, Size: 0}}},
	}
	for name, bad := range cases {
		if _, err := up.Begin(bad); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: Begin = %v, want ErrInvalid", name, err)
		}
	}
	if _, err := up.Begin(m); err != nil {
		t.Fatalf("Begin(valid): %v", err)
	}
}
