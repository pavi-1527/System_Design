package envelope

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"testing/iotest"
	"time"
)

// ---------- helpers (prefixed with t to avoid clashing with your code) ----------

func tWait(t *testing.T, wg *sync.WaitGroup, d time.Duration, what string) {
	t.Helper()
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(d):
		t.Fatalf("%s did not finish within %v (deadlock or livelock in the implementation?)", what, d)
	}
}

func tKMS(t *testing.T) *KMS {
	t.Helper()
	k := NewKMS(nil)
	if err := k.CreateKey("kek-main"); err != nil {
		t.Fatalf("CreateKey: %v; every other expectation in this test depends on it", err)
	}
	return k
}

func tVault(t *testing.T, k *KMS, mod func(*Config)) *Vault {
	t.Helper()
	cfg := Config{KMS: k, KEKID: "kek-main"}
	if mod != nil {
		mod(&cfg)
	}
	v, err := NewVault(cfg)
	if err != nil {
		t.Fatalf("NewVault: %v; every other expectation in this test depends on it", err)
	}
	return v
}

func tSeal(t *testing.T, v *Vault, tenant, object, pt string) Sealed {
	t.Helper()
	s, err := v.Seal(tenant, object, []byte(pt))
	if err != nil {
		t.Fatalf("Seal(%s, %s): %v; every other expectation in this test depends on it", tenant, object, err)
	}
	return s
}

// tRepeat is a "random" source that returns the same byte forever: a stand-in
// for a broken or misconfigured entropy source.
type tRepeat byte

func (r tRepeat) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = byte(r)
	}
	return len(p), nil
}

// ---------- KMS ----------

func TestKMS_WrapUnwrapBindsToContext(t *testing.T) {
	k := tKMS(t)
	dek := bytes.Repeat([]byte{0x42}, 32)
	w, err := k.Wrap("kek-main", dek, []byte("ctx-a"))
	if err != nil {
		t.Fatalf("Wrap: %v", err)
	}
	if w.KeyID != "kek-main" || w.Version != 1 {
		t.Fatalf("WrappedKey = %+v; expected it to name kek-main version 1", w)
	}
	if bytes.Contains(w.Blob, dek) {
		t.Fatalf("the wrapped blob contains the plaintext DEK")
	}
	got, err := k.Unwrap(w, []byte("ctx-a"))
	if err != nil || !bytes.Equal(got, dek) {
		t.Fatalf("Unwrap = %x, %v; expected the original DEK", got, err)
	}
	if _, err := k.Unwrap(w, []byte("ctx-b")); !errors.Is(err, ErrDecrypt) {
		t.Fatalf("Unwrap with the wrong encryption context = %v; expected ErrDecrypt (a wrapped key must not be movable to another owner)", err)
	}
	tampered := w
	tampered.Blob = append([]byte(nil), w.Blob...)
	tampered.Blob[len(tampered.Blob)-1] ^= 1
	if _, err := k.Unwrap(tampered, []byte("ctx-a")); !errors.Is(err, ErrDecrypt) {
		t.Fatalf("Unwrap of a modified blob = %v; expected ErrDecrypt", err)
	}
	w2, _ := k.Wrap("kek-main", dek, []byte("ctx-a"))
	if bytes.Equal(w.Blob, w2.Blob) {
		t.Fatalf("wrapping the same DEK twice gave identical blobs; every wrap needs a fresh random nonce")
	}
	if _, err := k.Wrap("nope", dek, nil); !errors.Is(err, ErrNoSuchKey) {
		t.Fatalf("Wrap with an unknown key = %v; expected ErrNoSuchKey", err)
	}
	if err := k.CreateKey("kek-main"); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("creating an existing key = %v; expected ErrInvalidArgument", err)
	}
}

func TestKMS_RotateRewrapAndDestroy(t *testing.T) {
	k := tKMS(t)
	dek := bytes.Repeat([]byte{7}, 32)
	old, _ := k.Wrap("kek-main", dek, []byte("c"))

	v2, err := k.Rotate("kek-main")
	if err != nil || v2 != 2 {
		t.Fatalf("Rotate = %d, %v; expected version 2", v2, err)
	}
	if got, err := k.Unwrap(old, []byte("c")); err != nil || !bytes.Equal(got, dek) {
		t.Fatalf("a key wrapped before rotation no longer unwraps: %v", err)
	}
	fresh, _ := k.Wrap("kek-main", dek, []byte("c"))
	if fresh.Version != 2 {
		t.Fatalf("new wraps use version %d; expected the current version 2", fresh.Version)
	}
	rew, err := k.Rewrap(old, []byte("c"))
	if err != nil || rew.Version != 2 || bytes.Equal(rew.Blob, old.Blob) {
		t.Fatalf("Rewrap = %+v, %v; expected a new blob under version 2", rew, err)
	}
	if got, err := k.Unwrap(rew, []byte("c")); err != nil || !bytes.Equal(got, dek) {
		t.Fatalf("rewrapped key unwraps to %x, %v; expected the same DEK", got, err)
	}
	if _, err := k.Rewrap(old, []byte("wrong")); !errors.Is(err, ErrDecrypt) {
		t.Fatalf("Rewrap with the wrong context = %v; expected ErrDecrypt", err)
	}

	if err := k.DestroyVersion("kek-main", 2); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("destroying the current version = %v; expected ErrInvalidArgument (rotate first)", err)
	}
	if err := k.DestroyVersion("kek-main", 1); err != nil {
		t.Fatalf("DestroyVersion(1): %v", err)
	}
	if _, err := k.Unwrap(old, []byte("c")); !errors.Is(err, ErrKeyDestroyed) {
		t.Fatalf("unwrapping under a destroyed version = %v; expected ErrKeyDestroyed", err)
	}
	if got, err := k.Unwrap(rew, []byte("c")); err != nil || !bytes.Equal(got, dek) {
		t.Fatalf("the rewrapped key must survive destroying version 1: %v", err)
	}
	if c := k.Calls(); c.Rewrap != 2 || c.Wrap != 2 || c.Unwrap < 3 {
		t.Fatalf("Calls = %+v; expected Wrap=2 (Rewrap's internal wrap is not an API call), Rewrap=2", c)
	}
}

// ---------- Vault: encryption basics ----------

func TestVault_RoundTripsAndNeverRepeatsACiphertext(t *testing.T) {
	v := tVault(t, tKMS(t), nil)
	for _, pt := range []string{"", "x", "balance=1204311.52", strings.Repeat("A", 1<<20)} {
		s := tSeal(t, v, "cu-alpha", "acct-1", pt)
		if len(s.Nonce) != 12 {
			t.Fatalf("nonce is %d bytes; GCM wants 12", len(s.Nonce))
		}
		if len(s.Ciphertext) != len(pt)+16 {
			t.Fatalf("ciphertext is %d bytes for %d plaintext bytes; expected +16 for the GCM tag", len(s.Ciphertext), len(pt))
		}
		if len(pt) >= 8 && bytes.Contains(s.Ciphertext, []byte(pt[:8])) && pt[:8] != strings.Repeat("A", 8) {
			t.Fatalf("plaintext visible in ciphertext")
		}
		got, err := v.Open("cu-alpha", "acct-1", s)
		if err != nil || string(got) != pt {
			t.Fatalf("Open = %q, %v; expected the plaintext back (len %d)", truncate(string(got)), err, len(pt))
		}
	}
	a := tSeal(t, v, "cu-alpha", "acct-1", "same plaintext")
	b := tSeal(t, v, "cu-alpha", "acct-1", "same plaintext")
	if bytes.Equal(a.Nonce, b.Nonce) || bytes.Equal(a.Ciphertext, b.Ciphertext) {
		t.Fatalf("sealing the same plaintext twice gave the same nonce or ciphertext; encryption must be randomised (this also leaks equality)")
	}
}

func truncate(s string) string {
	if len(s) > 40 {
		return s[:40] + "..."
	}
	return s
}

func TestVault_NoncesAreUniqueAcrossManySeals(t *testing.T) {
	v := tVault(t, tKMS(t), nil)
	seen := map[string]bool{}
	for i := 0; i < 20000; i++ {
		s := tSeal(t, v, "cu-alpha", "o", "p")
		if seen[string(s.Nonce)] {
			t.Fatalf("nonce repeated after %d seals under one key; nonce reuse under AES-GCM breaks both confidentiality and authenticity", i)
		}
		seen[string(s.Nonce)] = true
	}
}

func TestVault_TamperingAndWrongObjectFailAuthentication(t *testing.T) {
	v := tVault(t, tKMS(t), nil)
	s := tSeal(t, v, "cu-alpha", "acct-1", "pay $500 to payee-9")
	other := tSeal(t, v, "cu-alpha", "acct-2", "pay $1 to payee-3")

	flip := func(b []byte, i int) []byte { c := append([]byte(nil), b...); c[i] ^= 0x80; return c }
	bad := map[string]Sealed{
		"flipped ciphertext bit": {KeyID: s.KeyID, Nonce: s.Nonce, Ciphertext: flip(s.Ciphertext, 3)},
		"flipped tag bit":        {KeyID: s.KeyID, Nonce: s.Nonce, Ciphertext: flip(s.Ciphertext, len(s.Ciphertext)-1)},
		"flipped nonce bit":      {KeyID: s.KeyID, Nonce: flip(s.Nonce, 0), Ciphertext: s.Ciphertext},
		"truncated ciphertext":   {KeyID: s.KeyID, Nonce: s.Nonce, Ciphertext: s.Ciphertext[:len(s.Ciphertext)-1]},
		"short nonce":            {KeyID: s.KeyID, Nonce: s.Nonce[:8], Ciphertext: s.Ciphertext},
		"unknown key id":         {KeyID: "cu-alpha#99", Nonce: s.Nonce, Ciphertext: s.Ciphertext},
	}
	for name, b := range bad {
		if _, err := v.Open("cu-alpha", "acct-1", b); !errors.Is(err, ErrDecrypt) {
			t.Errorf("%s: Open = %v; expected ErrDecrypt", name, err)
		}
	}
	// Substitution inside one tenant: the blob for acct-2 presented as acct-1's data.
	if got, err := v.Open("cu-alpha", "acct-1", other); !errors.Is(err, ErrDecrypt) {
		t.Fatalf("Open(acct-1, <acct-2's blob>) = %q, %v; expected ErrDecrypt: the associated data must bind a ciphertext to its object id, or anyone with write access to the storage layer can swap values between rows", got, err)
	}
	if got, err := v.Open("cu-alpha", "acct-1", s); err != nil || string(got) != "pay $500 to payee-9" {
		t.Fatalf("the genuine blob stopped opening: %v", err)
	}
}

func TestVault_OneDEKPerTenantAndCrossTenantOpenFails(t *testing.T) {
	k := tKMS(t)
	v := tVault(t, k, nil)
	a := tSeal(t, v, "cu-alpha", "acct-1", "alpha secret")
	b := tSeal(t, v, "cu-beta", "acct-1", "beta secret")
	if a.KeyID == b.KeyID {
		t.Fatalf("two tenants share DEK %q; a per-tenant key is what makes isolation, rotation and shredding per tenant", a.KeyID)
	}
	if got, err := v.Open("cu-beta", "acct-1", a); !errors.Is(err, ErrDecrypt) {
		t.Fatalf("cu-beta opened cu-alpha's blob: %q, %v", got, err)
	}
	if got, err := v.Open("cu-alpha", "acct-1", b); !errors.Is(err, ErrDecrypt) {
		t.Fatalf("cu-alpha opened cu-beta's blob: %q, %v", got, err)
	}
	ea := tSeal(t, v, "cu-alpha", "acct-1", "x")
	eb := tSeal(t, v, "cu-beta", "acct-1", "x")
	errA := func() error { _, err := v.Open("cu-beta", "acct-1", ea); return err }()
	_, errUnknown := v.Open("cu-beta", "acct-1", Sealed{KeyID: "nope#1", Nonce: eb.Nonce, Ciphertext: eb.Ciphertext})
	if errA == nil || errUnknown == nil || errA.Error() != errUnknown.Error() {
		t.Fatalf("errors differ between 'another tenant's key' (%v) and 'unknown key' (%v); they must be indistinguishable", errA, errUnknown)
	}
}

func TestVault_SharedKeyStillBindsCiphertextToTenantAndObject(t *testing.T) {
	// A deliberately bad configuration (one DEK for everyone). The associated
	// data is then the ONLY thing separating tenants and objects.
	v := tVault(t, tKMS(t), func(c *Config) { c.SharedKey = true })
	a := tSeal(t, v, "cu-alpha", "acct-1", "alpha secret")
	if got, err := v.Open("cu-beta", "acct-1", a); !errors.Is(err, ErrDecrypt) {
		t.Fatalf("cross-tenant swap succeeded under a shared key: tenant cu-beta read %q (err=%v); without AAD a storage-level attacker can move one tenant's ciphertext into another's row", got, err)
	}
	// Concatenation ambiguity: ("ab","c") must differ from ("a","bc").
	s := tSeal(t, v, "ab", "c", "secret")
	if got, err := v.Open("a", "bc", s); !errors.Is(err, ErrDecrypt) {
		t.Fatalf("Open(a, bc) accepted a blob sealed for (ab, c): %q, %v; encode the AAD fields with length prefixes", got, err)
	}
	if got, err := v.Open("ab", "c", s); err != nil || string(got) != "secret" {
		t.Fatalf("the genuine blob does not open: %v", err)
	}
	if err := v.ShredTenant("cu-alpha"); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("ShredTenant with a shared key = %v; expected ErrInvalidArgument (it would destroy everyone's data)", err)
	}
}

// ---------- Vault: caching, rotation, shredding ----------

func TestVault_CachesDEKsSoKMSCallsDoNotScaleWithObjects(t *testing.T) {
	k := tKMS(t)
	v := tVault(t, k, nil)
	var sealed []Sealed
	for i := 0; i < 1000; i++ {
		sealed = append(sealed, tSeal(t, v, "cu-alpha", fmt.Sprint("o", i), "payload"))
	}
	if c := k.Calls(); c.Wrap != 1 || c.Unwrap != 0 {
		t.Fatalf("after 1000 seals for one tenant the KMS saw %+v; expected exactly 1 Wrap and 0 Unwrap (the DEK is generated once and cached; a KMS call per object is a cost and availability disaster)", c)
	}
	for i := 0; i < 1000; i++ {
		if _, err := v.Open("cu-alpha", fmt.Sprint("o", i), sealed[i]); err != nil {
			t.Fatalf("Open %d: %v", i, err)
		}
	}
	if c := k.Calls(); c.Unwrap != 0 {
		t.Fatalf("reads with a warm cache called Unwrap %d times; expected 0", c.Unwrap)
	}
	v.DropCache() // a restart
	for i := 0; i < 1000; i++ {
		if _, err := v.Open("cu-alpha", fmt.Sprint("o", i), sealed[i]); err != nil {
			t.Fatalf("Open %d after cache drop: %v", i, err)
		}
	}
	if c := k.Calls(); c.Unwrap != 1 {
		t.Fatalf("1000 reads after a cold start made %d Unwrap calls; expected exactly 1 (then cached)", c.Unwrap)
	}
	tSeal(t, v, "cu-beta", "o", "x")
	tSeal(t, v, "cu-gamma", "o", "x")
	if c := k.Calls(); c.Wrap != 3 {
		t.Fatalf("three tenants caused %d Wrap calls; expected 3, one per tenant DEK", c.Wrap)
	}
}

func TestVault_RotatingTheKEKReWrapsKeysNotData(t *testing.T) {
	k := tKMS(t)
	v := tVault(t, k, nil)
	type item struct {
		tenant, object, pt string
		s                  Sealed
		snapshot           []byte
	}
	var items []item
	for ti := 0; ti < 5; ti++ {
		for oi := 0; oi < 20; oi++ {
			it := item{tenant: fmt.Sprint("cu-", ti), object: fmt.Sprint("obj-", oi), pt: fmt.Sprintf("payload %d/%d", ti, oi)}
			it.s = tSeal(t, v, it.tenant, it.object, it.pt)
			it.snapshot = append([]byte(nil), it.s.Ciphertext...)
			items = append(items, it)
		}
	}
	before := k.Calls()
	if _, err := k.Rotate("kek-main"); err != nil {
		t.Fatal(err)
	}
	n, err := v.RewrapKeys()
	if err != nil || n != 5 {
		t.Fatalf("RewrapKeys = %d, %v; expected 5 (one per tenant DEK; 100 objects must not mean 100 KMS calls)", n, err)
	}
	if c := k.Calls(); c.Rewrap-before.Rewrap != 5 {
		t.Fatalf("KMS Rewrap calls = %d; expected 5", c.Rewrap-before.Rewrap)
	}
	if err := k.DestroyVersion("kek-main", 1); err != nil {
		t.Fatalf("DestroyVersion: %v", err)
	}
	v.DropCache()
	for _, it := range items {
		got, err := v.Open(it.tenant, it.object, it.s)
		if err != nil || string(got) != it.pt {
			t.Fatalf("%s/%s no longer opens after KEK rotation + destroying the old KEK version: %v", it.tenant, it.object, err)
		}
		if !bytes.Equal(it.s.Ciphertext, it.snapshot) {
			t.Fatalf("stored ciphertext changed during rotation; rotating a KEK must not touch data")
		}
	}
}

func TestVault_DestroyingAnOldKEKWithoutRewrappingLosesData(t *testing.T) {
	k := tKMS(t)
	v := tVault(t, k, nil)
	s := tSeal(t, v, "cu-alpha", "o", "irreplaceable")
	k.Rotate("kek-main")
	if err := k.DestroyVersion("kek-main", 1); err != nil {
		t.Fatal(err)
	}
	// With a warm cache nothing hurts yet, which is exactly why this bug ships.
	if _, err := v.Open("cu-alpha", "o", s); err != nil {
		t.Fatalf("warm cache read failed: %v", err)
	}
	v.DropCache()
	if _, err := v.Open("cu-alpha", "o", s); !errors.Is(err, ErrKeyDestroyed) {
		t.Fatalf("Open after a cold start = %v; expected ErrKeyDestroyed: the DEK was still wrapped under the destroyed KEK version", err)
	}
}

func TestVault_ShredTenantMakesItsDataUnreadableAndLeavesOthersAlone(t *testing.T) {
	k := tKMS(t)
	v := tVault(t, k, nil)
	var alpha []Sealed
	for i := 0; i < 3; i++ {
		alpha = append(alpha, tSeal(t, v, "cu-alpha", fmt.Sprint("o", i), "alpha data"))
	}
	beta := tSeal(t, v, "cu-beta", "o0", "beta data")
	if v.KeyCount() != 2 {
		t.Fatalf("KeyCount = %d; expected 2 DEKs", v.KeyCount())
	}
	if err := v.ShredTenant("cu-alpha"); err != nil {
		t.Fatalf("ShredTenant: %v", err)
	}
	if v.KeyCount() != 1 {
		t.Fatalf("KeyCount after shred = %d; expected the tenant's wrapped DEK to be gone (that deletion IS the erasure)", v.KeyCount())
	}
	for i, s := range alpha {
		if _, err := v.Open("cu-alpha", fmt.Sprint("o", i), s); !errors.Is(err, ErrTenantShredded) {
			t.Errorf("Open of shredded tenant object %d = %v; expected ErrTenantShredded", i, err)
		}
	}
	if _, err := v.Seal("cu-alpha", "o9", []byte("new")); !errors.Is(err, ErrTenantShredded) {
		t.Fatalf("Seal for a shredded tenant = %v; expected ErrTenantShredded (silently minting a new key would 'resurrect' an offboarded tenant)", err)
	}
	if got, err := v.Open("cu-beta", "o0", beta); err != nil || string(got) != "beta data" {
		t.Fatalf("other tenant affected by the shred: %v", err)
	}
	// Even a fresh vault over the same KMS cannot recover the key: nothing in the KMS maps tenants to DEKs.
	v2 := tVault(t, k, nil)
	if _, err := v2.Open("cu-alpha", "o0", alpha[0]); err == nil {
		t.Fatalf("a second vault opened shredded data")
	}
	if err := v.ShredTenant(""); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("ShredTenant(\"\") = %v; expected ErrInvalidArgument", err)
	}
}

func TestVault_RetiresADEKAfterMaxSealsAndKeepsOldOnesReadable(t *testing.T) {
	k := tKMS(t)
	v := tVault(t, k, func(c *Config) { c.MaxSealsPerKey = 3 })
	var all []Sealed
	keys := map[string]int{}
	for i := 0; i < 10; i++ {
		s := tSeal(t, v, "cu-alpha", fmt.Sprint("o", i), fmt.Sprint("p", i))
		all = append(all, s)
		keys[s.KeyID]++
	}
	if len(keys) != 4 {
		t.Fatalf("10 seals with MaxSealsPerKey=3 used %d distinct DEKs %v; expected 4 (3+3+3+1): a key must not encrypt more messages than random-nonce GCM safely allows", len(keys), keys)
	}
	for id, n := range keys {
		if n > 3 {
			t.Fatalf("DEK %s encrypted %d messages; the limit is 3", id, n)
		}
	}
	if c := k.Calls(); c.Wrap != 4 || v.KeyCount() != 4 {
		t.Fatalf("KMS Wrap calls=%d keys=%d; expected 4 and 4", c.Wrap, v.KeyCount())
	}
	for i, s := range all {
		if got, err := v.Open("cu-alpha", fmt.Sprint("o", i), s); err != nil || string(got) != fmt.Sprint("p", i) {
			t.Fatalf("object %d sealed under a retired DEK no longer opens: %v", i, err)
		}
	}
	if n, err := v.RewrapKeys(); err != nil || n != 4 {
		t.Fatalf("RewrapKeys = %d, %v; expected all 4 generations re-wrapped", n, err)
	}
}

// ---------- Vault: randomness failures and argument checks ----------

func TestVault_DetectsAStuckRandomSource(t *testing.T) {
	k := tKMS(t)
	v := tVault(t, k, func(c *Config) { c.Rand = tRepeat(0x07) })
	if _, err := v.Seal("cu-alpha", "o1", []byte("first")); err != nil {
		t.Fatalf("first Seal: %v", err)
	}
	if _, err := v.Seal("cu-alpha", "o2", []byte("second")); !errors.Is(err, ErrNonceRepeat) {
		t.Fatalf("second Seal with a stuck RNG = %v; expected ErrNonceRepeat. Two messages under the same key and nonce reveal the XOR of the plaintexts and let an attacker forge tags; refusing is the only safe answer", err)
	}
}

func TestVault_RandomSourceErrorsSurfaceAsErrors(t *testing.T) {
	boom := errors.New("entropy source on fire")
	v := tVault(t, tKMS(t), func(c *Config) { c.Rand = iotest.ErrReader(boom) })
	if _, err := v.Seal("cu-alpha", "o", []byte("x")); !errors.Is(err, boom) {
		t.Fatalf("Seal with a failing RNG = %v; expected the RNG error to be returned (never fall back to zeros)", err)
	}
	short := tVault(t, tKMS(t), func(c *Config) { c.Rand = io.LimitReader(tRepeat(1), 5) })
	if _, err := short.Seal("cu-alpha", "o", []byte("x")); err == nil {
		t.Fatalf("Seal succeeded with an RNG that returned only 5 bytes; use io.ReadFull and check the error")
	}
}

func TestVault_ValidatesArguments(t *testing.T) {
	k := tKMS(t)
	if _, err := NewVault(Config{KMS: k, KEKID: "missing"}); !errors.Is(err, ErrNoSuchKey) {
		t.Fatalf("NewVault with an unknown KEK = %v; expected ErrNoSuchKey", err)
	}
	if _, err := NewVault(Config{KEKID: "kek-main"}); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("NewVault without a KMS = %v; expected ErrInvalidArgument", err)
	}
	v := tVault(t, k, nil)
	if _, err := v.Seal("", "o", nil); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("Seal with an empty tenant = %v; expected ErrInvalidArgument", err)
	}
	if _, err := v.Seal("cu-alpha", "", nil); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("Seal with an empty object id = %v; expected ErrInvalidArgument", err)
	}
	if _, err := v.Open("", "o", Sealed{}); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("Open with an empty tenant = %v; expected ErrInvalidArgument", err)
	}
}

// ---------- concurrency ----------

func TestVault_ConcurrentSealOpenAndRewrapUnderRace(t *testing.T) {
	k := tKMS(t)
	v := tVault(t, k, nil)
	tenants := []string{"cu-a", "cu-b", "cu-c", "cu-d"}
	const goroutines, each = 8, 100
	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < each; i++ {
				tn := tenants[(g+i)%len(tenants)]
				obj := fmt.Sprintf("g%d-o%d", g, i)
				pt := fmt.Sprintf("%s|%s", tn, obj)
				s, err := v.Seal(tn, obj, []byte(pt))
				if err != nil {
					t.Errorf("Seal: %v", err)
					return
				}
				if g == 0 && i%25 == 0 {
					v.DropCache()
				}
				got, err := v.Open(tn, obj, s)
				if err != nil || string(got) != pt {
					t.Errorf("Open(%s, %s) = %q, %v", tn, obj, got, err)
					return
				}
			}
		}(g)
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 5; i++ {
			if _, err := k.Rotate("kek-main"); err != nil {
				t.Errorf("Rotate: %v", err)
				return
			}
			if _, err := v.RewrapKeys(); err != nil {
				t.Errorf("RewrapKeys: %v", err)
				return
			}
		}
	}()
	tWait(t, &wg, 60*time.Second, "concurrent vault workload")
	if c := k.Calls(); c.Wrap != len(tenants) {
		t.Fatalf("KMS Wrap calls = %d; expected exactly %d (racing first Seals of a tenant must create its DEK once)", c.Wrap, len(tenants))
	}
}
