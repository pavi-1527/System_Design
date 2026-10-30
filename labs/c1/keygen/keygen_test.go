package keygen

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// ---------- helpers (prefixed with t to avoid clashing with your code) ----------

// tWait waits for wg, failing the test instead of hanging if it takes longer
// than d (e.g. a deadlock in the implementation under test).
func tWait(t *testing.T, wg *sync.WaitGroup, d time.Duration, what string) {
	t.Helper()
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(d):
		t.Fatalf("%s: still running after %v (deadlock, or a Reserve loop that never ends?)", what, d)
	}
}

func tAllocator(t *testing.T, cfg AllocatorConfig) *Allocator {
	t.Helper()
	a, err := NewAllocator(cfg)
	if err != nil {
		t.Fatalf("NewAllocator(%+v): %v", cfg, err)
	}
	return a
}

func tMixer(t *testing.T, bits uint, seed uint64) Mixer {
	t.Helper()
	m, err := NewMixer(bits, seed)
	if err != nil {
		t.Fatalf("NewMixer(%d, %d): %v", bits, seed, err)
	}
	return m
}

func tWithin(got, want, rel float64) bool {
	if want == 0 {
		return got == 0
	}
	return math.Abs(got-want)/math.Abs(want) <= rel
}

// tFlakyCounter wraps a Counter and makes the first n CompareAndSwap calls
// lose (return false) as if another node had just advanced the counter.
type tFlakyCounter struct {
	Counter
	lose atomic.Int64
}

func (c *tFlakyCounter) CompareAndSwap(ctx context.Context, old, new uint64) (bool, error) {
	if c.lose.Add(-1) >= 0 {
		return false, nil
	}
	return c.Counter.CompareAndSwap(ctx, old, new)
}

// tScriptedInserter answers Insert from a script of results; a nil error
// with false means "already taken".
type tScriptedInserter struct {
	mu     sync.Mutex
	script []bool
	err    error
	calls  int
}

func (s *tScriptedInserter) Insert(context.Context, string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	if s.err != nil {
		return false, s.err
	}
	if len(s.script) == 0 {
		return false, nil
	}
	r := s.script[0]
	s.script = s.script[1:]
	return r, nil
}

// ---------- codes ----------

func TestEncode_KnownValuesAndZeroPadding(t *testing.T) {
	cases := []struct {
		id     uint64
		length int
		want   string
	}{
		{0, 1, "0"}, {9, 1, "9"}, {10, 1, "a"}, {35, 1, "z"}, {36, 1, "A"}, {61, 1, "Z"},
		{62, 2, "10"}, {62, 4, "0010"}, {3843, 2, "ZZ"}, {3844, 3, "100"},
		{238327, 3, "ZZZ"}, {1, 7, "0000001"},
	}
	for _, c := range cases {
		got, err := Encode(c.id, c.length)
		if err != nil || got != c.want {
			t.Fatalf("Encode(%d, %d) = %q, %v; want %q (alphabet order is 0-9, a-z, A-Z, most significant digit first)", c.id, c.length, got, err, c.want)
		}
	}
	if _, err := Encode(3844, 2); !errors.Is(err, ErrBadArgument) {
		t.Fatalf("Encode(3844, 2) = %v, want ErrBadArgument: 62^2 = 3844 codes fit in two digits, 3844 itself does not", err)
	}
	if _, err := Encode(1, 0); !errors.Is(err, ErrBadArgument) {
		t.Fatalf("Encode(1, 0) = %v, want ErrBadArgument", err)
	}
}

func TestDecode_RoundTripsAndRejectsBadInput(t *testing.T) {
	for _, id := range []uint64{0, 1, 61, 62, 3843, 3844, 1 << 20, 1 << 40, DefaultLimit - 1, math.MaxUint64} {
		code, err := Encode(id, 11)
		if err != nil {
			t.Fatalf("Encode(%d, 11): %v (11 base-62 digits hold every uint64)", id, err)
		}
		back, err := Decode(code)
		if err != nil || back != id {
			t.Fatalf("Decode(Encode(%d)) = %d, %v", id, back, err)
		}
	}
	if v, err := Decode("0000001"); err != nil || v != 1 {
		t.Fatalf("Decode(\"0000001\") = %d, %v; leading zeros are part of a fixed-length code", v, err)
	}
	for _, bad := range []string{"", "a-b", "abc def", "é", strings.Repeat("Z", 12)} {
		if _, err := Decode(bad); !errors.Is(err, ErrInvalidCode) {
			t.Fatalf("Decode(%q) = %v, want ErrInvalidCode", bad, err)
		}
	}
}

func TestCapacityAndCollisionProbability(t *testing.T) {
	if got := Capacity(7); !tWithin(got, 3.5216146e12, 1e-6) {
		t.Fatalf("Capacity(7) = %g, want 62^7 ≈ 3.52e12", got)
	}
	if got := Capacity(0); got != 0 {
		t.Fatalf("Capacity(0) = %g, want 0", got)
	}
	// 6 billion links after five years at 100M/month.
	if got := CollisionProbability(7, 6e9); !tWithin(got, 1.7e-3, 0.05) {
		t.Fatalf("CollisionProbability(7, 6e9) = %g, want ≈ 0.0017: one retry per ~590 inserts at the end of life", got)
	}
	if got := CollisionProbability(6, 6e9); !tWithin(got, 0.1056, 0.05) {
		t.Fatalf("CollisionProbability(6, 6e9) = %g, want ≈ 0.106: with 6-character codes one insert in ten collides", got)
	}
	if got := CollisionProbability(1, 100); got != 1 {
		t.Fatalf("CollisionProbability(1, 100) = %g, want 1 (more codes in use than exist: cap at 1)", got)
	}
	if got := CollisionProbability(7, 0); got != 0 {
		t.Fatalf("CollisionProbability(7, 0) = %g, want 0", got)
	}
}

// ---------- mixer ----------

func TestMixer_IsABijectionOnTheWholeDomain(t *testing.T) {
	m := tMixer(t, 12, 42)
	seen := make(map[uint64]uint64, 4096)
	for id := uint64(0); id < 4096; id++ {
		c, err := m.Mix(id)
		if err != nil {
			t.Fatalf("Mix(%d): %v", id, err)
		}
		if c >= 4096 {
			t.Fatalf("Mix(%d) = %d, outside [0, 2^12)", id, c)
		}
		if prev, dup := seen[c]; dup {
			t.Fatalf("Mix(%d) = Mix(%d) = %d: two ids map to one code, so two links would share a short URL", id, prev, c)
		}
		seen[c] = id
		back, err := m.Unmix(c)
		if err != nil || back != id {
			t.Fatalf("Unmix(Mix(%d)) = %d, %v", id, back, err)
		}
	}
	if _, err := m.Mix(4096); !errors.Is(err, ErrBadArgument) {
		t.Fatalf("Mix(2^12) = %v, want ErrBadArgument", err)
	}
	if _, err := m.Unmix(4096); !errors.Is(err, ErrBadArgument) {
		t.Fatalf("Unmix(2^12) = %v, want ErrBadArgument", err)
	}
	for _, bits := range []uint{0, 65} {
		if _, err := NewMixer(bits, 1); !errors.Is(err, ErrBadArgument) {
			t.Fatalf("NewMixer(%d) = %v, want ErrBadArgument", bits, err)
		}
	}
	m64 := tMixer(t, 64, 7)
	for _, id := range []uint64{0, 1, 1 << 63, math.MaxUint64, 123456789} {
		c, _ := m64.Mix(id)
		if back, _ := m64.Unmix(c); back != id {
			t.Fatalf("64-bit mixer: Unmix(Mix(%d)) = %d", id, back)
		}
	}
}

func TestMixer_ConsecutiveIDsDoNotLookConsecutive(t *testing.T) {
	m := tMixer(t, 41, 2026)
	var prev uint64
	adjacent, sameHead := 0, 0
	for id := uint64(0); id < 2000; id++ {
		c, err := m.Mix(id)
		if err != nil {
			t.Fatalf("Mix(%d): %v", id, err)
		}
		if id > 0 {
			if c == prev+1 || c+1 == prev {
				adjacent++
			}
			a, _ := Encode(c, 7)
			b, _ := Encode(prev, 7)
			if a[:6] == b[:6] {
				sameHead++
			}
		}
		prev = c
	}
	if adjacent > 0 {
		t.Fatalf("%d of 1999 consecutive ids produced adjacent codes; a visitor could enumerate links by adding one", adjacent)
	}
	if sameHead > 20 {
		t.Fatalf("%d of 1999 consecutive ids shared the first six characters of their code; the permutation is not scattering ids", sameHead)
	}
}

func TestMixer_SameSeedSamePermutation(t *testing.T) {
	a, b, c := tMixer(t, 41, 99), tMixer(t, 41, 99), tMixer(t, 41, 100)
	differ := 0
	for id := uint64(0); id < 100; id++ {
		x, _ := a.Mix(id)
		y, _ := b.Mix(id)
		z, _ := c.Mix(id)
		if x != y {
			t.Fatalf("two mixers with the same seed disagree on id %d (%d vs %d); every API server must compute the same code", id, x, y)
		}
		if x != z {
			differ++
		}
	}
	if differ < 90 {
		t.Fatalf("mixers with different seeds agreed on %d of 100 ids; the seed is not being used", 100-differ)
	}
}

// ---------- counter and allocator ----------

func TestMemoryCounter_CompareAndSwapIsAtomic(t *testing.T) {
	c := NewMemoryCounter(0)
	var wins atomic.Int64
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < 200; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			ok, err := c.CompareAndSwap(context.Background(), 0, 1000)
			if err == nil && ok {
				wins.Add(1)
			}
		}()
	}
	close(start)
	tWait(t, &wg, 10*time.Second, "200 concurrent CompareAndSwap calls")
	if wins.Load() != 1 {
		t.Fatalf("%d of 200 concurrent CompareAndSwap(0, 1000) calls succeeded; exactly 1 must, or two nodes get the same range", wins.Load())
	}
	if v, _ := c.Load(context.Background()); v != 1000 {
		t.Fatalf("counter = %d after the swap, want 1000", v)
	}
}

func TestAllocator_ReservesDisjointRangesUnderContention(t *testing.T) {
	c := NewMemoryCounter(0)
	a := tAllocator(t, AllocatorConfig{Counter: c, Size: 10, MaxRetries: 1000})
	const workers, each = 16, 25
	var mu sync.Mutex
	var ranges []Range
	var wg sync.WaitGroup
	start := make(chan struct{})
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for i := 0; i < each; i++ {
				r, err := a.Reserve(context.Background())
				if err != nil {
					t.Errorf("Reserve: %v", err)
					return
				}
				mu.Lock()
				ranges = append(ranges, r)
				mu.Unlock()
			}
		}()
	}
	close(start)
	tWait(t, &wg, 20*time.Second, "concurrent Reserve calls")
	if len(ranges) != workers*each {
		t.Fatalf("got %d ranges, want %d", len(ranges), workers*each)
	}
	sort.Slice(ranges, func(i, j int) bool { return ranges[i].Start < ranges[j].Start })
	for i, r := range ranges {
		if r.Len() != 10 {
			t.Fatalf("range %v has %d ids, want 10", r, r.Len())
		}
		if r.Start != uint64(i)*10 {
			t.Fatalf("ranges are not disjoint and contiguous: range #%d is %v (want start %d); an overlap means duplicate short codes", i, r, i*10)
		}
	}
	if v, _ := c.Load(context.Background()); v != workers*each*10 {
		t.Fatalf("counter = %d, want %d", v, workers*each*10)
	}
}

func TestAllocator_RetriesLostSwapsAndGivesUp(t *testing.T) {
	flaky := &tFlakyCounter{Counter: NewMemoryCounter(0)}
	flaky.lose.Store(3)
	a := tAllocator(t, AllocatorConfig{Counter: flaky, Size: 100, MaxRetries: 5})
	r, err := a.Reserve(context.Background())
	if err != nil || r != (Range{0, 100}) {
		t.Fatalf("Reserve after 3 lost swaps = %v, %v; want [0,100) (a lost swap just means another node moved first: reload and retry)", r, err)
	}
	flaky.lose.Store(1 << 30)
	if _, err := a.Reserve(context.Background()); !errors.Is(err, ErrContended) {
		t.Fatalf("Reserve with every swap lost = %v, want ErrContended after MaxRetries", err)
	}
	if v, _ := flaky.Load(context.Background()); v != 100 {
		t.Fatalf("counter = %d after a failed Reserve, want 100: a failed reservation must not advance the counter", v)
	}
}

func TestAllocator_StopsAtTheKeyspaceLimit(t *testing.T) {
	c := NewMemoryCounter(0)
	a := tAllocator(t, AllocatorConfig{Counter: c, Size: 10, Limit: 25})
	want := []Range{{0, 10}, {10, 20}, {20, 25}}
	for i, w := range want {
		r, err := a.Reserve(context.Background())
		if err != nil || r != w {
			t.Fatalf("Reserve #%d = %v, %v; want %v (the last range is cut at the limit)", i+1, r, err, w)
		}
	}
	if _, err := a.Reserve(context.Background()); !errors.Is(err, ErrExhausted) {
		t.Fatalf("Reserve past the limit = %v, want ErrExhausted", err)
	}
	if v, _ := c.Load(context.Background()); v != 25 {
		t.Fatalf("counter = %d, want 25: an exhausted keyspace must not keep advancing", v)
	}
}

func TestAllocator_ValidatesConfigAndReturnsStoreErrors(t *testing.T) {
	if _, err := NewAllocator(AllocatorConfig{}); !errors.Is(err, ErrBadArgument) {
		t.Fatalf("NewAllocator without a counter = %v, want ErrBadArgument", err)
	}
	c := NewMemoryCounter(0)
	a := tAllocator(t, AllocatorConfig{Counter: c})
	if a.Size() != 1000 {
		t.Fatalf("default Size = %d, want 1000", a.Size())
	}
	down := errors.New("postgres: connection refused")
	c.Fail(down)
	if _, err := a.Reserve(context.Background()); !errors.Is(err, down) {
		t.Fatalf("Reserve with the counter down = %v, want the store's error (wrapped)", err)
	}
	c.Fail(nil)
	if r, err := a.Reserve(context.Background()); err != nil || r != (Range{0, 1000}) {
		t.Fatalf("Reserve after recovery = %v, %v; want [0,1000)", r, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := a.Reserve(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Reserve with a cancelled context = %v, want context.Canceled", err)
	}
}

// ---------- generator ----------

func TestGenerator_IDsAreUniqueAcrossNodesAndGoroutines(t *testing.T) {
	c := NewMemoryCounter(0)
	const nodes, goroutines, each, size = 4, 8, 500, 50
	gens := make([]*Generator, nodes)
	for i := range gens {
		gens[i] = NewGenerator(tAllocator(t, AllocatorConfig{Counter: c, Size: size, MaxRetries: 1000}))
	}
	ids := make(chan uint64, nodes*goroutines*each)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for _, g := range gens {
		for w := 0; w < goroutines; w++ {
			wg.Add(1)
			go func(g *Generator) {
				defer wg.Done()
				<-start
				for i := 0; i < each; i++ {
					id, err := g.Next(context.Background())
					if err != nil {
						t.Errorf("Next: %v", err)
						return
					}
					ids <- id
				}
			}(g)
		}
	}
	close(start)
	tWait(t, &wg, 30*time.Second, "concurrent Next calls")
	close(ids)
	seen := make(map[uint64]bool, nodes*goroutines*each)
	var max uint64
	for id := range ids {
		if seen[id] {
			t.Fatalf("id %d was handed out twice; two links would get the same short code", id)
		}
		seen[id] = true
		if id > max {
			max = id
		}
	}
	const total = nodes * goroutines * each
	if len(seen) != total {
		t.Fatalf("got %d ids, want %d", len(seen), total)
	}
	if max >= total+nodes*size {
		t.Fatalf("largest id is %d; with %d ids over %d nodes and ranges of %d it must stay below %d (ranges are being skipped, not consumed)", max, total, nodes, size, total+nodes*size)
	}
}

func TestGenerator_HotPathDoesNotTouchTheCounter(t *testing.T) {
	c := NewMemoryCounter(0)
	g := NewGenerator(tAllocator(t, AllocatorConfig{Counter: c, Size: 1000}))
	for i := 0; i < 1000; i++ {
		if _, err := g.Next(context.Background()); err != nil {
			t.Fatalf("Next #%d: %v", i, err)
		}
	}
	loads, swaps := c.Ops()
	if loads != 1 || swaps != 1 {
		t.Fatalf("1000 ids with a range of 1000 cost %d loads and %d swaps on the counter; want 1 and 1 (the store is touched once per range, never per id)", loads, swaps)
	}
	if g.Remaining() != 0 {
		t.Fatalf("Remaining = %d after consuming the whole range, want 0", g.Remaining())
	}
	if id, err := g.Next(context.Background()); err != nil || id != 1000 {
		t.Fatalf("1001st id = %d, %v; want 1000 from a second range", id, err)
	}
	if loads, swaps := c.Ops(); loads != 2 || swaps != 2 {
		t.Fatalf("after the second range: %d loads, %d swaps; want 2 and 2", loads, swaps)
	}
	if g.Remaining() != 999 {
		t.Fatalf("Remaining = %d, want 999", g.Remaining())
	}
}

func TestGenerator_ACrashedNodeLosesAtMostOneRange(t *testing.T) {
	c := NewMemoryCounter(0)
	a := tAllocator(t, AllocatorConfig{Counter: c, Size: 100})
	nodeA := NewGenerator(a)
	for i := 0; i < 3; i++ {
		if id, err := nodeA.Next(context.Background()); err != nil || id != uint64(i) {
			t.Fatalf("node A id #%d = %d, %v", i, id, err)
		}
	}
	// Node A crashes here with 97 unused ids in memory. They are gone for good.
	nodeB := NewGenerator(a)
	id, err := nodeB.Next(context.Background())
	if err != nil {
		t.Fatalf("node B Next: %v", err)
	}
	if id < 3 {
		t.Fatalf("node B received id %d, which node A already handed out: a restart must never reuse ids", id)
	}
	if id != 100 {
		t.Fatalf("node B received id %d; want 100: the crash wastes the rest of A's range (ids 3..99), no more and no less", id)
	}
}

func TestGenerator_CounterOutageIsAnErrorNotADuplicate(t *testing.T) {
	c := NewMemoryCounter(0)
	g := NewGenerator(tAllocator(t, AllocatorConfig{Counter: c, Size: 5}))
	for i := 0; i < 5; i++ {
		if _, err := g.Next(context.Background()); err != nil {
			t.Fatalf("Next #%d: %v", i, err)
		}
	}
	down := errors.New("redis: i/o timeout")
	c.Fail(down)
	for i := 0; i < 3; i++ {
		if id, err := g.Next(context.Background()); !errors.Is(err, down) {
			t.Fatalf("Next during the outage = %d, %v; want the store's error and no id (never invent an id when the counter is unreachable)", id, err)
		}
	}
	if g.Remaining() != 0 {
		t.Fatalf("Remaining = %d during the outage, want 0", g.Remaining())
	}
	c.Fail(nil)
	if id, err := g.Next(context.Background()); err != nil || id != 5 {
		t.Fatalf("Next after recovery = %d, %v; want 5 (the failed attempts consumed nothing)", id, err)
	}
}

func TestGenerator_ConcurrentRangeRolloverNeverDuplicates(t *testing.T) {
	c := NewMemoryCounter(1_000_000)
	g := NewGenerator(tAllocator(t, AllocatorConfig{Counter: c, Size: 7, MaxRetries: 1000}))
	const goroutines, each = 32, 100
	ids := make(chan uint64, goroutines*each)
	var wg sync.WaitGroup
	for w := 0; w < goroutines; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < each; i++ {
				id, err := g.Next(context.Background())
				if err != nil {
					t.Errorf("Next: %v", err)
					return
				}
				ids <- id
			}
		}()
	}
	tWait(t, &wg, 20*time.Second, "concurrent Next with tiny ranges")
	close(ids)
	seen := map[uint64]bool{}
	for id := range ids {
		if seen[id] {
			t.Fatalf("id %d handed out twice during range rollover", id)
		}
		if id < 1_000_000 {
			t.Fatalf("id %d is below the counter's starting value", id)
		}
		seen[id] = true
	}
	if len(seen) != goroutines*each {
		t.Fatalf("got %d distinct ids, want %d", len(seen), goroutines*each)
	}
}

// ---------- random codes ----------

func TestRandomCode_UsesTheAlphabetAndIsNotBiased(t *testing.T) {
	const n, length = 3000, 7
	counts := map[byte]int{}
	seen := map[string]bool{}
	for i := 0; i < n; i++ {
		code, err := RandomCode(nil, length)
		if err != nil {
			t.Fatalf("RandomCode: %v", err)
		}
		if len(code) != length {
			t.Fatalf("RandomCode returned %q (%d chars), want %d", code, len(code), length)
		}
		for j := 0; j < len(code); j++ {
			if !strings.ContainsRune(Alphabet, rune(code[j])) {
				t.Fatalf("code %q contains %q, which is not in the alphabet", code, code[j])
			}
			counts[code[j]]++
		}
		if seen[code] {
			t.Fatalf("code %q drawn twice in %d draws from a 3.5e12 space; the source is not random", code, n)
		}
		seen[code] = true
	}
	mean := float64(n*length) / 62
	for i := 0; i < len(Alphabet); i++ {
		if got := float64(counts[Alphabet[i]]); got < 0.6*mean || got > 1.4*mean {
			t.Fatalf("character %q drawn %d times, mean %.0f: the draw is biased (take bytes modulo 62 only after rejecting the top of the range)", Alphabet[i], counts[Alphabet[i]], mean)
		}
	}
	if _, err := RandomCode(nil, 0); !errors.Is(err, ErrBadArgument) {
		t.Fatalf("RandomCode(nil, 0) = %v, want ErrBadArgument", err)
	}
}

func TestRandomCode_IsUniformWithAFixedSource(t *testing.T) {
	// Feed every byte value in order, several times. Without rejection
	// sampling, bytes 248..255 would map to '0'..'7' and skew those digits.
	var src bytes.Buffer
	for rep := 0; rep < 40; rep++ {
		for b := 0; b < 256; b++ {
			src.WriteByte(byte(b))
		}
	}
	code, err := RandomCode(&src, 40*248)
	if err != nil {
		t.Fatalf("RandomCode: %v", err)
	}
	counts := map[byte]int{}
	for i := 0; i < len(code); i++ {
		counts[code[i]]++
	}
	for i := 0; i < len(Alphabet); i++ {
		if counts[Alphabet[i]] != 40*4 {
			t.Fatalf("character %q appears %d times, want %d: bytes >= 248 must be rejected, not folded onto the first digits", Alphabet[i], counts[Alphabet[i]], 40*4)
		}
	}
	if _, err := RandomCode(bytes.NewReader([]byte{1, 2, 3}), 5); err == nil {
		t.Fatalf("RandomCode with a source that runs dry returned no error")
	}
}

func TestRandomGenerator_RetriesOnCollisionAndGivesUp(t *testing.T) {
	ins := &tScriptedInserter{script: []bool{false, false, false, true}}
	g, err := NewRandomGenerator(RandomConfig{Inserter: ins, Length: 7, MaxAttempts: 5})
	if err != nil {
		t.Fatalf("NewRandomGenerator: %v", err)
	}
	code, attempts, err := g.Next(context.Background())
	if err != nil || len(code) != 7 || attempts != 4 {
		t.Fatalf("Next = %q, %d attempts, %v; want a 7-char code after 4 inserts (3 collisions)", code, attempts, err)
	}
	ins = &tScriptedInserter{} // every insert collides
	g, _ = NewRandomGenerator(RandomConfig{Inserter: ins, Length: 7, MaxAttempts: 5})
	code, attempts, err = g.Next(context.Background())
	if !errors.Is(err, ErrTooManyCollisions) || code != "" || attempts != 5 {
		t.Fatalf("Next with endless collisions = %q, %d attempts, %v; want ErrTooManyCollisions after exactly 5 attempts", code, attempts, err)
	}
	if ins.calls != 5 {
		t.Fatalf("inserter called %d times, want 5", ins.calls)
	}
}

func TestRandomGenerator_InserterErrorsAreNotCollisions(t *testing.T) {
	down := errors.New("dynamodb: throttled")
	ins := &tScriptedInserter{err: down}
	g, _ := NewRandomGenerator(RandomConfig{Inserter: ins})
	_, attempts, err := g.Next(context.Background())
	if !errors.Is(err, down) || errors.Is(err, ErrTooManyCollisions) || attempts != 1 {
		t.Fatalf("Next with a failing inserter = %d attempts, %v; want the store's error after 1 attempt, not a retry storm", attempts, err)
	}
	if _, err := NewRandomGenerator(RandomConfig{}); !errors.Is(err, ErrBadArgument) {
		t.Fatalf("NewRandomGenerator without an inserter = %v, want ErrBadArgument", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := g.Next(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Next with a cancelled context = %v, want context.Canceled", err)
	}
}

func TestRandomGenerator_ConcurrentInsertsNeverDuplicate(t *testing.T) {
	// Two-character codes: 3,844 possibilities, so 1,600 inserts collide often.
	ins := NewMemoryInserter()
	g, _ := NewRandomGenerator(RandomConfig{Inserter: ins, Length: 2, MaxAttempts: 60})
	const goroutines, each = 16, 100
	codes := make(chan string, goroutines*each)
	var attempts atomic.Int64
	var wg sync.WaitGroup
	for w := 0; w < goroutines; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < each; i++ {
				c, n, err := g.Next(context.Background())
				attempts.Add(int64(n))
				if err != nil {
					t.Errorf("Next: %v", err)
					return
				}
				codes <- c
			}
		}()
	}
	tWait(t, &wg, 20*time.Second, "concurrent random inserts")
	close(codes)
	seen := map[string]bool{}
	for c := range codes {
		if seen[c] {
			t.Fatalf("code %q returned twice: the inserter's check-and-claim is not atomic", c)
		}
		seen[c] = true
	}
	if len(seen) != goroutines*each || ins.Len() != goroutines*each {
		t.Fatalf("%d distinct codes returned, %d stored; want %d", len(seen), ins.Len(), goroutines*each)
	}
	if attempts.Load() <= goroutines*each {
		t.Fatalf("%d inserts for %d codes: at 42%% occupancy collisions must have happened, so the retry path was never exercised", attempts.Load(), goroutines*each)
	}
	t.Logf("%d codes claimed with %d inserts (%.2f per code) at %.0f%% occupancy", len(seen), attempts.Load(), float64(attempts.Load())/float64(len(seen)), 100*float64(len(seen))/Capacity(2))
}

func TestMemoryInserter_InsertIsAtomicAndReportsErrors(t *testing.T) {
	ins := NewMemoryInserter()
	var wins atomic.Int64
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			ok, err := ins.Insert(context.Background(), "aZ3kQ9x")
			if err == nil && ok {
				wins.Add(1)
			}
		}()
	}
	close(start)
	tWait(t, &wg, 10*time.Second, "100 concurrent inserts of one code")
	if wins.Load() != 1 || ins.Len() != 1 {
		t.Fatalf("%d of 100 concurrent inserts of the same code succeeded (%d stored); exactly 1 must", wins.Load(), ins.Len())
	}
	down := fmt.Errorf("store down")
	ins.Fail(down)
	if _, err := ins.Insert(context.Background(), "other"); !errors.Is(err, down) {
		t.Fatalf("Insert while failing = %v, want the injected error", err)
	}
	ins.Fail(nil)
	if ok, err := ins.Insert(context.Background(), "other"); err != nil || !ok {
		t.Fatalf("Insert after recovery = %v, %v; want inserted", ok, err)
	}
}
