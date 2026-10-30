// Package lsm is a small log-structured merge tree: writes go to an in-memory
// memtable, full memtables are flushed as immutable sorted tables (SSTables),
// each table carries a bloom filter, and tables are merged by a tiered
// compaction that drops shadowed versions and, when it is safe, tombstones.
// It is the lab for A2 Unit 2 §5. Everything lives in memory so the tests are
// fast and deterministic; the on-disk concerns (WAL, file formats, block
// caches) are covered in the unit text and in the wal lab.
//
// What the tests check (they depend on every line of this list):
//
//   - Put/Delete go to the memtable. When the memtable holds
//     Options.MemtableEntries distinct keys it is flushed to a new table at
//     level 0. Flush can also be called by hand.
//   - Levels are tiered: level i holds up to Fanout-1 tables; when it reaches
//     Fanout tables they are merged into ONE table that is appended to level
//     i+1, and the cascade continues. After k flushes of distinct keys the
//     number of tables per level is the base-Fanout representation of k (the
//     least significant digit is level 0). Everything in level i+1 is older
//     than everything in level i; within a level later tables are newer.
//   - Get checks the memtable, then tables newest to oldest. A tombstone
//     found first means "deleted". For each table it asks Table.Lookup, which
//     skips the table by key range, then by bloom filter, and only then
//     binary-searches; DB.Get counts those outcomes in Stats.
//   - Merge keeps the newest version of each key (runs[0] is the newest run).
//     A tombstone is dropped only when nothing older can exist: compaction
//     passes dropTombstones when every level below the merge output is empty.
//     Dropping one earlier resurrects deleted data.
//   - Scan merges the memtable and every table, newest wins, tombstones hide
//     keys, bounds are lo <= key < hi (empty means unbounded).
//   - Stats.UserBytes counts the bytes handed to Put/Delete; BytesWritten
//     counts bytes written into tables by flushes and compactions. Their
//     ratio is the write amplification.
//   - DB is safe for concurrent use.
//
// Suggested order: Bloom, Merge, Table.Lookup, DB.get, DB.put and flush,
// compaction, DB.scan, CompactAll. Run: go test -race ./labs/a2/lsm/
package lsm

import (
	"errors"
	"hash/fnv"
	"sort"
	"sync"
	"sync/atomic"
)

// entryOverhead is the fixed per-entry cost added to len(key)+len(value) when
// sizing entries (sequence number, lengths, framing in a real file format).
const entryOverhead = 16

var (
	// ErrEmptyKey: the empty key is reserved to mean "unbounded" in Scan.
	ErrEmptyKey = errors.New("lsm: empty key")
	// ErrBadOptions: Open was given options that cannot work.
	ErrBadOptions = errors.New("lsm: MemtableEntries must be >= 1, Fanout >= 2 and BloomBitsPerKey >= 0")
)

// Entry is one versioned record. A Tombstone entry means "this key was
// deleted"; its Value is empty.
type Entry struct {
	Key       string
	Value     string
	Tombstone bool
}

// Size is the entry's accounting size in bytes. Provided.
func (e Entry) Size() int { return len(e.Key) + len(e.Value) + entryOverhead }

func runSize(es []Entry) int64 {
	var n int64
	for _, e := range es {
		n += int64(e.Size())
	}
	return n
}

// ---------- bloom filter ----------

// Bloom is a bloom filter: it answers "definitely not present" or "maybe
// present". A nil *Bloom is a valid "disabled" filter that always says maybe.
type Bloom struct {
	bits []uint64
	m    uint64 // number of bits, a multiple of 64
	k    int    // probes per key
}

// hashes returns two independent-ish 64-bit hashes of key for double
// hashing: probe i uses (h1 + i*h2) mod m. h2 is forced odd. Provided.
func hashes(key string) (h1, h2 uint64) {
	f := fnv.New64a()
	_, _ = f.Write([]byte(key))
	h1 = f.Sum64()
	// splitmix64 finaliser over h1 gives a second, well-mixed value.
	z := h1 + 0x9e3779b97f4a7c15
	z = (z ^ (z >> 30)) * 0xbf58476d1ce4e5b9
	z = (z ^ (z >> 27)) * 0x94d049bb133111eb
	z ^= z >> 31
	return h1, z | 1
}

// NewBloom sizes a filter for expectedKeys keys at bitsPerKey bits each:
// m = max(64, expectedKeys*bitsPerKey) rounded up to a multiple of 64 bits,
// and k = round(bitsPerKey * ln 2) clamped to [1, 30], the optimum for that
// density. bitsPerKey <= 0 returns nil (no filter).
//
// TODO(lab): compute m and k as above, allocate the bit array, return the
// filter. Suggested: bits := make([]uint64, m/64); math.Round and math.Ln2 need
// the "math" import.
func NewBloom(expectedKeys, bitsPerKey int) *Bloom {
	return nil
}

// Add records key.
//
// TODO(lab): set the k bits (h1 + i*h2) mod m from hashes(key). Handle a nil filter.
func (b *Bloom) Add(key string) {
}

// MayContain reports false only if key was definitely never added.
//
// TODO(lab): false as soon as one of the k bits is clear; a nil filter answers true.
func (b *Bloom) MayContain(key string) bool {
	return true
}

// Bits returns the filter size in bits (0 for a nil filter). Provided.
func (b *Bloom) Bits() int {
	if b == nil {
		return 0
	}
	return int(b.m)
}

// K returns the number of probes per key (0 for a nil filter). Provided.
func (b *Bloom) K() int {
	if b == nil {
		return 0
	}
	return b.k
}

// ---------- merge ----------

// Merge combines runs into one sorted run with unique keys. Every run is
// sorted by key with unique keys. runs[0] is the NEWEST run: when several
// runs contain the same key, the entry from the lowest-index run wins. If
// dropTombstones is true, winning tombstones are omitted from the output
// (their shadowed older versions are omitted either way). The inputs are not
// modified; the result is never nil.
//
// TODO(lab): a k-way merge. Each step takes the smallest key among the run
// heads, picks the entry from the lowest-index run holding it, and advances
// every run past that key. Dropping tombstones must still skip the shadowed
// older versions.
func Merge(runs [][]Entry, dropTombstones bool) []Entry {
	return nil
}

// ---------- table ----------

// Outcome says how a single-table lookup ended.
type Outcome int

// Lookup outcomes.
const (
	OutRange Outcome = iota + 1 // key outside the table's [min, max]: skipped
	OutBloom                    // bloom filter said "definitely absent": skipped
	OutMiss                     // binary search ran and the key was absent (a false positive)
	OutHit                      // binary search ran and found the key (maybe a tombstone)
)

// Table is an immutable sorted run with a bloom filter and key range.
type Table struct {
	entries  []Entry
	bloom    *Bloom
	min, max string
}

// NewTable builds a table from entries sorted by key with unique keys.
// bitsPerKey <= 0 disables the bloom filter. Provided.
func NewTable(entries []Entry, bitsPerKey int) *Table {
	t := &Table{entries: entries, bloom: NewBloom(len(entries), bitsPerKey)}
	if len(entries) > 0 {
		t.min, t.max = entries[0].Key, entries[len(entries)-1].Key
	}
	for _, e := range entries {
		t.bloom.Add(e.Key)
	}
	return t
}

// Len returns the number of entries. Provided.
func (t *Table) Len() int { return len(t.entries) }

// Range returns the entries with lo <= key < hi (empty bound = unbounded) as
// a sub-slice of the table; callers must not modify it. Provided.
func (t *Table) Range(lo, hi string) []Entry {
	i := sort.Search(len(t.entries), func(i int) bool { return t.entries[i].Key >= lo })
	j := len(t.entries)
	if hi != "" {
		j = sort.Search(len(t.entries), func(i int) bool { return t.entries[i].Key >= hi })
	}
	if j < i {
		j = i
	}
	return t.entries[i:j]
}

// Lookup finds key. Check, in this order: outside [min, max] => OutRange;
// bloom says no => OutBloom; otherwise binary-search the entries and return
// OutHit with the entry or OutMiss. The entry is only meaningful for OutHit.
//
// TODO(lab): see the order above. sort.Search over t.entries; t.bloom may be nil.
func (t *Table) Lookup(key string) (Entry, Outcome) {
	return Entry{}, OutMiss
}

// ---------- DB ----------

// Options configures a DB.
type Options struct {
	MemtableEntries int // flush when the memtable holds this many distinct keys
	Fanout          int // tables per level that trigger a merge into the next level
	BloomBitsPerKey int // bloom filter density for new tables; 0 disables filters
}

// Stats is a snapshot of the DB's counters and shape.
type Stats struct {
	Flushes      int64
	Compactions  int64
	UserBytes    int64 // bytes passed to Put/Delete
	BytesWritten int64 // bytes written into tables by flushes and compactions

	TableLookups int64 // lookups that binary-searched a table
	BloomSkips   int64 // lookups avoided by a bloom filter
	RangeSkips   int64 // lookups avoided by a table's key range

	Levels     []int // tables per level, level 0 first
	Tables     int
	Entries    int // entries stored across all tables (versions and tombstones)
	Tombstones int // tombstone entries stored across all tables
}

// WriteAmp returns BytesWritten / UserBytes (0 if nothing was written).
// Provided.
func (s Stats) WriteAmp() float64 {
	if s.UserBytes == 0 {
		return 0
	}
	return float64(s.BytesWritten) / float64(s.UserBytes)
}

// DB is the LSM tree.
type DB struct {
	mu     sync.RWMutex
	opts   Options
	mem    map[string]Entry
	levels [][]*Table // levels[0] is the newest; within a level, later = newer

	flushes, compactions     atomic.Int64
	userBytes, bytesWritten  atomic.Int64
	tableLookups, bloomSkips atomic.Int64
	rangeSkips               atomic.Int64
}

// Open validates opts and returns an empty DB. Provided.
func Open(opts Options) (*DB, error) {
	if opts.MemtableEntries < 1 || opts.Fanout < 2 || opts.BloomBitsPerKey < 0 {
		return nil, ErrBadOptions
	}
	return &DB{opts: opts, mem: map[string]Entry{}}, nil
}

// Put sets key to value. Provided wrapper; the work is in put.
func (db *DB) Put(key, value string) error {
	if key == "" {
		return ErrEmptyKey
	}
	db.mu.Lock()
	defer db.mu.Unlock()
	db.put(Entry{Key: key, Value: value})
	return nil
}

// Delete writes a tombstone for key (even if the key does not exist: an LSM
// cannot know without reading). Provided wrapper; the work is in put.
func (db *DB) Delete(key string) error {
	if key == "" {
		return ErrEmptyKey
	}
	db.mu.Lock()
	defer db.mu.Unlock()
	db.put(Entry{Key: key, Tombstone: true})
	return nil
}

// Get returns the live value for key. Provided wrapper; the work is in get.
func (db *DB) Get(key string) (string, bool) {
	db.mu.RLock()
	defer db.mu.RUnlock()
	return db.get(key)
}

// Scan returns live entries with lo <= key < hi in key order (empty bound =
// unbounded; limit > 0 caps the count). Never nil. Provided wrapper; the work
// is in scan.
func (db *DB) Scan(lo, hi string, limit int) []Entry {
	db.mu.RLock()
	defer db.mu.RUnlock()
	out := db.scan(lo, hi, limit)
	if out == nil {
		out = []Entry{}
	}
	return out
}

// Flush writes the memtable out as a level-0 table (and compacts if a level
// is full). A no-op when the memtable is empty. Provided wrapper.
func (db *DB) Flush() {
	db.mu.Lock()
	defer db.mu.Unlock()
	db.flush()
}

// CompactAll flushes the memtable and merges every table into one,
// dropping all tombstones and shadowed versions (a "major compaction").
// Provided wrapper; the work is in compactAll.
func (db *DB) CompactAll() {
	db.mu.Lock()
	defer db.mu.Unlock()
	db.flush()
	db.compactAll()
}

// Stats returns a snapshot. Provided.
func (db *DB) Stats() Stats {
	db.mu.RLock()
	defer db.mu.RUnlock()
	s := Stats{
		Flushes: db.flushes.Load(), Compactions: db.compactions.Load(),
		UserBytes: db.userBytes.Load(), BytesWritten: db.bytesWritten.Load(),
		TableLookups: db.tableLookups.Load(), BloomSkips: db.bloomSkips.Load(), RangeSkips: db.rangeSkips.Load(),
	}
	for _, lvl := range db.levels {
		s.Levels = append(s.Levels, len(lvl))
		s.Tables += len(lvl)
		for _, t := range lvl {
			s.Entries += len(t.entries)
			for _, e := range t.entries {
				if e.Tombstone {
					s.Tombstones++
				}
			}
		}
	}
	return s
}

// put records e in the memtable (replacing any version of the same key),
// adds e.Size() to userBytes, and flushes when the memtable reaches
// opts.MemtableEntries distinct keys. Caller holds the write lock.
//
// TODO(lab): db.userBytes.Add, store in db.mem, flush when full.
func (db *DB) put(e Entry) {
}

// get implements Get. Caller holds the read lock. Count each table lookup
// outcome in rangeSkips, bloomSkips or tableLookups (OutMiss and OutHit both
// count as lookups).
//
// TODO(lab): memtable first (a tombstone means deleted), then levels from 0
// upwards and, inside a level, tables from last to first. Use Table.Lookup.
func (db *DB) get(key string) (string, bool) {
	return "", false
}

// sortedMemtable returns the memtable's entries sorted by key.
func (db *DB) sortedMemtable() []Entry {
	es := make([]Entry, 0, len(db.mem))
	for _, e := range db.mem {
		es = append(es, e)
	}
	sort.Slice(es, func(i, j int) bool { return es[i].Key < es[j].Key })
	return es
}

// flush turns the memtable into a table at the end of level 0, counts its
// bytes in bytesWritten and flushes in flushes, clears the memtable, and
// runs compact. Caller holds the write lock.
//
// TODO(lab): sortedMemtable gives the sorted entries; NewTable builds the
// table; db.levels may still be empty.
func (db *DB) flush() {
}

// compact applies the tiered policy from the package comment: while level i
// has Fanout tables, merge them (newest first) into one table appended to
// level i+1 and empty level i. Tombstones are dropped only if every level
// deeper than i is empty (the merge output becomes the oldest data). Count
// bytes in bytesWritten and each merge in compactions. An empty merge result
// produces no table. Caller holds the write lock.
//
// TODO(lab): loop over levels; collect runs newest first (last table of the
// level first); decide dropTombstones; Merge; install the output at level i+1
// (grow db.levels when i+1 does not exist yet). Pass
// "!deeper || breakAlwaysDropTombstones" to Merge so the Break-it switch works.
func (db *DB) compact() {
}

// compactAll merges every table into a single table placed at the deepest
// level, dropping tombstones. Caller holds the write lock and has flushed.
//
// TODO(lab): collect every table newest first, Merge with dropTombstones, keep one table at the deepest level.
func (db *DB) compactAll() {
}

// scan implements Scan: gather the in-range slice of the memtable and of
// every table, newest first, Merge them dropping tombstones, apply limit.
// Caller holds the read lock.
//
// TODO(lab): memtable slice first (it is the newest run), then tables newest first via Table.Range.
func (db *DB) scan(lo, hi string, limit int) []Entry {
	return nil
}
