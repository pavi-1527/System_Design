//go:build breakit

package breakit

import "testing"

// ValidationError is a perfectly ordinary pointer-receiver error type.
type ValidationError struct{ Field string }

func (e *ValidationError) Error() string { return "invalid " + e.Field }

// 1. The typed-nil trap (U1 §5). validate returns the error interface, but the
// value it returns is a *ValidationError that happens to be nil. An interface
// is a (type, value) pair; (*ValidationError, nil) is NOT the nil interface, so
// callers see `err != nil` for a perfectly valid name.
//
// Predict the output of %T before you run it.
// Fix: return nil explicitly on the success path (never return a nil concrete
// pointer through an interface), or build the error only when there is one.
func validate(name string) error {
	var err *ValidationError
	if name == "" {
		err = &ValidationError{Field: "name"}
	}
	return err // BUG: typed nil inside a non-nil interface
}

func TestTypedNilError(t *testing.T) {
	if err := validate("alice"); err != nil {
		t.Fatalf("validate(%q) = %v (dynamic type %T); want a nil error. An interface holding a nil pointer is not nil.", "alice", err, err)
	}
	if err := validate(""); err == nil {
		t.Fatal("validate(\"\") = nil; want an error")
	}
}
