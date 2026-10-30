package shortener

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"regexp"
	"strings"
	"time"
)

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

var aliasRe = regexp.MustCompile(`^[A-Za-z0-9_-]{3,32}$`)

var reserved = map[string]bool{"api": true, "admin": true, "static": true, "health": true, "login": true, "logout": true}

func validAlias(a string) bool { return aliasRe.MatchString(a) && !reserved[strings.ToLower(a)] }

// ShortenWith is Shorten with options.
//
//   - No alias and no TTL: identical to Shorten (deduplicated).
//   - Otherwise the call creates a *distinct* link: it never reuses an
//     existing code and never registers one for later plain Shorten calls.
//   - Alias: 3-32 chars of [A-Za-z0-9_-], not a reserved word (api, admin,
//     static, health, login, logout, compared case-insensitively), else
//     ErrInvalidAlias. A live code with the same name is ErrAliasTaken; an
//     EXPIRED one is reclaimed. Exactly one concurrent claimant wins.
//   - TTL < 0 is ErrInvalidTTL.
func (s *Service) ShortenWith(ctx context.Context, longURL string, opts ShortenOptions) (string, error) {
	if opts.TTL < 0 {
		return "", ErrInvalidTTL
	}
	if opts.Alias == "" && opts.TTL == 0 {
		return s.Shorten(ctx, longURL)
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	u, err := s.normalize(longURL)
	if err != nil {
		return "", err
	}
	if opts.Alias == "" {
		return s.mint(ctx, u, opts.TTL)
	}
	if !validAlias(opts.Alias) {
		return "", ErrInvalidAlias
	}
	return s.claim(ctx, opts.Alias, u, opts.TTL)
}

// claim stores u under alias. The stripe for alias is held throughout, so
// reclaiming an expired record (read, delete, put) cannot interleave with
// another claimant's, or with Delete: a loser must never delete the winner's
// fresh record.
func (s *Service) claim(ctx context.Context, alias, u string, ttl time.Duration) (string, error) {
	l := s.lock(alias)
	l.Lock()
	defer l.Unlock()

	rec := s.newRecord(alias, u, ttl)
	err := s.store.Put(ctx, rec)
	if errors.Is(err, ErrExists) {
		old, gerr := s.store.Get(ctx, alias)
		switch {
		case gerr == nil && !s.expired(old):
			return "", ErrAliasTaken
		case gerr != nil && !errors.Is(gerr, ErrNotFound):
			return "", gerr
		}
		if derr := s.store.Delete(ctx, alias); derr != nil && !errors.Is(derr, ErrNotFound) {
			return "", derr
		}
		err = s.store.Put(ctx, rec)
		if errors.Is(err, ErrExists) { // a generated code took the name in between
			return "", ErrAliasTaken
		}
	}
	if err != nil {
		return "", err
	}
	s.resetClicks(alias)
	return alias, nil
}

// Stats describes a live link.
type Stats struct {
	URL       string
	Clicks    int64 // successful Resolve calls since the link was created
	CreatedAt time.Time
	ExpiresAt time.Time // zero = never
}

// Stats returns the link's metadata and click count, or ErrNotFound.
func (s *Service) Stats(ctx context.Context, code string) (Stats, error) {
	if err := ctx.Err(); err != nil {
		return Stats{}, err
	}
	if code == "" {
		return Stats{}, ErrNotFound
	}
	rec, err := s.store.Get(ctx, code)
	if err != nil {
		return Stats{}, err
	}
	if s.expired(rec) {
		return Stats{}, ErrNotFound
	}
	s.mu.Lock()
	n := s.clicks[code]
	s.mu.Unlock()
	return Stats{URL: rec.URL, Clicks: n, CreatedAt: rec.CreatedAt, ExpiresAt: rec.ExpiresAt}, nil
}

// Delete removes a link: later Resolve calls return ErrNotFound, its counter
// is dropped, and the code (or alias) may be created again. Deleting a code
// that does not exist is ErrNotFound.
func (s *Service) Delete(ctx context.Context, code string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if code == "" {
		return ErrNotFound
	}
	l := s.lock(code)
	l.Lock()
	defer l.Unlock()
	if err := s.store.Delete(ctx, code); err != nil {
		return err
	}
	s.resetClicks(code)
	return nil
}

// Random is an IDGenerator that returns random IDs of a fixed bit width.
type Random struct {
	src  func() uint64
	mask uint64
}

// NewRandom returns a Random producing values in [0, 2^bits). bits must be in
// [1, 63]. src supplies raw 64-bit randomness; nil means math/rand/v2's
// Uint64 (fast, NOT cryptographically secure).
func NewRandom(src func() uint64, bits uint) (*Random, error) {
	if bits < 1 || bits > 63 {
		return nil, fmt.Errorf("shortener: bits must be in [1, 63], got %d", bits)
	}
	if src == nil {
		src = rand.Uint64
	}
	return &Random{src: src, mask: 1<<bits - 1}, nil
}

// Next returns the next random ID. Safe for concurrent use if src is.
func (r *Random) Next() (uint64, error) { return r.src() & r.mask, nil }
