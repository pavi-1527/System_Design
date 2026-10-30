//go:build breakit

package breakit

import "testing"

// 4. The zero value is not useful (U1 §4). Reading a nil map is fine; writing
// to one panics. Registry only works if the caller remembers to call a
// constructor, and the compiler cannot make them.
//
// Run it ALONE: a panic ends the whole test binary.
// Fix (pick one and say why): allocate lazily inside Register, or hide the
// type behind NewRegistry and keep the fields unexported (they already are),
// or make the zero value work by design.
type Registry struct {
	handlers map[string]func()
}

func (r *Registry) Register(name string, f func()) {
	r.handlers[name] = f // BUG: panics with "assignment to entry in nil map" on the zero value
}

func (r *Registry) Lookup(name string) (func(), bool) {
	f, ok := r.handlers[name] // fine on a nil map
	return f, ok
}

func TestZeroValueRegistry(t *testing.T) {
	var r Registry
	if _, ok := r.Lookup("x"); ok {
		t.Fatal("empty registry found a handler")
	}
	r.Register("x", func() {})
	if _, ok := r.Lookup("x"); !ok {
		t.Fatal("handler not found after Register")
	}
}
