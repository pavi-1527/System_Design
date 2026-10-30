//go:build breakit && copylocks

package breakit

import (
	"sync"
	"testing"
)

// 6. Copying a lock (U1 §4). This file has an extra build tag so that the
// repo-wide `go vet -tags breakit` stays clean; switch it on explicitly:
//
//	go vet -tags 'breakit copylocks' ./labs/b1/breakit/
//	go test -tags 'breakit copylocks' -run TestValueReceiverCounter ./labs/b1/breakit/
//
// Predict what vet prints (look for "passes lock by value") and what the test
// reports. Every call to Inc operates on a COPY of the Counter, mutex and all,
// so the original never changes and the lock protects nothing.
// Fix: pointer receivers (and call New or use &Counter{}).
type Counter struct {
	mu sync.Mutex
	n  int
}

func (c Counter) Inc() { // BUG: value receiver copies the mutex and the count
	c.mu.Lock()
	defer c.mu.Unlock()
	c.n++
}

func (c *Counter) Value() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.n
}

func TestValueReceiverCounter(t *testing.T) {
	var c Counter
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c.Inc()
		}()
	}
	wg.Wait()
	if got := c.Value(); got != 100 {
		t.Fatalf("Value() = %d, want 100: each Inc incremented its own copy", got)
	}
}
