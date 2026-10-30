// Package envelope is a TEACHING SKETCH of envelope encryption (A9 Unit 2
// §3, STRETCH). It exists so you can see, in about 300 lines, what a KMS and
// an encryption library do for you: a key hierarchy (KEK wraps DEK, DEK
// encrypts data), authenticated encryption with associated data binding each
// ciphertext to its tenant and object, rotation by re-wrapping keys instead of
// re-encrypting data, and crypto-shredding.
//
// # NEVER ROLL YOUR OWN CRYPTO IN PRODUCTION
//
// This code is not reviewed, not constant-time where it matters, keeps keys in
// ordinary Go memory, has no key backup or audit, and the "KMS" below is an
// in-process map. In production use a vetted library (for example the AWS
// Encryption SDK, Google Tink, or libsodium via its Go bindings) together
// with a managed KMS or HSM. Use this package only to understand the shape.
//
// Run: go test -race ./labs/a9/envelope/
//
// # Key hierarchy
//
//	KMS: key encryption key (KEK) "kek-main", versions 1, 2, ...  (never leaves the KMS)
//	  wraps -> data encryption key (DEK), one per tenant (and per "generation")
//	    encrypts -> objects, with AES-256-GCM, a fresh random 96-bit nonce
//	                and AAD = enc(tenant) || enc(object)
//
// # KMS behaviour
//
//   - Keys are named; CreateKey makes version 1, Rotate adds the next version
//     and makes it current. Wrap always uses the current version and records
//     it in the WrappedKey; Unwrap uses the version the WrappedKey names.
//   - Wrap/Unwrap take a context ("encryption context"): the wrapped DEK only
//     unwraps with the same context. The KEK's AES-GCM AAD is
//     enc(keyID) || enc(version) || enc(ctx) (enc = 4-byte big-endian length
//     then bytes; version as 8-byte big-endian).
//   - Rewrap unwraps and wraps under the current version without returning the
//     plaintext DEK to the caller.
//   - DestroyVersion removes a version's key material; Unwrap of anything
//     wrapped under it => ErrKeyDestroyed. The current version cannot be
//     destroyed (ErrInvalidArgument): rotate first.
//   - Calls() counts Wrap, Unwrap and Rewrap calls (KMS calls cost money and
//     latency; the vault is expected to cache DEKs).
//
// # Vault behaviour (tests depend on every line)
//
//   - Seal creates the tenant's DEK on first use (32 random bytes, wrapped by
//     the KMS with ctx "dek:"+keyID), caches the plaintext DEK, and encrypts.
//     Sealed.KeyID names the DEK ("<tenant>#<n>"; "*#<n>" with SharedKey).
//   - Open uses only the tenant and object the CALLER passes to compute AAD,
//     never fields from Sealed. Any authentication failure, wrong tenant,
//     wrong object or tampering => ErrDecrypt (one error: no oracle).
//   - A stuck random source is detected: if a nonce equals the previous nonce
//     generated for the same DEK, Seal fails with ErrNonceRepeat.
//   - After MaxSealsPerKey encryptions a DEK is retired and a new generation
//     is created for that tenant; old generations keep decrypting.
//   - RewrapKeys re-wraps every stored DEK under the KMS's current KEK
//     version (one KMS Rewrap per DEK) and returns how many it re-wrapped.
//     No data is re-encrypted.
//   - ShredTenant deletes every DEK of the tenant and leaves a tombstone:
//     afterwards Open and Seal for that tenant fail with ErrTenantShredded,
//     and other tenants are unaffected. With SharedKey it is ErrInvalidArgument.
package envelope

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"sync"
)

// Errors.
var (
	ErrInvalidArgument = errors.New("envelope: invalid argument")
	ErrNoSuchKey       = errors.New("envelope: no such key")
	ErrKeyDestroyed    = errors.New("envelope: key version destroyed")
	ErrDecrypt         = errors.New("envelope: decryption failed")
	ErrTenantShredded  = errors.New("envelope: tenant keys were shredded")
	ErrNonceRepeat     = errors.New("envelope: random source repeated a nonce")
)

const (
	keySize   = 32 // AES-256
	nonceSize = 12 // standard GCM nonce
)

func enc(buf []byte, b []byte) []byte {
	buf = binary.BigEndian.AppendUint32(buf, uint32(len(b)))
	return append(buf, b...)
}

func newGCM(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// ---------------------------------------------------------------------------
// KMS
// ---------------------------------------------------------------------------

// WrappedKey is a DEK encrypted under a KEK version. Blob = nonce || ciphertext.
type WrappedKey struct {
	KeyID   string
	Version int
	Blob    []byte
}

// Calls are the KMS API call counters.
type Calls struct{ Wrap, Unwrap, Rewrap int }

type kekEntry struct {
	versions map[int][]byte // nil value = destroyed
	current  int
}

// KMS is an in-process stand-in for a key management service.
type KMS struct {
	mu    sync.Mutex
	rand  io.Reader
	keys  map[string]*kekEntry
	calls Calls
}

// NewKMS returns an empty KMS. A nil rand means crypto/rand.Reader.
func NewKMS(r io.Reader) *KMS {
	if r == nil {
		r = rand.Reader
	}
	return &KMS{rand: r, keys: make(map[string]*kekEntry)}
}

// CreateKey creates key id at version 1. Creating an existing key =>
// ErrInvalidArgument.
func (k *KMS) CreateKey(id string) error {
	if id == "" {
		return ErrInvalidArgument
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	if _, ok := k.keys[id]; ok {
		return fmt.Errorf("%w: key %q exists", ErrInvalidArgument, id)
	}
	material := make([]byte, keySize)
	if _, err := io.ReadFull(k.rand, material); err != nil {
		return err
	}
	k.keys[id] = &kekEntry{versions: map[int][]byte{1: material}, current: 1}
	return nil
}

// Rotate adds a new version of key id, makes it current and returns it.
func (k *KMS) Rotate(id string) (int, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	e, ok := k.keys[id]
	if !ok {
		return 0, ErrNoSuchKey
	}
	material := make([]byte, keySize)
	if _, err := io.ReadFull(k.rand, material); err != nil {
		return 0, err
	}
	e.current++
	e.versions[e.current] = material
	return e.current, nil
}

func wrapAAD(id string, version int, ctx []byte) []byte {
	aad := enc(nil, []byte(id))
	aad = binary.BigEndian.AppendUint64(aad, uint64(version))
	return enc(aad, ctx)
}

// wrapLocked encrypts dek under the current version. Caller holds k.mu.
func (k *KMS) wrapLocked(id string, dek, ctx []byte) (WrappedKey, error) {
	e, ok := k.keys[id]
	if !ok {
		return WrappedKey{}, ErrNoSuchKey
	}
	aead, err := newGCM(e.versions[e.current])
	if err != nil {
		return WrappedKey{}, err
	}
	nonce := make([]byte, nonceSize)
	if _, err := io.ReadFull(k.rand, nonce); err != nil {
		return WrappedKey{}, err
	}
	blob := aead.Seal(nonce, nonce, dek, wrapAAD(id, e.current, ctx))
	return WrappedKey{KeyID: id, Version: e.current, Blob: blob}, nil
}

// unwrapLocked decrypts w. Caller holds k.mu.
func (k *KMS) unwrapLocked(w WrappedKey, ctx []byte) ([]byte, error) {
	e, ok := k.keys[w.KeyID]
	if !ok {
		return nil, ErrNoSuchKey
	}
	material, ok := e.versions[w.Version]
	if !ok {
		return nil, ErrNoSuchKey
	}
	if material == nil {
		return nil, ErrKeyDestroyed
	}
	if len(w.Blob) < nonceSize {
		return nil, ErrDecrypt
	}
	aead, err := newGCM(material)
	if err != nil {
		return nil, err
	}
	dek, err := aead.Open(nil, w.Blob[:nonceSize], w.Blob[nonceSize:], wrapAAD(w.KeyID, w.Version, ctx))
	if err != nil {
		return nil, ErrDecrypt
	}
	return dek, nil
}

// Wrap encrypts dek under the current version of key id, bound to ctx.
func (k *KMS) Wrap(id string, dek, ctx []byte) (WrappedKey, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.calls.Wrap++
	return k.wrapLocked(id, dek, ctx)
}

// Unwrap decrypts a wrapped DEK; ctx must equal the context used to wrap it.
func (k *KMS) Unwrap(w WrappedKey, ctx []byte) ([]byte, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.calls.Unwrap++
	return k.unwrapLocked(w, ctx)
}

// Rewrap moves a wrapped DEK to the current KEK version without exposing it.
func (k *KMS) Rewrap(w WrappedKey, ctx []byte) (WrappedKey, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.calls.Rewrap++
	dek, err := k.unwrapLocked(w, ctx)
	if err != nil {
		return WrappedKey{}, err
	}
	defer clear(dek)
	return k.wrapLocked(w.KeyID, dek, ctx)
}

// DestroyVersion irrevocably removes one version's key material.
func (k *KMS) DestroyVersion(id string, version int) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	e, ok := k.keys[id]
	if !ok {
		return ErrNoSuchKey
	}
	m, ok := e.versions[version]
	if !ok {
		return ErrNoSuchKey
	}
	if version == e.current {
		return fmt.Errorf("%w: version %d is current; rotate first", ErrInvalidArgument, version)
	}
	clear(m)
	e.versions[version] = nil
	return nil
}

// Calls returns a snapshot of the API call counters.
func (k *KMS) Calls() Calls {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.calls
}

// ---------------------------------------------------------------------------
// Vault
// ---------------------------------------------------------------------------

// Config configures a Vault.
type Config struct {
	KMS            *KMS
	KEKID          string    // must already exist in KMS
	SharedKey      bool      // use ONE DEK for every tenant (a mistake this lab lets you make, to see what AAD still protects)
	MaxSealsPerKey uint64    // 0 means 1<<32, the NIST limit for random 96-bit GCM nonces under one key
	Rand           io.Reader // nil means crypto/rand.Reader
}

// Sealed is an encrypted object as it would be stored: no key material, just a
// reference to the DEK that encrypted it.
type Sealed struct {
	KeyID      string
	Nonce      []byte
	Ciphertext []byte
}

type dekRecord struct {
	scope     string
	wrapped   WrappedKey
	uses      uint64
	lastNonce []byte
}

// Vault encrypts and decrypts objects for tenants. Safe for concurrent use.
type Vault struct {
	mu       sync.Mutex
	cfg      Config
	rand     io.Reader
	records  map[string]*dekRecord // keyID -> record (the "key table")
	current  map[string]string     // scope -> keyID used for new Seals
	gen      map[string]int        // scope -> last generation number
	cache    map[string][]byte     // keyID -> plaintext DEK
	shredded map[string]bool
}

// NewVault returns a Vault using cfg.KMS and key cfg.KEKID.
func NewVault(cfg Config) (*Vault, error) {
	if cfg.KMS == nil || cfg.KEKID == "" {
		return nil, ErrInvalidArgument
	}
	cfg.KMS.mu.Lock()
	_, ok := cfg.KMS.keys[cfg.KEKID]
	cfg.KMS.mu.Unlock()
	if !ok {
		return nil, ErrNoSuchKey
	}
	if cfg.MaxSealsPerKey == 0 {
		cfg.MaxSealsPerKey = 1 << 32
	}
	r := cfg.Rand
	if r == nil {
		r = rand.Reader
	}
	return &Vault{
		cfg: cfg, rand: r,
		records:  map[string]*dekRecord{},
		current:  map[string]string{},
		gen:      map[string]int{},
		cache:    map[string][]byte{},
		shredded: map[string]bool{},
	}, nil
}

func (v *Vault) scope(tenant string) string {
	if v.cfg.SharedKey {
		return "*"
	}
	return tenant
}

func dekContext(keyID string) []byte { return []byte("dek:" + keyID) }

// aadFor binds a ciphertext to its tenant and object.
func aadFor(tenant, object string) []byte {
	if breakNoAAD {
		return nil // break-it variant: ciphertexts are not bound to anything
	}
	return enc(enc(nil, []byte(tenant)), []byte(object))
}

// newDEK creates, wraps, stores and caches a fresh DEK generation for scope.
// Caller holds v.mu.
func (v *Vault) newDEK(scope string) (*dekRecord, string, error) {
	dek := make([]byte, keySize)
	if _, err := io.ReadFull(v.rand, dek); err != nil {
		return nil, "", fmt.Errorf("envelope: generating DEK: %w", err)
	}
	gen := v.gen[scope] + 1
	keyID := fmt.Sprintf("%s#%d", scope, gen)
	w, err := v.cfg.KMS.Wrap(v.cfg.KEKID, dek, dekContext(keyID))
	if err != nil {
		return nil, "", err
	}
	v.gen[scope] = gen
	rec := &dekRecord{scope: scope, wrapped: w}
	v.records[keyID] = rec
	v.current[scope] = keyID
	v.cache[keyID] = dek
	return rec, keyID, nil
}

// dekFor returns the plaintext DEK for a stored key, unwrapping through the
// KMS only on a cache miss. Caller holds v.mu.
func (v *Vault) dekFor(keyID string, rec *dekRecord) ([]byte, error) {
	if dek, ok := v.cache[keyID]; ok {
		return dek, nil
	}
	dek, err := v.cfg.KMS.Unwrap(rec.wrapped, dekContext(keyID))
	if err != nil {
		return nil, err
	}
	v.cache[keyID] = dek
	return dek, nil
}

// Seal encrypts plaintext for (tenant, object).
func (v *Vault) Seal(tenant, object string, plaintext []byte) (Sealed, error) {
	if tenant == "" || object == "" {
		return Sealed{}, ErrInvalidArgument
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.shredded[tenant] {
		return Sealed{}, ErrTenantShredded
	}
	scope := v.scope(tenant)
	var (
		keyID string
		rec   *dekRecord
		err   error
	)
	if id, ok := v.current[scope]; ok && v.records[id].uses < v.cfg.MaxSealsPerKey {
		keyID, rec = id, v.records[id]
	} else if rec, keyID, err = v.newDEK(scope); err != nil {
		return Sealed{}, err
	}
	dek, err := v.dekFor(keyID, rec)
	if err != nil {
		return Sealed{}, err
	}
	aead, err := newGCM(dek)
	if err != nil {
		return Sealed{}, err
	}
	nonce := make([]byte, nonceSize)
	if _, err := io.ReadFull(v.rand, nonce); err != nil {
		return Sealed{}, fmt.Errorf("envelope: generating nonce: %w", err)
	}
	if rec.lastNonce != nil && string(rec.lastNonce) == string(nonce) {
		return Sealed{}, ErrNonceRepeat
	}
	rec.lastNonce = nonce
	rec.uses++
	ct := aead.Seal(nil, nonce, plaintext, aadFor(tenant, object))
	return Sealed{KeyID: keyID, Nonce: append([]byte(nil), nonce...), Ciphertext: ct}, nil
}

// Open decrypts s as the object (tenant, object).
func (v *Vault) Open(tenant, object string, s Sealed) ([]byte, error) {
	if tenant == "" || object == "" {
		return nil, ErrInvalidArgument
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.shredded[tenant] {
		return nil, ErrTenantShredded
	}
	rec, ok := v.records[s.KeyID]
	if !ok || rec.scope != v.scope(tenant) || len(s.Nonce) != nonceSize {
		return nil, ErrDecrypt
	}
	dek, err := v.dekFor(s.KeyID, rec)
	if err != nil {
		return nil, err
	}
	aead, err := newGCM(dek)
	if err != nil {
		return nil, err
	}
	pt, err := aead.Open(nil, s.Nonce, s.Ciphertext, aadFor(tenant, object))
	if err != nil {
		return nil, ErrDecrypt
	}
	return pt, nil
}

// RewrapKeys re-wraps every stored DEK under the KMS's current KEK version and
// returns how many it re-wrapped. Data is not touched.
func (v *Vault) RewrapKeys() (int, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	n := 0
	for keyID, rec := range v.records {
		w, err := v.cfg.KMS.Rewrap(rec.wrapped, dekContext(keyID))
		if err != nil {
			return n, err
		}
		rec.wrapped = w
		n++
	}
	return n, nil
}

// DropCache forgets every plaintext DEK (as a process restart would), so the
// next use of each DEK costs a KMS Unwrap.
func (v *Vault) DropCache() {
	v.mu.Lock()
	defer v.mu.Unlock()
	for _, dek := range v.cache {
		clear(dek)
	}
	v.cache = map[string][]byte{}
}

// KeyCount returns the number of wrapped DEKs the vault holds.
func (v *Vault) KeyCount() int {
	v.mu.Lock()
	defer v.mu.Unlock()
	return len(v.records)
}

// ShredTenant destroys every DEK of the tenant (the wrapped copy and any
// cached plaintext) and records a tombstone so keys are never silently
// re-created. Ciphertext that still exists anywhere becomes unreadable.
func (v *Vault) ShredTenant(tenant string) error {
	if tenant == "" {
		return ErrInvalidArgument
	}
	if v.cfg.SharedKey {
		return fmt.Errorf("%w: tenants share a key; shredding one would shred all", ErrInvalidArgument)
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	for keyID, rec := range v.records {
		if rec.scope == tenant {
			clear(v.cache[keyID])
			delete(v.cache, keyID)
			delete(v.records, keyID)
		}
	}
	delete(v.current, tenant)
	v.shredded[tenant] = true
	return nil
}
