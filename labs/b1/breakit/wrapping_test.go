//go:build breakit

package breakit

import (
	"errors"
	"fmt"
	"testing"
)

// ErrNotFound is the sentinel callers are meant to test for.
var ErrNotFound = errors.New("not found")

// 3a. %v flattens an error to text (U2 §1). The chain is cut; errors.Is
// cannot see ErrNotFound any more.
// Fix: %w.
func lookup(id string) error {
	return fmt.Errorf("lookup %s: %v", id, ErrNotFound) // BUG: %v, not %w
}

// 3b. A custom wrapper without Unwrap is a dead end for errors.Is / errors.As.
// Fix: add `func (e *opError) Unwrap() error { return e.err }`.
type opError struct {
	op  string
	err error
}

func (e *opError) Error() string { return e.op + ": " + e.err.Error() }

func load(id string) error {
	return &opError{op: "load " + id, err: fmt.Errorf("lookup %s: %w", id, ErrNotFound)}
}

func TestErrorsIs_PercentVBreaksTheChain(t *testing.T) {
	if err := lookup("acct-9"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("errors.Is(%q, ErrNotFound) = false: the error was formatted with %%v, so only its text survived", err)
	}
}

func TestErrorsIs_WrapperWithoutUnwrapBreaksTheChain(t *testing.T) {
	if err := load("acct-9"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("errors.Is(%q, ErrNotFound) = false: *opError has no Unwrap method", err)
	}
}
