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
// Suggested order: KMS (CreateKey, Wrap, Unwrap), Vault Seal/Open, DEK
// caching, Rotate/Rewrap/RewrapKeys, ShredTenant, MaxSealsPerKey.
// Use crypto/aes, cipher.NewGCM and crypto/rand from the standard library;
// nothing else.
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
	"errors"
	"io"
)

var errNotImplemented = errors.New("envelope: not implemented")

// Errors.
var (
	ErrInvalidArgument = errors.New("envelope: invalid argument")
	ErrNoSuchKey       = errors.New("envelope: no such key")
	ErrKeyDestroyed    = errors.New("envelope: key version destroyed")
	ErrDecrypt         = errors.New("envelope: decryption failed")
	ErrTenantShredded  = errors.New("envelope: tenant keys were shredded")
	ErrNonceRepeat     = errors.New("envelope: random source repeated a nonce")
)

// WrappedKey is a DEK encrypted under a KEK version. Blob = nonce || ciphertext.
type WrappedKey struct {
	KeyID   string
	Version int
	Blob    []byte
}

// Calls are the KMS API call counters.
type Calls struct{ Wrap, Unwrap, Rewrap int }

// KMS is an in-process stand-in for a key management service. Safe for
// concurrent use.
type KMS struct {
	// TODO(lab): a mutex, the random source, keys by id (each with versions
	// and a current version; a destroyed version is remembered as destroyed)
	// and the call counters.
}

// NewKMS returns an empty KMS. A nil rand means crypto/rand.Reader.
func NewKMS(rand io.Reader) *KMS {
	// TODO(lab)
	return &KMS{}
}

// CreateKey creates key id at version 1 (32 random bytes of key material).
// Creating an existing key => ErrInvalidArgument.
func (k *KMS) CreateKey(id string) error {
	// TODO(lab)
	return errNotImplemented
}

// Rotate adds a new version of key id, makes it current and returns it.
// Unknown id => ErrNoSuchKey.
func (k *KMS) Rotate(id string) (int, error) {
	// TODO(lab)
	return 0, errNotImplemented
}

// Wrap encrypts dek under the current version of key id, bound to ctx
// (AES-256-GCM, random nonce; WrappedKey.Blob = nonce || ciphertext).
func (k *KMS) Wrap(id string, dek, ctx []byte) (WrappedKey, error) {
	// TODO(lab)
	return WrappedKey{}, errNotImplemented
}

// Unwrap decrypts a wrapped DEK; ctx must equal the context used to wrap it
// (else ErrDecrypt). A destroyed version => ErrKeyDestroyed.
func (k *KMS) Unwrap(w WrappedKey, ctx []byte) ([]byte, error) {
	// TODO(lab)
	return nil, errNotImplemented
}

// Rewrap moves a wrapped DEK to the current KEK version without exposing it.
func (k *KMS) Rewrap(w WrappedKey, ctx []byte) (WrappedKey, error) {
	// TODO(lab)
	return WrappedKey{}, errNotImplemented
}

// DestroyVersion irrevocably removes one version's key material. Destroying
// the current version => ErrInvalidArgument.
func (k *KMS) DestroyVersion(id string, version int) error {
	// TODO(lab)
	return errNotImplemented
}

// Calls returns a snapshot of the API call counters (Wrap, Unwrap and Rewrap
// each count API calls made by callers, not internal steps).
func (k *KMS) Calls() Calls {
	// TODO(lab)
	return Calls{}
}

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

// Vault encrypts and decrypts objects for tenants. Safe for concurrent use.
type Vault struct {
	// TODO(lab): a mutex; the key table (keyID -> wrapped DEK, owner scope,
	// use count, last nonce); the current key per scope; a cache of plaintext
	// DEKs; shredded tenants.
}

// NewVault returns a Vault using cfg.KMS and key cfg.KEKID. A nil KMS or empty
// KEKID => ErrInvalidArgument; an unknown KEK => ErrNoSuchKey.
func NewVault(cfg Config) (*Vault, error) {
	// TODO(lab)
	return nil, errNotImplemented
}

// Seal encrypts plaintext for (tenant, object). Empty tenant or object =>
// ErrInvalidArgument.
//
// Break-it: when breakNoAAD is true (go test -tags breakit), pass nil as the
// associated data in Seal and Open and watch what the isolation tests say.
func (v *Vault) Seal(tenant, object string, plaintext []byte) (Sealed, error) {
	// TODO(lab): read nonces and keys with io.ReadFull; AAD with length prefixes.
	return Sealed{}, errNotImplemented
}

// Open decrypts s as the object (tenant, object).
func (v *Vault) Open(tenant, object string, s Sealed) ([]byte, error) {
	// TODO(lab)
	return nil, errNotImplemented
}

// RewrapKeys re-wraps every stored DEK under the KMS's current KEK version and
// returns how many it re-wrapped. Data is not touched.
func (v *Vault) RewrapKeys() (int, error) {
	// TODO(lab)
	return 0, errNotImplemented
}

// DropCache forgets every plaintext DEK (as a process restart would), so the
// next use of each DEK costs a KMS Unwrap.
func (v *Vault) DropCache() {
	// TODO(lab)
}

// KeyCount returns the number of wrapped DEKs the vault holds.
func (v *Vault) KeyCount() int {
	// TODO(lab)
	return 0
}

// ShredTenant destroys every DEK of the tenant (the wrapped copy and any
// cached plaintext) and records a tombstone so keys are never silently
// re-created. Ciphertext that still exists anywhere becomes unreadable.
func (v *Vault) ShredTenant(tenant string) error {
	// TODO(lab)
	return errNotImplemented
}
