//go:build breakit

package breakit

import "testing"

// 2. Embedding is composition, not inheritance (U1 §2). Base.Describe calls
// b.Name(), and b is a Base: Go has no virtual dispatch through an embedded
// struct, so Premium's Name is never called from inside Describe.
//
// Predict what (Premium{}).Describe() returns before you run it.
// Fix: make Describe a plain function that accepts a small interface
// (`type Namer interface{ Name() string }`) and pass the outer value, or give
// Premium its own Describe. Do not reach for reflection.
type Base struct{}

func (Base) Name() string       { return "base" }
func (b Base) Describe() string { return "I am " + b.Name() }

type Premium struct{ Base }

func (Premium) Name() string { return "premium" }

func TestEmbeddingIsNotInheritance(t *testing.T) {
	if got := (Premium{}).Describe(); got != "I am premium" {
		t.Fatalf("Describe() = %q, want %q: the promoted method runs with the embedded Base as its receiver, so the override is invisible to it", got, "I am premium")
	}
}
