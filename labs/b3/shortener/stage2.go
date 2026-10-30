package shortener

import (
	"context"
	"errors"
	"time"
)

// Stage 2 (≈ 20 min): aliases, expiry, stats, deletion, a second ID strategy.

// Stage 2 errors.
var (
	ErrInvalidAlias       = errors.New("shortener: invalid alias")
	ErrAliasTaken         = errors.New("shortener: alias already taken")
	ErrInvalidTTL         = errors.New("shortener: negative TTL")
	ErrCollisionExhausted = errors.New("shortener: could not find a free code")
)

// ShortenOptions customise a link. The zero value is a plain Shorten.
type ShortenOptions struct {
	Alias string        // custom code; "" = generate one
	TTL   time.Duration // > 0: the link expires TTL after creation; 0 = never
}

// ShortenWith is Shorten with options.
//
//   - No alias and no TTL: identical to Shorten (deduplicated).
//   - Otherwise the call creates a *distinct* link: it never reuses an
//     existing code and never registers one for later plain Shorten calls.
//   - Alias: 3-32 chars of [A-Za-z0-9_-], not a reserved word (api, admin,
//     static, health, login, logout, compared case-insensitively), else
//     ErrInvalidAlias; validate before touching the Store. A live code with
//     that name is ErrAliasTaken (even for the same URL); an EXPIRED one is
//     reclaimed. Exactly one concurrent claimant wins, and a losing claimant
//     must never delete the winner's record.
//   - TTL < 0 is ErrInvalidTTL. A link is expired when now >= CreatedAt+TTL.
//   - Expired links behave as absent: Resolve and Stats return ErrNotFound.
//
// Generated codes: if Store.Put reports ErrExists (an alias, or a random ID
// colliding), ask the IDGenerator again, up to Config.MaxAttempts Put attempts
// in total, then return ErrCollisionExhausted. A failed attempt leaves no
// trace. An error from IDGenerator.Next is returned (wrapping is fine).
func (s *Service) ShortenWith(ctx context.Context, longURL string, opts ShortenOptions) (string, error) {
	// TODO(lab)
	_, _, _ = ctx, longURL, opts
	return "", errNotImplemented
}

// Stats describes a live link.
type Stats struct {
	URL       string
	Clicks    int64 // successful Resolve calls since the link was created
	CreatedAt time.Time
	ExpiresAt time.Time // zero = never
}

// Stats returns the link's metadata and click count; ErrNotFound for an
// unknown, empty or expired code. Failed or expired Resolve calls do not
// count as clicks. A deleted-then-recreated (or reclaimed) code starts at 0.
func (s *Service) Stats(ctx context.Context, code string) (Stats, error) {
	// TODO(lab)
	_, _ = ctx, code
	return Stats{}, errNotImplemented
}

// Delete removes a link: later Resolve calls return ErrNotFound, its counter
// is dropped, and the code (or alias) may be created again. Deleting a code
// that does not exist (or the empty code) is ErrNotFound. If the Store's
// Delete fails, return its error and change nothing else.
//
// After Delete returns, no Resolve that starts afterwards may succeed.
func (s *Service) Delete(ctx context.Context, code string) error {
	// TODO(lab)
	_, _ = ctx, code
	return errNotImplemented
}

// Random is an IDGenerator that returns random IDs of a fixed bit width.
type Random struct {
	// TODO(lab)
}

// NewRandom returns a Random producing values in [0, 2^bits). bits must be in
// [1, 63], otherwise an error is returned. src supplies raw 64-bit randomness;
// nil means math/rand/v2's Uint64 (fast, NOT cryptographically secure).
func NewRandom(src func() uint64, bits uint) (*Random, error) {
	// TODO(lab)
	_, _ = src, bits
	return nil, errNotImplemented
}

// Next returns the next random ID (the raw value masked to the bit width).
func (r *Random) Next() (uint64, error) {
	// TODO(lab)
	return 0, errNotImplemented
}
