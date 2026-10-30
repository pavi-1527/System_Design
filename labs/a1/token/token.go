// Package token issues and verifies HMAC-SHA256 signed bearer tokens in the
// JWT compact format (header.payload.signature, base64url without padding,
// "alg":"HS256"), with exp/nbf and clock-skew leeway, kid-based key rotation
// and strict algorithm checking. Stdlib only.
//
// It is the STRETCH lab for A1 Unit 3 §3 (not required for the gate).
// Run: go test -race ./labs/a1/token/ It is deliberately small: it is
// here so you see every check a JWT library does for you, and the order they
// must happen in. In production use a maintained library and asymmetric keys
// (RS256/ES256/EdDSA) published via JWKS when the verifier is not the issuer.
//
// Verification order (tests depend on it):
//  1. exactly three dot-separated segments, header decodes to JSON => else ErrMalformed
//  2. header alg must be exactly "HS256" => else ErrUnsupportedAlg (this rejects "none")
//  3. header kid must name a key in the keyring => else ErrUnknownKey
//  4. signature must decode and match HMAC-SHA256(key, header "." payload),
//     compared in constant time => else ErrMalformed (bad encoding) / ErrBadSignature
//  5. only now decode the payload => else ErrMalformed; exp is required (else ErrMalformed)
//  6. exp: reject if now >= exp + Leeway => ErrExpired
//  7. nbf (if set): reject if now < nbf - Leeway => ErrNotYetValid
//  8. aud: if Verifier.Audience != "", claims.aud must equal it => else ErrAudience
package token

import (
	"errors"
	"time"
)

var errNotImplemented = errors.New("token: not implemented")

// Errors returned by Sign and Verify. Use errors.Is.
var (
	ErrMalformed      = errors.New("token: malformed")
	ErrUnsupportedAlg = errors.New("token: unsupported alg")
	ErrUnknownKey     = errors.New("token: unknown key id")
	ErrBadSignature   = errors.New("token: bad signature")
	ErrExpired        = errors.New("token: expired")
	ErrNotYetValid    = errors.New("token: not yet valid")
	ErrAudience       = errors.New("token: wrong audience")
	ErrNoCurrentKey   = errors.New("token: no current signing key")
)

// Claims are the registered JWT claims this package understands, plus two
// private ones. Times are Unix seconds. aud is a single string here (RFC 7519
// also allows an array; this lab does not).
type Claims struct {
	Issuer    string `json:"iss,omitempty"`
	Subject   string `json:"sub,omitempty"`
	Audience  string `json:"aud,omitempty"`
	ExpiresAt int64  `json:"exp,omitempty"`
	NotBefore int64  `json:"nbf,omitempty"`
	IssuedAt  int64  `json:"iat,omitempty"`
	Scope     string `json:"scope,omitempty"`
	Tenant    string `json:"tenant,omitempty"` // e.g. the credit union a partner token is bound to
}

// Keyring holds HMAC secrets by key id and which one signs new tokens.
// Safe for concurrent use. Rotation: Add the new key, SetCurrent to it, keep
// the old one until every token it signed has expired, then Remove it.
type Keyring struct {
	// TODO(lab): sync.RWMutex, map[kid]secret, current kid.
}

// NewKeyring returns an empty keyring.
func NewKeyring() *Keyring {
	// TODO(lab)
	return &Keyring{}
}

// Add stores a copy of secret under kid (replacing any existing key).
func (k *Keyring) Add(kid string, secret []byte) {
	// TODO(lab): copy the slice; the caller may wipe it.
}

// SetCurrent selects the key that Sign uses. ErrUnknownKey if absent.
func (k *Keyring) SetCurrent(kid string) error {
	// TODO(lab)
	return errNotImplemented
}

// Remove deletes kid. If it was current, there is no current key afterwards.
func (k *Keyring) Remove(kid string) {
	// TODO(lab)
}

// Sign returns a compact token for c, signed with the keyring's current key
// and carrying its kid in the header ({"alg":"HS256","kid":...,"typ":"JWT"}).
// ErrNoCurrentKey if none is set.
//
// Format: base64url_nopad(header JSON) "." base64url_nopad(claims JSON) "."
// base64url_nopad(HMAC-SHA256(key, first two segments joined by ".")).
func Sign(k *Keyring, c Claims) (string, error) {
	// TODO(lab): encoding/json, encoding/base64.RawURLEncoding, crypto/hmac.
	return "", errNotImplemented
}

// Verifier checks tokens. Leeway absorbs clock skew between issuer and
// verifier (typically tens of seconds). Now defaults to time.Now.
type Verifier struct {
	Keys     *Keyring
	Leeway   time.Duration
	Now      func() time.Time
	Audience string // if non-empty, claims.aud must equal it
}

// Verify checks tok in the order given in the package doc and returns its
// claims. Compare signatures with hmac.Equal, never bytes.Equal or ==.
func (v *Verifier) Verify(tok string) (Claims, error) {
	// TODO(lab): follow steps 1-8 of the package doc, in order.
	return Claims{}, errNotImplemented
}
