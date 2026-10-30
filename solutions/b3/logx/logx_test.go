package logx

import (
	"errors"
	"fmt"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const testTimeout = 5 * time.Second

// within fails the test if fn does not return in time, turning a hang in your
// implementation into a message.
func within(t *testing.T, what string, fn func()) {
	t.Helper()
	done := make(chan struct{})
	go func() { defer close(done); fn() }()
	select {
	case <-done:
	case <-time.After(testTimeout):
		t.Fatalf("%s did not finish within %v (blocked on a lock, a full queue or a sink that never returns?)", what, testTimeout)
	}
}

// recSink records every entry it is given.
type recSink struct {
	mu      sync.Mutex
	entries []Entry
	err     error
}

func (r *recSink) Write(e Entry) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.entries = append(r.entries, e)
	return r.err
}

func (r *recSink) all() []Entry {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]Entry(nil), r.entries...)
}

func (r *recSink) msgs() []string {
	var out []string
	for _, e := range r.all() {
		out = append(out, e.Msg)
	}
	return out
}

// fakeClock returns a time that moves forward one second per call and counts calls.
type fakeClock struct {
	mu    sync.Mutex
	t     time.Time
	calls int
}

func newFakeClock() *fakeClock { return &fakeClock{t: time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)} }

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls++
	t := c.t
	c.t = c.t.Add(time.Second)
	return t
}

func (c *fakeClock) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
}

func fieldsOf(e Entry) string {
	var parts []string
	for _, f := range e.Fields {
		parts = append(parts, fmt.Sprintf("%s=%v", f.Key, f.Value))
	}
	return strings.Join(parts, ",")
}

// ---------------------------------------------------------------- Stage 1

func TestStage1_LoggerBuildsTheEntry(t *testing.T) {
	clk := newFakeClock()
	rec := &recSink{}
	l := New(Options{Now: clk.Now}, rec)
	l.Warn("disk low", F("path", "/var"), F("pct", 91))

	got := rec.all()
	if len(got) != 1 {
		t.Fatalf("sink received %d entries, want 1", len(got))
	}
	e := got[0]
	if e.Level != Warn || e.Msg != "disk low" {
		t.Fatalf("entry = level %v msg %q, want WARN %q", e.Level, e.Msg, "disk low")
	}
	if want := time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC); !e.Time.Equal(want) {
		t.Fatalf("entry time = %v, want %v from the injected clock", e.Time, want)
	}
	if want := []Field{F("path", "/var"), F("pct", 91)}; !reflect.DeepEqual(e.Fields, want) {
		t.Fatalf("fields = %v, want %v", e.Fields, want)
	}
	for _, lv := range []Level{Debug, Info, Warn, Error} { // every helper maps to its own level
		rec2 := &recSink{}
		l2 := New(Options{}, rec2)
		switch lv {
		case Debug:
			l2.Debug("m")
		case Info:
			l2.Info("m")
		case Warn:
			l2.Warn("m")
		case Error:
			l2.Error("m")
		}
		if es := rec2.all(); len(es) != 1 || es[0].Level != lv {
			t.Fatalf("helper for %v produced %+v", lv, es)
		}
	}
}

func TestStage1_MinimumLevelFilters(t *testing.T) {
	rec := &recSink{}
	l := New(Options{Level: Warn}, rec)
	l.Debug("d")
	l.Info("i")
	l.Warn("w")
	l.Error("e")
	if got, want := rec.msgs(), []string{"w", "e"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("with Level=Warn the sink saw %q, want %q", got, want)
	}
	if l.Enabled(Info) || !l.Enabled(Warn) || !l.Enabled(Error) {
		t.Fatal("Enabled disagrees with the minimum level")
	}

	all := &recSink{}
	New(Options{}, all).Debug("zero value of Options.Level logs everything")
	if len(all.all()) != 1 {
		t.Fatal("the zero Options.Level (Debug) must log everything")
	}
}

func TestStage1_DisabledLevelIsCheap(t *testing.T) {
	clk := newFakeClock()
	rec := &recSink{}
	l := New(Options{Level: Error, Now: clk.Now}, rec)
	for range 100 {
		l.Info("hot path", F("k", 1))
	}
	if clk.count() != 0 {
		t.Fatalf("clock was read %d times for entries below the minimum level; check the level before doing any work", clk.count())
	}
	if len(rec.all()) != 0 {
		t.Fatal("below-level entries reached the sink")
	}
	l.Error("enabled")
	if len(rec.all()) != 1 || clk.count() != 1 {
		t.Fatalf("an enabled entry: sink saw %d entries and the clock was read %d times, want 1 and 1", len(rec.all()), clk.count())
	}
}

func TestStage1_WithAddsFieldsAndNeverLeaksIntoSiblings(t *testing.T) {
	rec := &recSink{}
	root := New(Options{}, rec)
	// Build a chain so that a naive append(parent.fields, ...) has spare capacity.
	base := root.With(F("a", 1)).With(F("b", 2)).With(F("c", 3))
	s1 := base.With(F("d", 4))
	s2 := base.With(F("e", 5))

	s1.Info("one", F("x", 9))
	s2.Info("two")
	base.Info("three")
	root.Info("four")

	want := []string{"a=1,b=2,c=3,d=4,x=9", "a=1,b=2,c=3,e=5", "a=1,b=2,c=3", ""}
	if n := len(rec.all()); n != len(want) {
		t.Fatalf("sink received %d entries, want %d", n, len(want))
	}
	for i, e := range rec.all() {
		if got := fieldsOf(e); got != want[i] {
			t.Fatalf("entry %q has fields %q, want %q: With must copy, not append into a shared backing array", e.Msg, got, want[i])
		}
	}
}

func TestStage1_EntriesDoNotAliasCallerOrLoggerSlices(t *testing.T) {
	rec := &recSink{}
	l := New(Options{}, rec)

	callSite := []Field{F("k", "original")}
	l.Info("a", callSite...) // no With fields: the easiest place to store the caller's slice by mistake
	callSite[0] = F("k", "changed after the call")

	child := l.With(F("w", 1))
	child.Info("b")
	child.Info("c")
	es := rec.all()
	if len(es) != 3 || len(es[1].Fields) != 1 {
		t.Fatalf("sink received %d entries (%+v), want 3 with one field on the second", len(es), es)
	}
	es[1].Fields[0] = F("w", "a sink scribbled on its copy") // an entry's slice is private to that entry

	if got := fieldsOf(es[0]); got != "k=original" {
		t.Fatalf("first entry's fields became %q: the Logger kept the caller's variadic slice", got)
	}
	if got := fieldsOf(es[2]); got != "w=1" {
		t.Fatalf("a later entry's fields became %q: entries from one Logger share a Fields slice", got)
	}
}

func TestStage1_LevelFilterDecorator(t *testing.T) {
	rec := &recSink{}
	s := LevelFilter(Warn, rec)
	for _, lv := range []Level{Debug, Info, Warn, Error} {
		if err := s.Write(Entry{Level: lv, Msg: lv.String()}); err != nil {
			t.Fatalf("Write = %v", err)
		}
	}
	if got, want := rec.msgs(), []string{"WARN", "ERROR"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("LevelFilter(Warn) forwarded %q, want %q", got, want)
	}
}

func TestStage1_MultiFansOutAndOneFailureDoesNotStarveTheOthers(t *testing.T) {
	errA, errC := errors.New("sink A down"), errors.New("sink C down")
	a, b, c := &recSink{err: errA}, &recSink{}, &recSink{err: errC}
	m := Multi(a, nil, b, c)
	err := m.Write(Entry{Msg: "x"})
	for name, r := range map[string]*recSink{"a": a, "b": b, "c": c} {
		if len(r.all()) != 1 {
			t.Fatalf("sink %s received %d entries, want 1: a failing sink must not stop the fan-out", name, len(r.all()))
		}
	}
	if !errors.Is(err, errA) || !errors.Is(err, errC) {
		t.Fatalf("Multi returned %v, want an error that satisfies errors.Is for both failures", err)
	}
	if err := Multi(b).Write(Entry{Msg: "y"}); err != nil {
		t.Fatalf("Multi of a healthy sink returned %v", err)
	}
	if err := Multi().Write(Entry{}); err != nil {
		t.Fatalf("Multi of nothing returned %v", err)
	}
}

func TestStage1_SinkErrorsGoToOnErrorAndNeverToTheCaller(t *testing.T) {
	boom := errors.New("boom")
	var got []error
	l := New(Options{OnError: func(err error) { got = append(got, err) }}, &recSink{err: boom})
	l.Info("x")
	l.Info("y")
	if len(got) != 2 || !errors.Is(got[0], boom) {
		t.Fatalf("OnError saw %v, want the sink's error twice", got)
	}
	// No OnError, no sink: neither may panic.
	New(Options{}, &recSink{err: boom}).Info("ignored")
	New(Options{}, nil).With(F("k", 1)).Error("discarded")
}

func TestStage1_WriterSinkFormat(t *testing.T) {
	var sb strings.Builder
	s := NewWriterSink(&sb)
	ist := time.FixedZone("IST", 5*3600+1800)
	e := Entry{
		Time: time.Date(2026, 3, 4, 10, 36, 7, 0, ist), Level: Warn, Msg: "disk low",
		Fields: []Field{F("path", "/var/log"), F("pct", 91.5), F("note", "a b"), F("empty", ""), F("err", errors.New("x = y"))},
	}
	if err := s.Write(e); err != nil {
		t.Fatal(err)
	}
	want := `2026-03-04T05:06:07Z WARN disk low path=/var/log pct=91.5 note="a b" empty="" err="x = y"` + "\n"
	if sb.String() != want {
		t.Fatalf("line =\n%q\nwant\n%q", sb.String(), want)
	}
}

func TestStage1_WriterSinkOneEntryIsOneLine(t *testing.T) {
	var sb strings.Builder
	s := NewWriterSink(&sb)
	// A user-controlled string must not be able to forge a second log line.
	_ = s.Write(Entry{Time: time.Unix(0, 0), Level: Info, Msg: "login\n2026-01-01T00:00:00Z ERROR forged",
		Fields: []Field{F("user", "bob\nadmin=true")}})
	if n := strings.Count(sb.String(), "\n"); n != 1 || !strings.HasSuffix(sb.String(), "\n") {
		t.Fatalf("one entry produced %d newlines in %q, want exactly one, at the end", n, sb.String())
	}
}

// exclusiveWriter fails if two Write calls overlap, and records each call.
type exclusiveWriter struct {
	busy    atomic.Int32
	overlap atomic.Bool
	mu      sync.Mutex
	calls   []string
}

func (w *exclusiveWriter) Write(p []byte) (int, error) {
	if w.busy.Add(1) != 1 {
		w.overlap.Store(true)
	}
	runtime.Gosched()
	w.mu.Lock()
	w.calls = append(w.calls, string(p))
	w.mu.Unlock()
	w.busy.Add(-1)
	return len(p), nil
}

func TestStage1_WriterSinkLinesNeverInterleave(t *testing.T) {
	w := &exclusiveWriter{}
	l := New(Options{}, NewWriterSink(w))
	const goroutines, per = 32, 100
	var wg sync.WaitGroup
	for g := range goroutines {
		wg.Add(1)
		go func() {
			defer wg.Done()
			child := l.With(F("g", g))
			for n := range per {
				child.Info("tick", F("n", n))
			}
		}()
	}
	within(t, "concurrent logging", wg.Wait)
	if w.overlap.Load() {
		t.Fatal("two Write calls reached the underlying writer at the same time; the sink must serialise them (an os.File is, a bytes.Buffer is not)")
	}
	if len(w.calls) != goroutines*per {
		t.Fatalf("underlying writer got %d calls, want %d: one Write call per entry, so lines stay whole", len(w.calls), goroutines*per)
	}
	for _, c := range w.calls {
		if !strings.HasSuffix(c, "\n") || strings.Count(c, "\n") != 1 || !strings.Contains(c, " INFO tick g=") {
			t.Fatalf("malformed or split line %q", c)
		}
	}
}

func TestStage1_LoggersAreSafeForConcurrentUse(t *testing.T) {
	rec := &recSink{}
	root := New(Options{Level: Info}, LevelFilter(Info, Multi(rec)))
	var wg sync.WaitGroup
	for g := range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			child := root.With(F("g", g)) // derive concurrently from a shared parent
			for n := range 200 {
				child.With(F("n", n)).Info("m")
				child.Debug("filtered")
			}
		}()
	}
	within(t, "concurrent logging", wg.Wait)
	if got := len(rec.all()); got != 16*200 {
		t.Fatalf("sink saw %d entries, want %d", got, 16*200)
	}
}
