package token

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

// ---------- helpers (prefixed with t to avoid clashing with your code) ----------

var tNow = time.Date(2026, 6, 1, 10, 0, 0, 0, time.UTC)

func tKeys(t *testing.T) *Keyring {
	t.Helper()
	k := NewKeyring()
	k.Add("k1", []byte("secret-one-32-bytes-long-xxxxxxx"))
	if err := k.SetCurrent("k1"); err != nil {
		t.Fatalf("SetCurrent(k1): %v", err)
	}
	return k
}

func tVerifier(k *Keyring) *Verifier {
	return &Verifier{Keys: k, Leeway: 30 * time.Second, Now: func() time.Time { return tNow }}
}

func tClaims() Claims {
	return Claims{Issuer: "https://auth.example-cu.test", Subject: "partner-42", Audience: "transfers-api",
		IssuedAt: tNow.Unix(), ExpiresAt: tNow.Add(5 * time.Minute).Unix(), Scope: "transfers:write", Tenant: "cu-001"}
}

var tB64 = base64.RawURLEncoding

// tHandBuilt assembles a token without using Sign, so tests can pin the wire
// format and forge bad tokens. sigKey nil => empty signature segment.
func tHandBuilt(headerJSON, payloadJSON string, sigKey []byte) string {
	input := tB64.EncodeToString([]byte(headerJSON)) + "." + tB64.EncodeToString([]byte(payloadJSON))
	sig := ""
	if sigKey != nil {
		m := hmac.New(sha256.New, sigKey)
		m.Write([]byte(input))
		sig = tB64.EncodeToString(m.Sum(nil))
	}
	return input + "." + sig
}

func tPayload(t *testing.T, c Claims) string {
	t.Helper()
	b, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func tWantErr(t *testing.T, what string, err, want error) {
	t.Helper()
	if !errors.Is(err, want) {
		t.Fatalf("%s: got error %v, want %v", what, err, want)
	}
}

// tWait waits for wg, failing the test instead of hanging if it takes longer
// than d (e.g. a deadlock in the implementation under test).
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

// ---------- tests ----------

func TestToken_SignVerifyRoundTrip(t *testing.T) {
	k := tKeys(t)
	tok, err := Sign(k, tClaims())
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	got, err := tVerifier(k).Verify(tok)
	if err != nil {
		t.Fatalf("Verify(fresh token): %v", err)
	}
	if got != tClaims() {
		t.Fatalf("claims round trip: got %+v, want %+v", got, tClaims())
	}
}

func TestToken_SignProducesStandardHS256JWT(t *testing.T) {
	k := tKeys(t)
	tok, err := Sign(k, tClaims())
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		t.Fatalf("token %q has %d segments, want 3", tok, len(parts))
	}
	if strings.ContainsAny(tok, "=+/") {
		t.Fatalf("token %q must use base64url without padding", tok)
	}
	hb, err := tB64.DecodeString(parts[0])
	if err != nil {
		t.Fatalf("header not base64url: %v", err)
	}
	var h map[string]any
	if err := json.Unmarshal(hb, &h); err != nil {
		t.Fatalf("header not JSON: %v", err)
	}
	if h["alg"] != "HS256" || h["kid"] != "k1" {
		t.Fatalf("header %s, want alg HS256 and kid k1", hb)
	}
	m := hmac.New(sha256.New, []byte("secret-one-32-bytes-long-xxxxxxx"))
	m.Write([]byte(parts[0] + "." + parts[1]))
	if parts[2] != tB64.EncodeToString(m.Sum(nil)) {
		t.Fatalf("signature is not base64url(HMAC-SHA256(key, header.payload)); other JWT libraries could not verify it")
	}
}

func TestToken_VerifiesHandBuiltJWT(t *testing.T) {
	k := tKeys(t)
	tok := tHandBuilt(`{"typ":"JWT","alg":"HS256","kid":"k1"}`, tPayload(t, tClaims()), []byte("secret-one-32-bytes-long-xxxxxxx"))
	got, err := tVerifier(k).Verify(tok)
	if err != nil {
		t.Fatalf("Verify(hand-built standard JWT): %v", err)
	}
	if got.Subject != "partner-42" || got.Tenant != "cu-001" {
		t.Fatalf("claims %+v", got)
	}
}

func TestToken_RejectsUnsupportedAlgorithms(t *testing.T) {
	k := tKeys(t)
	secret := []byte("secret-one-32-bytes-long-xxxxxxx")
	p := tPayload(t, tClaims())
	cases := map[string]string{
		"alg none, empty sig":     tHandBuilt(`{"alg":"none","kid":"k1"}`, p, nil),
		"alg none, valid HMAC":    tHandBuilt(`{"alg":"none","kid":"k1"}`, p, secret),
		"alg None (case variant)": tHandBuilt(`{"alg":"None","kid":"k1"}`, p, nil),
		"alg RS256 (confusion)":   tHandBuilt(`{"alg":"RS256","kid":"k1"}`, p, secret),
		"alg HS512":               tHandBuilt(`{"alg":"HS512","kid":"k1"}`, p, secret),
		"alg missing":             tHandBuilt(`{"kid":"k1"}`, p, secret),
	}
	for name, tok := range cases {
		_, err := tVerifier(k).Verify(tok)
		tWantErr(t, name, err, ErrUnsupportedAlg)
	}
}

func TestToken_RejectsTamperedPayload(t *testing.T) {
	k := tKeys(t)
	tok, _ := Sign(k, tClaims())
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		t.Fatalf("Sign returned %q", tok)
	}
	evil := tClaims()
	evil.Tenant = "cu-002" // try to read another credit union's data
	forged := parts[0] + "." + tB64.EncodeToString([]byte(tPayload(t, evil))) + "." + parts[2]
	_, err := tVerifier(k).Verify(forged)
	tWantErr(t, "payload changed, signature kept", err, ErrBadSignature)

	wrongKey := tHandBuilt(`{"alg":"HS256","kid":"k1"}`, tPayload(t, tClaims()), []byte("guessed-secret"))
	_, err = tVerifier(k).Verify(wrongKey)
	tWantErr(t, "signed with the wrong secret", err, ErrBadSignature)
}

func TestToken_SignatureIsCheckedBeforeClaims(t *testing.T) {
	k := tKeys(t)
	c := tClaims()
	c.ExpiresAt = tNow.Add(-time.Hour).Unix()
	tok := tHandBuilt(`{"alg":"HS256","kid":"k1"}`, tPayload(t, c), []byte("guessed-secret"))
	_, err := tVerifier(k).Verify(tok)
	tWantErr(t, "expired AND badly signed", err, ErrBadSignature)
}

func TestToken_RejectsUnknownOrMissingKid(t *testing.T) {
	k := tKeys(t)
	p := tPayload(t, tClaims())
	_, err := tVerifier(k).Verify(tHandBuilt(`{"alg":"HS256","kid":"k9"}`, p, []byte("whatever")))
	tWantErr(t, "unknown kid", err, ErrUnknownKey)
	_, err = tVerifier(k).Verify(tHandBuilt(`{"alg":"HS256"}`, p, []byte("secret-one-32-bytes-long-xxxxxxx")))
	tWantErr(t, "missing kid", err, ErrUnknownKey)
}

func TestToken_RejectsMalformed(t *testing.T) {
	k := tKeys(t)
	good, _ := Sign(k, tClaims())
	noExp := tClaims()
	noExp.ExpiresAt = 0
	cases := map[string]string{
		"empty":            "",
		"two segments":     "abc.def",
		"four segments":    good + ".x",
		"header not b64":   "!!!." + strings.SplitN(good+"..", ".", 3)[1] + ".sig",
		"header not JSON":  tB64.EncodeToString([]byte("not json")) + ".e30.sig",
		"missing exp":      tHandBuilt(`{"alg":"HS256","kid":"k1"}`, tPayload(t, noExp), []byte("secret-one-32-bytes-long-xxxxxxx")),
		"payload not JSON": tHandBuilt(`{"alg":"HS256","kid":"k1"}`, "not json", []byte("secret-one-32-bytes-long-xxxxxxx")),
	}
	for name, tok := range cases {
		_, err := tVerifier(k).Verify(tok)
		tWantErr(t, name, err, ErrMalformed)
	}
}

func TestToken_ExpiryWithLeeway(t *testing.T) {
	k := tKeys(t)
	cases := []struct {
		name   string
		exp    time.Duration // relative to now
		leeway time.Duration
		want   error
	}{
		{"valid", time.Minute, 0, nil},
		{"expired 10s ago, 30s leeway", -10 * time.Second, 30 * time.Second, nil},
		{"expired 31s ago, 30s leeway", -31 * time.Second, 30 * time.Second, ErrExpired},
		{"exp == now, no leeway", 0, 0, ErrExpired},
	}
	for _, c := range cases {
		cl := tClaims()
		cl.ExpiresAt = tNow.Add(c.exp).Unix()
		tok, err := Sign(k, cl)
		if err != nil {
			t.Fatalf("Sign: %v", err)
		}
		v := tVerifier(k)
		v.Leeway = c.leeway
		_, err = v.Verify(tok)
		if c.want == nil && err != nil {
			t.Errorf("%s: unexpected error %v", c.name, err)
		}
		if c.want != nil && !errors.Is(err, c.want) {
			t.Errorf("%s: got %v, want %v", c.name, err, c.want)
		}
	}
}

func TestToken_NotBeforeWithLeeway(t *testing.T) {
	k := tKeys(t)
	for _, c := range []struct {
		nbf  time.Duration
		want error
	}{
		{20 * time.Second, nil}, // issuer clock 20s ahead: tolerated
		{40 * time.Second, ErrNotYetValid},
	} {
		cl := tClaims()
		cl.NotBefore = tNow.Add(c.nbf).Unix()
		tok, _ := Sign(k, cl)
		_, err := tVerifier(k).Verify(tok)
		if c.want == nil && err != nil {
			t.Errorf("nbf now+%v with 30s leeway: unexpected %v", c.nbf, err)
		}
		if c.want != nil && !errors.Is(err, c.want) {
			t.Errorf("nbf now+%v with 30s leeway: got %v, want %v", c.nbf, err, c.want)
		}
	}
}

func TestToken_AudienceMustMatch(t *testing.T) {
	k := tKeys(t)
	tok, _ := Sign(k, tClaims()) // aud = transfers-api
	v := tVerifier(k)
	v.Audience = "statements-api"
	_, err := v.Verify(tok)
	tWantErr(t, "token for transfers-api presented to statements-api", err, ErrAudience)
	v.Audience = "transfers-api"
	if _, err := v.Verify(tok); err != nil {
		t.Fatalf("matching audience: %v", err)
	}
}

func TestToken_KeyRotation(t *testing.T) {
	k := tKeys(t)
	old, _ := Sign(k, tClaims())
	k.Add("k2", []byte("secret-two-32-bytes-long-yyyyyyy"))
	if err := k.SetCurrent("k2"); err != nil {
		t.Fatalf("SetCurrent(k2): %v", err)
	}
	fresh, _ := Sign(k, tClaims())
	if h, _ := tB64.DecodeString(strings.Split(fresh, ".")[0]); !strings.Contains(string(h), `"kid":"k2"`) {
		t.Fatalf("after SetCurrent(k2), new tokens must carry kid k2; header %s", h)
	}
	v := tVerifier(k)
	if _, err := v.Verify(old); err != nil {
		t.Fatalf("token signed with k1 must still verify during the overlap window: %v", err)
	}
	if _, err := v.Verify(fresh); err != nil {
		t.Fatalf("token signed with k2: %v", err)
	}
	k.Remove("k1")
	_, err := v.Verify(old)
	tWantErr(t, "k1 token after k1 removed", err, ErrUnknownKey)
	if _, err := v.Verify(fresh); err != nil {
		t.Fatalf("k2 token after k1 removed: %v", err)
	}
	tWantErr(t, "SetCurrent(unknown)", k.SetCurrent("k7"), ErrUnknownKey)
	k.Remove("k2")
	_, err = Sign(k, tClaims())
	tWantErr(t, "Sign after removing the current key", err, ErrNoCurrentKey)
}

func TestToken_KeyringCopiesSecrets(t *testing.T) {
	secret := []byte("secret-three-32-bytes-long-zzzzz")
	k := NewKeyring()
	k.Add("k3", secret)
	_ = k.SetCurrent("k3")
	tok, _ := Sign(k, tClaims())
	for i := range secret {
		secret[i] = 0 // caller wipes its buffer
	}
	if _, err := tVerifier(k).Verify(tok); err != nil {
		t.Fatalf("Keyring must copy secrets; after the caller zeroed its slice, Verify failed: %v", err)
	}
}

func TestToken_ConcurrentVerifyDuringRotation(t *testing.T) {
	k := tKeys(t)
	tok, _ := Sign(k, tClaims())
	v := tVerifier(k)
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	stop := make(chan struct{})
	wg.Add(1)
	go func() { // rotator: keeps adding and switching keys, never removes k1
		defer wg.Done()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			kid := fmt.Sprintf("r%d", i)
			k.Add(kid, []byte(kid+"-secret"))
			_ = k.SetCurrent(kid)
			if _, err := Sign(k, tClaims()); err != nil {
				errs <- err
				return
			}
			if i > 2 {
				k.Remove(fmt.Sprintf("r%d", i-2))
			}
		}
	}()
	var readers sync.WaitGroup
	for g := 0; g < 6; g++ {
		readers.Add(1)
		go func() {
			defer readers.Done()
			for i := 0; i < 300; i++ {
				if _, err := v.Verify(tok); err != nil {
					errs <- err
					return
				}
			}
		}()
	}
	tWait(t, &readers, 20*time.Second, "concurrent Verify calls")
	close(stop)
	tWait(t, &wg, 10*time.Second, "key rotator")
	close(errs)
	for err := range errs {
		t.Errorf("during concurrent rotation: %v", err)
	}
}
