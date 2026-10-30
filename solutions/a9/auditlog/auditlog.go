// Package auditlog is the lab for A9 Unit 2 §4 (audit trails and
// tamper-evidence): an append-only, hash-chained log in which every entry
// commits to the one before it. Anyone holding the entries (and a trusted
// head hash from somewhere the attacker cannot write) can prove that nothing
// was edited, removed, reordered or cut off.
//
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
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

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

// String renders "seq:hash", the export format.
func (c Checkpoint) String() string { return strconv.FormatUint(c.Seq, 10) + ":" + c.Hash }

// ParseCheckpoint parses String's output. The hash must be 64 lowercase hex
// characters and the sequence number a plain decimal.
func ParseCheckpoint(s string) (Checkpoint, error) {
	seq, hash, ok := strings.Cut(s, ":")
	if !ok || seq == "" || len(hash) != 64 {
		return Checkpoint{}, fmt.Errorf("%w: %q", ErrBadCheckpoint, s)
	}
	for _, c := range seq {
		if c < '0' || c > '9' {
			return Checkpoint{}, fmt.Errorf("%w: %q", ErrBadCheckpoint, s)
		}
	}
	n, err := strconv.ParseUint(seq, 10, 64)
	if err != nil {
		return Checkpoint{}, fmt.Errorf("%w: %q", ErrBadCheckpoint, s)
	}
	for _, c := range hash {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return Checkpoint{}, fmt.Errorf("%w: %q", ErrBadCheckpoint, s)
		}
	}
	return Checkpoint{Seq: n, Hash: hash}, nil
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

func (e Entry) clone() Entry {
	if e.Details != nil {
		m := make(map[string]string, len(e.Details))
		for k, v := range e.Details {
			m[k] = v
		}
		e.Details = m
	}
	return e
}

func putString(buf []byte, s string) []byte {
	buf = binary.BigEndian.AppendUint32(buf, uint32(len(s)))
	return append(buf, s...)
}

// Canonical returns the exact bytes that ComputeHash hashes (see the package
// documentation for the format).
func Canonical(e Entry) []byte {
	buf := putString(nil, "auditlog/v1")
	buf = binary.BigEndian.AppendUint64(buf, e.Seq)
	buf = binary.BigEndian.AppendUint64(buf, uint64(e.Time.UnixNano()))
	for _, s := range []string{e.Tenant, e.Actor, e.Action, e.Resource, e.Outcome, e.PrevHash} {
		buf = putString(buf, s)
	}
	keys := make([]string, 0, len(e.Details))
	for k := range e.Details {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	buf = binary.BigEndian.AppendUint32(buf, uint32(len(keys)))
	for _, k := range keys {
		buf = putString(buf, k)
		buf = putString(buf, e.Details[k])
	}
	return buf
}

// ComputeHash returns the hex SHA-256 of Canonical(e). The e.Hash field is
// ignored.
func ComputeHash(e Entry) string {
	sum := sha256.Sum256(Canonical(e))
	return hex.EncodeToString(sum[:])
}

// Config configures a Log.
type Config struct {
	Now func() time.Time // default time.Now
}

// Log is a single hash chain. Safe for concurrent use.
type Log struct {
	mu      sync.Mutex
	now     func() time.Time
	entries []Entry
}

// New returns an empty Log.
func New(cfg Config) *Log {
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	return &Log{now: now}
}

func validate(in Input) error {
	switch {
	case in.Actor == "":
		return fmt.Errorf("%w: actor is required", ErrInvalidEntry)
	case in.Action == "":
		return fmt.Errorf("%w: action is required", ErrInvalidEntry)
	case in.Outcome == "":
		return fmt.Errorf("%w: outcome is required (log failures and denials too)", ErrInvalidEntry)
	}
	return nil
}

// Append adds an entry to the end of the chain and returns it.
func (l *Log) Append(in Input) (Entry, error) {
	if err := validate(in); err != nil {
		return Entry{}, err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	e := Entry{
		Seq:      uint64(len(l.entries)) + 1,
		Time:     l.now().UTC(),
		Tenant:   in.Tenant,
		Actor:    in.Actor,
		Action:   in.Action,
		Resource: in.Resource,
		Outcome:  in.Outcome,
		Details:  in.Details,
		PrevHash: GenesisHash,
	}
	if n := len(l.entries); n > 0 {
		e.PrevHash = l.entries[n-1].Hash
	}
	e = e.clone()
	e.Hash = ComputeHash(e)
	l.entries = append(l.entries, e)
	return e.clone(), nil
}

// Len returns the number of entries.
func (l *Log) Len() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.entries)
}

// Head returns the checkpoint after the latest entry (Genesis if empty).
func (l *Log) Head() Checkpoint {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.head()
}

func (l *Log) head() Checkpoint {
	if n := len(l.entries); n > 0 {
		return Checkpoint{Seq: l.entries[n-1].Seq, Hash: l.entries[n-1].Hash}
	}
	return Genesis
}

// Entries returns a deep copy of every entry.
func (l *Log) Entries() []Entry { return l.Since(0) }

// Since returns a deep copy of the entries with Seq > seq.
func (l *Log) Since(seq uint64) []Entry {
	l.mu.Lock()
	defer l.mu.Unlock()
	if seq >= uint64(len(l.entries)) {
		return []Entry{}
	}
	out := make([]Entry, 0, len(l.entries)-int(seq))
	for _, e := range l.entries[seq:] {
		out = append(out, e.clone())
	}
	return out
}

// VerifyError says what Verify found and where.
type VerifyError struct {
	Kind  error  // one of the Err* sentinels; errors.Is works through Unwrap
	Index int    // position in the slice passed to Verify; -1 for anchor errors
	Seq   uint64 // the Seq field of the offending entry (anchor errors: the anchor's Seq)
	Msg   string
}

func (e *VerifyError) Error() string {
	return fmt.Sprintf("%v (index %d, seq %d): %s", e.Kind, e.Index, e.Seq, e.Msg)
}

// Unwrap returns the sentinel kind.
func (e *VerifyError) Unwrap() error { return e.Kind }

// Verify checks that entries form an unbroken chain that begins right after
// start (use Genesis for a complete log) and, if anchor is non-nil, that the
// chain agrees with that independently stored checkpoint. See the package
// documentation for the exact order of checks.
func Verify(entries []Entry, start Checkpoint, anchor *Checkpoint) error {
	prevHash, prevSeq := start.Hash, start.Seq
	for i, e := range entries {
		if got := ComputeHash(e); e.Hash != got {
			return &VerifyError{ErrModified, i, e.Seq, "stored hash " + short(e.Hash) + " but content hashes to " + short(got)}
		}
		if e.PrevHash != prevHash {
			return &VerifyError{ErrBrokenLink, i, e.Seq, "prev_hash " + short(e.PrevHash) + " but predecessor's hash is " + short(prevHash)}
		}
		if e.Seq != prevSeq+1 {
			return &VerifyError{ErrSeqGap, i, e.Seq, fmt.Sprintf("expected seq %d", prevSeq+1)}
		}
		prevHash, prevSeq = e.Hash, e.Seq
	}
	if anchor == nil {
		return nil
	}
	switch {
	case anchor.Seq < start.Seq:
		return &VerifyError{ErrAnchorNotCovered, -1, anchor.Seq, fmt.Sprintf("verified segment starts after seq %d", start.Seq)}
	case anchor.Seq > prevSeq:
		return &VerifyError{ErrTruncated, -1, anchor.Seq, fmt.Sprintf("anchor says seq %d exists but the log ends at %d", anchor.Seq, prevSeq)}
	}
	at := start.Hash
	if anchor.Seq > start.Seq {
		at = entries[anchor.Seq-start.Seq-1].Hash
	}
	if at != anchor.Hash {
		return &VerifyError{ErrAnchorMismatch, -1, anchor.Seq, "hash at the anchored position is " + short(at) + " but the anchor recorded " + short(anchor.Hash)}
	}
	return nil
}

func short(h string) string {
	if len(h) > 12 {
		return h[:12] + "..."
	}
	return h
}

// TenantLogs keeps one independent chain per tenant, so that each credit
// union can verify (and export) its own history without seeing anyone else's.
// Safe for concurrent use.
type TenantLogs struct {
	mu   sync.Mutex
	cfg  Config
	logs map[string]*Log
}

// NewTenantLogs returns an empty set of per-tenant logs.
func NewTenantLogs(cfg Config) *TenantLogs {
	return &TenantLogs{cfg: cfg, logs: make(map[string]*Log)}
}

// Append routes the entry to in.Tenant's chain (created on first use).
// An empty Tenant => ErrInvalidEntry.
func (t *TenantLogs) Append(in Input) (Entry, error) {
	if in.Tenant == "" {
		return Entry{}, fmt.Errorf("%w: tenant is required", ErrInvalidEntry)
	}
	if err := validate(in); err != nil {
		return Entry{}, err
	}
	t.mu.Lock()
	l, ok := t.logs[in.Tenant]
	if !ok {
		l = New(t.cfg)
		t.logs[in.Tenant] = l
	}
	t.mu.Unlock()
	return l.Append(in)
}

func (t *TenantLogs) get(tenant string) *Log {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.logs[tenant]
}

// Entries returns a deep copy of the tenant's chain (empty if it has none).
func (t *TenantLogs) Entries(tenant string) []Entry {
	if l := t.get(tenant); l != nil {
		return l.Entries()
	}
	return []Entry{}
}

// Head returns the tenant's head checkpoint (Genesis if it has no entries).
func (t *TenantLogs) Head(tenant string) Checkpoint {
	if l := t.get(tenant); l != nil {
		return l.Head()
	}
	return Genesis
}

// Heads returns every tenant's head checkpoint.
func (t *TenantLogs) Heads() map[string]Checkpoint {
	t.mu.Lock()
	logs := make(map[string]*Log, len(t.logs))
	for k, v := range t.logs {
		logs[k] = v
	}
	t.mu.Unlock()
	out := make(map[string]Checkpoint, len(logs))
	for k, l := range logs {
		out[k] = l.Head()
	}
	return out
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
	names := make([]string, 0, len(heads))
	for n := range heads {
		names = append(names, n)
	}
	sort.Strings(names)
	buf := putString(nil, "auditlog/heads/v1")
	buf = binary.BigEndian.AppendUint32(buf, uint32(len(names)))
	for _, n := range names {
		buf = putString(buf, n)
		buf = binary.BigEndian.AppendUint64(buf, heads[n].Seq)
		buf = putString(buf, heads[n].Hash)
	}
	sum := sha256.Sum256(buf)
	return hex.EncodeToString(sum[:])
}
