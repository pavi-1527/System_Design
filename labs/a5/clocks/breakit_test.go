//go:build breakit

package clocks

// Break-it experiment for A5 Unit 2 §2–§3: how often does last-write-wins
// throw away a write that causally came LATER, as a function of clock skew?
// A measurement, not a pass/fail test; seeded, so the numbers are reproducible.
//
// Run: go test -tags breakit -run BreakIt -v ./labs/a5/clocks/

import (
	"fmt"
	"math/rand/v2"
	"testing"
)

// TestBreakIt_LWWvsSiblingsUnderSkew: 20,000 pairs of writes to one key. Writer
// A writes; then writer B READS A's value and writes 1–500 ms later (so B's
// write is causally after A's). Each writer's clock is off by an independent
// normal error with the given standard deviation. LWW keeps the write with the
// higher timestamp; the sibling register decides by causality only.
func TestBreakIt_LWWvsSiblingsUnderSkew(t *testing.T) {
	const pairs = 20000
	t.Logf("%-14s %22s %26s", "clock error σ", "LWW: later write lost", "siblings: later write lost")
	for _, sigmaMs := range []float64{0.5, 5, 50, 500, 5000} {
		rng := rand.New(rand.NewPCG(1, 2))
		lwwLost, sibLost := 0, 0
		for i := 0; i < pairs; i++ {
			gapMs := 1 + rng.Float64()*499
			offA := rng.NormFloat64() * sigmaMs
			offB := rng.NormFloat64() * sigmaMs
			tsA := int64((0 + offA) * 1000) // microseconds
			tsB := int64((gapMs + offB) * 1000)

			var lww LWWRegister
			lww.Write("A", tsA)
			lww.Write("B", tsB)
			if v, _, _ := lww.Get(); v != "B" {
				lwwLost++
			}

			var sib SiblingRegister
			sib.Put("A", nil, "A")
			_, ctx := sib.Get() // B reads A's write ...
			sib.Put("B", ctx, "B")
			if vs, _ := sib.Get(); len(vs) != 1 || vs[0].Value != "B" {
				sibLost++
			}
		}
		t.Logf("%-14s %21.1f%% %25.1f%%", fmtMs(sigmaMs), 100*float64(lwwLost)/pairs, 100*float64(sibLost)/pairs)
	}

	// Truly concurrent writes (neither writer saw the other): LWW keeps one,
	// siblings keep both, whatever the clocks say.
	var lww LWWRegister
	lww.Write("alice", 100)
	lww.Write("bob", 101)
	var s1, s2 SiblingRegister
	s1.Put("alice", nil, "alice")
	s2.Put("bob", nil, "bob")
	s1.Merge(&s2)
	vs, _ := s1.Get()
	v, _, _ := lww.Get()
	t.Logf("concurrent writes alice+bob: LWW keeps 1 value (%q); siblings keep %d", v, len(vs))
}

func fmtMs(ms float64) string {
	if ms >= 1000 {
		return fmt.Sprintf("%g s", ms/1000)
	}
	return fmt.Sprintf("%g ms", ms)
}
