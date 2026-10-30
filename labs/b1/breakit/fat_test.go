//go:build breakit

package breakit

import "testing"

// Three test doubles, each forced to implement five methods to test one.

type staticStore struct{ v string }

func (s staticStore) Get(string) (string, bool) { return s.v, true }
func (staticStore) Put(string, string)          { panic("unused") }
func (staticStore) Delete(string)               { panic("unused") }
func (staticStore) Keys() []string              { panic("unused") }
func (staticStore) Close() error                { return nil }

type emptyStore struct{}

func (emptyStore) Get(string) (string, bool) { return "", false }
func (emptyStore) Put(string, string)        { panic("unused") }
func (emptyStore) Delete(string)             { panic("unused") }
func (emptyStore) Keys() []string            { panic("unused") }
func (emptyStore) Close() error              { return nil }

type spyStore struct{ asked []string }

func (s *spyStore) Get(k string) (string, bool) { s.asked = append(s.asked, k); return "", false }
func (*spyStore) Put(string, string)            { panic("unused") }
func (*spyStore) Delete(string)                 { panic("unused") }
func (*spyStore) Keys() []string                { panic("unused") }
func (*spyStore) Close() error                  { return nil }

func TestGreeting(t *testing.T) {
	if got := Greeting(staticStore{" hi there "}, "ann"); got != "hi there" {
		t.Errorf("got %q", got)
	}
	if got := Greeting(emptyStore{}, "ann"); got != "hello" {
		t.Errorf("got %q", got)
	}
	spy := &spyStore{}
	Greeting(spy, "bob")
	if len(spy.asked) != 1 || spy.asked[0] != "greeting:bob" {
		t.Errorf("asked %v", spy.asked)
	}
}
