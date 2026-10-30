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
//
// Hints:
//   - The gear table and DefaultCDC are GIVEN so that boundaries are stable.
//   - Validate a manifest once, in one helper, and use it from Commit,
//     Assemble and Begin.
//   - The store and the uploader each get their own mutex; the uploader must
//     not hold its lock while hashing or storing a chunk, or every parallel
//     connection serialises on it.
//   - "Activity extends the session": every successful lookup refreshes the
//     last-touched time.
package chunker

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"math/rand"
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

var errNotImplemented = errors.New("chunker: not implemented")

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
// systems use ≈ 1–8 MiB. GIVEN.
var DefaultCDC = CDCParams{Min: 1024, Avg: 4096, Max: 16384}

// gear is the per-byte random table of the gear hash. It is generated from
// a fixed seed so chunk boundaries are stable across processes and runs. GIVEN.
var gear = func() [256]uint64 {
	var g [256]uint64
	rng := rand.New(rand.NewSource(0x5EED))
	for i := range g {
		g[i] = rng.Uint64()
	}
	return g
}()

// HashChunk returns the hex SHA-256 of b. GIVEN.
func HashChunk(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// FixedChunks splits data into pieces of size bytes.
func FixedChunks(data []byte, size int) ([][]byte, error) {
	// TODO(lab)
	_, _ = data, size
	return nil, errNotImplemented
}

// CDCChunks splits data at content-defined boundaries.
func CDCChunks(data []byte, p CDCParams) ([][]byte, error) {
	// TODO(lab): validate p; walk the bytes keeping h = h<<1 + gear[b];
	// cut when the chunk is >= p.Min and h&(p.Avg-1) == 0, or at p.Max.
	_, _, _ = data, p, gear
	return nil, errNotImplemented
}

// BuildManifest describes chunks as a manifest.
func BuildManifest(chunks [][]byte) Manifest {
	// TODO(lab): per-chunk hash and size, total size, whole-content hash.
	_ = chunks
	return Manifest{}
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
	// TODO(lab): a mutex, hash → bytes, hash → reference count, logical bytes.
}

// NewStore returns an empty store.
func NewStore() *Store {
	// TODO(lab)
	return &Store{}
}

// PutChunk stores data under its hash. existed reports deduplication.
func (s *Store) PutChunk(data []byte) (hash string, existed bool) {
	// TODO(lab)
	_ = data
	return "", false
}

// Has reports whether the store holds a chunk.
func (s *Store) Has(hash string) bool {
	// TODO(lab)
	_ = hash
	return false
}

// Get returns a copy of a chunk after verifying it.
func (s *Store) Get(hash string) ([]byte, error) {
	// TODO(lab)
	_ = hash
	return nil, errNotImplemented
}

// Commit references every chunk of a manifest.
func (s *Store) Commit(m Manifest) error {
	// TODO(lab): validate, check every chunk exists, then count references.
	_ = m
	return errNotImplemented
}

// Release drops a manifest's references, deleting unreferenced chunks.
func (s *Store) Release(m Manifest) {
	// TODO(lab)
	_ = m
}

// Assemble rebuilds a file from its manifest, verifying integrity.
func (s *Store) Assemble(m Manifest) ([]byte, error) {
	// TODO(lab)
	_ = m
	return nil, errNotImplemented
}

// Stats reports what the store holds.
func (s *Store) Stats() Stats {
	// TODO(lab)
	return Stats{}
}

// Corrupt flips one byte of a stored chunk (test hook simulating bit rot).
func (s *Store) Corrupt(hash string) bool {
	// TODO(lab)
	_ = hash
	return false
}

// Session is what a client sees of an upload.
type Session struct {
	ID       string
	Manifest Manifest
	Missing  []ChunkRef
}

// Uploader runs resumable, deduplicating upload sessions over a Store. The
// zero value is not usable; call NewUploader.
type Uploader struct {
	store *Store
	ttl   time.Duration
	now   func() time.Time
	// TODO(lab): a mutex, the sessions by id, a sequence for ids.
}

// NewUploader returns an uploader whose sessions expire after ttl of
// inactivity. now defaults to time.Now.
func NewUploader(store *Store, ttl time.Duration, now func() time.Time) *Uploader {
	if now == nil {
		now = time.Now
	}
	// TODO(lab): initialise your maps.
	return &Uploader{store: store, ttl: ttl, now: now}
}

// Begin opens a session for a manifest and reports which chunks are missing.
func (u *Uploader) Begin(m Manifest) (Session, error) {
	// TODO(lab)
	_ = m
	return Session{}, errNotImplemented
}

// Missing returns the chunks a session still needs.
func (u *Uploader) Missing(id string) ([]ChunkRef, error) {
	// TODO(lab)
	_ = id
	return nil, errNotImplemented
}

// PutChunk accepts one chunk for a session.
func (u *Uploader) PutChunk(id string, data []byte) error {
	// TODO(lab)
	_, _ = id, data
	return errNotImplemented
}

// Commit finishes a session: all chunks present → manifest committed once.
func (u *Uploader) Commit(id string) (Manifest, error) {
	// TODO(lab)
	_ = id
	return Manifest{}, errNotImplemented
}

// Abort forgets a session.
func (u *Uploader) Abort(id string) error {
	// TODO(lab)
	_ = id
	return errNotImplemented
}

// GC deletes chunks that no committed manifest references and no live
// session is waiting on, and forgets expired sessions.
func (u *Uploader) GC() int {
	// TODO(lab)
	return 0
}
