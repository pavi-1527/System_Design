// Package token issues and verifies HMAC-SHA256 signed bearer tokens in the
// JWT compact format (header.payload.signature, base64url without padding,
// "alg":"HS256"), with exp/nbf and clock-skew leeway, kid-based key rotation
// and strict algorithm checking. Stdlib only.
//
// It is the STRETCH lab for A1 Unit 3 §3. It is deliberately small: it is
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
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"time"
)

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

type header struct {
	Alg string `json:"alg"`
	Kid string `json:"kid,omitempty"`
	Typ string `json:"typ,omitempty"`
}

var b64 = base64.RawURLEncoding

// Keyring holds HMAC secrets by key id and which one signs new tokens.
// Safe for concurrent use. Rotation: Add the new key, SetCurrent to it, keep
// the old one until every token it signed has expired, then Remove it.
type Keyring struct {
	mu      sync.RWMutex
	keys    map[string][]byte
	current string
}

// NewKeyring returns an empty keyring.
func NewKeyring() *Keyring { return &Keyring{keys: map[string][]byte{}} }

// Add stores a copy of secret under kid (replacing any existing key).
func (k *Keyring) Add(kid string, secret []byte) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.keys[kid] = append([]byte(nil), secret...)
}

// SetCurrent selects the key that Sign uses. ErrUnknownKey if absent.
func (k *Keyring) SetCurrent(kid string) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	if _, ok := k.keys[kid]; !ok {
		return ErrUnknownKey
	}
	k.current = kid
	return nil
}

// Remove deletes kid. If it was current, there is no current key afterwards.
func (k *Keyring) Remove(kid string) {
	k.mu.Lock()
	defer k.mu.Unlock()
	delete(k.keys, kid)
	if k.current == kid {
		k.current = ""
	}
}

func (k *Keyring) lookup(kid string) ([]byte, bool) {
	k.mu.RLock()
	defer k.mu.RUnlock()
	s, ok := k.keys[kid]
	return s, ok
}

func (k *Keyring) currentKey() (string, []byte, bool) {
	k.mu.RLock()
	defer k.mu.RUnlock()
	if k.current == "" {
		return "", nil, false
	}
	return k.current, k.keys[k.current], true
}

func mac(key []byte, signingInput string) []byte {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(signingInput))
	return m.Sum(nil)
}

// Sign returns a compact token for c, signed with the keyring's current key
// and carrying its kid in the header ({"alg":"HS256","kid":...,"typ":"JWT"}).
// ErrNoCurrentKey if none is set.
func Sign(k *Keyring, c Claims) (string, error) {
	kid, key, ok := k.currentKey()
	if !ok {
		return "", ErrNoCurrentKey
	}
	hb, err := json.Marshal(header{Alg: "HS256", Kid: kid, Typ: "JWT"})
	if err != nil {
		return "", err
	}
	pb, err := json.Marshal(c)
	if err != nil {
		return "", err
	}
	input := b64.EncodeToString(hb) + "." + b64.EncodeToString(pb)
	return input + "." + b64.EncodeToString(mac(key, input)), nil
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
// claims.
func (v *Verifier) Verify(tok string) (Claims, error) {
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		return Claims{}, ErrMalformed
	}
	hb, err := b64.DecodeString(parts[0])
	if err != nil {
		return Claims{}, ErrMalformed
	}
	var h header
	if err := json.Unmarshal(hb, &h); err != nil {
		return Claims{}, ErrMalformed
	}
	if h.Alg != "HS256" {
		return Claims{}, ErrUnsupportedAlg
	}
	if v.Keys == nil {
		return Claims{}, ErrUnknownKey
	}
	key, ok := v.Keys.lookup(h.Kid)
	if !ok || h.Kid == "" {
		return Claims{}, ErrUnknownKey
	}
	sig, err := b64.DecodeString(parts[2])
	if err != nil {
		return Claims{}, ErrMalformed
	}
	if !hmac.Equal(sig, mac(key, parts[0]+"."+parts[1])) {
		return Claims{}, ErrBadSignature
	}
	pb, err := b64.DecodeString(parts[1])
	if err != nil {
		return Claims{}, ErrMalformed
	}
	var c Claims
	if err := json.Unmarshal(pb, &c); err != nil {
		return Claims{}, ErrMalformed
	}
	if c.ExpiresAt == 0 {
		return Claims{}, ErrMalformed
	}
	now := time.Now
	if v.Now != nil {
		now = v.Now
	}
	t := now()
	if !t.Before(time.Unix(c.ExpiresAt, 0).Add(v.Leeway)) {
		return Claims{}, ErrExpired
	}
	if c.NotBefore != 0 && t.Before(time.Unix(c.NotBefore, 0).Add(-v.Leeway)) {
		return Claims{}, ErrNotYetValid
	}
	if v.Audience != "" && c.Audience != v.Audience {
		return Claims{}, ErrAudience
	}
	return c, nil
}
