//go:build breakit

package envelope

// Break-it experiments for A9 Unit 2 §3. Measurements, not pass/fail tests.
//
// Run: go test -tags breakit -run BreakIt -v ./labs/a9/envelope/

import (
	"fmt"
	"math"
	"testing"
)

// TestBreakIt_SwapWithoutAAD: a platform that (mistakenly) uses one DEK for
// every tenant, and an attacker with write access to the storage layer who
// copies tenant alpha's encrypted blob into tenant beta's row. Does beta's
// next read return alpha's data? With the associated data in place the answer
// is no; wire breakNoAAD into aadFor (see breakit_on.go) and the same
// experiment prints a different story.
func TestBreakIt_SwapWithoutAAD(t *testing.T) {
	k := NewKMS(nil)
	if err := k.CreateKey("kek-main"); err != nil {
		t.Fatal(err)
	}
	v, err := NewVault(Config{KMS: k, KEKID: "kek-main", SharedKey: true})
	if err != nil {
		t.Fatal(err)
	}
	alpha, err := v.Seal("cu-alpha", "wire-limit", []byte("alpha wire limit: $250,000"))
	if err != nil {
		t.Fatal(err)
	}
	if got, err := v.Open("cu-beta", "wire-limit", alpha); err != nil {
		t.Logf("cross-tenant swap: REJECTED (%v)", err)
	} else {
		t.Logf("cross-tenant swap: SUCCEEDED, tenant cu-beta read %q", got)
	}
	mine, _ := v.Seal("cu-alpha", "daily-limit", []byte("daily limit: $1,000"))
	if got, err := v.Open("cu-alpha", "wire-limit", mine); err != nil {
		t.Logf("same-tenant object swap: REJECTED (%v)", err)
	} else {
		t.Logf("same-tenant object swap: SUCCEEDED, 'wire-limit' now reads %q", got)
	}
}

// TestBreakIt_KMSCallsWithAndWithoutDEKCache: 10,000 reads of one tenant's
// objects. Predict the number of KMS Unwrap calls with the DEK cache, and if
// the cache is dropped before every read (as a service that "doesn't keep
// keys in memory" would behave).
func TestBreakIt_KMSCallsWithAndWithoutDEKCache(t *testing.T) {
	const reads = 10000
	run := func(drop bool) Calls {
		k := NewKMS(nil)
		k.CreateKey("kek-main")
		v, _ := NewVault(Config{KMS: k, KEKID: "kek-main"})
		s, err := v.Seal("cu-alpha", "o", []byte("payload"))
		if err != nil {
			t.Fatal(err)
		}
		for i := 0; i < reads; i++ {
			if drop {
				v.DropCache()
			}
			if _, err := v.Open("cu-alpha", "o", s); err != nil {
				t.Fatal(err)
			}
		}
		return k.Calls()
	}
	t.Logf("%d reads, DEK cached:           %+v", reads, run(false))
	t.Logf("%d reads, cache dropped each time: %+v", reads, run(true))
}

// TestBreakIt_NonceCollisionOdds prints the birthday bound for random 96-bit
// GCM nonces under one key: P(any repeat among n messages) is about n^2 / 2^97.
// This is why the vault retires a DEK after MaxSealsPerKey messages.
func TestBreakIt_NonceCollisionOdds(t *testing.T) {
	t.Logf("%-12s %-22s", "messages", "P(any nonce repeats) ~")
	for _, exp := range []int{20, 28, 32, 36, 40} {
		n := math.Ldexp(1, exp)
		p := n * n / math.Ldexp(1, 97)
		t.Logf("2^%-10d %-22s", exp, fmt.Sprintf("%.3g", p))
	}
}
