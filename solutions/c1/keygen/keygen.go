// Package keygen generates unique short codes for a URL shortener, which is
// the first deep dive of case study C1: every created link needs a code that
// no other link has, forever, across every API server, without a single
// serialising point on the hot path, and without the codes being guessable.
//
// It is the lab for C1 Unit 1 §5. Three pieces, in the order the tests expect
// you to build them:
//
//  1. Codes: base-62 encoding (Encode/Decode/Capacity), the collision
//     arithmetic for random codes (CollisionProbability), and a Mixer, a
//     bijection on [0, 2^bits) that turns a counter's consecutive ids into
//     codes that do not look consecutive (so nobody can enumerate your
//     links by counting up).
//  2. Range allocation: an Allocator reserves disjoint ranges of ids from a
//     durable Counter with compare-and-swap, and a per-node Generator hands
//     out ids from its current range without touching the store. A crashed
//     node loses at most one range of ids, never uniqueness.
//  3. Random codes: a RandomGenerator draws random codes and relies on an
//     atomic insert-if-absent (Inserter) to detect the rare collision, with a
//     bounded number of attempts.
//
// Run: go test -race ./labs/c1/keygen/
package keygen

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"math"
	"sync"
)

// Errors returned by this package. Wrap them; callers use errors.Is.
var (
	// ErrBadArgument is returned for an id that does not fit, a length <= 0,
	// or an invalid configuration.
	ErrBadArgument = errors.New("keygen: bad argument")
	// ErrInvalidCode is returned by Decode for an empty code, a character
	// outside Alphabet, or a value that overflows uint64.
	ErrInvalidCode = errors.New("keygen: invalid code")
	// ErrExhausted is returned by Allocator.Reserve once the counter has
	// reached the configured limit: the keyspace is used up.
	ErrExhausted = errors.New("keygen: keyspace exhausted")
	// ErrContended is returned by Allocator.Reserve when MaxRetries
	// compare-and-swap attempts in a row lost to other allocators.
	ErrContended = errors.New("keygen: counter contended")
	// ErrTooManyCollisions is returned by RandomGenerator.Next when
	// MaxAttempts random codes all collided with existing ones.
	ErrTooManyCollisions = errors.New("keygen: too many collisions")
)

// Alphabet is the base-62 digit set, least significant digit value first:
// '0' = 0 ... '9' = 9, 'a' = 10 ... 'z' = 35, 'A' = 36 ... 'Z' = 61.
const Alphabet = "0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ"

const base = uint64(len(Alphabet))

// digitValue maps a byte to its value in Alphabet, or -1.
var digitValue = func() [256]int8 {
	var t [256]int8
	for i := range t {
		t[i] = -1
	}
	for i := 0; i < len(Alphabet); i++ {
		t[Alphabet[i]] = int8(i)
	}
	return t
}()

// Capacity returns the number of distinct codes of the given length,
// 62^length, as a float64 (0 for length <= 0).
func Capacity(length int) float64 {
	if length <= 0 {
		return 0
	}
	return math.Pow(float64(base), float64(length))
}

// Encode returns id in base 62 (Alphabet, most significant digit first),
// left-padded with '0' to exactly length characters. It returns
// ErrBadArgument if length <= 0 or id needs more than length digits.
func Encode(id uint64, length int) (string, error) {
	if length <= 0 {
		return "", fmt.Errorf("%w: length %d", ErrBadArgument, length)
	}
	buf := make([]byte, length)
	for i := length - 1; i >= 0; i-- {
		buf[i] = Alphabet[id%base]
		id /= base
	}
	if id != 0 {
		return "", fmt.Errorf("%w: id does not fit in %d base-62 digits", ErrBadArgument, length)
	}
	return string(buf), nil
}

// Decode is the inverse of Encode (leading zeros are fine). It returns
// ErrInvalidCode for an empty code, a character outside Alphabet, or a value
// that overflows uint64.
func Decode(code string) (uint64, error) {
	if code == "" {
		return 0, fmt.Errorf("%w: empty", ErrInvalidCode)
	}
	var v uint64
	for i := 0; i < len(code); i++ {
		d := digitValue[code[i]]
		if d < 0 {
			return 0, fmt.Errorf("%w: character %q", ErrInvalidCode, code[i])
		}
		if v > (math.MaxUint64-uint64(d))/base {
			return 0, fmt.Errorf("%w: overflow", ErrInvalidCode)
		}
		v = v*base + uint64(d)
	}
	return v, nil
}

// CollisionProbability is the chance that one more uniformly random code of
// the given length equals one of `existing` codes already in use:
// existing / 62^length, capped at 1. (The birthday bound for a *sequence* of
// inserts follows from it: summing over the inserts.)
func CollisionProbability(length int, existing float64) float64 {
	cap := Capacity(length)
	if cap <= 0 {
		return 1
	}
	if existing <= 0 {
		return 0
	}
	p := existing / cap
	if p > 1 {
		return 1
	}
	return p
}

// ---------------------------------------------------------------------------
// Mixer: a bijection on [0, 2^bits) so that counter ids become scattered codes
// ---------------------------------------------------------------------------

// Mixer permutes the integers in [0, 2^Bits). Mix(id) = ((id XOR x) * m) mod
// 2^Bits with m odd, which is invertible: Unmix(c) = ((c * m⁻¹) mod 2^Bits)
// XOR x, where m⁻¹ is the modular inverse of m. The constants m and x are
// derived from the seed, so two servers with the same seed agree, and a code
// reveals nothing about creation order without the seed.
type Mixer struct {
	bits uint
	mask uint64
	mul  uint64
	inv  uint64
	xor  uint64
}

// NewMixer returns a Mixer for the given width (1..64) and seed. The same
// (bits, seed) always gives the same permutation.
func NewMixer(bits uint, seed uint64) (Mixer, error) {
	if bits < 1 || bits > 64 {
		return Mixer{}, fmt.Errorf("%w: bits must be in 1..64, got %d", ErrBadArgument, bits)
	}
	mask := uint64(math.MaxUint64)
	if bits < 64 {
		mask = (uint64(1) << bits) - 1
	}
	// Scramble the seed so that nearby seeds give unrelated constants, then
	// force the multiplier odd (every odd number is invertible mod 2^k).
	s := seed*0x9E3779B97F4A7C15 + 0x632BE59BD9B4E019
	s ^= s >> 29
	mul := (s | 1) & mask
	x := (s * 0xBF58476D1CE4E5B9) >> (64 - bits)
	if bits == 64 {
		x = s * 0xBF58476D1CE4E5B9
	}
	// Newton's iteration: each step doubles the number of correct low bits.
	inv := mul
	for i := 0; i < 6; i++ {
		inv *= 2 - mul*inv
	}
	return Mixer{bits: bits, mask: mask, mul: mul, inv: inv & mask, xor: x & mask}, nil
}

// Bits returns the width of the permutation.
func (m Mixer) Bits() uint { return m.bits }

// Mix maps id to its position in the permutation. ErrBadArgument if id >= 2^Bits.
func (m Mixer) Mix(id uint64) (uint64, error) {
	if id&^m.mask != 0 {
		return 0, fmt.Errorf("%w: id %d does not fit in %d bits", ErrBadArgument, id, m.bits)
	}
	return ((id ^ m.xor) * m.mul) & m.mask, nil
}

// Unmix is the inverse of Mix. ErrBadArgument if code >= 2^Bits.
func (m Mixer) Unmix(code uint64) (uint64, error) {
	if code&^m.mask != 0 {
		return 0, fmt.Errorf("%w: code %d does not fit in %d bits", ErrBadArgument, code, m.bits)
	}
	return ((code * m.inv) & m.mask) ^ m.xor, nil
}

// ---------------------------------------------------------------------------
// Range allocation from a durable counter
// ---------------------------------------------------------------------------

// Counter is the one durable number behind every id: a row in a relational
// database updated with "UPDATE ... WHERE value = $old", a Redis key updated
// by a script, an etcd key with a compare-and-swap transaction.
// Implementations must make CompareAndSwap atomic: of N concurrent calls with
// the same old value, exactly one succeeds.
type Counter interface {
	// Load returns the current value.
	Load(ctx context.Context) (uint64, error)
	// CompareAndSwap sets the value to new if it currently equals old, and
	// reports whether it did.
	CompareAndSwap(ctx context.Context, old, new uint64) (swapped bool, err error)
}

// MemoryCounter is an in-process Counter with fault injection and operation
// counts. Safe for concurrent use.
type MemoryCounter struct {
	mu    sync.Mutex
	v     uint64
	fail  error
	loads int
	swaps int
}

// NewMemoryCounter returns a counter holding start.
func NewMemoryCounter(start uint64) *MemoryCounter { return &MemoryCounter{v: start} }

// Load implements Counter.
func (c *MemoryCounter) Load(_ context.Context) (uint64, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.loads++
	if c.fail != nil {
		return 0, c.fail
	}
	return c.v, nil
}

// CompareAndSwap implements Counter.
func (c *MemoryCounter) CompareAndSwap(_ context.Context, old, new uint64) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.swaps++
	if c.fail != nil {
		return false, c.fail
	}
	if c.v != old {
		return false, nil
	}
	c.v = new
	return true, nil
}

// Fail makes every following Load and CompareAndSwap return err until
// Fail(nil) is called. It simulates the counter's store being unreachable.
func (c *MemoryCounter) Fail(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.fail = err
}

// Ops returns how many Load and CompareAndSwap calls the counter has seen
// (failed ones included).
func (c *MemoryCounter) Ops() (loads, swaps int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.loads, c.swaps
}

// Range is a half-open interval of ids [Start, End).
type Range struct {
	Start, End uint64
}

// Len returns the number of ids in the range.
func (r Range) Len() uint64 {
	if r.End <= r.Start {
		return 0
	}
	return r.End - r.Start
}

// AllocatorConfig configures an Allocator. Zero values get defaults.
type AllocatorConfig struct {
	// Counter is the durable counter (required).
	Counter Counter
	// Size is how many ids one Reserve takes (default 1000).
	Size uint64
	// Limit is the exclusive upper bound of the keyspace (default 62^7).
	// Reserve never hands out an id >= Limit.
	Limit uint64
	// MaxRetries bounds how many lost compare-and-swap rounds Reserve
	// tolerates before returning ErrContended (default 16).
	MaxRetries int
}

// DefaultLimit is 62^7: the number of 7-character base-62 codes.
const DefaultLimit = uint64(3_521_614_606_208)

// Allocator reserves disjoint id ranges from a Counter. Safe for concurrent
// use; several allocators (one per node) may share one counter.
type Allocator struct {
	cfg AllocatorConfig
}

// NewAllocator validates cfg and applies defaults. A nil Counter, or a Size or
// Limit of 0 after defaults, is ErrBadArgument.
func NewAllocator(cfg AllocatorConfig) (*Allocator, error) {
	if cfg.Counter == nil {
		return nil, fmt.Errorf("%w: nil counter", ErrBadArgument)
	}
	if cfg.Size == 0 {
		cfg.Size = 1000
	}
	if cfg.Limit == 0 {
		cfg.Limit = DefaultLimit
	}
	if cfg.MaxRetries <= 0 {
		cfg.MaxRetries = 16
	}
	return &Allocator{cfg: cfg}, nil
}

// Size returns the configured range size.
func (a *Allocator) Size() uint64 { return a.cfg.Size }

// Reserve takes the next Size ids (fewer at the end of the keyspace) with a
// Load + CompareAndSwap loop. A lost swap is retried, up to MaxRetries
// times (ErrContended). A counter at or past Limit is ErrExhausted. Counter
// errors are returned wrapped. A cancelled context returns ctx.Err().
func (a *Allocator) Reserve(ctx context.Context) (Range, error) {
	for attempt := 0; attempt < a.cfg.MaxRetries; attempt++ {
		if err := ctx.Err(); err != nil {
			return Range{}, err
		}
		cur, err := a.cfg.Counter.Load(ctx)
		if err != nil {
			return Range{}, fmt.Errorf("keygen: load counter: %w", err)
		}
		if cur >= a.cfg.Limit {
			return Range{}, ErrExhausted
		}
		end := cur + a.cfg.Size
		if end > a.cfg.Limit || end < cur { // the second clause guards against overflow
			end = a.cfg.Limit
		}
		ok, err := a.cfg.Counter.CompareAndSwap(ctx, cur, end)
		if err != nil {
			return Range{}, fmt.Errorf("keygen: advance counter: %w", err)
		}
		if ok {
			return Range{Start: cur, End: end}, nil
		}
	}
	return Range{}, ErrContended
}

// Generator hands out ids from ranges reserved by an Allocator: the hot path
// is a mutex and an increment; the store is touched once per range. Safe for
// concurrent use.
type Generator struct {
	alloc *Allocator
	mu    sync.Mutex
	cur   Range
	next  uint64
}

// NewGenerator returns a Generator with no range reserved yet (the first
// Next reserves one).
func NewGenerator(alloc *Allocator) *Generator { return &Generator{alloc: alloc} }

// Next returns the next unused id, reserving a new range when the current
// one is used up. Errors from Reserve are returned as they are, and no id is
// consumed; a later call tries again.
func (g *Generator) Next(ctx context.Context) (uint64, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.next >= g.cur.End {
		r, err := g.alloc.Reserve(ctx)
		if err != nil {
			return 0, err
		}
		g.cur, g.next = r, r.Start
	}
	id := g.next
	g.next++
	return id, nil
}

// Remaining returns how many ids are left in the current range.
func (g *Generator) Remaining() uint64 {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.next >= g.cur.End {
		return 0
	}
	return g.cur.End - g.next
}

// ---------------------------------------------------------------------------
// Random codes with insert-if-absent
// ---------------------------------------------------------------------------

// Inserter is the link store's "insert this code if it is not taken". In
// production it is a unique index (INSERT ... ON CONFLICT DO NOTHING), a
// conditional put, or SET NX. It must be atomic: of N concurrent inserts of
// the same code, exactly one reports inserted == true.
type Inserter interface {
	Insert(ctx context.Context, code string) (inserted bool, err error)
}

// MemoryInserter is an in-process Inserter. Safe for concurrent use.
type MemoryInserter struct {
	mu   sync.Mutex
	set  map[string]struct{}
	fail error
}

// NewMemoryInserter returns an empty MemoryInserter.
func NewMemoryInserter() *MemoryInserter { return &MemoryInserter{set: map[string]struct{}{}} }

// Insert implements Inserter.
func (m *MemoryInserter) Insert(_ context.Context, code string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.fail != nil {
		return false, m.fail
	}
	if _, taken := m.set[code]; taken {
		return false, nil
	}
	m.set[code] = struct{}{}
	return true, nil
}

// Len returns how many codes are stored.
func (m *MemoryInserter) Len() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.set)
}

// Fail makes every following Insert return err until Fail(nil).
func (m *MemoryInserter) Fail(err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.fail = err
}

// RandomCode returns length uniformly random characters from Alphabet, read
// from r (crypto/rand.Reader when r is nil). The draw is unbiased: a byte is
// used only when it falls below the largest multiple of 62 that fits in a
// byte (rejection sampling), so no character is favoured. ErrBadArgument if
// length <= 0.
func RandomCode(r io.Reader, length int) (string, error) {
	if length <= 0 {
		return "", fmt.Errorf("%w: length %d", ErrBadArgument, length)
	}
	if r == nil {
		r = rand.Reader
	}
	const limit = 248 // 4 * 62: bytes >= 248 are rejected to keep the draw uniform
	out := make([]byte, 0, length)
	buf := make([]byte, 2*length+8)
	for len(out) < length {
		n, err := io.ReadFull(r, buf)
		if err != nil && n == 0 {
			return "", fmt.Errorf("keygen: random source: %w", err)
		}
		for _, b := range buf[:n] {
			if b >= limit {
				continue
			}
			out = append(out, Alphabet[int(b)%len(Alphabet)])
			if len(out) == length {
				break
			}
		}
		if err != nil {
			break
		}
	}
	if len(out) < length {
		return "", fmt.Errorf("keygen: random source: %w", io.ErrUnexpectedEOF)
	}
	return string(out), nil
}

// RandomConfig configures a RandomGenerator. Zero values get defaults.
type RandomConfig struct {
	// Inserter checks and claims a code atomically (required).
	Inserter Inserter
	// Length of the codes (default 7).
	Length int
	// MaxAttempts bounds the number of Insert calls per Next (default 5).
	MaxAttempts int
	// Rand is the random source (default crypto/rand.Reader).
	Rand io.Reader
}

// RandomGenerator draws random codes and claims them with the Inserter,
// retrying on collision. Safe for concurrent use.
type RandomGenerator struct {
	cfg RandomConfig
}

// NewRandomGenerator validates cfg and applies defaults. A nil Inserter is
// ErrBadArgument.
func NewRandomGenerator(cfg RandomConfig) (*RandomGenerator, error) {
	if cfg.Inserter == nil {
		return nil, fmt.Errorf("%w: nil inserter", ErrBadArgument)
	}
	if cfg.Length <= 0 {
		cfg.Length = 7
	}
	if cfg.MaxAttempts <= 0 {
		cfg.MaxAttempts = 5
	}
	if cfg.Rand == nil {
		cfg.Rand = rand.Reader
	}
	return &RandomGenerator{cfg: cfg}, nil
}

// Next returns a freshly claimed code and the number of Insert calls it
// took. Collisions are retried up to MaxAttempts times (ErrTooManyCollisions
// afterwards); an Inserter error is returned at once, wrapped. A cancelled
// context returns ctx.Err() before the next attempt.
func (r *RandomGenerator) Next(ctx context.Context) (code string, attempts int, err error) {
	for attempts < r.cfg.MaxAttempts {
		if err := ctx.Err(); err != nil {
			return "", attempts, err
		}
		c, err := RandomCode(r.cfg.Rand, r.cfg.Length)
		if err != nil {
			return "", attempts, err
		}
		attempts++
		inserted, err := r.cfg.Inserter.Insert(ctx, c)
		if err != nil {
			return "", attempts, fmt.Errorf("keygen: insert: %w", err)
		}
		if inserted {
			return c, attempts, nil
		}
	}
	return "", attempts, ErrTooManyCollisions
}
