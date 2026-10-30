package estimate

import (
	"math/rand/v2"
	"strings"
	"testing"
)

// Every generated problem must be self-consistent: a positive answer, a worked
// solution, and the displayed reference answer must itself be accepted.
func TestGeneratedProblemsRoundTrip(t *testing.T) {
	rng := rand.New(rand.NewPCG(1, 2))
	for _, k := range Kinds() {
		for i := 0; i < 200; i++ {
			p, err := Generate(k, rng)
			if err != nil {
				t.Fatal(err)
			}
			if p.Answer <= 0 || len(p.Steps) == 0 || p.Prompt == "" || p.Decision == "" {
				t.Fatalf("%s: incomplete problem %+v", k, p)
			}
			r, err := Check(p, Show(p.Answer, p.Unit), 0.25)
			if err != nil {
				t.Fatalf("%s: reference answer %q does not parse: %v", k, Show(p.Answer, p.Unit), err)
			}
			if !r.Within {
				t.Fatalf("%s: reference answer %q rejected (ratio %.3f)", k, Show(p.Answer, p.Unit), r.Ratio)
			}
		}
	}
}

func TestCheckCatchesBitsBytes(t *testing.T) {
	p := Problem{Unit: "Gbps", Answer: 8e9}
	r, err := Check(p, "1 Gbps", 0.25)
	if err != nil {
		t.Fatal(err)
	}
	if r.Within {
		t.Fatal("1 Gbps accepted for 8 Gbps")
	}
	r, _ = Check(p, "1 GB/s", 0.25)
	if !r.Within || r.Note == "" {
		t.Fatalf("1 GB/s should convert to 8 Gbps with a note, got %+v", r)
	}
}

func TestUnknownKind(t *testing.T) {
	if _, err := Generate("nope", rand.New(rand.NewPCG(1, 1))); err == nil {
		t.Fatal("expected error")
	}
}

func TestDiagnoseNamesTheLikelySlip(t *testing.T) {
	for ratio, want := range map[float64]string{8: "bits vs bytes", 0.125: "bits vs bytes", 1000: "power of 1000", 86400: "86,400", 3: "replication"} {
		if got := Diagnose(ratio); !strings.Contains(got, want) {
			t.Errorf("Diagnose(%g) = %q, want it to mention %q", ratio, got, want)
		}
	}
}
