// Package auditlog is the lab for A9 Unit 2 §4 (audit trails and
// tamper-evidence): an append-only, hash-chained log in which every entry
// commits to the one before it. Anyone holding the entries (and a trusted
// head hash from somewhere the attacker cannot write) can prove that nothing
// was edited, removed, reordered or cut off.
//
// Suggested order: Canonical/ComputeHash, Append, Entries/Since/Head, Verify,
// ParseCheckpoint, TenantLogs and CombineHeads.
// Run: go test -race ./labs/a9/auditlog/
//
// # Canonical encoding (v1): what the hash covers
//
// An entry's Hash is the hex SHA-256 of Canonical(entry), the concatenation of:
//
//	string  "auditlog/v1"                      (domain separation)
//	uint64  Seq                                (8 bytes, big endian)
//	int64   Time.UnixNano()                    (8 bytes, big endian)
//	string  Tenant, Actor, Action, Resource, Outcome, PrevHash
//	uint32  len(Details)                       (4 bytes, big endian)
//	then for each Details key in ascending byte order:  string key, string value
//
// where "string" means a uint32 big-endian byte length followed by the bytes.
// Length prefixes make the encoding unambiguous: ("ab","c") and ("a","bc")
// hash differently. The Hash field itself is not part of the encoding.
//
// # Behaviour (tests depend on every line)
//
//   - Append requires Actor, Action and Outcome (else ErrInvalidEntry and the
//     log is unchanged: no sequence number is burned). It assigns Seq (1, 2,
//     3 ...), Time (the injected clock, converted to UTC), PrevHash (the
//     previous entry's Hash, or GenesisHash for the first) and Hash. Details
//     are copied. Appends are totally ordered and safe for concurrent use.
//   - Entries and Since return deep copies: nothing a caller does to them can
//     change the log. There is deliberately no Update or Delete method.
//   - Verify checks entries in order and reports the first problem, running
//     these checks on each entry in this order: (1) Hash == ComputeHash:
//     ErrModified; (2) PrevHash == previous entry's Hash (the start checkpoint
//     for the first entry): ErrBrokenLink; (3) Seq == previous Seq + 1:
//     ErrSeqGap. After the last entry, if an anchor is given: anchor.Seq below
//     start.Seq => ErrAnchorNotCovered; anchor.Seq beyond the last entry =>
//     ErrTruncated; the hash at anchor.Seq differs from anchor.Hash =>
//     ErrAnchorMismatch.
//   - Without an anchor, Verify cannot detect truncation of the tail or a
//     rewrite of the tail with recomputed hashes. That is not a bug: it is why
//     heads must be published somewhere the log's operator cannot rewrite.
//   - TenantLogs keeps one independent chain per tenant. CombineHeads reduces
//     every tenant's head to a single digest that can be published or
//     anchored externally.
package auditlog

import (
	"errors"
	"strconv"
	"time"
)

var errNotImplemented = errors.New("auditlog: not implemented")

// GenesisHash is the PrevHash of the first entry of every chain.
const GenesisHash = "0000000000000000000000000000000000000000000000000000000000000000"

// Errors. Use errors.Is; Verify wraps them in *VerifyError.
var (
	ErrInvalidEntry     = errors.New("auditlog: invalid entry")
	ErrModified         = errors.New("auditlog: entry content does not match its hash")
	ErrBrokenLink       = errors.New("auditlog: entry does not chain to its predecessor")
	ErrSeqGap           = errors.New("auditlog: sequence numbers are not contiguous")
	ErrTruncated        = errors.New("auditlog: log is shorter than the anchor says it should be")
	ErrAnchorMismatch   = errors.New("auditlog: history differs from the anchored head")
	ErrAnchorNotCovered = errors.New("auditlog: anchor lies before the verified segment")
	ErrBadCheckpoint    = errors.New("auditlog: malformed checkpoint")
)

// Checkpoint names a point in a chain: "after entry Seq, whose hash was Hash".
// Publishing the latest checkpoint somewhere the log's operator cannot edit
// (a WORM bucket, a witness service, a printed report) is what makes
// truncation and wholesale rewrites detectable.
type Checkpoint struct {
	Seq  uint64
	Hash string
}

// Genesis is the checkpoint before the first entry.
var Genesis = Checkpoint{Seq: 0, Hash: GenesisHash}

// String renders "seq:hash", the export format. Provided.
func (c Checkpoint) String() string { return strconv.FormatUint(c.Seq, 10) + ":" + c.Hash }

// ParseCheckpoint parses String's output. The hash must be exactly 64
// lowercase hex characters and the sequence number a plain decimal that fits
// in a uint64 (no sign, no spaces). Anything else => an error wrapping
// ErrBadCheckpoint.
func ParseCheckpoint(s string) (Checkpoint, error) {
	// TODO(lab)
	return Checkpoint{}, errNotImplemented
}

// Input is what a caller supplies to Append: who did what, to what, with what
// outcome. Never put secrets or raw PII in Details.
type Input struct {
	Tenant   string
	Actor    string
	Action   string
	Resource string
	Outcome  string // for example "success", "denied", "error"
	Details  map[string]string
}

// Entry is one record of the chain.
type Entry struct {
	Seq      uint64            `json:"seq"`
	Time     time.Time         `json:"time"`
	Tenant   string            `json:"tenant,omitempty"`
	Actor    string            `json:"actor"`
	Action   string            `json:"action"`
	Resource string            `json:"resource,omitempty"`
	Outcome  string            `json:"outcome"`
	Details  map[string]string `json:"details,omitempty"`
	PrevHash string            `json:"prev_hash"`
	Hash     string            `json:"hash"`
}

// Canonical returns the exact bytes that ComputeHash hashes (see the package
// documentation for the format).
func Canonical(e Entry) []byte {
	// TODO(lab): encoding/binary's BigEndian.AppendUint32/AppendUint64 and
	// sort.Strings for the Details keys.
	return nil
}

// ComputeHash returns the hex SHA-256 of Canonical(e). The e.Hash field is
// ignored.
func ComputeHash(e Entry) string {
	// TODO(lab)
	return ""
}

// Config configures a Log.
type Config struct {
	Now func() time.Time // default time.Now
}

// Log is a single hash chain. Safe for concurrent use.
type Log struct {
	// TODO(lab): a mutex, the clock and the entries.
}

// New returns an empty Log.
func New(cfg Config) *Log {
	// TODO(lab)
	return &Log{}
}

// Append adds an entry to the end of the chain and returns it (a copy).
func (l *Log) Append(in Input) (Entry, error) {
	// TODO(lab): validate first, then assign Seq/Time/PrevHash/Hash under the lock.
	return Entry{}, errNotImplemented
}

// Len returns the number of entries.
func (l *Log) Len() int {
	// TODO(lab)
	return 0
}

// Head returns the checkpoint after the latest entry (Genesis if empty).
func (l *Log) Head() Checkpoint {
	// TODO(lab)
	return Checkpoint{}
}

// Entries returns a deep copy of every entry.
func (l *Log) Entries() []Entry {
	// TODO(lab)
	return nil
}

// Since returns a deep copy of the entries with Seq > seq (an empty, non-nil
// slice when there are none).
func (l *Log) Since(seq uint64) []Entry {
	// TODO(lab)
	return nil
}

// VerifyError says what Verify found and where.
type VerifyError struct {
	Kind  error  // one of the Err* sentinels; errors.Is works through Unwrap
	Index int    // position in the slice passed to Verify; -1 for anchor errors
	Seq   uint64 // the Seq field of the offending entry (anchor errors: the anchor's Seq)
	Msg   string
}

// Error describes the failure: the kind, the position and the sequence number.
func (e *VerifyError) Error() string {
	// TODO(lab): include e.Kind, e.Index, e.Seq and e.Msg.
	return "auditlog: verification failed"
}

// Unwrap returns the sentinel kind.
func (e *VerifyError) Unwrap() error { return e.Kind }

// Verify checks that entries form an unbroken chain that begins right after
// start (use Genesis for a complete log) and, if anchor is non-nil, that the
// chain agrees with that independently stored checkpoint. See the package
// documentation for the exact order of checks. Failures are *VerifyError.
func Verify(entries []Entry, start Checkpoint, anchor *Checkpoint) error {
	// TODO(lab)
	return errNotImplemented
}

// TenantLogs keeps one independent chain per tenant, so that each credit
// union can verify (and export) its own history without seeing anyone else's.
// Safe for concurrent use.
type TenantLogs struct {
	// TODO(lab): a mutex and a map of tenant -> *Log.
}

// NewTenantLogs returns an empty set of per-tenant logs.
func NewTenantLogs(cfg Config) *TenantLogs {
	// TODO(lab)
	return &TenantLogs{}
}

// Append routes the entry to in.Tenant's chain (created on first use).
// An empty Tenant => ErrInvalidEntry.
func (t *TenantLogs) Append(in Input) (Entry, error) {
	// TODO(lab)
	return Entry{}, errNotImplemented
}

// Entries returns a deep copy of the tenant's chain (empty if it has none).
func (t *TenantLogs) Entries(tenant string) []Entry {
	// TODO(lab)
	return nil
}

// Head returns the tenant's head checkpoint (Genesis if it has no entries).
func (t *TenantLogs) Head(tenant string) Checkpoint {
	// TODO(lab)
	return Checkpoint{}
}

// Heads returns every tenant's head checkpoint.
func (t *TenantLogs) Heads() map[string]Checkpoint {
	// TODO(lab)
	return nil
}

// CombineHeads reduces a set of heads to one hex SHA-256 digest that commits
// to every tenant name, sequence number and hash:
//
//	string "auditlog/heads/v1", uint32 count, then for each tenant in
//	ascending order: string tenant, uint64 Seq, string Hash
//
// (string = uint32 big-endian length + bytes). Publish this single value and
// any change to any tenant's history, including a tenant being dropped,
// changes the digest.
func CombineHeads(heads map[string]Checkpoint) string {
	// TODO(lab)
	return ""
}
