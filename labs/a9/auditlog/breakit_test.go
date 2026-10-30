//go:build breakit

package auditlog

// Break-it experiments for A9 Unit 2 §4. Measurements, not pass/fail tests.
//
// Run: go test -tags breakit -run BreakIt -v ./labs/a9/auditlog/

import (
	"errors"
	"fmt"
	"testing"
	"time"
)

func tVerdict(err error) string {
	if err == nil {
		return "NOT DETECTED"
	}
	var ve *VerifyError
	if errors.As(err, &ve) {
		switch {
		case errors.Is(err, ErrModified):
			return fmt.Sprintf("modified@%d", ve.Index)
		case errors.Is(err, ErrBrokenLink):
			return fmt.Sprintf("broken-link@%d", ve.Index)
		case errors.Is(err, ErrSeqGap):
			return fmt.Sprintf("seq-gap@%d", ve.Index)
		case errors.Is(err, ErrTruncated):
			return "truncated"
		case errors.Is(err, ErrAnchorMismatch):
			return "anchor-mismatch"
		}
	}
	return err.Error()
}

// TestBreakIt_TamperMatrix: ten entries, then an attacker with write access to
// the stored log (but not to the published head) tries nine things. Predict,
// for each row, which check fires without an anchor and which with one.
func TestBreakIt_TamperMatrix(t *testing.T) {
	l, orig := tLog(t, 10)
	head := l.Head()
	attacks := []struct {
		name string
		do   func(es []Entry) []Entry
	}{
		{"edit entry 5, keep its hash", func(es []Entry) []Entry { es[4].Actor = "mallory"; return es }},
		{"edit entry 5, recompute its hash", func(es []Entry) []Entry { es[4].Actor = "mallory"; es[4].Hash = ComputeHash(es[4]); return es }},
		{"delete entry 5", func(es []Entry) []Entry { return append(es[:4], es[5:]...) }},
		{"swap entries 4 and 5", func(es []Entry) []Entry { es[3], es[4] = es[4], es[3]; return es }},
		{"truncate to 7 entries", func(es []Entry) []Entry { return es[:7] }},
		{"edit the LAST entry, recompute its hash", func(es []Entry) []Entry {
			es[9].Outcome = "denied"
			es[9].Hash = ComputeHash(es[9])
			return es
		}},
		{"delete entry 5, rechain, keep Seq", func(es []Entry) []Entry { es = append(es[:4], es[5:]...); tRechain(es, 4, false); return es }},
		{"delete entry 5, rechain, renumber", func(es []Entry) []Entry { es = append(es[:4], es[5:]...); tRechain(es, 4, true); return es }},
		{"rewrite entry 3, rechain everything", func(es []Entry) []Entry { es[2].Actor = "mallory"; tRechain(es, 2, false); return es }},
	}
	t.Logf("%-42s | %-18s | %s", "attack", "no anchor", "anchored at seq 10")
	for _, a := range attacks {
		es := a.do(tCopy(orig))
		t.Logf("%-42s | %-18s | %s", a.name, tVerdict(Verify(es, Genesis, nil)), tVerdict(Verify(es, Genesis, &head)))
	}
}

// TestBreakIt_VerifyCost measures what verification costs per entry, so you
// can answer "how long does it take to audit a year of logs?" with numbers.
func TestBreakIt_VerifyCost(t *testing.T) {
	const n = 100000
	l := New(Config{Now: tNewClock().Now})
	start := time.Now()
	for i := 0; i < n; i++ {
		l.Append(Input{Tenant: "cu-alpha", Actor: "member:42", Action: "transfer.create", Resource: "acct:9", Outcome: "success",
			Details: map[string]string{"amount_cents": "12500", "ip": "10.0.0.7"}})
	}
	appendCost := time.Since(start)
	es := l.Entries()
	start = time.Now()
	if err := Verify(es, Genesis, nil); err != nil {
		t.Fatal(err)
	}
	verifyCost := time.Since(start)
	perEntry := verifyCost / n
	t.Logf("appended %d entries in %v (%v each)", n, appendCost.Round(time.Millisecond), (appendCost / n).Round(time.Nanosecond))
	t.Logf("verified them in %v (%v each, single core)", verifyCost.Round(time.Millisecond), perEntry.Round(time.Nanosecond))
	t.Logf("=> 1 billion entries would take about %v to verify on one core; chains verify in O(n) but parallelise across tenants and segments",
		(perEntry * 1_000_000_000).Round(time.Second))
}
