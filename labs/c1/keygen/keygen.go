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
	"errors"
	"io"
	"sync"
)

var errNotImplemented = errors.New("keygen: not implemented")

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

// Capacity returns the number of distinct codes of the given length,
// 62^length, as a float64 (0 for length <= 0).
func Capacity(length int) float64 {
	// TODO(lab)
	_ = length
	return 0
}

// Encode returns id in base 62 (Alphabet, most significant digit first),
// left-padded with '0' to exactly length characters. It returns
// ErrBadArgument if length <= 0 or id needs more than length digits.
func Encode(id uint64, length int) (string, error) {
	// TODO(lab): fill a byte slice from the right with id % 62, then id /= 62.
	_, _ = id, length
	return "", errNotImplemented
}

// Decode is the inverse of Encode (leading zeros are fine). It returns
// ErrInvalidCode for an empty code, a character outside Alphabet, or a value
// that overflows uint64.
func Decode(code string) (uint64, error) {
	// TODO(lab): check for overflow before each v = v*62 + d.
	_ = code
	return 0, errNotImplemented
}

// CollisionProbability is the chance that one more uniformly random code of
// the given length equals one of `existing` codes already in use:
// existing / 62^length, capped at 1. (The birthday bound for a *sequence* of
// inserts follows from it: summing over the inserts.)
func CollisionProbability(length int, existing float64) float64 {
	// TODO(lab)
	_, _ = length, existing
	return 0
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
	// TODO(lab): bits, a mask for the low `bits` bits, the odd multiplier,
	// its modular inverse mod 2^bits, and the xor constant.
	bits uint
}

// NewMixer returns a Mixer for the given width (1..64) and seed. The same
// (bits, seed) always gives the same permutation.
//
// Hints: derive the multiplier from the seed and force it odd (every odd
// number is invertible modulo a power of two). Its inverse modulo 2^64 comes
// from Newton's iteration, inv = inv * (2 - mul*inv), repeated six times
// starting from inv = mul; mask it to `bits` bits afterwards. Derive the xor
// constant from the seed as well, masked to `bits` bits.
func NewMixer(bits uint, seed uint64) (Mixer, error) {
	// TODO(lab)
	_, _ = bits, seed
	return Mixer{}, errNotImplemented
}

// Bits returns the width of the permutation.
func (m Mixer) Bits() uint { return m.bits }

// Mix maps id to its position in the permutation. ErrBadArgument if id >= 2^Bits.
func (m Mixer) Mix(id uint64) (uint64, error) {
	// TODO(lab)
	_ = id
	return 0, errNotImplemented
}

// Unmix is the inverse of Mix. ErrBadArgument if code >= 2^Bits.
func (m Mixer) Unmix(code uint64) (uint64, error) {
	// TODO(lab)
	_ = code
	return 0, errNotImplemented
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
	// TODO(lab): a mutex, the value, the injected error, and the two
	// operation counts. The compare and the swap must happen under one lock
	// acquisition: that is the whole point of the type.
	mu sync.Mutex
}

// NewMemoryCounter returns a counter holding start.
func NewMemoryCounter(start uint64) *MemoryCounter {
	// TODO(lab)
	_ = start
	return &MemoryCounter{}
}

// Load implements Counter.
func (c *MemoryCounter) Load(_ context.Context) (uint64, error) {
	// TODO(lab): count the call even when it fails.
	return 0, errNotImplemented
}

// CompareAndSwap implements Counter.
func (c *MemoryCounter) CompareAndSwap(_ context.Context, old, new uint64) (bool, error) {
	// TODO(lab)
	_, _ = old, new
	return false, errNotImplemented
}

// Fail makes every following Load and CompareAndSwap return err until
// Fail(nil) is called. It simulates the counter's store being unreachable.
func (c *MemoryCounter) Fail(err error) {
	// TODO(lab)
	_ = err
}

// Ops returns how many Load and CompareAndSwap calls the counter has seen
// (failed ones included).
func (c *MemoryCounter) Ops() (loads, swaps int) {
	// TODO(lab)
	return 0, 0
}

// Range is a half-open interval of ids [Start, End).
type Range struct {
	Start, End uint64
}

// Len returns the number of ids in the range.
func (r Range) Len() uint64 {
	// TODO(lab): 0 when End <= Start.
	return 0
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
	// TODO(lab): the config after defaults.
	cfg AllocatorConfig
}

// NewAllocator validates cfg and applies defaults. A nil Counter, or a Size or
// Limit of 0 after defaults, is ErrBadArgument.
func NewAllocator(cfg AllocatorConfig) (*Allocator, error) {
	// TODO(lab)
	_ = cfg
	return nil, errNotImplemented
}

// Size returns the configured range size.
func (a *Allocator) Size() uint64 {
	// TODO(lab)
	return 0
}

// Reserve takes the next Size ids (fewer at the end of the keyspace) with a
// Load + CompareAndSwap loop. A lost swap is retried, up to MaxRetries
// times (ErrContended). A counter at or past Limit is ErrExhausted. Counter
// errors are returned wrapped. A cancelled context returns ctx.Err().
func (a *Allocator) Reserve(ctx context.Context) (Range, error) {
	// TODO(lab): loop: ctx check → Load → limit check → end = min(cur+Size,
	// Limit) → CompareAndSwap(cur, end) → return [cur, end) on success.
	_ = ctx
	return Range{}, errNotImplemented
}

// Generator hands out ids from ranges reserved by an Allocator: the hot path
// is a mutex and an increment; the store is touched once per range. Safe for
// concurrent use.
type Generator struct {
	// TODO(lab): the allocator, a mutex, the current range and the next id.
	mu sync.Mutex
}

// NewGenerator returns a Generator with no range reserved yet (the first
// Next reserves one).
func NewGenerator(alloc *Allocator) *Generator {
	// TODO(lab)
	_ = alloc
	return &Generator{}
}

// Next returns the next unused id, reserving a new range when the current
// one is used up. Errors from Reserve are returned as they are, and no id is
// consumed; a later call tries again.
func (g *Generator) Next(ctx context.Context) (uint64, error) {
	// TODO(lab)
	_ = ctx
	return 0, errNotImplemented
}

// Remaining returns how many ids are left in the current range.
func (g *Generator) Remaining() uint64 {
	// TODO(lab)
	return 0
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
	// TODO(lab): a mutex, a set of codes, and the injected error.
	mu sync.Mutex
}

// NewMemoryInserter returns an empty MemoryInserter.
func NewMemoryInserter() *MemoryInserter {
	// TODO(lab)
	return &MemoryInserter{}
}

// Insert implements Inserter.
func (m *MemoryInserter) Insert(_ context.Context, code string) (bool, error) {
	// TODO(lab)
	_ = code
	return false, errNotImplemented
}

// Len returns how many codes are stored.
func (m *MemoryInserter) Len() int {
	// TODO(lab)
	return 0
}

// Fail makes every following Insert return err until Fail(nil).
func (m *MemoryInserter) Fail(err error) {
	// TODO(lab)
	_ = err
}

// RandomCode returns length uniformly random characters from Alphabet, read
// from r (crypto/rand.Reader when r is nil). The draw is unbiased: a byte is
// used only when it falls below the largest multiple of 62 that fits in a
// byte (rejection sampling), so no character is favoured. ErrBadArgument if
// length <= 0.
func RandomCode(r io.Reader, length int) (string, error) {
	// TODO(lab): read bytes; keep b only if b < 248 (= 4 × 62), then use
	// Alphabet[b % 62]. Return an error if the reader runs dry.
	_, _ = r, length
	return "", errNotImplemented
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
	// TODO(lab): the config after defaults.
	cfg RandomConfig
}

// NewRandomGenerator validates cfg and applies defaults. A nil Inserter is
// ErrBadArgument.
func NewRandomGenerator(cfg RandomConfig) (*RandomGenerator, error) {
	// TODO(lab)
	_ = cfg
	return nil, errNotImplemented
}

// Next returns a freshly claimed code and the number of Insert calls it
// took. Collisions are retried up to MaxAttempts times (ErrTooManyCollisions
// afterwards); an Inserter error is returned at once, wrapped. A cancelled
// context returns ctx.Err() before the next attempt.
func (r *RandomGenerator) Next(ctx context.Context) (code string, attempts int, err error) {
	// TODO(lab)
	_ = ctx
	return "", 0, errNotImplemented
}
