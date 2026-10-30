//go:build breakit

package breakit

import "strings"

// 5. The fat interface (U1 §3). Store has five methods; Greeting uses ONE of
// them. Every fake in fat_test.go must implement all five, and so will every
// fake somebody writes next year.
//
// Exercise: add a sixth method to Store,
//
//	Watch(key string) <-chan string
//
// and count the compile errors:
//
//	go vet -tags breakit ./labs/b1/breakit/
//
// Then fix the design, not the symptom: give Greeting the smallest interface it
// needs (declared next to Greeting, at the consumer) and watch the fakes stop
// caring. Only the real implementation has to grow the new method.
type Store interface {
	Get(key string) (string, bool)
	Put(key, value string)
	Delete(key string)
	Keys() []string
	Close() error
}

// MemStore is the production implementation.
type MemStore struct{ m map[string]string }

var _ Store = (*MemStore)(nil)

func (s *MemStore) Get(k string) (string, bool) { v, ok := s.m[k]; return v, ok }
func (s *MemStore) Put(k, v string) {
	if s.m == nil {
		s.m = map[string]string{}
	}
	s.m[k] = v
}
func (s *MemStore) Delete(k string) { delete(s.m, k) }
func (s *MemStore) Keys() []string {
	out := make([]string, 0, len(s.m))
	for k := range s.m {
		out = append(out, k)
	}
	return out
}
func (s *MemStore) Close() error { return nil }

// Greeting reads one key. It takes the whole Store.
func Greeting(s Store, user string) string {
	if v, ok := s.Get("greeting:" + user); ok {
		return strings.TrimSpace(v)
	}
	return "hello"
}
