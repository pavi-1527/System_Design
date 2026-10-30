package auditlog

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// ---------- helpers (prefixed with t to avoid clashing with your code) ----------

type tClock struct {
	mu sync.Mutex
	t  time.Time
}

// The clock deliberately lives in a non-UTC zone: entries must be stamped in UTC.
func tNewClock() *tClock {
	ist := time.FixedZone("IST", 5*3600+1800)
	return &tClock{t: time.Date(2026, 5, 4, 12, 0, 0, 123456789, ist)}
}

// Now returns the current time and advances the clock by 1ms, so consecutive
// entries get distinct timestamps.
func (c *tClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	t := c.t
	c.t = c.t.Add(time.Millisecond)
	return t
}

func tWait(t *testing.T, wg *sync.WaitGroup, d time.Duration, what string) {
	t.Helper()
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(d):
		t.Fatalf("%s did not finish within %v (deadlock or livelock in the implementation?)", what, d)
	}
}

func tInput(i int) Input {
	return Input{
		Tenant:   "cu-alpha",
		Actor:    fmt.Sprintf("staff:%d", i%3),
		Action:   "transfer.approve",
		Resource: fmt.Sprintf("transfer:%d", i),
		Outcome:  "success",
		Details:  map[string]string{"amount_cents": strconv.Itoa(1000 * i), "ip": "10.0.0.7"},
	}
}

// tLog appends n entries and returns the log plus a copy of its entries.
func tLog(t *testing.T, n int) (*Log, []Entry) {
	t.Helper()
	l := New(Config{Now: tNewClock().Now})
	for i := 1; i <= n; i++ {
		if _, err := l.Append(tInput(i)); err != nil {
			t.Fatalf("Append #%d: %v; every other expectation in this test depends on it", i, err)
		}
	}
	es := l.Entries()
	if len(es) != n {
		t.Fatalf("Entries() returned %d entries after %d appends", len(es), n)
	}
	return l, es
}

// tRechain recomputes PrevHash/Hash for entries[from:], as an attacker with
// write access to the whole log (but not to the external anchor) would.
// With renumber, Seq is rewritten too so the sequence stays contiguous.
func tRechain(es []Entry, from int, renumber bool) {
	for j := from; j < len(es); j++ {
		if renumber {
			es[j].Seq = uint64(j) + 1
		}
		if j == 0 {
			es[j].PrevHash = GenesisHash
		} else {
			es[j].PrevHash = es[j-1].Hash
		}
		es[j].Hash = ComputeHash(es[j])
	}
}

func tCopy(es []Entry) []Entry {
	out := make([]Entry, len(es))
	for i, e := range es {
		if e.Details != nil {
			m := map[string]string{}
			for k, v := range e.Details {
				m[k] = v
			}
			e.Details = m
		}
		out[i] = e
	}
	return out
}

func tCheckpointOf(e Entry) Checkpoint { return Checkpoint{Seq: e.Seq, Hash: e.Hash} }

// ---------- canonical encoding and hashing ----------

// tSpec is an independent encoder written straight from the format in the
// package documentation. Your Canonical must agree with it byte for byte.
func tSpec(e Entry) []byte {
	var b bytes.Buffer
	str := func(s string) {
		binary.Write(&b, binary.BigEndian, uint32(len(s)))
		b.WriteString(s)
	}
	str("auditlog/v1")
	binary.Write(&b, binary.BigEndian, e.Seq)
	binary.Write(&b, binary.BigEndian, e.Time.UnixNano())
	for _, s := range []string{e.Tenant, e.Actor, e.Action, e.Resource, e.Outcome, e.PrevHash} {
		str(s)
	}
	keys := make([]string, 0, len(e.Details))
	for k := range e.Details {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	binary.Write(&b, binary.BigEndian, uint32(len(keys)))
	for _, k := range keys {
		str(k)
		str(e.Details[k])
	}
	return b.Bytes()
}

func tSampleEntry() Entry {
	return Entry{
		Seq:      7,
		Time:     time.Unix(1700000000, 123456789).UTC(),
		Tenant:   "cu-alpha",
		Actor:    "member:42",
		Action:   "transfer.create",
		Resource: "acct:9",
		Outcome:  "success",
		Details:  map[string]string{"b": "2", "a": "1"},
		PrevHash: strings.Repeat("ab", 32),
	}
}

func TestCanonical_MatchesTheDocumentedFormatByteForByte(t *testing.T) {
	e := tSampleEntry()
	if got, want := Canonical(e), tSpec(e); !bytes.Equal(got, want) {
		t.Fatalf("Canonical(e) differs from the documented encoding\n got: %x\nwant: %x", got, want)
	}
	sum := sha256.Sum256(tSpec(e))
	if got := ComputeHash(e); got != hex.EncodeToString(sum[:]) {
		t.Fatalf("ComputeHash = %s; expected the lowercase hex SHA-256 of the canonical bytes", got)
	}
	// A pinned value: two independent verifiers must agree on this forever.
	const golden = "2ea142978e802aafa75548366de22db52c5bb7710349673f246b2fb002155f88"
	if got := ComputeHash(e); got != golden {
		t.Fatalf("ComputeHash(sample) = %s; the pinned value is %s (the encoding is a persistent format: changing it invalidates every stored chain)", got, golden)
	}
}

func TestCanonical_IgnoresTheHashFieldAndDetailsOrder(t *testing.T) {
	a := tSampleEntry()
	if len(ComputeHash(a)) != 64 {
		t.Fatalf("ComputeHash returned %q; expected 64 hex characters", ComputeHash(a))
	}
	b := tSampleEntry()
	b.Hash = "something else entirely"
	b.Details = map[string]string{"a": "1", "b": "2"}
	if ComputeHash(a) != ComputeHash(b) {
		t.Fatalf("hash depends on the Hash field or on map construction order")
	}
	c := tSampleEntry()
	c.Details = nil
	d := tSampleEntry()
	d.Details = map[string]string{}
	if ComputeHash(c) != ComputeHash(d) {
		t.Fatalf("nil and empty Details hash differently; a JSON round trip (omitempty) would then break verification")
	}
}

func TestCanonical_FieldBoundariesAreUnambiguous(t *testing.T) {
	base := tSampleEntry()
	base.Details = nil
	mk := func(f func(*Entry)) Entry { e := base; f(&e); return e }
	pairs := []struct {
		name string
		a, b Entry
	}{
		{"actor/action", mk(func(e *Entry) { e.Actor, e.Action = "ab", "c" }), mk(func(e *Entry) { e.Actor, e.Action = "a", "bc" })},
		{"tenant/actor", mk(func(e *Entry) { e.Tenant, e.Actor = "cu", "xa" }), mk(func(e *Entry) { e.Tenant, e.Actor = "cux", "a" })},
		{"resource/outcome", mk(func(e *Entry) { e.Resource, e.Outcome = "r", "successs" }), mk(func(e *Entry) { e.Resource, e.Outcome = "rs", "success" })},
		{"details key/value", mk(func(e *Entry) { e.Details = map[string]string{"a": "bc"} }), mk(func(e *Entry) { e.Details = map[string]string{"ab": "c"} })},
		{"details split", mk(func(e *Entry) { e.Details = map[string]string{"a": "1", "b": "2"} }), mk(func(e *Entry) { e.Details = map[string]string{"a": "1b", "": "2"} })},
		{"empty vs missing field", mk(func(e *Entry) { e.Resource = ""; e.Action = "xy" }), mk(func(e *Entry) { e.Resource = "x"; e.Action = "y" })},
	}
	for _, p := range pairs {
		if ComputeHash(p.a) == ComputeHash(p.b) {
			t.Errorf("%s: two different entries hash identically; without length prefixes an attacker can shift bytes between fields and keep the hash", p.name)
		}
	}
}

func TestHash_CoversEveryField(t *testing.T) {
	base := tSampleEntry()
	baseHash := ComputeHash(base)
	mutations := map[string]func(*Entry){
		"Seq":           func(e *Entry) { e.Seq++ },
		"Time":          func(e *Entry) { e.Time = e.Time.Add(time.Nanosecond) },
		"Tenant":        func(e *Entry) { e.Tenant = "cu-beta" },
		"Actor":         func(e *Entry) { e.Actor = "member:43" },
		"Action":        func(e *Entry) { e.Action = "transfer.delete" },
		"Resource":      func(e *Entry) { e.Resource = "acct:10" },
		"Outcome":       func(e *Entry) { e.Outcome = "denied" },
		"PrevHash":      func(e *Entry) { e.PrevHash = strings.Repeat("cd", 32) },
		"Details value": func(e *Entry) { e.Details = map[string]string{"a": "1", "b": "3"} },
		"Details key":   func(e *Entry) { e.Details = map[string]string{"a": "1", "c": "2"} },
		"Details extra": func(e *Entry) { e.Details = map[string]string{"a": "1", "b": "2", "c": "3"} },
	}
	for name, mutate := range mutations {
		e := tSampleEntry()
		mutate(&e)
		if ComputeHash(e) == baseHash {
			t.Errorf("changing %s does not change the hash; that field is not tamper-evident", name)
		}
	}
}

// ---------- Append and reading ----------

func TestAppend_AssignsSequenceTimeAndChainsEntries(t *testing.T) {
	_, es := tLog(t, 5)
	clk := tNewClock()
	for i, e := range es {
		if e.Seq != uint64(i)+1 {
			t.Errorf("entry %d has Seq %d; expected %d", i, e.Seq, i+1)
		}
		wantPrev := GenesisHash
		if i > 0 {
			wantPrev = es[i-1].Hash
		}
		if e.PrevHash != wantPrev {
			t.Errorf("entry %d PrevHash = %s; expected the predecessor's hash %s", i, e.PrevHash, wantPrev)
		}
		if e.Hash != ComputeHash(e) {
			t.Errorf("entry %d Hash does not match ComputeHash of its content", i)
		}
		if want := clk.Now(); !e.Time.Equal(want) || e.Time.Location() != time.UTC {
			t.Errorf("entry %d Time = %v; expected %v in UTC from the injected clock", i, e.Time, want)
		}
	}
	if len(es[0].PrevHash) != 64 || es[0].PrevHash != strings.Repeat("0", 64) {
		t.Errorf("first entry must chain to GenesisHash (64 zeros), got %q", es[0].PrevHash)
	}
}

func TestAppend_RejectsIncompleteEntriesWithoutBurningASequenceNumber(t *testing.T) {
	l := New(Config{Now: tNewClock().Now})
	bad := []Input{
		{Action: "a", Outcome: "success"},
		{Actor: "x", Outcome: "success"},
		{Actor: "x", Action: "a"},
	}
	for _, in := range bad {
		if _, err := l.Append(in); !errors.Is(err, ErrInvalidEntry) {
			t.Errorf("Append(%+v) = %v; expected ErrInvalidEntry", in, err)
		}
	}
	if l.Len() != 0 {
		t.Fatalf("rejected appends left %d entries in the log", l.Len())
	}
	e, err := l.Append(Input{Actor: "x", Action: "a", Outcome: "denied"})
	if err != nil || e.Seq != 1 || e.PrevHash != GenesisHash {
		t.Fatalf("first valid Append after rejections: %+v err=%v; expected Seq 1 chained to genesis (a gap would look like deletion)", e, err)
	}
}

func TestLog_CopiesInAndCopiesOut(t *testing.T) {
	l := New(Config{Now: tNewClock().Now})
	details := map[string]string{"k": "v"}
	in := Input{Actor: "x", Action: "a", Outcome: "success", Details: details}
	ret, err := l.Append(in)
	if err != nil {
		t.Fatal(err)
	}
	details["k"] = "changed-after-append"
	ret.Details["k"] = "changed-via-return-value"
	ret.Actor = "mallory"
	es := l.Entries()
	es[0].Details["k"] = "changed-via-entries"
	es[0].Hash = "forged"
	since := l.Since(0)
	since[0].Details["k"] = "changed-via-since"
	if err := Verify(l.Entries(), Genesis, nil); err != nil {
		t.Fatalf("log no longer verifies after callers mutated what they were given: %v; the log must copy Details on the way in and return deep copies on the way out (an audit log whose internals are reachable is not append-only)", err)
	}
	if got := l.Entries()[0].Details["k"]; got != "v" {
		t.Fatalf("stored detail is %q; expected the original value", got)
	}
}

func TestLog_HeadAndSince(t *testing.T) {
	l := New(Config{Now: tNewClock().Now})
	if h := l.Head(); h != Genesis {
		t.Fatalf("empty log Head = %+v; expected Genesis", h)
	}
	if got := l.Since(0); len(got) != 0 {
		t.Fatalf("Since(0) on an empty log = %d entries", len(got))
	}
	for i := 1; i <= 10; i++ {
		l.Append(tInput(i))
	}
	es := l.Entries()
	if h := l.Head(); h != tCheckpointOf(es[9]) || h.Seq != 10 {
		t.Fatalf("Head = %+v; expected seq 10 and the last entry's hash", h)
	}
	if got := l.Since(4); len(got) != 6 || got[0].Seq != 5 || got[5].Seq != 10 {
		t.Fatalf("Since(4) returned %d entries starting at %v; expected seq 5..10", len(got), got)
	}
	if got := l.Since(10); len(got) != 0 {
		t.Fatalf("Since(10) = %d entries; expected none", len(got))
	}
	if got := l.Since(99); len(got) != 0 {
		t.Fatalf("Since(99) = %d entries; expected none", len(got))
	}
}

// ---------- Verify: intact logs ----------

func TestVerify_AcceptsIntactLogsAndSegments(t *testing.T) {
	if err := Verify(nil, Genesis, nil); err != nil {
		t.Fatalf("empty log: %v", err)
	}
	l, es := tLog(t, 100)
	if err := Verify(es, Genesis, nil); err != nil {
		t.Fatalf("intact 100-entry log: %v", err)
	}
	head := l.Head()
	if err := Verify(es, Genesis, &head); err != nil {
		t.Fatalf("intact log with its own head as anchor: %v", err)
	}
	mid := tCheckpointOf(es[49])
	if err := Verify(l.Since(50), mid, &head); err != nil {
		t.Fatalf("segment 51..100 starting from checkpoint 50: %v; archived segments must be verifiable from the checkpoint before them", err)
	}
	wrong := Checkpoint{Seq: 50, Hash: es[48].Hash}
	if err := Verify(l.Since(50), wrong, nil); !errors.Is(err, ErrBrokenLink) {
		t.Fatalf("segment with the wrong starting checkpoint: %v; expected ErrBrokenLink", err)
	}
}

func TestVerify_EmptyLogAgainstNonGenesisAnchorIsTruncation(t *testing.T) {
	_, es := tLog(t, 3)
	anchor := tCheckpointOf(es[2])
	err := Verify(nil, Genesis, &anchor)
	if !errors.Is(err, ErrTruncated) {
		t.Fatalf("an emptied log checked against an anchor at seq 3: %v; expected ErrTruncated", err)
	}
}

// ---------- Verify: tampering, one check at a time ----------

func TestVerify_DetectsTamperingAndNamesTheCheckThatCaughtIt(t *testing.T) {
	const n = 10
	l, orig := tLog(t, n)
	head := l.Head()
	oldAnchor7 := tCheckpointOf(orig[6])

	type tc struct {
		name   string
		mutate func(es []Entry) []Entry
		anchor *Checkpoint
		want   error // nil = tampering goes undetected
		index  int
	}
	cases := []tc{
		{"edit a field, leave the hash", func(es []Entry) []Entry { es[4].Actor = "mallory"; return es }, nil, ErrModified, 4},
		{"edit a detail, leave the hash", func(es []Entry) []Entry { es[4].Details["amount_cents"] = "1"; return es }, nil, ErrModified, 4},
		{"edit the timestamp, leave the hash", func(es []Entry) []Entry { es[4].Time = es[4].Time.Add(time.Hour); return es }, nil, ErrModified, 4},
		{"edit the hash itself", func(es []Entry) []Entry { es[4].Hash = es[3].Hash; return es }, nil, ErrModified, 4},
		{"edit a middle entry and recompute its hash", func(es []Entry) []Entry {
			es[4].Actor = "mallory"
			es[4].Hash = ComputeHash(es[4])
			return es
		}, nil, ErrBrokenLink, 5},
		{"delete a middle entry", func(es []Entry) []Entry { return append(es[:4], es[5:]...) }, nil, ErrBrokenLink, 4},
		{"delete the first entry", func(es []Entry) []Entry { return es[1:] }, nil, ErrBrokenLink, 0},
		{"swap two adjacent entries", func(es []Entry) []Entry { es[3], es[4] = es[4], es[3]; return es }, nil, ErrBrokenLink, 3},
		{"replay (duplicate) an entry", func(es []Entry) []Entry {
			out := append([]Entry{}, es[:5]...)
			out = append(out, es[4])
			return append(out, es[5:]...)
		}, nil, ErrBrokenLink, 5},
		{"delete a middle entry and recompute every later link, keeping Seq", func(es []Entry) []Entry {
			es = append(es[:4], es[5:]...)
			tRechain(es, 4, false)
			return es
		}, nil, ErrSeqGap, 4},

		// What a hash chain alone cannot see, and what an anchor adds.
		{"truncate the tail (no anchor)", func(es []Entry) []Entry { return es[:7] }, nil, nil, 0},
		{"truncate the tail (anchored)", func(es []Entry) []Entry { return es[:7] }, &head, ErrTruncated, -1},
		{"rewrite the last entry and rehash it (no anchor)", func(es []Entry) []Entry {
			es[n-1].Outcome = "denied"
			es[n-1].Hash = ComputeHash(es[n-1])
			return es
		}, nil, nil, 0},
		{"rewrite the last entry and rehash it (anchored)", func(es []Entry) []Entry {
			es[n-1].Outcome = "denied"
			es[n-1].Hash = ComputeHash(es[n-1])
			return es
		}, &head, ErrAnchorMismatch, -1},
		{"delete + rechain + renumber (no anchor)", func(es []Entry) []Entry {
			es = append(es[:4], es[5:]...)
			tRechain(es, 4, true)
			return es
		}, nil, nil, 0},
		{"delete + rechain + renumber (anchored at the old head)", func(es []Entry) []Entry {
			es = append(es[:4], es[5:]...)
			tRechain(es, 4, true)
			return es
		}, &head, ErrTruncated, -1},
		{"delete + rechain + renumber (anchored at an older seq 7)", func(es []Entry) []Entry {
			es = append(es[:4], es[5:]...)
			tRechain(es, 4, true)
			return es
		}, &oldAnchor7, ErrAnchorMismatch, -1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			es := c.mutate(tCopy(orig))
			err := Verify(es, Genesis, c.anchor)
			if c.want == nil {
				if err != nil {
					t.Fatalf("Verify = %v; this tampering is invisible to the chain alone, so Verify must return nil (and the unit explains why that is the point of anchoring)", err)
				}
				return
			}
			if !errors.Is(err, c.want) {
				t.Fatalf("Verify = %v; expected %v", err, c.want)
			}
			var ve *VerifyError
			if !errors.As(err, &ve) {
				t.Fatalf("error %T is not a *VerifyError; callers need to know where the chain broke", err)
			}
			if ve.Index != c.index {
				t.Fatalf("VerifyError.Index = %d; expected %d (first offending entry)", ve.Index, c.index)
			}
		})
	}
}

func TestVerify_AnchorBeforeTheVerifiedSegmentCannotBeChecked(t *testing.T) {
	l, es := tLog(t, 10)
	old := tCheckpointOf(es[2])
	err := Verify(l.Since(5), tCheckpointOf(es[4]), &old)
	if !errors.Is(err, ErrAnchorNotCovered) {
		t.Fatalf("anchor at seq 3 for a segment starting after seq 5: %v; expected ErrAnchorNotCovered", err)
	}
	// An anchor exactly at the segment's start is checked against the start checkpoint.
	start := tCheckpointOf(es[4])
	if err := Verify(l.Since(5), start, &start); err != nil {
		t.Fatalf("anchor equal to the start checkpoint: %v", err)
	}
	bad := Checkpoint{Seq: start.Seq, Hash: strings.Repeat("f", 64)}
	if err := Verify(l.Since(5), start, &bad); !errors.Is(err, ErrAnchorMismatch) {
		t.Fatalf("anchor at the start seq with a different hash: %v; expected ErrAnchorMismatch", err)
	}
}

func TestVerifyError_MessageSaysWhatAndWhere(t *testing.T) {
	_, es := tLog(t, 8)
	es[5].Actor = "mallory"
	err := Verify(es, Genesis, nil)
	if err == nil {
		t.Fatal("tampered log verified")
	}
	msg := err.Error()
	if !strings.Contains(msg, "6") || !strings.Contains(msg, "hash") {
		t.Fatalf("error message %q should name the offending sequence number (6) and what failed; an on-call engineer will read this at 3am", msg)
	}
}

// ---------- the anchoring story, end to end ----------

func TestAnchoredHeadSurvivesRoundTripAndCatchesRewrite(t *testing.T) {
	l := New(Config{Now: tNewClock().Now})
	for i := 1; i <= 10; i++ {
		l.Append(tInput(i))
	}
	published := l.Head().String() // written to a place the log operator cannot edit
	for i := 11; i <= 20; i++ {
		l.Append(tInput(i))
	}
	anchor, err := ParseCheckpoint(published)
	if err != nil {
		t.Fatalf("ParseCheckpoint(%q): %v", published, err)
	}
	if err := Verify(l.Entries(), Genesis, &anchor); err != nil {
		t.Fatalf("honest log fails against its own earlier anchor: %v", err)
	}
	// The operator quietly rewrites entry 3 (before the anchor) and rechains everything.
	es := l.Entries()
	es[2].Details["amount_cents"] = "0"
	tRechain(es, 2, false)
	if err := Verify(es, Genesis, nil); err != nil {
		t.Fatalf("a fully rechained log should look internally consistent, got %v", err)
	}
	if err := Verify(es, Genesis, &anchor); !errors.Is(err, ErrAnchorMismatch) {
		t.Fatalf("rewrite of history before the anchor: %v; expected ErrAnchorMismatch", err)
	}
}

func TestCheckpoint_StringAndParse(t *testing.T) {
	hash := strings.Repeat("0123456789abcdef", 4)
	c := Checkpoint{Seq: 1234567, Hash: hash}
	if c.String() != "1234567:"+hash {
		t.Fatalf("String() = %q", c.String())
	}
	back, err := ParseCheckpoint(c.String())
	if err != nil || back != c {
		t.Fatalf("round trip: %+v, %v", back, err)
	}
	if g, err := ParseCheckpoint(Genesis.String()); err != nil || g != Genesis {
		t.Fatalf("Genesis round trip: %+v, %v", g, err)
	}
	for _, bad := range []string{
		"", "5", ":" + hash, "x:" + hash, "-1:" + hash, "5:" + hash[:63], "5:" + hash + "0",
		"5:" + strings.ToUpper(hash), "5:" + strings.Repeat("g", 64), " 5:" + hash, "5:" + hash + " ",
		"99999999999999999999999:" + hash,
	} {
		if _, err := ParseCheckpoint(bad); !errors.Is(err, ErrBadCheckpoint) {
			t.Errorf("ParseCheckpoint(%q) = %v; expected ErrBadCheckpoint", bad, err)
		}
	}
}

func TestVerify_SurvivesJSONRoundTrip(t *testing.T) {
	l, _ := tLog(t, 20)
	raw, err := json.Marshal(l.Entries())
	if err != nil {
		t.Fatal(err)
	}
	var back []Entry
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	head := l.Head()
	if err := Verify(back, Genesis, &head); err != nil {
		t.Fatalf("exported-and-reimported log fails verification: %v; the canonical form must not depend on the in-memory representation (time zone, nil vs empty map, ...)", err)
	}
}

// ---------- concurrency ----------

func TestAppend_ConcurrentAppendsFormOneTotalOrder(t *testing.T) {
	l := New(Config{Now: tNewClock().Now})
	const goroutines, each = 16, 100
	var wg sync.WaitGroup
	seqs := make([][]uint64, goroutines)
	stop := make(chan struct{})
	var readers sync.WaitGroup
	for r := 0; r < 2; r++ {
		readers.Add(1)
		go func() {
			defer readers.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				es := l.Entries()
				if err := Verify(es, Genesis, nil); err != nil {
					t.Errorf("a concurrent reader saw an inconsistent chain: %v", err)
					return
				}
				_ = l.Head()
			}
		}()
	}
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < each; i++ {
				e, err := l.Append(Input{Tenant: "cu-alpha", Actor: fmt.Sprintf("g%d", g), Action: "tick", Outcome: "success", Details: map[string]string{"i": strconv.Itoa(i)}})
				if err != nil {
					t.Errorf("Append: %v", err)
					return
				}
				seqs[g] = append(seqs[g], e.Seq)
			}
		}(g)
	}
	tWait(t, &wg, 30*time.Second, "concurrent appends")
	close(stop)
	tWait(t, &readers, 30*time.Second, "concurrent readers")

	if l.Len() != goroutines*each {
		t.Fatalf("Len = %d; expected %d", l.Len(), goroutines*each)
	}
	if err := Verify(l.Entries(), Genesis, nil); err != nil {
		t.Fatalf("log built by concurrent appends does not verify: %v", err)
	}
	seen := map[uint64]bool{}
	for g, s := range seqs {
		for i, q := range s {
			if seen[q] {
				t.Fatalf("sequence number %d was handed out twice", q)
			}
			seen[q] = true
			if i > 0 && q <= s[i-1] {
				t.Fatalf("goroutine %d: its own appends are out of order (%d after %d); a caller's causal order must be preserved", g, q, s[i-1])
			}
		}
	}
	if len(seen) != goroutines*each {
		t.Fatalf("%d distinct sequence numbers; expected %d", len(seen), goroutines*each)
	}
}

// ---------- per-tenant chains ----------

func TestTenantLogs_EachTenantHasItsOwnIndependentChain(t *testing.T) {
	tl := NewTenantLogs(Config{Now: tNewClock().Now})
	order := []string{"cu-a", "cu-b", "cu-a", "cu-b", "cu-a"}
	for i, tn := range order {
		if _, err := tl.Append(Input{Tenant: tn, Actor: "x", Action: "act", Outcome: "success", Resource: strconv.Itoa(i)}); err != nil {
			t.Fatal(err)
		}
	}
	a, b := tl.Entries("cu-a"), tl.Entries("cu-b")
	if len(a) != 3 || len(b) != 2 {
		t.Fatalf("chains have %d and %d entries; expected 3 and 2", len(a), len(b))
	}
	for i, e := range a {
		if e.Tenant != "cu-a" || e.Seq != uint64(i)+1 {
			t.Fatalf("cu-a entry %d: %+v; each tenant's sequence must start at 1 and contain only its own entries", i, e)
		}
	}
	if b[0].PrevHash != GenesisHash || b[0].Seq != 1 {
		t.Fatalf("cu-b's first entry %+v is not chained to genesis; tenants must not share a chain", b[0])
	}
	for _, tn := range []string{"cu-a", "cu-b"} {
		h := tl.Head(tn)
		if err := Verify(tl.Entries(tn), Genesis, &h); err != nil {
			t.Fatalf("%s: %v", tn, err)
		}
	}
	// Tampering with one tenant's chain does not disturb the other's verification.
	bt := tl.Entries("cu-b")
	bt[0].Actor = "mallory"
	if Verify(bt, Genesis, nil) == nil {
		t.Fatalf("tampered cu-b chain verified")
	}
	if err := Verify(tl.Entries("cu-a"), Genesis, nil); err != nil {
		t.Fatalf("cu-a affected by a copy of cu-b being modified: %v", err)
	}
	if h := tl.Head("nobody"); h != Genesis {
		t.Fatalf("Head of an unknown tenant = %+v; expected Genesis", h)
	}
	if es := tl.Entries("nobody"); len(es) != 0 {
		t.Fatalf("Entries of an unknown tenant = %d", len(es))
	}
	if _, err := tl.Append(Input{Actor: "x", Action: "act", Outcome: "success"}); !errors.Is(err, ErrInvalidEntry) {
		t.Fatalf("Append without a tenant = %v; expected ErrInvalidEntry", err)
	}
	if hs := tl.Heads(); len(hs) != 2 || hs["cu-a"] != tl.Head("cu-a") || hs["cu-b"] != tl.Head("cu-b") {
		t.Fatalf("Heads() = %+v", hs)
	}
}

func TestTenantLogs_ConcurrentAppendsAcrossTenants(t *testing.T) {
	tl := NewTenantLogs(Config{Now: tNewClock().Now})
	const tenants, goroutines, each = 8, 4, 50
	var wg sync.WaitGroup
	for ti := 0; ti < tenants; ti++ {
		for g := 0; g < goroutines; g++ {
			wg.Add(1)
			go func(ti, g int) {
				defer wg.Done()
				for i := 0; i < each; i++ {
					if _, err := tl.Append(Input{Tenant: fmt.Sprintf("cu-%d", ti), Actor: fmt.Sprint("g", g), Action: "tick", Outcome: "success"}); err != nil {
						t.Errorf("Append: %v", err)
						return
					}
				}
			}(ti, g)
		}
	}
	tWait(t, &wg, 30*time.Second, "concurrent per-tenant appends")
	for ti := 0; ti < tenants; ti++ {
		tn := fmt.Sprintf("cu-%d", ti)
		h := tl.Head(tn)
		if h.Seq != goroutines*each {
			t.Fatalf("%s head seq = %d; expected %d", tn, h.Seq, goroutines*each)
		}
		if err := Verify(tl.Entries(tn), Genesis, &h); err != nil {
			t.Fatalf("%s: %v", tn, err)
		}
	}
}

func TestCombineHeads_CommitsToEveryTenantSeqAndHash(t *testing.T) {
	h1 := strings.Repeat("a1", 32)
	h2 := strings.Repeat("b2", 32)
	base := map[string]Checkpoint{"cu-a": {3, h1}, "cu-b": {5, h2}}
	d := CombineHeads(base)
	if len(d) != 64 {
		t.Fatalf("digest %q is not 64 hex characters", d)
	}
	if again := CombineHeads(map[string]Checkpoint{"cu-b": {5, h2}, "cu-a": {3, h1}}); again != d {
		t.Fatalf("digest depends on map construction order")
	}
	variants := map[string]map[string]Checkpoint{
		"seq changed":          {"cu-a": {4, h1}, "cu-b": {5, h2}},
		"hash changed":         {"cu-a": {3, h2}, "cu-b": {5, h2}},
		"tenants swapped":      {"cu-a": {5, h2}, "cu-b": {3, h1}},
		"tenant dropped":       {"cu-a": {3, h1}},
		"tenant renamed":       {"cu-a": {3, h1}, "cu-c": {5, h2}},
		"tenant added":         {"cu-a": {3, h1}, "cu-b": {5, h2}, "cu-c": {1, h1}},
		"names shifted bytes":  {"cu-": {3, h1}, "acu-b": {5, h2}},
		"empty":                {},
		"nil":                  nil,
		"single, others empty": {"cu-a": {3, h1}, "": {0, GenesisHash}},
	}
	for name, v := range variants {
		if CombineHeads(v) == d {
			t.Errorf("%s: digest unchanged; an anchor that cannot see this change cannot protect it", name)
		}
	}
}
