// Package chunker is the storage core of a file-sync service in miniature:
// content-defined chunking, a content-addressed chunk store that deduplicates
// and reference-counts, manifests that describe a file as a list of chunk
// hashes, and resumable upload sessions that only ask the client for the
// chunks the store does not already have.
//
// It is the lab for C10 Unit 2 §5–§6. Suggested order: FixedChunks and
// CDCChunks (pure functions), HashChunk and BuildManifest, the Store, then
// the Uploader. Run: go test -race ./labs/c10/chunker/
//
// Contract (the tests depend on every line):
//   - FixedChunks(data, size) splits data into consecutive pieces of size
//     bytes; the last piece may be shorter; empty data gives no chunks;
//     size <= 0 is ErrInvalid. Returned slices are copies, never aliases of
//     data.
//   - CDCChunks(data, p) splits data at content-defined boundaries using a
//     gear rolling hash: h = h<<1 + gear[b] over the bytes of the current
//     chunk, with a boundary after byte i when the chunk is at least p.Min
//     long and (h & (p.Avg-1)) == 0, or when it reaches p.Max. p.Avg must be
//     a power of two, 0 < p.Min <= p.Avg <= p.Max; otherwise ErrInvalid.
//     The hash restarts at every boundary. The same bytes always give the
//     same chunks, and because the hash forgets bytes older than 64
//     positions, an insertion only changes the chunks around it.
//   - HashChunk(b) is the lowercase hex SHA-256 of b. BuildManifest(chunks)
//     records each chunk's hash and size, the total size and the SHA-256 of
//     the whole content.
//   - Store: PutChunk stores bytes under their hash and reports whether the
//     chunk was already present; Has/Get look up by hash (Get: ErrNotFound,
//     or ErrCorrupt when the stored bytes no longer hash to their key);
//     Commit(manifest) adds one reference to every chunk of the manifest, all
//     of which must exist (ErrMissingChunk, and then nothing is referenced);
//     Release(manifest) removes those references and deletes chunks whose
//     count reaches zero; Assemble(manifest) returns the file's bytes after
//     verifying every chunk hash, the sizes and the whole-file hash
//     (ErrCorrupt on any mismatch); Stats reports chunks held, physical bytes
//     held and logical bytes committed (the sum of committed manifest
//     sizes). Corrupt(hash) flips a byte of a stored chunk: a test hook that
//     simulates bit rot, never called in production.
//   - Uploader: Begin(manifest) validates the manifest (ErrInvalid for an
//     empty one, a non-positive size or a malformed hash), opens a session
//     and returns it with Missing = the chunks the store does not hold yet,
//     in manifest order and without duplicates. Missing(id) returns what is
//     still needed. PutChunk(id, data) accepts a chunk the session is waiting
//     for, stores it and is idempotent; a chunk the manifest does not list is
//     ErrUnexpectedChunk. Chunks may arrive in any order and concurrently.
//     Commit(id) fails with ErrIncomplete while chunks are missing; otherwise
//     it commits the manifest to the store exactly once and returns it;
//     calling Commit again on the same session returns the same manifest
//     without adding references. Abort(id) forgets the session. A session
//     untouched for longer than the TTL is ErrSessionExpired on every call;
//     an id never issued is ErrUnknownSession.
//   - GC() deletes chunks that no committed manifest references and no live
//     (unexpired, uncommitted) session is waiting on, and returns how many it
//     deleted: the orphan reaper for uploads that never finished.
//   - All exported methods on Store and Uploader are safe for concurrent
//     use. Time comes from the injected clock.
package chunker

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math/rand"
	"sync"
	"time"
)

// Errors are values: compare with errors.Is.
var (
	ErrInvalid         = errors.New("chunker: invalid argument")
	ErrNotFound        = errors.New("chunker: chunk not found")
	ErrCorrupt         = errors.New("chunker: content does not match its hash")
	ErrMissingChunk    = errors.New("chunker: manifest references a chunk the store does not hold")
	ErrUnknownSession  = errors.New("chunker: unknown upload session")
	ErrSessionExpired  = errors.New("chunker: upload session expired")
	ErrUnexpectedChunk = errors.New("chunker: chunk is not part of this upload")
	ErrIncomplete      = errors.New("chunker: upload is incomplete")
)

// ChunkRef names one chunk of a file.
type ChunkRef struct {
	Hash string
	Size int
}

// Manifest describes a file as an ordered list of chunks.
type Manifest struct {
	Size   int64
	Hash   string // SHA-256 of the whole content
	Chunks []ChunkRef
}

// CDCParams tunes content-defined chunking. Avg must be a power of two.
type CDCParams struct {
	Min, Avg, Max int
}

// DefaultCDC is a reasonable default: ≈ 4 KiB chunks for the tests; real
// systems use ≈ 1–8 MiB.
var DefaultCDC = CDCParams{Min: 1024, Avg: 4096, Max: 16384}

// gear is the per-byte random table of the gear hash. It is generated from
// a fixed seed so chunk boundaries are stable across processes and runs.
var gear = func() [256]uint64 {
	var g [256]uint64
	rng := rand.New(rand.NewSource(0x5EED))
	for i := range g {
		g[i] = rng.Uint64()
	}
	return g
}()

// HashChunk returns the hex SHA-256 of b.
func HashChunk(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// FixedChunks splits data into pieces of size bytes.
func FixedChunks(data []byte, size int) ([][]byte, error) {
	if size <= 0 {
		return nil, fmt.Errorf("%w: chunk size must be positive", ErrInvalid)
	}
	var out [][]byte
	for start := 0; start < len(data); start += size {
		end := start + size
		if end > len(data) {
			end = len(data)
		}
		out = append(out, append([]byte(nil), data[start:end]...))
	}
	return out, nil
}

// CDCChunks splits data at content-defined boundaries.
func CDCChunks(data []byte, p CDCParams) ([][]byte, error) {
	if p.Min <= 0 || p.Avg < p.Min || p.Max < p.Avg || p.Avg&(p.Avg-1) != 0 {
		return nil, fmt.Errorf("%w: need 0 < Min <= Avg <= Max with Avg a power of two", ErrInvalid)
	}
	mask := uint64(p.Avg - 1)
	var out [][]byte
	start := 0
	var h uint64
	for i := 0; i < len(data); i++ {
		h = h<<1 + gear[data[i]]
		n := i - start + 1
		if n < p.Min {
			continue
		}
		if h&mask == 0 || n >= p.Max {
			out = append(out, append([]byte(nil), data[start:i+1]...))
			start = i + 1
			h = 0
		}
	}
	if start < len(data) {
		out = append(out, append([]byte(nil), data[start:]...))
	}
	return out, nil
}

// BuildManifest describes chunks as a manifest.
func BuildManifest(chunks [][]byte) Manifest {
	whole := sha256.New()
	m := Manifest{}
	for _, c := range chunks {
		whole.Write(c)
		m.Chunks = append(m.Chunks, ChunkRef{Hash: HashChunk(c), Size: len(c)})
		m.Size += int64(len(c))
	}
	m.Hash = hex.EncodeToString(whole.Sum(nil))
	return m
}

func validManifest(m Manifest) error {
	if len(m.Chunks) == 0 {
		return fmt.Errorf("%w: manifest has no chunks", ErrInvalid)
	}
	var total int64
	for i, c := range m.Chunks {
		if c.Size <= 0 {
			return fmt.Errorf("%w: chunk %d has size %d", ErrInvalid, i, c.Size)
		}
		if len(c.Hash) != 64 {
			return fmt.Errorf("%w: chunk %d hash %q is not a hex SHA-256", ErrInvalid, i, c.Hash)
		}
		if _, err := hex.DecodeString(c.Hash); err != nil {
			return fmt.Errorf("%w: chunk %d hash %q is not hex", ErrInvalid, i, c.Hash)
		}
		total += int64(c.Size)
	}
	if total != m.Size {
		return fmt.Errorf("%w: chunk sizes sum to %d, manifest says %d", ErrInvalid, total, m.Size)
	}
	if len(m.Hash) != 64 {
		return fmt.Errorf("%w: manifest hash %q is not a hex SHA-256", ErrInvalid, m.Hash)
	}
	return nil
}

// Stats summarises a Store.
type Stats struct {
	Chunks        int
	PhysicalBytes int64 // bytes held
	LogicalBytes  int64 // bytes of committed files (what users think they stored)
}

// Store is a content-addressed chunk store. The zero value is not usable;
// call NewStore.
type Store struct {
	mu      sync.Mutex
	chunks  map[string][]byte
	refs    map[string]int
	logical int64
}

// NewStore returns an empty store.
func NewStore() *Store {
	return &Store{chunks: map[string][]byte{}, refs: map[string]int{}}
}

// PutChunk stores data under its hash. existed reports deduplication.
func (s *Store) PutChunk(data []byte) (hash string, existed bool) {
	hash = HashChunk(data)
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.chunks[hash]; ok {
		return hash, true
	}
	s.chunks[hash] = append([]byte(nil), data...)
	return hash, false
}

// Has reports whether the store holds a chunk.
func (s *Store) Has(hash string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.chunks[hash]
	return ok
}

// Get returns a copy of a chunk after verifying it.
func (s *Store) Get(hash string) ([]byte, error) {
	s.mu.Lock()
	data, ok := s.chunks[hash]
	s.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrNotFound, hash)
	}
	if HashChunk(data) != hash {
		return nil, fmt.Errorf("%w: chunk %s", ErrCorrupt, hash)
	}
	return append([]byte(nil), data...), nil
}

// Commit references every chunk of a manifest.
func (s *Store) Commit(m Manifest) error {
	if err := validManifest(m); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, c := range m.Chunks {
		if _, ok := s.chunks[c.Hash]; !ok {
			return fmt.Errorf("%w: %s", ErrMissingChunk, c.Hash)
		}
	}
	for _, c := range m.Chunks {
		s.refs[c.Hash]++
	}
	s.logical += m.Size
	return nil
}

// Release drops a manifest's references, deleting unreferenced chunks.
func (s *Store) Release(m Manifest) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, c := range m.Chunks {
		if s.refs[c.Hash] == 0 {
			continue
		}
		s.refs[c.Hash]--
		if s.refs[c.Hash] == 0 {
			delete(s.refs, c.Hash)
			delete(s.chunks, c.Hash)
		}
	}
	s.logical -= m.Size
	if s.logical < 0 {
		s.logical = 0
	}
}

// Assemble rebuilds a file from its manifest, verifying integrity.
func (s *Store) Assemble(m Manifest) ([]byte, error) {
	if err := validManifest(m); err != nil {
		return nil, err
	}
	out := make([]byte, 0, m.Size)
	whole := sha256.New()
	for _, c := range m.Chunks {
		data, err := s.Get(c.Hash)
		if err != nil {
			return nil, err
		}
		if len(data) != c.Size {
			return nil, fmt.Errorf("%w: chunk %s has %d bytes, manifest says %d", ErrCorrupt, c.Hash, len(data), c.Size)
		}
		whole.Write(data)
		out = append(out, data...)
	}
	if got := hex.EncodeToString(whole.Sum(nil)); got != m.Hash {
		return nil, fmt.Errorf("%w: assembled file hashes to %s, manifest says %s", ErrCorrupt, got, m.Hash)
	}
	return out, nil
}

// Stats reports what the store holds.
func (s *Store) Stats() Stats {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := Stats{Chunks: len(s.chunks), LogicalBytes: s.logical}
	for _, c := range s.chunks {
		st.PhysicalBytes += int64(len(c))
	}
	return st
}

// Corrupt flips one byte of a stored chunk (test hook simulating bit rot).
func (s *Store) Corrupt(hash string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, ok := s.chunks[hash]
	if !ok || len(data) == 0 {
		return false
	}
	data[0] ^= 0xFF
	return true
}

// deleteUnreferenced removes chunks not referenced by any manifest and not in
// keep. Caller holds s.mu.
func (s *Store) deleteUnreferenced(keep map[string]bool) int {
	n := 0
	for hash := range s.chunks {
		if s.refs[hash] == 0 && !keep[hash] {
			delete(s.chunks, hash)
			n++
		}
	}
	return n
}

// Session is what a client sees of an upload.
type Session struct {
	ID       string
	Manifest Manifest
	Missing  []ChunkRef
}

type session struct {
	manifest  Manifest
	needed    map[string]bool // hashes still missing
	order     []ChunkRef      // manifest order, deduplicated
	committed bool
	lastTouch time.Time
}

// Uploader runs resumable, deduplicating upload sessions over a Store. The
// zero value is not usable; call NewUploader.
type Uploader struct {
	store    *Store
	ttl      time.Duration
	now      func() time.Time
	mu       sync.Mutex
	sessions map[string]*session
	seq      uint64
}

// NewUploader returns an uploader whose sessions expire after ttl of
// inactivity. now defaults to time.Now.
func NewUploader(store *Store, ttl time.Duration, now func() time.Time) *Uploader {
	if now == nil {
		now = time.Now
	}
	return &Uploader{store: store, ttl: ttl, now: now, sessions: map[string]*session{}}
}

// Begin opens a session for a manifest and reports which chunks are missing.
func (u *Uploader) Begin(m Manifest) (Session, error) {
	if err := validManifest(m); err != nil {
		return Session{}, err
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	u.seq++
	s := &session{manifest: m, needed: map[string]bool{}, lastTouch: u.now()}
	seen := map[string]bool{}
	for _, c := range m.Chunks {
		if seen[c.Hash] {
			continue
		}
		seen[c.Hash] = true
		s.order = append(s.order, c)
		if !u.store.Has(c.Hash) {
			s.needed[c.Hash] = true
		}
	}
	id := fmt.Sprintf("up_%06d", u.seq)
	u.sessions[id] = s
	return Session{ID: id, Manifest: m, Missing: s.missingLocked()}, nil
}

func (s *session) missingLocked() []ChunkRef {
	var out []ChunkRef
	for _, c := range s.order {
		if s.needed[c.Hash] {
			out = append(out, c)
		}
	}
	return out
}

// lookup returns a live session, touching it. Caller holds u.mu.
func (u *Uploader) lookup(id string) (*session, error) {
	s, ok := u.sessions[id]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrUnknownSession, id)
	}
	now := u.now()
	if now.Sub(s.lastTouch) > u.ttl {
		return nil, fmt.Errorf("%w: %s", ErrSessionExpired, id)
	}
	s.lastTouch = now
	return s, nil
}

// Missing returns the chunks a session still needs.
func (u *Uploader) Missing(id string) ([]ChunkRef, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	s, err := u.lookup(id)
	if err != nil {
		return nil, err
	}
	return s.missingLocked(), nil
}

// PutChunk accepts one chunk for a session.
func (u *Uploader) PutChunk(id string, data []byte) error {
	hash := HashChunk(data)
	u.mu.Lock()
	s, err := u.lookup(id)
	if err != nil {
		u.mu.Unlock()
		return err
	}
	listed := false
	for _, c := range s.order {
		if c.Hash == hash {
			listed = true
			break
		}
	}
	if !listed {
		u.mu.Unlock()
		return fmt.Errorf("%w: %s", ErrUnexpectedChunk, hash)
	}
	u.mu.Unlock()
	u.store.PutChunk(data) // outside u.mu: the store has its own lock, and uploads should not serialise on the session lock
	u.mu.Lock()
	if s2, ok := u.sessions[id]; ok && s2 == s {
		delete(s.needed, hash)
	}
	u.mu.Unlock()
	return nil
}

// Commit finishes a session: all chunks present → manifest committed once.
func (u *Uploader) Commit(id string) (Manifest, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	s, err := u.lookup(id)
	if err != nil {
		return Manifest{}, err
	}
	if s.committed {
		return s.manifest, nil
	}
	if len(s.needed) > 0 {
		return Manifest{}, fmt.Errorf("%w: %d chunks missing", ErrIncomplete, len(s.needed))
	}
	if err := u.store.Commit(s.manifest); err != nil {
		return Manifest{}, err
	}
	s.committed = true
	return s.manifest, nil
}

// Abort forgets a session.
func (u *Uploader) Abort(id string) error {
	u.mu.Lock()
	defer u.mu.Unlock()
	if _, ok := u.sessions[id]; !ok {
		return fmt.Errorf("%w: %s", ErrUnknownSession, id)
	}
	delete(u.sessions, id)
	return nil
}

// GC deletes chunks that no committed manifest references and no live
// session is waiting on, and forgets expired sessions.
func (u *Uploader) GC() int {
	u.mu.Lock()
	defer u.mu.Unlock()
	now := u.now()
	keep := map[string]bool{}
	for id, s := range u.sessions {
		if now.Sub(s.lastTouch) > u.ttl {
			delete(u.sessions, id)
			continue
		}
		if s.committed {
			continue
		}
		for _, c := range s.order {
			keep[c.Hash] = true
		}
	}
	u.store.mu.Lock()
	defer u.store.mu.Unlock()
	return u.store.deleteUnreferenced(keep)
}
